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
	)

	return cmd
}

func newKnowledgeListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all knowledge assets from the catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.ListProfiles == nil || app.ToolDeps.ListEngineAssets == nil || app.ToolDeps.ListModelAssets == nil {
				return fmt.Errorf("knowledge list tools not available")
			}
			ctx := cmd.Context()

			profilesRaw, err := app.ToolDeps.ListProfiles(ctx)
			if err != nil {
				return fmt.Errorf("list profiles: %w", err)
			}
			enginesRaw, err := app.ToolDeps.ListEngineAssets(ctx)
			if err != nil {
				return fmt.Errorf("list engine assets: %w", err)
			}
			modelsRaw, err := app.ToolDeps.ListModelAssets(ctx)
			if err != nil {
				return fmt.Errorf("list model assets: %w", err)
			}

			var profiles []map[string]any
			var engines []map[string]any
			var models []map[string]any
			if err := json.Unmarshal(profilesRaw, &profiles); err != nil {
				return fmt.Errorf("decode profiles: %w", err)
			}
			if err := json.Unmarshal(enginesRaw, &engines); err != nil {
				return fmt.Errorf("decode engines: %w", err)
			}
			if err := json.Unmarshal(modelsRaw, &models); err != nil {
				return fmt.Errorf("decode models: %w", err)
			}

			summary := map[string]any{
				"hardware_profiles": len(profiles),
				"engine_assets":     len(engines),
				"model_assets":      len(models),
			}

			profileNames := make([]string, 0, len(profiles))
			for _, hp := range profiles {
				if n, ok := hp["name"].(string); ok && n != "" {
					profileNames = append(profileNames, n)
					continue
				}
				if n, ok := hp["id"].(string); ok && n != "" {
					profileNames = append(profileNames, n)
				}
			}
			summary["profiles"] = profileNames

			engineNames := make([]string, 0, len(engines))
			for _, ea := range engines {
				if t, ok := ea["type"].(string); ok && t != "" {
					engineNames = append(engineNames, t)
					continue
				}
				if n, ok := ea["name"].(string); ok && n != "" {
					engineNames = append(engineNames, n)
					continue
				}
				if n, ok := ea["id"].(string); ok && n != "" {
					engineNames = append(engineNames, n)
				}
			}
			summary["engines"] = engineNames

			modelNames := make([]string, 0, len(models))
			for _, ma := range models {
				if n, ok := ma["name"].(string); ok && n != "" {
					modelNames = append(modelNames, n)
					continue
				}
				if n, ok := ma["id"].(string); ok && n != "" {
					modelNames = append(modelNames, n)
				}
			}
			summary["models"] = modelNames

			out, _ := json.MarshalIndent(summary, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
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
