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

func TestHandleOnboardingInit_CompletesWhenAutoInitIsAllowed(t *testing.T) {
	origDetect := detectOnboardingInitCapability
	detectOnboardingInitCapability = func(deps *mcp.ToolDeps) (bool, string) {
		return true, ""
	}
	defer func() {
		detectOnboardingInitCapability = origDetect
	}()

	var scanned bool
	deps := &mcp.ToolDeps{
		StackStatus: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"components":[{"name":"docker","ready":false},{"name":"k3s","ready":false}],"all_ready":false}`), nil
		},
		StackPreflight: func(ctx context.Context, tier string) (json.RawMessage, error) {
			return json.RawMessage(`[]`), nil
		},
		StackInit: func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error) {
			return json.RawMessage(`{"components":[{"name":"docker","ready":true,"message":"ready"}],"all_ready":true}`), nil
		},
		ScanEngines: func(ctx context.Context, runtimeFilter string, autoImport bool) (json.RawMessage, error) {
			scanned = autoImport
			return json.RawMessage(`[]`), nil
		},
	}
	buildOnboardingDeps(&appContext{}, deps)

	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-init", strings.NewReader(`{"tier":"k3s","allow_download":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	handleOnboardingInit(&appContext{}, deps).ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "event: init_complete") {
		t.Fatalf("expected init_complete event in response: %s", body)
	}
	if !strings.Contains(body, "event: init_component") {
		t.Fatalf("expected init_component event in response: %s", body)
	}
	if !scanned {
		t.Fatal("expected k3s init path to refresh engines with autoImport=true")
	}
}

func TestHandleOnboardingInit_RejectsWhenAutoInitIsBlocked(t *testing.T) {
	origDetect := detectOnboardingInitCapability
	detectOnboardingInitCapability = func(deps *mcp.ToolDeps) (bool, string) {
		return false, "need privileged helper"
	}
	defer func() {
		detectOnboardingInitCapability = origDetect
	}()

	deps := &mcp.ToolDeps{
		StackStatus: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"components":[{"name":"docker","ready":false},{"name":"k3s","ready":false}],"all_ready":false}`), nil
		},
		StackPreflight: func(ctx context.Context, tier string) (json.RawMessage, error) {
			return json.RawMessage(`[]`), nil
		},
		StackInit: func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error) {
			return json.RawMessage(`{"components":[],"all_ready":true}`), nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-init", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	handleOnboardingInit(&appContext{}, deps).ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("expected error event in response: %s", body)
	}
	if strings.Contains(body, "event: init_complete") {
		t.Fatalf("unexpected init_complete event in response: %s", body)
	}
}
