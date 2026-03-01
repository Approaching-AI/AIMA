package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/zeroclaw"
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
	zcMgr := zeroclaw.NewManager()
	goAgent := agent.NewAgent(nil, nil)
	dispatcher := agent.NewDispatcher(goAgent, zcMgr)

	return &App{
		DB:         db,
		Catalog:    cat,
		Proxy:      proxyServer,
		MCP:        mcpServer,
		Dispatcher: dispatcher,
		ZeroClaw:   zcMgr,
		DataDir:    t.TempDir(),
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
