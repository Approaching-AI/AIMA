package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/jguan/aima/internal/proxy"
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
			data, err := fleetHTTP(cmd, "GET", "/api/v1/devices", nil)
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
			data, err := fleetHTTP(cmd, "GET", "/api/v1/devices/"+args[0], nil)
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
			data, err := fleetHTTP(cmd, "GET", "/api/v1/devices/"+args[0]+"/tools", nil)
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
			var body []byte = []byte(`{}`)
			if len(args) >= 3 {
				body = []byte(args[2])
			}
			path := "/api/v1/devices/" + args[0] + "/tools/" + args[1]
			data, err := fleetHTTP(cmd, "POST", path, body)
			if err != nil {
				return err
			}
			cmd.Println(formatJSON(data))
			return nil
		},
	}
}

// fleetHTTP calls the local aima serve REST API.
// Fleet CLI commands require a running 'aima serve --mdns --discover' instance.
func fleetHTTP(cmd *cobra.Command, method, path string, body []byte) (json.RawMessage, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", proxy.DefaultPort, path)

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(cmd.Context(), method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := os.Getenv("AIMA_API_KEY"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to local aima serve (is 'aima serve --mdns --discover' running?): %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	return json.RawMessage(data), nil
}
