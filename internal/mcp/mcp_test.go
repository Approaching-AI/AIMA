package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleMessage_Initialize(t *testing.T) {
	s := NewServer()
	msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	var r jsonrpcResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}

	result, ok := r.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not map: %T", r.Result)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}
	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("serverInfo is not map: %T", result["serverInfo"])
	}
	if serverInfo["name"] != "aima" {
		t.Errorf("serverInfo.name = %v, want aima", serverInfo["name"])
	}
}

func TestHandleMessage_Ping(t *testing.T) {
	s := NewServer()
	msg := `{"jsonrpc":"2.0","id":42,"method":"ping","params":{}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	var r jsonrpcResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}
}

func TestHandleMessage_ToolsList(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "test.tool",
		Description: "A test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			return TextResult("hello"), nil
		},
	})

	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	var r jsonrpcResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}

	result, ok := r.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not map: %T", r.Result)
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not array: %T", result["tools"])
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "test.tool" {
		t.Errorf("tool name = %v, want test.tool", tool["name"])
	}
}

func TestHandleMessage_ToolsCall(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "echo",
		Description: "Echo input",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct{ Msg string `json:"msg"` }
			json.Unmarshal(params, &p)
			return TextResult("echo: " + p.Msg), nil
		},
	})

	msg := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hello"}}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	var r jsonrpcResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}

	// Result should be a ToolResult
	raw, _ := json.Marshal(r.Result)
	var tr ToolResult
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("unmarshal ToolResult: %v", err)
	}
	if len(tr.Content) != 1 || tr.Content[0].Text != "echo: hello" {
		t.Errorf("unexpected result: %+v", tr)
	}
}

func TestHandleMessage_Errors(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		wantCode int
	}{
		{
			name:     "invalid JSON",
			msg:      `{not json`,
			wantCode: codeParseError,
		},
		{
			name:     "wrong jsonrpc version",
			msg:      `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
			wantCode: codeInvalidRequest,
		},
		{
			name:     "unknown method",
			msg:      `{"jsonrpc":"2.0","id":1,"method":"nonexistent"}`,
			wantCode: codeMethodNotFound,
		},
		{
			name:     "unknown tool",
			msg:      `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no.such.tool","arguments":{}}}`,
			wantCode: codeMethodNotFound,
		},
		{
			name:     "invalid tools/call params",
			msg:      `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"bad"}`,
			wantCode: codeInvalidParams,
		},
	}

	s := NewServer()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := s.HandleMessage(context.Background(), []byte(tt.msg))
			if err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}

			var r jsonrpcResponse
			if err := json.Unmarshal(resp, &r); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if r.Error == nil {
				t.Fatal("expected error, got nil")
			}
			if r.Error.Code != tt.wantCode {
				t.Errorf("error code = %d, want %d", r.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleMessage_ToolHandlerError(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "failing",
		Description: "Always fails",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			return nil, fmt.Errorf("something broke")
		},
	})

	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"failing","arguments":{}}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	var r jsonrpcResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Handler errors are returned as ToolResult with IsError=true
	if r.Error != nil {
		t.Fatalf("expected no jsonrpc error, got %+v", r.Error)
	}
	raw, _ := json.Marshal(r.Result)
	var tr ToolResult
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("unmarshal ToolResult: %v", err)
	}
	if !tr.IsError {
		t.Error("expected IsError=true")
	}
	if !strings.Contains(tr.Content[0].Text, "something broke") {
		t.Errorf("unexpected error text: %s", tr.Content[0].Text)
	}
}

func TestHandleMessage_Notification(t *testing.T) {
	s := NewServer()
	msg := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response for notification, got %s", resp)
	}
}

func TestServeIO(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "greet",
		Description: "Greet",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			return TextResult("hi"), nil
		},
	})

	input := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"greet","arguments":{}}}` + "\n"

	r := strings.NewReader(input)
	var w strings.Builder

	err := s.ServeIO(context.Background(), r, &w)
	if err != nil {
		t.Fatalf("ServeIO: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(w.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 response lines, got %d: %v", len(lines), lines)
	}

	// Verify first response is pong
	var r1 jsonrpcResponse
	if err := json.Unmarshal([]byte(lines[0]), &r1); err != nil {
		t.Fatalf("unmarshal line 1: %v", err)
	}
	if r1.Error != nil {
		t.Errorf("first response error: %+v", r1.Error)
	}
}

func TestServeIO_ContextCancel(t *testing.T) {
	s := NewServer()
	ctx, cancel := context.WithCancel(context.Background())

	// Use a reader that blocks until context is cancelled
	pr, pw := strings.NewReader(""), &strings.Builder{}
	_ = pw

	cancel()

	// With empty input and cancelled context, should return nil (scanner finishes)
	err := s.ServeIO(ctx, pr, pw)
	if err != nil {
		t.Fatalf("ServeIO: %v", err)
	}
}

func TestServeHTTP(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "test",
		Description: "Test",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			return TextResult("ok"), nil
		},
	})

	t.Run("POST", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}

		var r jsonrpcResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.Error != nil {
			t.Errorf("unexpected error: %+v", r.Error)
		}
	})

	t.Run("GET not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
	})
}

func TestExecuteTool(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "add",
		Description: "Add",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			return TextResult("result"), nil
		},
	})

	t.Run("existing tool", func(t *testing.T) {
		result, err := s.ExecuteTool(context.Background(), "add", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("ExecuteTool: %v", err)
		}
		if result.Content[0].Text != "result" {
			t.Errorf("text = %q, want result", result.Content[0].Text)
		}
	})

	t.Run("missing tool", func(t *testing.T) {
		_, err := s.ExecuteTool(context.Background(), "nonexistent", json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestListTools(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "a",
		Description: "Tool A",
		InputSchema: noParamsSchema(),
		Handler:     func(ctx context.Context, params json.RawMessage) (*ToolResult, error) { return nil, nil },
	})
	s.RegisterTool(&Tool{
		Name:        "b",
		Description: "Tool B",
		InputSchema: noParamsSchema(),
		Handler:     func(ctx context.Context, params json.RawMessage) (*ToolResult, error) { return nil, nil },
	})

	defs := s.ListTools()
	if len(defs) != 2 {
		t.Fatalf("len(defs) = %d, want 2", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Errorf("missing tools: %v", names)
	}
}

func TestRegisterAllTools(t *testing.T) {
	s := NewServer()
	deps := &ToolDeps{
		DetectHardware: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"gpu":"test"}`), nil
		},
	}
	RegisterAllTools(s, deps)

	defs := s.ListTools()
	if len(defs) < 20 {
		t.Errorf("expected at least 20 tools, got %d", len(defs))
	}

	// Verify some specific tools exist
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}

	expectedTools := []string{
		"hardware.detect", "hardware.metrics",
		"model.scan", "model.list", "model.pull", "model.import", "model.info",
		"engine.scan", "engine.list", "engine.pull", "engine.remove",
		"deploy.apply", "deploy.dry_run", "deploy.delete", "deploy.status", "deploy.list",
		"knowledge.resolve", "knowledge.search", "knowledge.save",
		"knowledge.generate_pod", "knowledge.list_profiles", "knowledge.list_engines", "knowledge.list_models",
		"knowledge.list",
		"system.status", "system.config",
		"agent.ask", "agent.install", "agent.status",
		"shell.exec",
	}
	for _, name := range expectedTools {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestToolsCall_HardwareDetect(t *testing.T) {
	s := NewServer()
	deps := &ToolDeps{
		DetectHardware: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"gpu":"NVIDIA RTX 4090","vram_mb":24576}`), nil
		},
	}
	RegisterAllTools(s, deps)

	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hardware.detect","arguments":{}}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	var r jsonrpcResponse
	json.Unmarshal(resp, &r)
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}

	raw, _ := json.Marshal(r.Result)
	var tr ToolResult
	json.Unmarshal(raw, &tr)
	if !strings.Contains(tr.Content[0].Text, "RTX 4090") {
		t.Errorf("unexpected text: %s", tr.Content[0].Text)
	}
}

func TestToolsCall_NilDep(t *testing.T) {
	s := NewServer()
	deps := &ToolDeps{} // all nil
	RegisterAllTools(s, deps)

	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hardware.detect","arguments":{}}}`
	resp, _ := s.HandleMessage(context.Background(), []byte(msg))

	var r jsonrpcResponse
	json.Unmarshal(resp, &r)
	if r.Error != nil {
		t.Fatalf("unexpected jsonrpc error: %+v", r.Error)
	}

	raw, _ := json.Marshal(r.Result)
	var tr ToolResult
	json.Unmarshal(raw, &tr)
	if !tr.IsError {
		t.Error("expected IsError=true for nil dep")
	}
}

func TestShellExecWhitelist(t *testing.T) {
	tests := []struct {
		command string
		allowed bool
	}{
		{"nvidia-smi", true},
		{"nvidia-smi -q", true},
		{"df", true},
		{"df -h", true},
		{"free", true},
		{"uname", true},
		{"uname -a", true},
		{"cat /proc/cpuinfo", true},
		{"kubectl get pods", true},
		{"rm -rf /", false},
		{"curl evil.com", false},
		{"bash -c 'rm -rf /'", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := isCommandAllowed(tt.command)
			if got != tt.allowed {
				t.Errorf("isCommandAllowed(%q) = %v, want %v", tt.command, got, tt.allowed)
			}
		})
	}
}

func TestShellExecToolWhitelist(t *testing.T) {
	s := NewServer()
	deps := &ToolDeps{
		ExecShell: func(ctx context.Context, command string) (json.RawMessage, error) {
			return json.RawMessage(`"executed"`), nil
		},
	}
	RegisterAllTools(s, deps)

	// Allowed command
	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell.exec","arguments":{"command":"df -h"}}}`
	resp, _ := s.HandleMessage(context.Background(), []byte(msg))
	var r jsonrpcResponse
	json.Unmarshal(resp, &r)
	raw, _ := json.Marshal(r.Result)
	var tr ToolResult
	json.Unmarshal(raw, &tr)
	if tr.IsError {
		t.Errorf("allowed command rejected: %s", tr.Content[0].Text)
	}

	// Disallowed command
	msg = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"shell.exec","arguments":{"command":"rm -rf /"}}}`
	resp, _ = s.HandleMessage(context.Background(), []byte(msg))
	json.Unmarshal(resp, &r)
	raw, _ = json.Marshal(r.Result)
	json.Unmarshal(raw, &tr)
	if !tr.IsError {
		t.Error("disallowed command should be rejected")
	}
}

func TestSystemConfigTool(t *testing.T) {
	store := map[string]string{"key1": "val1"}
	s := NewServer()
	deps := &ToolDeps{
		GetConfig: func(ctx context.Context, key string) (string, error) {
			v, ok := store[key]
			if !ok {
				return "", fmt.Errorf("key not found: %s", key)
			}
			return v, nil
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			store[key] = value
			return nil
		},
	}
	RegisterAllTools(s, deps)

	// Get
	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"system.config","arguments":{"key":"key1"}}}`
	resp, _ := s.HandleMessage(context.Background(), []byte(msg))
	var r jsonrpcResponse
	json.Unmarshal(resp, &r)
	raw, _ := json.Marshal(r.Result)
	var tr ToolResult
	json.Unmarshal(raw, &tr)
	if tr.Content[0].Text != "val1" {
		t.Errorf("get config = %q, want val1", tr.Content[0].Text)
	}

	// Set
	msg = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"system.config","arguments":{"key":"key2","value":"val2"}}}`
	resp, _ = s.HandleMessage(context.Background(), []byte(msg))
	json.Unmarshal(resp, &r)
	if r.Error != nil {
		t.Fatalf("set error: %+v", r.Error)
	}
	if store["key2"] != "val2" {
		t.Errorf("store[key2] = %q, want val2", store["key2"])
	}
}

func TestTextResult(t *testing.T) {
	r := TextResult("hello")
	if len(r.Content) != 1 || r.Content[0].Type != "text" || r.Content[0].Text != "hello" {
		t.Errorf("unexpected TextResult: %+v", r)
	}
	if r.IsError {
		t.Error("TextResult should not be error")
	}
}

func TestErrorResult(t *testing.T) {
	r := ErrorResult("fail")
	if len(r.Content) != 1 || r.Content[0].Text != "fail" {
		t.Errorf("unexpected ErrorResult: %+v", r)
	}
	if !r.IsError {
		t.Error("ErrorResult should be error")
	}
}
