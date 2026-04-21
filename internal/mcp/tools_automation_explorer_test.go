package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestExploreListRuns_DispatchesWithParams verifies list_runs forwards the raw
// JSON params so the dep can see status/kind/limit filters.
func TestExploreListRuns_DispatchesWithParams(t *testing.T) {
	s := NewServer()
	var got json.RawMessage
	registerAutomationTools(s, &ToolDeps{
		ExploreListRuns: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			got = params
			return json.RawMessage(`[{"id":"r1"}]`), nil
		},
	})

	body := `{"action":"list_runs","status":"running","kind":"validate","limit":10}`
	result, err := s.ExecuteTool(context.Background(), "explore", json.RawMessage(body))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result)
	}
	if len(got) == 0 {
		t.Fatal("expected params to be forwarded to dep")
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal forwarded params: %v", err)
	}
	if parsed["status"] != "running" {
		t.Errorf("status forwarded = %v, want running", parsed["status"])
	}
}

// TestExploreNewActions_NilDep_ReturnsErrorResult ensures every new read-only
// action fails gracefully (not panic, not 500) when its dep is unwired.
func TestExploreNewActions_NilDep_ReturnsErrorResult(t *testing.T) {
	s := NewServer()
	registerAutomationTools(s, &ToolDeps{}) // all deps nil

	cases := []struct {
		action string
		body   string
	}{
		{"list_runs", `{"action":"list_runs"}`},
		{"get_run_detail", `{"action":"get_run_detail","id":"r1"}`},
		{"get_run_events", `{"action":"get_run_events","id":"r1"}`},
		{"get_workspace_doc", `{"action":"get_workspace_doc","doc":"plan.md"}`},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			result, err := s.ExecuteTool(context.Background(), "explore", json.RawMessage(tc.body))
			if err != nil {
				t.Fatalf("ExecuteTool: %v", err)
			}
			if !result.IsError {
				t.Fatalf("expected IsError=true for nil dep, got %+v", result)
			}
			if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "not implemented") {
				t.Errorf("expected 'not implemented' message, got %+v", result.Content)
			}
		})
	}
}

// TestExploreNewActions_RequiredParamChecks verifies boundary validation for
// each action that requires an id or doc field before reaching its dep.
func TestExploreNewActions_RequiredParamChecks(t *testing.T) {
	s := NewServer()
	registerAutomationTools(s, &ToolDeps{
		// Non-nil deps so the failure clearly comes from the param check,
		// not from dep==nil.
		ExploreRunDetail: func(ctx context.Context, runID string) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
		ExploreRunEvents: func(ctx context.Context, runID string) (json.RawMessage, error) {
			return json.RawMessage(`[]`), nil
		},
		ExploreWorkspaceDoc: func(ctx context.Context, doc string) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	})

	cases := []struct {
		name            string
		body            string
		wantErrContains string
	}{
		{"get_run_detail_missing_id", `{"action":"get_run_detail"}`, "id is required"},
		{"get_run_events_missing_id", `{"action":"get_run_events"}`, "id is required"},
		{"get_workspace_doc_missing_doc", `{"action":"get_workspace_doc"}`, "doc is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.ExecuteTool(context.Background(), "explore", json.RawMessage(tc.body))
			if err != nil {
				t.Fatalf("ExecuteTool: %v", err)
			}
			if !result.IsError {
				t.Fatalf("expected IsError=true")
			}
			if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, tc.wantErrContains) {
				t.Errorf("error text = %v; want to contain %q", result.Content, tc.wantErrContains)
			}
		})
	}
}

// TestExploreGetWorkspaceDoc_ForwardsDocName ensures the doc name reaches the
// dep verbatim (the whitelist check is in the dep, not the handler).
func TestExploreGetWorkspaceDoc_ForwardsDocName(t *testing.T) {
	s := NewServer()
	var gotDoc string
	registerAutomationTools(s, &ToolDeps{
		ExploreWorkspaceDoc: func(ctx context.Context, doc string) (json.RawMessage, error) {
			gotDoc = doc
			return json.RawMessage(fmt.Sprintf(`{"doc":%q,"exists":false}`, doc)), nil
		},
	})

	result, err := s.ExecuteTool(context.Background(), "explore", json.RawMessage(`{"action":"get_workspace_doc","doc":"plan.md"}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result)
	}
	if gotDoc != "plan.md" {
		t.Errorf("doc forwarded = %q, want plan.md", gotDoc)
	}
}

// TestExplorerDbDeltas_ForwardsSince verifies the since timestamp is passed
// through as a string so the dep can parse/interpret it.
func TestExplorerDbDeltas_ForwardsSince(t *testing.T) {
	s := NewServer()
	var gotSince string
	registerAutomationTools(s, &ToolDeps{
		ExplorerDbDeltas: func(ctx context.Context, since string) (json.RawMessage, error) {
			gotSince = since
			return json.RawMessage(`{"configurations":0,"benchmark_results":0,"exploration_events":0}`), nil
		},
	})

	result, err := s.ExecuteTool(context.Background(), "explorer", json.RawMessage(`{"action":"db_deltas","since":"2026-04-21T10:00:00Z"}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result)
	}
	if gotSince != "2026-04-21T10:00:00Z" {
		t.Errorf("since = %q, want 2026-04-21T10:00:00Z", gotSince)
	}
}

// TestExplorerDbDeltas_NilDep verifies graceful degradation on the explorer tool.
func TestExplorerDbDeltas_NilDep(t *testing.T) {
	s := NewServer()
	registerAutomationTools(s, &ToolDeps{}) // ExplorerDbDeltas nil

	result, err := s.ExecuteTool(context.Background(), "explorer", json.RawMessage(`{"action":"db_deltas"}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for nil dep")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "not implemented") {
		t.Errorf("expected 'not implemented' message, got %+v", result.Content)
	}
}
