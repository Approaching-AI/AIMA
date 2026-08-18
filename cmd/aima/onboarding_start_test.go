package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/mcp"
)

func TestHandleOnboardingStartDispatchesToolDeps(t *testing.T) {
	t.Parallel()

	var gotLocale string
	deps := &mcp.ToolDeps{
		OnboardingStart: func(ctx context.Context, locale string) (json.RawMessage, error) {
			gotLocale = locale
			return json.RawMessage(`{"next_model":"qwen3-4b","next_command":"aima run qwen3-4b"}`), nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-start", strings.NewReader(`{"locale":"zh"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	handleOnboardingStart(&appContext{}, deps).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if gotLocale != "zh" {
		t.Fatalf("locale = %q, want zh", gotLocale)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if !strings.Contains(rr.Body.String(), `"next_command":"aima run qwen3-4b"`) {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}

func TestHandleOnboardingStartRequiresToolDep(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-start", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	handleOnboardingStart(&appContext{}, &mcp.ToolDeps{}).ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}
