package cli

import (
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
		newEngineInfoCmd(app),
		newEnginePullCmd(app),
		newEngineImportCmd(app),
		newEngineRemoveCmd(app),
	)

	return cmd
}

func newEngineScanCmd(app *App) *cobra.Command {
	var runtime string
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for locally available engine images",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if runtime == "" {
				runtime = "auto"
			}
			if runtime != "auto" && runtime != "container" && runtime != "native" {
				return fmt.Errorf("invalid runtime: %s (must be auto, container, or native)", runtime)
			}

			data, err := app.ToolDeps.ScanEngines(ctx, runtime)
			if err != nil {
				return fmt.Errorf("scan engines: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "auto", "Runtime filter: auto, container, or native")
	return cmd
}

func newEngineInfoCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info <name>",
		Short: "Get full information about an engine (catalog knowledge + live availability)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			data, err := app.ToolDeps.GetEngineInfo(ctx, name)
			if err != nil {
				return fmt.Errorf("engine info %s: %w", name, err)
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

			data, err := app.ToolDeps.ListEngines(ctx)
			if err != nil {
				return fmt.Errorf("list engines: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newEnginePullCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "pull [name]",
		Short: "Pull an inference engine (default: catalog default engine)",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var name string
			if len(args) > 0 {
				name = args[0]
			}

			if name == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Pulling default engine...")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Pulling engine %s...\n", name)
			}
			if err := app.ToolDeps.PullEngine(ctx, name); err != nil {
				return fmt.Errorf("pull engine: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Engine pulled successfully")
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

			if app.ToolDeps.RemoveEngine == nil {
				return fmt.Errorf("engine.remove not implemented")
			}
			if err := app.ToolDeps.RemoveEngine(ctx, name); err != nil {
				return fmt.Errorf("remove engine %s: %w", name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Engine %s removed\n", name)
			return nil
		},
	}
}
