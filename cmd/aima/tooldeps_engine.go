package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"

	state "github.com/jguan/aima/internal"
)

type engineRemovalStore interface {
	GetEngine(ctx context.Context, id string) (*state.Engine, error)
	EngineHasReferences(ctx context.Context, id string) (bool, error)
	SaveSnapshot(ctx context.Context, snapshot *state.RollbackSnapshot) error
	DeleteEngine(ctx context.Context, id string) error
}

// buildEngineDeps wires the current engine management surface:
// engine.scan, engine.list, engine.info, engine.pull, engine.import, and engine.remove.
func buildEngineDeps(ac *appContext, deps *mcp.ToolDeps,
	scanEnginesCore func(ctx context.Context, runtimeFilter string, autoImport bool) (json.RawMessage, error),
	dlTracker *DownloadTracker,
) {
	cat := ac.cat
	db := ac.db
	rt := ac.rt
	dockerRt := ac.dockerRt
	dataDir := ac.dataDir

	deps.ScanEngines = scanEnginesCore
	lifecycleService := buildEngineLifecycleService(ac)
	deps.EnsureEngine = func(ctx context.Context, name, version string, apply bool) (json.RawMessage, error) {
		result, ensureErr := lifecycleService.Ensure(ctx, engine.EnsureRequest{
			Name: name, Version: version, Apply: apply,
		})
		data, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode engine ensure result: %w", marshalErr)
		}
		return data, ensureErr
	}
	deps.RollbackEngine = func(ctx context.Context, name string, confirm bool) (json.RawMessage, error) {
		result, rollbackErr := lifecycleService.Rollback(ctx, name, confirm)
		data, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode engine rollback result: %w", marshalErr)
		}
		return data, rollbackErr
	}

	deps.ListEngines = func(ctx context.Context) (json.RawMessage, error) {
		engines, err := db.ListEngines(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(engines)
	}

	deps.GetEngineInfo = func(ctx context.Context, name string) (json.RawMessage, error) {
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		nameLower := strings.ToLower(name)

		// Catalog lookup: exact name -> type+hw preference -> image substring
		asset := cat.FindEngineByName(name, hwInfo)

		// Find installed instances in DB (by type, image name, or ID)
		allEngines, err := db.ListEngines(ctx)
		if err != nil {
			return nil, err
		}
		installed := make([]*state.Engine, 0)
		for _, e := range allEngines {
			if strings.ToLower(e.Type) == nameLower ||
				strings.Contains(strings.ToLower(e.Image), nameLower) ||
				strings.HasPrefix(e.ID, name) {
				installed = append(installed, e)
			}
		}

		if asset == nil && len(installed) == 0 {
			return nil, fmt.Errorf("engine %q not found in catalog or database", name)
		}

		// If found only in DB, try to find the catalog asset by installed type
		if asset == nil && len(installed) > 0 {
			asset = cat.FindEngineByName(installed[0].Type, hwInfo)
		}

		result := struct {
			Asset     *knowledge.EngineAsset `json:"asset"`
			Installed []*state.Engine        `json:"installed"`
		}{
			Asset:     asset,
			Installed: installed,
		}
		return json.Marshal(result)
	}

	deps.PullEngine = func(ctx context.Context, name string, onProgress func(engine.ProgressEvent)) error {
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		if name == "" {
			if ea := defaultEngineAsset(cat, hwInfo); ea != nil {
				name = ea.Metadata.Name
			} else {
				name = cat.DefaultEngine()
			}
		}
		dlID := fmt.Sprintf("engine-%s-%d", name, time.Now().UnixMilli())
		dlTracker.Start(dlID, "engine", name)
		dlTracker.Update(dlID, "starting", "Resolving engine...", -1, -1, -1)
		keepAliveStop := make(chan struct{})
		go dlTracker.KeepAlive(dlID, keepAliveStop)
		reportProgress := func(ev engine.ProgressEvent) {
			dlTracker.Update(dlID, ev.Phase, ev.Message, ev.Downloaded, ev.Total, ev.Speed)
			if onProgress != nil {
				onProgress(ev)
			}
		}
		err := func() error {
			defer close(keepAliveStop)
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			ea := cat.FindEngineByName(name, hwInfo)
			if ea == nil {
				return fmt.Errorf("engine %q not found in catalog for gpu_arch %q", name, hwInfo.GPUArch)
			}

			// Local-only engines cannot be pulled from a registry
			if ea.Image.Distribution == "local" {
				return fmt.Errorf("engine %q is a locally-built image (distribution: local); build it on the target device or import with: aima engine import <tarball>", name)
			}

			// Native binary path: prefer if platform is supported
			platform := goruntime.GOOS + "/" + goruntime.GOARCH
			preferredRuntime := preferredEngineRuntimeType(ea, platform)
			if preferredRuntime == "native" && ea.Source != nil && ea.Source.Supports(platform) {
				distPlatform := goruntime.GOOS + "-" + goruntime.GOARCH
				distDir := filepath.Join(dataDir, "dist", distPlatform)
				mgr := engine.NewBinaryManager(distDir)
				_, downloaded, err := mgr.Ensure(ctx, toEngineBinarySource(ea.Source), reportProgress)
				if err != nil {
					return err
				}
				_, _ = scanEnginesCore(ctx, "native", false)
				if !downloaded {
					reportProgress(engine.ProgressEvent{
						Phase:      "already_available",
						Downloaded: -1,
						Total:      -1,
						Speed:      -1,
						Message:    "engine binary already available locally",
					})
				}
				return nil
			}
			// Container image path
			if ea.Image.Name != "" && imageSupportsPlatform(ea, platform) {
				fullRef := ea.Image.Name + ":" + ea.Image.Tag
				runner := &execRunner{}
				inContainerd := engine.ImageExistsInContainerd(ctx, fullRef, runner)
				inDocker := engine.ImageExistsInDocker(ctx, fullRef, runner)
				if inContainerd || inDocker {
					slog.Info("engine image already available locally", "image", fullRef, "containerd", inContainerd, "docker", inDocker)
					if rt.Name() == "k3s" && !inContainerd && inDocker {
						if os.Getuid() != 0 {
							_, _ = scanEnginesCore(ctx, "container", false)
							if dockerRt != nil {
								reportProgress(engine.ProgressEvent{
									Phase:      "already_available",
									Downloaded: -1,
									Total:      -1,
									Speed:      -1,
									Message:    "engine image already available in Docker; Docker runtime can use it without K3S import",
								})
								return nil
							}
							return fmt.Errorf("%s", k3sDockerImportHint(fullRef))
						}
						if err := engine.ImportDockerToContainerd(ctx, fullRef, runner); err != nil {
							return fmt.Errorf("import existing engine image %s into containerd: %w", fullRef, err)
						}
						inContainerd = true
					}
					_, _ = scanEnginesCore(ctx, "container", false)
					msg := "engine image already available locally"
					if rt.Name() == "k3s" && inContainerd && inDocker {
						msg = "engine image already available locally (docker + containerd)"
					} else if rt.Name() == "k3s" && inContainerd {
						msg = "engine image already available in K3S containerd"
					}
					reportProgress(engine.ProgressEvent{
						Phase:      "already_available",
						Downloaded: -1,
						Total:      -1,
						Speed:      -1,
						Message:    msg,
					})
					return nil
				}
				// Knowledge-driven reuse: if any compatible tag of the same image
				// is already present in Docker, alias it to the pinned tag instead
				// of re-pulling multi-GB of bytes. Compat list lives in engine YAML
				// (INV-1: no Go branch per engine type).
				for _, compatTag := range ea.Image.CompatibleTags {
					if compatTag == "" || compatTag == ea.Image.Tag {
						continue
					}
					compatRef := ea.Image.Name + ":" + compatTag
					if !engine.ImageExistsInDocker(ctx, compatRef, runner) {
						continue
					}
					if err := engine.TagDockerImage(ctx, compatRef, fullRef, runner); err != nil {
						slog.Warn("compatible tag alias failed; falling through to pull", "src", compatRef, "dst", fullRef, "error", err)
						break
					}
					slog.Info("aliased compatible engine image", "src", compatRef, "dst", fullRef)
					if rt.Name() == "k3s" && os.Getuid() == 0 {
						if err := engine.ImportDockerToContainerd(ctx, fullRef, runner); err != nil {
							return fmt.Errorf("import aliased engine image %s into containerd: %w", fullRef, err)
						}
					}
					_, _ = scanEnginesCore(ctx, "container", false)
					reportProgress(engine.ProgressEvent{
						Phase:      "already_available",
						Downloaded: -1,
						Total:      -1,
						Speed:      -1,
						Message:    fmt.Sprintf("reused compatible image %s (aliased to %s)", compatRef, fullRef),
					})
					return nil
				}
				if err := engine.Pull(ctx, engine.PullOptions{
					Image:      ea.Image.Name,
					Tag:        ea.Image.Tag,
					Registries: ea.Image.Registries,
					SizeHintMB: ea.Image.SizeApproxMB,
					OnProgress: reportProgress,
					Runner:     &execRunner{},
				}); err != nil {
					return err
				}
				_, _ = scanEnginesCore(ctx, "container", false)
				return nil
			}
			if ea.Source != nil && ea.Source.Supports(platform) {
				distPlatform := goruntime.GOOS + "-" + goruntime.GOARCH
				distDir := filepath.Join(dataDir, "dist", distPlatform)
				mgr := engine.NewBinaryManager(distDir)
				_, downloaded, err := mgr.Ensure(ctx, toEngineBinarySource(ea.Source), reportProgress)
				if err != nil {
					return err
				}
				_, _ = scanEnginesCore(ctx, "native", false)
				if !downloaded {
					reportProgress(engine.ProgressEvent{
						Phase:      "already_available",
						Downloaded: -1,
						Total:      -1,
						Speed:      -1,
						Message:    "engine binary already available locally",
					})
				}
				return nil
			}
			return fmt.Errorf("engine %q has no download source for platform %s/%s", name, goruntime.GOOS, goruntime.GOARCH)
		}()
		dlTracker.Finish(dlID, err)
		return err
	}

	deps.ImportEngine = func(ctx context.Context, path string) error {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve path %s: %w", path, err)
		}
		if looksLikeNativeEngineBundle(absPath) {
			if _, err := lifecycleService.ImportNative(ctx, absPath); err != nil {
				return fmt.Errorf("import native engine bundle from %s: %w", path, err)
			}
			return nil
		}
		before, listErr := db.ListEngines(ctx)
		if listErr != nil {
			return fmt.Errorf("list engines before container import: %w", listErr)
		}
		knownIDs := make(map[string]struct{}, len(before))
		for _, entry := range before {
			knownIDs[entry.ID] = struct{}{}
		}
		if err := engine.Import(ctx, absPath, &execRunner{}); err != nil {
			if _, nativeErr := lifecycleService.ImportNative(ctx, absPath); nativeErr == nil {
				return nil
			} else {
				return fmt.Errorf("import engine from %s as container image failed: %w; native bundle import also failed: %v", path, err, nativeErr)
			}
		}
		// Refresh only container inventory, then upgrade newly discovered rows to
		// imported ownership. Digest-matched Catalog images are verified.
		scanData, err := scanEnginesCore(ctx, "container", false)
		if err != nil {
			return fmt.Errorf("scan imported container image: %w", err)
		}
		var importedImages []*engine.EngineImage
		if err := json.Unmarshal(scanData, &importedImages); err != nil {
			return fmt.Errorf("decode imported container scan: %w", err)
		}
		for _, image := range importedImages {
			if image == nil || image.RuntimeType != "container" {
				continue
			}
			if _, existed := knownIDs[image.ID]; existed {
				continue
			}
			entry := stateEngineFromScan(image)
			entry.Origin = "imported"
			if digest := catalogContainerDigest(cat, image.AssetName); digest != "" && strings.EqualFold(digest, image.ContentDigest) {
				entry.Version = entry.CatalogVersion
				entry.LifecycleStatus = "verified"
				entry.VerificationStatus = "verified"
			}
			if err := db.UpsertScannedEngine(ctx, entry); err != nil {
				return fmt.Errorf("record imported container Engine %s: %w", entry.ID, err)
			}
		}
		return nil
	}

	deps.RemoveEngine = func(ctx context.Context, name string, deleteFiles bool) error {
		return removeEngine(ctx, db, dataDir, name, deleteFiles)
	}

}

func catalogContainerDigest(cat *knowledge.Catalog, assetName string) string {
	if cat == nil {
		return ""
	}
	for i := range cat.EngineAssets {
		asset := &cat.EngineAssets[i]
		if !strings.EqualFold(asset.Metadata.Name, assetName) {
			continue
		}
		digest := strings.TrimSpace(asset.Image.Digest)
		if digest != "" && !strings.HasPrefix(strings.ToLower(digest), "sha256:") {
			digest = "sha256:" + digest
		}
		return digest
	}
	return ""
}

func removeEngine(ctx context.Context, store engineRemovalStore, dataDir, id string, deleteFiles bool) error {
	if store == nil {
		return fmt.Errorf("engine removal store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("engine ID is required")
	}
	entry, err := store.GetEngine(ctx, id)
	if err != nil {
		return err
	}
	referenced, err := store.EngineHasReferences(ctx, id)
	if err != nil {
		return fmt.Errorf("check engine %s references: %w", id, err)
	}
	if referenced {
		return fmt.Errorf("engine %s is active or referenced and cannot be removed", id)
	}

	physicalPath := ""
	if deleteFiles {
		physicalPath, err = authorizedEnginePhysicalPath(dataDir, entry)
		if err != nil {
			return err
		}
	}
	snapshot, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode rollback snapshot for engine %s: %w", id, err)
	}
	if err := store.SaveSnapshot(ctx, &state.RollbackSnapshot{
		ToolName: "engine.remove", ResourceType: "engine", ResourceName: id, Snapshot: string(snapshot),
	}); err != nil {
		return fmt.Errorf("save rollback snapshot for engine %s: %w", id, err)
	}
	if physicalPath != "" {
		if err := removeAuthorizedEnginePath(physicalPath); err != nil {
			return fmt.Errorf("remove engine files for %s: %w", id, err)
		}
	}
	if err := store.DeleteEngine(ctx, id); err != nil {
		return err
	}
	return nil
}

func authorizedEnginePhysicalPath(dataDir string, entry *state.Engine) (string, error) {
	if entry == nil {
		return "", fmt.Errorf("engine inventory entry is nil")
	}
	origin := strings.ToLower(strings.TrimSpace(entry.Origin))
	if origin != "managed" && origin != "imported" {
		return "", fmt.Errorf("engine %s origin %q is protected from physical deletion", entry.ID, entry.Origin)
	}
	if entry.RuntimeType != "native" {
		return "", fmt.Errorf("engine %s physical deletion is restricted to native assets stored below AIMA_DATA_DIR", entry.ID)
	}
	target := strings.TrimSpace(entry.BinaryPath)
	if target == "" {
		target = strings.TrimSpace(entry.Location)
	}
	if target == "" {
		return "", fmt.Errorf("engine %s has no canonical filesystem location", entry.ID)
	}
	if !filepath.IsAbs(target) {
		return "", fmt.Errorf("engine %s location %q is not absolute", entry.ID, target)
	}
	rootAbs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve AIMA data directory: %w", err)
	}
	rootCanonical, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve AIMA data directory %s: %w", rootAbs, err)
	}
	targetAbs := filepath.Clean(target)
	if err := requirePathBelow(rootAbs, targetAbs); err != nil {
		return "", fmt.Errorf("engine %s location is outside AIMA_DATA_DIR: %w", entry.ID, err)
	}
	if _, err := os.Lstat(targetAbs); err == nil {
		targetCanonical, resolveErr := filepath.EvalSymlinks(targetAbs)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve engine %s location %s: %w", entry.ID, targetAbs, resolveErr)
		}
		if err := requirePathBelow(rootCanonical, targetCanonical); err != nil {
			return "", fmt.Errorf("engine %s location is outside canonical AIMA_DATA_DIR: %w", entry.ID, err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect engine %s location %s: %w", entry.ID, targetAbs, err)
	}
	return targetAbs, nil
}

func requirePathBelow(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s is not below %s", target, root)
	}
	return nil
}

func removeAuthorizedEnginePath(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func looksLikeNativeEngineBundle(path string) bool {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return true
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"),
		strings.HasSuffix(lower, ".tgz"),
		strings.HasSuffix(lower, ".tar.gz"),
		strings.HasSuffix(lower, ".exe"),
		strings.HasSuffix(lower, ".appimage"):
		return true
	default:
		return false
	}
}

// suppress "imported and not used" for packages only used in type literals
var _ = goruntime.GOOS
