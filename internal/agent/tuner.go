package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
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
	MaxConfigs  int            `json:"max_configs,omitempty"` // cap grid search
}

// TuningResult holds a single candidate's benchmark outcome.
type TuningResult struct {
	ConfigOverrides map[string]any `json:"config_overrides"`
	ThroughputTPS   float64        `json:"throughput_tps"`
	TTFTP95Ms       float64        `json:"ttft_p95_ms"`
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

// Tuner orchestrates parameter search + benchmark loops.
type Tuner struct {
	tools   ToolExecutor
	mu      sync.Mutex
	session *TuningSession
	cancel  context.CancelFunc
}

// NewTuner creates a tuner.
func NewTuner(tools ToolExecutor) *Tuner {
	return &Tuner{tools: tools}
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

	go t.run(ctx, session, candidates)
	return session, nil
}

// Stop cancels the running tuning session.
func (t *Tuner) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
	if t.session != nil && t.session.Status == "running" {
		t.session.Status = "cancelled"
		t.session.CompletedAt = time.Now()
	}
}

// CurrentSession returns the current/last session.
func (t *Tuner) CurrentSession() *TuningSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.session
}

func (t *Tuner) run(ctx context.Context, session *TuningSession, candidates []map[string]any) {
	defer func() {
		t.mu.Lock()
		if session.Status == "running" {
			session.Status = "completed"
		}
		session.CompletedAt = time.Now()
		t.mu.Unlock()
	}()

	for i, candidate := range candidates {
		select {
		case <-ctx.Done():
			return
		default:
		}

		slog.Info("tuning: testing config", "progress", fmt.Sprintf("%d/%d", i+1, session.Total), "config", candidate)

		// Deploy with this config
		deployArgs, _ := json.Marshal(map[string]any{
			"model":  session.Config.Model,
			"engine": session.Config.Engine,
			"config": candidate,
		})
		deployResult, err := t.tools.ExecuteTool(ctx, "deploy.apply", deployArgs)
		if err == nil {
			err = toolResultError(deployResult)
		}
		if err != nil {
			slog.Warn("tuning: deploy failed, skipping config", "error", err)
			continue
		}

		// Benchmark
		benchArgs, _ := json.Marshal(map[string]any{
			"model":       session.Config.Model,
			"endpoint":    session.Config.Endpoint,
			"concurrency": session.Config.Concurrency,
			"rounds":      session.Config.Rounds,
			"hardware":    session.Config.Hardware,
			"engine":      session.Config.Engine,
		})
		result, err := t.tools.ExecuteTool(ctx, "benchmark.run", benchArgs)
		if err == nil {
			err = toolResultError(result)
		}
		if err != nil {
			slog.Warn("tuning: benchmark failed, skipping config", "error", err)
			continue
		}

		// Parse benchmark result
		var benchResult struct {
			Result struct {
				ThroughputTPS float64 `json:"throughput_tps"`
				TTFTP95Ms     float64 `json:"ttft_p95_ms"`
			} `json:"result"`
			ThroughputTPS float64 `json:"throughput_tps"`
			TTFTP95Ms     float64 `json:"ttft_p95_ms"`
		}
		if err := json.Unmarshal([]byte(result.Content), &benchResult); err != nil {
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
		tr := TuningResult{
			ConfigOverrides: candidate,
			ThroughputTPS:   throughput,
			TTFTP95Ms:       ttftP95,
			Score:           score,
		}

		t.mu.Lock()
		session.Results = append(session.Results, tr)
		session.Progress = i + 1
		if score > session.BestScore {
			session.BestScore = score
			session.BestConfig = candidate
		}
		t.mu.Unlock()
	}

	// Redeploy best config as final state
	if session.BestConfig != nil {
		deployArgs, _ := json.Marshal(map[string]any{
			"model":  session.Config.Model,
			"engine": session.Config.Engine,
			"config": session.BestConfig,
		})
		deployResult, err := t.tools.ExecuteTool(ctx, "deploy.apply", deployArgs)
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
