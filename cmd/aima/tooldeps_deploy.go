package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
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
	deployRunCore func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
		onPhase func(phase, msg string), onEngineProgress func(engine.ProgressEvent), onModelProgress func(downloaded, total int64)) (json.RawMessage, error),
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

	deps.DeployApply = func(ctx context.Context, engineType, modelName, slot string, configOverrides map[string]any, noPull bool) (json.RawMessage, error) {
		if noPull {
			ctx = withDeployAutoPull(ctx, false)
		}
		allowAutoPull := deployAutoPullAllowed(ctx)
		// Internal flag: _auto_pull=false disables model/engine auto-download.
		if v, ok := configOverrides["_auto_pull"]; ok {
			if b, isBool := v.(bool); isBool && !b {
				allowAutoPull = false
			}
			delete(configOverrides, "_auto_pull")
		}
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
		upstreamModel := resolvedServedModelName(modelName, resolved.Config)

		modelPath, modelPathErr := resolveLocalModelPathNoPull(modelName, resolved, dataDir)
		if modelPathErr != nil {
			if !allowAutoPull {
				return nil, modelPathErr
			}
			slog.Info("model not found locally, auto-pulling", "model", modelName)
			if pullErr := pullModelCore(ctx, modelName, nil, nil); pullErr != nil {
				return nil, fmt.Errorf("auto-pull model %s: %w", modelName, pullErr)
			}
			modelPath, modelPathErr = resolveLocalModelPathNoPull(modelName, resolved, dataDir)
			if modelPathErr != nil {
				return nil, modelPathErr
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
				// Label carries the resolved asset metadata.name so the
				// runtime's findEngineAsset lookup (keyed on metadata.name)
				// can gate health_check + warmup. Fall through to the type
				// alias when the resolver has no asset binding.
				"aima.dev/engine":      firstNonEmpty(resolved.EngineAssetName, resolved.Engine),
				"aima.dev/model":       modelName,
				"aima.dev/slot":        resolved.Slot,
				proxy.LabelServedModel: upstreamModel,
			},
		}
		if parameterCount := catalogModelParameterCount(cat, modelName); parameterCount != "" {
			req.Labels[proxy.LabelParameterCount] = parameterCount
		}
		if contextWindow := contextWindowFromResolvedConfig(resolved.Config); contextWindow > 0 {
			req.Labels["aima.dev/context_window"] = strconv.Itoa(contextWindow)
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
					ModelName:           modelName,
					UpstreamModel:       deploymentUpstreamModel(existing, upstreamModel),
					EngineType:          resolved.Engine,
					Address:             existing.Address,
					Ready:               existing.Ready,
					ParameterCount:      firstNonEmpty(existing.Labels[proxy.LabelParameterCount], catalogModelParameterCount(cat, modelName)),
					ContextWindowTokens: firstPositiveInt(contextWindowFromStatus(existing), contextWindowFromResolvedConfig(resolved.Config)),
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
					"config":  resolved.Config,
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
			ModelName:           modelName,
			UpstreamModel:       upstreamModel,
			EngineType:          resolved.Engine,
			Ready:               false,
			ParameterCount:      catalogModelParameterCount(cat, modelName),
			ContextWindowTokens: contextWindowFromResolvedConfig(resolved.Config),
		})
		result := map[string]any{
			"name":  deployName,
			"model": modelName, "engine": resolved.Engine,
			"slot": resolved.Slot, "status": "deploying",
			"runtime": activeRt.Name(),
			"config":  resolved.Config,
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
		if golden, goldenBench, err := db.FindGoldenBenchmark(ctx, hwKey, resolved.Engine, rd.ModelName, "text"); err == nil && golden != nil && goldenBench != nil {
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
		matches := findMatchingDeployments(ctx, name, nil, rt, nativeRt, dockerRt)
		if len(matches) == 0 {
			return fmt.Errorf("deployment %q not found", name)
		}

		for _, match := range matches {
			if match.Status == nil {
				continue
			}
			if snap, snapErr := json.Marshal(match.Status); snapErr == nil {
				_ = db.SaveSnapshot(ctx, &state.RollbackSnapshot{
					ToolName:     "deploy.delete",
					ResourceType: "deployment",
					ResourceName: match.Status.Name,
					Snapshot:     string(snap),
				})
			}
		}

		deletedAt := time.Now()
		tombstoneKeys := []string{name}
		seenKeys := map[string]struct{}{normalizeDeletedDeploymentKey(name): {}}
		rememberKey := func(key string) {
			norm := normalizeDeletedDeploymentKey(key)
			if norm == "" {
				return
			}
			if _, ok := seenKeys[norm]; ok {
				return
			}
			seenKeys[norm] = struct{}{}
			tombstoneKeys = append(tombstoneKeys, key)
		}

		for _, match := range matches {
			if match.Runtime == nil || match.Status == nil {
				continue
			}
			if err := match.Runtime.Delete(ctx, match.Status.Name); err != nil {
				return fmt.Errorf("delete deployment %q on %s: %w", match.Status.Name, match.Runtime.Name(), err)
			}
			rememberKey(match.Status.Name)
			rememberKey(deploymentModelKey(match.Status))
		}

		if remaining := findMatchingDeployments(ctx, name, nil, rt, nativeRt, dockerRt); len(remaining) > 0 {
			return fmt.Errorf("delete deployment %q: deployment still active after delete (%s)", name, summarizeMatchedDeployments(remaining))
		}

		for _, key := range tombstoneKeys {
			proxyServer.RemoveBackend(key)
		}
		if err := markDeletedDeployments(ctx, db, deletedAt, tombstoneKeys...); err != nil {
			slog.Warn("record deleted deployment tombstone", "error", err, "name", name, "keys", tombstoneKeys)
		}
		return nil
	}

	deps.DeployStatus = func(ctx context.Context, name string) (json.RawMessage, error) {
		suppressRecentlyDeleted := loadDeletedDeploymentSuppressor(ctx, db)
		s, err := findDeploymentStatus(ctx, name, suppressRecentlyDeleted, rt, nativeRt, dockerRt)
		if err != nil {
			return nil, err
		}
		populateDeploymentOverviewFields(s)
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
		overviews := make([]deploymentOverview, 0, len(statuses))
		for _, status := range statuses {
			overviews = append(overviews, deploymentOverviewFromStatus(status))
		}
		return json.Marshal(overviews)
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

func catalogModelParameterCount(cat *knowledge.Catalog, name string) string {
	if cat == nil {
		return ""
	}
	for _, model := range cat.ModelAssets {
		if strings.EqualFold(model.Metadata.Name, name) {
			return strings.TrimSpace(model.Metadata.ParameterCount)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func populateDeploymentOverviewFields(status *runtime.DeploymentStatus) {
	if status == nil {
		return
	}
	status.Model = firstNonEmpty(
		status.Model,
		status.Labels["aima.dev/model"],
		status.Name,
	)
	status.Engine = firstNonEmpty(
		status.Engine,
		status.Labels["aima.dev/engine"],
	)
	status.Slot = firstNonEmpty(
		status.Slot,
		status.Labels["aima.dev/slot"],
	)
}

type deploymentOverview struct {
	Name                string `json:"name"`
	Model               string `json:"model"`
	Engine              string `json:"engine,omitempty"`
	Slot                string `json:"slot,omitempty"`
	Phase               string `json:"phase"`
	Status              string `json:"status"`
	Ready               bool   `json:"ready"`
	Address             string `json:"address,omitempty"`
	Runtime             string `json:"runtime"`
	StartTime           string `json:"start_time,omitempty"`
	StartedAtUnix       int64  `json:"started_at_unix,omitempty"`
	Message             string `json:"message,omitempty"`
	Restarts            int    `json:"restarts,omitempty"`
	ExitCode            *int   `json:"exit_code,omitempty"`
	StartupPhase        string `json:"startup_phase,omitempty"`
	StartupProgress     int    `json:"startup_progress,omitempty"`
	StartupMessage      string `json:"startup_message,omitempty"`
	EstimatedTotalS     int    `json:"estimated_total_s,omitempty"`
	ErrorLines          string `json:"error_lines,omitempty"`
	ServedModel         string `json:"served_model,omitempty"`
	ParameterCount      string `json:"parameter_count,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
}

func deploymentOverviewFromStatus(status *runtime.DeploymentStatus) deploymentOverview {
	populateDeploymentOverviewFields(status)
	if status == nil {
		return deploymentOverview{}
	}
	return deploymentOverview{
		Name:                status.Name,
		Model:               status.Model,
		Engine:              status.Engine,
		Slot:                status.Slot,
		Phase:               status.Phase,
		Status:              status.Phase,
		Ready:               status.Ready,
		Address:             status.Address,
		Runtime:             status.Runtime,
		StartTime:           status.StartTime,
		StartedAtUnix:       status.StartedAtUnix,
		Message:             status.Message,
		Restarts:            status.Restarts,
		ExitCode:            status.ExitCode,
		StartupPhase:        status.StartupPhase,
		StartupProgress:     status.StartupProgress,
		StartupMessage:      status.StartupMessage,
		EstimatedTotalS:     status.EstimatedTotalS,
		ErrorLines:          status.ErrorLines,
		ServedModel:         deploymentUpstreamModel(status, ""),
		ParameterCount:      firstNonEmpty(status.Labels[proxy.LabelParameterCount]),
		ContextWindowTokens: contextWindowFromStatus(status),
	}
}

func contextWindowFromResolvedConfig(config map[string]any) int {
	if len(config) == 0 {
		return 0
	}
	switch value := config["ctx_size"].(type) {
	case int:
		if value > 0 {
			return value
		}
	case int32:
		if value > 0 {
			return int(value)
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return int(value)
		}
	case json.Number:
		if parsed, err := value.Int64(); err == nil && parsed > 0 {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func contextWindowFromStatus(status *runtime.DeploymentStatus) int {
	if status == nil {
		return 0
	}
	raw := strings.TrimSpace(status.Labels["aima.dev/context_window"])
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func resolvedServedModelName(modelName string, config map[string]any) string {
	if config != nil {
		if raw, ok := config["served_model_name"].(string); ok {
			return normalizeServedModelName(modelName, raw)
		}
	}
	return modelName
}

func deploymentUpstreamModel(ds *runtime.DeploymentStatus, fallback string) string {
	if ds != nil && ds.Labels != nil {
		if served := normalizeServedModelName("", ds.Labels[proxy.LabelServedModel]); served != "" {
			return served
		}
	}
	if fallback != "" {
		return fallback
	}
	if ds != nil && ds.Labels != nil {
		if model := strings.TrimSpace(ds.Labels["aima.dev/model"]); model != "" {
			return model
		}
	}
	return ""
}

func normalizeServedModelName(modelName, raw string) string {
	served := strings.TrimSpace(raw)
	if served == "" {
		return modelName
	}
	if modelName != "" {
		served = strings.ReplaceAll(served, "{{.ModelName}}", modelName)
	}
	served = strings.TrimSpace(served)
	if served == "" || strings.Contains(served, "{{") || strings.Contains(served, "}}") {
		return modelName
	}
	return served
}
