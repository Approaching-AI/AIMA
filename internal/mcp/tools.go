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
	RemoveModel func(ctx context.Context, name string, deleteFiles bool) error

	// Engine management
	ScanEngines    func(ctx context.Context, runtime string) (json.RawMessage, error) // runtime: "auto" | "container" | "native"
	ListEngines    func(ctx context.Context) (json.RawMessage, error)
	GetEngineInfo  func(ctx context.Context, name string) (json.RawMessage, error)
	PullEngine     func(ctx context.Context, name string) error
	ImportEngine   func(ctx context.Context, path string) error
	RemoveEngine   func(ctx context.Context, name string) error

	// Deployment (runtime package)
	DeployApply  func(ctx context.Context, engine, model, slot string, configOverrides map[string]any) (json.RawMessage, error)
	DeployDryRun func(ctx context.Context, engine, model, slot string, configOverrides map[string]any) (json.RawMessage, error)
	DeployDelete func(ctx context.Context, name string) error
	DeployStatus func(ctx context.Context, name string) (json.RawMessage, error)
	DeployList   func(ctx context.Context) (json.RawMessage, error)
	DeployLogs   func(ctx context.Context, name string, tailLines int) (string, error)

	// Knowledge
	ResolveConfig    func(ctx context.Context, model, engine string, overrides map[string]any) (json.RawMessage, error)
	SearchKnowledge  func(ctx context.Context, filter map[string]string) (json.RawMessage, error)
	SaveKnowledge    func(ctx context.Context, note json.RawMessage) error
	GeneratePod      func(ctx context.Context, model, engine, slot string) (json.RawMessage, error)
	ListProfiles     func(ctx context.Context) (json.RawMessage, error)
	ListEngineAssets func(ctx context.Context) (json.RawMessage, error)
	ListModelAssets  func(ctx context.Context) (json.RawMessage, error)

	// Benchmark
	RecordBenchmark func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	PromoteConfig   func(ctx context.Context, configID, status string) (json.RawMessage, error)

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

	// Discovery
	DiscoverLAN func(ctx context.Context, timeoutS int) (json.RawMessage, error)

	// Catalog overlay
	CatalogOverride func(ctx context.Context, kind, name, content string) (json.RawMessage, error)
	CatalogStatus   func(ctx context.Context) (json.RawMessage, error)

	// Deploy approval
	DeployApprove func(ctx context.Context, id int64) (json.RawMessage, error)

	// Agent
	DispatchAsk     func(ctx context.Context, query string, forceLocal, forceDeep, skipPerms bool, sessionID string) (json.RawMessage, string, error)
	AgentInstall    func(ctx context.Context) (json.RawMessage, error)
	AgentStatus     func(ctx context.Context) (json.RawMessage, error)
	AgentGuide      func(ctx context.Context) (json.RawMessage, error)
	RollbackList    func(ctx context.Context) (json.RawMessage, error)
	RollbackRestore func(ctx context.Context, id int64) (json.RawMessage, error)

	// System
	SystemStatus func(ctx context.Context) (json.RawMessage, error)
	ExecShell    func(ctx context.Context, command string) (json.RawMessage, error)
	GetConfig    func(ctx context.Context, key string) (string, error)
	SetConfig    func(ctx context.Context, key, value string) error

	// Knowledge (summary)
	ListKnowledgeSummary func(ctx context.Context) (json.RawMessage, error)

	// Fleet management
	FleetListDevices func(ctx context.Context) (json.RawMessage, error)
	FleetDeviceInfo  func(ctx context.Context, deviceID string) (json.RawMessage, error)
	FleetDeviceTools func(ctx context.Context, deviceID string) (json.RawMessage, error)
	FleetExecTool    func(ctx context.Context, deviceID, toolName string, params json.RawMessage) (json.RawMessage, error)
}

// validConfigKeys is the whitelist for system.config get/set.
var validConfigKeys = map[string]bool{
	"api_key":          true,
	"llm.endpoint":     true,
	"llm.model":        true,
	"llm.api_key":      true,
	"llm.user_agent":   true,
	"llm.extra_params": true,
}

// IsValidConfigKey reports whether key is a recognized configuration key.
func IsValidConfigKey(key string) bool {
	return validConfigKeys[key]
}

// isCommandAllowed checks if a command is in the whitelist.
func isCommandAllowed(command string) bool {
	// allowedCommands is the whitelist for shell.exec.
	// Single-word entries match exact command name. Multi-word entries match exact prefix.
	allowedCommands := []string{
		"nvidia-smi",
		"df",
		"free",
		"uname",
		"cat /proc/cpuinfo",
	}

	// allowedKubectlSubcommands restricts kubectl to read-only operations.
	allowedKubectlSubcommands := map[string]bool{
		"get":      true,
		"describe": true,
		"logs":     true,
		"top":      true,
		"version":  true,
	}

	cmd := strings.TrimSpace(command)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return false
	}

	// kubectl: require subcommand to be in the safe list
	if parts[0] == "kubectl" {
		return len(parts) >= 2 && allowedKubectlSubcommands[parts[1]]
	}

	// Other commands: exact match or match with additional arguments.
	// For multi-word entries (e.g. "cat /proc/cpuinfo"), ALL tokens must match —
	// additional arguments after the pattern are rejected to prevent file read escalation.
	for _, allowed := range allowedCommands {
		if cmd == allowed {
			return true
		}
		allowedParts := strings.Fields(allowed)
		if len(allowedParts) == 1 && strings.HasPrefix(cmd, allowed+" ") {
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
		Description: "Detect this device's hardware capabilities: GPU model, VRAM, compute SDK, CPU cores, total RAM, and NPU if present. Returns a structured hardware profile. Use when you need to understand what this device can run before deploying a model. Do not use for real-time GPU utilization or temperature (use hardware.metrics) or for a combined system overview (use system.status).",
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
		Description: "Collect real-time hardware metrics: GPU utilization percentage, GPU memory used/total, temperature, and power draw. Use when monitoring a running deployment's resource usage or diagnosing performance issues. Do not use for static hardware capability detection (use hardware.detect).",
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
		Description: "Scan the local filesystem for model files (GGUF, SafeTensors) and register newly discovered ones in the database. Use when models were manually downloaded or copied to disk and need to be registered. Do not use for listing already-registered models (use model.list) or browsing the YAML catalog of all supported models (use knowledge.list_models).",
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
		Description: "List models registered in the local database (previously found by model.scan or model.import). Returns names, file paths, sizes, and statuses. Use when checking what models are locally available for deployment. Do not use for browsing YAML catalog definitions of supported models (use knowledge.list_models) or discovering new files on disk (use model.scan).",
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
		Description: "Download a model by name from a remote source and register it in the database. Use when the user wants a model that is not yet on disk. Call model.list first to check if it is already available locally.",
		InputSchema: schema(`"name":{"type":"string","description":"Model name to download, e.g. 'qwen3-0.6b', 'qwen3.5-35b-a3b'. Must match a name in the knowledge base (call knowledge.list_models to see available names)."}`, "name"),
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
		Description: "Import a model from a local file path and register it in the database. Use when a model file exists on disk but is not yet registered. Do not use for downloading from remote sources (use model.pull).",
		InputSchema: schema(`"path":{"type":"string","description":"Absolute path to model file on disk, e.g. '/data/models/qwen3-0.6b.gguf'"}`, "path"),
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
		Description: "Get detailed information about a specific model: file path, size, format, quantization, and knowledge base metadata. Use when you need specifics about one model before deployment or troubleshooting. Call model.list first if you do not know the exact name.",
		InputSchema: schema(`"name":{"type":"string","description":"Model name as registered in the database, e.g. 'qwen3-0.6b'. Call model.list to see available names."}`, "name"),
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

	// model.remove
	s.RegisterTool(&Tool{
		Name:        "model.remove",
		Description: "Remove a model record from the database. Optionally deletes model files from disk. This is a destructive operation (a rollback snapshot is created automatically). Blocked for agent-initiated calls.",
		InputSchema: schema(`"name":{"type":"string","description":"Model name to remove, e.g. 'qwen3-0.6b'. Call model.list to see registered models."},"delete_files":{"type":"boolean","description":"If true, also delete model files from disk. If false (default), only removes the database record."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RemoveModel == nil {
				return ErrorResult("model.remove not implemented"), nil
			}
			var p struct {
				Name        string `json:"name"`
				DeleteFiles bool   `json:"delete_files"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if err := deps.RemoveModel(ctx, p.Name, p.DeleteFiles); err != nil {
				return nil, fmt.Errorf("remove model %s: %w", p.Name, err)
			}
			if p.DeleteFiles {
				return TextResult(fmt.Sprintf("model %s removed (files deleted)", p.Name)), nil
			}
			return TextResult(fmt.Sprintf("model %s removed (database only)", p.Name)), nil
		},
	})

	// engine.scan
	s.RegisterTool(&Tool{
		Name:        "engine.scan",
		Description: "Scan this device for locally available inference engines (container images and native binaries) and register newly found ones in the database. Use after pulling or importing an engine to ensure it is detected. Do not use for listing already-registered engines (use engine.list) or browsing YAML catalog engine definitions (use knowledge.list_engines).",
		InputSchema: schema(`"runtime":{"type":"string","enum":["auto","container","native"],"description":"Runtime filter: 'auto' scans both container and native (default), 'container' scans only K3S/Docker images, 'native' scans only local binaries"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScanEngines == nil {
				return ErrorResult("engine.scan not implemented"), nil
			}
			var p struct {
				Runtime string `json:"runtime"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult(fmt.Sprintf("invalid params: %v", err)), nil
				}
			}
			if p.Runtime == "" {
				p.Runtime = "auto"
			}
			data, err := deps.ScanEngines(ctx, p.Runtime)
			if err != nil {
				return nil, fmt.Errorf("scan engines: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// engine.info
	s.RegisterTool(&Tool{
		Name:        "engine.info",
		Description: "Get full information about a specific engine: live availability from database plus knowledge base details (hardware requirements, startup config, supported features, constraints). Use when you need specifics about an engine before deployment. Call engine.list first if you do not know the exact name.",
		InputSchema: schema(`"name":{"type":"string","description":"Engine type (e.g. 'llamacpp', 'vllm', 'sglang'), image name, or engine ID. Call engine.list to see available names."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.GetEngineInfo == nil {
				return ErrorResult("engine.info not implemented"), nil
			}
			var p struct{ Name string `json:"name"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			data, err := deps.GetEngineInfo(ctx, p.Name)
			if err != nil {
				return nil, fmt.Errorf("engine info %s: %w", p.Name, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// engine.list
	s.RegisterTool(&Tool{
		Name:        "engine.list",
		Description: "List inference engines registered in the local database (previously found by engine.scan or engine.import). Returns engine names, types, runtime (container/native), and statuses. Use when checking what engines are locally available. Do not use for browsing YAML catalog definitions of all supported engines (use knowledge.list_engines) or detecting new engines on disk (use engine.scan).",
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
		Description: "Download an inference engine image or binary from its configured source. Downloads a container image or native binary depending on this device's platform. If name is omitted, pulls the catalog's default engine for this hardware. Run engine.scan after pulling to register it.",
		InputSchema: schema(`"name":{"type":"string","description":"Engine type to pull, e.g. 'llamacpp', 'vllm', 'sglang'. Omit to pull the default engine for this hardware."}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PullEngine == nil {
				return ErrorResult("engine.pull not implemented"), nil
			}
			var p struct{ Name string `json:"name"` }
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult(fmt.Sprintf("invalid params: %v", err)), nil
				}
			}
			name := p.Name
			// Empty name is handled by the PullEngine implementation (uses catalog.DefaultEngine)
			if err := deps.PullEngine(ctx, name); err != nil {
				return nil, fmt.Errorf("pull engine %s: %w", name, err)
			}
			return TextResult(fmt.Sprintf("engine %s pulled successfully", name)), nil
		},
	})

	// engine.import
	s.RegisterTool(&Tool{
		Name:        "engine.import",
		Description: "Import an engine container image from a local OCI tar file and register it. Use when an engine image was transferred offline (airgap). Do not use for downloading from a registry (use engine.pull).",
		InputSchema: schema(`"path":{"type":"string","description":"Absolute path to the OCI tar file, e.g. '/data/images/vllm-cuda.tar'"}`, "path"),
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

	// engine.remove
	s.RegisterTool(&Tool{
		Name:        "engine.remove",
		Description: "Remove an engine record from the local database. This is a destructive operation (a rollback snapshot is created automatically). Blocked for agent-initiated calls.",
		InputSchema: schema(`"name":{"type":"string","description":"Engine name or ID to remove. Call engine.list to see registered engines."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RemoveEngine == nil {
				return ErrorResult("engine.remove not implemented"), nil
			}
			var p struct{ Name string `json:"name"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if err := deps.RemoveEngine(ctx, p.Name); err != nil {
				return nil, fmt.Errorf("remove engine %s: %w", p.Name, err)
			}
			return TextResult(fmt.Sprintf("engine %s removed", p.Name)), nil
		},
	})

	// deploy.apply
	s.RegisterTool(&Tool{
		Name:        "deploy.apply",
		Description: "Deploy a model as an inference service. Auto-detects hardware, resolves the optimal engine and config from the knowledge base, and creates a K3S Pod or native process. Returns NEEDS_APPROVAL with a deployment plan — present it to the user, then call deploy.approve with the approval ID to execute. If engine is omitted, the best engine is auto-selected. Do not use for previewing without executing (use deploy.dry_run) or checking existing deployment status (use deploy.status).",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model to deploy, e.g. 'qwen3-0.6b'. Call model.list to verify it is available locally."},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm', 'llamacpp'. Omit to auto-select the best engine for this hardware."},`+
				`"slot":{"type":"string","description":"Partition slot for multi-model deployment, e.g. 'slot-0'. Omit for default full-device allocation."},`+
				`"config":{"type":"object","description":"Engine config overrides, e.g. {\"gpu_memory_utilization\": 0.9, \"max_model_len\": 131072, \"tensor_parallel_size\": 2}"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployApply == nil {
				return ErrorResult("deploy.apply not implemented"), nil
			}
			var p struct {
				Model  string         `json:"model"`
				Engine string         `json:"engine"`
				Slot   string         `json:"slot"`
				Config map[string]any `json:"config"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			data, err := deps.DeployApply(ctx, p.Engine, p.Model, p.Slot, p.Config)
			if err != nil {
				return nil, fmt.Errorf("deploy apply %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.dry_run
	s.RegisterTool(&Tool{
		Name:        "deploy.dry_run",
		Description: "Preview a deployment without executing it. Returns the resolved config, hardware fitness report, generated Pod YAML, and any warnings. Use before deploy.apply to verify the configuration is correct. No side effects — nothing is deployed.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model to deploy, e.g. 'qwen3-0.6b'"},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm', 'llamacpp'. Omit to auto-select."},`+
				`"slot":{"type":"string","description":"Partition slot for multi-model, e.g. 'slot-0'. Omit for default."},`+
				`"config":{"type":"object","description":"Engine config overrides, e.g. {\"gpu_memory_utilization\": 0.9}"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployDryRun == nil {
				return ErrorResult("deploy.dry_run not implemented"), nil
			}
			var p struct {
				Model  string         `json:"model"`
				Engine string         `json:"engine"`
				Slot   string         `json:"slot"`
				Config map[string]any `json:"config"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			data, err := deps.DeployDryRun(ctx, p.Engine, p.Model, p.Slot, p.Config)
			if err != nil {
				return nil, fmt.Errorf("deploy dry run %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.approve
	s.RegisterTool(&Tool{
		Name:        "deploy.approve",
		Description: "Approve and execute a pending deployment. Call this only after presenting the NEEDS_APPROVAL plan from deploy.apply to the user and receiving their confirmation. Do not call without user approval.",
		InputSchema: schema(`"id":{"type":"integer","description":"Approval ID from the deploy.apply NEEDS_APPROVAL response, e.g. 1"}`, "id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployApprove == nil {
				return ErrorResult("deploy.approve not implemented"), nil
			}
			var p struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			data, err := deps.DeployApprove(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("deploy approve %d: %w", p.ID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.delete
	s.RegisterTool(&Tool{
		Name:        "deploy.delete",
		Description: "Delete a running deployment (stops the inference service and removes the K3S Pod or native process). This is a destructive operation (a rollback snapshot is created automatically). Blocked for agent-initiated calls.",
		InputSchema: schema(`"name":{"type":"string","description":"Deployment name to delete, e.g. 'aima-vllm-qwen3-0-6b'. Call deploy.list to see active deployments."}`, "name"),
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
		Description: "Check the health of a specific deployment: phase (Running/Pending/Failed), ready state, restart count, and exit code. Accepts either the deployment name or model name. Use after deploy.apply to verify the service started correctly, or when diagnosing issues.",
		InputSchema: schema(`"name":{"type":"string","description":"Deployment name (e.g. 'aima-vllm-qwen3-0-6b') or model name (e.g. 'qwen3-0.6b'). Call deploy.list if unsure."}`, "name"),
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
		Description: "List all active deployments on this device with their names, models, engines, and statuses. Use as the first step when checking what is currently running, or to get deployment names for deploy.status or deploy.logs.",
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

	// deploy.logs
	s.RegisterTool(&Tool{
		Name:        "deploy.logs",
		Description: "Get recent log output from a deployment. Use when diagnosing startup failures, crashes, or runtime errors. Accepts deployment name or model name.",
		InputSchema: schema(
			`"name":{"type":"string","description":"Deployment name (e.g. 'aima-vllm-qwen3-0-6b') or model name. Call deploy.list if unsure."},`+
				`"tail":{"type":"integer","description":"Number of log lines to return, e.g. 50. Default: 100."}`,
			"name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployLogs == nil {
				return ErrorResult("deploy.logs not implemented"), nil
			}
			var p struct {
				Name string `json:"name"`
				Tail int    `json:"tail"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if p.Tail <= 0 {
				p.Tail = 100
			}
			logs, err := deps.DeployLogs(ctx, p.Name, p.Tail)
			if err != nil {
				return nil, fmt.Errorf("deploy logs %s: %w", p.Name, err)
			}
			return TextResult(logs), nil
		},
	})

	// knowledge.resolve
	s.RegisterTool(&Tool{
		Name:        "knowledge.resolve",
		Description: "Find the optimal engine and configuration for deploying a model on this hardware. Merges YAML defaults (L0), golden configs from the database (L2), and user overrides (L1) into a final resolved config. Use before deploy.apply to understand what configuration will be used, or when the user asks 'what engine should I use for model X'. If engine is omitted, the best available engine is auto-selected.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name to resolve, e.g. 'qwen3-0.6b'. Call model.list or knowledge.list_models to see available names."},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm', 'llamacpp'. Omit to auto-select the best engine."},`+
				`"overrides":{"type":"object","description":"Config overrides to apply on top of resolved defaults, e.g. {\"gpu_memory_utilization\": 0.85}"}`,
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
		Description: "Search knowledge notes (agent exploration records containing trial results and recommendations) by hardware, model, or engine filter. Returns matching notes with titles, tags, and content summaries. Use when looking for past exploration or experiment notes. Do not use for querying tested configurations with performance metrics (use knowledge.search_configs) or browsing YAML catalog assets (use knowledge.list).",
		InputSchema: schema(
			`"hardware":{"type":"string","description":"Filter by hardware profile, e.g. 'nvidia-rtx4060'"},` +
				`"model":{"type":"string","description":"Filter by model name, e.g. 'qwen3-0.6b'"},` +
				`"engine":{"type":"string","description":"Filter by engine type, e.g. 'vllm'"}`),
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
		Description: "Save a knowledge note recording exploration results, experiment findings, or recommendations. Use after completing a benchmark or deployment experiment to preserve the findings for future reference.",
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
		Description: "Generate K3S Pod YAML manifest for a model/engine deployment without applying it. Use for inspecting or customizing the Pod YAML. For normal deployments, use deploy.apply which generates and applies the Pod automatically.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name, e.g. 'qwen3-0.6b'"},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm'"},`+
				`"slot":{"type":"string","description":"Partition slot, e.g. 'slot-0'. Omit for default."}`,
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
		Description: "List all hardware profiles defined in the YAML knowledge base. Each profile describes a GPU/CPU/RAM capability vector with container access settings and resource names. Use when exploring what hardware types AIMA knows about. For this device's actual hardware, use hardware.detect instead.",
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
		Description: "List all engine assets defined in the YAML knowledge base (catalog). Shows every engine type AIMA supports with their hardware requirements, image sources, and features. Use when browsing what engines are available in the catalog. Do not use for listing engines that are actually installed locally (use engine.list).",
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

	// knowledge.list_models
	s.RegisterTool(&Tool{
		Name:        "knowledge.list_models",
		Description: "List all model assets defined in the YAML knowledge base (catalog). Shows every model AIMA supports with their variants, download sources, and compatible engines. Use when browsing what models are available for download or to check model compatibility. Do not use for listing models already installed locally (use model.list).",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListModelAssets == nil {
				return ErrorResult("knowledge.list_models not implemented"), nil
			}
			data, err := deps.ListModelAssets(ctx)
			if err != nil {
				return nil, fmt.Errorf("list model assets: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// stack.preflight
	s.RegisterTool(&Tool{
		Name:        "stack.preflight",
		Description: "Check which infrastructure stack components (K3S, HAMi) need files downloaded before installation. Returns a checklist of components and their download status. Use before stack.init to see what is missing.",
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
		Description: "Install and configure the infrastructure stack (K3S, HAMi) on this device. Run stack.preflight first to check prerequisites. This is a significant operation that modifies the system. Blocked for agent-initiated calls.",
		InputSchema: schema(`"allow_download":{"type":"boolean","description":"Auto-download missing component files (default false)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.StackInit == nil {
				return ErrorResult("stack.init not implemented"), nil
			}
			var p struct {
				AllowDownload bool `json:"allow_download"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult(fmt.Sprintf("invalid params: %v", err)), nil
				}
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
		Description: "Check the installation status of infrastructure stack components (K3S, HAMi). Shows whether each component is installed, its version, and health. Use to verify the infrastructure is ready for container-based deployments.",
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
		Description: "Execute a whitelisted shell command on this device. Only the following commands are allowed: nvidia-smi, df, free, uname, cat /proc/cpuinfo, and read-only kubectl subcommands (get, describe, logs, top, version). Use when you need raw system information not available through other tools.",
		InputSchema: schema(`"command":{"type":"string","description":"Shell command to run. Must be one of: 'nvidia-smi', 'df -h', 'free -h', 'uname -a', 'cat /proc/cpuinfo', 'kubectl get pods', etc."}`, "command"),
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
				return ErrorResult(fmt.Sprintf("command not allowed: %s (allowed: nvidia-smi, df, free, uname, cat /proc/cpuinfo, kubectl get/describe/logs/top/version)", p.Command)), nil
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
		Description: "Search tested Configuration records (Hardware x Engine x Model combos) in the database with multi-dimensional filtering and sorting. Returns configurations with benchmark results and performance metrics. Use when comparing proven setups or finding the best config for specific hardware. Do not use for searching YAML catalog assets (use knowledge.list) or agent exploration notes (use knowledge.search).",
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
		Description: "Compare multiple Configuration records side-by-side on performance metrics (throughput, latency, VRAM). Returns a formatted comparison table. Use when the user wants to compare specific tested configurations. Requires config_ids from knowledge.search_configs.",
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
		Description: "Find Configuration records with similar performance profiles using vector distance in 6-dimensional performance space. Useful for cross-hardware migration — find equivalent setups on different hardware. Requires a config_id from knowledge.search_configs.",
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
		Description: "Trace the evolution chain of a Configuration — find all ancestor and descendant configs with their performance progression over time. Use when investigating how a configuration was derived or how performance changed across iterations.",
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
		Description: "Identify untested Hardware x Engine x Model combinations that lack benchmark data. Use when planning what to benchmark next or assessing coverage completeness.",
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
		Description: "Aggregate benchmark statistics grouped by engine, hardware, or model. Returns averages, min/max, and sample counts for throughput, latency, and VRAM. Use for high-level performance summaries and trend analysis.",
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

	// knowledge.promote
	s.RegisterTool(&Tool{
		Name:        "knowledge.promote",
		Description: "Change a Configuration's status: 'experiment' (default for new), 'golden' (battle-tested, auto-injected as L2 defaults on future deployments), or 'archived' (deprecated). Use after confirming a configuration performs well to promote it to golden status.",
		InputSchema: schema(
			`"config_id":{"type":"string","description":"Configuration ID to promote"},`+
				`"status":{"type":"string","enum":["golden","experiment","archived"],"description":"Target status"}`,
			"config_id", "status"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PromoteConfig == nil {
				return ErrorResult("knowledge.promote not implemented"), nil
			}
			var p struct {
				ConfigID string `json:"config_id"`
				Status   string `json:"status"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ConfigID == "" || p.Status == "" {
				return ErrorResult("config_id and status are required"), nil
			}
			data, err := deps.PromoteConfig(ctx, p.ConfigID, p.Status)
			if err != nil {
				return nil, fmt.Errorf("promote config: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// benchmark.record
	s.RegisterTool(&Tool{
		Name:        "benchmark.record",
		Description: "Record a benchmark result with performance metrics. Auto-creates a Configuration record (Hardware x Engine x Model) if one does not exist. Use after running inference tests to store throughput, latency, and resource usage data.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"hardware":{"type":"string","description":"Hardware profile ID (e.g. nvidia-gb10-arm64)"},
			"engine":{"type":"string","description":"Engine type (e.g. vllm-nightly)"},
			"model":{"type":"string","description":"Model name (e.g. qwen3.5-35b-a3b)"},
			"device_id":{"type":"string","description":"Device ID from fleet (e.g. gb10)"},
			"config":{"type":"object","description":"Engine config used (gpu_memory_utilization, max_model_len, etc.)"},
			"concurrency":{"type":"integer","description":"Number of concurrent requests","default":1},
			"input_len_bucket":{"type":"string","description":"Input length category (e.g. short, medium, long)"},
			"output_len_bucket":{"type":"string","description":"Output length category"},
			"ttft_ms_p50":{"type":"number","description":"Time-to-first-token p50 in ms"},
			"ttft_ms_p95":{"type":"number","description":"Time-to-first-token p95 in ms"},
			"tpot_ms_p50":{"type":"number","description":"Time-per-output-token p50 in ms"},
			"tpot_ms_p95":{"type":"number","description":"Time-per-output-token p95 in ms"},
			"throughput_tps":{"type":"number","description":"Tokens per second (single request)"},
			"qps":{"type":"number","description":"Queries per second"},
			"vram_usage_mib":{"type":"integer","description":"VRAM usage in MiB"},
			"sample_count":{"type":"integer","description":"Number of samples in benchmark"},
			"stability":{"type":"string","description":"Stability assessment (stable, fluctuating, unstable)"},
			"notes":{"type":"string","description":"Free-form notes about the benchmark"}
		},"required":["hardware","engine","model","throughput_tps"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RecordBenchmark == nil {
				return ErrorResult("benchmark.record not implemented"), nil
			}
			data, err := deps.RecordBenchmark(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("record benchmark: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// discover.lan
	s.RegisterTool(&Tool{
		Name:        "discover.lan",
		Description: "Low-level mDNS scan for AIMA instances on the local network. Returns raw service entries. For most use cases, prefer fleet.list_devices which performs mDNS discovery automatically and returns structured device information.",
		InputSchema: schema(`"timeout_s":{"type":"integer","description":"Scan timeout in seconds (default 3)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DiscoverLAN == nil {
				return ErrorResult("discover.lan not implemented"), nil
			}
			var p struct {
				TimeoutS int `json:"timeout_s"`
			}
			if len(params) > 0 {
				json.Unmarshal(params, &p)
			}
			if p.TimeoutS <= 0 {
				p.TimeoutS = 3
			}
			data, err := deps.DiscoverLAN(ctx, p.TimeoutS)
			if err != nil {
				return nil, fmt.Errorf("discover lan: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.override
	s.RegisterTool(&Tool{
		Name:        "catalog.override",
		Description: "Write a YAML asset to the runtime overlay catalog (~/.aima/catalog/). Overrides the factory-embedded asset with the same metadata.name, or adds a new one. Takes effect on next restart. Use for customizing engine or model definitions without recompiling. Agent calls are restricted to engine_asset and model_asset kinds only.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["engine_asset","model_asset","hardware_profile","partition_strategy","stack_component"],"description":"Asset kind"},"name":{"type":"string","description":"metadata.name of the asset"},"content":{"type":"string","description":"Full YAML content of the asset"}},"required":["kind","name","content"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogOverride == nil {
				return ErrorResult("catalog.override not implemented"), nil
			}
			var p struct {
				Kind    string `json:"kind"`
				Name    string `json:"name"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Kind == "" || p.Name == "" || p.Content == "" {
				return ErrorResult("kind, name, and content are required"), nil
			}
			data, err := deps.CatalogOverride(ctx, p.Kind, p.Name, p.Content)
			if err != nil {
				return nil, fmt.Errorf("catalog override: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.status
	s.RegisterTool(&Tool{
		Name:        "catalog.status",
		Description: "Show catalog asset counts: factory (compiled-in) vs overlay (runtime) for each asset type (hardware profiles, engines, models, partitions, stack). Use to check if any runtime overrides are active.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogStatus == nil {
				return ErrorResult("catalog.status not implemented"), nil
			}
			data, err := deps.CatalogStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("catalog status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// system.status
	s.RegisterTool(&Tool{
		Name:        "system.status",
		Description: "Get a combined system overview in one call: hardware summary, all active deployments, and current GPU metrics. Use as a quick first step to understand the overall state of this device. For detailed hardware info use hardware.detect; for detailed deployment info use deploy.list + deploy.status.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SystemStatus == nil {
				return ErrorResult("system.status not implemented"), nil
			}
			data, err := deps.SystemStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("system status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.list
	s.RegisterTool(&Tool{
		Name:        "knowledge.list",
		Description: "List a summary of all YAML knowledge base assets: counts and names of hardware profiles, engine assets, model assets, and partition strategies. Use as a quick overview of the catalog contents. For detailed listing of a specific asset type, use knowledge.list_profiles, knowledge.list_engines, or knowledge.list_models.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListKnowledgeSummary == nil {
				return ErrorResult("knowledge.list not implemented"), nil
			}
			data, err := deps.ListKnowledgeSummary(ctx)
			if err != nil {
				return nil, fmt.Errorf("knowledge list: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.ask
	s.RegisterTool(&Tool{
		Name:        "agent.ask",
		Description: "Route a natural language query through the agent dispatcher. Auto-selects L3a (Go Agent) or L3b (ZeroClaw) based on query complexity. Returns the agent's response and a session_id for multi-turn conversations. Blocked for agent-initiated calls (prevents recursive invocation).",
		InputSchema: schema(
			`"query":{"type":"string","description":"The question to ask"},`+
				`"force_local":{"type":"boolean","description":"Force use of Go Agent (L3a)"},`+
				`"force_deep":{"type":"boolean","description":"Force use of ZeroClaw (L3b)"},`+
				`"dangerously_skip_permissions":{"type":"boolean","description":"Skip deploy approval gate (use with caution)"},`+
				`"session_id":{"type":"string","description":"Session ID to continue a conversation (works with both L3a and L3b)"}`,
			"query"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DispatchAsk == nil {
				return ErrorResult("agent.ask not implemented"), nil
			}
			var p struct {
				Query     string `json:"query"`
				ForceLocal bool   `json:"force_local"`
				ForceDeep  bool   `json:"force_deep"`
				SkipPerms  bool   `json:"dangerously_skip_permissions"`
				SessionID  string `json:"session_id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Query == "" {
				return ErrorResult("query is required"), nil
			}
			data, sid, err := deps.DispatchAsk(ctx, p.Query, p.ForceLocal, p.ForceDeep, p.SkipPerms, p.SessionID)
			if err != nil {
				return nil, fmt.Errorf("agent ask: %w", err)
			}
			// Merge session_id into the response
			var resp map[string]any
			if err := json.Unmarshal(data, &resp); err != nil {
				resp = map[string]any{"result": string(data)}
			}
			if sid != "" {
				resp["session_id"] = sid
			}
			merged, _ := json.Marshal(resp)
			return TextResult(string(merged)), nil
		},
	})

	// agent.install
	s.RegisterTool(&Tool{
		Name:        "agent.install",
		Description: "Download and install the ZeroClaw sidecar binary (L3b agent with persistent memory and deep reasoning). Blocked for agent-initiated calls.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AgentInstall == nil {
				return ErrorResult("agent.install not implemented"), nil
			}
			data, err := deps.AgentInstall(ctx)
			if err != nil {
				return nil, fmt.Errorf("install agent: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.status
	s.RegisterTool(&Tool{
		Name:        "agent.status",
		Description: "Check agent subsystem availability: whether L3a (Go Agent) and L3b (ZeroClaw) are configured and healthy. Use to diagnose agent-related issues.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AgentStatus == nil {
				return ErrorResult("agent.status not implemented"), nil
			}
			data, err := deps.AgentStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("agent status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.guide
	s.RegisterTool(&Tool{
		Name:        "agent.guide",
		Description: "Return the full AIMA Agent Usage Guide with detailed tool parameters, workflow examples, and API reference. Only call this when the system prompt does not provide enough information and you need the complete reference.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AgentGuide == nil {
				return ErrorResult("agent.guide not implemented"), nil
			}
			data, err := deps.AgentGuide(ctx)
			if err != nil {
				return nil, fmt.Errorf("agent guide: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.rollback_list
	s.RegisterTool(&Tool{
		Name:        "agent.rollback_list",
		Description: "List available rollback snapshots. Snapshots are auto-created before destructive operations (model.remove, engine.remove, deploy.delete). Use to see what can be restored with agent.rollback.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RollbackList == nil {
				return ErrorResult("agent.rollback_list not implemented"), nil
			}
			data, err := deps.RollbackList(ctx)
			if err != nil {
				return nil, fmt.Errorf("list rollback snapshots: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.rollback
	s.RegisterTool(&Tool{
		Name:        "agent.rollback",
		Description: "Restore a resource from a rollback snapshot. For models/engines, restores the database record. For deployments, redeploys with the original config. Call agent.rollback_list first to get the snapshot ID. Blocked for agent-initiated calls.",
		InputSchema: schema(`"id":{"type":"integer","description":"Snapshot ID from agent.rollback_list"}`, "id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RollbackRestore == nil {
				return ErrorResult("agent.rollback not implemented"), nil
			}
			var p struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ID <= 0 {
				return ErrorResult("id is required (positive integer)"), nil
			}
			data, err := deps.RollbackRestore(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("rollback snapshot %d: %w", p.ID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// system.config
	s.RegisterTool(&Tool{
		Name:        "system.config",
		Description: "Get or set a persistent system configuration value. Supported keys: api_key (auth token), llm.endpoint (Agent LLM URL), llm.model (Agent LLM model name), llm.api_key (Agent LLM auth), llm.extra_params (JSON object merged into every LLM request, e.g. temperature/top_p). Values for api_key and llm.api_key are masked in responses. Setting api_key hot-reloads auth; setting llm.* hot-swaps the Agent LLM client. Omit value to read, provide value to write.",
		InputSchema: schema(
			`"key":{"type":"string","description":"Configuration key: 'api_key', 'llm.endpoint', 'llm.model', 'llm.api_key', 'llm.user_agent', or 'llm.extra_params'"},`+
				`"value":{"type":"string","description":"Value to set. Omit this field to read the current value."}`,
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
			if !IsValidConfigKey(p.Key) {
				return ErrorResult(fmt.Sprintf("unknown config key %q; supported keys: api_key, llm.endpoint, llm.model, llm.api_key, llm.user_agent, llm.extra_params", p.Key)), nil
			}

			if p.Value != nil {
				// Set
				if deps.SetConfig == nil {
					return ErrorResult("system.config set not implemented"), nil
				}
				if err := deps.SetConfig(ctx, p.Key, *p.Value); err != nil {
					return nil, fmt.Errorf("set config %s: %w", p.Key, err)
				}
				display := *p.Value
				if p.Key == "api_key" || p.Key == "llm.api_key" {
					display = "***"
				}
				return TextResult(fmt.Sprintf("config %s set to %s", p.Key, display)), nil
			}

			// Get
			if deps.GetConfig == nil {
				return ErrorResult("system.config get not implemented"), nil
			}
			val, err := deps.GetConfig(ctx, p.Key)
			if err != nil {
				return nil, fmt.Errorf("get config %s: %w", p.Key, err)
			}
			if p.Key == "api_key" || p.Key == "llm.api_key" {
				val = "***"
			}
			return TextResult(val), nil
		},
	})

	// fleet.list_devices
	s.RegisterTool(&Tool{
		Name:        "fleet.list_devices",
		Description: "List all AIMA devices discovered on the LAN via mDNS. Performs a fresh scan each time. Returns device IDs, hostnames, addresses, and ports. Use as the first step when managing remote devices. Prefer this over discover.lan for structured results.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetListDevices == nil {
				return ErrorResult("fleet.list_devices not implemented"), nil
			}
			data, err := deps.FleetListDevices(ctx)
			if err != nil {
				return nil, fmt.Errorf("fleet list devices: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// fleet.device_info
	s.RegisterTool(&Tool{
		Name:        "fleet.device_info",
		Description: "Get detailed information about a specific remote device: hardware capabilities, installed models, running deployments. Use after fleet.list_devices to drill into a specific device.",
		InputSchema: schema(`"device_id":{"type":"string","description":"Device ID from fleet.list_devices, e.g. 'gb10', 'mac-m4'"}`, "device_id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetDeviceInfo == nil {
				return ErrorResult("fleet.device_info not implemented"), nil
			}
			var p struct{ DeviceID string `json:"device_id"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.DeviceID == "" {
				return ErrorResult("device_id is required"), nil
			}
			data, err := deps.FleetDeviceInfo(ctx, p.DeviceID)
			if err != nil {
				return nil, fmt.Errorf("fleet device info %s: %w", p.DeviceID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// fleet.device_tools
	s.RegisterTool(&Tool{
		Name:        "fleet.device_tools",
		Description: "List the MCP tools available on a specific remote device. Use to check what operations a remote device supports before calling fleet.exec_tool.",
		InputSchema: schema(`"device_id":{"type":"string","description":"Device ID from fleet.list_devices, e.g. 'gb10'"}`, "device_id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetDeviceTools == nil {
				return ErrorResult("fleet.device_tools not implemented"), nil
			}
			var p struct{ DeviceID string `json:"device_id"` }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.DeviceID == "" {
				return ErrorResult("device_id is required"), nil
			}
			data, err := deps.FleetDeviceTools(ctx, p.DeviceID)
			if err != nil {
				return nil, fmt.Errorf("fleet device tools %s: %w", p.DeviceID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// fleet.exec_tool
	s.RegisterTool(&Tool{
		Name:        "fleet.exec_tool",
		Description: "Execute any MCP tool on a remote fleet device. Sends the tool call over HTTP and returns the remote result. Use after fleet.list_devices to identify the device and fleet.device_tools to verify the tool is available. Do not use for local tools — call them directly instead. Agent guardrails apply to the inner tool_name — blocked and confirmable tools are enforced consistently with local calls.",
		InputSchema: schema(
			`"device_id":{"type":"string","description":"Device ID from fleet.list_devices, e.g. 'gb10', 'linux-1'. Call fleet.list_devices first if unsure."},`+
				`"tool_name":{"type":"string","description":"MCP tool name to execute remotely, e.g. 'hardware.detect', 'model.list', 'deploy.status'. Call fleet.device_tools first to see available tools."},`+
				`"params":{"type":"object","description":"Tool parameters as a JSON object. Omit or pass {} if the tool takes no parameters. Example: {\"name\": \"aima-vllm-qwen3-0-6b\"} for deploy.status."}`,
			"device_id", "tool_name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetExecTool == nil {
				return ErrorResult("fleet.exec_tool not implemented"), nil
			}
			var p struct {
				DeviceID string          `json:"device_id"`
				ToolName string          `json:"tool_name"`
				Params   json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.DeviceID == "" || p.ToolName == "" {
				return ErrorResult("device_id and tool_name are required"), nil
			}
			data, err := deps.FleetExecTool(ctx, p.DeviceID, p.ToolName, p.Params)
			if err != nil {
				return nil, fmt.Errorf("fleet exec %s on %s: %w", p.ToolName, p.DeviceID, err)
			}
			return TextResult(string(data)), nil
		},
	})
}
