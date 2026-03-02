package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newFleetCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Manage fleet of AIMA devices on the LAN",
		Long:  "Query and manage AIMA devices discovered on the LAN.\nRequires a running 'aima serve' instance with --mdns --discover.",
	}

	cmd.AddCommand(
		newFleetDevicesCmd(app),
		newFleetInfoCmd(app),
		newFleetToolsCmd(app),
		newFleetExecCmd(app),
	)
	return cmd
}

func newFleetDevicesCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List all discovered AIMA devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.FleetListDevices == nil {
				return fmt.Errorf("fleet not available (is 'aima serve --mdns --discover' running?)")
			}
			data, err := app.ToolDeps.FleetListDevices(cmd.Context())
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
}

func newFleetInfoCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info <device-id>",
		Short: "Get detailed info about a device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.FleetDeviceInfo == nil {
				return fmt.Errorf("fleet not available (is 'aima serve --mdns --discover' running?)")
			}
			data, err := app.ToolDeps.FleetDeviceInfo(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
}

func newFleetToolsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "tools <device-id>",
		Short: "List available tools on a device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.FleetDeviceTools == nil {
				return fmt.Errorf("fleet not available (is 'aima serve --mdns --discover' running?)")
			}
			data, err := app.ToolDeps.FleetDeviceTools(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
}

func newFleetExecCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "exec <device-id> <tool-name> [params-json]",
		Short: "Execute a tool on a remote device",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.FleetExecTool == nil {
				return fmt.Errorf("fleet not available (is 'aima serve --mdns --discover' running?)")
			}
			var params json.RawMessage = json.RawMessage(`{}`)
			if len(args) >= 3 {
				params = json.RawMessage(args[2])
			}
			data, err := app.ToolDeps.FleetExecTool(cmd.Context(), args[0], args[1], params)
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
}
