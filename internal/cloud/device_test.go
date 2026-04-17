package cloud

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStore is an in-memory ConfigGetter backed by a map, sufficient for unit tests.
type fakeStore map[string]string

func (f fakeStore) Get(_ context.Context, key string) (string, error) {
	return f[key], nil
}

func TestReadIdentity_Empty(t *testing.T) {
	store := fakeStore{}
	id := ReadIdentity(context.Background(), store.Get)
	if id.Registered() {
		t.Fatalf("expected empty identity to not be registered, got %+v", id)
	}
}

func TestReadIdentity_Populated(t *testing.T) {
	expires := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	store := fakeStore{
		ConfigDeviceID:          "dev-abc123",
		ConfigDeviceToken:       "tok-xyz",
		ConfigRecoveryCode:      "rc-1",
		ConfigTokenExpiresAt:    expires.Format(time.RFC3339),
		ConfigRegistrationState: StateRegistered,
		ConfigReferralCode:      "COOL-WAVE-42",
	}
	id := ReadIdentity(context.Background(), store.Get)
	if !id.Registered() {
		t.Fatalf("expected registered, got %+v", id)
	}
	if id.DeviceID != "dev-abc123" || id.Token != "tok-xyz" {
		t.Errorf("unexpected core identity: %+v", id)
	}
	if !id.TokenExpiresAt.Equal(expires) {
		t.Errorf("token expiry mismatch: got %v want %v", id.TokenExpiresAt, expires)
	}
	if id.ReferralCode != "COOL-WAVE-42" {
		t.Errorf("unexpected referral: %+v", id)
	}
}

func TestReadIdentity_MalformedExpiry(t *testing.T) {
	store := fakeStore{
		ConfigDeviceID:       "dev-1",
		ConfigDeviceToken:    "tok-1",
		ConfigTokenExpiresAt: "not-a-timestamp",
	}
	id := ReadIdentity(context.Background(), store.Get)
	if !id.TokenExpiresAt.IsZero() {
		t.Errorf("expected zero time for malformed expiry, got %v", id.TokenExpiresAt)
	}
	if !id.Registered() {
		t.Errorf("malformed expiry should not block Registered(), got %+v", id)
	}
}

func TestReadDeviceID(t *testing.T) {
	store := fakeStore{ConfigDeviceID: "  dev-trim  "}
	if got := ReadDeviceID(context.Background(), store.Get); got != "dev-trim" {
		t.Errorf("expected trimmed device id, got %q", got)
	}
}

func TestRequireRegistered(t *testing.T) {
	empty := fakeStore{}
	if _, err := RequireRegistered(context.Background(), empty.Get); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("expected ErrNotRegistered, got %v", err)
	}

	populated := fakeStore{ConfigDeviceID: "dev-ok"}
	id, err := RequireRegistered(context.Background(), populated.Get)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if id != "dev-ok" {
		t.Errorf("expected dev-ok, got %q", id)
	}
}

func TestRequireRegistered_PropagatesErrorsAsNotRegistered(t *testing.T) {
	brokenStore := func(_ context.Context, _ string) (string, error) {
		return "", errors.New("db closed")
	}
	// ReadDeviceID swallows errors (best-effort), so a broken backend surfaces
	// as empty → ErrNotRegistered. This keeps the outbound call-gate behavior
	// unambiguous even when storage misbehaves.
	if _, err := RequireRegistered(context.Background(), brokenStore); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("expected ErrNotRegistered on store error, got %v", err)
	}
}
