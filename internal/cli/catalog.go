package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newCatalogCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Manage the YAML knowledge catalog",
	}

	cmd.AddCommand(newCatalogStatusCmd(app))
	cmd.AddCommand(newCatalogOverrideCmd(app))
	cmd.AddCommand(newCatalogValidateCmd(app))
	return cmd
}

func newCatalogOverrideCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "override <kind> <name> <yaml-file>",
		Short: "Write a user-owned YAML patch to the overlay catalog (takes effect on next restart)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.CatalogOverride == nil {
				return fmt.Errorf("catalog.override not available")
			}
			kind, name, yamlFile := args[0], args[1], args[2]
			content, err := os.ReadFile(yamlFile)
			if err != nil {
				return fmt.Errorf("read %s: %w", yamlFile, err)
			}
			data, err := app.ToolDeps.CatalogOverride(cmd.Context(), kind, name, string(content))
			if err != nil {
				return err
			}
			var pretty json.RawMessage = data
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
	return cmd
}

func newCatalogValidateCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate engine YAML catalog for schema issues (missing registries, proxy-in-name, single-point-of-failure)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.CatalogValidate == nil {
				return fmt.Errorf("catalog.validate not available")
			}
			data, err := app.ToolDeps.CatalogValidate(cmd.Context())
			if err != nil {
				return err
			}
			var pretty json.RawMessage = data
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}

func newCatalogStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show catalog status: factory assets, overlay assets, and staleness warnings",
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.CatalogStatus == nil {
				return fmt.Errorf("catalog.status not available")
			}
			data, err := app.ToolDeps.CatalogStatus(cmd.Context())
			if err != nil {
				return err
			}
			var pretty json.RawMessage = data
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}
