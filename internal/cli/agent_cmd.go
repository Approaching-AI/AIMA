package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func newAgentCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage the AI agent subsystem",
	}

	cmd.AddCommand(
		newAgentInstallCmd(app),
		newAgentStatusCmd(app),
		newAgentRollbackListCmd(app),
		newAgentRollbackCmd(app),
	)

	return cmd
}

func newAgentInstallCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the ZeroClaw sidecar",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.AgentInstall(cmd.Context())
			if err != nil {
				return fmt.Errorf("install agent: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newAgentStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show agent availability status",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.AgentStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("agent status: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newAgentRollbackListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "rollback-list",
		Short: "List available rollback snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.RollbackList(cmd.Context())
			if err != nil {
				return fmt.Errorf("list rollback snapshots: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newAgentRollbackCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <snapshot-id>",
		Short: "Restore a resource from a rollback snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid snapshot ID %q: %w", args[0], err)
			}
			data, err := app.ToolDeps.RollbackRestore(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("rollback snapshot %d: %w", id, err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}
