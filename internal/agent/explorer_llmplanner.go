package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// LLMPlanner generates exploration plans using LLM reasoning (Tier 2).
type LLMPlanner struct {
	agent *Agent
}

// llmPlannerIdleTimeout is the maximum silence (no streaming tokens) before
// the planner gives up.  As long as the LLM keeps producing tokens (content or
// reasoning), the deadline keeps sliding forward.
var llmPlannerIdleTimeout = 30 * time.Second

func NewLLMPlanner(agent *Agent) *LLMPlanner {
	return &LLMPlanner{agent: agent}
}

func (p *LLMPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	// B8: Filter input to reduce prompt size — only send local-hardware
	// gaps and limit collections to avoid multi-minute LLM calls.
	filtered := filterPlanInput(input)

	// The planner emits a pure JSON plan and does not request tool calls.
	// It intentionally bypasses profile-filtered tool discovery.
	prompt := buildPlannerPrompt(filtered)
	slog.Info("explorer: LLM planner calling ChatCompletion", "prompt_len", len(prompt))
	planCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Idle timer: cancels the context only when the stream stops producing
	// tokens for llmPlannerIdleTimeout.  Every delta resets the timer.
	idleTimer := time.AfterFunc(llmPlannerIdleTimeout, cancel)
	defer idleTimer.Stop()
	messages := []Message{
		{Role: "system", Content: llmPlannerSystemPrompt},
		{Role: "user", Content: prompt},
	}
	var (
		resp             *Response
		err              error
		firstDeltaLogged bool
		streamContentLen int
		streamReasonLen  int
		lastProgressLog  time.Time
		startedAt        = time.Now()
	)
	if streamer, ok := p.agent.llm.(StreamingLLMClient); ok {
		resp, err = streamer.ChatCompletionStream(planCtx, messages, nil, func(delta CompletionDelta) {
			idleTimer.Reset(llmPlannerIdleTimeout)
			streamContentLen += len(delta.Content)
			streamReasonLen += len(delta.ReasoningContent)
			if !firstDeltaLogged {
				firstDeltaLogged = true
				slog.Info("explorer: LLM planner first stream delta",
					"after", time.Since(startedAt),
					"content_len", len(delta.Content),
					"reasoning_len", len(delta.ReasoningContent))
				lastProgressLog = time.Now()
				return
			}
			if lastProgressLog.IsZero() || time.Since(lastProgressLog) >= 2*time.Second {
				lastProgressLog = time.Now()
				slog.Info("explorer: LLM planner streaming",
					"elapsed", time.Since(startedAt),
					"content_len", streamContentLen,
					"reasoning_len", streamReasonLen)
			}
		})
	} else {
		resp, err = p.agent.llm.ChatCompletion(planCtx, messages, nil)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("LLM plan generation: %w", err)
	}
	slog.Info("explorer: LLM planner response received", "content_len", len(resp.Content))
	plan, err := parsePlanResponse(resp.Content)
	if err != nil {
		return nil, 0, err
	}
	defaultHardware := firstTaskHardware(input.Hardware.Profile, input.Hardware.GPUArch)
	localModels := toSet(input.LocalModels)
	localEngineTypes := localEngineTypeSet(input.LocalEngines)
	modelFormats := localModelFormatMap(input.LocalModels)
	modelTypes := localModelTypeMap(input.LocalModels)
	totalVRAMMiB := input.Hardware.VRAMMiB * input.Hardware.GPUCount
	if totalVRAMMiB == 0 {
		totalVRAMMiB = input.Hardware.VRAMMiB
	}
	for i := range plan.Tasks {
		if plan.Tasks[i].Hardware == "" {
			plan.Tasks[i].Hardware = defaultHardware
		}
	}
	preFilterCount := len(plan.Tasks)
	for _, t := range plan.Tasks {
		slog.Info("explorer: LLM proposed task", "kind", t.Kind, "model", t.Model, "engine", t.Engine, "reason", t.Reason)
	}
	// Post-filter: remove tasks referencing models/engines not on this device + format/type/VRAM check
	plan.Tasks = filterLocalTasks(plan.Tasks, localModels, localEngineTypes, modelFormats, modelTypes, input.LocalModels, totalVRAMMiB)
	afterLocalFilter := len(plan.Tasks)
	slog.Info("explorer: LLM plan filtering",
		"llm_proposed", preFilterCount, "after_local_filter", afterLocalFilter)
	return plan, resp.TotalTokens, nil
}

const (
	maxLLMGaps          = 20
	maxLLMOpenQuestions = 10
	maxLLMHistory       = 5
)

// filterPlanInput reduces PlanInput to a size suitable for LLM prompts.
// Only gaps matching local hardware and locally available resources are kept.
func filterPlanInput(input PlanInput) PlanInput {
	localHW := firstTaskHardware(input.Hardware.Profile, input.Hardware.GPUArch)
	localModels := toSet(input.LocalModels)
	localEngineTypes := localEngineTypeSet(input.LocalEngines)
	modelFormats := localModelFormatMap(input.LocalModels)
	modelTypes := localModelTypeMap(input.LocalModels)
	totalVRAMMiB := input.Hardware.VRAMMiB * input.Hardware.GPUCount
	if totalVRAMMiB == 0 {
		totalVRAMMiB = input.Hardware.VRAMMiB
	}

	// Filter gaps to local hardware AND locally available resources AND format/type/VRAM compatibility
	var localGaps []GapEntry
	for _, g := range input.Gaps {
		if g.Hardware != localHW && g.Hardware != "" {
			continue
		}
		if !isLocallyAvailable(g.Model, g.Engine, localModels, localEngineTypes) {
			continue
		}
		if !engineFormatCompatible(g.Engine, modelFormats[g.Model]) {
			continue
		}
		if !engineSupportsModelType(g.Engine, modelTypes[g.Model]) {
			continue
		}
		if !modelFitsVRAM(g.Model, input.LocalModels, totalVRAMMiB) {
			continue
		}
		localGaps = append(localGaps, g)
	}
	if len(localGaps) > maxLLMGaps {
		localGaps = localGaps[:maxLLMGaps]
	}

	// Cap open questions
	oq := input.OpenQuestions
	if len(oq) > maxLLMOpenQuestions {
		oq = oq[:maxLLMOpenQuestions]
	}

	// Cap history
	hist := input.History
	if len(hist) > maxLLMHistory {
		hist = hist[:maxLLMHistory]
	}

	return PlanInput{
		Hardware:      input.Hardware,
		Gaps:          localGaps,
		ActiveDeploys: input.ActiveDeploys,
		Advisories:    input.Advisories,
		History:       hist,
		OpenQuestions: oq,
		LocalModels:   input.LocalModels,
		LocalEngines:  input.LocalEngines,
		Event:         input.Event,
		SkipCombos:    input.SkipCombos, // pass through unfiltered — compact key-value pairs
	}
}

const llmPlannerSystemPrompt = `You are an AI inference optimization planner for edge devices. Given device hardware info, locally available models/engines, knowledge gaps, and deployment state, generate an exploration plan as JSON.

CRITICAL CONSTRAINTS:
- You can ONLY use models listed in "local_models" — these are physically present on the device.
- You can ONLY use engines listed in "local_engines" — these are installed and ready to run on the device.
- Do NOT suggest downloading new models or engines. Everything must already be available locally.
- Do NOT plan tasks for model+engine combos that are not locally available.
- Model-engine format compatibility: llamacpp requires GGUF format models; vllm/sglang/sglang-kt use safetensors format. Check the model's "format" field and only pair with compatible engines.

Output format:
{"tasks":[{"kind":"validate|tune|open_question","model":"...","engine":"...","params":{},"reason":"...","priority":0}]}

Task kinds:
- validate: benchmark a model+engine config to establish baseline performance
- tune: adjust engine-specific parameters to optimize performance. Use ONLY parameters from the engine's "tunable_params" field. Each param value in "params" must be a single value, not an array.
- open_question: test a specific hypothesis from the open questions list

Engine metadata:
- Each engine in "local_engines" includes "tunable_params" (the knobs you can adjust) and "features" (capabilities like cpu_gpu_hybrid_moe).
- For tune tasks, choose parameters from tunable_params and suggest specific values based on the hardware (VRAM, GPU count, CPU cores).
- Pay attention to engine "features" and "notes" — they describe the engine's architecture (e.g., CPU+GPU hybrid means some params control CPU offloading).

Strategy — BREADTH FIRST:
- First priority: validate tasks for DIFFERENT models to cover as many gaps as possible. Spread across distinct model+engine combos rather than repeating the same model.
- Second priority: tune tasks ONLY for models that already have a completed validate baseline in "history". Never tune a model that hasn't been validated yet.
- Third priority: open_question tasks to test hypotheses.

CRITICAL — SKIP COMBOS:
The "skip_combos" list contains model+engine pairs that have ALREADY been explored on this device.
- "completed": validated successfully — do NOT propose validate tasks for these.
- "failed:N": failed N times — do NOT propose these at all.
This list is authoritative and complete. NEVER include a skip_combos entry in your plan.
If all gaps are covered by skip_combos, return {"tasks":[]}.

Rules:
- For tune tasks, suggest specific parameter values (not ranges) from the engine's tunable_params
- Consider central advisories and validate them
- Max 5 tasks per plan
- Only use task kinds listed above
- Be specific about WHY each task matters
- Keep your reasoning concise. Focus on selecting the right tasks, not exhaustive analysis.`

func buildPlannerPrompt(input PlanInput) string {
	type promptHistory struct {
		Kind      string `json:"kind"`
		Model     string `json:"model,omitempty"`
		Engine    string `json:"engine,omitempty"`
		Status    string `json:"status"`
		Goal      string `json:"goal,omitempty"`
		SourceRef string `json:"source_ref,omitempty"`
		Error     string `json:"error,omitempty"`
		Summary   string `json:"summary,omitempty"`
	}
	type promptQuestion struct {
		ID       string `json:"id"`
		Hardware string `json:"hardware,omitempty"`
		Model    string `json:"model,omitempty"`
		Engine   string `json:"engine,omitempty"`
		Status   string `json:"status"`
		Question string `json:"question"`
	}
	type promptAdvisory struct {
		ID         string         `json:"id"`
		Type       string         `json:"type"`
		Hardware   string         `json:"hardware,omitempty"`
		Model      string         `json:"model,omitempty"`
		Engine     string         `json:"engine,omitempty"`
		Config     map[string]any `json:"config,omitempty"`
		Confidence string         `json:"confidence,omitempty"`
		Reasoning  string         `json:"reasoning,omitempty"`
	}
	type promptModel struct {
		Name    string `json:"name"`
		Format  string `json:"format"`
		Type    string `json:"type"`
		SizeGiB int64  `json:"size_gib"`
	}
	type promptEngine struct {
		Type          string   `json:"type"`
		Runtime       string   `json:"runtime"`
		Features      []string `json:"features,omitempty"`
		Notes         string   `json:"notes,omitempty"`
		TunableParams []string `json:"tunable_params,omitempty"`
	}

	history := make([]promptHistory, 0, len(input.History))
	for _, item := range input.History {
		history = append(history, promptHistory{
			Kind:      item.Kind,
			Model:     item.ModelID,
			Engine:    item.EngineID,
			Status:    item.Status,
			Goal:      truncatePromptText(item.Goal, 120),
			SourceRef: item.SourceRef,
			Error:     truncatePromptText(item.Error, 120),
			Summary:   truncatePromptText(item.SummaryJSON, 160),
		})
	}

	openQuestions := make([]promptQuestion, 0, len(input.OpenQuestions))
	for _, item := range input.OpenQuestions {
		openQuestions = append(openQuestions, promptQuestion{
			ID:       item.ID,
			Hardware: item.Hardware,
			Model:    item.Model,
			Engine:   item.Engine,
			Status:   item.Status,
			Question: truncatePromptText(item.Question, 160),
		})
	}

	advisories := make([]promptAdvisory, 0, len(input.Advisories))
	for _, item := range input.Advisories {
		advisories = append(advisories, promptAdvisory{
			ID:         item.ID,
			Type:       item.Type,
			Hardware:   item.TargetHardware,
			Model:      item.TargetModel,
			Engine:     item.TargetEngine,
			Config:     item.Config,
			Confidence: item.Confidence,
			Reasoning:  truncatePromptText(item.Reasoning, 160),
		})
	}

	localModels := make([]promptModel, 0, len(input.LocalModels))
	for _, item := range input.LocalModels {
		localModels = append(localModels, promptModel{
			Name:    item.Name,
			Format:  item.Format,
			Type:    item.Type,
			SizeGiB: item.SizeBytes / (1024 * 1024 * 1024),
		})
	}

	localEngines := make([]promptEngine, 0, len(input.LocalEngines))
	for _, item := range input.LocalEngines {
		keys := make([]string, 0, len(item.TunableParams))
		for key := range item.TunableParams {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		localEngines = append(localEngines, promptEngine{
			Type:          item.Type,
			Runtime:       item.Runtime,
			Features:      item.Features,
			Notes:         truncatePromptText(item.Notes, 120),
			TunableParams: keys,
		})
	}

	promptData := map[string]any{
		"hardware":       input.Hardware,
		"gaps":           input.Gaps,
		"active_deploys": input.ActiveDeploys,
		"advisories":     advisories,
		"open_questions": openQuestions,
		"local_models":   localModels,
		"local_engines":  localEngines,
		"history":        history,
		"event":          input.Event,
		"skip_combos":    input.SkipCombos,
	}
	data, _ := json.Marshal(promptData)
	return string(data)
}

func truncatePromptText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func parsePlanResponse(content string) (*ExplorerPlan, error) {
	// Strip markdown code fences that LLMs commonly wrap JSON in
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		if i := strings.Index(trimmed, "\n"); i != -1 {
			trimmed = trimmed[i+1:]
		}
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	var parsed struct {
		Tasks []PlanTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, fmt.Errorf("parse LLM plan response: %w", err)
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	id := fmt.Sprintf("%x", h)[:8]
	return &ExplorerPlan{
		ID:        id,
		Tier:      2,
		Tasks:     parsed.Tasks,
		Reasoning: trimmed, // O6: use stripped version, not raw content with code fences
	}, nil
}

// filterLocalTasks removes tasks whose model or engine is not locally available,
// where the model format/type is incompatible, or where the model won't fit in VRAM.
func filterLocalTasks(tasks []PlanTask, localModels, localEngines map[string]bool, modelFormats, modelTypes map[string]string, allModels []LocalModel, totalVRAMMiB int) []PlanTask {
	if len(localModels) == 0 && len(localEngines) == 0 {
		return tasks
	}
	filtered := tasks[:0]
	for _, t := range tasks {
		if !isLocallyAvailable(t.Model, t.Engine, localModels, localEngines) {
			slog.Info("explorer: filter rejected (not local)", "model", t.Model, "engine", t.Engine)
			continue
		}
		if !engineFormatCompatible(t.Engine, modelFormats[t.Model]) {
			slog.Info("explorer: filter rejected (format)", "model", t.Model, "engine", t.Engine, "format", modelFormats[t.Model])
			continue
		}
		if !engineSupportsModelType(t.Engine, modelTypes[t.Model]) {
			slog.Info("explorer: filter rejected (type)", "model", t.Model, "engine", t.Engine, "type", modelTypes[t.Model])
			continue
		}
		if !modelFitsVRAM(t.Model, allModels, totalVRAMMiB) {
			slog.Info("explorer: filter rejected (VRAM)", "model", t.Model, "total_vram_mib", totalVRAMMiB)
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}
