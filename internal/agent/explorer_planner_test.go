package agent

import (
	"context"
	"fmt"
	"testing"
)

func TestRulePlanner_DeployedWithoutBenchmark(t *testing.T) {
	p := &RulePlanner{}
	plan, _, err := p.Plan(context.Background(), PlanInput{
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
	if plan.Tasks[0].Hardware != "" {
		t.Errorf("unexpected hardware without hardware input: %q", plan.Tasks[0].Hardware)
	}
	if plan.Tier != 1 {
		t.Errorf("tier = %d, want 1", plan.Tier)
	}
}

func TestRulePlanner_AdvisoryPriority(t *testing.T) {
	p := &RulePlanner{}
	plan, _, err := p.Plan(context.Background(), PlanInput{
		Hardware: HardwareInfo{Profile: "nvidia-gb10-arm64"},
		Advisories: []Advisory{
			{ID: "adv-1", TargetHardware: "nvidia-gb10-arm64", TargetModel: "qwen3-30b", TargetEngine: "vllm", Config: map[string]any{"gpu_memory_utilization": 0.78}},
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
			if task.Hardware != "nvidia-gb10-arm64" {
				t.Fatalf("hardware = %q, want nvidia-gb10-arm64", task.Hardware)
			}
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
	plan, _, err := p.Plan(context.Background(), PlanInput{Gaps: gaps})
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
	plan, _, err := p.Plan(context.Background(), PlanInput{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) != 0 {
		t.Errorf("tasks = %d, want 0 for empty input", len(plan.Tasks))
	}
}

func TestRulePlanner_OpenQuestions(t *testing.T) {
	p := &RulePlanner{}
	plan, _, err := p.Plan(context.Background(), PlanInput{
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
			if task.SourceRef != "q-1" {
				t.Fatalf("source_ref = %q, want q-1", task.SourceRef)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("no open_question task for untested question")
	}
}

func TestRulePlanner_DedupesDuplicateGaps(t *testing.T) {
	p := &RulePlanner{}
	plan, _, err := p.Plan(context.Background(), PlanInput{
		Hardware: HardwareInfo{Profile: "nvidia-rtx4090-x86", GPUCount: 1, VRAMMiB: 49140},
		Gaps: []GapEntry{
			{Model: "qwen3-30b-a3b", Engine: "sglang-kt", Hardware: "nvidia-rtx4090-x86"},
			{Model: "qwen3-30b-a3b", Engine: "sglang-kt", Hardware: "nvidia-rtx4090-x86"},
		},
		LocalModels: []LocalModel{
			{Name: "qwen3-30b-a3b", Format: "safetensors", Type: "llm", SizeBytes: 20 * 1024 * 1024 * 1024},
		},
		LocalEngines: []LocalEngine{
			{Name: "sglang-kt", Type: "sglang-kt", Runtime: "container"},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	count := 0
	for _, task := range plan.Tasks {
		if task.Model == "qwen3-30b-a3b" && task.Engine == "sglang-kt" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate gap tasks=%d, want 1", count)
	}
}

func TestRulePlanner_DedupesAcrossRulesByPriority(t *testing.T) {
	p := &RulePlanner{}
	plan, _, err := p.Plan(context.Background(), PlanInput{
		Hardware: HardwareInfo{Profile: "nvidia-rtx4090-x86", GPUCount: 1, VRAMMiB: 49140},
		ActiveDeploys: []DeployStatus{
			{Model: "qwen3-8b", Engine: "vllm", Status: "running"},
		},
		Gaps: []GapEntry{
			{Model: "qwen3-8b", Engine: "vllm", Hardware: "nvidia-rtx4090-x86"},
		},
		LocalModels: []LocalModel{
			{Name: "qwen3-8b", Format: "safetensors", Type: "llm", SizeBytes: 8 * 1024 * 1024 * 1024},
		},
		LocalEngines: []LocalEngine{
			{Name: "vllm", Type: "vllm", Runtime: "container"},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	count := 0
	var reason string
	for _, task := range plan.Tasks {
		if task.Model == "qwen3-8b" && task.Engine == "vllm" {
			count++
			reason = task.Reason
		}
	}
	if count != 1 {
		t.Fatalf("tasks for qwen3-8b/vllm=%d, want 1", count)
	}
	if reason != "deployed without benchmark baseline" {
		t.Fatalf("reason=%q, want deployed baseline reason", reason)
	}
}
