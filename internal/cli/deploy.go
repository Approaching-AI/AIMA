package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newDeployCmd(app *App) *cobra.Command {
	var (
		engineType      string
		slot            string
		dryRun          bool
		configOverrides []string
	)

	cmd := &cobra.Command{
		Use:   "deploy <model>",
		Short: "Deploy an inference service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			modelName := args[0]

			configMap := parseConfigOverrides(configOverrides)

			if dryRun {
				data, err := app.ToolDeps.DeployDryRun(ctx, engineType, modelName, slot, configMap)
				if err != nil {
					return fmt.Errorf("deploy dry-run %s: %w", modelName, err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
				return nil
			}

			data, err := app.ToolDeps.DeployApply(ctx, engineType, modelName, slot, configMap)
			if err != nil {
				return fmt.Errorf("deploy %s: %w", modelName, err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}

	cmd.Flags().StringVar(&engineType, "engine", "", "Engine type (e.g., vllm, llamacpp)")
	cmd.Flags().StringVar(&slot, "slot", "", "Partition slot name")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview deployment without executing")
	cmd.Flags().StringSliceVar(&configOverrides, "config", nil, "Config overrides (key=value, can repeat)")
	cmd.AddCommand(newDeployListCmd(app))
	cmd.AddCommand(newDeployStatusCmd(app))
	cmd.AddCommand(newDeployLogsCmd(app))

	return cmd
}

func newDeployListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all active deployments",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.DeployList == nil {
				return fmt.Errorf("deploy list not available")
			}
			data, err := app.ToolDeps.DeployList(cmd.Context())
			if err != nil {
				return fmt.Errorf("deploy list: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newDeployStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Show status of a deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.DeployStatus == nil {
				return fmt.Errorf("deploy status not available")
			}
			data, err := app.ToolDeps.DeployStatus(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("deploy status %s: %w", args[0], err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newDeployLogsCmd(app *App) *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show logs of a deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.DeployLogs == nil {
				return fmt.Errorf("deploy logs not available")
			}
			data, err := app.ToolDeps.DeployLogs(cmd.Context(), args[0], lines)
			if err != nil {
				return fmt.Errorf("deploy logs %s: %w", args[0], err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	cmd.Flags().IntVar(&lines, "lines", 100, "Number of log lines to show")
	return cmd
}

func newUndeployCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "undeploy <name>",
		Short: "Remove a deployed inference service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			if err := app.ToolDeps.DeployDelete(ctx, name); err != nil {
				return fmt.Errorf("undeploy %s: %w", name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Deployment %s removed\n", name)
			return nil
		},
	}
}

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show system status",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.SystemStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("system status: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

// formatJSON pretty-prints a json.RawMessage.
func formatJSON(data json.RawMessage) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(data)
	}
	return string(out)
}

// parseConfigOverrides converts ["key=value", ...] to map[string]any with type inference.
func parseConfigOverrides(pairs []string) map[string]any {
	if len(pairs) == 0 {
		return nil
	}
	m := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		m[k] = parseValue(v)
	}
	return m
}

// parseValue tries to convert a string to the most specific type.
// Order matters: int before bool, so "0" → 0 (int) not false (bool).
// Only "true"/"false" (case-insensitive) are treated as booleans,
// not strconv.ParseBool which also accepts "1", "t", "T", etc.
func parseValue(s string) any {
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	if lower := strings.ToLower(s); lower == "true" || lower == "false" {
		return lower == "true"
	}
	return s
}
