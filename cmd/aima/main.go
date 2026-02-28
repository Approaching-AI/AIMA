package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

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
	k3sClient := newK3SClient(dataDir)
	proxyServer := proxy.NewServer()
	zeroClawMgr := zeroclaw.NewManager(
		zeroclaw.WithDataDir(dataDir),
	)

	// 7. Select runtime: K3S if available (Linux), else native process
	nativeRt := buildNativeRuntime(dataDir)
	rt := selectRuntime(ctx, k3sClient, nativeRt)
	slog.Info("runtime selected", "runtime", rt.Name())

	// 8. Create MCP server with tool deps wired
	mcpServer := mcp.NewServer()
	deps := buildToolDeps(cat, db, knowledgeStore, rt, nativeRt, proxyServer, k3sClient, dataDir)
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

// findModelAssetOrVariant resolves a name to a model asset, optionally via variant name.
// Priority: model name (via findModelAsset) → variant name match.
// When matched by variant name, the returned variant pointer is non-nil.
func findModelAssetOrVariant(cat *knowledge.Catalog, name string) (*knowledge.ModelAsset, *knowledge.ModelVariant) {
	// First try as model name
	if ma, _ := findModelAsset(cat, name); ma != nil {
		return ma, nil
	}
	// Then try as variant name
	lower := strings.ToLower(name)
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		for j := range ma.Variants {
			if strings.ToLower(ma.Variants[j].Name) == lower {
				return ma, &ma.Variants[j]
			}
		}
	}
	return nil, nil
}

// registerPulledModel scans and registers a downloaded model in the database.
func registerPulledModel(ctx context.Context, destPath, dataDir string, db *state.DB) error {
	modelsDir := filepath.Join(dataDir, "models")
	info, err := model.Import(ctx, destPath, modelsDir)
	if err != nil {
		slog.Warn("model downloaded but scan/register failed", "path", destPath, "err", err)
		return nil // download succeeded; registration failure is non-fatal
	}
	return db.UpsertScannedModel(ctx, &state.Model{
		ID:             info.ID,
		Name:           info.Name,
		Type:           info.Type,
		Path:           info.Path,
		Format:         info.Format,
		SizeBytes:      info.SizeBytes,
		DetectedArch:   info.DetectedArch,
		DetectedParams: info.DetectedParams,
		ModelClass:     info.ModelClass,
		TotalParams:    info.TotalParams,
		ActiveParams:   info.ActiveParams,
		Quantization:   info.Quantization,
		QuantSrc:       info.QuantSrc,
		Status:         "registered",
	})
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

// toEngineBinarySource converts a knowledge.EngineSource to engine.BinarySource.
// Centralises the mapping so callers don't repeat the 4-field struct literal.
func toEngineBinarySource(src *knowledge.EngineSource) *engine.BinarySource {
	return &engine.BinarySource{
		Binary:    src.Binary,
		Platforms: src.Platforms,
		Download:  src.Download,
		Mirror:    src.Mirror,
	}
}

// execRunner implements engine.CommandRunner using real exec.
type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// podQuerierAdapter bridges k3s.Client to stack.PodQuerier interface.
type podQuerierAdapter struct {
	client *k3s.Client
}

func (a *podQuerierAdapter) ListPodsByLabel(ctx context.Context, namespace, label string) ([]stack.PodDetail, error) {
	pods, err := a.client.ListPodsByLabel(ctx, namespace, label)
	if err != nil {
		return nil, err
	}
	details := make([]stack.PodDetail, len(pods))
	for i, p := range pods {
		details[i] = stack.PodDetail{
			Name:    p.Name,
			Phase:   p.Phase,
			Ready:   p.Ready,
			Message: p.Message,
		}
	}
	return details, nil
}

// detectHWProfile returns the hardware profile string (e.g. "Blackwell-arm64") or "" if detection fails.
func detectHWProfile(ctx context.Context) string {
	hw, err := hal.Detect(ctx)
	if err != nil || hw.GPU == nil {
		return ""
	}
	return hw.GPU.Arch + "-" + hw.CPU.Arch
}

// newK3SClient creates a K3S client configured for the current system.
// If "kubectl" is in PATH, uses it directly. Otherwise, looks for the k3s binary
// in dist/ or PATH and uses its built-in kubectl (k3s kubectl ...).
func newK3SClient(dataDir string) *k3s.Client {
	if _, err := exec.LookPath("kubectl"); err == nil {
		return k3s.NewClient()
	}
	// kubectl not in PATH — try k3s binary directly
	platform := goruntime.GOOS + "-" + goruntime.GOARCH
	k3sPath := filepath.Join(dataDir, "dist", platform, "k3s")
	if _, err := os.Stat(k3sPath); err == nil {
		return k3s.NewClient(k3s.WithK3SBinary(k3sPath))
	}
	if p, err := exec.LookPath("k3s"); err == nil {
		return k3s.NewClient(k3s.WithK3SBinary(p))
	}
	return k3s.NewClient()
}

// buildNativeRuntime constructs a native process runtime for the current platform.
func buildNativeRuntime(dataDir string) runtime.Runtime {
	platform := goruntime.GOOS + "-" + goruntime.GOARCH
	distDir := filepath.Join(dataDir, "dist", platform)
	bm := engine.NewBinaryManager(distDir)
	return runtime.NewNativeRuntime(
		filepath.Join(dataDir, "logs"),
		distDir,
		filepath.Join(dataDir, "deployments"),
		runtime.WithBinaryResolver(func(ctx context.Context, src *engine.BinarySource) (string, error) {
			return bm.Resolve(ctx, src)
		}),
	)
}

// selectRuntime picks the best runtime: K3S on Linux if available, else native.
func selectRuntime(ctx context.Context, k3sClient *k3s.Client, nativeRt runtime.Runtime) runtime.Runtime {
	if goruntime.GOOS == "linux" && runtime.K3SAvailable(ctx, k3sClient) {
		return runtime.NewK3SRuntime(k3sClient)
	}
	return nativeRt
}

// buildHardwareInfo creates a HardwareInfo with platform, runtime, and hardware awareness.
// Populates both static fields (from hal.Detect) and dynamic fields (from hal.CollectMetrics).
// Missing data results in zero values, which downstream functions treat as "unknown" and skip.
func buildHardwareInfo(ctx context.Context, rtName string) knowledge.HardwareInfo {
	hwInfo := knowledge.HardwareInfo{
		Platform:    goruntime.GOOS + "/" + goruntime.GOARCH,
		RuntimeType: rtName,
	}
	if hw, err := hal.Detect(ctx); err == nil {
		if hw.GPU != nil {
			hwInfo.GPUArch = hw.GPU.Arch
			hwInfo.GPUVRAMMiB = hw.GPU.VRAMMiB
			hwInfo.GPUCount = hw.GPU.Count
			hwInfo.UnifiedMemory = hw.GPU.UnifiedMemory
		}
		hwInfo.CPUArch = hw.CPU.Arch
		hwInfo.CPUCores = hw.CPU.Cores
		hwInfo.RAMTotalMiB = hw.RAM.TotalMiB
		hwInfo.RAMAvailMiB = hw.RAM.AvailableMiB
	}
	// Dynamic layer: collect runtime GPU metrics (failure is non-fatal)
	if m, err := hal.CollectMetrics(ctx); err == nil && m.GPU != nil {
		hwInfo.GPUMemUsedMiB = m.GPU.MemoryUsedMiB
		hwInfo.GPUMemFreeMiB = m.GPU.MemoryTotalMiB - m.GPU.MemoryUsedMiB
	}
	return hwInfo
}

// resolveWithFallback tries catalog resolution first; on "not found in catalog",
// falls back to building a synthetic ModelAsset from the model's DB scan record.
func resolveWithFallback(ctx context.Context, cat *knowledge.Catalog, db *state.DB, hw knowledge.HardwareInfo, modelName, engineType string, overrides map[string]any, dataDir string) (*knowledge.ResolvedConfig, string, error) {
	resolved, err := cat.Resolve(hw, modelName, engineType, overrides)
	if err == nil {
		// Catalog hit — but ModelPath may be empty if no override was given.
		// Look up DB for the actual registered path from scan/import.
		if resolved.ModelPath == "" {
			if dbModel, dbErr := db.FindModelByName(ctx, modelName); dbErr == nil && dbModel.Path != "" {
				resolved.ModelPath = dbModel.Path
			}
		}
		return resolved, modelName, nil
	}
	if !strings.Contains(err.Error(), "not found in catalog") {
		return nil, "", fmt.Errorf("resolve config: %w", err)
	}

	// Catalog miss — try the scan database
	dbModel, dbErr := db.FindModelByName(ctx, modelName)
	if dbErr != nil {
		return nil, "", fmt.Errorf("resolve config: model %q not found in catalog (also not found in scan database)", modelName)
	}
	if dbModel.Format == "" {
		return nil, "", fmt.Errorf("model %q found on disk but has no format info; cannot auto-detect engine", dbModel.Name)
	}

	slog.Info("model not in catalog, using auto-detected config",
		"model", dbModel.Name, "format", dbModel.Format, "path", dbModel.Path)

	synth := knowledge.BuildSyntheticModelAsset(
		dbModel.Name, dbModel.Type, dbModel.DetectedArch, dbModel.DetectedParams, dbModel.Format)
	cat.RegisterModel(synth)

	if overrides == nil {
		overrides = map[string]any{}
	}
	overrides["model_path"] = dbModel.Path

	resolved, err = cat.Resolve(hw, dbModel.Name, engineType, overrides)
	if err != nil {
		return nil, "", fmt.Errorf("resolve auto-detected config for %s: %w", dbModel.Name, err)
	}
	return resolved, dbModel.Name, nil
}

// buildToolDeps wires all ToolDeps fields to real implementations.
// nativeRt is always provided so DeployApply can use it when the engine recommends native runtime.
func buildToolDeps(cat *knowledge.Catalog, db *state.DB, kStore *knowledge.Store, rt runtime.Runtime, nativeRt runtime.Runtime, proxyServer *proxy.Server, k3sClient *k3s.Client, dataDir string) *mcp.ToolDeps {
	scanEnginesImpl := func(ctx context.Context, runtimeFilter string) (json.RawMessage, error) {
		assetPatterns := make(map[string][]string)
		binaryAssets := make(map[string]string)
		for _, ea := range cat.EngineAssets {
			if len(ea.Patterns) > 0 {
				assetPatterns[ea.Metadata.Type] = ea.Patterns
			}
			if ea.Source != nil && ea.Source.Binary != "" {
				binaryAssets[ea.Source.Binary] = ea.Metadata.Type
				binaryAssets[ea.Source.Binary+".exe"] = ea.Metadata.Type
			}
		}
		platform := goruntime.GOOS + "-" + goruntime.GOARCH
		distDir := filepath.Join(dataDir, "dist", platform)
		images, err := engine.ScanUnified(ctx, engine.ScanOptions{
			AssetPatterns: assetPatterns,
			Runner:       &execRunner{},
			DistDir:      distDir,
			Platform:     platform,
			BinaryAssets: binaryAssets,
		})
		if err != nil {
			return nil, err
		}
		filtered := make([]*engine.EngineImage, 0)
		for _, img := range images {
			if runtimeFilter == "auto" || img.RuntimeType == runtimeFilter {
				filtered = append(filtered, img)
				_ = db.UpsertScannedEngine(ctx, &state.Engine{
					ID:          img.ID,
					Type:        img.Type,
					Image:       img.Image,
					Tag:         img.Tag,
					SizeBytes:   img.SizeBytes,
					Platform:    img.Platform,
					RuntimeType: img.RuntimeType,
					BinaryPath:  img.BinaryPath,
					Available:   img.Available,
				})
			}
		}
		return json.Marshal(filtered)
	}
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
			for _, m := range models {
				_ = db.UpsertScannedModel(ctx, &state.Model{
					ID:             m.ID,
					Name:           m.Name,
					Type:           m.Type,
					Path:           m.Path,
					Format:         m.Format,
					SizeBytes:      m.SizeBytes,
					DetectedArch:   m.DetectedArch,
					DetectedParams: m.DetectedParams,
					ModelClass:     m.ModelClass,
					TotalParams:    m.TotalParams,
					ActiveParams:   m.ActiveParams,
					Quantization:   m.Quantization,
					QuantSrc:       m.QuantSrc,
				})
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
			// Try model name first, then variant name
			ma, matchedVariant := findModelAssetOrVariant(cat, name)
			if ma == nil {
				return fmt.Errorf("model %q not found in catalog\navailable: %s", name, catalogModelNames(cat))
			}

			// If matched by variant name, use variant's source directly if available
			if matchedVariant != nil && matchedVariant.Source != nil {
				slog.Info("model pull: using variant source", "variant", matchedVariant.Name, "repo", matchedVariant.Source.Repo)
				destPath := filepath.Join(dataDir, "models", ma.Metadata.Name)
				sources := []model.Source{{Type: matchedVariant.Source.Type, Repo: matchedVariant.Source.Repo}}
				if err := model.DownloadFromSource(ctx, sources, destPath); err != nil {
					return err
				}
				return registerPulledModel(ctx, destPath, dataDir, db)
			}

			// Determine required format: resolve what engine this hardware would use
			requiredFormat := ""
			var resolvedVariant *knowledge.ModelVariant
			hwInfo := buildHardwareInfo(ctx, rt.Name())
			if engineType, err := cat.InferEngineType(ma.Metadata.Name, hwInfo); err == nil {
				for i, v := range ma.Variants {
					if v.Engine == engineType {
						requiredFormat = v.Format
						resolvedVariant = &ma.Variants[i]
						break
					}
				}
				slog.Info("model pull: inferred format", "engine", engineType, "format", requiredFormat)
			}

			// If resolved variant has its own source, use it directly
			if resolvedVariant != nil && resolvedVariant.Source != nil {
				slog.Info("model pull: using variant source", "variant", resolvedVariant.Name, "repo", resolvedVariant.Source.Repo)
				destPath := filepath.Join(dataDir, "models", ma.Metadata.Name)
				sources := []model.Source{{Type: resolvedVariant.Source.Type, Repo: resolvedVariant.Source.Repo}}
				if err := model.DownloadFromSource(ctx, sources, destPath); err != nil {
					return err
				}
				return registerPulledModel(ctx, destPath, dataDir, db)
			}

			// Fallback: filter global sources by format
			destPath := filepath.Join(dataDir, "models", ma.Metadata.Name)
			var sources []model.Source
			for _, s := range ma.Storage.Sources {
				if s.Type == "local_path" {
					continue
				}
				if requiredFormat != "" && s.Format != "" && s.Format != requiredFormat {
					continue
				}
				sources = append(sources, model.Source{Type: s.Type, Repo: s.Repo})
			}
			if len(sources) == 0 {
				return fmt.Errorf("no download source for model %q with format %q", name, requiredFormat)
			}
			if err := model.DownloadFromSource(ctx, sources, destPath); err != nil {
				return err
			}
			return registerPulledModel(ctx, destPath, dataDir, db)
		},
		ImportModel: func(ctx context.Context, path string) (json.RawMessage, error) {
			destDir := filepath.Join(dataDir, "models")
			info, err := model.Import(ctx, path, destDir)
			if err != nil {
				return nil, err
			}
			// Register imported model in database
			if err := db.UpsertScannedModel(ctx, &state.Model{
				ID:             info.ID,
				Name:           info.Name,
				Type:           info.Type,
				Path:           info.Path,
				Format:         info.Format,
				SizeBytes:      info.SizeBytes,
				DetectedArch:   info.DetectedArch,
				DetectedParams: info.DetectedParams,
				ModelClass:     info.ModelClass,
				TotalParams:    info.TotalParams,
				ActiveParams:   info.ActiveParams,
				Quantization:   info.Quantization,
				QuantSrc:       info.QuantSrc,
				Status:         "registered",
			}); err != nil {
				return nil, fmt.Errorf("register imported model: %w", err)
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
		RemoveModel: func(ctx context.Context, name string, deleteFiles bool) error {
			// First get the model to find its ID and Path
			m, err := db.GetModel(ctx, name)
			if err != nil {
				return err
			}
			// Delete from database
			if err := db.DeleteModel(ctx, m.ID); err != nil {
				return err
			}
			// Delete files from disk if requested
			if deleteFiles {
				if m.Path != "" {
					// For GGUF models, Path is the file path itself
					// For other models, Path is the directory
					info, statErr := os.Stat(m.Path)
					if statErr == nil {
						if info.IsDir() {
							os.RemoveAll(m.Path)
						} else {
							os.Remove(m.Path)
						}
					}
				}
			}
			return nil
		},

		// Engine management
		ScanEngines: scanEnginesImpl,
		ListEngines: func(ctx context.Context) (json.RawMessage, error) {
			engines, err := db.ListEngines(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(engines)
		},
		GetEngineInfo: func(ctx context.Context, name string) (json.RawMessage, error) {
			// Find matching catalog asset (by type, asset name, or image name)
			var asset *knowledge.EngineAsset
			nameLower := strings.ToLower(name)
			for i := range cat.EngineAssets {
				ea := &cat.EngineAssets[i]
				if strings.ToLower(ea.Metadata.Type) == nameLower ||
					strings.ToLower(ea.Metadata.Name) == nameLower ||
					strings.Contains(strings.ToLower(ea.Image.Name), nameLower) {
					asset = ea
					break
				}
			}

			// Find installed instances in DB (by type, image name, or ID)
			allEngines, err := db.ListEngines(ctx)
			if err != nil {
				return nil, err
			}
			installed := make([]*state.Engine, 0)
			for _, e := range allEngines {
				if strings.ToLower(e.Type) == nameLower ||
					strings.Contains(strings.ToLower(e.Image), nameLower) ||
					strings.HasPrefix(e.ID, name) {
					installed = append(installed, e)
				}
			}

			if asset == nil && len(installed) == 0 {
				return nil, fmt.Errorf("engine %q not found in catalog or database", name)
			}

			// If found only in DB, try to find the catalog asset by type
			if asset == nil && len(installed) > 0 {
				typeLower := strings.ToLower(installed[0].Type)
				for i := range cat.EngineAssets {
					if strings.ToLower(cat.EngineAssets[i].Metadata.Type) == typeLower {
						asset = &cat.EngineAssets[i]
						break
					}
				}
			}

			result := struct {
				Asset     *knowledge.EngineAsset `json:"asset"`
				Installed []*state.Engine        `json:"installed"`
			}{
				Asset:     asset,
				Installed: installed,
			}
			return json.Marshal(result)
		},
		PullEngine: func(ctx context.Context, name string) error {
			if name == "" {
				name = "llamacpp"
			}
			hwInfo := buildHardwareInfo(ctx, rt.Name())
			nameLower := strings.ToLower(name)

			// Select best engine asset: exact gpu_arch match first, then wildcard "*"
			var matched, wildcard *knowledge.EngineAsset
			for i := range cat.EngineAssets {
				ea := &cat.EngineAssets[i]
				if strings.ToLower(ea.Metadata.Type) != nameLower && strings.ToLower(ea.Metadata.Name) != nameLower {
					continue
				}
				if ea.Hardware.GPUArch == hwInfo.GPUArch {
					matched = ea
					break
				}
				if ea.Hardware.GPUArch == "*" && wildcard == nil {
					wildcard = ea
				}
			}
			if matched == nil {
				matched = wildcard
			}
			if matched == nil {
				return fmt.Errorf("engine %q not found in catalog", name)
			}
			ea := matched

			// Native binary path: prefer if platform is supported
			platform := goruntime.GOOS + "/" + goruntime.GOARCH
			if ea.Source != nil && ea.Source.Supports(platform) {
				distPlatform := goruntime.GOOS + "-" + goruntime.GOARCH
				distDir := filepath.Join(dataDir, "dist", distPlatform)
				mgr := engine.NewBinaryManager(distDir)
				return mgr.Download(ctx, toEngineBinarySource(ea.Source))
			}
			// Container image path
			if ea.Image.Name != "" {
				// Skip network pull if the image is already available locally
				if engine.ImageExists(ctx, ea.Image.Name, ea.Image.Tag, &execRunner{}) {
					slog.Info("engine image already available locally", "image", ea.Image.Name+":"+ea.Image.Tag)
					return nil
				}
				return engine.Pull(ctx, engine.PullOptions{
					Image:      ea.Image.Name,
					Tag:        ea.Image.Tag,
					Registries: ea.Image.Registries,
					Runner:     &execRunner{},
				})
			}
			return fmt.Errorf("engine %q has no download source for platform %s/%s", name, goruntime.GOOS, goruntime.GOARCH)
		},
		ImportEngine: func(ctx context.Context, path string) error {
			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve path %s: %w", path, err)
			}
			if err := engine.Import(ctx, absPath, &execRunner{}); err != nil {
				return err
			}
			// Refresh DB: imported image only visible via runtime scan
			_, _ = scanEnginesImpl(ctx, "auto")
			return nil
		},

		// Deployment (runtime abstraction: K3S or native)
		DeployApply: func(ctx context.Context, engineType, modelName, slot string) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, rt.Name())
			overrides := map[string]any{}
			if slot != "" {
				overrides["slot"] = slot
			}
			resolved, canonicalName, err := resolveWithFallback(ctx, cat, db, hwInfo, modelName, engineType, overrides, dataDir)
			if err != nil {
				return nil, err
			}
			modelName = canonicalName

			// Hardware fitness check: validate and auto-adjust config
			fit := knowledge.CheckFit(resolved, hwInfo)
			if !fit.Fit {
				return nil, fmt.Errorf("hardware check: %s", fit.Reason)
			}
			for _, w := range fit.Warnings {
				slog.Warn("deploy fitness", "warning", w)
			}
			for k, v := range fit.Adjustments {
				resolved.Config[k] = v
				resolved.Provenance[k] = "L0-auto"
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
			// Native binary engines require a single model file path; container engines
			// take the directory. Use the presence of Source (native binary download) to
			// distinguish — not the engine type name.
			if resolved.Source != nil {
				if fi, err := os.Stat(modelPath); err == nil && fi.IsDir() {
					if f := findModelFileInDir(modelPath); f != "" {
						modelPath = f
					}
				}
			}

			req := &runtime.DeployRequest{
				Name:             modelName,
				Engine:           resolved.Engine,
				Image:            resolved.EngineImage,
				Command:          resolved.Command,
				ModelPath:        modelPath,
				Port:             port,
				Config:           resolved.Config,
				RuntimeClassName: resolved.RuntimeClassName,
				Labels: map[string]string{
					"aima.dev/engine": resolved.Engine,
					"aima.dev/model":  modelName,
					"aima.dev/slot":   resolved.Slot,
					"aima.dev/port":   fmt.Sprintf("%d", port),
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
			if resolved.Source != nil {
				req.BinarySource = toEngineBinarySource(resolved.Source)
			}
			if resolved.Warmup != nil {
				req.Warmup = &runtime.WarmupConfig{
					Prompt:    resolved.Warmup.Prompt,
					MaxTokens: resolved.Warmup.MaxTokens,
					TimeoutS:  resolved.Warmup.TimeoutS,
				}
			}

			// Use native runtime when the engine explicitly recommends it (e.g. Vulkan on AMD).
			activeRt := rt
			if resolved.RuntimeRecommendation == "native" && nativeRt != nil {
				activeRt = nativeRt
			}
			if err := activeRt.Deploy(ctx, req); err != nil {
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
				"runtime": activeRt.Name(),
			}
			return json.Marshal(result)
		},
		DeployDelete: func(ctx context.Context, name string) error {
			// Try exact pod name first, then fall back to searching by model label.
			// Pod names are "<model>-<engine>" (e.g. qwen3-8b-vllm), but users
			// often pass just the model name (e.g. qwen3-8b).
			deleted := name
			err := rt.Delete(ctx, name)
			if err != nil {
				// Exact name failed — search deployments for this model name.
				if deployments, listErr := rt.List(ctx); listErr == nil {
					for _, d := range deployments {
						if d.Labels["aima.dev/model"] == name || d.Name == name {
							if delErr := rt.Delete(ctx, d.Name); delErr == nil {
								deleted = d.Name
								err = nil
								break
							}
						}
					}
				}
			}
			if err != nil && nativeRt != nil && nativeRt != rt {
				err = nativeRt.Delete(ctx, name)
				if err == nil {
					deleted = name
				}
			}
			if err != nil {
				return fmt.Errorf("deployment %q not found", name)
			}
			proxyServer.RemoveBackend(deleted)
			return nil
		},
		DeployStatus: func(ctx context.Context, name string) (json.RawMessage, error) {
			s, err := rt.Status(ctx, name)
			if err != nil && nativeRt != nil && nativeRt != rt {
				s, err = nativeRt.Status(ctx, name)
			}
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
			// Also include native deployments (when engine recommended native on a K3S machine).
			if nativeRt != nil && nativeRt != rt {
				nativeStatuses, _ := nativeRt.List(ctx)
				statuses = append(statuses, nativeStatuses...)
			}
			return json.Marshal(statuses)
		},
		DeployLogs: func(ctx context.Context, name string, tailLines int) (string, error) {
			logs, err := rt.Logs(ctx, name, tailLines)
			if err != nil && nativeRt != nil && nativeRt != rt {
				logs, err = nativeRt.Logs(ctx, name, tailLines)
			}
			return logs, err
		},

		// Knowledge
		ResolveConfig: func(ctx context.Context, modelName, engineType string, overrides map[string]any) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, rt.Name())
			resolved, _, err := resolveWithFallback(ctx, cat, db, hwInfo, modelName, engineType, overrides, dataDir)
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
			resolved, _, err := resolveWithFallback(ctx, cat, db, hwInfo, modelName, engineType, overrides, dataDir)
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

		// Benchmark
		RecordBenchmark: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Hardware       string         `json:"hardware"`
				Engine         string         `json:"engine"`
				Model          string         `json:"model"`
				DeviceID       string         `json:"device_id"`
				Config         map[string]any `json:"config"`
				Concurrency    int            `json:"concurrency"`
				InputLenBucket string         `json:"input_len_bucket"`
				OutputLenBucket string        `json:"output_len_bucket"`
				TTFTP50ms      float64        `json:"ttft_ms_p50"`
				TTFTP95ms      float64        `json:"ttft_ms_p95"`
				TPOTP50ms      float64        `json:"tpot_ms_p50"`
				TPOTP95ms      float64        `json:"tpot_ms_p95"`
				ThroughputTPS  float64        `json:"throughput_tps"`
				QPS            float64        `json:"qps"`
				VRAMUsageMiB   int            `json:"vram_usage_mib"`
				SampleCount    int            `json:"sample_count"`
				Stability      string         `json:"stability"`
				Notes          string         `json:"notes"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse benchmark params: %w", err)
			}
			if p.Concurrency <= 0 {
				p.Concurrency = 1
			}

			// Find or create configuration
			configJSON, _ := json.Marshal(p.Config)
			configHash := fmt.Sprintf("%x", sha256.Sum256(
				[]byte(p.Hardware+"|"+p.Engine+"|"+p.Model+"|"+string(configJSON))))

			cfg, err := db.FindConfigByHash(ctx, configHash)
			if err != nil {
				return nil, err
			}
			if cfg == nil {
				cfg = &state.Configuration{
					ID:         fmt.Sprintf("%x", sha256.Sum256([]byte(configHash)))[:16],
					HardwareID: p.Hardware,
					EngineID:   p.Engine,
					ModelID:    p.Model,
					Config:     string(configJSON),
					ConfigHash: configHash,
					Status:     "experiment",
					Source:     "benchmark",
					DeviceID:   p.DeviceID,
				}
				if err := db.InsertConfiguration(ctx, cfg); err != nil {
					return nil, fmt.Errorf("create configuration: %w", err)
				}
			}

			// Insert benchmark result
			benchID := fmt.Sprintf("%x", sha256.Sum256(
				[]byte(cfg.ID+"|"+fmt.Sprintf("%d", time.Now().UnixNano()))))[:16]
			br := &state.BenchmarkResult{
				ID:              benchID,
				ConfigID:        cfg.ID,
				Concurrency:     p.Concurrency,
				InputLenBucket:  p.InputLenBucket,
				OutputLenBucket: p.OutputLenBucket,
				Modality:        "text",
				TTFTP50ms:       p.TTFTP50ms,
				TTFTP95ms:       p.TTFTP95ms,
				TPOTP50ms:       p.TPOTP50ms,
				TPOTP95ms:       p.TPOTP95ms,
				ThroughputTPS:   p.ThroughputTPS,
				QPS:             p.QPS,
				VRAMUsageMiB:    p.VRAMUsageMiB,
				SampleCount:     p.SampleCount,
				Stability:       p.Stability,
				TestedAt:        time.Now(),
				AgentModel:      "claude-opus-4.6",
				Notes:           p.Notes,
			}
			if err := db.InsertBenchmarkResult(ctx, br); err != nil {
				return nil, fmt.Errorf("insert benchmark: %w", err)
			}

			return json.Marshal(map[string]any{
				"benchmark_id": benchID,
				"config_id":    cfg.ID,
				"status":       "recorded",
				"hardware":     p.Hardware,
				"engine":       p.Engine,
				"model":        p.Model,
			})
		},

		// Discovery
		DiscoverLAN: func(ctx context.Context, timeoutS int) (json.RawMessage, error) {
			services, err := proxy.Discover(ctx, time.Duration(timeoutS)*time.Second)
			if err != nil {
				return nil, err
			}
			return json.Marshal(services)
		},

		// Stack management
		StackPreflight: func(ctx context.Context) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir)
			items := installer.Preflight(cat.StackComponents)
			return json.Marshal(items)
		},
		StackInit: func(ctx context.Context, allowDownload bool) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir).
				WithPodQuerier(&podQuerierAdapter{client: k3sClient})
			if err := installer.PreCheck(ctx, cat.StackComponents); err != nil {
				return nil, err
			}
			if allowDownload {
				missing := installer.Preflight(cat.StackComponents)
				if err := stack.DownloadItems(ctx, missing); err != nil {
					return nil, fmt.Errorf("download: %w", err)
				}
			}
			hwProfile := detectHWProfile(ctx)
			result, err := installer.Init(ctx, cat.StackComponents, hwProfile)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},
		StackStatus: func(ctx context.Context) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir).
				WithPodQuerier(&podQuerierAdapter{client: k3sClient})
			hwProfile := detectHWProfile(ctx)
			result, err := installer.Status(ctx, cat.StackComponents, hwProfile)
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

// findModelFileInDir returns the first model file found inside dir.
// Only called for native binary engines (where the engine YAML has a source: field);
// container engines receive the directory path directly.
func findModelFileInDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".gguf", ".ggml", ".bin", ".safetensors":
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}
