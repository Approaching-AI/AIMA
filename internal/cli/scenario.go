package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newScenarioCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scenario",
		Short: "Manage deployment scenarios",
	}

	cmd.AddCommand(
		newScenarioListCmd(app),
		newScenarioShowCmd(app),
		newScenarioApplyCmd(app),
		newScenarioGenerateCmd(app),
		newScenarioListCentralCmd(app),
	)
	return cmd
}

func newScenarioListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available deployment scenarios",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.ScenarioList == nil {
				return fmt.Errorf("scenario.list not available")
			}
			data, err := app.ToolDeps.ScenarioList(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newScenarioShowCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show <scenario-name>",
		Short: "Show full details of a deployment scenario",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.ScenarioShow == nil {
				return fmt.Errorf("scenario.show not available")
			}
			data, err := app.ToolDeps.ScenarioShow(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newScenarioApplyCmd(app *App) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply <scenario-name>",
		Short: "Deploy all models defined in a scenario",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.ScenarioApply == nil {
				return fmt.Errorf("scenario.apply not available")
			}
			data, err := app.ToolDeps.ScenarioApply(cmd.Context(), args[0], dryRun)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview deployments without executing")
	return cmd
}

func newScenarioGenerateCmd(app *App) *cobra.Command {
	var hardware, goal string

	cmd := &cobra.Command{
		Use:   "generate --hardware <profile> --models <model1,model2,...>",
		Short: "Generate a multi-model deployment scenario via central AI advisor",
		Long: `Request the central server to generate an AI-powered deployment scenario
for the given hardware and model set. Requires central.endpoint to be configured.

Examples:
  aima scenario generate --hardware nvidia-gb10-arm64 --models qwen3-8b,glm-4.7-flash
  aima scenario generate --hardware nvidia-rtx4090-x86 --models qwen3-30b-a3b --goal low-latency`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.RequestScenario == nil {
				return fmt.Errorf("scenario.generate not configured — set central.endpoint first")
			}
			modelsStr, _ := cmd.Flags().GetString("models")
			if hardware == "" || modelsStr == "" {
				return fmt.Errorf("--hardware and --models are required")
			}
			models := strings.Split(modelsStr, ",")
			data, err := app.ToolDeps.RequestScenario(cmd.Context(), hardware, models, goal)
			if err != nil {
				return fmt.Errorf("scenario generate: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(json.RawMessage(data)))
			return nil
		},
	}

	cmd.Flags().StringVar(&hardware, "hardware", "", "Hardware profile name")
	cmd.Flags().String("models", "", "Comma-separated model names")
	cmd.Flags().StringVar(&goal, "goal", "", "Optimization goal: balanced, low-latency, maximize-models")
	_ = cmd.MarkFlagRequired("hardware")
	_ = cmd.MarkFlagRequired("models")
	return cmd
}

func newScenarioListCentralCmd(app *App) *cobra.Command {
	var hardware, source string

	cmd := &cobra.Command{
		Use:   "list-central",
		Short: "List deployment scenarios from the central server",
		Long: `List AI-generated or manually uploaded deployment scenarios from the central
knowledge server. Requires central.endpoint to be configured.

Examples:
  aima scenario list-central
  aima scenario list-central --hardware nvidia-gb10-arm64
  aima scenario list-central --source advisor`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.ListCentralScenarios == nil {
				return fmt.Errorf("scenario.list_central not configured — set central.endpoint first")
			}
			data, err := app.ToolDeps.ListCentralScenarios(cmd.Context(), hardware, source)
			if err != nil {
				return fmt.Errorf("list central scenarios: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(json.RawMessage(data)))
			return nil
		},
	}

	cmd.Flags().StringVar(&hardware, "hardware", "", "Filter by hardware profile")
	cmd.Flags().StringVar(&source, "source", "", "Filter by source: advisor, user, analyzer")
	return cmd
}
