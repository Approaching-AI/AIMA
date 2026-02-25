package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCmd(app *App) *cobra.Command {
	var yesFlag bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install and configure infrastructure stack (K3S, HAMi)",
		Long:  "Detect hardware, install K3S and HAMi with AIMA-optimized defaults, and verify readiness. Missing files are auto-downloaded with confirmation.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			allowDownload := false

			// 1. Preflight: check for missing files
			if app.ToolDeps.StackPreflight != nil {
				preflightData, err := app.ToolDeps.StackPreflight(ctx)
				if err != nil {
					return fmt.Errorf("preflight: %w", err)
				}

				var downloads []struct {
					Name     string `json:"name"`
					FileName string `json:"file_name"`
					URL      string `json:"url"`
				}
				if err := json.Unmarshal(preflightData, &downloads); err == nil && len(downloads) > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "The following files need to be downloaded:\n")
					for _, d := range downloads {
						fmt.Fprintf(cmd.ErrOrStderr(), "  %s (%s)\n    %s\n", d.Name, d.FileName, d.URL)
					}

					if yesFlag {
						allowDownload = true
					} else {
						fmt.Fprintf(cmd.ErrOrStderr(), "\nDownload these files? [Y/n] ")
						scanner := bufio.NewScanner(cmd.InOrStdin())
						if scanner.Scan() {
							answer := strings.TrimSpace(scanner.Text())
							allowDownload = answer == "" || strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
						}
					}

					if !allowDownload {
						fmt.Fprintf(cmd.ErrOrStderr(), "Skipping download. Init will proceed without missing files.\n")
					}
				}
			}

			// 2. Run init
			fmt.Fprintln(cmd.OutOrStdout(), "Initializing AIMA infrastructure stack...")

			data, err := app.ToolDeps.StackInit(ctx, allowDownload)
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}

			// 3. Display results
			var result struct {
				Components []struct {
					Name    string `json:"name"`
					Ready   bool   `json:"ready"`
					Skipped bool   `json:"skipped"`
					Message string `json:"message"`
				} `json:"components"`
				AllReady bool `json:"all_ready"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}

			for _, c := range result.Components {
				status := "FAIL"
				if c.Ready {
					status = "OK"
				} else if c.Skipped {
					status = "SKIP"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s: %s\n", status, c.Name, c.Message)
			}

			if result.AllReady {
				fmt.Fprintln(cmd.OutOrStdout(), "\nAll components ready. Run 'aima serve' to begin.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "\nSome components failed. Check messages above.")
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip download confirmation prompt")
	return cmd
}
