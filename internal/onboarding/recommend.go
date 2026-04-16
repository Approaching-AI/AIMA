package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

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
	installedEngines, allEngines := buildInstalledEngineMap(ctx, db)
	localModels := buildLocalModelMap(ctx, db)

	// Step 4a: First pass — compute the largest model (by parameter count, in
	// billions) that actually fits this hardware. The number anchors the
	// "largest fittable" bonus in the second pass so the wizard prefers a
	// model that uses the box's capacity instead of always picking the
	// smallest one. A box with two RTX 4090s (49 GB total) will end up with
	// a maxFit of ~120B for MoE variants, ~30B for dense ones — that becomes
	// the reference scale for everyone else.
	maxFitBillion := 0.0
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		_, variant, _, err := cat.ResolveVariantForPull(ma.Metadata.Name, hwInfo)
		if err != nil || variant == nil {
			continue
		}
		engineAsset := cat.FindEngineByName(variant.Engine, hwInfo)
		if !engineDeployable(engineAsset, hwInfo.Platform, allEngines) {
			continue
		}
		resolved := buildMinimalResolvedConfig(variant, engineAsset, hwInfo)
		fit := knowledge.CheckFit(resolved, hwInfo)
		if !fit.Fit {
			continue
		}
		if b := parseParamBillion(ma.Metadata.ParameterCount); b > maxFitBillion {
			maxFitBillion = b
		}
	}

	// Step 4b: Second pass — evaluate every model with the new spread-wide
	// scoring formula (modality bonus, recency bonus, largest-fittable bonus).
	var recs []ModelRecommendation
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]

		rec, ok := evaluateModelAsset(ctx, cat, kStore, ma, hwInfo, hwProfile, installedEngines, allEngines, localModels, locale, maxFitBillion)
		if !ok {
			continue
		}
		recs = append(recs, rec)
	}

	// Step 5: Sort by fit_score desc, then param size desc (largest first
	// when scores tie), then released_at desc (newer wins next), then name
	// asc (stable). The chained tiebreakers eliminate the random-map ordering
	// that UAT users complained about ("the list reorders every refresh").
	sort.Slice(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if a.FitScore != b.FitScore {
			return a.FitScore > b.FitScore
		}
		ap, bp := parseParamBillion(a.ParamCount), parseParamBillion(b.ParamCount)
		if ap != bp {
			return ap > bp
		}
		ad, bd := parseReleasedAt(a.ReleasedAt), parseReleasedAt(b.ReleasedAt)
		if !ad.Equal(bd) {
			return ad.After(bd)
		}
		return a.ModelName < b.ModelName
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
	allEngines []*state.Engine,
	localModels map[string]*state.Model,
	locale string,
	maxFitBillion float64,
) (ModelRecommendation, bool) {
	modelName := ma.Metadata.Name

	_, variant, engineType, err := cat.ResolveVariantForPull(modelName, hwInfo)
	if err != nil || variant == nil {
		slog.Debug("onboarding recommend: no compatible variant", "model", modelName, "error", err)
		return ModelRecommendation{}, false
	}

	engineAsset := cat.FindEngineByName(engineType, hwInfo)

	engineStatus := checkEngineStatus(engineType, engineAsset, installedEngines)

	// Verify the engine is actually deployable on this platform. An engine
	// is deployable if its exact image:tag exists locally OR it has a valid
	// download source for the current OS/arch. This catches cases where the
	// engine type was "installed" via a container scan (e.g. zhiwen-llama-box
	// registered as type "llamacpp") but the catalog engine asset has no
	// image/binary for this platform.
	if !engineDeployable(engineAsset, hwInfo.Platform, allEngines) {
		if altVariant, altEngineType, altAsset, altStatus, ok := findDeployableEngineVariant(cat, ma, hwInfo, installedEngines, allEngines); ok {
			slog.Debug("onboarding recommend: switched to deployable engine variant",
				"model", modelName,
				"original_engine", engineType,
				"deployable_engine", altEngineType)
			variant = altVariant
			engineType = altEngineType
			engineAsset = altAsset
			engineStatus = altStatus
		} else {
			slog.Debug("onboarding recommend: no deployable engine for any variant",
				"model", modelName, "platform", hwInfo.Platform)
			return ModelRecommendation{}, false
		}
	} else if !engineStatus.Installed {
		// Engine is deployable (pullable) but not yet installed. Prefer a
		// variant whose engine is both deployable AND already installed.
		if altVariant, altEngineType, altAsset, altStatus, ok := findDeployableEngineVariant(cat, ma, hwInfo, installedEngines, allEngines); ok {
			slog.Debug("onboarding recommend: prefer installed+deployable engine variant",
				"model", modelName,
				"original_engine", engineType,
				"installed_engine", altEngineType)
			variant = altVariant
			engineType = altEngineType
			engineAsset = altAsset
			engineStatus = altStatus
		}
		// If no installed alternative, keep the original (it's deployable, just needs pull)
	}

	resolved := buildMinimalResolvedConfig(variant, engineAsset, hwInfo)
	fit := knowledge.CheckFit(resolved, hwInfo)

	perf := variant.ParsedExpectedPerf()

	localModel := findLocalModel(strings.ToLower(modelName), localModels)
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

	score := computeFitScore(ma, hwInfo, variant, fit, engineStatus, modelAvailable, goldenExists, maxFitBillion)

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
		ReleasedAt:   ma.Metadata.ReleasedAt,
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

// computeFitScore returns an unbounded integer score so meaningful gaps are
// preserved (the previous 0-100 clamp produced ten-way ties at 100). Higher
// is better. Components:
//
//	+100 base for any model that fits
//	+modality bonus (LLM dominates, then VLM/embedding/asr/tts)
//	+local-asset bonuses (cached weights, installed engine, golden config)
//	+hardware-match bonuses (arch, vram sweet-spot)
//	+largest-fittable bonus (proportional to params/maxFit on this box)
//	+recency bonus (newer release date wins)
//	-multi-gpu penalty (per extra GPU required)
//	-vram-oversubscribed penalty
func computeFitScore(
	ma *knowledge.ModelAsset,
	hw knowledge.HardwareInfo,
	variant *knowledge.ModelVariant,
	fit *knowledge.FitReport,
	engineStatus RecommendedEngineStatus,
	modelAvailable bool,
	goldenExists bool,
	maxFitBillion float64,
) int {
	if !fit.Fit {
		return 0
	}

	score := 100 // Base: hardware fits

	score += modalityWeight(ma.Metadata.Type)

	if modelAvailable {
		score += 200
	}
	if engineStatus.Installed {
		score += 100
	}
	if goldenExists {
		score += 80
	}

	if variant.Hardware.GPUArch == hw.GPUArch {
		score += 60
	}

	if hw.GPUVRAMMiB > 0 && variant.Hardware.VRAMMinMiB > 0 {
		utilization := float64(variant.Hardware.VRAMMinMiB) / float64(hw.GPUVRAMMiB) * 100
		switch {
		case utilization <= 50:
			score += 20
		case utilization <= 85:
			score += 50
		case utilization <= 95:
			score += 30
		default:
			score -= 50
		}
	}

	score += largestFittableBonus(ma, maxFitBillion)
	score += recencyBonus(ma.Metadata.ReleasedAt)

	if variant.Hardware.GPUCountMin > 1 {
		score -= 30 * (variant.Hardware.GPUCountMin - 1)
	}

	if score < 0 {
		score = 0
	}
	return score
}

// modalityWeight implements the "LLM first" rule by giving each modality
// a different additive bonus. LLM dominates because that is the default
// answer for a fresh edge box; the other modalities still receive points
// so they can surface when no LLM fits.
func modalityWeight(modelType string) int {
	switch strings.ToLower(strings.TrimSpace(modelType)) {
	case "llm":
		return 200
	case "vlm":
		return 150
	case "embedding", "rerank":
		return 90
	case "asr", "tts":
		return 60
	case "image", "video", "t2i", "t2v", "i2v":
		return 70
	default:
		return 40
	}
}

// largestFittableBonus rewards the largest model that still fits the box.
// Bonus scales linearly with params/maxFit, capped at +120. A 30B model
// on a box whose maxFit is 30B gets the full +120; a 4B model on the same
// box gets +16. Falls back to 0 when params are unparseable.
func largestFittableBonus(ma *knowledge.ModelAsset, maxFitBillion float64) int {
	if maxFitBillion <= 0 {
		return 0
	}
	b := parseParamBillion(ma.Metadata.ParameterCount)
	if b <= 0 {
		return 0
	}
	ratio := b / maxFitBillion
	if ratio > 1 {
		ratio = 1
	}
	return int(ratio * 120)
}

// recencyBonus rewards newer models. A model released this month gets the
// full +100; the bonus drops by ~3 points per month and reaches 0 around
// 33 months. Models without released_at metadata get no bonus (and no
// penalty) so the catalog can be backfilled incrementally without
// destabilizing rankings.
func recencyBonus(releasedAt string) int {
	t := parseReleasedAt(releasedAt)
	if t.IsZero() {
		return 0
	}
	months := int(time.Since(t).Hours() / 24 / 30)
	if months < 0 {
		months = 0
	}
	bonus := 100 - months*3
	if bonus < 0 {
		bonus = 0
	}
	return bonus
}

// parseParamBillion turns a free-form parameter_count string ("8B", "1.7B",
// "220M", "30B-A3B" for MoE) into a count in billions. Returns 0 if the
// value is unparseable so callers can branch on "missing".
func parseParamBillion(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// MoE annotations like "30B-A3B" or "122B-A10B" — keep the leading
	// total-params number, drop the active-params suffix.
	if idx := strings.Index(strings.ToUpper(s), "-A"); idx > 0 {
		s = s[:idx]
	}
	upper := strings.ToUpper(s)
	multiplier := 1.0
	switch {
	case strings.HasSuffix(upper, "B"):
		multiplier = 1.0
		s = upper[:len(upper)-1]
	case strings.HasSuffix(upper, "M"):
		multiplier = 0.001
		s = upper[:len(upper)-1]
	case strings.HasSuffix(upper, "T"):
		multiplier = 1000
		s = upper[:len(upper)-1]
	default:
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v * multiplier
}

// parseReleasedAt accepts YYYY-MM, YYYY-MM-DD, or YYYY. Returns zero time
// when unparseable so the recency bonus degrades gracefully.
func parseReleasedAt(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
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

// engineDeployable checks whether a catalog engine asset can actually be
// deployed on the given platform. An engine is deployable if:
//   - Its exact image:tag is already present locally (container), OR
//   - Its Image.Platforms includes the current platform (container pullable), OR
//   - Its Source supports the platform (native binary downloadable)
//
// This prevents recommending models paired with engines that will fail at
// deploy time (e.g. llamacpp on linux/arm64 where no binary/container exists).
func engineDeployable(engineAsset *knowledge.EngineAsset, platform string, allEngines []*state.Engine) bool {
	if engineAsset == nil || platform == "" {
		return false
	}

	// Blocked engines are never deployable
	if strings.EqualFold(engineAsset.Metadata.Status, "blocked") {
		return false
	}

	// Check 1: engine image already present locally (exact image:tag match).
	// Uses the full engine slice (not the type-keyed map) because multiple
	// engines can share the same type key and the map loses all but the last.
	if engineAsset.Image.Name != "" {
		wantBase := imageBasename(engineAsset.Image.Name)
		wantTag := engineAsset.Image.Tag
		if wantTag == "" {
			wantTag = "latest"
		}
		for _, eng := range allEngines {
			engBase := imageBasename(eng.Image)
			if engBase == wantBase && eng.Tag == wantTag {
				return true
			}
		}
		// Also check compatible tags
		for _, compatTag := range engineAsset.Image.CompatibleTags {
			for _, eng := range allEngines {
				if imageBasename(eng.Image) == wantBase && eng.Tag == compatTag {
					return true
				}
			}
		}
	}

	// Check 2: container image can be pulled for this platform
	for _, p := range engineAsset.Image.Platforms {
		if p == platform {
			return true
		}
	}

	// Check 3: native binary source supports this platform
	if engineAsset.Source != nil && engineAsset.Source.Supports(platform) {
		return true
	}

	return false
}

// imageBasename extracts the last path segment of a container image name.
// "nvcr.io/nvidia/vllm" → "vllm", "ghcr.io/ggml-org/llama.cpp" → "llama.cpp"
func imageBasename(image string) string {
	if idx := strings.LastIndex(image, "/"); idx >= 0 {
		return strings.ToLower(image[idx+1:])
	}
	return strings.ToLower(image)
}

// findDeployableEngineVariant searches through a model's variants for one whose
// engine can actually be deployed on this platform. Prefers variants whose
// engine is both deployable AND already installed locally (faster startup).
// Returns false if no variant has a deployable engine on the current hardware.
func findDeployableEngineVariant(
	cat *knowledge.Catalog,
	ma *knowledge.ModelAsset,
	hwInfo knowledge.HardwareInfo,
	installedEngines map[string]*state.Engine,
	allEngines []*state.Engine,
) (*knowledge.ModelVariant, string, *knowledge.EngineAsset, RecommendedEngineStatus, bool) {
	type candidate struct {
		variant     *knowledge.ModelVariant
		engineType  string
		engineAsset *knowledge.EngineAsset
		status      RecommendedEngineStatus
	}
	var bestInstalled, bestPullable *candidate

	for i := range ma.Variants {
		v := &ma.Variants[i]
		if strings.TrimSpace(v.Compatibility.UnsupportedReason) != "" {
			continue
		}
		if v.Hardware.GPUArch != hwInfo.GPUArch && v.Hardware.GPUArch != "*" {
			continue
		}
		if v.Hardware.GPUCountMin > 0 && hwInfo.GPUCount > 0 && hwInfo.GPUCount < v.Hardware.GPUCountMin {
			continue
		}
		engineAsset := cat.FindEngineByName(v.Engine, hwInfo)
		if !engineDeployable(engineAsset, hwInfo.Platform, allEngines) {
			continue
		}
		// Verify hardware fit
		resolved := buildMinimalResolvedConfig(v, engineAsset, hwInfo)
		fit := knowledge.CheckFit(resolved, hwInfo)
		if !fit.Fit {
			continue
		}
		status := checkEngineStatus(v.Engine, engineAsset, installedEngines)
		c := &candidate{variant: v, engineType: v.Engine, engineAsset: engineAsset, status: status}
		if status.Installed && bestInstalled == nil {
			bestInstalled = c
		}
		if bestPullable == nil {
			bestPullable = c
		}
	}

	// Prefer installed engine (already local, no download needed)
	if bestInstalled != nil {
		return bestInstalled.variant, bestInstalled.engineType, bestInstalled.engineAsset, bestInstalled.status, true
	}
	if bestPullable != nil {
		return bestPullable.variant, bestPullable.engineType, bestPullable.engineAsset, bestPullable.status, true
	}
	return nil, "", nil, RecommendedEngineStatus{}, false
}

// buildInstalledEngineMap returns a type-based lookup map AND a full slice of
// all available engines. The map is for checkEngineStatus (type-based matching);
// the slice is for engineDeployable (exact image:tag matching that needs ALL
// engines, not just the last-one-wins per type key).
func buildInstalledEngineMap(ctx context.Context, db *state.DB) (map[string]*state.Engine, []*state.Engine) {
	m := make(map[string]*state.Engine)
	if db == nil {
		return m, nil
	}
	engines, err := db.ListEngines(ctx)
	if err != nil {
		slog.Warn("onboarding recommend: failed to list engines", "error", err)
		return m, nil
	}
	var all []*state.Engine
	for _, e := range engines {
		if e == nil || !e.Available {
			continue
		}
		all = append(all, e)
		m[strings.ToLower(e.Type)] = e
		if e.Image != "" {
			m[strings.ToLower(e.Image)] = e
		}
	}
	return m, all
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

// findLocalModel looks up a catalog model name in the local model map.
// First tries exact match, then prefix match (e.g. catalog "qwen3-30b-a3b"
// matches local "qwen3-30b-a3b-instruct-2507"). This handles naming
// differences between the catalog and model files on disk.
func findLocalModel(catalogName string, localModels map[string]*state.Model) *state.Model {
	if m, ok := localModels[catalogName]; ok {
		return m
	}
	// Prefix match: local model name starts with catalog name
	for key, m := range localModels {
		if strings.HasPrefix(key, catalogName+"-") || strings.HasPrefix(key, catalogName+"_") {
			return m
		}
	}
	return nil
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
