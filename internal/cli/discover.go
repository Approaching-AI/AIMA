package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newDiscoverCmd(app *App) *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover LLM inference services on the local network",
		Long:  "Scan the local network for AIMA inference services advertised via mDNS (_llm._tcp).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.DiscoverLAN == nil {
				return fmt.Errorf("discover.lan tool not available")
			}
			raw, err := app.ToolDeps.DiscoverLAN(cmd.Context(), int(timeout.Seconds()))
			if err != nil {
				return fmt.Errorf("discover: %w", err)
			}
			// Pretty-print the JSON
			var pretty json.RawMessage
			if err := json.Unmarshal(raw, &pretty); err != nil {
				cmd.Println(string(raw))
				return nil
			}
			data, err := json.MarshalIndent(pretty, "", "  ")
			if err != nil {
				cmd.Println(string(raw))
				return nil
			}
			cmd.Println(string(data))
			return nil
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "mDNS scan timeout")
	return cmd
}
