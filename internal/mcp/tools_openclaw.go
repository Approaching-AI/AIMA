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
		Description: "OpenClaw integration management. action=sync: sync AIMA deployed models to OpenClaw config (categorizes by modality, writes providers, manages MCP server entry). action=status: inspect current OpenClaw integration state (gateway reachability, config presence, sync drift). action=claim: explicitly claim legacy OpenClaw config that already points at the local AIMA proxy.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["sync","status","claim"],"description":"OpenClaw action"},`+
				`"dry_run":{"type":"boolean","description":"Preview changes without writing (for sync and claim, default false)"},`+
				`"sections":{"type":"array","items":{"type":"string"},"description":"Optional claim sections: llm, asr, vision, tts, image_gen. Default claims all detectable sections (for claim)."}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action   string   `json:"action"`
				DryRun   bool     `json:"dry_run"`
				Sections []string `json:"sections"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "sync":
				if deps.OpenClawSync == nil {
					return ErrorResult("openclaw.sync not available"), nil
				}
				data, err := deps.OpenClawSync(ctx, p.DryRun)
				if err != nil {
					return nil, fmt.Errorf("openclaw sync: %w", err)
				}
				return TextResult(string(data)), nil
			case "status":
				if deps.OpenClawStatus == nil {
					return ErrorResult("openclaw.status not available"), nil
				}
				data, err := deps.OpenClawStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("openclaw status: %w", err)
				}
				return TextResult(string(data)), nil
			case "claim":
				if deps.OpenClawClaim == nil {
					return ErrorResult("openclaw.claim not available"), nil
				}
				data, err := deps.OpenClawClaim(ctx, p.Sections, p.DryRun)
				if err != nil {
					return nil, fmt.Errorf("openclaw claim: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: sync, status, claim", p.Action)), nil
			}
		},
	})
}
