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

func TestBuildOnboardingDeps_MarksCompletedAfterReadyDeploy(t *testing.T) {
	var setCalls int
	var gotKey, gotValue string

	deps := &mcp.ToolDeps{
		DeployRun: func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
			onPhase func(string, string), onEngineProgress func(engine.ProgressEvent),
		) (json.RawMessage, error) {
			return json.RawMessage(`{"status":"ready","address":"127.0.0.1:6188"}`), nil
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			setCalls++
			gotKey = key
			gotValue = value
			return nil
		},
	}

	buildOnboardingDeps(&appContext{}, deps)

	if _, err := deps.DeployRun(context.Background(), "qwen3-8b", "", "", nil, false, nil, nil); err != nil {
		t.Fatalf("DeployRun: %v", err)
	}
	if setCalls != 1 {
		t.Fatalf("SetConfig call count = %d, want 1", setCalls)
	}
	if gotKey != "onboarding_completed" || gotValue != "true" {
		t.Fatalf("SetConfig(%q, %q), want onboarding_completed=true", gotKey, gotValue)
	}
}

func TestBuildOnboardingDeps_DoesNotMarkCompletedOnTimeout(t *testing.T) {
	var setCalls int

	deps := &mcp.ToolDeps{
		DeployRun: func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
			onPhase func(string, string), onEngineProgress func(engine.ProgressEvent),
		) (json.RawMessage, error) {
			return json.RawMessage(`{"status":"timeout","message":"deployment started but not ready within 10 minutes"}`), nil
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			setCalls++
			return nil
		},
	}

	buildOnboardingDeps(&appContext{}, deps)

	if _, err := deps.DeployRun(context.Background(), "qwen3-8b", "", "", nil, false, nil, nil); err != nil {
		t.Fatalf("DeployRun: %v", err)
	}
	if setCalls != 0 {
		t.Fatalf("SetConfig call count = %d, want 0", setCalls)
	}
}

func TestBuildOnboardingDeps_ImportsEnginesAfterReadyK3SInit(t *testing.T) {
	var scanCalls int
	deps := &mcp.ToolDeps{
		StackInit: func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error) {
			return json.RawMessage(`{"components":[{"name":"docker","ready":true,"message":"ready"}],"all_ready":true}`), nil
		},
		ScanEngines: func(ctx context.Context, runtimeFilter string, autoImport bool) (json.RawMessage, error) {
			if runtimeFilter != "auto" || !autoImport {
				t.Fatalf("ScanEngines(%q, %v), want autoImport on auto runtime", runtimeFilter, autoImport)
			}
			scanCalls++
			return json.RawMessage(`[]`), nil
		},
	}

	buildOnboardingDeps(&appContext{}, deps)

	raw, err := deps.StackInit(context.Background(), "k3s", true)
	if err != nil {
		t.Fatalf("StackInit: %v", err)
	}
	if scanCalls != 1 {
		t.Fatalf("ScanEngines call count = %d, want 1", scanCalls)
	}

	var resp struct {
		AllReady              bool `json:"all_ready"`
		EngineImportTriggered bool `json:"engine_import_triggered"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal wrapped stack init result: %v", err)
	}
	if !resp.AllReady || !resp.EngineImportTriggered {
		t.Fatalf("wrapped stack init result = %+v, want all_ready=true and engine_import_triggered=true", resp)
	}
}

func TestHandleOnboardingDeploy_DoesNotCompleteOnTimeout(t *testing.T) {
	deps := &mcp.ToolDeps{
		DeployRun: func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
			onPhase func(string, string), onEngineProgress func(engine.ProgressEvent),
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
