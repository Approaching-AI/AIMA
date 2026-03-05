package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newBenchmarkCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Record and query benchmark results",
	}

	cmd.AddCommand(
		newBenchmarkRecordCmd(app),
		newBenchmarkRunCmd(app),
		newBenchmarkMatrixCmd(app),
		newBenchmarkListCmd(app),
	)

	return cmd
}

func newBenchmarkRecordCmd(app *App) *cobra.Command {
	var (
		hardware        string
		engine          string
		model           string
		deviceID        string
		concurrency     int
		inputLenBucket  string
		outputLenBucket string
		ttftP50         float64
		ttftP95         float64
		tpotP50         float64
		tpotP95         float64
		throughput      float64
		qps             float64
		vramUsage       int
		sampleCount     int
		stability       string
		notes           string
	)

	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record a benchmark result into the knowledge database",
		Long: `Record a benchmark measurement. Auto-creates a Configuration (Hardware×Engine×Model)
if one doesn't exist. The result is stored in SQLite for knowledge queries.

Example:
  aima benchmark record \
    --hardware nvidia-gb10-arm64 --engine vllm-nightly --model qwen3.5-35b-a3b \
    --throughput 29.6 --ttft-p50 498 --tpot-p50 33.5 --vram 67100 \
    --input-bucket 1K --concurrency 1 --samples 3 \
    --notes "128K context test, vLLM v0.16.0rc2"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			params := map[string]any{
				"hardware":          hardware,
				"engine":            engine,
				"model":             model,
				"concurrency":       concurrency,
				"throughput_tps":    throughput,
				"ttft_ms_p50":       ttftP50,
				"ttft_ms_p95":       ttftP95,
				"tpot_ms_p50":       tpotP50,
				"tpot_ms_p95":       tpotP95,
				"qps":               qps,
				"vram_usage_mib":    vramUsage,
				"sample_count":      sampleCount,
				"stability":         stability,
				"notes":             notes,
				"input_len_bucket":  inputLenBucket,
				"output_len_bucket": outputLenBucket,
			}
			if deviceID != "" {
				params["device_id"] = deviceID
			}

			raw, err := json.Marshal(params)
			if err != nil {
				return err
			}

			result, err := app.ToolDeps.RecordBenchmark(ctx, raw)
			if err != nil {
				return fmt.Errorf("record benchmark: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(result))
			return nil
		},
	}

	cmd.Flags().StringVar(&hardware, "hardware", "", "Hardware profile ID (e.g. nvidia-gb10-arm64)")
	cmd.Flags().StringVar(&engine, "engine", "", "Engine type (e.g. vllm-nightly)")
	cmd.Flags().StringVar(&model, "model", "", "Model name (e.g. qwen3.5-35b-a3b)")
	cmd.Flags().StringVar(&deviceID, "device", "", "Device ID (e.g. gb10)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 1, "Concurrency level during test")
	cmd.Flags().StringVar(&inputLenBucket, "input-bucket", "", "Input length bucket (e.g. 1K, 8K, 128K)")
	cmd.Flags().StringVar(&outputLenBucket, "output-bucket", "", "Output length bucket (e.g. 128)")
	cmd.Flags().Float64Var(&ttftP50, "ttft-p50", 0, "Time to first token P50 (ms)")
	cmd.Flags().Float64Var(&ttftP95, "ttft-p95", 0, "Time to first token P95 (ms)")
	cmd.Flags().Float64Var(&tpotP50, "tpot-p50", 0, "Time per output token P50 (ms)")
	cmd.Flags().Float64Var(&tpotP95, "tpot-p95", 0, "Time per output token P95 (ms)")
	cmd.Flags().Float64Var(&throughput, "throughput", 0, "Tokens per second")
	cmd.Flags().Float64Var(&qps, "qps", 0, "Queries per second")
	cmd.Flags().IntVar(&vramUsage, "vram", 0, "VRAM usage (MiB)")
	cmd.Flags().IntVar(&sampleCount, "samples", 0, "Number of test samples")
	cmd.Flags().StringVar(&stability, "stability", "stable", "Stability (stable, fluctuating, unstable)")
	cmd.Flags().StringVar(&notes, "notes", "", "Free-form notes")

	_ = cmd.MarkFlagRequired("hardware")
	_ = cmd.MarkFlagRequired("engine")
	_ = cmd.MarkFlagRequired("model")
	_ = cmd.MarkFlagRequired("throughput")

	return cmd
}

func newBenchmarkRunCmd(app *App) *cobra.Command {
	var (
		modelName      string
		endpoint       string
		concurrency    int
		requests       int
		maxTokens      int
		inputTokens    int
		warmup         int
		rounds         int
		minOutputRatio float64
		maxRetries     int
		noSave         bool
		hardware       string
		engine         string
		notes          string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a live benchmark against a deployed model",
		Long: `Send streaming inference requests and measure TTFT, TPOT, and throughput.
Results are automatically saved to the knowledge database unless --no-save is used.

Examples:
  aima benchmark run --model qwen3-8b
  aima benchmark run --model qwen3-8b --concurrency 4 --requests 20
  aima benchmark run --model gpt-4 --endpoint https://api.openai.com/v1/chat/completions --no-save
  aima benchmark run --model qwen3-8b --rounds 3 --min-output-ratio 0.5 --max-retries 2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			save := !noSave
			params := map[string]any{
				"model":            modelName,
				"endpoint":         endpoint,
				"concurrency":      concurrency,
				"num_requests":     requests,
				"max_tokens":       maxTokens,
				"input_tokens":     inputTokens,
				"warmup":           warmup,
				"rounds":           rounds,
				"min_output_ratio": minOutputRatio,
				"max_retries":      maxRetries,
				"save":             save,
				"hardware":         hardware,
				"engine":           engine,
				"notes":            notes,
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return err
			}
			result, err := app.ToolDeps.RunBenchmark(cmd.Context(), raw)
			if err != nil {
				return fmt.Errorf("benchmark run: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(result))
			return nil
		},
	}

	cmd.Flags().StringVar(&modelName, "model", "", "Model name (required)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "OpenAI-compatible endpoint URL (auto-detect if empty)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 1, "Number of concurrent requests")
	cmd.Flags().IntVar(&requests, "requests", 10, "Total requests to send")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 256, "Max output tokens per request")
	cmd.Flags().IntVar(&inputTokens, "input-tokens", 128, "Approximate input length in tokens")
	cmd.Flags().IntVar(&warmup, "warmup", 2, "Warmup requests to discard")
	cmd.Flags().IntVar(&rounds, "rounds", 1, "Number of measurement rounds")
	cmd.Flags().Float64Var(&minOutputRatio, "min-output-ratio", 0, "Minimum output tokens ratio (0-1, retry below)")
	cmd.Flags().IntVar(&maxRetries, "max-retries", 0, "Per-request retry count on failure")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "Skip saving results to DB")
	cmd.Flags().StringVar(&hardware, "hardware", "", "Hardware profile ID for saving")
	cmd.Flags().StringVar(&engine, "engine", "", "Engine type for saving")
	cmd.Flags().StringVar(&notes, "notes", "", "Free-form notes")
	_ = cmd.MarkFlagRequired("model")

	return cmd
}

func newBenchmarkMatrixCmd(app *App) *cobra.Command {
	var (
		modelName      string
		endpoint       string
		concurrencyStr string
		inputTokensStr string
		maxTokensStr   string
		requests       int
		rounds         int
		minOutputRatio float64
		maxRetries     int
		noSave         bool
		hardware       string
		engine         string
	)

	cmd := &cobra.Command{
		Use:   "matrix",
		Short: "Run a benchmark test matrix",
		Long: `Run benchmarks across multiple concurrency levels and input/output length combinations.

Examples:
  aima benchmark matrix --model qwen3-8b
  aima benchmark matrix --model qwen3-8b --concurrency 1,4,8 --hardware nvidia-gb10-arm64 --engine vllm
  aima benchmark matrix --model qwen3-8b --rounds 3 --max-retries 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			save := !noSave
			params := map[string]any{
				"model":              modelName,
				"endpoint":           endpoint,
				"concurrency_levels": parseIntList(concurrencyStr),
				"input_token_levels": parseIntList(inputTokensStr),
				"max_token_levels":   parseIntList(maxTokensStr),
				"requests_per_combo": requests,
				"rounds":             rounds,
				"min_output_ratio":   minOutputRatio,
				"max_retries":        maxRetries,
				"save":               save,
				"hardware":           hardware,
				"engine":             engine,
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return err
			}
			result, err := app.ToolDeps.RunBenchmarkMatrix(cmd.Context(), raw)
			if err != nil {
				return fmt.Errorf("benchmark matrix: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(result))
			return nil
		},
	}

	cmd.Flags().StringVar(&modelName, "model", "", "Model name (required)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "OpenAI-compatible endpoint URL (auto-detect if empty)")
	cmd.Flags().StringVar(&concurrencyStr, "concurrency", "1,4", "Comma-separated concurrency levels")
	cmd.Flags().StringVar(&inputTokensStr, "input-tokens", "128,1024", "Comma-separated input token lengths")
	cmd.Flags().StringVar(&maxTokensStr, "max-tokens", "128,512", "Comma-separated output token lengths")
	cmd.Flags().IntVar(&requests, "requests", 5, "Requests per combination")
	cmd.Flags().IntVar(&rounds, "rounds", 1, "Measurement rounds per combination")
	cmd.Flags().Float64Var(&minOutputRatio, "min-output-ratio", 0, "Minimum output tokens ratio (0-1)")
	cmd.Flags().IntVar(&maxRetries, "max-retries", 0, "Per-request retry count")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "Skip saving results to DB")
	cmd.Flags().StringVar(&hardware, "hardware", "", "Hardware profile ID")
	cmd.Flags().StringVar(&engine, "engine", "", "Engine type")
	_ = cmd.MarkFlagRequired("model")

	return cmd
}

func newBenchmarkListCmd(app *App) *cobra.Command {
	var (
		configID string
		hardware string
		modelName string
		engine   string
		limit    int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List benchmark results from the knowledge database",
		Long: `List historical benchmark results. Filter by model, hardware, engine, or config ID.

Examples:
  aima benchmark list --model qwen3-8b
  aima benchmark list --hardware nvidia-gb10-arm64
  aima benchmark list --limit 50`,
		RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]any{
				"config_id": configID,
				"hardware":  hardware,
				"model":     modelName,
				"engine":    engine,
				"limit":     limit,
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return err
			}
			result, err := app.ToolDeps.ListBenchmarks(cmd.Context(), raw)
			if err != nil {
				return fmt.Errorf("list benchmarks: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(result))
			return nil
		},
	}

	cmd.Flags().StringVar(&configID, "config", "", "Filter by configuration ID")
	cmd.Flags().StringVar(&hardware, "hardware", "", "Filter by hardware profile ID")
	cmd.Flags().StringVar(&modelName, "model", "", "Filter by model name")
	cmd.Flags().StringVar(&engine, "engine", "", "Filter by engine type")
	cmd.Flags().IntVar(&limit, "limit", 20, "Max results to return")

	return cmd
}

func parseIntList(s string) []int {
	parts := strings.Split(s, ",")
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if v, err := strconv.Atoi(p); err == nil {
			result = append(result, v)
		}
	}
	return result
}
