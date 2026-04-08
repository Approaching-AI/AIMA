package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// HarvestInput contains the exploration task result for post-processing.
type HarvestInput struct {
	Task   PlanTask
	Result HarvestResult
}

// HarvestResult captures benchmark/exploration outcomes.
type HarvestResult struct {
	Success         bool
	BenchmarkID     string
	ConfigID        string
	ExecutionPath   string
	Throughput      float64
	QPS             float64
	TTFTP95         float64
	TPOTP95         float64
	VRAMMiB         float64
	RAMMiB          float64
	CPUUsagePct     float64
	GPUUtilPct      float64
	PowerWatts      float64
	Concurrency     int
	NumRequests     int
	WarmupCount     int
	Rounds          int
	InputTokens     int
	MaxTokens       int
	AvgInputTokens  int
	AvgOutputTokens int
	EngineVersion   string
	EngineImage     string
	Config          map[string]any
	Promoted        bool // set by maybeAutoPromote
	Error           string
}

// HarvestAction describes a post-exploration side effect.
type HarvestAction struct {
	Type   string // "promote", "note", "sync_push", "update_question", "feedback"
	Detail string
}

// Harvester collects exploration results and performs post-processing.
type Harvester struct {
	tier     int
	llm      LLMClient // nil for Tier 1
	syncPush func(ctx context.Context) error
	saveNote func(ctx context.Context, title, content, hardware, model, engine string) error
}

type HarvesterOption func(*Harvester)

func WithHarvesterLLM(llm LLMClient) HarvesterOption {
	return func(h *Harvester) { h.llm = llm }
}

func WithSyncPush(fn func(ctx context.Context) error) HarvesterOption {
	return func(h *Harvester) { h.syncPush = fn }
}

func WithSaveNote(fn func(ctx context.Context, title, content, hardware, model, engine string) error) HarvesterOption {
	return func(h *Harvester) { h.saveNote = fn }
}

func NewHarvester(tier int, opts ...HarvesterOption) *Harvester {
	h := &Harvester{tier: tier}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Harvest processes an exploration result and returns actions taken.
func (h *Harvester) Harvest(ctx context.Context, input HarvestInput) []HarvestAction {
	var actions []HarvestAction

	if !input.Result.Success {
		note := fmt.Sprintf("%s on %s: FAILED -- %s", input.Task.Model, input.Task.Engine, input.Result.Error)
		actions = append(actions, HarvestAction{Type: "note", Detail: note})
		if h.saveNote != nil {
			_ = h.saveNote(ctx, "exploration failed", note, input.Task.Hardware, input.Task.Model, input.Task.Engine)
		}
		return actions
	}

	// Record knowledge note
	note, insightPending := h.generateNote(ctx, input)
	actions = append(actions, HarvestAction{Type: "note", Detail: note})
	if insightPending {
		actions = append(actions, HarvestAction{Type: "insight_pending", Detail: "LLM unavailable, template note only"})
	}
	if h.saveNote != nil {
		title := fmt.Sprintf("%s on %s benchmark", input.Task.Model, input.Task.Engine)
		if insightPending {
			title += " [insight_pending]"
		}
		_ = h.saveNote(ctx, title, note, input.Task.Hardware, input.Task.Model, input.Task.Engine)
	}

	// Track promotion
	if input.Result.Promoted {
		actions = append(actions, HarvestAction{
			Type:   "promote",
			Detail: fmt.Sprintf("%s promoted to golden", input.Task.Model),
		})
	}

	// Sync push if available
	if h.syncPush != nil {
		if err := h.syncPush(ctx); err != nil {
			slog.Warn("harvester sync push failed", "error", err)
		} else {
			actions = append(actions, HarvestAction{Type: "sync_push", Detail: "incremental push"})
		}
	}

	return actions
}

func (h *Harvester) generateNote(ctx context.Context, input HarvestInput) (string, bool) {
	if h.tier >= 2 && h.llm != nil {
		note, err := h.generateLLMNote(ctx, input)
		if err == nil {
			return note, false
		}
		slog.Warn("LLM note generation failed, falling back to template", "error", err)
		// Return template note with insight_pending flag
		return h.generateTemplateNote(input), true
	}
	return h.generateTemplateNote(input), false
}

func (h *Harvester) generateTemplateNote(input HarvestInput) string {
	engineLabel := input.Task.Engine
	if input.Result.EngineVersion != "" {
		engineLabel += "@" + input.Result.EngineVersion
	}
	summary := fmt.Sprintf("%s on %s: %.1f tok/s", input.Task.Model, engineLabel, input.Result.Throughput)
	if input.Result.QPS > 0 {
		summary += fmt.Sprintf(", QPS %.2f", input.Result.QPS)
	}
	if input.Result.TTFTP95 > 0 {
		summary += fmt.Sprintf(", TTFT P95 %.0fms", input.Result.TTFTP95)
	}
	if input.Result.TPOTP95 > 0 {
		summary += fmt.Sprintf(", TPOT P95 %.1fms", input.Result.TPOTP95)
	}

	var sentences []string
	sentences = append(sentences, summary+".")

	if profile := benchmarkProfileSummary(input.Result); profile != "" {
		sentences = append(sentences, "Profile: "+profile+".")
	}
	if resources := benchmarkResourceSummary(input.Result); resources != "" {
		sentences = append(sentences, "Resources: "+resources+".")
	}
	if artifacts := benchmarkArtifactSummary(input.Result); artifacts != "" {
		sentences = append(sentences, "Artifacts: "+artifacts+".")
	}
	sentences = append(sentences, fmt.Sprintf("Config=%v.", input.Result.Config))
	return strings.Join(sentences, " ")
}

func (h *Harvester) generateLLMNote(ctx context.Context, input HarvestInput) (string, error) {
	if h.llm == nil {
		return "", fmt.Errorf("no LLM client")
	}
	prompt := fmt.Sprintf(
		"Summarize this benchmark result in 2-3 sentences with actionable insights.\n"+
			"Use only the facts below. Do not speculate about missing data or failure causes. Mention artifact ids when present.\n"+
			"Model: %s, Engine: %s\n"+
			"Engine version: %s\n"+
			"Benchmark id: %s, Config id: %s\n"+
			"Throughput: %.1f tok/s, QPS: %.2f, TTFT P95: %.0fms, TPOT P95: %.1fms\n"+
			"Benchmark profile: conc=%d, requests=%d, warmup=%d, rounds=%d, input_tokens=%d, max_tokens=%d, avg_input=%d, avg_output=%d\n"+
			"Resources: VRAM=%.0f MiB, RAM=%.0f MiB, CPU=%.1f%%, GPU=%.1f%%, Power=%.1f W\n"+
			"Config: %v",
		input.Task.Model, input.Task.Engine,
		input.Result.EngineVersion,
		input.Result.BenchmarkID, input.Result.ConfigID,
		input.Result.Throughput, input.Result.QPS, input.Result.TTFTP95, input.Result.TPOTP95,
		input.Result.Concurrency, input.Result.NumRequests, input.Result.WarmupCount, input.Result.Rounds,
		input.Result.InputTokens, input.Result.MaxTokens, input.Result.AvgInputTokens, input.Result.AvgOutputTokens,
		input.Result.VRAMMiB, input.Result.RAMMiB, input.Result.CPUUsagePct, input.Result.GPUUtilPct, input.Result.PowerWatts,
		input.Result.Config)
	resp, err := h.llm.ChatCompletion(ctx, []Message{
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func benchmarkProfileSummary(result HarvestResult) string {
	var parts []string
	if result.Concurrency > 0 {
		parts = append(parts, fmt.Sprintf("conc=%d", result.Concurrency))
	}
	if result.NumRequests > 0 {
		parts = append(parts, fmt.Sprintf("requests=%d", result.NumRequests))
	}
	if result.WarmupCount > 0 {
		parts = append(parts, fmt.Sprintf("warmup=%d", result.WarmupCount))
	}
	if result.Rounds > 0 {
		parts = append(parts, fmt.Sprintf("rounds=%d", result.Rounds))
	}
	if result.InputTokens > 0 {
		parts = append(parts, fmt.Sprintf("input=%d", result.InputTokens))
	}
	if result.MaxTokens > 0 {
		parts = append(parts, fmt.Sprintf("max_out=%d", result.MaxTokens))
	}
	if result.AvgInputTokens > 0 {
		parts = append(parts, fmt.Sprintf("avg_in=%d", result.AvgInputTokens))
	}
	if result.AvgOutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("avg_out=%d", result.AvgOutputTokens))
	}
	return strings.Join(parts, ", ")
}

func benchmarkResourceSummary(result HarvestResult) string {
	var parts []string
	if result.VRAMMiB > 0 {
		parts = append(parts, fmt.Sprintf("VRAM %.0f MiB", result.VRAMMiB))
	}
	if result.RAMMiB > 0 {
		parts = append(parts, fmt.Sprintf("RAM %.0f MiB", result.RAMMiB))
	}
	if result.CPUUsagePct > 0 {
		parts = append(parts, fmt.Sprintf("CPU %.1f%%", result.CPUUsagePct))
	}
	if result.GPUUtilPct > 0 {
		parts = append(parts, fmt.Sprintf("GPU %.1f%%", result.GPUUtilPct))
	}
	if result.PowerWatts > 0 {
		parts = append(parts, fmt.Sprintf("Power %.1f W", result.PowerWatts))
	}
	if len(parts) == 0 {
		return ""
	}
	if result.ExecutionPath != "" {
		parts = append(parts, "path "+result.ExecutionPath)
	}
	return strings.Join(parts, ", ")
}

func benchmarkArtifactSummary(result HarvestResult) string {
	var parts []string
	if result.BenchmarkID != "" {
		parts = append(parts, fmt.Sprintf("benchmark_id=%s", result.BenchmarkID))
	}
	if result.ConfigID != "" {
		parts = append(parts, fmt.Sprintf("config_id=%s", result.ConfigID))
	}
	return strings.Join(parts, ", ")
}
