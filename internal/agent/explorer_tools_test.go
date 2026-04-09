package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplorerToolCat(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	_ = os.WriteFile(filepath.Join(dir, "plan.md"), []byte("hello world"), 0644)

	tools := NewExplorerToolExecutor(ws, nil)
	result := tools.Execute("cat", json.RawMessage(`{"path":"plan.md"}`))
	if result.IsError {
		t.Fatalf("cat error: %s", result.Content)
	}
	if result.Content != "hello world" {
		t.Errorf("cat: %q", result.Content)
	}
}

func TestExplorerToolLs(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	_ = os.WriteFile(filepath.Join(dir, "plan.md"), []byte("x"), 0644)

	tools := NewExplorerToolExecutor(ws, nil)
	result := tools.Execute("ls", json.RawMessage(`{}`))
	if result.IsError {
		t.Fatalf("ls error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "plan.md") {
		t.Errorf("ls: %s", result.Content)
	}
}

func TestExplorerToolWriteAndCat(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	tools := NewExplorerToolExecutor(ws, nil)

	result := tools.Execute("write", json.RawMessage(`{"path":"plan.md","content":"# Plan\ntest\n"}`))
	if result.IsError {
		t.Fatalf("write error: %s", result.Content)
	}

	result = tools.Execute("cat", json.RawMessage(`{"path":"plan.md"}`))
	if !strings.Contains(result.Content, "# Plan") {
		t.Errorf("cat after write: %s", result.Content)
	}
}

func TestExplorerToolWriteReadOnly(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	tools := NewExplorerToolExecutor(ws, nil)
	result := tools.Execute("write", json.RawMessage(`{"path":"device-profile.md","content":"hack"}`))
	if !result.IsError {
		t.Error("write to read-only should fail")
	}
}

func TestExplorerToolGrep(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	_ = os.WriteFile(filepath.Join(dir, "plan.md"), []byte("line1\nfoo bar\nline3\n"), 0644)

	tools := NewExplorerToolExecutor(ws, nil)
	result := tools.Execute("grep", json.RawMessage(`{"pattern":"foo","path":"plan.md"}`))
	if result.IsError {
		t.Fatalf("grep error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "foo bar") {
		t.Errorf("grep: %s", result.Content)
	}
}

func TestExplorerToolDone(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	tools := NewExplorerToolExecutor(ws, nil)
	result := tools.Execute("done", json.RawMessage(`{"verdict":"continue"}`))
	if result.IsError {
		t.Fatalf("done error: %s", result.Content)
	}
	if tools.Verdict() != "continue" {
		t.Errorf("verdict=%s", tools.Verdict())
	}
}

func TestExplorerToolDefinitions(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	tools := NewExplorerToolExecutor(ws, nil)
	defs := tools.ToolDefinitions()
	if len(defs) != 7 {
		t.Errorf("got %d tool definitions, want 7", len(defs))
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"cat", "ls", "write", "append", "grep", "query", "done"} {
		if !names[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
}
