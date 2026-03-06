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
	Engine      string         `json:"engine,omitempty"`
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

	candidates := generateCandidates(config.Parameters)
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
			"model":            session.Config.Model,
			"engine":           session.Config.Engine,
			"config_overrides": candidate,
		})
		if _, err := t.tools.ExecuteTool(ctx, "deploy.apply", deployArgs); err != nil {
			slog.Warn("tuning: deploy failed, skipping config", "error", err)
			continue
		}

		// Benchmark
		benchArgs, _ := json.Marshal(map[string]any{
			"model":       session.Config.Model,
			"concurrency": session.Config.Concurrency,
			"rounds":      session.Config.Rounds,
		})
		result, err := t.tools.ExecuteTool(ctx, "benchmark.run", benchArgs)
		if err != nil {
			slog.Warn("tuning: benchmark failed, skipping config", "error", err)
			continue
		}

		// Parse benchmark result
		var benchResult struct {
			ThroughputTPS float64 `json:"throughput_tps"`
			TTFTP95       float64 `json:"ttft_ms_p95"`
		}
		_ = json.Unmarshal([]byte(result.Content), &benchResult)

		score := benchResult.ThroughputTPS // simple scoring: maximize throughput
		tr := TuningResult{
			ConfigOverrides: candidate,
			ThroughputTPS:   benchResult.ThroughputTPS,
			TTFTP95Ms:       benchResult.TTFTP95,
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
			"model":            session.Config.Model,
			"engine":           session.Config.Engine,
			"config_overrides": session.BestConfig,
		})
		if _, err := t.tools.ExecuteTool(ctx, "deploy.apply", deployArgs); err != nil {
			slog.Warn("tuning: failed to deploy best config", "error", err)
		} else {
			slog.Info("tuning: deployed best config", "score", session.BestScore, "config", session.BestConfig)
		}
	}
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
