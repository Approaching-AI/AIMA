package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
			DiagnosticsExport: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"path":"/tmp/aima-diagnostics.json","telemetry_free":true}`), nil
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
		"diagnostics", "onboarding",
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

	expected := []string{"scan", "list", "pull", "import", "ensure", "rollback", "remove"}
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

func TestEngineEnsureCmd(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		wantVersion string
		wantApply   bool
	}{
		{name: "plan only", args: []string{"engine", "ensure", "engine-a"}},
		{name: "versioned apply", args: []string{"engine", "ensure", "engine-a", "--version", "b9330", "--apply"}, wantVersion: "b9330", wantApply: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t)
			calls := 0
			app.ToolDeps.EnsureEngine = func(_ context.Context, name, version string, apply bool) (json.RawMessage, error) {
				calls++
				if name != "engine-a" || version != tc.wantVersion || apply != tc.wantApply {
					t.Fatalf("EnsureEngine(%q, %q, %v)", name, version, apply)
				}
				return json.RawMessage(`{"plan":{"action":"install"},"applied":false}`), nil
			}
			root := NewRootCmd(app)
			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetArgs(tc.args)

			if err := root.Execute(); err != nil {
				t.Fatalf("engine ensure failed: %v", err)
			}
			if calls != 1 || !strings.Contains(buf.String(), `"action": "install"`) {
				t.Fatalf("calls=%d output=%s", calls, buf.String())
			}
		})
	}
}

func TestEngineRollbackCmd(t *testing.T) {
	app := testApp(t)
	calls := 0
	app.ToolDeps.RollbackEngine = func(_ context.Context, name string, confirm bool) (json.RawMessage, error) {
		calls++
		if name != "engine-a" || !confirm {
			t.Fatalf("RollbackEngine(%q, %v)", name, confirm)
		}
		return json.RawMessage(`{"asset_name":"engine-a","applied":true,"active_engine_id":"engine-v1"}`), nil
	}
	root := NewRootCmd(app)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"engine", "rollback", "engine-a", "--confirm"})

	if err := root.Execute(); err != nil {
		t.Fatalf("engine rollback failed: %v", err)
	}
	if calls != 1 || !strings.Contains(buf.String(), `"active_engine_id": "engine-v1"`) {
		t.Fatalf("calls=%d output=%s", calls, buf.String())
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

func TestCatalogEffectiveCmdPrintsYAML(t *testing.T) {
	app := testApp(t)
	app.ToolDeps.CatalogEffective = func(ctx context.Context, kind, name string) (json.RawMessage, error) {
		return json.RawMessage(`{"kind":"model_asset","name":"demo","yaml":"kind: model_asset\nmetadata:\n  name: demo\n"}`), nil
	}
	root := NewRootCmd(app)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"catalog", "effective", "model_asset", "demo"})

	if err := root.Execute(); err != nil {
		t.Fatalf("catalog effective failed: %v", err)
	}
	if !strings.Contains(buf.String(), "kind: model_asset") || !strings.Contains(buf.String(), "name: demo") {
		t.Fatalf("unexpected catalog effective output: %s", buf.String())
	}
}

func TestCatalogValidatePatchCmdReadsFile(t *testing.T) {
	app := testApp(t)
	var gotContent string
	app.ToolDeps.CatalogValidatePatch = func(ctx context.Context, content string) (json.RawMessage, error) {
		gotContent = content
		return json.RawMessage(`{"valid":true,"effective_yaml":"kind: model_asset\nmetadata:\n  name: demo\n"}`), nil
	}
	patchPath := t.TempDir() + "/demo.patch.yaml"
	if err := os.WriteFile(patchPath, []byte("kind: model_asset_patch\nmetadata:\n  name: demo\n"), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}
	root := NewRootCmd(app)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"catalog", "validate-patch", patchPath})

	if err := root.Execute(); err != nil {
		t.Fatalf("catalog validate-patch failed: %v", err)
	}
	if !strings.Contains(gotContent, "kind: model_asset_patch") {
		t.Fatalf("validate-patch did not read file content: %q", gotContent)
	}
	if !strings.Contains(buf.String(), `"valid": true`) {
		t.Fatalf("unexpected validate-patch output: %s", buf.String())
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

func TestOnboardingRootRunsGuidedStart(t *testing.T) {
	app := testApp(t)
	var startCalls int
	var gotLocale string
	app.ToolDeps.OnboardingStart = func(ctx context.Context, locale string) (json.RawMessage, error) {
		startCalls++
		gotLocale = locale
		return json.RawMessage(`{"status":{"onboarding_completed":false,"hardware":{"profile_match":"apple-m4-16g","os":"darwin","arch":"arm64","ram_mib":16384,"gpu":[]},"stack_status":{"docker":"skipped","k3s":"skipped","needs_init":false,"init_tier_recommendation":"native","can_auto_init":false}},"scan":{"engines":[{"type":"llamacpp","runtime":"native"}],"models":[],"central_connected":false,"configs_pulled":0,"benchmarks_pulled":0},"recommend":{"hardware_profile":"apple-m4-16g","gpu_arch":"","gpu_vram_mib":0,"gpu_count":0,"total_models_evaluated":2,"recommendations":[{"model_name":"qwen3-4b","model_type":"llm","family":"qwen3","parameter_count":"4B","fit_score":72,"hardware_fit":true,"recommendation_reason":"safe first run","engine":{"type":"llamacpp","name":"llamacpp"},"model_status":{"local_available":false}}]},"next_model":"qwen3-4b","next_command":"aima run qwen3-4b"}`), nil
	}

	root := NewRootCmd(app)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"onboarding", "--locale", "zh"})

	if err := root.Execute(); err != nil {
		t.Fatalf("onboarding failed: %v\n%s", err, buf.String())
	}
	if startCalls != 1 {
		t.Fatalf("start calls = %d, want 1", startCalls)
	}
	if gotLocale != "zh" {
		t.Fatalf("locale = %q, want zh", gotLocale)
	}
	output := buf.String()
	for _, want := range []string{
		"AIMA first-run guide",
		"Stack: docker=skipped k3s=skipped needs_init=false",
		"Next: aima run qwen3-4b",
		"Keep the local API/UI open with: aima serve",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("onboarding output missing %q:\n%s", want, output)
		}
	}
}

func TestOnboardingStartAliasRunsGuidedStart(t *testing.T) {
	app := testApp(t)
	app.ToolDeps.OnboardingStart = func(ctx context.Context, locale string) (json.RawMessage, error) {
		return json.RawMessage(`{"status":{"onboarding_completed":false,"hardware":{"profile_match":"","os":"linux","arch":"amd64","ram_mib":32768,"gpu":[]},"stack_status":{"docker":"not_installed","k3s":"not_installed","needs_init":true,"init_tier_recommendation":"docker","can_auto_init":false,"init_blocked_reason":"automatic init requires root"}},"scan":{"engines":[],"models":[],"central_connected":false},"recommend":{"total_models_evaluated":1,"recommendations":[{"model_name":"qwen3-4b","model_type":"llm","family":"qwen3","parameter_count":"4B","fit_score":68,"hardware_fit":true,"model_status":{"local_available":false}}]},"next_model":"qwen3-4b","next_command":"aima run qwen3-4b"}`), nil
	}

	root := NewRootCmd(app)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"onboarding", "start"})

	if err := root.Execute(); err != nil {
		t.Fatalf("onboarding start failed: %v\n%s", err, buf.String())
	}
	output := buf.String()
	if !strings.Contains(output, "Init blocked: automatic init requires root") {
		t.Fatalf("onboarding start output missing blocked reason:\n%s", output)
	}
	if !strings.Contains(output, "Manual init: sudo aima onboarding init --tier docker --yes") {
		t.Fatalf("onboarding start output missing manual init suggestion:\n%s", output)
	}
	if !strings.Contains(output, "Next: aima run qwen3-4b") {
		t.Fatalf("onboarding start output missing next command:\n%s", output)
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

func TestDiagnosticsExportCommand(t *testing.T) {
	app := testApp(t)
	var gotParams map[string]any
	app.ToolDeps.DiagnosticsExport = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		if err := json.Unmarshal(params, &gotParams); err != nil {
			t.Fatalf("unmarshal params: %v", err)
		}
		return json.RawMessage(`{"path":"/tmp/aima-diagnostics.json","telemetry_free":true}`), nil
	}

	root := NewRootCmd(app)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"diagnostics", "export", "--stdout", "--no-logs", "--log-lines", "5"})

	if err := root.Execute(); err != nil {
		t.Fatalf("diagnostics export failed: %v\n%s", err, buf.String())
	}
	if gotParams["inline"] != true || gotParams["include_logs"] != false || gotParams["log_lines"] != float64(5) {
		t.Fatalf("params = %#v, want inline=true include_logs=false log_lines=5", gotParams)
	}
	if !strings.Contains(buf.String(), `"telemetry_free": true`) {
		t.Fatalf("diagnostics output missing telemetry marker:\n%s", buf.String())
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

	expected := []string{"status", "override", "validate", "effective", "diff", "validate-patch"}
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
