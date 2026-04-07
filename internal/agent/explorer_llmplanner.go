package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	resp, err := p.agent.llm.ChatCompletion(ctx, []Message{
		{Role: "system", Content: llmPlannerSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("LLM plan generation: %w", err)
	}
	plan, err := parsePlanResponse(resp.Content)
	if err != nil {
		return nil, err
	}
	defaultHardware := firstTaskHardware(input.Hardware.Profile, input.Hardware.GPUArch)
	for i := range plan.Tasks {
		if plan.Tasks[i].Hardware == "" {
			plan.Tasks[i].Hardware = defaultHardware
		}
	}
	return plan, nil
}

const (
	maxLLMGaps          = 20
	maxLLMOpenQuestions  = 10
	maxLLMHistory       = 5
)

// filterPlanInput reduces PlanInput to a size suitable for LLM prompts.
// Only gaps matching local hardware are kept; collections are capped.
func filterPlanInput(input PlanInput) PlanInput {
	localHW := firstTaskHardware(input.Hardware.Profile, input.Hardware.GPUArch)

	// Filter gaps to local hardware only
	var localGaps []GapEntry
	for _, g := range input.Gaps {
		if g.Hardware == localHW || g.Hardware == "" {
			localGaps = append(localGaps, g)
		}
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
		Event:         input.Event,
	}
}

const llmPlannerSystemPrompt = `You are an AI inference optimization planner. Given device hardware info, knowledge gaps, and deployment state, generate an exploration plan as JSON.

Output format:
{"tasks":[{"kind":"validate|tune|open_question","model":"...","engine":"...","params":{},"reason":"...","priority":0}]}

Task kinds:
- validate: benchmark a model+engine config to establish baseline performance
- tune: search parameter space (quantization, batch_size, tp_size) to optimize performance
- open_question: test a specific hypothesis from the open questions list

Rules:
- Prioritize deployed models without benchmarks (kind=validate)
- For tune tasks, suggest specific parameter ranges based on hardware VRAM
- Consider central advisories and validate them
- Max 5 tasks per plan
- Only use task kinds listed above
- Be specific about WHY each task matters`

func buildPlannerPrompt(input PlanInput) string {
	data, _ := json.MarshalIndent(map[string]any{
		"hardware":       input.Hardware,
		"gaps":           input.Gaps,
		"active_deploys": input.ActiveDeploys,
		"advisories":     input.Advisories,
		"open_questions": input.OpenQuestions,
		"event":          input.Event,
	}, "", "  ")
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
