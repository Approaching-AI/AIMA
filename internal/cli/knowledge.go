package cli

import (
	"encoding/json"
	"fmt"

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
		newKnowledgeExportCmd(app),
		newKnowledgeImportCmd(app),
		newKnowledgeSyncCmd(app),
		newKnowledgeValidateCmd(app),
		newKnowledgeAdviseCmd(app),
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

func newKnowledgeExportCmd(app *App) *cobra.Command {
	var (
		hardware   string
		model      string
		engine     string
		outputPath string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export knowledge data to JSON",
		Long: `Export configurations, benchmark results, and knowledge notes to a JSON file.
Filter by hardware, model, or engine. Outputs to stdout if --output is not specified.

Examples:
  aima knowledge export --hardware nvidia-gb10-arm64 --output gb10.json
  aima knowledge export --model qwen3-8b > qwen3.json
  aima knowledge export`,
		RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]any{
				"hardware":    hardware,
				"model":       model,
				"engine":      engine,
				"output_path": outputPath,
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return err
			}
			result, err := app.ToolDeps.ExportKnowledge(cmd.Context(), raw)
			if err != nil {
				return fmt.Errorf("export knowledge: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(result))
			return nil
		},
	}

	cmd.Flags().StringVar(&hardware, "hardware", "", "Filter by hardware profile ID")
	cmd.Flags().StringVar(&model, "model", "", "Filter by model name")
	cmd.Flags().StringVar(&engine, "engine", "", "Filter by engine type")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path (default: stdout)")

	return cmd
}

func newKnowledgeImportCmd(app *App) *cobra.Command {
	var (
		inputPath string
		conflict  string
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import knowledge data from JSON",
		Long: `Import configurations, benchmark results, and knowledge notes from a JSON file.

Conflict resolution:
  skip (default): Skip records that already exist
  overwrite: Replace existing records

Examples:
  aima knowledge import --input gb10.json
  aima knowledge import --input gb10.json --conflict overwrite
  aima knowledge import --input gb10.json --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]any{
				"input_path": inputPath,
				"conflict":   conflict,
				"dry_run":    dryRun,
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return err
			}
			result, err := app.ToolDeps.ImportKnowledge(cmd.Context(), raw)
			if err != nil {
				return fmt.Errorf("import knowledge: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(result))
			return nil
		},
	}

	cmd.Flags().StringVarP(&inputPath, "input", "i", "", "Input JSON file path")
	cmd.Flags().StringVar(&conflict, "conflict", "skip", "Conflict resolution: skip | overwrite")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview import without writing")
	_ = cmd.MarkFlagRequired("input")

	return cmd
}

func newKnowledgeSyncCmd(app *App) *cobra.Command {
	var push, pull bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync knowledge with central server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !push && !pull {
				push = true
				pull = true
			}
			if push {
				data, err := app.ToolDeps.SyncPush(cmd.Context())
				if err != nil {
					return fmt.Errorf("push: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Push:", formatJSON(data))
			}
			if pull {
				data, err := app.ToolDeps.SyncPull(cmd.Context())
				if err != nil {
					return fmt.Errorf("pull: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Pull:", formatJSON(data))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&push, "push", false, "Push local knowledge to central")
	cmd.Flags().BoolVar(&pull, "pull", false, "Pull knowledge from central")
	return cmd
}

func newKnowledgeValidateCmd(app *App) *cobra.Command {
	var hardware, engine, model string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Compare predicted vs actual performance",
		RunE: func(cmd *cobra.Command, args []string) error {
			params, _ := json.Marshal(map[string]string{
				"hardware": hardware, "engine": engine, "model": model,
			})
			data, err := app.ToolDeps.ValidateKnowledge(cmd.Context(), params)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&hardware, "hardware", "", "Filter by hardware profile")
	cmd.Flags().StringVar(&engine, "engine", "", "Filter by engine type")
	cmd.Flags().StringVar(&model, "model", "", "Filter by model name")
	return cmd
}

func newKnowledgeAdviseCmd(app *App) *cobra.Command {
	var engine, intent string

	cmd := &cobra.Command{
		Use:   "advise <model>",
		Short: "Request AI-powered recommendation from central server",
		Long: `Request a config/engine recommendation from the central knowledge server.
The response is normalized to the edge-facing v2 advisory shape when possible.
Requires central.endpoint to be configured.

Examples:
  aima knowledge advise qwen3-8b
  aima knowledge advise qwen3-8b --engine vllm --intent low-latency`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.RequestAdvise == nil {
				return fmt.Errorf("central advise not configured — set central.endpoint first")
			}
			data, err := app.ToolDeps.RequestAdvise(cmd.Context(), args[0], engine, intent)
			if err != nil {
				return fmt.Errorf("advise for %s: %w", args[0], err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}

	cmd.Flags().StringVar(&engine, "engine", "", "Engine type (omit for recommendation)")
	cmd.Flags().StringVar(&intent, "intent", "", "Optimization intent: low-latency, high-throughput, balanced")
	return cmd
}
