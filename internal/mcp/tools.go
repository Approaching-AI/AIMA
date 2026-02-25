package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ToolDeps collects all dependencies that tool handlers need.
// Each field is a function provided by other packages at wiring time.
type ToolDeps struct {
	// Hardware (hal package)
	DetectHardware func(ctx context.Context) (json.RawMessage, error)
	CollectMetrics func(ctx context.Context) (json.RawMessage, error)

	// Model management
	ScanModels  func(ctx context.Context) (json.RawMessage, error)
	ListModels  func(ctx context.Context) (json.RawMessage, error)
	PullModel   func(ctx context.Context, name string) error
	ImportModel func(ctx context.Context, path string) (json.RawMessage, error)
	GetModelInfo func(ctx context.Context, name string) (json.RawMessage, error)

	// Engine management
	ScanEngines  func(ctx context.Context) (json.RawMessage, error)
	ListEngines  func(ctx context.Context) (json.RawMessage, error)
	PullEngine   func(ctx context.Context, name string) error
	ImportEngine func(ctx context.Context, path string) error

	// Deployment (k3s package)
	DeployApply  func(ctx context.Context, engine, model, slot string) (json.RawMessage, error)
	DeployDelete func(ctx context.Context, name string) error
	DeployStatus func(ctx context.Context, name string) (json.RawMessage, error)
	DeployList   func(ctx context.Context) (json.RawMessage, error)

	// Knowledge
	ResolveConfig    func(ctx context.Context, model, engine string, overrides map[string]any) (json.RawMessage, error)
	SearchKnowledge  func(ctx context.Context, filter map[string]string) (json.RawMessage, error)
	SaveKnowledge    func(ctx context.Context, note json.RawMessage) error
	GeneratePod      func(ctx context.Context, model, engine, slot string) (json.RawMessage, error)
	ListProfiles     func(ctx context.Context) (json.RawMessage, error)
	ListEngineAssets func(ctx context.Context) (json.RawMessage, error)

	// Knowledge query (enhanced — powered by SQLite relational queries)
	SearchConfigs     func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	CompareConfigs    func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	SimilarConfigs    func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	LineageConfigs    func(ctx context.Context, configID string) (json.RawMessage, error)
	GapsKnowledge     func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	AggregateKnowledge func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

	// Stack management
	StackPreflight func(ctx context.Context) (json.RawMessage, error)
	StackInit      func(ctx context.Context, allowDownload bool) (json.RawMessage, error)
	StackStatus    func(ctx context.Context) (json.RawMessage, error)

	// System
	ExecShell func(ctx context.Context, command string) (json.RawMessage, error)
	GetConfig func(ctx context.Context, key string) (string, error)
	SetConfig func(ctx context.Context, key, value string) error
}

// allowedCommands is the whitelist for shell.exec.
// Single-word entries match exact command name. Multi-word entries match exact prefix.
var allowedCommands = []string{
	"nvidia-smi",
	"df",
	"free",
	"uname",
	"cat /proc/cpuinfo",
}

// allowedKubectlSubcommands restricts kubectl to read-only operations.
var allowedKubectlSubcommands = map[string]bool{
	"get":      true,
	"describe": true,
	"logs":     true,
	"top":      true,
	"version":  true,
}

// isCommandAllowed checks if a command is in the whitelist.
func isCommandAllowed(command string) bool {
	cmd := strings.TrimSpace(command)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return false
	}

	// kubectl: require subcommand to be in the safe list
	if parts[0] == "kubectl" {
		return len(parts) >= 2 && allowedKubectlSubcommands[parts[1]]
	}

	// Other commands: exact or prefix match
	for _, allowed := range allowedCommands {
		if cmd == allowed || strings.HasPrefix(cmd, allowed+" ") {
			return true
		}
	}
	return false
}

// schema helpers for JSON Schema generation
func noParamsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func schema(properties string, required ...string) json.RawMessage {
	req := "[]"
	if len(required) > 0 {
		parts := make([]string, len(required))
		for i, r := range required {
			parts[i] = `"` + r + `"`
		}
		req = "[" + strings.Join(parts, ",") + "]"
	}
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s}`, properties, req))
}

// RegisterAllTools registers the complete set of MCP tools.
func RegisterAllTools(s *Server, deps *ToolDeps) {
	// hardware.detect
	s.RegisterTool(&Tool{
		Name:        "hardware.detect",
		Description: "Detect hardware capabilities (GPU, CPU, RAM)",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DetectHardware == nil {
				return ErrorResult("hardware.detect not implemented"), nil
			}
			data, err := deps.DetectHardware(ctx)
			if err != nil {
				return nil, fmt.Errorf("detect hardware: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// hardware.metrics
	s.RegisterTool(&Tool{
		Name:        "hardware.metrics",
		Description: "Collect current hardware metrics (GPU utilization, memory, temperature)",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CollectMetrics == nil {
				return ErrorResult("hardware.metrics not implemented"), nil
			}
			data, err := deps.CollectMetrics(ctx)
			if err != nil {
				return nil, fmt.Errorf("collect metrics: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// model.scan
	s.RegisterTool(&Tool{
		Name:        "model.scan",
		Description: "Scan local filesystem for available model files",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScanModels == nil {
				return ErrorResult("model.scan not implemented"), nil
			}
			data, err := deps.ScanModels(ctx)
			if err != nil {
				return nil, fmt.Errorf("scan models: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// model.list
	s.RegisterTool(&Tool{
		Name:        "model.list",
		Description: "List all known models from the knowledge base",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListModels == nil {
				return ErrorResult("model.list not implemented"), nil
			}
			data, err := deps.ListModels(ctx)
			if err != nil {
				return nil, fmt.Errorf("list models: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// model.pull
	s.RegisterTool(&Tool{
		Name:        "model.pull",
		Description: "Download a model by name",
		InputSchema: schema(`"name":{"type":"string","description":"Model name to pull"}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PullModel == nil {
				return ErrorResult("model.pull not implemented"), nil
			}
			var p struct{ Name string `json:"name"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if err := deps.PullModel(ctx, p.Name); err != nil {
				return nil, fmt.Errorf("pull model %s: %w", p.Name, err)
			}
			return TextResult(fmt.Sprintf("model %s pull started", p.Name)), nil
		},
	})

	// model.import
	s.RegisterTool(&Tool{
		Name:        "model.import",
		Description: "Import a model from a local file path",
		InputSchema: schema(`"path":{"type":"string","description":"Path to model file"}`, "path"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ImportModel == nil {
				return ErrorResult("model.import not implemented"), nil
			}
			var p struct{ Path string `json:"path"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Path == "" {
				return ErrorResult("path is required"), nil
			}
			data, err := deps.ImportModel(ctx, p.Path)
			if err != nil {
				return nil, fmt.Errorf("import model from %s: %w", p.Path, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// model.info
	s.RegisterTool(&Tool{
		Name:        "model.info",
		Description: "Get detailed information about a specific model",
		InputSchema: schema(`"name":{"type":"string","description":"Model name"}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.GetModelInfo == nil {
				return ErrorResult("model.info not implemented"), nil
			}
			var p struct{ Name string `json:"name"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			data, err := deps.GetModelInfo(ctx, p.Name)
			if err != nil {
				return nil, fmt.Errorf("get model info %s: %w", p.Name, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// engine.scan
	s.RegisterTool(&Tool{
		Name:        "engine.scan",
		Description: "Scan for locally available inference engine images",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScanEngines == nil {
				return ErrorResult("engine.scan not implemented"), nil
			}
			data, err := deps.ScanEngines(ctx)
			if err != nil {
				return nil, fmt.Errorf("scan engines: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// engine.list
	s.RegisterTool(&Tool{
		Name:        "engine.list",
		Description: "List all known engine assets from the knowledge base",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListEngines == nil {
				return ErrorResult("engine.list not implemented"), nil
			}
			data, err := deps.ListEngines(ctx)
			if err != nil {
				return nil, fmt.Errorf("list engines: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// engine.pull
	s.RegisterTool(&Tool{
		Name:        "engine.pull",
		Description: "Pull an inference engine image by name",
		InputSchema: schema(`"name":{"type":"string","description":"Engine image name to pull"}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PullEngine == nil {
				return ErrorResult("engine.pull not implemented"), nil
			}
			var p struct{ Name string `json:"name"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if err := deps.PullEngine(ctx, p.Name); err != nil {
				return nil, fmt.Errorf("pull engine %s: %w", p.Name, err)
			}
			return TextResult(fmt.Sprintf("engine %s pull started", p.Name)), nil
		},
	})

	// engine.import
	s.RegisterTool(&Tool{
		Name:        "engine.import",
		Description: "Import an engine image from a local OCI tar file",
		InputSchema: schema(`"path":{"type":"string","description":"Path to the OCI tar file"}`, "path"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ImportEngine == nil {
				return ErrorResult("engine.import not implemented"), nil
			}
			var p struct{ Path string `json:"path"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Path == "" {
				return ErrorResult("path is required"), nil
			}
			if err := deps.ImportEngine(ctx, p.Path); err != nil {
				return nil, fmt.Errorf("import engine from %s: %w", p.Path, err)
			}
			return TextResult(fmt.Sprintf("engine image imported from %s", p.Path)), nil
		},
	})

	// deploy.apply
	s.RegisterTool(&Tool{
		Name:        "deploy.apply",
		Description: "Deploy an inference service with the given model and engine",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model to deploy"},`+
				`"engine":{"type":"string","description":"Engine to use (optional)"},`+
				`"slot":{"type":"string","description":"Partition slot (optional)"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployApply == nil {
				return ErrorResult("deploy.apply not implemented"), nil
			}
			var p struct {
				Model  string `json:"model"`
				Engine string `json:"engine"`
				Slot   string `json:"slot"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			data, err := deps.DeployApply(ctx, p.Engine, p.Model, p.Slot)
			if err != nil {
				return nil, fmt.Errorf("deploy apply %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.delete
	s.RegisterTool(&Tool{
		Name:        "deploy.delete",
		Description: "Delete a deployed inference service",
		InputSchema: schema(`"name":{"type":"string","description":"Deployment name to delete"}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployDelete == nil {
				return ErrorResult("deploy.delete not implemented"), nil
			}
			var p struct{ Name string `json:"name"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if err := deps.DeployDelete(ctx, p.Name); err != nil {
				return nil, fmt.Errorf("deploy delete %s: %w", p.Name, err)
			}
			return TextResult(fmt.Sprintf("deployment %s deleted", p.Name)), nil
		},
	})

	// deploy.status
	s.RegisterTool(&Tool{
		Name:        "deploy.status",
		Description: "Get the status of a deployed inference service",
		InputSchema: schema(`"name":{"type":"string","description":"Deployment name"}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployStatus == nil {
				return ErrorResult("deploy.status not implemented"), nil
			}
			var p struct{ Name string `json:"name"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			data, err := deps.DeployStatus(ctx, p.Name)
			if err != nil {
				return nil, fmt.Errorf("deploy status %s: %w", p.Name, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.list
	s.RegisterTool(&Tool{
		Name:        "deploy.list",
		Description: "List all deployed inference services",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployList == nil {
				return ErrorResult("deploy.list not implemented"), nil
			}
			data, err := deps.DeployList(ctx)
			if err != nil {
				return nil, fmt.Errorf("deploy list: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.resolve
	s.RegisterTool(&Tool{
		Name:        "knowledge.resolve",
		Description: "Resolve optimal configuration for a model/engine combination",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name"},`+
				`"engine":{"type":"string","description":"Engine name (optional)"},`+
				`"overrides":{"type":"object","description":"Configuration overrides (optional)"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ResolveConfig == nil {
				return ErrorResult("knowledge.resolve not implemented"), nil
			}
			var p struct {
				Model     string         `json:"model"`
				Engine    string         `json:"engine"`
				Overrides map[string]any `json:"overrides"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			data, err := deps.ResolveConfig(ctx, p.Model, p.Engine, p.Overrides)
			if err != nil {
				return nil, fmt.Errorf("resolve config for %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.search
	s.RegisterTool(&Tool{
		Name:        "knowledge.search",
		Description: "Search knowledge notes by filter criteria",
		InputSchema: schema(
			`"hardware":{"type":"string","description":"Hardware filter (optional)"},` +
				`"model":{"type":"string","description":"Model filter (optional)"},` +
				`"engine":{"type":"string","description":"Engine filter (optional)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SearchKnowledge == nil {
				return ErrorResult("knowledge.search not implemented"), nil
			}
			var p struct {
				Hardware string `json:"hardware"`
				Model    string `json:"model"`
				Engine   string `json:"engine"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			filter := make(map[string]string)
			if p.Hardware != "" {
				filter["hardware"] = p.Hardware
			}
			if p.Model != "" {
				filter["model"] = p.Model
			}
			if p.Engine != "" {
				filter["engine"] = p.Engine
			}
			data, err := deps.SearchKnowledge(ctx, filter)
			if err != nil {
				return nil, fmt.Errorf("search knowledge: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.save
	s.RegisterTool(&Tool{
		Name:        "knowledge.save",
		Description: "Save a knowledge note",
		InputSchema: schema(`"note":{"type":"object","description":"Knowledge note to save"}`, "note"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SaveKnowledge == nil {
				return ErrorResult("knowledge.save not implemented"), nil
			}
			var p struct{ Note json.RawMessage `json:"note"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if len(p.Note) == 0 {
				return ErrorResult("note is required"), nil
			}
			if err := deps.SaveKnowledge(ctx, p.Note); err != nil {
				return nil, fmt.Errorf("save knowledge: %w", err)
			}
			return TextResult("knowledge note saved"), nil
		},
	})

	// knowledge.generate_pod
	s.RegisterTool(&Tool{
		Name:        "knowledge.generate_pod",
		Description: "Generate K3S Pod YAML for a model/engine deployment",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name"},`+
				`"engine":{"type":"string","description":"Engine name"},`+
				`"slot":{"type":"string","description":"Partition slot (optional)"}`,
			"model", "engine"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.GeneratePod == nil {
				return ErrorResult("knowledge.generate_pod not implemented"), nil
			}
			var p struct {
				Model  string `json:"model"`
				Engine string `json:"engine"`
				Slot   string `json:"slot"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" || p.Engine == "" {
				return ErrorResult("model and engine are required"), nil
			}
			data, err := deps.GeneratePod(ctx, p.Model, p.Engine, p.Slot)
			if err != nil {
				return nil, fmt.Errorf("generate pod for %s/%s: %w", p.Model, p.Engine, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.list_profiles
	s.RegisterTool(&Tool{
		Name:        "knowledge.list_profiles",
		Description: "List all hardware profiles from the knowledge base",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListProfiles == nil {
				return ErrorResult("knowledge.list_profiles not implemented"), nil
			}
			data, err := deps.ListProfiles(ctx)
			if err != nil {
				return nil, fmt.Errorf("list profiles: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.list_engines
	s.RegisterTool(&Tool{
		Name:        "knowledge.list_engines",
		Description: "List all engine assets from the knowledge base",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListEngineAssets == nil {
				return ErrorResult("knowledge.list_engines not implemented"), nil
			}
			data, err := deps.ListEngineAssets(ctx)
			if err != nil {
				return nil, fmt.Errorf("list engine assets: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// stack.preflight
	s.RegisterTool(&Tool{
		Name:        "stack.preflight",
		Description: "Check which stack components need files downloaded before init",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.StackPreflight == nil {
				return ErrorResult("stack.preflight not implemented"), nil
			}
			data, err := deps.StackPreflight(ctx)
			if err != nil {
				return nil, fmt.Errorf("stack preflight: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// stack.init
	s.RegisterTool(&Tool{
		Name:        "stack.init",
		Description: "Install and configure infrastructure stack (K3S, HAMi). Set allow_download=true to auto-download missing files.",
		InputSchema: schema(`"allow_download":{"type":"boolean","description":"Auto-download missing component files (default false)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.StackInit == nil {
				return ErrorResult("stack.init not implemented"), nil
			}
			var p struct {
				AllowDownload bool `json:"allow_download"`
			}
			if len(params) > 0 {
				json.Unmarshal(params, &p)
			}
			data, err := deps.StackInit(ctx, p.AllowDownload)
			if err != nil {
				return nil, fmt.Errorf("stack init: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// stack.status
	s.RegisterTool(&Tool{
		Name:        "stack.status",
		Description: "Check installation status of infrastructure stack components",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.StackStatus == nil {
				return ErrorResult("stack.status not implemented"), nil
			}
			data, err := deps.StackStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("stack status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// shell.exec
	s.RegisterTool(&Tool{
		Name:        "shell.exec",
		Description: "Execute a whitelisted shell command",
		InputSchema: schema(`"command":{"type":"string","description":"Shell command to execute (must be whitelisted)"}`, "command"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ExecShell == nil {
				return ErrorResult("shell.exec not implemented"), nil
			}
			var p struct{ Command string `json:"command"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Command == "" {
				return ErrorResult("command is required"), nil
			}
			if !isCommandAllowed(p.Command) {
				return ErrorResult(fmt.Sprintf("command not allowed: %s (allowed: %v)", p.Command, allowedCommands)), nil
			}
			data, err := deps.ExecShell(ctx, p.Command)
			if err != nil {
				return nil, fmt.Errorf("exec shell %q: %w", p.Command, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.search_configs (enhanced — multi-dimensional search with SQL preprocessing)
	s.RegisterTool(&Tool{
		Name:        "knowledge.search_configs",
		Description: "Search configurations and performance data with multi-dimensional filtering, sorting, and aggregation. Returns pre-processed results optimized for Agent reasoning.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"hardware":{"type":"string","description":"Hardware profile ID or GPU architecture"},"model":{"type":"string","description":"Model ID or model family"},"engine":{"type":"string","description":"Engine type"},"engine_features":{"type":"array","items":{"type":"string"},"description":"Required engine features"},"constraints":{"type":"object","properties":{"ttft_ms_p95_max":{"type":"number"},"throughput_tps_min":{"type":"number"},"vram_mib_max":{"type":"integer"},"power_watts_max":{"type":"number"}}},"concurrency":{"type":"integer"},"status":{"type":"string","enum":["experiment","candidate","production"]},"sort_by":{"type":"string","enum":["throughput","latency","vram","power","created"]},"sort_order":{"type":"string","enum":["asc","desc"]},"limit":{"type":"integer"}}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SearchConfigs == nil {
				return ErrorResult("knowledge.search_configs not implemented"), nil
			}
			data, err := deps.SearchConfigs(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("search configs: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.compare
	s.RegisterTool(&Tool{
		Name:        "knowledge.compare",
		Description: "Compare multiple configurations side-by-side on performance metrics. Returns a pre-formatted comparison table.",
		InputSchema: schema(
			`"config_ids":{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":10,"description":"Configuration IDs to compare"},`+
				`"metrics":{"type":"array","items":{"type":"string"},"description":"Metrics to compare (default: throughput, latency, vram)"},`+
				`"concurrency":{"type":"integer","description":"Fixed concurrency for comparison"}`,
			"config_ids"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CompareConfigs == nil {
				return ErrorResult("knowledge.compare not implemented"), nil
			}
			data, err := deps.CompareConfigs(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("compare configs: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.similar
	s.RegisterTool(&Tool{
		Name:        "knowledge.similar",
		Description: "Find configurations with similar performance profiles using vector distance. Useful for cross-hardware migration recommendations.",
		InputSchema: schema(
			`"config_id":{"type":"string","description":"Reference configuration ID"},`+
				`"weights":{"type":"object","description":"Custom metric weights (throughput, latency, vram, power, qps)"},`+
				`"filter_hardware":{"type":"string","description":"Limit search to specific hardware"},`+
				`"exclude_same_config":{"type":"boolean","default":true},`+
				`"limit":{"type":"integer","default":5}`,
			"config_id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SimilarConfigs == nil {
				return ErrorResult("knowledge.similar not implemented"), nil
			}
			data, err := deps.SimilarConfigs(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("similar configs: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.lineage
	s.RegisterTool(&Tool{
		Name:        "knowledge.lineage",
		Description: "Trace the evolution chain of a configuration — find all ancestors and descendants with their performance progression.",
		InputSchema: schema(`"config_id":{"type":"string","description":"Configuration ID to trace"}`, "config_id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.LineageConfigs == nil {
				return ErrorResult("knowledge.lineage not implemented"), nil
			}
			var p struct{ ConfigID string `json:"config_id"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ConfigID == "" {
				return ErrorResult("config_id is required"), nil
			}
			data, err := deps.LineageConfigs(ctx, p.ConfigID)
			if err != nil {
				return nil, fmt.Errorf("lineage %s: %w", p.ConfigID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.gaps
	s.RegisterTool(&Tool{
		Name:        "knowledge.gaps",
		Description: "Discover knowledge gaps — Hardware×Engine×Model combinations that lack sufficient benchmark data. Helps Agent plan exploration.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"hardware":{"type":"string","description":"Limit to specific hardware"},"min_benchmarks":{"type":"integer","default":3,"description":"Threshold below which a combination is considered a gap"}}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.GapsKnowledge == nil {
				return ErrorResult("knowledge.gaps not implemented"), nil
			}
			data, err := deps.GapsKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("knowledge gaps: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.aggregate
	s.RegisterTool(&Tool{
		Name:        "knowledge.aggregate",
		Description: "Aggregate performance statistics grouped by engine, hardware, or model. Returns averages, min/max, and counts.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"hardware":{"type":"string","description":"Filter by hardware"},"model":{"type":"string","description":"Filter by model"},"group_by":{"type":"string","enum":["engine","hardware","model"],"default":"engine","description":"Dimension to group by"}}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AggregateKnowledge == nil {
				return ErrorResult("knowledge.aggregate not implemented"), nil
			}
			data, err := deps.AggregateKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("knowledge aggregate: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// system.config
	s.RegisterTool(&Tool{
		Name:        "system.config",
		Description: "Get or set a system configuration value",
		InputSchema: schema(
			`"key":{"type":"string","description":"Configuration key"},`+
				`"value":{"type":"string","description":"Value to set (omit to get current value)"}`,
			"key"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Key   string  `json:"key"`
				Value *string `json:"value"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Key == "" {
				return ErrorResult("key is required"), nil
			}

			if p.Value != nil {
				// Set
				if deps.SetConfig == nil {
					return ErrorResult("system.config set not implemented"), nil
				}
				if err := deps.SetConfig(ctx, p.Key, *p.Value); err != nil {
					return nil, fmt.Errorf("set config %s: %w", p.Key, err)
				}
				return TextResult(fmt.Sprintf("config %s set to %s", p.Key, *p.Value)), nil
			}

			// Get
			if deps.GetConfig == nil {
				return ErrorResult("system.config get not implemented"), nil
			}
			val, err := deps.GetConfig(ctx, p.Key)
			if err != nil {
				return nil, fmt.Errorf("get config %s: %w", p.Key, err)
			}
			return TextResult(val), nil
		},
	})
}
