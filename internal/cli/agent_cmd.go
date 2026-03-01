package cli

import (
	"fmt"

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
