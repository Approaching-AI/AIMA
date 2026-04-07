package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	goruntime "runtime"
	"strings"

	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/model"

	state "github.com/jguan/aima/internal"
)

// resolvedDeployment holds the shared result of resolve + CheckFit,
// used by both DeployApply and DeployDryRun.
type resolvedDeployment struct {
	ModelName string
	Resolved  *knowledge.ResolvedConfig
	Fit       *knowledge.FitReport
}

// queryGoldenOverrides returns config overrides from the best golden configuration
// matching the given hardware/engine/model. Returns nil if no golden config found
// or if hwProfile is empty (to prevent cross-hardware injection).
func queryGoldenOverrides(ctx context.Context, kStore *knowledge.Store, hwProfile, engineType, modelName string) map[string]any {
	if kStore == nil || hwProfile == "" {
		return nil
	}
	resp, err := kStore.Search(ctx, knowledge.SearchParams{
		Hardware: hwProfile,
		Engine:   engineType,
		Model:    modelName,
		Status:   "golden",
		SortBy:   "throughput",
		Limit:    1,
	})
	if err != nil || len(resp.Results) == 0 {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(resp.Results[0].Config, &cfg); err != nil {
		return nil
	}
	if len(cfg) == 0 {
		return nil
	}
	slog.Info("L2 golden config found",
		"config_id", resp.Results[0].ConfigID,
		"keys", len(cfg))
	return cfg
}

// resolveDeployment performs the common resolve -> CheckFit sequence.
// Runtime selection is done separately by callers via pickRuntimeForDeployment.
func resolveDeployment(ctx context.Context, cat *knowledge.Catalog, db *state.DB, kStore *knowledge.Store, hwInfo knowledge.HardwareInfo, modelName, engineType, slot string, overrides map[string]any, dataDir string) (*resolvedDeployment, error) {
	if overrides == nil {
		overrides = map[string]any{}
	}
	if slot != "" {
		overrides["slot"] = slot
	}

	// Extract deployment constraints (not config params)
	var resolveOpts []knowledge.ResolveOption
	if mcs, ok := overrides["max_cold_start_s"]; ok {
		var v int
		switch x := mcs.(type) {
		case float64:
			v = int(x)
		case int:
			v = x
		case json.Number:
			if n, err := x.Int64(); err == nil {
				v = int(n)
			}
		}
		if v > 0 {
			resolveOpts = append(resolveOpts, knowledge.WithMaxColdStart(v))
		}
		delete(overrides, "max_cold_start_s")
	}

	// L2c: inject golden config into resolve chain (applied between L0 and L1 inside Resolve)
	resolveOpts = append(resolveOpts, knowledge.WithGoldenConfig(func(hardware, engine, model string) map[string]any {
		return queryGoldenOverrides(ctx, kStore, hardware, engine, model)
	}))

	resolved, canonicalName, err := resolveWithFallback(ctx, cat, db, hwInfo, modelName, engineType, overrides, dataDir, resolveOpts...)
	if err != nil {
		return nil, err
	}

	fit := knowledge.CheckFit(resolved, hwInfo)
	for k, v := range fit.Adjustments {
		resolved.Config[k] = v
		resolved.Provenance[k] = "L0-auto"
	}

	return &resolvedDeployment{
		ModelName: canonicalName,
		Resolved:  resolved,
		Fit:       fit,
	}, nil
}

// buildHardwareInfo creates a HardwareInfo with platform, runtime, and hardware awareness.
// Populates both static fields (from hal.Detect) and dynamic fields (from hal.CollectMetrics).
// Missing data results in zero values, which downstream functions treat as "unknown" and skip.
func buildHardwareInfo(ctx context.Context, cat *knowledge.Catalog, rtName string) knowledge.HardwareInfo {
	hwInfo := knowledge.HardwareInfo{
		Platform:    goruntime.GOOS + "/" + goruntime.GOARCH,
		RuntimeType: rtName,
	}
	if hw, err := hal.Detect(ctx); err == nil {
		if hw.GPU != nil {
			hwInfo.GPUArch = hw.GPU.Arch
			hwInfo.GPUModel = hw.GPU.Name
			hwInfo.GPUVRAMMiB = hw.GPU.VRAMMiB
			hwInfo.GPUCount = hw.GPU.Count
			hwInfo.UnifiedMemory = hw.GPU.UnifiedMemory
		}
		hwInfo.CPUArch = hw.CPU.Arch
		hwInfo.CPUCores = hw.CPU.Cores
		hwInfo.RAMTotalMiB = hw.RAM.TotalMiB
		hwInfo.RAMAvailMiB = hw.RAM.AvailableMiB
		hwInfo.SwapTotalMiB = hw.RAM.SwapTotalMiB
	}
	// Dynamic layer: collect runtime GPU metrics (failure is non-fatal)
	if m, err := hal.CollectMetrics(ctx); err == nil && m.GPU != nil {
		hwInfo.GPUMemUsedMiB = m.GPU.MemoryUsedMiB
		hwInfo.GPUMemFreeMiB = m.GPU.MemoryTotalMiB - m.GPU.MemoryUsedMiB
	}
	// Match to specific hardware profile and populate TDP
	if cat != nil {
		if hp := cat.MatchHardwareProfile(hwInfo); hp != nil {
			hwInfo.HardwareProfile = hp.Metadata.Name
		}
		hwInfo.TDPWatts = cat.FindHardwareTDP(hwInfo)
	}
	return hwInfo
}

// resolveWithFallback tries catalog resolution first; on "not found in catalog",
// falls back to building a synthetic ModelAsset from the model's DB scan record.
func resolveWithFallback(ctx context.Context, cat *knowledge.Catalog, db *state.DB, hw knowledge.HardwareInfo, modelName, engineType string, overrides map[string]any, dataDir string, opts ...knowledge.ResolveOption) (*knowledge.ResolvedConfig, string, error) {
	resolved, err := cat.Resolve(hw, modelName, engineType, overrides, opts...)
	if err == nil {
		// Catalog hit — but ModelPath may be empty if no override was given.
		// Look up DB for the actual registered path from scan/import.
		if resolved.ModelPath == "" {
			if dbModel, dbErr := db.FindModelByName(ctx, modelName); dbErr == nil && dbModel.Path != "" {
				if model.PathLooksCompatible(dbModel.Path, dbModel.Format, resolvedQuantizationHint(resolved)) {
					resolved.ModelPath = dbModel.Path
				} else {
					slog.Warn("ignoring incompatible scanned model path",
						"model", modelName,
						"path", dbModel.Path,
						"format", dbModel.Format,
						"detected_quantization", dbModel.Quantization,
						"expected_quantization", resolvedQuantizationHint(resolved))
				}
			}
		}
		return resolved, resolved.ModelName, nil
	}
	rebuildSynthetic := strings.Contains(err.Error(), "not found in catalog")
	// Also trigger synthetic rebuild when the model exists in catalog but has
	// no variant for the requested engine — Explorer needs this to discover
	// working configs for engine+model combos not yet cataloged.
	if !rebuildSynthetic && strings.Contains(err.Error(), "no variant of model") {
		rebuildSynthetic = true
	}
	if !rebuildSynthetic && cat.HasSyntheticModel(modelName) {
		rebuildSynthetic = true
	}
	if !rebuildSynthetic {
		return nil, "", fmt.Errorf("resolve config: %w", err)
	}

	// Catalog miss or stale synthetic model — rebuild from the scan database.
	dbModel, dbErr := db.FindModelByName(ctx, modelName)
	if dbErr != nil {
		return nil, "", fmt.Errorf("resolve config: model %q not found in catalog (also not found in scan database)", modelName)
	}
	if dbModel.Format == "" {
		return nil, "", fmt.Errorf("model %q found on disk but has no format info; cannot auto-detect engine", dbModel.Name)
	}

	slog.Info("model not in catalog, using auto-detected config",
		"model", dbModel.Name, "format", dbModel.Format, "path", dbModel.Path)

	synth := cat.BuildSyntheticModelAsset(knowledge.ScanMetadata{
		Name:         dbModel.Name,
		Type:         dbModel.Type,
		Family:       dbModel.DetectedArch,
		ParamCount:   dbModel.DetectedParams,
		Format:       dbModel.Format,
		SizeBytes:    dbModel.SizeBytes,
		TotalParams:  dbModel.TotalParams,
		ActiveParams: dbModel.ActiveParams,
		Quantization: dbModel.Quantization,
		ModelClass:   dbModel.ModelClass,
	}, hw, engineType)
	cat.UpsertSyntheticModel(synth)

	if overrides == nil {
		overrides = map[string]any{}
	}
	overrides["model_path"] = dbModel.Path

	resolved, err = cat.Resolve(hw, dbModel.Name, engineType, overrides, opts...)
	if err != nil {
		return nil, "", fmt.Errorf("resolve auto-detected config for %s: %w", dbModel.Name, err)
	}
	return resolved, dbModel.Name, nil
}

func resolvedQuantizationHint(resolved *knowledge.ResolvedConfig) string {
	if resolved == nil || resolved.Config == nil {
		return ""
	}
	if q, ok := resolved.Config["quantization"].(string); ok {
		return q
	}
	return ""
}
