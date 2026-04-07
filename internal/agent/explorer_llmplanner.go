package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LLMPlanner generates exploration plans using LLM reasoning (Tier 2).
type LLMPlanner struct {
	agent *Agent
}

func NewLLMPlanner(agent *Agent) *LLMPlanner {
	return &LLMPlanner{agent: agent}
}

func (p *LLMPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, error) {
	// B8: Filter input to reduce prompt size — only send local-hardware
	// gaps and limit collections to avoid multi-minute LLM calls.
	filtered := filterPlanInput(input)

	// The planner emits a pure JSON plan and does not request tool calls.
	// It intentionally bypasses profile-filtered tool discovery.
	prompt := buildPlannerPrompt(filtered)
	slog.Info("explorer: LLM planner calling ChatCompletion", "prompt_len", len(prompt))
	resp, err := p.agent.llm.ChatCompletion(ctx, []Message{
		{Role: "system", Content: llmPlannerSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("LLM plan generation: %w", err)
	}
	slog.Info("explorer: LLM planner response received", "content_len", len(resp.Content))
	plan, err := parsePlanResponse(resp.Content)
	if err != nil {
		return nil, err
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
	// B11: dedup against completed history
	plan.Tasks = deduplicateTasks(plan.Tasks, input.History)
	slog.Info("explorer: LLM plan filtering",
		"llm_proposed", preFilterCount, "after_local_filter", afterLocalFilter, "after_dedup", len(plan.Tasks))
	return plan, nil
}

const (
	maxLLMGaps         = 20
	maxLLMOpenQuestions = 10
	maxLLMHistory      = 5
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
		OpenQuestions:  oq,
		LocalModels:   input.LocalModels,
		LocalEngines:  input.LocalEngines,
		Event:         input.Event,
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

Rules:
- Prioritize deployed models without benchmarks (kind=validate)
- For tune tasks, suggest specific parameter values (not ranges) from the engine's tunable_params
- Consider central advisories and validate them
- Max 5 tasks per plan
- Only use task kinds listed above
- Skip model+engine combos that already appear in "history" with status "completed" unless you have a specific new config to test
- Be specific about WHY each task matters`

func buildPlannerPrompt(input PlanInput) string {
	promptData := map[string]any{
		"hardware":       input.Hardware,
		"gaps":           input.Gaps,
		"active_deploys": input.ActiveDeploys,
		"advisories":     input.Advisories,
		"open_questions": input.OpenQuestions,
		"local_models":   input.LocalModels,
		"local_engines":  input.LocalEngines,
		"history":        input.History,
		"event":          input.Event,
	}
	data, _ := json.MarshalIndent(promptData, "", "  ")
	return string(data)
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
