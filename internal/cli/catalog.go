package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newCatalogCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Manage the YAML knowledge catalog",
	}

	cmd.AddCommand(newCatalogStatusCmd(app))
	return cmd
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
