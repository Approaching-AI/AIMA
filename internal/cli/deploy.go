package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newDeployCmd(app *App) *cobra.Command {
	var (
		engineType string
		slot       string
	)

	cmd := &cobra.Command{
		Use:   "deploy <model>",
		Short: "Deploy an inference service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			modelName := args[0]

			data, err := app.ToolDeps.DeployApply(ctx, engineType, modelName, slot)
			if err != nil {
				return fmt.Errorf("deploy %s: %w", modelName, err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}

	cmd.Flags().StringVar(&engineType, "engine", "", "Engine type (e.g., vllm, llamacpp)")
	cmd.Flags().StringVar(&slot, "slot", "", "Partition slot name")

	return cmd
}

func newUndeployCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "undeploy <name>",
		Short: "Remove a deployed inference service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			if err := app.ToolDeps.DeployDelete(ctx, name); err != nil {
				return fmt.Errorf("undeploy %s: %w", name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Deployment %s removed\n", name)
			return nil
		},
	}
}

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show system status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			hw, err := app.ToolDeps.DetectHardware(ctx)
			if err != nil {
				return fmt.Errorf("detect hardware: %w", err)
			}

			// Non-fatal: K3S may not be running
			pods, _ := app.ToolDeps.DeployList(ctx)
			metrics, _ := app.ToolDeps.CollectMetrics(ctx)

			status := map[string]json.RawMessage{
				"hardware": hw,
			}
			if pods != nil {
				status["deployments"] = pods
			}
			if metrics != nil {
				status["metrics"] = metrics
			}

			out, _ := json.MarshalIndent(status, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}

// formatJSON pretty-prints a json.RawMessage.
func formatJSON(data json.RawMessage) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(data)
	}
	return string(out)
}
