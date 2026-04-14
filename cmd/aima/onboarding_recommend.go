package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/stack"

	state "github.com/jguan/aima/internal"
)

// modelRecommendation is the JSON output for a single recommended model.
type modelRecommendation struct {
	ModelName    string `json:"model_name"`
	ModelType    string `json:"model_type"`
	Family       string `json:"family"`
	ParamCount   string `json:"parameter_count"`
	ActiveParams string `json:"active_params,omitempty"`

	Variant *recommendedVariant `json:"variant,omitempty"`
	Engine  *recommendedEngine  `json:"engine,omitempty"`

	EngineStatus recommendedEngineStatus `json:"engine_status"`
	Performance  recommendedPerformance  `json:"performance"`
	GoldenConfig recommendedGolden       `json:"golden_config"`
	ModelStatus  recommendedModelStatus  `json:"model_status"`
	FitScore     int                     `json:"fit_score"`
	Reason       string                  `json:"recommendation_reason"`
	FitWarnings  []string                `json:"fit_warnings,omitempty"`
	HardwareFit  bool                    `json:"hardware_fit"`
}

type recommendedVariant struct {
	Name           string `json:"name"`
	Format         string `json:"format"`
	Quantization   string `json:"quantization,omitempty"`
	PrecisionLabel string `json:"precision_label,omitempty"`
	VRAMReqMiB     int    `json:"vram_required_mib,omitempty"`
	GPUCountMin    int    `json:"gpu_count_min,omitempty"`
	DiskSizeMiB    int    `json:"disk_size_mib,omitempty"`
}

type recommendedEngine struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	Image      string `json:"image,omitempty"`
	ColdStartS []int  `json:"cold_start_s,omitempty"`
}

type recommendedEngineStatus struct {
	Available     bool `json:"available"`
	Installed     bool `json:"installed"`
	NeedsDownload bool `json:"needs_download"`
	NeedsBuild    bool `json:"needs_build"`
}

type recommendedPerformance struct {
	Source         string     `json:"source"`
	TokensPerSec   [2]float64 `json:"tokens_per_second,omitempty"`
	TTFTMs         [2]float64 `json:"ttft_ms,omitempty"`
	ThroughputNote string     `json:"throughput_note,omitempty"`
}

type recommendedGolden struct {
	Exists bool   `json:"exists"`
	Source string `json:"source,omitempty"`
}

type recommendedModelStatus struct {
	LocalAvailable     bool   `json:"local_available"`
	DownloadSource     string `json:"download_source,omitempty"`
	DownloadRepo       string `json:"download_repo,omitempty"`
	EstDownloadTimeMin int    `json:"estimated_download_time_min,omitempty"`
}

// buildOnboardingDeps wires the onboarding-related tool dependencies.
func buildOnboardingDeps(ac *appContext, deps *mcp.ToolDeps) {
	deps.RecommendModels = func(ctx context.Context) (json.RawMessage, error) {
		return buildModelRecommendations(ctx, ac, deps)
	}
	if deps.StackInit != nil {
		baseStackInit := deps.StackInit
		deps.StackInit = func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error) {
			data, err := baseStackInit(ctx, tier, allowDownload)
			if err != nil {
				return data, err
			}
			result, decodeErr := decodeStackInitResult(data)
			if decodeErr != nil || !result.AllReady || normalizeOnboardingInitTier(tier) != "k3s" || deps.ScanEngines == nil {
				return data, nil
			}

			resp := stackInitResponse{
				InitResult:            result,
				EngineImportTriggered: true,
			}
			importCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if _, scanErr := deps.ScanEngines(importCtx, "auto", true); scanErr != nil {
				resp.EngineImportWarning = scanErr.Error()
				slog.Warn("stack init: post-init engine import failed", "tier", tier, "error", scanErr)
			}
			encoded, marshalErr := json.Marshal(resp)
			if marshalErr != nil {
				return data, nil
			}
			return encoded, nil
		}
	}
	if deps.DeployRun != nil {
		baseDeployRun := deps.DeployRun
		deps.DeployRun = func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
			onPhase func(string, string), onEngineProgress func(engine.ProgressEvent),
		) (json.RawMessage, error) {
			data, err := baseDeployRun(ctx, model, engineType, slot, configOverrides, noPull, onPhase, onEngineProgress)
			if err != nil {
				return data, err
			}
			if !onboardingDeployCompleted(data) || deps.SetConfig == nil {
				return data, nil
			}
			persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if cfgErr := deps.SetConfig(persistCtx, "onboarding_completed", "true"); cfgErr != nil {
				slog.Warn("onboarding deploy: failed to mark onboarding completed", "error", cfgErr)
			}
			return data, nil
		}
	}
}

type stackInitResponse struct {
	stack.InitResult
	EngineImportTriggered bool   `json:"engine_import_triggered,omitempty"`
	EngineImportWarning   string `json:"engine_import_warning,omitempty"`
}

func decodeStackInitResult(data json.RawMessage) (stack.InitResult, error) {
	var result stack.InitResult
	if err := json.Unmarshal(data, &result); err != nil {
		return stack.InitResult{}, err
	}
	return result, nil
}

func onboardingDeployCompleted(data json.RawMessage) bool {
	result, err := decodeOnboardingDeployResult(data)
	if err != nil {
		return false
	}
	return result.ready()
}

// buildModelRecommendations iterates all catalog model assets, matches them
// against detected hardware, and returns ranked recommendations with full
// metadata suitable for onboarding UI cards.
func buildModelRecommendations(ctx context.Context, ac *appContext, deps *mcp.ToolDeps) (json.RawMessage, error) {
	cat := ac.cat
	db := ac.db
	kStore := ac.kStore

	if cat == nil {
		return nil, fmt.Errorf("catalog not loaded")
	}

	// Step 1: Detect hardware and build HardwareInfo
	hwInfo := buildHardwareInfo(ctx, cat, ac.rt.Name())

	// Step 2: Get hardware profile name
	hwProfile := hwInfo.HardwareProfile
	if hwProfile == "" {
		hwProfile = detectHWProfile(ctx, cat)
	}

	slog.Info("onboarding recommend: hardware detected",
		"gpu_arch", hwInfo.GPUArch,
		"gpu_vram_mib", hwInfo.GPUVRAMMiB,
		"gpu_count", hwInfo.GPUCount,
		"profile", hwProfile)

	// Step 3: Build lookup maps for installed engines and local models
	installedEngines := buildInstalledEngineMap(ctx, db)
	localModels := buildLocalModelMap(ctx, db)

	// Step 4: Iterate all catalog model assets and build recommendations
	var recs []modelRecommendation
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]

		rec, ok := evaluateModelAsset(ctx, cat, kStore, ma, hwInfo, hwProfile, installedEngines, localModels)
		if !ok {
			continue
		}
		recs = append(recs, rec)
	}

	// Step 5: Sort by fit_score descending
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].FitScore > recs[j].FitScore
	})

	// Step 6: Return top 20
	if len(recs) > 20 {
		recs = recs[:20]
	}

	result := struct {
		HardwareProfile string                `json:"hardware_profile"`
		GPUArch         string                `json:"gpu_arch"`
		GPUVRAMMiB      int                   `json:"gpu_vram_mib"`
		GPUCount        int                   `json:"gpu_count"`
		TotalModels     int                   `json:"total_models_evaluated"`
		Recommendations []modelRecommendation `json:"recommendations"`
	}{
		HardwareProfile: hwProfile,
		GPUArch:         hwInfo.GPUArch,
		GPUVRAMMiB:      hwInfo.GPUVRAMMiB,
		GPUCount:        hwInfo.GPUCount,
		TotalModels:     len(cat.ModelAssets),
		Recommendations: recs,
	}

	return json.Marshal(result)
}

// evaluateModelAsset checks whether a single model asset is compatible with the
// hardware, and if so builds a full recommendation entry. Returns false if the
// model cannot run on this hardware.
func evaluateModelAsset(
	ctx context.Context,
	cat *knowledge.Catalog,
	kStore *knowledge.Store,
	ma *knowledge.ModelAsset,
	hwInfo knowledge.HardwareInfo,
	hwProfile string,
	installedEngines map[string]*state.Engine,
	localModels map[string]*state.Model,
) (modelRecommendation, bool) {
	modelName := ma.Metadata.Name

	// Use ResolveVariantForPull which internally calls InferEngineType + findModelVariant.
	// This is the exported API that gives us both the variant and engine type.
	_, variant, engineType, err := cat.ResolveVariantForPull(modelName, hwInfo)
	if err != nil || variant == nil {
		slog.Debug("onboarding recommend: no compatible variant", "model", modelName, "error", err)
		return modelRecommendation{}, false
	}

	// Find the engine asset to get image/cold_start info
	engineAsset := cat.FindEngineByName(engineType, hwInfo)

	// Build a minimal ResolvedConfig for CheckFit
	resolved := buildMinimalResolvedConfig(variant, engineAsset, hwInfo)
	fit := knowledge.CheckFit(resolved, hwInfo)

	// Parse expected performance from variant
	perf := variant.ParsedExpectedPerf()

	// Check engine installation status
	engineStatus := checkEngineStatus(engineType, engineAsset, installedEngines)

	// Check model local availability
	localModel := localModels[strings.ToLower(modelName)]
	modelAvailable := localModel != nil

	// Check for golden config in knowledge store
	goldenExists := false
	goldenSource := ""
	if kStore != nil && hwProfile != "" {
		resp, qErr := kStore.Search(ctx, knowledge.SearchParams{
			Hardware: hwProfile,
			Engine:   engineType,
			Model:    modelName,
			Status:   "golden",
			Limit:    1,
		})
		if qErr == nil && len(resp.Results) > 0 {
			goldenExists = true
			goldenSource = "local"
		}
	}

	// Compute fit score
	score := computeFitScore(hwInfo, variant, fit, engineStatus, modelAvailable, goldenExists)

	// Build recommendation reason
	reason := buildRecommendationReason(ma, variant, engineType, fit, perf, hwInfo)

	// Build download source info
	dlSource, dlRepo := extractDownloadSource(ma, variant)

	// Estimate download time (rough: assume 50 MB/s)
	estimatedDLMin := 0
	if perf.DiskMiB > 0 && !modelAvailable {
		estimatedDLMin = perf.DiskMiB / (50 * 60) // MiB / (50 MB/s * 60s)
		if estimatedDLMin < 1 && perf.DiskMiB > 0 {
			estimatedDLMin = 1
		}
	}

	// Build quantization label
	quantLabel := ""
	if variant.DefaultConfig != nil {
		if q, ok := variant.DefaultConfig["quantization"].(string); ok {
			quantLabel = strings.ToUpper(q)
		}
	}
	if quantLabel == "" && variant.Format != "" {
		quantLabel = strings.ToUpper(variant.Format)
	}

	// Active params from model metadata (e.g. "3B" in MoE models)
	activeParams := extractActiveParams(ma)

	rec := modelRecommendation{
		ModelName:    modelName,
		ModelType:    ma.Metadata.Type,
		Family:       ma.Metadata.Family,
		ParamCount:   ma.Metadata.ParameterCount,
		ActiveParams: activeParams,
		Variant: &recommendedVariant{
			Name:           variant.Name,
			Format:         variant.Format,
			Quantization:   quantLabel,
			PrecisionLabel: quantLabel,
			VRAMReqMiB:     variant.Hardware.VRAMMinMiB,
			GPUCountMin:    variant.Hardware.GPUCountMin,
			DiskSizeMiB:    perf.DiskMiB,
		},
		EngineStatus: engineStatus,
		Performance: recommendedPerformance{
			Source:       performanceSource(perf),
			TokensPerSec: perf.TokensPerSecond,
		},
		GoldenConfig: recommendedGolden{
			Exists: goldenExists,
			Source: goldenSource,
		},
		ModelStatus: recommendedModelStatus{
			LocalAvailable:     modelAvailable,
			DownloadSource:     dlSource,
			DownloadRepo:       dlRepo,
			EstDownloadTimeMin: estimatedDLMin,
		},
		FitScore:    score,
		Reason:      reason,
		FitWarnings: fit.Warnings,
		HardwareFit: fit.Fit,
	}

	if engineAsset != nil {
		rec.Engine = &recommendedEngine{
			Type: engineAsset.Metadata.Type,
			Name: engineAsset.Metadata.Name,
		}
		if engineAsset.Image.Name != "" {
			tag := engineAsset.Image.Tag
			if tag == "" {
				tag = "latest"
			}
			rec.Engine.Image = engineAsset.Image.Name + ":" + tag
		}
		if len(engineAsset.TimeConstraints.ColdStartS) > 0 {
			rec.Engine.ColdStartS = engineAsset.TimeConstraints.ColdStartS
		}
	}

	return rec, true
}

// buildMinimalResolvedConfig creates a ResolvedConfig with just enough data for CheckFit.
func buildMinimalResolvedConfig(variant *knowledge.ModelVariant, engine *knowledge.EngineAsset, hw knowledge.HardwareInfo) *knowledge.ResolvedConfig {
	rc := &knowledge.ResolvedConfig{
		Config:     make(map[string]any),
		Provenance: make(map[string]string),
	}

	// Copy default config from variant
	for k, v := range variant.DefaultConfig {
		rc.Config[k] = v
		rc.Provenance[k] = "L0-yaml"
	}

	// Set engine info
	if engine != nil {
		rc.Engine = engine.Metadata.Name
		if engine.Image.Name != "" {
			tag := engine.Image.Tag
			if tag == "" {
				tag = "latest"
			}
			rc.EngineImage = engine.Image.Name + ":" + tag
		}
	}

	// Set VRAM estimate
	perf := variant.ParsedExpectedPerf()
	if perf.VRAMMiB > 0 {
		rc.EstimatedVRAMMiB = perf.VRAMMiB
	}
	if perf.VRAMMiB > 0 || perf.RAMMiB > 0 || perf.DiskMiB > 0 {
		rc.ResourceEstimate = &knowledge.ResourceEstimate{
			VRAMMiB: perf.VRAMMiB,
			RAMMiB:  perf.RAMMiB,
			DiskMiB: perf.DiskMiB,
		}
	}

	return rc
}

// computeFitScore calculates a 0-100 score for how well a model fits the hardware.
func computeFitScore(
	hw knowledge.HardwareInfo,
	variant *knowledge.ModelVariant,
	fit *knowledge.FitReport,
	engineStatus recommendedEngineStatus,
	modelAvailable bool,
	goldenExists bool,
) int {
	if !fit.Fit {
		return 0
	}

	score := 50 // Base: hardware matches

	// +20 if exact arch match (not wildcard)
	if variant.Hardware.GPUArch == hw.GPUArch {
		score += 20
	}

	// +10 if golden config exists
	if goldenExists {
		score += 10
	}

	// +5 if model is locally available
	if modelAvailable {
		score += 5
	}

	// +5 if engine is locally installed
	if engineStatus.Installed {
		score += 5
	}

	// -10 if needs >1 GPU (complexity penalty for beginners)
	if variant.Hardware.GPUCountMin > 1 {
		score -= 10
	}

	// VRAM utilization sweet spot
	if hw.GPUVRAMMiB > 0 && variant.Hardware.VRAMMinMiB > 0 {
		utilization := float64(variant.Hardware.VRAMMinMiB) / float64(hw.GPUVRAMMiB) * 100
		if utilization >= 60 && utilization <= 85 {
			score += 10 // sweet spot
		} else if utilization > 95 {
			score -= 5 // too tight
		}
	}

	// Clamp to [0, 100]
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return score
}

// checkEngineStatus checks whether an engine is installed locally.
func checkEngineStatus(engineType string, engineAsset *knowledge.EngineAsset, installed map[string]*state.Engine) recommendedEngineStatus {
	status := recommendedEngineStatus{
		NeedsDownload: true,
	}

	typeLower := strings.ToLower(engineType)

	// Check installed engines map by type
	for key, eng := range installed {
		if key == typeLower || (eng != nil && strings.ToLower(eng.Type) == typeLower) {
			status.Available = true
			status.Installed = true
			status.NeedsDownload = false
			return status
		}
	}

	// Also try by engine asset name if available
	if engineAsset != nil {
		nameLower := strings.ToLower(engineAsset.Metadata.Name)
		for key, eng := range installed {
			if key == nameLower || (eng != nil && strings.Contains(strings.ToLower(eng.Image), strings.ToLower(engineAsset.Image.Name))) {
				status.Available = true
				status.Installed = true
				status.NeedsDownload = false
				return status
			}
		}
	}

	// Engine exists in catalog but not installed
	if engineAsset != nil {
		status.Available = true // available to download
	}

	return status
}

// buildInstalledEngineMap returns a map of engine type (lowercase) -> Engine record.
func buildInstalledEngineMap(ctx context.Context, db *state.DB) map[string]*state.Engine {
	m := make(map[string]*state.Engine)
	if db == nil {
		return m
	}
	engines, err := db.ListEngines(ctx)
	if err != nil {
		slog.Warn("onboarding recommend: failed to list engines", "error", err)
		return m
	}
	for _, e := range engines {
		if e == nil || !e.Available {
			continue
		}
		m[strings.ToLower(e.Type)] = e
		// Also index by image name for cross-reference
		if e.Image != "" {
			m[strings.ToLower(e.Image)] = e
		}
	}
	return m
}

// buildLocalModelMap returns a map of model name (lowercase) -> Model record.
func buildLocalModelMap(ctx context.Context, db *state.DB) map[string]*state.Model {
	m := make(map[string]*state.Model)
	if db == nil {
		return m
	}
	models, err := db.ListModels(ctx)
	if err != nil {
		slog.Warn("onboarding recommend: failed to list models", "error", err)
		return m
	}
	for _, mdl := range models {
		if mdl == nil {
			continue
		}
		m[strings.ToLower(mdl.Name)] = mdl
	}
	return m
}

// extractDownloadSource finds the best download source for a model.
func extractDownloadSource(ma *knowledge.ModelAsset, variant *knowledge.ModelVariant) (string, string) {
	// Variant-specific source takes priority
	if variant.Source != nil && variant.Source.Repo != "" {
		return variant.Source.Type, variant.Source.Repo
	}
	// Fall back to global model sources
	for _, src := range ma.Storage.Sources {
		if src.Repo != "" {
			return src.Type, src.Repo
		}
	}
	return "", ""
}

// extractActiveParams extracts active parameter count from model name or metadata.
// For MoE models like "qwen3-30b-a3b", the active params portion is "3B".
func extractActiveParams(ma *knowledge.ModelAsset) string {
	name := strings.ToLower(ma.Metadata.Name)
	// Look for "-aXb" pattern (e.g. "a3b" in "qwen3-30b-a3b")
	parts := strings.Split(name, "-")
	for _, p := range parts {
		if len(p) >= 2 && p[0] == 'a' && p[len(p)-1] == 'b' {
			// Check if middle is a number
			mid := p[1 : len(p)-1]
			isNum := true
			for _, c := range mid {
				if c < '0' || c > '9' {
					isNum = false
					break
				}
			}
			if isNum && len(mid) > 0 {
				return strings.ToUpper(mid) + "B"
			}
		}
	}
	return ""
}

// performanceSource returns the source label for performance data.
func performanceSource(perf knowledge.ExpectedPerf) string {
	if perf.TokensPerSecond[0] > 0 || perf.TokensPerSecond[1] > 0 {
		return "catalog_estimate"
	}
	return "unknown"
}

// buildRecommendationReason generates a human-readable recommendation reason.
func buildRecommendationReason(
	ma *knowledge.ModelAsset,
	variant *knowledge.ModelVariant,
	engineType string,
	fit *knowledge.FitReport,
	perf knowledge.ExpectedPerf,
	hw knowledge.HardwareInfo,
) string {
	var parts []string

	// Model architecture info
	activeParams := extractActiveParams(ma)
	if activeParams != "" {
		parts = append(parts, fmt.Sprintf("MoE architecture, only %s active params", activeParams))
	}

	// GPU count
	if variant.Hardware.GPUCountMin <= 1 {
		parts = append(parts, "fits in single GPU")
	} else {
		parts = append(parts, fmt.Sprintf("requires %d GPUs", variant.Hardware.GPUCountMin))
	}

	// VRAM utilization
	if hw.GPUVRAMMiB > 0 && variant.Hardware.VRAMMinMiB > 0 {
		util := float64(variant.Hardware.VRAMMinMiB) / float64(hw.GPUVRAMMiB) * 100
		if util <= 50 {
			parts = append(parts, "lightweight VRAM usage")
		} else if util <= 85 {
			parts = append(parts, "good VRAM utilization")
		} else {
			parts = append(parts, "tight VRAM fit")
		}
	}

	// Performance hint
	if perf.TokensPerSecond[1] > 0 {
		parts = append(parts, fmt.Sprintf("~%.0f tok/s expected", perf.TokensPerSecond[1]))
	}

	// Warnings
	if !fit.Fit {
		parts = append(parts, "may not fit: "+fit.Reason)
	}

	if len(parts) == 0 {
		return fmt.Sprintf("compatible with %s via %s", hw.GPUArch, engineType)
	}
	return strings.Join(parts, ", ")
}
