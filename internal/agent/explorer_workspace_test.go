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

func TestParsePlanTasks_NoYamlBlock(t *testing.T) {
	// D2: When Act phase writes no yaml block (just prose), should return nil, nil.
	md := `# Exploration Plan

## Tasks
No new tasks needed — all failures were environmental.
`
	tasks, err := parsePlanTasks(md)
	if err != nil {
		t.Fatalf("expected nil error for no-yaml Tasks, got: %v", err)
	}
	if tasks != nil {
		t.Fatalf("expected nil tasks, got %d", len(tasks))
	}
}

func TestParsePlanTasks_CommentOnlyYaml(t *testing.T) {
	// D2: When Act phase writes yaml block with only comments, should return nil tasks.
	md := `# Exploration Plan

## Tasks
` + "```yaml\n# No new tasks for this cycle\n# All combos are blocked\n```\n"
	tasks, err := parsePlanTasks(md)
	if err != nil {
		t.Fatalf("expected nil error for comment-only yaml, got: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestParsePlanTasks_WrappedTasksMap(t *testing.T) {
	md := `# Exploration Plan

## Tasks
` + "```yaml\n" + `tasks:
  - kind: validate
    model: qwen3-4b
    engine: vllm
    engine_params:
      tensor_parallel_size: 1
      gpu_memory_utilization: 0.9
    benchmark:
      concurrency: [1, 2, 4]
      input_tokens: [512, 1024]
      max_tokens: [256, 512]
      requests_per_combo: 10
    reason: "baseline validation"
` + "```\n"

	tasks, err := parsePlanTasks(md)
	if err != nil {
		t.Fatalf("parsePlanTasks(wrapped): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Kind != "validate" || tasks[0].Model != "qwen3-4b" || tasks[0].Engine != "vllm" {
		t.Fatalf("task = %+v", tasks[0])
	}
}

func TestParseRecommendedConfigs(t *testing.T) {
	md := `# Exploration Summary

## Key Findings
- sglang-kt has 20% speedup for MoE models

## Confirmed Blockers
` + "```yaml\n" + `[]
` + "```\n" + `

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

func TestParseRecommendedConfigs_WithScenario(t *testing.T) {
	md := `# Summary

## Recommended Configurations
` + "```yaml\n" + `- model: qwen3-4b
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params:
    gpu_memory_utilization: 0.9
  performance:
    throughput_tps: 445.6
    throughput_scenario: "concurrency=8, input=512, max_tokens=1024"
    latency_p50_ms: 25.0
    latency_scenario: "concurrency=1, input=128, max_tokens=256"
  confidence: validated
  note: "fast small LLM"
` + "```\n" + `
## Current Strategy
done
`
	configs, err := parseRecommendedConfigs(md)
	if err != nil {
		t.Fatalf("parseRecommendedConfigs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if configs[0].Performance.ThroughputScenario != "concurrency=8, input=512, max_tokens=1024" {
		t.Errorf("throughput_scenario = %q", configs[0].Performance.ThroughputScenario)
	}
	if configs[0].Performance.LatencyScenario != "concurrency=1, input=128, max_tokens=256" {
		t.Errorf("latency_scenario = %q", configs[0].Performance.LatencyScenario)
	}
}

func TestParseSummaryMachineReadableSections(t *testing.T) {
	md := `# Exploration Summary

## Key Findings
- vllm works

## Bugs And Failures
- port 8000 conflict

## Confirmed Blockers
` + "```yaml\n" + `- family: port_conflict
  scope: combo
  model: qwen3-4b
  engine: vllm
  reason: port 8000 is occupied
  retry_when: port allocator can move off busy ports
  confidence: confirmed
` + "```\n" + `

## Do Not Retry This Cycle
` + "```yaml\n" + `- model: qwen3-4b
  engine: vllm
  reason_family: port_conflict
  reason: port 8000 is occupied
` + "```\n" + `

## Evidence Ledger
` + "```yaml\n" + `- source: this_cycle
  kind: benchmark
  model: qwen3-4b
  engine: vllm
  evidence: benchmark_id=bench-001
  summary: 24 cells, 24 ok
  confidence: benchmark-backed
` + "```\n" + `

## Recommended Configurations
` + "```yaml\n" + `- model: qwen3-4b
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params:
    gpu_memory_utilization: 0.90
  performance:
    throughput_tps: 95.2
    latency_p50_ms: 42
  confidence: validated
  note: first validation
` + "```\n"

	blockers, err := parseConfirmedBlockers(md)
	if err != nil {
		t.Fatalf("parseConfirmedBlockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0].Family != "port_conflict" || blockers[0].RetryWhen == "" {
		t.Fatalf("blockers: %+v", blockers)
	}

	deny, err := parseDoNotRetryThisCycle(md)
	if err != nil {
		t.Fatalf("parseDoNotRetryThisCycle: %v", err)
	}
	if len(deny) != 1 || deny[0].ReasonFamily != "port_conflict" {
		t.Fatalf("denylist: %+v", deny)
	}

	evidence, err := parseEvidenceLedger(md)
	if err != nil {
		t.Fatalf("parseEvidenceLedger: %v", err)
	}
	if len(evidence) != 1 || evidence[0].Source != "this_cycle" || evidence[0].Kind != "benchmark" {
		t.Fatalf("evidence: %+v", evidence)
	}

	configs, err := parseRecommendedConfigs(md)
	if err != nil {
		t.Fatalf("parseRecommendedConfigs: %v", err)
	}
	if len(configs) != 1 || configs[0].Model != "qwen3-4b" {
		t.Fatalf("configs: %+v", configs)
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
	for _, name := range []string{"index.md", "device-profile.md", "available-combos.md", "knowledge-base.md", "experiment-facts.md"} {
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
		Status:        "completed",
		StartedAt:     "2026-04-09T20:15:03Z",
		DurationS:     342,
		ColdStartS:    45,
		BenchmarkID:   "bench-001",
		ConfigID:      "cfg-001",
		EngineVersion: "1.2.3",
		EngineImage:   "example/vllm:1.2.3",
		ResourceUsage: map[string]any{"vram_usage_mib": 1234},
		DeployConfig:  map[string]any{"tensor_parallel_size": 2},
		MatrixCells:   1,
		SuccessCells:  1,
		Benchmarks: []BenchmarkEntry{
			{Concurrency: 1, InputTokens: 128, MaxTokens: 256,
				ThroughputTPS: 95.2, TTFTP95Ms: 42, TPOTP95Ms: 118, BenchmarkID: "bench-001", ConfigID: "cfg-001"},
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
	for _, want := range []string{"bench-001", "cfg-001", "resource_usage", "tensor_parallel_size", "TTFT P95", "TPOT P95"} {
		if !strings.Contains(content, want) {
			t.Fatalf("experiment missing %q: %s", want, content)
		}
	}
}

func TestWriteExperimentResult_NoOutputStatus(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	task := TaskSpec{Kind: "validate", Model: "test-model", Engine: "vllm"}
	result := ExperimentResult{
		Status: "completed",
		Benchmarks: []BenchmarkEntry{
			{Concurrency: 1, InputTokens: 128, MaxTokens: 256,
				ThroughputTPS: 95.2, TTFTP95Ms: 42, TPOTP95Ms: 10},
			// Zero throughput + zero TTFT = no-output (not "ok")
			{Concurrency: 1, InputTokens: 8192, MaxTokens: 1024,
				ThroughputTPS: 0, TTFTP95Ms: 0, TPOTP95Ms: 0},
			// Zero throughput but has error = show error, not no-output
			{Concurrency: 1, InputTokens: 4096, MaxTokens: 1024,
				ThroughputTPS: 0, TTFTP95Ms: 0, TPOTP95Ms: 0, Error: "timeout"},
		},
	}

	path, err := ws.WriteExperimentResult(1, task, result)
	if err != nil {
		t.Fatalf("WriteExperimentResult: %v", err)
	}

	content, _ := ws.ReadFile(path)
	// First row: normal throughput → ok
	if !strings.Contains(content, "| 95.2 | 42 | 10 | ok |") {
		t.Error("expected ok status for successful benchmark")
	}
	// Second row: zero throughput, no error → no-output
	if !strings.Contains(content, "no-output") {
		t.Error("expected no-output status for zero throughput cell")
	}
	// Third row: zero throughput with error → show error
	if !strings.Contains(content, "| timeout |") {
		t.Error("expected timeout status for errored cell")
	}
}

func TestWriteExperimentResult_AllocatesUniqueOrdinal(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	task := TaskSpec{Kind: "validate", Model: "test-model", Engine: "vllm"}
	result := ExperimentResult{Status: "completed"}

	first, err := ws.WriteExperimentResult(1, task, result)
	if err != nil {
		t.Fatalf("first WriteExperimentResult: %v", err)
	}
	second, err := ws.WriteExperimentResult(1, task, result)
	if err != nil {
		t.Fatalf("second WriteExperimentResult: %v", err)
	}
	if first == second {
		t.Fatalf("paths should differ, got %q", first)
	}
	if !strings.Contains(first, "001-test-model-vllm.md") {
		t.Fatalf("first path = %q, want 001 ordinal", first)
	}
	if !strings.Contains(second, "002-test-model-vllm.md") {
		t.Fatalf("second path = %q, want 002 ordinal", second)
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

func TestLoadExperimentRecords(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	_, err := ws.WriteExperimentResult(1, TaskSpec{
		Kind:   "validate",
		Model:  "test-model",
		Engine: "vllm",
	}, ExperimentResult{
		Status: "completed",
		Benchmarks: []BenchmarkEntry{{
			Concurrency:   1,
			InputTokens:   128,
			MaxTokens:     256,
			ThroughputTPS: 88.0,
		}},
	})
	if err != nil {
		t.Fatalf("WriteExperimentResult: %v", err)
	}

	records, err := ws.LoadExperimentRecords()
	if err != nil {
		t.Fatalf("LoadExperimentRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records len=%d, want 1", len(records))
	}
	if records[0].Task.Model != "test-model" || records[0].Result.Status != "completed" {
		t.Fatalf("record = %+v, want parsed task/result", records[0])
	}
}

func TestLoadExperimentRecordsRecoversMalformedResultYAML(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	if err := ws.writeFactDocument("experiments/001-bad.md", "# Experiment 001\n\n## Task\n```yaml\nkind: validate\nmodel: bad-model\nengine: vllm\n```\n\n## Result\n```yaml\nstatus: failed\nerror: \"broken\n```\n"); err != nil {
		t.Fatalf("writeFactDocument: %v", err)
	}

	records, err := ws.LoadExperimentRecords()
	if err != nil {
		t.Fatalf("LoadExperimentRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records len=%d, want 1", len(records))
	}
	if records[0].Task.Model != "bad-model" {
		t.Fatalf("Task.Model = %q, want bad-model", records[0].Task.Model)
	}
	if records[0].Result.Status != "invalid_record" {
		t.Fatalf("Result.Status = %q, want invalid_record", records[0].Result.Status)
	}
	if !strings.Contains(records[0].Result.Error, "parse result yaml") {
		t.Fatalf("Result.Error = %q, want parse result yaml", records[0].Result.Error)
	}

	if err := ws.RefreshFactDocuments(PlanInput{}); err != nil {
		t.Fatalf("RefreshFactDocuments: %v", err)
	}
	ef, err := ws.ReadFile("experiment-facts.md")
	if err != nil {
		t.Fatalf("ReadFile experiment-facts.md: %v", err)
	}
	if !strings.Contains(ef, "invalid_record") {
		t.Fatalf("experiment-facts.md = %q, want invalid_record row", ef)
	}
}

func TestGenerateExperimentFacts_ClassifiesSignals(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	if _, err := ws.WriteExperimentResult(1, TaskSpec{
		Kind: "validate", Model: "ok-model", Engine: "vllm-nightly",
	}, ExperimentResult{
		Status:       "completed",
		MatrixCells:  2,
		SuccessCells: 2,
		Benchmarks: []BenchmarkEntry{{
			Concurrency:   1,
			InputTokens:   128,
			MaxTokens:     256,
			ThroughputTPS: 110,
			BenchmarkID:   "bench-ok",
		}},
	}); err != nil {
		t.Fatalf("WriteExperimentResult ok-model: %v", err)
	}
	if _, err := ws.WriteExperimentResult(2, TaskSpec{
		Kind: "validate", Model: "no-output-model", Engine: "vllm-nightly",
	}, ExperimentResult{
		Status:       "failed",
		Error:        "benchmark matrix: no successful cells (total=6)",
		MatrixCells:  6,
		SuccessCells: 0,
		Benchmarks: []BenchmarkEntry{{
			Concurrency: 1, InputTokens: 128, MaxTokens: 256, ThroughputTPS: 0,
		}},
	}); err != nil {
		t.Fatalf("WriteExperimentResult no-output-model: %v", err)
	}
	if _, err := ws.WriteExperimentResult(3, TaskSpec{
		Kind: "validate", Model: "deploy-fail-model", Engine: "sglang",
	}, ExperimentResult{
		Status: "failed",
		Error:  "pre-flight deploy: wait for deployed endpoint deploy-fail-model: timeout waiting for inference endpoint",
	}); err != nil {
		t.Fatalf("WriteExperimentResult deploy-fail-model: %v", err)
	}

	if err := ws.RefreshFactDocuments(PlanInput{}); err != nil {
		t.Fatalf("RefreshFactDocuments: %v", err)
	}
	ef, err := ws.ReadFile("experiment-facts.md")
	if err != nil {
		t.Fatalf("ReadFile experiment-facts.md: %v", err)
	}
	for _, want := range []string{"| Signal |", "benchmark_ok", "inference_no_output", "deploy_failed", "Best Rate"} {
		if !strings.Contains(ef, want) {
			t.Fatalf("experiment-facts.md missing %q: %s", want, ef)
		}
	}
}

func TestNormalizeSummaryMarkdown_AppendsRequiredSections(t *testing.T) {
	got := normalizeSummaryMarkdown("# Exploration Summary\n\n## Key Findings\n\n- one fact\n")
	for _, want := range []string{
		"## Bugs And Failures",
		"## Confirmed Blockers",
		"## Evidence Ledger",
		"## Recommended Configurations",
		"## Next Cycle Candidates",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalized summary missing %q: %s", want, got)
		}
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
				Artifact: "/tmp/sglang-kt", TunableParams: map[string]any{"gpu_memory_utilization": 0.90}},
			{Name: "vllm", Type: "vllm", Runtime: "container", Artifact: "zhiwen-vllm:v3.3.1"},
		},
		ComboFacts: []ComboFact{
			{Model: "qwen3-4b", Engine: "vllm", Runtime: "docker", Artifact: "zhiwen-vllm:v3.3.1", Status: "ready"},
			{Model: "qwen3-4b", Engine: "sglang-kt", Runtime: "native", Artifact: "/tmp/sglang-kt", Status: "ready"},
			{Model: "bge-m3", Engine: "vllm", Runtime: "docker", Artifact: "zhiwen-vllm:v3.3.1", Status: "blocked", Reason: "type mismatch"},
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
	if !strings.Contains(dp, "zhiwen-vllm:v3.3.1") {
		t.Error("device-profile missing engine artifact")
	}

	// Check available-combos.md
	ac, _ := ws.ReadFile("available-combos.md")
	if !strings.Contains(ac, "Ready Combos") {
		t.Error("available-combos missing Ready Combos section")
	}
	if !strings.Contains(ac, "Already Explored") {
		t.Error("available-combos missing Already Explored section")
	}
	if !strings.Contains(ac, "Blocked Combos") {
		t.Error("available-combos missing Blocked Combos section")
	}
	if !strings.Contains(ac, "resolver and local no-pull runtime checks passed") {
		t.Error("available-combos missing ready reason")
	}
	if !strings.Contains(ac, "type mismatch") {
		t.Error("available-combos missing blocked reason")
	}

	// Check knowledge-base.md exists
	kb, _ := ws.ReadFile("knowledge-base.md")
	if !strings.Contains(kb, "Knowledge Base") {
		t.Error("knowledge-base.md missing header")
	}
	if !strings.Contains(kb, "Frontier Coverage") {
		t.Error("knowledge-base.md missing Frontier Coverage section")
	}
	if !strings.Contains(kb, "prefer for new coverage") {
		t.Error("knowledge-base.md missing frontier coverage guidance")
	}

	// Check index.md exists and encodes authority rules
	index, _ := ws.ReadFile("index.md")
	if !strings.Contains(index, "Source Of Truth") {
		t.Error("index.md missing source-of-truth section")
	}
	if !strings.Contains(index, "Ready Combos") {
		t.Error("index.md missing ready-combo rule")
	}
	if !strings.Contains(index, "experiment-facts.md") {
		t.Error("index.md missing experiment-facts read order")
	}
	ef, _ := ws.ReadFile("experiment-facts.md")
	if !strings.Contains(ef, "No experiments yet") {
		t.Error("experiment-facts missing empty-state summary")
	}
}

func TestEnsureWorkingDocuments(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	if err := ws.EnsureWorkingDocuments(); err != nil {
		t.Fatalf("EnsureWorkingDocuments: %v", err)
	}

	plan, err := ws.ReadFile("plan.md")
	if err != nil {
		t.Fatalf("read plan.md: %v", err)
	}
	if !strings.Contains(plan, "## Task Board") {
		t.Fatal("plan.md missing Task Board template")
	}
	if !strings.Contains(plan, "summary.md blockers and evidence") {
		t.Fatal("plan.md missing summary blocker guidance")
	}

	summary, err := ws.ReadFile("summary.md")
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	if !strings.Contains(summary, "## Bugs And Failures") {
		t.Fatal("summary.md missing Bugs And Failures template")
	}
	if !strings.Contains(summary, "## Confirmed Blockers") {
		t.Fatal("summary.md missing Confirmed Blockers template")
	}
	if !strings.Contains(summary, "## Do Not Retry This Cycle") {
		t.Fatal("summary.md missing Do Not Retry This Cycle template")
	}
	if !strings.Contains(summary, "## Evidence Ledger") {
		t.Fatal("summary.md missing Evidence Ledger template")
	}
}
