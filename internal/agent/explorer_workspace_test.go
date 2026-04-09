package agent

import (
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
