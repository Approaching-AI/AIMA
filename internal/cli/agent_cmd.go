package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jguan/aima/internal/zeroclaw"
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
			ctx := cmd.Context()

			destDir := filepath.Join(app.DataDir, "bin")
			binPath, err := zeroclaw.Install(ctx, destDir)
			if err != nil {
				return fmt.Errorf("install zeroclaw: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "ZeroClaw installed at %s\n", binPath)
			return nil
		},
	}
}

func newAgentStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show agent availability status",
		RunE: func(cmd *cobra.Command, args []string) error {
			status := map[string]any{
				"zeroclaw_available": app.ZeroClaw.Available(),
				"zeroclaw_healthy":   app.ZeroClaw.Health(),
			}

			out, _ := json.MarshalIndent(status, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}
