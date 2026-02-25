package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/knowledge"
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
			summary := map[string]any{
				"hardware_profiles":    len(app.Catalog.HardwareProfiles),
				"engine_assets":        len(app.Catalog.EngineAssets),
				"model_assets":         len(app.Catalog.ModelAssets),
				"partition_strategies": len(app.Catalog.PartitionStrategies),
			}

			profiles := make([]string, 0, len(app.Catalog.HardwareProfiles))
			for _, hp := range app.Catalog.HardwareProfiles {
				profiles = append(profiles, hp.Metadata.Name)
			}
			summary["profiles"] = profiles

			engines := make([]string, 0, len(app.Catalog.EngineAssets))
			for _, ea := range app.Catalog.EngineAssets {
				engines = append(engines, ea.Metadata.Name)
			}
			summary["engines"] = engines

			models := make([]string, 0, len(app.Catalog.ModelAssets))
			for _, ma := range app.Catalog.ModelAssets {
				models = append(models, ma.Metadata.Name)
			}
			summary["models"] = models

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
			ctx := cmd.Context()
			modelName := args[0]

			hw, err := hal.Detect(ctx)
			if err != nil {
				return fmt.Errorf("detect hardware: %w", err)
			}

			gpuArch := ""
			if hw.GPU != nil {
				gpuArch = hw.GPU.Arch
			}

			hwInfo := knowledge.HardwareInfo{
				GPUArch: gpuArch,
				CPUArch: hw.CPU.Arch,
			}
			resolved, err := app.Catalog.Resolve(hwInfo, modelName, engineType, nil)
			if err != nil {
				return fmt.Errorf("resolve config for %s: %w", modelName, err)
			}

			out, _ := json.MarshalIndent(resolved, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&engineType, "engine", "", "Engine type to resolve for")

	return cmd
}
