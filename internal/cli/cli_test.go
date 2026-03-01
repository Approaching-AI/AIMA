package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
)

// Ensure cobra import is used.
var _ *cobra.Command

func testApp(t *testing.T) *App {
	t.Helper()

	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cat := &knowledge.Catalog{}
	proxyServer := proxy.NewServer()
	mcpServer := mcp.NewServer()

	return &App{
		DB:      db,
		Catalog: cat,
		Proxy:   proxyServer,
		MCP:     mcpServer,
		ToolDeps: &mcp.ToolDeps{
			ListProfiles: func(ctx context.Context) (json.RawMessage, error) {
				return json.RawMessage(`[{"name":"test-hw"}]`), nil
			},
			ListEngineAssets: func(ctx context.Context) (json.RawMessage, error) {
				return json.RawMessage(`[{"type":"llamacpp"}]`), nil
			},
			ListModelAssets: func(ctx context.Context) (json.RawMessage, error) {
				return json.RawMessage(`[{"name":"test-model"}]`), nil
			},
			ListKnowledgeSummary: func(ctx context.Context) (json.RawMessage, error) {
				return json.RawMessage(`{"hardware_profiles":1,"engine_assets":1,"model_assets":1}`), nil
			},
			AgentStatus: func(ctx context.Context) (json.RawMessage, error) {
				return json.RawMessage(`{"zeroclaw_available":false,"zeroclaw_healthy":false}`), nil
			},
		},
	}
}

func TestNewRootCmd(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	if root.Use != "aima" {
		t.Errorf("Use = %q, want %q", root.Use, "aima")
	}

	// Verify all expected subcommands are registered
	expected := []string{
		"init", "hal",
		"deploy", "undeploy", "status",
		"model", "engine", "knowledge", "catalog",
		"ask", "agent", "serve", "discover",
	}
	cmds := make(map[string]bool)
	for _, c := range root.Commands() {
		cmds[c.Name()] = true
	}
	for _, name := range expected {
		if !cmds[name] {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestRootCmdHelp(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("help command failed: %v", err)
	}

	if buf.Len() == 0 {
		t.Error("help output is empty")
	}
}

func TestModelSubcommands(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	// Find the model command
	var modelCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "model" {
			modelCmd = c
			break
		}
	}
	if modelCmd == nil {
		t.Fatal("model command not found")
	}

	expected := []string{"scan", "list", "pull", "import", "info", "remove"}
	subs := make(map[string]bool)
	for _, c := range modelCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range expected {
		if !subs[name] {
			t.Errorf("model missing subcommand %q", name)
		}
	}
}

func TestEngineSubcommands(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var engineCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "engine" {
			engineCmd = c
			break
		}
	}
	if engineCmd == nil {
		t.Fatal("engine command not found")
	}

	expected := []string{"scan", "list", "pull", "import", "remove"}
	subs := make(map[string]bool)
	for _, c := range engineCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range expected {
		if !subs[name] {
			t.Errorf("engine missing subcommand %q", name)
		}
	}
}

func TestKnowledgeSubcommands(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var knowledgeCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "knowledge" {
			knowledgeCmd = c
			break
		}
	}
	if knowledgeCmd == nil {
		t.Fatal("knowledge command not found")
	}

	expected := []string{"list", "resolve"}
	subs := make(map[string]bool)
	for _, c := range knowledgeCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range expected {
		if !subs[name] {
			t.Errorf("knowledge missing subcommand %q", name)
		}
	}
}

func TestAgentSubcommands(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var agentCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "agent" {
			agentCmd = c
			break
		}
	}
	if agentCmd == nil {
		t.Fatal("agent command not found")
	}

	expected := []string{"install", "status"}
	subs := make(map[string]bool)
	for _, c := range agentCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range expected {
		if !subs[name] {
			t.Errorf("agent missing subcommand %q", name)
		}
	}
}

func TestKnowledgeListCmd(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"knowledge", "list"})

	if err := root.Execute(); err != nil {
		t.Fatalf("knowledge list failed: %v", err)
	}

	if buf.Len() == 0 {
		t.Error("knowledge list output is empty")
	}
}

func TestHalSubcommands(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var halCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "hal" {
			halCmd = c
			break
		}
	}
	if halCmd == nil {
		t.Fatal("hal command not found")
	}

	expected := []string{"detect", "metrics"}
	subs := make(map[string]bool)
	for _, c := range halCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range expected {
		if !subs[name] {
			t.Errorf("hal missing subcommand %q", name)
		}
	}
}

func TestCatalogSubcommands(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var catalogCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "catalog" {
			catalogCmd = c
			break
		}
	}
	if catalogCmd == nil {
		t.Fatal("catalog command not found")
	}

	expected := []string{"status", "override"}
	subs := make(map[string]bool)
	for _, c := range catalogCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range expected {
		if !subs[name] {
			t.Errorf("catalog missing subcommand %q", name)
		}
	}
}

func TestParseConfigOverrides(t *testing.T) {
	tests := []struct {
		name  string
		pairs []string
		want  map[string]any
	}{
		{"nil input", nil, nil},
		{"empty input", []string{}, nil},
		{"integer", []string{"max_model_len=8000"}, map[string]any{"max_model_len": 8000}},
		{"float", []string{"gpu_memory_utilization=0.85"}, map[string]any{"gpu_memory_utilization": 0.85}},
		{"bool true", []string{"enable_chunked_prefill=true"}, map[string]any{"enable_chunked_prefill": true}},
		{"bool false", []string{"enable_chunked_prefill=false"}, map[string]any{"enable_chunked_prefill": false}},
		{"bool True case insensitive", []string{"flag=True"}, map[string]any{"flag": true}},
		{"string", []string{"dtype=float16"}, map[string]any{"dtype": "float16"}},
		{"zero is int not bool", []string{"n_gpu_layers=0"}, map[string]any{"n_gpu_layers": 0}},
		{"t is string not bool", []string{"dtype=t"}, map[string]any{"dtype": "t"}},
		{"f is string not bool", []string{"dtype=f"}, map[string]any{"dtype": "f"}},
		{"empty value", []string{"key="}, map[string]any{"key": ""}},
		{"no equals", []string{"invalid"}, map[string]any{}},
		{"multiple", []string{"gpu_memory_utilization=0.8", "max_model_len=4096", "dtype=auto"},
			map[string]any{"gpu_memory_utilization": 0.8, "max_model_len": 4096, "dtype": "auto"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseConfigOverrides(tt.pairs)
			if tt.want == nil {
				if got != nil {
					t.Errorf("got %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("len = %d, want %d; got %v", len(got), len(tt.want), got)
				return
			}
			for k, wantV := range tt.want {
				gotV, ok := got[k]
				if !ok {
					t.Errorf("missing key %q", k)
					continue
				}
				if gotV != wantV {
					t.Errorf("key %q: got %v (%T), want %v (%T)", k, gotV, gotV, wantV, wantV)
				}
			}
		})
	}
}

func TestAgentStatusCmd(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"agent", "status"})

	if err := root.Execute(); err != nil {
		t.Fatalf("agent status failed: %v", err)
	}

	if buf.Len() == 0 {
		t.Error("agent status output is empty")
	}
}
