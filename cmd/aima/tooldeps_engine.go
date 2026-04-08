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
		if err := engine.Import(ctx, absPath, &execRunner{}); err != nil {
			return fmt.Errorf("import engine from %s: %w", path, err)
		}
		// Refresh DB: imported image only visible via runtime scan
		_, _ = scanEnginesCore(ctx, "auto", false)
		return nil
	}

	deps.RemoveEngine = func(ctx context.Context, name string, deleteFiles bool) error {
		// Save rollback snapshot before deletion
		e, getErr := db.GetEngine(ctx, name)
		if getErr == nil {
			if snap, snapErr := json.Marshal(e); snapErr == nil {
				_ = db.SaveSnapshot(ctx, &state.RollbackSnapshot{
					ToolName: "engine.remove", ResourceType: "engine", ResourceName: name, Snapshot: string(snap),
				})
			}
		}

		// Optionally clean up actual files/images
		if deleteFiles && e != nil {
			runner := &execRunner{}
			if e.RuntimeType == "native" && e.BinaryPath != "" {
				if rmErr := os.Remove(e.BinaryPath); rmErr != nil && !os.IsNotExist(rmErr) {
					slog.Warn("failed to remove engine binary", "path", e.BinaryPath, "error", rmErr)
				} else {
					slog.Info("removed engine binary", "path", e.BinaryPath)
				}
			} else if e.Image != "" {
				ref := e.Image
				if e.Tag != "" {
					ref += ":" + e.Tag
				}
				// Try docker rmi (best effort)
				if _, err := runner.Run(ctx, "docker", "rmi", ref); err != nil {
					slog.Debug("docker rmi failed (may not be in docker)", "image", ref, "error", err)
				} else {
					slog.Info("removed docker image", "image", ref)
				}
				// Try crictl/k3s rmi (best effort)
				if _, err := runner.Run(ctx, "crictl", "rmi", ref); err == nil {
					slog.Info("removed containerd image via crictl", "image", ref)
				} else if _, err := runner.Run(ctx, "k3s", "crictl", "rmi", ref); err == nil {
					slog.Info("removed containerd image via k3s crictl", "image", ref)
				}
			}
		}

		return db.DeleteEngine(ctx, name)
	}

}

// suppress "imported and not used" for packages only used in type literals
var _ = goruntime.GOOS
