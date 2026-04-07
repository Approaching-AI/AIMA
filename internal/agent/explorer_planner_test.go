package agent

import (
	"context"
	"fmt"
	"testing"
)

func TestRulePlanner_DeployedWithoutBenchmark(t *testing.T) {
	p := &RulePlanner{}
	plan, err := p.Plan(context.Background(), PlanInput{
		ActiveDeploys: []DeployStatus{
			{Model: "qwen3-8b", Engine: "vllm", Status: "running"},
		},
		Gaps:       nil,
		Advisories: nil,
		History:    nil,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) == 0 {
		t.Fatal("expected at least 1 task for deployed model without benchmark")
	}
	if plan.Tasks[0].Kind != "validate" {
		t.Errorf("first task kind = %q, want validate", plan.Tasks[0].Kind)
	}
	if plan.Tasks[0].Model != "qwen3-8b" {
		t.Errorf("first task model = %q, want qwen3-8b", plan.Tasks[0].Model)
	}
	if plan.Tier != 1 {
		t.Errorf("tier = %d, want 1", plan.Tier)
	}
}

func TestRulePlanner_AdvisoryPriority(t *testing.T) {
	p := &RulePlanner{}
	plan, err := p.Plan(context.Background(), PlanInput{
		Advisories: []Advisory{
			{ID: "adv-1", TargetModel: "qwen3-30b", TargetEngine: "vllm", Config: map[string]any{"gpu_memory_utilization": 0.78}},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) == 0 {
		t.Fatal("expected task for advisory")
	}
	found := false
	for _, task := range plan.Tasks {
		if task.Model == "qwen3-30b" && task.Kind == "validate" {
			found = true
			break
		}
	}
	if !found {
		t.Error("no validate task for advisory model qwen3-30b")
	}
}

func TestRulePlanner_GapsLimited(t *testing.T) {
	p := &RulePlanner{}
	gaps := make([]GapEntry, 10)
	for i := range gaps {
		gaps[i] = GapEntry{Model: fmt.Sprintf("model-%d", i), Engine: "vllm"}
	}
	plan, err := p.Plan(context.Background(), PlanInput{Gaps: gaps})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Should limit to 3 gap tasks per cycle
	gapTasks := 0
	for _, task := range plan.Tasks {
		if task.Reason == "knowledge gap" {
			gapTasks++
		}
	}
	if gapTasks > 3 {
		t.Errorf("gap tasks = %d, want <= 3", gapTasks)
	}
}

func TestRulePlanner_EmptyInput(t *testing.T) {
	p := &RulePlanner{}
	plan, err := p.Plan(context.Background(), PlanInput{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) != 0 {
		t.Errorf("tasks = %d, want 0 for empty input", len(plan.Tasks))
	}
}

func TestRulePlanner_OpenQuestions(t *testing.T) {
	p := &RulePlanner{}
	plan, err := p.Plan(context.Background(), PlanInput{
		OpenQuestions: []OpenQuestion{
			{ID: "q-1", Status: "untested", Model: "qwen3-8b"},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	found := false
	for _, task := range plan.Tasks {
		if task.Kind == "open_question" {
			found = true
			break
		}
	}
	if !found {
		t.Error("no open_question task for untested question")
	}
}
