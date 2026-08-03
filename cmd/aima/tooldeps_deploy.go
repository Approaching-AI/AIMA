package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/model"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/recovery"
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
	deploymentLocks := newDeploymentOperationLocks()

	deps.DeployApply = func(ctx context.Context, engineType, modelName, slot string, configOverrides map[string]any, noPull bool, recoveryPolicy recovery.PolicyPatch) (json.RawMessage, error) {
		// A1: keep the user's original input before it's canonicalized below, so the
		// result can surface the original↔canonical mapping (requested_model).
		requestedModel := modelName
		var pinnedIntent *recovery.Intent
		if recovery.IsReconcilerSource(ctx) {
			claim, ok := recovery.ReconcilerClaim(ctx)
			if !ok {
				return nil, fmt.Errorf("reconciler deployment apply requires a committed claim")
			}
			if claim.Model != modelName {
				return nil, fmt.Errorf("reconciler model drift for deployment %q: pinned %q, requested %q", claim.Name, claim.Model, modelName)
			}
			pinnedIntent = &claim
		}
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
		// Verbatim model identity: keep `modelName` as the EXACT name passed to
		// `aima deploy` — that is the published identity used by the proxy route,
		// /v1/models, the aima.dev/model label, llm.model, and openclaw. Do NOT
		// canonicalize it. `canonicalName` is the catalog-canonical id, used ONLY
		// for catalog/path/pull lookups that are not alias-aware.
		canonicalName := rd.ModelName
		resolved := rd.Resolved
		engineAsset, err := resolvedEngineAsset(cat, resolved.EngineAssetName)
		if err != nil {
			return nil, err
		}
		resolvedRecoveryPolicy := recovery.Policy{}
		if pinnedIntent != nil {
			if pinnedIntent.EngineAsset != resolved.EngineAssetName {
				return nil, fmt.Errorf("reconciler engine asset drift for deployment %q: pinned %q, resolved %q", pinnedIntent.Name, pinnedIntent.EngineAsset, resolved.EngineAssetName)
			}
			if currentVersion := engineAssetVersion(engineAsset); pinnedIntent.EngineVersion != currentVersion {
				return nil, fmt.Errorf("reconciler engine version drift for deployment %q: pinned %q, resolved %q", pinnedIntent.Name, pinnedIntent.EngineVersion, currentVersion)
			}
			resolvedRecoveryPolicy = pinnedIntent.Policy
		} else {
			resolvedRecoveryPolicy, err = recovery.ResolvePolicy(
				recovery.DefaultPolicy(),
				engineAssetRecoveryPatch(engineAsset),
				recoveryPolicy,
			)
			if err != nil {
				return nil, fmt.Errorf("resolve recovery policy: %w", err)
			}
		}
		upstreamModel := resolvedServedModelName(modelName, resolved.Config)
		modelType := firstNonEmpty(resolved.ModelType, catalogModelType(cat, canonicalName))

		modelPath, modelPathErr := resolveLocalModelPathNoPull(canonicalName, resolved, dataDir)
		if modelPathErr != nil {
			if !allowAutoPull {
				return nil, modelPathErr
			}
			slog.Info("model not found locally, auto-pulling", "model", canonicalName)
			if pullErr := pullModelCore(ctx, canonicalName, nil, nil); pullErr != nil {
				return nil, fmt.Errorf("auto-pull model %s: %w", canonicalName, pullErr)
			}
			modelPath, modelPathErr = resolveLocalModelPathNoPull(canonicalName, resolved, dataDir)
			if modelPathErr != nil {
				return nil, modelPathErr
			}
		}

		// Auto-wire the multimodal projector for llama.cpp VL models. A GGUF vision
		// model ships a co-located mmproj-*.gguf, and llama-server needs --mmproj to
		// accept images. If the caller didn't set it, inject the projector path so
		// vision works zero-config (it flows through configToFlags as --mmproj).
		if resolved.ModelFormat == "gguf" && strings.HasPrefix(strings.ToLower(resolved.Engine), "llamacpp") {
			if _, set := resolved.Config["mmproj"]; !set {
				if mm := findColocatedMMProj(modelPath); mm != "" {
					if resolved.Config == nil {
						resolved.Config = map[string]any{}
					}
					resolved.Config["mmproj"] = mm
					slog.Info("auto-wired multimodal projector for vision", "model", modelName, "mmproj", mm)
				}
			}

			// Hardware-aware context sizing: a high catalog/user ctx_size can exceed
			// what the detected memory holds — llama-server would OOM at load. Clamp
			// ctx_size down to fit weights + projector + KV cache (and cap at the
			// model's trained context), degrading gracefully instead of failing.
			if clamped, oldCtx, newCtx, reason := fitContextToMemory(modelPath, resolved.Config, hwInfo); clamped {
				slog.Warn("clamped context window to fit hardware memory",
					"model", modelName, "ctx_size_requested", oldCtx, "ctx_size_applied", newCtx, "detail", reason)
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
			ModelType:        modelType,
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
		if parameterCount := catalogModelParameterCount(cat, canonicalName); parameterCount != "" {
			req.Labels[proxy.LabelParameterCount] = parameterCount
		}
		if modelType != "" {
			req.Labels[proxy.LabelModelType] = modelType
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
		if err := requirePinnedRuntime(pinnedIntent, activeRt); err != nil {
			return nil, err
		}
		deployName := knowledge.SanitizePodName(modelName + "-" + resolved.Engine)
		operationNames := deploymentApplyLockNames(cat, modelName, canonicalName, deployName, deploymentIntentName(req, activeRt.Name()), pinnedIntent)
		var existing *runtime.DeploymentStatus
		var unlockDeployment func()
		for {
			unlockDeployment = deploymentLocks.lock(operationNames...)
			intents, intentErr := listDeploymentIntents(ctx, db)
			if intentErr != nil {
				unlockDeployment()
				return nil, intentErr
			}
			suppressRecentlyDeleted := loadDeletedDeploymentSuppressor(ctx, db)
			searchRuntimes := []runtime.Runtime{activeRt, rt, nativeRt, dockerRt, k3sRt}
			if pinnedIntent != nil {
				searchRuntimes = []runtime.Runtime{activeRt}
			}
			existing, err = findReusableDeployment(ctx, deployName, modelName, engineType, slot, configOverrides, suppressRecentlyDeleted, searchRuntimes...)
			if err != nil {
				unlockDeployment()
				return nil, err
			}
			discoveredNames := mergeDeploymentOperationLockNames(
				operationNames,
				deploymentIntentLockNames(cat, matchingDeploymentIntentsForOperation(cat, intents, modelName, false)...),
				deploymentStatusLockNames(cat, existing),
			)
			if hasAllDeploymentOperationLockNames(operationNames, discoveredNames) {
				break
			}
			unlockDeployment()
			operationNames = discoveredNames
		}
		defer unlockDeployment()
		if pinnedIntent != nil {
			if err := validateDeploymentRecoveryClaim(ctx, db, *pinnedIntent, recovery.StateRecovering); err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			status, exists, observeErr := observeExactDeploymentOnRuntime(ctx, pinnedIntent.Name, activeRt)
			if observeErr != nil {
				return nil, observeErr
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if exists && (status.Ready || isNativeDeploymentStarting(activeRt, status)) {
				existing = status
			} else if exists {
				if activeRt.Name() != "native" {
					return nil, fmt.Errorf("reconciler apply is unsupported for runtime %q", activeRt.Name())
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if err := activeRt.Delete(ctx, pinnedIntent.Name); err != nil {
					return nil, fmt.Errorf("delete unhealthy native deployment %q: %w", pinnedIntent.Name, err)
				}
				_, stillExists, confirmErr := observeExactDeploymentOnRuntime(ctx, pinnedIntent.Name, activeRt)
				if confirmErr != nil {
					return nil, fmt.Errorf("verify unhealthy native deployment %q deletion: %w", pinnedIntent.Name, confirmErr)
				}
				if stillExists {
					return nil, fmt.Errorf("unhealthy native deployment %q still exists after delete", pinnedIntent.Name)
				}
			}
		}
		if existing != nil {
			runtimeName := activeRt.Name()
			if existing.Runtime != "" {
				runtimeName = existing.Runtime
			}
			existingName := firstNonEmpty(existing.Name, deployName)
			if pinnedIntent != nil && existingName != pinnedIntent.Name {
				return nil, fmt.Errorf("reconciler deployment name drift for deployment %q: resolved %q", pinnedIntent.Name, existingName)
			}
			if err := persistDeploymentIntent(ctx, db, deploymentIntentSpec{
				Name:          existingName,
				Model:         modelName,
				EngineAsset:   resolved.EngineAssetName,
				EngineVersion: engineAssetVersion(engineAsset),
				Slot:          resolved.Slot,
				Runtime:       runtimeName,
				Config:        resolved.Config,
				Policy:        resolvedRecoveryPolicy,
			}, pinnedIntent); err != nil {
				return nil, err
			}
			proxyServer.RegisterBackend(modelName, &proxy.Backend{
				ModelName:           modelName,
				UpstreamModel:       deploymentUpstreamModel(existing, upstreamModel),
				EngineType:          resolved.Engine,
				ModelType:           modelType,
				Address:             existing.Address,
				Ready:               existing.Ready,
				ParameterCount:      firstNonEmpty(existing.Labels[proxy.LabelParameterCount], catalogModelParameterCount(cat, canonicalName)),
				ContextWindowTokens: firstPositiveInt(contextWindowFromStatus(existing), contextWindowFromResolvedConfig(resolved.Config)),
			})
			status := "deploying"
			if existing.Ready {
				status = "ready"
			}
			result := map[string]any{
				"name":            existingName,
				"model":           modelName,
				"requested_model": requestedModel,
				"engine":          resolved.Engine,
				"slot":            resolved.Slot,
				"status":          status,
				"phase":           existing.Phase,
				"runtime":         runtimeName,
				"config":          resolved.Config,
				"reused":          true,
				"message":         fmt.Sprintf("deployment %s already exists; returning current deployment", existingName),
			}
			if existing.Address != "" {
				result["address"] = existing.Address
			}
			if err := setActiveLLMModelConfigForType(ctx, db, modelName, modelType); err != nil {
				return nil, err
			}
			return json.Marshal(result)
		}
		if requiresNativeLlamaMemoryPreflight(activeRt.Name(), resolved.ModelFormat, resolved.Engine) {
			// Reducing ctx_size only reduces KV cache; it cannot make oversized model
			// weights or a multimodal projector fit in memory. This check runs after
			// reusable deployments are returned so it cannot reject a healthy service.
			if err := ensureLlamaMinimumMemoryFit(modelPath, resolved.Config, hwInfo); err != nil {
				return nil, newDeploymentRunError(deployErrorOutOfMemory, err.Error(), deploymentCleanupResult{})
			}
		}
		// Pre-flight: ensure image is available in containerd for K3S deployments.
		// Auto-import from Docker or pre-pull from registries if needed.
		// Note: containerd operations require root; skip gracefully if not root.
		if activeRt.Name() == "k3s" && req.Image != "" {
			engineRegistries := engineRegistriesWithEnv(resolved.EngineRegistries)
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
				} else if activeRt.Name() == "k3s" && len(engineRegistries) > 0 {
					if !allowAutoPull {
						return nil, fmt.Errorf("engine image %s not found in K3S containerd and auto-pull is disabled", req.Image)
					}
					slog.Info("pre-pulling engine image", "image", req.Image, "registries", len(engineRegistries))
					imgName, imgTag := splitImageRef(req.Image)
					if pullErr := engine.Pull(ctx, engine.PullOptions{
						Image:          imgName,
						Tag:            imgTag,
						Registries:     engineRegistries,
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
				engineRegistries := engineRegistriesWithEnv(resolved.EngineRegistries)
				if len(engineRegistries) > 0 {
					if !allowAutoPull {
						return nil, fmt.Errorf("engine image %s not found in Docker and auto-pull is disabled", req.Image)
					}
					slog.Info("auto-pulling engine image for Docker deploy", "image", req.Image)
					imgName, imgTag := splitImageRef(req.Image)
					if pullErr := engine.Pull(ctx, engine.PullOptions{
						Image:          imgName,
						Tag:            imgTag,
						Registries:     engineRegistries,
						Runner:         &execRunner{},
						ExpectedDigest: resolved.EngineDigest,
					}); pullErr != nil {
						return nil, fmt.Errorf("auto-pull engine image %s: %w", req.Image, pullErr)
					}
					if aliasErr := ensureDockerImageAlias(ctx, &execRunner{}, req.Image, engineRegistries); aliasErr != nil {
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
		if err := requirePinnedRuntime(pinnedIntent, activeRt); err != nil {
			return nil, err
		}
		releasePorts, err := allocateDeploymentPorts(ctx, deployName, activeRt.Name(), req, resolved.Provenance, listAllRuntimes(ctx, rt, nativeRt, dockerRt))
		if err != nil {
			return nil, fmt.Errorf("allocate ports: %w", err)
		}
		intentName := deploymentIntentName(req, activeRt.Name())
		if pinnedIntent != nil {
			if intentName != pinnedIntent.Name {
				releasePorts()
				return nil, fmt.Errorf("reconciler deployment name drift for deployment %q: resolved %q", pinnedIntent.Name, intentName)
			}
			intentName = pinnedIntent.Name
		}
		if err := persistDeploymentIntent(ctx, db, deploymentIntentSpec{
			Name:          intentName,
			Model:         modelName,
			EngineAsset:   resolved.EngineAssetName,
			EngineVersion: engineAssetVersion(engineAsset),
			Slot:          resolved.Slot,
			Runtime:       activeRt.Name(),
			Config:        resolved.Config,
			Policy:        resolvedRecoveryPolicy,
		}, pinnedIntent); err != nil {
			releasePorts()
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			releasePorts()
			return nil, err
		}
		deployErr := activeRt.Deploy(ctx, req)
		// Deploy persists deployment metadata (including host-port labels)
		// synchronously before returning, so reservedHostPorts() now covers the
		// chosen ports. Release the in-flight reservation regardless of outcome.
		releasePorts()
		if deployErr != nil {
			return nil, fmt.Errorf("deploy: %w", deployErr)
		}
		proxyServer.RegisterBackend(modelName, &proxy.Backend{
			ModelName:           modelName,
			UpstreamModel:       upstreamModel,
			EngineType:          resolved.Engine,
			ModelType:           modelType,
			Ready:               false,
			ParameterCount:      catalogModelParameterCount(cat, canonicalName),
			ContextWindowTokens: contextWindowFromResolvedConfig(resolved.Config),
		})
		if err := setActiveLLMModelConfigForType(ctx, db, modelName, modelType); err != nil {
			return nil, err
		}
		result := map[string]any{
			// Return the name the runtime actually registered the deployment under
			// (req.Name == modelName), NOT the sanitized pod name. The readiness
			// poller (deployRunCore.waitForDeployment) and the UI look up status by
			// this name; the native runtime keys deployments by req.Name, so the
			// sanitized deployName never matched → status was never found → the deploy
			// looked stuck "not ready" even though the engine was serving fine.
			"name":  req.Name,
			"model": modelName, "requested_model": requestedModel, "engine": resolved.Engine,
			"slot": resolved.Slot, "status": "deploying",
			"runtime": activeRt.Name(),
			"config":  resolved.Config,
		}
		return json.Marshal(result)
	}

	deps.RecoveryObserve = func(ctx context.Context, intent recovery.Intent) (recovery.Observation, error) {
		intentRuntime := runtimeByName(intent.Runtime, rt, nativeRt, dockerRt, k3sRt)
		if intentRuntime == nil {
			return recovery.Observation{}, fmt.Errorf("pinned runtime %q is unavailable for deployment %q", intent.Runtime, intent.Name)
		}
		status, exists, err := observeExactDeploymentOnRuntime(ctx, intent.Name, intentRuntime)
		if err != nil {
			return recovery.Observation{}, err
		}
		if !exists {
			return recovery.Observation{Exists: false}, nil
		}
		return recoveryObservationFromStatus(status), nil
	}
	deps.RecoveryApply = func(ctx context.Context, intent recovery.Intent) error {
		claim, ok := recovery.ReconcilerClaim(ctx)
		if !ok || !reflect.DeepEqual(claim, intent) {
			return fmt.Errorf("recovery apply requires the exact committed claim for deployment %q", intent.Name)
		}
		_, err := deps.DeployApply(ctx, intent.EngineAsset, intent.Model, intent.Slot, intent.Config, true, recovery.PolicyPatch{})
		return err
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
			"model":                rd.ModelName,
			"requested_model":      modelName,
			"engine":               resolved.Engine,
			"engine_image":         resolved.EngineImage,
			"slot":                 resolved.Slot,
			"runtime":              runtimeName,
			"config":               resolved.Config,
			"resolved_config":      rd.ResolvedConfig,
			"effective_config":     resolved.Config,
			"fit_adjustments":      rd.Fit.Adjustments,
			"ports":                knowledge.ResolvePortBindingsFromSpecs(resolved.PortSpecs, resolved.Config),
			"provenance":           resolved.Provenance,
			"resolved_provenance":  rd.ResolvedProvenance,
			"effective_provenance": resolved.Provenance,
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
		name = strings.TrimSpace(name)
		operationNames := deploymentCatalogLockNames(cat, name)
		var intents []*recovery.Intent
		var matches []matchedDeployment
		var intentMatches []*recovery.Intent
		var strictDiscoveryErr error
		var unlockDeployments func()
		for {
			unlockDeployments = deploymentLocks.lock(operationNames...)
			var err error
			intents, err = listDeploymentIntents(ctx, db)
			if err != nil {
				unlockDeployments()
				return err
			}
			matches = findExactDeploymentNameMatches(ctx, name, nil, rt, nativeRt, dockerRt)
			intentMatches = matchingDeploymentIntentsForOperation(cat, intents, name, true)
			if len(matches) == 0 && len(intentMatches) == 0 {
				matches = findMatchingDeployments(ctx, name, nil, rt, nativeRt, dockerRt)
				intentMatches = matchingDeploymentIntentsForOperation(cat, intents, name, false)
			}
			if len(matches) == 0 && len(intentMatches) == 0 {
				// Backward-compatible fallback: some UI paths pass the model name
				// instead of the concrete deployment name.
				suppressRecentlyDeleted := loadDeletedDeploymentSuppressor(ctx, db)
				modelStatus, statusErr := findDeploymentStatus(ctx, name, suppressRecentlyDeleted, rt, nativeRt, dockerRt)
				if statusErr == nil && modelStatus != nil && modelStatus.Name != "" {
					matches = findMatchingDeployments(ctx, modelStatus.Name, nil, rt, nativeRt, dockerRt)
				}
			}
			if len(matches) == 0 && len(intentMatches) == 0 {
				// A1: deploy canonicalizes the model name (e.g. the alias
				// "Qwen2.5-VL-3B-Instruct-Q4_K_M" deploys as "qwen2.5-vl-3b-instruct"),
				// but undeploy with the original alias would not match. Canonicalize the
				// query and retry so alias-deploy → alias-undeploy works.
				if canonical := canonicalModelAlt(cat, name); canonical != "" {
					matches = findExactDeploymentNameMatches(ctx, canonical, nil, rt, nativeRt, dockerRt)
					intentMatches = matchingDeploymentIntentsForOperation(cat, intents, canonical, true)
					if len(matches) == 0 && len(intentMatches) == 0 {
						matches = findMatchingDeployments(ctx, canonical, nil, rt, nativeRt, dockerRt)
						intentMatches = matchingDeploymentIntentsForOperation(cat, intents, canonical, false)
					}
				}
			}
			if len(matches) == 0 {
				// A Catalog alias may be present in the runtime label while the query
				// resolves to the canonical model (or vice versa). Search all explicit
				// Catalog spellings so an existing runtime is not left behind when an
				// intent match already made the delete target visible.
				matches = findMatchingDeploymentsForCatalog(ctx, cat, name, nil, rt, nativeRt, dockerRt)
			}
			for _, match := range matches {
				if match.Status == nil {
					continue
				}
				if intent := deploymentIntentByExactName(intents, match.Status.Name); intent != nil {
					intentMatches = appendUniqueDeploymentIntents(intentMatches, intent)
				}
			}
			var strictDiscoveryFailures []error
			for _, intent := range intentMatches {
				if intent == nil || strings.TrimSpace(intent.Runtime) == "" {
					continue
				}
				intentRuntime := runtimeByName(intent.Runtime, rt, nativeRt, dockerRt, k3sRt)
				if intentRuntime == nil {
					strictDiscoveryFailures = append(strictDiscoveryFailures, fmt.Errorf("pinned runtime %q is unavailable for deployment %q", intent.Runtime, intent.Name))
					continue
				}
				status, exists, observeErr := observeExactDeploymentOnRuntime(ctx, intent.Name, intentRuntime)
				if exists && status != nil {
					matches = appendUniqueMatchedDeployments(matches, matchedDeployment{Runtime: intentRuntime, Status: status})
					continue
				}
				if observeErr != nil {
					strictDiscoveryFailures = append(strictDiscoveryFailures, observeErr)
				}
			}
			strictDiscoveryErr = errors.Join(strictDiscoveryFailures...)
			discoveredNames := mergeDeploymentOperationLockNames(
				operationNames,
				deploymentIntentLockNames(cat, intentMatches...),
				deploymentMatchedStatusLockNames(cat, matches...),
			)
			if hasAllDeploymentOperationLockNames(operationNames, discoveredNames) {
				break
			}
			unlockDeployments()
			operationNames = discoveredNames
		}
		defer unlockDeployments()
		if len(matches) == 0 && len(intentMatches) == 0 {
			return fmt.Errorf("deployment %q not found", name)
		}
		if len(matches) > 1 {
			return fmt.Errorf("deployment %q is ambiguous; matches: %s; use an exact deployment name", name, summarizeMatchedDeployments(matches))
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
		for _, intent := range intentMatches {
			if err := db.StopDeploymentIntent(ctx, intent.Name); err != nil {
				return err
			}
		}
		if strictDiscoveryErr != nil {
			return fmt.Errorf("deployment %q stopped, but runtime cleanup could not be confirmed: %w", name, strictDiscoveryErr)
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
		for _, intent := range intentMatches {
			rememberKey(intent.Name)
			rememberKey(intent.Model)
		}

		for _, match := range matches {
			if match.Runtime == nil || match.Status == nil {
				continue
			}
			if err := match.Runtime.Delete(ctx, match.Status.Name); err != nil {
				return fmt.Errorf("delete deployment %q on %s: %w", match.Status.Name, match.Runtime.Name(), err)
			}
			rememberKey(match.Status.Name)
			modelKey := deploymentModelKey(match.Status)
			rememberKey(modelKey)
			_, exists, confirmErr := observeExactDeploymentOnRuntime(ctx, match.Status.Name, match.Runtime)
			if confirmErr != nil && !exists {
				return fmt.Errorf("verify deletion of deployment %q on %s: %w", match.Status.Name, match.Runtime.Name(), confirmErr)
			}
			if exists {
				return fmt.Errorf("delete deployment %q: deployment still active after delete (%s/%s)", name, match.Runtime.Name(), match.Status.Name)
			}
		}

		for _, key := range tombstoneKeys {
			proxyServer.RemoveBackend(key)
		}
		if err := markDeletedDeployments(ctx, db, deletedAt, tombstoneKeys...); err != nil {
			slog.Warn("record deleted deployment tombstone", "error", err, "name", name, "keys", tombstoneKeys)
		}
		return nil
	}

	deps.RecoveryDelete = func(ctx context.Context, intent recovery.Intent) error {
		claim, ok := recovery.ReconcilerClaim(ctx)
		if !ok || !reflect.DeepEqual(claim, intent) {
			return fmt.Errorf("recovery delete requires the exact committed claim for deployment %q", intent.Name)
		}
		intentRuntime := runtimeByName(claim.Runtime, rt, nativeRt, dockerRt, k3sRt)
		if intentRuntime == nil {
			return fmt.Errorf("pinned runtime %q is unavailable for deployment %q", claim.Runtime, claim.Name)
		}
		operationNames := mergeDeploymentOperationLockNames(
			deploymentCatalogLockNames(cat, claim.Name),
			deploymentCatalogLockNames(cat, claim.Model),
			deploymentIntentLockNames(cat, &claim),
		)
		unlock := deploymentLocks.lock(operationNames...)
		defer unlock()
		if err := validateDeploymentRecoveryClaim(ctx, db, claim, recovery.StateQuarantined); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, exists, observeErr := observeExactDeploymentOnRuntime(ctx, claim.Name, intentRuntime)
		if observeErr != nil && !exists {
			return observeErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !exists {
			proxyServer.RemoveBackend(claim.Model)
			proxyServer.RemoveBackend(claim.Name)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := intentRuntime.Delete(ctx, claim.Name); err != nil {
			return fmt.Errorf("delete quarantined deployment %q on %s: %w", claim.Name, intentRuntime.Name(), err)
		}
		_, stillExists, confirmErr := observeExactDeploymentOnRuntime(ctx, claim.Name, intentRuntime)
		if confirmErr != nil {
			return fmt.Errorf("verify quarantined deployment %q deletion: %w", claim.Name, confirmErr)
		}
		if stillExists {
			return fmt.Errorf("quarantined deployment %q still exists after delete", claim.Name)
		}
		proxyServer.RemoveBackend(claim.Model)
		proxyServer.RemoveBackend(claim.Name)
		return nil
	}

	deps.DeployStatus = func(ctx context.Context, name string) (json.RawMessage, error) {
		intents, intentErr := listDeploymentIntents(ctx, db)
		if intentErr != nil {
			return nil, intentErr
		}
		suppressRecentlyDeleted := loadDeletedDeploymentSuppressor(ctx, db)
		s, err := findDeploymentStatus(ctx, name, suppressRecentlyDeleted, rt, nativeRt, dockerRt)
		if err != nil {
			// A1: retry with the canonical name so `status <alias>` works too.
			if canonical := canonicalModelAlt(cat, name); canonical != "" {
				if s2, err2 := findDeploymentStatus(ctx, canonical, suppressRecentlyDeleted, rt, nativeRt, dockerRt); err2 == nil {
					s, err = s2, nil
				}
			}
		}
		if err != nil {
			intent, matchErr := quarantinedIntentForQuery(intents, name)
			if matchErr != nil {
				return nil, matchErr
			}
			if intent == nil {
				if canonical := canonicalModelAlt(cat, name); canonical != "" {
					intent, matchErr = quarantinedIntentForQuery(intents, canonical)
					if matchErr != nil {
						return nil, matchErr
					}
				}
			}
			if intent == nil {
				return nil, err
			}
			return json.Marshal(synthesizedQuarantinedStatus(intent))
		}
		populateDeploymentOverviewFields(s)
		enrichDeploymentStatusWithIntent(s, deploymentIntentByExactName(intents, s.Name))
		return json.Marshal(s)
	}

	deps.DeployList = func(ctx context.Context) (json.RawMessage, error) {
		intents, err := listDeploymentIntents(ctx, db)
		if err != nil {
			return nil, err
		}
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
		overviews := make([]deploymentOverview, 0, len(statuses)+len(intents))
		actualNames := make(map[string]struct{}, len(statuses))
		for _, status := range statuses {
			if status == nil {
				continue
			}
			intent := deploymentIntentByExactName(intents, status.Name)
			if intent != nil && intent.DesiredState == recovery.DesiredStopped {
				continue
			}
			actualNames[status.Name] = struct{}{}
			enrichDeploymentStatusWithIntent(status, intent)
			overviews = append(overviews, deploymentOverviewFromStatus(status, cat))
		}
		for _, intent := range intents {
			if intent == nil || intent.DesiredState != recovery.DesiredRunning || intent.RecoveryState != recovery.StateQuarantined {
				continue
			}
			if _, exists := actualNames[intent.Name]; exists {
				continue
			}
			overviews = append(overviews, deploymentOverviewFromStatus(synthesizedQuarantinedStatus(intent), cat))
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
			// A1: also match the canonical name so `logs <alias>` works.
			canonical := canonicalModelAlt(cat, name)
			allDeps := listAllRuntimes(ctx, rt, nativeRt, dockerRt)
			for _, d := range allDeps {
				if deploymentMatchesQuery(d, name) || (canonical != "" && deploymentMatchesQuery(d, canonical)) {
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

func isNativeDeploymentStarting(rt runtime.Runtime, status *runtime.DeploymentStatus) bool {
	return rt != nil &&
		strings.EqualFold(strings.TrimSpace(rt.Name()), "native") &&
		status != nil &&
		strings.EqualFold(strings.TrimSpace(status.Phase), "starting") &&
		!status.Stalled
}

func setActiveLLMModelConfigForType(ctx context.Context, db *state.DB, modelName, modelType string) error {
	modelName = strings.TrimSpace(modelName)
	if db == nil || modelName == "" {
		return nil
	}
	if !isChatModelType(modelType) {
		return nil
	}
	if err := db.SetConfig(ctx, "llm.model", modelName); err != nil {
		return fmt.Errorf("update llm.model after deploy: %w", err)
	}
	return nil
}

func isChatModelType(modelType string) bool {
	switch strings.ToLower(strings.TrimSpace(modelType)) {
	case "", "llm", "vlm", "chat", "text":
		return true
	default:
		return false
	}
}

func catalogModelParameterCount(cat *knowledge.Catalog, name string) string {
	if cat == nil {
		return ""
	}
	for _, model := range cat.ModelAssets {
		if modelAssetNameMatches(model, name) {
			return strings.TrimSpace(model.Metadata.ParameterCount)
		}
	}
	return ""
}

func catalogModelType(cat *knowledge.Catalog, name string) string {
	if cat == nil {
		return ""
	}
	for i := range cat.ModelAssets {
		if modelAssetNameMatches(cat.ModelAssets[i], name) {
			return strings.TrimSpace(cat.ModelAssets[i].Metadata.Type)
		}
	}
	if ma, _ := findModelAssetOrVariant(cat, name); ma != nil {
		return strings.TrimSpace(ma.Metadata.Type)
	}
	return ""
}

func modelAssetNameMatches(model knowledge.ModelAsset, name string) bool {
	if strings.EqualFold(model.Metadata.Name, name) {
		return true
	}
	for _, alias := range model.Metadata.Aliases {
		if strings.EqualFold(alias, name) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// canonicalModelAlt returns the canonical catalog model name for a query when it
// differs from the input (e.g. an alias carrying a quant suffix / different case),
// or "" otherwise. Deployments are stored under the canonical name, so name-taking
// commands (undeploy/status/logs) use this to also accept the original deploy-time
// alias the user typed.
func canonicalModelAlt(cat *knowledge.Catalog, name string) string {
	if cat == nil {
		return ""
	}
	c := strings.TrimSpace(cat.ResolveCatalogModelName(name))
	if c != "" && !strings.EqualFold(c, name) {
		return c
	}
	return ""
}

// openclawSetDefaultFromEnv reads AIMA_OPENCLAW_SET_DEFAULT as a tri-state:
// unset/unparseable → nil (default: AIMA sets the primary chat model); otherwise
// the parsed bool (false = leave the user's primary model untouched).
func openclawSetDefaultFromEnv() *bool {
	v := strings.TrimSpace(os.Getenv("AIMA_OPENCLAW_SET_DEFAULT"))
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil
	}
	return &b
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

type deploymentIntentSpec struct {
	Name          string
	Model         string
	EngineAsset   string
	EngineVersion string
	Slot          string
	Runtime       string
	Config        map[string]any
	Policy        recovery.Policy
}

type deploymentOperationLocks struct {
	mu      sync.Mutex
	entries map[string]*deploymentOperationLock
}

type deploymentOperationLock struct {
	mu   sync.Mutex
	refs int
}

func newDeploymentOperationLocks() *deploymentOperationLocks {
	return &deploymentOperationLocks{entries: make(map[string]*deploymentOperationLock)}
}

func (l *deploymentOperationLocks) lock(names ...string) func() {
	keys := deploymentOperationLockNames(names...)

	l.mu.Lock()
	entries := make([]*deploymentOperationLock, 0, len(keys))
	for _, key := range keys {
		entry := l.entries[key]
		if entry == nil {
			entry = &deploymentOperationLock{}
			l.entries[key] = entry
		}
		entry.refs++
		entries = append(entries, entry)
	}
	l.mu.Unlock()

	for _, entry := range entries {
		entry.mu.Lock()
	}
	return func() {
		for i := len(entries) - 1; i >= 0; i-- {
			entries[i].mu.Unlock()
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		for i, key := range keys {
			entry := entries[i]
			entry.refs--
			if entry.refs == 0 && l.entries[key] == entry {
				delete(l.entries, key)
			}
		}
	}
}

func deploymentOperationLockName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func deploymentOperationLockNames(names ...string) []string {
	unique := make(map[string]struct{}, len(names))
	keys := make([]string, 0, len(names))
	for _, name := range names {
		key := deploymentOperationLockName(name)
		if key == "" {
			continue
		}
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func deploymentCatalogLockNames(cat *knowledge.Catalog, names ...string) []string {
	expanded := make([]string, 0, len(names)*2)
	for _, name := range names {
		expanded = append(expanded, name)
		if cat != nil {
			expanded = append(expanded, cat.ResolveCatalogModelName(name))
		}
	}
	return deploymentOperationLockNames(expanded...)
}

func deploymentApplyLockNames(cat *knowledge.Catalog, modelName, canonicalName, deployName, intentName string, pinned *recovery.Intent) []string {
	names := []string{modelName, canonicalName, deployName, intentName}
	if pinned != nil {
		names = append(names, pinned.Name, pinned.Model)
	}
	return deploymentCatalogLockNames(cat, names...)
}

func deploymentIntentLockNames(cat *knowledge.Catalog, intents ...*recovery.Intent) []string {
	names := make([]string, 0, len(intents)*2)
	for _, intent := range intents {
		if intent == nil {
			continue
		}
		names = append(names, intent.Name, intent.Model)
	}
	return deploymentCatalogLockNames(cat, names...)
}

func deploymentStatusLockNames(cat *knowledge.Catalog, statuses ...*runtime.DeploymentStatus) []string {
	names := make([]string, 0, len(statuses)*3)
	for _, status := range statuses {
		if status == nil {
			continue
		}
		names = append(names, status.Name, status.Model, status.Labels["aima.dev/model"])
	}
	return deploymentCatalogLockNames(cat, names...)
}

func deploymentMatchedStatusLockNames(cat *knowledge.Catalog, matches ...matchedDeployment) []string {
	statuses := make([]*runtime.DeploymentStatus, 0, len(matches))
	for _, match := range matches {
		statuses = append(statuses, match.Status)
	}
	return deploymentStatusLockNames(cat, statuses...)
}

func mergeDeploymentOperationLockNames(existing []string, groups ...[]string) []string {
	count := len(existing)
	for _, group := range groups {
		count += len(group)
	}
	names := make([]string, 0, count)
	names = append(names, existing...)
	for _, group := range groups {
		names = append(names, group...)
	}
	return deploymentOperationLockNames(names...)
}

func hasAllDeploymentOperationLockNames(held, required []string) bool {
	heldNames := deploymentOperationLockNames(held...)
	requiredNames := deploymentOperationLockNames(required...)
	if len(heldNames) < len(requiredNames) {
		return false
	}
	heldSet := make(map[string]struct{}, len(heldNames))
	for _, name := range heldNames {
		heldSet[name] = struct{}{}
	}
	for _, name := range requiredNames {
		if _, ok := heldSet[name]; !ok {
			return false
		}
	}
	return true
}

func resolvedEngineAsset(cat *knowledge.Catalog, name string) (*knowledge.EngineAsset, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	if cat == nil {
		return nil, fmt.Errorf("resolved engine asset %q is not available in catalog", name)
	}
	for i := range cat.EngineAssets {
		if cat.EngineAssets[i].Metadata.Name == name {
			return &cat.EngineAssets[i], nil
		}
	}
	return nil, fmt.Errorf("resolved engine asset %q is not available in catalog", name)
}

func engineAssetRecoveryPatch(asset *knowledge.EngineAsset) recovery.PolicyPatch {
	if asset == nil {
		return recovery.PolicyPatch{}
	}
	policy := asset.Startup.Recovery
	return recovery.PolicyPatch{
		Enabled:             policy.Enabled,
		CheckIntervalS:      policy.CheckIntervalS,
		ConsecutiveFailures: policy.ConsecutiveFailures,
		MaxAttempts:         policy.MaxAttempts,
		WindowS:             policy.WindowS,
		BackoffS:            append([]int(nil), policy.BackoffS...),
		StableResetS:        policy.StableResetS,
	}
}

func engineAssetVersion(asset *knowledge.EngineAsset) string {
	if asset == nil {
		return ""
	}
	return asset.Metadata.Version
}

func deploymentIntentName(req *runtime.DeployRequest, runtimeName string) string {
	if req == nil {
		return ""
	}
	switch runtimeName {
	case "k3s", "docker":
		return knowledge.SanitizePodName(req.Name + "-" + req.Engine)
	default:
		return req.Name
	}
}

func persistDeploymentIntent(ctx context.Context, db *state.DB, spec deploymentIntentSpec, pinned *recovery.Intent) error {
	if db == nil {
		return fmt.Errorf("persist deployment intent %q: state database is unavailable", spec.Name)
	}
	sanitizedConfig, err := recovery.SanitizeConfigChecked(spec.Config)
	if err != nil {
		return fmt.Errorf("sanitize deployment intent %q config: %w", spec.Name, err)
	}
	intent := &recovery.Intent{
		Name:          spec.Name,
		Model:         spec.Model,
		EngineAsset:   spec.EngineAsset,
		EngineVersion: spec.EngineVersion,
		Slot:          spec.Slot,
		Runtime:       spec.Runtime,
		Config:        sanitizedConfig,
		DesiredState:  recovery.DesiredRunning,
		RecoveryState: recovery.StateHealthy,
		Policy:        spec.Policy,
	}
	if pinned != nil {
		if drift := deploymentIntentPinDrift(pinned, intent); drift != "" {
			return fmt.Errorf("reconciler deployment intent pin changed for deployment %q: %s", pinned.Name, drift)
		}
		return validateDeploymentRecoveryClaim(ctx, db, *pinned, recovery.StateRecovering)
	}
	intents, err := db.ListDeploymentIntents(ctx)
	if err != nil {
		return err
	}
	for _, existing := range intents {
		if existing.Name != spec.Name {
			continue
		}
		intent.Revision = existing.Revision + 1
		intent.CreatedAt = existing.CreatedAt
		break
	}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		return fmt.Errorf("persist deployment intent %q: %w", spec.Name, err)
	}
	return nil
}

func sameDeploymentIntentPin(a, b *recovery.Intent) bool {
	return deploymentIntentPinDrift(a, b) == ""
}

func deploymentIntentPinDrift(a, b *recovery.Intent) string {
	if a == nil || b == nil {
		return "missing intent"
	}
	drift := make([]string, 0, 8)
	if a.Name != b.Name {
		drift = append(drift, "name")
	}
	if a.Model != b.Model {
		drift = append(drift, "model")
	}
	if a.EngineAsset != b.EngineAsset {
		drift = append(drift, "engine asset")
	}
	if a.EngineVersion != b.EngineVersion {
		drift = append(drift, "engine version")
	}
	if a.Slot != b.Slot {
		drift = append(drift, "slot")
	}
	if a.Runtime != b.Runtime {
		drift = append(drift, "runtime")
	}
	if !reflect.DeepEqual(a.Config, b.Config) {
		drift = append(drift, "config")
	}
	if !reflect.DeepEqual(a.Policy, b.Policy) {
		drift = append(drift, "policy")
	}
	return strings.Join(drift, ", ")
}

func validateDeploymentRecoveryClaim(ctx context.Context, db *state.DB, claim recovery.Intent, requiredState string) error {
	if db == nil {
		return fmt.Errorf("validate reconciler deployment intent %q: state database is unavailable", claim.Name)
	}
	current, err := db.GetDeploymentIntent(ctx, claim.Name)
	if err != nil {
		return fmt.Errorf("re-read reconciler deployment intent %q: %w", claim.Name, err)
	}
	if current.DesiredState != recovery.DesiredRunning {
		return fmt.Errorf("reconciler deployment intent %q is %s", claim.Name, current.DesiredState)
	}
	if current.RecoveryState != requiredState {
		return fmt.Errorf("reconciler deployment intent %q recovery state is %s, want %s", claim.Name, current.RecoveryState, requiredState)
	}
	if drift := deploymentRecoveryClaimDrift(&claim, current); drift != "" {
		return fmt.Errorf("reconciler deployment intent %q changed concurrently: %s", claim.Name, drift)
	}
	return nil
}

func deploymentRecoveryClaimDrift(a, b *recovery.Intent) string {
	if a == nil || b == nil {
		return "missing intent"
	}
	drift := make([]string, 0, 12)
	checks := []struct {
		name string
		same bool
	}{
		{"name", a.Name == b.Name},
		{"model", a.Model == b.Model},
		{"engine asset", a.EngineAsset == b.EngineAsset},
		{"engine version", a.EngineVersion == b.EngineVersion},
		{"slot", a.Slot == b.Slot},
		{"runtime", a.Runtime == b.Runtime},
		{"revision", a.Revision == b.Revision},
		{"config", reflect.DeepEqual(a.Config, b.Config)},
		{"desired state", a.DesiredState == b.DesiredState},
		{"recovery state", a.RecoveryState == b.RecoveryState},
		{"policy", reflect.DeepEqual(a.Policy, b.Policy)},
		{"attempt count", a.AttemptCount == b.AttemptCount},
		{"consecutive failure count", a.ConsecutiveFailureCount == b.ConsecutiveFailureCount},
		{"observed restart count", a.ObservedRestartCount == b.ObservedRestartCount},
		{"window start", a.WindowStartedAt.Equal(b.WindowStartedAt)},
		{"next attempt", a.NextAttemptAt.Equal(b.NextAttemptAt)},
		{"healthy since", a.HealthySince.Equal(b.HealthySince)},
		{"last exit code", equalOptionalInt(a.LastExitCode, b.LastExitCode)},
		{"last error", a.LastError == b.LastError},
	}
	for _, check := range checks {
		if !check.same {
			drift = append(drift, check.name)
		}
	}
	return strings.Join(drift, ", ")
}

func equalOptionalInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func recoveryObservationFromStatus(status *runtime.DeploymentStatus) recovery.Observation {
	if status == nil {
		return recovery.Observation{Exists: false}
	}
	return recovery.Observation{
		Exists:   true,
		Ready:    status.Ready,
		Phase:    status.Phase,
		Restarts: status.Restarts,
		ExitCode: status.ExitCode,
		Error:    firstNonEmpty(status.Message, status.ErrorLines),
		Stalled:  status.Stalled,
	}
}

func listDeploymentIntents(ctx context.Context, db *state.DB) ([]*recovery.Intent, error) {
	if db == nil {
		return nil, nil
	}
	return db.ListDeploymentIntents(ctx)
}

func requirePinnedRuntime(intent *recovery.Intent, selected runtime.Runtime) error {
	if intent == nil {
		return nil
	}
	selectedName := ""
	if selected != nil {
		selectedName = selected.Name()
	}
	if selectedName != intent.Runtime {
		return fmt.Errorf("reconciler runtime drift for deployment %q: pinned %q, selected %q", intent.Name, intent.Runtime, selectedName)
	}
	return nil
}

func deploymentIntentByExactName(intents []*recovery.Intent, name string) *recovery.Intent {
	for _, intent := range intents {
		if intent != nil && intent.Name == name {
			return intent
		}
	}
	return nil
}

func matchingDeploymentIntents(intents []*recovery.Intent, query string, exactNameOnly bool) []*recovery.Intent {
	matches := make([]*recovery.Intent, 0)
	for _, intent := range intents {
		if intent == nil {
			continue
		}
		if strings.EqualFold(intent.Name, query) || (!exactNameOnly && strings.EqualFold(intent.Model, query)) {
			matches = append(matches, intent)
		}
	}
	return matches
}

func matchingDeploymentIntentsForOperation(cat *knowledge.Catalog, intents []*recovery.Intent, query string, exactNameOnly bool) []*recovery.Intent {
	matches := make([]*recovery.Intent, 0)
	queryKey := deploymentOperationLockName(query)
	for _, intent := range intents {
		if intent == nil {
			continue
		}
		if deploymentOperationLockName(intent.Name) == queryKey {
			matches = append(matches, intent)
			continue
		}
		if !exactNameOnly && sameDeploymentCatalogModel(cat, intent.Model, query) {
			matches = append(matches, intent)
		}
	}
	return matches
}

func sameDeploymentCatalogModel(cat *knowledge.Catalog, left, right string) bool {
	leftKey := deploymentOperationLockName(left)
	rightKey := deploymentOperationLockName(right)
	if leftKey == "" || rightKey == "" {
		return false
	}
	if leftKey == rightKey {
		return true
	}
	if cat == nil {
		return false
	}
	leftCanonical := deploymentOperationLockName(cat.ResolveCatalogModelName(left))
	rightCanonical := deploymentOperationLockName(cat.ResolveCatalogModelName(right))
	return leftCanonical != "" && leftCanonical == rightCanonical
}

func deploymentCatalogModelQueries(cat *knowledge.Catalog, query string) []string {
	queries := make([]string, 0, 3)
	seen := make(map[string]struct{})
	appendQuery := func(value string) {
		value = strings.TrimSpace(value)
		key := deploymentOperationLockName(value)
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		queries = append(queries, value)
	}
	appendQuery(query)
	if cat == nil {
		return queries
	}
	canonical := strings.TrimSpace(cat.ResolveCatalogModelName(query))
	appendQuery(canonical)
	for _, asset := range cat.ModelAssets {
		if deploymentOperationLockName(asset.Metadata.Name) != deploymentOperationLockName(canonical) {
			continue
		}
		appendQuery(asset.Metadata.Name)
		for _, alias := range asset.Metadata.Aliases {
			appendQuery(alias)
		}
	}
	return queries
}

func findMatchingDeploymentsForCatalog(ctx context.Context, cat *knowledge.Catalog, query string, suppress func(*runtime.DeploymentStatus) bool, rts ...runtime.Runtime) []matchedDeployment {
	var matches []matchedDeployment
	for _, candidate := range deploymentCatalogModelQueries(cat, query) {
		matches = appendUniqueMatchedDeployments(matches, findMatchingDeployments(ctx, candidate, suppress, rts...)...)
	}
	return matches
}

func appendUniqueMatchedDeployments(dst []matchedDeployment, candidates ...matchedDeployment) []matchedDeployment {
	seen := make(map[string]struct{}, len(dst)+len(candidates))
	for _, match := range dst {
		if match.Runtime != nil && match.Status != nil {
			seen[fmt.Sprintf("%p|%s", match.Runtime, match.Status.Name)] = struct{}{}
		}
	}
	for _, match := range candidates {
		if match.Runtime == nil || match.Status == nil {
			continue
		}
		key := fmt.Sprintf("%p|%s", match.Runtime, match.Status.Name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		dst = append(dst, match)
	}
	return dst
}

func appendUniqueDeploymentIntents(dst []*recovery.Intent, candidates ...*recovery.Intent) []*recovery.Intent {
	seen := make(map[string]struct{}, len(dst)+len(candidates))
	for _, intent := range dst {
		if intent != nil {
			seen[intent.Name] = struct{}{}
		}
	}
	for _, intent := range candidates {
		if intent == nil {
			continue
		}
		if _, ok := seen[intent.Name]; ok {
			continue
		}
		seen[intent.Name] = struct{}{}
		dst = append(dst, intent)
	}
	return dst
}

func enrichDeploymentStatusWithIntent(status *runtime.DeploymentStatus, intent *recovery.Intent) {
	if status == nil || intent == nil {
		return
	}
	status.DesiredState = intent.DesiredState
	status.RecoveryState = intent.RecoveryState
	status.RecoveryAttempts = intent.AttemptCount
	if !intent.NextAttemptAt.IsZero() {
		status.NextRecoveryAt = intent.NextAttemptAt.UTC().Format(time.RFC3339)
	}
	if intent.RecoveryState == recovery.StateQuarantined {
		status.QuarantineReason = intent.LastError
	}
}

func synthesizedQuarantinedStatus(intent *recovery.Intent) *runtime.DeploymentStatus {
	if intent == nil {
		return nil
	}
	status := &runtime.DeploymentStatus{
		Name:    intent.Name,
		Model:   intent.Model,
		Engine:  intent.EngineAsset,
		Slot:    intent.Slot,
		Phase:   recovery.StateQuarantined,
		Ready:   false,
		Config:  intent.Config,
		Runtime: intent.Runtime,
		Labels: map[string]string{
			"aima.dev/model":  intent.Model,
			"aima.dev/engine": intent.EngineAsset,
			"aima.dev/slot":   intent.Slot,
		},
		Message: intent.LastError,
	}
	enrichDeploymentStatusWithIntent(status, intent)
	return status
}

func quarantinedIntentForQuery(intents []*recovery.Intent, query string) (*recovery.Intent, error) {
	exact := matchingDeploymentIntents(intents, query, true)
	matches := exact
	if len(matches) == 0 {
		matches = matchingDeploymentIntents(intents, query, false)
	}
	quarantined := make([]*recovery.Intent, 0, len(matches))
	for _, intent := range matches {
		if intent.DesiredState == recovery.DesiredRunning && intent.RecoveryState == recovery.StateQuarantined {
			quarantined = append(quarantined, intent)
		}
	}
	if len(quarantined) == 0 {
		return nil, nil
	}
	if len(quarantined) > 1 {
		names := make([]string, 0, len(quarantined))
		for _, intent := range quarantined {
			names = append(names, intent.Name)
		}
		return nil, fmt.Errorf("deployment %q is ambiguous; matches quarantined intents: %s", query, strings.Join(names, ", "))
	}
	return quarantined[0], nil
}

type deploymentOverview struct {
	Name                string `json:"name"`
	Model               string `json:"model"`
	Engine              string `json:"engine,omitempty"`
	Image               string `json:"image,omitempty"`
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
	GPUMemoryMiB        int    `json:"gpu_memory_mib,omitempty"`
	GPUMemorySource     string `json:"gpu_memory_source,omitempty"`
	StartupPhase        string `json:"startup_phase,omitempty"`
	StartupProgress     int    `json:"startup_progress,omitempty"`
	StartupMessage      string `json:"startup_message,omitempty"`
	EstimatedTotalS     int    `json:"estimated_total_s,omitempty"`
	ErrorLines          string `json:"error_lines,omitempty"`
	ServedModel         string `json:"served_model,omitempty"`
	ModelType           string `json:"model_type,omitempty"`
	ParameterCount      string `json:"parameter_count,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
	DesiredState        string `json:"desired_state,omitempty"`
	RecoveryState       string `json:"recovery_state,omitempty"`
	RecoveryAttempts    int    `json:"recovery_attempts,omitempty"`
	NextRecoveryAt      string `json:"next_recovery_at,omitempty"`
	QuarantineReason    string `json:"quarantine_reason,omitempty"`
}

func deploymentOverviewFromStatus(status *runtime.DeploymentStatus, cat *knowledge.Catalog) deploymentOverview {
	populateDeploymentOverviewFields(status)
	if status == nil {
		return deploymentOverview{}
	}
	return deploymentOverview{
		Name:                status.Name,
		Model:               status.Model,
		Engine:              status.Engine,
		Image:               status.Image,
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
		GPUMemoryMiB:        status.GPUMemoryMiB,
		GPUMemorySource:     status.GPUMemorySource,
		StartupPhase:        status.StartupPhase,
		StartupProgress:     status.StartupProgress,
		StartupMessage:      status.StartupMessage,
		EstimatedTotalS:     status.EstimatedTotalS,
		ErrorLines:          status.ErrorLines,
		ServedModel:         deploymentUpstreamModel(status, ""),
		ModelType:           firstNonEmpty(status.Labels[proxy.LabelModelType], catalogModelType(cat, status.Model)),
		ParameterCount:      firstNonEmpty(status.Labels[proxy.LabelParameterCount]),
		ContextWindowTokens: contextWindowFromStatus(status),
		DesiredState:        status.DesiredState,
		RecoveryState:       status.RecoveryState,
		RecoveryAttempts:    status.RecoveryAttempts,
		NextRecoveryAt:      status.NextRecoveryAt,
		QuarantineReason:    status.QuarantineReason,
	}
}

const (
	llamaComputeReserveMiB    = 1024
	llamaMinimumContextTokens = 2048
	llamaMemorySafetyFraction = 0.90
	bytesPerMiB               = 1024 * 1024
)

type llamaMemoryFit struct {
	Evaluated   bool
	Fits        bool
	UsableMiB   int
	RequiredMiB int
	BudgetMiB   int
	KVAtMinMiB  int
}

var llamaGGUFShardRE = regexp.MustCompile(`(?i)^(.+)-(\d+)-of-(\d+)\.gguf$`)

func requiresNativeLlamaMemoryPreflight(runtimeName, modelFormat, engineName string) bool {
	return strings.EqualFold(strings.TrimSpace(runtimeName), "native") &&
		strings.EqualFold(strings.TrimSpace(modelFormat), "gguf") &&
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(engineName)), "llamacpp")
}

// minimumLlamaMemoryFit reports whether model weights, llama.cpp compute
// buffers, and the KV cache at the minimum useful context can fit in the safe
// fraction of detected usable memory. Unknown memory or architecture leaves the
// result unevaluated so deployments retain their existing graceful behavior.
func minimumLlamaMemoryFit(kvPerTok int64, usableMiB, nonKVMiB int) llamaMemoryFit {
	if kvPerTok <= 0 || usableMiB <= 0 || nonKVMiB < 0 {
		return llamaMemoryFit{Fits: true}
	}

	budgetMiB := int(float64(usableMiB) * llamaMemorySafetyFraction)
	kvAtMinMiB := int((int64(llamaMinimumContextTokens)*kvPerTok + bytesPerMiB - 1) / bytesPerMiB)
	requiredMiB := nonKVMiB + llamaComputeReserveMiB + kvAtMinMiB
	return llamaMemoryFit{
		Evaluated:   true,
		Fits:        requiredMiB <= budgetMiB,
		UsableMiB:   usableMiB,
		RequiredMiB: requiredMiB,
		BudgetMiB:   budgetMiB,
		KVAtMinMiB:  kvAtMinMiB,
	}
}

// llamaModelWeightMiB sums all shards when modelPath points to the first shard
// of a split GGUF. llama.cpp loads every matching shard, so checking the first
// file alone would understate the memory needed to load the model.
func llamaModelWeightMiB(modelPath string) int {
	if modelPath == "" {
		return 0
	}
	match := llamaGGUFShardRE.FindStringSubmatch(filepath.Base(modelPath))
	if match == nil {
		return fileSizeMiB(modelPath)
	}

	entries, err := os.ReadDir(filepath.Dir(modelPath))
	if err != nil {
		return fileSizeMiB(modelPath)
	}
	var totalBytes int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		part := llamaGGUFShardRE.FindStringSubmatch(entry.Name())
		if part == nil || !strings.EqualFold(part[1], match[1]) || part[3] != match[3] {
			continue
		}
		if info, err := entry.Info(); err == nil {
			totalBytes += info.Size()
		}
	}
	if totalBytes == 0 {
		return fileSizeMiB(modelPath)
	}
	return int(totalBytes / bytesPerMiB)
}

func llamaNonKVMiB(modelPath string, config map[string]any) int {
	nonKVMiB := llamaModelWeightMiB(modelPath)
	if mm, _ := config["mmproj"].(string); mm != "" {
		nonKVMiB += fileSizeMiB(mm)
	}
	return nonKVMiB
}

// ensureLlamaMinimumMemoryFit returns a user-actionable error before spawning
// llama.cpp when the model cannot fit even at the minimum useful context.
func ensureLlamaMinimumMemoryFit(modelPath string, config map[string]any, hw knowledge.HardwareInfo) error {
	if modelPath == "" {
		return nil
	}
	arch, ok := model.ReadKVArch(modelPath)
	if !ok {
		return nil
	}

	nonKVMiB := llamaNonKVMiB(modelPath, config)
	fit := minimumLlamaMemoryFit(arch.KVBytesPerToken(), usableMemoryMiB(hw), nonKVMiB)
	if !fit.Evaluated || fit.Fits {
		return nil
	}

	return fmt.Errorf(
		"model requires at least %d MiB (weights+projector %d MiB, compute reserve %d MiB, KV %d MiB at ctx_size %d), but the safe memory budget is %d MiB from %d MiB detected; use a smaller or more-quantized model, or free/increase available memory",
		fit.RequiredMiB, nonKVMiB, llamaComputeReserveMiB, fit.KVAtMinMiB, llamaMinimumContextTokens, fit.BudgetMiB, fit.UsableMiB,
	)
}

// fitContextToMemory shrinks config["ctx_size"] so the llama.cpp KV cache plus
// model weights and the multimodal projector fit the detected memory, and caps it
// at the model's trained context. It only ever lowers ctx_size — never raises it.
// Returns (clamped, requestedCtx, appliedCtx, reason). It is a no-op (clamped=false)
// when ctx_size is unset, the GGUF architecture can't be read, or memory is unknown,
// so unsupported models/hardware degrade gracefully instead of erroring.
func fitContextToMemory(modelPath string, config map[string]any, hw knowledge.HardwareInfo) (bool, int, int, string) {
	reqCtx := contextWindowFromResolvedConfig(config)
	if reqCtx <= 0 || modelPath == "" || config == nil {
		return false, 0, 0, ""
	}
	arch, ok := model.ReadKVArch(modelPath)
	if !ok {
		return false, 0, 0, "" // can't estimate KV → leave ctx_size untouched
	}

	nonKVMiB := llamaNonKVMiB(modelPath, config)

	target, reasons := clampContextForMemory(reqCtx, arch.NCtxTrain, arch.KVBytesPerToken(), usableMemoryMiB(hw), nonKVMiB)
	if target >= reqCtx {
		return false, reqCtx, reqCtx, ""
	}
	config["ctx_size"] = target
	return true, reqCtx, target, strings.Join(reasons, "; ")
}

// clampContextForMemory computes the largest context window ≤ reqCtx that fits:
// (a) the model's trained context (nCtxTrain, 0 = unknown/skip) and (b) the KV
// budget left after weights+projector in usableMiB (0 = unknown/skip). kvPerTok
// is the f16 KV bytes per token. It floors at a minimally useful context. Pure
// (no I/O) for testability.
func clampContextForMemory(reqCtx, nCtxTrain int, kvPerTok int64, usableMiB, nonKVMiB int) (int, []string) {
	target := reqCtx
	var reasons []string

	if nCtxTrain > 0 && target > nCtxTrain {
		target = nCtxTrain
		reasons = append(reasons, fmt.Sprintf("capped at trained context %d", nCtxTrain))
	}

	if usableMiB > 0 && kvPerTok > 0 {
		kvBudgetMiB := int(float64(usableMiB)*llamaMemorySafetyFraction) - nonKVMiB - llamaComputeReserveMiB
		if kvBudgetMiB < 0 {
			kvBudgetMiB = 0
		}
		maxCtx := int(int64(kvBudgetMiB) * 1024 * 1024 / kvPerTok)
		maxCtx -= maxCtx % 256 // clean multiple
		if maxCtx < target {
			target = maxCtx
			reasons = append(reasons, fmt.Sprintf("%d MiB usable, weights+projector %d MiB, KV %d B/token",
				usableMiB, nonKVMiB, kvPerTok))
		}
	}

	if target < llamaMinimumContextTokens {
		target = llamaMinimumContextTokens
	}
	return target, reasons
}

// usableMemoryMiB returns the memory budget an all-layers-offloaded llama.cpp
// model can use: GPU VRAM for discrete GPUs, or system RAM minus an OS reserve
// for unified-memory / CPU hosts. Returns 0 when memory is unknown.
func usableMemoryMiB(hw knowledge.HardwareInfo) int {
	ramReserve := func(total int) int {
		reserve := total / 4
		if reserve < 2048 {
			reserve = 2048
		}
		if reserve > 16384 {
			reserve = 16384
		}
		return reserve
	}
	// An all-layers-offloaded llama.cpp model is bounded by GPU memory. For a
	// unified-memory APU this is the carved iGPU pool (read via ROCm), which is the
	// correct budget — NOT the OS-visible system RAM, which Win32 under-reports on
	// such APUs (e.g. Strix Halo shows ~32 GB OS RAM but ~110 GB iGPU VRAM). Prefer
	// GPU memory whenever it's known; fall back to system RAM only for CPU-only hosts.
	if hw.GPUMemFreeMiB > 0 {
		return hw.GPUMemFreeMiB
	}
	if hw.GPUVRAMMiB > 0 {
		return hw.GPUVRAMMiB
	}
	if hw.RAMTotalMiB > 0 {
		return hw.RAMTotalMiB - ramReserve(hw.RAMTotalMiB)
	}
	return 0
}

// fileSizeMiB returns the file's size in MiB, or 0 if it can't be stat'd.
func fileSizeMiB(path string) int {
	if path == "" {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return int(fi.Size() / (1024 * 1024))
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

// findColocatedMMProj returns the path of a multimodal projector (mmproj-*.gguf)
// next to a GGUF model, preferring an f16 projector for quality. Returns "" when
// none is present (i.e. the model is not multimodal). modelPath may be the model
// file or its directory; the projector is expected in the same directory.
func findColocatedMMProj(modelPath string) string {
	dir := modelPath
	if fi, err := os.Stat(modelPath); err == nil && !fi.IsDir() {
		dir = filepath.Dir(modelPath)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var f16, other string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if !strings.HasSuffix(lower, ".gguf") || !strings.Contains(lower, "mmproj") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if strings.Contains(lower, "f16") || strings.Contains(lower, "fp16") {
			f16 = full
		} else if other == "" {
			other = full
		}
	}
	if f16 != "" {
		return f16
	}
	return other
}
