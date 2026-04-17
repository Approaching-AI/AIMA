package cli

import (
	"context"
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

// explorerCall dispatches to the remote MCP `explorer` tool when --remote is
// set, otherwise falls back to the local in-process closure. Mirrors the
// onboarding CLI pattern so CLI / MCP / Web UI stay on a single code path.
func explorerCall(
	ctx context.Context,
	app *App,
	action string,
	args map[string]any,
	local func(ctx context.Context) (json.RawMessage, error),
) (json.RawMessage, error) {
	if app.RemoteClient.Configured() {
		merged := map[string]any{"action": action}
		for k, v := range args {
			merged[k] = v
		}
		return app.RemoteClient.CallTool(ctx, "explorer", merged)
	}
	return local(ctx)
}

func newExplorerStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show explorer status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.RemoteClient.Configured() && app.ToolDeps.ExplorerStatus == nil {
				return fmt.Errorf("explorer not available")
			}
			data, err := explorerCall(cmd.Context(), app, "status", nil,
				func(ctx context.Context) (json.RawMessage, error) {
					return app.ToolDeps.ExplorerStatus(ctx)
				})
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
		Short: "Trigger a manual exploration cycle",
		Long: `Trigger a manual exploration cycle.

With --remote set, this dispatches via MCP against a running 'aima serve --mcp'
and the explorer executes asynchronously in that remote process.

Without --remote, this invocation publishes to the in-process EventBus and
will only execute if the same process is also running 'aima serve --mcp' in
the background (otherwise the CLI exits before the goroutine can process the
event). Use --remote for any real trigger.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.RemoteClient.Configured() && app.ToolDeps.ExplorerTrigger == nil {
				return fmt.Errorf("explorer not available")
			}
			data, err := explorerCall(cmd.Context(), app, "trigger", nil,
				func(ctx context.Context) (json.RawMessage, error) {
					return app.ToolDeps.ExplorerTrigger(ctx)
				})
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
			if !app.RemoteClient.Configured() && app.ToolDeps.ExplorerConfig == nil {
				return fmt.Errorf("explorer not available")
			}
			action, _ := cmd.Flags().GetString("action")
			key, _ := cmd.Flags().GetString("key")
			value, _ := cmd.Flags().GetString("value")

			data, err := explorerCall(cmd.Context(), app, "config",
				map[string]any{"config_action": action, "key": key, "value": value},
				func(ctx context.Context) (json.RawMessage, error) {
					params, _ := json.Marshal(map[string]string{
						"action": action,
						"key":    key,
						"value":  value,
					})
					return app.ToolDeps.ExplorerConfig(ctx, params)
				})
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
