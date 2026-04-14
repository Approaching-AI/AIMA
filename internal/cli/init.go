package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// isTTY returns true if stdin is a terminal (not piped or redirected).
func isTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func newInitCmd(app *App) *cobra.Command {
	var (
		yesFlag  bool
		k3sFlag  bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install infrastructure stack (Docker tier by default, --k3s for full K3S+HAMi)",
		Long: `Initialize AIMA infrastructure on this device.

Tiers:
  aima init        Docker + nvidia-ctk + aima-serve (lightweight container inference)
  aima init --k3s  + K3S + HAMi (GPU partitioning, multi-model scheduling)

The K3S tier is a superset of the Docker tier. Missing files are auto-downloaded
when confirmed (or use --yes to skip the prompt).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			allowDownload := false

			tier := "docker"
			if k3sFlag {
				tier = "k3s"
			}

			// 1. Preflight: check for missing files
			if app.ToolDeps.StackPreflight != nil {
				preflightData, err := app.ToolDeps.StackPreflight(ctx, tier)
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

					if yesFlag || !isTTY() {
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
			tierLabel := "Docker"
			if k3sFlag {
				tierLabel = "K3S (full stack)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Initializing AIMA infrastructure stack [%s tier]...\n", tierLabel)

			data, err := app.ToolDeps.StackInit(ctx, tier, allowDownload)
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
					Pods    []struct {
						Name    string `json:"name"`
						Phase   string `json:"phase"`
						Ready   bool   `json:"ready"`
						Message string `json:"message"`
					} `json:"pods"`
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
				for _, p := range c.Pods {
					podStatus := "FAIL"
					if p.Ready {
						podStatus = "OK"
					}
					detail := p.Phase
					if p.Message != "" {
						detail += " (" + p.Message + ")"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "    [%s] pod/%s: %s\n", podStatus, p.Name, detail)
				}
			}

			// 4. Auto-import Docker images to K3S containerd (only for K3S tier)
			if k3sFlag && result.AllReady && app.ToolDeps.ScanEngines != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "\nImporting Docker engine images to K3S containerd...")
				if _, err := app.ToolDeps.ScanEngines(ctx, "auto", true); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: engine import failed: %v\n", err)
				}
			}

			if result.AllReady {
				fmt.Fprintln(cmd.OutOrStdout(), "\nAll components ready. Run 'aima serve' to begin.")
			} else {
				allSkipped := true
				for _, c := range result.Components {
					if !c.Ready && !c.Skipped {
						allSkipped = false
						break
					}
				}
				if allSkipped {
					fmt.Fprintln(cmd.OutOrStdout(), "\nNo supported components on this platform.")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "\nSome components failed. Check messages above.")
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip download confirmation prompt")
	cmd.Flags().BoolVar(&k3sFlag, "k3s", false, "Install full K3S+HAMi stack (GPU partitioning, multi-model scheduling)")
	return cmd
}
