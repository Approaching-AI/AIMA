package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/jguan/aima/catalog"
	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/cli"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/fleet"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/model"
	"github.com/jguan/aima/internal/openclaw"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/runtime"
	"github.com/jguan/aima/internal/support"
	"github.com/jguan/aima/internal/ui"

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

	// Fast path: --help, version, completion don't need DB/catalog/runtime.
	if isLightweightInvocation() {
		app := &cli.App{} // nil deps — version/help don't use them
		rootCmd := cli.NewRootCmd(app)
		return rootCmd.ExecuteContext(ctx)
	}

	// 1. Determine data directory
	// Priority: AIMA_DATA_DIR env > /etc/aima/data-dir (shared config from systemd install) > ~/.aima
	dataDir := os.Getenv("AIMA_DATA_DIR")
	if dataDir == "" {
		if shared, err := os.ReadFile("/etc/aima/data-dir"); err == nil {
			dataDir = strings.TrimSpace(string(shared))
		}
	}
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home dir: %w", err)
		}
		dataDir = filepath.Join(home, ".aima")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		if _, statErr := os.Stat(dataDir); statErr != nil {
			return fmt.Errorf("create data dir: %w", err)
		}
		// Directory exists but we can't write to it (different owner).
		// Fall back to user's home dir for writable state (DB, cache).
		slog.Info("shared data dir is read-only for current user, using home for state", "shared", dataDir)
	}

	// 2. Open state database
	db, err := state.Open(ctx, filepath.Join(dataDir, "aima.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// 3-4. Load catalog (embedded + overlay), sync to SQLite.
	cat, factoryDigests, err := initCatalog(ctx, db, dataDir)
	if err != nil {
		return err
	}

	// 5. Create knowledge query store (backed by SQLite)
	knowledgeStore := knowledge.NewStore(db.RawDB())

	// 6. Create infrastructure components
	k3sClient := newK3SClient(dataDir)
	proxyServer := proxy.NewServer()
	// 7. Build all available runtimes, select default (K3S > Docker > Native)
	nativeRt := buildNativeRuntime(dataDir, cat.EngineAssets)
	var dockerRt, k3sRt runtime.Runtime
	if goruntime.GOOS == "linux" {
		if runtime.DockerAvailable(ctx) {
			dockerRt = runtime.NewDockerRuntime(cat.EngineAssets)
		}
		if runtime.K3SAvailable(ctx, k3sClient) {
			k3sRt = runtime.NewK3SRuntime(k3sClient, runtime.WithEngineAssets(cat.EngineAssets))
		}
	}
	rt := selectDefaultRuntime(k3sRt, dockerRt, nativeRt)
	slog.Info("runtime selected", "runtime", rt.Name(),
		"docker_available", dockerRt != nil, "k3s_available", k3sRt != nil)
	if err := seedCatalogOpenQuestions(ctx, db, cat); err != nil {
		return err
	}

	// 8. Create MCP server with tool deps wired
	mcpServer := mcp.NewServer()
	supportSvc := support.NewService(db, support.WithLogger(slog.Default()))
	ac := &appContext{
		cat:      cat,
		db:       db,
		kStore:   knowledgeStore,
		rt:       rt,
		nativeRt: nativeRt,
		dockerRt: dockerRt,
		k3sRt:    k3sRt,
		proxy:    proxyServer,
		k3s:      k3sClient,
		dataDir:  dataDir,
		digests:  factoryDigests,
		support:  supportSvc,
	}
	deps := buildToolDeps(ac)

	// 9. Create agent (L3a Go Agent)
	toolAdapter := &mcpToolAdapter{server: mcpServer, db: db, pending: make(map[int64]*pendingApproval)}
	automationTools := &automationToolAdapter{base: toolAdapter}
	var explorationMgr *agent.ExplorationManager
	llmClient := buildLLMClient(ctx, db)
	sessionStore := agent.NewSessionStore()
	goAgent := agent.NewAgent(llmClient, toolAdapter, agent.WithSessions(sessionStore))
	dispatcher := agent.NewDispatcher(goAgent)

	// 9b. Wire agent-related ToolDeps (dispatcher created after buildToolDeps)
	deps.DispatchAsk = func(ctx context.Context, query string, skipPerms bool, sessionID string) (json.RawMessage, string, error) {
		if skipPerms {
			ctx = context.WithValue(ctx, ctxKeySkipPerms, true)
		}
		result, sid, toolCalls, err := dispatcher.Ask(ctx, query, agent.DispatchOption{SessionID: sessionID})
		if err != nil {
			return nil, "", err
		}
		data, err := json.Marshal(map[string]any{"result": result, "session_id": sid, "tool_calls": toolCalls})
		return data, sid, err
	}
	deps.DeployApprove = func(ctx context.Context, id int64) (json.RawMessage, error) {
		return toolAdapter.executeApproval(ctx, id)
	}
	deps.AgentStatus = func(ctx context.Context) (json.RawMessage, error) {
		activeRuns := 0
		if explorationMgr != nil {
			activeRuns = explorationMgr.ActiveCount()
		}
		return buildAgentStatusPayload(ctx, llmClient, goAgent.ToolMode(), activeRuns)
	}
	deps.AgentGuide = func(ctx context.Context) (json.RawMessage, error) {
		guide, err := catalog.FS.ReadFile("agent-guide.md")
		if err != nil {
			return nil, fmt.Errorf("read agent guide: %w", err)
		}
		return json.Marshal(map[string]string{"guide": string(guide)})
	}

	// 9c. Wire rollback tools
	deps.RollbackList = func(ctx context.Context) (json.RawMessage, error) {
		snapshots, err := db.ListSnapshots(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(snapshots)
	}
	deps.RollbackRestore = func(ctx context.Context, id int64) (json.RawMessage, error) {
		snap, err := db.GetSnapshot(ctx, id)
		if err != nil {
			return nil, err
		}
		switch snap.ResourceType {
		case "model":
			var m state.Model
			if err := json.Unmarshal([]byte(snap.Snapshot), &m); err != nil {
				return nil, fmt.Errorf("unmarshal model snapshot: %w", err)
			}
			if err := db.UpsertScannedModel(ctx, &m); err != nil {
				return nil, fmt.Errorf("restore model %s: %w", m.Name, err)
			}
			return json.Marshal(map[string]string{"restored": "model", "name": m.Name, "note": "DB record restored; if files were deleted, re-import or re-pull the model"})
		case "engine":
			var e state.Engine
			if err := json.Unmarshal([]byte(snap.Snapshot), &e); err != nil {
				return nil, fmt.Errorf("unmarshal engine snapshot: %w", err)
			}
			if err := db.UpsertScannedEngine(ctx, &e); err != nil {
				return nil, fmt.Errorf("restore engine %s: %w", e.ID, err)
			}
			return json.Marshal(map[string]string{"restored": "engine", "name": e.ID, "note": "DB record restored; if image was removed, re-pull or re-import"})
		case "deployment":
			var d map[string]any
			if err := json.Unmarshal([]byte(snap.Snapshot), &d); err != nil {
				return nil, fmt.Errorf("unmarshal deployment snapshot: %w", err)
			}
			labels, _ := d["labels"].(map[string]any)
			modelName, _ := labels["aima.dev/model"].(string)
			engineType, _ := labels["aima.dev/engine"].(string)
			if modelName == "" {
				return nil, fmt.Errorf("snapshot missing model label, cannot redeploy")
			}
			result, err := deps.DeployApply(ctx, engineType, modelName, "", nil)
			if err != nil {
				return nil, fmt.Errorf("redeploy %s: %w", modelName, err)
			}
			return result, nil
		default:
			return nil, fmt.Errorf("unknown resource type %q", snap.ResourceType)
		}
	}

	deps.SupportAskForHelp = supportSvc.AskForHelpJSON
	// 9e. Fleet management: registry + client + REST routes + MCP tools
	fleetRegistry := fleet.NewRegistry(proxy.DefaultPort)
	fleetClient := fleet.NewClient(os.Getenv("AIMA_API_KEY"))
	fleetMCP := &fleetMCPAdapter{server: mcpServer}
	fleetDeps := &fleet.Deps{
		Registry: fleetRegistry,
		MCP:      fleetMCP,
		Client:   fleetClient,
		DeviceInfo: func(ctx context.Context) (json.RawMessage, error) {
			if deps.SystemStatus != nil {
				return deps.SystemStatus(ctx)
			}
			return json.Marshal(map[string]string{"status": "ok"})
		},
		DispatchAskStream: func(ctx context.Context, query, sessionID string, cb func(string, []byte)) (json.RawMessage, error) {
			var streamCB agent.StreamCallback
			if cb != nil {
				streamCB = func(ev agent.StreamEvent) {
					data, _ := json.Marshal(ev)
					cb(ev.Type, data)
				}
			}
			result, sid, toolCalls, err := dispatcher.Ask(ctx, query, agent.DispatchOption{
				SessionID:      sessionID,
				StreamCallback: streamCB,
			})
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(map[string]any{"result": result, "session_id": sid, "tool_calls": toolCalls})
			return data, err
		},
	}
	fleetRoutes := fleet.RegisterRoutes(fleetDeps)
	uiRoutes := ui.RegisterRoutes(&ui.Deps{
		SupportManifest: supportSvc.GoUXManifestJSON,
		OnboardingManifest: func(ctx context.Context) (json.RawMessage, error) {
			_ = ctx
			raw, err := buildOnboardingManifestJSON(cat)
			if err != nil {
				return nil, fmt.Errorf("build ui onboarding manifest: %w", err)
			}
			return raw, nil
		},
	})

	// OpenClaw integration: wire adapters + routes + sync tool
	mcpCommand := "aima"
	if exe, err := os.Executable(); err == nil && exe != "" {
		mcpCommand = exe
	}
	openclawDeps := &openclaw.Deps{
		Backends:   proxyBackendAdapter{proxyServer},
		Catalog:    catalogAdapter{cat},
		ConfigPath: openclaw.DefaultConfigPath(),
		ProxyAddr:  fmt.Sprintf("http://127.0.0.1:%d/v1", proxy.DefaultPort),
		APIKey:     proxyServer.APIKey,
		MCPCommand: mcpCommand,
	}
	openclawRoutes := openclaw.RegisterRoutes(openclawDeps)
	proxyServer.SetRequestRewriter(openclaw.RequestBodyRewriter(openclawDeps.Catalog))
	refreshOpenClawBackends := func(ctx context.Context) {
		// Ensure proxy has up-to-date backends (CLI mode has no sync loop).
		if deps.DeployList != nil {
			if raw, err := deps.DeployList(ctx); err == nil {
				var infos []*proxy.DeploymentInfo
				if err := json.Unmarshal(raw, &infos); err == nil {
					proxy.SyncBackends(proxyServer, infos)
				}
			}
		}
	}
	deps.OpenClawStatus = func(ctx context.Context) (json.RawMessage, error) {
		refreshOpenClawBackends(ctx)
		result, err := openclaw.Inspect(ctx, openclawDeps)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.OpenClawSync = func(ctx context.Context, dryRun bool) (json.RawMessage, error) {
		refreshOpenClawBackends(ctx)
		result, err := openclaw.Sync(ctx, openclawDeps, dryRun)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.OpenClawClaim = func(ctx context.Context, sections []string, dryRun bool) (json.RawMessage, error) {
		refreshOpenClawBackends(ctx)
		result, err := openclaw.Claim(ctx, openclawDeps, openclaw.ClaimOptions{
			DryRun:   dryRun,
			Sections: sections,
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}

	// Wire integration tools (scenarios, apps, sync, power, validation, engine switch cost).
	// OpenQuestions is overwritten below where explorationMgr is available.
	buildIntegrationDeps(ac, deps)

	var (
		patrol *agent.Patrol // created later; captured by closure, safe because serve runs after init
		app    *cli.App      // created later; captured by closure so HTTP routes can reuse the exact Cobra tree
	)
	proxyServer.SetExtraRoutes(func(mux *http.ServeMux) {
		fleetRoutes(mux)
		uiRoutes(mux)
		openclawRoutes(mux)
		mux.HandleFunc("POST /api/v1/cli/exec", cli.NewExecHandler(func() *cli.App { return app }))
		mux.HandleFunc("/api/v1/power", handlePowerSnapshot(cat))
		mux.HandleFunc("/api/v1/power/history", func(w http.ResponseWriter, r *http.Request) {
			from := r.URL.Query().Get("from")
			to := r.URL.Query().Get("to")
			if from == "" {
				from = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
			}
			if to == "" {
				to = time.Now().UTC().Format(time.RFC3339)
			}
			results, err := db.QueryPowerHistory(r.Context(), from, to, 60)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(results)
		})

		// Start power sampling goroutine (30s interval, 7-day retention)
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				m, err := hal.CollectMetrics(context.Background())
				if err != nil || m.GPU == nil {
					continue
				}
				_ = db.InsertPowerSample(context.Background(), 0,
					m.GPU.PowerDrawWatts, m.GPU.TemperatureCelsius,
					float64(m.GPU.UtilizationPercent), m.GPU.MemoryUsedMiB, m.GPU.MemoryTotalMiB)
				_ = db.PrunePowerSamples(context.Background(), 7)
			}
		}()

		// Start patrol loop
		patrol.Start(context.Background())
	})

	// Wire fleet MCP tools (list_devices, device_info, device_tools, exec_tool).
	buildFleetDeps(deps, fleetRegistry, fleetClient, mcpServer)

	// 9f. Wrap SetConfig for API key hot-reload (needs proxyServer + fleetClient in scope)
	baseSetConfig := deps.SetConfig
	deps.SetConfig = func(ctx context.Context, key, value string) error {
		if key == "llm.extra_params" {
			if _, err := parseExtraParamsStrict(value); err != nil {
				return err
			}
		}
		if err := baseSetConfig(ctx, key, value); err != nil {
			return err
		}
		switch key {
		case "api_key":
			proxyServer.SetAPIKey(value)
			fleetClient.SetAPIKey(value)
			slog.Info("API key hot-reloaded via system.config")
		}
		switch key {
		case "api_key", "llm.endpoint", "llm.model", "llm.api_key", "llm.user_agent", "llm.extra_params":
			settings := reloadLLMSettings(ctx, db, llmClient, proxyServer.APIKey())
			slog.Info("LLM settings hot-reloaded via system.config",
				"trigger", key,
				"endpoint", settings.Endpoint,
				"model", settings.Model,
				"has_api_key", settings.APIKey != "",
				"has_extra_params", settings.ExtraParams != nil)
		}
		return nil
	}

	// 9g. Patrol, tuner, healer (A2, A3, A4)
	healer := agent.NewHealer(automationTools)
	tuner := agent.NewTuner(automationTools)
	explorationMgr = agent.NewExplorationManager(db, tuner, automationTools)
	patrol = agent.NewPatrol(agent.DefaultPatrolConfig(), toolAdapter, db.InsertPatrolAlert,
		agent.WithHealer(healer),
		agent.WithActionCallback(func(ctx context.Context, a agent.PatrolAction) {
			slog.Info("patrol_action_audit",
				"alert_id", a.AlertID, "type", a.Type,
				"success", a.Success, "detail", a.Detail)
		}),
	)

	// Wire agent, patrol, tuning, exploration, and open questions tools.
	buildAgentDeps(ac, deps, patrol, tuner, explorationMgr)

	// 9h. Register all tools (after all deps are fully wired)
	mcp.RegisterAllTools(mcpServer, deps)

	// 10. Build App and run CLI
	app = &cli.App{
		DB:            db,
		Catalog:       cat,
		Proxy:         proxyServer,
		MCP:           mcpServer,
		ToolDeps:      deps,
		OpenClaw:      openclawDeps,
		FleetRegistry: fleetRegistry,
		FleetClient:   fleetClient,
		Support:       supportSvc,
		LLMClient:     llmClient,
		OpenBrowser:   defaultRootArgs(os.Args) != nil,
	}

	rootCmd := cli.NewRootCmd(app)
	if args := defaultRootArgs(os.Args); args != nil {
		rootCmd.SetArgs(args)
	}
	return rootCmd.ExecuteContext(ctx)
}

// buildToolDeps wires all ToolDeps fields to real implementations.
// All runtime variants are provided via ac so DeployApply can select per-deployment.
func buildToolDeps(ac *appContext) *mcp.ToolDeps {
	cat := ac.cat
	db := ac.db
	rt := ac.rt
	proxyServer := ac.proxy
	dataDir := ac.dataDir

	dlTracker := NewDownloadTracker(filepath.Join(dataDir, "downloads"))

	scanEnginesCore := func(ctx context.Context, runtimeFilter string, autoImport bool) (json.RawMessage, error) {
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		assetPatterns := make(map[string][]string)
		binaryAssets := make(map[string]string)
		// Generic interpreters — not engine binaries, skip when inferring from startup.command[0].
		interpreters := map[string]bool{
			"python": true, "python3": true, "python2": true,
			"bash": true, "sh": true, "zsh": true,
			"node": true, "java": true, "ruby": true,
		}
		for _, ea := range cat.EngineAssets {
			if len(ea.Patterns) > 0 {
				assetPatterns[ea.Metadata.Type] = append(assetPatterns[ea.Metadata.Type], ea.Patterns...)
			}
			// Determine native binary name: explicit source.binary, or infer from startup.command[0]
			binName := ""
			if ea.Source != nil && ea.Source.Binary != "" {
				binName = ea.Source.Binary
			} else if len(ea.Startup.Command) > 0 {
				candidate := filepath.Base(ea.Startup.Command[0])
				if !interpreters[candidate] {
					binName = candidate
				}
			}
			if binName != "" {
				// First registration wins — avoids variant-specific types (e.g. "vllm-spark")
				// overwriting the generic type (e.g. "vllm") when multiple engine YAMLs share
				// the same binary. The resolver picks the correct variant by hardware later.
				if _, exists := binaryAssets[binName]; !exists {
					binaryAssets[binName] = ea.Metadata.Type
					binaryAssets[binName+".exe"] = ea.Metadata.Type
				}
			}
		}
		// Build preinstalled probes from engine assets with source.install_type == "preinstalled"
		preinstalledProbes := make(map[string]*knowledge.EngineSourceProbe)
		for _, ea := range cat.EngineAssets {
			if ea.Source != nil && ea.Source.InstallType == "preinstalled" && ea.Source.Probe != nil {
				preinstalledProbes[ea.Metadata.Name] = ea.Source.Probe
			}
		}
		platform := goruntime.GOOS + "-" + goruntime.GOARCH
		distDir := filepath.Join(dataDir, "dist", platform)
		images, err := engine.ScanUnified(ctx, engine.ScanOptions{
			AssetPatterns:      assetPatterns,
			Runner:             &execRunner{},
			DistDir:            distDir,
			Platform:           platform,
			BinaryAssets:       binaryAssets,
			AutoImport:         autoImport,
			PreinstalledProbes: preinstalledProbes,
		})
		if err != nil {
			return nil, err
		}
		images = dedupeScannedEngines(images, preferredContainerImagesByTypeTag(cat, hwInfo))
		filtered := make([]*engine.EngineImage, 0)
		var scannedIDs []string
		for _, img := range images {
			if runtimeFilter == "auto" || img.RuntimeType == runtimeFilter {
				filtered = append(filtered, img)
				scannedIDs = append(scannedIDs, img.ID)
				if err := db.UpsertScannedEngine(ctx, &state.Engine{
					ID:          img.ID,
					Type:        img.Type,
					Image:       img.Image,
					Tag:         img.Tag,
					SizeBytes:   img.SizeBytes,
					Platform:    img.Platform,
					RuntimeType: img.RuntimeType,
					BinaryPath:  img.BinaryPath,
					Available:   img.Available,
				}); err != nil {
					slog.Warn("engine scan: failed to persist engine", "id", img.ID, "error", err)
				}
			}
		}
		// Mark engines not found in this scan as unavailable (handles renamed/deleted images).
		// When filtering by runtime, only affect that runtime's engines to avoid cross-runtime pollution.
		markRuntime := ""
		if runtimeFilter != "auto" {
			markRuntime = runtimeFilter
		}
		if err := db.MarkEnginesUnavailableExcept(ctx, scannedIDs, markRuntime); err != nil {
			slog.Warn("engine scan: failed to mark stale engines", "error", err)
		}
		return json.Marshal(filtered)
	}

	// resolveEndpoint auto-detects the inference endpoint from proxy backends or falls back to localhost.
	resolveEndpoint := func(explicit, model string) string {
		if explicit != "" {
			return explicit
		}
		backends := proxyServer.ListBackends()
		if b, ok := backends[strings.ToLower(model)]; ok && b.Ready {
			return fmt.Sprintf("http://%s%s/v1/chat/completions", b.Address, b.BasePath)
		}
		return "http://localhost:6188/v1/chat/completions"
	}

	// pullModelCore extracts the model download logic so it can be reused
	// by both PullModel and DeployApply (auto-pull).
	pullModelCore := func(ctx context.Context, name string, onStatus func(phase, msg string), onProgress func(downloaded, total int64)) error {
		ma, matchedVariant := findModelAssetOrVariant(cat, name)
		if ma == nil {
			return fmt.Errorf("model %q not found in catalog\navailable: %s", name, catalogModelNames(cat))
		}
		destPath := filepath.Join(dataDir, "models", ma.Metadata.Name)
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		resolvedVariant := matchedVariant
		engineType := ""
		if resolvedVariant == nil {
			_, resolvedVariant, engineType, _ = cat.ResolveVariantForPull(ma.Metadata.Name, hwInfo)
		} else {
			engineType = resolvedVariant.Engine
		}
		requiredFormat := ""
		requiredQuantization := ""
		if resolvedVariant != nil {
			requiredFormat = resolvedVariant.Format
			requiredQuantization = variantQuantizationHint(resolvedVariant)
		}
		if engineType != "" {
			variantName := ""
			if resolvedVariant != nil {
				variantName = resolvedVariant.Name
			}
			slog.Info("model pull: inferred format", "engine", engineType, "format", requiredFormat, "variant", variantName)
		}

		localCandidates := make([]string, 0, 4)
		if matchedVariant != nil && matchedVariant.Source != nil && matchedVariant.Source.Type == "local_path" && matchedVariant.Source.Path != "" {
			localCandidates = append(localCandidates, matchedVariant.Source.Path)
		}
		if resolvedVariant != nil && resolvedVariant.Source != nil && resolvedVariant.Source.Type == "local_path" && resolvedVariant.Source.Path != "" {
			localCandidates = append(localCandidates, resolvedVariant.Source.Path)
		}
		for _, s := range ma.Storage.Sources {
			if s.Type == "local_path" && s.Path != "" {
				localCandidates = append(localCandidates, s.Path)
			}
		}
		if dbModel, err := db.FindModelByName(ctx, ma.Metadata.Name); err == nil && dbModel.Path != "" {
			localCandidates = append(localCandidates, dbModel.Path)
		}
		if alt := findModelDir(ma.Metadata.Name, dataDir, requiredFormat, requiredQuantization); alt != "" {
			localCandidates = append(localCandidates, alt)
		}
		localCandidates = append(localCandidates, destPath)
		for _, candidate := range localCandidates {
			if candidate == "" || !model.PathLooksCompatible(candidate, requiredFormat, requiredQuantization) {
				continue
			}
			slog.Info("model already available locally", "model", ma.Metadata.Name, "path", candidate, "format", requiredFormat)
			if err := registerExistingModel(ctx, candidate, db); err != nil {
				slog.Warn("register existing model failed", "path", candidate, "error", err)
			}
			if onStatus != nil {
				onStatus("complete", "model already available locally")
			}
			return nil
		}

		if resolvedVariant != nil && resolvedVariant.Source != nil && resolvedVariant.Source.Type != "local_path" {
			slog.Info("model pull: using variant source", "variant", resolvedVariant.Name, "repo", resolvedVariant.Source.Repo)
			if onStatus != nil {
				onStatus("downloading", "Downloading "+resolvedVariant.Name+"...")
			}
			sources := []model.Source{{
				Type:         resolvedVariant.Source.Type,
				Repo:         resolvedVariant.Source.Repo,
				Path:         resolvedVariant.Source.Path,
				Format:       resolvedVariant.Source.Format,
				Quantization: resolvedVariant.Source.Quantization,
			}}
			if err := model.DownloadFromSource(ctx, sources, destPath, model.DownloadPlan{
				Format:       requiredFormat,
				Quantization: requiredQuantization,
				OnProgress:   onProgress,
			}); err != nil {
				return fmt.Errorf("download model %s: %w", name, err)
			}
			return registerPulledModel(ctx, destPath, dataDir, db)
		}

		exactQuantSources := make([]model.Source, 0)
		fallbackSources := make([]model.Source, 0)
		var sources []model.Source
		for _, s := range ma.Storage.Sources {
			if s.Type == "local_path" {
				continue
			}
			if requiredFormat != "" && s.Format != "" && s.Format != requiredFormat {
				continue
			}
			src := model.Source{
				Type:         s.Type,
				Repo:         s.Repo,
				Path:         s.Path,
				Format:       s.Format,
				Quantization: s.Quantization,
			}
			if requiredQuantization != "" && strings.EqualFold(s.Quantization, requiredQuantization) {
				exactQuantSources = append(exactQuantSources, src)
				continue
			}
			if requiredQuantization != "" && s.Quantization != "" {
				continue
			}
			fallbackSources = append(fallbackSources, src)
		}
		if len(exactQuantSources) > 0 {
			sources = append(sources, exactQuantSources...)
		} else {
			sources = append(sources, fallbackSources...)
		}
		if len(sources) == 0 {
			return fmt.Errorf("no download source for model %q with format %q quantization %q", name, requiredFormat, requiredQuantization)
		}
		if onStatus != nil {
			onStatus("downloading", "Downloading "+name+"...")
		}
		if err := model.DownloadFromSource(ctx, sources, destPath, model.DownloadPlan{
			Format:       requiredFormat,
			Quantization: requiredQuantization,
			OnProgress:   onProgress,
		}); err != nil {
			return fmt.Errorf("download model %s: %w", name, err)
		}
		return registerPulledModel(ctx, destPath, dataDir, db)
	}

	// deployRunCore orchestrates the full run workflow: resolve → pull → deploy → wait.
	// Business logic lives here so CLI remains a thin presentation layer.
	var deps *mcp.ToolDeps
	deployRunCore := func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool,
		onPhase func(phase, msg string), onEngineProgress func(engine.ProgressEvent)) (json.RawMessage, error) {

		notify := func(phase, msg string) {
			if onPhase != nil {
				onPhase(phase, msg)
			}
		}

		waitForDeployment := func(deployName, runtimeName, resolvedEngine string) (json.RawMessage, error) {
			notify("waiting", deployName)
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			timer := time.NewTimer(10 * time.Minute)
			defer timer.Stop()

			for {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return json.Marshal(map[string]any{
						"name": deployName, "status": "timeout",
						"message": "deployment started but not ready within 10 minutes",
					})
				case <-ticker.C:
					statusData, err := deps.DeployStatus(ctx, deployName)
					if err != nil {
						continue
					}
					var status struct {
						Phase           string `json:"phase"`
						Ready           bool   `json:"ready"`
						Address         string `json:"address"`
						Runtime         string `json:"runtime"`
						StartupPhase    string `json:"startup_phase"`
						StartupProgress int    `json:"startup_progress"`
						StartupMessage  string `json:"startup_message"`
						ErrorLines      string `json:"error_lines,omitempty"`
						Message         string `json:"message,omitempty"`
					}
					if err := json.Unmarshal(statusData, &status); err != nil {
						continue
					}
					if status.Ready {
						notify("ready", status.Address)
						if status.Runtime != "" {
							runtimeName = status.Runtime
						}
						return json.Marshal(map[string]any{
							"name": deployName, "model": model, "engine": resolvedEngine,
							"runtime": runtimeName, "address": status.Address, "status": "ready",
						})
					}
					if status.Phase == "failed" {
						msg := refineDeploymentFailure(ctx, deployName, deploymentFailureDetails{
							Message:        status.Message,
							StartupMessage: status.StartupMessage,
							ErrorLines:     status.ErrorLines,
						}, deps.DeployStatus, deps.DeployLogs)
						return nil, fmt.Errorf("deployment failed: %s", msg)
					}
					phase := status.StartupPhase
					if phase == "" {
						phase = status.Phase
					}
					if status.StartupProgress > 0 {
						phase = fmt.Sprintf("%s %d%%", phase, status.StartupProgress)
					}
					notify("startup", phase)
				}
			}
		}

		// Step 1: Resolve via dry-run
		notify("resolving", model)
		dryRunData, err := deps.DeployDryRun(ctx, engineType, model, slot, configOverrides)
		if err != nil {
			return nil, fmt.Errorf("resolve: %w", err)
		}
		var plan struct {
			Engine    string `json:"engine"`
			Runtime   string `json:"runtime"`
			FitReport struct {
				Fit    bool     `json:"fit"`
				Reason string   `json:"reason"`
				Warns  []string `json:"warnings"`
			} `json:"fit_report"`
		}
		if err := json.Unmarshal(dryRunData, &plan); err != nil {
			return nil, fmt.Errorf("parse resolve result: %w", err)
		}
		if !plan.FitReport.Fit {
			return nil, fmt.Errorf("hardware not compatible: %s", plan.FitReport.Reason)
		}
		notify("resolved", fmt.Sprintf("engine=%s runtime=%s", plan.Engine, plan.Runtime))
		for _, warn := range plan.FitReport.Warns {
			notify("warning", warn)
		}
		deployName := knowledge.SanitizePodName(model + "-" + plan.Engine)
		if statusData, statusErr := deps.DeployStatus(ctx, deployName); statusErr == nil {
			var status struct {
				Phase   string `json:"phase"`
				Ready   bool   `json:"ready"`
				Address string `json:"address"`
				Runtime string `json:"runtime"`
			}
			if err := json.Unmarshal(statusData, &status); err == nil {
				switch {
				case status.Ready:
					notify("ready", status.Address)
					runtimeName := plan.Runtime
					if status.Runtime != "" {
						runtimeName = status.Runtime
					}
					return json.Marshal(map[string]any{
						"name": deployName, "model": model, "engine": plan.Engine,
						"runtime": runtimeName, "address": status.Address, "status": "ready",
					})
				case status.Phase == "running" || status.Phase == "starting":
					notify("reusing", deployName)
					runtimeName := plan.Runtime
					if status.Runtime != "" {
						runtimeName = status.Runtime
					}
					return waitForDeployment(deployName, runtimeName, plan.Engine)
				}
			}
		}

		// Step 2: Pull engine
		if !noPull {
			notify("pulling_engine", plan.Engine)
			if err := deps.PullEngine(ctx, plan.Engine, onEngineProgress); err != nil {
				return nil, fmt.Errorf("pull engine: %w", err)
			}
		}

		// Step 3: Pull model (non-fatal — may be local or pre-installed)
		if !noPull {
			notify("pulling_model", model)
			if err := deps.PullModel(ctx, model); err != nil {
				notify("model_skip", err.Error())
			}
		}

		// Step 4: Deploy
		notify("deploying", model)
		deployCtx := ctx
		if noPull {
			deployCtx = withDeployAutoPull(ctx, false)
		}
		deployData, err := deps.DeployApply(deployCtx, engineType, model, slot, configOverrides)
		if err != nil {
			return nil, fmt.Errorf("deploy: %w", err)
		}
		var deployResult struct {
			Name    string `json:"name"`
			Runtime string `json:"runtime"`
		}
		if err := json.Unmarshal(deployData, &deployResult); err != nil || deployResult.Name == "" {
			return deployData, nil
		}
		return waitForDeployment(deployResult.Name, deployResult.Runtime, plan.Engine)
	}

	deps = &mcp.ToolDeps{}

	// Wire all domain-specific tool dependencies.
	buildSystemDeps(ac, deps)
	buildEngineDeps(ac, deps, scanEnginesCore, dlTracker)
	buildModelDeps(ac, deps, pullModelCore, dlTracker)
	buildDeployDeps(ac, deps, pullModelCore, deployRunCore)
	buildKnowledgeDeps(ac, deps)
	buildBenchmarkDeps(ac, deps, resolveEndpoint)

	return deps
}
