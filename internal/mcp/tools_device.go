package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerDeviceTools(s *Server, deps *ToolDeps) {
	s.RegisterTool(&Tool{
		Name: "device.register",
		Description: "Register this edge with aima-service (or re-register with a recovery code). " +
			"The resulting device_id + token flow automatically to Central and all other cloud calls. " +
			"Safe to call on an already-registered device; pass force=true to replace the existing identity.",
		InputSchema: schema(
			`"invite_code":{"type":"string","description":"aima-service invite code; required for new devices"},` +
				`"recovery_code":{"type":"string","description":"Saved recovery code; required when re-registering the same hardware"},` +
				`"force":{"type":"boolean","description":"Re-register even if a valid token is already stored"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeviceRegister == nil {
				return ErrorResult("device.register not implemented"), nil
			}
			var p struct {
				InviteCode   string `json:"invite_code"`
				RecoveryCode string `json:"recovery_code"`
				Force        bool   `json:"force"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
			}
			data, err := deps.DeviceRegister(ctx, p.InviteCode, p.RecoveryCode, p.Force)
			if err != nil {
				return nil, fmt.Errorf("device.register: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name: "device.status",
		Description: "Show this edge's aima-service registration state: device_id, token expiry, " +
			"registration_state (unregistered / pending / registered / failed), and referral code.",
		InputSchema: schema(``),
		Handler: func(ctx context.Context, _ json.RawMessage) (*ToolResult, error) {
			if deps.DeviceStatus == nil {
				return ErrorResult("device.status not implemented"), nil
			}
			data, err := deps.DeviceStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("device.status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "device.renew",
		Description: "Force-renew this edge's aima-service bearer token. Normally unnecessary — the background renewer handles this automatically.",
		InputSchema: schema(``),
		Handler: func(ctx context.Context, _ json.RawMessage) (*ToolResult, error) {
			if deps.DeviceRenew == nil {
				return ErrorResult("device.renew not implemented"), nil
			}
			data, err := deps.DeviceRenew(ctx)
			if err != nil {
				return nil, fmt.Errorf("device.renew: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name: "device.reset",
		Description: "DESTRUCTIVE: clear this edge's aima-service identity (device_id, token, recovery code). " +
			"The next `device.register` call produces a new cloud identity. Requires confirm=true.",
		InputSchema: schema(
			`"confirm":{"type":"boolean","description":"Must be true to proceed"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeviceReset == nil {
				return ErrorResult("device.reset not implemented"), nil
			}
			var p struct {
				Confirm bool `json:"confirm"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
			}
			data, err := deps.DeviceReset(ctx, p.Confirm)
			if err != nil {
				return nil, fmt.Errorf("device.reset: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
