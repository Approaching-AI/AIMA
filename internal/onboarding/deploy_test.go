package onboarding

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/mcp"
)

// TestRunDeploy_MarksCompletedAfterReadyDeploy ports the behavioral assertion
// previously held by cmd/aima/onboarding_deploy_test.go:
// TestBuildOnboardingDeps_MarksCompletedAfterReadyDeploy. The decorator that
// bolted the SetConfig("onboarding_completed","true") side effect onto
// DeployRun has been retired; the same side effect is now inlined in
// onboarding.RunDeploy. This test pins that behavior.
func TestRunDeploy_MarksCompletedAfterReadyDeploy(t *testing.T) {
	var setCalls int
	var gotKey, gotValue string

	td := &mcp.ToolDeps{
		DeployRun: func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
			onPhase func(string, string), onEngineProgress func(engine.ProgressEvent), onModelProgress func(int64, int64),
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

	deps := &Deps{ToolDeps: td}

	if _, _, err := RunDeploy(context.Background(), deps, "qwen3-8b", "", "", nil, false, nil); err != nil {
		t.Fatalf("RunDeploy: %v", err)
	}
	if setCalls != 1 {
		t.Fatalf("SetConfig call count = %d, want 1", setCalls)
	}
	if gotKey != "onboarding_completed" || gotValue != "true" {
		t.Fatalf("SetConfig(%q, %q), want onboarding_completed=true", gotKey, gotValue)
	}
}

// TestRunDeploy_DoesNotMarkCompletedOnTimeout ports the behavioral assertion
// previously held by cmd/aima/onboarding_deploy_test.go:
// TestBuildOnboardingDeps_DoesNotMarkCompletedOnTimeout. A non-ready deploy
// outcome must NOT flip the onboarding_completed flag — otherwise a timed-out
// deploy would falsely mark the wizard as complete.
func TestRunDeploy_DoesNotMarkCompletedOnTimeout(t *testing.T) {
	var setCalls int

	td := &mcp.ToolDeps{
		DeployRun: func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
			onPhase func(string, string), onEngineProgress func(engine.ProgressEvent), onModelProgress func(int64, int64),
		) (json.RawMessage, error) {
			return json.RawMessage(`{"status":"timeout","message":"deployment started but not ready within 10 minutes"}`), nil
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			setCalls++
			return nil
		},
	}

	deps := &Deps{ToolDeps: td}

	// RunDeploy surfaces the non-ready status as an error (failureMessage()).
	// The test cares only that SetConfig was not called.
	if _, _, err := RunDeploy(context.Background(), deps, "qwen3-8b", "", "", nil, false, nil); err == nil {
		t.Fatalf("RunDeploy on timeout status: expected error, got nil")
	}
	if setCalls != 0 {
		t.Fatalf("SetConfig call count = %d, want 0", setCalls)
	}
}
