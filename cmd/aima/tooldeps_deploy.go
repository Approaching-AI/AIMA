package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/model"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/runtime"

	state "github.com/jguan/aima/internal"
)

// buildDeployDeps wires deploy.apply, deploy.dry_run, deploy.run, deploy.delete,
// deploy.status, deploy.list, and deploy.logs tools.
//
// pullModelCore and deployRunCore are closures created in buildToolDeps that
// capture shared state (forward-referenced deps pointer, etc). They are passed
// here rather than re-created to preserve the closure chain.
func buildDeployDeps(ac *appContext, deps *mcp.ToolDeps,
	pullModelCore func(ctx context.Context, name string, onStatus func(phase, msg string), onProgress func(downloaded, total int64)) error,
	deployRunCore func(ctx context.Context, model, engineType, slot string, noPull bool,
		onPhase func(phase, msg string), onEngineProgress func(engine.ProgressEvent)) (json.RawMessage, error),
) {
	cat := ac.cat
	db := ac.db
	kStore := ac.kStore
	rt := ac.rt
	nativeRt := ac.nativeRt
	dockerRt := ac.dockerRt
	k3sRt := ac.k3sRt
	proxyServer := ac.proxy
	dataDir := ac.dataDir

	deps.DeployApply = func(ctx context.Context, engineType, modelName, slot string, configOverrides map[string]any) (json.RawMessage, error) {
		allowAutoPull := deployAutoPullAllowed(ctx)
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		rd, err := resolveDeployment(ctx, cat, db, kStore, hwInfo, modelName, engineType, slot, configOverrides, dataDir)
		if err != nil {
			return nil, err
		}
		if !rd.Fit.Fit {
			return nil, fmt.Errorf("hardware check: %s", rd.Fit.Reason)
		}
		for _, w := range rd.Fit.Warnings {
			slog.Warn("deploy fitness", "warning", w)
		}
		modelName = rd.ModelName
		resolved := rd.Resolved

		modelPath := resolved.ModelPath
		if modelPath == "" {
			modelPath = filepath.Join(dataDir, "models", modelName)
		}
		requiredFormat := resolved.ModelFormat
		requiredQuantization := resolvedQuantizationHint(resolved)
		// Guard: if the resolved model path is empty or missing model files,
		// search alternative locations. This handles the case where aima serve
		// runs as root (HOME=/root) but deploy is invoked as a regular user,
		// so $HOME/.aima/models differs from where the model was downloaded.
		if !model.PathLooksCompatible(modelPath, requiredFormat, requiredQuantization) {
			if alt := findModelDir(modelName, dataDir, requiredFormat, requiredQuantization); alt != "" {
				slog.Info("model path fallback: using alternative location",
					"original", modelPath, "resolved", alt)
				modelPath = alt
			} else {
				if !allowAutoPull {
					return nil, fmt.Errorf("model %s not found locally and auto-pull is disabled", modelName)
				}
				slog.Info("model not found locally, auto-pulling", "model", modelName)
				if pullErr := pullModelCore(ctx, modelName, nil, nil); pullErr != nil {
					return nil, fmt.Errorf("auto-pull model %s: %w", modelName, pullErr)
				}
				// Re-resolve model path after download
				modelPath = filepath.Join(dataDir, "models", modelName)
				if alt := findModelDir(modelName, dataDir, requiredFormat, requiredQuantization); alt != "" {
					modelPath = alt
				}
			}
		}
		// Native binary engines require a single model file path; container engines
		// take the directory. Collapse only file-style model directories (GGUF etc.);
		// HuggingFace-style directories with config.json must stay as directories.
		if resolved.Source != nil {
			if fi, err := os.Stat(modelPath); err == nil && fi.IsDir() && dirRequiresSingleFileModelPath(modelPath) {
				if f := findModelFileInDir(modelPath); f != "" {
					modelPath = f
				}
			}
		}

		req := &runtime.DeployRequest{
			Name:             modelName,
			Engine:           resolved.Engine,
			Image:            resolved.EngineImage,
			Command:          resolved.Command,
			PortSpecs:        append([]knowledge.StartupPort(nil), resolved.PortSpecs...),
			InitCommands:     resolved.InitCommands,
			ModelPath:        modelPath,
			Config:           resolved.Config,
			RuntimeClassName: resolved.RuntimeClassName,
			CPUArch:          resolved.CPUArch,
			Env:              resolved.Env,
			WorkDir:          resolved.WorkDir,
			Container:        resolved.Container,
			GPUResourceName:  resolved.GPUResourceName,
			ExtraVolumes:     resolved.ExtraVolumes,
			Labels: map[string]string{
				"aima.dev/engine": resolved.Engine,
				"aima.dev/model":  modelName,
				"aima.dev/slot":   resolved.Slot,
			},
		}
		if resolved.Partition != nil {
			req.Partition = &runtime.PartitionRequest{
				GPUMemoryMiB:    resolved.Partition.GPUMemoryMiB,
				GPUCoresPercent: resolved.Partition.GPUCoresPercent,
				CPUCores:        resolved.Partition.CPUCores,
				RAMMiB:          resolved.Partition.RAMMiB,
			}
		}
		if resolved.HealthCheck != nil {
			req.HealthCheck = &runtime.HealthCheckConfig{
				Path:     resolved.HealthCheck.Path,
				TimeoutS: resolved.HealthCheck.TimeoutS,
			}
		}
		if resolved.Source != nil {
			req.BinarySource = toEngineBinarySource(resolved.Source)
		}
		if resolved.Warmup != nil {
			req.Warmup = &runtime.WarmupConfig{
				Prompt:    resolved.Warmup.Prompt,
				MaxTokens: resolved.Warmup.MaxTokens,
				TimeoutS:  resolved.Warmup.TimeoutS,
			}
		}

		// Select runtime based on engine recommendation and available runtimes.
		// All-zero partition (full device) does not require K3S+HAMi GPU splitting.
		hasPartition := req.Partition != nil && (req.Partition.GPUMemoryMiB > 0 || req.Partition.GPUCoresPercent > 0)
		activeRt, rtErr := pickRuntimeForDeployment(resolved.RuntimeRecommendation, k3sRt, dockerRt, nativeRt, rt, hasPartition)
		if rtErr != nil {
			return nil, rtErr
		}
		deployName := knowledge.SanitizePodName(modelName + "-" + resolved.Engine)
		suppressRecentlyDeleted := loadDeletedDeploymentSuppressor(ctx, db)
		if existing, _ := findDeploymentStatus(ctx, deployName, suppressRecentlyDeleted, activeRt, rt, nativeRt, dockerRt); existing != nil {
			if shouldReuseExistingDeployment(existing, engineType, slot, configOverrides) {
				proxyServer.RegisterBackend(modelName, &proxy.Backend{
					ModelName:  modelName,
					EngineType: resolved.Engine,
					Address:    existing.Address,
					Ready:      existing.Ready,
				})
				runtimeName := activeRt.Name()
				if existing.Runtime != "" {
					runtimeName = existing.Runtime
				}
				status := "deploying"
				if existing.Ready {
					status = "ready"
				}
				result := map[string]any{
					"name":    deployName,
					"model":   modelName,
					"engine":  resolved.Engine,
					"slot":    resolved.Slot,
					"status":  status,
					"phase":   existing.Phase,
					"runtime": runtimeName,
				}
				if existing.Address != "" {
					result["address"] = existing.Address
				}
				return json.Marshal(result)
			}
		}
		// Pre-flight: ensure image is available in containerd for K3S deployments.
		// Auto-import from Docker or pre-pull from registries if needed.
		// Note: containerd operations require root; skip gracefully if not root.
		if activeRt.Name() == "k3s" && req.Image != "" {
			inContainerd := engine.ImageExistsInContainerd(ctx, req.Image, &execRunner{})
			if !inContainerd {
				inDocker := engine.ImageExistsInDocker(ctx, req.Image, &execRunner{})
				if inDocker {
					if shouldFallbackToDockerRuntime(activeRt.Name(), hasPartition, inContainerd, inDocker, os.Getuid() == 0, dockerRt != nil) {
						slog.Info("falling back to Docker runtime because K3S image import requires root", "image", req.Image)
						activeRt = dockerRt
					} else if requiresRootImportForK3S(inContainerd, inDocker, os.Getuid() == 0) {
						return nil, fmt.Errorf("engine image %s is only available in Docker; K3S deployment requires importing it into containerd as root (sudo docker save %s | sudo k3s ctr -n k8s.io images import -)", req.Image, req.Image)
					} else {
						slog.Info("auto-importing image from Docker to containerd", "image", req.Image)
						if importErr := engine.ImportDockerToContainerd(ctx, req.Image, &execRunner{}); importErr != nil {
							slog.Warn("auto-import failed, K3S will try registries.yaml", "image", req.Image, "error", importErr)
						}
					}
				} else if activeRt.Name() == "k3s" && len(resolved.EngineRegistries) > 0 {
					if !allowAutoPull {
						return nil, fmt.Errorf("engine image %s not found in K3S containerd and auto-pull is disabled", req.Image)
					}
					slog.Info("pre-pulling engine image", "image", req.Image, "registries", len(resolved.EngineRegistries))
					imgName, imgTag := splitImageRef(req.Image)
					if pullErr := engine.Pull(ctx, engine.PullOptions{
						Image:          imgName,
						Tag:            imgTag,
						Registries:     resolved.EngineRegistries,
						Runner:         &execRunner{},
						ExpectedDigest: resolved.EngineDigest,
					}); pullErr != nil {
						slog.Warn("pre-pull failed, K3S will try registries.yaml", "image", req.Image, "error", pullErr)
					}
				}
			}
		}
		// Pre-flight: ensure image is available in Docker for Docker deployments.
		if activeRt.Name() == "docker" && req.Image != "" {
			fullRef := req.Image
			if !strings.Contains(fullRef, ":") {
				fullRef += ":latest"
			}
			if !engine.ImageExistsInDocker(ctx, fullRef, &execRunner{}) {
				if len(resolved.EngineRegistries) > 0 {
					if !allowAutoPull {
						return nil, fmt.Errorf("engine image %s not found in Docker and auto-pull is disabled", req.Image)
					}
					slog.Info("auto-pulling engine image for Docker deploy", "image", req.Image)
					imgName, imgTag := splitImageRef(req.Image)
					if pullErr := engine.Pull(ctx, engine.PullOptions{
						Image:          imgName,
						Tag:            imgTag,
						Registries:     resolved.EngineRegistries,
						Runner:         &execRunner{},
						ExpectedDigest: resolved.EngineDigest,
					}); pullErr != nil {
						return nil, fmt.Errorf("auto-pull engine image %s: %w", req.Image, pullErr)
					}
					if aliasErr := ensureDockerImageAlias(ctx, &execRunner{}, req.Image, resolved.EngineRegistries); aliasErr != nil {
						return nil, fmt.Errorf("normalize pulled docker image %s: %w", req.Image, aliasErr)
					}
				} else {
					slog.Warn("engine image not found locally and no registries configured",
						"image", req.Image,
						"hint", "run 'aima engine pull' first or ensure registries are configured in engine YAML")
				}
			}
		}
		compatPlan, compatErr := prepareContainerCompatibility(ctx, &execRunner{}, allowAutoPull, activeRt.Name(), modelPath, resolved)
		if compatErr != nil {
			return nil, compatErr
		}
		if len(compatPlan.RepairInitCommands) > 0 {
			req.InitCommands = append(append([]string(nil), compatPlan.RepairInitCommands...), req.InitCommands...)
		}
		if compatPlan.DockerImageChanged && activeRt.Name() == "k3s" {
			if os.Getuid() == 0 {
				slog.Info("syncing compatibility-validated Docker image into K3S containerd", "image", req.Image)
				if importErr := engine.ImportDockerToContainerd(ctx, req.Image, &execRunner{}); importErr != nil {
					if shouldFallbackToDockerRuntime(activeRt.Name(), hasPartition, false, true, true, dockerRt != nil) {
						slog.Warn("containerd image sync failed, falling back to Docker runtime", "image", req.Image, "error", importErr)
						activeRt = dockerRt
					} else {
						return nil, fmt.Errorf("sync compatibility-validated image %s into K3S containerd: %w", req.Image, importErr)
					}
				}
			} else if shouldFallbackToDockerRuntime(activeRt.Name(), hasPartition, false, true, false, dockerRt != nil) {
				slog.Info("falling back to Docker runtime because compatibility-validated image change cannot be synced into K3S without root", "image", req.Image)
				activeRt = dockerRt
			} else {
				return nil, fmt.Errorf("compatibility validation refreshed %s in Docker, but syncing that image into K3S containerd requires root", req.Image)
			}
		}
		if err := allocateDeploymentPorts(ctx, deployName, activeRt.Name(), req, resolved.Provenance, listAllRuntimes(ctx, rt, nativeRt, dockerRt)); err != nil {
			return nil, fmt.Errorf("allocate ports: %w", err)
		}
		if err := activeRt.Deploy(ctx, req); err != nil {
			return nil, fmt.Errorf("deploy: %w", err)
		}
		proxyServer.RegisterBackend(modelName, &proxy.Backend{
			ModelName:  modelName,
			EngineType: resolved.Engine,
			Ready:      false,
		})
		result := map[string]any{
			"name":  deployName,
			"model": modelName, "engine": resolved.Engine,
			"slot": resolved.Slot, "status": "deploying",
			"runtime": activeRt.Name(),
		}
		return json.Marshal(result)
	}

	deps.DeployDryRun = func(ctx context.Context, engineType, modelName, slot string, overrides map[string]any) (json.RawMessage, error) {
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		rd, err := resolveDeployment(ctx, cat, db, kStore, hwInfo, modelName, engineType, slot, overrides, dataDir)
		if err != nil {
			return nil, err
		}

		// Select runtime for display
		resolved := rd.Resolved
		hasPartition := resolved.Partition != nil && (resolved.Partition.GPUMemoryMiB > 0 || resolved.Partition.GPUCoresPercent > 0)
		selectedRt, rtErr := pickRuntimeForDeployment(resolved.RuntimeRecommendation, k3sRt, dockerRt, nativeRt, rt, hasPartition)
		if rtErr != nil {
			return nil, rtErr
		}
		runtimeName := selectedRt.Name()
		var warnings []string
		warnings = append(warnings, rd.Fit.Warnings...)

		if runtimeName == "k3s" && resolved.EngineImage != "" {
			inContainerd := engine.ImageExistsInContainerd(ctx, resolved.EngineImage, &execRunner{})
			inDocker := engine.ImageExistsInDocker(ctx, resolved.EngineImage, &execRunner{})
			if shouldFallbackToDockerRuntime(runtimeName, hasPartition, inContainerd, inDocker, os.Getuid() == 0, dockerRt != nil) {
				selectedRt = dockerRt
				runtimeName = selectedRt.Name()
				warnings = append(warnings, k3sDockerFallbackWarning(resolved.EngineImage))
			} else if requiresRootImportForK3S(inContainerd, inDocker, os.Getuid() == 0) {
				warnings = append(warnings, k3sDockerImportHint(resolved.EngineImage))
			}
		}

		result := map[string]any{
			"model":        rd.ModelName,
			"engine":       resolved.Engine,
			"engine_image": resolved.EngineImage,
			"slot":         resolved.Slot,
			"runtime":      runtimeName,
			"config":       resolved.Config,
			"ports":        knowledge.ResolvePortBindingsFromSpecs(resolved.PortSpecs, resolved.Config),
			"provenance":   resolved.Provenance,
			"fit_report": map[string]any{
				"fit":         rd.Fit.Fit,
				"reason":      rd.Fit.Reason,
				"warnings":    rd.Fit.Warnings,
				"adjustments": rd.Fit.Adjustments,
			},
		}

		if !rd.Fit.Fit {
			warnings = append(warnings, "WILL NOT DEPLOY: "+rd.Fit.Reason)
		}

		// Time estimates
		if resolved.ColdStartSMax > 0 {
			result["cold_start_s"] = map[string]int{"min": resolved.ColdStartSMin, "max": resolved.ColdStartSMax}
		}
		if resolved.StartupTimeS > 0 {
			result["startup_time_s"] = resolved.StartupTimeS
		}

		// Power estimates
		if resolved.EnginePowerWattsMax > 0 {
			result["engine_power_watts"] = map[string]int{"min": resolved.EnginePowerWattsMin, "max": resolved.EnginePowerWattsMax}
		}

		// Resource estimates (full cost vector)
		resourceEstimate := map[string]any{}
		if resolved.ResourceEstimate != nil {
			if resolved.ResourceEstimate.VRAMMiB > 0 {
				resourceEstimate["vram_mib"] = resolved.ResourceEstimate.VRAMMiB
			}
			if resolved.ResourceEstimate.RAMMiB > 0 {
				resourceEstimate["ram_mib"] = resolved.ResourceEstimate.RAMMiB
			}
			if resolved.ResourceEstimate.CPUCores > 0 {
				resourceEstimate["cpu_cores"] = resolved.ResourceEstimate.CPUCores
			}
			if resolved.ResourceEstimate.DiskMiB > 0 {
				resourceEstimate["disk_mib"] = resolved.ResourceEstimate.DiskMiB
			}
			if resolved.ResourceEstimate.PowerWatts > 0 {
				resourceEstimate["power_watts"] = resolved.ResourceEstimate.PowerWatts
			}
		} else if resolved.EstimatedVRAMMiB > 0 {
			resourceEstimate["vram_mib"] = resolved.EstimatedVRAMMiB
		}
		if resolved.Partition != nil {
			if resolved.Partition.GPUMemoryMiB > 0 {
				resourceEstimate["partition_gpu_memory_mib"] = resolved.Partition.GPUMemoryMiB
			}
			if resolved.Partition.CPUCores > 0 {
				resourceEstimate["partition_cpu_cores"] = resolved.Partition.CPUCores
			}
			if resolved.Partition.RAMMiB > 0 {
				resourceEstimate["partition_ram_mib"] = resolved.Partition.RAMMiB
			}
		}
		if len(resourceEstimate) > 0 {
			result["resource_estimate"] = resourceEstimate
		}

		// Amplifier info
		if resolved.AmplifierScore > 0 {
			result["amplifier_score"] = resolved.AmplifierScore
		}
		if resolved.OffloadPath {
			result["offload_path"] = true
		}

		// Performance reference (K4 -- attach best known perf data)
		perfRef := map[string]any{"source": "unknown"}
		hwKey := hwInfo.HardwareProfile
		if hwKey == "" {
			hwKey = hwInfo.GPUArch
		}
		if golden, goldenBench, err := db.FindGoldenBenchmark(ctx, hwKey, resolved.Engine, rd.ModelName); err == nil && golden != nil && goldenBench != nil {
			perfRef = map[string]any{
				"source":         "benchmark",
				"benchmark_id":   goldenBench.ID,
				"throughput_tps": goldenBench.ThroughputTPS,
				"ttft_ms_p95":    goldenBench.TTFTP95ms,
				"power_watts":    goldenBench.PowerDrawWatts,
			}
		} else if resolved.ResourceEstimate != nil && resolved.ResourceEstimate.PowerWatts > 0 {
			perfRef["source"] = "yaml_estimate"
			perfRef["power_watts"] = resolved.ResourceEstimate.PowerWatts
		}
		result["performance_reference"] = perfRef

		if runtimeName == "k3s" {
			if podYAML, podErr := knowledge.GeneratePod(resolved); podErr == nil {
				result["pod_yaml"] = string(podYAML)
			} else {
				warnings = append(warnings, "pod generation failed: "+podErr.Error())
			}
		}

		if len(warnings) > 0 {
			result["warnings"] = warnings
		}

		return json.Marshal(result)
	}

	deps.DeployDelete = func(ctx context.Context, name string) error {
		// Gap 3: Save rollback snapshot before deletion (capture deployment state)
		if deployments, listErr := rt.List(ctx); listErr == nil {
			for _, d := range deployments {
				if deploymentMatchesQuery(d, name) {
					if snap, snapErr := json.Marshal(d); snapErr == nil {
						_ = db.SaveSnapshot(ctx, &state.RollbackSnapshot{
							ToolName: "deploy.delete", ResourceType: "deployment", ResourceName: d.Name, Snapshot: string(snap),
						})
					}
					break
				}
			}
		}
		deleted := name
		modelKey := ""
		if existing, err := findDeploymentStatus(ctx, name, nil, rt, nativeRt, dockerRt); err == nil && existing != nil {
			deleted = existing.Name
			modelKey = deploymentModelKey(existing)
		}
		// Try exact pod name first, then fall back to searching by model label.
		// Pod names are "<model>-<engine>" (e.g. qwen3-8b-vllm), but users
		// often pass just the model name (e.g. qwen3-8b).
		err := rt.Delete(ctx, name)
		if err != nil {
			// Exact name failed -- search deployments for this model name.
			if deployments, listErr := rt.List(ctx); listErr == nil {
				for _, d := range deployments {
					if deploymentMatchesQuery(d, name) {
						if delErr := rt.Delete(ctx, d.Name); delErr == nil {
							deleted = d.Name
							modelKey = d.Labels["aima.dev/model"]
							err = nil
							break
						}
					}
				}
			}
		}
		if err != nil && nativeRt != nil && nativeRt != rt {
			// Try exact name and model-label search on native runtime.
			err = nativeRt.Delete(ctx, name)
			if err != nil {
				if nativeDeps, nErr := nativeRt.List(ctx); nErr == nil {
					for _, d := range nativeDeps {
						if deploymentMatchesQuery(d, name) {
							if delErr := nativeRt.Delete(ctx, d.Name); delErr == nil {
								deleted = d.Name
								err = nil
								break
							}
						}
					}
				}
			} else {
				deleted = name
			}
		}
		if err != nil && dockerRt != nil && dockerRt != rt {
			err = dockerRt.Delete(ctx, name)
			if err != nil {
				if dockerDeps, dErr := dockerRt.List(ctx); dErr == nil {
					for _, d := range dockerDeps {
						if deploymentMatchesQuery(d, name) {
							if delErr := dockerRt.Delete(ctx, d.Name); delErr == nil {
								deleted = d.Name
								err = nil
								break
							}
						}
					}
				}
			} else {
				deleted = name
			}
		}
		if err != nil {
			return fmt.Errorf("delete deployment %q: %w", name, err)
		}
		if modelKey != "" {
			proxyServer.RemoveBackend(modelKey)
		}
		proxyServer.RemoveBackend(name)
		proxyServer.RemoveBackend(deleted)
		if err := markDeletedDeployments(ctx, db, time.Now(), name, deleted, modelKey); err != nil {
			slog.Warn("record deleted deployment tombstone", "error", err, "name", name, "deleted", deleted)
		}
		return nil
	}

	deps.DeployStatus = func(ctx context.Context, name string) (json.RawMessage, error) {
		suppressRecentlyDeleted := loadDeletedDeploymentSuppressor(ctx, db)
		s, err := findDeploymentStatus(ctx, name, suppressRecentlyDeleted, rt, nativeRt, dockerRt)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	}

	deps.DeployList = func(ctx context.Context) (json.RawMessage, error) {
		statuses, err := rt.List(ctx)
		if err != nil {
			// Primary runtime failed -- still try to collect from other runtimes.
			slog.Warn("deploy list: primary runtime failed", "runtime", rt.Name(), "error", err)
			statuses = make([]*runtime.DeploymentStatus, 0)
		}
		// Also include native deployments (when engine recommended native on a K3S machine).
		if nativeRt != nil && nativeRt != rt {
			if nativeStatuses, nErr := nativeRt.List(ctx); nErr == nil {
				statuses = append(statuses, nativeStatuses...)
			}
		}
		// Also include Docker deployments.
		if dockerRt != nil && dockerRt != rt {
			if dockerStatuses, dErr := dockerRt.List(ctx); dErr == nil {
				statuses = append(statuses, dockerStatuses...)
			}
		}
		suppressRecentlyDeleted := loadDeletedDeploymentSuppressor(ctx, db)
		statuses = filterDeploymentStatuses(statuses, suppressRecentlyDeleted)
		return json.Marshal(statuses)
	}

	deps.DeployRun = deployRunCore

	deps.DeployLogs = func(ctx context.Context, name string, tailLines int) (string, error) {
		logs, err := rt.Logs(ctx, name, tailLines)
		if err != nil && nativeRt != nil && nativeRt != rt {
			logs, err = nativeRt.Logs(ctx, name, tailLines)
		}
		if err != nil && dockerRt != nil && dockerRt != rt {
			logs, err = dockerRt.Logs(ctx, name, tailLines)
		}
		if err != nil {
			// Exact pod name failed -- search by model label across all runtimes.
			allDeps := listAllRuntimes(ctx, rt, nativeRt, dockerRt)
			for _, d := range allDeps {
				if deploymentMatchesQuery(d, name) {
					// Try each runtime for logs by actual deployment name.
					for _, tryRt := range []runtime.Runtime{rt, nativeRt, dockerRt} {
						if tryRt == nil {
							continue
						}
						if l, e := tryRt.Logs(ctx, d.Name, tailLines); e == nil {
							return l, nil
						}
					}
					break
				}
			}
		}
		return logs, err
	}
}
