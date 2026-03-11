package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newTuningCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tuning",
		Short: "Auto-tuning: parameter search + benchmark + apply optimal",
	}

	// tuning start
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start an auto-tuning session",
		RunE: func(cmd *cobra.Command, args []string) error {
			model, _ := cmd.Flags().GetString("model")
			engine, _ := cmd.Flags().GetString("engine")
			hardware, _ := cmd.Flags().GetString("hardware")
			endpoint, _ := cmd.Flags().GetString("endpoint")
			maxConfigs, _ := cmd.Flags().GetInt("max-configs")

			params := map[string]any{"model": model}
			if engine != "" {
				params["engine"] = engine
			}
			if hardware != "" {
				params["hardware"] = hardware
			}
			if endpoint != "" {
				params["endpoint"] = endpoint
			}
			if maxConfigs > 0 {
				params["max_configs"] = maxConfigs
			}

			paramsBytes, _ := json.Marshal(params)
			data, err := app.ToolDeps.TuningStart(context.Background(), paramsBytes)
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}
	startCmd.Flags().String("model", "", "Model to tune")
	startCmd.Flags().String("engine", "", "Engine type")
	startCmd.Flags().String("hardware", "", "Hardware profile for benchmark persistence")
	startCmd.Flags().String("endpoint", "", "Inference endpoint")
	startCmd.Flags().Int("max-configs", 20, "Maximum configs to evaluate")

	// tuning status
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show current tuning session progress",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.TuningStatus(context.Background())
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}

	// tuning stop
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the current tuning session",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.TuningStop(context.Background())
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}

	// tuning results
	resultsCmd := &cobra.Command{
		Use:   "results",
		Short: "Show tuning session results",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.TuningResults(context.Background())
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}

	cmd.AddCommand(startCmd, statusCmd, stopCmd, resultsCmd)
	return cmd
}
