package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/jguan/aima/internal/engine"
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
	normalizeAutoPortOverrides(overrides)
	if slot != "" {
		overrides["slot"] = slot
	}

	resolveCat := resolveCatalogWithLocalEngineOverlay(ctx, cat, db, hwInfo, dataDir)

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

	resolved, canonicalName, err := resolveWithFallback(ctx, resolveCat, db, hwInfo, modelName, engineType, overrides, dataDir, resolveOpts...)
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

// normalizeAutoPortOverrides removes "auto" sentinels from port-like override keys
// before resolution. This preserves the engine YAML default port so Go-side host
// port allocation can still choose a free host port later in deploy.apply.
func normalizeAutoPortOverrides(overrides map[string]any) {
	for key, value := range overrides {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(raw), "auto") {
			continue
		}
		if !isPortOverrideKey(key) {
			continue
		}
		delete(overrides, key)
	}
}

func isPortOverrideKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "port" {
		return true
	}
	return strings.HasSuffix(key, "_port") || strings.HasPrefix(key, "port_") || strings.Contains(key, "_port_")
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
		// Catalog hit — prefer the actual scanned path when the catalog default
		// is empty or no longer matches the files present on this device.
		if dbModel, dbErr := db.FindModelByName(ctx, modelName); dbErr == nil && dbModel.Path != "" {
			pathReady := resolved.ModelPath != "" && model.PathLooksCompatible(resolved.ModelPath, resolved.ModelFormat, resolvedQuantizationHint(resolved))
			if !pathReady {
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
	if !rebuildSynthetic && strings.Contains(err.Error(), "no variant of model") && !cat.HasCatalogModel(modelName) {
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

var (
	resolveImageExistsInDocker     = engine.ImageExistsInDocker
	resolveImageExistsInContainerd = engine.ImageExistsInContainerd
)

func resolveCatalogWithLocalEngineOverlay(ctx context.Context, cat *knowledge.Catalog, db *state.DB, hwInfo knowledge.HardwareInfo, dataDir string) *knowledge.Catalog {
	if cat == nil || db == nil {
		return cat
	}

	overlay, err := localEngineOverlayCatalog(ctx, cat, db, hwInfo, dataDir)
	if err != nil {
		slog.Warn("resolve: local engine overlay skipped", "error", err)
		return cloneCatalog(cat)
	}
	base := cloneCatalog(cat)
	if base == nil {
		return cat
	}
	if overlay == nil || len(overlay.EngineAssets) == 0 {
		return base
	}

	merged := knowledge.MergeCatalog(base, overlay)
	slog.Info("resolve: merged local engine overlay", "overlay_assets", len(overlay.EngineAssets))
	return merged
}

type localEngineOverlayCandidate struct {
	base         knowledge.EngineAsset
	containerRef string
	nativeBinary string
}

func localEngineOverlayCatalog(ctx context.Context, cat *knowledge.Catalog, db *state.DB, hwInfo knowledge.HardwareInfo, dataDir string) (*knowledge.Catalog, error) {
	installed, err := db.ListEngines(ctx)
	if err != nil {
		return nil, fmt.Errorf("list engines for overlay: %w", err)
	}

	candidates := make(map[string]*localEngineOverlayCandidate)
	for _, inst := range installed {
		if inst == nil || !inst.Available || strings.TrimSpace(inst.Type) == "" {
			continue
		}
		base := cat.FindEngineByName(inst.Type, hwInfo)
		if base == nil {
			continue
		}
		cand := candidates[base.Metadata.Name]
		if cand == nil {
			cand = &localEngineOverlayCandidate{
				base: cloneEngineAsset(*base),
			}
			candidates[base.Metadata.Name] = cand
		}

		switch strings.ToLower(strings.TrimSpace(inst.RuntimeType)) {
		case "container":
			if cand.containerRef == "" {
				if ref := localInstalledContainerRef(ctx, inst); ref != "" && normalizedImageRef(ref) != normalizedImageRef(engineImageRef(&cand.base)) {
					cand.containerRef = ref
				}
			}
		case "native":
			if cand.nativeBinary == "" {
				if path := localInstalledNativeBinaryPath(inst); path != "" {
					cand.nativeBinary = path
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	overlay := &knowledge.Catalog{EngineAssets: make([]knowledge.EngineAsset, 0, len(candidates))}
	for _, cand := range candidates {
		asset := cand.base
		changed := false

		if cand.containerRef != "" {
			name, tag := splitImageRef(cand.containerRef)
			if name != "" && (asset.Image.Name != name || asset.Image.Tag != tag) {
				asset.Image.Name = name
				asset.Image.Tag = tag
				changed = true
			}
		}
		if cand.nativeBinary != "" {
			if asset.Source == nil {
				asset.Source = &knowledge.EngineSource{}
			}
			if asset.Source.Binary != filepath.Base(cand.nativeBinary) {
				asset.Source.Binary = filepath.Base(cand.nativeBinary)
				changed = true
			}
			if asset.Source.InstallType != "preinstalled" {
				asset.Source.InstallType = "preinstalled"
				changed = true
			}
			before := 0
			if asset.Source.Probe != nil {
				before = len(asset.Source.Probe.Paths)
			}
			ensureResolvedEngineProbePath(&knowledge.ResolvedConfig{Source: asset.Source}, cand.nativeBinary)
			if len(asset.Source.Probe.Paths) != before {
				changed = true
			}
		}

		if changed {
			overlay.EngineAssets = append(overlay.EngineAssets, asset)
		}
	}

	if len(overlay.EngineAssets) == 0 {
		return nil, nil
	}
	sort.Slice(overlay.EngineAssets, func(i, j int) bool {
		return overlay.EngineAssets[i].Metadata.Name < overlay.EngineAssets[j].Metadata.Name
	})
	return overlay, nil
}

func localInstalledContainerRef(ctx context.Context, inst *state.Engine) string {
	if inst == nil {
		return ""
	}
	ref := normalizedImageRef(explorerInstalledImageRef(inst))
	if ref == "" {
		return ""
	}
	runner := &execRunner{}
	if resolveImageExistsInContainerd(ctx, ref, runner) || resolveImageExistsInDocker(ctx, ref, runner) {
		return ref
	}
	return ""
}

func localInstalledNativeBinaryPath(inst *state.Engine) string {
	if inst == nil || inst.BinaryPath == "" {
		return ""
	}
	if _, err := os.Stat(inst.BinaryPath); err == nil {
		return inst.BinaryPath
	}
	return ""
}

func cloneCatalog(cat *knowledge.Catalog) *knowledge.Catalog {
	if cat == nil {
		return nil
	}
	clone := &knowledge.Catalog{
		HardwareProfiles:      append([]knowledge.HardwareProfile(nil), cat.HardwareProfiles...),
		PartitionStrategies:   append([]knowledge.PartitionStrategy(nil), cat.PartitionStrategies...),
		EngineAssets:          append([]knowledge.EngineAsset(nil), cat.EngineAssets...),
		RawEngineAssets:       append([]knowledge.EngineAsset(nil), cat.RawEngineAssets...),
		ModelAssets:           append([]knowledge.ModelAsset(nil), cat.ModelAssets...),
		StackComponents:       append([]knowledge.StackComponent(nil), cat.StackComponents...),
		DeploymentScenarios:   append([]knowledge.DeploymentScenario(nil), cat.DeploymentScenarios...),
		BenchmarkProfileTiers: append([]knowledge.BenchmarkProfileTier(nil), cat.BenchmarkProfileTiers...),
	}
	if len(cat.EngineProfiles) > 0 {
		clone.EngineProfiles = make(map[string]*knowledge.EngineProfile, len(cat.EngineProfiles))
		for name, profile := range cat.EngineProfiles {
			clone.EngineProfiles[name] = profile
		}
	}
	return clone
}

func cloneEngineAsset(asset knowledge.EngineAsset) knowledge.EngineAsset {
	clone := asset
	if asset.Source != nil {
		clone.Source = cloneEngineSource(asset.Source)
	}
	return clone
}

func cloneEngineSource(src *knowledge.EngineSource) *knowledge.EngineSource {
	if src == nil {
		return nil
	}
	// Shallow copy is sufficient — overlay logic only mutates Binary, InstallType,
	// and Probe.Paths. Other fields (Download, Mirror, SHA256, etc.) are read-only.
	clone := *src
	if src.Probe != nil {
		probe := *src.Probe
		if len(src.Probe.Paths) > 0 {
			probe.Paths = append([]string(nil), src.Probe.Paths...)
		}
		clone.Probe = &probe
	}
	return &clone
}

func ensureResolvedEngineProbePath(resolved *knowledge.ResolvedConfig, binaryPath string) {
	if resolved == nil || binaryPath == "" {
		return
	}
	if resolved.Source == nil {
		resolved.Source = &knowledge.EngineSource{
			Binary:      filepath.Base(binaryPath),
			InstallType: "preinstalled",
			Probe: &knowledge.EngineSourceProbe{
				Paths: []string{binaryPath},
			},
		}
		return
	}
	if resolved.Source.Binary == "" {
		resolved.Source.Binary = filepath.Base(binaryPath)
	}
	if resolved.Source.Probe == nil {
		resolved.Source.Probe = &knowledge.EngineSourceProbe{}
	}
	for _, existing := range resolved.Source.Probe.Paths {
		if existing == binaryPath {
			return
		}
	}
	resolved.Source.Probe.Paths = append([]string{binaryPath}, resolved.Source.Probe.Paths...)
}
