package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newModelCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Manage models",
	}

	cmd.AddCommand(
		newModelScanCmd(app),
		newModelListCmd(app),
		newModelPullCmd(app),
		newModelImportCmd(app),
		newModelInfoCmd(app),
		newModelRemoveCmd(app),
	)

	return cmd
}

func newModelScanCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan local filesystem for model files",
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		data, err := app.ToolDeps.ScanModels(ctx)
		if err != nil {
			return fmt.Errorf("scan models: %w", err)
		}

		fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
		return nil
	}

	return cmd
}

func newModelListCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all known models from the database",
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		data, err := app.ToolDeps.ListModels(ctx)
		if err != nil {
			return fmt.Errorf("list models: %w", err)
		}

		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}

	return cmd
}

func newModelPullCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull <name>",
		Short: "Download a model by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			fmt.Fprintf(cmd.OutOrStdout(), "Pulling model %s...\n", name)
			if err := app.ToolDeps.PullModel(ctx, name); err != nil {
				return fmt.Errorf("pull model %s: %w", name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Model %s downloaded successfully\n", name)
			return nil
		},
	}

	return cmd
}

func newModelImportCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <path>",
		Short: "Import a model from a local path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			srcPath := args[0]

			data, err := app.ToolDeps.ImportModel(ctx, srcPath)
			if err != nil {
				return fmt.Errorf("import model from %s: %w", srcPath, err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}

	return cmd
}

func newModelInfoCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info <name>",
		Short: "Get detailed information about a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			data, err := app.ToolDeps.GetModelInfo(ctx, name)
			if err != nil {
				return fmt.Errorf("get model info %s: %w", name, err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}

	return cmd
}

func newModelRemoveCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a model from the database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			// Call MCP tool for removal
			if app.ToolDeps.RemoveModel == nil {
				return fmt.Errorf("model.remove not implemented")
			}
			if err := app.ToolDeps.RemoveModel(ctx, name); err != nil {
				return fmt.Errorf("remove model %s: %w", name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Model %s removed\n", name)
			return nil
		},
	}

	return cmd
}
