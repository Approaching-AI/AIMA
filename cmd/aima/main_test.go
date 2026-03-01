package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/mcp"
)

func TestValidateOverlayAssetName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid simple", input: "qwen3-8b", wantErr: false},
		{name: "valid dotted", input: "qwen3.5-35b-a3b", wantErr: false},
		{name: "valid underscore", input: "vllm_rocm", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "path traversal", input: "../evil", wantErr: true},
		{name: "slash", input: "models/evil", wantErr: true},
		{name: "backslash", input: `models\evil`, wantErr: true},
		{name: "absolute path", input: "/tmp/evil", wantErr: true},
		{name: "invalid chars", input: "evil;rm -rf", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOverlayAssetName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateOverlayAssetName(%q) error = %v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestIsBlockedAgentTool(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		args      json.RawMessage
		wantBlock bool
	}{
		{name: "blocked static", tool: "shell.exec", args: json.RawMessage(`{"command":"whoami"}`), wantBlock: true},
		{name: "allowed readonly", tool: "knowledge.resolve", args: json.RawMessage(`{"model":"qwen3-8b"}`), wantBlock: false},
		{name: "system config read allowed", tool: "system.config", args: json.RawMessage(`{"key":"foo"}`), wantBlock: false},
		{name: "system config write blocked", tool: "system.config", args: json.RawMessage(`{"key":"foo","value":"bar"}`), wantBlock: true},
		{name: "system config null value blocked", tool: "system.config", args: json.RawMessage(`{"key":"foo","value":null}`), wantBlock: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, _ := isBlockedAgentTool(tt.tool, tt.args)
			if blocked != tt.wantBlock {
				t.Fatalf("isBlockedAgentTool(%q) = %v, want %v", tt.tool, blocked, tt.wantBlock)
			}
		})
	}
}

func TestMCPToolAdapter_BlocksHighRiskTool(t *testing.T) {
	s := mcp.NewServer()
	called := 0
	s.RegisterTool(&mcp.Tool{
		Name:        "shell.exec",
		Description: "test shell",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*mcp.ToolResult, error) {
			called++
			return mcp.TextResult("should not run"), nil
		},
	})

	adapter := &mcpToolAdapter{server: s}
	result, err := adapter.ExecuteTool(context.Background(), "shell.exec", json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected blocked tool result to be an error")
	}
	if !strings.Contains(result.Content, "BLOCKED") {
		t.Fatalf("expected BLOCKED message, got %q", result.Content)
	}
	if called != 0 {
		t.Fatalf("blocked tool should not execute, called=%d", called)
	}
}

func TestMCPToolAdapter_SystemConfigReadAllowedWriteBlocked(t *testing.T) {
	s := mcp.NewServer()
	called := 0
	s.RegisterTool(&mcp.Tool{
		Name:        "system.config",
		Description: "test config",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*mcp.ToolResult, error) {
			called++
			return mcp.TextResult("value"), nil
		},
	})

	adapter := &mcpToolAdapter{server: s}

	readResult, err := adapter.ExecuteTool(context.Background(), "system.config", json.RawMessage(`{"key":"foo"}`))
	if err != nil {
		t.Fatalf("read ExecuteTool: %v", err)
	}
	if readResult.IsError || readResult.Content != "value" {
		t.Fatalf("expected successful read result, got %+v", readResult)
	}
	if called != 1 {
		t.Fatalf("expected read call to execute tool once, called=%d", called)
	}

	writeResult, err := adapter.ExecuteTool(context.Background(), "system.config", json.RawMessage(`{"key":"foo","value":"bar"}`))
	if err != nil {
		t.Fatalf("write ExecuteTool: %v", err)
	}
	if !writeResult.IsError {
		t.Fatal("expected write call to be blocked")
	}
	if !strings.Contains(writeResult.Content, "BLOCKED") {
		t.Fatalf("expected BLOCKED message, got %q", writeResult.Content)
	}
	if called != 1 {
		t.Fatalf("blocked write should not execute tool handler, called=%d", called)
	}
}
