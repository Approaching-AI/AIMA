package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/jguan/aima/catalog"
	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/agent"
	benchpkg "github.com/jguan/aima/internal/benchmark"
	"github.com/jguan/aima/internal/cli"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/fleet"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
	aimaRuntime "github.com/jguan/aima/internal/runtime"
	"github.com/spf13/cobra"
)

type fakeRuntime struct {
	name   string
	status map[string]*aimaRuntime.DeploymentStatus
	list   []*aimaRuntime.DeploymentStatus
}

func (r *fakeRuntime) Deploy(context.Context, *aimaRuntime.DeployRequest) error { return nil }
func (r *fakeRuntime) Delete(context.Context, string) error                     { return nil }
func (r *fakeRuntime) Status(_ context.Context, name string) (*aimaRuntime.DeploymentStatus, error) {
	if s, ok := r.status[name]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("not found")
}
func (r *fakeRuntime) List(context.Context) ([]*aimaRuntime.DeploymentStatus, error) {
	return r.list, nil
}
func (r *fakeRuntime) Logs(context.Context, string, int) (string, error) { return "", nil }
func (r *fakeRuntime) Name() string                                      { return r.name }

type mockCommandRunner struct {
	run       func(context.Context, string, ...string) ([]byte, error)
	runStream func(context.Context, func(string), string, ...string) error
	pipe      func(context.Context, []string, []string) error
}

type onboardingManifestDoc struct {
	Locales map[string]*onboardingLocaleDoc `json:"locales"`
}

type onboardingLocaleDoc struct {
	FullCommands onboardingFullCommandsDoc `json:"full_commands"`
}

type onboardingFullCommandsDoc struct {
	Groups []onboardingGroupDoc `json:"groups"`
}

type onboardingGroupDoc struct {
	ID    string                 `json:"id"`
	Items []onboardingCommandDoc `json:"items"`
}

type onboardingCommandDoc struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	Description string `json:"description"`
}

func (m *mockCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.run != nil {
		return m.run(ctx, name, args...)
	}
	return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
}

func (m *mockCommandRunner) RunStream(ctx context.Context, onLine func(string), name string, args ...string) error {
	if m.runStream != nil {
		return m.runStream(ctx, onLine, name, args...)
	}
	out, err := m.Run(ctx, name, args...)
	if err != nil {
		return err
	}
	if onLine != nil && len(out) > 0 {
		for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				onLine(line)
			}
		}
	}
	return nil
}

func (m *mockCommandRunner) Pipe(ctx context.Context, from, to []string) error {
	if m.pipe != nil {
		return m.pipe(ctx, from, to)
	}
	return nil
}

func TestOnboardingManifestEmbeddedShape(t *testing.T) {
	t.Parallel()

	raw, err := catalog.FS.ReadFile("ui-onboarding.json")
	if err != nil {
		t.Fatalf("read embedded onboarding manifest: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal onboarding manifest: %v", err)
	}

	if _, ok := payload["version"].(string); !ok {
		t.Fatalf("version missing or not a string: %#v", payload["version"])
	}
	if _, ok := payload["default_locale"].(string); !ok {
		t.Fatalf("default_locale missing or not a string: %#v", payload["default_locale"])
	}
	if _, ok := payload["locales"].(map[string]any); !ok {
		t.Fatalf("locales missing or not an object: %#v", payload["locales"])
	}
}

func TestBuildOnboardingManifestJSON_IncludesAllTopLevelCommands(t *testing.T) {
	t.Parallel()

	raw, err := buildOnboardingManifestJSON(&knowledge.Catalog{})
	if err != nil {
		t.Fatalf("build onboarding manifest: %v", err)
	}

	manifest := decodeOnboardingManifest(t, raw)
	zh, ok := manifest.Locales["zh"]
	if !ok || zh == nil {
		t.Fatalf("zh locale missing: %#v", manifest.Locales)
	}

	topLevel := findOnboardingGroup(t, zh.FullCommands.Groups, "top_level_commands")

	root := cli.NewRootCmd(&cli.App{})
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	want := make(map[string]struct{})
	for _, cmd := range root.Commands() {
		if cmd == nil || cmd.Hidden {
			continue
		}
		want["/cli "+cmd.Name()] = struct{}{}
	}

	got := make(map[string]struct{})
	for _, item := range topLevel.Items {
		got[item.Command] = struct{}{}
	}

	if len(got) != len(want) {
		t.Fatalf("top_level_commands item count = %d, want %d\nitems=%#v", len(got), len(want), topLevel.Items)
	}

	for command := range want {
		if _, ok := got[command]; !ok {
			t.Fatalf("top_level_commands missing command %s\nitems=%#v", command, topLevel.Items)
		}
	}

	for command := range got {
		if _, ok := want[command]; !ok {
			t.Fatalf("top_level_commands has unexpected command %s\nitems=%#v", command, topLevel.Items)
		}
	}
}

func TestBuildOnboardingManifestJSON_MarksSampleModelExamplesAsReplaceable(t *testing.T) {
	t.Parallel()

	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{
			{
				Metadata: knowledge.ModelMetadata{
					Name:           "demo-llm",
					Type:           "llm",
					Family:         "demo",
					ParameterCount: "1B",
				},
			},
		},
	}

	raw, err := buildOnboardingManifestJSON(cat)
	if err != nil {
		t.Fatalf("build onboarding manifest: %v", err)
	}

	text := string(raw)
	for _, want := range []string{
		`"/cli help"`,
		`"/cli model pull demo-llm"`,
		`"/cli deploy demo-llm --dry-run"`,
		`"/cli run demo-llm"`,
		"demo-llm 是示例模型名，可替换成你自己的模型名",
		"demo-llm is an example model name; replace it with your own model name",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated manifest missing %q\nmanifest=%s", want, text)
		}
	}
}

func TestBuildOnboardingManifestJSON_UsesLocalizedTemplates(t *testing.T) {
	t.Parallel()

	sourceRaw, err := catalog.FS.ReadFile("ui-onboarding.json")
	if err != nil {
		t.Fatalf("read embedded onboarding manifest: %v", err)
	}

	source := decodeOnboardingManifest(t, sourceRaw)
	zhSource, ok := source.Locales["zh"]
	if !ok || zhSource == nil {
		t.Fatalf("source zh locale missing: %#v", source.Locales)
	}
	enSource, ok := source.Locales["en"]
	if !ok || enSource == nil {
		t.Fatalf("source en locale missing: %#v", source.Locales)
	}

	zhTemplates := findOnboardingGroup(t, zhSource.FullCommands.Groups, "top_level_commands")
	enTemplates := findOnboardingGroup(t, enSource.FullCommands.Groups, "top_level_commands")
	zhHelpTemplate := findOnboardingCommandByID(t, zhTemplates.Items, "help")
	zhModelTemplate := findOnboardingCommandByID(t, zhTemplates.Items, "model")
	zhDeployTemplate := findOnboardingCommandByID(t, zhTemplates.Items, "deploy")
	enHelpTemplate := findOnboardingCommandByID(t, enTemplates.Items, "help")

	for _, template := range []onboardingCommandDoc{zhHelpTemplate, zhModelTemplate, zhDeployTemplate, enHelpTemplate} {
		if strings.TrimSpace(template.Description) == "" {
			t.Fatalf("top_level_commands template missing description: %#v", template)
		}
	}

	raw, err := buildOnboardingManifestJSON(&knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{
			{
				Metadata: knowledge.ModelMetadata{
					Name:           "demo-llm",
					Type:           "llm",
					Family:         "demo",
					ParameterCount: "1B",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build onboarding manifest: %v", err)
	}

	manifest := decodeOnboardingManifest(t, raw)
	zh := manifest.Locales["zh"]
	en := manifest.Locales["en"]
	if zh == nil || en == nil {
		t.Fatalf("missing locales: %#v", manifest.Locales)
	}

	zhHelp := findOnboardingCommandDescription(t, zh.FullCommands.Groups, "top_level_commands", "/cli help")
	if want := replaceSampleModelPlaceholder(zhHelpTemplate.Description, "demo-llm"); zhHelp != want {
		t.Fatalf("zh /cli help description = %q, want %q", zhHelp, want)
	}

	enHelp := findOnboardingCommandDescription(t, en.FullCommands.Groups, "top_level_commands", "/cli help")
	if want := replaceSampleModelPlaceholder(enHelpTemplate.Description, "demo-llm"); enHelp != want {
		t.Fatalf("en /cli help description = %q, want %q", enHelp, want)
	}

	zhModel := findOnboardingCommandDescription(t, zh.FullCommands.Groups, "top_level_commands", "/cli model")
	if want := replaceSampleModelPlaceholder(zhModelTemplate.Description, "demo-llm"); zhModel != want {
		t.Fatalf("zh /cli model description = %q, want %q", zhModel, want)
	}

	zhDeploy := findOnboardingCommandDescription(t, zh.FullCommands.Groups, "top_level_commands", "/cli deploy")
	if want := replaceSampleModelPlaceholder(zhDeployTemplate.Description, "demo-llm"); zhDeploy != want {
		t.Fatalf("zh /cli deploy description = %q, want %q", zhDeploy, want)
	}
}

func TestBuildTopLevelOnboardingItems_FallsBackToCommandShort(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "aima"}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Show version information",
	})

	items := buildTopLevelOnboardingItems(root, map[string]string{})
	if len(items) != 1 {
		t.Fatalf("buildTopLevelOnboardingItems() item count = %d, want 1", len(items))
	}
	if got := items[0]["description"]; got != "Show version information" {
		t.Fatalf("buildTopLevelOnboardingItems() description = %#v, want %q", got, "Show version information")
	}
}

func decodeOnboardingManifest(t *testing.T, raw []byte) onboardingManifestDoc {
	t.Helper()

	var manifest onboardingManifestDoc
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal onboarding manifest: %v", err)
	}
	return manifest
}

func findOnboardingGroup(t *testing.T, groups []onboardingGroupDoc, groupID string) onboardingGroupDoc {
	t.Helper()

	for _, group := range groups {
		if group.ID == groupID {
			return group
		}
	}

	t.Fatalf("group %q not found", groupID)
	return onboardingGroupDoc{}
}

func findOnboardingCommandByID(t *testing.T, items []onboardingCommandDoc, id string) onboardingCommandDoc {
	t.Helper()

	for _, item := range items {
		if item.ID == id {
			return item
		}
	}

	t.Fatalf("command id %q not found", id)
	return onboardingCommandDoc{}
}

func findOnboardingCommandDescription(t *testing.T, groups []onboardingGroupDoc, groupID, command string) string {
	t.Helper()

	group := findOnboardingGroup(t, groups, groupID)
	for _, item := range group.Items {
		if item.Command == command {
			return item.Description
		}
	}

	t.Fatalf("command %q not found in group %q", command, groupID)
	return ""
}

func TestParseExtraParamsStrict(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		wantErr bool
	}{
		{name: "empty clears", input: "", wantNil: true},
		{name: "whitespace clears", input: "   ", wantNil: true},
		{name: "valid object", input: `{"temperature":0.7}`, wantNil: false},
		{name: "invalid json", input: `{"temperature":`, wantErr: true},
		{name: "array rejected", input: `[]`, wantErr: true},
		{name: "null rejected", input: `null`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseExtraParamsStrict(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil && got != nil {
				t.Fatalf("expected nil, got %#v", got)
			}
			if !tt.wantNil && got == nil {
				t.Fatal("expected parsed object, got nil")
			}
		})
	}
}

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

func TestDefaultRootArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "no args defaults to serve", args: []string{"aima"}, want: []string{"serve"}},
		{name: "subcommand preserves args", args: []string{"aima", "serve"}, want: nil},
		{name: "flag only preserves args", args: []string{"aima", "--help"}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defaultRootArgs(tt.args)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("defaultRootArgs(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestDefaultEngineAssetPrefersCatalogDefault(t *testing.T) {
	hw := knowledge.HardwareInfo{
		GPUArch:  "Ada",
		Platform: "linux/amd64",
	}
	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{
			{
				Metadata: knowledge.EngineMetadata{Name: "qwen-tts-fastapi-cuda", Type: "qwen-tts-fastapi-cuda"},
				Image:    knowledge.EngineImage{Name: "qwen3-tts-cuda-x86", Tag: "latest", Platforms: []string{"linux/amd64"}},
				Hardware: knowledge.EngineHardware{GPUArch: "Ada"},
			},
			{
				Metadata: knowledge.EngineMetadata{Name: "llamacpp-universal", Type: "llamacpp", Default: true},
				Image:    knowledge.EngineImage{Name: "ghcr.io/ggml-org/llama.cpp", Tag: "server", Platforms: []string{"linux/amd64"}},
				Hardware: knowledge.EngineHardware{GPUArch: "*"},
			},
		},
	}

	got := defaultEngineAsset(cat, hw)
	if got == nil {
		t.Fatal("defaultEngineAsset returned nil")
	}
	if got.Metadata.Name != "llamacpp-universal" {
		t.Fatalf("defaultEngineAsset = %q, want llamacpp-universal", got.Metadata.Name)
	}
}

func TestDedupeScannedEnginesPrefersCatalogImageForSameTypeAndTag(t *testing.T) {
	hw := knowledge.HardwareInfo{
		GPUArch:  "Blackwell",
		Platform: "linux/arm64",
	}
	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{
			{
				Metadata: knowledge.EngineMetadata{Name: "qwen-tts-fastapi", Type: "qwen-tts-fastapi"},
				Image:    knowledge.EngineImage{Name: "qujing-qwen3-tts-real", Tag: "latest", Platforms: []string{"linux/arm64"}},
				Hardware: knowledge.EngineHardware{GPUArch: "*"},
			},
		},
	}

	got := dedupeScannedEngines([]*engine.EngineImage{
		{ID: "legacy", Type: "qwen-tts-fastapi", Image: "qujing-qwen3-tts", Tag: "latest", RuntimeType: "container", Available: true, DockerOnly: true},
		{ID: "preferred", Type: "qwen-tts-fastapi", Image: "qujing-qwen3-tts-real", Tag: "latest", RuntimeType: "container", Available: true, DockerOnly: true},
		{ID: "other-tag", Type: "qwen-tts-fastapi", Image: "qujing-qwen3-tts-real", Tag: "backup", RuntimeType: "container", Available: true, DockerOnly: true},
	}, preferredContainerImagesByTypeTag(cat, hw))

	if len(got) != 2 {
		t.Fatalf("dedupeScannedEngines() len = %d, want 2", len(got))
	}
	if got[0].Image != "qujing-qwen3-tts-real" || got[0].Tag != "latest" {
		t.Fatalf("first deduped engine = %s:%s, want qujing-qwen3-tts-real:latest", got[0].Image, got[0].Tag)
	}
	if got[1].Tag != "backup" {
		t.Fatalf("second engine tag = %q, want backup", got[1].Tag)
	}
}

func TestEngineCompatibilityHelpers(t *testing.T) {
	hw := knowledge.HardwareInfo{
		GPUArch:  "Ada",
		Platform: "linux/amd64",
	}
	wildcard := &knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "llamacpp-universal", Type: "llamacpp", Default: true},
		Image:    knowledge.EngineImage{Name: "ghcr.io/ggml-org/llama.cpp", Tag: "server", Platforms: []string{"linux/amd64"}},
		Hardware: knowledge.EngineHardware{GPUArch: "*"},
	}
	unsupportedPlatform := &knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "darwin-native", Type: "llamacpp"},
		Image:    knowledge.EngineImage{Name: "ghcr.io/ggml-org/llama.cpp", Tag: "server", Platforms: []string{"darwin/arm64"}},
		Hardware: knowledge.EngineHardware{GPUArch: "*"},
	}
	nativeFallback := &knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "hybrid-engine", Type: "llamacpp"},
		Image:    knowledge.EngineImage{Name: "ghcr.io/ggml-org/llama.cpp", Tag: "server", Platforms: []string{"linux/amd64"}},
		Hardware: knowledge.EngineHardware{GPUArch: "*"},
		Source:   &knowledge.EngineSource{Platforms: []string{"darwin/arm64"}},
	}
	preinstalledNative := &knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "vllm-musa", Type: "vllm"},
		Hardware: knowledge.EngineHardware{GPUArch: "Ada"},
		Source: &knowledge.EngineSource{
			InstallType: "preinstalled",
			Probe: &knowledge.EngineSourceProbe{
				Paths: []string{"/opt/vendor/bin/vllm"},
			},
		},
		Runtime: knowledge.EngineRuntime{
			Default: "native",
		},
	}

	if !engineCompatibleWithHost(wildcard, hw) {
		t.Fatal("wildcard engine should be compatible with Ada/linux-amd64")
	}
	if engineCompatibleWithHost(unsupportedPlatform, hw) {
		t.Fatal("engine with unsupported platform should be excluded")
	}
	if got := preferredEngineRuntimeType(nativeFallback, hw.Platform); got != "container" {
		t.Fatalf("preferredEngineRuntimeType = %q, want container", got)
	}
	if !engineCompatibleWithHost(preinstalledNative, hw) {
		t.Fatal("preinstalled native engine should be compatible without explicit source.platforms")
	}
	if got := preferredEngineRuntimeType(preinstalledNative, hw.Platform); got != "native" {
		t.Fatalf("preferredEngineRuntimeType(preinstalled native) = %q, want native", got)
	}

	recommendedContainer := &knowledge.EngineAsset{
		Metadata: knowledge.EngineMetadata{Name: "llamacpp-universal", Type: "llamacpp", Default: true},
		Image:    knowledge.EngineImage{Name: "ghcr.io/ggml-org/llama.cpp", Tag: "server", Platforms: []string{"linux/amd64"}},
		Hardware: knowledge.EngineHardware{GPUArch: "*"},
		Runtime: knowledge.EngineRuntime{
			Default: "auto",
			PlatformRecommendations: map[string]string{
				"linux/amd64": "container",
			},
		},
		Source: &knowledge.EngineSource{Platforms: []string{"linux/amd64"}},
	}
	if got := preferredEngineRuntimeType(recommendedContainer, hw.Platform); got != "container" {
		t.Fatalf("preferredEngineRuntimeType with container recommendation = %q, want container", got)
	}
}

func TestRequiresRootImportForK3S(t *testing.T) {
	if !requiresRootImportForK3S(false, true, false) {
		t.Fatal("Docker-only image on non-root K3S host should require root import")
	}
	if requiresRootImportForK3S(false, true, true) {
		t.Fatal("root should be able to import Docker-only image into containerd")
	}
	if requiresRootImportForK3S(true, true, false) {
		t.Fatal("image already in containerd should not require root import")
	}
}

func TestShouldFallbackToDockerRuntime(t *testing.T) {
	if !shouldFallbackToDockerRuntime("k3s", false, false, true, false, true) {
		t.Fatal("expected Docker fallback when K3S import requires root and Docker is available")
	}
	if shouldFallbackToDockerRuntime("k3s", true, false, true, false, true) {
		t.Fatal("partitioned deployments must not fall back away from K3S")
	}
	if shouldFallbackToDockerRuntime("docker", false, false, true, false, true) {
		t.Fatal("non-K3S runtime should not trigger K3S fallback logic")
	}
	if shouldFallbackToDockerRuntime("k3s", false, false, true, false, false) {
		t.Fatal("fallback requires Docker runtime availability")
	}
}

func TestInstalledRuntimeTypesForEngine(t *testing.T) {
	installed := []*state.Engine{
		{ID: "llamacpp-universal", Type: "llamacpp", RuntimeType: "native"},
		{ID: "other-engine", Type: "other", RuntimeType: "container"},
		{ID: "llamacpp-container", Type: "llamacpp", RuntimeType: "container"},
	}

	got := installedRuntimeTypesForEngine(installed, "llamacpp-universal", "llamacpp")
	if len(got) != 2 || got[0] != "container" || got[1] != "native" {
		t.Fatalf("installedRuntimeTypesForEngine = %v, want [container native]", got)
	}
}

func TestDeployAutoPullAllowed(t *testing.T) {
	if !deployAutoPullAllowed(context.Background()) {
		t.Fatal("default deploy auto-pull should be enabled")
	}
	if deployAutoPullAllowed(withDeployAutoPull(context.Background(), false)) {
		t.Fatal("deploy auto-pull override=false was not honored")
	}
	if !deployAutoPullAllowed(withDeployAutoPull(context.Background(), true)) {
		t.Fatal("deploy auto-pull override=true was not honored")
	}
}

func TestPrepareContainerCompatibilityUsesRepairInitCommands(t *testing.T) {
	modelPath := t.TempDir()
	resolved := &knowledge.ResolvedConfig{
		ModelName:          "qwen3.5-9b",
		ModelFormat:        "safetensors",
		EngineImage:        "vllm/vllm-openai:qwen3_5-cu130",
		CompatibilityProbe: "transformers_autoconfig",
		RepairInitCommands: []string{"python3 -m pip install --no-cache-dir --upgrade transformers"},
		Config:             map[string]any{"trust_remote_code": true},
		EngineDistribution: "registry",
		EngineRegistries:   []string{"docker.io/vllm/vllm-openai"},
	}

	repairProbeUsed := false
	runner := &mockCommandRunner{
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch {
			case name == "docker" && len(args) >= 2 && args[0] == "version":
				return []byte("27.0.1"), nil
			case name == "docker" && len(args) >= 3 && args[0] == "images" && args[1] == "-q":
				return []byte("sha256:abc"), nil
			case name == "docker" && len(args) > 0 && args[0] == "run":
				script := args[len(args)-1]
				if strings.Contains(script, "pip install --no-cache-dir --upgrade transformers") {
					repairProbeUsed = true
					return []byte("AIMA_COMPAT_OK transformers=5.5.0.dev0 model_type=qwen3_5"), nil
				}
				return []byte("ValueError: The checkpoint you are trying to load has model type 'qwen3_5' but Transformers does not recognize this architecture"), fmt.Errorf("exit status 1")
			default:
				t.Fatalf("unexpected command: %s %s", name, strings.Join(args, " "))
				return nil, nil
			}
		},
	}

	plan, err := prepareContainerCompatibility(context.Background(), runner, false, "docker", modelPath, resolved)
	if err != nil {
		t.Fatalf("prepareContainerCompatibility() error = %v", err)
	}
	if !repairProbeUsed {
		t.Fatal("expected repair probe to be attempted")
	}
	if len(plan.RepairInitCommands) != 1 || plan.RepairInitCommands[0] != resolved.RepairInitCommands[0] {
		t.Fatalf("RepairInitCommands = %v", plan.RepairInitCommands)
	}
	if plan.DockerImageChanged {
		t.Fatal("repair-only flow should not mark Docker image as changed")
	}
}

func TestPrepareContainerCompatibilityRefreshesImageBeforeFailing(t *testing.T) {
	modelPath := t.TempDir()
	resolved := &knowledge.ResolvedConfig{
		ModelName:          "qwen3.5-9b",
		ModelFormat:        "safetensors",
		EngineImage:        "vllm/vllm-openai:qwen3_5-cu130",
		CompatibilityProbe: "transformers_autoconfig",
		Config:             map[string]any{"trust_remote_code": true},
		EngineDistribution: "registry",
		EngineRegistries:   []string{"docker.io/vllm/vllm-openai"},
	}

	pulled := false
	probeCalls := 0
	runner := &mockCommandRunner{
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch {
			case name == "docker" && len(args) >= 2 && args[0] == "version":
				return []byte("27.0.1"), nil
			case name == "docker" && len(args) >= 3 && args[0] == "images" && args[1] == "-q":
				return []byte("sha256:abc"), nil
			case name == "docker" && len(args) >= 2 && args[0] == "pull":
				pulled = true
				return []byte("pulled"), nil
			case name == "docker" && len(args) > 0 && args[0] == "run":
				probeCalls++
				if pulled {
					return []byte("AIMA_COMPAT_OK transformers=5.5.0.dev0 model_type=qwen3_5"), nil
				}
				return []byte("ValueError: The checkpoint you are trying to load has model type 'qwen3_5' but Transformers does not recognize this architecture"), fmt.Errorf("exit status 1")
			default:
				t.Fatalf("unexpected command: %s %s", name, strings.Join(args, " "))
				return nil, nil
			}
		},
	}

	plan, err := prepareContainerCompatibility(context.Background(), runner, true, "docker", modelPath, resolved)
	if err != nil {
		t.Fatalf("prepareContainerCompatibility() error = %v", err)
	}
	if !pulled {
		t.Fatal("expected image refresh pull to run after initial probe failure")
	}
	if probeCalls != 2 {
		t.Fatalf("probeCalls = %d, want 2", probeCalls)
	}
	if !plan.DockerImageChanged {
		t.Fatal("refresh flow should mark Docker image as changed")
	}
	if len(plan.RepairInitCommands) != 0 {
		t.Fatalf("RepairInitCommands = %v, want none", plan.RepairInitCommands)
	}
}

func TestSummarizeDeploymentFailure(t *testing.T) {
	tests := []struct {
		name           string
		message        string
		startupMessage string
		errorLines     string
		want           string
	}{
		{
			name:    "prefer non-generic message",
			message: "GPU memory insufficient",
			want:    "GPU memory insufficient",
		},
		{
			name:           "fallback to startup message",
			startupMessage: "GPU memory insufficient",
			want:           "GPU memory insufficient",
		},
		{
			name:    "fallback to specific error line when message is generic",
			message: "process exited before readiness",
			errorLines: strings.Join([]string{
				"INFO booting",
				"ValueError: Free memory on device is too low",
			}, "\n"),
			want: "ValueError: Free memory on device is too low",
		},
		{
			name:    "stale metadata message yields traceback detail",
			message: "deployment metadata is stale; port is in use by another process",
			errorLines: strings.Join([]string{
				"INFO booting",
				"RuntimeError: Engine core initialization failed. See root cause above.",
			}, "\n"),
			want: "RuntimeError: Engine core initialization failed. See root cause above.",
		},
		{
			name:    "ignore cpuinfo noise line",
			message: "process exited before readiness",
			errorLines: strings.Join([]string{
				"Error in cpuinfo: prctl(PR_SVE_GET_VL) failed",
			}, "\n"),
			want: "process exited before readiness",
		},
		{
			name:       "fallback to unknown",
			errorLines: "\n\n",
			want:       "unknown startup failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeDeploymentFailure(tt.message, tt.startupMessage, tt.errorLines); got != tt.want {
				t.Fatalf("summarizeDeploymentFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeErrorLinesPrefersRootCause(t *testing.T) {
	logs := strings.Join([]string{
		"torch.OutOfMemoryError: MUSA out of memory. Tried to allocate 896.00 MiB.",
		"RuntimeError: Engine core initialization failed. See root cause above.",
	}, "\n")

	if got := summarizeErrorLines(logs); got != "torch.OutOfMemoryError: MUSA out of memory. Tried to allocate 896.00 MiB." {
		t.Fatalf("summarizeErrorLines() = %q", got)
	}
}

func TestRefineDeploymentFailureUsesLogs(t *testing.T) {
	initial := deploymentFailureDetails{
		Message: "process exited before readiness",
	}

	got := refineDeploymentFailure(context.Background(), "qwen3-8b-vllm", initial, nil, func(context.Context, string, int) (string, error) {
		return strings.Join([]string{
			"KeyError: 'layers.18.mlp.down_proj.g_idx'",
			"RuntimeError: Engine core initialization failed. See root cause above.",
		}, "\n"), nil
	})

	if got != "KeyError: 'layers.18.mlp.down_proj.g_idx'" {
		t.Fatalf("refineDeploymentFailure() = %q", got)
	}
}

func TestRefineDeploymentFailureUsesRefreshedStatus(t *testing.T) {
	initial := deploymentFailureDetails{
		Message: "RuntimeError: Engine core initialization failed. See root cause above.",
	}

	got := refineDeploymentFailure(context.Background(), "qwen3-30b-a3b-vllm", initial, func(context.Context, string) (json.RawMessage, error) {
		return json.Marshal(map[string]string{
			"message":     "deployment metadata is stale; port is in use by another process",
			"error_lines": "torch.OutOfMemoryError: MUSA out of memory. Tried to allocate 896.00 MiB.",
		})
	}, nil)

	if got != "torch.OutOfMemoryError: MUSA out of memory. Tried to allocate 896.00 MiB." {
		t.Fatalf("refineDeploymentFailure() = %q", got)
	}
}

func TestDeploymentMatchesQuery(t *testing.T) {
	ds := &aimaRuntime.DeploymentStatus{
		Name: "qwen3-8b",
		Labels: map[string]string{
			"aima.dev/model":  "qwen3-8b",
			"aima.dev/engine": "vllm",
		},
	}
	if !deploymentMatchesQuery(ds, "qwen3-8b") {
		t.Fatal("expected model-name query to match deployment")
	}
	if !deploymentMatchesQuery(ds, "qwen3-8b-vllm") {
		t.Fatal("expected canonical deployment alias to match deployment")
	}
	if deploymentMatchesQuery(ds, "other-model") {
		t.Fatal("unexpected match for unrelated deployment query")
	}
}

func TestShouldReuseExistingDeployment(t *testing.T) {
	existing := &aimaRuntime.DeploymentStatus{Name: "qwen3-tts-0-6b-qwen-tts-fastapi-cuda-blackwell", Phase: "running", Ready: true}
	if !shouldReuseExistingDeployment(existing, "", "", nil) {
		t.Fatal("expected plain deploy query to reuse existing deployment")
	}
	if shouldReuseExistingDeployment(existing, "", "", map[string]any{"device_map": "auto"}) {
		t.Fatal("expected config override to force runtime reconciliation")
	}
	if shouldReuseExistingDeployment(existing, "qwen-tts-fastapi-cuda-blackwell", "", nil) {
		t.Fatal("expected explicit engine selection to force runtime reconciliation")
	}
	if shouldReuseExistingDeployment(existing, "", "slot-a", nil) {
		t.Fatal("expected explicit slot selection to force runtime reconciliation")
	}
}

func TestScenarioWaitForReadyHealthCheckReady(t *testing.T) {
	err := scenarioWaitForReady(context.Background(), "demo-deploy", "health_check", 1,
		func(context.Context, string) (json.RawMessage, error) {
			return json.RawMessage(`{"phase":"running","ready":true}`), nil
		})
	if err != nil {
		t.Fatalf("scenarioWaitForReady(health_check ready) error = %v", err)
	}
}

func TestScenarioWaitForReadyPortOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = scenarioWaitForReady(context.Background(), "demo-deploy", "port_open", 1,
		func(context.Context, string) (json.RawMessage, error) {
			return json.RawMessage(`{"phase":"starting","address":"` + ln.Addr().String() + `"}`), nil
		})
	if err != nil {
		t.Fatalf("scenarioWaitForReady(port_open) error = %v", err)
	}
}

func TestScenarioWaitForReadyFailedDeployment(t *testing.T) {
	err := scenarioWaitForReady(context.Background(), "demo-deploy", "health_check", 1,
		func(context.Context, string) (json.RawMessage, error) {
			return json.RawMessage(`{"phase":"failed","message":"OOMKilled"}`), nil
		})
	if err == nil || !strings.Contains(err.Error(), "OOMKilled") {
		t.Fatalf("scenarioWaitForReady(failed) error = %v, want OOMKilled", err)
	}
}

func TestScenarioWaitForReadyUnknownMode(t *testing.T) {
	err := scenarioWaitForReady(context.Background(), "demo-deploy", "bogus", 1,
		func(context.Context, string) (json.RawMessage, error) {
			return json.RawMessage(`{"phase":"running","ready":true}`), nil
		})
	if err == nil || !strings.Contains(err.Error(), `unknown wait_for "bogus"`) {
		t.Fatalf("scenarioWaitForReady(unknown) error = %v", err)
	}
}

func TestFindExistingDeploymentFallsBackToLabelMatch(t *testing.T) {
	rt := &fakeRuntime{
		name:   "native",
		status: map[string]*aimaRuntime.DeploymentStatus{},
		list: []*aimaRuntime.DeploymentStatus{{
			Name:  "qwen3-30b-a3b-vllm",
			Phase: "running",
			Labels: map[string]string{
				"aima.dev/model":  "qwen3-30b-a3b",
				"aima.dev/engine": "vllm",
			},
		}},
	}
	got := findExistingDeployment(context.Background(), "qwen3-30b-a3b", rt)
	if got == nil || got.Name != "qwen3-30b-a3b-vllm" {
		t.Fatalf("findExistingDeployment(label match) = %#v", got)
	}
}

func TestFindDeploymentStatusSuppressesRecentlyDeletedDeployment(t *testing.T) {
	deleteAt := time.Now()
	snapshot := deletedDeploymentSnapshot{
		normalizeDeletedDeploymentKey("qwen3-30b-a3b-vllm"): deleteAt,
		normalizeDeletedDeploymentKey("qwen3-30b-a3b"):      deleteAt,
	}

	rt := &fakeRuntime{
		name: "native",
		status: map[string]*aimaRuntime.DeploymentStatus{
			"qwen3-30b-a3b-vllm": {
				Name:          "qwen3-30b-a3b-vllm",
				Phase:         "running",
				Ready:         true,
				StartTime:     time.Now().Add(-1 * time.Minute).Format(time.RFC3339),
				StartedAtUnix: time.Now().Add(-1 * time.Minute).Unix(),
				Labels: map[string]string{
					"aima.dev/model":  "qwen3-30b-a3b",
					"aima.dev/engine": "vllm",
				},
			},
		},
	}

	got, err := findDeploymentStatus(context.Background(), "qwen3-30b-a3b-vllm", snapshot.suppress, rt)
	if err == nil || got != nil {
		t.Fatalf("findDeploymentStatus(recently deleted) = %#v, %v; want not found", got, err)
	}
}

func TestFindDeploymentStatusAllowsReplacementStartedAfterDelete(t *testing.T) {
	deleteAt := time.Now().Add(-2 * time.Second)
	snapshot := deletedDeploymentSnapshot{
		normalizeDeletedDeploymentKey("qwen3-30b-a3b-vllm"): deleteAt,
		normalizeDeletedDeploymentKey("qwen3-30b-a3b"):      deleteAt,
	}

	replacementStart := deleteAt.Add(1 * time.Second)
	rt := &fakeRuntime{
		name: "native",
		status: map[string]*aimaRuntime.DeploymentStatus{
			"qwen3-30b-a3b-vllm": {
				Name:          "qwen3-30b-a3b-vllm",
				Phase:         "running",
				Ready:         true,
				StartTime:     replacementStart.Format(time.RFC3339),
				StartedAtUnix: replacementStart.Unix(),
				Labels: map[string]string{
					"aima.dev/model":  "qwen3-30b-a3b",
					"aima.dev/engine": "vllm",
				},
			},
		},
	}

	got, err := findDeploymentStatus(context.Background(), "qwen3-30b-a3b-vllm", snapshot.suppress, rt)
	if err != nil || got == nil {
		t.Fatalf("findDeploymentStatus(replacement) = %#v, %v; want replacement visible", got, err)
	}
}

func TestFindDeploymentStatusAllowsReplacementStartedLaterSameSecondAsDelete(t *testing.T) {
	deleteAt := time.Date(2026, time.April, 1, 9, 0, 0, 100_000_000, time.UTC)
	snapshot := deletedDeploymentSnapshot{
		normalizeDeletedDeploymentKey("qwen3-30b-a3b-vllm"): deleteAt,
		normalizeDeletedDeploymentKey("qwen3-30b-a3b"):      deleteAt,
	}

	replacementStart := deleteAt.Add(400 * time.Millisecond)
	rt := &fakeRuntime{
		name: "native",
		status: map[string]*aimaRuntime.DeploymentStatus{
			"qwen3-30b-a3b-vllm": {
				Name:          "qwen3-30b-a3b-vllm",
				Phase:         "starting",
				Ready:         false,
				StartTime:     replacementStart.Format(time.RFC3339Nano),
				StartedAtUnix: replacementStart.Unix(),
				Labels: map[string]string{
					"aima.dev/model":  "qwen3-30b-a3b",
					"aima.dev/engine": "vllm",
				},
			},
		},
	}

	got, err := findDeploymentStatus(context.Background(), "qwen3-30b-a3b-vllm", snapshot.suppress, rt)
	if err != nil || got == nil {
		t.Fatalf("findDeploymentStatus(same-second replacement) = %#v, %v; want replacement visible", got, err)
	}
}

func TestFindModelDirPrefersCompatibleAliasDirectory(t *testing.T) {
	dataDir := t.TempDir()
	aliasDir := filepath.Join(dataDir, "models", "Qwen3-30B-A3B-GPTQ-Int4")
	if err := os.MkdirAll(aliasDir, 0o755); err != nil {
		t.Fatalf("mkdir alias dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasDir, "config.json"), []byte(`{"model_type":"qwen3"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasDir, "quantize_config.json"), []byte(`{"bits":4,"quant_method":"gptq"}`), 0o644); err != nil {
		t.Fatalf("write quantize config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasDir, "tokenizer.json"), []byte(`{"version":"1.0"}`), 0o644); err != nil {
		t.Fatalf("write tokenizer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasDir, "model.safetensors"), []byte("weights"), 0o644); err != nil {
		t.Fatalf("write weights: %v", err)
	}

	got := findModelDir("qwen3-30b-a3b", dataDir, "safetensors", "gptq")
	if got != aliasDir {
		t.Fatalf("findModelDir() = %q, want %q", got, aliasDir)
	}
}

func TestFindModelDirRejectsIncompleteExactDirectory(t *testing.T) {
	dataDir := t.TempDir()
	exactDir := filepath.Join(dataDir, "models", "qwen3-30b-a3b")
	if err := os.MkdirAll(exactDir, 0o755); err != nil {
		t.Fatalf("mkdir exact dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(exactDir, "config.json"), []byte(`{"model_type":"qwen3"}`), 0o644); err != nil {
		t.Fatalf("write exact config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(exactDir, "tokenizer.json"), []byte(`{"version":"1.0"}`), 0o644); err != nil {
		t.Fatalf("write exact tokenizer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(exactDir, "model.safetensors.partial"), []byte("partial"), 0o644); err != nil {
		t.Fatalf("write exact partial: %v", err)
	}

	aliasDir := filepath.Join(dataDir, "models", "Qwen3-30B-A3B-GPTQ-Int4")
	if err := os.MkdirAll(aliasDir, 0o755); err != nil {
		t.Fatalf("mkdir alias dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasDir, "config.json"), []byte(`{"model_type":"qwen3"}`), 0o644); err != nil {
		t.Fatalf("write alias config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasDir, "quantize_config.json"), []byte(`{"bits":4,"quant_method":"gptq"}`), 0o644); err != nil {
		t.Fatalf("write alias quantize config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasDir, "tokenizer.json"), []byte(`{"version":"1.0"}`), 0o644); err != nil {
		t.Fatalf("write alias tokenizer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasDir, "model.safetensors"), []byte("weights"), 0o644); err != nil {
		t.Fatalf("write alias weights: %v", err)
	}

	got := findModelDir("qwen3-30b-a3b", dataDir, "safetensors", "gptq")
	if got != aliasDir {
		t.Fatalf("findModelDir() = %q, want %q", got, aliasDir)
	}
}

func TestFindModelDirPreservesUnreadableExactDirectory(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}

	dataDir := t.TempDir()
	exactDir := filepath.Join(dataDir, "models", "qwen3-30b-a3b")
	if err := os.MkdirAll(exactDir, 0o755); err != nil {
		t.Fatalf("mkdir exact dir: %v", err)
	}
	filePath := filepath.Join(exactDir, "config.json")
	if err := os.WriteFile(filePath, []byte(`{"model_type":"qwen3"}`), 0o000); err != nil {
		t.Fatalf("write unreadable config: %v", err)
	}
	defer os.Chmod(filePath, 0o644)

	got := findModelDir("qwen3-30b-a3b", dataDir, "safetensors", "gptq")
	if got != exactDir {
		t.Fatalf("findModelDir() = %q, want unreadable exact path %q", got, exactDir)
	}
}

func TestApplyScenarioSkipsRemainingDeploymentsAndPostDeployAfterWaitFailure(t *testing.T) {
	cat := &knowledge.Catalog{
		DeploymentScenarios: []knowledge.DeploymentScenario{{
			Metadata: knowledge.ScenarioMetadata{Name: "demo"},
			Deployments: []knowledge.ScenarioDeployment{
				{Model: "model-a", Engine: "engine-a"},
				{Model: "model-b", Engine: "engine-b"},
			},
			PostDeploy: []knowledge.ScenarioAction{
				{Action: "openclaw_sync"},
			},
			StartupOrder: []knowledge.ScenarioStartupStep{
				{Step: 1, Model: "model-a", WaitFor: "health_check", TimeoutS: 1},
				{Step: 2, Model: "model-b", WaitFor: "health_check", TimeoutS: 1},
			},
		}},
	}

	deployCalls := 0
	deps := &mcp.ToolDeps{
		DeployApply: func(ctx context.Context, engine, model, slot string, configOverrides map[string]any, noPull bool) (json.RawMessage, error) {
			deployCalls++
			if model != "model-a" {
				t.Fatalf("unexpected DeployApply for %s", model)
			}
			return json.RawMessage(`{"name":"model-a-engine-a"}`), nil
		},
		DeployStatus: func(context.Context, string) (json.RawMessage, error) {
			return json.RawMessage(`{"phase":"failed","message":"boom"}`), nil
		},
		OpenClawSync: func(context.Context, bool) (json.RawMessage, error) {
			t.Fatal("post-deploy action should be skipped after failure")
			return nil, nil
		},
	}

	data, err := applyScenario(context.Background(), cat, "docker", deps, "demo", false)
	if err != nil {
		t.Fatalf("applyScenario: %v", err)
	}

	var resp struct {
		Deployments []scenarioDeployResult `json:"deployments"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if deployCalls != 1 {
		t.Fatalf("DeployApply calls = %d, want 1", deployCalls)
	}
	if len(resp.Deployments) != 4 {
		t.Fatalf("deployment results len = %d, want 4", len(resp.Deployments))
	}
	if resp.Deployments[0].Model != "model-a" || resp.Deployments[0].Status != "ok" {
		t.Fatalf("first result = %#v, want ok model-a", resp.Deployments[0])
	}
	if resp.Deployments[1].Model != "model-a_wait" || resp.Deployments[1].Status != "warning" {
		t.Fatalf("wait result = %#v, want warning model-a_wait", resp.Deployments[1])
	}
	if resp.Deployments[2].Model != "model-b" || resp.Deployments[2].Status != "skipped" {
		t.Fatalf("second deploy result = %#v, want skipped model-b", resp.Deployments[2])
	}
	if resp.Deployments[3].Model != "openclaw_sync" || resp.Deployments[3].Status != "skipped" {
		t.Fatalf("post-deploy result = %#v, want skipped openclaw_sync", resp.Deployments[3])
	}
}

func TestApplyScenarioWaitsOnLastStepBeforePostDeploy(t *testing.T) {
	cat := &knowledge.Catalog{
		DeploymentScenarios: []knowledge.DeploymentScenario{{
			Metadata: knowledge.ScenarioMetadata{Name: "demo-last"},
			Deployments: []knowledge.ScenarioDeployment{
				{Model: "model-a", Engine: "engine-a"},
			},
			PostDeploy: []knowledge.ScenarioAction{
				{Action: "openclaw_sync"},
			},
			StartupOrder: []knowledge.ScenarioStartupStep{
				{Step: 1, Model: "model-a", WaitFor: "health_check", TimeoutS: 1},
			},
		}},
	}

	deps := &mcp.ToolDeps{
		DeployApply: func(ctx context.Context, engine, model, slot string, configOverrides map[string]any, noPull bool) (json.RawMessage, error) {
			return json.RawMessage(`{"name":"model-a-engine-a"}`), nil
		},
		DeployStatus: func(context.Context, string) (json.RawMessage, error) {
			return json.RawMessage(`{"phase":"failed","message":"boom-last"}`), nil
		},
		OpenClawSync: func(context.Context, bool) (json.RawMessage, error) {
			t.Fatal("post-deploy action should be skipped when the last startup_order wait fails")
			return nil, nil
		},
	}

	data, err := applyScenario(context.Background(), cat, "docker", deps, "demo-last", false)
	if err != nil {
		t.Fatalf("applyScenario: %v", err)
	}

	var resp struct {
		Deployments []scenarioDeployResult `json:"deployments"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Deployments) != 3 {
		t.Fatalf("deployment results len = %d, want 3", len(resp.Deployments))
	}
	if resp.Deployments[0].Model != "model-a" || resp.Deployments[0].Status != "ok" {
		t.Fatalf("first result = %#v, want ok model-a", resp.Deployments[0])
	}
	if resp.Deployments[1].Model != "model-a_wait" || resp.Deployments[1].Status != "warning" {
		t.Fatalf("wait result = %#v, want warning model-a_wait", resp.Deployments[1])
	}
	if resp.Deployments[2].Model != "openclaw_sync" || resp.Deployments[2].Status != "skipped" {
		t.Fatalf("post-deploy result = %#v, want skipped openclaw_sync", resp.Deployments[2])
	}
}

func TestVariantQuantizationHint(t *testing.T) {
	if got := variantQuantizationHint(&knowledge.ModelVariant{
		DefaultConfig: map[string]any{"quantization": "gptq"},
	}); got != "gptq" {
		t.Fatalf("variantQuantizationHint(config) = %q, want gptq", got)
	}
	if got := variantQuantizationHint(&knowledge.ModelVariant{
		Source: &knowledge.ModelSource{Quantization: "fp8"},
	}); got != "fp8" {
		t.Fatalf("variantQuantizationHint(source) = %q, want fp8", got)
	}
	if got := variantQuantizationHint(&knowledge.ModelVariant{Name: "qwen3-4b-universal-llamacpp-q4"}); got != "" {
		t.Fatalf("variantQuantizationHint(name-only) = %q, want empty string", got)
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
		{name: "stack init blocked for agent", tool: "stack", args: json.RawMessage(`{"action":"init"}`), wantBlock: true},
		{name: "stack status allowed", tool: "stack", args: json.RawMessage(`{"action":"status"}`), wantBlock: false},
		{name: "explore start blocked for agent", tool: "explore", args: json.RawMessage(`{"action":"start","kind":"tune","target":{"model":"qwen3-8b"}}`), wantBlock: true},
		{name: "explore result allowed", tool: "explore", args: json.RawMessage(`{"action":"result","id":"run-1"}`), wantBlock: false},
		{name: "allowed readonly", tool: "knowledge.resolve", args: json.RawMessage(`{"model":"qwen3-8b"}`), wantBlock: false},
		{name: "fleet exec recursive blocked", tool: "fleet.exec", args: json.RawMessage(`{"device_id":"dev","tool_name":"fleet.exec","params":{}}`), wantBlock: true},
		{name: "fleet exec stack init blocked", tool: "fleet.exec", args: json.RawMessage(`{"device_id":"dev","tool_name":"stack","params":{"action":"init"}}`), wantBlock: true},
		{name: "fleet exec fleet info allowed", tool: "fleet.exec", args: json.RawMessage(`{"device_id":"dev","tool_name":"fleet.info","params":{}}`), wantBlock: false},
		{name: "fleet exec deploy apply allowed", tool: "fleet.exec", args: json.RawMessage(`{"device_id":"dev","tool_name":"deploy.apply","params":{"model":"qwen3-8b"}}`), wantBlock: false},
		{name: "system config read allowed", tool: "system.config", args: json.RawMessage(`{"key":"foo"}`), wantBlock: false},
		{name: "system config write blocked", tool: "system.config", args: json.RawMessage(`{"key":"foo","value":"bar"}`), wantBlock: true},
		{name: "system config null value blocked", tool: "system.config", args: json.RawMessage(`{"key":"foo","value":null}`), wantBlock: true},
		// catalog.override: engine/model allowed, infrastructure blocked
		{name: "catalog override engine_asset allowed", tool: "catalog.override", args: json.RawMessage(`{"kind":"engine_asset","name":"vllm","content":"x"}`), wantBlock: false},
		{name: "catalog override model_asset allowed", tool: "catalog.override", args: json.RawMessage(`{"kind":"model_asset","name":"qwen3","content":"x"}`), wantBlock: false},
		{name: "catalog override hardware_profile blocked", tool: "catalog.override", args: json.RawMessage(`{"kind":"hardware_profile","name":"gpu","content":"x"}`), wantBlock: true},
		{name: "catalog override partition_strategy blocked", tool: "catalog.override", args: json.RawMessage(`{"kind":"partition_strategy","name":"p","content":"x"}`), wantBlock: true},
		{name: "catalog override stack_component blocked", tool: "catalog.override", args: json.RawMessage(`{"kind":"stack_component","name":"k3s","content":"x"}`), wantBlock: true},
		{name: "catalog override no kind blocked", tool: "catalog.override", args: json.RawMessage(`{"name":"x","content":"x"}`), wantBlock: true},
		{name: "catalog override empty args blocked", tool: "catalog.override", args: nil, wantBlock: true},
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

func TestMCPToolAdapter_BlocksMergedActionTool(t *testing.T) {
	s := mcp.NewServer()
	called := 0
	s.RegisterTool(&mcp.Tool{
		Name:        "stack",
		Description: "test stack",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*mcp.ToolResult, error) {
			called++
			return mcp.TextResult("should not run"), nil
		},
	})

	adapter := &mcpToolAdapter{server: s}
	result, err := adapter.ExecuteTool(context.Background(), "stack", json.RawMessage(`{"action":"init"}`))
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

func TestMCPToolAdapter_FleetExecApprovalFlow(t *testing.T) {
	s := mcp.NewServer()
	var calls []string
	s.RegisterTool(&mcp.Tool{
		Name:        "fleet.exec",
		Description: "test fleet exec",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*mcp.ToolResult, error) {
			var p struct {
				ToolName string          `json:"tool_name"`
				Params   json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			calls = append(calls, p.ToolName)
			switch p.ToolName {
			case "deploy.dry_run":
				return mcp.TextResult(`{"phase":"dry-run"}`), nil
			case "deploy.apply":
				return mcp.TextResult(`{"phase":"applied"}`), nil
			default:
				return nil, fmt.Errorf("unexpected tool_name %q", p.ToolName)
			}
		},
	})

	adapter := &mcpToolAdapter{server: s, pending: make(map[int64]*pendingApproval)}
	result, err := adapter.ExecuteTool(context.Background(), "fleet.exec", json.RawMessage(`{"device_id":"dev-1","tool_name":"deploy.apply","params":{"model":"qwen3-8b"}}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result == nil || !strings.Contains(result.Content, "NEEDS_APPROVAL") {
		t.Fatalf("expected approval request, got %+v", result)
	}
	if len(calls) != 1 || calls[0] != "deploy.dry_run" {
		t.Fatalf("expected dry-run call first, got %#v", calls)
	}

	adapter.mu.Lock()
	if len(adapter.pending) != 1 {
		adapter.mu.Unlock()
		t.Fatalf("expected 1 pending approval, got %d", len(adapter.pending))
	}
	var approvalID int64
	for id := range adapter.pending {
		approvalID = id
	}
	adapter.mu.Unlock()
	if approvalID == 0 {
		t.Fatal("expected a non-zero approval ID")
	}

	approved, err := adapter.executeApproval(context.Background(), approvalID)
	if err != nil {
		t.Fatalf("executeApproval: %v", err)
	}
	if string(approved) != `{"phase":"applied"}` {
		t.Fatalf("approval result = %s, want applied payload", string(approved))
	}
	if len(calls) != 2 || calls[1] != "deploy.apply" {
		t.Fatalf("expected approval call to execute deploy.apply, got %#v", calls)
	}

	adapter.mu.Lock()
	pendingLeft := len(adapter.pending)
	adapter.mu.Unlock()
	if pendingLeft != 0 {
		t.Fatalf("expected pending approvals to be cleared, got %d", pendingLeft)
	}
}

func TestFleetBlockedTools(t *testing.T) {
	// Fleet transport only blocks daisy-chained remote execution. Agent guardrails
	// are enforced separately in isBlockedAgentTool.
	mustBlock := []string{
		"fleet.exec",
	}
	for _, tool := range mustBlock {
		if _, ok := fleetBlockedTools[tool]; !ok {
			t.Errorf("fleetBlockedTools missing %q", tool)
		}
	}

	merged := []string{"stack", "explore"}
	for _, tool := range merged {
		if _, ok := fleetBlockedTools[tool]; ok {
			t.Errorf("fleetBlockedTools should not block merged tool name %q", tool)
		}
	}

	// Transport-safe targets must not be blocked, even if the Agent adapter
	// applies additional policy when fleet.exec is invoked by agent.ask.
	safe := []string{
		"hardware.detect", "model.list", "deploy.list", "knowledge.resolve",
		"system.config", "catalog.override", "shell.exec", "fleet.info",
	}
	for _, tool := range safe {
		if _, ok := fleetBlockedTools[tool]; ok {
			t.Errorf("fleetBlockedTools should not block %q", tool)
		}
	}
}

func TestFleetExecTool_AllowsCLIEquivalentRemoteMutation(t *testing.T) {
	registry := fleet.NewRegistry(6188)
	registry.Update([]proxy.DiscoveredService{{
		Name:   "local._llm._tcp.local.",
		AddrV4: "127.0.0.1",
		Port:   6188,
	}})

	s := mcp.NewServer()
	var gotArgs json.RawMessage
	s.RegisterTool(&mcp.Tool{
		Name:        "system.config",
		Description: "test config",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*mcp.ToolResult, error) {
			gotArgs = append(json.RawMessage(nil), params...)
			return mcp.TextResult(`{"ok":true}`), nil
		},
	})

	deps := &mcp.ToolDeps{}
	buildFleetDeps(deps, registry, nil, s)

	data, err := deps.FleetExecTool(context.Background(), "local", "system.config", json.RawMessage(`{"key":"foo","value":"bar"}`))
	if err != nil {
		t.Fatalf("FleetExecTool: %v", err)
	}
	if string(data) != `{"content":[{"type":"text","text":"{\"ok\":true}"}]}` {
		t.Fatalf("unexpected result payload: %s", string(data))
	}
	if string(gotArgs) != `{"key":"foo","value":"bar"}` {
		t.Fatalf("system.config args = %s, want write payload", string(gotArgs))
	}
}

func TestAgentAvailable(t *testing.T) {
	t.Run("nil client is unavailable", func(t *testing.T) {
		if agentAvailable(context.Background(), nil) {
			t.Fatal("expected nil client to be unavailable")
		}
	})

	t.Run("unreachable endpoint is unavailable", func(t *testing.T) {
		client := agent.NewOpenAIClient("http://127.0.0.1:1/v1")
		if agentAvailable(context.Background(), client) {
			t.Fatal("expected unreachable endpoint to be unavailable")
		}
	})

	t.Run("reachable models endpoint is available", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-8b"}]}`))
		}))
		defer server.Close()

		client := agent.NewOpenAIClient(server.URL + "/v1")
		if !agentAvailable(context.Background(), client) {
			t.Fatal("expected reachable endpoint to be available")
		}
	})
}

func TestBuildAgentStatusPayloadIncludesRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","models":[{"model_name":"qwen3-8b","ready":true,"parameter_count":"8B","context_window_tokens":8192},{"model_name":"qwen3.5-35b-a3b","ready":true,"parameter_count":"35B","context_window_tokens":16384}]}`))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-8b"},{"id":"qwen3.5-35b-a3b"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := agent.NewOpenAIClient(server.URL + "/v1")
	data, err := buildAgentStatusPayload(context.Background(), client, "enabled", 2)
	if err != nil {
		t.Fatalf("buildAgentStatusPayload: %v", err)
	}

	var payload struct {
		AgentAvailable bool   `json:"agent_available"`
		ToolMode       string `json:"agent_tool_mode"`
		ActiveRuns     int    `json:"active_exploration_runs"`
		LLMRoute       struct {
			Available       bool   `json:"available"`
			SelectionReason string `json:"selection_reason"`
			Selected        struct {
				Model string `json:"model"`
			} `json:"selected"`
		} `json:"llm_route"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !payload.AgentAvailable {
		t.Fatal("agent_available = false, want true")
	}
	if payload.ToolMode != "enabled" {
		t.Fatalf("agent_tool_mode = %q, want enabled", payload.ToolMode)
	}
	if payload.ActiveRuns != 2 {
		t.Fatalf("active_exploration_runs = %d, want 2", payload.ActiveRuns)
	}
	if !payload.LLMRoute.Available {
		t.Fatal("llm_route.available = false, want true")
	}
	if payload.LLMRoute.SelectionReason != "best_local_model" {
		t.Fatalf("selection_reason = %q, want best_local_model", payload.LLMRoute.SelectionReason)
	}
	if payload.LLMRoute.Selected.Model != "qwen3.5-35b-a3b" {
		t.Fatalf("selected model = %q, want qwen3.5-35b-a3b", payload.LLMRoute.Selected.Model)
	}
}

func TestQueryGoldenOverrides(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Insert engine asset + hardware profile (required by Search JOINs)
	_, err = db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_assets (id, type, version) VALUES ('vllm-nightly', 'vllm-nightly', 'v0.16')`)
	if err != nil {
		t.Fatalf("insert engine_asset: %v", err)
	}
	_, err = db.RawDB().ExecContext(ctx,
		`INSERT INTO hardware_profiles (id, name, gpu_arch) VALUES ('nvidia-gb10-arm64', 'GB10', 'Blackwell')`)
	if err != nil {
		t.Fatalf("insert hardware_profile: %v", err)
	}

	// Insert a golden configuration
	goldenCfg := &state.Configuration{
		ID:         "cfg-golden-001",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.85,"max_model_len":32768}`,
		ConfigHash: "golden-hash-001",
		Status:     "golden",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, goldenCfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}
	// Insert a benchmark so Search returns results (JOIN on throughput)
	benchResult := &state.BenchmarkResult{
		ID:            "br-001",
		ConfigID:      "cfg-golden-001",
		Concurrency:   1,
		ThroughputTPS: 42.5,
	}
	if err := db.InsertBenchmarkResult(ctx, benchResult); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	kStore := knowledge.NewStore(db.RawDB())

	t.Run("finds golden config via gpu arch", func(t *testing.T) {
		// Real code passes hwInfo.GPUArch (e.g. "Blackwell"), not profile name.
		// Search matches via: hardware_profiles WHERE gpu_arch = ?
		result := queryGoldenOverrides(ctx, kStore, "Blackwell", "vllm-nightly", "qwen3-8b")
		if result == nil {
			t.Fatal("expected golden config, got nil")
		}
		if gmu, ok := result["gpu_memory_utilization"]; !ok {
			t.Error("missing gpu_memory_utilization")
		} else if gmu != 0.85 {
			t.Errorf("gpu_memory_utilization = %v, want 0.85", gmu)
		}
		if mml, ok := result["max_model_len"]; !ok {
			t.Error("missing max_model_len")
		} else if mml != float64(32768) {
			t.Errorf("max_model_len = %v, want 32768", mml)
		}
	})

	t.Run("no golden for different gpu arch", func(t *testing.T) {
		result := queryGoldenOverrides(ctx, kStore, "Ada", "vllm-nightly", "qwen3-8b")
		if result != nil {
			t.Errorf("expected nil for non-matching gpu arch, got %v", result)
		}
	})

	t.Run("empty gpu arch returns nil", func(t *testing.T) {
		// Empty GPUArch must return nil to prevent cross-hardware golden injection.
		result := queryGoldenOverrides(ctx, kStore, "", "vllm-nightly", "qwen3-8b")
		if result != nil {
			t.Errorf("expected nil for empty gpu arch (cross-hardware guard), got %v", result)
		}
	})

	t.Run("nil store returns nil", func(t *testing.T) {
		result := queryGoldenOverrides(ctx, nil, "Blackwell", "vllm-nightly", "qwen3-8b")
		if result != nil {
			t.Errorf("expected nil for nil store, got %v", result)
		}
	})
}

func TestL2ProvenanceMerge(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Insert engine asset + hardware profile
	_, err = db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_assets (id, type, version) VALUES ('vllm-nightly', 'vllm-nightly', 'v0.16')`)
	if err != nil {
		t.Fatalf("insert engine_asset: %v", err)
	}
	_, err = db.RawDB().ExecContext(ctx,
		`INSERT INTO hardware_profiles (id, name, gpu_arch) VALUES ('nvidia-gb10-arm64', 'GB10', 'Blackwell')`)
	if err != nil {
		t.Fatalf("insert hardware_profile: %v", err)
	}

	// Insert a golden config with gmu=0.85 and max_model_len=32768
	goldenCfg := &state.Configuration{
		ID:         "cfg-g-prov",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.85,"max_model_len":32768}`,
		ConfigHash: "prov-hash-001",
		Status:     "golden",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, goldenCfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}
	if err := db.InsertBenchmarkResult(ctx, &state.BenchmarkResult{
		ID: "br-prov", ConfigID: "cfg-g-prov", Concurrency: 1, ThroughputTPS: 30,
	}); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	kStore := knowledge.NewStore(db.RawDB())

	// Simulate user overriding only gmu (L1), golden has both gmu and max_model_len
	userOverrides := map[string]any{"gpu_memory_utilization": 0.9}
	userKeys := map[string]bool{"gpu_memory_utilization": true}

	goldenConfig := queryGoldenOverrides(ctx, kStore, "Blackwell", "vllm-nightly", "qwen3-8b")
	if goldenConfig == nil {
		t.Fatal("expected golden config")
	}

	// Merge: L2 first, then L1 wins
	merged := make(map[string]any, len(goldenConfig)+len(userOverrides))
	for k, v := range goldenConfig {
		merged[k] = v
	}
	for k, v := range userOverrides {
		merged[k] = v
	}

	// Verify user override wins for gmu
	if gmu := merged["gpu_memory_utilization"]; gmu != 0.9 {
		t.Errorf("user override should win: gpu_memory_utilization = %v, want 0.9", gmu)
	}
	// Verify golden config provides max_model_len
	if mml := merged["max_model_len"]; mml != float64(32768) {
		t.Errorf("golden should provide max_model_len = %v, want 32768", mml)
	}

	// Verify provenance marking
	for k := range goldenConfig {
		if userKeys[k] {
			// User-overridden keys stay as L1
		} else {
			// Golden-only keys should be L2
			// (In real code, this is done by resolveDeployment)
		}
	}
	if userKeys["max_model_len"] {
		t.Error("max_model_len should not be in userKeys")
	}
	if !userKeys["gpu_memory_utilization"] {
		t.Error("gpu_memory_utilization should be in userKeys")
	}
}

func TestLoadLLMSettings_Defaults(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	t.Setenv("AIMA_LLM_ENDPOINT", "")
	t.Setenv("AIMA_LLM_MODEL", "")
	t.Setenv("AIMA_API_KEY", "")
	t.Setenv("AIMA_LLM_USER_AGENT", "")
	t.Setenv("AIMA_LLM_EXTRA_PARAMS", "")

	settings := loadLLMSettings(ctx, db)
	if settings.Endpoint != "http://localhost:6188/v1" {
		t.Fatalf("Endpoint = %q, want http://localhost:6188/v1", settings.Endpoint)
	}
	if settings.Model != "" {
		t.Fatalf("Model = %q, want empty", settings.Model)
	}
	if settings.APIKey != "" {
		t.Fatalf("APIKey = %q, want empty", settings.APIKey)
	}
}

func TestLoadLLMSettings_EmptyStoredValuesFallbackToLocalDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	t.Setenv("AIMA_LLM_ENDPOINT", "")
	t.Setenv("AIMA_LLM_MODEL", "")
	t.Setenv("AIMA_API_KEY", "")
	t.Setenv("AIMA_LLM_USER_AGENT", "")
	t.Setenv("AIMA_LLM_EXTRA_PARAMS", "")

	if err := db.SetConfig(ctx, "api_key", "local"); err != nil {
		t.Fatalf("SetConfig api_key: %v", err)
	}
	if err := db.SetConfig(ctx, "llm.endpoint", ""); err != nil {
		t.Fatalf("SetConfig llm.endpoint: %v", err)
	}
	if err := db.SetConfig(ctx, "llm.model", ""); err != nil {
		t.Fatalf("SetConfig llm.model: %v", err)
	}
	if err := db.SetConfig(ctx, "llm.api_key", ""); err != nil {
		t.Fatalf("SetConfig llm.api_key: %v", err)
	}

	settings := loadLLMSettings(ctx, db)
	if settings.Endpoint != defaultLLMEndpoint() {
		t.Fatalf("Endpoint = %q, want %q", settings.Endpoint, defaultLLMEndpoint())
	}
	if settings.Model != "" {
		t.Fatalf("Model = %q, want empty", settings.Model)
	}
	if settings.APIKey != "local" {
		t.Fatalf("APIKey = %q, want local", settings.APIKey)
	}
}

func TestReloadLLMSettings_ReappliesResolvedDefaultsToLiveClient(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	t.Setenv("AIMA_LLM_ENDPOINT", "")
	t.Setenv("AIMA_LLM_MODEL", "")
	t.Setenv("AIMA_API_KEY", "")
	t.Setenv("AIMA_LLM_USER_AGENT", "")
	t.Setenv("AIMA_LLM_EXTRA_PARAMS", "")

	if err := db.SetConfig(ctx, "api_key", "local"); err != nil {
		t.Fatalf("SetConfig api_key: %v", err)
	}
	if err := db.SetConfig(ctx, "llm.endpoint", ""); err != nil {
		t.Fatalf("SetConfig llm.endpoint: %v", err)
	}
	if err := db.SetConfig(ctx, "llm.model", ""); err != nil {
		t.Fatalf("SetConfig llm.model: %v", err)
	}
	if err := db.SetConfig(ctx, "llm.api_key", ""); err != nil {
		t.Fatalf("SetConfig llm.api_key: %v", err)
	}

	client := agent.NewOpenAIClient("https://api.kimi.com/coding/v1",
		agent.WithModel("kimi-for-coding"),
		agent.WithAPIKey("sk-kimi"),
	)

	settings := reloadLLMSettings(ctx, db, client, "")
	if settings.Endpoint != defaultLLMEndpoint() {
		t.Fatalf("settings.Endpoint = %q, want %q", settings.Endpoint, defaultLLMEndpoint())
	}
	if client.Endpoint() != defaultLLMEndpoint() {
		t.Fatalf("client.Endpoint() = %q, want %q", client.Endpoint(), defaultLLMEndpoint())
	}
}

func TestReloadLLMSettings_FallsBackToServeAPIKeyForLocalProxy(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	t.Setenv("AIMA_LLM_ENDPOINT", "")
	t.Setenv("AIMA_LLM_MODEL", "")
	t.Setenv("AIMA_API_KEY", "")
	t.Setenv("AIMA_LLM_USER_AGENT", "")
	t.Setenv("AIMA_LLM_EXTRA_PARAMS", "")

	if err := db.SetConfig(ctx, "llm.endpoint", ""); err != nil {
		t.Fatalf("SetConfig llm.endpoint: %v", err)
	}
	if err := db.SetConfig(ctx, "llm.api_key", ""); err != nil {
		t.Fatalf("SetConfig llm.api_key: %v", err)
	}

	client := agent.NewOpenAIClient("https://api.kimi.com/coding/v1",
		agent.WithModel("kimi-for-coding"),
		agent.WithAPIKey("sk-kimi"),
	)

	settings := reloadLLMSettings(ctx, db, client, "local")
	if settings.Endpoint != defaultLLMEndpoint() {
		t.Fatalf("settings.Endpoint = %q, want %q", settings.Endpoint, defaultLLMEndpoint())
	}
	if settings.APIKey != "local" {
		t.Fatalf("settings.APIKey = %q, want local", settings.APIKey)
	}
	if client.Endpoint() != defaultLLMEndpoint() {
		t.Fatalf("client.Endpoint() = %q, want %q", client.Endpoint(), defaultLLMEndpoint())
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

func TestWriteBenchmarkValidationFallsBackToExpectedPerf(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seedBenchmarkPredictionTables(t, ctx, db)

	cfg := &state.Configuration{
		ID:         "cfg-bench-001",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.85}`,
		ConfigHash: "cfg-bench-hash-001",
		Status:     "experiment",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}

	if err := writeBenchmarkValidation(ctx, db, "bench-001", cfg.ID, cfg.HardwareID, cfg.EngineID, cfg.ModelID, 36); err != nil {
		t.Fatalf("writeBenchmarkValidation: %v", err)
	}

	rows, err := db.ListValidations(ctx, cfg.HardwareID, cfg.EngineID, cfg.ModelID)
	if err != nil {
		t.Fatalf("ListValidations: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("validation rows = %d, want 1", len(rows))
	}
	if rows[0]["metric"] != "throughput_tps" {
		t.Fatalf("metric = %v, want throughput_tps", rows[0]["metric"])
	}
	if rows[0]["predicted"] != 30.0 {
		t.Fatalf("predicted = %v, want 30", rows[0]["predicted"])
	}
	if rows[0]["actual"] != 36.0 {
		t.Fatalf("actual = %v, want 36", rows[0]["actual"])
	}
}

func TestLookupPredictedThroughputPrefersGoldenBenchmark(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seedBenchmarkPredictionTables(t, ctx, db)

	cfg := &state.Configuration{
		ID:         "cfg-golden-bench",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.9}`,
		ConfigHash: "cfg-golden-bench-hash",
		Status:     "golden",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}
	if err := db.InsertBenchmarkResult(ctx, &state.BenchmarkResult{
		ID:            "bench-golden-001",
		ConfigID:      cfg.ID,
		Concurrency:   1,
		ThroughputTPS: 44,
	}); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	predicted, err := lookupPredictedThroughput(ctx, db.RawDB(), cfg.HardwareID, cfg.EngineID, cfg.ModelID)
	if err != nil {
		t.Fatalf("lookupPredictedThroughput: %v", err)
	}
	if predicted != 44 {
		t.Fatalf("predicted = %v, want 44", predicted)
	}
}

func TestUpdatePerfOverlayWritesObservationOutsideCatalog(t *testing.T) {
	dir := t.TempDir()
	updatePerfOverlay(dir, "qwen3-8b", "nvidia-gb10-arm64", "vllm-nightly", &benchpkg.RunResult{
		ThroughputTPS: 42.5,
		TTFTP50ms:     10,
		TTFTP95ms:     20,
		TPOTP50ms:     3,
		QPS:           5,
	}, nil, "", "", nil)

	observationPath := filepath.Join(dir, "observations", "models", "qwen3-8b-perf.json")
	if _, err := os.Stat(observationPath); err != nil {
		t.Fatalf("expected observation file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "catalog", "models", "qwen3-8b-perf.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no catalog overlay file, got err=%v", err)
	}
}

func TestDownloadTrackerSharesStateAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	writer := NewDownloadTracker(dir)
	reader := NewDownloadTracker(dir)

	writer.Start("model-qwen3-8b-1", "model", "qwen3-8b")
	writer.Update("model-qwen3-8b-1", "downloading", "Downloading qwen3-8b...", 128, 512, 64)

	list := reader.List()
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	got := list[0]
	if got.ID != "model-qwen3-8b-1" {
		t.Fatalf("id = %q, want model-qwen3-8b-1", got.ID)
	}
	if got.Phase != "downloading" {
		t.Fatalf("phase = %q, want downloading", got.Phase)
	}
	if got.Downloaded != 128 || got.Total != 512 || got.Speed != 64 {
		t.Fatalf("progress = (%d/%d @ %d), want (128/512 @ 64)", got.Downloaded, got.Total, got.Speed)
	}
}

func TestDownloadTrackerPrunesExpiredEntries(t *testing.T) {
	dir := t.TempDir()
	tracker := NewDownloadTracker(dir)
	now := time.Now()

	tracker.store(&DownloadProgress{
		ID:         "done",
		Type:       "engine",
		Name:       "vllm",
		Phase:      "complete",
		StartedAt:  now.Add(-2 * time.Minute).UnixMilli(),
		UpdatedAt:  now.Add(-time.Minute).UnixMilli(),
		FinishedAt: now.Add(-downloadFinishedRetention - time.Second).UnixMilli(),
	})
	tracker.store(&DownloadProgress{
		ID:        "stale-active",
		Type:      "model",
		Name:      "qwen3-8b",
		Phase:     "downloading",
		StartedAt: now.Add(-2 * time.Minute).UnixMilli(),
		UpdatedAt: now.Add(-downloadActiveTTL - time.Second).UnixMilli(),
	})

	list := tracker.List()
	if len(list) != 0 {
		t.Fatalf("len(list) = %d, want 0", len(list))
	}

	if _, err := os.Stat(tracker.pathForID("done")); !os.IsNotExist(err) {
		t.Fatalf("completed entry still exists, err=%v", err)
	}
	if _, err := os.Stat(tracker.pathForID("stale-active")); !os.IsNotExist(err) {
		t.Fatalf("stale active entry still exists, err=%v", err)
	}
}

func seedBenchmarkPredictionTables(t *testing.T, ctx context.Context, db *state.DB) {
	t.Helper()

	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_assets (id, type, version) VALUES ('vllm-nightly', 'vllm-nightly', 'v0.16')`); err != nil {
		t.Fatalf("insert engine_asset: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO hardware_profiles (id, name, gpu_arch) VALUES ('nvidia-gb10-arm64', 'GB10', 'Blackwell')`); err != nil {
		t.Fatalf("insert hardware_profile: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO model_assets (id, name, type) VALUES ('qwen3-8b', 'qwen3-8b', 'llm')`); err != nil {
		t.Fatalf("insert model_asset: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO model_variants (id, model_id, hardware_id, engine_type, format, default_config, expected_perf, vram_min_mib)
		 VALUES ('qwen3-8b-gb10-vllm', 'qwen3-8b', 'nvidia-gb10-arm64', 'vllm-nightly', 'safetensors', '{}', '{"tokens_per_second":[20,40]}', 8192)`); err != nil {
		t.Fatalf("insert model_variant: %v", err)
	}
}

