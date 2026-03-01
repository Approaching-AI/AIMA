package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// EngineImage represents a locally available engine (container image or native binary).
type EngineImage struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Image       string `json:"image"`               // container image name (container engines) or empty (native)
	Tag         string `json:"tag"`                  // container image tag (container engines) or empty (native)
	SizeBytes   int64  `json:"size_bytes"`
	Platform    string `json:"platform"`
	RuntimeType string `json:"runtime_type"`         // "container" or "native"
	BinaryPath  string `json:"binary_path"`          // path to native binary (native engines only)
	Available   bool   `json:"available"`
	DockerOnly  bool   `json:"docker_only,omitempty"` // true if image is in Docker but not K3S containerd
}

// CommandRunner abstracts shell command execution for testability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// Pipe connects stdout of 'from' to stdin of 'to' (e.g. docker save | k3s ctr import).
	Pipe(ctx context.Context, from, to []string) error
}

// ScanOptions configures engine scanning (both container and native).
type ScanOptions struct {
	AssetPatterns map[string][]string // engine type -> patterns from Engine Asset YAML
	Runner       CommandRunner
	DistDir      string // dist directory for native binaries (~/.aima/dist/{os}-{arch}/)
	Platform     string // current platform (e.g., "windows-amd64")
	BinaryAssets map[string]string // binary name -> engine type (native engines)
}

// ScanUnified discovers both container images and native binaries.
// Returns all available engines from both runtimes (container + native).
// For Docker-only images, attempts import to K3S containerd; warns if no permission.
func ScanUnified(ctx context.Context, opts ScanOptions) ([]*EngineImage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan engines: %w", err)
	}

	var allEngines []*EngineImage

	// Scan container images
	images, err := listImages(ctx, opts.Runner)
	if err == nil {
		matched := matchImages(images, opts.AssetPatterns)

		// Check if any Docker-only images need import
		hasDockerOnly := false
		for _, img := range matched {
			if img.DockerOnly {
				hasDockerOnly = true
				break
			}
		}

		// Pre-check containerd access before attempting expensive imports
		canImport := false
		if hasDockerOnly {
			_, checkErr := opts.Runner.Run(ctx, "k3s", "ctr", "-n", "k8s.io", "version")
			canImport = checkErr == nil
		}

		for _, img := range matched {
			img.RuntimeType = "container"
			img.Platform = opts.Platform

			// Docker-only image: try to import into K3S containerd
			if img.DockerOnly {
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
		allEngines = append(allEngines, matched...)
	}

	// Scan native binaries
	if opts.DistDir != "" {
		native, err := ScanNative(ctx, opts)
		if err == nil {
			allEngines = append(allEngines, native...)
		}
	}

	return allEngines, nil
}

// ScanNative discovers native engine binaries in distDir and PATH.
func ScanNative(ctx context.Context, opts ScanOptions) ([]*EngineImage, error) {
	if opts.DistDir == "" {
		return nil, fmt.Errorf("distDir not configured")
	}

	// BinaryAssets maps binary filename -> engine type; populated from YAML source.binary fields.
	// If not provided, native scan returns empty (caller must supply the mapping).
	knownBinaries := opts.BinaryAssets
	if knownBinaries == nil {
		return nil, nil
	}

	// Build reverse lookup: filename (without .exe) -> engine type
	filenameLookup := make(map[string]string)
	for filename, engineType := range knownBinaries {
		filenameLookup[filename] = engineType
	}

	var found []*EngineImage
	seen := make(map[string]bool)

	// Scan distDir
	if entries, err := os.ReadDir(opts.DistDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if seen[name] {
				continue
			}
			// Check if this is a known engine binary (with or without .exe)
			binaryName := name
			if strings.HasSuffix(name, ".exe") {
				binaryName = strings.TrimSuffix(name, ".exe")
			}
			engineType, ok1 := filenameLookup[binaryName]
			if !ok1 {
				engineType, ok1 = filenameLookup[name]
			}
			if ok1 {
				path := filepath.Join(opts.DistDir, name)
				info, err := entry.Info()
				if err != nil {
					continue
				}
				binaryID := binaryHash(name)
				found = append(found, &EngineImage{
					ID:         binaryID,
					Type:       engineType,
					Image:      "",
					Tag:        "",
					SizeBytes:  info.Size(),
					Platform:   opts.Platform,
					RuntimeType: "native",
					BinaryPath: path,
					Available:  true,
				})
				seen[name] = true
			}
		}
	}

	// Scan PATH for additional binaries
	pathEnv := os.Getenv("PATH")
	if pathEnv != "" {
		sep := string(os.PathListSeparator)
		for _, dir := range strings.Split(pathEnv, sep) {
			if entries, err := os.ReadDir(dir); err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						continue
					}
					name := entry.Name()
					if seen[name] {
						continue
					}
					// Check if this is a known engine binary
					binaryName := name
					if strings.HasSuffix(name, ".exe") {
						binaryName = strings.TrimSuffix(name, ".exe")
					}
					engineType, ok1 := filenameLookup[binaryName]
					if !ok1 {
						engineType, ok1 = filenameLookup[name]
					}
					if ok1 {
						path := filepath.Join(dir, name)
						info, err := entry.Info()
						if err != nil {
							continue
						}
						binaryID := binaryHash(name + "-" + dir)
						found = append(found, &EngineImage{
							ID:         binaryID,
							Type:       engineType,
							Image:      "",
							Tag:        "",
							SizeBytes:  info.Size(),
							Platform:   opts.Platform,
							RuntimeType: "native",
							BinaryPath: path,
							Available:  true,
						})
						seen[name] = true
					}
				}
			}
		}
	}

	return found, nil
}

func binaryHash(name string) string {
	h := sha256.Sum256([]byte(name))
	return hex.EncodeToString(h[:])[:16]
}

type imageInfo struct {
	id     string
	repo   string // image name without tag
	tag    string
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
			ID       string   `json:"id"`
			RepoTags []string `json:"repoTags"`
			Size     string   `json:"size"`
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
				size:   size,
				source: "containerd",
			})
		}
	}

	return images, nil
}

func listDockerImages(ctx context.Context, runner CommandRunner) ([]imageInfo, error) {
	output, err := runner.Run(ctx, "docker", "images", "--format", "{{json .}}", "--no-trunc")
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
			Size       string `json:"Size"`
		}
		if err := json.Unmarshal([]byte(line), &img); err != nil {
			continue
		}
		images = append(images, imageInfo{
			id:     img.ID,
			repo:   img.Repository,
			tag:    img.Tag,
			size:   0, // Docker format doesn't reliably include size
			source: "docker",
		})
	}

	return images, nil
}

// patternEntry pairs a pattern with its engine type. Using a slice instead of
// a map guarantees deterministic matching order when multiple patterns exist.
type patternEntry struct {
	pattern    string
	engineType string
}

// matchImages matches images to engine types using YAML knowledge.
// Knowledge-driven: patterns come from Engine Asset YAMLs, not hardcoded.
// Tag-aware: patterns containing ":" match against "repo:tag"; others match repo only.
// Tag-aware patterns take priority over repo-only patterns.
func matchImages(images []imageInfo, assetPatterns map[string][]string) []*EngineImage {
	var matched []*EngineImage
	seen := make(map[string]bool)

	// Split patterns into ordered slices: tag-aware (contain ":") vs repo-only.
	// Sorted by engine type then pattern for deterministic order.
	var tagPatterns, repoPatterns []patternEntry
	engineTypes := make([]string, 0, len(assetPatterns))
	for et := range assetPatterns {
		engineTypes = append(engineTypes, et)
	}
	sort.Strings(engineTypes)

	for _, engineType := range engineTypes {
		for _, pattern := range assetPatterns[engineType] {
			clean := strings.TrimPrefix(strings.TrimSuffix(pattern, "$"), "^")
			entry := patternEntry{pattern: pattern, engineType: engineType}
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
		matchedEngineType := patternMatch(searchRef, tagPatterns)
		if matchedEngineType == "" {
			matchedEngineType = patternMatch(searchName, repoPatterns)
		}
		if matchedEngineType == "" {
			continue
		}

		matched = append(matched, &EngineImage{
			ID:         img.id,
			Type:       matchedEngineType,
			Image:      img.repo,
			Tag:        img.tag,
			SizeBytes:  img.size,
			Available:  true,
			DockerOnly: img.source == "docker",
		})
		seen[img.id] = true
	}

	return matched
}

// patternMatch checks search string against a set of patterns.
// Supports anchors: ^pattern (prefix), pattern$ (suffix), ^pattern$ (exact).
// Patterns are sorted by specificity (exact > anchored > contains), then lexically.
func patternMatch(search string, patterns []patternEntry) string {
	type rule struct {
		pattern    string
		engineType string
		score      int
	}
	rules := make([]rule, 0, len(patterns))
	for _, p := range patterns {
		rules = append(rules, rule{
			pattern:    p.pattern,
			engineType: p.engineType,
			score:      patternScore(strings.ToLower(p.pattern)),
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
		return rules[i].engineType < rules[j].engineType
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
				return r.engineType
			}
		case hasPrefix:
			if strings.HasPrefix(search, cmp) {
				return r.engineType
			}
		case hasSuffix:
			if strings.HasSuffix(search, cmp) {
				return r.engineType
			}
		default:
			if search == cmp || strings.Contains(search, cmp) {
				return r.engineType
			}
		}
	}
	return ""
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
