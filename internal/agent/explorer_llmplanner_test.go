package agent

import (
	"context"
	"testing"
)

func TestLLMPlanner_ParsesStructuredResponse(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{
			Content: `{"tasks":[{"kind":"validate","model":"qwen3-8b","engine":"vllm","reason":"test baseline"}]}`,
		}},
	}
	p := NewLLMPlanner(NewAgent(llm, &mockTools{}))
	plan, err := p.Plan(context.Background(), PlanInput{
		Hardware: HardwareInfo{Profile: "nvidia-rtx4090-x86", VRAMMiB: 24576},
		Gaps:     []GapEntry{{Model: "qwen3-8b", Engine: "vllm"}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Tier != 2 {
		t.Errorf("tier = %d, want 2", plan.Tier)
	}
	if len(plan.Tasks) == 0 {
		t.Fatal("expected tasks from LLM response")
	}
}

func TestLLMPlanner_FallbackOnInvalidJSON(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "I can't generate a plan right now."}},
	}
	p := NewLLMPlanner(NewAgent(llm, &mockTools{}))
	_, err := p.Plan(context.Background(), PlanInput{})
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
}
