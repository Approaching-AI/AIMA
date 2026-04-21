package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/cli"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/fleet"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/model"
	"github.com/jguan/aima/internal/onboarding"
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

func warmupInferenceReady(ctx context.Context, address, model string, cfg knowledge.WarmupConfig) bool {
	if !cfg.Enabled {
		return true
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return false
	}
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		address = "http://" + address
	}
	url := strings.TrimRight(address, "/") + "/v1/chat/completions"
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = "Hello"
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1
	}
	timeout := time.Duration(cfg.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"max_tokens":%d}`, model, prompt, maxTokens)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
	goAgent := agent.NewAgent(llmClient, toolAdapter, agent.WithSessions(sessionStore), agent.WithProfile("operator"))
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
			result, err := deps.DeployApply(ctx, engineType, modelName, "", nil, false)
			if err != nil {
				return nil, fmt.Errorf("redeploy %s: %w", modelName, err)
			}
			return result, nil
		default:
			return nil, fmt.Errorf("unknown resource type %q", snap.ResourceType)
		}
	}

	deps.SupportAskForHelp = supportSvc.AskForHelpJSON
	wireDeviceDeps(deps, supportSvc)
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

		// Onboarding wizard API endpoints
		mux.HandleFunc("GET /ui/api/onboarding-status", func(w http.ResponseWriter, r *http.Request) {
			if !requireOnboardingRead(ac, w, r) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			data, err := buildOnboardingStatusJSON(r.Context(), ac, deps)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_, _ = w.Write(data)
		})
		mux.HandleFunc("POST /ui/api/onboarding-scan", handleOnboardingScan(ac, deps))
		mux.HandleFunc("POST /ui/api/onboarding-init", handleOnboardingInit(ac, deps))
		mux.HandleFunc("POST /ui/api/onboarding-recommend", func(w http.ResponseWriter, r *http.Request) {
			if !requireOnboardingMutation(ac, w, r) {
				return
			}
			locale := ""
			if r.Body != nil {
				body, _ := io.ReadAll(io.LimitReader(r.Body, 4*1024))
				if len(body) > 0 {
					var req struct {
						Locale string `json:"locale"`
					}
					if json.Unmarshal(body, &req) == nil {
						locale = strings.TrimSpace(req.Locale)
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			data, err := buildModelRecommendations(r.Context(), ac, deps, locale)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_, _ = w.Write(data)
		})
		mux.HandleFunc("POST /ui/api/onboarding-deploy", handleOnboardingDeploy(ac, deps))
		mux.HandleFunc("POST /ui/api/onboarding-stop-container", handleStopContainer(ac))

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
	tuner := agent.NewTuner(automationTools, agent.WithTuningSink(func(ctx context.Context, session *agent.TuningSession) {
		if session == nil {
			return
		}
		bestConfigJSON := ""
		if len(session.BestConfig) > 0 {
			if b, err := json.Marshal(session.BestConfig); err == nil {
				bestConfigJSON = string(b)
			}
		}
		resultsJSON := ""
		if len(session.Results) > 0 {
			if b, err := json.Marshal(session.Results); err == nil {
				resultsJSON = string(b)
			}
		}
		var completedAt *time.Time
		if !session.CompletedAt.IsZero() {
			t := session.CompletedAt
			completedAt = &t
		}
		if err := db.UpsertTuningSession(ctx, session.ID, session.Config.Model, session.Config.Engine, session.Status, session.Progress, session.Total, bestConfigJSON, resultsJSON, session.BestScore, completedAt); err != nil {
			slog.Warn("tuning: persist session failed", "id", session.ID, "error", err)
		}
	}))
	explorationMgr = agent.NewExplorationManager(db, tuner, automationTools)
	eventBus := agent.NewEventBus()
	ac.eventBus = eventBus
	patrol = agent.NewPatrol(agent.DefaultPatrolConfig(), toolAdapter, db.InsertPatrolAlert,
		agent.WithHealer(healer),
		agent.WithActionCallback(func(ctx context.Context, a agent.PatrolAction) {
			slog.Info("patrol_action_audit",
				"alert_id", a.AlertID, "type", a.Type,
				"success", a.Success, "detail", a.Detail)
		}),
		agent.WithEventBus(eventBus),
	)

	// 9h. Explorer subsystem (v0.4) with advisory feedback bridge
	explorerOpts := []agent.ExplorerOption{}
	if roundsStr, err := db.GetConfig(ctx, explorerConfigStorageKey("rounds_used")); err == nil && roundsStr != "" {
		if n, parseErr := strconv.Atoi(roundsStr); parseErr == nil && n > 0 {
			explorerOpts = append(explorerOpts, agent.WithRoundsUsed(n))
		}
	}
	explorerOpts = append(explorerOpts,
		agent.WithGatherHardware(func(ctx context.Context) (agent.HardwareInfo, error) {
			hw := buildHardwareInfo(ctx, cat, rt.Name())
			return agent.HardwareInfo{
				Profile:  hw.HardwareProfile,
				GPUArch:  hw.GPUArch,
				GPUCount: hw.GPUCount,
				VRAMMiB:  hw.GPUVRAMMiB,
			}, nil
		}),
		agent.WithGatherGaps(func(ctx context.Context) ([]agent.GapEntry, error) {
			hw := buildHardwareInfo(ctx, cat, rt.Name())
			if hw.HardwareProfile == "" {
				return nil, nil
			}
			gaps, err := knowledgeStore.Gaps(ctx, knowledge.GapsParams{
				Hardware:      hw.HardwareProfile,
				MinBenchmarks: 3,
			})
			if err != nil {
				return nil, err
			}
			entries := make([]agent.GapEntry, 0, len(gaps))
			for _, gap := range gaps {
				entries = append(entries, agent.GapEntry{
					Model:          gap.ModelID,
					Engine:         gap.EngineType,
					Hardware:       gap.HardwareID,
					BenchmarkCount: gap.BenchmarkCount,
				})
			}
			return entries, nil
		}),
		agent.WithGatherDeploys(func(ctx context.Context) ([]agent.DeployStatus, error) {
			if deps.DeployList == nil {
				return nil, nil
			}
			data, err := deps.DeployList(ctx)
			if err != nil {
				return nil, err
			}
			var deployments []runtime.DeploymentStatus
			if err := json.Unmarshal(data, &deployments); err != nil {
				return nil, fmt.Errorf("parse deploy list for explorer: %w", err)
			}
			statuses := make([]agent.DeployStatus, 0, len(deployments))
			for _, deployment := range deployments {
				modelName := firstNonEmpty(deployment.Model, deployment.Labels["aima.dev/model"])
				engineType := firstNonEmpty(deployment.Engine, deployment.Labels["aima.dev/engine"])
				if modelName == "" || engineType == "" {
					continue
				}
				statuses = append(statuses, agent.DeployStatus{
					Model:  modelName,
					Engine: engineType,
					Status: deployment.Phase,
				})
			}
			return statuses, nil
		}),
		agent.WithCleanupDeploys(func(ctx context.Context) (int, error) {
			if deps.DeployList == nil || deps.DeployDelete == nil {
				return 0, nil
			}
			data, err := deps.DeployList(ctx)
			if err != nil {
				return 0, err
			}
			var deployments []runtime.DeploymentStatus
			if err := json.Unmarshal(data, &deployments); err != nil {
				return 0, err
			}
			cleaned := 0
			for _, d := range deployments {
				name := d.Name
				if name == "" {
					continue
				}
				slog.Info("explorer: pre-cycle cleanup stopping deployment", "name", name)
				if err := deps.DeployDelete(ctx, name); err != nil {
					slog.Warn("explorer: pre-cycle cleanup failed for deployment", "name", name, "error", err)
					continue
				}
				cleaned++
			}
			return cleaned, nil
		}),
		agent.WithCleanupModelDeploy(func(ctx context.Context, name string) error {
			if deps.DeployDelete == nil || name == "" {
				return nil
			}
			return deps.DeployDelete(ctx, name)
		}),
		agent.WithGatherOpenQuestions(func(ctx context.Context) ([]agent.OpenQuestion, error) {
			rows, err := db.ListOpenQuestions(ctx, "untested")
			if err != nil {
				return nil, err
			}
			questions := make([]agent.OpenQuestion, 0, len(rows))
			for _, row := range rows {
				modelName := firstNonEmpty(stringField(row, "model"), stringField(row, "target_model"))
				engineType := firstNonEmpty(stringField(row, "engine"), stringField(row, "target_engine"))
				if modelName == "" {
					continue
				}
				questions = append(questions, agent.OpenQuestion{
					ID:       stringField(row, "id"),
					Hardware: stringField(row, "hardware"),
					Model:    modelName,
					Engine:   engineType,
					Question: stringField(row, "question"),
					Status:   firstNonEmpty(stringField(row, "status"), "untested"),
				})
			}
			return questions, nil
		}),
		agent.WithGatherAdvisories(func(ctx context.Context) ([]agent.Advisory, error) {
			if deps.SyncPullAdvisories == nil {
				return nil, nil
			}
			data, err := deps.SyncPullAdvisories(ctx)
			if err != nil {
				return nil, err
			}
			var items []map[string]any
			if err := json.Unmarshal(data, &items); err != nil {
				return nil, nil // no advisories or unparseable
			}
			advisories := make([]agent.Advisory, 0, len(items))
			for _, item := range items {
				adv := agent.Advisory{
					ID:             stringField(item, "id"),
					Type:           stringField(item, "type"),
					TargetHardware: stringField(item, "target_hardware"),
					TargetModel:    stringField(item, "target_model"),
					TargetEngine:   stringField(item, "target_engine"),
					Confidence:     stringField(item, "confidence"),
					Reasoning:      stringField(item, "reasoning"),
				}
				if cfg, ok := item["config"].(map[string]any); ok {
					adv.Config = cfg
				} else if content, ok := item["content"].(map[string]any); ok {
					adv.Config = content
				}
				if adv.ID != "" && adv.TargetModel != "" {
					advisories = append(advisories, adv)
				}
			}
			return advisories, nil
		}),
		agent.WithExplorerSaveNote(func(ctx context.Context, title, content, hardware, model, engine string) error {
			return db.InsertNote(ctx, &state.KnowledgeNote{
				Title:           title,
				HardwareProfile: hardware,
				Model:           model,
				Engine:          engine,
				Content:         content,
				Confidence:      "medium",
			})
		}),
		agent.WithExplorerSyncPush(func(ctx context.Context) error {
			if deps.SyncPush == nil {
				return nil
			}
			_, err := deps.SyncPush(ctx)
			return err
		}),
		agent.WithAdvisoryFeedback(func(ctx context.Context, advisoryID, status, reason string) error {
			if deps.AdvisoryFeedback == nil {
				return nil
			}
			_, err := deps.AdvisoryFeedback(ctx, advisoryID, status, reason)
			return err
		}),
		agent.WithGatherLocalModels(func(ctx context.Context) ([]agent.LocalModel, error) {
			if deps.ScanModels == nil {
				return nil, nil
			}
			data, err := deps.ScanModels(ctx)
			if err != nil {
				return nil, err
			}
			var models []struct {
				Name      string `json:"name"`
				Format    string `json:"format"`
				Type      string `json:"type"`
				SizeBytes int64  `json:"size_bytes"`
			}
			if err := json.Unmarshal(data, &models); err != nil {
				return nil, nil
			}
			result := make([]agent.LocalModel, 0, len(models))
			for _, m := range models {
				if m.Name != "" {
					lm := enrichExplorerLocalModel(cat, agent.LocalModel{
						Name:          m.Name,
						Format:        m.Format,
						Type:          m.Type,
						SizeBytes:     m.SizeBytes,
						MaxContextLen: cat.ModelMaxContextLen(m.Name),
					})
					result = append(result, lm)
				}
			}
			return result, nil
		}),
		agent.WithGatherLocalEngines(func(ctx context.Context) ([]agent.LocalEngine, error) {
			return gatherExplorerLocalEngines(ctx, cat, db, rt, nativeRt, dockerRt, k3sRt, dataDir)
		}),
		agent.WithGatherComboFacts(func(ctx context.Context, hardware agent.HardwareInfo, models []agent.LocalModel, engines []agent.LocalEngine) ([]agent.ComboFact, error) {
			return gatherExplorerComboFacts(ctx, cat, db, knowledgeStore, rt, nativeRt, dockerRt, k3sRt, dataDir, hardware, models, engines)
		}),
		agent.WithExplorerQueryFunc(func(qType string, filter map[string]any, limit int) (string, error) {
			filterJSON, _ := json.Marshal(filter)
			if limit <= 0 {
				limit = 10
			}
			var result any
			var err error
			switch qType {
			case "search":
				var p knowledge.SearchParams
				_ = json.Unmarshal(filterJSON, &p)
				if p.Limit == 0 {
					p.Limit = limit
				}
				result, err = knowledgeStore.Search(ctx, p)
			case "compare":
				var p knowledge.CompareParams
				_ = json.Unmarshal(filterJSON, &p)
				result, err = knowledgeStore.Compare(ctx, p)
			case "gaps":
				var p knowledge.GapsParams
				_ = json.Unmarshal(filterJSON, &p)
				result, err = knowledgeStore.Gaps(ctx, p)
			case "aggregate":
				var p knowledge.AggregateParams
				_ = json.Unmarshal(filterJSON, &p)
				result, err = knowledgeStore.Aggregate(ctx, p)
			default:
				return "", fmt.Errorf("unknown query type: %s (supported: search, compare, gaps, aggregate)", qType)
			}
			if err != nil {
				return "", err
			}
			out, _ := json.MarshalIndent(result, "", "  ")
			return string(out), nil
		}),
		agent.WithBenchmarkProfiles(func(totalVRAMMiB int) []agent.ExplorationBenchmarkProfile {
			catalogProfiles := cat.BenchmarkProfilesForVRAM(totalVRAMMiB)
			if len(catalogProfiles) == 0 {
				return nil // fall back to Go defaults
			}
			profiles := make([]agent.ExplorationBenchmarkProfile, len(catalogProfiles))
			for i, cp := range catalogProfiles {
				profiles[i] = agent.ExplorationBenchmarkProfile{
					Label:             cp.Label,
					ConcurrencyLevels: cp.ConcurrencyLevels,
					InputTokenLevels:  cp.InputTokenLevels,
					MaxTokenLevels:    cp.MaxTokenLevels,
					RequestsPerCombo:  cp.RequestsPerCombo,
					Rounds:            cp.Rounds,
				}
			}
			return profiles
		}),
	)
	explorerConfig := loadExplorerConfig(ctx, db)
	if explorerConfig.WorkspaceDir == "" {
		explorerConfig.WorkspaceDir = filepath.Join(dataDir, "explorer")
	}
	explorer := agent.NewExplorer(explorerConfig, goAgent, explorationMgr, db, eventBus, explorerOpts...)
	go explorer.Start(context.Background())

	// Wire explorer MCP tools
	deps.ExplorerStatus = func(ctx context.Context) (json.RawMessage, error) {
		return json.Marshal(explorer.Status())
	}
	deps.ExplorerConfig = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Action string `json:"action"`
			Key    string `json:"key"`
			Value  string `json:"value"`
		}
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse explorer config params: %w", err)
			}
		}
		if p.Action == "get" || p.Action == "" {
			return json.Marshal(explorerConfigResponse(explorer))
		}
		if p.Action != "set" {
			return nil, fmt.Errorf("unsupported explorer config action %q", p.Action)
		}
		normalized, err := explorer.UpdateConfig(p.Key, p.Value)
		if err != nil {
			return nil, err
		}
		if err := db.SetConfig(ctx, explorerConfigStorageKey(p.Key), normalized); err != nil {
			return nil, err
		}
		return json.Marshal(explorerConfigResponse(explorer))
	}
	deps.ExplorerTrigger = func(ctx context.Context) (json.RawMessage, error) {
		explorer.Trigger()
		return json.Marshal(map[string]string{"status": "triggered"})
	}
	deps.ExplorerCleanup = func(ctx context.Context) (json.RawMessage, error) {
		cleaned, err := explorer.CleanupDeploys(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"status":  "cleaned",
			"stopped": cleaned,
		})
	}
	deps.ExplorerDbDeltas = func(ctx context.Context, sinceISO string) (json.RawMessage, error) {
		var since time.Time
		if sinceISO != "" {
			t, err := time.Parse(time.RFC3339, sinceISO)
			if err != nil {
				return nil, fmt.Errorf("parse since=%q as RFC3339: %w", sinceISO, err)
			}
			since = t
		}
		counts, err := db.ExplorationDbDeltas(ctx, since)
		if err != nil {
			return nil, err
		}
		resp := map[string]any{
			"configurations":     counts["configurations"],
			"benchmark_results":  counts["benchmark_results"],
			"exploration_events": counts["exploration_events"],
		}
		if !since.IsZero() {
			resp["since"] = since.UTC().Format(time.RFC3339)
		}
		return json.Marshal(resp)
	}
	// Read-only access to the Explorer workspace. The same directory is reused
	// across runs (see explorer.setupPlannerLocked), so responses reflect the
	// active Explorer session rather than any specific run_id.
	allowedWorkspaceDocs := map[string]bool{
		"plan.md":             true,
		"summary.md":          true,
		"device-profile.md":   true,
		"available-combos.md": true,
		"knowledge-base.md":   true,
		"experiment-facts.md": true,
		"index.md":            true,
	}
	deps.ExploreWorkspaceDoc = func(ctx context.Context, doc string) (json.RawMessage, error) {
		if !allowedWorkspaceDocs[doc] {
			return nil, fmt.Errorf("doc %q is not in the workspace whitelist", doc)
		}
		path := filepath.Join(explorerConfig.WorkspaceDir, doc)
		info, statErr := os.Stat(path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				return json.Marshal(map[string]any{
					"doc":    doc,
					"exists": false,
					"path":   path,
				})
			}
			return nil, fmt.Errorf("stat workspace doc: %w", statErr)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read workspace doc: %w", err)
		}
		return json.Marshal(map[string]any{
			"doc":     doc,
			"exists":  true,
			"path":    path,
			"mtime":   info.ModTime().UTC().Format(time.RFC3339),
			"size":    info.Size(),
			"content": string(data),
		})
	}

	// Wire agent, patrol, tuning, exploration, and open questions tools.
	buildAgentDeps(ac, deps, patrol, tuner, explorationMgr)

	// 9i. Register all tools (after all deps are fully wired)
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

func loadExplorerConfig(ctx context.Context, db *state.DB) agent.ExplorerConfig {
	config := agent.ExplorerConfig{
		Schedule: agent.DefaultScheduleConfig(),
		Enabled:  true,
		// DC-5: a single plan cycle on a hot workspace can burn 300K+ tokens at
		// reasoning-model prices. Default to 2M/day — about 5 cycles — so an
		// unattended multi-cycle scheduler doesn't blow through quota. Users
		// can raise this explicitly via `automation.patrol config set`.
		MaxTokensPerDay: 2_000_000,
	}
	if db == nil {
		return config
	}
	for _, key := range []string{
		"gap_scan_interval",
		"sync_interval",
		"full_audit_interval",
		"quiet_start",
		"quiet_end",
		"max_concurrent_runs",
		"enabled",
		"mode",
		"max_rounds",
		"max_tokens_per_day",
		"max_cycles",
		"max_tasks",
	} {
		value, err := db.GetConfig(ctx, explorerConfigStorageKey(key))
		if err != nil || strings.TrimSpace(value) == "" {
			continue
		}
		switch key {
		case "gap_scan_interval":
			if duration, parseErr := time.ParseDuration(value); parseErr == nil {
				config.Schedule.GapScanInterval = duration
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "sync_interval":
			if duration, parseErr := time.ParseDuration(value); parseErr == nil {
				config.Schedule.SyncInterval = duration
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "full_audit_interval":
			if duration, parseErr := time.ParseDuration(value); parseErr == nil {
				config.Schedule.FullAuditInterval = duration
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "quiet_start":
			if hour, parseErr := strconv.Atoi(value); parseErr == nil {
				config.Schedule.QuietStart = hour
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "quiet_end":
			if hour, parseErr := strconv.Atoi(value); parseErr == nil {
				config.Schedule.QuietEnd = hour
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "max_concurrent_runs":
			if maxRuns, parseErr := strconv.Atoi(value); parseErr == nil {
				config.Schedule.MaxConcurrentRuns = maxRuns
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "enabled":
			if enabled, parseErr := strconv.ParseBool(value); parseErr == nil {
				config.Enabled = enabled
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "mode":
			switch value {
			case "continuous", "once", "budget":
				config.Mode = value
			default:
				slog.Warn("ignore invalid explorer config", "key", key, "value", value)
			}
		case "max_rounds":
			if n, parseErr := strconv.Atoi(value); parseErr == nil {
				config.MaxRounds = n
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "max_tokens_per_day":
			if n, parseErr := strconv.Atoi(value); parseErr == nil {
				config.MaxTokensPerDay = n
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "max_cycles":
			if n, parseErr := strconv.Atoi(value); parseErr == nil {
				config.MaxCycles = n
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		case "max_tasks":
			if n, parseErr := strconv.Atoi(value); parseErr == nil {
				config.MaxTasks = n
			} else {
				slog.Warn("ignore invalid explorer config", "key", key, "value", value, "error", parseErr)
			}
		}
	}
	return config
}

func explorerConfigResponse(explorer *agent.Explorer) map[string]any {
	status := explorer.Status()
	config := status.Schedule
	return map[string]any{
		"enabled":             status.Enabled,
		"gap_scan_interval":   config.GapScanInterval.String(),
		"sync_interval":       config.SyncInterval.String(),
		"full_audit_interval": config.FullAuditInterval.String(),
		"quiet_start":         config.QuietStart,
		"quiet_end":           config.QuietEnd,
		"max_concurrent_runs": config.MaxConcurrentRuns,
		"mode":                status.Mode,
		"max_rounds":          status.MaxRounds,
		"rounds_used":         status.RoundsUsed,
		"max_tokens_per_day":  status.MaxTokensPerDay,
		"tokens_used_today":   status.TokensUsedToday,
		"max_cycles":          status.MaxCycles,
		"max_tasks":           status.MaxTasks,
	}
}

func explorerConfigStorageKey(key string) string {
	return "explorer." + key
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
		onPhase func(phase, msg string), onEngineProgress func(engine.ProgressEvent), onModelProgress func(downloaded, total int64)) (json.RawMessage, error) {

		notify := func(phase, msg string) {
			if onPhase != nil {
				onPhase(phase, msg)
			}
		}

		waitForDeployment := func(deployName, runtimeName, resolvedEngine string, resolvedConfig map[string]any, warmup knowledge.WarmupConfig, deployTimeout time.Duration) (json.RawMessage, error) {
			notify("waiting", deployName)
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			// deployTimeout caps how long we wait for /health and optional warmup.
			// Callers pass engine.Startup.HealthCheck.TimeoutS when the engine
			// specifies one (vllm-nightly sets 600s to cover the transformers
			// pip-install on first boot); default to 10m otherwise.
			if deployTimeout <= 0 {
				deployTimeout = 10 * time.Minute
			}
			timer := time.NewTimer(deployTimeout)
			defer timer.Stop()

			for {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return json.Marshal(map[string]any{
						"name": deployName, "status": "timeout",
						"message": fmt.Sprintf("deployment started but not ready within %s", deployTimeout),
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
						if warmup.Enabled && !warmupInferenceReady(ctx, status.Address, resolvedServedModelName(model, resolvedConfig), warmup) {
							notify("startup", "warmup")
							continue
						}
						notify("ready", status.Address)
						if status.Runtime != "" {
							runtimeName = status.Runtime
						}
						return json.Marshal(map[string]any{
							"name": deployName, "model": model, "engine": resolvedEngine,
							"runtime": runtimeName, "address": status.Address, "status": "ready",
							"config": resolvedConfig,
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
		resolveMsg := fmt.Sprintf("resolving engine for %s", model)
		if engineType != "" {
			resolveMsg = fmt.Sprintf("checking engine %s", engineType)
		}
		notify("resolving", resolveMsg)
		dryRunData, err := deps.DeployDryRun(ctx, engineType, model, slot, configOverrides)
		if err != nil {
			return nil, fmt.Errorf("resolve: %w", err)
		}
		var plan struct {
			Engine    string         `json:"engine"`
			Runtime   string         `json:"runtime"`
			Config    map[string]any `json:"config"`
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
					notify("reusing", deployName)
					notify("ready", status.Address)
					runtimeName := plan.Runtime
					if status.Runtime != "" {
						runtimeName = status.Runtime
					}
					return json.Marshal(map[string]any{
						"name": deployName, "model": model, "engine": plan.Engine,
						"runtime": runtimeName, "address": status.Address, "status": "ready",
						"config": plan.Config,
					})
				case status.Phase == "running" || status.Phase == "starting":
					notify("reusing", deployName)
					runtimeName := plan.Runtime
					if status.Runtime != "" {
						runtimeName = status.Runtime
					}
					hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
					warmup := knowledge.WarmupConfig{}
					deployTimeout := time.Duration(0)
					if asset := cat.FindEngineByName(plan.Engine, hwInfo); asset != nil {
						warmup = asset.Startup.Warmup
						if t := asset.Startup.HealthCheck.TimeoutS; t > 0 {
							deployTimeout = time.Duration(t) * time.Second
						}
					}
					return waitForDeployment(deployName, runtimeName, plan.Engine, plan.Config, warmup, deployTimeout)
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

		// Step 3: Pull model (non-fatal — may be local or pre-installed). Use
		// pullModelCore directly so byte-level progress can flow back to the
		// caller via onModelProgress (the deps.PullModel closure swallows it).
		if !noPull {
			notify("pulling_model", model)
			modelStatus := func(phase, msg string) {
				if msg != "" {
					notify("pulling_model", msg)
				}
			}
			if err := pullModelCore(ctx, model, modelStatus, onModelProgress); err != nil {
				notify("model_skip", err.Error())
			}
		}

		// Step 4: Deploy
		notify("deploying", model)
		deployData, err := deps.DeployApply(ctx, engineType, model, slot, configOverrides, noPull)
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
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		warmup := knowledge.WarmupConfig{}
		deployTimeout := time.Duration(0)
		if asset := cat.FindEngineByName(plan.Engine, hwInfo); asset != nil {
			warmup = asset.Startup.Warmup
			if t := asset.Startup.HealthCheck.TimeoutS; t > 0 {
				deployTimeout = time.Duration(t) * time.Second
			}
		}
		return waitForDeployment(deployResult.Name, deployResult.Runtime, plan.Engine, plan.Config, warmup, deployTimeout)
	}

	deps = &mcp.ToolDeps{}

	// Wire all domain-specific tool dependencies.
	buildSystemDeps(ac, deps)
	buildEngineDeps(ac, deps, scanEnginesCore, dlTracker)
	buildModelDeps(ac, deps, pullModelCore, dlTracker)
	buildDeployDeps(ac, deps, pullModelCore, deployRunCore)
	buildKnowledgeDeps(ac, deps)
	buildBenchmarkDeps(ac, deps, resolveEndpoint)

	// Onboarding (multi-action MCP tool). The 5 closures below wrap the
	// internal/onboarding package entry points; scan/init/deploy collect
	// Event slices into the response JSON so MCP callers receive the full
	// progress log in a single request-response cycle.
	deps.OnboardingStatus = func(ctx context.Context) (json.RawMessage, error) {
		obDeps := buildOnboardingDepsStruct(ac, deps)
		result, err := onboarding.BuildStatus(ctx, obDeps)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.OnboardingScan = func(ctx context.Context) (json.RawMessage, error) {
		obDeps := buildOnboardingDepsStruct(ac, deps)
		result, events, err := onboarding.RunScan(ctx, obDeps, nil)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"result": result, "events": events})
	}
	deps.OnboardingRecommend = func(ctx context.Context, locale string) (json.RawMessage, error) {
		obDeps := buildOnboardingDepsStruct(ac, deps)
		if locale == "" {
			locale = "en"
		}
		result, err := onboarding.Recommend(ctx, obDeps, locale)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.OnboardingInit = func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error) {
		obDeps := buildOnboardingDepsStruct(ac, deps)
		if tier == "" {
			tier = "auto"
		}
		result, events, err := onboarding.RunInit(ctx, obDeps, tier, allowDownload, nil)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"result": result, "events": events})
	}
	deps.OnboardingDeploy = func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool) (json.RawMessage, error) {
		obDeps := buildOnboardingDepsStruct(ac, deps)
		result, events, err := onboarding.RunDeploy(ctx, obDeps, model, engineType, slot, configOverrides, noPull, nil)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"result": result, "events": events})
	}

	return deps
}
