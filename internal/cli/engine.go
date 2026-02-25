package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newEngineCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "engine",
		Short: "Manage inference engines",
	}

	cmd.AddCommand(
		newEngineScanCmd(app),
		newEngineListCmd(app),
		newEnginePullCmd(app),
		newEngineImportCmd(app),
		newEngineRemoveCmd(app),
	)

	return cmd
}

func newEngineScanCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "scan",
		Short: "Scan for locally available engine images",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			data, err := app.ToolDeps.ScanEngines(ctx)
			if err != nil {
				return fmt.Errorf("scan engines: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newEngineListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List known engines from the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			engines, err := app.DB.ListEngines(ctx)
			if err != nil {
				return fmt.Errorf("list engines: %w", err)
			}

			out, _ := json.MarshalIndent(engines, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}

func newEnginePullCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "pull <name>",
		Short: "Pull an inference engine image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			fmt.Fprintf(cmd.OutOrStdout(), "Pulling engine %s...\n", name)
			if err := app.ToolDeps.PullEngine(ctx, name); err != nil {
				return fmt.Errorf("pull engine %s: %w", name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Engine %s pulled successfully\n", name)
			return nil
		},
	}
}

func newEngineImportCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "import <path>",
		Short: "Import an engine image from a tar file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			tarPath := args[0]

			if err := app.ToolDeps.ImportEngine(ctx, tarPath); err != nil {
				return fmt.Errorf("import engine from %s: %w", tarPath, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Engine image imported from %s\n", tarPath)
			return nil
		},
	}
}

func newEngineRemoveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an engine from the database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			if err := app.DB.DeleteEngine(ctx, name); err != nil {
				return fmt.Errorf("remove engine %s: %w", name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Engine %s removed\n", name)
			return nil
		},
	}
}
