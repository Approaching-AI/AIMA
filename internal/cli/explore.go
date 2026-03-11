package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newExploreCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explore",
		Short: "Persistent exploration runs",
	}

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start a persisted exploration run",
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, _ := cmd.Flags().GetString("kind")
			goal, _ := cmd.Flags().GetString("goal")
			model, _ := cmd.Flags().GetString("model")
			engine, _ := cmd.Flags().GetString("engine")
			hardware, _ := cmd.Flags().GetString("hardware")
			endpoint, _ := cmd.Flags().GetString("endpoint")
			planner, _ := cmd.Flags().GetString("planner")
			sourceRef, _ := cmd.Flags().GetString("source-ref")
			requestedBy, _ := cmd.Flags().GetString("requested-by")
			maxCandidates, _ := cmd.Flags().GetInt("max-candidates")
			concurrency, _ := cmd.Flags().GetInt("concurrency")
			rounds, _ := cmd.Flags().GetInt("rounds")
			noWait, _ := cmd.Flags().GetBool("no-wait")

			params := map[string]any{
				"kind": kind,
				"target": map[string]any{
					"model":    model,
					"engine":   engine,
					"hardware": hardware,
				},
				"benchmark_profile": map[string]any{
					"endpoint":    endpoint,
					"concurrency": concurrency,
					"rounds":      rounds,
				},
				"constraints": map[string]any{
					"max_candidates": maxCandidates,
				},
			}
			if goal != "" {
				params["goal"] = goal
			}
			if planner != "" {
				params["planner"] = planner
			}
			if sourceRef != "" {
				params["source_ref"] = sourceRef
			}
			if requestedBy != "" {
				params["requested_by"] = requestedBy
			}

			paramsBytes, _ := json.Marshal(params)

			if noWait {
				data, err := app.ToolDeps.ExploreStart(context.Background(), paramsBytes)
				if err != nil {
					return err
				}
				fmt.Println(formatJSON(data))
				return nil
			}

			data, err := app.ToolDeps.ExploreStartAndWait(context.Background(), paramsBytes)
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}
	startCmd.Flags().String("kind", "tune", "Exploration kind")
	startCmd.Flags().String("goal", "", "Exploration goal")
	startCmd.Flags().String("model", "", "Target model")
	startCmd.Flags().String("engine", "", "Target engine")
	startCmd.Flags().String("hardware", "", "Target hardware")
	startCmd.Flags().String("endpoint", "", "Inference endpoint override")
	startCmd.Flags().String("planner", "", "Planner identity (none or zeroclaw)")
	startCmd.Flags().String("source-ref", "", "Source reference such as open question ID")
	startCmd.Flags().String("requested-by", "", "Requester identity")
	startCmd.Flags().Int("max-candidates", 20, "Maximum candidate configs")
	startCmd.Flags().Int("concurrency", 1, "Benchmark concurrency")
	startCmd.Flags().Int("rounds", 1, "Benchmark rounds")
	startCmd.Flags().Bool("no-wait", false, "Return immediately without waiting for completion")
	_ = startCmd.MarkFlagRequired("model")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show exploration run status",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("id")
			data, err := app.ToolDeps.ExploreStatus(context.Background(), id)
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}
	statusCmd.Flags().String("id", "", "Exploration run ID")
	_ = statusCmd.MarkFlagRequired("id")

	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a running exploration run",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("id")
			data, err := app.ToolDeps.ExploreStop(context.Background(), id)
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}
	stopCmd.Flags().String("id", "", "Exploration run ID")
	_ = stopCmd.MarkFlagRequired("id")

	resultCmd := &cobra.Command{
		Use:   "result",
		Short: "Show exploration run result",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("id")
			data, err := app.ToolDeps.ExploreResult(context.Background(), id)
			if err != nil {
				return err
			}
			fmt.Println(formatJSON(data))
			return nil
		},
	}
	resultCmd.Flags().String("id", "", "Exploration run ID")
	_ = resultCmd.MarkFlagRequired("id")

	cmd.AddCommand(startCmd, statusCmd, stopCmd, resultCmd)
	return cmd
}
