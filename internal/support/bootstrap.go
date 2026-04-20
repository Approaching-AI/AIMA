package support

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jguan/aima/internal/cloud"
)

// Environment variable name introduced for the unified cloud bootstrap. It
// takes priority over the legacy AIMA_SUPPORT_INVITE_CODE so operators can
// set one value and have it flow to both the registration worker and any
// ad-hoc `aima support ask` invocation.
const EnvInviteCode = "AIMA_INVITE_CODE"

// BootstrapOptions carries optional overrides for an auto-register attempt.
// All fields are optional — Bootstrap() falls back to existing config / env
// defaults when they are empty.
type BootstrapOptions struct {
	Endpoint     string
	InviteCode   string
	WorkerCode   string
	RecoveryCode string
	ReferralCode string
	// Force re-registration even when a valid token already exists. Used by
	// `aima device register --force` to recover from a corrupted local state.
	Force bool
}

// BootstrapResult reports the outcome of Bootstrap().
type BootstrapResult struct {
	DeviceID          string `json:"device_id"`
	Endpoint          string `json:"endpoint"`
	AlreadyRegistered bool   `json:"already_registered"`
	Created           bool   `json:"created"`
	TokenExpiresAt    string `json:"token_expires_at,omitempty"`
	ReferralCode      string `json:"referral_code,omitempty"`
}

// Bootstrap ensures this edge has a valid aima-service identity. Call it on
// startup (non-blocking goroutine) and from the `aima device register` CLI.
//
// Behavior:
//   - If the edge is already registered and the token isn't expiring soon,
//     Bootstrap is a no-op (AlreadyRegistered=true).
//   - Otherwise it persists any provided overrides, resolves invite_code from
//     opts → AIMA_INVITE_CODE env → AIMA_SUPPORT_INVITE_CODE env → config →
//     DefaultInviteCode, then POSTs /devices/self-register.
//   - On success, canonical cloud.* config keys are written via the saveState
//     mirror so the rest of AIMA sees a single source of truth.
//   - On failure, device.registration_state is tagged "failed".
func (s *Service) Bootstrap(ctx context.Context, opts BootstrapOptions) (BootstrapResult, error) {
	if !opts.Force {
		if state := s.loadState(ctx); state.DeviceID != "" && state.Token != "" && !tokenExpiringSoon(state, s.now()) {
			return BootstrapResult{
				AlreadyRegistered: true,
				DeviceID:          state.DeviceID,
				Endpoint:          s.endpointFromConfig(ctx),
				TokenExpiresAt:    state.TokenExpiresAt,
				ReferralCode:      state.ReferralCode,
			}, nil
		}
	}

	// Surface "pending" while the worker attempts registration so `aima device
	// status` reflects the in-flight state.
	if err := s.store.SetConfig(ctx, cloud.ConfigRegistrationState, cloud.StatePending); err != nil {
		return BootstrapResult{}, fmt.Errorf("set registration_state=pending: %w", err)
	}

	req := AskRequest{
		Endpoint:     strings.TrimSpace(opts.Endpoint),
		InviteCode:   resolveInviteCode(opts.InviteCode),
		WorkerCode:   strings.TrimSpace(opts.WorkerCode),
		RecoveryCode: strings.TrimSpace(opts.RecoveryCode),
		ReferralCode: strings.TrimSpace(opts.ReferralCode),
	}

	if err := s.persistOverrides(ctx, req); err != nil {
		s.tagRegistrationFailed(ctx)
		return BootstrapResult{}, err
	}

	state, endpoint, resp, err := s.ensureRegistered(ctx, req)
	if err != nil {
		// Prompt errors mean the edge is waiting on user input; keep state
		// at "unregistered" so offline-first semantics stay accurate. Only
		// genuine server/network failures get tagged "failed".
		var prompt *RegistrationPromptError
		if errors.As(err, &prompt) {
			if setErr := s.store.SetConfig(ctx, cloud.ConfigRegistrationState, cloud.StateUnregistered); setErr != nil {
				s.logger.Warn("restore registration_state=unregistered after prompt error",
					"error", setErr, "prompt_kind", prompt.Kind)
			}
			return BootstrapResult{}, err
		}
		s.tagRegistrationFailed(ctx)
		return BootstrapResult{}, err
	}

	// ensureRegistered runs saveState internally which mirrors the canonical
	// keys and marks state=registered. Nothing extra to do here on success.
	result := BootstrapResult{
		DeviceID:       state.DeviceID,
		Endpoint:       endpoint,
		Created:        resp != nil,
		TokenExpiresAt: state.TokenExpiresAt,
		ReferralCode:   state.ReferralCode,
	}
	return result, nil
}

// IsRegistered reports whether the canonical identity has a device_id + token.
func (s *Service) IsRegistered(ctx context.Context) bool {
	state := s.loadState(ctx)
	return state.DeviceID != "" && state.Token != ""
}

// RenewToken force-renews the aima-service bearer token regardless of its
// current expiry. Returns the new expiry on success. Normal operation relies
// on the in-process renewal path inside RunBackground() or the patrol loop;
// this method exists for operator-initiated renewal via `aima device renew`.
func (s *Service) RenewToken(ctx context.Context) (time.Time, error) {
	state := s.loadState(ctx)
	if state.DeviceID == "" || state.Token == "" {
		return time.Time{}, cloud.ErrNotRegistered
	}
	endpoint := s.endpointFromConfig(ctx)
	if endpoint == "" {
		return time.Time{}, fmt.Errorf("aima-service endpoint is not configured")
	}

	var resp renewTokenResponse
	url := endpoint + "/devices/" + state.DeviceID + "/renew-token"
	if err := s.doJSON(ctx, http.MethodPost, url, state.Token, nil, &resp); err != nil {
		return time.Time{}, err
	}
	if resp.Token != "" {
		state.Token = resp.Token
	}
	if resp.TokenExpiresAt != "" {
		state.TokenExpiresAt = resp.TokenExpiresAt
	}
	if err := s.saveState(ctx, state); err != nil {
		return time.Time{}, err
	}
	expires, _ := time.Parse(time.RFC3339, state.TokenExpiresAt)
	return expires, nil
}

// ResetIdentity clears all aima-service identity state (device_id, token,
// recovery_code, referral_code). The next `device.register` call produces a
// fresh cloud identity. DESTRUCTIVE — only call when the user has explicitly
// confirmed. Note: support state (budget, referral metadata, task history)
// is left in place; the caller must re-register to resume cloud traffic.
func (s *Service) ResetIdentity(ctx context.Context) error {
	keys := []string{
		configStateDeviceID,
		configStateToken,
		configStateRecoveryCode,
		configStateReferralCode,
		configStateTokenExpiresAt,
		cloud.ConfigDeviceID,
		cloud.ConfigDeviceToken,
		cloud.ConfigRecoveryCode,
		cloud.ConfigTokenExpiresAt,
		cloud.ConfigReferralCode,
	}
	for _, key := range keys {
		if err := s.store.SetConfig(ctx, key, ""); err != nil {
			return fmt.Errorf("clear %s: %w", key, err)
		}
	}
	return s.store.SetConfig(ctx, cloud.ConfigRegistrationState, cloud.StateUnregistered)
}

func (s *Service) tagRegistrationFailed(ctx context.Context) {
	if err := s.store.SetConfig(ctx, cloud.ConfigRegistrationState, cloud.StateFailed); err != nil {
		s.logger.Warn("mark registration_state=failed", "error", err)
	}
}

// tokenExpiringSoon returns true when the persisted token is within 7 days of
// expiry (mirrors internal/cloud/token_renewer's future threshold). An empty
// TokenExpiresAt is treated as "unknown/valid" so we don't force renewal on
// servers that don't set the field.
func tokenExpiringSoon(state deviceState, now time.Time) bool {
	if strings.TrimSpace(state.TokenExpiresAt) == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, state.TokenExpiresAt)
	if err != nil {
		return false
	}
	return t.Sub(now) < 7*24*time.Hour
}

// resolveInviteCode picks the highest-priority invite code source. Precedence:
//  1. explicit opts.InviteCode (CLI flag)
//  2. AIMA_INVITE_CODE env var (new unified name)
//  3. AIMA_SUPPORT_INVITE_CODE env var (legacy compat)
//  4. (empty string — ensureRegistered will fall back to ConfigInviteCode
//     or DefaultInviteCode on its own)
func resolveInviteCode(explicit string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(EnvInviteCode)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("AIMA_SUPPORT_INVITE_CODE")); v != "" {
		return v
	}
	return ""
}

// ErrInviteCodeRequired is returned when Bootstrap cannot find any source of
// an invite code and the server rejects the default. Separated out so the
// onboarding wizard and CLI can render a friendly prompt.
var ErrInviteCodeRequired = errors.New("aima-service invite code required (pass --invite-code or set AIMA_INVITE_CODE)")

// Backoff bounds for StartRegistrationWorker. Exponential doubling from
// minRegistrationBackoff, capped at maxRegistrationBackoff. These are package
// vars (not consts) so tests can shrink them to keep retry coverage fast;
// production code never mutates them.
var (
	minRegistrationBackoff = time.Second
	maxRegistrationBackoff = 5 * time.Minute
)

// StartRegistrationWorker runs Bootstrap in an exponential-backoff retry loop
// until success or ctx cancellation. Safe to invoke unconditionally on server
// startup — if the edge is already registered Bootstrap short-circuits on the
// first iteration without network I/O.
//
// Exit conditions:
//   - success (Bootstrap returns nil): worker returns, leaving the edge ready
//     for outbound Central/aima-service calls.
//   - context cancellation: worker returns without an error.
//   - RegistrationPromptError (invalid invite / recovery required): worker
//     gives up because the condition cannot be fixed without user input. The
//     CLI / onboarding wizard is expected to re-trigger Bootstrap after the
//     user provides the missing credential.
//
// Other failures (network down, 5xx, timeouts) keep looping.
func (s *Service) StartRegistrationWorker(ctx context.Context, opts BootstrapOptions) {
	backoff := minRegistrationBackoff
	for {
		res, err := s.Bootstrap(ctx, opts)
		if err == nil {
			if res.AlreadyRegistered {
				s.logger.Debug("device already registered", "device_id", res.DeviceID)
			} else {
				s.logger.Info("device registered with aima-service",
					"device_id", res.DeviceID, "endpoint", res.Endpoint)
			}
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		var prompt *RegistrationPromptError
		if errors.As(err, &prompt) {
			s.logger.Warn("device registration blocked on user input — run `aima device register` after providing the credential",
				"kind", prompt.Kind, "detail", prompt.Detail)
			return
		}
		s.logger.Warn("device registration failed; retrying",
			"error", err, "retry_in", backoff.String())
		if err := sleepContext(ctx, backoff); err != nil {
			return
		}
		if backoff < maxRegistrationBackoff {
			backoff *= 2
			if backoff > maxRegistrationBackoff {
				backoff = maxRegistrationBackoff
			}
		}
	}
}
