package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newAskCmd(app *App) *cobra.Command {
	var (
		skipPerms bool
		sessionID string
	)

	cmd := &cobra.Command{
		Use:   "ask <query>",
		Short: "Ask the AI agent a question",
		Long:  "Route a query through the Go Agent (L3a) for multi-turn tool-calling conversations.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			query := strings.Join(args, " ")

			data, sid, err := app.ToolDeps.DispatchAsk(ctx, query, skipPerms, sessionID)
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			if sid != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "\nSession: %s (use --session %s to continue)\n", sid, sid)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&skipPerms, "dangerously-skip-permissions", false, "Skip deploy approval gate (use with caution)")
	cmd.Flags().StringVar(&sessionID, "session", "", "Continue a conversation by session ID")

	return cmd
}
