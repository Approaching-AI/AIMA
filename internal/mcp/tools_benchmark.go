package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerBenchmarkTools(s *Server, deps *ToolDeps) {
	// benchmark.record
	s.RegisterTool(&Tool{
		Name:        "benchmark.record",
		Description: "Record a benchmark result with performance metrics. Auto-creates a Configuration record if needed.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"hardware":{"type":"string","description":"Hardware profile ID (e.g. nvidia-gb10-arm64)"},
			"engine":{"type":"string","description":"Engine type (e.g. vllm-nightly)"},
			"model":{"type":"string","description":"Model name (e.g. qwen3.5-35b-a3b)"},
			"device_id":{"type":"string","description":"Device ID from fleet (e.g. gb10)"},
			"config":{"type":"object","description":"Engine config used (gpu_memory_utilization, max_model_len, etc.)"},
			"concurrency":{"type":"integer","description":"Number of concurrent requests","default":1},
			"input_len_bucket":{"type":"string","description":"Input length category (e.g. short, medium, long)"},
			"output_len_bucket":{"type":"string","description":"Output length category"},
			"ttft_ms_p50":{"type":"number","description":"Time-to-first-token p50 in ms"},
			"ttft_ms_p95":{"type":"number","description":"Time-to-first-token p95 in ms"},
			"tpot_ms_p50":{"type":"number","description":"Time-per-output-token p50 in ms"},
			"tpot_ms_p95":{"type":"number","description":"Time-per-output-token p95 in ms"},
			"throughput_tps":{"type":"number","description":"Tokens per second (single request)"},
			"qps":{"type":"number","description":"Queries per second"},
			"vram_usage_mib":{"type":"integer","description":"VRAM usage in MiB"},
			"sample_count":{"type":"integer","description":"Number of samples in benchmark"},
			"stability":{"type":"string","description":"Stability assessment (stable, fluctuating, unstable)"},
			"notes":{"type":"string","description":"Free-form notes about the benchmark"}
		},"required":["hardware","engine","model","throughput_tps"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RecordBenchmark == nil {
				return ErrorResult("benchmark.record not implemented"), nil
			}
			data, err := deps.RecordBenchmark(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("record benchmark: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// benchmark.run
	s.RegisterTool(&Tool{
		Name:        "benchmark.run",
		Description: "Run a performance benchmark against a deployed model. Measures TTFT, TPOT, and throughput. Results auto-saved to database.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name (must match a deployed model)"},`+
				`"endpoint":{"type":"string","description":"OpenAI-compatible endpoint URL. Auto-detected from proxy if omitted."},`+
				`"concurrency":{"type":"integer","description":"Number of concurrent requests (default: 1)"},`+
				`"num_requests":{"type":"integer","description":"Total requests to send (default: 10)"},`+
				`"max_tokens":{"type":"integer","description":"Max output tokens per request (default: 256)"},`+
				`"input_tokens":{"type":"integer","description":"Approximate input length in tokens (default: 128)"},`+
				`"warmup":{"type":"integer","description":"Warmup requests to discard (default: 2)"},`+
				`"rounds":{"type":"integer","description":"Number of measurement rounds (default: 1). Multiple rounds improve statistical significance."},`+
				`"min_output_ratio":{"type":"number","description":"Minimum output tokens as ratio of max_tokens (0-1, default: 0). Retries requests below this threshold."},`+
				`"max_retries":{"type":"integer","description":"Per-request retry count on failure or output too short (default: 0)"},`+
				`"save":{"type":"boolean","description":"Save results to knowledge DB (default: true)"},`+
				`"hardware":{"type":"string","description":"Hardware profile ID for saving (e.g. nvidia-gb10-arm64)"},`+
				`"engine":{"type":"string","description":"Engine type for saving (e.g. vllm)"},`+
				`"notes":{"type":"string","description":"Free-form notes"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RunBenchmark == nil {
				return ErrorResult("benchmark.run not implemented"), nil
			}
			data, err := deps.RunBenchmark(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("benchmark run: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// benchmark.matrix
	s.RegisterTool(&Tool{
		Name:        "benchmark.matrix",
		Description: "Run a benchmark matrix across multiple concurrency levels and input/output length combinations.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name"},`+
				`"endpoint":{"type":"string","description":"OpenAI-compatible endpoint URL. Auto-detected from proxy if omitted."},`+
				`"concurrency_levels":{"type":"array","items":{"type":"integer"},"description":"Concurrency levels to test (default: [1,4])"},`+
				`"input_token_levels":{"type":"array","items":{"type":"integer"},"description":"Input lengths in tokens (default: [128,1024])"},`+
				`"max_token_levels":{"type":"array","items":{"type":"integer"},"description":"Output lengths in tokens (default: [128,512])"},`+
				`"requests_per_combo":{"type":"integer","description":"Requests per combination (default: 5)"},`+
				`"rounds":{"type":"integer","description":"Measurement rounds per combination (default: 1)"},`+
				`"min_output_ratio":{"type":"number","description":"Minimum output tokens ratio for retry (0-1, default: 0)"},`+
				`"max_retries":{"type":"integer","description":"Per-request retry count (default: 0)"},`+
				`"save":{"type":"boolean","description":"Save results to knowledge DB (default: true)"},`+
				`"hardware":{"type":"string","description":"Hardware profile ID"},`+
				`"engine":{"type":"string","description":"Engine type"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RunBenchmarkMatrix == nil {
				return ErrorResult("benchmark.matrix not implemented"), nil
			}
			data, err := deps.RunBenchmarkMatrix(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("benchmark matrix: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// benchmark.list
	s.RegisterTool(&Tool{
		Name:        "benchmark.list",
		Description: "List benchmark results from the database. Filter by model, hardware, or configuration ID.",
		InputSchema: schema(
			`"config_id":{"type":"string","description":"Filter by configuration ID"},`+
				`"hardware":{"type":"string","description":"Filter by hardware profile ID"},`+
				`"model":{"type":"string","description":"Filter by model name"},`+
				`"engine":{"type":"string","description":"Filter by engine type"},`+
				`"limit":{"type":"integer","description":"Max results to return (default: 20)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListBenchmarks == nil {
				return ErrorResult("benchmark.list not implemented"), nil
			}
			data, err := deps.ListBenchmarks(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("list benchmarks: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
