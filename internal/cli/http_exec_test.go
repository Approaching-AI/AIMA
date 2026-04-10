package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jguan/aima/internal/engine"
)

func TestSplitCommandLine(t *testing.T) {
	got, err := splitCommandLine(`config set llm.model "qwen 3"`)
	if err != nil {
		t.Fatalf("splitCommandLine returned error: %v", err)
	}

	want := []string{"config", "set", "llm.model", "qwen 3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestExecuteLineDeployUsesRealCLIFlags(t *testing.T) {
	app := testApp(t)

	var (
		gotEngine string
		gotModel  string
		gotSlot   string
		gotConfig map[string]any
	)
	app.ToolDeps.DeployApply = func(ctx context.Context, engine, model, slot string, config map[string]any, noPull bool) (json.RawMessage, error) {
		gotEngine = engine
		gotModel = model
		gotSlot = slot
		gotConfig = config
		return json.RawMessage(`{"status":"ok"}`), nil
	}

	result := ExecuteLine(context.Background(), app, `deploy qwen3-8b --engine llamacpp --slot slot-1 --config gpu_memory_utilization=0.9 --config max_model_len=4096 --max-cold-start 12`, nil)
	if result.ExitCode != 0 {
		t.Fatalf("ExecuteLine exit_code=%d error=%q output=%q", result.ExitCode, result.Error, result.Output)
	}

	if gotEngine != "llamacpp" {
		t.Fatalf("engine = %q, want %q", gotEngine, "llamacpp")
	}
	if gotModel != "qwen3-8b" {
		t.Fatalf("model = %q, want %q", gotModel, "qwen3-8b")
	}
	if gotSlot != "slot-1" {
		t.Fatalf("slot = %q, want %q", gotSlot, "slot-1")
	}
	if gotConfig["gpu_memory_utilization"] != 0.9 {
		t.Fatalf("gpu_memory_utilization = %#v, want 0.9", gotConfig["gpu_memory_utilization"])
	}
	if gotConfig["max_model_len"] != 4096 {
		t.Fatalf("max_model_len = %#v, want 4096", gotConfig["max_model_len"])
	}
	if gotConfig["max_cold_start_s"] != 12 {
		t.Fatalf("max_cold_start_s = %#v, want 12", gotConfig["max_cold_start_s"])
	}
}

func TestExecuteLineRunUsesRealCLIFlags(t *testing.T) {
	app := testApp(t)

	var (
		gotEngine string
		gotModel  string
		gotSlot   string
		gotConfig map[string]any
		gotNoPull bool
	)
	app.ToolDeps.DeployRun = func(ctx context.Context, model, engineType, slot string, config map[string]any, noPull bool, onPhase func(string, string), onProgress func(engine.ProgressEvent)) (json.RawMessage, error) {
		gotEngine = engineType
		gotModel = model
		gotSlot = slot
		gotConfig = config
		gotNoPull = noPull
		return json.RawMessage(`{"status":"ready","name":"qwen3-8b-llamacpp","address":"127.0.0.1:8080","runtime":"native"}`), nil
	}

	result := ExecuteLine(context.Background(), app, `run qwen3-8b --engine llamacpp --slot slot-2 --config gpu_memory_utilization=0.92 --config max_model_len=8192 --max-cold-start 18 --no-pull`, nil)
	if result.ExitCode != 0 {
		t.Fatalf("ExecuteLine exit_code=%d error=%q output=%q", result.ExitCode, result.Error, result.Output)
	}

	if gotEngine != "llamacpp" {
		t.Fatalf("engine = %q, want %q", gotEngine, "llamacpp")
	}
	if gotModel != "qwen3-8b" {
		t.Fatalf("model = %q, want %q", gotModel, "qwen3-8b")
	}
	if gotSlot != "slot-2" {
		t.Fatalf("slot = %q, want %q", gotSlot, "slot-2")
	}
	if !gotNoPull {
		t.Fatal("expected no-pull=true")
	}
	if gotConfig["gpu_memory_utilization"] != 0.92 {
		t.Fatalf("gpu_memory_utilization = %#v, want 0.92", gotConfig["gpu_memory_utilization"])
	}
	if gotConfig["max_model_len"] != 8192 {
		t.Fatalf("max_model_len = %#v, want 8192", gotConfig["max_model_len"])
	}
	if gotConfig["max_cold_start_s"] != 18 {
		t.Fatalf("max_cold_start_s = %#v, want 18", gotConfig["max_cold_start_s"])
	}
}

func TestExecuteLineUndeployUsesRealCLIArgs(t *testing.T) {
	app := testApp(t)

	var gotName string
	app.ToolDeps.DeployDelete = func(ctx context.Context, name string) error {
		gotName = name
		return nil
	}

	result := ExecuteLine(context.Background(), app, `undeploy qwen3-8b-vllm`, nil)
	if result.ExitCode != 0 {
		t.Fatalf("ExecuteLine exit_code=%d error=%q output=%q", result.ExitCode, result.Error, result.Output)
	}
	if gotName != "qwen3-8b-vllm" {
		t.Fatalf("name = %q, want %q", gotName, "qwen3-8b-vllm")
	}
}

func TestExecuteLineConfigGetUsesCLIOutput(t *testing.T) {
	app := testApp(t)
	app.ToolDeps.GetConfig = func(ctx context.Context, key string) (string, error) {
		if key != "llm.model" {
			t.Fatalf("unexpected key %q", key)
		}
		return "qwen3-8b", nil
	}

	result := ExecuteLine(context.Background(), app, `config get llm.model`, nil)
	if result.ExitCode != 0 {
		t.Fatalf("ExecuteLine exit_code=%d error=%q output=%q", result.ExitCode, result.Error, result.Output)
	}
	if result.Output != "qwen3-8b\n" {
		t.Fatalf("output = %q, want %q", result.Output, "qwen3-8b\n")
	}
}
