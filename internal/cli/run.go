package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newRunCmd(app *App) *cobra.Command {
	var (
		engineType string
		slot       string
		noPull     bool
	)

	cmd := &cobra.Command{
		Use:   "run <model>",
		Short: "Download, deploy and serve a model (like ollama run)",
		Long:  "One command to detect hardware, resolve config, pull engine and model if needed, deploy, and wait for the service to be ready.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			modelName := args[0]
			w := cmd.OutOrStdout()
			isTTY := isTerminal(w)

			pr := &pullProgressRenderer{
				w:          w,
				isTTY:      isTTY,
				lastReport: -1,
			}

			lastPhase := ""
			onPhase := func(phase, msg string) {
				switch phase {
				case "resolving":
					fmt.Fprintf(w, "Resolving %s...\n", msg)
				case "resolved":
					fmt.Fprintf(w, "  %s\n", msg)
				case "warning":
					fmt.Fprintf(w, "  Warning: %s\n", msg)
				case "pulling_engine":
					fmt.Fprintf(w, "Checking engine %s...\n", msg)
				case "model_skip":
					fmt.Fprintf(w, "  Model pull skipped: %s\n", msg)
				case "pulling_model":
					fmt.Fprintf(w, "Checking model %s...\n", msg)
				case "deploying":
					pr.finish()
					fmt.Fprintf(w, "Deploying...\n")
				case "waiting":
					fmt.Fprintf(w, "Waiting for %s to be ready", msg)
					if !isTTY {
						fmt.Fprintln(w)
					}
				case "startup":
					if msg != lastPhase {
						if isTTY {
							fmt.Fprintf(w, "\r\033[K  %s...", msg)
						} else {
							fmt.Fprintf(w, "  %s\n", msg)
						}
						lastPhase = msg
					}
				case "ready":
					if isTTY {
						fmt.Fprintf(w, "\r\033[K")
					}
					fmt.Fprintf(w, "Ready!\n")
				}
			}

			data, err := app.ToolDeps.DeployRun(ctx, modelName, engineType, slot, noPull, onPhase, pr.onProgress)
			pr.finish()
			if err != nil {
				return err
			}

			// Print final result summary
			var result struct {
				Name    string `json:"name"`
				Address string `json:"address"`
				Runtime string `json:"runtime"`
				Status  string `json:"status"`
			}
			if err := json.Unmarshal(data, &result); err == nil && result.Address != "" {
				fmt.Fprintf(w, "\n")
				fmt.Fprintf(w, "  Name:     %s\n", result.Name)
				fmt.Fprintf(w, "  Endpoint: http://%s\n", result.Address)
				fmt.Fprintf(w, "  Runtime:  %s\n", result.Runtime)
			} else if result.Status == "timeout" {
				fmt.Fprintf(w, "\nTimed out waiting for deployment to be ready.\n")
				fmt.Fprintf(w, "Check status with: aima deploy status %s\n", result.Name)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&engineType, "engine", "", "Engine type override")
	cmd.Flags().StringVar(&slot, "slot", "", "Partition slot name")
	cmd.Flags().BoolVar(&noPull, "no-pull", false, "Skip auto-downloading missing engine/model")
	return cmd
}
