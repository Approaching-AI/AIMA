package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/buildinfo"
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
	if serverInfo["version"] != buildinfo.Version {
		t.Errorf("serverInfo.version = %v, want %s", serverInfo["version"], buildinfo.Version)
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
			var p struct {
				Msg string `json:"msg"`
			}
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
		"deploy.apply", "deploy.run", "deploy.dry_run", "deploy.delete", "deploy.status", "deploy.list",
		"knowledge.resolve", "knowledge.search", "knowledge.save", "knowledge.promote",
		"knowledge.analytics", "knowledge.evaluate",
		"catalog.list", "catalog.override", "catalog.validate",
		"central.sync", "central.advise", "central.scenario",
		"data.export", "data.import",
		"patrol", "explore", "tuning", "explorer",
		"fleet.info", "fleet.exec",
		"scenario.show", "scenario.apply",
		"openclaw", "stack",
		"system.status", "system.config",
		"agent.ask", "agent.status", "agent.rollback",
		"support",
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

func TestSystemConfigTool(t *testing.T) {
	store := map[string]string{"llm.endpoint": "http://localhost:8080"}
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

	// Get valid key
	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"system.config","arguments":{"key":"llm.endpoint"}}}`
	resp, _ := s.HandleMessage(context.Background(), []byte(msg))
	var r jsonrpcResponse
	json.Unmarshal(resp, &r)
	raw, _ := json.Marshal(r.Result)
	var tr ToolResult
	json.Unmarshal(raw, &tr)
	if tr.Content[0].Text != "http://localhost:8080" {
		t.Errorf("get config = %q, want http://localhost:8080", tr.Content[0].Text)
	}

	// Set valid key
	msg = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"system.config","arguments":{"key":"llm.model","value":"qwen3"}}}`
	resp, _ = s.HandleMessage(context.Background(), []byte(msg))
	json.Unmarshal(resp, &r)
	if r.Error != nil {
		t.Fatalf("set error: %+v", r.Error)
	}
	if store["llm.model"] != "qwen3" {
		t.Errorf("store[llm.model] = %q, want qwen3", store["llm.model"])
	}

	// Reject unknown key
	msg = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"system.config","arguments":{"key":"bogus_key","value":"x"}}}`
	resp, _ = s.HandleMessage(context.Background(), []byte(msg))
	json.Unmarshal(resp, &r)
	raw, _ = json.Marshal(r.Result)
	json.Unmarshal(raw, &tr)
	if !tr.IsError {
		t.Error("unknown key should be rejected")
	}
}

func TestSupportAskForHelpTool(t *testing.T) {
	s := NewServer()
	deps := &ToolDeps{
		SupportAskForHelp: func(ctx context.Context, description, endpoint, inviteCode, workerCode, recoveryCode, referralCode string) (json.RawMessage, error) {
			return json.RawMessage(`{"enabled":true,"device_id":"dev-1","created":true,"task_id":"task-1"}`), nil
		},
	}
	RegisterAllTools(s, deps)

	msg := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"support","arguments":{"description":"fix this"}}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	var r jsonrpcResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, _ := json.Marshal(r.Result)
	var tr ToolResult
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("unmarshal ToolResult: %v", err)
	}
	if tr.IsError {
		t.Fatalf("support returned error: %+v", tr)
	}
	if !strings.Contains(tr.Content[0].Text, `"task_id":"task-1"`) {
		t.Fatalf("unexpected tool result: %s", tr.Content[0].Text)
	}
}

func TestIsValidProfile(t *testing.T) {
	tests := []struct {
		profile Profile
		valid   bool
	}{
		{ProfileFull, true},
		{ProfileOperator, true},
		{ProfilePatrol, true},
		{ProfileExplorer, true},
		{Profile("opreator"), false}, // typo
		{Profile("unknown"), false},
		{Profile("OPERATOR"), false}, // case-sensitive
	}
	for _, tt := range tests {
		got := IsValidProfile(tt.profile)
		if got != tt.valid {
			t.Errorf("IsValidProfile(%q) = %v, want %v", tt.profile, got, tt.valid)
		}
	}
}

func TestProfileMatches(t *testing.T) {
	tests := []struct {
		profile Profile
		tool    string
		want    bool
	}{
		// ProfileFull matches everything
		{ProfileFull, "hardware.detect", true},
		{ProfileFull, "anything.here", true},

		// ProfileOperator: prefix matches
		{ProfileOperator, "hardware.detect", true},
		{ProfileOperator, "hardware.metrics", true},
		{ProfileOperator, "model.scan", true},
		{ProfileOperator, "model.list", true},
		{ProfileOperator, "engine.pull", true},
		{ProfileOperator, "deploy.apply", true},
		{ProfileOperator, "deploy.approve", true},
		{ProfileOperator, "deploy.dry_run", true},
		{ProfileOperator, "system.status", true},
		{ProfileOperator, "system.config", true},
		{ProfileOperator, "fleet.info", true},  // fleet. prefix
		{ProfileOperator, "fleet.exec", true},  // fleet. prefix
		{ProfileOperator, "scenario.apply", true},
		{ProfileOperator, "scenario.show", true},

		// ProfileOperator: exact matches
		{ProfileOperator, "catalog.list", true},
		{ProfileOperator, "openclaw", true},
		{ProfileOperator, "support", true},
		{ProfileOperator, "knowledge.resolve", true},
		{ProfileOperator, "knowledge.search", true},
		{ProfileOperator, "knowledge.promote", true},
		{ProfileOperator, "benchmark.run", true},
		{ProfileOperator, "benchmark.list", true},
		{ProfileOperator, "agent.ask", true},
		{ProfileOperator, "agent.status", true},
		{ProfileOperator, "agent.rollback", true},

		// ProfileOperator: excluded tools
		{ProfileOperator, "knowledge.analytics", false},
		{ProfileOperator, "knowledge.save", false},
		{ProfileOperator, "knowledge.evaluate", false},
		{ProfileOperator, "catalog.override", false},
		{ProfileOperator, "patrol", false},
		{ProfileOperator, "explore", false},
		{ProfileOperator, "tuning", false},
		{ProfileOperator, "explorer", false},
		{ProfileOperator, "central.sync", false},
		{ProfileOperator, "data.export", false},

		// ProfilePatrol: included
		{ProfilePatrol, "hardware.metrics", true},
		{ProfilePatrol, "deploy.list", true},
		{ProfilePatrol, "deploy.status", true},
		{ProfilePatrol, "deploy.logs", true},
		{ProfilePatrol, "deploy.apply", true},
		{ProfilePatrol, "deploy.approve", true},
		{ProfilePatrol, "deploy.dry_run", true},
		{ProfilePatrol, "knowledge.resolve", true},
		{ProfilePatrol, "benchmark.run", true},
		{ProfilePatrol, "patrol", true},

		// ProfilePatrol: excluded
		{ProfilePatrol, "hardware.detect", false},
		{ProfilePatrol, "model.list", false},
		{ProfilePatrol, "deploy.delete", false},
		{ProfilePatrol, "system.status", false},
		{ProfilePatrol, "explore", false},
		{ProfilePatrol, "tuning", false},

		// ProfileExplorer: included
		{ProfileExplorer, "benchmark.run", true},
		{ProfileExplorer, "benchmark.record", true},
		{ProfileExplorer, "benchmark.list", true},
		{ProfileExplorer, "explore", true},
		{ProfileExplorer, "tuning", true},
		{ProfileExplorer, "explorer", true},
		{ProfileExplorer, "deploy.apply", true},
		{ProfileExplorer, "deploy.approve", true},
		{ProfileExplorer, "deploy.delete", true},
		{ProfileExplorer, "hardware.detect", true},
		{ProfileExplorer, "knowledge.resolve", true},
		{ProfileExplorer, "knowledge.search", true},
		{ProfileExplorer, "knowledge.promote", true},
		{ProfileExplorer, "knowledge.save", true},
		{ProfileExplorer, "central.advise", true},

		// ProfileExplorer: excluded
		{ProfileExplorer, "model.list", false},
		{ProfileExplorer, "agent.ask", false},
		{ProfileExplorer, "fleet.info", false},
		{ProfileExplorer, "system.status", false},

		// Unknown profile matches everything (backward compat)
		{Profile("unknown"), "anything", true},
	}

	for _, tt := range tests {
		name := fmt.Sprintf("%s/%s", tt.profile, tt.tool)
		if tt.profile == "" {
			name = "full/" + tt.tool
		}
		t.Run(name, func(t *testing.T) {
			got := ProfileMatches(tt.profile, tt.tool)
			if got != tt.want {
				t.Errorf("ProfileMatches(%q, %q) = %v, want %v", tt.profile, tt.tool, got, tt.want)
			}
		})
	}
}

func TestListToolsIgnoresProfile(t *testing.T) {
	s := NewServer()
	// Register tools from multiple categories
	toolNames := []string{
		"hardware.detect", "hardware.metrics",
		"model.list", "model.scan",
		"deploy.apply", "deploy.list",
		"knowledge.resolve", "knowledge.compare", "knowledge.gaps",
		"agent.ask", "agent.patrol_status",
		"benchmark.run", "explore.start", "tuning.start",
	}
	for _, name := range toolNames {
		s.RegisterTool(&Tool{
			Name:        name,
			Description: "test",
			InputSchema: noParamsSchema(),
			Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
				return TextResult("ok"), nil
			},
		})
	}

	// No profile = all tools
	defs := s.ListTools()
	if len(defs) != len(toolNames) {
		t.Errorf("no profile: got %d tools, want %d", len(defs), len(toolNames))
	}

	// Operator profile should not affect internal ListTools callers.
	s.SetProfile(ProfileOperator)
	defs = s.ListTools()
	if len(defs) != len(toolNames) {
		t.Errorf("operator profile: got %d tools, want %d", len(defs), len(toolNames))
	}

	// Patrol profile should not affect internal ListTools callers either.
	s.SetProfile(ProfilePatrol)
	defs = s.ListTools()
	if len(defs) != len(toolNames) {
		t.Errorf("patrol profile: got %d tools, want %d", len(defs), len(toolNames))
	}

	// Reset to full
	s.SetProfile(ProfileFull)
	defs = s.ListTools()
	if len(defs) != len(toolNames) {
		t.Errorf("full profile: got %d tools, want %d", len(defs), len(toolNames))
	}
}

func TestExecuteToolIgnoresProfile(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "knowledge.gaps",
		Description: "test",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			return TextResult("gaps result"), nil
		},
	})

	// Set operator profile — knowledge.gaps is NOT in this profile
	s.SetProfile(ProfileOperator)

	// Internal ListTools should still include it.
	defs := s.ListTools()
	found := false
	for _, d := range defs {
		if d.Name == "knowledge.gaps" {
			found = true
		}
	}
	if !found {
		t.Error("knowledge.gaps should still be visible to internal ListTools callers")
	}

	// But ExecuteTool should still work
	result, err := s.ExecuteTool(context.Background(), "knowledge.gaps", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.Content[0].Text != "gaps result" {
		t.Errorf("unexpected result: %s", result.Content[0].Text)
	}
}

func TestProfileFilteringViaJSONRPC(t *testing.T) {
	s := NewServer()
	s.RegisterTool(&Tool{
		Name:        "deploy.apply",
		Description: "deploy",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			return TextResult("deployed"), nil
		},
	})
	s.RegisterTool(&Tool{
		Name:        "tuning.start",
		Description: "tune",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			return TextResult("tuning"), nil
		},
	})

	// Set operator profile — tuning.start should be hidden
	s.SetProfile(ProfileOperator)

	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	resp, err := s.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	var r jsonrpcResponse
	json.Unmarshal(resp, &r)
	result := r.Result.(map[string]any)
	tools := result["tools"].([]any)

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool in operator profile, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "deploy.apply" {
		t.Errorf("expected deploy.apply, got %s", tool["name"])
	}

	// But tuning.start can still be called
	callMsg := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tuning.start","arguments":{}}}`
	resp, err = s.HandleMessage(context.Background(), []byte(callMsg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	json.Unmarshal(resp, &r)
	if r.Error != nil {
		t.Fatalf("unexpected error calling hidden tool: %+v", r.Error)
	}
}

func TestProfilesExposeDeployApprovalWithDeployApply(t *testing.T) {
	profiles := []Profile{ProfileOperator, ProfilePatrol, ProfileExplorer}
	for _, profile := range profiles {
		if ProfileMatches(profile, "deploy.apply") && !ProfileMatches(profile, "deploy.approve") {
			t.Fatalf("%q exposes deploy.apply without deploy.approve", profile)
		}
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
