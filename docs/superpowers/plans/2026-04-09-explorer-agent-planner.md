# Explorer Agent Planner 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Explorer LLM Planner 从单次 JSON prompt 重构为文档驱动的 Agent 工作流（PDCA 循环）

**Architecture:** LLM 作为 tool-calling agent，通过 6 个 bash-like tools 读写文档工作区（`~/.aima/explorer/`）。每轮 explore 执行 Plan → Do → Check → Act 循环。Go 侧解析 LLM 写入的 YAML，透传执行 deploy/benchmark/undeploy。

**Tech Stack:** Go, `gopkg.in/yaml.v3`, OpenAI-compatible tool calling（现有 `agent.OpenAIClient`）, SQLite

**Spec:** `docs/superpowers/specs/2026-04-09-explorer-agent-planner-design.md`

---

## 文件结构

| 操作 | 文件 | 职责 |
|------|------|------|
| Create | `internal/agent/explorer_workspace.go` | 文档工作区管理：目录初始化、事实文档生成、文件读写、YAML 解析 |
| Create | `internal/agent/explorer_workspace_test.go` | 工作区单元测试 |
| Create | `internal/agent/explorer_tools.go` | 6+1 个 tools 定义 + 执行器（cat/ls/write/append/grep/query/done） |
| Create | `internal/agent/explorer_tools_test.go` | Tools 单元测试 |
| Create | `internal/agent/explorer_agent_planner.go` | Agent Planner：tool-calling agent loop + Plan()/Analyze() |
| Create | `internal/agent/explorer_agent_planner_test.go` | Agent Planner 单元测试 |
| Modify | `internal/agent/explorer_planner.go` | 新增 TaskSpec/RecommendedConfig 类型，扩展 Planner 接口 |
| Modify | `internal/agent/explorer.go:320-520` | handleEvent 重构为 PDCA 循环，ExplorerConfig 新增字段 |
| Modify | `cmd/aima/main.go:594-660` | 引擎发现解耦：移除 installedEnginesContainResolvedAsset 检查 |
| Delete | `internal/agent/explorer_llmplanner.go` | 被 explorer_agent_planner.go 替代 |

---

### Task 1: TaskSpec + RecommendedConfig 类型与 YAML 解析

**Files:**
- Modify: `internal/agent/explorer_planner.go`
- Create: `internal/agent/explorer_workspace.go` (仅 YAML 解析函数)
- Create: `internal/agent/explorer_workspace_test.go`

- [ ] **Step 1: 写 TaskSpec 解析的失败测试**

```go
// internal/agent/explorer_workspace_test.go
package agent

import (
	"testing"
)

func TestParsePlanTasks(t *testing.T) {
	md := `# Exploration Plan

## Strategy
Test vllm on this device for the first time.

## Tasks
` + "```yaml\n" + `- kind: validate
  model: gemma-4-31B-it
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
    tensor_parallel_size: 2
    max_model_len: 4096
  benchmark:
    concurrency: [1, 4]
    input_tokens: [128, 512]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "first vllm test on this device"

- kind: tune
  model: qwen3.5-27b
  engine: sglang-kt
  engine_params:
    gpu_memory_utilization: 0.70
    cpu_offload_gb: 20
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 2
  reason: "reduce gmu to avoid OOM"
` + "```\n"

	tasks, err := parsePlanTasks(md)
	if err != nil {
		t.Fatalf("parsePlanTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].Kind != "validate" || tasks[0].Model != "gemma-4-31B-it" {
		t.Errorf("task 0: kind=%s model=%s", tasks[0].Kind, tasks[0].Model)
	}
	if tasks[0].EngineParams["tensor_parallel_size"] != 2 {
		t.Errorf("task 0 tp=%v", tasks[0].EngineParams["tensor_parallel_size"])
	}
	if len(tasks[0].Benchmark.Concurrency) != 2 {
		t.Errorf("task 0 concurrency=%v", tasks[0].Benchmark.Concurrency)
	}
	if tasks[1].Kind != "tune" || tasks[1].Engine != "sglang-kt" {
		t.Errorf("task 1: kind=%s engine=%s", tasks[1].Kind, tasks[1].Engine)
	}
}

func TestParseRecommendedConfigs(t *testing.T) {
	md := `# Exploration Summary

## Key Findings
- sglang-kt has 20% speedup for MoE models

## Recommended Configurations
` + "```yaml\n" + `- model: gemma-4-31B-it
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params:
    gpu_memory_utilization: 0.90
    tensor_parallel_size: 2
  performance:
    throughput_tps: 95.2
    latency_p50_ms: 42
  confidence: validated
  note: "first validation passed"
` + "```\n" + `
## Current Strategy
Focus on engine comparison.
`

	configs, err := parseRecommendedConfigs(md)
	if err != nil {
		t.Fatalf("parseRecommendedConfigs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if configs[0].Model != "gemma-4-31B-it" || configs[0].Confidence != "validated" {
		t.Errorf("config 0: model=%s confidence=%s", configs[0].Model, configs[0].Confidence)
	}
	if configs[0].Performance.ThroughputTPS != 95.2 {
		t.Errorf("config 0 throughput=%f", configs[0].Performance.ThroughputTPS)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestParsePlanTasks|TestParseRecommendedConfigs' -v`
Expected: FAIL — functions not defined

- [ ] **Step 3: 在 explorer_planner.go 中添加 TaskSpec 和 RecommendedConfig 类型**

在 `internal/agent/explorer_planner.go` 的 `ExplorerPlan` 定义之后（约 line 108）添加：

```go
// TaskSpec is an LLM-authored exploration task parsed from plan.md YAML.
// The LLM fills in all structured fields; Go transparently executes.
type TaskSpec struct {
	Kind         string         `yaml:"kind" json:"kind"`     // "validate" | "tune"
	Model        string         `yaml:"model" json:"model"`
	Engine       string         `yaml:"engine" json:"engine"`
	EngineParams map[string]any `yaml:"engine_params" json:"engine_params,omitempty"`
	Benchmark    BenchmarkSpec  `yaml:"benchmark" json:"benchmark"`
	Reason       string         `yaml:"reason" json:"reason"`
}

// BenchmarkSpec defines the benchmark matrix for one task.
type BenchmarkSpec struct {
	Concurrency     []int `yaml:"concurrency" json:"concurrency"`
	InputTokens     []int `yaml:"input_tokens" json:"input_tokens"`
	MaxTokens       []int `yaml:"max_tokens" json:"max_tokens"`
	RequestsPerCombo int  `yaml:"requests_per_combo" json:"requests_per_combo"`
}

// RecommendedConfig is an LLM-authored golden configuration from summary.md YAML.
type RecommendedConfig struct {
	Model        string         `yaml:"model" json:"model"`
	Engine       string         `yaml:"engine" json:"engine"`
	Hardware     string         `yaml:"hardware" json:"hardware"`
	EngineParams map[string]any `yaml:"engine_params" json:"engine_params,omitempty"`
	Performance  PerfSummary    `yaml:"performance" json:"performance"`
	Confidence   string         `yaml:"confidence" json:"confidence"` // "validated" | "tuned" | "provisional"
	Note         string         `yaml:"note" json:"note,omitempty"`
}

// PerfSummary captures key performance metrics.
type PerfSummary struct {
	ThroughputTPS float64 `yaml:"throughput_tps" json:"throughput_tps"`
	LatencyP50Ms  float64 `yaml:"latency_p50_ms" json:"latency_p50_ms"`
}
```

- [ ] **Step 4: 在 explorer_workspace.go 中实现 YAML 解析函数**

```go
// internal/agent/explorer_workspace.go
package agent

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlBlockRe matches a fenced yaml code block.
var yamlBlockRe = regexp.MustCompile("(?s)```ya?ml\n(.*?)```")

// parsePlanTasks extracts TaskSpec list from plan.md markdown.
// Looks for the yaml code block under "## Tasks".
func parsePlanTasks(md string) ([]TaskSpec, error) {
	section := extractSection(md, "## Tasks")
	if section == "" {
		return nil, fmt.Errorf("no ## Tasks section found")
	}
	matches := yamlBlockRe.FindStringSubmatch(section)
	if len(matches) < 2 {
		return nil, fmt.Errorf("no yaml code block in ## Tasks section")
	}
	var tasks []TaskSpec
	if err := yaml.Unmarshal([]byte(matches[1]), &tasks); err != nil {
		return nil, fmt.Errorf("parse tasks yaml: %w", err)
	}
	return tasks, nil
}

// parseRecommendedConfigs extracts RecommendedConfig list from summary.md.
// Looks for the yaml code block under "## Recommended Configurations".
func parseRecommendedConfigs(md string) ([]RecommendedConfig, error) {
	section := extractSection(md, "## Recommended Configurations")
	if section == "" {
		return nil, nil // no recommendations yet is normal
	}
	matches := yamlBlockRe.FindStringSubmatch(section)
	if len(matches) < 2 {
		return nil, nil
	}
	var configs []RecommendedConfig
	if err := yaml.Unmarshal([]byte(matches[1]), &configs); err != nil {
		return nil, fmt.Errorf("parse recommendations yaml: %w", err)
	}
	return configs, nil
}

// extractSection returns the content from a markdown heading until the next
// heading of equal or higher level (or end of document).
func extractSection(md, heading string) string {
	level := strings.Count(strings.TrimRight(heading, " "), "#")
	idx := strings.Index(md, heading)
	if idx == -1 {
		return ""
	}
	rest := md[idx+len(heading):]
	// Find next heading of same or higher level
	prefix := strings.Repeat("#", level) + " "
	for i := 0; i < len(rest); i++ {
		if i == 0 || rest[i-1] == '\n' {
			remaining := rest[i:]
			if strings.HasPrefix(remaining, prefix) || (level > 1 && strings.HasPrefix(remaining, strings.Repeat("#", level-1)+" ")) {
				return rest[:i]
			}
		}
	}
	return rest
}
```

- [ ] **Step 5: 添加 yaml.v3 依赖**

Run: `cd /Users/jguan/projects/AIMA && go get gopkg.in/yaml.v3`

- [ ] **Step 6: 运行测试确认通过**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestParsePlanTasks|TestParseRecommendedConfigs' -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/agent/explorer_planner.go internal/agent/explorer_workspace.go internal/agent/explorer_workspace_test.go go.mod go.sum
git commit -m "feat(explorer): add TaskSpec/RecommendedConfig types with YAML parsing"
```

---

### Task 2: ExplorerWorkspace — 文件操作与目录管理

**Files:**
- Modify: `internal/agent/explorer_workspace.go`
- Modify: `internal/agent/explorer_workspace_test.go`

- [ ] **Step 1: 写工作区初始化和文件操作的失败测试**

```go
// 追加到 explorer_workspace_test.go
func TestWorkspaceInit(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	if err := ws.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// experiments/ 子目录应该存在
	info, err := os.Stat(filepath.Join(dir, "experiments"))
	if err != nil || !info.IsDir() {
		t.Fatal("experiments/ dir not created")
	}
}

func TestWorkspaceReadWrite(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	// Write
	if err := ws.WriteFile("plan.md", "# Test Plan\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Read
	content, err := ws.ReadFile("plan.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if content != "# Test Plan\n" {
		t.Errorf("got %q", content)
	}
	// Append
	if err := ws.AppendFile("plan.md", "more\n"); err != nil {
		t.Fatalf("AppendFile: %v", err)
	}
	content, _ = ws.ReadFile("plan.md")
	if !strings.HasSuffix(content, "more\n") {
		t.Errorf("append failed: %q", content)
	}
}

func TestWorkspaceReadOnlyGuard(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	for _, name := range []string{"device-profile.md", "available-combos.md", "knowledge-base.md"} {
		if err := ws.WriteFile(name, "hack"); err == nil {
			t.Errorf("WriteFile(%s) should fail for read-only doc", name)
		}
	}
}

func TestWorkspaceListDir(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	_ = os.WriteFile(filepath.Join(dir, "plan.md"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "summary.md"), []byte("x"), 0644)

	entries, err := ws.ListDir(".")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("got %d entries, want >= 2", len(entries))
	}
}

func TestWorkspaceGrepFile(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()
	_ = os.WriteFile(filepath.Join(dir, "plan.md"), []byte("line1\nfoo bar\nline3\n"), 0644)

	matches, err := ws.GrepFile("foo", "plan.md")
	if err != nil {
		t.Fatalf("GrepFile: %v", err)
	}
	if len(matches) != 1 || !strings.Contains(matches[0], "foo bar") {
		t.Errorf("grep results: %v", matches)
	}
}

func TestWorkspacePathEscape(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	_, err := ws.ReadFile("../../etc/passwd")
	if err == nil {
		t.Error("path escape should fail")
	}
}
```

需要在测试文件顶部添加 `import "os"` 和 `"path/filepath"`.

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestWorkspace' -v`
Expected: FAIL

- [ ] **Step 3: 实现 ExplorerWorkspace 结构体和文件操作**

在 `explorer_workspace.go` 中（parsePlanTasks 之前）添加：

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// readOnlyDocs are AIMA-managed fact documents that the agent cannot write to.
var readOnlyDocs = map[string]bool{
	"device-profile.md":   true,
	"available-combos.md": true,
	"knowledge-base.md":   true,
}

// ExplorerWorkspace manages the document workspace at ~/.aima/explorer/.
type ExplorerWorkspace struct {
	root string
}

// NewExplorerWorkspace creates a workspace rooted at the given directory.
func NewExplorerWorkspace(root string) *ExplorerWorkspace {
	return &ExplorerWorkspace{root: root}
}

// Init creates the workspace directory structure.
func (w *ExplorerWorkspace) Init() error {
	return os.MkdirAll(filepath.Join(w.root, "experiments"), 0755)
}

// safePath resolves a relative path within the workspace, blocking escapes.
func (w *ExplorerWorkspace) safePath(rel string) (string, error) {
	abs := filepath.Join(w.root, filepath.Clean(rel))
	if !strings.HasPrefix(abs, w.root) {
		return "", fmt.Errorf("path escape: %s", rel)
	}
	return abs, nil
}

// ReadFile reads a file from the workspace.
func (w *ExplorerWorkspace) ReadFile(rel string) (string, error) {
	p, err := w.safePath(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFile writes content to a file in the workspace.
// Fails for read-only fact documents.
func (w *ExplorerWorkspace) WriteFile(rel, content string) error {
	base := filepath.Base(rel)
	if readOnlyDocs[base] {
		return fmt.Errorf("read-only: %s is managed by AIMA", base)
	}
	p, err := w.safePath(rel)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(p); dir != w.root {
		_ = os.MkdirAll(dir, 0755)
	}
	return os.WriteFile(p, []byte(content), 0644)
}

// writeFactDocument writes an AIMA-managed fact document (bypasses read-only guard).
func (w *ExplorerWorkspace) writeFactDocument(rel, content string) error {
	p, err := w.safePath(rel)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0644)
}

// AppendFile appends content to a file in the workspace.
func (w *ExplorerWorkspace) AppendFile(rel, content string) error {
	base := filepath.Base(rel)
	if readOnlyDocs[base] {
		return fmt.Errorf("read-only: %s is managed by AIMA", base)
	}
	p, err := w.safePath(rel)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// ListDir lists entries in a workspace directory.
func (w *ExplorerWorkspace) ListDir(rel string) ([]string, error) {
	p, err := w.safePath(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names[i] = name
	}
	return names, nil
}

// GrepFile searches for a pattern in a file, returning matching lines.
func (w *ExplorerWorkspace) GrepFile(pattern, rel string) ([]string, error) {
	content, err := w.ReadFile(rel)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	var matches []string
	for i, line := range strings.Split(content, "\n") {
		if re.MatchString(line) {
			matches = append(matches, fmt.Sprintf("%d:%s", i+1, line))
		}
	}
	return matches, nil
}

// GrepDir searches for a pattern across all files in a directory.
func (w *ExplorerWorkspace) GrepDir(pattern, rel string) ([]string, error) {
	p, err := w.safePath(rel)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	var results []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p, e.Name()))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d:%s", e.Name(), i+1, line))
			}
		}
	}
	return results, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestWorkspace' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_workspace.go internal/agent/explorer_workspace_test.go
git commit -m "feat(explorer): ExplorerWorkspace with file ops, read-only guard, path safety"
```

---

### Task 3: 事实文档生成器

**Files:**
- Modify: `internal/agent/explorer_workspace.go`
- Modify: `internal/agent/explorer_workspace_test.go`

- [ ] **Step 1: 写 RefreshFactDocuments 的失败测试**

```go
func TestRefreshFactDocuments(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	input := PlanInput{
		Hardware: HardwareInfo{
			Profile:  "nvidia-rtx4090-x86",
			GPUArch:  "Ada",
			GPUCount: 2,
			VRAMMiB:  49140,
		},
		LocalModels: []LocalModel{
			{Name: "qwen3-4b", Format: "safetensors", Type: "llm", SizeBytes: 7_500_000_000},
			{Name: "bge-m3", Format: "pytorch", Type: "embedding", SizeBytes: 2_000_000_000},
		},
		LocalEngines: []LocalEngine{
			{Name: "sglang-kt", Type: "sglang-kt", Runtime: "native", Features: []string{"cpu_gpu_hybrid_moe"},
				TunableParams: map[string]any{"gpu_memory_utilization": 0.90}},
			{Name: "vllm", Type: "vllm", Runtime: "container"},
		},
		ActiveDeploys: []DeployStatus{{Model: "qwen3-4b", Engine: "sglang-kt", Status: "running"}},
		SkipCombos: []SkipCombo{
			{Model: "qwen3-4b", Engine: "sglang-kt", Reason: "completed"},
		},
	}

	if err := ws.RefreshFactDocuments(input); err != nil {
		t.Fatalf("RefreshFactDocuments: %v", err)
	}

	// Check device-profile.md
	dp, err := os.ReadFile(filepath.Join(dir, "device-profile.md"))
	if err != nil {
		t.Fatalf("read device-profile.md: %v", err)
	}
	dpStr := string(dp)
	if !strings.Contains(dpStr, "RTX 4090") || !strings.Contains(dpStr, "49140") {
		t.Errorf("device-profile missing hardware: %s", dpStr[:200])
	}
	if !strings.Contains(dpStr, "qwen3-4b") {
		t.Error("device-profile missing model")
	}
	if !strings.Contains(dpStr, "sglang-kt") {
		t.Error("device-profile missing engine")
	}

	// Check available-combos.md
	ac, err := os.ReadFile(filepath.Join(dir, "available-combos.md"))
	if err != nil {
		t.Fatalf("read available-combos.md: %v", err)
	}
	acStr := string(ac)
	// qwen3-4b+sglang-kt is completed → should be in "Already Explored"
	if !strings.Contains(acStr, "Already Explored") {
		t.Error("available-combos missing Already Explored section")
	}
	// bge-m3 is embedding type → should be in Incompatible
	if !strings.Contains(acStr, "bge-m3") {
		t.Error("available-combos missing bge-m3 in Incompatible")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestRefreshFactDocuments -v`
Expected: FAIL

- [ ] **Step 3: 实现 RefreshFactDocuments**

在 `explorer_workspace.go` 中添加：

```go
import "time"

// RefreshFactDocuments regenerates the three AIMA-managed fact documents.
func (w *ExplorerWorkspace) RefreshFactDocuments(input PlanInput) error {
	now := time.Now().UTC().Format(time.RFC3339)

	if err := w.writeFactDocument("device-profile.md", generateDeviceProfile(input, now)); err != nil {
		return err
	}
	if err := w.writeFactDocument("available-combos.md", generateAvailableCombos(input, now)); err != nil {
		return err
	}
	return w.writeFactDocument("knowledge-base.md", generateKnowledgeBase(input, now))
}

func generateDeviceProfile(input PlanInput, now string) string {
	var b strings.Builder
	hw := input.Hardware
	totalVRAM := hw.VRAMMiB * hw.GPUCount
	if totalVRAM == 0 {
		totalVRAM = hw.VRAMMiB
	}

	fmt.Fprintf(&b, "# Device Profile\nUpdated: %s\n\n", now)
	fmt.Fprintf(&b, "## Hardware\n")
	fmt.Fprintf(&b, "- GPU: %d× %s (%d MiB each, %d MiB total)\n", hw.GPUCount, hw.GPUArch, hw.VRAMMiB, totalVRAM)
	fmt.Fprintf(&b, "- Hardware Profile: %s\n", hw.Profile)
	fmt.Fprintf(&b, "\n")

	// Models table
	fmt.Fprintf(&b, "## Models (%d available)\n", len(input.LocalModels))
	fmt.Fprintf(&b, "| Name | Format | Type | Size | Fits VRAM |\n")
	fmt.Fprintf(&b, "|------|--------|------|------|-----------|\n")
	for _, m := range input.LocalModels {
		sizeGiB := float64(m.SizeBytes) / (1024 * 1024 * 1024)
		fits := "✅"
		if totalVRAM > 0 {
			modelMiB := int(m.SizeBytes / (1024 * 1024))
			needed := modelMiB + modelMiB/4
			if needed > totalVRAM {
				fits = fmt.Sprintf("❌ (%dG > %dG)", needed/1024, totalVRAM/1024)
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.1fG | %s |\n", m.Name, m.Format, m.Type, sizeGiB, fits)
	}
	fmt.Fprintf(&b, "\n")

	// Engines table
	fmt.Fprintf(&b, "## Engines (%d available)\n", len(input.LocalEngines))
	fmt.Fprintf(&b, "| Type | Runtime | Features | Tunable Params |\n")
	fmt.Fprintf(&b, "|------|---------|----------|----------------|\n")
	for _, e := range input.LocalEngines {
		features := strings.Join(e.Features, ", ")
		if features == "" {
			features = "—"
		}
		params := "—"
		if len(e.TunableParams) > 0 {
			keys := make([]string, 0, len(e.TunableParams))
			for k := range e.TunableParams {
				keys = append(keys, k)
			}
			params = strings.Join(keys, ", ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", e.Type, e.Runtime, features, params)
	}
	fmt.Fprintf(&b, "\n")

	// Active deployments
	fmt.Fprintf(&b, "## Active Deployments\n")
	if len(input.ActiveDeploys) == 0 {
		fmt.Fprintf(&b, "(none running)\n")
	} else {
		for _, d := range input.ActiveDeploys {
			fmt.Fprintf(&b, "- %s on %s (%s)\n", d.Model, d.Engine, d.Status)
		}
	}
	return b.String()
}

func generateAvailableCombos(input PlanInput, now string) string {
	var b strings.Builder
	hw := input.Hardware
	totalVRAM := hw.VRAMMiB * hw.GPUCount
	if totalVRAM == 0 {
		totalVRAM = hw.VRAMMiB
	}
	modelFormats := localModelFormatMap(input.LocalModels)
	modelTypes := localModelTypeMap(input.LocalModels)

	// Build skip map
	skipMap := make(map[string]SkipCombo)
	for _, sc := range input.SkipCombos {
		skipMap[sc.Model+"|"+sc.Engine] = sc
	}

	type combo struct {
		Model, Engine, Status, Note string
	}
	var unexplored, explored, incompatible []combo

	for _, m := range input.LocalModels {
		for _, e := range input.LocalEngines {
			key := m.Name + "|" + e.Type

			// Check compatibility
			if !engineFormatCompatible(e.Type, m.Format) {
				incompatible = append(incompatible, combo{m.Name, e.Type, "", fmt.Sprintf("%s format incompatible with %s", m.Format, e.Type)})
				continue
			}
			if !engineSupportsModelType(e.Type, m.Type) {
				incompatible = append(incompatible, combo{m.Name, e.Type, "", fmt.Sprintf("%s type not supported by %s", m.Type, e.Type)})
				continue
			}
			if !modelFitsVRAM(m.Name, input.LocalModels, totalVRAM) {
				incompatible = append(incompatible, combo{m.Name, e.Type, "", "exceeds VRAM"})
				continue
			}

			if sc, ok := skipMap[key]; ok {
				explored = append(explored, combo{m.Name, e.Type, sc.Reason, ""})
			} else {
				unexplored = append(unexplored, combo{m.Name, e.Type, "", ""})
			}
		}
	}

	fmt.Fprintf(&b, "# Available Exploration Combos\nUpdated: %s\n\n", now)

	fmt.Fprintf(&b, "## Unexplored (%d combos)\n", len(unexplored))
	fmt.Fprintf(&b, "| Model | Engine |\n|-------|--------|\n")
	for _, c := range unexplored {
		fmt.Fprintf(&b, "| %s | %s |\n", c.Model, c.Engine)
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "## Already Explored (%d combos)\n", len(explored))
	fmt.Fprintf(&b, "| Model | Engine | Status |\n|-------|--------|--------|\n")
	for _, c := range explored {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Model, c.Engine, c.Status)
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "## Incompatible (%d combos)\n", len(incompatible))
	for _, c := range incompatible {
		fmt.Fprintf(&b, "- %s + %s — %s\n", c.Model, c.Engine, c.Note)
	}
	return b.String()
}

// KnowledgeBaseData provides DB-sourced data for knowledge-base.md.
type KnowledgeBaseData struct {
	GoldenConfigs    []RecommendedConfig
	BenchmarkHistory []BenchmarkHistoryEntry
	CrossDeviceHints []CrossDeviceHint
}

// BenchmarkHistoryEntry is a simplified benchmark record for the knowledge base doc.
type BenchmarkHistoryEntry struct {
	Model      string
	Engine     string
	Date       string
	Status     string
	Throughput float64
	Notes      string
}

// CrossDeviceHint is knowledge from central about other devices' results.
type CrossDeviceHint struct {
	Model      string
	Engine     string
	Hardware   string
	Throughput float64
	Confidence string
	Advisory   string
}

func generateKnowledgeBase(input PlanInput, now string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Knowledge Base\nUpdated: %s\n\n", now)

	fmt.Fprintf(&b, "## Advisories (%d pending)\n", len(input.Advisories))
	if len(input.Advisories) > 0 {
		fmt.Fprintf(&b, "| ID | Type | Model | Engine | Confidence | Reasoning |\n")
		fmt.Fprintf(&b, "|----|------|-------|--------|------------|----------|\n")
		for _, a := range input.Advisories {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
				a.ID, a.Type, a.TargetModel, a.TargetEngine, a.Confidence, a.Reasoning)
		}
	} else {
		fmt.Fprintf(&b, "(none)\n")
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "## Recent History (%d runs)\n", len(input.History))
	if len(input.History) > 0 {
		fmt.Fprintf(&b, "| Model | Engine | Kind | Status | Summary |\n")
		fmt.Fprintf(&b, "|-------|--------|------|--------|--------|\n")
		for _, h := range input.History {
			summary := h.SummaryJSON
			if len(summary) > 80 {
				summary = summary[:80] + "..."
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				h.ModelID, h.EngineID, h.Kind, h.Status, summary)
		}
	} else {
		fmt.Fprintf(&b, "(no exploration history yet)\n")
	}
	fmt.Fprintf(&b, "\n")

	// Engine capabilities from catalog
	fmt.Fprintf(&b, "## Catalog Engine Capabilities\n")
	fmt.Fprintf(&b, "| Engine | Supported Formats | Model Types | Key Features |\n")
	fmt.Fprintf(&b, "|--------|------------------|-------------|-------------|\n")
	for _, e := range input.LocalEngines {
		formats := "safetensors"
		if e.Type == "llamacpp" {
			formats = "gguf"
		}
		features := strings.Join(e.Features, ", ")
		if features == "" {
			features = "—"
		}
		fmt.Fprintf(&b, "| %s | %s | llm | %s |\n", e.Type, formats, features)
	}
	return b.String()
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestRefreshFactDocuments -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_workspace.go internal/agent/explorer_workspace_test.go
git commit -m "feat(explorer): fact document generators for workspace"
```

---

### Task 4: 实验文档写入 + ParsePlan/ExtractRecommendations 方法

**Files:**
- Modify: `internal/agent/explorer_workspace.go`
- Modify: `internal/agent/explorer_workspace_test.go`

- [ ] **Step 1: 写实验文档写入和提取方法的失败测试**

```go
func TestWriteExperimentResult(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	task := TaskSpec{
		Kind:   "validate",
		Model:  "gemma-4-31B-it",
		Engine: "vllm",
		EngineParams: map[string]any{
			"gpu_memory_utilization": 0.90,
			"tensor_parallel_size":  2,
		},
	}
	result := ExperimentResult{
		Status:    "completed",
		StartedAt: "2026-04-09T20:15:03Z",
		DurationS: 342,
		ColdStartS: 45,
		Benchmarks: []BenchmarkEntry{
			{Concurrency: 1, InputTokens: 128, MaxTokens: 256,
				ThroughputTPS: 95.2, LatencyP50Ms: 42, LatencyP99Ms: 118},
		},
	}

	path, err := ws.WriteExperimentResult(1, task, result)
	if err != nil {
		t.Fatalf("WriteExperimentResult: %v", err)
	}

	content, _ := ws.ReadFile(path)
	if !strings.Contains(content, "gemma-4-31B-it") {
		t.Error("experiment missing model name")
	}
	if !strings.Contains(content, "completed") {
		t.Error("experiment missing status")
	}
	if !strings.Contains(content, "95.2") {
		t.Error("experiment missing throughput")
	}
}

func TestParsePlanFromWorkspace(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planMD := `# Exploration Plan

## Strategy
Test two combos.

## Tasks
` + "```yaml\n" + `- kind: validate
  model: test-model
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "test"
` + "```\n"

	_ = ws.WriteFile("plan.md", planMD)
	tasks, err := ws.ParsePlan()
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Model != "test-model" {
		t.Errorf("ParsePlan: got %+v", tasks)
	}
}

func TestExtractRecommendations(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	summaryMD := `# Exploration Summary

## Key Findings
- vllm works

## Recommended Configurations
` + "```yaml\n" + `- model: test-model
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params:
    gpu_memory_utilization: 0.90
  performance:
    throughput_tps: 95.2
    latency_p50_ms: 42
  confidence: validated
  note: "first test"
` + "```\n"

	_ = ws.WriteFile("summary.md", summaryMD)
	configs, err := ws.ExtractRecommendations()
	if err != nil {
		t.Fatalf("ExtractRecommendations: %v", err)
	}
	if len(configs) != 1 || configs[0].Model != "test-model" {
		t.Errorf("got %+v", configs)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestWriteExperiment|TestParsePlanFromWorkspace|TestExtractRecommendations' -v`
Expected: FAIL

- [ ] **Step 3: 实现 ExperimentResult 类型和方法**

在 `explorer_workspace.go` 中添加：

```go
// ExperimentResult captures the outcome of a single exploration task.
type ExperimentResult struct {
	Status     string           `yaml:"status"`
	StartedAt  string           `yaml:"started_at"`
	DurationS  float64          `yaml:"duration_s"`
	ColdStartS float64          `yaml:"cold_start_s,omitempty"`
	Error      string           `yaml:"error,omitempty"`
	Benchmarks []BenchmarkEntry `yaml:"benchmarks,omitempty"`
}

// BenchmarkEntry is one cell in the benchmark matrix.
type BenchmarkEntry struct {
	Concurrency   int     `yaml:"concurrency"`
	InputTokens   int     `yaml:"input_tokens"`
	MaxTokens     int     `yaml:"max_tokens"`
	ThroughputTPS float64 `yaml:"throughput_tps"`
	LatencyP50Ms  float64 `yaml:"latency_p50_ms"`
	LatencyP99Ms  float64 `yaml:"latency_p99_ms"`
}

// WriteExperimentResult writes an experiment markdown file and returns its relative path.
func (w *ExplorerWorkspace) WriteExperimentResult(index int, task TaskSpec, result ExperimentResult) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Experiment: %s + %s\n\n", task.Model, task.Engine)

	// Task YAML
	taskYAML, _ := yaml.Marshal(task)
	fmt.Fprintf(&b, "## Task\n```yaml\n%s```\n\n", string(taskYAML))

	// Result YAML
	resultYAML, _ := yaml.Marshal(result)
	fmt.Fprintf(&b, "## Result\n```yaml\n%s```\n\n", string(resultYAML))

	// Benchmark matrix table (human-readable)
	if len(result.Benchmarks) > 0 {
		fmt.Fprintf(&b, "## Benchmark Matrix\n")
		fmt.Fprintf(&b, "| Concurrency | Input Tokens | Max Tokens | Throughput (tok/s) | P50 (ms) | P99 (ms) |\n")
		fmt.Fprintf(&b, "|-------------|-------------|------------|-------------------|----------|----------|\n")
		for _, bm := range result.Benchmarks {
			fmt.Fprintf(&b, "| %d | %d | %d | %.1f | %.0f | %.0f |\n",
				bm.Concurrency, bm.InputTokens, bm.MaxTokens,
				bm.ThroughputTPS, bm.LatencyP50Ms, bm.LatencyP99Ms)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Agent Notes\n<!-- Agent fills in analysis during Check phase -->\n")

	rel := fmt.Sprintf("experiments/%03d-%s-%s.md", index, task.Model, task.Engine)
	return rel, w.writeFactDocument(rel, b.String()) // use writeFactDocument to bypass guard for experiments
}

// ParsePlan reads plan.md and extracts TaskSpec list from the YAML block.
func (w *ExplorerWorkspace) ParsePlan() ([]TaskSpec, error) {
	content, err := w.ReadFile("plan.md")
	if err != nil {
		return nil, err
	}
	return parsePlanTasks(content)
}

// ExtractRecommendations reads summary.md and extracts RecommendedConfig list.
func (w *ExplorerWorkspace) ExtractRecommendations() ([]RecommendedConfig, error) {
	content, err := w.ReadFile("summary.md")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseRecommendedConfigs(content)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestWriteExperiment|TestParsePlanFromWorkspace|TestExtractRecommendations' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_workspace.go internal/agent/explorer_workspace_test.go
git commit -m "feat(explorer): experiment result writer, ParsePlan, ExtractRecommendations"
```

---

### Task 5: Explorer Tools — 定义 + 执行器

**Files:**
- Create: `internal/agent/explorer_tools.go`
- Create: `internal/agent/explorer_tools_test.go`

- [ ] **Step 1: 写 tools 执行的失败测试**

```go
// internal/agent/explorer_tools_test.go
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

	// Write plan.md
	result := tools.Execute("write", json.RawMessage(`{"path":"plan.md","content":"# Plan\ntest\n"}`))
	if result.IsError {
		t.Fatalf("write error: %s", result.Content)
	}

	// Read back
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestExplorerTool' -v`
Expected: FAIL

- [ ] **Step 3: 实现 explorer_tools.go**

```go
// internal/agent/explorer_tools.go
package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	state "github.com/jguan/aima/internal"
)

// ExplorerToolResult is the result of executing an explorer tool.
type ExplorerToolResult struct {
	Content string
	IsError bool
}

// QueryFunc executes a knowledge base query. Returns JSON result.
type QueryFunc func(qType string, filter map[string]any, limit int) (string, error)

// ExplorerToolExecutor dispatches explorer tool calls.
type ExplorerToolExecutor struct {
	ws      *ExplorerWorkspace
	queryFn QueryFunc
	verdict string
	done    bool
}

// NewExplorerToolExecutor creates a tool executor for the explorer agent.
func NewExplorerToolExecutor(ws *ExplorerWorkspace, queryFn QueryFunc) *ExplorerToolExecutor {
	return &ExplorerToolExecutor{ws: ws, queryFn: queryFn}
}

// Verdict returns the verdict set by the done tool (empty if not called or Plan phase).
func (e *ExplorerToolExecutor) Verdict() string { return e.verdict }

// Done returns true if the done tool was called.
func (e *ExplorerToolExecutor) Done() bool { return e.done }

// Reset clears done/verdict state for a new phase.
func (e *ExplorerToolExecutor) Reset() {
	e.done = false
	e.verdict = ""
}

// Execute dispatches a tool call by name.
func (e *ExplorerToolExecutor) Execute(name string, args json.RawMessage) ExplorerToolResult {
	switch name {
	case "cat":
		return e.execCat(args)
	case "ls":
		return e.execLs(args)
	case "write":
		return e.execWrite(args)
	case "append":
		return e.execAppend(args)
	case "grep":
		return e.execGrep(args)
	case "query":
		return e.execQuery(args)
	case "done":
		return e.execDone(args)
	default:
		return ExplorerToolResult{Content: fmt.Sprintf("unknown tool: %s", name), IsError: true}
	}
}

func (e *ExplorerToolExecutor) execCat(args json.RawMessage) ExplorerToolResult {
	var p struct{ Path string `json:"path"` }
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	content, err := e.ws.ReadFile(p.Path)
	if err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: content}
}

func (e *ExplorerToolExecutor) execLs(args json.RawMessage) ExplorerToolResult {
	var p struct{ Path string `json:"path"` }
	_ = json.Unmarshal(args, &p)
	if p.Path == "" {
		p.Path = "."
	}
	entries, err := e.ws.ListDir(p.Path)
	if err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: strings.Join(entries, "\n")}
}

func (e *ExplorerToolExecutor) execWrite(args json.RawMessage) ExplorerToolResult {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if err := e.ws.WriteFile(p.Path, p.Content); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: "ok"}
}

func (e *ExplorerToolExecutor) execAppend(args json.RawMessage) ExplorerToolResult {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if err := e.ws.AppendFile(p.Path, p.Content); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: "ok"}
}

func (e *ExplorerToolExecutor) execGrep(args json.RawMessage) ExplorerToolResult {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if p.Path == "" {
		p.Path = "."
	}

	// If path is a directory, grep across all files
	var matches []string
	var err error
	if strings.HasSuffix(p.Path, "/") || p.Path == "." {
		matches, err = e.ws.GrepDir(p.Pattern, p.Path)
	} else {
		matches, err = e.ws.GrepFile(p.Pattern, p.Path)
	}
	if err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if len(matches) == 0 {
		return ExplorerToolResult{Content: "(no matches)"}
	}
	return ExplorerToolResult{Content: strings.Join(matches, "\n")}
}

func (e *ExplorerToolExecutor) execQuery(args json.RawMessage) ExplorerToolResult {
	var p struct {
		Type   string         `json:"type"`
		Filter map[string]any `json:"filter"`
		Limit  int            `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if e.queryFn == nil {
		return ExplorerToolResult{Content: "query not available (no database)", IsError: true}
	}
	result, err := e.queryFn(p.Type, p.Filter, p.Limit)
	if err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: result}
}

func (e *ExplorerToolExecutor) execDone(args json.RawMessage) ExplorerToolResult {
	var p struct{ Verdict string `json:"verdict"` }
	_ = json.Unmarshal(args, &p)
	e.done = true
	e.verdict = p.Verdict
	return ExplorerToolResult{Content: "ok"}
}

// ToolDefinitions returns OpenAI-compatible tool definitions for the explorer tools.
func (e *ExplorerToolExecutor) ToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "cat",
			Description: "Read file contents. Path relative to workspace root.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path relative to workspace"}},"required":["path"]}`),
		},
		{
			Name:        "ls",
			Description: "List directory entries. Default: workspace root.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory path (default: '.')"}}}`),
		},
		{
			Name:        "write",
			Description: "Write content to a file (overwrite). Cannot write AIMA-managed fact documents.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		},
		{
			Name:        "append",
			Description: "Append content to a file. Cannot write AIMA-managed fact documents.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		},
		{
			Name:        "grep",
			Description: "Search for pattern in file or directory. Returns matching lines with line numbers.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regex pattern"},"path":{"type":"string","description":"File or directory path (default: '.')"}},"required":["pattern"]}`),
		},
		{
			Name:        "query",
			Description: "Query the knowledge base (SQLite, read-only). Types: configurations, benchmarks, advisories, exploration_runs.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string","enum":["configurations","benchmarks","advisories","exploration_runs"]},"filter":{"type":"object"},"limit":{"type":"integer"}},"required":["type"]}`),
		},
		{
			Name:        "done",
			Description: "Signal that the current phase is complete. In Check phase, set verdict to 'continue' or 'done'.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string","enum":["continue","done"],"description":"Only for Check phase: continue=need more experiments, done=round complete"}}}`),
		},
	}
}
```

注意：`state` import 可能暂时不用（query 通过 QueryFunc 间接），上面代码先 import 但在编译时可能需要移除未用的 import。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestExplorerTool' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_tools.go internal/agent/explorer_tools_test.go
git commit -m "feat(explorer): 7 bash-like tools with definitions and executor"
```

---

### Task 6: ExplorerAgentPlanner — Agent Loop 核心

**Files:**
- Create: `internal/agent/explorer_agent_planner.go`
- Create: `internal/agent/explorer_agent_planner_test.go`

- [ ] **Step 1: 写 agent loop 的失败测试（mock LLM）**

```go
// internal/agent/explorer_agent_planner_test.go
package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// mockStreamingLLM is a test double that returns pre-scripted responses.
type mockStreamingLLM struct {
	responses []Response
	callIndex int
	calls     [][]Message
}

func (m *mockStreamingLLM) ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	m.calls = append(m.calls, messages)
	if m.callIndex >= len(m.responses) {
		return &Response{Content: ""}, nil
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return &resp, nil
}

func (m *mockStreamingLLM) ChatCompletionStream(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(CompletionDelta)) (*Response, error) {
	return m.ChatCompletion(ctx, messages, tools)
}

func TestRunPhase_PlanWritesAndDone(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planContent := `# Exploration Plan

## Strategy
Test combo.

## Tasks
` + "```yaml\n" + `- kind: validate
  model: test-model
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "test"
` + "```\n"

	mock := &mockStreamingLLM{
		responses: []Response{
			// Turn 1: LLM reads device-profile
			{ToolCalls: []ToolCall{
				{ID: "1", Name: "cat", Arguments: `{"path":"device-profile.md"}`},
			}},
			// Turn 2: LLM writes plan.md
			{ToolCalls: []ToolCall{
				{ID: "2", Name: "write", Arguments: `{"path":"plan.md","content":` + jsonEscape(planContent) + `}`},
			}},
			// Turn 3: LLM calls done
			{ToolCalls: []ToolCall{
				{ID: "3", Name: "done", Arguments: `{}`},
			}},
		},
	}

	// Populate device-profile so cat works
	_ = ws.writeFactDocument("device-profile.md", "# Device\n## Hardware\n- GPU: test\n")

	planner := &ExplorerAgentPlanner{
		llm:       mock,
		workspace: ws,
		maxTasks:  5,
		maxCycles: 3,
	}

	tokens, err := planner.runPhase(context.Background(), "plan", "test system prompt")
	if err != nil {
		t.Fatalf("runPhase: %v", err)
	}
	if tokens < 0 {
		t.Errorf("tokens=%d", tokens)
	}

	// Verify plan.md was written
	tasks, err := ws.ParsePlan()
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Model != "test-model" {
		t.Errorf("tasks: %+v", tasks)
	}
}

// jsonEscape returns a JSON string literal for content.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestRunPhase -v`
Expected: FAIL

- [ ] **Step 3: 实现 ExplorerAgentPlanner 核心结构和 runPhase**

```go
// internal/agent/explorer_agent_planner.go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// ExplorerAgentPlanner implements Planner using a tool-calling agent loop.
// It replaces LLMPlanner with a document-driven PDCA workflow.
type ExplorerAgentPlanner struct {
	llm       LLMClient // must also implement StreamingLLMClient for streaming
	workspace *ExplorerWorkspace
	tools     *ExplorerToolExecutor
	queryFn   QueryFunc
	maxCycles int
	maxTasks  int
}

// ExplorerAgentPlannerOption configures the ExplorerAgentPlanner.
type ExplorerAgentPlannerOption func(*ExplorerAgentPlanner)

// WithAgentMaxCycles sets the max PDCA iterations per round.
func WithAgentMaxCycles(n int) ExplorerAgentPlannerOption {
	return func(p *ExplorerAgentPlanner) { p.maxCycles = n }
}

// WithAgentMaxTasks sets the max tasks per plan.
func WithAgentMaxTasks(n int) ExplorerAgentPlannerOption {
	return func(p *ExplorerAgentPlanner) { p.maxTasks = n }
}

// WithAgentQueryFunc sets the knowledge base query function.
func WithAgentQueryFunc(fn QueryFunc) ExplorerAgentPlannerOption {
	return func(p *ExplorerAgentPlanner) { p.queryFn = fn }
}

// NewExplorerAgentPlanner creates a new agent planner.
func NewExplorerAgentPlanner(llm LLMClient, workspace *ExplorerWorkspace, opts ...ExplorerAgentPlannerOption) *ExplorerAgentPlanner {
	p := &ExplorerAgentPlanner{
		llm:       llm,
		workspace: workspace,
		maxCycles: 3,
		maxTasks:  5,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// runPhase executes one agent loop phase (plan, check, or act).
// The LLM reads/writes workspace documents via tool calls until it calls done().
// Returns total tokens used.
func (p *ExplorerAgentPlanner) runPhase(ctx context.Context, phase, systemPrompt string) (int, error) {
	// Create tool executor fresh for each phase
	tools := NewExplorerToolExecutor(p.workspace, p.queryFn)
	toolDefs := tools.ToolDefinitions()

	messages := []Message{
		{Role: "system", Content: systemPrompt},
	}

	var totalTokens int
	maxTurns := 30 // safety limit

	for turn := 0; turn < maxTurns; turn++ {
		select {
		case <-ctx.Done():
			return totalTokens, ctx.Err()
		default:
		}

		var resp *Response
		var err error
		if streamer, ok := p.llm.(StreamingLLMClient); ok {
			resp, err = streamer.ChatCompletionStream(ctx, messages, toolDefs, func(delta CompletionDelta) {
				// Could add streaming progress logging here
			})
		} else {
			resp, err = p.llm.ChatCompletion(ctx, messages, toolDefs)
		}
		if err != nil {
			return totalTokens, fmt.Errorf("LLM call in %s phase (turn %d): %w", phase, turn, err)
		}
		totalTokens += resp.TotalTokens

		// No tool calls → phase complete (LLM gave a text response)
		if len(resp.ToolCalls) == 0 {
			slog.Info("explorer agent: phase ended (no tool calls)", "phase", phase, "turn", turn)
			break
		}

		// Append assistant message with tool calls
		messages = append(messages, Message{
			Role:             "assistant",
			Content:          resp.Content,
			ReasoningContent: resp.ReasoningContent,
			ToolCalls:        resp.ToolCalls,
		})

		// Execute each tool call
		for _, tc := range resp.ToolCalls {
			slog.Debug("explorer agent: tool call", "phase", phase, "tool", tc.Name)
			result := tools.Execute(tc.Name, json.RawMessage(tc.Arguments))

			content := result.Content
			if result.IsError {
				content = "error: " + content
			}
			messages = append(messages, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})

			if tools.Done() {
				slog.Info("explorer agent: phase done", "phase", phase, "verdict", tools.Verdict(), "turn", turn)
				p.tools = tools // preserve for verdict access
				return totalTokens, nil
			}
		}
	}

	p.tools = tools
	return totalTokens, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestRunPhase -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_agent_planner.go internal/agent/explorer_agent_planner_test.go
git commit -m "feat(explorer): ExplorerAgentPlanner with runPhase agent loop"
```

---

### Task 7: ExplorerAgentPlanner — Plan() + Analyze() 方法

**Files:**
- Modify: `internal/agent/explorer_agent_planner.go`
- Modify: `internal/agent/explorer_agent_planner_test.go`
- Modify: `internal/agent/explorer_planner.go` (扩展 Planner 接口)

- [ ] **Step 1: 写 Plan() 和 Analyze() 的失败测试**

```go
func TestAgentPlannerPlan(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planYAML := `- kind: validate
  model: test-model
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "test"`

	planContent := "# Exploration Plan\n\n## Strategy\nTest.\n\n## Tasks\n```yaml\n" + planYAML + "\n```\n"

	mock := &mockStreamingLLM{
		responses: []Response{
			// Turn 1: cat device-profile
			{ToolCalls: []ToolCall{{ID: "1", Name: "cat", Arguments: `{"path":"device-profile.md"}`}}},
			// Turn 2: write plan.md
			{ToolCalls: []ToolCall{{ID: "2", Name: "write", Arguments: `{"path":"plan.md","content":` + jsonEscape(planContent) + `}`}}},
			// Turn 3: done
			{ToolCalls: []ToolCall{{ID: "3", Name: "done", Arguments: `{}`}}},
		},
	}

	input := PlanInput{
		Hardware: HardwareInfo{Profile: "nvidia-rtx4090-x86", GPUArch: "Ada", GPUCount: 2, VRAMMiB: 49140},
		LocalModels:  []LocalModel{{Name: "test-model", Format: "safetensors", Type: "llm", SizeBytes: 5_000_000_000}},
		LocalEngines: []LocalEngine{{Name: "vllm", Type: "vllm", Runtime: "container"}},
	}

	planner := NewExplorerAgentPlanner(mock, ws)
	plan, tokens, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if tokens <= 0 {
		t.Logf("tokens=%d (mock returns 0, ok)", tokens)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(plan.Tasks))
	}
	if plan.Tasks[0].Model != "test-model" {
		t.Errorf("task model=%s", plan.Tasks[0].Model)
	}
	if plan.Tier != 2 {
		t.Errorf("tier=%d", plan.Tier)
	}
}

func TestAgentPlannerAnalyze(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	summaryContent := `# Exploration Summary

## Key Findings
- vllm works well

## Recommended Configurations
` + "```yaml\n" + `- model: test-model
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params: {}
  performance:
    throughput_tps: 100.0
    latency_p50_ms: 40
  confidence: validated
  note: "good"
` + "```\n" + `
## Current Strategy
Done for now.
`

	mock := &mockStreamingLLM{
		responses: []Response{
			// Turn 1: cat experiment result
			{ToolCalls: []ToolCall{{ID: "1", Name: "cat", Arguments: `{"path":"experiments/001-test-model-vllm.md"}`}}},
			// Turn 2: write summary.md
			{ToolCalls: []ToolCall{{ID: "2", Name: "write", Arguments: `{"path":"summary.md","content":` + jsonEscape(summaryContent) + `}`}}},
			// Turn 3: done with verdict
			{ToolCalls: []ToolCall{{ID: "3", Name: "done", Arguments: `{"verdict":"done"}`}}},
		},
	}

	// Write a fake experiment result for the agent to read
	_ = ws.writeFactDocument("experiments/001-test-model-vllm.md", "# Experiment\n## Result\nstatus: completed\n")

	planner := NewExplorerAgentPlanner(mock, ws)
	verdict, extraTasks, _, err := planner.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if verdict != "done" {
		t.Errorf("verdict=%s", verdict)
	}
	if len(extraTasks) != 0 {
		t.Errorf("extraTasks=%d (expected 0 for verdict=done)", len(extraTasks))
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestAgentPlannerPlan|TestAgentPlannerAnalyze' -v`
Expected: FAIL

- [ ] **Step 3: 实现 Plan() 和 Analyze()**

在 `explorer_agent_planner.go` 中添加系统提示和方法：

```go
// Plan implements Planner: refreshes fact docs, runs Plan phase, parses tasks.
func (p *ExplorerAgentPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	// Initialize workspace
	if err := p.workspace.Init(); err != nil {
		return nil, 0, fmt.Errorf("init workspace: %w", err)
	}

	// Refresh fact documents
	if err := p.workspace.RefreshFactDocuments(input); err != nil {
		return nil, 0, fmt.Errorf("refresh fact docs: %w", err)
	}

	// Build system prompt with max_tasks
	prompt := strings.ReplaceAll(planPhaseSystemPrompt, "{max_tasks}", fmt.Sprintf("%d", p.maxTasks))

	// Run Plan phase
	tokens, err := p.runPhase(ctx, "plan", prompt)
	if err != nil {
		return nil, tokens, fmt.Errorf("plan phase: %w", err)
	}

	// Parse plan.md
	tasks, err := p.workspace.ParsePlan()
	if err != nil {
		return nil, tokens, fmt.Errorf("parse plan: %w", err)
	}

	// Enforce max_tasks
	if len(tasks) > p.maxTasks {
		tasks = tasks[:p.maxTasks]
	}

	// Convert TaskSpec → PlanTask for compatibility with existing executor
	planTasks := make([]PlanTask, len(tasks))
	for i, ts := range tasks {
		planTasks[i] = taskSpecToPlanTask(ts, input.Hardware.Profile)
	}

	plan := &ExplorerPlan{
		ID:        generatePlanID(),
		Tier:      2,
		Tasks:     planTasks,
		Reasoning: "agent-planned",
	}
	return plan, tokens, nil
}

// Analyze runs Check phase (+ optional Act phase if verdict="continue").
// Returns the verdict, any additional tasks from Act phase, total tokens used.
func (p *ExplorerAgentPlanner) Analyze(ctx context.Context) (string, []TaskSpec, int, error) {
	// Run Check phase
	prompt := checkPhaseSystemPrompt
	tokens, err := p.runPhase(ctx, "check", prompt)
	if err != nil {
		return "", nil, tokens, fmt.Errorf("check phase: %w", err)
	}

	verdict := ""
	if p.tools != nil {
		verdict = p.tools.Verdict()
	}

	if verdict != "continue" {
		return verdict, nil, tokens, nil
	}

	// Run Act phase — agent revises plan.md with additional tasks
	actPrompt := strings.ReplaceAll(actPhaseSystemPrompt, "{max_tasks}", fmt.Sprintf("%d", p.maxTasks))
	actTokens, err := p.runPhase(ctx, "act", actPrompt)
	tokens += actTokens
	if err != nil {
		return verdict, nil, tokens, fmt.Errorf("act phase: %w", err)
	}

	// Parse revised plan.md for additional tasks
	extraTasks, err := p.workspace.ParsePlan()
	if err != nil {
		slog.Warn("explorer agent: parse revised plan failed", "error", err)
		return verdict, nil, tokens, nil
	}

	return verdict, extraTasks, tokens, nil
}

// taskSpecToPlanTask converts an LLM-authored TaskSpec to the existing PlanTask format.
func taskSpecToPlanTask(ts TaskSpec, defaultHardware string) PlanTask {
	params := make(map[string]any)
	for k, v := range ts.EngineParams {
		params[k] = v
	}
	return PlanTask{
		Kind:     ts.Kind,
		Hardware: defaultHardware,
		Model:    ts.Model,
		Engine:   ts.Engine,
		Params:   params,
		Reason:   ts.Reason,
	}
}

func generatePlanID() string {
	h := sha256Sum(fmt.Sprintf("%d", timeNow().UnixNano()))
	return h[:8]
}

// sha256Sum returns hex-encoded SHA-256 of input.
func sha256Sum(input string) string {
	h := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", h)
}

// timeNow is a var for testing.
var timeNow = time.Now
```

需要添加 imports: `"crypto/sha256"`, `"time"`

- [ ] **Step 4: 添加系统提示常量**

在 `explorer_agent_planner.go` 底部添加：

```go
const planPhaseSystemPrompt = `你是一个 AI 推理优化研究员，负责在边缘设备上探索最佳的模型+引擎配置。

你的工作环境是一个文档工作区（~/.aima/explorer/），包含：
- device-profile.md — 设备硬件、已安装模型和引擎的完整信息
- available-combos.md — 经过兼容性过滤的可行 model×engine 组合
- knowledge-base.md — 已有的知识库（历史记录、中央 advisory、引擎能力）
- summary.md — 你之前积累的发现和策略（可能为空）
- experiments/ — 历次实验的详细报告

你的目标：制定一个探索计划，选择最有价值的实验来扩展对这台设备的推理能力认知。

工作流程：
1. 用 cat 读取关键文档，了解设备状态和已有知识
2. 用 ls/grep 按需查看实验历史
3. 用 query 深入查询知识库细节（如需）
4. 思考策略，用 write 写入 plan.md
5. plan.md 的 ## Tasks 下必须包含一个 yaml code block，定义具体任务
6. 用 done() 通知系统你已完成规划

计划原则：
- 每轮最多 {max_tasks} 个任务
- 优先覆盖未测试的 model+engine 组合（breadth first）
- 已有 baseline 的模型才可以 tune
- 考虑引擎特性选择参数（如 MoE 模型用支持 cpu_gpu_hybrid 的引擎）
- 为每个任务设计合理的 benchmark 参数（concurrency、input_tokens、max_tokens）
- reason 字段要说明为什么这个实验有价值

Task YAML 格式：
- kind: validate|tune
  model: <model name>
  engine: <engine type>
  engine_params:
    <key>: <value>
  benchmark:
    concurrency: [<int>, ...]
    input_tokens: [<int>, ...]
    max_tokens: [<int>, ...]
    requests_per_combo: <int>
  reason: "<string>"`

const checkPhaseSystemPrompt = `你是一个 AI 推理实验分析师。刚刚完成了一轮探索实验，你需要分析结果并更新知识。

工作区状态：
- plan.md — 本轮执行的计划
- experiments/ — 新产生的实验报告（含 benchmark matrix 数据）
- summary.md — 之前的发现和策略
- knowledge-base.md — 已有知识库

你的任务：
1. 用 cat 读取新产生的实验报告
2. 分析结果：哪些成功了、性能如何、有没有意外发现
3. 对比已有知识（knowledge-base.md）：新结果是否优于已知最佳配置
4. 用 write/append 更新 summary.md：
   - ## Key Findings 追加新发现
   - ## Recommended Configurations 的 yaml block 更新推荐配置
   - ## Current Strategy 更新策略
5. 可选：用 append 为实验文件补充 ## Agent Notes
6. 用 done(verdict) 通知系统：
   - verdict="done" — 本轮目标达成，无需追加实验
   - verdict="continue" — 发现需要追加/重试的实验

Recommended Configurations YAML 格式：
- model: <name>
  engine: <engine>
  hardware: <profile>
  engine_params: { ... }
  performance:
    throughput_tps: <float>
    latency_p50_ms: <float>
  confidence: validated|tuned|provisional
  note: "<string>"`

const actPhaseSystemPrompt = `你是一个 AI 推理实验规划师。根据上一轮实验的分析结果，你决定追加实验。

工作区中 summary.md 已更新了最新分析。请：
1. 读取 summary.md 了解分析结论
2. 读取 available-combos.md 确认可行组合
3. 修订 plan.md，在 ## Tasks 的 yaml block 中只写追加的新任务
4. 用 done() 通知系统

修订原则：
- 只追加新任务，不重复已完成的实验
- 针对具体发现做针对性调整（如降低 gmu、换引擎、调 TP）
- 最多追加 {max_tasks} 个任务`
```

- [ ] **Step 5: 运行测试确认通过**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run 'TestAgentPlannerPlan|TestAgentPlannerAnalyze' -v`
Expected: PASS

- [ ] **Step 6: 运行全量测试确认无回归**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -v -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/agent/explorer_agent_planner.go internal/agent/explorer_agent_planner_test.go
git commit -m "feat(explorer): Plan() + Analyze() with system prompts and PDCA phases"
```

---

### Task 8: PDCA 循环集成到 Explorer

**Files:**
- Modify: `internal/agent/explorer.go:17-24` (ExplorerConfig)
- Modify: `internal/agent/explorer.go:200-206` (setupPlannerLocked)
- Modify: `internal/agent/explorer.go:320-520` (handleEvent)
- Modify: `internal/agent/explorer.go:620-720` (executePlan)

- [ ] **Step 1: ExplorerConfig 新增字段**

在 `explorer.go` 的 `ExplorerConfig` 结构体中添加：

```go
type ExplorerConfig struct {
	Schedule        ScheduleConfig
	Enabled         bool
	Mode            string        // "continuous" | "once" | "budget"
	MaxRounds       int           // budget mode: max plans to execute (0=unlimited)
	MaxPlanDuration time.Duration // per-plan time budget (default 30min)
	MaxTokensPerDay int           // daily LLM token cap (0=unlimited)
	MaxCycles       int           // PDCA max iterations per round (default 3)
	MaxTasks        int           // max tasks per plan (default 5)
	WorkspaceDir    string        // workspace root (default ~/.aima/explorer/)
}
```

- [ ] **Step 2: Explorer 新增 workspace 字段和 AnalyzablePlanner 接口**

在 `explorer_planner.go` 中添加（在 Planner 接口之后）：

```go
// AnalyzablePlanner extends Planner with result analysis capability (PDCA Check+Act).
type AnalyzablePlanner interface {
	Planner
	Analyze(ctx context.Context) (verdict string, extraTasks []TaskSpec, tokens int, err error)
}
```

在 `explorer.go` 的 `Explorer` struct 中添加 workspace 字段：

```go
type Explorer struct {
	// ... existing fields ...
	workspace *ExplorerWorkspace // PDCA document workspace
}
```

- [ ] **Step 3: 修改 setupPlannerLocked 使用新 AgentPlanner**

```go
func (e *Explorer) setupPlannerLocked() {
	if e.tier >= 2 && e.agent != nil {
		wsDir := e.config.WorkspaceDir
		if wsDir == "" {
			home, _ := os.UserHomeDir()
			wsDir = filepath.Join(home, ".aima", "explorer")
		}
		e.workspace = NewExplorerWorkspace(wsDir)
		e.planner = NewExplorerAgentPlanner(
			e.agent.llm,
			e.workspace,
			WithAgentMaxCycles(e.config.MaxCycles),
			WithAgentMaxTasks(e.config.MaxTasks),
		)
	} else {
		e.planner = &RulePlanner{}
	}
}
```

需要在 explorer.go 中添加 imports: `"os"`, `"path/filepath"`.

- [ ] **Step 4: 修改 handleEvent — 在执行后添加 PDCA Check+Act 循环**

在 `handleEvent` 中的 `e.executePlan(planCtx, plan)` 之后（约 line 499）、`planCancel()` 之前，添加 PDCA 循环：

```go
	e.executePlan(planCtx, plan)

	// PDCA Check+Act loop (only for AnalyzablePlanner, i.e., Tier 2 agent planner)
	maxCycles := e.config.MaxCycles
	if maxCycles <= 0 {
		maxCycles = 3
	}
	if ap, ok := planner.(AnalyzablePlanner); ok && !degraded {
		for cycle := 0; cycle < maxCycles; cycle++ {
			select {
			case <-planCtx.Done():
				slog.Info("explorer: PDCA timeout", "cycle", cycle)
				goto pdcaDone
			default:
			}

			slog.Info("explorer: PDCA Check phase", "cycle", cycle+1)
			verdict, extraTasks, analyzeTokens, err := ap.Analyze(planCtx)
			if analyzeTokens > 0 {
				e.mu.Lock()
				e.tokensUsedToday += analyzeTokens
				e.mu.Unlock()
			}
			if err != nil {
				slog.Warn("explorer: PDCA analyze failed", "error", err, "cycle", cycle+1)
				break
			}
			slog.Info("explorer: PDCA verdict", "verdict", verdict, "extra_tasks", len(extraTasks), "cycle", cycle+1)

			if verdict != "continue" || len(extraTasks) == 0 {
				break
			}

			// Convert TaskSpec → PlanTask and execute
			extraPlanTasks := make([]PlanTask, len(extraTasks))
			hw := input.Hardware
			for i, ts := range extraTasks {
				extraPlanTasks[i] = taskSpecToPlanTask(ts, firstTaskHardware(hw.Profile, hw.GPUArch))
			}
			extraPlan := &ExplorerPlan{
				ID:        plan.ID + fmt.Sprintf("-c%d", cycle+1),
				Tier:      2,
				Tasks:     extraPlanTasks,
				Reasoning: "PDCA Act cycle " + fmt.Sprintf("%d", cycle+1),
			}
			slog.Info("explorer: PDCA Do phase", "tasks", len(extraPlanTasks), "cycle", cycle+1)
			e.executePlan(planCtx, extraPlan)
		}
	}
pdcaDone:

	planCancel()
```

- [ ] **Step 5: 在 executePlan 中写实验结果到 workspace**

在 `executePlan` 方法中，每个 task 执行完毕后（`task.Status` 设置之后、harvest 之前），添加实验文档写入：

```go
		// Write experiment result to workspace (for PDCA Check phase)
		if e.workspace != nil {
			expResult := harvestResultToExperimentResult(result, taskStart)
			expTask := planTaskToTaskSpec(*task)
			if _, err := e.workspace.WriteExperimentResult(i+1, expTask, expResult); err != nil {
				slog.Debug("explorer: write experiment result failed", "error", err)
			}
		}
```

在 `explorer.go` 底部添加辅助函数：

```go
// harvestResultToExperimentResult converts HarvestResult to ExperimentResult for workspace.
func harvestResultToExperimentResult(hr HarvestResult, started time.Time) ExperimentResult {
	status := "completed"
	errMsg := ""
	if !hr.Success {
		status = "failed"
		errMsg = hr.Error
	}
	return ExperimentResult{
		Status:    status,
		StartedAt: started.UTC().Format(time.RFC3339),
		DurationS: time.Since(started).Seconds(),
		Error:     errMsg,
		Benchmarks: []BenchmarkEntry{{
			Concurrency:   hr.Concurrency,
			InputTokens:   hr.InputTokens,
			MaxTokens:     hr.MaxTokens,
			ThroughputTPS: hr.Throughput,
			LatencyP50Ms:  hr.TTFTP95 * 0.7, // approximation (P50 ≈ 0.7 × P95)
			LatencyP99Ms:  hr.TTFTP95 * 1.3,
		}},
	}
}

// planTaskToTaskSpec converts a PlanTask back to TaskSpec for experiment writing.
func planTaskToTaskSpec(t PlanTask) TaskSpec {
	return TaskSpec{
		Kind:         t.Kind,
		Model:        t.Model,
		Engine:       t.Engine,
		EngineParams: t.Params,
		Reason:       t.Reason,
	}
}
```

- [ ] **Step 6: 修改 Explorer 构造中的默认值**

在 `NewExplorer` 中（line ~170），设置默认值：

```go
func NewExplorer(config ExplorerConfig, agent *Agent, explMgr *ExplorationManager, db *state.DB, bus *EventBus, opts ...ExplorerOption) *Explorer {
	if config.MaxCycles <= 0 {
		config.MaxCycles = 3
	}
	if config.MaxTasks <= 0 {
		config.MaxTasks = 5
	}
	e := &Explorer{
		// ... existing ...
	}
	// ... rest unchanged ...
}
```

- [ ] **Step 7: 编译验证**

Run: `cd /Users/jguan/projects/AIMA && go build ./...`
Expected: 编译通过

- [ ] **Step 8: 运行全量测试**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -v -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/agent/explorer.go internal/agent/explorer_planner.go
git commit -m "feat(explorer): PDCA cycle integration — Check+Act loop after plan execution"
```

---

### Task 9: 引擎发现解耦

**Files:**
- Modify: `cmd/aima/main.go:594-660`

- [ ] **Step 1: 修改 WithGatherLocalEngines 回调**

将 `cmd/aima/main.go` 中 line 626-639 的 `GetEngineInfo` + `installedEnginesContainResolvedAsset` 检查替换为：

```go
		agent.WithGatherLocalEngines(func(ctx context.Context) ([]agent.LocalEngine, error) {
			if deps.ListEngines == nil {
				return nil, nil
			}
			data, err := deps.ListEngines(ctx)
			if err != nil {
				return nil, err
			}
			var engines []struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Runtime   string `json:"runtime"`
				Available bool   `json:"available"`
			}
			if err := json.Unmarshal(data, &engines); err != nil {
				return nil, nil
			}
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			result := make([]agent.LocalEngine, 0, len(engines))
			seen := make(map[string]bool)
			for _, e := range engines {
				engineType := e.Type
				if engineType == "" {
					engineType = e.Name
				}
				if !e.Available {
					continue
				}
				if seen[engineType] {
					continue
				}
				seen[engineType] = true

				le := agent.LocalEngine{
					Name:    e.Name,
					Type:    engineType,
					Runtime: e.Runtime,
				}
				// Optional catalog enrichment (step 2: metadata, not gate)
				if asset := cat.FindEngineByName(engineType, knowledge.HardwareInfo{
					GPUArch: hwInfo.GPUArch,
				}); asset != nil {
					le.Features = asset.Amplifier.Features
					le.Notes = asset.Amplifier.PerformanceGain
					le.TunableParams = asset.Startup.DefaultArgs
					le.InternalArgs = asset.Startup.InternalArgs
				}
				result = append(result, le)
			}
			return result, nil
		}),
```

核心变化：
- 移除 `deps.GetEngineInfo != nil` 分支及 `installedEnginesContainResolvedAsset` 检查
- 所有 `Available=true` 的引擎都列入，不管有没有 catalog match
- Catalog 只做增强（features/params），不做门控

- [ ] **Step 2: 编译验证**

Run: `cd /Users/jguan/projects/AIMA && go build ./cmd/aima/`
Expected: 编译通过

- [ ] **Step 3: Commit**

```bash
git add cmd/aima/main.go
git commit -m "fix(explorer): decouple engine discovery from catalog match — list all available engines"
```

---

### Task 10: 清理 + CLI wiring + 删除旧 LLMPlanner

**Files:**
- Delete: `internal/agent/explorer_llmplanner.go`
- Modify: `cmd/aima/main.go` (新增 MaxCycles/MaxTasks 配置)
- Modify: `internal/agent/explorer.go` (setupPlannerLocked 引用清理)

- [ ] **Step 1: 确认旧 LLMPlanner 不再被引用**

Run: `cd /Users/jguan/projects/AIMA && grep -r 'NewLLMPlanner\|LLMPlanner' --include='*.go' internal/ cmd/`

应该只在 `explorer_llmplanner.go` 自身和 `explorer.go:setupPlannerLocked` 中。`setupPlannerLocked` 已在 Task 8 中改为使用 `NewExplorerAgentPlanner`。

- [ ] **Step 2: 删除旧文件**

Run: `rm internal/agent/explorer_llmplanner.go`

- [ ] **Step 3: 在 cmd/aima/main.go 中 wire MaxCycles/MaxTasks**

找到 Explorer 构造部分（搜索 `ExplorerConfig{`），添加新字段：

```go
		explorerConfig := agent.ExplorerConfig{
			// ... existing fields ...
			MaxCycles: explorerMaxCycles, // new
			MaxTasks:  explorerMaxTasks,  // new
		}
```

在 CLI flags 中添加（搜索 `explorerMaxRounds` 或类似 flag 定义处）：

```go
	var explorerMaxCycles int
	var explorerMaxTasks int
	// 在 cobra.Command 的 flag 定义中：
	exploreCmd.Flags().IntVar(&explorerMaxCycles, "max-cycles", 3, "Max PDCA iterations per exploration round")
	exploreCmd.Flags().IntVar(&explorerMaxTasks, "max-tasks", 5, "Max tasks per exploration plan")
```

- [ ] **Step 4: 编译 + 全量测试**

Run: `cd /Users/jguan/projects/AIMA && go build ./... && go test ./internal/agent/ -v -count=1`
Expected: 编译通过，测试通过

- [ ] **Step 5: 运行 go vet**

Run: `cd /Users/jguan/projects/AIMA && go vet ./...`
Expected: 无 warning

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(explorer): complete Agent Planner — delete old LLMPlanner, wire CLI params"
```

---

### Task 11: 集成冒烟测试

**Files:**
- Modify: `internal/agent/explorer_agent_planner_test.go`

- [ ] **Step 1: 写完整 PDCA 流程的集成测试**

```go
func TestFullPDCACycle(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planYAML := `- kind: validate
  model: test-model
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "first test"`

	planContent := "# Exploration Plan\n\n## Strategy\nTest.\n\n## Tasks\n```yaml\n" + planYAML + "\n```\n"

	summaryContent := `# Exploration Summary

## Key Findings
- works

## Recommended Configurations
` + "```yaml\n" + `- model: test-model
  engine: vllm
  hardware: test-hw
  engine_params: {}
  performance:
    throughput_tps: 100.0
    latency_p50_ms: 40
  confidence: validated
  note: "ok"
` + "```\n" + `
## Current Strategy
Done.
`

	// Mock: Plan phase writes plan.md, Check phase writes summary.md and calls done(done)
	mock := &mockStreamingLLM{
		responses: []Response{
			// Plan phase
			{ToolCalls: []ToolCall{{ID: "1", Name: "cat", Arguments: `{"path":"device-profile.md"}`}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "write", Arguments: `{"path":"plan.md","content":` + jsonEscape(planContent) + `}`}}},
			{ToolCalls: []ToolCall{{ID: "3", Name: "done", Arguments: `{}`}}},
			// Check phase
			{ToolCalls: []ToolCall{{ID: "4", Name: "ls", Arguments: `{"path":"experiments"}`}}},
			{ToolCalls: []ToolCall{{ID: "5", Name: "write", Arguments: `{"path":"summary.md","content":` + jsonEscape(summaryContent) + `}`}}},
			{ToolCalls: []ToolCall{{ID: "6", Name: "done", Arguments: `{"verdict":"done"}`}}},
		},
	}

	input := PlanInput{
		Hardware:     HardwareInfo{Profile: "test-hw", GPUArch: "Ada", GPUCount: 2, VRAMMiB: 49140},
		LocalModels:  []LocalModel{{Name: "test-model", Format: "safetensors", Type: "llm", SizeBytes: 5e9}},
		LocalEngines: []LocalEngine{{Name: "vllm", Type: "vllm", Runtime: "container"}},
	}

	planner := NewExplorerAgentPlanner(mock, ws)

	// 1. Plan
	plan, _, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("plan tasks=%d", len(plan.Tasks))
	}

	// Simulate Do: write experiment result
	_, _ = ws.WriteExperimentResult(1, TaskSpec{
		Kind: "validate", Model: "test-model", Engine: "vllm",
	}, ExperimentResult{Status: "completed", Benchmarks: []BenchmarkEntry{
		{Concurrency: 1, InputTokens: 128, MaxTokens: 256, ThroughputTPS: 100},
	}})

	// 2. Check
	verdict, extra, _, err := planner.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if verdict != "done" {
		t.Errorf("verdict=%s", verdict)
	}
	if len(extra) != 0 {
		t.Errorf("extra tasks=%d", len(extra))
	}

	// 3. Verify summary.md has recommendations
	configs, _ := ws.ExtractRecommendations()
	if len(configs) != 1 || configs[0].Model != "test-model" {
		t.Errorf("recommendations: %+v", configs)
	}
}
```

- [ ] **Step 2: 运行测试**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestFullPDCACycle -v`
Expected: PASS

- [ ] **Step 3: 运行全量测试确认无回归**

Run: `cd /Users/jguan/projects/AIMA && go test ./... -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/explorer_agent_planner_test.go
git commit -m "test(explorer): full PDCA cycle integration test"
```
