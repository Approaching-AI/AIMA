package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// registerOnboardingTools registers the single "onboarding" MCP tool which
// exposes the cold-start wizard flow to AI agents via 6 actions:
//   - start     (read-only)   : status + scan + recommend + next command
//   - status    (read-only)   : hardware/stack/version/onboarding_completed
//   - scan      (read-only)   : parallel engines/models/central_sync
//   - recommend (read-only)   : model recommendations
//   - init      (destructive) : install docker/k3s stack
//   - deploy    (destructive) : apply a deployment
//
// scan/init/deploy collect asynchronous Event items into an array returned
// as part of the response JSON — MCP is a request-response protocol and does
// not stream, unlike the HTTP handler which serves the same business logic
// over SSE.
func registerOnboardingTools(s *Server, deps *ToolDeps) {
	s.RegisterTool(&Tool{
		Name: "onboarding",
		Description: "Manage edge device onboarding (cold-start wizard) — used by both human UI and AI agents. " +
			"Actions: start (read-only first-run guide), status (read-only), scan (read-only), recommend (read-only), init (destructive — installs docker/k3s stack), deploy (destructive — applies a deployment).",
		InputSchema: schema(
			`"action":{"type":"string","enum":["start","status","scan","recommend","init","deploy"],"description":"Which onboarding sub-action to run"},`+
				`"locale":{"type":"string","description":"Locale for recommend action (e.g. \"en\", \"zh\"). Optional."},`+
				`"tier":{"type":"string","enum":["auto","docker","k3s"],"description":"Init action: which stack tier to install. Default: auto."},`+
				`"allow_download":{"type":"boolean","description":"Init action: allow internet download. Default: false."},`+
				`"model":{"type":"string","description":"Deploy action: model name."},`+
				`"engine":{"type":"string","description":"Deploy action: engine type (e.g. vllm, llamacpp). Optional — auto-resolved if empty."},`+
				`"slot":{"type":"string","description":"Deploy action: resource slot. Optional."},`+
				`"config_overrides":{"type":"object","description":"Deploy action: override resolved config values. Optional."},`+
				`"no_pull":{"type":"boolean","description":"Deploy action: skip image pull. Default: false."}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var req struct {
				Action          string         `json:"action"`
				Locale          string         `json:"locale"`
				Tier            string         `json:"tier"`
				AllowDownload   bool           `json:"allow_download"`
				Model           string         `json:"model"`
				Engine          string         `json:"engine"`
				Slot            string         `json:"slot"`
				ConfigOverrides map[string]any `json:"config_overrides"`
				NoPull          bool           `json:"no_pull"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &req); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
			}

			switch req.Action {
			case "start":
				if deps.OnboardingStart == nil {
					return ErrorResult("onboarding action=start not implemented"), nil
				}
				raw, err := deps.OnboardingStart(ctx, req.Locale)
				if err != nil {
					return nil, fmt.Errorf("onboarding start: %w", err)
				}
				return TextResult(string(raw)), nil
			case "status":
				if deps.OnboardingStatus == nil {
					return ErrorResult("onboarding action=status not implemented"), nil
				}
				raw, err := deps.OnboardingStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("onboarding status: %w", err)
				}
				return TextResult(string(raw)), nil
			case "scan":
				if deps.OnboardingScan == nil {
					return ErrorResult("onboarding action=scan not implemented"), nil
				}
				raw, err := deps.OnboardingScan(ctx)
				if err != nil {
					return nil, fmt.Errorf("onboarding scan: %w", err)
				}
				return TextResult(string(raw)), nil
			case "recommend":
				if deps.OnboardingRecommend == nil {
					return ErrorResult("onboarding action=recommend not implemented"), nil
				}
				raw, err := deps.OnboardingRecommend(ctx, req.Locale)
				if err != nil {
					return nil, fmt.Errorf("onboarding recommend: %w", err)
				}
				return TextResult(string(raw)), nil
			case "init":
				if deps.OnboardingInit == nil {
					return ErrorResult("onboarding action=init not implemented"), nil
				}
				raw, err := deps.OnboardingInit(ctx, req.Tier, req.AllowDownload)
				if err != nil {
					return nil, fmt.Errorf("onboarding init: %w", err)
				}
				return TextResult(string(raw)), nil
			case "deploy":
				if deps.OnboardingDeploy == nil {
					return ErrorResult("onboarding action=deploy not implemented"), nil
				}
				raw, err := deps.OnboardingDeploy(ctx, req.Model, req.Engine, req.Slot, req.ConfigOverrides, req.NoPull)
				if err != nil {
					return nil, fmt.Errorf("onboarding deploy: %w", err)
				}
				return TextResult(string(raw)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: start, status, scan, recommend, init, deploy", req.Action)), nil
			}
		},
	})
}
