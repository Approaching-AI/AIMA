package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jguan/aima/internal/mcp"
)

func TestAutomationToolAdapterAllowsDeployDelete(t *testing.T) {
	server := mcp.NewServer()
	calls := 0
	server.RegisterTool(&mcp.Tool{
		Name:        "deploy.delete",
		Description: "delete deployment",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*mcp.ToolResult, error) {
			calls++
			return mcp.TextResult("deleted"), nil
		},
	})

	base := &mcpToolAdapter{
		server:  server,
		pending: make(map[int64]*pendingApproval),
	}

	blocked, err := base.ExecuteTool(context.Background(), "deploy.delete", json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("base ExecuteTool: %v", err)
	}
	if !blocked.IsError {
		t.Fatal("expected base adapter to block deploy.delete")
	}
	if calls != 0 {
		t.Fatalf("deploy.delete calls = %d, want 0 before automation bypass", calls)
	}

	automation := &automationToolAdapter{base: base}
	result, err := automation.ExecuteTool(context.Background(), "deploy.delete", json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("automation ExecuteTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("automation result unexpectedly blocked: %s", result.Content)
	}
	if calls != 1 {
		t.Fatalf("deploy.delete calls = %d, want 1 after automation bypass", calls)
	}
}
