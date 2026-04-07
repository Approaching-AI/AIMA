package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	prompt := buildPlannerPrompt(input)
	resp, err := p.agent.llm.ChatCompletion(ctx, []Message{
		{Role: "system", Content: llmPlannerSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("LLM plan generation: %w", err)
	}
	return parsePlanResponse(resp.Content)
}

const llmPlannerSystemPrompt = `You are an AI inference optimization planner. Given device hardware info, knowledge gaps, and deployment state, generate an exploration plan as JSON.

Output format:
{"tasks":[{"kind":"validate|tune|open_question|compare","model":"...","engine":"...","params":{},"reason":"...","priority":0}]}

Rules:
- Prioritize deployed models without benchmarks (kind=validate)
- For tune tasks, suggest specific parameter ranges based on hardware VRAM
- Consider central advisories and validate them
- Max 5 tasks per plan
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
	var parsed struct {
		Tasks []PlanTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, fmt.Errorf("parse LLM plan response: %w", err)
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	id := fmt.Sprintf("%x", h)[:8]
	return &ExplorerPlan{
		ID:        id,
		Tier:      2,
		Tasks:     parsed.Tasks,
		Reasoning: content,
	}, nil
}
