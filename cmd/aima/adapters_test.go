package main

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
)

func TestToEngineBinarySourceUsesVerifiedPreinstalledProbeWithoutArchivePin(t *testing.T) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	probePath := "/verified/managed/bin/aima-engine"
	source := &knowledge.EngineSource{
		Binary:      "aima-engine",
		Platforms:   []string{platform},
		InstallType: "preinstalled",
		Probe:       &knowledge.EngineSourceProbe{Paths: []string{probePath}},
		SHA256:      map[string]string{platform: "archive-sha256"},
	}

	got := toEngineBinarySource(source)
	if got == nil {
		t.Fatal("toEngineBinarySource returned nil")
	}
	if len(got.ProbePaths) != 1 || got.ProbePaths[0] != probePath {
		t.Fatalf("probe paths = %v, want [%s]", got.ProbePaths, probePath)
	}
	if len(got.SHA256) != 0 {
		t.Fatalf("preinstalled runtime source retained archive SHA-256: %v", got.SHA256)
	}
	if source.SHA256[platform] != "archive-sha256" {
		t.Fatalf("catalog source SHA-256 was mutated: %v", source.SHA256)
	}
}

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
