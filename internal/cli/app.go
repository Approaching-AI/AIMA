package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newAppCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage application dependency declarations",
	}

	// app register
	registerCmd := &cobra.Command{
		Use:   "register",
		Short: "Register an app with inference dependencies",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			needsJSON, _ := cmd.Flags().GetString("needs")
			if name == "" {
				return fmt.Errorf("--name required")
			}

			params := map[string]any{"name": name}
			if needsJSON != "" {
				var needs json.RawMessage
				if err := json.Unmarshal([]byte(needsJSON), &needs); err != nil {
					return fmt.Errorf("invalid --needs JSON: %w", err)
				}
				params["inference_needs"] = needs
			}

			paramsBytes, _ := json.Marshal(params)
			data, err := app.ToolDeps.AppRegister(context.Background(), paramsBytes)
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}
	registerCmd.Flags().String("name", "", "Application name")
	registerCmd.Flags().String("needs", "", "Inference needs as JSON array")

	// app provision
	provisionCmd := &cobra.Command{
		Use:   "provision [name]",
		Short: "Auto-deploy inference services for a registered app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, _ := json.Marshal(map[string]string{"name": args[0]})
			data, err := app.ToolDeps.AppProvision(context.Background(), params)
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}

	// app list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List registered apps and dependency status",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := app.ToolDeps.AppList(context.Background())
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}

	cmd.AddCommand(registerCmd, provisionCmd, listCmd)
	return cmd
}
