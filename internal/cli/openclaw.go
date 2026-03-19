package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newOpenClawCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openclaw",
		Short: "OpenClaw integration — sync AIMA models as providers",
	}

	cmd.AddCommand(
		newOpenClawSyncCmd(app),
		newOpenClawStatusCmd(app),
	)
	return cmd
}

func newOpenClawSyncCmd(app *App) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync deployed models to OpenClaw config",
		Long:  "Reads currently deployed AIMA backends and writes them as providers into openclaw.json.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.OpenClawSync == nil {
				return fmt.Errorf("openclaw integration not available")
			}
			data, err := app.ToolDeps.OpenClawSync(cmd.Context(), dryRun)
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview config changes without writing")
	return cmd
}

func newOpenClawStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current OpenClaw provider status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.OpenClawSync == nil {
				return fmt.Errorf("openclaw integration not available")
			}
			// Status is just a dry-run sync — shows what would be written
			data, err := app.ToolDeps.OpenClawSync(cmd.Context(), true)
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
}
