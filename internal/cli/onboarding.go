package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// onboardingCall routes an onboarding action to either the configured remote
// MCP endpoint or the supplied in-process ToolDeps closure. The MCP tool is a
// single "onboarding" tool with an "action" argument, so the action is merged
// into args before the remote call. Both branches return the same JSON payload
// shape so the per-subcommand printers stay identical.
func onboardingCall(
	ctx context.Context,
	app *App,
	action string,
	args map[string]any,
	local func(ctx context.Context) (json.RawMessage, error),
) (json.RawMessage, error) {
	if app.RemoteClient.Configured() {
		merged := map[string]any{"action": action}
		for k, v := range args {
			merged[k] = v
		}
		return app.RemoteClient.CallTool(ctx, "onboarding", merged)
	}
	return local(ctx)
}

// newOnboardingCmd exposes the unified onboarding workflow as CLI subcommands.
// Each subcommand is a thin wrapper that parses flags and delegates to either
// the matching ToolDeps.OnboardingX closure (in-process — same code path as
// Web UI / MCP) or, when root --remote is set, the remote `aima serve --mcp`
// endpoint.
func newOnboardingCmd(app *App) *cobra.Command {
	var (
		locale string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "onboarding",
		Short: "Manage edge device onboarding (cold-start wizard)",
		Long:  "Manage edge device onboarding. Same code path as the Web UI wizard and MCP onboarding tool.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnboardingStart(cmd.Context(), app, cmd.OutOrStdout(), locale, asJSON)
		},
	}
	cmd.Flags().StringVar(&locale, "locale", "en", "Locale for recommendation reasons (en|zh)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	cmd.AddCommand(newOnboardingStartCmd(app))
	cmd.AddCommand(newOnboardingStatusCmd(app))
	cmd.AddCommand(newOnboardingScanCmd(app))
	cmd.AddCommand(newOnboardingRecommendCmd(app))
	cmd.AddCommand(newOnboardingInitCmd(app))
	cmd.AddCommand(newOnboardingDeployCmd(app))
	return cmd
}

func newOnboardingStartCmd(app *App) *cobra.Command {
	var (
		locale string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Run the guided first-run checks",
		Long:  "Run status, scan, and recommendation checks, then print the next command for a safe first model run.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnboardingStart(cmd.Context(), app, cmd.OutOrStdout(), locale, asJSON)
		},
	}
	cmd.Flags().StringVar(&locale, "locale", "en", "Locale for recommendation reasons (en|zh)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

type onboardingStartStatus struct {
	OnboardingCompleted bool `json:"onboarding_completed"`
	Hardware            struct {
		ProfileMatch string `json:"profile_match"`
		OS           string `json:"os"`
		Arch         string `json:"arch"`
		RAMMiB       int    `json:"ram_mib"`
		GPU          []struct {
			Name    string `json:"name"`
			VRAMMiB int    `json:"vram_mib"`
			Count   int    `json:"count"`
			Arch    string `json:"arch"`
		} `json:"gpu"`
	} `json:"hardware"`
	StackStatus struct {
		Docker                 string `json:"docker"`
		K3S                    string `json:"k3s"`
		NeedsInit              bool   `json:"needs_init"`
		InitTierRecommendation string `json:"init_tier_recommendation"`
		CanAutoInit            bool   `json:"can_auto_init"`
		InitBlockedReason      string `json:"init_blocked_reason,omitempty"`
	} `json:"stack_status"`
}

type onboardingStartScanResult struct {
	Engines []struct {
		Type    string `json:"type"`
		Runtime string `json:"runtime"`
	} `json:"engines"`
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
	CentralConnected bool `json:"central_connected"`
	ConfigsPulled    int  `json:"configs_pulled,omitempty"`
	BenchmarksPulled int  `json:"benchmarks_pulled,omitempty"`
}

type onboardingStartRecommendResult struct {
	HardwareProfile string `json:"hardware_profile"`
	GPUArch         string `json:"gpu_arch"`
	GPUVRAMMiB      int    `json:"gpu_vram_mib"`
	GPUCount        int    `json:"gpu_count"`
	TotalModels     int    `json:"total_models_evaluated"`
	Recommendations []struct {
		ModelName   string `json:"model_name"`
		ModelType   string `json:"model_type"`
		Family      string `json:"family"`
		ParamCount  string `json:"parameter_count"`
		FitScore    int    `json:"fit_score"`
		Reason      string `json:"recommendation_reason"`
		HardwareFit bool   `json:"hardware_fit"`
		Engine      *struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"engine,omitempty"`
		ModelStatus struct {
			LocalAvailable bool `json:"local_available"`
		} `json:"model_status"`
	} `json:"recommendations"`
}

type onboardingStartResult struct {
	Status      onboardingStartStatus          `json:"status"`
	Scan        onboardingStartScanResult      `json:"scan"`
	Recommend   onboardingStartRecommendResult `json:"recommend"`
	NextModel   string                         `json:"next_model,omitempty"`
	NextCommand string                         `json:"next_command,omitempty"`
}

func runOnboardingStart(ctx context.Context, app *App, w io.Writer, locale string, asJSON bool) error {
	data, err := onboardingCall(ctx, app, "start", map[string]any{"locale": locale}, func(ctx context.Context) (json.RawMessage, error) {
		if app.ToolDeps.OnboardingStart == nil {
			return nil, fmt.Errorf("onboarding.start not available")
		}
		return app.ToolDeps.OnboardingStart(ctx, locale)
	})
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(w, data)
	}

	var result onboardingStartResult
	if err := json.Unmarshal(data, &result); err != nil {
		return printJSON(w, data)
	}

	fmt.Fprintln(w, "AIMA first-run guide")
	fmt.Fprintf(w, "Hardware: %s / %s, RAM=%d MiB, profile=%s\n",
		fallback(result.Status.Hardware.OS, "-"),
		fallback(result.Status.Hardware.Arch, "-"),
		result.Status.Hardware.RAMMiB,
		fallback(result.Status.Hardware.ProfileMatch, "(none)"))
	if len(result.Status.Hardware.GPU) == 0 {
		fmt.Fprintln(w, "GPU: (none)")
	} else {
		for i, g := range result.Status.Hardware.GPU {
			fmt.Fprintf(w, "GPU[%d]: %s x%d, %d MiB, arch=%s\n", i, g.Name, g.Count, g.VRAMMiB, fallback(g.Arch, "-"))
		}
	}
	fmt.Fprintf(w, "Stack: docker=%s k3s=%s needs_init=%v\n",
		fallback(result.Status.StackStatus.Docker, "-"),
		fallback(result.Status.StackStatus.K3S, "-"),
		result.Status.StackStatus.NeedsInit)
	if result.Status.StackStatus.NeedsInit {
		tier := fallback(result.Status.StackStatus.InitTierRecommendation, "auto")
		if result.Status.StackStatus.CanAutoInit {
			fmt.Fprintf(w, "Init suggestion: aima onboarding init --tier %s --yes\n", tier)
		} else {
			if result.Status.StackStatus.InitBlockedReason != "" {
				fmt.Fprintf(w, "Init blocked: %s\n", result.Status.StackStatus.InitBlockedReason)
			}
			fmt.Fprintf(w, "Manual init: sudo aima onboarding init --tier %s --yes\n", tier)
		}
	}
	fmt.Fprintf(w, "Scan: engines=%d models=%d central_connected=%v configs=%d benchmarks=%d\n",
		len(result.Scan.Engines), len(result.Scan.Models), result.Scan.CentralConnected, result.Scan.ConfigsPulled, result.Scan.BenchmarksPulled)
	fmt.Fprintf(w, "Recommendations: %d of %d models evaluated\n\n", len(result.Recommend.Recommendations), result.Recommend.TotalModels)

	limit := len(result.Recommend.Recommendations)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		r := result.Recommend.Recommendations[i]
		engine := "(auto)"
		if r.Engine != nil {
			engine = r.Engine.Type + "/" + r.Engine.Name
		}
		fmt.Fprintf(w, "[%d] %s (%s, %s, %s) score=%d local=%v engine=%s\n",
			i+1, r.ModelName, r.Family, r.ParamCount, r.ModelType, r.FitScore, r.ModelStatus.LocalAvailable, engine)
		if r.Reason != "" {
			fmt.Fprintf(w, "    reason: %s\n", r.Reason)
		}
	}

	if result.NextCommand == "" {
		fmt.Fprintln(w, "\nNext: no deployable recommendation found; run `aima onboarding recommend --json` for details.")
		return nil
	}
	fmt.Fprintf(w, "\nNext: %s\n", result.NextCommand)
	fmt.Fprintln(w, "Keep the local API/UI open with: aima serve")
	return nil
}

func newOnboardingStatusCmd(app *App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show onboarding state (hardware, stack readiness, version)",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := onboardingCall(cmd.Context(), app, "status", nil, func(ctx context.Context) (json.RawMessage, error) {
				if app.ToolDeps.OnboardingStatus == nil {
					return nil, fmt.Errorf("onboarding.status not available")
				}
				return app.ToolDeps.OnboardingStatus(ctx)
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), data)
			}

			var s struct {
				OnboardingCompleted bool `json:"onboarding_completed"`
				Hardware            struct {
					ProfileMatch string `json:"profile_match"`
					OS           string `json:"os"`
					Arch         string `json:"arch"`
					RAMMiB       int    `json:"ram_mib"`
					GPU          []struct {
						Name    string `json:"name"`
						VRAMMiB int    `json:"vram_mib"`
						Count   int    `json:"count"`
						Arch    string `json:"arch"`
					} `json:"gpu"`
				} `json:"hardware"`
				StackStatus struct {
					Docker                 string `json:"docker"`
					K3S                    string `json:"k3s"`
					NeedsInit              bool   `json:"needs_init"`
					InitTierRecommendation string `json:"init_tier_recommendation"`
					CanAutoInit            bool   `json:"can_auto_init"`
					InitBlockedReason      string `json:"init_blocked_reason,omitempty"`
				} `json:"stack_status"`
				Version struct {
					Current          string `json:"current"`
					Latest           string `json:"latest,omitempty"`
					UpgradeAvailable bool   `json:"upgrade_available"`
					ReleaseURL       string `json:"release_url,omitempty"`
				} `json:"version"`
			}
			if err := json.Unmarshal(data, &s); err != nil {
				return printJSON(cmd.OutOrStdout(), data)
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Onboarding completed : %v\n", s.OnboardingCompleted)
			fmt.Fprintf(w, "Profile match        : %s\n", fallback(s.Hardware.ProfileMatch, "(none)"))
			fmt.Fprintf(w, "OS / Arch            : %s / %s\n", s.Hardware.OS, s.Hardware.Arch)
			fmt.Fprintf(w, "RAM                  : %d MiB\n", s.Hardware.RAMMiB)
			if len(s.Hardware.GPU) == 0 {
				fmt.Fprintln(w, "GPU                  : (none)")
			} else {
				for i, g := range s.Hardware.GPU {
					fmt.Fprintf(w, "GPU[%d]               : %s x%d, %d MiB, arch=%s\n", i, g.Name, g.Count, g.VRAMMiB, g.Arch)
				}
			}
			fmt.Fprintf(w, "Stack / docker       : %s\n", s.StackStatus.Docker)
			fmt.Fprintf(w, "Stack / k3s          : %s\n", s.StackStatus.K3S)
			fmt.Fprintf(w, "Needs init           : %v (tier=%s, can_auto=%v)\n",
				s.StackStatus.NeedsInit, s.StackStatus.InitTierRecommendation, s.StackStatus.CanAutoInit)
			if s.StackStatus.InitBlockedReason != "" {
				fmt.Fprintf(w, "Init blocked reason  : %s\n", s.StackStatus.InitBlockedReason)
			}
			fmt.Fprintf(w, "Version              : %s (upgrade_available=%v, latest=%s)\n",
				s.Version.Current, s.Version.UpgradeAvailable, fallback(s.Version.Latest, "-"))
			if s.Version.ReleaseURL != "" {
				fmt.Fprintf(w, "Release URL          : %s\n", s.Version.ReleaseURL)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

func newOnboardingScanCmd(app *App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan engines, models, and central connectivity",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := onboardingCall(cmd.Context(), app, "scan", nil, func(ctx context.Context) (json.RawMessage, error) {
				if app.ToolDeps.OnboardingScan == nil {
					return nil, fmt.Errorf("onboarding.scan not available")
				}
				return app.ToolDeps.OnboardingScan(ctx)
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), data)
			}

			var env struct {
				Result json.RawMessage `json:"result"`
				Events []struct {
					Type string         `json:"type"`
					Data map[string]any `json:"data"`
				} `json:"events"`
			}
			if err := json.Unmarshal(data, &env); err != nil {
				return printJSON(cmd.OutOrStdout(), data)
			}

			w := cmd.OutOrStdout()
			for _, ev := range env.Events {
				printEvent(w, ev.Type, ev.Data)
			}

			var res struct {
				Engines []struct {
					Type    string `json:"type"`
					Image   string `json:"image,omitempty"`
					Runtime string `json:"runtime"`
				} `json:"engines"`
				Models []struct {
					Name      string `json:"name"`
					Format    string `json:"format,omitempty"`
					SizeBytes int64  `json:"size_bytes,omitempty"`
				} `json:"models"`
				CentralConnected bool `json:"central_connected"`
				ConfigsPulled    int  `json:"configs_pulled,omitempty"`
				BenchmarksPulled int  `json:"benchmarks_pulled,omitempty"`
			}
			if err := json.Unmarshal(env.Result, &res); err == nil {
				fmt.Fprintf(w, "\nScan complete: engines=%d models=%d central_connected=%v (configs=%d benchmarks=%d)\n",
					len(res.Engines), len(res.Models), res.CentralConnected, res.ConfigsPulled, res.BenchmarksPulled)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

func newOnboardingRecommendCmd(app *App) *cobra.Command {
	var (
		locale string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "recommend",
		Short: "Recommend model/engine combos fit for this hardware",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := onboardingCall(cmd.Context(), app, "recommend", map[string]any{"locale": locale}, func(ctx context.Context) (json.RawMessage, error) {
				if app.ToolDeps.OnboardingRecommend == nil {
					return nil, fmt.Errorf("onboarding.recommend not available")
				}
				return app.ToolDeps.OnboardingRecommend(ctx, locale)
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), data)
			}

			var res struct {
				HardwareProfile string `json:"hardware_profile"`
				GPUArch         string `json:"gpu_arch"`
				GPUVRAMMiB      int    `json:"gpu_vram_mib"`
				GPUCount        int    `json:"gpu_count"`
				TotalModels     int    `json:"total_models_evaluated"`
				Recommendations []struct {
					ModelName  string `json:"model_name"`
					ModelType  string `json:"model_type"`
					Family     string `json:"family"`
					ParamCount string `json:"parameter_count"`
					FitScore   int    `json:"fit_score"`
					Reason     string `json:"recommendation_reason"`
					Engine     *struct {
						Type string `json:"type"`
						Name string `json:"name"`
					} `json:"engine,omitempty"`
					ModelStatus struct {
						LocalAvailable bool `json:"local_available"`
					} `json:"model_status"`
				} `json:"recommendations"`
			}
			if err := json.Unmarshal(data, &res); err != nil {
				return printJSON(cmd.OutOrStdout(), data)
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Hardware profile : %s (arch=%s, vram=%d MiB, count=%d)\n",
				fallback(res.HardwareProfile, "(none)"), res.GPUArch, res.GPUVRAMMiB, res.GPUCount)
			fmt.Fprintf(w, "Models evaluated : %d\n", res.TotalModels)
			fmt.Fprintf(w, "Recommendations  : %d\n\n", len(res.Recommendations))
			for i, r := range res.Recommendations {
				engine := "(none)"
				if r.Engine != nil {
					engine = r.Engine.Type + "/" + r.Engine.Name
				}
				fmt.Fprintf(w, "[%d] %s (%s, %s, %s)\n", i+1, r.ModelName, r.Family, r.ParamCount, r.ModelType)
				fmt.Fprintf(w, "    score=%d local=%v engine=%s\n", r.FitScore, r.ModelStatus.LocalAvailable, engine)
				if r.Reason != "" {
					fmt.Fprintf(w, "    reason: %s\n", r.Reason)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&locale, "locale", "en", "Locale for recommendation reasons (en|zh)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

func newOnboardingInitCmd(app *App) *cobra.Command {
	var (
		tier          string
		allowDownload bool
		yesFlag       bool
		asJSON        bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install infrastructure stack (docker/k3s/auto)",
		Long: `Initialize AIMA infrastructure stack on this device.

Tiers:
  --tier docker   Docker + nvidia-ctk + aima-serve (lightweight container inference)
  --tier k3s      + K3S + HAMi (GPU partitioning, multi-model scheduling)
  --tier auto     Pick the most appropriate tier from status recommendation

This command installs system software. Use --yes to skip the confirmation prompt.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.RemoteClient.Configured() && app.ToolDeps.OnboardingInit == nil {
				return fmt.Errorf("onboarding.init not available")
			}

			if !yesFlag && isTTY() {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"WARNING: `onboarding init` will install system software (docker/k3s/hami) on this host.\nTier=%s, allow-download=%v. Continue? [y/N] ",
					tier, allowDownload)
				scanner := bufio.NewScanner(cmd.InOrStdin())
				if !scanner.Scan() {
					return fmt.Errorf("aborted")
				}
				answer := strings.TrimSpace(scanner.Text())
				if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
					fmt.Fprintln(cmd.ErrOrStderr(), "Aborted.")
					return nil
				}
			}

			if app.RemoteClient.Configured() {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"Dispatching remote init to %s (this may take several minutes; events will be printed in order once the call completes — remote SSE streaming not yet supported, see server logs for live progress).\n",
					app.RemoteClient.Endpoint)
			}
			data, err := onboardingCall(cmd.Context(), app, "init",
				map[string]any{"tier": tier, "allow_download": allowDownload},
				func(ctx context.Context) (json.RawMessage, error) {
					return app.ToolDeps.OnboardingInit(ctx, tier, allowDownload)
				})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), data)
			}

			var env struct {
				Result json.RawMessage `json:"result"`
				Events []struct {
					Type string         `json:"type"`
					Data map[string]any `json:"data"`
				} `json:"events"`
			}
			if err := json.Unmarshal(data, &env); err != nil {
				return printJSON(cmd.OutOrStdout(), data)
			}

			w := cmd.OutOrStdout()
			for _, ev := range env.Events {
				printEvent(w, ev.Type, ev.Data)
			}

			var res struct {
				AllReady    bool `json:"all_ready"`
				StackStatus struct {
					Docker string `json:"docker"`
					K3S    string `json:"k3s"`
				} `json:"stack_status"`
				Tier string `json:"tier,omitempty"`
			}
			if err := json.Unmarshal(env.Result, &res); err == nil {
				fmt.Fprintf(w, "\nInit complete: tier=%s all_ready=%v (docker=%s k3s=%s)\n",
					res.Tier, res.AllReady, res.StackStatus.Docker, res.StackStatus.K3S)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tier, "tier", "auto", "Stack tier to install (docker|k3s|auto)")
	cmd.Flags().BoolVar(&allowDownload, "allow-download", false, "Allow downloading missing archives/binaries")
	cmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

func newOnboardingDeployCmd(app *App) *cobra.Command {
	var (
		model           string
		engineType      string
		slot            string
		configOverrides string
		noPull          bool
		yesFlag         bool
		asJSON          bool
	)
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a model using the onboarding pipeline",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.RemoteClient.Configured() && app.ToolDeps.OnboardingDeploy == nil {
				return fmt.Errorf("onboarding.deploy not available")
			}
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("--model is required")
			}

			var overrides map[string]any
			if strings.TrimSpace(configOverrides) != "" {
				if err := json.Unmarshal([]byte(configOverrides), &overrides); err != nil {
					return fmt.Errorf("parse --config-overrides: %w", err)
				}
			}

			if !yesFlag && isTTY() {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"About to deploy model=%s engine=%s slot=%s no_pull=%v. Continue? [y/N] ",
					model, fallback(engineType, "auto"), fallback(slot, "auto"), noPull)
				scanner := bufio.NewScanner(cmd.InOrStdin())
				if !scanner.Scan() {
					return fmt.Errorf("aborted")
				}
				answer := strings.TrimSpace(scanner.Text())
				if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
					fmt.Fprintln(cmd.ErrOrStderr(), "Aborted.")
					return nil
				}
			}

			if app.RemoteClient.Configured() {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"Dispatching remote deploy to %s (events will be printed in order after the call completes — remote SSE streaming not yet supported, see server logs for live progress).\n",
					app.RemoteClient.Endpoint)
			}
			data, err := onboardingCall(cmd.Context(), app, "deploy",
				map[string]any{
					"model":            model,
					"engine":           engineType,
					"slot":             slot,
					"config_overrides": overrides,
					"no_pull":          noPull,
				},
				func(ctx context.Context) (json.RawMessage, error) {
					return app.ToolDeps.OnboardingDeploy(ctx, model, engineType, slot, overrides, noPull)
				})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), data)
			}

			var env struct {
				Result json.RawMessage `json:"result"`
				Events []struct {
					Type string         `json:"type"`
					Data map[string]any `json:"data"`
				} `json:"events"`
			}
			if err := json.Unmarshal(data, &env); err != nil {
				return printJSON(cmd.OutOrStdout(), data)
			}

			w := cmd.OutOrStdout()
			for _, ev := range env.Events {
				printEvent(w, ev.Type, ev.Data)
			}

			var res struct {
				Name     string `json:"name,omitempty"`
				Model    string `json:"model"`
				Engine   string `json:"engine"`
				Endpoint string `json:"endpoint"`
				Status   string `json:"status"`
				Message  string `json:"message,omitempty"`
			}
			if err := json.Unmarshal(env.Result, &res); err == nil {
				fmt.Fprintf(w, "\nDeploy complete: model=%s engine=%s name=%s status=%s\n",
					res.Model, res.Engine, fallback(res.Name, "-"), res.Status)
				if res.Endpoint != "" {
					fmt.Fprintf(w, "Endpoint: %s\n", res.Endpoint)
				}
				if res.Message != "" {
					fmt.Fprintf(w, "Message:  %s\n", res.Message)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Model name to deploy (required)")
	cmd.Flags().StringVar(&engineType, "engine", "", "Engine type (auto-detected when empty)")
	cmd.Flags().StringVar(&slot, "slot", "", "Partition slot (auto-selected when empty)")
	cmd.Flags().StringVar(&configOverrides, "config-overrides", "", "JSON object of config overrides")
	cmd.Flags().BoolVar(&noPull, "no-pull", false, "Skip image/model pull")
	cmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output raw JSON")
	return cmd
}

// printJSON writes raw JSON pretty-printed.
func printJSON(w io.Writer, data json.RawMessage) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Fprintln(w, string(data))
		return nil
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(out))
	return nil
}

// printEvent renders a single onboarding event as one line.
// e.g. "[scan_start] phase=engines"
func printEvent(w io.Writer, typ string, data map[string]any) {
	if len(data) == 0 {
		fmt.Fprintf(w, "[%s]\n", typ)
		return
	}
	var parts []string
	for k, v := range data {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	fmt.Fprintf(w, "[%s] %s\n", typ, strings.Join(parts, " "))
}

func fallback(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
