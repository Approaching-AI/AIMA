// Package cloud exposes the canonical device-identity surface for AIMA edge.
//
// A device's identity (device_id, token, recovery_code, registration state) is
// signed by aima-service during first-boot bootstrap and persisted in the
// SQLite config table under the device.* namespace. Every outbound cloud call
// (Central sync, advise, scenario) must resolve this identity before sending
// traffic so the server can attribute records to the correct device.
package cloud

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Canonical config keys under which cloud identity is persisted. These are
// written by the support package's saveState mirror and read by every
// outbound cloud caller (Central sync, advise, scenario). Adding a new key
// here is a wire-contract change — update the Central handler's expected
// query params in sync.
const (
	ConfigDeviceID          = "device.id"
	ConfigDeviceToken       = "device.token"
	ConfigRecoveryCode      = "device.recovery_code"
	ConfigTokenExpiresAt    = "device.token_expires_at"
	ConfigRegistrationState = "device.registration_state"
	ConfigReferralCode      = "device.referral_code"
)

// Registration state values stored under ConfigRegistrationState.
const (
	StateUnregistered = "unregistered"
	StatePending      = "pending"
	StateRegistered   = "registered"
	StateFailed       = "failed"
)

// ErrNotRegistered is returned by RequireRegistered when the edge has no
// valid aima-service device identity yet. Callers should surface a clear
// "please run `aima device register --invite-code ...`" message.
var ErrNotRegistered = errors.New("device not registered with aima-service")

// ConfigGetter is the minimal read interface cloud helpers need. It matches
// the signature of mcp.ToolDeps.GetConfig so wiring is trivial.
type ConfigGetter func(ctx context.Context, key string) (string, error)

// Identity bundles the canonical cloud identity fields.
type Identity struct {
	DeviceID          string
	Token             string
	RecoveryCode      string
	TokenExpiresAt    time.Time // zero if unknown
	RegistrationState string
	ReferralCode      string
}

// Registered reports whether the identity has a device_id + token pair.
func (i Identity) Registered() bool {
	return i.DeviceID != "" && i.Token != ""
}

// ReadIdentity returns the currently persisted cloud identity. Missing keys
// produce empty fields rather than an error; callers inspect Registered().
func ReadIdentity(ctx context.Context, get ConfigGetter) Identity {
	read := func(key string) string {
		v, err := get(ctx, key)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(v)
	}
	id := Identity{
		DeviceID:          read(ConfigDeviceID),
		Token:             read(ConfigDeviceToken),
		RecoveryCode:      read(ConfigRecoveryCode),
		RegistrationState: read(ConfigRegistrationState),
		ReferralCode:      read(ConfigReferralCode),
	}
	if raw := read(ConfigTokenExpiresAt); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			id.TokenExpiresAt = t
		}
	}
	return id
}

// ReadDeviceID returns the current device id, or empty string if not set.
// Use this for best-effort attribution (e.g. logs). For outbound calls that
// must fail fast, use RequireRegistered.
func ReadDeviceID(ctx context.Context, get ConfigGetter) string {
	v, err := get(ctx, ConfigDeviceID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// RequireRegistered returns the device id, or ErrNotRegistered if the edge
// has not completed aima-service bootstrap. Callers of Central / aima-service
// endpoints must gate outbound HTTP on this function.
func RequireRegistered(ctx context.Context, get ConfigGetter) (string, error) {
	id := ReadDeviceID(ctx, get)
	if id == "" {
		return "", ErrNotRegistered
	}
	return id, nil
}
