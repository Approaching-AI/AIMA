package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/knowledge"
)

// EngineImage represents a locally available engine (container image or native binary).
type EngineImage struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	AssetName       string `json:"asset_name,omitempty"`
	Image           string `json:"image"` // container image name (container engines) or empty (native)
	Tag             string `json:"tag"`   // container image tag (container engines) or empty (native)
	SizeBytes       int64  `json:"size_bytes"`
	Platform        string `json:"platform"`
	RuntimeType     string `json:"runtime_type"` // "container" or "native"
	BinaryPath      string `json:"binary_path"`  // path to native binary (native engines only)
	Available       bool   `json:"available"`
	Origin          string `json:"origin,omitempty"`
	CatalogVersion  string `json:"catalog_version,omitempty"`
	ContentDigest   string `json:"content_digest,omitempty"`
	ContentVerified bool   `json:"content_verified,omitempty"`
	DockerOnly      bool   `json:"docker_only,omitempty"`      // true if image is in Docker but not K3S containerd
	DetectedVersion string `json:"detected_version,omitempty"` // version found by probing
	VersionMatch    string `json:"version_match,omitempty"`    // "exact", "compatible", "unknown", "mismatch"
}

// AssetDescriptor contains only the Catalog evidence needed by scanning.
type AssetDescriptor struct {
	AssetName          string
	Type               string
	CatalogVersion     string
	CompatibleVersions []string
	Patterns           []string
	Probe              *knowledge.EngineSourceProbe
	ExpectedSHA256     string
}

// CommandRunner abstracts shell command execution for testability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// Pipe connects stdout of 'from' to stdin of 'to' (e.g. docker save | k3s ctr import).
	Pipe(ctx context.Context, from, to []string) error
	// RunStream executes a command and calls onLine for each line of combined stdout+stderr.
	// Used to capture streaming output from commands like 'docker pull'.
	RunStream(ctx context.Context, onLine func(line string), name string, args ...string) error
}

// ScanOptions configures engine scanning (both container and native).
type ScanOptions struct {
	Assets       []AssetDescriptor
	Runner       CommandRunner
	DistDir      string            // dist directory for native binaries (~/.aima/dist/{os}-{arch}/)
	ExtraDirs    []string          // extra dirs to scan for native binaries (from AIMA_ENGINE_DIR); engines installed off-PATH/off-dist
	Platform     string            // current platform (e.g., "windows-amd64")
	BinaryAssets map[string]string // binary name -> Engine Asset name; legacy engine-type values remain accepted
	AutoImport   bool              // when true, auto-import Docker-only images to K3S containerd (heavy; use only during init)
}

// ScanUnified discovers both container images and native binaries.
// Returns all available engines from both runtimes (container + native).
// When opts.AutoImport is true, Docker-only images are imported to K3S containerd
// (heavy operation; intended for init only). Otherwise they are just flagged as DockerOnly.
func ScanUnified(ctx context.Context, opts ScanOptions) ([]*EngineImage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan engines: %w", err)
	}

	var allEngines []*EngineImage

	// Scan container images
	images, err := listImages(ctx, opts.Runner)
	if err == nil {
		matched := matchImages(images, opts.Assets)

		// Auto-import Docker-only images to containerd (only when explicitly requested)
		if opts.AutoImport {
			hasDockerOnly := false
			for _, img := range matched {
				if img.DockerOnly {
					hasDockerOnly = true
					break
				}
			}

			canImport := false
			if hasDockerOnly {
				_, checkErr := opts.Runner.Run(ctx, "k3s", "ctr", "-n", "k8s.io", "version")
				canImport = checkErr == nil
			}

			for _, img := range matched {
				if !img.DockerOnly {
					continue
				}
				ref := img.Image + ":" + img.Tag
				if !canImport {
					slog.Warn("engine in Docker but not in K3S containerd; import requires root",
						"engine", img.Type, "image", ref,
						"fix", "sudo docker save "+ref+" | sudo k3s ctr -n k8s.io images import -")
				} else if err := ImportDockerToContainerd(ctx, ref, opts.Runner); err != nil {
					slog.Warn("failed to import engine from Docker to K3S containerd",
						"engine", img.Type, "image", ref, "error", err)
				} else {
					slog.Info("imported engine from Docker to K3S containerd", "image", ref)
					img.DockerOnly = false
				}
			}
		}

		for _, img := range matched {
			img.RuntimeType = "container"
			img.Platform = opts.Platform
		}
		allEngines = append(allEngines, matched...)
	}

	// Scan native binaries
	if opts.DistDir != "" {
		native, err := ScanNative(ctx, opts)
		if err == nil {
			allEngines = append(allEngines, native...)
		}
	}

	// Probe pre-installed engines
	preinstalled := probePreinstalled(ctx, opts)
	allEngines = append(allEngines, preinstalled...)

	return dedupeEngineImages(allEngines), nil
}

// ScanNative discovers native engine binaries in distDir, the AIMA_ENGINE_DIR
// extra dirs, and PATH. Engines installed off the system drive in arbitrary
// dirs (e.g. D:\tools\llama-b9180-win-hip-radeon-x64\llama-server.exe) are
// neither in distDir nor on PATH, so ExtraDirs is the path for those.
func ScanNative(ctx context.Context, opts ScanOptions) ([]*EngineImage, error) {
	if opts.DistDir == "" {
		return nil, fmt.Errorf("distDir not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan native engines: %w", err)
	}

	// BinaryAssets maps binary filename -> Engine Asset name and is populated from
	// YAML source.binary fields. Engine-type values remain accepted for callers
	// that do not yet have descriptor identity.
	// If not provided, native scan returns empty (caller must supply the mapping).
	knownBinaries := opts.BinaryAssets
	if knownBinaries == nil {
		return nil, nil
	}

	filenameLookup := make(map[string]string, len(knownBinaries))
	for filename, assetRef := range knownBinaries {
		filenameLookup[normalizedBinaryName(filename)] = assetRef
	}
	assets := newAssetDescriptorIndex(opts.Assets)

	// distDir is the AIMA-owned root. It may contain both the legacy flat layout
	// and versioned <asset>/<version-or-digest>/<binary> layouts.
	found, err := scanManagedEngineBinaries(ctx, opts.DistDir, filenameLookup, assets, opts.Platform)
	if err != nil {
		return nil, err
	}
	seenExternal := make(map[string]bool)
	for _, image := range found {
		seenExternal[normalizedBinaryName(filepath.Base(image.BinaryPath))] = true
	}

	// External directories and PATH are never treated as AIMA-owned. The first
	// external match for a binary name wins, preserving the existing scan order.
	for _, dir := range opts.ExtraDirs {
		found = append(found, scanExternalEngineDir(dir, filenameLookup, assets, seenExternal, opts.Platform)...)
	}
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		for _, dir := range strings.Split(pathEnv, string(os.PathListSeparator)) {
			found = append(found, scanExternalEngineDir(dir, filenameLookup, assets, seenExternal, opts.Platform)...)
		}
	}

	return found, nil
}

type assetDescriptorIndex struct {
	byName map[string]AssetDescriptor
	byType map[string][]AssetDescriptor
}

func newAssetDescriptorIndex(assets []AssetDescriptor) assetDescriptorIndex {
	index := assetDescriptorIndex{
		byName: make(map[string]AssetDescriptor, len(assets)),
		byType: make(map[string][]AssetDescriptor),
	}
	for _, asset := range assets {
		if name := normalizedAssetKey(asset.AssetName); name != "" {
			index.byName[name] = asset
		}
		if engineType := normalizedAssetKey(asset.Type); engineType != "" {
			index.byType[engineType] = append(index.byType[engineType], asset)
		}
	}
	return index
}

func (index assetDescriptorIndex) resolve(ref string) AssetDescriptor {
	key := normalizedAssetKey(ref)
	if asset, ok := index.byName[key]; ok {
		return asset
	}
	if assets := index.byType[key]; len(assets) > 0 {
		return assets[0]
	}
	return AssetDescriptor{Type: strings.TrimSpace(ref)}
}

func normalizedAssetKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizedBinaryName(value string) string {
	value = strings.ToLower(strings.TrimSpace(filepath.Base(value)))
	return strings.TrimSuffix(value, ".exe")
}

func scanManagedEngineBinaries(ctx context.Context, root string, filenameLookup map[string]string, assets assetDescriptorIndex, platform string) ([]*EngineImage, error) {
	var found []*EngineImage
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		assetRef, ok := filenameLookup[normalizedBinaryName(entry.Name())]
		if !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}

		descriptor := assets.resolve(assetRef)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		parts := splitPathComponents(rel)
		detectedVersion := ""
		if len(parts) >= 3 && (descriptor.AssetName == "" || strings.EqualFold(parts[0], descriptor.AssetName)) {
			detectedVersion = parts[1]
		}
		id := binaryHash(entry.Name())
		if len(parts) > 1 {
			id = ManagedNativeEngineID(descriptor.AssetName, detectedVersion, rel)
		}
		found = append(found, newNativeEngineImage(
			id, path, info.Size(), platform, "managed", descriptor, detectedVersion,
		))
		return nil
	})
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan managed engines: %w", err)
	}
	if walkErr != nil {
		return nil, nil
	}
	return found, nil
}

func scanExternalEngineDir(dir string, filenameLookup map[string]string, assets assetDescriptorIndex, seen map[string]bool, platform string) []*EngineImage {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []*EngineImage
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		nameKey := normalizedBinaryName(entry.Name())
		if seen[nameKey] {
			continue
		}
		assetRef, ok := filenameLookup[nameKey]
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		found = append(found, newNativeEngineImage(
			binaryHash(entry.Name()+"-"+dir), path, info.Size(), platform,
			"preinstalled", assets.resolve(assetRef), "",
		))
		seen[nameKey] = true
	}
	return found
}

func newNativeEngineImage(id, path string, size int64, platform, origin string, descriptor AssetDescriptor, detectedVersion string) *EngineImage {
	contentDigest := fileContentDigest(path)
	return &EngineImage{
		ID:              id,
		Type:            descriptor.Type,
		AssetName:       descriptor.AssetName,
		SizeBytes:       size,
		Platform:        platform,
		RuntimeType:     "native",
		BinaryPath:      path,
		Available:       true,
		Origin:          origin,
		CatalogVersion:  descriptor.CatalogVersion,
		ContentDigest:   contentDigest,
		ContentVerified: digestMatches(contentDigest, descriptor.ExpectedSHA256),
		DetectedVersion: detectedVersion,
		VersionMatch:    compareDetectedVersion(detectedVersion, descriptor.CatalogVersion, descriptor.CompatibleVersions),
	}
}

func splitPathComponents(path string) []string {
	clean := filepath.Clean(path)
	if clean == "." || clean == "" {
		return nil
	}
	return strings.FieldsFunc(clean, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

// probePreinstalled discovers pre-installed engines by checking known paths
// and optionally running version detection commands.
func probePreinstalled(ctx context.Context, opts ScanOptions) []*EngineImage {
	if len(opts.Assets) == 0 {
		return nil
	}
	var found []*EngineImage
	for _, descriptor := range opts.Assets {
		probe := descriptor.Probe
		if probe == nil {
			continue
		}
		// Search probe.Paths for the binary
		var binaryPath string
		for _, p := range probe.Paths {
			if _, err := os.Stat(p); err == nil {
				binaryPath = p
				break
			}
		}
		if binaryPath == "" {
			continue // not installed on this device
		}

		// Detect version
		detectedVersion := probe.FallbackVersion
		if len(probe.VersionCommand) > 0 && opts.Runner != nil {
			// Execute version command with 5s timeout
			cmdName, cmdArgs := resolveProbeCommand(binaryPath, probe.VersionCommand)
			vCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			out, err := opts.Runner.Run(vCtx, cmdName, cmdArgs...)
			cancel()
			if err == nil && probe.VersionPattern != "" {
				re, reErr := regexp.Compile(probe.VersionPattern)
				if reErr == nil {
					if matches := re.FindSubmatch(out); len(matches) > 1 {
						detectedVersion = string(matches[1])
					}
				}
			}
		}

		info, _ := os.Stat(binaryPath)
		var size int64
		if info != nil {
			size = info.Size()
		}

		identity := descriptor.AssetName
		if identity == "" {
			identity = descriptor.Type
		}
		contentDigest := fileContentDigest(binaryPath)
		found = append(found, &EngineImage{
			ID:              binaryHash("preinstalled-" + identity),
			Type:            descriptor.Type,
			AssetName:       descriptor.AssetName,
			SizeBytes:       size,
			Platform:        opts.Platform,
			RuntimeType:     "native",
			BinaryPath:      binaryPath,
			Available:       true,
			Origin:          "preinstalled",
			CatalogVersion:  descriptor.CatalogVersion,
			ContentDigest:   contentDigest,
			ContentVerified: digestMatches(contentDigest, descriptor.ExpectedSHA256),
			DetectedVersion: detectedVersion,
			VersionMatch:    compareDetectedVersion(detectedVersion, descriptor.CatalogVersion, descriptor.CompatibleVersions),
		})
	}
	return found
}

func compareDetectedVersion(detected, catalog string, compatible []string) string {
	detected = strings.TrimSpace(detected)
	catalog = strings.TrimSpace(catalog)
	if detected == "" || strings.EqualFold(detected, "unknown") || catalog == "" || strings.EqualFold(catalog, "unknown") {
		return "unknown"
	}
	if detected == catalog {
		return "exact"
	}
	for _, declared := range compatible {
		declared = strings.TrimSpace(declared)
		if declared == detected {
			return "compatible"
		}
		if strings.HasSuffix(declared, ".x") {
			prefix := strings.TrimSuffix(declared, "x")
			if len(detected) > len(prefix) && strings.HasPrefix(detected, prefix) {
				return "compatible"
			}
		}
	}
	return "mismatch"
}

func fileContentDigest(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func resolveProbeCommand(binaryPath string, command []string) (string, []string) {
	if len(command) == 0 {
		return "", nil
	}
	name := command[0]
	if strings.HasPrefix(name, "./") {
		name = filepath.Join(filepath.Dir(binaryPath), strings.TrimPrefix(name, "./"))
	} else if !strings.ContainsRune(name, os.PathSeparator) && filepath.Base(binaryPath) == name {
		name = binaryPath
	}
	return name, command[1:]
}

func binaryHash(name string) string {
	h := sha256.Sum256([]byte(name))
	return hex.EncodeToString(h[:])[:16]
}

func dedupeEngineImages(images []*EngineImage) []*EngineImage {
	out := make([]*EngineImage, 0, len(images))
	seen := make(map[string]int)
	for _, image := range images {
		if image == nil {
			continue
		}
		key := image.RuntimeType + "|" + image.ID
		if image.RuntimeType == "native" && image.BinaryPath != "" {
			key = "native|" + filepath.Clean(image.BinaryPath)
		}
		if index, ok := seen[key]; ok {
			if engineEvidenceScore(image) > engineEvidenceScore(out[index]) {
				out[index] = image
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, image)
	}
	return out
}

func engineEvidenceScore(image *EngineImage) int {
	if image == nil {
		return 0
	}
	score := 0
	if image.AssetName != "" {
		score += 2
	}
	if image.DetectedVersion != "" && !strings.EqualFold(image.DetectedVersion, "unknown") {
		score += 2
	}
	if image.ContentDigest != "" {
		score++
	}
	if image.Origin == "managed" {
		score += 4
	}
	return score
}

type imageInfo struct {
	id     string
	repo   string // image name without tag
	tag    string
	digest string // registry/OCI manifest digest, never a runtime image/config ID
	size   int64
	source string // "containerd" or "docker"
}

func listImages(ctx context.Context, runner CommandRunner) ([]imageInfo, error) {
	var allImages []imageInfo

	// Try crictl (K3S containerd)
	containerdSet := make(map[string]bool)
	crictlImages, err := listCrictlImages(ctx, runner)
	if err == nil {
		allImages = append(allImages, crictlImages...)
		for _, img := range crictlImages {
			containerdSet[img.repo+":"+img.tag] = true
		}
	}

	// Also try docker (may have additional images)
	dockerImages, err := listDockerImages(ctx, runner)
	if err == nil {
		for _, img := range dockerImages {
			if containerdSet[img.repo+":"+img.tag] {
				continue // already in containerd, skip Docker duplicate
			}
			allImages = append(allImages, img)
		}
	}

	if len(allImages) == 0 {
		return nil, fmt.Errorf("neither crictl nor docker available")
	}

	return allImages, nil
}

// runCrictl tries standalone crictl, then K3S-embedded crictl as fallback.
// K3S bundles crictl as a subcommand (k3s crictl) — standalone crictl may not exist.
func runCrictl(ctx context.Context, runner CommandRunner, args ...string) ([]byte, error) {
	if out, err := runner.Run(ctx, "crictl", args...); err == nil {
		return out, nil
	}
	k3sArgs := append([]string{"crictl"}, args...)
	return runner.Run(ctx, "k3s", k3sArgs...)
}

func listCrictlImages(ctx context.Context, runner CommandRunner) ([]imageInfo, error) {
	output, err := runCrictl(ctx, runner, "images", "-o", "json")
	if err != nil {
		return nil, err
	}

	var result struct {
		Images []struct {
			ID          string   `json:"id"`
			RepoTags    []string `json:"repoTags"`
			RepoDigests []string `json:"repoDigests"`
			Size        string   `json:"size"`
		} `json:"images"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse crictl output: %w", err)
	}

	var images []imageInfo
	for _, img := range result.Images {
		size, _ := strconv.ParseInt(img.Size, 10, 64)
		for _, tag := range img.RepoTags {
			repo, tagStr := splitImageTag(tag)
			images = append(images, imageInfo{
				id:     img.ID,
				repo:   repo,
				tag:    tagStr,
				digest: repoContentDigest(repo, img.RepoDigests),
				size:   size,
				source: "containerd",
			})
		}
	}

	return images, nil
}

func listDockerImages(ctx context.Context, runner CommandRunner) ([]imageInfo, error) {
	output, err := runner.Run(ctx, "docker", "images", "--digests", "--format", "{{json .}}", "--no-trunc")
	if err != nil {
		return nil, err
	}

	var images []imageInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		var img struct {
			Repository string `json:"Repository"`
			Tag        string `json:"Tag"`
			ID         string `json:"ID"`
			Digest     string `json:"Digest"`
			Size       string `json:"Size"`
		}
		if err := json.Unmarshal([]byte(line), &img); err != nil {
			continue
		}
		images = append(images, imageInfo{
			id:     img.ID,
			repo:   img.Repository,
			tag:    img.Tag,
			digest: normalizedContentDigest(img.Digest),
			size:   0, // Docker format doesn't reliably include size
			source: "docker",
		})
	}

	return images, nil
}

// patternEntry keeps Catalog identity with each matching rule.
type patternEntry struct {
	pattern    string
	descriptor AssetDescriptor
	order      int
}

// matchImages matches images to Engine Assets using YAML knowledge.
// Knowledge-driven: patterns come from Engine Asset YAMLs, not hardcoded.
// Tag-aware: patterns containing ":" match against "repo:tag"; others match repo only.
// Tag-aware patterns take priority over repo-only patterns.
func matchImages(images []imageInfo, assets []AssetDescriptor) []*EngineImage {
	var matched []*EngineImage
	seen := make(map[string]bool)

	// Split patterns into ordered slices: tag-aware (contain ":") vs repo-only.
	var tagPatterns, repoPatterns []patternEntry
	for order, descriptor := range assets {
		for _, pattern := range descriptor.Patterns {
			clean := strings.TrimPrefix(strings.TrimSuffix(pattern, "$"), "^")
			entry := patternEntry{pattern: pattern, descriptor: descriptor, order: order}
			if strings.Contains(clean, ":") {
				tagPatterns = append(tagPatterns, entry)
			} else {
				repoPatterns = append(repoPatterns, entry)
			}
		}
	}

	for _, img := range images {
		if seen[img.id] || img.repo == "<none>" || img.tag == "<none>" {
			continue
		}

		searchRef := strings.ToLower(img.repo + ":" + img.tag)
		searchName := strings.ToLower(img.repo)

		// Tag-aware patterns take priority (match against repo:tag).
		descriptor, ok := patternMatch(searchRef, tagPatterns)
		if !ok {
			descriptor, ok = patternMatch(searchName, repoPatterns)
		}
		if !ok {
			continue
		}

		matched = append(matched, &EngineImage{
			ID:             img.id,
			Type:           descriptor.Type,
			AssetName:      descriptor.AssetName,
			Image:          img.repo,
			Tag:            img.tag,
			SizeBytes:      img.size,
			Available:      true,
			Origin:         "preinstalled",
			CatalogVersion: descriptor.CatalogVersion,
			ContentDigest:  img.digest,
			DockerOnly:     img.source == "docker",
			VersionMatch:   "unknown",
		})
		seen[img.id] = true
	}

	return matched
}

// patternMatch checks search string against a set of patterns.
// Supports anchors: ^pattern (prefix), pattern$ (suffix), ^pattern$ (exact).
// Patterns are sorted by specificity (exact > anchored > contains), then lexically.
func patternMatch(search string, patterns []patternEntry) (AssetDescriptor, bool) {
	type rule struct {
		pattern    string
		descriptor AssetDescriptor
		score      int
		order      int
	}
	rules := make([]rule, 0, len(patterns))
	for _, p := range patterns {
		rules = append(rules, rule{
			pattern:    p.pattern,
			descriptor: p.descriptor,
			score:      patternScore(strings.ToLower(p.pattern)),
			order:      p.order,
		})
	}
	// Deterministic order: higher specificity first, then lexical tie-break.
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].score != rules[j].score {
			return rules[i].score > rules[j].score
		}
		if rules[i].pattern != rules[j].pattern {
			return rules[i].pattern < rules[j].pattern
		}
		if rules[i].order != rules[j].order {
			return rules[i].order < rules[j].order
		}
		return rules[i].descriptor.AssetName < rules[j].descriptor.AssetName
	})

	for _, r := range rules {
		lower := strings.ToLower(r.pattern)
		cmp := lower
		hasPrefix := strings.HasPrefix(cmp, "^")
		hasSuffix := strings.HasSuffix(cmp, "$")
		if hasPrefix {
			cmp = cmp[1:]
		}
		if hasSuffix {
			cmp = cmp[:len(cmp)-1]
		}

		switch {
		case hasPrefix && hasSuffix:
			if search == cmp {
				return r.descriptor, true
			}
		case hasPrefix:
			if strings.HasPrefix(search, cmp) {
				return r.descriptor, true
			}
		case hasSuffix:
			if strings.HasSuffix(search, cmp) {
				return r.descriptor, true
			}
		default:
			if search == cmp || strings.Contains(search, cmp) {
				return r.descriptor, true
			}
		}
	}
	return AssetDescriptor{}, false
}

func repoContentDigest(repo string, repoDigests []string) string {
	for _, value := range repoDigests {
		name, digest, ok := strings.Cut(strings.TrimSpace(value), "@")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(repo)) {
			continue
		}
		if normalized := normalizedContentDigest(digest); normalized != "" {
			return normalized
		}
	}
	return ""
}

func normalizedContentDigest(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "sha256:") && len(value) > len("sha256:") {
		return value
	}
	return ""
}

func digestMatches(actual, expected string) bool {
	actual = normalizedContentDigest(actual)
	expected = strings.TrimSpace(expected)
	if expected != "" && !strings.Contains(expected, ":") {
		expected = "sha256:" + expected
	}
	expected = normalizedContentDigest(expected)
	return actual != "" && expected != "" && strings.EqualFold(actual, expected)
}

func patternScore(pattern string) int {
	cmp := pattern
	hasPrefix := strings.HasPrefix(cmp, "^")
	hasSuffix := strings.HasSuffix(cmp, "$")
	if hasPrefix {
		cmp = cmp[1:]
	}
	if hasSuffix && len(cmp) > 0 {
		cmp = cmp[:len(cmp)-1]
	}
	base := len(cmp)
	switch {
	case hasPrefix && hasSuffix:
		return 3000 + base
	case hasPrefix || hasSuffix:
		return 2000 + base
	default:
		return 1000 + base
	}
}

func splitImageTag(ref string) (repo, tag string) {
	// Handle format "repo:tag"
	if idx := strings.LastIndex(ref, ":"); idx != -1 {
		// Make sure the colon is not inside a port number (check if after last /)
		slashIdx := strings.LastIndex(ref, "/")
		if idx > slashIdx {
			return ref[:idx], ref[idx+1:]
		}
	}
	return ref, ""
}
