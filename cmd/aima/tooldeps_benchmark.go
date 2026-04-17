package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	benchpkg "github.com/jguan/aima/internal/benchmark"
	"github.com/jguan/aima/internal/mcp"

	state "github.com/jguan/aima/internal"
)

// modalityParams holds all modality-specific parameters shared between
// benchmark.run and benchmark.matrix handlers.
type modalityParams struct {
	Modality string `json:"modality"`
	Prompt   string `json:"prompt"`
	// VLM
	ImageURLs []string `json:"image_urls"`
	// TTS
	Voice       string   `json:"voice"`
	AudioFormat string   `json:"audio_format"`
	Texts       []string `json:"texts"`
	// ASR
	AudioFiles []string `json:"audio_files"`
	Language   string   `json:"language"`
	// T2I / T2V
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	Steps         int     `json:"steps"`
	GuidanceScale float64 `json:"guidance_scale"`
	NumImages     int     `json:"num_images"`
	// T2V
	DurationS     float64 `json:"duration_s"`
	FPS           int     `json:"fps"`
	InputImageURL string  `json:"input_image_url"`
}

func (mp *modalityParams) resolvedModality() string {
	if mp.Modality == "" {
		return "llm"
	}
	return mp.Modality
}

// buildBenchmarkDeps wires benchmark.record, benchmark.run, benchmark.matrix,
// benchmark.list, and knowledge.promote tools.
func buildBenchmarkDeps(ac *appContext, deps *mcp.ToolDeps, resolveEndpoint func(explicit, model string) string) {
	db := ac.db
	kStore := ac.kStore
	rt := ac.rt
	nativeRt := ac.nativeRt
	dockerRt := ac.dockerRt

	selectDeployConfig := func(ctx context.Context, modelName, engineName string, explicit map[string]any) map[string]any {
		matches := findMatchingDeployments(ctx, modelName, nil, rt, nativeRt, dockerRt)
		return selectReadyDeployConfig(engineName, explicit, matches)
	}

	deps.RecordBenchmark = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Hardware        string         `json:"hardware"`
			Engine          string         `json:"engine"`
			Model           string         `json:"model"`
			DeviceID        string         `json:"device_id"`
			Config          map[string]any `json:"config"`
			Modality        string         `json:"modality"`
			Concurrency     int            `json:"concurrency"`
			InputLenBucket  string         `json:"input_len_bucket"`
			OutputLenBucket string         `json:"output_len_bucket"`
			TTFTP50ms       float64        `json:"ttft_ms_p50"`
			TTFTP95ms       float64        `json:"ttft_ms_p95"`
			TPOTP50ms       float64        `json:"tpot_ms_p50"`
			TPOTP95ms       float64        `json:"tpot_ms_p95"`
			ThroughputTPS   float64        `json:"throughput_tps"`
			QPS             float64        `json:"qps"`
			VRAMUsageMiB    int            `json:"vram_usage_mib"`
			RAMUsageMiB     int            `json:"ram_usage_mib"`
			PowerDrawWatts  float64        `json:"power_draw_watts"`
			GPUUtilPct      float64        `json:"gpu_utilization_pct"`
			CPUUsagePct     float64        `json:"cpu_usage_pct"`
			SampleCount     int            `json:"sample_count"`
			Stability       string         `json:"stability"`
			Notes           string         `json:"notes"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse benchmark params: %w", err)
		}
		if p.Concurrency <= 0 {
			p.Concurrency = 1
		}

		// Find or create configuration
		configJSON, err := json.Marshal(p.Config)
		if err != nil {
			return nil, fmt.Errorf("marshal benchmark config: %w", err)
		}
		configHash := fmt.Sprintf("%x", sha256.Sum256(
			[]byte(p.Hardware+"|"+p.Engine+"|"+p.Model+"|"+string(configJSON))))

		cfg, err := db.FindConfigByHash(ctx, configHash)
		if err != nil {
			return nil, err
		}
		if cfg == nil {
			cfg = &state.Configuration{
				ID:         configHash[:16],
				HardwareID: p.Hardware,
				EngineID:   p.Engine,
				ModelID:    p.Model,
				Config:     string(configJSON),
				ConfigHash: configHash,
				Status:     "experiment",
				Source:     "benchmark",
				DeviceID:   p.DeviceID,
			}
			if err := db.InsertConfiguration(ctx, cfg); err != nil {
				return nil, fmt.Errorf("create configuration: %w", err)
			}
		}

		// Insert benchmark result
		benchID := fmt.Sprintf("%x", sha256.Sum256(
			[]byte(cfg.ID+"|"+fmt.Sprintf("%d", time.Now().UnixNano()))))[:16]
		br := &state.BenchmarkResult{
			ID:              benchID,
			ConfigID:        cfg.ID,
			Concurrency:     p.Concurrency,
			InputLenBucket:  p.InputLenBucket,
			OutputLenBucket: p.OutputLenBucket,
			Modality:        storageBenchmarkModality(p.Modality),
			TTFTP50ms:       p.TTFTP50ms,
			TTFTP95ms:       p.TTFTP95ms,
			TPOTP50ms:       p.TPOTP50ms,
			TPOTP95ms:       p.TPOTP95ms,
			ThroughputTPS:   p.ThroughputTPS,
			QPS:             p.QPS,
			VRAMUsageMiB:    p.VRAMUsageMiB,
			RAMUsageMiB:     p.RAMUsageMiB,
			PowerDrawWatts:  p.PowerDrawWatts,
			GPUUtilPct:      p.GPUUtilPct,
			CPUUsagePct:     p.CPUUsagePct,
			SampleCount:     p.SampleCount,
			Stability:       p.Stability,
			TestedAt:        time.Now(),
			AgentModel:      "claude-opus-4.6",
			Notes:           p.Notes,
		}
		if err := db.InsertBenchmarkResult(ctx, br); err != nil {
			return nil, fmt.Errorf("insert benchmark: %w", err)
		}
		postProcessBenchmarkSave(ctx, db, kStore, benchID, cfg.ID, p.Hardware, p.Engine, p.Model, p.ThroughputTPS)

		return json.Marshal(map[string]any{
			"benchmark_id": benchID,
			"config_id":    cfg.ID,
			"status":       "recorded",
			"hardware":     p.Hardware,
			"engine":       p.Engine,
			"model":        p.Model,
		})
	}

	deps.PromoteConfig = func(ctx context.Context, configID, status string) (json.RawMessage, error) {
		validStatuses := map[string]bool{"golden": true, "experiment": true, "archived": true}
		if !validStatuses[status] {
			return nil, fmt.Errorf("invalid status %q: must be golden, experiment, or archived", status)
		}
		// Fetch current config to return old status
		cfg, err := db.GetConfiguration(ctx, configID)
		if err != nil {
			return nil, fmt.Errorf("get configuration: %w", err)
		}
		oldStatus := cfg.Status
		if err := db.UpdateConfigStatus(ctx, configID, status); err != nil {
			return nil, fmt.Errorf("promote config: %w", err)
		}
		return json.Marshal(map[string]any{
			"config_id":  configID,
			"old_status": oldStatus,
			"new_status": status,
			"message":    fmt.Sprintf("Configuration %s promoted from %s to %s", configID, oldStatus, status),
		})
	}

	deps.RunBenchmark = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			modalityParams
			Model          string         `json:"model"`
			Endpoint       string         `json:"endpoint"`
			Concurrency    int            `json:"concurrency"`
			NumRequests    int            `json:"num_requests"`
			MaxTokens      int            `json:"max_tokens"`
			InputTokens    int            `json:"input_tokens"`
			Warmup         *int           `json:"warmup"`
			Rounds         int            `json:"rounds"`
			MinOutputRatio float64        `json:"min_output_ratio"`
			MaxRetries     int            `json:"max_retries"`
			Save           *bool          `json:"save"`
			Hardware       string         `json:"hardware"`
			Engine         string         `json:"engine"`
			Notes          string         `json:"notes"`
			DeployConfig   map[string]any `json:"deploy_config"`
			ResolvedConfig map[string]any `json:"resolved_config"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse benchmark params: %w", err)
		}

		modality := p.resolvedModality()

		endpoint := resolveEndpoint(p.Endpoint, p.Model)

		warmup := 2
		if p.Warmup != nil {
			warmup = *p.Warmup
		}

		cfg := benchpkg.RunConfig{
			Endpoint:       endpoint,
			Model:          p.Model,
			Concurrency:    p.Concurrency,
			NumRequests:    p.NumRequests,
			MaxTokens:      p.MaxTokens,
			InputTokens:    p.InputTokens,
			WarmupCount:    warmup,
			Rounds:         p.Rounds,
			MinOutputRatio: p.MinOutputRatio,
			MaxRetries:     p.MaxRetries,
		}

		req, err := buildRequester(modality, cfg, &p.modalityParams)
		if err != nil {
			return nil, fmt.Errorf("build requester: %w", err)
		}

		result, observedMetrics, err := runBenchmarkWithMetricsAndRequester(ctx, cfg, req)
		if err != nil {
			return nil, fmt.Errorf("benchmark run: %w", err)
		}

		// Save to DB unless explicitly disabled
		save := p.Save == nil || *p.Save
		var (
			benchmarkID string
			configID    string
			savedBench  *state.BenchmarkResult
		)
		deployConfig := selectDeployConfig(ctx, p.Model, p.Engine, p.DeployConfig)
		if len(deployConfig) == 0 {
			deployConfig = selectDeployConfig(ctx, p.Model, p.Engine, p.ResolvedConfig)
		}
		if save && p.Hardware != "" && p.Engine != "" {
			var err error
			benchmarkID, configID, savedBench, err = saveBenchmarkResult(ctx, db,
				p.Hardware, p.Engine, p.Model, modality, result, deployConfig, observedMetrics,
				cfg.Concurrency, p.Notes)
			if err != nil {
				return nil, err
			}
			postProcessBenchmarkSave(ctx, db, kStore, benchmarkID, configID, p.Hardware, p.Engine, p.Model, result.ThroughputTPS)
		}

		engineVersion, engineImage := "", ""
		if version, image, err := db.LookupEngineAssetMetadata(ctx, p.Engine, p.Hardware); err != nil {
			slog.Warn("benchmark: lookup engine asset metadata failed", "engine", p.Engine, "hardware", p.Hardware, "error", err)
		} else {
			engineVersion, engineImage = version, image
		}
		resp := map[string]any{
			"result": result,
			"saved":  save && benchmarkID != "",
			"benchmark_profile": map[string]any{
				"concurrency":       result.Config.Concurrency,
				"num_requests":      result.Config.NumRequests,
				"warmup_count":      result.Config.WarmupCount,
				"rounds":            result.Config.Rounds,
				"input_tokens":      result.Config.InputTokens,
				"max_tokens":        result.Config.MaxTokens,
				"avg_input_tokens":  result.AvgInputTokens,
				"avg_output_tokens": result.AvgOutputTokens,
			},
		}
		if len(deployConfig) > 0 {
			resp["deploy_config"] = deployConfig
		}
		if engineVersion != "" {
			resp["engine_version"] = engineVersion
		}
		if engineImage != "" {
			resp["engine_image"] = engineImage
		}
		resourceUsage := resourceUsageMap(observedMetrics)
		if len(resourceUsage) > 0 {
			resp["resource_usage"] = resourceUsage
		}
		if p.Hardware != "" && p.Engine != "" {
			if hints, err := db.LookupEngineExecutionHints(ctx, p.Engine, p.Hardware); err != nil {
				slog.Warn("benchmark: lookup engine execution hints failed", "engine", p.Engine, "hardware", p.Hardware, "error", err)
			} else if hetero := state.BuildHeterogeneousObservation(hints, deployConfig, resourceUsage); len(hetero) > 0 {
				resp["heterogeneous_observation"] = hetero
			}
		}
		if benchmarkID != "" {
			resp["benchmark_id"] = benchmarkID
			resp["config_id"] = configID
			if savedBench != nil {
				resp["saved_benchmark"] = savedBench
			}

			// L2c auto-promote: if new benchmark beats current golden by >5%
			if promoted, oldID := maybeAutoPromote(ctx, db, configID, result.ThroughputTPS, p.Hardware, p.Engine, p.Model, modality); promoted {
				resp["auto_promoted"] = true
				if oldID != "" {
					resp["old_golden_id"] = oldID
				}
			}

			// K5: Update runtime overlay with actual performance data
			if p.Model != "" {
				go updatePerfOverlay(ac.dataDir, p.Model, p.Hardware, p.Engine, result, savedBench, engineVersion, engineImage, resp["heterogeneous_observation"])
			}
		}
		return json.Marshal(resp)
	}

	deps.RunBenchmarkMatrix = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			modalityParams
			Model             string         `json:"model"`
			Endpoint          string         `json:"endpoint"`
			ConcurrencyLevels []int          `json:"concurrency_levels"`
			InputTokenLevels  []int          `json:"input_token_levels"`
			MaxTokenLevels    []int          `json:"max_token_levels"`
			RequestsPerCombo  int            `json:"requests_per_combo"`
			Rounds            int            `json:"rounds"`
			MinOutputRatio    float64        `json:"min_output_ratio"`
			MaxRetries        int            `json:"max_retries"`
			Save              *bool          `json:"save"`
			Hardware          string         `json:"hardware"`
			Engine            string         `json:"engine"`
			DeployConfig      map[string]any `json:"deploy_config"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse matrix params: %w", err)
		}

		modality := p.resolvedModality()

		if len(p.ConcurrencyLevels) == 0 {
			p.ConcurrencyLevels = []int{1, 4}
		}
		if len(p.InputTokenLevels) == 0 {
			p.InputTokenLevels = []int{128, 1024}
		}
		if len(p.MaxTokenLevels) == 0 {
			p.MaxTokenLevels = []int{128, 512}
		}
		p.MaxTokenLevels = effectiveMatrixMaxTokenLevels(modality, p.MaxTokenLevels)
		if p.RequestsPerCombo <= 0 {
			p.RequestsPerCombo = 5
		}

		endpoint := resolveEndpoint(p.Endpoint, p.Model)
		deployConfig := selectDeployConfig(ctx, p.Model, p.Engine, p.DeployConfig)
		engineVersion, engineImage := "", ""
		if p.Hardware != "" && p.Engine != "" {
			if version, image, err := db.LookupEngineAssetMetadata(ctx, p.Engine, p.Hardware); err != nil {
				slog.Warn("benchmark matrix: lookup engine asset metadata failed", "engine", p.Engine, "hardware", p.Hardware, "error", err)
			} else {
				engineVersion, engineImage = version, image
			}
		}

		type matrixCell struct {
			Concurrency   int                 `json:"concurrency"`
			InputTokens   int                 `json:"input_tokens"`
			MaxTokens     int                 `json:"max_tokens"`
			Result        *benchpkg.RunResult `json:"result"`
			Error         string              `json:"error,omitempty"`
			BenchmarkID   string              `json:"benchmark_id,omitempty"`
			ConfigID      string              `json:"config_id,omitempty"`
			EngineVersion string              `json:"engine_version,omitempty"`
			EngineImage   string              `json:"engine_image,omitempty"`
			ResourceUsage map[string]any      `json:"resource_usage,omitempty"`
			DeployConfig  map[string]any      `json:"deploy_config,omitempty"`
		}

		var cells []matrixCell
		refreshVectors := false
		totalCells := len(p.ConcurrencyLevels) * len(p.InputTokenLevels) * len(p.MaxTokenLevels)
		cellIdx := 0
		for _, conc := range p.ConcurrencyLevels {
			for _, inTok := range p.InputTokenLevels {
				for _, maxTok := range p.MaxTokenLevels {
					cellIdx++
					// Bug-3: log cell start so operators tailing serve.log see
					// progress during long matrices (previously the matrix ran
					// silently for minutes).
					cellStart := time.Now()
					slog.Info("benchmark matrix: cell start",
						"model", p.Model, "engine", p.Engine,
						"progress", fmt.Sprintf("%d/%d", cellIdx, totalCells),
						"concurrency", conc, "input_tokens", inTok, "max_tokens", maxTok)
					cfg := benchpkg.RunConfig{
						Endpoint:       endpoint,
						Model:          p.Model,
						Concurrency:    conc,
						NumRequests:    p.RequestsPerCombo,
						MaxTokens:      maxTok,
						InputTokens:    inTok,
						WarmupCount:    1,
						Rounds:         p.Rounds,
						MinOutputRatio: p.MinOutputRatio,
						MaxRetries:     p.MaxRetries,
					}
					req, reqErr := buildRequester(modality, cfg, &p.modalityParams)
					var result *benchpkg.RunResult
					var observedMetrics benchmarkSystemMetrics
					var err error
					if reqErr != nil {
						err = reqErr
					} else {
						result, observedMetrics, err = runBenchmarkWithMetricsAndRequester(ctx, cfg, req)
					}
					cell := matrixCell{
						Concurrency: conc,
						InputTokens: inTok,
						MaxTokens:   maxTok,
					}
					if err != nil {
						cell.Error = err.Error()
					} else {
						cell.Result = result
						cell.EngineVersion = engineVersion
						cell.EngineImage = engineImage
						cell.ResourceUsage = resourceUsageMap(observedMetrics)
						if len(deployConfig) > 0 {
							cell.DeployConfig = cloneConfigMapForBenchmark(deployConfig)
						}
						// Save each cell if requested
						save := p.Save == nil || *p.Save
						if save && p.Hardware != "" && p.Engine != "" {
							notes := fmt.Sprintf("matrix: conc=%d in=%d out=%d", conc, inTok, maxTok)
							benchmarkID, configID, _, saveErr := saveBenchmarkResult(ctx, db, p.Hardware, p.Engine, p.Model, modality, result, deployConfig, observedMetrics, conc, notes)
							if saveErr != nil {
								slog.Warn("benchmark matrix: save failed", "error", saveErr, "concurrency", conc, "input_tokens", inTok, "max_tokens", maxTok)
							} else {
								refreshVectors = true
								if err := writeBenchmarkValidation(ctx, db, benchmarkID, configID, p.Hardware, p.Engine, p.Model, result.ThroughputTPS); err != nil {
									slog.Warn("benchmark validation: write failed", "error", err, "benchmark_id", benchmarkID)
								}
								cell.BenchmarkID = benchmarkID
								cell.ConfigID = configID
							}
						}
					}
					// Bug-3: log cell end with duration + headline metric so the
					// matrix execution shows up meaningfully in serve.log.
					dur := time.Since(cellStart).Round(time.Millisecond)
					if cell.Error != "" {
						slog.Info("benchmark matrix: cell end",
							"model", p.Model,
							"progress", fmt.Sprintf("%d/%d", cellIdx, totalCells),
							"duration", dur.String(), "status", "error",
							"error", cell.Error)
					} else if cell.Result != nil {
						slog.Info("benchmark matrix: cell end",
							"model", p.Model,
							"progress", fmt.Sprintf("%d/%d", cellIdx, totalCells),
							"duration", dur.String(), "status", "ok",
							"throughput_tps", cell.Result.ThroughputTPS,
							"ttft_p95_ms", cell.Result.TTFTP95ms)
					}
					cells = append(cells, cell)
				}
			}
		}
		if refreshVectors {
			refreshPerfVectors(ctx, kStore)
		}

		return json.Marshal(map[string]any{
			"model":         p.Model,
			"cells":         cells,
			"total":         len(cells),
			"deploy_config": deployConfig,
		})
	}

	deps.ListBenchmarks = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			ConfigID string `json:"config_id"`
			Hardware string `json:"hardware"`
			Model    string `json:"model"`
			Engine   string `json:"engine"`
			Modality string `json:"modality"`
			Limit    int    `json:"limit"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse list params: %w", err)
		}
		if p.Limit <= 0 {
			p.Limit = 20
		}

		var configIDs []string
		if p.ConfigID != "" {
			configIDs = []string{p.ConfigID}
		} else if p.Hardware != "" || p.Model != "" || p.Engine != "" {
			configs, err := db.ListConfigurations(ctx, p.Hardware, p.Model, p.Engine)
			if err != nil {
				return nil, fmt.Errorf("list configurations: %w", err)
			}
			for _, c := range configs {
				configIDs = append(configIDs, c.ID)
			}
			if len(configIDs) == 0 {
				return json.Marshal(map[string]any{
					"results": []any{},
					"total":   0,
				})
			}
		}

		results, err := db.ListBenchmarkResults(ctx, configIDs, p.Limit)
		if err != nil {
			return nil, fmt.Errorf("list benchmarks: %w", err)
		}
		if p.Modality != "" {
			want := storageBenchmarkModality(p.Modality)
			filtered := make([]*state.BenchmarkResult, 0, len(results))
			for _, item := range results {
				if item != nil && strings.EqualFold(item.Modality, want) {
					filtered = append(filtered, item)
				}
			}
			results = filtered
		}

		return json.Marshal(map[string]any{
			"results": results,
			"total":   len(results),
		})
	}
}

func effectiveMatrixMaxTokenLevels(modality string, levels []int) []int {
	if strings.EqualFold(modality, "embedding") || strings.EqualFold(modality, "reranker") {
		return []int{0}
	}
	if len(levels) == 0 {
		return []int{128, 512}
	}
	return levels
}

func selectReadyDeployConfig(engineName string, explicit map[string]any, matches []matchedDeployment) map[string]any {
	if len(explicit) > 0 {
		return cloneConfigMapForBenchmark(explicit)
	}
	for _, match := range matches {
		if match.Status == nil || !match.Status.Ready || len(match.Status.Config) == 0 {
			continue
		}
		matchedEngine := strings.TrimSpace(match.Status.Labels["aima.dev/engine"])
		if engineName != "" && !strings.EqualFold(matchedEngine, engineName) {
			continue
		}
		return cloneConfigMapForBenchmark(match.Status.Config)
	}
	return nil
}

func cloneConfigMapForBenchmark(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// buildRequester creates the appropriate Requester for the given modality.
func buildRequester(modality string, cfg benchpkg.RunConfig, mp *modalityParams) (benchpkg.Requester, error) {
	switch modality {
	case "llm":
		req := defaultChatRequester(cfg)
		req.Prompt = mp.Prompt
		return req, nil
	case "vlm":
		req := defaultChatRequester(cfg)
		req.Prompt = mp.Prompt
		req.ImageURLs = mp.ImageURLs
		return req, nil
	case "embedding":
		return &benchpkg.EmbeddingRequester{
			Model:       cfg.Model,
			InputTokens: cfg.InputTokens,
			Prompt:      mp.Prompt,
			APIKey:      cfg.APIKey,
			Timeout:     cfg.Timeout,
		}, nil
	case "reranker":
		return &benchpkg.RerankRequester{
			Model:       cfg.Model,
			InputTokens: cfg.InputTokens,
			Prompt:      mp.Prompt,
			APIKey:      cfg.APIKey,
			Timeout:     cfg.Timeout,
		}, nil
	case "tts":
		return &benchpkg.AudioSpeechRequester{
			Model:   cfg.Model,
			Voice:   mp.Voice,
			Format:  mp.AudioFormat,
			Texts:   mp.Texts,
			Timeout: cfg.Timeout,
		}, nil
	case "asr":
		loaded, err := loadAudioInputs(mp.AudioFiles)
		if err != nil {
			return nil, fmt.Errorf("load ASR audio files: %w", err)
		}
		return &benchpkg.TranscriptionRequester{
			Model:      cfg.Model,
			AudioFiles: loaded,
			Language:   mp.Language,
			Timeout:    cfg.Timeout,
		}, nil
	case "image_gen":
		return &benchpkg.ImageGenRequester{
			Model:         cfg.Model,
			Prompt:        mp.Prompt,
			Width:         mp.Width,
			Height:        mp.Height,
			Steps:         mp.Steps,
			GuidanceScale: mp.GuidanceScale,
			NumImages:     mp.NumImages,
			Timeout:       cfg.Timeout,
		}, nil
	case "video_gen":
		return &benchpkg.VideoGenRequester{
			Model:         cfg.Model,
			Prompt:        mp.Prompt,
			Width:         mp.Width,
			Height:        mp.Height,
			DurationS:     mp.DurationS,
			FPS:           mp.FPS,
			Steps:         mp.Steps,
			GuidanceScale: mp.GuidanceScale,
			InputImageURL: mp.InputImageURL,
			Timeout:       cfg.Timeout,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported modality %q", modality)
	}
}

// loadAudioInputs reads audio files from disk into memory for ASR benchmarking.
func loadAudioInputs(paths []string) ([]benchpkg.AudioInput, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var inputs []benchpkg.AudioInput
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		inputs = append(inputs, benchpkg.AudioInput{
			Filename:  filepath.Base(path),
			Data:      data,
			DurationS: 0, // duration calculated by server or unknown
		})
	}
	return inputs, nil
}

// suppress "imported and not used" for packages only used in type literals
var _ = strings.ToLower
