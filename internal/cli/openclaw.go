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
		newOpenClawClaimCmd(app),
		newOpenClawExcludeCmd(app),
		newOpenClawIncludeCmd(app),
	)
	return cmd
}

func newOpenClawExcludeCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "exclude <model>",
		Short: "Revoke a model from OpenClaw sync (persistent, reversible)",
		Long: "Removes the model from openclaw.json and marks it so future syncs (including the auto-sync loop) skip it. " +
			"The model keeps serving in AIMA; only its OpenClaw exposure is revoked. Reversible with 'aima openclaw include <model>'.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.OpenClawExclude == nil {
				return fmt.Errorf("openclaw integration not available")
			}
			data, err := app.ToolDeps.OpenClawExclude(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
}

func newOpenClawIncludeCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "include <model>",
		Short: "Restore a previously revoked model so sync re-adds it to OpenClaw",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.OpenClawInclude == nil {
				return fmt.Errorf("openclaw integration not available")
			}
			data, err := app.ToolDeps.OpenClawInclude(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
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
		Short: "Show current OpenClaw integration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.OpenClawStatus == nil {
				return fmt.Errorf("openclaw integration not available")
			}
			data, err := app.ToolDeps.OpenClawStatus(cmd.Context())
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
}

func newOpenClawClaimCmd(app *App) *cobra.Command {
	var (
		dryRun   bool
		sections []string
	)
	cmd := &cobra.Command{
		Use:   "claim",
		Short: "Claim legacy OpenClaw config into AIMA-managed ownership",
		Long:  "Detects existing OpenClaw config that already points at the local AIMA proxy and records explicit AIMA ownership for those sections.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.OpenClawClaim == nil {
				return fmt.Errorf("openclaw integration not available")
			}
			data, err := app.ToolDeps.OpenClawClaim(cmd.Context(), sections, dryRun)
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview claimable sections without writing managed state")
	cmd.Flags().StringSliceVar(&sections, "sections", nil, "Claim sections: llm, asr, vision, tts, image_gen (default all)")
	return cmd
}
