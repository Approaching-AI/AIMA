package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePlanTasks(t *testing.T) {
	md := `# Exploration Plan

## Strategy
Test vllm on this device for the first time.

## Tasks
` + "```yaml\n" + `- kind: validate
  model: gemma-4-31B-it
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
    tensor_parallel_size: 2
    max_model_len: 4096
  benchmark:
    concurrency: [1, 4]
    input_tokens: [128, 512]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "first vllm test on this device"

- kind: tune
  model: qwen3.5-27b
  engine: sglang-kt
  engine_params:
    gpu_memory_utilization: 0.70
    cpu_offload_gb: 20
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 2
  reason: "reduce gmu to avoid OOM"
` + "```\n"

	tasks, err := parsePlanTasks(md)
	if err != nil {
		t.Fatalf("parsePlanTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].Kind != "validate" || tasks[0].Model != "gemma-4-31B-it" {
		t.Errorf("task 0: kind=%s model=%s", tasks[0].Kind, tasks[0].Model)
	}
	if tasks[0].EngineParams["tensor_parallel_size"] != 2 {
		t.Errorf("task 0 tp=%v", tasks[0].EngineParams["tensor_parallel_size"])
	}
	if len(tasks[0].Benchmark.Concurrency) != 2 {
		t.Errorf("task 0 concurrency=%v", tasks[0].Benchmark.Concurrency)
	}
	if tasks[1].Kind != "tune" || tasks[1].Engine != "sglang-kt" {
		t.Errorf("task 1: kind=%s engine=%s", tasks[1].Kind, tasks[1].Engine)
	}
}

func TestParseRecommendedConfigs(t *testing.T) {
	md := `# Exploration Summary

## Key Findings
- sglang-kt has 20% speedup for MoE models

## Recommended Configurations
` + "```yaml\n" + `- model: gemma-4-31B-it
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params:
    gpu_memory_utilization: 0.90
    tensor_parallel_size: 2
  performance:
    throughput_tps: 95.2
    latency_p50_ms: 42
  confidence: validated
  note: "first validation passed"
` + "```\n" + `
## Current Strategy
Focus on engine comparison.
`

	configs, err := parseRecommendedConfigs(md)
	if err != nil {
		t.Fatalf("parseRecommendedConfigs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if configs[0].Model != "gemma-4-31B-it" || configs[0].Confidence != "validated" {
		t.Errorf("config 0: model=%s confidence=%s", configs[0].Model, configs[0].Confidence)
	}
	if configs[0].Performance.ThroughputTPS != 95.2 {
		t.Errorf("config 0 throughput=%f", configs[0].Performance.ThroughputTPS)
	}
}

func TestExtractSection(t *testing.T) {
	tests := []struct {
		name    string
		md      string
		heading string
		want    string
	}{
		{
			name: "section at end of document (no trailing heading)",
			md: `# Main
## Details
Content here
with multiple lines`,
			heading: "## Details",
			want:    "\nContent here\nwith multiple lines",
		},
		{
			name: "section followed by same-level heading",
			md: `## Section A
Content A

## Section B
Content B`,
			heading: "## Section A",
			want:    "\nContent A\n\n",
		},
		{
			name: "section followed by higher-level heading (h1 after h2)",
			md: `# Title
## Subsection
Body text
# Next Top Level
More text`,
			heading: "## Subsection",
			want:    "\nBody text\n",
		},
		{
			name: "heading with embedded hash symbols (C# Results)",
			md: `## C# Results
Performance data here

## Conclusion
Final notes`,
			heading: "## C# Results",
			want:    "\nPerformance data here\n\n",
		},
		{
			name: "heading not found",
			md: `# Page
## Section A
Content`,
			heading: "## Missing",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSection(tt.md, tt.heading)
			if got != tt.want {
				t.Errorf("extractSection(%q, %q) = %q, want %q", tt.md, tt.heading, got, tt.want)
			}
		})
	}
}

func TestWorkspaceInit(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	if err := ws.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "experiments"))
	if err != nil || !info.IsDir() {
		t.Fatal("experiments/ dir not created")
	}
}

func TestWorkspaceReadWrite(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	if err := ws.WriteFile("plan.md", "# Test Plan\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	content, err := ws.ReadFile("plan.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if content != "# Test Plan\n" {
		t.Errorf("got %q", content)
	}
	if err := ws.AppendFile("plan.md", "more\n"); err != nil {
		t.Fatalf("AppendFile: %v", err)
	}
	content, _ = ws.ReadFile("plan.md")
	if !strings.HasSuffix(content, "more\n") {
		t.Errorf("append failed: %q", content)
	}
}

func TestWorkspaceReadOnlyGuard(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	for _, name := range []string{"device-profile.md", "available-combos.md", "knowledge-base.md"} {
		if err := ws.WriteFile(name, "hack"); err == nil {
			t.Errorf("WriteFile(%s) should fail for read-only doc", name)
		}
	}
}

func TestWorkspaceListDir(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	_ = os.WriteFile(filepath.Join(dir, "plan.md"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "summary.md"), []byte("x"), 0644)
	entries, err := ws.ListDir(".")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("got %d entries, want >= 2", len(entries))
	}
}

func TestWorkspaceGrepFile(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	_ = os.WriteFile(filepath.Join(dir, "plan.md"), []byte("line1\nfoo bar\nline3\n"), 0644)
	matches, err := ws.GrepFile("foo", "plan.md")
	if err != nil {
		t.Fatalf("GrepFile: %v", err)
	}
	if len(matches) != 1 || !strings.Contains(matches[0], "foo bar") {
		t.Errorf("grep results: %v", matches)
	}
}

func TestWorkspacePathEscape(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	_, err := ws.ReadFile("../../etc/passwd")
	if err == nil {
		t.Error("path escape should fail")
	}
}

func TestWriteExperimentResult(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	task := TaskSpec{
		Kind:   "validate",
		Model:  "gemma-4-31B-it",
		Engine: "vllm",
		EngineParams: map[string]any{
			"gpu_memory_utilization": 0.90,
			"tensor_parallel_size":   2,
		},
	}
	result := ExperimentResult{
		Status:     "completed",
		StartedAt:  "2026-04-09T20:15:03Z",
		DurationS:  342,
		ColdStartS: 45,
		Benchmarks: []BenchmarkEntry{
			{Concurrency: 1, InputTokens: 128, MaxTokens: 256,
				ThroughputTPS: 95.2, LatencyP50Ms: 42, LatencyP99Ms: 118},
		},
	}

	path, err := ws.WriteExperimentResult(1, task, result)
	if err != nil {
		t.Fatalf("WriteExperimentResult: %v", err)
	}

	content, _ := ws.ReadFile(path)
	if !strings.Contains(content, "gemma-4-31B-it") {
		t.Error("experiment missing model name")
	}
	if !strings.Contains(content, "completed") {
		t.Error("experiment missing status")
	}
	if !strings.Contains(content, "95.2") {
		t.Error("experiment missing throughput")
	}
}

func TestParsePlanFromWorkspace(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planMD := "# Exploration Plan\n\n## Strategy\nTest.\n\n## Tasks\n```yaml\n- kind: validate\n  model: test-model\n  engine: vllm\n  engine_params:\n    gpu_memory_utilization: 0.90\n  benchmark:\n    concurrency: [1]\n    input_tokens: [128]\n    max_tokens: [256]\n    requests_per_combo: 3\n  reason: \"test\"\n```\n"

	_ = ws.WriteFile("plan.md", planMD)
	tasks, err := ws.ParsePlan()
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Model != "test-model" {
		t.Errorf("ParsePlan: got %+v", tasks)
	}
}

func TestExtractRecommendations(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	summaryMD := "# Exploration Summary\n\n## Key Findings\n- vllm works\n\n## Recommended Configurations\n```yaml\n- model: test-model\n  engine: vllm\n  hardware: nvidia-rtx4090-x86\n  engine_params:\n    gpu_memory_utilization: 0.90\n  performance:\n    throughput_tps: 95.2\n    latency_p50_ms: 42\n  confidence: validated\n  note: \"first test\"\n```\n"

	_ = ws.WriteFile("summary.md", summaryMD)
	configs, err := ws.ExtractRecommendations()
	if err != nil {
		t.Fatalf("ExtractRecommendations: %v", err)
	}
	if len(configs) != 1 || configs[0].Model != "test-model" {
		t.Errorf("got %+v", configs)
	}
}

func TestExtractRecommendations_NoSummary(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	// summary.md doesn't exist yet
	configs, err := ws.ExtractRecommendations()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configs != nil {
		t.Errorf("expected nil configs, got %+v", configs)
	}
}

func TestRefreshFactDocuments(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	input := PlanInput{
		Hardware: HardwareInfo{
			Profile:  "nvidia-rtx4090-x86",
			GPUArch:  "Ada",
			GPUCount: 2,
			VRAMMiB:  49140,
		},
		LocalModels: []LocalModel{
			{Name: "qwen3-4b", Format: "safetensors", Type: "llm", SizeBytes: 7_500_000_000},
			{Name: "bge-m3", Format: "pytorch", Type: "embedding", SizeBytes: 2_000_000_000},
		},
		LocalEngines: []LocalEngine{
			{Name: "sglang-kt", Type: "sglang-kt", Runtime: "native", Features: []string{"cpu_gpu_hybrid_moe"},
				TunableParams: map[string]any{"gpu_memory_utilization": 0.90}},
			{Name: "vllm", Type: "vllm", Runtime: "container"},
		},
		ActiveDeploys: []DeployStatus{{Model: "qwen3-4b", Engine: "sglang-kt", Status: "running"}},
		SkipCombos: []SkipCombo{
			{Model: "qwen3-4b", Engine: "sglang-kt", Reason: "completed"},
		},
	}

	if err := ws.RefreshFactDocuments(input); err != nil {
		t.Fatalf("RefreshFactDocuments: %v", err)
	}

	// Check device-profile.md exists and has hardware info
	dp, _ := ws.ReadFile("device-profile.md")
	if !strings.Contains(dp, "49140") {
		t.Error("device-profile missing VRAM")
	}
	if !strings.Contains(dp, "qwen3-4b") {
		t.Error("device-profile missing model")
	}
	if !strings.Contains(dp, "sglang-kt") {
		t.Error("device-profile missing engine")
	}

	// Check available-combos.md
	ac, _ := ws.ReadFile("available-combos.md")
	if !strings.Contains(ac, "Already Explored") {
		t.Error("available-combos missing Already Explored section")
	}
	if !strings.Contains(ac, "bge-m3") {
		t.Error("available-combos missing bge-m3 in Incompatible")
	}

	// Check knowledge-base.md exists
	kb, _ := ws.ReadFile("knowledge-base.md")
	if !strings.Contains(kb, "Knowledge Base") {
		t.Error("knowledge-base.md missing header")
	}
}
