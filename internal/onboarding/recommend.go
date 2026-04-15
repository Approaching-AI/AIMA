package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/knowledge"
)

// Recommend iterates all catalog model assets, matches them against detected
// hardware, and returns ranked recommendations with full metadata suitable for
// onboarding UI cards. The locale parameter ("zh", "en", ...) controls the
// language of user-facing reason strings. Unknown locales fall back to English.
func Recommend(ctx context.Context, deps *Deps, locale string) (RecommendResult, error) {
	if deps == nil || deps.Cat == nil {
		return RecommendResult{}, fmt.Errorf("catalog not loaded")
	}
	cat := deps.Cat
	db := deps.DB
	kStore := deps.KStore

	// Step 1: Detect hardware
	var hwInfo knowledge.HardwareInfo
	if deps.BuildHardwareInfo != nil {
		hwInfo = deps.BuildHardwareInfo(ctx)
	}

	// Step 2: Get hardware profile name
	hwProfile := hwInfo.HardwareProfile
	if hwProfile == "" && deps.DetectHWProfile != nil {
		hwProfile = deps.DetectHWProfile(ctx)
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
	var recs []ModelRecommendation
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]

		rec, ok := evaluateModelAsset(ctx, cat, kStore, ma, hwInfo, hwProfile, installedEngines, localModels, locale)
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

	return RecommendResult{
		HardwareProfile: hwProfile,
		GPUArch:         hwInfo.GPUArch,
		GPUVRAMMiB:      hwInfo.GPUVRAMMiB,
		GPUCount:        hwInfo.GPUCount,
		TotalModels:     len(cat.ModelAssets),
		Recommendations: recs,
	}, nil
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
	locale string,
) (ModelRecommendation, bool) {
	modelName := ma.Metadata.Name

	_, variant, engineType, err := cat.ResolveVariantForPull(modelName, hwInfo)
	if err != nil || variant == nil {
		slog.Debug("onboarding recommend: no compatible variant", "model", modelName, "error", err)
		return ModelRecommendation{}, false
	}

	engineAsset := cat.FindEngineByName(engineType, hwInfo)

	resolved := buildMinimalResolvedConfig(variant, engineAsset, hwInfo)
	fit := knowledge.CheckFit(resolved, hwInfo)

	perf := variant.ParsedExpectedPerf()

	engineStatus := checkEngineStatus(engineType, engineAsset, installedEngines)

	localModel := localModels[strings.ToLower(modelName)]
	modelAvailable := localModel != nil

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

	score := computeFitScore(hwInfo, variant, fit, engineStatus, modelAvailable, goldenExists)

	reason := buildRecommendationReason(ma, variant, engineType, fit, perf, hwInfo, locale)

	dlSource, dlRepo := extractDownloadSource(ma, variant)

	estimatedDLMin := 0
	if perf.DiskMiB > 0 && !modelAvailable {
		estimatedDLMin = perf.DiskMiB / (50 * 60)
		if estimatedDLMin < 1 && perf.DiskMiB > 0 {
			estimatedDLMin = 1
		}
	}

	quantLabel := ""
	if variant.DefaultConfig != nil {
		if q, ok := variant.DefaultConfig["quantization"].(string); ok {
			quantLabel = strings.ToUpper(q)
		}
	}
	if quantLabel == "" && variant.Format != "" {
		quantLabel = strings.ToUpper(variant.Format)
	}

	activeParams := extractActiveParams(ma)

	rec := ModelRecommendation{
		ModelName:    modelName,
		ModelType:    ma.Metadata.Type,
		Family:       ma.Metadata.Family,
		ParamCount:   ma.Metadata.ParameterCount,
		ActiveParams: activeParams,
		Variant: &RecommendedVariant{
			Name:           variant.Name,
			Format:         variant.Format,
			Quantization:   quantLabel,
			PrecisionLabel: quantLabel,
			VRAMReqMiB:     variant.Hardware.VRAMMinMiB,
			GPUCountMin:    variant.Hardware.GPUCountMin,
			DiskSizeMiB:    perf.DiskMiB,
		},
		EngineStatus: engineStatus,
		Performance: RecommendedPerformance{
			Source:       performanceSource(perf),
			TokensPerSec: perf.TokensPerSecond,
		},
		GoldenConfig: RecommendedGolden{
			Exists: goldenExists,
			Source: goldenSource,
		},
		ModelStatus: RecommendedModelStatus{
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
		rec.Engine = &RecommendedEngine{
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

	for k, v := range variant.DefaultConfig {
		rc.Config[k] = v
		rc.Provenance[k] = "L0-yaml"
	}

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
	engineStatus RecommendedEngineStatus,
	modelAvailable bool,
	goldenExists bool,
) int {
	if !fit.Fit {
		return 0
	}

	score := 50 // Base: hardware matches

	if variant.Hardware.GPUArch == hw.GPUArch {
		score += 20
	}

	if goldenExists {
		score += 10
	}

	// Locally available assets dominate sort order. UAT showed deploying a
	// recommended model that needed a 16 GB download was a poor first-run
	// experience when several smaller, cached models existed. Boost both
	// model and engine presence aggressively so the wizard's top picks are
	// "deployable in seconds" rather than "deployable in an hour".
	if modelAvailable {
		score += 30
	}

	if engineStatus.Installed {
		score += 15
	}

	if variant.Hardware.GPUCountMin > 1 {
		score -= 10
	}

	if hw.GPUVRAMMiB > 0 && variant.Hardware.VRAMMinMiB > 0 {
		utilization := float64(variant.Hardware.VRAMMinMiB) / float64(hw.GPUVRAMMiB) * 100
		if utilization >= 60 && utilization <= 85 {
			score += 10
		} else if utilization > 95 {
			score -= 5
		}
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return score
}

// checkEngineStatus checks whether an engine is installed locally.
func checkEngineStatus(engineType string, engineAsset *knowledge.EngineAsset, installed map[string]*state.Engine) RecommendedEngineStatus {
	status := RecommendedEngineStatus{
		NeedsDownload: true,
	}

	typeLower := strings.ToLower(engineType)

	for key, eng := range installed {
		if key == typeLower || (eng != nil && strings.ToLower(eng.Type) == typeLower) {
			status.Available = true
			status.Installed = true
			status.NeedsDownload = false
			return status
		}
	}

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

	if engineAsset != nil {
		status.Available = true
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
	if variant.Source != nil && variant.Source.Repo != "" {
		return variant.Source.Type, variant.Source.Repo
	}
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
	parts := strings.Split(name, "-")
	for _, p := range parts {
		if len(p) >= 2 && p[0] == 'a' && p[len(p)-1] == 'b' {
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

// reasonMessages is the i18n table for user-facing recommendation reasons.
// Keys are stable identifiers; values are format strings (Printf-style) or
// plain text. Unknown locales fall back to English.
var reasonMessages = map[string]map[string]string{
	"en": {
		"moe_active":        "MoE architecture, only %s active params",
		"single_gpu":        "fits in single GPU",
		"multi_gpu":         "requires %d GPUs",
		"vram_light":        "lightweight VRAM usage",
		"vram_good":         "good VRAM utilization",
		"vram_tight":        "tight VRAM fit",
		"tps_expected":      "~%.0f tok/s expected",
		"may_not_fit":       "may not fit: %s",
		"generic_compat":    "compatible with %s via %s",
	},
	"zh": {
		"moe_active":        "MoE 架构，仅 %s 激活参数",
		"single_gpu":        "单卡即可运行",
		"multi_gpu":         "需要 %d 张 GPU",
		"vram_light":        "显存占用轻量",
		"vram_good":         "显存利用率良好",
		"vram_tight":        "显存紧张",
		"tps_expected":      "预计 ~%.0f tok/s",
		"may_not_fit":       "可能无法运行：%s",
		"generic_compat":    "兼容 %s，引擎 %s",
	},
}

// tr looks up a localized message by key. Falls back to English if the
// locale or key is unknown. Returns the key itself as a last resort so
// missing translations are visible rather than silent.
func tr(locale, key string) string {
	if m, ok := reasonMessages[locale]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if v, ok := reasonMessages["en"][key]; ok {
		return v
	}
	return key
}

// buildRecommendationReason generates a human-readable recommendation reason.
// The locale parameter selects the language of the reason strings; unknown
// locales fall back to English.
func buildRecommendationReason(
	ma *knowledge.ModelAsset,
	variant *knowledge.ModelVariant,
	engineType string,
	fit *knowledge.FitReport,
	perf knowledge.ExpectedPerf,
	hw knowledge.HardwareInfo,
	locale string,
) string {
	var parts []string

	activeParams := extractActiveParams(ma)
	if activeParams != "" {
		parts = append(parts, fmt.Sprintf(tr(locale, "moe_active"), activeParams))
	}

	if variant.Hardware.GPUCountMin <= 1 {
		parts = append(parts, tr(locale, "single_gpu"))
	} else {
		parts = append(parts, fmt.Sprintf(tr(locale, "multi_gpu"), variant.Hardware.GPUCountMin))
	}

	if hw.GPUVRAMMiB > 0 && variant.Hardware.VRAMMinMiB > 0 {
		util := float64(variant.Hardware.VRAMMinMiB) / float64(hw.GPUVRAMMiB) * 100
		if util <= 50 {
			parts = append(parts, tr(locale, "vram_light"))
		} else if util <= 85 {
			parts = append(parts, tr(locale, "vram_good"))
		} else {
			parts = append(parts, tr(locale, "vram_tight"))
		}
	}

	if perf.TokensPerSecond[1] > 0 {
		parts = append(parts, fmt.Sprintf(tr(locale, "tps_expected"), perf.TokensPerSecond[1]))
	}

	if !fit.Fit {
		parts = append(parts, fmt.Sprintf(tr(locale, "may_not_fit"), fit.Reason))
	}

	if len(parts) == 0 {
		return fmt.Sprintf(tr(locale, "generic_compat"), hw.GPUArch, engineType)
	}
	return strings.Join(parts, ", ")
}
