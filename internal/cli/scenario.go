package cli

import (
	"fmt"

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
