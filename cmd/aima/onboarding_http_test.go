package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/proxy"
)

func TestRequireOnboardingMutation_AllowsSameOriginJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-init", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	if ok := requireOnboardingMutation(&appContext{}, rr, req); !ok {
		t.Fatalf("expected request to pass gate, status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestRequireOnboardingMutation_RejectsCrossOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-init", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	rr := httptest.NewRecorder()

	if ok := requireOnboardingMutation(&appContext{}, rr, req); ok {
		t.Fatal("expected cross-origin request to be rejected")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestRequireOnboardingMutation_RejectsNonJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-init", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	if ok := requireOnboardingMutation(&appContext{}, rr, req); ok {
		t.Fatal("expected non-JSON request to be rejected")
	}
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnsupportedMediaType)
	}
}

func TestRequireOnboardingMutation_RequiresAPIKeyWhenConfigured(t *testing.T) {
	srv := proxy.NewServer()
	srv.SetAPIKey("secret")
	ac := &appContext{proxy: srv}

	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-init", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	if ok := requireOnboardingMutation(ac, rr, req); ok {
		t.Fatal("expected request without bearer token to be rejected")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-init", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()

	if ok := requireOnboardingMutation(ac, rr, req); !ok {
		t.Fatalf("expected request with bearer token to pass gate, status=%d body=%q", rr.Code, rr.Body.String())
	}
}
