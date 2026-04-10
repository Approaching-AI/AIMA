package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newExplorerCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explorer",
		Short: "Knowledge exploration automation",
	}
	cmd.AddCommand(
		newExplorerStatusCmd(app),
		newExplorerTriggerCmd(app),
		newExplorerConfigCmd(app),
	)
	return cmd
}

func newExplorerStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show explorer status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.ExplorerStatus == nil {
				return fmt.Errorf("explorer not available")
			}
			data, err := app.ToolDeps.ExplorerStatus(cmd.Context())
			if err != nil {
				return err
			}
			var pretty json.RawMessage = data
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}
}

func newExplorerTriggerCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "trigger",
		Short: "Trigger a manual exploration cycle (requires 'aima serve' mode)",
		Long: `Trigger a manual exploration cycle.

NOTE: This command publishes an event to the in-process EventBus. The Explorer
processes events asynchronously in a background goroutine that only runs while
the process is alive. In CLI mode the process exits immediately after the event
is published, so the exploration will NOT actually execute.

Use 'aima serve --mcp' and call explorer.trigger via MCP for actual execution.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.ExplorerTrigger == nil {
				return fmt.Errorf("explorer not available")
			}
			data, err := app.ToolDeps.ExplorerTrigger(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		},
	}
}

func newExplorerConfigCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config [get|set]",
		Short: "Get or set explorer schedule configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.ExplorerConfig == nil {
				return fmt.Errorf("explorer not available")
			}
			action, _ := cmd.Flags().GetString("action")
			key, _ := cmd.Flags().GetString("key")
			value, _ := cmd.Flags().GetString("value")
			params, _ := json.Marshal(map[string]string{
				"action": action,
				"key":    key,
				"value":  value,
			})
			data, err := app.ToolDeps.ExplorerConfig(cmd.Context(), params)
			if err != nil {
				return err
			}
			var pretty json.RawMessage = data
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}
	cmd.Flags().String("action", "get", "get or set")
	cmd.Flags().String("key", "", "Config key")
	cmd.Flags().String("value", "", "Config value (for set)")
	return cmd
}
