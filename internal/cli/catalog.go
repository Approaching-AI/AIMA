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
	cmd.AddCommand(newCatalogEffectiveCmd(app))
	cmd.AddCommand(newCatalogDiffCmd(app))
	cmd.AddCommand(newCatalogValidatePatchCmd(app))
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

func newCatalogEffectiveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "effective <kind> <name>",
		Short: "Show the effective YAML for one catalog asset after factory, central, and user overlays",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.CatalogEffective == nil {
				return fmt.Errorf("catalog.effective not available")
			}
			data, err := app.ToolDeps.CatalogEffective(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			return printCatalogStringField(cmd, data, "yaml", true)
		},
	}
}

func newCatalogDiffCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "diff <kind> <name>",
		Short: "Show the factory-to-effective diff for one catalog asset",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.CatalogDiff == nil {
				return fmt.Errorf("catalog.diff not available")
			}
			data, err := app.ToolDeps.CatalogDiff(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			return printCatalogStringField(cmd, data, "diff", false)
		},
	}
}

func newCatalogValidatePatchCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "validate-patch <yaml-file>",
		Short: "Validate one catalog patch against the current effective catalog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ToolDeps.CatalogValidatePatch == nil {
				return fmt.Errorf("catalog.validate_patch not available")
			}
			content, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read %s: %w", args[0], err)
			}
			data, err := app.ToolDeps.CatalogValidatePatch(cmd.Context(), string(content))
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

func printCatalogStringField(cmd *cobra.Command, data json.RawMessage, field string, fallbackJSON bool) error {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err == nil {
		if value, ok := payload[field].(string); ok {
			if value == "" && field == "diff" {
				fmt.Fprintln(cmd.OutOrStdout(), "no changes")
				return nil
			}
			fmt.Fprint(cmd.OutOrStdout(), value)
			if value == "" || value[len(value)-1] != '\n' {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		}
	}
	if fallbackJSON {
		var pretty json.RawMessage = data
		out, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
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
