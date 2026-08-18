package support

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEnsureRegisteredRefreshesExpiredTokenWithIdentitySession(t *testing.T) {
	t.Parallel()

	publicPEM, privatePEM, err := generateIdentityKeyPEM()
	if err != nil {
		t.Fatalf("generate identity key: %v", err)
	}

	store := newMemoryStore()
	ctx := context.Background()
	_ = store.SetConfig(ctx, configStateDeviceID, "dev-identity")
	_ = store.SetConfig(ctx, configStateToken, "expired-token")
	_ = store.SetConfig(ctx, configStateTokenExpiresAt, time.Now().Add(-time.Hour).Format(time.RFC3339))
	_ = store.SetConfig(ctx, configIdentityDeviceID, "dev-identity")
	_ = store.SetConfig(ctx, configIdentityKeyID, "dkey-1")
	_ = store.SetConfig(ctx, configIdentityPrivateKeyPEM, privatePEM)
	_ = store.SetConfig(ctx, configIdentityPublicKeyPEM, publicPEM)

	var selfRegisterHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/dev-identity/active-task", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh-session" {
			http.Error(w, `{"detail":"token expired"}`, http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, map[string]any{"has_active_task": false})
	})
	mux.HandleFunc("/api/v1/devices/identity/challenge", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode challenge request: %v", err)
		}
		if body["device_id"] != "dev-identity" || body["key_id"] != "dkey-1" || body["purpose"] != "session" {
			t.Fatalf("unexpected challenge request: %+v", body)
		}
		writeJSON(t, w, map[string]any{
			"challenge_id":             "chal-1",
			"device_id":                "dev-identity",
			"key_id":                   "dkey-1",
			"nonce":                    "nonce-1234567890",
			"expires_at":               time.Now().Add(5 * time.Minute).Format(time.RFC3339),
			"canonicalization_version": deviceIdentityCanonicalV1,
		})
	})
	mux.HandleFunc("/api/v1/devices/identity/session", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode session request: %v", err)
		}
		assertSessionProofRequest(t, body, "dev-identity", "dkey-1", "chal-1", "nonce-1234567890")
		writeJSON(t, w, map[string]any{
			"token":                             "fresh-session",
			"token_expires_at":                  time.Now().Add(time.Hour).Format(time.RFC3339),
			"device_id":                         "dev-identity",
			"key_id":                            "dkey-1",
			"assurance_level":                   "A0",
			"token_kind":                        "session_ticket",
			"token_persistence":                 "session_only",
			"persistent_token_fallback_enabled": false,
		})
	})
	mux.HandleFunc("/api/v1/devices/self-register", func(w http.ResponseWriter, r *http.Request) {
		selfRegisterHit = true
		http.Error(w, `{"detail":"self-register should not be needed"}`, http.StatusConflict)
	})

	server := httptest.NewServer(mux)
	defer server.Close()
	_ = store.SetConfig(ctx, ConfigEndpoint, server.URL)

	svc := NewService(store, WithHTTPClient(server.Client()))
	result, err := svc.AskForHelp(ctx, AskRequest{})
	if err != nil {
		t.Fatalf("AskForHelp: %v", err)
	}
	if result.DeviceID != "dev-identity" {
		t.Fatalf("device_id = %q, want dev-identity", result.DeviceID)
	}
	if selfRegisterHit {
		t.Fatal("expired token with local identity key should refresh by identity session, not self-register")
	}
	if got := store.mustGet(configStateToken); got != "fresh-session" {
		t.Fatalf("stored token = %q, want fresh-session", got)
	}
	if got := store.mustGet(configStateTokenPersistence); got != "session_only" {
		t.Fatalf("token_persistence = %q, want session_only", got)
	}
}

func TestEnsureRegisteredEnrollsIdentityForBootstrapOnlyToken(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	ctx := context.Background()

	var enrollHit bool
	var selfRegisterHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/self-register", func(w http.ResponseWriter, r *http.Request) {
		selfRegisterHit = true
		writeJSON(t, w, map[string]any{
			"device_id":                         "dev-new",
			"token":                             "bootstrap-token",
			"recovery_code":                     "rec-new",
			"token_expires_at":                  time.Now().Add(15 * time.Minute).Format(time.RFC3339),
			"token_kind":                        "bootstrap",
			"token_persistence":                 "bootstrap_only",
			"persistent_token_fallback_enabled": false,
			"poll_interval_seconds":             5,
		})
	})
	mux.HandleFunc("/api/v1/devices/identity/enroll", func(w http.ResponseWriter, r *http.Request) {
		enrollHit = true
		if r.Header.Get("Authorization") != "Bearer bootstrap-token" {
			t.Fatalf("enroll Authorization = %q, want bootstrap token", r.Header.Get("Authorization"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode enroll request: %v", err)
		}
		if body["device_id"] != "dev-new" || !strings.Contains(body["public_key_pem"], "PUBLIC KEY") {
			t.Fatalf("unexpected enroll request: %+v", body)
		}
		writeJSON(t, w, map[string]any{
			"key_id":          "dkey-new",
			"device_id":       "dev-new",
			"algorithm":       deviceIdentityAlgorithm,
			"storage_class":   deviceIdentityStorageClass,
			"assurance_level": "A0",
			"status":          "active",
		})
	})
	mux.HandleFunc("/api/v1/devices/identity/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"challenge_id":             "chal-new",
			"device_id":                "dev-new",
			"key_id":                   "dkey-new",
			"nonce":                    "nonce-new-1234567890",
			"expires_at":               time.Now().Add(5 * time.Minute).Format(time.RFC3339),
			"canonicalization_version": deviceIdentityCanonicalV1,
		})
	})
	mux.HandleFunc("/api/v1/devices/identity/session", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode session request: %v", err)
		}
		assertSessionProofRequest(t, body, "dev-new", "dkey-new", "chal-new", "nonce-new-1234567890")
		writeJSON(t, w, map[string]any{
			"token":                             "session-token",
			"token_expires_at":                  time.Now().Add(time.Hour).Format(time.RFC3339),
			"device_id":                         "dev-new",
			"key_id":                            "dkey-new",
			"assurance_level":                   "A0",
			"token_kind":                        "session_ticket",
			"token_persistence":                 "session_only",
			"persistent_token_fallback_enabled": false,
		})
	})
	mux.HandleFunc("/api/v1/devices/dev-new/active-task", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session-token" {
			http.Error(w, `{"detail":"unexpected token"}`, http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, map[string]any{"has_active_task": false})
	})

	server := httptest.NewServer(mux)
	defer server.Close()
	_ = store.SetConfig(ctx, ConfigEndpoint, server.URL)

	svc := NewService(store, WithHTTPClient(server.Client()))
	result, err := svc.AskForHelp(ctx, AskRequest{InviteCode: "invite-123"})
	if err != nil {
		t.Fatalf("AskForHelp: %v", err)
	}
	if !selfRegisterHit || !enrollHit {
		t.Fatalf("selfRegisterHit=%v enrollHit=%v, want both true", selfRegisterHit, enrollHit)
	}
	if result.DeviceID != "dev-new" {
		t.Fatalf("device_id = %q, want dev-new", result.DeviceID)
	}
	if got := store.mustGet(configIdentityKeyID); got != "dkey-new" {
		t.Fatalf("identity key id = %q, want dkey-new", got)
	}
	if got := store.mustGet(configStateToken); got != "session-token" {
		t.Fatalf("stored token = %q, want session-token", got)
	}
	if got := store.mustGet(configStateTokenPersistence); got != "session_only" {
		t.Fatalf("token_persistence = %q, want session_only", got)
	}
}

func TestClassifyRegistrationErrorBrowserConfirmation(t *testing.T) {
	t.Parallel()

	err := classifyRegistrationError(newHTTPStatusError(http.StatusConflict, []byte(`{
		"detail":"browser confirmation required to recover existing device credentials",
		"reauth_method":"browser_confirmation",
		"device_id":"dev-bound",
		"user_code":"ABCD-EFGH",
		"device_code":"device-flow-code",
		"verification_uri":"https://aimaserver.com/device",
		"verification_uri_complete":"https://aimaserver.com/device?user_code=ABCD-EFGH",
		"expires_in":900,
		"interval":5
	}`)))
	var browser *BrowserConfirmationError
	if !errors.As(err, &browser) {
		t.Fatalf("expected BrowserConfirmationError, got %T: %v", err, err)
	}
	if browser.UserCode != "ABCD-EFGH" || browser.VerificationURIComplete == "" {
		t.Fatalf("unexpected browser confirmation payload: %+v", browser)
	}
	if !strings.Contains(browser.Error(), "ABCD-EFGH") {
		t.Fatalf("error should include user code or complete URL, got %q", browser.Error())
	}
}

func TestClassifyRegistrationErrorHardwareIdentityConflict(t *testing.T) {
	t.Parallel()

	err := classifyRegistrationError(newHTTPStatusError(http.StatusConflict, []byte(`{
		"detail":"hardware identity conflict: cloned machine-id detected",
		"reauth_method":"hardware_identity_conflict",
		"device_id":"dev-existing",
		"existing_hostname":"mt-AIBOOK-ABA14104",
		"incoming_hostname":"devtech-AIBOOK-ABA14102"
	}`)))
	var prompt *RegistrationPromptError
	if !errors.As(err, &prompt) {
		t.Fatalf("expected RegistrationPromptError, got %T: %v", err, err)
	}
	if prompt.Kind != RegistrationPromptHardwareIdentity {
		t.Fatalf("prompt kind = %q, want %q", prompt.Kind, RegistrationPromptHardwareIdentity)
	}
	if !strings.Contains(prompt.Error(), "hardware identity conflict") {
		t.Fatalf("error should explain hardware identity conflict, got %q", prompt.Error())
	}
}

func assertSessionProofRequest(t *testing.T, body map[string]string, deviceID, keyID, challengeID, nonce string) {
	t.Helper()
	if body["device_id"] != deviceID || body["key_id"] != keyID || body["challenge_id"] != challengeID || body["nonce"] != nonce {
		t.Fatalf("unexpected identity session request: %+v", body)
	}
	if body["method"] != deviceIdentitySessionMethod || body["path"] != deviceIdentitySessionPath || body["body_sha256"] != deviceIdentityEmptyBodySHA256 {
		t.Fatalf("unexpected identity proof target: %+v", body)
	}
	if _, err := base64.StdEncoding.DecodeString(body["signature"]); err != nil {
		t.Fatalf("signature is not base64: %v", err)
	}
}
