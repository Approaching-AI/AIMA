package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/mcp"
)

// TestHandleOnboardingDeploy_DoesNotCompleteOnTimeout is the HTTP handler
// end-to-end check: a timed-out deploy must NOT emit deploy_complete, and
// must emit an error event so the wizard UI can surface the failure.
//
// The decorator-level unit tests that previously lived in this file have
// been moved to internal/onboarding/deploy_test.go (TestRunDeploy_*) because
// the buildOnboardingDeps decorator was retired; equivalent side effects
// now live inside onboarding.RunDeploy.
func TestHandleOnboardingDeploy_DoesNotCompleteOnTimeout(t *testing.T) {
	deps := &mcp.ToolDeps{
		DeployRun: func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
			onPhase func(string, string), onEngineProgress func(engine.ProgressEvent), onModelProgress func(int64, int64),
		) (json.RawMessage, error) {
			return json.RawMessage(`{"status":"timeout","message":"deployment started but not ready within 10 minutes"}`), nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/ui/api/onboarding-deploy", strings.NewReader(`{"model":"qwen3-8b"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()

	handleOnboardingDeploy(&appContext{}, deps).ServeHTTP(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "event: deploy_complete") {
		t.Fatalf("unexpected deploy_complete event in response: %s", body)
	}
	if !strings.Contains(body, "event: error") {
		t.Fatalf("expected error event in response: %s", body)
	}
}
