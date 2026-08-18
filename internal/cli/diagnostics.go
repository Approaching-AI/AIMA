package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newDiagnosticsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diagnostics",
		Short: "Export local troubleshooting diagnostics",
	}

	cmd.AddCommand(newDiagnosticsExportCmd(app))
	return cmd
}

func newDiagnosticsExportCmd(app *App) *cobra.Command {
	var (
		outputPath string
		stdout     bool
		noLogs     bool
		logLines   int
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a telemetry-free local diagnostics bundle",
		Long: `Export a local diagnostics JSON bundle for troubleshooting first-run failures.
The command does not upload anything. Sensitive config values and token-like
fields are redacted before output.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps == nil || app.ToolDeps.DiagnosticsExport == nil {
				return fmt.Errorf("system.diagnostics not available")
			}
			params, err := json.Marshal(map[string]any{
				"output_path":  outputPath,
				"inline":       stdout,
				"include_logs": !noLogs,
				"log_lines":    logLines,
			})
			if err != nil {
				return err
			}
			data, err := app.ToolDeps.DiagnosticsExport(cmd.Context(), params)
			if err != nil {
				return fmt.Errorf("export diagnostics: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output JSON path (default: AIMA diagnostics directory)")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "Print the full diagnostics bundle to stdout instead of writing a file")
	cmd.Flags().BoolVar(&noLogs, "no-logs", false, "Do not include deployment log tails")
	cmd.Flags().IntVar(&logLines, "log-lines", 80, "Deployment log tail lines per deployment")
	return cmd
}
