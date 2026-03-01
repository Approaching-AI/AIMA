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
