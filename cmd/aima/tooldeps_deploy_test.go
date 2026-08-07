package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/recovery"
	"github.com/jguan/aima/internal/runtime"
)

type deploymentIntentRuntime struct {
	name         string
	statuses     map[string]*runtime.DeploymentStatus
	deployCalls  int
	deleteCalls  int
	beforeDeploy func(*runtime.DeployRequest)
	beforeDelete func(string)
	beforeList   func()
	beforeName   func()
	afterStatus  func()
	statusErr    error
	listErr      error
	deployErr    error
	deleteErr    error
	events       []string
}

func (r *deploymentIntentRuntime) Deploy(_ context.Context, req *runtime.DeployRequest) error {
	r.deployCalls++
	r.events = append(r.events, "deploy")
	if r.beforeDeploy != nil {
		r.beforeDeploy(req)
	}
	return r.deployErr
}

func (r *deploymentIntentRuntime) Delete(_ context.Context, name string) error {
	r.deleteCalls++
	r.events = append(r.events, "delete")
	if r.beforeDelete != nil {
		r.beforeDelete(name)
	}
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.statuses, name)
	return nil
}

func (r *deploymentIntentRuntime) Status(_ context.Context, name string) (*runtime.DeploymentStatus, error) {
	if r.statusErr != nil {
		return nil, r.statusErr
	}
	status, ok := r.statuses[name]
	if !ok {
		return nil, fmt.Errorf("deployment %q not found", name)
	}
	if r.afterStatus != nil {
		r.afterStatus()
	}
	return status, nil
}

type armedCancellationContext struct {
	context.Context
	armed atomic.Bool
}

func (c *armedCancellationContext) Done() <-chan struct{} { return nil }

func (c *armedCancellationContext) Err() error {
	if c.armed.Load() {
		return context.Canceled
	}
	return nil
}

func (r *deploymentIntentRuntime) List(context.Context) ([]*runtime.DeploymentStatus, error) {
	if r.beforeList != nil {
		r.beforeList()
	}
	if r.listErr != nil {
		return nil, r.listErr
	}
	statuses := make([]*runtime.DeploymentStatus, 0, len(r.statuses))
	for _, status := range r.statuses {
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (r *deploymentIntentRuntime) Logs(context.Context, string, int) (string, error) {
	return "", nil
}

func (r *deploymentIntentRuntime) Name() string {
	if r.beforeName != nil {
		r.beforeName()
	}
	return r.name
}

func newDeploymentIntentHarness(t *testing.T) (context.Context, *state.DB, *mcp.ToolDeps, *deploymentIntentRuntime, string) {
	t.Helper()
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	modelName := "intent-model"
	dataDir := t.TempDir()
	modelDir := filepath.Join(dataDir, "models", modelName)
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("mkdir model dir: %v", err)
	}
	for name, content := range map[string]string{
		"config.json":                      `{"model_type":"test"}`,
		"tokenizer.json":                   `{"version":"1.0"}`,
		"model-00001-of-00001.safetensors": "weights",
	} {
		if err := os.WriteFile(filepath.Join(modelDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	maxAttempts := 5
	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{{
			Metadata: knowledge.EngineMetadata{
				Name:             "vllm-test",
				Type:             "vllm",
				Version:          "1.2.3",
				Default:          true,
				SupportedFormats: []string{"safetensors"},
			},
			Hardware: knowledge.EngineHardware{GPUArch: "*"},
			Startup: knowledge.EngineStartup{
				Command:     []string{"serve", "{{.ModelPath}}"},
				DefaultArgs: map[string]any{"port": 18080},
				Recovery:    knowledge.RecoveryPolicy{MaxAttempts: &maxAttempts},
			},
			Source:  &knowledge.EngineSource{InstallType: "preinstalled"},
			Runtime: knowledge.EngineRuntime{Default: "native"},
		}},
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{Name: modelName, Type: "llm", Aliases: []string{"alias-" + modelName}},
			Storage:  knowledge.ModelStorage{Formats: []string{"safetensors"}},
			Variants: []knowledge.ModelVariant{{
				Name:          "bf16",
				Engine:        "vllm",
				Format:        "safetensors",
				Hardware:      knowledge.ModelVariantHardware{GPUArch: "*"},
				DefaultConfig: map[string]any{},
			}},
		}},
	}
	rt := &deploymentIntentRuntime{name: "native", statuses: map[string]*runtime.DeploymentStatus{}}
	deps := &mcp.ToolDeps{}
	buildDeployDeps(&appContext{
		cat:      cat,
		db:       db,
		kStore:   knowledge.NewStore(db.RawDB()),
		rt:       rt,
		nativeRt: rt,
		proxy:    proxy.NewServer(),
		dataDir:  dataDir,
	}, deps,
		func(context.Context, string, func(string, string), func(int64, int64)) error { return nil },
		func(context.Context, string, string, string, map[string]any, bool, func(string, string), func(engine.ProgressEvent), func(int64, int64)) (json.RawMessage, error) {
			return nil, nil
		},
	)
	return ctx, db, deps, rt, modelName
}

func deploymentReconcilerContext(t *testing.T, ctx context.Context, db *state.DB, name string) context.Context {
	t.Helper()
	intent, err := db.GetDeploymentIntent(ctx, name)
	if err != nil {
		t.Fatalf("GetDeploymentIntent(%s): %v", name, err)
	}
	return recovery.WithReconcilerClaim(ctx, *intent)
}

func seedRecoveryIntent(t *testing.T, ctx context.Context, db *state.DB, name, runtimeName, recoveryState string) recovery.Intent {
	t.Helper()
	intent := &recovery.Intent{
		Name:                    name,
		Model:                   name,
		EngineAsset:             "vllm-test",
		EngineVersion:           "1.2.3",
		Slot:                    "default",
		Runtime:                 runtimeName,
		Config:                  map[string]any{"port": 18080},
		DesiredState:            recovery.DesiredRunning,
		RecoveryState:           recoveryState,
		Policy:                  recovery.DefaultPolicy(),
		AttemptCount:            1,
		ConsecutiveFailureCount: recovery.DefaultPolicy().ConsecutiveFailures - 1,
		NextAttemptAt:           time.Now().UTC().Add(-time.Second),
	}
	if err := db.UpsertDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("seed recovery intent: %v", err)
	}
	stored, err := db.GetDeploymentIntent(ctx, name)
	if err != nil {
		t.Fatalf("GetDeploymentIntent(%s): %v", name, err)
	}
	return *stored
}

func TestResolvedServedModelNameExpandsModelTemplate(t *testing.T) {
	got := resolvedServedModelName("GLM-4.1V-9B-Thinking-FP4", map[string]any{
		"served_model_name": "{{.ModelName}}",
	})
	if got != "GLM-4.1V-9B-Thinking-FP4" {
		t.Fatalf("resolvedServedModelName = %q, want model name", got)
	}
}

func TestDeploymentUpstreamModelIgnoresUnresolvedTemplateLabel(t *testing.T) {
	got := deploymentUpstreamModel(&runtime.DeploymentStatus{
		Labels: map[string]string{
			proxy.LabelServedModel: "{{.ModelName}}",
			"aima.dev/model":       "GLM-4.1V-9B-Thinking-FP4",
		},
	}, "")
	if got != "GLM-4.1V-9B-Thinking-FP4" {
		t.Fatalf("deploymentUpstreamModel = %q, want model label fallback", got)
	}
}

func TestDeploymentOverviewIncludesCatalogModelType(t *testing.T) {
	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{Name: "qwen3-tts-0.6b", Type: "tts"},
		}},
	}
	overview := deploymentOverviewFromStatus(&runtime.DeploymentStatus{
		Name:            "qwen3-tts-0.6b-qwen-tts-fastapi",
		Model:           "qwen3-tts-0.6b",
		Image:           "docker.1ms.run/example/qwen-tts:latest",
		Phase:           "running",
		Ready:           true,
		GPUMemoryMiB:    1536,
		GPUMemorySource: "nvidia-smi",
	}, cat)
	if overview.ModelType != "tts" {
		t.Fatalf("ModelType = %q, want tts", overview.ModelType)
	}
	if overview.Image != "docker.1ms.run/example/qwen-tts:latest" {
		t.Fatalf("Image = %q, want docker.1ms.run/example/qwen-tts:latest", overview.Image)
	}
	if overview.GPUMemoryMiB != 1536 || overview.GPUMemorySource != "nvidia-smi" {
		t.Fatalf("GPU memory = %d/%q, want 1536/nvidia-smi", overview.GPUMemoryMiB, overview.GPUMemorySource)
	}
}

func TestDeployApplyPersistsExplicitDeploymentIntentBeforeRuntime(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	priorExit := 137
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name:                    modelName,
		Model:                   modelName,
		DesiredState:            recovery.DesiredRunning,
		RecoveryState:           recovery.StateQuarantined,
		Policy:                  recovery.DefaultPolicy(),
		AttemptCount:            4,
		ConsecutiveFailureCount: 6,
		ObservedRestartCount:    8,
		WindowStartedAt:         time.Now().Add(-time.Minute),
		NextAttemptAt:           time.Now().Add(time.Minute),
		HealthySince:            time.Now().Add(-time.Hour),
		LastExitCode:            &priorExit,
		LastError:               "previous failure",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	rt.beforeDeploy = func(req *runtime.DeployRequest) {
		got, err := db.GetDeploymentIntent(ctx, modelName)
		if err != nil {
			t.Fatalf("GetDeploymentIntent before Runtime.Deploy: %v", err)
		}
		if got.DesiredState != recovery.DesiredRunning || got.RecoveryState != recovery.StateHealthy {
			t.Fatalf("state before Runtime.Deploy = %s/%s", got.DesiredState, got.RecoveryState)
		}
		if got.EngineAsset != "vllm-test" || got.EngineVersion != "1.2.3" || got.Runtime != "native" {
			t.Fatalf("resolved identity before Runtime.Deploy = %+v", got)
		}
		if got.AttemptCount != 0 || got.ConsecutiveFailureCount != 0 || got.ObservedRestartCount != 0 || !got.WindowStartedAt.IsZero() || !got.NextAttemptAt.IsZero() || !got.HealthySince.IsZero() || got.LastExitCode != nil || got.LastError != "" {
			t.Fatalf("explicit apply did not reset recovery bookkeeping: %+v", got)
		}
		if got.Config["api_key"] != "[REDACTED]" || got.Config["batch_size"] != json.Number("4") {
			t.Fatalf("persisted config = %#v", got.Config)
		}
		if got.Policy.MaxAttempts != 7 || len(got.Policy.BackoffS) != 2 || got.Policy.BackoffS[0] != 1 || got.Policy.BackoffS[1] != 3 {
			t.Fatalf("resolved recovery policy = %+v", got.Policy)
		}
	}
	maxAttempts := 7
	if _, err := deps.DeployApply(ctx, "vllm", modelName, "", map[string]any{
		"api_key":    "must-not-persist",
		"batch_size": 4,
	}, true, recovery.PolicyPatch{MaxAttempts: &maxAttempts, BackoffS: []int{1, 3}}); err != nil {
		t.Fatalf("DeployApply: %v", err)
	}
	if rt.deployCalls != 1 {
		t.Fatalf("Runtime.Deploy calls = %d, want 1", rt.deployCalls)
	}
}

func TestDeployApplyReconcilerPreservesDeploymentIntentBookkeeping(t *testing.T) {
	ctx, db, deps, _, modelName := newDeploymentIntentHarness(t)
	exitCode := 9
	windowStartedAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	nextAttemptAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	pinnedPolicy := recovery.DefaultPolicy()
	pinnedPolicy.MaxAttempts = 11
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name:                    modelName,
		Model:                   modelName,
		EngineAsset:             "vllm-test",
		EngineVersion:           "1.2.3",
		Slot:                    "default",
		Runtime:                 "native",
		Config:                  map[string]any{"port": 18080},
		DesiredState:            recovery.DesiredRunning,
		RecoveryState:           recovery.StateRecovering,
		Policy:                  pinnedPolicy,
		AttemptCount:            2,
		ConsecutiveFailureCount: 4,
		ObservedRestartCount:    6,
		WindowStartedAt:         windowStartedAt,
		NextAttemptAt:           nextAttemptAt,
		LastExitCode:            &exitCode,
		LastError:               "still tracked",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	if _, err := deps.DeployApply(deploymentReconcilerContext(t, ctx, db, modelName), "vllm", modelName, "", nil, true, recovery.PolicyPatch{}); err != nil {
		t.Fatalf("DeployApply: %v", err)
	}
	got, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent: %v", err)
	}
	if got.RecoveryState != recovery.StateRecovering || got.AttemptCount != 2 || got.ConsecutiveFailureCount != 4 || got.ObservedRestartCount != 6 || !got.WindowStartedAt.Equal(windowStartedAt) || !got.NextAttemptAt.Equal(nextAttemptAt) || got.LastExitCode == nil || *got.LastExitCode != exitCode || got.LastError != "still tracked" {
		t.Fatalf("reconciler apply changed recovery bookkeeping: %+v", got)
	}
	if got.Policy.MaxAttempts != 11 {
		t.Fatalf("reconciler policy = %+v, want stored max_attempts=11", got.Policy)
	}
}

func TestDeployApplyReconcilerRejectsDeploymentIntentCatalogVersionDrift(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	pinnedPolicy := recovery.DefaultPolicy()
	pinnedPolicy.MaxAttempts = 11
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, EngineAsset: "vllm-test", EngineVersion: "1.2.2", Runtime: "native",
		DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateWaiting, Policy: pinnedPolicy,
		AttemptCount: 2, ConsecutiveFailureCount: 4, LastError: "pinned failure",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	_, err := deps.DeployApply(deploymentReconcilerContext(t, ctx, db, modelName), "vllm-test", modelName, "", nil, true, recovery.PolicyPatch{})
	if err == nil || !strings.Contains(err.Error(), "engine version drift") {
		t.Fatalf("DeployApply error = %v, want engine version drift", err)
	}
	if rt.deployCalls != 0 {
		t.Fatalf("Runtime.Deploy calls = %d, want 0", rt.deployCalls)
	}
	got, getErr := db.GetDeploymentIntent(ctx, modelName)
	if getErr != nil {
		t.Fatalf("GetDeploymentIntent: %v", getErr)
	}
	if got.EngineVersion != "1.2.2" || got.Policy.MaxAttempts != 11 || got.RecoveryState != recovery.StateWaiting || got.AttemptCount != 2 || got.ConsecutiveFailureCount != 4 || got.LastError != "pinned failure" {
		t.Fatalf("pinned intent changed after catalog drift: %+v", got)
	}
}

func TestDeployApplyReconcilerUsesPinnedInventoryAfterCatalogVersionChanges(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	binaryPath := filepath.Join(t.TempDir(), "vllm-old")
	if err := os.WriteFile(binaryPath, []byte("old-version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertEngine(ctx, &state.Engine{
		ID: "vllm-old", Type: "vllm", AssetName: "vllm-test", Version: "1.2.2", CatalogVersion: "1.2.2",
		Platform: goruntime.GOOS + "-" + goruntime.GOARCH, RuntimeType: "native", BinaryPath: binaryPath,
		Available: true, LifecycleStatus: "verified", VerificationStatus: "verified", Origin: "managed",
	}); err != nil {
		t.Fatalf("InsertEngine: %v", err)
	}
	pinnedPolicy := recovery.DefaultPolicy()
	pinnedPolicy.MaxAttempts = 11
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, EngineAsset: "vllm-test", EngineVersion: "1.2.2", Slot: "default", Runtime: "native",
		Config:       map[string]any{"port": 18080},
		DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateRecovering, Policy: pinnedPolicy,
		AttemptCount: 2, ConsecutiveFailureCount: 4, LastError: "pinned failure",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	rt.beforeDeploy = func(req *runtime.DeployRequest) {
		if req.BinarySource == nil || len(req.BinarySource.ProbePaths) == 0 || req.BinarySource.ProbePaths[0] != binaryPath {
			t.Fatalf("recovery binary source = %+v, want pinned inventory path %s", req.BinarySource, binaryPath)
		}
	}

	if _, err := deps.DeployApply(deploymentReconcilerContext(t, ctx, db, modelName), "vllm-test", modelName, "", nil, true, recovery.PolicyPatch{}); err != nil {
		t.Fatalf("DeployApply: %v", err)
	}
	if rt.deployCalls != 1 {
		t.Fatalf("Runtime.Deploy calls = %d, want 1", rt.deployCalls)
	}
	got, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatal(err)
	}
	if got.EngineVersion != "1.2.2" || got.Policy.MaxAttempts != 11 || got.AttemptCount != 2 || got.LastError != "pinned failure" {
		t.Fatalf("pinned intent changed: %+v", got)
	}
}

func TestDeployApplyReconcilerRejectsDeploymentIntentEngineAssetDrift(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, EngineAsset: "different-vllm-asset", EngineVersion: "1.2.3", Runtime: "native",
		DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateWaiting, Policy: recovery.DefaultPolicy(),
		AttemptCount: 2, LastError: "asset pinned",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	_, err := deps.DeployApply(deploymentReconcilerContext(t, ctx, db, modelName), "vllm", modelName, "", nil, true, recovery.PolicyPatch{})
	if err == nil || !strings.Contains(err.Error(), "engine asset drift") {
		t.Fatalf("DeployApply error = %v, want engine asset drift", err)
	}
	if rt.deployCalls != 0 {
		t.Fatalf("Runtime.Deploy calls = %d, want 0", rt.deployCalls)
	}
	got, getErr := db.GetDeploymentIntent(ctx, modelName)
	if getErr != nil {
		t.Fatalf("GetDeploymentIntent: %v", getErr)
	}
	if got.EngineAsset != "different-vllm-asset" || got.RecoveryState != recovery.StateWaiting || got.AttemptCount != 2 || got.LastError != "asset pinned" {
		t.Fatalf("pinned intent changed after engine asset drift: %+v", got)
	}
}

func TestDeployApplyReconcilerRejectsDeploymentIntentRuntimeDrift(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, EngineAsset: "vllm-test", EngineVersion: "1.2.3", Runtime: "docker",
		DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateRecovering, Policy: recovery.DefaultPolicy(),
		AttemptCount: 3, ConsecutiveFailureCount: 5, LastError: "runtime pinned",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	_, err := deps.DeployApply(deploymentReconcilerContext(t, ctx, db, modelName), "vllm-test", modelName, "", nil, true, recovery.PolicyPatch{})
	if err == nil || !strings.Contains(err.Error(), "runtime drift") {
		t.Fatalf("DeployApply error = %v, want runtime drift", err)
	}
	if rt.deployCalls != 0 {
		t.Fatalf("Runtime.Deploy calls = %d, want 0", rt.deployCalls)
	}
	got, getErr := db.GetDeploymentIntent(ctx, modelName)
	if getErr != nil {
		t.Fatalf("GetDeploymentIntent: %v", getErr)
	}
	if got.Runtime != "docker" || got.RecoveryState != recovery.StateRecovering || got.AttemptCount != 3 || got.ConsecutiveFailureCount != 5 || got.LastError != "runtime pinned" {
		t.Fatalf("pinned intent changed after runtime drift: %+v", got)
	}
}

func TestDeployApplyReconcilerRejectsDeploymentIntentNameDrift(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	intentName := "previous-deployment-name"
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: intentName, Model: modelName, EngineAsset: "vllm-test", EngineVersion: "1.2.3", Runtime: "native",
		DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateRecovering, Policy: recovery.DefaultPolicy(),
		AttemptCount: 3, LastError: "name pinned",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	_, err := deps.DeployApply(deploymentReconcilerContext(t, ctx, db, intentName), "vllm", modelName, "", nil, true, recovery.PolicyPatch{})
	if err == nil || !strings.Contains(err.Error(), "deployment name drift") {
		t.Fatalf("DeployApply error = %v, want deployment name drift", err)
	}
	if rt.deployCalls != 0 {
		t.Fatalf("Runtime.Deploy calls = %d, want 0", rt.deployCalls)
	}
	got, getErr := db.GetDeploymentIntent(ctx, intentName)
	if getErr != nil {
		t.Fatalf("GetDeploymentIntent: %v", getErr)
	}
	if got.Name != intentName || got.RecoveryState != recovery.StateRecovering || got.AttemptCount != 3 || got.LastError != "name pinned" {
		t.Fatalf("pinned intent changed after deployment name drift: %+v", got)
	}
}

func TestRecoveryApplyDeletesUnhealthyNativeBeforeDeployWithoutSecondIntentCAS(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	claim := seedRecoveryIntent(t, ctx, db, modelName, "native", recovery.StateRecovering)
	rt.statuses[modelName] = &runtime.DeploymentStatus{
		Name: modelName, Model: modelName, Runtime: "native", Phase: "failed",
		Labels: map[string]string{"aima.dev/model": modelName},
	}

	if err := deps.RecoveryApply(recovery.WithReconcilerClaim(ctx, claim), claim); err != nil {
		t.Fatalf("RecoveryApply: %v", err)
	}
	if !reflect.DeepEqual(rt.events, []string{"delete", "deploy"}) {
		t.Fatalf("runtime events = %v, want delete then deploy", rt.events)
	}
	stored, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent: %v", err)
	}
	if stored.Revision != claim.Revision || stored.RecoveryState != recovery.StateRecovering {
		t.Fatalf("stored intent = %+v, want unchanged committed claim revision", stored)
	}
}

func TestRecoveryApplyRejectsRedactedCredentialsBeforeRuntimeDeploy(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	claim := seedRecoveryIntent(t, ctx, db, modelName, "native", recovery.StateRecovering)
	claim.Config = map[string]any{
		"port": 18080,
		"auth": map[string]any{
			"api_key": "[REDACTED]",
		},
	}
	if err := db.UpsertDeploymentIntent(ctx, &claim); err != nil {
		t.Fatalf("update recovery intent: %v", err)
	}
	stored, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent: %v", err)
	}

	err = deps.RecoveryApply(recovery.WithReconcilerClaim(ctx, *stored), *stored)
	if err == nil || !strings.Contains(err.Error(), "redacted credentials") || !strings.Contains(err.Error(), "secure source") {
		t.Fatalf("RecoveryApply error = %v, want explicit secure credential rejection", err)
	}
	if rt.deployCalls != 0 || rt.deleteCalls != 0 {
		t.Fatalf("runtime side effects: deploy=%d delete=%d, want zero", rt.deployCalls, rt.deleteCalls)
	}
}

func TestRecoveryApplyReadyAtLockedRecheckHasZeroRuntimeSideEffects(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	claim := seedRecoveryIntent(t, ctx, db, modelName, "native", recovery.StateRecovering)
	rt.statuses[modelName] = &runtime.DeploymentStatus{
		Name: modelName, Model: modelName, Engine: "vllm", Runtime: "native",
		Phase: "running", Ready: true, Address: "127.0.0.1:18080",
		Labels: map[string]string{"aima.dev/model": modelName, "aima.dev/engine": "vllm-test"},
	}

	if err := deps.RecoveryApply(recovery.WithReconcilerClaim(ctx, claim), claim); err != nil {
		t.Fatalf("RecoveryApply: %v", err)
	}
	if len(rt.events) != 0 {
		t.Fatalf("runtime events = %v, want zero when locked recheck is Ready", rt.events)
	}
}

func TestRecoveryApplyNativeStartingAtLockedRecheckHonorsStallSignal(t *testing.T) {
	for _, tt := range []struct {
		name       string
		stalled    bool
		wantEvents []string
	}{
		{name: "normal cold start", wantEvents: nil},
		{name: "stalled start", stalled: true, wantEvents: []string{"delete", "deploy"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
			claim := seedRecoveryIntent(t, ctx, db, modelName, "native", recovery.StateRecovering)
			rt.statuses[modelName] = &runtime.DeploymentStatus{
				Name: modelName, Model: modelName, Engine: "vllm", Runtime: "native",
				Phase: "starting", Stalled: tt.stalled,
				Labels: map[string]string{"aima.dev/model": modelName, "aima.dev/engine": "vllm-test"},
			}

			if err := deps.RecoveryApply(recovery.WithReconcilerClaim(ctx, claim), claim); err != nil {
				t.Fatalf("RecoveryApply: %v", err)
			}
			if !reflect.DeepEqual(rt.events, tt.wantEvents) {
				t.Fatalf("runtime events = %v, want %v", rt.events, tt.wantEvents)
			}
			if rt.deleteCalls != len(tt.wantEvents)/2 || rt.deployCalls != len(tt.wantEvents)/2 {
				t.Fatalf("delete/deploy calls = %d/%d, want %d/%d", rt.deleteCalls, rt.deployCalls, len(tt.wantEvents)/2, len(tt.wantEvents)/2)
			}
			stored, err := db.GetDeploymentIntent(ctx, modelName)
			if err != nil {
				t.Fatalf("GetDeploymentIntent: %v", err)
			}
			if stored.Revision != claim.Revision || stored.RecoveryState != recovery.StateRecovering {
				t.Fatalf("stored intent = %+v, want unchanged committed claim revision", stored)
			}
		})
	}
}

func TestRecoveryApplyCancellationAfterLockedObservationPreventsNativeDelete(t *testing.T) {
	baseCtx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	claim := seedRecoveryIntent(t, baseCtx, db, modelName, "native", recovery.StateRecovering)
	rt.statuses[modelName] = &runtime.DeploymentStatus{
		Name: modelName, Model: modelName, Runtime: "native", Phase: "failed",
		Labels: map[string]string{"aima.dev/model": modelName},
	}
	ctx, cancel := context.WithCancel(baseCtx)
	statusCalls := 0
	rt.afterStatus = func() {
		statusCalls++
		if statusCalls == 2 {
			cancel()
		}
	}

	err := deps.RecoveryApply(recovery.WithReconcilerClaim(ctx, claim), claim)
	if statusCalls < 2 {
		t.Fatalf("status calls = %d, want cancellation during the locked recheck", statusCalls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RecoveryApply error = %v, want context canceled", err)
	}
	if len(rt.events) != 0 || rt.deleteCalls != 0 || rt.deployCalls != 0 {
		t.Fatalf("runtime events = %v, delete/deploy = %d/%d; want zero after cancellation", rt.events, rt.deleteCalls, rt.deployCalls)
	}
}

func TestRecoveryApplyCancellationAfterFinalClaimValidationPreventsDeploy(t *testing.T) {
	firstCtx, firstDB, firstDeps, firstRT, modelName := newDeploymentIntentHarness(t)
	firstClaim := seedRecoveryIntent(t, firstCtx, firstDB, modelName, "native", recovery.StateRecovering)
	var firstNameCalls, namesBeforeDeploy int32
	firstRT.beforeName = func() { atomic.AddInt32(&firstNameCalls, 1) }
	firstRT.beforeDeploy = func(*runtime.DeployRequest) {
		namesBeforeDeploy = atomic.LoadInt32(&firstNameCalls)
	}
	if err := firstDeps.RecoveryApply(recovery.WithReconcilerClaim(firstCtx, firstClaim), firstClaim); err != nil {
		t.Fatalf("calibrate RecoveryApply: %v", err)
	}
	if namesBeforeDeploy == 0 {
		t.Fatal("calibration did not reach Runtime.Deploy")
	}

	baseCtx, db, deps, rt, secondModelName := newDeploymentIntentHarness(t)
	claim := seedRecoveryIntent(t, baseCtx, db, secondModelName, "native", recovery.StateRecovering)
	cancelCtx := &armedCancellationContext{Context: baseCtx}
	var nameCalls int32
	rt.beforeName = func() {
		if atomic.AddInt32(&nameCalls, 1) == namesBeforeDeploy {
			cancelCtx.armed.Store(true)
		}
	}

	err := deps.RecoveryApply(recovery.WithReconcilerClaim(cancelCtx, claim), claim)
	if !cancelCtx.armed.Load() {
		t.Fatal("test context was not canceled at the final pre-deploy boundary")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RecoveryApply error = %v, want context canceled", err)
	}
	if rt.deployCalls != 0 || rt.deleteCalls != 0 {
		t.Fatalf("deploy/delete calls = %d/%d, want zero after final claim validation cancellation", rt.deployCalls, rt.deleteCalls)
	}
}

func TestRecoveryApplyRejectsClaimsSupersededByExplicitIntentChanges(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(context.Context, *state.DB, recovery.Intent) error
	}{
		{
			name: "explicit apply",
			mutate: func(ctx context.Context, db *state.DB, claim recovery.Intent) error {
				claim.Revision++
				claim.RecoveryState = recovery.StateHealthy
				claim.AttemptCount = 0
				return db.UpsertDeploymentIntent(ctx, &claim)
			},
		},
		{
			name: "explicit delete",
			mutate: func(ctx context.Context, db *state.DB, claim recovery.Intent) error {
				return db.StopDeploymentIntent(ctx, claim.Name)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
			claim := seedRecoveryIntent(t, ctx, db, modelName, "native", recovery.StateRecovering)
			if err := tt.mutate(ctx, db, claim); err != nil {
				t.Fatalf("mutate intent: %v", err)
			}
			if err := deps.RecoveryApply(recovery.WithReconcilerClaim(ctx, claim), claim); err == nil {
				t.Fatal("RecoveryApply error = nil, want stale claim rejection")
			}
			if len(rt.events) != 0 {
				t.Fatalf("runtime events = %v, want zero for stale recovery", rt.events)
			}
		})
	}
}

func TestExplicitApplyWinsAfterRecoveryClaimBeforeLockedRuntimeWork(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	claim := seedRecoveryIntent(t, ctx, db, modelName, "native", recovery.StateRecovering)
	recoveryReachedPrelock := make(chan struct{})
	continueRecovery := make(chan struct{})
	var nameCalls int32
	rt.beforeName = func() {
		if atomic.AddInt32(&nameCalls, 1) == 1 {
			close(recoveryReachedPrelock)
			<-continueRecovery
		}
	}
	recoveryDone := make(chan error, 1)
	go func() {
		recoveryDone <- deps.RecoveryApply(recovery.WithReconcilerClaim(ctx, claim), claim)
	}()

	select {
	case <-recoveryReachedPrelock:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not reach the pre-lock barrier")
	}
	if _, err := deps.DeployApply(ctx, "vllm", modelName, "", nil, true, recovery.PolicyPatch{}); err != nil {
		t.Fatalf("explicit DeployApply: %v", err)
	}
	if rt.deployCalls != 1 {
		t.Fatalf("explicit Runtime.Deploy calls = %d, want 1", rt.deployCalls)
	}
	rt.events = nil
	close(continueRecovery)
	select {
	case err := <-recoveryDone:
		if err == nil || (!strings.Contains(err.Error(), "changed concurrently") && !strings.Contains(err.Error(), "recovery state")) {
			t.Fatalf("RecoveryApply error = %v, want stale claim rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not finish after releasing the barrier")
	}
	if len(rt.events) != 0 || rt.deployCalls != 1 || rt.deleteCalls != 0 {
		t.Fatalf("post-explicit recovery runtime events = %v, deploy/delete calls = %d/%d; want zero stale work", rt.events, rt.deployCalls, rt.deleteCalls)
	}
}

func TestRecoveryControllerApplyFailurePersistsWaitingFromCommittedRevision(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	claim := seedRecoveryIntent(t, ctx, db, modelName, "native", recovery.StateWaiting)
	rt.deployErr = errors.New("runtime launch failed token=private-token")
	controller := recovery.NewController(db, deps.RecoveryObserve, deps.RecoveryApply, deps.RecoveryDelete)

	err := controller.RunOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "runtime launch failed") {
		t.Fatalf("RunOnce error = %v, want runtime launch failure", err)
	}
	if strings.Contains(err.Error(), "private-token") {
		t.Fatalf("RunOnce error leaked credential: %v", err)
	}
	stored, getErr := db.GetDeploymentIntent(ctx, modelName)
	if getErr != nil {
		t.Fatalf("GetDeploymentIntent: %v", getErr)
	}
	if stored.Revision != claim.Revision+2 || stored.RecoveryState != recovery.StateWaiting || stored.NextAttemptAt.IsZero() {
		t.Fatalf("stored failure intent = %+v, want waiting persisted from committed revision", stored)
	}
	if strings.Contains(stored.LastError, "private-token") || !strings.Contains(stored.LastError, "[REDACTED]") {
		t.Fatalf("stored LastError = %q, want redacted runtime error", stored.LastError)
	}
}

func TestRecoveryDeleteKeepsQuarantinedDesiredRunning(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	rt.name = "docker"
	claim := seedRecoveryIntent(t, ctx, db, modelName, "docker", recovery.StateQuarantined)
	rt.statuses[modelName] = &runtime.DeploymentStatus{Name: modelName, Runtime: "docker", Phase: "running", Ready: true}

	if err := deps.RecoveryDelete(recovery.WithReconcilerClaim(ctx, claim), claim); err != nil {
		t.Fatalf("RecoveryDelete: %v", err)
	}
	if !reflect.DeepEqual(rt.events, []string{"delete"}) {
		t.Fatalf("runtime events = %v, want one concrete delete", rt.events)
	}
	stored, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent: %v", err)
	}
	if stored.DesiredState != recovery.DesiredRunning || stored.RecoveryState != recovery.StateQuarantined || stored.Revision != claim.Revision {
		t.Fatalf("stored intent = %+v, want unchanged desired-running quarantine", stored)
	}
}

func TestRecoveryDeleteCancellationAfterLockedObservationPreventsQuarantineDelete(t *testing.T) {
	baseCtx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	rt.name = "docker"
	claim := seedRecoveryIntent(t, baseCtx, db, modelName, "docker", recovery.StateQuarantined)
	rt.statuses[modelName] = &runtime.DeploymentStatus{Name: modelName, Runtime: "docker", Phase: "failed"}
	ctx, cancel := context.WithCancel(baseCtx)
	rt.afterStatus = cancel

	err := deps.RecoveryDelete(recovery.WithReconcilerClaim(ctx, claim), claim)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RecoveryDelete error = %v, want context canceled", err)
	}
	if len(rt.events) != 0 || rt.deleteCalls != 0 {
		t.Fatalf("runtime events = %v, delete calls = %d; want zero after cancellation", rt.events, rt.deleteCalls)
	}
}

func TestRecoveryObserveUsesOnlyPinnedRuntimeAndDistinguishesErrors(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	intent := seedRecoveryIntent(t, ctx, db, modelName, "docker", recovery.StateHealthy)
	rt.statuses[modelName] = &runtime.DeploymentStatus{Name: modelName, Runtime: "native", Ready: true}
	if _, err := deps.RecoveryObserve(ctx, intent); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("RecoveryObserve error = %v, want unavailable pinned runtime without fallback", err)
	}

	intent.Runtime = "native"
	rt.statusErr = errors.New("status unavailable")
	rt.statuses = map[string]*runtime.DeploymentStatus{}
	observation, err := deps.RecoveryObserve(ctx, intent)
	if err != nil || observation.Exists {
		t.Fatalf("confirmed absence observation = %+v, error = %v", observation, err)
	}
	rt.listErr = errors.New("list unavailable")
	if _, err := deps.RecoveryObserve(ctx, intent); err == nil {
		t.Fatal("RecoveryObserve error = nil, want Status/List infrastructure error")
	}
	rt.listErr = nil
	rt.statuses[modelName] = &runtime.DeploymentStatus{Name: modelName, Runtime: "native"}
	if _, err := deps.RecoveryObserve(ctx, intent); err == nil || !strings.Contains(err.Error(), "details") {
		t.Fatalf("RecoveryObserve error = %v, want present object detail failure", err)
	}
	rt.statusErr = nil
	exitCode := 17
	rt.statuses[modelName] = &runtime.DeploymentStatus{
		Name: modelName, Runtime: "native", Ready: false, Phase: "failed", Restarts: 4,
		ExitCode: &exitCode, Message: "health failed", Stalled: true,
	}
	observation, err = deps.RecoveryObserve(ctx, intent)
	if err != nil {
		t.Fatalf("RecoveryObserve mapped status: %v", err)
	}
	if !observation.Exists || observation.Ready || observation.Phase != "failed" || observation.Restarts != 4 || observation.ExitCode == nil || *observation.ExitCode != exitCode || observation.Error != "health failed" || !observation.Stalled {
		t.Fatalf("mapped recovery observation = %+v", observation)
	}
}

func TestDeployApplyReusableDeploymentIntentIsHealthy(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	rt.statuses[modelName] = &runtime.DeploymentStatus{
		Name: modelName, Model: modelName, Engine: "vllm", Runtime: "native",
		Phase: "running", Ready: true, Address: "127.0.0.1:18080",
		Labels: map[string]string{"aima.dev/model": modelName, "aima.dev/engine": "vllm-test"},
	}

	if _, err := deps.DeployApply(ctx, "", modelName, "", nil, true, recovery.PolicyPatch{}); err != nil {
		t.Fatalf("DeployApply: %v", err)
	}
	if rt.deployCalls != 0 {
		t.Fatalf("Runtime.Deploy calls = %d, want 0 for reusable deployment", rt.deployCalls)
	}
	got, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent: %v", err)
	}
	if got.DesiredState != recovery.DesiredRunning || got.RecoveryState != recovery.StateHealthy || got.Runtime != "native" {
		t.Fatalf("reusable deployment intent = %+v", got)
	}
}

func TestDeployDeleteStopsDeploymentIntentBeforeRuntimeDelete(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, DesiredState: recovery.DesiredRunning,
		RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy(),
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	rt.statuses[modelName] = &runtime.DeploymentStatus{
		Name: modelName, Model: modelName, Runtime: "native", Phase: "running",
		Labels: map[string]string{"aima.dev/model": modelName},
	}
	rt.beforeDelete = func(name string) {
		got, err := db.GetDeploymentIntent(ctx, name)
		if err != nil {
			t.Fatalf("GetDeploymentIntent before Runtime.Delete: %v", err)
		}
		if got.DesiredState != recovery.DesiredStopped {
			t.Fatalf("desired_state before Runtime.Delete = %q, want stopped", got.DesiredState)
		}
	}

	if err := deps.DeployDelete(ctx, modelName); err != nil {
		t.Fatalf("DeployDelete: %v", err)
	}
	if rt.deleteCalls != 1 {
		t.Fatalf("Runtime.Delete calls = %d, want 1", rt.deleteCalls)
	}
}

func TestDeployDeleteReportsUnconfirmedRuntimeCleanupAfterStoppingIntent(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, Runtime: "native",
		DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateHealthy,
		Policy: recovery.DefaultPolicy(),
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	rt.statusErr = errors.New("status failed authorization=private-status")
	rt.listErr = errors.New("list failed token=private-list")

	err := deps.DeployDelete(ctx, modelName)
	if err == nil || !strings.Contains(err.Error(), "cleanup could not be confirmed") {
		t.Fatalf("DeployDelete error = %v, want unconfirmed cleanup error", err)
	}
	if strings.Contains(err.Error(), "private-status") || strings.Contains(err.Error(), "private-list") {
		t.Fatalf("DeployDelete error leaked credentials: %v", err)
	}
	intent, getErr := db.GetDeploymentIntent(ctx, modelName)
	if getErr != nil {
		t.Fatalf("GetDeploymentIntent: %v", getErr)
	}
	if intent.DesiredState != recovery.DesiredStopped {
		t.Fatalf("desired_state = %q, want stopped despite unconfirmed runtime cleanup", intent.DesiredState)
	}
	if rt.deleteCalls != 0 {
		t.Fatalf("Runtime.Delete calls = %d, want zero without a confirmed target", rt.deleteCalls)
	}
}

func TestDeployDeleteWinsAgainstStaleReconciler(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, EngineAsset: "vllm-test", EngineVersion: "1.2.3", Runtime: "native",
		DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateWaiting, Policy: recovery.DefaultPolicy(),
		AttemptCount: 2, LastError: "pending recovery",
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	reconcilerReadIntent := make(chan struct{})
	continueReconciler := make(chan struct{})
	var nameCalls int32
	rt.beforeName = func() {
		if atomic.AddInt32(&nameCalls, 1) == 1 {
			close(reconcilerReadIntent)
			<-continueReconciler
		}
	}
	reconcileDone := make(chan error, 1)
	go func() {
		_, err := deps.DeployApply(deploymentReconcilerContext(t, ctx, db, modelName), "vllm", modelName, "", nil, true, recovery.PolicyPatch{})
		reconcileDone <- err
	}()

	select {
	case <-reconcilerReadIntent:
	case <-time.After(5 * time.Second):
		t.Fatal("reconciler did not reach post-intent-read barrier")
	}
	if err := deps.DeployDelete(ctx, modelName); err != nil {
		t.Fatalf("DeployDelete: %v", err)
	}
	stopped, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent after delete: %v", err)
	}
	if stopped.DesiredState != recovery.DesiredStopped {
		t.Fatalf("desired_state after delete = %q, want stopped", stopped.DesiredState)
	}
	close(continueReconciler)

	select {
	case err := <-reconcileDone:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("reconciler error = %v, want stopped intent rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconciler did not finish after barrier release")
	}
	if rt.deployCalls != 0 {
		t.Fatalf("Runtime.Deploy calls = %d, want 0", rt.deployCalls)
	}
	finalIntent, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent final: %v", err)
	}
	if finalIntent.DesiredState != recovery.DesiredStopped {
		t.Fatalf("final desired_state = %q, want stopped", finalIntent.DesiredState)
	}
}

func TestDeployDeleteWaitsForConcurrentApplyAndRediscoversInsideLock(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	applyAtDiscovery := make(chan struct{})
	continueApply := make(chan struct{})
	var listCalls int32
	rt.beforeList = func() {
		if atomic.AddInt32(&listCalls, 1) == 1 {
			close(applyAtDiscovery)
			<-continueApply
		}
	}
	rt.beforeDeploy = func(req *runtime.DeployRequest) {
		rt.statuses[req.Name] = &runtime.DeploymentStatus{
			Name: req.Name, Model: req.Name, Engine: req.Engine, Runtime: rt.name,
			Phase: "running", Ready: true,
			Labels: map[string]string{"aima.dev/model": req.Name},
		}
	}

	applyDone := make(chan error, 1)
	go func() {
		_, err := deps.DeployApply(ctx, "vllm", modelName, "", nil, true, recovery.PolicyPatch{})
		applyDone <- err
	}()
	select {
	case <-applyAtDiscovery:
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not reach the locked discovery barrier")
	}

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- deps.DeployDelete(ctx, "  "+strings.ToUpper(modelName)+"  ")
	}()
	select {
	case err := <-deleteDone:
		t.Fatalf("delete returned before the in-flight apply persisted its intent: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(continueApply)
	select {
	case err := <-applyDone:
		if err != nil {
			t.Fatalf("DeployApply: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not finish after releasing the discovery barrier")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("DeployDelete: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not rediscover the completed apply")
	}

	intent, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent: %v", err)
	}
	if intent.DesiredState != recovery.DesiredStopped {
		t.Fatalf("final desired_state = %q, want stopped", intent.DesiredState)
	}
	if rt.deployCalls != 1 || rt.deleteCalls != 1 {
		t.Fatalf("runtime calls deploy/delete = %d/%d, want 1/1", rt.deployCalls, rt.deleteCalls)
	}
	if len(rt.statuses) != 0 {
		t.Fatalf("runtime deployments after delete = %+v, want none", rt.statuses)
	}
}

func TestDeploymentOperationLocksNormalizeCaseWhitespaceAndReleaseEntries(t *testing.T) {
	locks := newDeploymentOperationLocks()
	unlockFirst := locks.lock("  Mixed-Case-Deployment  ")
	secondStarted := make(chan struct{})
	secondAcquired := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		close(secondStarted)
		unlockSecond := locks.lock("mixed-case-deployment")
		close(secondAcquired)
		unlockSecond()
		close(secondDone)
	}()
	<-secondStarted
	select {
	case <-secondAcquired:
		t.Fatal("case/whitespace variants acquired different deployment locks")
	case <-time.After(150 * time.Millisecond):
	}
	unlockFirst()
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("normalized deployment lock waiter did not finish")
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("deployment lock table retained %d entries after release", len(locks.entries))
	}
}

func TestDeploymentOperationLockCandidatesIncludeCatalogAndDiscoveredIdentity(t *testing.T) {
	cat := &knowledge.Catalog{ModelAssets: []knowledge.ModelAsset{{
		Metadata: knowledge.ModelMetadata{Name: "canonical-model", Aliases: []string{"catalog-alias"}},
	}}}
	applyNames := deploymentApplyLockNames(cat, "catalog-alias", "canonical-model", "computed-name", "intent-name", nil)
	deleteNames := deploymentCatalogLockNames(cat, "canonical-model")
	if !hasAllDeploymentOperationLockNames(applyNames, deleteNames) {
		t.Fatalf("apply candidates %v do not overlap canonical delete candidates %v", applyNames, deleteNames)
	}

	discovered := deploymentStatusLockNames(cat, &runtime.DeploymentStatus{
		Name: "Actual-Runtime-Name", Model: "catalog-alias",
		Labels: map[string]string{"aima.dev/model": "catalog-alias"},
	})
	for _, want := range []string{"actual-runtime-name", "catalog-alias", "canonical-model"} {
		if !hasAllDeploymentOperationLockNames(discovered, []string{want}) {
			t.Fatalf("discovered candidates %v missing %q", discovered, want)
		}
	}
}

func TestDeployDeleteCanonicalQueryRemovesAliasDeployment(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	alias := "alias-" + modelName
	rt.beforeDeploy = func(req *runtime.DeployRequest) {
		if got := req.Labels[proxy.LabelRequestedModel]; got != alias {
			t.Fatalf("requested model label = %q, want %q", got, alias)
		}
	}
	if _, err := deps.DeployApply(ctx, "vllm", alias, "", nil, true, recovery.PolicyPatch{}); err != nil {
		t.Fatalf("DeployApply alias: %v", err)
	}
	rt.statuses[alias] = &runtime.DeploymentStatus{
		Name: alias, Model: alias, Engine: "vllm", Runtime: rt.name,
		Phase: "running", Ready: true,
		Labels: map[string]string{"aima.dev/model": alias},
	}
	if err := deps.DeployDelete(ctx, modelName); err != nil {
		t.Fatalf("DeployDelete canonical query: %v", err)
	}
	intent, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent canonical model: %v", err)
	}
	if intent.DesiredState != recovery.DesiredStopped {
		t.Fatalf("alias intent desired_state = %q, want stopped", intent.DesiredState)
	}
	if rt.deleteCalls != 1 || len(rt.statuses) != 0 {
		t.Fatalf("runtime delete calls/statuses = %d/%+v, want 1/none", rt.deleteCalls, rt.statuses)
	}
}

func TestDeployDeleteQuarantinedIntentWithoutRuntimeSucceeds(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, DesiredState: recovery.DesiredRunning,
		RecoveryState: recovery.StateQuarantined, Policy: recovery.DefaultPolicy(),
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	if err := deps.DeployDelete(ctx, modelName); err != nil {
		t.Fatalf("DeployDelete: %v", err)
	}
	if rt.deleteCalls != 0 {
		t.Fatalf("Runtime.Delete calls = %d, want 0", rt.deleteCalls)
	}
	got, err := db.GetDeploymentIntent(ctx, modelName)
	if err != nil {
		t.Fatalf("GetDeploymentIntent: %v", err)
	}
	if got.DesiredState != recovery.DesiredStopped {
		t.Fatalf("desired_state = %q, want stopped", got.DesiredState)
	}
}

func TestDeployListAndStatusSynthesizeQuarantinedDeploymentIntent(t *testing.T) {
	ctx, db, deps, _, modelName := newDeploymentIntentHarness(t)
	nextAttemptAt := time.Date(2026, 7, 30, 8, 9, 10, 0, time.UTC)
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, EngineAsset: "vllm-test", EngineVersion: "1.2.3",
		Runtime: "native", DesiredState: recovery.DesiredRunning, RecoveryState: recovery.StateQuarantined,
		Policy: recovery.DefaultPolicy(), AttemptCount: 5, NextAttemptAt: nextAttemptAt,
		LastError: "restart budget exhausted",
	}); err != nil {
		t.Fatalf("seed quarantined intent: %v", err)
	}
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: "ordinary-stopped", Model: "ordinary-stopped", DesiredState: recovery.DesiredStopped,
		RecoveryState: recovery.StateHealthy, Policy: recovery.DefaultPolicy(),
	}); err != nil {
		t.Fatalf("seed stopped intent: %v", err)
	}

	rawList, err := deps.DeployList(ctx)
	if err != nil {
		t.Fatalf("DeployList: %v", err)
	}
	var list []deploymentOverview
	if err := json.Unmarshal(rawList, &list); err != nil {
		t.Fatalf("unmarshal DeployList: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("DeployList = %+v, want one quarantined desired-running entry", list)
	}
	if list[0].Name != modelName || list[0].DesiredState != recovery.DesiredRunning || list[0].RecoveryState != recovery.StateQuarantined || list[0].RecoveryAttempts != 5 || list[0].NextRecoveryAt != nextAttemptAt.Format(time.RFC3339) || list[0].QuarantineReason != "restart budget exhausted" {
		t.Fatalf("quarantined overview = %+v", list[0])
	}

	rawStatus, err := deps.DeployStatus(ctx, modelName)
	if err != nil {
		t.Fatalf("DeployStatus: %v", err)
	}
	var status runtime.DeploymentStatus
	if err := json.Unmarshal(rawStatus, &status); err != nil {
		t.Fatalf("unmarshal DeployStatus: %v", err)
	}
	if status.Name != modelName || status.Phase != recovery.StateQuarantined || status.DesiredState != recovery.DesiredRunning || status.RecoveryState != recovery.StateQuarantined || status.RecoveryAttempts != 5 || status.NextRecoveryAt != nextAttemptAt.Format(time.RFC3339) || status.QuarantineReason != "restart budget exhausted" {
		t.Fatalf("quarantined status = %+v", status)
	}
}

func TestDeployStatusEnrichesActualDeploymentIntent(t *testing.T) {
	ctx, db, deps, rt, modelName := newDeploymentIntentHarness(t)
	if err := db.UpsertDeploymentIntent(ctx, &recovery.Intent{
		Name: modelName, Model: modelName, DesiredState: recovery.DesiredRunning,
		RecoveryState: recovery.StateWaiting, Policy: recovery.DefaultPolicy(), AttemptCount: 2,
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	rt.statuses[modelName] = &runtime.DeploymentStatus{
		Name: modelName, Model: modelName, Runtime: "native", Phase: "failed",
		Labels: map[string]string{"aima.dev/model": modelName},
	}

	raw, err := deps.DeployStatus(ctx, modelName)
	if err != nil {
		t.Fatalf("DeployStatus: %v", err)
	}
	var status runtime.DeploymentStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("unmarshal DeployStatus: %v", err)
	}
	if status.Phase != "failed" || status.DesiredState != recovery.DesiredRunning || status.RecoveryState != recovery.StateWaiting || status.RecoveryAttempts != 2 {
		t.Fatalf("enriched actual status = %+v", status)
	}
}
