package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newKnowledgeCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "knowledge",
		Short: "Manage the knowledge base",
	}

	cmd.AddCommand(
		newKnowledgeListCmd(app),
		newKnowledgeResolveCmd(app),
		newKnowledgePromoteCmd(app),
		newKnowledgeSaveCmd(app),
		newKnowledgeUpdateConfigCmd(app),
	)

	return cmd
}

func newKnowledgeListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all knowledge assets from the catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.ListKnowledgeSummary(cmd.Context())
			if err != nil {
				return fmt.Errorf("knowledge list: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newKnowledgeResolveCmd(app *App) *cobra.Command {
	var engineType string

	cmd := &cobra.Command{
		Use:   "resolve <model>",
		Short: "Resolve optimal configuration for a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.ResolveConfig == nil {
				return fmt.Errorf("knowledge.resolve not available")
			}
			ctx := cmd.Context()
			modelName := args[0]

			resolved, err := app.ToolDeps.ResolveConfig(ctx, modelName, engineType, nil)
			if err != nil {
				return fmt.Errorf("resolve config for %s: %w", modelName, err)
			}

			out, _ := json.MarshalIndent(json.RawMessage(resolved), "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&engineType, "engine", "", "Engine type to resolve for")

	return cmd
}

func newKnowledgePromoteCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote <config-id> <status>",
		Short: "Change a Configuration's status (golden, experiment, archived)",
		Long: `Promote a Configuration to golden (auto-injected as L2 defaults),
demote to experiment, or archive it.

Example:
  aima knowledge promote 5468a92291302deb golden`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.PromoteConfig == nil {
				return fmt.Errorf("knowledge.promote not available")
			}
			data, err := app.ToolDeps.PromoteConfig(cmd.Context(), args[0], args[1])
			if err != nil {
				return fmt.Errorf("promote config: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
	return cmd
}

func newKnowledgeSaveCmd(app *App) *cobra.Command {
	var (
		title    string
		hardware string
		model    string
		engine   string
		tags     string
		confLvl  string
	)

	cmd := &cobra.Command{
		Use:   "save",
		Short: "Save a knowledge note from stdin or --content flag",
		Long: `Save experiment findings or recommendations as a knowledge note.
Content is read from stdin (pipe or redirect).

Example:
  echo "SGLang NEXTN optimal: topk=2, draft=8" | \
    aima knowledge save --title "SGLang optimal config" \
    --hardware nvidia-gb10-arm64 --engine sglang-spark --model qwen3.5-35b-a3b \
    --tags "sglang,optimal" --confidence high`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.SaveKnowledge == nil {
				return fmt.Errorf("knowledge.save not available")
			}

			// Read content from stdin
			var content string
			stat, _ := os.Stdin.Stat()
			if (stat.Mode() & os.ModeCharDevice) == 0 {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				content = strings.TrimSpace(string(data))
			}
			if content == "" {
				return fmt.Errorf("content is required (pipe to stdin)")
			}

			var tagList []string
			if tags != "" {
				for _, t := range splitAndTrim(tags) {
					if t != "" {
						tagList = append(tagList, t)
					}
				}
			}

			note := map[string]any{
				"title":            title,
				"hardware_profile": hardware,
				"model":            model,
				"engine":           engine,
				"tags":             tagList,
				"confidence":       confLvl,
				"content":          content,
			}
			raw, _ := json.Marshal(note)
			if err := app.ToolDeps.SaveKnowledge(cmd.Context(), raw); err != nil {
				return fmt.Errorf("save knowledge: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "knowledge note saved")
			return nil
		},
	}

	cmd.Flags().StringVar(&title, "title", "", "Note title")
	cmd.Flags().StringVar(&hardware, "hardware", "", "Hardware profile ID")
	cmd.Flags().StringVar(&model, "model", "", "Model name")
	cmd.Flags().StringVar(&engine, "engine", "", "Engine type")
	cmd.Flags().StringVar(&tags, "tags", "", "Comma-separated tags")
	cmd.Flags().StringVar(&confLvl, "confidence", "medium", "Confidence level (high, medium, low)")
	_ = cmd.MarkFlagRequired("title")

	return cmd
}

func newKnowledgeUpdateConfigCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update-config <config-id> <json-params>",
		Short: "Update a Configuration's engine parameters",
		Long: `Set the engine parameters (config JSON) on an existing Configuration.
These parameters are auto-injected as L2 defaults when the Configuration is golden.

Example:
  aima knowledge update-config 5468a92291302deb \
    '{"mem_fraction_static":0.9,"speculative_algo":"NEXTN","speculative_num_steps":4}'`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.UpdateConfigParams == nil {
				return fmt.Errorf("knowledge.update_config not available")
			}
			configID := args[0]
			configJSON := json.RawMessage(args[1])

			// Validate it's valid JSON
			if !json.Valid(configJSON) {
				return fmt.Errorf("second argument must be valid JSON")
			}

			data, err := app.ToolDeps.UpdateConfigParams(cmd.Context(), configID, configJSON)
			if err != nil {
				return fmt.Errorf("update config: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
	return cmd
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}
