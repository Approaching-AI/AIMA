package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TunableParam defines a parameter search dimension.
type TunableParam struct {
	Key    string  `json:"key"    yaml:"key"`
	Values []any   `json:"values" yaml:"values,omitempty"` // explicit candidates
	Min    float64 `json:"min"    yaml:"min,omitempty"`    // range-based
	Max    float64 `json:"max"    yaml:"max,omitempty"`
	Step   float64 `json:"step"   yaml:"step,omitempty"`
}

// TuningConfig defines what to tune.
type TuningConfig struct {
	Model       string         `json:"model"`
	Hardware    string         `json:"hardware,omitempty"`
	Engine      string         `json:"engine,omitempty"`
	Endpoint    string         `json:"endpoint,omitempty"`
	Parameters  []TunableParam `json:"parameters"`
	Concurrency int            `json:"concurrency,omitempty"`
	Rounds      int            `json:"rounds,omitempty"`
	NumRequests int            `json:"num_requests,omitempty"`
	InputTokens int            `json:"input_tokens,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	WarmupCount int            `json:"warmup_count,omitempty"`
	Modality    string         `json:"modality,omitempty"`
	MaxConfigs  int            `json:"max_configs,omitempty"` // cap grid search

	// CleanupAfter asks the tuner to tear down any running deploy it created
	// once the session ends, skipping the best-config redeploy. Set by the
	// explorer so the task-boundary teardown sees a stopped GPU.
	CleanupAfter bool `json:"-"`
}

// TuningResult holds a single candidate's benchmark outcome.
type TuningResult struct {
	ConfigOverrides map[string]any `json:"config_overrides"`
	ThroughputTPS   float64        `json:"throughput_tps"`
	LatencyP50Ms    float64        `json:"latency_p50_ms,omitempty"`
	TTFTP50Ms       float64        `json:"ttft_p50_ms,omitempty"`
	TTFTP95Ms       float64        `json:"ttft_p95_ms"`
	TPOTP50Ms       float64        `json:"tpot_p50_ms,omitempty"`
	TPOTP95Ms       float64        `json:"tpot_p95_ms,omitempty"`
	QPS             float64        `json:"qps,omitempty"`
	Concurrency     int            `json:"concurrency,omitempty"`
	NumRequests     int            `json:"num_requests,omitempty"`
	WarmupCount     int            `json:"warmup_count,omitempty"`
	Rounds          int            `json:"rounds,omitempty"`
	InputTokens     int            `json:"input_tokens,omitempty"`
	MaxTokens       int            `json:"max_tokens,omitempty"`
	AvgInputTokens  int            `json:"avg_input_tokens,omitempty"`
	AvgOutputTokens int            `json:"avg_output_tokens,omitempty"`
	VRAMUsageMiB    float64        `json:"vram_usage_mib,omitempty"`
	RAMUsageMiB     float64        `json:"ram_usage_mib,omitempty"`
	CPUUsagePct     float64        `json:"cpu_usage_pct,omitempty"`
	GPUUtilPct      float64        `json:"gpu_utilization_pct,omitempty"`
	PowerDrawWatts  float64        `json:"power_draw_watts,omitempty"`
	BenchmarkID     string         `json:"benchmark_id,omitempty"`
	ConfigID        string         `json:"config_id,omitempty"`
	EngineVersion   string         `json:"engine_version,omitempty"`
	EngineImage     string         `json:"engine_image,omitempty"`
	ResourceUsage   map[string]any `json:"resource_usage,omitempty"`
	Score           float64        `json:"score"` // composite ranking score
}

// TuningSession tracks an ongoing or completed tuning run.
type TuningSession struct {
	ID          string         `json:"id"`
	Config      TuningConfig   `json:"config"`
	Status      string         `json:"status"` // "running", "completed", "cancelled", "failed"
	Progress    int            `json:"progress"`
	Total       int            `json:"total"`
	Results     []TuningResult `json:"results,omitempty"`
	BestConfig  map[string]any `json:"best_config,omitempty"`
	BestScore   float64        `json:"best_score"`
	StartedAt   time.Time      `json:"started_at"`
	CompletedAt time.Time      `json:"completed_at,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// TuningSink persists a session snapshot. Called on start, on each progress
// tick, and when the session terminates (completed/failed/cancelled).
// Nil sink is a no-op so callers that don't care skip persistence.
type TuningSink func(ctx context.Context, session *TuningSession)

// Tuner orchestrates parameter search + benchmark loops.
type Tuner struct {
	tools           ToolExecutor
	mu              sync.Mutex
	session         *TuningSession
	cancel          context.CancelFunc
	gpuReleaseSleep time.Duration // grace period after deploy.delete; 0 in tests
	sink            TuningSink
}

// TunerOption configures a Tuner.
type TunerOption func(*Tuner)

// WithTuningSink wires a sink that persists session state on every update.
func WithTuningSink(sink TuningSink) TunerOption {
	return func(t *Tuner) { t.sink = sink }
}

// NewTuner creates a tuner.
func NewTuner(tools ToolExecutor, opts ...TunerOption) *Tuner {
	t := &Tuner{tools: tools, gpuReleaseSleep: gpuReleaseGrace}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// emitSink snapshots the session under the mutex and invokes the sink outside
// it so sink I/O cannot deadlock the tuner.
func (t *Tuner) emitSink(ctx context.Context, session *TuningSession) {
	if t.sink == nil {
		return
	}
	t.mu.Lock()
	snapshot := *session
	if len(session.Results) > 0 {
		snapshot.Results = append([]TuningResult(nil), session.Results...)
	}
	if session.BestConfig != nil {
		snapshot.BestConfig = cloneAnyMap(session.BestConfig)
	}
	t.mu.Unlock()
	t.sink(ctx, &snapshot)
}

// Start kicks off a tuning session. Returns immediately with the session ID.
func (t *Tuner) Start(ctx context.Context, config TuningConfig) (*TuningSession, error) {
	t.mu.Lock()
	if t.session != nil && t.session.Status == "running" {
		t.mu.Unlock()
		return nil, fmt.Errorf("tuning session %s already running", t.session.ID)
	}
	t.mu.Unlock()

	if len(config.Parameters) == 0 {
		defaults, resolvedEngine, err := t.defaultParameters(ctx, config)
		if err != nil {
			return nil, err
		}
		config.Parameters = defaults
		if config.Engine == "" {
			config.Engine = resolvedEngine
		}
	} else if config.Engine == "" {
		resolvedEngine, err := t.resolveEngine(ctx, config.Model)
		if err != nil {
			return nil, err
		}
		config.Engine = resolvedEngine
	}

	candidates := generateCandidates(config.Parameters)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no tuning candidates generated; provide parameters or a supported engine")
	}
	if config.MaxConfigs > 0 && len(candidates) > config.MaxConfigs {
		candidates = candidates[:config.MaxConfigs]
	}

	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", config.Model, time.Now().UnixNano())))
	session := &TuningSession{
		ID:        hex.EncodeToString(h[:8]),
		Config:    config,
		Status:    "running",
		Total:     len(candidates),
		StartedAt: time.Now(),
	}

	t.mu.Lock()
	if t.session != nil && t.session.Status == "running" {
		t.mu.Unlock()
		return nil, fmt.Errorf("tuning session %s already running", t.session.ID)
	}
	t.session = session

	ctx, t.cancel = context.WithCancel(ctx)
	t.mu.Unlock()

	t.emitSink(ctx, session)
	go t.run(ctx, session, candidates)
	return session, nil
}

// Stop cancels the running tuning session.
func (t *Tuner) Stop() {
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	var stopped *TuningSession
	if t.session != nil && t.session.Status == "running" {
		t.session.Status = "cancelled"
		t.session.CompletedAt = time.Now()
		stopped = t.session
	}
	t.mu.Unlock()
	if stopped != nil {
		t.emitSink(context.Background(), stopped)
	}
}

// CurrentSession returns a snapshot of the current/last session. Returning a
// copy (rather than the live pointer) lets callers read fields without
// coordinating with the tuner's internal mutex.
func (t *Tuner) CurrentSession() *TuningSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session == nil {
		return nil
	}
	snapshot := *t.session
	if len(t.session.Results) > 0 {
		snapshot.Results = append([]TuningResult(nil), t.session.Results...)
	}
	if t.session.BestConfig != nil {
		snapshot.BestConfig = cloneAnyMap(t.session.BestConfig)
	}
	return &snapshot
}

func (t *Tuner) run(ctx context.Context, session *TuningSession, candidates []map[string]any) {
	defer func() {
		t.mu.Lock()
		if session.Status == "running" {
			if len(session.Results) == 0 {
				session.Status = "failed"
				if strings.TrimSpace(session.Error) == "" {
					session.Error = "no successful tuning benchmark results"
				}
			} else {
				session.Status = "completed"
			}
		}
		session.CompletedAt = time.Now()
		t.mu.Unlock()
		t.emitSink(context.Background(), session)
	}()

	var (
		resolvedConfig     map[string]any
		lastDeployedConfig map[string]any
	)
	if resolved, err := t.resolveTarget(ctx, session.Config.Model, session.Config.Engine); err == nil {
		resolvedConfig = resolved.Config
	}

	for i, candidate := range candidates {
		select {
		case <-ctx.Done():
			return
		default:
		}

		candidate = normalizeTuningCandidate(candidate, resolvedConfig)
		if len(candidate) == 0 {
			t.markProgress(session, i+1)
			slog.Warn("tuning: candidate invalid after normalization, skipping", "progress", fmt.Sprintf("%d/%d", i+1, session.Total))
			continue
		}
		slog.Info("tuning: testing config", "progress", fmt.Sprintf("%d/%d", i+1, session.Total), "config", candidate)

		// Delete existing deployment before redeploying with new config.
		deleteArgs, _ := json.Marshal(map[string]string{"name": session.Config.Model})
		deleteResult, delErr := t.tools.ExecuteTool(ctx, "deploy.delete", deleteArgs)
		if delErr == nil {
			delErr = toolResultError(deleteResult)
		}
		if delErr != nil {
			slog.Debug("tuning: pre-delete (may not exist)", "model", session.Config.Model, "error", delErr)
		}
		waitForGPURelease(ctx, t.tools, session.Config.Model, t.gpuReleaseSleep)

		// Deploy with this config and benchmark the ready endpoint returned by deploy.run.
		deployArgs, _ := json.Marshal(map[string]any{
			"model":   session.Config.Model,
			"engine":  session.Config.Engine,
			"config":  candidate,
			"no_pull": true,
		})
		deployResult, err := t.tools.ExecuteTool(ctx, "deploy.run", deployArgs)
		if err == nil {
			err = toolResultError(deployResult)
		}
		if err != nil {
			t.markProgress(session, i+1)
			slog.Warn("tuning: deploy failed, skipping config", "error", err)
			continue
		}
		var deploySummary struct {
			Address string         `json:"address"`
			Config  map[string]any `json:"config"`
			Status  string         `json:"status"`
			Message string         `json:"message"`
		}
		if err := json.Unmarshal([]byte(deployResult.Content), &deploySummary); err != nil {
			t.markProgress(session, i+1)
			slog.Warn("tuning: deploy result parse failed, skipping config", "error", err)
			continue
		}

		// Benchmark
		endpoint := session.Config.Endpoint
		if endpoint == "" {
			endpoint = openAIChatCompletionsEndpoint(deploySummary.Address)
		}
		if endpoint == "" {
			t.markProgress(session, i+1)
			// Surface deploy status/message so timeout vs other empty-address
			// causes are distinguishable without re-reading the deploy code.
			slog.Warn("tuning: deploy result missing ready endpoint, skipping config",
				"deploy_status", deploySummary.Status,
				"deploy_message", deploySummary.Message)
			continue
		}
		deployConfig := deploySummary.Config
		if len(deployConfig) == 0 {
			deployConfig = candidate
		}
		lastDeployedConfig = cloneAnyMap(deployConfig)
		benchArgs, _ := json.Marshal(map[string]any{
			"model":         session.Config.Model,
			"endpoint":      endpoint,
			"concurrency":   session.Config.Concurrency,
			"rounds":        session.Config.Rounds,
			"hardware":      session.Config.Hardware,
			"engine":        session.Config.Engine,
			"deploy_config": deployConfig,
		})
		var benchPayload map[string]any
		_ = json.Unmarshal(benchArgs, &benchPayload)
		if session.Config.NumRequests > 0 {
			benchPayload["num_requests"] = session.Config.NumRequests
		}
		if session.Config.InputTokens > 0 {
			benchPayload["input_tokens"] = session.Config.InputTokens
		}
		if session.Config.MaxTokens > 0 {
			benchPayload["max_tokens"] = session.Config.MaxTokens
		}
		if session.Config.WarmupCount > 0 {
			benchPayload["warmup"] = session.Config.WarmupCount
		}
		if session.Config.Modality != "" {
			benchPayload["modality"] = session.Config.Modality
		}
		benchArgs, _ = json.Marshal(benchPayload)
		result, err := t.tools.ExecuteTool(ctx, "benchmark.run", benchArgs)
		if err == nil {
			err = toolResultError(result)
		}
		if err != nil {
			t.markProgress(session, i+1)
			slog.Warn("tuning: benchmark failed, skipping config", "error", err)
			continue
		}

		// Parse benchmark result
		var benchResult struct {
			BenchmarkID string `json:"benchmark_id"`
			ConfigID    string `json:"config_id"`
			Result      struct {
				ThroughputTPS   float64 `json:"throughput_tps"`
				LatencyP50Ms    float64 `json:"latency_p50_ms"`
				TTFTP50Ms       float64 `json:"ttft_p50_ms"`
				TTFTP95Ms       float64 `json:"ttft_p95_ms"`
				TPOTP50Ms       float64 `json:"tpot_p50_ms"`
				TPOTP95Ms       float64 `json:"tpot_p95_ms"`
				QPS             float64 `json:"qps"`
				AvgInputTokens  int     `json:"avg_input_tokens"`
				AvgOutputTokens int     `json:"avg_output_tokens"`
				Config          struct {
					Concurrency int `json:"concurrency"`
					NumRequests int `json:"num_requests"`
					WarmupCount int `json:"warmup_count"`
					Rounds      int `json:"rounds"`
					InputTokens int `json:"input_tokens"`
					MaxTokens   int `json:"max_tokens"`
				} `json:"config"`
			} `json:"result"`
			BenchmarkProfile struct {
				Concurrency     int `json:"concurrency"`
				NumRequests     int `json:"num_requests"`
				WarmupCount     int `json:"warmup_count"`
				Rounds          int `json:"rounds"`
				InputTokens     int `json:"input_tokens"`
				MaxTokens       int `json:"max_tokens"`
				AvgInputTokens  int `json:"avg_input_tokens"`
				AvgOutputTokens int `json:"avg_output_tokens"`
			} `json:"benchmark_profile"`
			ResourceUsage struct {
				VRAMUsageMiB      float64 `json:"vram_usage_mib"`
				RAMUsageMiB       float64 `json:"ram_usage_mib"`
				CPUUsagePct       float64 `json:"cpu_usage_pct"`
				GPUUtilizationPct float64 `json:"gpu_utilization_pct"`
				PowerDrawWatts    float64 `json:"power_draw_watts"`
			} `json:"resource_usage"`
			SavedBenchmark struct {
				VRAMUsageMiB   float64 `json:"vram_usage_mib"`
				RAMUsageMiB    float64 `json:"ram_usage_mib"`
				CPUUsagePct    float64 `json:"cpu_usage_pct"`
				GPUUtilPct     float64 `json:"gpu_util_pct"`
				PowerDrawWatts float64 `json:"power_draw_watts"`
			} `json:"saved_benchmark"`
			EngineVersion string  `json:"engine_version"`
			EngineImage   string  `json:"engine_image"`
			ThroughputTPS float64 `json:"throughput_tps"`
			TTFTP95Ms     float64 `json:"ttft_p95_ms"`
		}
		if err := json.Unmarshal([]byte(result.Content), &benchResult); err != nil {
			t.markProgress(session, i+1)
			slog.Warn("tuning: benchmark result parse failed, skipping config", "error", err)
			continue
		}

		throughput := benchResult.Result.ThroughputTPS
		if throughput == 0 {
			throughput = benchResult.ThroughputTPS
		}
		ttftP95 := benchResult.Result.TTFTP95Ms
		if ttftP95 == 0 {
			ttftP95 = benchResult.TTFTP95Ms
		}

		score := throughput // simple scoring: maximize throughput
		// Bug-6: record deploy-applied config (post safety cap), not planner-requested candidate.
		appliedConfig := deployConfig
		if len(appliedConfig) == 0 {
			appliedConfig = candidate
		}
		tr := TuningResult{
			ConfigOverrides: cloneAnyMap(appliedConfig),
			ThroughputTPS:   throughput,
			LatencyP50Ms:    benchResult.Result.LatencyP50Ms,
			TTFTP50Ms:       benchResult.Result.TTFTP50Ms,
			TTFTP95Ms:       ttftP95,
			TPOTP50Ms:       benchResult.Result.TPOTP50Ms,
			TPOTP95Ms:       benchResult.Result.TPOTP95Ms,
			QPS:             benchResult.Result.QPS,
			Concurrency:     firstPositiveInt(benchResult.BenchmarkProfile.Concurrency, benchResult.Result.Config.Concurrency),
			NumRequests:     firstPositiveInt(benchResult.BenchmarkProfile.NumRequests, benchResult.Result.Config.NumRequests),
			WarmupCount:     firstPositiveInt(benchResult.BenchmarkProfile.WarmupCount, benchResult.Result.Config.WarmupCount),
			Rounds:          firstPositiveInt(benchResult.BenchmarkProfile.Rounds, benchResult.Result.Config.Rounds),
			InputTokens:     firstPositiveInt(benchResult.BenchmarkProfile.InputTokens, benchResult.Result.Config.InputTokens),
			MaxTokens:       firstPositiveInt(benchResult.BenchmarkProfile.MaxTokens, benchResult.Result.Config.MaxTokens),
			AvgInputTokens:  firstPositiveInt(benchResult.BenchmarkProfile.AvgInputTokens, benchResult.Result.AvgInputTokens),
			AvgOutputTokens: firstPositiveInt(benchResult.BenchmarkProfile.AvgOutputTokens, benchResult.Result.AvgOutputTokens),
			VRAMUsageMiB:    firstPositiveFloat(benchResult.ResourceUsage.VRAMUsageMiB, benchResult.SavedBenchmark.VRAMUsageMiB),
			RAMUsageMiB:     firstPositiveFloat(benchResult.ResourceUsage.RAMUsageMiB, benchResult.SavedBenchmark.RAMUsageMiB),
			CPUUsagePct:     firstPositiveFloat(benchResult.ResourceUsage.CPUUsagePct, benchResult.SavedBenchmark.CPUUsagePct),
			GPUUtilPct:      firstPositiveFloat(benchResult.ResourceUsage.GPUUtilizationPct, benchResult.SavedBenchmark.GPUUtilPct),
			PowerDrawWatts:  firstPositiveFloat(benchResult.ResourceUsage.PowerDrawWatts, benchResult.SavedBenchmark.PowerDrawWatts),
			BenchmarkID:     benchResult.BenchmarkID,
			ConfigID:        benchResult.ConfigID,
			EngineVersion:   benchResult.EngineVersion,
			EngineImage:     benchResult.EngineImage,
			ResourceUsage:   tuningResourceUsageMap(benchResult.ResourceUsage, benchResult.SavedBenchmark),
			Score:           score,
		}

		t.mu.Lock()
		session.Results = append(session.Results, tr)
		if score > session.BestScore {
			session.BestScore = score
			session.BestConfig = cloneAnyMap(appliedConfig)
		}
		t.mu.Unlock()
		t.markProgress(session, i+1)
	}

	// When called from the explorer, the caller runs its own task-boundary
	// teardown; skip the final redeploy so we don't leave a GPU hot between
	// tasks. Best config is still recorded on the session for knowledge.
	if session.Config.CleanupAfter {
		if lastDeployedConfig != nil {
			deleteArgs, _ := json.Marshal(map[string]string{"name": session.Config.Model})
			_, _ = t.tools.ExecuteTool(ctx, "deploy.delete", deleteArgs)
		}
		return
	}

	// Redeploy best config as final state
	if session.BestConfig != nil {
		if sameConfigMap(session.BestConfig, lastDeployedConfig) {
			slog.Info("tuning: best config already deployed", "score", session.BestScore, "config", session.BestConfig)
			return
		}
		deleteArgs, _ := json.Marshal(map[string]string{"name": session.Config.Model})
		deleteResult, delErr := t.tools.ExecuteTool(ctx, "deploy.delete", deleteArgs)
		if delErr == nil {
			delErr = toolResultError(deleteResult)
		}
		if delErr != nil {
			slog.Debug("tuning: final pre-delete (may not exist)", "model", session.Config.Model, "error", delErr)
		}
		waitForGPURelease(ctx, t.tools, session.Config.Model, t.gpuReleaseSleep)
		deployArgs, _ := json.Marshal(map[string]any{
			"model":   session.Config.Model,
			"engine":  session.Config.Engine,
			"config":  session.BestConfig,
			"no_pull": true,
		})
		deployResult, err := t.tools.ExecuteTool(ctx, "deploy.run", deployArgs)
		if err == nil {
			err = toolResultError(deployResult)
		}
		if err != nil {
			slog.Warn("tuning: failed to deploy best config", "error", err)
		} else {
			slog.Info("tuning: deployed best config", "score", session.BestScore, "config", session.BestConfig)
		}
	}
}

func (t *Tuner) markProgress(session *TuningSession, progress int) {
	t.mu.Lock()
	if session.Progress < progress {
		session.Progress = progress
	}
	t.mu.Unlock()
	t.emitSink(context.Background(), session)
}

func tuningResourceUsageMap(primary struct {
	VRAMUsageMiB      float64 `json:"vram_usage_mib"`
	RAMUsageMiB       float64 `json:"ram_usage_mib"`
	CPUUsagePct       float64 `json:"cpu_usage_pct"`
	GPUUtilizationPct float64 `json:"gpu_utilization_pct"`
	PowerDrawWatts    float64 `json:"power_draw_watts"`
}, saved struct {
	VRAMUsageMiB   float64 `json:"vram_usage_mib"`
	RAMUsageMiB    float64 `json:"ram_usage_mib"`
	CPUUsagePct    float64 `json:"cpu_usage_pct"`
	GPUUtilPct     float64 `json:"gpu_util_pct"`
	PowerDrawWatts float64 `json:"power_draw_watts"`
}) map[string]any {
	usage := map[string]any{}
	if v := firstPositiveFloat(primary.VRAMUsageMiB, saved.VRAMUsageMiB); v > 0 {
		usage["vram_usage_mib"] = v
	}
	if v := firstPositiveFloat(primary.RAMUsageMiB, saved.RAMUsageMiB); v > 0 {
		usage["ram_usage_mib"] = v
	}
	if v := firstPositiveFloat(primary.CPUUsagePct, saved.CPUUsagePct); v > 0 {
		usage["cpu_usage_pct"] = v
	}
	if v := firstPositiveFloat(primary.GPUUtilizationPct, saved.GPUUtilPct); v > 0 {
		usage["gpu_utilization_pct"] = v
	}
	if v := firstPositiveFloat(primary.PowerDrawWatts, saved.PowerDrawWatts); v > 0 {
		usage["power_draw_watts"] = v
	}
	if len(usage) == 0 {
		return nil
	}
	return usage
}

func normalizeTuningCandidate(candidate, resolvedConfig map[string]any) map[string]any {
	if len(candidate) == 0 {
		return nil
	}
	normalized := make(map[string]any, len(candidate))
	for key, value := range candidate {
		base, hasBase := resolvedConfig[key]
		if !hasBase {
			normalized[key] = value
			continue
		}
		coerced, ok := coerceTuningValue(value, base)
		if !ok {
			slog.Warn("tuning: dropping invalid override", "key", key, "value", value, "resolved_value", base)
			continue
		}
		normalized[key] = coerced
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func coerceTuningValue(value, base any) (any, bool) {
	switch baseTyped := base.(type) {
	case bool:
		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
			return parsed, err == nil
		case float64:
			if typed == 0 || typed == 1 {
				return typed == 1, true
			}
		case int:
			if typed == 0 || typed == 1 {
				return typed == 1, true
			}
		}
		return nil, false
	case string:
		if typed, ok := value.(string); ok {
			return typed, true
		}
		return fmt.Sprintf("%v", value), true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		switch typed := value.(type) {
		case bool:
			if typed {
				// Preserve the resolved numeric default when the planner emits a
				// boolean toggle for a numeric knob (e.g. kt_cpuinfer:true).
				return baseTyped, true
			}
			return nil, false
		case string:
			if strings.TrimSpace(typed) == "" {
				return nil, false
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
				return parsed, true
			}
			return nil, false
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return typed, true
		}
		return nil, false
	default:
		return value, true
	}
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func sameConfigMap(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aj) == string(bj)
}

func (t *Tuner) defaultParameters(ctx context.Context, config TuningConfig) ([]TunableParam, string, error) {
	resolved, err := t.resolveTarget(ctx, config.Model, config.Engine)
	if err != nil {
		return nil, "", err
	}

	params := defaultTuningParams(resolved.Config)
	if len(params) == 0 {
		return nil, "", fmt.Errorf("no default tuning parameters for resolved config of engine %q; specify parameters explicitly", resolved.Engine)
	}
	return params, resolved.Engine, nil
}

func (t *Tuner) resolveTarget(ctx context.Context, model, engine string) (*resolvedTuningTarget, error) {
	resolveArgs := map[string]any{"model": model}
	if engine != "" {
		resolveArgs["engine"] = engine
	}
	payload, _ := json.Marshal(resolveArgs)
	result, err := t.tools.ExecuteTool(ctx, "knowledge.resolve", payload)
	if err == nil {
		err = toolResultError(result)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve tuning target for %s: %w", model, err)
	}

	var resolved resolvedTuningTarget
	if err := json.Unmarshal([]byte(result.Content), &resolved); err != nil {
		return nil, fmt.Errorf("parse resolved tuning target for %s: %w", model, err)
	}
	if resolved.Engine == "" {
		return nil, fmt.Errorf("resolved engine for %s is empty", model)
	}
	return &resolved, nil
}

type resolvedTuningTarget struct {
	Engine string         `json:"engine"`
	Config map[string]any `json:"config"`
}

func (t *Tuner) resolveEngine(ctx context.Context, model string) (string, error) {
	resolved, err := t.resolveTarget(ctx, model, "")
	if err != nil {
		return "", err
	}
	return resolved.Engine, nil
}

func defaultTuningParams(config map[string]any) []TunableParam {
	for _, key := range []string{"gpu_memory_utilization", "mem_fraction_static"} {
		if _, ok := config[key]; ok {
			return []TunableParam{{
				Key:    key,
				Values: []any{0.7, 0.75, 0.8, 0.85, 0.9},
			}}
		}
	}
	return nil
}

// generateCandidates produces the cross-product of all parameter values.
func generateCandidates(params []TunableParam) []map[string]any {
	if len(params) == 0 {
		return nil
	}

	// Expand each param into its value list
	expanded := make([][]any, len(params))
	for i, p := range params {
		if len(p.Values) > 0 {
			expanded[i] = p.Values
		} else if p.Step > 0 && p.Max >= p.Min {
			for v := p.Min; v <= p.Max+p.Step/2; v += p.Step {
				expanded[i] = append(expanded[i], v)
			}
		} else {
			expanded[i] = []any{nil} // placeholder
		}
	}

	// Cross-product
	var results []map[string]any
	var generate func(depth int, current map[string]any)
	generate = func(depth int, current map[string]any) {
		if depth == len(params) {
			cp := make(map[string]any, len(current))
			for k, v := range current {
				cp[k] = v
			}
			results = append(results, cp)
			return
		}
		for _, val := range expanded[depth] {
			if val != nil {
				current[params[depth].Key] = val
			}
			generate(depth+1, current)
		}
	}
	generate(0, make(map[string]any))
	return results
}

func firstPositiveInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstPositiveFloat(values ...float64) float64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}
