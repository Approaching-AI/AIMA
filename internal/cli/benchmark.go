package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newBenchmarkCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Record and query benchmark results",
	}

	cmd.AddCommand(newBenchmarkRecordCmd(app))

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
