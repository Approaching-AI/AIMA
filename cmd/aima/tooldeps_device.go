package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jguan/aima/internal/cloud"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/support"
)

// wireDeviceDeps attaches the aima-service device-identity tool closures to
// the MCP ToolDeps struct. The underlying behavior lives in the support
// package; this layer only marshals JSON in/out and maps errors, matching
// the "thin CLI" pattern used by every other MCP tool implementation.
func wireDeviceDeps(deps *mcp.ToolDeps, svc *support.Service) {
	deps.DeviceRegister = func(ctx context.Context, inviteCode, recoveryCode string, force bool) (json.RawMessage, error) {
		res, err := svc.Bootstrap(ctx, support.BootstrapOptions{
			InviteCode:   inviteCode,
			RecoveryCode: recoveryCode,
			Force:        force,
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	}

	deps.DeviceStatus = func(ctx context.Context) (json.RawMessage, error) {
		identity := cloud.ReadIdentity(ctx, deps.GetConfig)
		payload := map[string]any{
			"registration_state": identity.RegistrationState,
			"registered":         identity.Registered(),
			"device_id":          identity.DeviceID,
			"referral_code":      identity.ReferralCode,
		}
		if identity.RegistrationState == "" {
			payload["registration_state"] = cloud.StateUnregistered
		}
		if !identity.TokenExpiresAt.IsZero() {
			payload["token_expires_at"] = identity.TokenExpiresAt.UTC().Format(time.RFC3339)
			payload["token_expires_in_seconds"] = int(time.Until(identity.TokenExpiresAt).Seconds())
		}
		return json.Marshal(payload)
	}

	deps.DeviceRenew = func(ctx context.Context) (json.RawMessage, error) {
		expires, err := svc.RenewToken(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"renewed":          true,
			"token_expires_at": expires.UTC().Format(time.RFC3339),
		})
	}

	deps.DeviceReset = func(ctx context.Context, confirm bool) (json.RawMessage, error) {
		if !confirm {
			return json.Marshal(map[string]any{
				"reset":   false,
				"message": "pass confirm=true to clear the cloud identity",
			})
		}
		if err := svc.ResetIdentity(ctx); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"reset": true})
	}
}
