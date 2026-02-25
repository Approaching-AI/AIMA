package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/jguan/aima/catalog"
	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/cli"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/model"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/runtime"
	"github.com/jguan/aima/internal/stack"
	"github.com/jguan/aima/internal/zeroclaw"

	state "github.com/jguan/aima/internal"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "aima: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// 1. Determine data directory
	dataDir := os.Getenv("AIMA_DATA_DIR")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home dir: %w", err)
		}
		dataDir = filepath.Join(home, ".aima")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// 2. Open state database
	db, err := state.Open(ctx, filepath.Join(dataDir, "aima.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// 3. Load knowledge catalog (YAML → in-memory structs)
	cat, err := knowledge.LoadCatalog(catalog.FS)
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}

	// 4. Load static knowledge into SQLite relational tables
	if err := knowledge.LoadToSQLite(ctx, db.RawDB(), cat); err != nil {
		return fmt.Errorf("load knowledge to sqlite: %w", err)
	}
	if err := db.Analyze(ctx); err != nil {
		slog.Warn("analyze failed", "error", err)
	}

	// 5. Create knowledge query store (backed by SQLite)
	knowledgeStore := knowledge.NewStore(db.RawDB())

	// 6. Create infrastructure components
	k3sClient := k3s.NewClient()
	proxyServer := proxy.NewServer(proxy.WithAddr(":8080"))
	zeroClawMgr := zeroclaw.NewManager(
		zeroclaw.WithDataDir(dataDir),
	)

	// 7. Select runtime: K3S if available (Linux), else native process
	rt := selectRuntime(ctx, k3sClient, dataDir)
	slog.Info("runtime selected", "runtime", rt.Name())

	// 8. Create MCP server with tool deps wired
	mcpServer := mcp.NewServer()
	deps := buildToolDeps(cat, db, knowledgeStore, rt, proxyServer, dataDir)
	mcp.RegisterAllTools(mcpServer, deps)

	// 9. Create agent (L3a Go Agent)
	// Agent needs an LLM client — nil means agent is not available until a model is deployed.
	toolAdapter := &mcpToolAdapter{server: mcpServer}
	goAgent := agent.NewAgent(nil, toolAdapter)
	dispatcher := agent.NewDispatcher(goAgent, zeroClawMgr)

	// 10. Build App and run CLI
	app := &cli.App{
		DB:         db,
		Catalog:    cat,
		Proxy:      proxyServer,
		MCP:        mcpServer,
		Dispatcher: dispatcher,
		ZeroClaw:   zeroClawMgr,
		DataDir:    dataDir,
		ToolDeps:   deps,
	}

	rootCmd := cli.NewRootCmd(app)
	return rootCmd.ExecuteContext(ctx)
}

// findModelAsset resolves a user-provided name to a catalog ModelAsset.
// Priority: exact catalog name → case-insensitive name → exact source repo → source repo prefix.
func findModelAsset(cat *knowledge.Catalog, name string) (*knowledge.ModelAsset, *knowledge.ModelSource) {
	// 1. Exact catalog name
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		if ma.Metadata.Name == name && len(ma.Storage.Sources) > 0 {
			return ma, &ma.Storage.Sources[0]
		}
	}
	// 2. Case-insensitive catalog name
	lower := strings.ToLower(name)
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		if strings.ToLower(ma.Metadata.Name) == lower && len(ma.Storage.Sources) > 0 {
			return ma, &ma.Storage.Sources[0]
		}
	}
	// 3. Exact source repo match
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		for j := range ma.Storage.Sources {
			src := &ma.Storage.Sources[j]
			if src.Repo == name {
				return ma, src
			}
		}
	}
	// 4. Source repo prefix match (e.g. "Qwen/Qwen3-8B-GGUF" matches repo "Qwen/Qwen3-8B")
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		for j := range ma.Storage.Sources {
			src := &ma.Storage.Sources[j]
			if src.Repo != "" && strings.HasPrefix(name, src.Repo) {
				return ma, src
			}
		}
	}
	return nil, nil
}

// catalogModelNames returns a comma-separated list of available model names.
func catalogModelNames(cat *knowledge.Catalog) string {
	names := make([]string, 0, len(cat.ModelAssets))
	for _, ma := range cat.ModelAssets {
		names = append(names, ma.Metadata.Name)
	}
	return strings.Join(names, ", ")
}

// mcpToolAdapter bridges mcp.Server to agent.ToolExecutor interface.
type mcpToolAdapter struct {
	server *mcp.Server
}

func (a *mcpToolAdapter) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*agent.ToolResult, error) {
	result, err := a.server.ExecuteTool(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	// Convert mcp.ToolResult to agent.ToolResult
	var text string
	for _, c := range result.Content {
		text += c.Text
	}
	return &agent.ToolResult{
		Content: text,
		IsError: result.IsError,
	}, nil
}

func (a *mcpToolAdapter) ListTools() []agent.ToolDefinition {
	mcpDefs := a.server.ListTools()
	defs := make([]agent.ToolDefinition, len(mcpDefs))
	for i, d := range mcpDefs {
		defs[i] = agent.ToolDefinition{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: d.InputSchema,
		}
	}
	return defs
}

// execRunner implements engine.CommandRunner using real exec.
type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// selectRuntime picks the best runtime: K3S on Linux if available, else native.
func selectRuntime(ctx context.Context, k3sClient *k3s.Client, dataDir string) runtime.Runtime {
	if goruntime.GOOS == "linux" && runtime.K3SAvailable(ctx, k3sClient) {
		return runtime.NewK3SRuntime(k3sClient)
	}
	return runtime.NewNativeRuntime(filepath.Join(dataDir, "logs"))
}

// buildHardwareInfo creates a HardwareInfo with platform and runtime awareness.
func buildHardwareInfo(ctx context.Context, rtName string) knowledge.HardwareInfo {
	hwInfo := knowledge.HardwareInfo{
		Platform:    goruntime.GOOS + "/" + goruntime.GOARCH,
		RuntimeType: rtName,
	}
	if hw, err := hal.Detect(ctx); err == nil {
		if hw.GPU != nil {
			hwInfo.GPUArch = hw.GPU.Arch
		}
		hwInfo.CPUArch = hw.CPU.Arch
	}
	return hwInfo
}

// buildToolDeps wires all ToolDeps fields to real implementations.
func buildToolDeps(cat *knowledge.Catalog, db *state.DB, kStore *knowledge.Store, rt runtime.Runtime, proxyServer *proxy.Server, dataDir string) *mcp.ToolDeps {
	return &mcp.ToolDeps{
		// Hardware
		DetectHardware: func(ctx context.Context) (json.RawMessage, error) {
			hw, err := hal.Detect(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(hw)
		},
		CollectMetrics: func(ctx context.Context) (json.RawMessage, error) {
			m, err := hal.CollectMetrics(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(m)
		},

		// Model management
		ScanModels: func(ctx context.Context) (json.RawMessage, error) {
			models, err := model.Scan(ctx, model.ScanOptions{})
			if err != nil {
				return nil, err
			}
			return json.Marshal(models)
		},
		ListModels: func(ctx context.Context) (json.RawMessage, error) {
			models, err := db.ListModels(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(models)
		},
		PullModel: func(ctx context.Context, name string) error {
			ma, src := findModelAsset(cat, name)
			if ma == nil {
				return fmt.Errorf("model %q not found in catalog\navailable: %s", name, catalogModelNames(cat))
			}
			destPath := filepath.Join(dataDir, "models", ma.Metadata.Name)
			return model.DownloadFromSource(ctx, model.Source{Type: src.Type, Repo: src.Repo}, destPath)
		},
		ImportModel: func(ctx context.Context, path string) (json.RawMessage, error) {
			destDir := filepath.Join(dataDir, "models")
			info, err := model.Import(ctx, path, destDir)
			if err != nil {
				return nil, err
			}
			return json.Marshal(info)
		},
		GetModelInfo: func(ctx context.Context, name string) (json.RawMessage, error) {
			m, err := db.GetModel(ctx, name)
			if err != nil {
				return nil, err
			}
			return json.Marshal(m)
		},

		// Engine management
		ScanEngines: func(ctx context.Context) (json.RawMessage, error) {
			engineAssets := make(map[string][]string)
			for _, ea := range cat.EngineAssets {
				engineAssets[ea.Metadata.Type] = append(engineAssets[ea.Metadata.Type], ea.Image.Name)
			}
			images, err := engine.Scan(ctx, engine.ScanOptions{EngineAssets: engineAssets})
			if err != nil {
				return nil, err
			}
			return json.Marshal(images)
		},
		ListEngines: func(ctx context.Context) (json.RawMessage, error) {
			engines, err := db.ListEngines(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(engines)
		},
		PullEngine: func(ctx context.Context, name string) error {
			for _, ea := range cat.EngineAssets {
				if ea.Metadata.Type == name || ea.Image.Name == name {
					return engine.Pull(ctx, engine.PullOptions{
						Image:      ea.Image.Name,
						Tag:        ea.Image.Tag,
						Registries: ea.Image.Registries,
					})
				}
			}
			return fmt.Errorf("engine %q not found in catalog", name)
		},
		ImportEngine: func(ctx context.Context, path string) error {
			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve path %s: %w", path, err)
			}
			return engine.Import(ctx, absPath, &execRunner{})
		},

		// Deployment (runtime abstraction: K3S or native)
		DeployApply: func(ctx context.Context, engineType, modelName, slot string) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, rt.Name())
			overrides := map[string]any{}
			if slot != "" {
				overrides["slot"] = slot
			}
			resolved, err := cat.Resolve(hwInfo, modelName, engineType, overrides)
			if err != nil {
				return nil, fmt.Errorf("resolve config: %w", err)
			}

			port := 8000
			if p, ok := resolved.Config["port"]; ok {
				switch v := p.(type) {
				case int:
					port = v
				case float64:
					port = int(v)
				}
			}

			modelPath := resolved.ModelPath
			if modelPath == "" {
				modelPath = filepath.Join(dataDir, "models", modelName)
			}

			req := &runtime.DeployRequest{
				Name:      modelName,
				Engine:    resolved.Engine,
				Image:     resolved.EngineImage,
				Command:   resolved.Command,
				ModelPath: modelPath,
				Port:      port,
				Config:    resolved.Config,
				Labels: map[string]string{
					"aima.dev/engine": resolved.Engine,
					"aima.dev/model":  modelName,
					"aima.dev/slot":   resolved.Slot,
				},
			}
			if resolved.Partition != nil {
				req.Partition = &runtime.PartitionRequest{
					GPUMemoryMiB:    resolved.Partition.GPUMemoryMiB,
					GPUCoresPercent: resolved.Partition.GPUCoresPercent,
					CPUCores:        resolved.Partition.CPUCores,
					RAMMiB:          resolved.Partition.RAMMiB,
				}
			}
			if resolved.HealthCheck != nil {
				req.HealthCheck = &runtime.HealthCheckConfig{
					Path:     resolved.HealthCheck.Path,
					TimeoutS: resolved.HealthCheck.TimeoutS,
				}
			}

			if err := rt.Deploy(ctx, req); err != nil {
				return nil, fmt.Errorf("deploy: %w", err)
			}
			proxyServer.RegisterBackend(modelName, &proxy.Backend{
				ModelName:  modelName,
				EngineType: resolved.Engine,
				Ready:      false,
			})
			result := map[string]any{
				"model": modelName, "engine": resolved.Engine,
				"slot": resolved.Slot, "status": "deploying",
				"runtime": rt.Name(),
			}
			return json.Marshal(result)
		},
		DeployDelete: func(ctx context.Context, name string) error {
			if err := rt.Delete(ctx, name); err != nil {
				return err
			}
			proxyServer.RemoveBackend(name)
			return nil
		},
		DeployStatus: func(ctx context.Context, name string) (json.RawMessage, error) {
			s, err := rt.Status(ctx, name)
			if err != nil {
				return nil, err
			}
			return json.Marshal(s)
		},
		DeployList: func(ctx context.Context) (json.RawMessage, error) {
			statuses, err := rt.List(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(statuses)
		},
		DeployLogs: func(ctx context.Context, name string, tailLines int) (string, error) {
			return rt.Logs(ctx, name, tailLines)
		},

		// Knowledge
		ResolveConfig: func(ctx context.Context, modelName, engineType string, overrides map[string]any) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, rt.Name())
			resolved, err := cat.Resolve(hwInfo, modelName, engineType, overrides)
			if err != nil {
				return nil, err
			}
			return json.Marshal(resolved)
		},
		SearchKnowledge: func(ctx context.Context, filter map[string]string) (json.RawMessage, error) {
			nf := state.NoteFilter{
				HardwareProfile: filter["hardware"],
				Model:           filter["model"],
				Engine:          filter["engine"],
			}
			notes, err := db.SearchNotes(ctx, nf)
			if err != nil {
				return nil, err
			}
			return json.Marshal(notes)
		},
		SaveKnowledge: func(ctx context.Context, note json.RawMessage) error {
			var n state.KnowledgeNote
			if err := json.Unmarshal(note, &n); err != nil {
				return fmt.Errorf("parse knowledge note: %w", err)
			}
			return db.InsertNote(ctx, &n)
		},
		GeneratePod: func(ctx context.Context, modelName, engineType, slot string) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, rt.Name())
			overrides := map[string]any{}
			if slot != "" {
				overrides["slot"] = slot
			}
			resolved, err := cat.Resolve(hwInfo, modelName, engineType, overrides)
			if err != nil {
				return nil, err
			}
			podYAML, err := knowledge.GeneratePod(resolved)
			if err != nil {
				return nil, err
			}
			return json.RawMessage(podYAML), nil
		},
		ListProfiles: func(ctx context.Context) (json.RawMessage, error) {
			profiles, err := kStore.ListHardwareProfiles(ctx)
			if err != nil {
				return json.Marshal(cat.HardwareProfiles) // fallback to in-memory
			}
			return json.Marshal(profiles)
		},
		ListEngineAssets: func(ctx context.Context) (json.RawMessage, error) {
			assets, err := kStore.ListEngineAssets(ctx)
			if err != nil {
				return json.Marshal(cat.EngineAssets) // fallback to in-memory
			}
			return json.Marshal(assets)
		},

		// Knowledge query (enhanced — SQLite relational queries)
		SearchConfigs: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p knowledge.SearchParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse search params: %w", err)
			}
			result, err := kStore.Search(ctx, p)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},
		CompareConfigs: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p knowledge.CompareParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse compare params: %w", err)
			}
			result, err := kStore.Compare(ctx, p)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},
		SimilarConfigs: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p knowledge.SimilarParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse similar params: %w", err)
			}
			result, err := kStore.Similar(ctx, p)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},
		LineageConfigs: func(ctx context.Context, configID string) (json.RawMessage, error) {
			result, err := kStore.Lineage(ctx, configID)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},
		GapsKnowledge: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p knowledge.GapsParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse gaps params: %w", err)
			}
			result, err := kStore.Gaps(ctx, p)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},
		AggregateKnowledge: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p knowledge.AggregateParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse aggregate params: %w", err)
			}
			result, err := kStore.Aggregate(ctx, p)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},

		// Stack management
		StackPreflight: func(ctx context.Context) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir)
			items := installer.Preflight(cat.StackComponents)
			return json.Marshal(items)
		},
		StackInit: func(ctx context.Context, allowDownload bool) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir)
			if allowDownload {
				missing := installer.Preflight(cat.StackComponents)
				if err := stack.DownloadItems(ctx, missing); err != nil {
					return nil, fmt.Errorf("download: %w", err)
				}
			}
			// Detect hardware to pick the right profile
			hwProfile := ""
			if hw, err := hal.Detect(ctx); err == nil {
				if hw.GPU != nil {
					hwProfile = hw.GPU.Arch + "-" + hw.CPU.Arch
				}
			}
			result, err := installer.Init(ctx, cat.StackComponents, hwProfile)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},
		StackStatus: func(ctx context.Context) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir)
			result, err := installer.Status(ctx, cat.StackComponents)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},

		// System
		ExecShell: func(ctx context.Context, command string) (json.RawMessage, error) {
			parts := strings.Fields(command)
			if len(parts) == 0 {
				return nil, fmt.Errorf("empty command")
			}
			out, err := exec.CommandContext(ctx, parts[0], parts[1:]...).CombinedOutput()
			if err != nil {
				return json.Marshal(map[string]string{
					"output": string(out),
					"error":  err.Error(),
				})
			}
			return json.Marshal(map[string]string{"output": string(out)})
		},
		GetConfig: func(ctx context.Context, key string) (string, error) {
			return db.GetConfig(ctx, key)
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			return db.SetConfig(ctx, key, value)
		},
	}
}

