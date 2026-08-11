package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerOpenClawTools(s *Server, deps *ToolDeps) {
	// openclaw — sync/status/claim via action param
	s.RegisterTool(&Tool{
		Name:        "openclaw",
		Description: "OpenClaw integration management. action=sync: sync AIMA deployed models to OpenClaw config (categorizes by modality, writes providers, manages MCP server entry). action=status: inspect current OpenClaw integration state (gateway reachability, config presence, sync drift, excluded_models). action=claim: explicitly claim legacy OpenClaw config that already points at the local AIMA proxy. action=exclude: revoke a model (param 'model') from sync — removed from OpenClaw config and skipped by future syncs until included again; persistent + reversible. action=include: clear a model's revocation so the next sync re-adds it.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["sync","status","claim","exclude","include"],"description":"OpenClaw action"},`+
				`"model":{"type":"string","description":"Model ID to revoke/restore (required for exclude and include)"},`+
				`"dry_run":{"type":"boolean","description":"Preview changes without writing (for sync and claim, default false)"},`+
				`"sections":{"type":"array","items":{"type":"string"},"description":"Optional claim sections: llm, asr, vision, tts, image_gen. Default claims all detectable sections (for claim)."}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action   string   `json:"action"`
				Model    string   `json:"model"`
				DryRun   bool     `json:"dry_run"`
				Sections []string `json:"sections"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
				switch p.Action {
				case "sync":
					if deps.OpenClawSync == nil {
						return ErrorResult("openclaw action=sync not available"), nil
					}
				data, err := deps.OpenClawSync(ctx, p.DryRun)
				if err != nil {
					return nil, fmt.Errorf("openclaw sync: %w", err)
				}
				return TextResult(string(data)), nil
				case "status":
					if deps.OpenClawStatus == nil {
						return ErrorResult("openclaw action=status not available"), nil
					}
				data, err := deps.OpenClawStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("openclaw status: %w", err)
				}
				return TextResult(string(data)), nil
				case "claim":
					if deps.OpenClawClaim == nil {
						return ErrorResult("openclaw action=claim not available"), nil
					}
				data, err := deps.OpenClawClaim(ctx, p.Sections, p.DryRun)
				if err != nil {
					return nil, fmt.Errorf("openclaw claim: %w", err)
				}
				return TextResult(string(data)), nil
				case "exclude":
					if deps.OpenClawExclude == nil {
						return ErrorResult("openclaw action=exclude not available"), nil
					}
					if p.Model == "" {
						return ErrorResult("openclaw action=exclude requires 'model'"), nil
					}
					data, err := deps.OpenClawExclude(ctx, p.Model)
					if err != nil {
						return nil, fmt.Errorf("openclaw exclude: %w", err)
					}
					return TextResult(string(data)), nil
				case "include":
					if deps.OpenClawInclude == nil {
						return ErrorResult("openclaw action=include not available"), nil
					}
					if p.Model == "" {
						return ErrorResult("openclaw action=include requires 'model'"), nil
					}
					data, err := deps.OpenClawInclude(ctx, p.Model)
					if err != nil {
						return nil, fmt.Errorf("openclaw include: %w", err)
					}
					return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: sync, status, claim, exclude, include", p.Action)), nil
			}
		},
	})
}
