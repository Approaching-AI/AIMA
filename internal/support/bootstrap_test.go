package support

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jguan/aima/internal/cloud"
)

// newBootstrapFixture starts an httptest server handling /devices/self-register
// with a configurable status + body, wires a Service against it, and returns
// both so tests can inspect captured state.
type bootstrapFixture struct {
	server          *httptest.Server
	svc             *Service
	store           *memoryStore
	capturedBody    []byte
	capturedInvite  string
	capturedAuth    string
	registerHandler http.HandlerFunc
}

func newBootstrapFixture(t *testing.T, handler http.HandlerFunc) *bootstrapFixture {
	t.Helper()
	fx := &bootstrapFixture{registerHandler: handler, store: newMemoryStore()}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/self-register", func(w http.ResponseWriter, r *http.Request) {
		fx.capturedAuth = r.Header.Get("Authorization")
		body := map[string]any{}
		if r.Body != nil {
			defer r.Body.Close()
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &body)
		}
		if v, ok := body["invite_code"].(string); ok {
			fx.capturedInvite = v
		}
		fx.registerHandler(w, r)
	})
	fx.server = httptest.NewServer(mux)
	t.Cleanup(fx.server.Close)

	if err := fx.store.SetConfig(context.Background(), ConfigEndpoint, fx.server.URL); err != nil {
		t.Fatalf("set endpoint: %v", err)
	}
	fx.svc = NewService(fx.store, WithHTTPClient(fx.server.Client()))
	return fx
}

func successRegisterHandler(t *testing.T, deviceID string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"device_id":             deviceID,
			"token":                 "tok-" + deviceID,
			"recovery_code":         "rec-" + deviceID,
			"token_expires_at":      time.Now().Add(48 * time.Hour).Format(time.RFC3339),
			"poll_interval_seconds": 5,
			"referral_code":         "ref-" + deviceID,
			"share_text":            "share AIMA",
			"budget": map[string]any{
				"max_tasks": 10, "used_tasks": 0, "budget_usd": 1, "spent_usd": 0,
				"status": "active", "is_bound": false, "referral_count": 0,
			},
		})
	}
}

func TestBootstrap_FirstRegistration(t *testing.T) {
	t.Parallel()
	fx := newBootstrapFixture(t, successRegisterHandler(t, "dev-1"))

	res, err := fx.svc.Bootstrap(context.Background(), BootstrapOptions{InviteCode: "INVITE-123"})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if res.AlreadyRegistered {
		t.Errorf("expected AlreadyRegistered=false on first run")
	}
	if res.DeviceID != "dev-1" {
		t.Errorf("device_id = %q, want dev-1", res.DeviceID)
	}
	if fx.capturedInvite != "INVITE-123" {
		t.Errorf("server saw invite_code = %q, want INVITE-123", fx.capturedInvite)
	}

	// Canonical keys must be mirrored by saveState's mirrorCanonical hook.
	if got := fx.store.mustGet(cloud.ConfigDeviceID); got != "dev-1" {
		t.Errorf("canonical device.id = %q, want dev-1", got)
	}
	if got := fx.store.mustGet(cloud.ConfigDeviceToken); got != "tok-dev-1" {
		t.Errorf("canonical device.token = %q, want tok-dev-1", got)
	}
	if got := fx.store.mustGet(cloud.ConfigRegistrationState); got != cloud.StateRegistered {
		t.Errorf("registration_state = %q, want registered", got)
	}
}

func TestBootstrap_AlreadyRegisteredSkipsHTTP(t *testing.T) {
	t.Parallel()
	serverHit := false
	fx := newBootstrapFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		serverHit = true
		successRegisterHandler(t, "dev-2")(w, nil)
	})

	// Pre-populate a valid non-expiring state.
	ctx := context.Background()
	if err := fx.store.SetConfig(ctx, configStateDeviceID, "dev-existing"); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.SetConfig(ctx, configStateToken, "tok-existing"); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.SetConfig(ctx, configStateTokenExpiresAt, time.Now().Add(30*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	res, err := fx.svc.Bootstrap(ctx, BootstrapOptions{})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !res.AlreadyRegistered {
		t.Errorf("expected AlreadyRegistered=true")
	}
	if res.DeviceID != "dev-existing" {
		t.Errorf("device_id = %q, want dev-existing", res.DeviceID)
	}
	if serverHit {
		t.Errorf("Bootstrap should not hit server when already registered")
	}
}

func TestBootstrap_ForceReRegistration(t *testing.T) {
	t.Parallel()
	fx := newBootstrapFixture(t, successRegisterHandler(t, "dev-replaced"))

	ctx := context.Background()
	_ = fx.store.SetConfig(ctx, configStateDeviceID, "dev-old")
	_ = fx.store.SetConfig(ctx, configStateToken, "tok-old")
	_ = fx.store.SetConfig(ctx, configStateTokenExpiresAt, time.Now().Add(30*24*time.Hour).Format(time.RFC3339))

	res, err := fx.svc.Bootstrap(ctx, BootstrapOptions{InviteCode: "NEW", Force: true})
	if err != nil {
		t.Fatalf("Bootstrap(Force): %v", err)
	}
	if res.AlreadyRegistered {
		t.Errorf("Force should bypass already-registered check")
	}
	if res.DeviceID != "dev-replaced" {
		t.Errorf("device_id = %q, want dev-replaced", res.DeviceID)
	}
}

func TestBootstrap_FailureMarksFailedState(t *testing.T) {
	t.Parallel()
	fx := newBootstrapFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"server broke"}`, http.StatusInternalServerError)
	})

	_, err := fx.svc.Bootstrap(context.Background(), BootstrapOptions{InviteCode: "X"})
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
	if got := fx.store.mustGet(cloud.ConfigRegistrationState); got != cloud.StateFailed {
		t.Errorf("registration_state = %q, want failed", got)
	}
}

func TestBootstrap_ConflictSurfacesPromptError(t *testing.T) {
	t.Parallel()
	fx := newBootstrapFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":"saved recovery_code required for this hardware"}`))
	})

	_, err := fx.svc.Bootstrap(context.Background(), BootstrapOptions{InviteCode: "X"})
	var prompt *RegistrationPromptError
	if !errors.As(err, &prompt) {
		t.Fatalf("expected RegistrationPromptError, got %T: %v", err, err)
	}
	if prompt.Kind != RegistrationPromptRecovery {
		t.Errorf("prompt kind = %q, want %q", prompt.Kind, RegistrationPromptRecovery)
	}
}

// TestBootstrap_NoInviteLeavesUnregistered verifies offline-first: when no
// invite code is configured anywhere (opts, env, or config), Bootstrap must
// short-circuit with a RegistrationPromptError and leave registration_state
// as "unregistered" — NOT auto-register against a built-in default code
// (previous behavior that violated INV-8 offline-first).
func TestBootstrap_NoInviteLeavesUnregistered(t *testing.T) {
	// Cannot run in parallel: we rely on no env var leakage.
	serverHit := false
	fx := newBootstrapFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		serverHit = true
		http.Error(w, `{"detail":"should not be called"}`, http.StatusInternalServerError)
	})

	// Defensively unset any env-level invite sources — these tests may be run
	// in an environment that happens to have them set.
	t.Setenv(EnvInviteCode, "")
	t.Setenv("AIMA_SUPPORT_INVITE_CODE", "")

	_, err := fx.svc.Bootstrap(context.Background(), BootstrapOptions{})
	var prompt *RegistrationPromptError
	if !errors.As(err, &prompt) {
		t.Fatalf("expected RegistrationPromptError, got %T: %v", err, err)
	}
	if prompt.Kind != RegistrationPromptInviteOrWorker {
		t.Errorf("prompt kind = %q, want %q", prompt.Kind, RegistrationPromptInviteOrWorker)
	}
	if serverHit {
		t.Error("Bootstrap must not hit /self-register when no invite is configured")
	}

	got := fx.store.mustGet(cloud.ConfigRegistrationState)
	if got == cloud.StateRegistered {
		t.Errorf("registration_state = %q, must not be registered without an invite", got)
	}
	if got == cloud.StateFailed {
		t.Errorf("registration_state = %q, should stay unregistered (missing config, not failed attempt)", got)
	}
}

func TestBootstrap_InviteCodeFromEnvVar(t *testing.T) {
	// Cannot run in parallel: t.Setenv is incompatible with t.Parallel.
	fx := newBootstrapFixture(t, successRegisterHandler(t, "dev-env"))

	t.Setenv(EnvInviteCode, "FROM-ENV")

	res, err := fx.svc.Bootstrap(context.Background(), BootstrapOptions{})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if res.DeviceID != "dev-env" {
		t.Errorf("device_id = %q, want dev-env", res.DeviceID)
	}
	if fx.capturedInvite != "FROM-ENV" {
		t.Errorf("server saw invite_code = %q, want FROM-ENV", fx.capturedInvite)
	}
}

func TestStartRegistrationWorker_SucceedsFirstTry(t *testing.T) {
	t.Parallel()
	fx := newBootstrapFixture(t, successRegisterHandler(t, "dev-worker"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		fx.svc.StartRegistrationWorker(ctx, BootstrapOptions{InviteCode: "INV"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish within 2s on successful first try")
	}
	if got := fx.store.mustGet(cloud.ConfigDeviceID); got != "dev-worker" {
		t.Errorf("canonical device.id = %q, want dev-worker", got)
	}
}

func TestStartRegistrationWorker_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	var attempts int
	fx := newBootstrapFixture(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, `{"detail":"upstream flaky"}`, http.StatusBadGateway)
			return
		}
		successRegisterHandler(t, "dev-flaky")(w, r)
	})

	// Temporarily shrink the backoff so the test completes quickly.
	origMin := minRegistrationBackoffForTest()
	setMinRegistrationBackoffForTest(10 * time.Millisecond)
	defer setMinRegistrationBackoffForTest(origMin)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		fx.svc.StartRegistrationWorker(ctx, BootstrapOptions{InviteCode: "INV"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("worker hung; attempts=%d", attempts)
	}
	if attempts < 3 {
		t.Errorf("expected >=3 attempts, got %d", attempts)
	}
	if got := fx.store.mustGet(cloud.ConfigDeviceID); got != "dev-flaky" {
		t.Errorf("canonical device.id = %q, want dev-flaky", got)
	}
}

func TestStartRegistrationWorker_StopsOnPromptError(t *testing.T) {
	t.Parallel()
	var attempts int
	fx := newBootstrapFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"invalid invite code"}`))
	})

	origMin := minRegistrationBackoffForTest()
	setMinRegistrationBackoffForTest(10 * time.Millisecond)
	defer setMinRegistrationBackoffForTest(origMin)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		fx.svc.StartRegistrationWorker(ctx, BootstrapOptions{InviteCode: "BAD"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker should exit immediately on prompt error, not keep retrying")
	}
	if attempts != 1 {
		t.Errorf("expected exactly 1 attempt on prompt error, got %d", attempts)
	}
}

func TestStartRegistrationWorker_ExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	fx := newBootstrapFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"server-down"}`, http.StatusInternalServerError)
	})

	origMin := minRegistrationBackoffForTest()
	setMinRegistrationBackoffForTest(50 * time.Millisecond)
	defer setMinRegistrationBackoffForTest(origMin)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		fx.svc.StartRegistrationWorker(ctx, BootstrapOptions{InviteCode: "X"})
		close(done)
	}()

	// Let it fail at least once, then cancel.
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("worker did not exit within 1s of ctx cancellation")
	}
}

func TestTokenExpiringSoon(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty=valid", "", false},
		{"malformed=valid", "not-a-time", false},
		{"10-days=valid", now.Add(10 * 24 * time.Hour).Format(time.RFC3339), false},
		{"3-days=expiring", now.Add(3 * 24 * time.Hour).Format(time.RFC3339), true},
		{"past=expiring", now.Add(-1 * time.Hour).Format(time.RFC3339), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenExpiringSoon(deviceState{TokenExpiresAt: tc.in}, now)
			if got != tc.want {
				t.Errorf("tokenExpiringSoon(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

