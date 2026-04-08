package agent

import (
	"context"
	"errors"
	"testing"
	"time"
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

type blockingPlannerLLM struct{}

func (blockingPlannerLLM) ChatCompletion(ctx context.Context, _ []Message, _ []ToolDefinition) (*Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLLMPlanner_TimesOut(t *testing.T) {
	oldTimeout := llmPlannerIdleTimeout
	llmPlannerIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { llmPlannerIdleTimeout = oldTimeout })

	p := NewLLMPlanner(NewAgent(blockingPlannerLLM{}, &mockTools{}))
	start := time.Now()
	_, err := p.Plan(context.Background(), PlanInput{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// Idle timeout fires via context.WithCancel, so the error is context.Canceled.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("planner idle timeout took too long: %v", elapsed)
	}
}
