package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newAskCmd(app *App) *cobra.Command {
	var (
		forceLocal bool
		forceDeep  bool
		sessionID  string
	)

	cmd := &cobra.Command{
		Use:   "ask <query>",
		Short: "Ask the AI agent a question",
		Long:  "Route a query through the dispatcher: auto-selects L3a (Go Agent) or L3b (ZeroClaw) based on complexity.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			query := strings.Join(args, " ")

			data, err := app.ToolDeps.DispatchAsk(ctx, query, forceLocal, forceDeep, sessionID)
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}

	cmd.Flags().BoolVar(&forceLocal, "local", false, "Force use of Go Agent (L3a)")
	cmd.Flags().BoolVar(&forceDeep, "deep", false, "Force use of ZeroClaw (L3b)")
	cmd.Flags().StringVar(&sessionID, "session", "", "Continue a ZeroClaw session by ID")

	return cmd
}
