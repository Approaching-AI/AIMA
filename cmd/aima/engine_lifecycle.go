package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/recovery"
)

type engineLifecycleInventory interface {
	ListEngines(ctx context.Context) ([]*state.Engine, error)
	UpsertScannedEngine(ctx context.Context, entry *state.Engine) error
	ActivateEngineVersion(ctx context.Context, id string) (string, error)
	RollbackEngineVersion(ctx context.Context, activeID string) (string, error)
	ListDeploymentIntents(ctx context.Context) ([]*recovery.Intent, error)
}

type stagedNativeEngine struct {
	RelativeBinaryPath string
	ContentDigest      string
	SizeBytes          int64
}

type engineEnsureResult struct {
	Plan             engine.EnsurePlan `json:"plan"`
	Applied          bool              `json:"applied"`
	ActiveEngineID   string            `json:"active_engine_id,omitempty"`
	PreviousEngineID string            `json:"previous_engine_id,omitempty"`
}

type engineRollbackResult struct {
	AssetName         string `json:"asset_name"`
	Confirmed         bool   `json:"confirmed"`
	Applied           bool   `json:"applied"`
	Refused           bool   `json:"refused,omitempty"`
	Reason            string `json:"reason,omitempty"`
	OldActiveEngineID string `json:"old_active_engine_id,omitempty"`
	ActiveEngineID    string `json:"active_engine_id,omitempty"`
}

type engineLifecycleService struct {
	inventory          engineLifecycleInventory
	dataDir            string
	inventoryPlatform  string
	catalogPlatform    string
	resolveAsset       func(ctx context.Context, name string) (*knowledge.EngineAsset, error)
	nativeImportAssets func() []knowledge.EngineAsset
	stageNative        func(ctx context.Context, source *engine.BinarySource, stagingDir string, localOnly bool) (stagedNativeEngine, error)
	pullContainer      func(ctx context.Context, asset *knowledge.EngineAsset, expectedDigest string) (*state.Engine, error)
}

type resolvedEnsureCandidate struct {
	planCandidate engine.EnsureCandidate
	nativeSource  *engine.BinarySource
	localOnly     bool
}

func (s *engineLifecycleService) Ensure(ctx context.Context, req engine.EnsureRequest) (engineEnsureResult, error) {
	var result engineEnsureResult
	if s == nil || s.inventory == nil || s.resolveAsset == nil {
		return result, fmt.Errorf("engine lifecycle service is not configured")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	asset, err := s.resolveAsset(ctx, strings.TrimSpace(req.Name))
	if err != nil {
		return result, err
	}
	if asset == nil {
		return result, fmt.Errorf("engine %q not found in Catalog", req.Name)
	}
	assetName := strings.TrimSpace(asset.Metadata.Name)
	if assetName == "" {
		return result, fmt.Errorf("resolved engine has no Catalog asset name")
	}
	requestedVersion := strings.TrimSpace(req.Version)
	if requestedVersion == "" {
		requestedVersion = strings.TrimSpace(asset.Metadata.Version)
	}
	req.Name = assetName
	req.Version = requestedVersion
	req.Platform = s.inventoryPlatform

	entries, err := s.inventory.ListEngines(ctx)
	if err != nil {
		return result, fmt.Errorf("list engine inventory: %w", err)
	}
	intents, err := s.inventory.ListDeploymentIntents(ctx)
	if err != nil {
		return result, fmt.Errorf("list deployment intents: %w", err)
	}

	resolved := s.resolveCandidate(asset, requestedVersion, entries)
	installed := make([]engine.InstalledEngine, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		installed = append(installed, installedEngineProjection(entry))
	}
	affected := affectedEngineDeployments(intents, assetName)
	result.Plan = engine.BuildEnsurePlan(req, resolved.planCandidate, installed, affected)
	if !req.Apply || result.Plan.Blocked {
		return result, nil
	}

	if result.Plan.Action == "reuse" {
		entry := findEngineInventoryEntry(entries, result.Plan.CandidateEngineID)
		if entry == nil {
			return result, fmt.Errorf("planned engine candidate %s disappeared", result.Plan.CandidateEngineID)
		}
		if entry.Active {
			result.Applied = true
			result.ActiveEngineID = entry.ID
			return result, nil
		}
		previous, err := s.inventory.ActivateEngineVersion(ctx, entry.ID)
		if err != nil {
			return result, fmt.Errorf("activate reused engine %s: %w", entry.ID, err)
		}
		result.Applied = true
		result.ActiveEngineID = entry.ID
		result.PreviousEngineID = previous
		return result, nil
	}

	switch resolved.planCandidate.RuntimeType {
	case "native":
		return s.applyNative(ctx, req, asset, resolved, result)
	case "container":
		return s.applyContainer(ctx, req, asset, result)
	default:
		return result, fmt.Errorf("unsupported engine runtime type %q", resolved.planCandidate.RuntimeType)
	}
}

func (s *engineLifecycleService) Rollback(ctx context.Context, name string, confirm bool) (engineRollbackResult, error) {
	result := engineRollbackResult{AssetName: strings.TrimSpace(name), Confirmed: confirm}
	if s == nil || s.inventory == nil {
		return result, fmt.Errorf("engine lifecycle service is not configured")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if result.AssetName == "" {
		return result, fmt.Errorf("engine asset name is required")
	}
	if !confirm {
		result.Refused = true
		result.Reason = "confirm=true is required to roll back an engine version"
		return result, nil
	}

	entries, err := s.inventory.ListEngines(ctx)
	if err != nil {
		return result, fmt.Errorf("list engine inventory: %w", err)
	}
	var current *state.Engine
	for _, entry := range entries {
		if entry == nil || !entry.Active || entry.AssetName != result.AssetName {
			continue
		}
		if s.inventoryPlatform != "" && entry.Platform != s.inventoryPlatform {
			continue
		}
		if current != nil {
			return result, fmt.Errorf("engine asset %s has multiple active runtime versions on platform %s", result.AssetName, s.inventoryPlatform)
		}
		current = entry
	}
	if current == nil {
		return result, fmt.Errorf("engine asset %s has no active version on platform %s", result.AssetName, s.inventoryPlatform)
	}
	if current.PreviousEngineID == "" {
		return result, fmt.Errorf("active engine %s has no previous version", current.ID)
	}

	var previous *state.Engine
	for _, entry := range entries {
		if entry != nil && entry.ID == current.PreviousEngineID {
			previous = entry
			break
		}
	}
	if previous == nil {
		return result, fmt.Errorf("previous engine %s is not available", current.PreviousEngineID)
	}
	if !previous.Available {
		return result, fmt.Errorf("previous engine %s is unavailable", previous.ID)
	}
	if previous.VerificationStatus != "verified" {
		return result, fmt.Errorf("previous engine %s verification status is %q, want verified", previous.ID, previous.VerificationStatus)
	}
	if previous.LifecycleStatus != "verified" && previous.LifecycleStatus != "active" {
		return result, fmt.Errorf("previous engine %s lifecycle status %q is not verified", previous.ID, previous.LifecycleStatus)
	}
	if previous.AssetName != current.AssetName || previous.Platform != current.Platform || previous.RuntimeType != current.RuntimeType {
		return result, fmt.Errorf("previous engine %s does not match active engine asset, platform, and runtime", previous.ID)
	}

	newActiveID, err := s.inventory.RollbackEngineVersion(ctx, current.ID)
	if err != nil {
		return result, fmt.Errorf("roll back active engine %s: %w", current.ID, err)
	}
	result.Applied = true
	result.OldActiveEngineID = current.ID
	result.ActiveEngineID = newActiveID
	return result, nil
}

type stagedNativeImport struct {
	Asset              knowledge.EngineAsset
	Version            string
	VersionRoot        string
	RelativeBinaryPath string
}

func (s *engineLifecycleService) ImportNative(ctx context.Context, bundlePath string) (*state.Engine, error) {
	if s == nil || s.inventory == nil || s.nativeImportAssets == nil {
		return nil, fmt.Errorf("native Engine import is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bundlePath = strings.TrimSpace(bundlePath)
	if bundlePath == "" {
		return nil, fmt.Errorf("native Engine bundle path is required")
	}
	stagingDir, err := engine.NewStagingDir(s.dataDir, "import")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	manager := engine.NewBinaryManager(stagingDir)
	if err := manager.ImportBundle(ctx, bundlePath, "", nil); err != nil {
		return nil, fmt.Errorf("stage native Engine bundle %s: %w", bundlePath, err)
	}
	candidate, err := discoverStagedNativeImport(stagingDir, s.catalogPlatform, s.nativeImportAssets())
	if err != nil {
		return nil, err
	}
	if err := verifyNativeImportBundle(bundlePath, candidate.Asset.Source, s.catalogPlatform); err != nil {
		return nil, err
	}

	relativeBinary, err := safeStagedRelativePath(candidate.RelativeBinaryPath)
	if err != nil {
		return nil, err
	}
	stagedBinary := filepath.Join(candidate.VersionRoot, relativeBinary)
	info, err := os.Lstat(stagedBinary)
	if err != nil {
		return nil, fmt.Errorf("inspect imported Engine binary %s: %w", stagedBinary, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("imported Engine binary %s is not a regular file", stagedBinary)
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		if err := os.Chmod(stagedBinary, info.Mode().Perm()|0o755); err != nil {
			return nil, fmt.Errorf("make imported Engine binary executable: %w", err)
		}
		info, err = os.Stat(stagedBinary)
		if err != nil {
			return nil, fmt.Errorf("inspect executable imported Engine binary: %w", err)
		}
	}
	contentDigest, err := sha256FileDigest(stagedBinary)
	if err != nil {
		return nil, fmt.Errorf("hash imported Engine binary %s: %w", stagedBinary, err)
	}
	destination, err := engine.ManagedVersionDir(
		s.dataDir, s.inventoryPlatform, candidate.Asset.Metadata.Name, candidate.Version,
	)
	if err != nil {
		return nil, err
	}
	_, destinationErr := os.Lstat(destination)
	destinationExisted := destinationErr == nil
	if destinationErr != nil && !os.IsNotExist(destinationErr) {
		return nil, fmt.Errorf("inspect imported Engine destination %s: %w", destination, destinationErr)
	}
	if err := engine.PromoteStagedDir(candidate.VersionRoot, destination); err != nil {
		return nil, err
	}
	finalBinary := filepath.Join(destination, relativeBinary)
	relativeFromDist := filepath.Join(candidate.Asset.Metadata.Name, candidate.Version, relativeBinary)
	entry := &state.Engine{
		ID:                 engine.ManagedNativeEngineID(candidate.Asset.Metadata.Name, candidate.Version, relativeFromDist),
		Type:               candidate.Asset.Metadata.Type,
		SizeBytes:          info.Size(),
		Platform:           s.inventoryPlatform,
		RuntimeType:        "native",
		BinaryPath:         finalBinary,
		Available:          true,
		AssetName:          candidate.Asset.Metadata.Name,
		Version:            candidate.Version,
		CatalogVersion:     candidate.Asset.Metadata.Version,
		Origin:             "imported",
		ContentDigest:      contentDigest,
		Location:           finalBinary,
		LifecycleStatus:    "verified",
		VerificationStatus: "verified",
	}
	if err := s.inventory.UpsertScannedEngine(ctx, entry); err != nil {
		if !destinationExisted {
			_ = os.RemoveAll(destination)
		}
		return nil, fmt.Errorf("record imported native Engine %s: %w", entry.ID, err)
	}
	return entry, nil
}

func discoverStagedNativeImport(stagingDir, catalogPlatform string, assets []knowledge.EngineAsset) (stagedNativeImport, error) {
	byBinary := make(map[string][]knowledge.EngineAsset)
	for i := range assets {
		asset := assets[i]
		if asset.Source == nil || strings.TrimSpace(asset.Source.Binary) == "" || !asset.Source.Supports(catalogPlatform) {
			continue
		}
		key := normalizedNativeImportBinary(asset.Source.Binary)
		byBinary[key] = append(byBinary[key], asset)
	}
	var candidates []stagedNativeImport
	err := filepath.WalkDir(stagingDir, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		matches := byBinary[normalizedNativeImportBinary(entry.Name())]
		if len(matches) == 0 {
			return nil
		}
		relative, err := filepath.Rel(stagingDir, filePath)
		if err != nil {
			return nil
		}
		for i := range matches {
			asset := matches[i]
			version, versionRoot, relativeBinary, ok := stagedNativeImportLayout(stagingDir, relative, asset.Metadata.Name)
			if !ok || !catalogAcceptsImportedVersion(asset.Metadata, version) {
				continue
			}
			candidates = append(candidates, stagedNativeImport{
				Asset: asset, Version: version, VersionRoot: versionRoot, RelativeBinaryPath: relativeBinary,
			})
		}
		return nil
	})
	if err != nil {
		return stagedNativeImport{}, fmt.Errorf("inspect staged native Engine bundle: %w", err)
	}
	if len(candidates) == 0 {
		return stagedNativeImport{}, fmt.Errorf("native Engine bundle contains no unique Catalog-compatible <asset>/<version>/<binary> layout")
	}
	if len(candidates) != 1 {
		sort.Slice(candidates, func(i, j int) bool {
			left := candidates[i].Asset.Metadata.Name + "|" + candidates[i].Version + "|" + candidates[i].RelativeBinaryPath
			right := candidates[j].Asset.Metadata.Name + "|" + candidates[j].Version + "|" + candidates[j].RelativeBinaryPath
			return left < right
		})
		return stagedNativeImport{}, fmt.Errorf("native Engine bundle is ambiguous: found %d Catalog-compatible binaries", len(candidates))
	}
	return candidates[0], nil
}

func stagedNativeImportLayout(stagingDir, relativePath, assetName string) (string, string, string, bool) {
	parts := splitPortablePath(relativePath)
	if len(parts) < 2 {
		return "", "", "", false
	}
	for i := 0; i+2 < len(parts); i++ {
		if !strings.EqualFold(parts[i], assetName) {
			continue
		}
		version := parts[i+1]
		root := filepath.Join(append([]string{stagingDir}, parts[:i+2]...)...)
		relativeBinary := filepath.Join(parts[i+2:]...)
		return version, root, relativeBinary, true
	}
	version := parts[0]
	root := filepath.Join(stagingDir, version)
	relativeBinary := filepath.Join(parts[1:]...)
	return version, root, relativeBinary, true
}

func splitPortablePath(value string) []string {
	return strings.FieldsFunc(filepath.Clean(value), func(r rune) bool { return r == '/' || r == '\\' })
}

func normalizedNativeImportBinary(value string) string {
	value = strings.ToLower(strings.TrimSpace(filepath.Base(value)))
	return strings.TrimSuffix(value, ".exe")
}

func catalogAcceptsImportedVersion(metadata knowledge.EngineMetadata, version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	if version == strings.TrimSpace(metadata.Version) {
		return true
	}
	for _, declared := range metadata.CompatibleVersions {
		declared = strings.TrimSpace(declared)
		if version == declared {
			return true
		}
		if strings.HasSuffix(declared, ".x") {
			prefix := strings.TrimSuffix(declared, "x")
			if len(version) > len(prefix) && strings.HasPrefix(version, prefix) {
				return true
			}
		}
	}
	return false
}

func verifyNativeImportBundle(bundlePath string, source *knowledge.EngineSource, catalogPlatform string) error {
	if source == nil {
		return fmt.Errorf("native Engine import has no Catalog source")
	}
	expected := strings.TrimSpace(source.SHA256[catalogPlatform])
	if expected == "" {
		return fmt.Errorf("verify native Engine bundle %s: Catalog SHA256 is required", bundlePath)
	}
	info, err := os.Stat(bundlePath)
	if err != nil {
		return fmt.Errorf("inspect native Engine bundle %s: %w", bundlePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("verify native Engine bundle %s: Catalog SHA256 requires a file bundle", bundlePath)
	}
	if !strings.HasPrefix(strings.ToLower(expected), "sha256:") {
		expected = "sha256:" + expected
	}
	actual, err := sha256FileDigest(bundlePath)
	if err != nil {
		return fmt.Errorf("hash native Engine bundle %s: %w", bundlePath, err)
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("sha256 mismatch for native Engine bundle %s: expected %s, got %s", bundlePath, expected, actual)
	}
	return nil
}

func (s *engineLifecycleService) applyNative(ctx context.Context, req engine.EnsureRequest, asset *knowledge.EngineAsset, resolved resolvedEnsureCandidate, result engineEnsureResult) (engineEnsureResult, error) {
	if s.stageNative == nil || resolved.nativeSource == nil {
		return result, fmt.Errorf("native engine staging is not configured")
	}
	stagingDir, err := engine.NewStagingDir(s.dataDir, asset.Metadata.Name)
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	artifact, err := s.stageNative(ctx, resolved.nativeSource, stagingDir, resolved.localOnly)
	if err != nil {
		return result, fmt.Errorf("stage native engine %s: %w", asset.Metadata.Name, err)
	}
	relativeBinary, err := safeStagedRelativePath(artifact.RelativeBinaryPath)
	if err != nil {
		return result, err
	}
	stagedBinary := filepath.Join(stagingDir, relativeBinary)
	info, err := os.Lstat(stagedBinary)
	if err != nil {
		return result, fmt.Errorf("inspect staged engine binary %s: %w", stagedBinary, err)
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("staged engine binary %s is not a regular file", stagedBinary)
	}
	if strings.TrimSpace(artifact.ContentDigest) == "" {
		return result, fmt.Errorf("staged engine %s has no verified content digest", asset.Metadata.Name)
	}
	actualDigest, err := sha256FileDigest(stagedBinary)
	if err != nil {
		return result, fmt.Errorf("hash staged engine binary %s: %w", stagedBinary, err)
	}
	artifact.ContentDigest = actualDigest
	if artifact.SizeBytes <= 0 {
		artifact.SizeBytes = info.Size()
	}

	destination, err := engine.ManagedVersionDir(s.dataDir, s.inventoryPlatform, asset.Metadata.Name, req.Version)
	if err != nil {
		return result, err
	}
	if err := engine.PromoteStagedDir(stagingDir, destination); err != nil {
		return result, err
	}
	finalBinary := filepath.Join(destination, relativeBinary)
	relativeFromDist := filepath.Join(asset.Metadata.Name, req.Version, relativeBinary)
	entry := &state.Engine{
		ID:                 engine.ManagedNativeEngineID(asset.Metadata.Name, req.Version, relativeFromDist),
		Type:               asset.Metadata.Type,
		SizeBytes:          artifact.SizeBytes,
		Platform:           s.inventoryPlatform,
		RuntimeType:        "native",
		BinaryPath:         finalBinary,
		Available:          true,
		AssetName:          asset.Metadata.Name,
		Version:            req.Version,
		CatalogVersion:     asset.Metadata.Version,
		Origin:             "managed",
		ContentDigest:      artifact.ContentDigest,
		Location:           finalBinary,
		LifecycleStatus:    "verified",
		VerificationStatus: "verified",
	}
	if err := s.inventory.UpsertScannedEngine(ctx, entry); err != nil {
		return result, fmt.Errorf("record verified native engine %s: %w", entry.ID, err)
	}
	previous, err := s.inventory.ActivateEngineVersion(ctx, entry.ID)
	if err != nil {
		return result, fmt.Errorf("activate native engine %s: %w", entry.ID, err)
	}
	result.Applied = true
	result.ActiveEngineID = entry.ID
	result.PreviousEngineID = previous
	result.Plan.CandidateEngineID = entry.ID
	return result, nil
}

func (s *engineLifecycleService) applyContainer(ctx context.Context, req engine.EnsureRequest, asset *knowledge.EngineAsset, result engineEnsureResult) (engineEnsureResult, error) {
	if s.pullContainer == nil {
		return result, fmt.Errorf("container engine pull is not configured")
	}
	entry, err := s.pullContainer(ctx, asset, asset.Image.Digest)
	if err != nil {
		return result, fmt.Errorf("stage container engine %s: %w", asset.Metadata.Name, err)
	}
	if entry == nil || strings.TrimSpace(entry.ID) == "" {
		return result, fmt.Errorf("container engine %s produced no inventory evidence", asset.Metadata.Name)
	}
	stored := *entry
	stored.Type = asset.Metadata.Type
	stored.Image = asset.Image.Name
	stored.Tag = asset.Image.Tag
	stored.Platform = s.inventoryPlatform
	stored.RuntimeType = "container"
	stored.Available = true
	stored.AssetName = asset.Metadata.Name
	stored.Version = req.Version
	stored.CatalogVersion = asset.Metadata.Version
	if stored.Origin == "" || stored.Origin == "legacy" {
		stored.Origin = "managed"
	}
	stored.ContentDigest = asset.Image.Digest
	stored.Location = asset.Image.Name
	if asset.Image.Tag != "" {
		stored.Location += ":" + asset.Image.Tag
	}
	stored.Active = false
	stored.LifecycleStatus = "verified"
	stored.VerificationStatus = "verified"
	stored.PreviousEngineID = ""
	if err := s.inventory.UpsertScannedEngine(ctx, &stored); err != nil {
		return result, fmt.Errorf("record verified container engine %s: %w", stored.ID, err)
	}
	previous, err := s.inventory.ActivateEngineVersion(ctx, stored.ID)
	if err != nil {
		return result, fmt.Errorf("activate container engine %s: %w", stored.ID, err)
	}
	result.Applied = true
	result.ActiveEngineID = stored.ID
	result.PreviousEngineID = previous
	result.Plan.CandidateEngineID = stored.ID
	return result, nil
}

func (s *engineLifecycleService) resolveCandidate(asset *knowledge.EngineAsset, requestedVersion string, entries []*state.Engine) resolvedEnsureCandidate {
	runtimeType := preferredEngineRuntimeType(asset, s.catalogPlatform)
	candidate := engine.EnsureCandidate{
		AssetName:   strings.TrimSpace(asset.Metadata.Name),
		Version:     strings.TrimSpace(asset.Metadata.Version),
		RuntimeType: runtimeType,
		Origin:      "managed",
	}
	if candidate.Version != "" && requestedVersion != "" && candidate.Version != requestedVersion {
		candidate.BlockReason = fmt.Sprintf("requested version %s is not available from Catalog asset %s at version %s", requestedVersion, candidate.AssetName, candidate.Version)
	}

	resolved := resolvedEnsureCandidate{planCandidate: candidate}
	if runtimeType == "native" {
		source := toEngineBinarySource(asset.Source)
		resolved.nativeSource = source
		if source == nil {
			resolved.planCandidate.BlockReason = "Catalog asset has no native source"
			return resolved
		}
		if source.InstallType == "preinstalled" {
			resolved.planCandidate.BlockReason = "preinstalled engine is not present in inventory and has no managed install source"
			return resolved
		}
		checksum := strings.TrimSpace(source.SHA256[s.catalogPlatform])
		if checksum != "" && !strings.HasPrefix(strings.ToLower(checksum), "sha256:") {
			checksum = "sha256:" + checksum
		}
		resolved.planCandidate.VerificationEvidence = checksum
		localBundle := firstExistingLocalBundle(source.LocalBundles)
		networkSource := ""
		if mirrors := source.Mirror[s.catalogPlatform]; len(mirrors) > 0 {
			networkSource = strings.TrimSpace(mirrors[0])
		}
		if networkSource == "" {
			networkSource = strings.TrimSpace(source.Download[s.catalogPlatform])
		}
		if localBundle != "" {
			resolved.planCandidate.Source = localBundle
			resolved.localOnly = networkSource == ""
			resolved.planCandidate.NetworkRequired = networkSource != ""
			return resolved
		}
		resolved.planCandidate.Source = networkSource
		resolved.planCandidate.NetworkRequired = networkSource != ""
		return resolved
	}

	resolved.planCandidate.Source = containerImageRef(asset)
	resolved.planCandidate.NetworkRequired = true
	resolved.planCandidate.VerificationEvidence = strings.TrimSpace(asset.Image.Digest)
	var localMatches []*state.Engine
	for _, entry := range entries {
		if entry != nil && entry.Available && entry.RuntimeType == "container" &&
			entry.Image == asset.Image.Name && entry.Tag == asset.Image.Tag &&
			(entry.Platform == "" || strings.EqualFold(entry.Platform, s.inventoryPlatform)) {
			localMatches = append(localMatches, entry)
		}
	}
	if len(localMatches) > 0 {
		sort.Slice(localMatches, func(i, j int) bool {
			if localMatches[i].Active != localMatches[j].Active {
				return localMatches[i].Active
			}
			return localMatches[i].ID < localMatches[j].ID
		})
		resolved.planCandidate.ID = localMatches[0].ID
		resolved.planCandidate.NetworkRequired = false
	}
	if asset.Image.Distribution == "local" {
		if len(localMatches) == 0 {
			resolved.planCandidate.BlockReason = "local-only container image is not present in inventory"
		}
	}
	return resolved
}

func buildEngineLifecycleService(ac *appContext) *engineLifecycleService {
	service := &engineLifecycleService{
		inventory:         ac.db,
		dataDir:           ac.dataDir,
		inventoryPlatform: runtimeInventoryPlatform(),
		catalogPlatform:   runtimeCatalogPlatform(),
	}
	service.resolveAsset = func(ctx context.Context, name string) (*knowledge.EngineAsset, error) {
		runtimeName := ""
		if ac.rt != nil {
			runtimeName = ac.rt.Name()
		}
		hwInfo := buildHardwareInfo(ctx, ac.cat, runtimeName)
		if name == "" {
			asset := defaultEngineAsset(ac.cat, hwInfo)
			if asset == nil {
				return nil, fmt.Errorf("no default engine is available for this host")
			}
			return asset, nil
		}
		asset := ac.cat.FindEngineByName(name, hwInfo)
		if asset == nil {
			return nil, fmt.Errorf("engine %q not found in Catalog for gpu_arch %q", name, hwInfo.GPUArch)
		}
		return asset, nil
	}
	service.nativeImportAssets = func() []knowledge.EngineAsset {
		return append([]knowledge.EngineAsset(nil), ac.cat.EngineAssets...)
	}
	service.stageNative = stageNativeEngineArtifact
	service.pullContainer = func(ctx context.Context, asset *knowledge.EngineAsset, expectedDigest string) (*state.Engine, error) {
		return pullAndDiscoverContainerEngine(ctx, asset, expectedDigest, service.inventoryPlatform)
	}
	return service
}

func stageNativeEngineArtifact(ctx context.Context, source *engine.BinarySource, stagingDir string, localOnly bool) (stagedNativeEngine, error) {
	if source == nil {
		return stagedNativeEngine{}, fmt.Errorf("native source is nil")
	}
	selected := *source
	if localOnly {
		selected.Download = nil
		selected.Mirror = nil
	}
	manager := engine.NewBinaryManager(stagingDir)
	if err := manager.Download(ctx, &selected, nil); err != nil {
		return stagedNativeEngine{}, err
	}
	binaryPath, err := findStagedNativeBinary(stagingDir, selected.Binary)
	if err != nil {
		return stagedNativeEngine{}, err
	}
	relative, err := filepath.Rel(stagingDir, binaryPath)
	if err != nil {
		return stagedNativeEngine{}, err
	}
	digest, err := sha256FileDigest(binaryPath)
	if err != nil {
		return stagedNativeEngine{}, err
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		return stagedNativeEngine{}, err
	}
	return stagedNativeEngine{RelativeBinaryPath: relative, ContentDigest: digest, SizeBytes: info.Size()}, nil
}

func pullAndDiscoverContainerEngine(ctx context.Context, asset *knowledge.EngineAsset, expectedDigest, inventoryPlatform string) (*state.Engine, error) {
	if asset == nil || asset.Image.Name == "" {
		return nil, fmt.Errorf("container image is not configured")
	}
	if strings.TrimSpace(expectedDigest) == "" {
		return nil, fmt.Errorf("container image digest is required")
	}
	runner := &execRunner{}
	ref := containerImageRef(asset)
	preexisting := engine.ImageExistsInContainerd(ctx, ref, runner) || engine.ImageExistsInDocker(ctx, ref, runner)
	if preexisting {
		if err := engine.VerifyImageDigest(ctx, runner, ref, expectedDigest); err != nil {
			return nil, err
		}
	} else if err := engine.Pull(ctx, engine.PullOptions{
		Image:          asset.Image.Name,
		Tag:            asset.Image.Tag,
		Registries:     engineRegistriesWithEnv(asset.Image.Registries),
		SizeHintMB:     asset.Image.SizeApproxMB,
		Runner:         runner,
		ExpectedDigest: expectedDigest,
	}); err != nil {
		return nil, err
	}
	patterns := append([]string(nil), asset.Patterns...)
	patterns = append(patterns, asset.Image.Name)
	images, err := engine.ScanUnified(ctx, engine.ScanOptions{
		Assets: []engine.AssetDescriptor{{
			AssetName:          asset.Metadata.Name,
			Type:               asset.Metadata.Type,
			CatalogVersion:     asset.Metadata.Version,
			CompatibleVersions: append([]string(nil), asset.Metadata.CompatibleVersions...),
			Patterns:           patterns,
		}},
		Runner:   runner,
		Platform: inventoryPlatform,
	})
	if err != nil {
		return nil, err
	}
	var matches []*engine.EngineImage
	for _, image := range images {
		if image != nil && image.Image == asset.Image.Name && image.Tag == asset.Image.Tag &&
			(image.AssetName == asset.Metadata.Name || image.Type == asset.Metadata.Type) {
			matches = append(matches, image)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("verified container image %s was not found by engine discovery", ref)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	entry := stateEngineFromScan(matches[0])
	if preexisting {
		entry.Origin = "preinstalled"
	} else {
		entry.Origin = "managed"
	}
	return entry, nil
}

func installedEngineProjection(entry *state.Engine) engine.InstalledEngine {
	source := entry.Location
	if source == "" {
		source = entry.BinaryPath
	}
	if source == "" {
		source = entry.Image
		if source != "" && entry.Tag != "" {
			source += ":" + entry.Tag
		}
	}
	return engine.InstalledEngine{
		ID:                 entry.ID,
		AssetName:          entry.AssetName,
		Version:            entry.Version,
		CatalogVersion:     entry.CatalogVersion,
		Platform:           entry.Platform,
		RuntimeType:        entry.RuntimeType,
		Origin:             entry.Origin,
		Source:             source,
		Available:          entry.Available,
		Active:             entry.Active,
		VerificationStatus: entry.VerificationStatus,
	}
}

func affectedEngineDeployments(intents []*recovery.Intent, assetName string) []string {
	var names []string
	for _, intent := range intents {
		if intent != nil && strings.EqualFold(strings.TrimSpace(intent.EngineAsset), strings.TrimSpace(assetName)) {
			names = append(names, intent.Name)
		}
	}
	return names
}

func findEngineInventoryEntry(entries []*state.Engine, id string) *state.Engine {
	for _, entry := range entries {
		if entry != nil && entry.ID == id {
			return entry
		}
	}
	return nil
}

func firstExistingLocalBundle(paths []string) string {
	for _, candidate := range paths {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func safeStagedRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return "", fmt.Errorf("invalid staged binary path %q", value)
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if len(normalized) >= 2 && normalized[1] == ':' &&
		((normalized[0] >= 'a' && normalized[0] <= 'z') || (normalized[0] >= 'A' && normalized[0] <= 'Z')) {
		return "", fmt.Errorf("invalid staged binary path %q", value)
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("invalid staged binary path %q", value)
	}
	return filepath.FromSlash(cleaned), nil
}

func findStagedNativeBinary(stagingDir, binaryName string) (string, error) {
	key := normalizedLifecycleBinaryName(binaryName)
	if key == "" {
		return "", fmt.Errorf("Catalog native binary name is required")
	}
	var matches []string
	err := filepath.WalkDir(stagingDir, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || normalizedLifecycleBinaryName(entry.Name()) != key {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			matches = append(matches, filePath)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("binary %s not found in verified staging directory", binaryName)
	}
	sort.Slice(matches, func(i, j int) bool {
		leftDepth := strings.Count(filepath.Clean(matches[i]), string(filepath.Separator))
		rightDepth := strings.Count(filepath.Clean(matches[j]), string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return matches[i] < matches[j]
	})
	return matches[0], nil
}

func normalizedLifecycleBinaryName(value string) string {
	value = strings.ToLower(strings.TrimSpace(filepath.Base(value)))
	return strings.TrimSuffix(value, ".exe")
}

func sha256FileDigest(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func containerImageRef(asset *knowledge.EngineAsset) string {
	if asset == nil {
		return ""
	}
	if asset.Image.Tag == "" {
		return asset.Image.Name
	}
	return asset.Image.Name + ":" + asset.Image.Tag
}

func runtimeInventoryPlatform() string {
	return goruntime.GOOS + "-" + goruntime.GOARCH
}

func runtimeCatalogPlatform() string {
	return goruntime.GOOS + "/" + goruntime.GOARCH
}
