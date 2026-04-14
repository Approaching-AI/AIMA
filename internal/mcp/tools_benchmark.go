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
			"modality":{"type":"string","description":"Benchmark modality: llm (default), vlm, embedding, tts, asr, image_gen, video_gen","enum":["llm","vlm","embedding","tts","asr","image_gen","video_gen"]},
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
			"ram_usage_mib":{"type":"integer","description":"Host RAM usage in MiB during the run"},
			"power_draw_watts":{"type":"number","description":"GPU power draw in watts during the run"},
			"gpu_utilization_pct":{"type":"number","description":"GPU utilization percent during the run"},
			"cpu_usage_pct":{"type":"number","description":"Host CPU usage percent during the run"},
			"sample_count":{"type":"integer","description":"Number of samples in benchmark"},
			"stability":{"type":"string","description":"Stability assessment (stable, fluctuating, unstable)"},
			"notes":{"type":"string","description":"Free-form notes about the benchmark"},
			"rtf_p50":{"type":"number","description":"[TTS/ASR] Real-time factor p50"},
			"rtf_p95":{"type":"number","description":"[TTS/ASR] Real-time factor p95"},
			"ttfa_p50_ms":{"type":"number","description":"[TTS] Time-to-first-audio p50 ms"},
			"audio_throughput":{"type":"number","description":"[TTS] Audio seconds generated per wall second"},
			"asr_throughput":{"type":"number","description":"[ASR] Audio hours processed per wall hour"},
			"latency_p50_ms":{"type":"number","description":"[T2I] Generation latency p50 ms"},
			"latency_p95_ms":{"type":"number","description":"[T2I] Generation latency p95 ms"},
			"images_per_sec":{"type":"number","description":"[T2I] Throughput"},
			"image_width":{"type":"integer","description":"[T2I] Image width"},
			"image_height":{"type":"integer","description":"[T2I] Image height"},
			"video_latency_p50_s":{"type":"number","description":"[T2V] Generation latency p50 seconds"},
			"videos_per_hour":{"type":"number","description":"[T2V] Throughput"},
			"video_width":{"type":"integer","description":"[T2V] Video width"},
			"video_height":{"type":"integer","description":"[T2V] Video height"},
			"video_duration_s":{"type":"number","description":"[T2V] Video duration seconds"},
			"video_fps":{"type":"integer","description":"[T2V] Frame rate"},
			"video_steps":{"type":"integer","description":"[T2V] Denoising steps"}
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
		Description: "Run a performance benchmark against a deployed model. Measures TTFT, TPOT, and throughput for LLM/VLM, or modality-specific metrics for TTS/ASR/T2I/T2V. Results auto-saved to database.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name (must match a deployed model)"},`+
				`"endpoint":{"type":"string","description":"OpenAI-compatible endpoint URL. Auto-detected from proxy if omitted."},`+
				`"modality":{"type":"string","description":"Benchmark modality: llm (default), vlm, embedding, tts, asr, image_gen, video_gen","enum":["llm","vlm","embedding","tts","asr","image_gen","video_gen"]},`+
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
				`"deploy_config":{"type":"object","description":"Resolved engine startup config to persist as the Configuration record when saving results."},`+
				`"resolved_config":{"type":"object","description":"Deprecated alias of deploy_config."},`+
				`"notes":{"type":"string","description":"Free-form notes"},`+
				`"image_urls":{"type":"array","items":{"type":"string"},"description":"[VLM] Image URLs for vision benchmark"},`+
				`"voice":{"type":"string","description":"[TTS] Voice name (e.g. alloy)"},`+
				`"audio_format":{"type":"string","description":"[TTS] Output format: pcm, mp3, wav"},`+
				`"texts":{"type":"array","items":{"type":"string"},"description":"[TTS] Test text corpus"},`+
				`"audio_files":{"type":"array","items":{"type":"string"},"description":"[ASR] Audio file paths for transcription benchmark"},`+
				`"language":{"type":"string","description":"[ASR] Recognition language (e.g. zh, en)"},`+
				`"prompt":{"type":"string","description":"[LLM/VLM/embedding/T2I/T2V] Prompt override"},`+
				`"width":{"type":"integer","description":"[T2I/T2V] Output width in pixels"},`+
				`"height":{"type":"integer","description":"[T2I/T2V] Output height in pixels"},`+
				`"steps":{"type":"integer","description":"[T2I/T2V] Inference steps"},`+
				`"guidance_scale":{"type":"number","description":"[T2I/T2V] CFG guidance scale"},`+
				`"num_images":{"type":"integer","description":"[T2I] Images per request"},`+
				`"duration_s":{"type":"number","description":"[T2V] Video duration in seconds"},`+
				`"fps":{"type":"integer","description":"[T2V] Video frame rate"},`+
				`"input_image_url":{"type":"string","description":"[T2V/I2V] Input image URL for image-to-video"}`,
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
		Description: "Run a benchmark matrix across multiple concurrency levels and input/output length combinations. Supports multi-modal sweep dimensions.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name"},`+
				`"endpoint":{"type":"string","description":"OpenAI-compatible endpoint URL. Auto-detected from proxy if omitted."},`+
				`"modality":{"type":"string","description":"Benchmark modality (default: llm)","enum":["llm","vlm","embedding","tts","asr","image_gen","video_gen"]},`+
				`"concurrency_levels":{"type":"array","items":{"type":"integer"},"description":"Concurrency levels to test (default: [1,4])"},`+
				`"input_token_levels":{"type":"array","items":{"type":"integer"},"description":"Input lengths in tokens (default: [128,1024])"},`+
				`"max_token_levels":{"type":"array","items":{"type":"integer"},"description":"Output lengths in tokens (default: [128,512])"},`+
				`"requests_per_combo":{"type":"integer","description":"Requests per combination (default: 5)"},`+
				`"rounds":{"type":"integer","description":"Measurement rounds per combination (default: 1)"},`+
				`"min_output_ratio":{"type":"number","description":"Minimum output tokens ratio for retry (0-1, default: 0)"},`+
				`"max_retries":{"type":"integer","description":"Per-request retry count (default: 0)"},`+
				`"save":{"type":"boolean","description":"Save results to knowledge DB (default: true)"},`+
				`"hardware":{"type":"string","description":"Hardware profile ID"},`+
				`"engine":{"type":"string","description":"Engine type"},`+
				`"deploy_config":{"type":"object","description":"Resolved engine startup config to persist for every saved cell."},`+
				`"text_lengths":{"type":"array","items":{"type":"string"},"description":"[TTS] Test texts of different lengths"},`+
				`"audio_file_groups":{"type":"array","items":{"type":"array","items":{"type":"string"}},"description":"[ASR] Audio file groups by duration"},`+
				`"resolutions":{"type":"array","items":{"type":"string"},"description":"[T2I/T2V] Resolution levels (e.g. 512x512, 1024x1024)"},`+
				`"step_levels":{"type":"array","items":{"type":"integer"},"description":"[T2I/T2V] Inference step levels"},`+
				`"video_durations":{"type":"array","items":{"type":"number"},"description":"[T2V] Video duration levels in seconds"},`+
				`"video_step_levels":{"type":"array","items":{"type":"integer"},"description":"[T2V] Denoising step levels"}`,
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
		Description: "List benchmark results from the database. Filter by model, hardware, modality, or configuration ID.",
		InputSchema: schema(
			`"config_id":{"type":"string","description":"Filter by configuration ID"},` +
				`"hardware":{"type":"string","description":"Filter by hardware profile ID"},` +
				`"model":{"type":"string","description":"Filter by model name"},` +
				`"engine":{"type":"string","description":"Filter by engine type"},` +
				`"modality":{"type":"string","description":"Filter by modality (default: all)","enum":["llm","vlm","embedding","tts","asr","image_gen","video_gen"]},` +
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
