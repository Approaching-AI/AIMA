package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/support"
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
			DeployList: func(ctx context.Context) (json.RawMessage, error) {
				return json.RawMessage(`[]`), nil
			},
			AgentStatus: func(ctx context.Context) (json.RawMessage, error) {
				return json.RawMessage(`{"agent_available":false,"active_exploration_runs":0}`), nil
			},
			SupportAskForHelp: func(ctx context.Context, description, endpoint, inviteCode, workerCode, recoveryCode, referralCode string) (json.RawMessage, error) {
				return json.RawMessage(`{"enabled":true,"device_id":"dev-test","created":false}`), nil
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
		"run", "init", "hal",
		"deploy", "undeploy", "status",
		"model", "engine", "knowledge", "catalog",
		"ask", "agent", "config", "serve", "mcp",
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

func TestDeploySubcommands(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var deployCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "deploy" {
			deployCmd = c
			break
		}
	}
	if deployCmd == nil {
		t.Fatal("deploy command not found")
	}

	expected := []string{"list"}
	subs := make(map[string]bool)
	for _, c := range deployCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range expected {
		if !subs[name] {
			t.Errorf("deploy missing subcommand %q", name)
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

	expected := []string{"status", "rollback-list", "rollback"}
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

func TestAskForHelpCommand(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"askforhelp", "help", "me"})

	if err := root.Execute(); err != nil {
		t.Fatalf("askforhelp failed: %v", err)
	}

	if got := buf.String(); got == "" || !bytes.Contains(buf.Bytes(), []byte(`"device_id": "dev-test"`)) {
		t.Fatalf("unexpected askforhelp output: %q", got)
	}
}

func TestExecuteAskForHelpPromptsForInviteCode(t *testing.T) {
	t.Parallel()

	var calls int
	uiOut := &bytes.Buffer{}
	ui := &askForHelpUI{
		reader:     bufio.NewReader(strings.NewReader("invite-123\n")),
		out:        uiOut,
		pretty:     true,
		promptable: true,
	}
	call := &askForHelpCall{}

	data, result, err := executeAskForHelp(context.Background(), func(ctx context.Context, description, endpoint, inviteCode, workerCode, recoveryCode, referralCode string) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return nil, &support.RegistrationPromptError{
				Kind:   support.RegistrationPromptInviteOrWorker,
				Detail: "invite_code or worker_enrollment_code is required for new device registration",
			}
		}
		if inviteCode != "invite-123" {
			t.Fatalf("inviteCode = %q, want invite-123", inviteCode)
		}
		return json.RawMessage(`{"enabled":true,"device_id":"dev-1"}`), nil
	}, ui, call)
	if err != nil {
		t.Fatalf("executeAskForHelp: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if call.inviteCode != "invite-123" {
		t.Fatalf("stored inviteCode = %q, want invite-123", call.inviteCode)
	}
	if result.DeviceID != "dev-1" {
		t.Fatalf("device_id = %q, want dev-1", result.DeviceID)
	}
	if len(data) == 0 {
		t.Fatal("expected response payload")
	}
	if !strings.Contains(uiOut.String(), "请输入邀请码或 Worker 接入码") {
		t.Fatalf("unexpected prompt output: %q", uiOut.String())
	}
}

func TestExecuteAskForHelpPromptsForRecoveryCode(t *testing.T) {
	t.Parallel()

	var calls int
	uiOut := &bytes.Buffer{}
	ui := &askForHelpUI{
		reader:     bufio.NewReader(strings.NewReader("rec-123\n")),
		out:        uiOut,
		pretty:     true,
		promptable: true,
	}
	call := &askForHelpCall{}

	_, _, err := executeAskForHelp(context.Background(), func(ctx context.Context, description, endpoint, inviteCode, workerCode, recoveryCode, referralCode string) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return nil, &support.RegistrationPromptError{
				Kind:   support.RegistrationPromptRecovery,
				Detail: "valid recovery_code required to refresh existing device credentials",
			}
		}
		if recoveryCode != "rec-123" {
			t.Fatalf("recoveryCode = %q, want rec-123", recoveryCode)
		}
		return json.RawMessage(`{"enabled":true,"device_id":"dev-2"}`), nil
	}, ui, call)
	if err != nil {
		t.Fatalf("executeAskForHelp: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if call.recoveryCode != "rec-123" {
		t.Fatalf("stored recoveryCode = %q, want rec-123", call.recoveryCode)
	}
	if !strings.Contains(uiOut.String(), "请输入已保存的恢复码") {
		t.Fatalf("unexpected prompt output: %q", uiOut.String())
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
		{"multiple", []string{"gpu_memory_utilization=0.8", "max_model_len=4096", "dtype=auto"},
			map[string]any{"gpu_memory_utilization": 0.8, "max_model_len": 4096, "dtype": "auto"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseConfigOverrides(tt.pairs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
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

	t.Run("no equals returns error", func(t *testing.T) {
		_, err := parseConfigOverrides([]string{"invalid"})
		if err == nil {
			t.Fatal("expected error for malformed entry, got nil")
		}
	})
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

func TestExploreStartDoesNotExposePlannerFlag(t *testing.T) {
	app := testApp(t)
	root := NewRootCmd(app)

	var exploreCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "explore" {
			exploreCmd = c
			break
		}
	}
	if exploreCmd == nil {
		t.Fatal("explore command not found")
	}

	var startCmd *cobra.Command
	for _, c := range exploreCmd.Commands() {
		if c.Name() == "start" {
			startCmd = c
			break
		}
	}
	if startCmd == nil {
		t.Fatal("explore start command not found")
	}

	if startCmd.Flags().Lookup("planner") != nil {
		t.Fatal("planner flag should not be exposed")
	}
}

func TestDeployListCmd(t *testing.T) {
	app := testApp(t)
	app.ToolDeps.DeployList = func(ctx context.Context) (json.RawMessage, error) {
		return json.RawMessage(`[{"name":"qwen3-8b-vllm","model":"qwen3-8b","engine":"vllm","phase":"running","ready":true}]`), nil
	}
	root := NewRootCmd(app)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"deploy", "list"})

	if err := root.Execute(); err != nil {
		t.Fatalf("deploy list failed: %v", err)
	}

	if buf.Len() == 0 {
		t.Error("deploy list output is empty")
	}
	output := buf.String()
	if !strings.Contains(output, `"model": "qwen3-8b"`) {
		t.Fatalf("deploy list output missing top-level model: %s", output)
	}
	if !strings.Contains(output, `"engine": "vllm"`) {
		t.Fatalf("deploy list output missing top-level engine: %s", output)
	}
}

// TestExplorerCmdRemoteDispatch locks in the New-Bug-1 fix: when --remote is
// set, `aima explorer {trigger,status,config}` must issue a tools/call
// against the remote MCP endpoint instead of falling through to the in-process
// event bus (which is dead once the CLI exits).
func TestExplorerCmdRemoteDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantAction string
		wantKey    string // only for config subcommand
	}{
		{name: "trigger", args: []string{"explorer", "trigger"}, wantAction: "trigger"},
		{name: "status", args: []string{"explorer", "status"}, wantAction: "status"},
		{name: "config-set", args: []string{"explorer", "config", "--action", "set", "--key", "max_rounds", "--value", "3"}, wantAction: "config", wantKey: "max_rounds"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotAction, gotConfigAction, gotKey string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var rpc struct {
					Params struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					} `json:"params"`
				}
				_ = json.Unmarshal(body, &rpc)
				if rpc.Params.Name != "explorer" {
					t.Errorf("tool name = %q, want explorer", rpc.Params.Name)
				}
				gotAction, _ = rpc.Params.Arguments["action"].(string)
				gotConfigAction, _ = rpc.Params.Arguments["config_action"].(string)
				gotKey, _ = rpc.Params.Arguments["key"].(string)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`))
			}))
			defer srv.Close()

			app := testApp(t)
			// Intentionally leave ToolDeps.Explorer* nil — if remote dispatch
			// is broken, the CLI will fall through and hit "explorer not
			// available", failing the test.
			app.ToolDeps.ExplorerStatus = nil
			app.ToolDeps.ExplorerTrigger = nil
			app.ToolDeps.ExplorerConfig = nil

			root := NewRootCmd(app)
			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetErr(&buf)
			args := append([]string{"--remote", srv.URL}, tc.args...)
			root.SetArgs(args)

			if err := root.Execute(); err != nil {
				t.Fatalf("%s: %v\n%s", tc.name, err, buf.String())
			}

			if gotAction != tc.wantAction {
				t.Fatalf("action = %q, want %q", gotAction, tc.wantAction)
			}
			if tc.wantAction == "config" {
				if gotConfigAction != "set" {
					t.Fatalf("config_action = %q, want set", gotConfigAction)
				}
				if gotKey != tc.wantKey {
					t.Fatalf("key = %q, want %q", gotKey, tc.wantKey)
				}
			}
		})
	}
}
