package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/catalog"
	"github.com/jguan/aima/internal/agent"
	benchpkg "github.com/jguan/aima/internal/benchmark"
	"github.com/jguan/aima/internal/buildinfo"
	"github.com/jguan/aima/internal/cli"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/fleet"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/model"
	"github.com/jguan/aima/internal/openclaw"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/runtime"
	"github.com/jguan/aima/internal/stack"
	"github.com/jguan/aima/internal/support"
	"github.com/jguan/aima/internal/ui"

	state "github.com/jguan/aima/internal"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "aima: %v\n", err)
		os.Exit(1)
	}
}

// isLightweightInvocation returns true if os.Args indicate a command that
// doesn't need the full init (DB, catalog, runtime selection).
// This avoids side effects (creating ~/.aima, opening DB) for --help, version, etc.
func isLightweightInvocation() bool {
	for _, a := range os.Args[1:] {
		switch a {
		case "-h", "--help", "help", "completion":
			return true
		case "version":
			return true
		}
	}
	// No subcommand at all → full init (auto-serve with browser open).
	return false
}

func isServeInvocation() bool {
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a == "serve"
	}
	return false
}

func defaultRootArgs(args []string) []string {
	if len(args) <= 1 {
		return []string{"serve"}
	}
	return nil
}

func registerUIRoutes(mux *http.ServeMux, cat *knowledge.Catalog, deps *ui.Deps) {
	if deps == nil {
		deps = &ui.Deps{}
	}
	deps.OnboardingManifest = func(ctx context.Context) (json.RawMessage, error) {
		_ = ctx
		raw, err := buildOnboardingManifestJSON(cat)
		if err != nil {
			return nil, fmt.Errorf("build ui onboarding manifest: %w", err)
		}
		return raw, nil
	}
	ui.RegisterRoutes(deps)(mux)
}

type scenarioDeployResult struct {
	Model  string          `json:"model"`
	Engine string          `json:"engine"`
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type orderedScenarioDeploy struct {
	deployment knowledge.ScenarioDeployment
	waitFor    string
	timeoutS   int
}

func applyScenario(ctx context.Context, cat *knowledge.Catalog, rtName string, deps *mcp.ToolDeps, name string, dryRun bool) (json.RawMessage, error) {
	var scenario *knowledge.DeploymentScenario
	for i := range cat.DeploymentScenarios {
		if cat.DeploymentScenarios[i].Metadata.Name == name {
			scenario = &cat.DeploymentScenarios[i]
			break
		}
	}
	if scenario == nil {
		names := make([]string, 0, len(cat.DeploymentScenarios))
		for _, ds := range cat.DeploymentScenarios {
			names = append(names, ds.Metadata.Name)
		}
		return nil, fmt.Errorf("scenario %q not found (available: %v)", name, names)
	}

	var results []scenarioDeployResult

	var hwWarning string
	if !dryRun && scenario.Target.HardwareProfile != "" {
		hwInfo := buildHardwareInfo(ctx, cat, rtName)
		if hwInfo.HardwareProfile != "" && hwInfo.HardwareProfile != scenario.Target.HardwareProfile {
			hwWarning = fmt.Sprintf("hardware mismatch: scenario targets %q but current device is %q",
				scenario.Target.HardwareProfile, hwInfo.HardwareProfile)
			slog.Warn(hwWarning)
		}
	}

	var ordered []orderedScenarioDeploy
	if len(scenario.StartupOrder) > 0 {
		byModel := make(map[string]knowledge.ScenarioDeployment, len(scenario.Deployments))
		for _, d := range scenario.Deployments {
			byModel[d.Model] = d
		}
		steps := make([]knowledge.ScenarioStartupStep, len(scenario.StartupOrder))
		copy(steps, scenario.StartupOrder)
		sort.Slice(steps, func(i, j int) bool { return steps[i].Step < steps[j].Step })
		for _, step := range steps {
			d, ok := byModel[step.Model]
			if !ok {
				results = append(results, scenarioDeployResult{
					Model:  step.Model,
					Status: "error",
					Error:  fmt.Sprintf("startup_order references unknown model %q", step.Model),
				})
				continue
			}
			ordered = append(ordered, orderedScenarioDeploy{
				deployment: d,
				waitFor:    step.WaitFor,
				timeoutS:   step.TimeoutS,
			})
			delete(byModel, step.Model)
		}
		for _, d := range scenario.Deployments {
			if _, remaining := byModel[d.Model]; remaining {
				ordered = append(ordered, orderedScenarioDeploy{deployment: d})
			}
		}
	} else {
		for _, d := range scenario.Deployments {
			ordered = append(ordered, orderedScenarioDeploy{deployment: d})
		}
	}

	blockFurther := false
	blockReason := ""
	for i, od := range ordered {
		d := od.deployment
		if blockFurther && !dryRun {
			results = append(results, scenarioDeployResult{
				Model:  d.Model,
				Engine: d.Engine,
				Status: "skipped",
				Error:  fmt.Sprintf("skipped after earlier deployment failure: %s", blockReason),
			})
			continue
		}
		if dryRun {
			if deps.DeployDryRun == nil {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Status: "error",
					Error:  "deploy.dry_run not available",
				})
				continue
			}
			data, err := deps.DeployDryRun(ctx, d.Engine, d.Model, d.Slot, d.Config)
			if err != nil {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Status: "error",
					Error:  err.Error(),
				})
			} else {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Status: "dry_run",
					Data:   data,
				})
			}
			continue
		}

		if deps.DeployApply == nil {
			blockFurther = true
			blockReason = "deploy.apply not available"
			results = append(results, scenarioDeployResult{
				Model:  d.Model,
				Engine: d.Engine,
				Status: "error",
				Error:  blockReason,
			})
			continue
		}
		data, err := deps.DeployApply(ctx, d.Engine, d.Model, d.Slot, d.Config)
		if err != nil {
			blockFurther = true
			blockReason = err.Error()
			results = append(results, scenarioDeployResult{
				Model:  d.Model,
				Engine: d.Engine,
				Status: "error",
				Error:  blockReason,
			})
			continue
		}

		var raw map[string]any
		if json.Unmarshal(data, &raw) == nil {
			if status, _ := raw["status"].(string); status == "NEEDS_APPROVAL" {
				if id, ok := raw["approval_id"].(float64); ok && deps.DeployApprove != nil {
					if approved, err := deps.DeployApprove(ctx, int64(id)); err == nil {
						data = approved
					}
				}
			}
		}
		results = append(results, scenarioDeployResult{
			Model:  d.Model,
			Engine: d.Engine,
			Status: "ok",
			Data:   data,
		})

		deploymentQuery := knowledge.SanitizePodName(d.Model + "-" + d.Engine)
		var deployStatusTarget struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &deployStatusTarget) == nil && deployStatusTarget.Name != "" {
			deploymentQuery = deployStatusTarget.Name
		}

		shouldWait := i < len(ordered)-1 || od.waitFor != "" || od.timeoutS > 0
		if shouldWait {
			if err := scenarioWaitForReady(ctx, deploymentQuery, od.waitFor, od.timeoutS, deps.DeployStatus); err != nil {
				slog.Warn("startup wait did not complete", "model", d.Model, "wait_for", od.waitFor, "err", err)
				blockFurther = true
				blockReason = err.Error()
				results = append(results, scenarioDeployResult{
					Model:  d.Model + "_wait",
					Status: "warning",
					Error:  err.Error(),
				})
			}
		}
	}

	if !dryRun {
		if blockFurther {
			for _, action := range scenario.PostDeploy {
				results = append(results, scenarioDeployResult{
					Model:  action.Action,
					Status: "skipped",
					Error:  fmt.Sprintf("skipped due to earlier deployment failure: %s", blockReason),
				})
			}
		} else {
			postDeployActions := map[string]func(context.Context) (json.RawMessage, error){
				"openclaw_sync": func(ctx context.Context) (json.RawMessage, error) {
					if deps.OpenClawSync == nil {
						return nil, fmt.Errorf("openclaw_sync not available")
					}
					return deps.OpenClawSync(ctx, false)
				},
			}
			for _, action := range scenario.PostDeploy {
				fn, ok := postDeployActions[action.Action]
				if !ok {
					results = append(results, scenarioDeployResult{
						Model:  action.Action,
						Status: "error",
						Error:  fmt.Sprintf("unknown post-deploy action: %s", action.Action),
					})
					continue
				}
				data, err := fn(ctx)
				if err != nil {
					results = append(results, scenarioDeployResult{
						Model:  action.Action,
						Status: "error",
						Error:  err.Error(),
					})
				} else {
					results = append(results, scenarioDeployResult{
						Model:  action.Action,
						Status: "ok",
						Data:   data,
					})
				}
			}
		}
	}

	resp := map[string]any{
		"scenario":    name,
		"dry_run":     dryRun,
		"deployments": results,
	}
	if hwWarning != "" {
		resp["hardware_warning"] = hwWarning
	}
	return json.Marshal(resp)
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

	// 3. Load knowledge catalog (embedded YAML → in-memory structs)
	cat, err := knowledge.LoadCatalog(catalog.FS)
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}

	// 3b. Merge overlay catalog from disk (if present) with staleness detection
	overlayDir := filepath.Join(dataDir, "catalog")
	factoryDigests := knowledge.ComputeDigests(catalog.FS)
	if info, e := os.Stat(overlayDir); e == nil && info.IsDir() {
		overlayFS := os.DirFS(overlayDir)
		overlayCat, parseWarnings := knowledge.LoadCatalogLenient(overlayFS)
		for _, w := range parseWarnings {
			slog.Warn("overlay file skipped", "reason", w)
		}
		before := catalogSize(cat)
		cat, staleWarnings := knowledge.MergeCatalogWithDigests(cat, overlayCat, factoryDigests, overlayFS)
		for _, w := range staleWarnings {
			slog.Warn(w)
		}
		slog.Info("catalog overlay merged",
			"dir", overlayDir,
			"overlay_assets", catalogSize(overlayCat),
			"new_assets", catalogSize(cat)-before,
		)
	}

	// 4. Load static knowledge into SQLite relational tables (only when catalog changes).
	if err := syncCatalogToSQLite(ctx, db, cat); err != nil {
		return err
	}
	if err := db.Analyze(ctx); err != nil {
		slog.Warn("analyze failed", "error", err)
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
	deps := buildToolDeps(cat, db, knowledgeStore, rt, nativeRt, dockerRt, k3sRt, proxyServer, k3sClient, dataDir, factoryDigests, supportSvc)

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
		return json.Marshal(map[string]any{
			"agent_available":         agentAvailable(ctx, llmClient),
			"active_exploration_runs": activeRuns,
		})
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
	uiDeps := &ui.Deps{
		SupportManifest: supportSvc.GoUXManifestJSON,
	}

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

	deps.ScenarioList = func(ctx context.Context) (json.RawMessage, error) {
		type entry struct {
			Name            string   `json:"name"`
			Description     string   `json:"description"`
			Target          string   `json:"target"`
			Deployments     int      `json:"deployments"`
			Modalities      []string `json:"modalities"`
			HasAlternatives bool     `json:"has_alternatives"`
			Verified        bool     `json:"verified"`
			VerifiedDate    string   `json:"verified_date,omitempty"`
		}
		var list []entry
		for _, ds := range cat.DeploymentScenarios {
			// Collect unique modalities across all deployments
			seen := make(map[string]bool)
			var mods []string
			for _, d := range ds.Deployments {
				for _, m := range d.Modalities {
					if !seen[m] {
						seen[m] = true
						mods = append(mods, m)
					}
				}
			}
			e := entry{
				Name:            ds.Metadata.Name,
				Description:     ds.Metadata.Description,
				Target:          ds.Target.HardwareProfile,
				Deployments:     len(ds.Deployments),
				Modalities:      mods,
				HasAlternatives: len(ds.AlternativeConfigs) > 0,
			}
			if ds.Verified != nil {
				e.Verified = true
				e.VerifiedDate = ds.Verified.Date
			}
			list = append(list, e)
		}
		return json.Marshal(list)
	}

	deps.ScenarioShow = func(ctx context.Context, name string) (json.RawMessage, error) {
		for i := range cat.DeploymentScenarios {
			if cat.DeploymentScenarios[i].Metadata.Name == name {
				ds := &cat.DeploymentScenarios[i]
				return json.Marshal(map[string]any{
					"name":                ds.Metadata.Name,
					"description":         ds.Metadata.Description,
					"target":              ds.Target,
					"deployments":         ds.Deployments,
					"post_deploy":         ds.PostDeploy,
					"integrations":        ds.Integrations,
					"verified":            ds.Verified,
					"open_questions":      ds.OpenQuestions,
					"memory_budget":       ds.MemoryBudget,
					"startup_order":       ds.StartupOrder,
					"alternative_configs": ds.AlternativeConfigs,
				})
			}
		}
		names := make([]string, 0, len(cat.DeploymentScenarios))
		for _, ds := range cat.DeploymentScenarios {
			names = append(names, ds.Metadata.Name)
		}
		return nil, fmt.Errorf("scenario %q not found (available: %v)", name, names)
	}

	deps.ScenarioApply = func(ctx context.Context, name string, dryRun bool) (json.RawMessage, error) {
		return applyScenario(ctx, cat, rt.Name(), deps, name, dryRun)
	}

	var (
		patrol *agent.Patrol // created later; captured by closure, safe because serve runs after init
		app    *cli.App      // created later; captured by closure so HTTP routes can reuse the exact Cobra tree
	)
	proxyServer.SetExtraRoutes(func(mux *http.ServeMux) {
		fleetRoutes(mux)
		registerUIRoutes(mux, cat, uiDeps)
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

	// fleetEnsureDiscovery runs a one-shot mDNS scan if the registry is empty.
	// This ensures fleet MCP tools work without serve --discover (INV-5 parity).
	fleetEnsureDiscovery := func(ctx context.Context) {
		if len(fleetRegistry.List()) > 0 {
			return
		}
		services, err := proxy.Discover(ctx, 3*time.Second)
		if err != nil {
			slog.Debug("fleet auto-discovery failed", "error", err)
			return
		}
		fleetRegistry.Update(services)
	}

	deps.FleetListDevices = func(ctx context.Context) (json.RawMessage, error) {
		// Always discover — this is the canonical "find devices" operation
		services, err := proxy.Discover(ctx, 3*time.Second)
		if err != nil {
			return nil, fmt.Errorf("mDNS discovery: %w", err)
		}
		fleetRegistry.Update(services)
		return json.Marshal(fleetRegistry.List())
	}
	deps.FleetDeviceInfo = func(ctx context.Context, deviceID string) (json.RawMessage, error) {
		fleetEnsureDiscovery(ctx)
		d := fleetRegistry.Get(deviceID)
		if d == nil {
			return nil, fmt.Errorf("device %q not found", deviceID)
		}
		if d.Self {
			if deps.SystemStatus != nil {
				return deps.SystemStatus(ctx)
			}
			return json.Marshal(d)
		}
		return fleetClient.GetDeviceInfo(ctx, d)
	}
	deps.FleetDeviceTools = func(ctx context.Context, deviceID string) (json.RawMessage, error) {
		fleetEnsureDiscovery(ctx)
		d := fleetRegistry.Get(deviceID)
		if d == nil {
			return nil, fmt.Errorf("device %q not found", deviceID)
		}
		if d.Self {
			return json.Marshal(mcpServer.ListTools())
		}
		return fleetClient.ListTools(ctx, d)
	}
	deps.FleetExecTool = func(ctx context.Context, deviceID, toolName string, params json.RawMessage) (json.RawMessage, error) {
		if strings.HasPrefix(toolName, "fleet.") {
			return nil, fmt.Errorf("cannot execute fleet tools remotely (recursive call blocked): %s", toolName)
		}
		// Block destructive tools from fleet execution path (matches agent guardrails)
		if reason, ok := fleetBlockedTools[toolName]; ok {
			return nil, fmt.Errorf("fleet.exec_tool: %s is blocked (%s)", toolName, reason)
		}
		fleetEnsureDiscovery(ctx)
		d := fleetRegistry.Get(deviceID)
		if d == nil {
			return nil, fmt.Errorf("device %q not found", deviceID)
		}
		if d.Self {
			result, err := mcpServer.ExecuteTool(ctx, toolName, params)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		}
		return fleetClient.CallTool(ctx, d, toolName, params)
	}

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
		case "llm.endpoint":
			llmClient.SetEndpoint(value)
			slog.Info("LLM endpoint hot-swapped via system.config", "endpoint", value)
		case "llm.model":
			llmClient.SetModel(value)
			slog.Info("LLM model hot-swapped via system.config", "model", value)
		case "llm.api_key":
			llmClient.SetAPIKey(value)
			slog.Info("LLM API key hot-swapped via system.config")
		case "llm.user_agent":
			llmClient.SetUserAgent(value)
			slog.Info("LLM User-Agent hot-swapped via system.config", "user_agent", value)
		case "llm.extra_params":
			llmClient.SetExtraParams(parseExtraParams(value))
			slog.Info("LLM extra params hot-swapped via system.config")
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

	deps.PatrolStatus = func(ctx context.Context) (json.RawMessage, error) {
		return json.Marshal(patrol.Status())
	}
	deps.PatrolAlerts = func(ctx context.Context) (json.RawMessage, error) {
		alerts := patrol.ActiveAlerts()
		if alerts == nil {
			alerts = []agent.Alert{}
		}
		return json.Marshal(alerts)
	}
	deps.PatrolConfig = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Action string `json:"action"`
			Key    string `json:"key"`
			Value  string `json:"value"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Action == "get" {
			return json.Marshal(patrol.Config())
		}
		switch p.Key {
		case "interval":
			d, err := time.ParseDuration(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid duration: %w", err)
			}
			if d < 0 {
				return nil, fmt.Errorf("interval must be >= 0")
			}
			patrol.SetInterval(d)
		case "gpu_temp_warn":
			v, err := strconv.Atoi(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid integer: %w", err)
			}
			if v < 0 {
				return nil, fmt.Errorf("gpu_temp_warn must be >= 0")
			}
			patrol.SetGPUTempWarn(v)
		case "gpu_idle_pct":
			v, err := strconv.Atoi(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid integer: %w", err)
			}
			if v < 0 || v > 100 {
				return nil, fmt.Errorf("gpu_idle_pct must be between 0 and 100")
			}
			patrol.SetGPUIdle(v, patrol.Config().GPUIdleMinutes)
		case "gpu_idle_minutes":
			v, err := strconv.Atoi(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid integer: %w", err)
			}
			if v < 0 {
				return nil, fmt.Errorf("gpu_idle_minutes must be >= 0")
			}
			patrol.SetGPUIdle(patrol.Config().GPUIdlePct, v)
		case "vram_opportunity_pct":
			v, err := strconv.Atoi(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid integer: %w", err)
			}
			if v < 0 || v > 100 {
				return nil, fmt.Errorf("vram_opportunity_pct must be between 0 and 100")
			}
			patrol.SetVRAMOpportunity(v)
		case "self_heal":
			patrol.SetSelfHeal(p.Value == "true" || p.Value == "1")
		default:
			return nil, fmt.Errorf("unknown patrol config key: %s", p.Key)
		}
		return json.Marshal(map[string]string{"status": "updated"})
	}
	deps.PatrolActions = func(ctx context.Context, limit int) (json.RawMessage, error) {
		actions := patrol.RecentActions(limit)
		if actions == nil {
			actions = []agent.PatrolAction{}
		}
		return json.Marshal(actions)
	}
	deps.TuningStart = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var config agent.TuningConfig
		if err := json.Unmarshal(params, &config); err != nil {
			return nil, err
		}
		if config.MaxConfigs == 0 {
			config.MaxConfigs = 20
		}
		session, err := tuner.Start(ctx, config)
		if err != nil {
			return nil, err
		}
		return json.Marshal(session)
	}
	deps.TuningStatus = func(ctx context.Context) (json.RawMessage, error) {
		s := tuner.CurrentSession()
		if s == nil {
			return json.Marshal(map[string]string{"status": "no session"})
		}
		return json.Marshal(s)
	}
	deps.TuningStop = func(ctx context.Context) (json.RawMessage, error) {
		tuner.Stop()
		return json.Marshal(map[string]string{"status": "stopped"})
	}
	deps.TuningResults = func(ctx context.Context) (json.RawMessage, error) {
		s := tuner.CurrentSession()
		if s == nil {
			return json.Marshal(map[string]string{"status": "no session"})
		}
		return json.Marshal(s)
	}
	deps.ExploreStart = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var req agent.ExplorationStart
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		run, err := explorationMgr.Start(ctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(run)
	}
	deps.ExploreStartAndWait = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var req agent.ExplorationStart
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		status, err := explorationMgr.StartAndWait(ctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	}
	deps.ExploreStatus = func(ctx context.Context, runID string) (json.RawMessage, error) {
		status, err := explorationMgr.Status(ctx, runID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	}
	deps.ExploreStop = func(ctx context.Context, runID string) (json.RawMessage, error) {
		status, err := explorationMgr.Stop(ctx, runID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	}
	deps.ExploreResult = func(ctx context.Context, runID string) (json.RawMessage, error) {
		result, err := explorationMgr.Result(ctx, runID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.OpenQuestions = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Action      string `json:"action"`
			Status      string `json:"status"`
			ID          string `json:"id"`
			Result      string `json:"result"`
			Hardware    string `json:"hardware"`
			Model       string `json:"model"`
			Engine      string `json:"engine"`
			Endpoint    string `json:"endpoint"`
			RequestedBy string `json:"requested_by"`
			Concurrency int    `json:"concurrency"`
			Rounds      int    `json:"rounds"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		switch p.Action {
		case "resolve":
			if p.ID == "" {
				return nil, fmt.Errorf("id required for resolve action")
			}
			status := "confirmed"
			if p.Status != "" {
				status = p.Status
			}
			if err := db.ResolveOpenQuestion(ctx, p.ID, status, p.Result, p.Hardware); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"status": "resolved", "id": p.ID})
		case "run", "validate":
			if explorationMgr == nil {
				return nil, fmt.Errorf("exploration manager unavailable")
			}
			if p.ID == "" {
				return nil, fmt.Errorf("id required for %s action", p.Action)
			}
			question, err := db.GetOpenQuestion(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			hardware := p.Hardware
			if hardware == "" {
				hardware = question.Hardware
			}
			requestedBy := p.RequestedBy
			if requestedBy == "" {
				requestedBy = "user"
			}
			run, err := explorationMgr.Start(ctx, agent.ExplorationStart{
				Kind: "open_question",
				Goal: fmt.Sprintf("validate open question: %s", question.Question),
				Target: agent.ExplorationTarget{
					Hardware: hardware,
					Model:    p.Model,
					Engine:   p.Engine,
				},
				RequestedBy:  requestedBy,
				SourceRef:    p.ID,
				ApprovalMode: "none",
				Benchmark: agent.ExplorationBenchmarkProfile{
					Endpoint:    p.Endpoint,
					Concurrency: p.Concurrency,
					Rounds:      p.Rounds,
				},
			})
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{
				"status":   "queued",
				"question": question,
				"run":      run,
			})
		default:
			questions, err := db.ListOpenQuestions(ctx, p.Status)
			if err != nil {
				return nil, err
			}
			if questions == nil {
				questions = []map[string]any{}
			}
			return json.Marshal(questions)
		}
	}
	deps.PowerHistory = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.From == "" {
			p.From = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		}
		if p.To == "" {
			p.To = time.Now().UTC().Format(time.RFC3339)
		}
		results, err := db.QueryPowerHistory(ctx, p.From, p.To, 60)
		if err != nil {
			return nil, err
		}
		return json.Marshal(results)
	}
	deps.ValidateKnowledge = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Hardware string `json:"hardware"`
			Engine   string `json:"engine"`
			Model    string `json:"model"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		results, err := db.ListValidations(ctx, p.Hardware, p.Engine, p.Model)
		if err != nil {
			return nil, err
		}
		return json.Marshal(results)
	}
	deps.EngineSwitchCost = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			CurrentEngine string `json:"current_engine"`
			TargetEngine  string `json:"target_engine"`
			Hardware      string `json:"hardware"`
			Model         string `json:"model"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		// Look up engines from catalog for cold_start data
		hwInfo := knowledge.HardwareInfo{GPUArch: p.Hardware}
		currentEngine := cat.FindEngineByName(p.CurrentEngine, hwInfo)
		targetEngine := cat.FindEngineByName(p.TargetEngine, hwInfo)

		result := map[string]any{
			"current_engine": p.CurrentEngine,
			"target_engine":  p.TargetEngine,
		}

		if targetEngine != nil && len(targetEngine.TimeConstraints.ColdStartS) >= 2 {
			result["switch_time_s"] = targetEngine.TimeConstraints.ColdStartS[1]
		}

		// Amplifier comparison
		currentMult := 1.0
		targetMult := 1.0
		if currentEngine != nil && currentEngine.Amplifier.PerformanceMultiplier > 0 {
			currentMult = currentEngine.Amplifier.PerformanceMultiplier
		}
		if targetEngine != nil && targetEngine.Amplifier.PerformanceMultiplier > 0 {
			targetMult = targetEngine.Amplifier.PerformanceMultiplier
		}
		result["current_multiplier"] = currentMult
		result["target_multiplier"] = targetMult

		if targetMult > currentMult*1.1 {
			result["recommendation"] = "switch"
			result["reason"] = fmt.Sprintf("target %.1fx vs current %.1fx performance multiplier (>10%% gain)", targetMult, currentMult)
		} else {
			result["recommendation"] = "stay"
			result["reason"] = fmt.Sprintf("target %.1fx vs current %.1fx — gain insufficient to justify switch cost", targetMult, currentMult)
		}
		return json.Marshal(result)
	}
	// 9j. App management (D4)
	deps.AppRegister = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Name            string          `json:"name"`
			InferenceNeeds  json.RawMessage `json:"inference_needs"`
			ResourceBudget  json.RawMessage `json:"resource_budget"`
			TimeConstraints json.RawMessage `json:"time_constraints"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Name == "" {
			return nil, fmt.Errorf("name required")
		}
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(p.Name)))[:16]
		specBytes, _ := json.Marshal(map[string]any{
			"name":             p.Name,
			"inference_needs":  json.RawMessage(p.InferenceNeeds),
			"resource_budget":  json.RawMessage(p.ResourceBudget),
			"time_constraints": json.RawMessage(p.TimeConstraints),
		})
		if err := db.InsertApp(ctx, id, p.Name, string(specBytes)); err != nil {
			return nil, err
		}

		// Parse inference needs and record dependencies
		var needs []struct {
			Type        string `json:"type"`
			Model       string `json:"model"`
			Required    bool   `json:"required"`
			Performance string `json:"performance"`
		}
		if p.InferenceNeeds != nil {
			_ = json.Unmarshal(p.InferenceNeeds, &needs)
		}
		for _, need := range needs {
			_ = db.UpsertAppDependency(ctx, id, need.Type, need.Model, "", false)
		}

		return json.Marshal(map[string]any{"id": id, "name": p.Name, "status": "registered", "dependencies": len(needs)})
	}
	deps.AppProvision = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		apps, err := db.ListApps(ctx)
		if err != nil {
			return nil, err
		}
		// Find the app
		var appSpec map[string]any
		var appID string
		for _, a := range apps {
			if a["name"] == p.Name {
				appID, _ = a["id"].(string)
				if specRaw, ok := a["spec"].(json.RawMessage); ok {
					_ = json.Unmarshal(specRaw, &appSpec)
				}
				break
			}
		}
		if appID == "" {
			return nil, fmt.Errorf("app %q not found", p.Name)
		}

		// Parse inference needs from spec
		var needs []struct {
			Type        string `json:"type"`
			Model       string `json:"model"`
			Required    bool   `json:"required"`
			Performance string `json:"performance"`
		}
		if needsRaw, ok := appSpec["inference_needs"]; ok {
			needsBytes, _ := json.Marshal(needsRaw)
			_ = json.Unmarshal(needsBytes, &needs)
		}

		// Check existing deployments
		deploys, _ := deps.DeployList(ctx)
		var deployList []map[string]any
		_ = json.Unmarshal(deploys, &deployList)

		report := make([]map[string]any, 0, len(needs))
		allSatisfied := true
		for _, need := range needs {
			satisfied := false
			deployName := ""
			// Check if already deployed
			for _, d := range deployList {
				dModel, _ := d["model"].(string)
				if need.Model != "" && strings.Contains(dModel, need.Model) {
					satisfied = true
					deployName, _ = d["name"].(string)
					break
				}
			}
			_ = db.UpsertAppDependency(ctx, appID, need.Type, need.Model, deployName, satisfied)
			if !satisfied && need.Required {
				allSatisfied = false
			}
			report = append(report, map[string]any{
				"type": need.Type, "model": need.Model, "satisfied": satisfied,
				"deploy_name": deployName, "required": need.Required,
			})
		}

		status := "provisioned"
		if !allSatisfied {
			status = "partial"
		}
		_ = db.UpdateAppStatus(ctx, appID, status)

		return json.Marshal(map[string]any{
			"app": p.Name, "status": status, "dependencies": report,
		})
	}
	deps.AppList = func(ctx context.Context) (json.RawMessage, error) {
		apps, err := db.ListApps(ctx)
		if err != nil {
			return nil, err
		}
		if apps == nil {
			apps = []map[string]any{}
		}
		return json.Marshal(apps)
	}

	// 9k. Knowledge sync (K6)
	syncHTTPClient := &http.Client{Timeout: 30 * time.Second}
	deps.SyncPush = func(ctx context.Context) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured — use system.config set central.endpoint <url>")
		}
		// Export local knowledge
		exportData, err := deps.ExportKnowledge(ctx, json.RawMessage(`{}`))
		if err != nil {
			return nil, fmt.Errorf("export failed: %w", err)
		}
		// Transform export envelope to central's IngestPayload format.
		// Export: {data: {configurations, benchmark_results, knowledge_notes}}
		// Ingest: {configurations, benchmarks, device_id, gpu_arch}
		var exportEnvelope struct {
			Data struct {
				Configurations   []json.RawMessage `json:"configurations"`
				BenchmarkResults []json.RawMessage `json:"benchmark_results"`
				KnowledgeNotes   []json.RawMessage `json:"knowledge_notes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(exportData, &exportEnvelope); err != nil {
			return nil, fmt.Errorf("parse export data: %w", err)
		}

		hwInfo, _ := hal.Detect(ctx)
		deviceID, _ := deps.GetConfig(ctx, "device.id")
		gpuArch := ""
		if hwInfo != nil && hwInfo.GPU != nil {
			gpuArch = hwInfo.GPU.Arch
		}

		ingestPayload, err := json.Marshal(map[string]any{
			"schema_version":  1,
			"device_id":       deviceID,
			"gpu_arch":        gpuArch,
			"configurations":  exportEnvelope.Data.Configurations,
			"benchmarks":      exportEnvelope.Data.BenchmarkResults,
			"knowledge_notes": exportEnvelope.Data.KnowledgeNotes,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal ingest payload: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/v1/ingest",
			strings.NewReader(string(ingestPayload)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("push to central: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d: %s", resp.StatusCode, string(body))
		}
		_ = db.SetSyncTimestamp(ctx, "push")
		return json.Marshal(map[string]any{
			"status":   "pushed",
			"endpoint": endpoint,
			"result":   json.RawMessage(body),
		})
	}
	deps.SyncPull = func(ctx context.Context) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured — use system.config set central.endpoint <url>")
		}
		since, _ := db.GetSyncTimestamp(ctx, "pull")
		syncURL := endpoint + "/api/v1/sync"
		if since != "" {
			syncURL += "?since=" + since
		}
		req, err := http.NewRequestWithContext(ctx, "GET", syncURL, nil)
		if err != nil {
			return nil, err
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("pull from central: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d", resp.StatusCode)
		}

		// Central sync returns the standard import envelope format:
		// {schema_version, data: {configurations, benchmark_results}}
		// Import it directly (write to temp file since ImportKnowledge is file-based).
		syncData, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read central response: %w", err)
		}

		tmpFile, err := os.CreateTemp("", "aima-sync-*.json")
		if err != nil {
			return nil, fmt.Errorf("create temp file: %w", err)
		}
		tmpPath := tmpFile.Name()
		defer os.Remove(tmpPath)
		if _, err := tmpFile.Write(syncData); err != nil {
			tmpFile.Close()
			return nil, fmt.Errorf("write temp file: %w", err)
		}
		tmpFile.Close()

		importParams, _ := json.Marshal(map[string]any{
			"input_path": tmpPath,
			"conflict":   "skip",
		})
		result, err := deps.ImportKnowledge(ctx, importParams)
		if err != nil {
			return nil, fmt.Errorf("import pulled knowledge: %w", err)
		}
		_ = db.SetSyncTimestamp(ctx, "pull")
		return result, nil
	}
	deps.SyncStatus = func(ctx context.Context) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		pushAt, _ := db.GetSyncTimestamp(ctx, "push")
		pullAt, _ := db.GetSyncTimestamp(ctx, "pull")
		connected := false
		if endpoint != "" {
			req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/api/v1/stats", nil)
			if err == nil {
				resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
				if err == nil {
					resp.Body.Close()
					connected = resp.StatusCode == http.StatusOK
				}
			}
		}
		return json.Marshal(map[string]any{
			"endpoint":  endpoint,
			"connected": connected,
			"last_push": pushAt,
			"last_pull": pullAt,
		})
	}

	// 9l. Power mode (S3)
	deps.PowerMode = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Action string `json:"action"`
			Mode   string `json:"mode"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		hw, err := hal.Detect(ctx)
		if err != nil {
			return nil, err
		}
		// Look up power modes from hardware profile
		var powerModes []int
		var tdpWatts int
		gpuArch := ""
		if hw.GPU != nil {
			gpuArch = hw.GPU.Arch
		}
		for _, hp := range cat.HardwareProfiles {
			if hp.Hardware.GPU.Arch == gpuArch {
				powerModes = hp.Constraints.PowerModes
				tdpWatts = hp.Constraints.TDPWatts
				break
			}
		}
		result := map[string]any{
			"gpu_arch":    gpuArch,
			"tdp_watts":   tdpWatts,
			"power_modes": powerModes,
		}
		if hw.GPU != nil {
			result["current_power_draw_watts"] = hw.GPU.PowerDrawWatts
			result["power_limit_watts"] = hw.GPU.PowerLimitWatts
		}
		return json.Marshal(result)
	}

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
		OpenBrowser:   defaultRootArgs(os.Args) != nil,
	}

	rootCmd := cli.NewRootCmd(app)
	if args := defaultRootArgs(os.Args); args != nil {
		rootCmd.SetArgs(args)
	}
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
	return upsertScannedModelInfo(ctx, info, db)
}

func registerExistingModel(ctx context.Context, modelPath string, db *state.DB) error {
	absPath, err := filepath.Abs(modelPath)
	if err != nil {
		return fmt.Errorf("resolve local model path %s: %w", modelPath, err)
	}
	scanRoot := absPath
	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		scanRoot = filepath.Dir(absPath)
	} else {
		scanRoot = filepath.Dir(absPath)
	}

	models, err := model.Scan(ctx, model.ScanOptions{
		Paths:             []string{scanRoot},
		MinModelSizeBytes: 1,
	})
	if err != nil {
		return fmt.Errorf("scan existing model %s: %w", modelPath, err)
	}

	targetDir := absPath + string(filepath.Separator)
	for _, m := range models {
		if m.Path == absPath || strings.HasPrefix(m.Path, targetDir) {
			return upsertScannedModelInfo(ctx, m, db)
		}
	}
	return nil
}

func upsertScannedModelInfo(ctx context.Context, info *model.ModelInfo, db *state.DB) error {
	if info == nil {
		return nil
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

func variantQuantizationHint(variant *knowledge.ModelVariant) string {
	if variant == nil {
		return ""
	}
	if variant.Source != nil && variant.Source.Quantization != "" {
		return strings.ToLower(variant.Source.Quantization)
	}
	if q, ok := variant.DefaultConfig["quantization"].(string); ok && q != "" {
		return strings.ToLower(q)
	}
	return ""
}

// catalogModelNames returns a comma-separated list of available model names.
func catalogModelNames(cat *knowledge.Catalog) string {
	names := make([]string, 0, len(cat.ModelAssets))
	for _, ma := range cat.ModelAssets {
		names = append(names, ma.Metadata.Name)
	}
	return strings.Join(names, ", ")
}

var overlayAssetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validateOverlayAssetName ensures the user-provided override name stays inside
// the overlay directory and is safe as a file basename.
func validateOverlayAssetName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid name %q: path traversal is not allowed", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid name %q: path separators are not allowed", name)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("invalid name %q: absolute paths are not allowed", name)
	}
	if !overlayAssetNamePattern.MatchString(name) {
		return fmt.Errorf("invalid name %q: only letters, digits, dot, underscore, and dash are allowed", name)
	}
	return nil
}

// fleetBlockedTools lists MCP tools that cannot be executed via fleet.exec_tool.
// Destructive operations must be performed locally, not via remote fleet calls.
var fleetBlockedTools = map[string]string{
	"model.remove":   "destructive: deletes model data",
	"engine.remove":  "destructive: deletes engine image",
	"deploy.delete":  "destructive: stops running deployment",
	"explore.start":  "orchestration: run-scoped approval not implemented remotely",
	"stack.init":     "infrastructure: modifies system services",
	"agent.rollback": "destructive: rolls back state",
	"shell.exec":     "arbitrary command execution",
}

// confirmableTools lists MCP tools that require user confirmation when called by the Agent.
// These are NOT blocked: instead, the adapter runs a dry-run and returns NEEDS_APPROVAL.
// The user can then approve via deploy.approve, or re-run with --dangerously-skip-permissions.
var confirmableTools = map[string]string{
	"deploy.apply": "creates or replaces inference deployment",
}

// blockedAgentTools lists MCP tools that the Agent must not call directly.
// These are blocked at the adapter level; users can still invoke them via CLI.
var blockedAgentTools = map[string]string{
	"model.remove":   "destructive operation",
	"engine.remove":  "destructive operation",
	"deploy.delete":  "destructive operation",
	"explore.start":  "run-scoped approval not implemented for agent-initiated exploration",
	"shell.exec":     "arbitrary command execution",
	"stack.init":     "infrastructure mutation",
	"agent.ask":      "recursive agent invocation",
	"agent.rollback": "state rollback mutation",
}

func isBlockedAgentTool(name string, arguments json.RawMessage) (bool, string) {
	if reason, ok := blockedAgentTools[name]; ok {
		return true, reason
	}

	// system.config supports both get and set. Agent may read, but writes are blocked.
	// Block when "value" key is present in the JSON (regardless of its value, including null).
	if name == "system.config" && len(arguments) > 0 {
		var raw map[string]json.RawMessage
		if json.Unmarshal(arguments, &raw) == nil {
			if _, hasValue := raw["value"]; hasValue {
				return true, "persistent configuration mutation"
			}
		}
	}

	// fleet.exec_tool: penetrate to inner tool_name — apply same guardrails as local calls.
	if name == "fleet.exec_tool" {
		if len(arguments) > 0 {
			var raw map[string]json.RawMessage
			if json.Unmarshal(arguments, &raw) == nil {
				if tnRaw, ok := raw["tool_name"]; ok {
					var innerTool string
					if json.Unmarshal(tnRaw, &innerTool) == nil {
						// Block fleet-recursive calls
						if strings.HasPrefix(innerTool, "fleet.") {
							return true, "recursive fleet call blocked"
						}
						// Apply same blocked/system.config/catalog.override checks to inner tool
						var innerParams json.RawMessage
						if paramsRaw, ok := raw["params"]; ok {
							innerParams = paramsRaw
						}
						return isBlockedAgentTool(innerTool, innerParams)
					}
				}
			}
		}
		// Can't parse tool_name → block as safety default
		return true, "fleet.exec_tool: cannot determine inner tool_name"
	}

	// catalog.override: allow engine_asset and model_asset (inference tuning),
	// block hardware_profile, partition_strategy, stack_component (infrastructure safety).
	if name == "catalog.override" {
		if len(arguments) > 0 {
			var raw map[string]json.RawMessage
			if json.Unmarshal(arguments, &raw) == nil {
				if kindRaw, ok := raw["kind"]; ok {
					var kind string
					if json.Unmarshal(kindRaw, &kind) == nil {
						switch kind {
						case "engine_asset", "model_asset":
							return false, ""
						}
					}
				}
			}
		}
		return true, "catalog override restricted to engine/model assets for Agent"
	}

	return false, ""
}

type ctxKey string

const ctxKeySkipPerms ctxKey = "skipPerms"

// mcpToolAdapter bridges mcp.Server to agent.ToolExecutor interface.
// It also enforces agent safety guardrails: destructive-op blocking, confirmation gates, and audit logging.
type mcpToolAdapter struct {
	server *mcp.Server
	db     *state.DB

	mu      sync.Mutex
	pending map[int64]*pendingApproval
	nextID  int64
}

type pendingApproval struct {
	toolName  string
	arguments json.RawMessage
	createdAt time.Time
}

func (a *mcpToolAdapter) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*agent.ToolResult, error) {
	// Gap 1: Block high-risk operations from the Agent.
	if blocked, reason := isBlockedAgentTool(name, arguments); blocked {
		msg := fmt.Sprintf("BLOCKED: %s is blocked for Agent-initiated calls (%s). Ask the user to run it via CLI instead.", name, reason)
		a.audit(ctx, name, string(arguments), "BLOCKED")
		return &agent.ToolResult{Content: msg, IsError: true}, nil
	}

	// Gap 1b: Confirmable tools require user approval (unless --dangerously-skip-permissions).
	skipPerms, _ := ctx.Value(ctxKeySkipPerms).(bool)

	// fleet.exec_tool wrapping a confirmable inner tool → needs approval too
	if name == "fleet.exec_tool" && !skipPerms {
		if len(arguments) > 0 {
			var raw map[string]json.RawMessage
			if json.Unmarshal(arguments, &raw) == nil {
				if tnRaw, ok := raw["tool_name"]; ok {
					var innerTool string
					if json.Unmarshal(tnRaw, &innerTool) == nil {
						if reason, ok := confirmableTools[innerTool]; ok {
							// Run remote dry-run via fleet.exec_tool itself
							dryArgs, _ := json.Marshal(map[string]any{
								"device_id": json.RawMessage(raw["device_id"]),
								"tool_name": "deploy.dry_run",
								"params":    json.RawMessage(raw["params"]),
							})
							dryResult, drErr := a.server.ExecuteTool(ctx, "fleet.exec_tool", dryArgs)
							var planText string
							if drErr == nil {
								for _, c := range dryResult.Content {
									planText += c.Text
								}
							} else {
								planText = "remote dry-run unavailable: " + drErr.Error()
							}
							id := a.storePending(name, arguments)
							msg := fmt.Sprintf("NEEDS_APPROVAL\n"+
								"Approval ID: %d\n"+
								"Tool: %s\n"+
								"Reason: %s\n\n"+
								"Deployment plan:\n%s\n\n"+
								"Present this plan to the user. When the user approves, call deploy.approve with id=%d.",
								id, innerTool, reason, planText, id)
							a.audit(ctx, name, string(arguments), fmt.Sprintf("NEEDS_APPROVAL id=%d", id))
							return &agent.ToolResult{Content: msg, IsError: false}, nil
						}
					}
				}
			}
		}
	}

	if reason, ok := confirmableTools[name]; ok && !skipPerms {
		dryResult, drErr := a.server.ExecuteTool(ctx, "deploy.dry_run", arguments)
		var planText string
		if drErr == nil {
			for _, c := range dryResult.Content {
				planText += c.Text
			}
		} else {
			planText = "dry-run unavailable: " + drErr.Error()
		}

		id := a.storePending(name, arguments)

		msg := fmt.Sprintf("NEEDS_APPROVAL\n"+
			"Approval ID: %d\n"+
			"Tool: %s\n"+
			"Reason: %s\n\n"+
			"Deployment plan:\n%s\n\n"+
			"Present this plan to the user. When the user approves, call deploy.approve with id=%d.",
			id, name, reason, planText, id)
		a.audit(ctx, name, string(arguments), fmt.Sprintf("NEEDS_APPROVAL id=%d", id))
		return &agent.ToolResult{Content: msg, IsError: false}, nil
	}

	result, err := a.server.ExecuteTool(ctx, name, arguments)
	if err != nil {
		a.audit(ctx, name, string(arguments), "ERROR: "+err.Error())
		return nil, err
	}
	// Convert mcp.ToolResult to agent.ToolResult
	var text string
	for _, c := range result.Content {
		text += c.Text
	}
	// Gap 2: Audit log every agent tool call
	summary := text
	if result.IsError {
		summary = "ERROR: " + text
	}
	a.audit(ctx, name, string(arguments), truncateStr(summary, 500))
	return &agent.ToolResult{
		Content: text,
		IsError: result.IsError,
	}, nil
}

// audit writes to audit_log. Failures are logged but do not block the tool call.
func (a *mcpToolAdapter) audit(ctx context.Context, tool, args, result string) {
	if a.db == nil {
		return
	}
	if err := a.db.LogAction(ctx, &state.AuditEntry{
		AgentType:     "L3a",
		ToolName:      tool,
		Arguments:     truncateStr(args, 500),
		ResultSummary: result,
	}); err != nil {
		slog.Warn("audit log write failed", "tool", tool, "error", err)
	}
}

// truncateStr truncates s to maxLen bytes, appending "…" if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// storePending saves a pending approval and returns its ID. Expired entries (>30min) are pruned.
func (a *mcpToolAdapter) storePending(tool string, args json.RawMessage) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for id, p := range a.pending {
		if now.Sub(p.createdAt) > 30*time.Minute {
			delete(a.pending, id)
		}
	}
	a.nextID++
	a.pending[a.nextID] = &pendingApproval{
		toolName:  tool,
		arguments: append(json.RawMessage{}, args...), // copy
		createdAt: now,
	}
	return a.nextID
}

// executeApproval looks up a pending approval by ID, executes it on the MCP server
// (bypassing the adapter's confirmation gate), and removes the entry.
// Safety: blocked tools can never reach the pending map (blocked check runs first in ExecuteTool).
func (a *mcpToolAdapter) executeApproval(ctx context.Context, id int64) (json.RawMessage, error) {
	a.mu.Lock()
	p, ok := a.pending[id]
	if ok {
		delete(a.pending, id)
	}
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("approval %d not found or expired", id)
	}

	// Defense-in-depth: re-check blocked tools (should never happen since blocked check
	// runs before confirmable check in ExecuteTool, but guard against future changes).
	if blocked, reason := isBlockedAgentTool(p.toolName, p.arguments); blocked {
		a.audit(ctx, "deploy.approve", fmt.Sprintf("id=%d", id), "BLOCKED: "+reason)
		return nil, fmt.Errorf("approval %d references blocked tool %s: %s", id, p.toolName, reason)
	}

	a.audit(ctx, p.toolName, string(p.arguments), fmt.Sprintf("APPROVED via deploy.approve id=%d", id))
	result, err := a.server.ExecuteTool(ctx, p.toolName, p.arguments)
	if err != nil {
		a.audit(ctx, p.toolName, string(p.arguments), "ERROR: "+err.Error())
		return nil, err
	}
	var text string
	for _, c := range result.Content {
		text += c.Text
	}
	a.audit(ctx, p.toolName, string(p.arguments), truncateStr(text, 500))
	return json.RawMessage(text), nil
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

type automationToolAdapter struct {
	base *mcpToolAdapter
}

func (a *automationToolAdapter) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*agent.ToolResult, error) {
	ctx = context.WithValue(ctx, ctxKeySkipPerms, true)
	return a.base.ExecuteTool(ctx, name, arguments)
}

func (a *automationToolAdapter) ListTools() []agent.ToolDefinition {
	return a.base.ListTools()
}

// fleetMCPAdapter bridges mcp.Server to fleet.MCPExecutor interface.
type fleetMCPAdapter struct {
	server *mcp.Server
}

func (a *fleetMCPAdapter) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	result, err := a.server.ExecuteTool(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (a *fleetMCPAdapter) ListToolDefs() json.RawMessage {
	data, _ := json.Marshal(a.server.ListTools())
	return data
}

// toEngineBinarySource converts a knowledge.EngineSource to engine.BinarySource.
// Centralises the mapping so callers don't repeat the 4-field struct literal.
func toEngineBinarySource(src *knowledge.EngineSource) *engine.BinarySource {
	var probePaths []string
	if src != nil && src.Probe != nil {
		probePaths = append(probePaths, src.Probe.Paths...)
	}
	return &engine.BinarySource{
		Binary:      src.Binary,
		Platforms:   src.Platforms,
		Download:    src.Download,
		Mirror:      src.Mirror,
		SHA256:      src.SHA256,
		InstallType: src.InstallType,
		ProbePaths:  probePaths,
	}
}

// execRunner implements engine.CommandRunner using real exec.
type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (r *execRunner) Pipe(ctx context.Context, from, to []string) error {
	fromCmd := exec.CommandContext(ctx, from[0], from[1:]...)
	toCmd := exec.CommandContext(ctx, to[0], to[1:]...)

	pipe, err := fromCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create pipe: %w", err)
	}
	toCmd.Stdin = pipe

	if err := fromCmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", from[0], err)
	}
	if err := toCmd.Start(); err != nil {
		_ = fromCmd.Process.Kill()
		_ = fromCmd.Wait()
		return fmt.Errorf("%s: %w", to[0], err)
	}

	// Wait for both concurrently. If the receiver (toCmd) dies early
	// (e.g., permission denied on containerd socket), kill the sender
	// to avoid blocking on a dead pipe.
	toErr := make(chan error, 1)
	go func() { toErr <- toCmd.Wait() }()

	fromErr := fromCmd.Wait()
	tErr := <-toErr

	if tErr != nil {
		_ = fromCmd.Process.Kill()
		return fmt.Errorf("%s: %w", to[0], tErr)
	}
	if fromErr != nil {
		return fmt.Errorf("%s: %w", from[0], fromErr)
	}
	return nil
}

func (r *execRunner) RunStream(ctx context.Context, onLine func(line string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe for %s: %w", name, err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	scanner := bufio.NewScanner(stdout)
	// Docker pull JSON lines can be long; increase buffer
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		onLine(scanner.Text())
	}
	return cmd.Wait()
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

// proxyBackendAdapter bridges proxy.Server to openclaw.BackendLister.
type proxyBackendAdapter struct{ s *proxy.Server }

func (a proxyBackendAdapter) ListBackends() map[string]*openclaw.Backend {
	pbs := a.s.ListBackends()
	result := make(map[string]*openclaw.Backend, len(pbs))
	for k, b := range pbs {
		result[k] = &openclaw.Backend{
			ModelName:  b.ModelName,
			EngineType: b.EngineType,
			Address:    b.Address,
			Ready:      b.Ready,
			Remote:     b.Remote,
		}
	}
	return result
}

// catalogAdapter bridges knowledge.Catalog to openclaw.CatalogReader.
type catalogAdapter struct{ cat *knowledge.Catalog }

func (a catalogAdapter) ModelType(name string) string {
	for _, m := range a.cat.ModelAssets {
		if m.Metadata.Name == name {
			return m.Metadata.Type
		}
	}
	return ""
}

func (a catalogAdapter) ModelContextWindow(name string) int {
	for _, m := range a.cat.ModelAssets {
		if m.Metadata.Name != name {
			continue
		}
		for _, v := range m.Variants {
			if ml, ok := v.DefaultConfig["max_model_len"]; ok {
				switch n := ml.(type) {
				case int:
					return n
				case float64:
					return int(n)
				}
			}
		}
	}
	return 0
}

func (a catalogAdapter) ModelFamily(name string) string {
	for _, m := range a.cat.ModelAssets {
		if m.Metadata.Name == name {
			return m.Metadata.Family
		}
	}
	return ""
}

// detectHWProfile returns the hardware profile name (e.g. "nvidia-rtx4090-x86") or "" if detection fails.
// Uses catalog matching for precise identification; falls back to "Arch-CPUArch" if no catalog.
func detectHWProfile(ctx context.Context, cat *knowledge.Catalog) string {
	hw, err := hal.Detect(ctx)
	if err != nil || hw.GPU == nil {
		return ""
	}
	if cat != nil {
		hwInfo := knowledge.HardwareInfo{
			GPUArch:    hw.GPU.Arch,
			GPUVRAMMiB: hw.GPU.VRAMMiB,
			CPUArch:    hw.CPU.Arch,
		}
		if hp := cat.MatchHardwareProfile(hwInfo); hp != nil {
			return hp.Metadata.Name
		}
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
func buildNativeRuntime(dataDir string, engineAssets []knowledge.EngineAsset) runtime.Runtime {
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
		runtime.WithNativeEngineAssets(engineAssets),
	)
}

type llmSettings struct {
	Endpoint    string
	Model       string
	APIKey      string
	UserAgent   string
	ExtraParams map[string]any
}

// buildLLMClient creates an OpenAI-compatible LLM client for the Go Agent.
// Endpoint defaults to localhost proxy; model auto-discovered from /v1/models.
func buildLLMClient(ctx context.Context, db *state.DB) *agent.OpenAIClient {
	settings := loadLLMSettings(ctx, db)
	opts := []agent.OpenAIOption{agent.WithDiscoverFunc(discoverFleetLLM)}
	if settings.Model != "" {
		opts = append(opts, agent.WithModel(settings.Model))
	}
	if settings.APIKey != "" {
		opts = append(opts, agent.WithAPIKey(settings.APIKey))
	}
	if settings.UserAgent != "" {
		opts = append(opts, agent.WithUserAgent(settings.UserAgent))
	}
	if settings.ExtraParams != nil {
		opts = append(opts, agent.WithExtraParams(settings.ExtraParams))
	}
	return agent.NewOpenAIClient(settings.Endpoint, opts...)
}

func agentAvailable(ctx context.Context, llmClient *agent.OpenAIClient) bool {
	if llmClient == nil {
		return false
	}
	return llmClient.Available(ctx)
}

func loadLLMSettings(ctx context.Context, db *state.DB) llmSettings {
	settings := llmSettings{
		Endpoint: fmt.Sprintf("http://localhost:%d/v1", proxy.DefaultPort),
	}
	if endpoint := os.Getenv("AIMA_LLM_ENDPOINT"); endpoint != "" {
		settings.Endpoint = endpoint
	} else if v, err := db.GetConfig(ctx, "llm.endpoint"); err == nil && v != "" {
		settings.Endpoint = v
	}
	if model := os.Getenv("AIMA_LLM_MODEL"); model != "" {
		settings.Model = model
	} else if v, err := db.GetConfig(ctx, "llm.model"); err == nil && v != "" {
		settings.Model = v
	}
	if apiKey := os.Getenv("AIMA_API_KEY"); apiKey != "" {
		settings.APIKey = apiKey
	} else if v, err := db.GetConfig(ctx, "llm.api_key"); err == nil && v != "" {
		settings.APIKey = v
	}
	if userAgent := os.Getenv("AIMA_LLM_USER_AGENT"); userAgent != "" {
		settings.UserAgent = userAgent
	} else if v, err := db.GetConfig(ctx, "llm.user_agent"); err == nil && v != "" {
		settings.UserAgent = v
	}
	if extra := os.Getenv("AIMA_LLM_EXTRA_PARAMS"); extra != "" {
		settings.ExtraParams = parseExtraParams(extra)
	} else if v, err := db.GetConfig(ctx, "llm.extra_params"); err == nil && v != "" {
		settings.ExtraParams = parseExtraParams(v)
	}
	return settings
}

func seedCatalogOpenQuestions(ctx context.Context, db *state.DB, cat *knowledge.Catalog) error {
	for _, ea := range cat.EngineAssets {
		for _, oq := range ea.OpenQuestions {
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(ea.Metadata.Name+":"+oq.Question)))[:16]
			status := strings.TrimSpace(oq.Status)
			if status == "" {
				status = "untested"
			}
			if err := db.UpsertOpenQuestion(ctx, id, "engine:"+ea.Metadata.Name, oq.Question, oq.TestMethod, oq.Hypothesis, status, oq.Finding); err != nil {
				return fmt.Errorf("seed engine open question %s: %w", ea.Metadata.Name, err)
			}
		}
	}
	for _, sc := range cat.StackComponents {
		for _, oq := range sc.OpenQuestions {
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(sc.Metadata.Name+":"+oq.Question)))[:16]
			status := strings.TrimSpace(oq.Status)
			if status == "" {
				status = "untested"
			}
			if err := db.UpsertOpenQuestion(ctx, id, "stack:"+sc.Metadata.Name, oq.Question, oq.TestMethod, oq.Hypothesis, status, oq.Finding); err != nil {
				return fmt.Errorf("seed stack open question %s: %w", sc.Metadata.Name, err)
			}
		}
	}
	return nil
}

func isLocalLLMEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	return strings.EqualFold(host, "localhost") || proxy.IsLocalIP(host)
}

func discoverDefaultLLMModel(ctx context.Context, settings llmSettings) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, settings.Endpoint+"/models", nil)
	if err != nil {
		return "", fmt.Errorf("create models request: %w", err)
	}
	if settings.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+settings.APIKey)
	}
	if settings.UserAgent != "" {
		req.Header.Set("User-Agent", settings.UserAgent)
	}

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("models endpoint: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &models); err != nil {
		return "", fmt.Errorf("decode models: %w", err)
	}
	if len(models.Data) == 0 || models.Data[0].ID == "" {
		return "", fmt.Errorf("no models available at %s/models", settings.Endpoint)
	}
	return models.Data[0].ID, nil
}

// parseExtraParams parses a JSON string into a map for LLM extra parameters.
func parseExtraParams(s string) map[string]any {
	m, err := parseExtraParamsStrict(s)
	if err != nil {
		slog.Warn("invalid llm.extra_params JSON, ignoring", "error", err)
		return nil
	}
	return m
}

func parseExtraParamsStrict(s string) (map[string]any, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("llm.extra_params must be a JSON object: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("llm.extra_params must be a JSON object")
	}
	return m, nil
}

// discoverFleetLLM discovers LLM endpoints from fleet devices via mDNS.
// Called lazily by OpenAIClient when local endpoint has no models.
func discoverFleetLLM(ctx context.Context, apiKey string) []agent.FleetEndpoint {
	services, err := proxy.Discover(ctx, 3*time.Second)
	if err != nil {
		slog.Debug("fleet LLM discovery: mDNS failed", "error", err)
		return nil
	}

	var endpoints []agent.FleetEndpoint
	for _, svc := range services {
		addr := svc.AddrV4
		if addr == "" {
			addr = svc.Host
		}
		if addr == "" {
			continue
		}
		if proxy.IsLocalIP(addr) {
			continue
		}
		models := proxy.QueryRemoteModels(ctx, addr, svc.Port, apiKey)
		if len(models) == 0 {
			continue
		}
		baseURL := fmt.Sprintf("http://%s:%d/v1", addr, svc.Port)
		slog.Debug("fleet LLM discovery: candidate", "addr", baseURL, "models", models)
		endpoints = append(endpoints, agent.FleetEndpoint{
			BaseURL: baseURL,
			Model:   models[0],
		})
	}
	return endpoints
}

// selectDefaultRuntime picks the best available runtime: K3S > Docker > Native.
func selectDefaultRuntime(k3sRt, dockerRt, nativeRt runtime.Runtime) runtime.Runtime {
	if k3sRt != nil {
		return k3sRt
	}
	if dockerRt != nil {
		return dockerRt
	}
	return nativeRt
}

// pickRuntimeForDeployment selects the runtime for a specific deployment based on
// the engine's runtime recommendation and available runtimes.
//
//	"native"    → nativeRt
//	"docker"    → dockerRt > nativeRt
//	"k3s"       → k3sRt > error
//	"container" → k3sRt > dockerRt (needs partition? k3s required)
//	"auto" / "" → defaultRt
func pickRuntimeForDeployment(recommendation string, k3sRt, dockerRt, nativeRt, defaultRt runtime.Runtime, hasPartition bool) (runtime.Runtime, error) {
	switch recommendation {
	case "native":
		return nativeRt, nil
	case "docker":
		if dockerRt != nil {
			return dockerRt, nil
		}
		return nativeRt, nil
	case "k3s":
		if k3sRt != nil {
			return k3sRt, nil
		}
		return nil, fmt.Errorf("K3S runtime required but not available. Run 'aima init --k3s' to install")
	case "container":
		if hasPartition {
			if k3sRt != nil {
				return k3sRt, nil
			}
			return nil, fmt.Errorf("GPU partitioning requires K3S. Run 'aima init --k3s' to install")
		}
		if k3sRt != nil {
			return k3sRt, nil
		}
		if dockerRt != nil {
			return dockerRt, nil
		}
		return nativeRt, nil
	default: // "auto" or ""
		return defaultRt, nil
	}
}

// listAllRuntimes aggregates deployment lists from all available runtimes.
func listAllRuntimes(ctx context.Context, rts ...runtime.Runtime) []*runtime.DeploymentStatus {
	var all []*runtime.DeploymentStatus
	seen := make(map[string]bool)
	for _, r := range rts {
		if r == nil {
			continue
		}
		// Deduplicate runtimes (e.g., nativeRt == rt).
		name := fmt.Sprintf("%p", r)
		if seen[name] {
			continue
		}
		seen[name] = true
		if deps, err := r.List(ctx); err == nil {
			all = append(all, deps...)
		}
	}
	return all
}

func findExistingDeployment(ctx context.Context, query string, rts ...runtime.Runtime) *runtime.DeploymentStatus {
	for _, r := range rts {
		if r == nil {
			continue
		}
		if status, err := r.Status(ctx, query); err == nil {
			return status
		}
	}
	for _, d := range listAllRuntimes(ctx, rts...) {
		if deploymentMatchesQuery(d, query) {
			return d
		}
	}
	return nil
}

func catalogSize(cat *knowledge.Catalog) int {
	return len(cat.EngineProfiles) + len(cat.HardwareProfiles) + len(cat.EngineAssets) + len(cat.ModelAssets) + len(cat.PartitionStrategies) + len(cat.StackComponents)
}

const catalogDigestConfigKey = "catalog.digest.sha256"

// syncCatalogToSQLite avoids full static-knowledge rewrites when catalog content
// is unchanged. This shortens startup and reduces SQLite write lock contention.
func syncCatalogToSQLite(ctx context.Context, db *state.DB, cat *knowledge.Catalog) error {
	digest, err := catalogDigest(cat)
	if err != nil {
		return fmt.Errorf("compute catalog digest: %w", err)
	}

	prevDigest, err := db.GetConfig(ctx, catalogDigestConfigKey)
	if err == nil && prevDigest == digest {
		// Guard against stale config key: only skip reload when static tables exist.
		if staticKnowledgeLoaded(ctx, db.RawDB()) {
			return nil
		}
	}

	if err := knowledge.LoadToSQLite(ctx, db.RawDB(), cat); err != nil {
		return fmt.Errorf("load knowledge to sqlite: %w", err)
	}
	if err := db.SetConfig(ctx, catalogDigestConfigKey, digest); err != nil {
		return fmt.Errorf("set catalog digest: %w", err)
	}
	return nil
}

func catalogDigest(cat *knowledge.Catalog) (string, error) {
	data, err := yaml.Marshal(cat)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

func staticKnowledgeLoaded(ctx context.Context, sqlDB *sql.DB) bool {
	var count int
	if err := sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM hardware_profiles").Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// buildHardwareInfo creates a HardwareInfo with platform, runtime, and hardware awareness.
// splitImageRef splits a container image reference into name and tag,
// handling registry:port/image:tag correctly by finding the last colon
// after the last slash.
func splitImageRef(ref string) (name, tag string) {
	// Find the last slash to isolate the image+tag portion from registry:port
	slashIdx := strings.LastIndex(ref, "/")
	afterSlash := ref
	if slashIdx >= 0 {
		afterSlash = ref[slashIdx+1:]
	}
	colonIdx := strings.LastIndex(afterSlash, ":")
	if colonIdx < 0 {
		return ref, "latest"
	}
	// colonIdx is relative to afterSlash; convert to absolute position
	absColon := colonIdx
	if slashIdx >= 0 {
		absColon = slashIdx + 1 + colonIdx
	}
	return ref[:absColon], ref[absColon+1:]
}

type deployOptions struct {
	allowAutoPull bool
}

type deployOptionsKey struct{}

func withDeployAutoPull(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, deployOptionsKey{}, deployOptions{allowAutoPull: allow})
}

func deployAutoPullAllowed(ctx context.Context) bool {
	opts, ok := ctx.Value(deployOptionsKey{}).(deployOptions)
	if !ok {
		return true
	}
	return opts.allowAutoPull
}

type containerCompatibilityPlan struct {
	RepairInitCommands []string
	DockerImageChanged bool
}

type dockerOnlyRunner struct {
	base engine.CommandRunner
}

func (r *dockerOnlyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "crictl" || (name == "k3s" && len(args) > 0 && args[0] == "crictl") {
		return nil, fmt.Errorf("crictl disabled for docker-only operation")
	}
	return r.base.Run(ctx, name, args...)
}

func (r *dockerOnlyRunner) Pipe(ctx context.Context, from, to []string) error {
	return r.base.Pipe(ctx, from, to)
}

func (r *dockerOnlyRunner) RunStream(ctx context.Context, onLine func(line string), name string, args ...string) error {
	if name == "crictl" || (name == "k3s" && len(args) > 0 && args[0] == "crictl") {
		return fmt.Errorf("crictl disabled for docker-only operation")
	}
	return r.base.RunStream(ctx, onLine, name, args...)
}

func shouldRunContainerCompatibilityProbe(resolved *knowledge.ResolvedConfig, runtimeName, modelPath string) bool {
	if resolved == nil || resolved.EngineImage == "" || resolved.CompatibilityProbe == "" || modelPath == "" {
		return false
	}
	if runtimeName != "docker" && runtimeName != "k3s" {
		return false
	}
	if resolved.Source != nil {
		return false
	}
	if resolved.ModelFormat != "" && !strings.EqualFold(resolved.ModelFormat, "safetensors") {
		return false
	}
	if fi, err := os.Stat(modelPath); err == nil && !fi.IsDir() {
		return false
	}
	return true
}

func prepareContainerCompatibility(
	ctx context.Context,
	runner engine.CommandRunner,
	allowAutoPull bool,
	runtimeName string,
	modelPath string,
	resolved *knowledge.ResolvedConfig,
) (containerCompatibilityPlan, error) {
	var plan containerCompatibilityPlan
	if !shouldRunContainerCompatibilityProbe(resolved, runtimeName, modelPath) {
		return plan, nil
	}
	if !dockerAvailableForCompatibilityProbe(ctx, runner) {
		slog.Warn("container compatibility probe skipped: Docker unavailable",
			"model", resolved.ModelName,
			"engine_image", resolved.EngineImage)
		return plan, nil
	}

	dockerReady, dockerChanged, err := ensureDockerImageForCompatibilityProbe(ctx, runner, allowAutoPull, resolved)
	if err != nil {
		slog.Warn("container compatibility probe setup failed",
			"model", resolved.ModelName,
			"engine_image", resolved.EngineImage,
			"error", err)
		return plan, nil
	}
	if !dockerReady {
		slog.Warn("container compatibility probe skipped: image unavailable in Docker",
			"model", resolved.ModelName,
			"engine_image", resolved.EngineImage)
		return plan, nil
	}
	plan.DockerImageChanged = dockerChanged

	trustRemoteCode := configBoolValue(resolved.Config, "trust_remote_code")
	probeOutput, probeErr := timedContainerCompatibilityProbe(ctx, runner, resolved.CompatibilityProbe, resolved.EngineImage, modelPath, trustRemoteCode, nil)
	if probeErr == nil {
		return plan, nil
	}

	probeSummary := summarizeCompatibilityProbeOutput(probeOutput, probeErr)
	if allowAutoPull && len(resolved.EngineRegistries) > 0 && !strings.EqualFold(resolved.EngineDistribution, "local") {
		if refreshErr := refreshDockerImageForCompatibilityProbe(ctx, runner, resolved); refreshErr == nil {
			plan.DockerImageChanged = true
			refreshedOutput, refreshedErr := timedContainerCompatibilityProbe(ctx, runner, resolved.CompatibilityProbe, resolved.EngineImage, modelPath, trustRemoteCode, nil)
			if refreshedErr == nil {
				slog.Info("container compatibility resolved after image refresh",
					"model", resolved.ModelName,
					"engine_image", resolved.EngineImage)
				return plan, nil
			}
			if refreshedSummary := summarizeCompatibilityProbeOutput(refreshedOutput, refreshedErr); moreSpecificFailure(refreshedSummary, probeSummary) || probeSummary == "" {
				probeSummary = refreshedSummary
			}
		} else {
			slog.Warn("container compatibility image refresh failed",
				"model", resolved.ModelName,
				"engine_image", resolved.EngineImage,
				"error", refreshErr)
		}
	}

	if len(resolved.RepairInitCommands) == 0 {
		return plan, fmt.Errorf("container compatibility check failed for %s with %s: %s", resolved.ModelName, resolved.EngineImage, probeSummary)
	}

	repairedOutput, repairedErr := timedContainerCompatibilityProbe(ctx, runner, resolved.CompatibilityProbe, resolved.EngineImage, modelPath, trustRemoteCode, resolved.RepairInitCommands)
	if repairedErr != nil {
		return plan, fmt.Errorf("container compatibility check failed for %s with %s: %s; auto-repair failed: %s",
			resolved.ModelName,
			resolved.EngineImage,
			probeSummary,
			summarizeCompatibilityProbeOutput(repairedOutput, repairedErr))
	}

	slog.Info("container compatibility auto-repair selected",
		"model", resolved.ModelName,
		"engine_image", resolved.EngineImage,
		"commands", len(resolved.RepairInitCommands))
	plan.RepairInitCommands = append([]string(nil), resolved.RepairInitCommands...)
	return plan, nil
}

func dockerAvailableForCompatibilityProbe(ctx context.Context, runner engine.CommandRunner) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := runner.Run(probeCtx, "docker", "version", "--format", "{{.Server.Version}}")
	return err == nil
}

func ensureDockerImageForCompatibilityProbe(ctx context.Context, runner engine.CommandRunner, allowAutoPull bool, resolved *knowledge.ResolvedConfig) (available bool, changed bool, err error) {
	if resolved == nil || resolved.EngineImage == "" {
		return false, false, nil
	}
	if engine.ImageExistsInDocker(ctx, resolved.EngineImage, runner) {
		return true, false, nil
	}
	if !allowAutoPull || len(resolved.EngineRegistries) == 0 || strings.EqualFold(resolved.EngineDistribution, "local") {
		return false, false, nil
	}
	if err := pullDockerImageForCompatibilityProbe(ctx, runner, resolved); err != nil {
		return false, false, err
	}
	return true, true, nil
}

func refreshDockerImageForCompatibilityProbe(ctx context.Context, runner engine.CommandRunner, resolved *knowledge.ResolvedConfig) error {
	if resolved == nil || resolved.EngineImage == "" {
		return nil
	}
	return pullDockerImageForCompatibilityProbe(ctx, runner, resolved)
}

func pullDockerImageForCompatibilityProbe(ctx context.Context, runner engine.CommandRunner, resolved *knowledge.ResolvedConfig) error {
	if resolved == nil {
		return fmt.Errorf("resolved config is nil")
	}
	if len(resolved.EngineRegistries) == 0 {
		return fmt.Errorf("no registries configured for %s", resolved.EngineImage)
	}
	imgName, imgTag := splitImageRef(resolved.EngineImage)
	if err := engine.Pull(ctx, engine.PullOptions{
		Image:          imgName,
		Tag:            imgTag,
		Registries:     resolved.EngineRegistries,
		Runner:         &dockerOnlyRunner{base: runner},
		ExpectedDigest: resolved.EngineDigest,
	}); err != nil {
		return err
	}
	return ensureDockerImageAlias(ctx, runner, resolved.EngineImage, resolved.EngineRegistries)
}

func ensureDockerImageAlias(ctx context.Context, runner engine.CommandRunner, image string, registries []string) error {
	if image == "" || engine.ImageExistsInDocker(ctx, image, runner) {
		return nil
	}
	name, tag := splitImageRef(image)
	for _, registry := range registries {
		pulledRef := buildRegistryImageRef(registry, name, tag)
		if !engine.ImageExistsInDocker(ctx, pulledRef, runner) {
			continue
		}
		if _, err := runner.Run(ctx, "docker", "tag", pulledRef, image); err != nil {
			return fmt.Errorf("docker tag %s %s: %w", pulledRef, image, err)
		}
		if engine.ImageExistsInDocker(ctx, image, runner) {
			return nil
		}
	}
	if engine.ImageExistsInDocker(ctx, image, runner) {
		return nil
	}
	return fmt.Errorf("image %s is not available under the expected local tag", image)
}

func buildRegistryImageRef(registry, image, tag string) string {
	registry = strings.TrimSuffix(registry, "/")
	image = strings.TrimSuffix(image, "/")
	baseName := filepath.Base(image)
	if registry == image || strings.HasSuffix(registry, "/"+baseName) {
		return fmt.Sprintf("%s:%s", registry, tag)
	}
	if strings.Contains(registry, "/") {
		return fmt.Sprintf("%s/%s:%s", registry, baseName, tag)
	}
	return fmt.Sprintf("%s/%s:%s", registry, image, tag)
}

func timedContainerCompatibilityProbe(
	ctx context.Context,
	runner engine.CommandRunner,
	probeType string,
	image string,
	modelPath string,
	trustRemoteCode bool,
	repairInitCommands []string,
) (string, error) {
	timeout := 2 * time.Minute
	if len(repairInitCommands) > 0 {
		timeout = 10 * time.Minute
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runContainerCompatibilityProbe(probeCtx, runner, probeType, image, modelPath, trustRemoteCode, repairInitCommands)
}

func runContainerCompatibilityProbe(
	ctx context.Context,
	runner engine.CommandRunner,
	probeType string,
	image string,
	modelPath string,
	trustRemoteCode bool,
	repairInitCommands []string,
) (string, error) {
	pythonCode, ok := compatibilityProbePython(probeType)
	if !ok {
		return "", fmt.Errorf("unknown compatibility probe %q", probeType)
	}

	args := []string{
		"run", "--rm",
		"--env", "AIMA_MODEL_PATH=/models",
		"--env", "AIMA_TRUST_REMOTE_CODE=" + strconv.FormatBool(trustRemoteCode),
		"--volume", modelPath + ":/models:ro",
	}
	if len(repairInitCommands) == 0 {
		args = append(args, "--entrypoint", "python3", image, "-c", pythonCode)
	} else {
		shellScript := strings.Join(append(append([]string(nil), repairInitCommands...), "python3 -c "+strconv.Quote(pythonCode)), " && ")
		args = append(args, "--entrypoint", "sh", image, "-c", shellScript)
	}

	out, err := runner.Run(ctx, "docker", args...)
	return string(out), err
}

func compatibilityProbePython(probeType string) (string, bool) {
	switch strings.TrimSpace(probeType) {
	case "transformers_autoconfig":
		return `import os, transformers; from pathlib import Path; from transformers import AutoConfig, AutoProcessor; model_path = os.environ.get("AIMA_MODEL_PATH", "/models"); trust_remote_code = os.environ.get("AIMA_TRUST_REMOTE_CODE", "false").lower() == "true"; cfg = AutoConfig.from_pretrained(model_path, local_files_only=True, trust_remote_code=trust_remote_code); model_dir = Path(model_path); processor_name = type(AutoProcessor.from_pretrained(model_path, local_files_only=True, trust_remote_code=trust_remote_code)).__name__ if any((model_dir / name).exists() for name in ("processor_config.json", "preprocessor_config.json")) else "none"; print("AIMA_COMPAT_OK transformers=%s model_type=%s processor=%s" % (transformers.__version__, getattr(cfg, "model_type", "unknown"), processor_name))`, true
	case "qwen_asr_autoconfig":
		return `import os, qwen_asr, transformers; from transformers import AutoConfig; model_path = os.environ.get("AIMA_MODEL_PATH", "/models"); trust_remote_code = os.environ.get("AIMA_TRUST_REMOTE_CODE", "false").lower() == "true"; cfg = AutoConfig.from_pretrained(model_path, local_files_only=True, trust_remote_code=trust_remote_code); print("AIMA_COMPAT_OK transformers=%s model_type=%s qwen_asr=%s" % (transformers.__version__, getattr(cfg, "model_type", "unknown"), getattr(qwen_asr, "__version__", "unknown")))`, true
	default:
		return "", false
	}
}

func configBoolValue(config map[string]any, key string) bool {
	if config == nil {
		return false
	}
	raw, ok := config[key]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && parsed
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case json.Number:
		n, err := v.Int64()
		return err == nil && n != 0
	default:
		return false
	}
}

func summarizeCompatibilityProbeOutput(output string, err error) string {
	if detail := summarizeErrorLines(output); detail != "" && detail != "unknown startup failure" {
		return detail
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	if err != nil {
		return err.Error()
	}
	return "compatibility probe failed"
}

func stringInSliceFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func imageSupportsPlatform(ea *knowledge.EngineAsset, platform string) bool {
	if ea == nil || ea.Image.Name == "" {
		return false
	}
	if platform == "" {
		return true
	}
	return len(ea.Image.Platforms) == 0 || stringInSliceFold(ea.Image.Platforms, platform)
}

func engineMatchesHardware(ea *knowledge.EngineAsset, hw knowledge.HardwareInfo) bool {
	if ea == nil {
		return false
	}
	arch := strings.TrimSpace(ea.Hardware.GPUArch)
	return arch == "" || arch == "*" || strings.EqualFold(arch, hw.GPUArch)
}

func engineSupportsPlatform(ea *knowledge.EngineAsset, platform string) bool {
	if ea == nil || platform == "" {
		return ea != nil
	}
	if ea.Source != nil && ea.Source.Supports(platform) {
		return true
	}
	return imageSupportsPlatform(ea, platform)
}

func engineCompatibleWithHost(ea *knowledge.EngineAsset, hw knowledge.HardwareInfo) bool {
	return engineMatchesHardware(ea, hw) && engineSupportsPlatform(ea, hw.Platform)
}

func preferredEngineRuntimeType(ea *knowledge.EngineAsset, platform string) string {
	if ea == nil {
		return "container"
	}

	recommendation := ea.Runtime.Default
	if platform != "" {
		if rec, ok := ea.Runtime.PlatformRecommendations[platform]; ok && rec != "" {
			recommendation = rec
		}
	}

	switch recommendation {
	case "native":
		if ea.Source != nil && (platform == "" || ea.Source.Supports(platform)) {
			return "native"
		}
		if imageSupportsPlatform(ea, platform) {
			return "container"
		}
	case "container":
		if imageSupportsPlatform(ea, platform) {
			return "container"
		}
		if ea.Source != nil && (platform == "" || ea.Source.Supports(platform)) {
			return "native"
		}
	}

	if ea.Source != nil && (platform == "" || ea.Source.Supports(platform)) {
		return "native"
	}
	if imageSupportsPlatform(ea, platform) {
		return "container"
	}
	return "container"
}

func requiresRootImportForK3S(inContainerd, inDocker, isRoot bool) bool {
	return inDocker && !inContainerd && !isRoot
}

func shouldFallbackToDockerRuntime(runtimeName string, hasPartition, inContainerd, inDocker, isRoot bool, dockerAvailable bool) bool {
	return runtimeName == "k3s" &&
		dockerAvailable &&
		!hasPartition &&
		requiresRootImportForK3S(inContainerd, inDocker, isRoot)
}

func k3sDockerImportHint(image string) string {
	return fmt.Sprintf("engine image %s exists in Docker but not in K3S containerd; import requires root (sudo docker save %s | sudo k3s ctr -n k8s.io images import -)", image, image)
}

func k3sDockerFallbackWarning(image string) string {
	return fmt.Sprintf("engine image %s is available in Docker but not K3S containerd; using Docker runtime because importing into containerd requires root", image)
}

func installedRuntimeTypesForEngine(installed []*state.Engine, engineName, engineType string) []string {
	keys := map[string]bool{
		strings.ToLower(engineName): true,
		strings.ToLower(engineType): true,
	}
	set := make(map[string]bool)
	for _, e := range installed {
		if e == nil {
			continue
		}
		if keys[strings.ToLower(e.ID)] || keys[strings.ToLower(e.Type)] {
			if e.RuntimeType != "" {
				set[e.RuntimeType] = true
			}
		}
	}
	runtimeTypes := make([]string, 0, len(set))
	for rt := range set {
		runtimeTypes = append(runtimeTypes, rt)
	}
	sort.Strings(runtimeTypes)
	return runtimeTypes
}

func defaultEngineAsset(cat *knowledge.Catalog, hw knowledge.HardwareInfo) *knowledge.EngineAsset {
	if cat == nil {
		return nil
	}
	if name := cat.DefaultEngine(); name != "" {
		if ea := cat.FindEngineByName(name, hw); engineCompatibleWithHost(ea, hw) {
			return ea
		}
	}
	for i := range cat.EngineAssets {
		ea := &cat.EngineAssets[i]
		if ea.Metadata.Default && engineCompatibleWithHost(ea, hw) {
			return ea
		}
	}
	for i := range cat.EngineAssets {
		ea := &cat.EngineAssets[i]
		if engineCompatibleWithHost(ea, hw) {
			return ea
		}
	}
	return nil
}

// scenarioWaitForReady waits for a deployed model to become ready before proceeding.
// waitFor: "health_check" polls deploy.status, "port_open" probes the returned address, "" defaults to 2s sleep.
// On timeout, returns an error (caller treats as warning, continues deployment).
func scenarioWaitForReady(ctx context.Context, query, waitFor string, timeoutS int, deployStatus func(context.Context, string) (json.RawMessage, error)) error {
	if waitFor == "" || timeoutS <= 0 {
		time.Sleep(2 * time.Second)
		return nil
	}
	if deployStatus == nil {
		return fmt.Errorf("deploy.status not available for wait_for=%q", waitFor)
	}

	switch waitFor {
	case "health_check", "port_open":
	default:
		return fmt.Errorf("unknown wait_for %q", waitFor)
	}

	checkReady := func() (bool, error) {
		data, err := deployStatus(ctx, query)
		if err != nil {
			return false, nil
		}
		var s struct {
			Phase          string `json:"phase"`
			Ready          bool   `json:"ready"`
			Address        string `json:"address"`
			Message        string `json:"message,omitempty"`
			StartupMessage string `json:"startup_message,omitempty"`
		}
		if err := json.Unmarshal(data, &s); err != nil {
			return false, nil
		}
		if s.Phase == "failed" {
			msg := s.Message
			if msg == "" {
				msg = s.StartupMessage
			}
			if msg == "" {
				msg = "deployment reported failed phase"
			}
			return false, fmt.Errorf("deployment %s failed: %s", query, msg)
		}
		switch waitFor {
		case "health_check":
			return s.Ready, nil
		case "port_open":
			if s.Address == "" {
				return false, nil
			}
			conn, err := net.DialTimeout("tcp", s.Address, time.Second)
			if err != nil {
				return false, nil
			}
			conn.Close()
			return true, nil
		default:
			return false, nil
		}
	}

	if ready, err := checkReady(); ready || err != nil {
		return err
	}

	timer := time.NewTimer(time.Duration(timeoutS) * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timeout after %ds waiting for %s (%s)", timeoutS, query, waitFor)
		case <-ticker.C:
			if ready, err := checkReady(); ready || err != nil {
				return err
			}
		}
	}
}

// Populates both static fields (from hal.Detect) and dynamic fields (from hal.CollectMetrics).
// Missing data results in zero values, which downstream functions treat as "unknown" and skip.
func buildHardwareInfo(ctx context.Context, cat *knowledge.Catalog, rtName string) knowledge.HardwareInfo {
	hwInfo := knowledge.HardwareInfo{
		Platform:    goruntime.GOOS + "/" + goruntime.GOARCH,
		RuntimeType: rtName,
	}
	if hw, err := hal.Detect(ctx); err == nil {
		if hw.GPU != nil {
			hwInfo.GPUArch = hw.GPU.Arch
			hwInfo.GPUModel = hw.GPU.Name
			hwInfo.GPUVRAMMiB = hw.GPU.VRAMMiB
			hwInfo.GPUCount = hw.GPU.Count
			hwInfo.UnifiedMemory = hw.GPU.UnifiedMemory
		}
		hwInfo.CPUArch = hw.CPU.Arch
		hwInfo.CPUCores = hw.CPU.Cores
		hwInfo.RAMTotalMiB = hw.RAM.TotalMiB
		hwInfo.RAMAvailMiB = hw.RAM.AvailableMiB
		hwInfo.SwapTotalMiB = hw.RAM.SwapTotalMiB
	}
	// Dynamic layer: collect runtime GPU metrics (failure is non-fatal)
	if m, err := hal.CollectMetrics(ctx); err == nil && m.GPU != nil {
		hwInfo.GPUMemUsedMiB = m.GPU.MemoryUsedMiB
		hwInfo.GPUMemFreeMiB = m.GPU.MemoryTotalMiB - m.GPU.MemoryUsedMiB
	}
	// Match to specific hardware profile and populate TDP
	if cat != nil {
		if hp := cat.MatchHardwareProfile(hwInfo); hp != nil {
			hwInfo.HardwareProfile = hp.Metadata.Name
		}
		hwInfo.TDPWatts = cat.FindHardwareTDP(hwInfo)
	}
	return hwInfo
}

// resolveWithFallback tries catalog resolution first; on "not found in catalog",
// falls back to building a synthetic ModelAsset from the model's DB scan record.
func resolveWithFallback(ctx context.Context, cat *knowledge.Catalog, db *state.DB, hw knowledge.HardwareInfo, modelName, engineType string, overrides map[string]any, dataDir string, opts ...knowledge.ResolveOption) (*knowledge.ResolvedConfig, string, error) {
	resolved, err := cat.Resolve(hw, modelName, engineType, overrides, opts...)
	if err == nil {
		// Catalog hit — but ModelPath may be empty if no override was given.
		// Look up DB for the actual registered path from scan/import.
		if resolved.ModelPath == "" {
			if dbModel, dbErr := db.FindModelByName(ctx, modelName); dbErr == nil && dbModel.Path != "" {
				if model.PathLooksCompatible(dbModel.Path, dbModel.Format, resolvedQuantizationHint(resolved)) {
					resolved.ModelPath = dbModel.Path
				} else {
					slog.Warn("ignoring incompatible scanned model path",
						"model", modelName,
						"path", dbModel.Path,
						"format", dbModel.Format,
						"detected_quantization", dbModel.Quantization,
						"expected_quantization", resolvedQuantizationHint(resolved))
				}
			}
		}
		return resolved, resolved.ModelName, nil
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

	synth := cat.BuildSyntheticModelAsset(
		dbModel.Name, dbModel.Type, dbModel.DetectedArch, dbModel.DetectedParams, dbModel.Format, engineType)
	cat.RegisterModel(synth)

	if overrides == nil {
		overrides = map[string]any{}
	}
	overrides["model_path"] = dbModel.Path

	resolved, err = cat.Resolve(hw, dbModel.Name, engineType, overrides, opts...)
	if err != nil {
		return nil, "", fmt.Errorf("resolve auto-detected config for %s: %w", dbModel.Name, err)
	}
	return resolved, dbModel.Name, nil
}

// resolvedDeployment holds the shared result of resolve + CheckFit,
// used by both DeployApply and DeployDryRun.
type resolvedDeployment struct {
	ModelName string
	Resolved  *knowledge.ResolvedConfig
	Fit       *knowledge.FitReport
}

// queryGoldenOverrides returns config overrides from the best golden configuration
// matching the given hardware/engine/model. Returns nil if no golden config found
// or if hwProfile is empty (to prevent cross-hardware injection).
func queryGoldenOverrides(ctx context.Context, kStore *knowledge.Store, hwProfile, engineType, modelName string) map[string]any {
	if kStore == nil || hwProfile == "" {
		return nil
	}
	resp, err := kStore.Search(ctx, knowledge.SearchParams{
		Hardware: hwProfile,
		Engine:   engineType,
		Model:    modelName,
		Status:   "golden",
		SortBy:   "throughput",
		Limit:    1,
	})
	if err != nil || len(resp.Results) == 0 {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(resp.Results[0].Config, &cfg); err != nil {
		return nil
	}
	if len(cfg) == 0 {
		return nil
	}
	slog.Info("L2 golden config found",
		"config_id", resp.Results[0].ConfigID,
		"keys", len(cfg))
	return cfg
}

// resolveDeployment performs the common resolve → CheckFit sequence.
// Runtime selection is done separately by callers via pickRuntimeForDeployment.
func resolveDeployment(ctx context.Context, cat *knowledge.Catalog, db *state.DB, kStore *knowledge.Store, hwInfo knowledge.HardwareInfo, modelName, engineType, slot string, overrides map[string]any, dataDir string) (*resolvedDeployment, error) {
	if overrides == nil {
		overrides = map[string]any{}
	}
	if slot != "" {
		overrides["slot"] = slot
	}

	// Extract deployment constraints (not config params)
	var resolveOpts []knowledge.ResolveOption
	if mcs, ok := overrides["max_cold_start_s"]; ok {
		var v int
		switch x := mcs.(type) {
		case float64:
			v = int(x)
		case int:
			v = x
		case json.Number:
			if n, err := x.Int64(); err == nil {
				v = int(n)
			}
		}
		if v > 0 {
			resolveOpts = append(resolveOpts, knowledge.WithMaxColdStart(v))
		}
		delete(overrides, "max_cold_start_s")
	}

	// L2c: inject golden config into resolve chain (applied between L0 and L1 inside Resolve)
	resolveOpts = append(resolveOpts, knowledge.WithGoldenConfig(func(hardware, engine, model string) map[string]any {
		return queryGoldenOverrides(ctx, kStore, hardware, engine, model)
	}))

	resolved, canonicalName, err := resolveWithFallback(ctx, cat, db, hwInfo, modelName, engineType, overrides, dataDir, resolveOpts...)
	if err != nil {
		return nil, err
	}

	fit := knowledge.CheckFit(resolved, hwInfo)
	for k, v := range fit.Adjustments {
		resolved.Config[k] = v
		resolved.Provenance[k] = "L0-auto"
	}

	return &resolvedDeployment{
		ModelName: canonicalName,
		Resolved:  resolved,
		Fit:       fit,
	}, nil
}

// buildToolDeps wires all ToolDeps fields to real implementations.
// All runtime variants are provided so DeployApply can select per-deployment.
func buildToolDeps(cat *knowledge.Catalog, db *state.DB, kStore *knowledge.Store, rt runtime.Runtime, nativeRt runtime.Runtime, dockerRt runtime.Runtime, k3sRt runtime.Runtime, proxyServer *proxy.Server, k3sClient *k3s.Client, dataDir string, factoryDigests map[string]string, supportView *support.Service) *mcp.ToolDeps {
	scanEnginesCore := func(ctx context.Context, runtimeFilter string, autoImport bool) (json.RawMessage, error) {
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
	pullModelCore := func(ctx context.Context, name string) error {
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
			return nil
		}

		if resolvedVariant != nil && resolvedVariant.Source != nil && resolvedVariant.Source.Type != "local_path" {
			slog.Info("model pull: using variant source", "variant", resolvedVariant.Name, "repo", resolvedVariant.Source.Repo)
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
		if err := model.DownloadFromSource(ctx, sources, destPath, model.DownloadPlan{
			Format:       requiredFormat,
			Quantization: requiredQuantization,
		}); err != nil {
			return fmt.Errorf("download model %s: %w", name, err)
		}
		return registerPulledModel(ctx, destPath, dataDir, db)
	}

	// deployRunCore orchestrates the full run workflow: resolve → pull → deploy → wait.
	// Business logic lives here so CLI remains a thin presentation layer.
	var deps *mcp.ToolDeps
	deployRunCore := func(ctx context.Context, model, engineType, slot string, noPull bool,
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
		dryRunData, err := deps.DeployDryRun(ctx, engineType, model, slot, nil)
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
		deployData, err := deps.DeployApply(deployCtx, engineType, model, slot, nil)
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

	deps = &mcp.ToolDeps{
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
			return pullModelCore(ctx, name)
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
			// Wrap info with engine_hint derived from catalog (INV-5: MCP response is the source of truth)
			raw, err := json.Marshal(info)
			if err != nil {
				return nil, err
			}
			var result map[string]any
			json.Unmarshal(raw, &result) //nolint:errcheck
			if hint := cat.FormatToEngine(info.Format); hint != "" {
				result["engine_hint"] = hint
			}
			return json.Marshal(result)
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
				return fmt.Errorf("find model %s: %w", name, err)
			}
			// Gap 3: Save rollback snapshot before deletion
			if snap, snapErr := json.Marshal(m); snapErr == nil {
				_ = db.SaveSnapshot(ctx, &state.RollbackSnapshot{
					ToolName: "model.remove", ResourceType: "model", ResourceName: m.Name, Snapshot: string(snap),
				})
			}
			// Delete from database
			if err := db.DeleteModel(ctx, m.ID); err != nil {
				return fmt.Errorf("delete model %s from database: %w", name, err)
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
		ScanEngines: scanEnginesCore,
		ListEngines: func(ctx context.Context) (json.RawMessage, error) {
			engines, err := db.ListEngines(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(engines)
		},
		GetEngineInfo: func(ctx context.Context, name string) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			nameLower := strings.ToLower(name)

			// Catalog lookup: exact name → type+hw preference → image substring
			asset := cat.FindEngineByName(name, hwInfo)

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

			// If found only in DB, try to find the catalog asset by installed type
			if asset == nil && len(installed) > 0 {
				asset = cat.FindEngineByName(installed[0].Type, hwInfo)
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
		PullEngine: func(ctx context.Context, name string, onProgress func(engine.ProgressEvent)) error {
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			if name == "" {
				if ea := defaultEngineAsset(cat, hwInfo); ea != nil {
					name = ea.Metadata.Name
				} else {
					name = cat.DefaultEngine()
				}
			}

			ea := cat.FindEngineByName(name, hwInfo)
			if ea == nil {
				return fmt.Errorf("engine %q not found in catalog for gpu_arch %q", name, hwInfo.GPUArch)
			}

			// Local-only engines cannot be pulled from a registry
			if ea.Image.Distribution == "local" {
				return fmt.Errorf("engine %q is a locally-built image (distribution: local); build it on the target device or import with: aima engine import <tarball>", name)
			}

			// Native binary path: prefer if platform is supported
			platform := goruntime.GOOS + "/" + goruntime.GOARCH
			preferredRuntime := preferredEngineRuntimeType(ea, platform)
			if preferredRuntime == "native" && ea.Source != nil && ea.Source.Supports(platform) {
				distPlatform := goruntime.GOOS + "-" + goruntime.GOARCH
				distDir := filepath.Join(dataDir, "dist", distPlatform)
				mgr := engine.NewBinaryManager(distDir)
				_, downloaded, err := mgr.Ensure(ctx, toEngineBinarySource(ea.Source), onProgress)
				if err != nil {
					return err
				}
				// Auto-scan to register in DB
				_, _ = scanEnginesCore(ctx, "native", false)
				if !downloaded && onProgress != nil {
					onProgress(engine.ProgressEvent{Phase: "already_available", Message: "engine binary already available locally"})
				}
				return nil
			}
			// Container image path
			if ea.Image.Name != "" && imageSupportsPlatform(ea, platform) {
				fullRef := ea.Image.Name + ":" + ea.Image.Tag
				runner := &execRunner{}
				inContainerd := engine.ImageExistsInContainerd(ctx, fullRef, runner)
				inDocker := engine.ImageExistsInDocker(ctx, fullRef, runner)
				if inContainerd || inDocker {
					slog.Info("engine image already available locally", "image", fullRef, "containerd", inContainerd, "docker", inDocker)
					if rt.Name() == "k3s" && !inContainerd && inDocker {
						if os.Getuid() != 0 {
							_, _ = scanEnginesCore(ctx, "container", false)
							if dockerRt != nil {
								if onProgress != nil {
									onProgress(engine.ProgressEvent{Phase: "already_available", Message: "engine image already available in Docker; Docker runtime can use it without K3S import"})
								}
								return nil
							}
							return fmt.Errorf("%s", k3sDockerImportHint(fullRef))
						}
						if err := engine.ImportDockerToContainerd(ctx, fullRef, runner); err != nil {
							return fmt.Errorf("import existing engine image %s into containerd: %w", fullRef, err)
						}
						inContainerd = true
					}
					_, _ = scanEnginesCore(ctx, "container", false)
					if onProgress != nil {
						msg := "engine image already available locally"
						if rt.Name() == "k3s" && inContainerd && inDocker {
							msg = "engine image already available locally (docker + containerd)"
						} else if rt.Name() == "k3s" && inContainerd {
							msg = "engine image already available in K3S containerd"
						}
						onProgress(engine.ProgressEvent{Phase: "already_available", Message: msg})
					}
					return nil
				}
				if err := engine.Pull(ctx, engine.PullOptions{
					Image:      ea.Image.Name,
					Tag:        ea.Image.Tag,
					Registries: ea.Image.Registries,
					SizeHintMB: ea.Image.SizeApproxMB,
					OnProgress: onProgress,
					Runner:     &execRunner{},
				}); err != nil {
					return err
				}
				// Auto-scan to register in DB
				_, _ = scanEnginesCore(ctx, "container", false)
				return nil
			}
			if ea.Source != nil && ea.Source.Supports(platform) {
				distPlatform := goruntime.GOOS + "-" + goruntime.GOARCH
				distDir := filepath.Join(dataDir, "dist", distPlatform)
				mgr := engine.NewBinaryManager(distDir)
				_, downloaded, err := mgr.Ensure(ctx, toEngineBinarySource(ea.Source), onProgress)
				if err != nil {
					return err
				}
				_, _ = scanEnginesCore(ctx, "native", false)
				if !downloaded && onProgress != nil {
					onProgress(engine.ProgressEvent{Phase: "already_available", Message: "engine binary already available locally"})
				}
				return nil
			}
			return fmt.Errorf("engine %q has no download source for platform %s/%s", name, goruntime.GOOS, goruntime.GOARCH)
		},
		ImportEngine: func(ctx context.Context, path string) error {
			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve path %s: %w", path, err)
			}
			if err := engine.Import(ctx, absPath, &execRunner{}); err != nil {
				return fmt.Errorf("import engine from %s: %w", path, err)
			}
			// Refresh DB: imported image only visible via runtime scan
			_, _ = scanEnginesCore(ctx, "auto", false)
			return nil
		},
		RemoveEngine: func(ctx context.Context, name string, deleteFiles bool) error {
			// Save rollback snapshot before deletion
			e, getErr := db.GetEngine(ctx, name)
			if getErr == nil {
				if snap, snapErr := json.Marshal(e); snapErr == nil {
					_ = db.SaveSnapshot(ctx, &state.RollbackSnapshot{
						ToolName: "engine.remove", ResourceType: "engine", ResourceName: name, Snapshot: string(snap),
					})
				}
			}

			// Optionally clean up actual files/images
			if deleteFiles && e != nil {
				runner := &execRunner{}
				if e.RuntimeType == "native" && e.BinaryPath != "" {
					if rmErr := os.Remove(e.BinaryPath); rmErr != nil && !os.IsNotExist(rmErr) {
						slog.Warn("failed to remove engine binary", "path", e.BinaryPath, "error", rmErr)
					} else {
						slog.Info("removed engine binary", "path", e.BinaryPath)
					}
				} else if e.Image != "" {
					ref := e.Image
					if e.Tag != "" {
						ref += ":" + e.Tag
					}
					// Try docker rmi (best effort)
					if _, err := runner.Run(ctx, "docker", "rmi", ref); err != nil {
						slog.Debug("docker rmi failed (may not be in docker)", "image", ref, "error", err)
					} else {
						slog.Info("removed docker image", "image", ref)
					}
					// Try crictl/k3s rmi (best effort)
					if _, err := runner.Run(ctx, "crictl", "rmi", ref); err == nil {
						slog.Info("removed containerd image via crictl", "image", ref)
					} else if _, err := runner.Run(ctx, "k3s", "crictl", "rmi", ref); err == nil {
						slog.Info("removed containerd image via k3s crictl", "image", ref)
					}
				}
			}

			return db.DeleteEngine(ctx, name)
		},
		EnginePlan: func(ctx context.Context) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			_, _ = scanEnginesCore(ctx, "auto", false)

			// Get all installed engines from DB
			allInstalled, _ := db.ListEngines(ctx)

			type engineEntry struct {
				Name                  string   `json:"name"`
				Type                  string   `json:"type"`
				RuntimeType           string   `json:"runtime_type"` // recommended runtime on this host
				SizeMB                int      `json:"size_mb,omitempty"`
				Installed             bool     `json:"installed"`
				InstalledRuntimeTypes []string `json:"installed_runtime_types,omitempty"`
			}

			var compatible []engineEntry

			for i := range cat.EngineAssets {
				ea := &cat.EngineAssets[i]
				if !engineCompatibleWithHost(ea, hwInfo) {
					continue
				}
				rtType := preferredEngineRuntimeType(ea, hwInfo.Platform)
				installedRuntimeTypes := installedRuntimeTypesForEngine(allInstalled, ea.Metadata.Name, ea.Metadata.Type)
				installed := stringInSliceFold(installedRuntimeTypes, rtType)
				sizeMB := ea.Image.SizeApproxMB
				if rtType == "native" {
					// Native sources don't track size in catalog; use 0
					sizeMB = 0
				}

				entry := engineEntry{
					Name:                  ea.Metadata.Name,
					Type:                  ea.Metadata.Type,
					RuntimeType:           rtType,
					SizeMB:                sizeMB,
					Installed:             installed,
					InstalledRuntimeTypes: installedRuntimeTypes,
				}
				compatible = append(compatible, entry)
			}

			result := map[string]any{
				"hardware": map[string]any{
					"gpu_arch":  hwInfo.GPUArch,
					"gpu_model": hwInfo.GPUModel,
					"vram_mib":  hwInfo.GPUVRAMMiB,
					"cpu_arch":  hwInfo.CPUArch,
				},
				"compatible_engines": compatible,
			}
			return json.Marshal(result)
		},

		// Deployment (runtime abstraction: K3S or native)
		DeployApply: func(ctx context.Context, engineType, modelName, slot string, configOverrides map[string]any) (json.RawMessage, error) {
			allowAutoPull := deployAutoPullAllowed(ctx)
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			rd, err := resolveDeployment(ctx, cat, db, kStore, hwInfo, modelName, engineType, slot, configOverrides, dataDir)
			if err != nil {
				return nil, err
			}
			if !rd.Fit.Fit {
				return nil, fmt.Errorf("hardware check: %s", rd.Fit.Reason)
			}
			for _, w := range rd.Fit.Warnings {
				slog.Warn("deploy fitness", "warning", w)
			}
			modelName = rd.ModelName
			resolved := rd.Resolved

			modelPath := resolved.ModelPath
			if modelPath == "" {
				modelPath = filepath.Join(dataDir, "models", modelName)
			}
			requiredFormat := resolved.ModelFormat
			requiredQuantization := resolvedQuantizationHint(resolved)
			// Guard: if the resolved model path is empty or missing model files,
			// search alternative locations. This handles the case where aima serve
			// runs as root (HOME=/root) but deploy is invoked as a regular user,
			// so $HOME/.aima/models differs from where the model was downloaded.
			if !model.PathLooksCompatible(modelPath, requiredFormat, requiredQuantization) {
				if alt := findModelDir(modelName, dataDir, requiredFormat, requiredQuantization); alt != "" {
					slog.Info("model path fallback: using alternative location",
						"original", modelPath, "resolved", alt)
					modelPath = alt
				} else {
					if !allowAutoPull {
						return nil, fmt.Errorf("model %s not found locally and auto-pull is disabled", modelName)
					}
					slog.Info("model not found locally, auto-pulling", "model", modelName)
					if pullErr := pullModelCore(ctx, modelName); pullErr != nil {
						return nil, fmt.Errorf("auto-pull model %s: %w", modelName, pullErr)
					}
					// Re-resolve model path after download
					modelPath = filepath.Join(dataDir, "models", modelName)
					if alt := findModelDir(modelName, dataDir, requiredFormat, requiredQuantization); alt != "" {
						modelPath = alt
					}
				}
			}
			// Native binary engines require a single model file path; container engines
			// take the directory. Collapse only file-style model directories (GGUF etc.);
			// HuggingFace-style directories with config.json must stay as directories.
			if resolved.Source != nil {
				if fi, err := os.Stat(modelPath); err == nil && fi.IsDir() && dirRequiresSingleFileModelPath(modelPath) {
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
				PortSpecs:        append([]knowledge.StartupPort(nil), resolved.PortSpecs...),
				InitCommands:     resolved.InitCommands,
				ModelPath:        modelPath,
				Config:           resolved.Config,
				RuntimeClassName: resolved.RuntimeClassName,
				CPUArch:          resolved.CPUArch,
				Env:              resolved.Env,
				WorkDir:          resolved.WorkDir,
				Container:        resolved.Container,
				GPUResourceName:  resolved.GPUResourceName,
				ExtraVolumes:     resolved.ExtraVolumes,
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

			// Select runtime based on engine recommendation and available runtimes.
			// All-zero partition (full device) does not require K3S+HAMi GPU splitting.
			hasPartition := req.Partition != nil && (req.Partition.GPUMemoryMiB > 0 || req.Partition.GPUCoresPercent > 0)
			activeRt, rtErr := pickRuntimeForDeployment(resolved.RuntimeRecommendation, k3sRt, dockerRt, nativeRt, rt, hasPartition)
			if rtErr != nil {
				return nil, rtErr
			}
			deployName := knowledge.SanitizePodName(modelName + "-" + resolved.Engine)
			if existing := findExistingDeployment(ctx, deployName, activeRt, rt, nativeRt, dockerRt); existing != nil {
				if existing.Ready || existing.Phase == "running" || existing.Phase == "starting" {
					proxyServer.RegisterBackend(modelName, &proxy.Backend{
						ModelName:  modelName,
						EngineType: resolved.Engine,
						Address:    existing.Address,
						Ready:      existing.Ready,
					})
					runtimeName := activeRt.Name()
					if existing.Runtime != "" {
						runtimeName = existing.Runtime
					}
					status := "deploying"
					if existing.Ready {
						status = "ready"
					}
					result := map[string]any{
						"name":    deployName,
						"model":   modelName,
						"engine":  resolved.Engine,
						"slot":    resolved.Slot,
						"status":  status,
						"phase":   existing.Phase,
						"runtime": runtimeName,
					}
					if existing.Address != "" {
						result["address"] = existing.Address
					}
					return json.Marshal(result)
				}
			}
			// Pre-flight: ensure image is available in containerd for K3S deployments.
			// Auto-import from Docker or pre-pull from registries if needed.
			// Note: containerd operations require root; skip gracefully if not root.
			if activeRt.Name() == "k3s" && req.Image != "" {
				inContainerd := engine.ImageExistsInContainerd(ctx, req.Image, &execRunner{})
				if !inContainerd {
					inDocker := engine.ImageExistsInDocker(ctx, req.Image, &execRunner{})
					if inDocker {
						if shouldFallbackToDockerRuntime(activeRt.Name(), hasPartition, inContainerd, inDocker, os.Getuid() == 0, dockerRt != nil) {
							slog.Info("falling back to Docker runtime because K3S image import requires root", "image", req.Image)
							activeRt = dockerRt
						} else if requiresRootImportForK3S(inContainerd, inDocker, os.Getuid() == 0) {
							return nil, fmt.Errorf("engine image %s is only available in Docker; K3S deployment requires importing it into containerd as root (sudo docker save %s | sudo k3s ctr -n k8s.io images import -)", req.Image, req.Image)
						} else {
							slog.Info("auto-importing image from Docker to containerd", "image", req.Image)
							if importErr := engine.ImportDockerToContainerd(ctx, req.Image, &execRunner{}); importErr != nil {
								slog.Warn("auto-import failed, K3S will try registries.yaml", "image", req.Image, "error", importErr)
							}
						}
					} else if activeRt.Name() == "k3s" && len(resolved.EngineRegistries) > 0 {
						if !allowAutoPull {
							return nil, fmt.Errorf("engine image %s not found in K3S containerd and auto-pull is disabled", req.Image)
						}
						slog.Info("pre-pulling engine image", "image", req.Image, "registries", len(resolved.EngineRegistries))
						imgName, imgTag := splitImageRef(req.Image)
						if pullErr := engine.Pull(ctx, engine.PullOptions{
							Image:          imgName,
							Tag:            imgTag,
							Registries:     resolved.EngineRegistries,
							Runner:         &execRunner{},
							ExpectedDigest: resolved.EngineDigest,
						}); pullErr != nil {
							slog.Warn("pre-pull failed, K3S will try registries.yaml", "image", req.Image, "error", pullErr)
						}
					}
				}
			}
			// Pre-flight: ensure image is available in Docker for Docker deployments.
			if activeRt.Name() == "docker" && req.Image != "" {
				fullRef := req.Image
				if !strings.Contains(fullRef, ":") {
					fullRef += ":latest"
				}
				if !engine.ImageExistsInDocker(ctx, fullRef, &execRunner{}) {
					if len(resolved.EngineRegistries) > 0 {
						if !allowAutoPull {
							return nil, fmt.Errorf("engine image %s not found in Docker and auto-pull is disabled", req.Image)
						}
						slog.Info("auto-pulling engine image for Docker deploy", "image", req.Image)
						imgName, imgTag := splitImageRef(req.Image)
						if pullErr := engine.Pull(ctx, engine.PullOptions{
							Image:          imgName,
							Tag:            imgTag,
							Registries:     resolved.EngineRegistries,
							Runner:         &execRunner{},
							ExpectedDigest: resolved.EngineDigest,
						}); pullErr != nil {
							return nil, fmt.Errorf("auto-pull engine image %s: %w", req.Image, pullErr)
						}
						if aliasErr := ensureDockerImageAlias(ctx, &execRunner{}, req.Image, resolved.EngineRegistries); aliasErr != nil {
							return nil, fmt.Errorf("normalize pulled docker image %s: %w", req.Image, aliasErr)
						}
					} else {
						slog.Warn("engine image not found locally and no registries configured",
							"image", req.Image,
							"hint", "run 'aima engine pull' first or ensure registries are configured in engine YAML")
					}
				}
			}
			compatPlan, compatErr := prepareContainerCompatibility(ctx, &execRunner{}, allowAutoPull, activeRt.Name(), modelPath, resolved)
			if compatErr != nil {
				return nil, compatErr
			}
			if len(compatPlan.RepairInitCommands) > 0 {
				req.InitCommands = append(append([]string(nil), compatPlan.RepairInitCommands...), req.InitCommands...)
			}
			if compatPlan.DockerImageChanged && activeRt.Name() == "k3s" {
				if os.Getuid() == 0 {
					slog.Info("syncing compatibility-validated Docker image into K3S containerd", "image", req.Image)
					if importErr := engine.ImportDockerToContainerd(ctx, req.Image, &execRunner{}); importErr != nil {
						if shouldFallbackToDockerRuntime(activeRt.Name(), hasPartition, false, true, true, dockerRt != nil) {
							slog.Warn("containerd image sync failed, falling back to Docker runtime", "image", req.Image, "error", importErr)
							activeRt = dockerRt
						} else {
							return nil, fmt.Errorf("sync compatibility-validated image %s into K3S containerd: %w", req.Image, importErr)
						}
					}
				} else if shouldFallbackToDockerRuntime(activeRt.Name(), hasPartition, false, true, false, dockerRt != nil) {
					slog.Info("falling back to Docker runtime because compatibility-validated image change cannot be synced into K3S without root", "image", req.Image)
					activeRt = dockerRt
				} else {
					return nil, fmt.Errorf("compatibility validation refreshed %s in Docker, but syncing that image into K3S containerd requires root", req.Image)
				}
			}
			if err := allocateDeploymentPorts(ctx, deployName, activeRt.Name(), req, resolved.Provenance, listAllRuntimes(ctx, rt, nativeRt, dockerRt)); err != nil {
				return nil, fmt.Errorf("allocate ports: %w", err)
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
				"name":  deployName,
				"model": modelName, "engine": resolved.Engine,
				"slot": resolved.Slot, "status": "deploying",
				"runtime": activeRt.Name(),
			}
			return json.Marshal(result)
		},
		DeployDryRun: func(ctx context.Context, engineType, modelName, slot string, overrides map[string]any) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			rd, err := resolveDeployment(ctx, cat, db, kStore, hwInfo, modelName, engineType, slot, overrides, dataDir)
			if err != nil {
				return nil, err
			}

			// Select runtime for display
			resolved := rd.Resolved
			hasPartition := resolved.Partition != nil && (resolved.Partition.GPUMemoryMiB > 0 || resolved.Partition.GPUCoresPercent > 0)
			selectedRt, rtErr := pickRuntimeForDeployment(resolved.RuntimeRecommendation, k3sRt, dockerRt, nativeRt, rt, hasPartition)
			if rtErr != nil {
				return nil, rtErr
			}
			runtimeName := selectedRt.Name()
			var warnings []string
			warnings = append(warnings, rd.Fit.Warnings...)

			if runtimeName == "k3s" && resolved.EngineImage != "" {
				inContainerd := engine.ImageExistsInContainerd(ctx, resolved.EngineImage, &execRunner{})
				inDocker := engine.ImageExistsInDocker(ctx, resolved.EngineImage, &execRunner{})
				if shouldFallbackToDockerRuntime(runtimeName, hasPartition, inContainerd, inDocker, os.Getuid() == 0, dockerRt != nil) {
					selectedRt = dockerRt
					runtimeName = selectedRt.Name()
					warnings = append(warnings, k3sDockerFallbackWarning(resolved.EngineImage))
				} else if requiresRootImportForK3S(inContainerd, inDocker, os.Getuid() == 0) {
					warnings = append(warnings, k3sDockerImportHint(resolved.EngineImage))
				}
			}

			result := map[string]any{
				"model":        rd.ModelName,
				"engine":       resolved.Engine,
				"engine_image": resolved.EngineImage,
				"slot":         resolved.Slot,
				"runtime":      runtimeName,
				"config":       resolved.Config,
				"ports":        knowledge.ResolvePortBindingsFromSpecs(resolved.PortSpecs, resolved.Config),
				"provenance":   resolved.Provenance,
				"fit_report": map[string]any{
					"fit":         rd.Fit.Fit,
					"reason":      rd.Fit.Reason,
					"warnings":    rd.Fit.Warnings,
					"adjustments": rd.Fit.Adjustments,
				},
			}

			if !rd.Fit.Fit {
				warnings = append(warnings, "WILL NOT DEPLOY: "+rd.Fit.Reason)
			}

			// Time estimates
			if resolved.ColdStartSMax > 0 {
				result["cold_start_s"] = map[string]int{"min": resolved.ColdStartSMin, "max": resolved.ColdStartSMax}
			}
			if resolved.StartupTimeS > 0 {
				result["startup_time_s"] = resolved.StartupTimeS
			}

			// Power estimates
			if resolved.EnginePowerWattsMax > 0 {
				result["engine_power_watts"] = map[string]int{"min": resolved.EnginePowerWattsMin, "max": resolved.EnginePowerWattsMax}
			}

			// Resource estimates (full cost vector)
			resourceEstimate := map[string]any{}
			if resolved.ResourceEstimate != nil {
				if resolved.ResourceEstimate.VRAMMiB > 0 {
					resourceEstimate["vram_mib"] = resolved.ResourceEstimate.VRAMMiB
				}
				if resolved.ResourceEstimate.RAMMiB > 0 {
					resourceEstimate["ram_mib"] = resolved.ResourceEstimate.RAMMiB
				}
				if resolved.ResourceEstimate.CPUCores > 0 {
					resourceEstimate["cpu_cores"] = resolved.ResourceEstimate.CPUCores
				}
				if resolved.ResourceEstimate.DiskMiB > 0 {
					resourceEstimate["disk_mib"] = resolved.ResourceEstimate.DiskMiB
				}
				if resolved.ResourceEstimate.PowerWatts > 0 {
					resourceEstimate["power_watts"] = resolved.ResourceEstimate.PowerWatts
				}
			} else if resolved.EstimatedVRAMMiB > 0 {
				resourceEstimate["vram_mib"] = resolved.EstimatedVRAMMiB
			}
			if resolved.Partition != nil {
				if resolved.Partition.GPUMemoryMiB > 0 {
					resourceEstimate["partition_gpu_memory_mib"] = resolved.Partition.GPUMemoryMiB
				}
				if resolved.Partition.CPUCores > 0 {
					resourceEstimate["partition_cpu_cores"] = resolved.Partition.CPUCores
				}
				if resolved.Partition.RAMMiB > 0 {
					resourceEstimate["partition_ram_mib"] = resolved.Partition.RAMMiB
				}
			}
			if len(resourceEstimate) > 0 {
				result["resource_estimate"] = resourceEstimate
			}

			// Amplifier info
			if resolved.AmplifierScore > 0 {
				result["amplifier_score"] = resolved.AmplifierScore
			}
			if resolved.OffloadPath {
				result["offload_path"] = true
			}

			// Performance reference (K4 — attach best known perf data)
			perfRef := map[string]any{"source": "unknown"}
			hwKey := hwInfo.HardwareProfile
			if hwKey == "" {
				hwKey = hwInfo.GPUArch
			}
			if golden, goldenBench, err := db.FindGoldenBenchmark(ctx, hwKey, resolved.Engine, rd.ModelName); err == nil && golden != nil && goldenBench != nil {
				perfRef = map[string]any{
					"source":         "benchmark",
					"benchmark_id":   goldenBench.ID,
					"throughput_tps": goldenBench.ThroughputTPS,
					"ttft_ms_p95":    goldenBench.TTFTP95ms,
					"power_watts":    goldenBench.PowerDrawWatts,
				}
			} else if resolved.ResourceEstimate != nil && resolved.ResourceEstimate.PowerWatts > 0 {
				perfRef["source"] = "yaml_estimate"
				perfRef["power_watts"] = resolved.ResourceEstimate.PowerWatts
			}
			result["performance_reference"] = perfRef

			if runtimeName == "k3s" {
				if podYAML, podErr := knowledge.GeneratePod(resolved); podErr == nil {
					result["pod_yaml"] = string(podYAML)
				} else {
					warnings = append(warnings, "pod generation failed: "+podErr.Error())
				}
			}

			if len(warnings) > 0 {
				result["warnings"] = warnings
			}

			return json.Marshal(result)
		},
		DeployDelete: func(ctx context.Context, name string) error {
			// Gap 3: Save rollback snapshot before deletion (capture deployment state)
			if deployments, listErr := rt.List(ctx); listErr == nil {
				for _, d := range deployments {
					if deploymentMatchesQuery(d, name) {
						if snap, snapErr := json.Marshal(d); snapErr == nil {
							_ = db.SaveSnapshot(ctx, &state.RollbackSnapshot{
								ToolName: "deploy.delete", ResourceType: "deployment", ResourceName: d.Name, Snapshot: string(snap),
							})
						}
						break
					}
				}
			}
			// Try exact pod name first, then fall back to searching by model label.
			// Pod names are "<model>-<engine>" (e.g. qwen3-8b-vllm), but users
			// often pass just the model name (e.g. qwen3-8b).
			deleted := name
			modelKey := ""
			err := rt.Delete(ctx, name)
			if err != nil {
				// Exact name failed — search deployments for this model name.
				if deployments, listErr := rt.List(ctx); listErr == nil {
					for _, d := range deployments {
						if deploymentMatchesQuery(d, name) {
							if delErr := rt.Delete(ctx, d.Name); delErr == nil {
								deleted = d.Name
								modelKey = d.Labels["aima.dev/model"]
								err = nil
								break
							}
						}
					}
				}
			}
			if err != nil && nativeRt != nil && nativeRt != rt {
				// Try exact name and model-label search on native runtime.
				err = nativeRt.Delete(ctx, name)
				if err != nil {
					if nativeDeps, nErr := nativeRt.List(ctx); nErr == nil {
						for _, d := range nativeDeps {
							if deploymentMatchesQuery(d, name) {
								if delErr := nativeRt.Delete(ctx, d.Name); delErr == nil {
									deleted = d.Name
									err = nil
									break
								}
							}
						}
					}
				} else {
					deleted = name
				}
			}
			if err != nil && dockerRt != nil && dockerRt != rt {
				err = dockerRt.Delete(ctx, name)
				if err != nil {
					if dockerDeps, dErr := dockerRt.List(ctx); dErr == nil {
						for _, d := range dockerDeps {
							if deploymentMatchesQuery(d, name) {
								if delErr := dockerRt.Delete(ctx, d.Name); delErr == nil {
									deleted = d.Name
									err = nil
									break
								}
							}
						}
					}
				} else {
					deleted = name
				}
			}
			if err != nil {
				return fmt.Errorf("delete deployment %q: %w", name, err)
			}
			if modelKey != "" {
				proxyServer.RemoveBackend(modelKey)
			}
			proxyServer.RemoveBackend(name)
			proxyServer.RemoveBackend(deleted)
			return nil
		},
		DeployStatus: func(ctx context.Context, name string) (json.RawMessage, error) {
			s, err := rt.Status(ctx, name)
			if err != nil && nativeRt != nil && nativeRt != rt {
				s, err = nativeRt.Status(ctx, name)
			}
			if err != nil && dockerRt != nil && dockerRt != rt {
				s, err = dockerRt.Status(ctx, name)
			}
			if err != nil {
				// Exact pod name failed — search by model label across all runtimes.
				allDeps := listAllRuntimes(ctx, rt, nativeRt, dockerRt)
				for _, d := range allDeps {
					if deploymentMatchesQuery(d, name) {
						s = d
						err = nil
						break
					}
				}
			}
			if err != nil {
				return nil, err
			}
			return json.Marshal(s)
		},
		DeployList: func(ctx context.Context) (json.RawMessage, error) {
			statuses, err := rt.List(ctx)
			if err != nil {
				// Primary runtime failed — still try to collect from other runtimes.
				slog.Warn("deploy list: primary runtime failed", "runtime", rt.Name(), "error", err)
				statuses = make([]*runtime.DeploymentStatus, 0)
			}
			// Also include native deployments (when engine recommended native on a K3S machine).
			if nativeRt != nil && nativeRt != rt {
				if nativeStatuses, nErr := nativeRt.List(ctx); nErr == nil {
					statuses = append(statuses, nativeStatuses...)
				}
			}
			// Also include Docker deployments.
			if dockerRt != nil && dockerRt != rt {
				if dockerStatuses, dErr := dockerRt.List(ctx); dErr == nil {
					statuses = append(statuses, dockerStatuses...)
				}
			}
			return json.Marshal(statuses)
		},
		DeployRun: deployRunCore,
		DeployLogs: func(ctx context.Context, name string, tailLines int) (string, error) {
			logs, err := rt.Logs(ctx, name, tailLines)
			if err != nil && nativeRt != nil && nativeRt != rt {
				logs, err = nativeRt.Logs(ctx, name, tailLines)
			}
			if err != nil && dockerRt != nil && dockerRt != rt {
				logs, err = dockerRt.Logs(ctx, name, tailLines)
			}
			if err != nil {
				// Exact pod name failed — search by model label across all runtimes.
				allDeps := listAllRuntimes(ctx, rt, nativeRt, dockerRt)
				for _, d := range allDeps {
					if deploymentMatchesQuery(d, name) {
						// Try each runtime for logs by actual deployment name.
						for _, tryRt := range []runtime.Runtime{rt, nativeRt, dockerRt} {
							if tryRt == nil {
								continue
							}
							if l, e := tryRt.Logs(ctx, d.Name, tailLines); e == nil {
								return l, nil
							}
						}
						break
					}
				}
			}
			return logs, err
		},

		// Knowledge
		ResolveConfig: func(ctx context.Context, modelName, engineType string, overrides map[string]any) (json.RawMessage, error) {
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			rd, err := resolveDeployment(ctx, cat, db, kStore, hwInfo, modelName, engineType, "", overrides, dataDir)
			if err != nil {
				return nil, err
			}
			return json.Marshal(rd.Resolved)
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
			hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
			overrides := map[string]any{}
			if slot != "" {
				overrides["slot"] = slot
			}
			goldenOpt := knowledge.WithGoldenConfig(func(hardware, engine, model string) map[string]any {
				return queryGoldenOverrides(ctx, kStore, hardware, engine, model)
			})
			resolved, _, err := resolveWithFallback(ctx, cat, db, hwInfo, modelName, engineType, overrides, dataDir, goldenOpt)
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
		ListModelAssets: func(ctx context.Context) (json.RawMessage, error) {
			return json.Marshal(cat.ModelAssets)
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
				Hardware        string         `json:"hardware"`
				Engine          string         `json:"engine"`
				Model           string         `json:"model"`
				DeviceID        string         `json:"device_id"`
				Config          map[string]any `json:"config"`
				Concurrency     int            `json:"concurrency"`
				InputLenBucket  string         `json:"input_len_bucket"`
				OutputLenBucket string         `json:"output_len_bucket"`
				TTFTP50ms       float64        `json:"ttft_ms_p50"`
				TTFTP95ms       float64        `json:"ttft_ms_p95"`
				TPOTP50ms       float64        `json:"tpot_ms_p50"`
				TPOTP95ms       float64        `json:"tpot_ms_p95"`
				ThroughputTPS   float64        `json:"throughput_tps"`
				QPS             float64        `json:"qps"`
				VRAMUsageMiB    int            `json:"vram_usage_mib"`
				SampleCount     int            `json:"sample_count"`
				Stability       string         `json:"stability"`
				Notes           string         `json:"notes"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse benchmark params: %w", err)
			}
			if p.Concurrency <= 0 {
				p.Concurrency = 1
			}

			// Find or create configuration
			configJSON, err := json.Marshal(p.Config)
			if err != nil {
				return nil, fmt.Errorf("marshal benchmark config: %w", err)
			}
			configHash := fmt.Sprintf("%x", sha256.Sum256(
				[]byte(p.Hardware+"|"+p.Engine+"|"+p.Model+"|"+string(configJSON))))

			cfg, err := db.FindConfigByHash(ctx, configHash)
			if err != nil {
				return nil, err
			}
			if cfg == nil {
				cfg = &state.Configuration{
					ID:         configHash[:16],
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
			postProcessBenchmarkSave(ctx, db, kStore, benchID, cfg.ID, p.Hardware, p.Engine, p.Model, p.ThroughputTPS)

			return json.Marshal(map[string]any{
				"benchmark_id": benchID,
				"config_id":    cfg.ID,
				"status":       "recorded",
				"hardware":     p.Hardware,
				"engine":       p.Engine,
				"model":        p.Model,
			})
		},

		PromoteConfig: func(ctx context.Context, configID, status string) (json.RawMessage, error) {
			validStatuses := map[string]bool{"golden": true, "experiment": true, "archived": true}
			if !validStatuses[status] {
				return nil, fmt.Errorf("invalid status %q: must be golden, experiment, or archived", status)
			}
			// Fetch current config to return old status
			cfg, err := db.GetConfiguration(ctx, configID)
			if err != nil {
				return nil, fmt.Errorf("get configuration: %w", err)
			}
			oldStatus := cfg.Status
			if err := db.UpdateConfigStatus(ctx, configID, status); err != nil {
				return nil, fmt.Errorf("promote config: %w", err)
			}
			return json.Marshal(map[string]any{
				"config_id":  configID,
				"old_status": oldStatus,
				"new_status": status,
				"message":    fmt.Sprintf("Configuration %s promoted from %s to %s", configID, oldStatus, status),
			})
		},

		// Benchmark execution
		RunBenchmark: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Model          string  `json:"model"`
				Endpoint       string  `json:"endpoint"`
				Concurrency    int     `json:"concurrency"`
				NumRequests    int     `json:"num_requests"`
				MaxTokens      int     `json:"max_tokens"`
				InputTokens    int     `json:"input_tokens"`
				Warmup         *int    `json:"warmup"`
				Rounds         int     `json:"rounds"`
				MinOutputRatio float64 `json:"min_output_ratio"`
				MaxRetries     int     `json:"max_retries"`
				Save           *bool   `json:"save"`
				Hardware       string  `json:"hardware"`
				Engine         string  `json:"engine"`
				Notes          string  `json:"notes"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse benchmark params: %w", err)
			}

			endpoint := resolveEndpoint(p.Endpoint, p.Model)

			warmup := 2
			if p.Warmup != nil {
				warmup = *p.Warmup
			}

			cfg := benchpkg.RunConfig{
				Endpoint:       endpoint,
				Model:          p.Model,
				Concurrency:    p.Concurrency,
				NumRequests:    p.NumRequests,
				MaxTokens:      p.MaxTokens,
				InputTokens:    p.InputTokens,
				WarmupCount:    warmup,
				Rounds:         p.Rounds,
				MinOutputRatio: p.MinOutputRatio,
				MaxRetries:     p.MaxRetries,
			}

			result, err := benchpkg.Run(ctx, cfg)
			if err != nil {
				return nil, fmt.Errorf("benchmark run: %w", err)
			}

			// Save to DB unless explicitly disabled
			save := p.Save == nil || *p.Save
			var benchmarkID, configID string
			if save && p.Hardware != "" && p.Engine != "" {
				var err error
				benchmarkID, configID, err = saveBenchmarkResult(ctx, db,
					p.Hardware, p.Engine, p.Model, result,
					cfg.Concurrency, cfg.InputTokens, cfg.MaxTokens, p.Notes)
				if err != nil {
					return nil, err
				}
				postProcessBenchmarkSave(ctx, db, kStore, benchmarkID, configID, p.Hardware, p.Engine, p.Model, result.ThroughputTPS)
			}

			resp := map[string]any{
				"result": result,
				"saved":  save && benchmarkID != "",
			}
			if benchmarkID != "" {
				resp["benchmark_id"] = benchmarkID
				resp["config_id"] = configID

				// L2c auto-promote: if new benchmark beats current golden by >5%
				if promoted, oldID := maybeAutoPromote(ctx, db, configID, result.ThroughputTPS, p.Hardware, p.Engine, p.Model); promoted {
					resp["auto_promoted"] = true
					if oldID != "" {
						resp["old_golden_id"] = oldID
					}
				}

				// K5: Update runtime overlay with actual performance data
				if p.Model != "" {
					go updatePerfOverlay(dataDir, p.Model, p.Hardware, p.Engine, result)
				}
			}
			return json.Marshal(resp)
		},

		RunBenchmarkMatrix: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Model             string  `json:"model"`
				Endpoint          string  `json:"endpoint"`
				ConcurrencyLevels []int   `json:"concurrency_levels"`
				InputTokenLevels  []int   `json:"input_token_levels"`
				MaxTokenLevels    []int   `json:"max_token_levels"`
				RequestsPerCombo  int     `json:"requests_per_combo"`
				Rounds            int     `json:"rounds"`
				MinOutputRatio    float64 `json:"min_output_ratio"`
				MaxRetries        int     `json:"max_retries"`
				Save              *bool   `json:"save"`
				Hardware          string  `json:"hardware"`
				Engine            string  `json:"engine"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse matrix params: %w", err)
			}
			if len(p.ConcurrencyLevels) == 0 {
				p.ConcurrencyLevels = []int{1, 4}
			}
			if len(p.InputTokenLevels) == 0 {
				p.InputTokenLevels = []int{128, 1024}
			}
			if len(p.MaxTokenLevels) == 0 {
				p.MaxTokenLevels = []int{128, 512}
			}
			if p.RequestsPerCombo <= 0 {
				p.RequestsPerCombo = 5
			}

			endpoint := resolveEndpoint(p.Endpoint, p.Model)

			type matrixCell struct {
				Concurrency int                 `json:"concurrency"`
				InputTokens int                 `json:"input_tokens"`
				MaxTokens   int                 `json:"max_tokens"`
				Result      *benchpkg.RunResult `json:"result"`
				Error       string              `json:"error,omitempty"`
			}

			var cells []matrixCell
			refreshVectors := false
			for _, conc := range p.ConcurrencyLevels {
				for _, inTok := range p.InputTokenLevels {
					for _, maxTok := range p.MaxTokenLevels {
						cfg := benchpkg.RunConfig{
							Endpoint:       endpoint,
							Model:          p.Model,
							Concurrency:    conc,
							NumRequests:    p.RequestsPerCombo,
							MaxTokens:      maxTok,
							InputTokens:    inTok,
							WarmupCount:    1,
							Rounds:         p.Rounds,
							MinOutputRatio: p.MinOutputRatio,
							MaxRetries:     p.MaxRetries,
						}
						result, err := benchpkg.Run(ctx, cfg)
						cell := matrixCell{
							Concurrency: conc,
							InputTokens: inTok,
							MaxTokens:   maxTok,
						}
						if err != nil {
							cell.Error = err.Error()
						} else {
							cell.Result = result
							// Save each cell if requested
							save := p.Save == nil || *p.Save
							if save && p.Hardware != "" && p.Engine != "" {
								notes := fmt.Sprintf("matrix: conc=%d in=%d out=%d", conc, inTok, maxTok)
								benchmarkID, configID, saveErr := saveBenchmarkResult(ctx, db, p.Hardware, p.Engine, p.Model, result, conc, inTok, maxTok, notes)
								if saveErr != nil {
									slog.Warn("benchmark matrix: save failed", "error", saveErr, "concurrency", conc, "input_tokens", inTok, "max_tokens", maxTok)
								} else {
									refreshVectors = true
									if err := writeBenchmarkValidation(ctx, db, benchmarkID, configID, p.Hardware, p.Engine, p.Model, result.ThroughputTPS); err != nil {
										slog.Warn("benchmark validation: write failed", "error", err, "benchmark_id", benchmarkID)
									}
								}
							}
						}
						cells = append(cells, cell)
					}
				}
			}
			if refreshVectors {
				refreshPerfVectors(ctx, kStore)
			}

			return json.Marshal(map[string]any{
				"model": p.Model,
				"cells": cells,
				"total": len(cells),
			})
		},

		ListBenchmarks: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				ConfigID string `json:"config_id"`
				Hardware string `json:"hardware"`
				Model    string `json:"model"`
				Engine   string `json:"engine"`
				Limit    int    `json:"limit"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse list params: %w", err)
			}
			if p.Limit <= 0 {
				p.Limit = 20
			}

			var configIDs []string
			if p.ConfigID != "" {
				configIDs = []string{p.ConfigID}
			} else if p.Hardware != "" || p.Model != "" || p.Engine != "" {
				configs, err := db.ListConfigurations(ctx, p.Hardware, p.Model, p.Engine)
				if err != nil {
					return nil, fmt.Errorf("list configurations: %w", err)
				}
				for _, c := range configs {
					configIDs = append(configIDs, c.ID)
				}
				if len(configIDs) == 0 {
					return json.Marshal(map[string]any{
						"results": []any{},
						"total":   0,
					})
				}
			}

			results, err := db.ListBenchmarkResults(ctx, configIDs, p.Limit)
			if err != nil {
				return nil, fmt.Errorf("list benchmarks: %w", err)
			}

			return json.Marshal(map[string]any{
				"results": results,
				"total":   len(results),
			})
		},

		// Knowledge export/import
		ExportKnowledge: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Hardware   string `json:"hardware"`
				Model      string `json:"model"`
				Engine     string `json:"engine"`
				OutputPath string `json:"output_path"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse export params: %w", err)
			}

			configs, err := db.ListConfigurations(ctx, p.Hardware, p.Model, p.Engine)
			if err != nil {
				return nil, fmt.Errorf("list configurations: %w", err)
			}

			var configIDs []string
			for _, c := range configs {
				configIDs = append(configIDs, c.ID)
			}

			// Only fetch benchmarks for matched configs.
			// When a filter is active but matches no configs, return empty benchmarks
			// instead of falling through to an unfiltered query.
			hasFilter := p.Hardware != "" || p.Model != "" || p.Engine != ""
			var benchmarks []*state.BenchmarkResult
			if len(configIDs) > 0 || !hasFilter {
				benchmarks, err = db.ListBenchmarkResults(ctx, configIDs, 0)
				if err != nil {
					return nil, fmt.Errorf("list benchmarks: %w", err)
				}
			}

			notes, err := db.SearchNotes(ctx, state.NoteFilter{
				HardwareProfile: p.Hardware,
				Model:           p.Model,
				Engine:          p.Engine,
			})
			if err != nil {
				return nil, fmt.Errorf("search notes: %w", err)
			}

			export := map[string]any{
				"schema_version": 1,
				"exported_at":    time.Now().UTC().Format(time.RFC3339),
				"aima_version":   buildinfo.Version,
				"filter":         map[string]string{"hardware": p.Hardware, "model": p.Model, "engine": p.Engine},
				"data": map[string]any{
					"configurations":    configs,
					"benchmark_results": benchmarks,
					"knowledge_notes":   notes,
				},
				"stats": map[string]int{
					"configurations":    len(configs),
					"benchmark_results": len(benchmarks),
					"knowledge_notes":   len(notes),
				},
			}

			exportJSON, err := json.MarshalIndent(export, "", "  ")
			if err != nil {
				return nil, fmt.Errorf("marshal export: %w", err)
			}

			if p.OutputPath != "" {
				if err := os.WriteFile(p.OutputPath, exportJSON, 0644); err != nil {
					return nil, fmt.Errorf("write export file: %w", err)
				}
				return json.Marshal(map[string]any{
					"path":  p.OutputPath,
					"stats": export["stats"],
				})
			}

			return exportJSON, nil
		},

		ImportKnowledge: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				InputPath string `json:"input_path"`
				Conflict  string `json:"conflict"`
				DryRun    bool   `json:"dry_run"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse import params: %w", err)
			}
			if p.Conflict == "" {
				p.Conflict = "skip"
			}

			data, err := os.ReadFile(p.InputPath)
			if err != nil {
				return nil, fmt.Errorf("read import file: %w", err)
			}

			var envelope struct {
				SchemaVersion int `json:"schema_version"`
				Data          struct {
					Configurations   []*state.Configuration   `json:"configurations"`
					BenchmarkResults []*state.BenchmarkResult `json:"benchmark_results"`
					KnowledgeNotes   []*state.KnowledgeNote   `json:"knowledge_notes"`
				} `json:"data"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				return nil, fmt.Errorf("parse import JSON: %w", err)
			}
			if envelope.SchemaVersion != 1 {
				return nil, fmt.Errorf("unsupported schema version %d (expected 1)", envelope.SchemaVersion)
			}

			imported := map[string]int{"configurations": 0, "benchmark_results": 0, "knowledge_notes": 0}
			skipped := 0
			var errors []string

			rawDB := db.RawDB()
			tx, err := rawDB.BeginTx(ctx, nil)
			if err != nil {
				return nil, fmt.Errorf("begin transaction: %w", err)
			}
			defer tx.Rollback()

			// All reads and writes go through tx to avoid deadlock
			// (db uses SetMaxOpenConns(1), so db.GetConfiguration would block).

			// Import configurations
			for _, c := range envelope.Data.Configurations {
				var exists int
				tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM configurations WHERE id = ?`, c.ID).Scan(&exists)
				if exists > 0 && p.Conflict == "skip" {
					skipped++
					continue
				}
				if p.DryRun {
					imported["configurations"]++
					continue
				}
				if exists > 0 {
					tx.ExecContext(ctx, `DELETE FROM configurations WHERE id = ?`, c.ID)
				}
				tagsJSON, _ := json.Marshal(c.Tags)
				var derivedFrom sql.NullString
				if c.DerivedFrom != "" {
					derivedFrom = sql.NullString{String: c.DerivedFrom, Valid: true}
				}
				_, insertErr := tx.ExecContext(ctx,
					`INSERT INTO configurations (id, hardware_id, engine_id, model_id, partition_slot,
						config, config_hash, derived_from, status, tags, source, device_id)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					c.ID, c.HardwareID, c.EngineID, c.ModelID, c.Slot,
					c.Config, c.ConfigHash, derivedFrom, c.Status, string(tagsJSON), c.Source, c.DeviceID)
				if insertErr != nil {
					errors = append(errors, fmt.Sprintf("config %s: %v", c.ID, insertErr))
					continue
				}
				imported["configurations"]++
			}

			// Import benchmark results
			for _, b := range envelope.Data.BenchmarkResults {
				var exists int
				tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM benchmark_results WHERE id = ?`, b.ID).Scan(&exists)
				if exists > 0 && p.Conflict == "skip" {
					skipped++
					continue
				}
				if p.DryRun {
					imported["benchmark_results"]++
					continue
				}
				if exists > 0 {
					tx.ExecContext(ctx, `DELETE FROM benchmark_results WHERE id = ?`, b.ID)
				}
				_, insertErr := tx.ExecContext(ctx,
					`INSERT INTO benchmark_results (id, config_id, concurrency, input_len_bucket, output_len_bucket, modality,
						ttft_ms_p50, ttft_ms_p95, ttft_ms_p99, tpot_ms_p50, tpot_ms_p95,
						throughput_tps, qps, vram_usage_mib, ram_usage_mib, power_draw_watts, gpu_utilization_pct,
						error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					b.ID, b.ConfigID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
					b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms,
					b.ThroughputTPS, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB, b.PowerDrawWatts, b.GPUUtilPct,
					b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, b.TestedAt, b.AgentModel, b.Notes)
				if insertErr != nil {
					errors = append(errors, fmt.Sprintf("benchmark %s: %v", b.ID, insertErr))
					continue
				}
				imported["benchmark_results"]++
			}

			// Import knowledge notes
			for _, n := range envelope.Data.KnowledgeNotes {
				var exists int
				tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_notes WHERE id = ?`, n.ID).Scan(&exists)
				if exists > 0 && p.Conflict == "skip" {
					skipped++
					continue
				}
				if p.DryRun {
					imported["knowledge_notes"]++
					continue
				}
				if exists > 0 {
					tx.ExecContext(ctx, `DELETE FROM knowledge_notes WHERE id = ?`, n.ID)
				}
				tagsJSON, _ := json.Marshal(n.Tags)
				_, insertErr := tx.ExecContext(ctx,
					`INSERT INTO knowledge_notes (id, title, tags, hardware_profile, model, engine, content, confidence)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					n.ID, n.Title, string(tagsJSON), n.HardwareProfile, n.Model, n.Engine, n.Content, n.Confidence)
				if insertErr != nil {
					errors = append(errors, fmt.Sprintf("note %s: %v", n.ID, insertErr))
					continue
				}
				imported["knowledge_notes"]++
			}

			// If any inserts failed, rollback the entire transaction
			if len(errors) > 0 {
				return json.Marshal(map[string]any{
					"imported": map[string]int{"configurations": 0, "benchmark_results": 0, "knowledge_notes": 0},
					"skipped":  skipped,
					"errors":   errors,
					"dry_run":  p.DryRun,
				})
			}

			if !p.DryRun {
				if err := tx.Commit(); err != nil {
					return nil, fmt.Errorf("commit import: %w", err)
				}
				if imported["benchmark_results"] > 0 {
					refreshPerfVectors(ctx, kStore)
				}
			}

			return json.Marshal(map[string]any{
				"imported": imported,
				"skipped":  skipped,
				"dry_run":  p.DryRun,
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
		StackPreflight: func(ctx context.Context, tier string) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir).
				WithPodQuerier(&podQuerierAdapter{client: k3sClient})
			hwProfile := detectHWProfile(ctx, cat)
			components := stack.FilterByTier(cat.StackComponents, tier)
			items := installer.Preflight(ctx, components, hwProfile)
			return json.Marshal(items)
		},
		StackInit: func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir).
				WithPodQuerier(&podQuerierAdapter{client: k3sClient})
			components := stack.FilterByTier(cat.StackComponents, tier)
			if err := installer.PreCheck(ctx, components); err != nil {
				return nil, err
			}
			hwProfile := detectHWProfile(ctx, cat)
			if allowDownload {
				missing := installer.Preflight(ctx, components, hwProfile)
				if err := stack.DownloadItems(ctx, missing); err != nil {
					return nil, fmt.Errorf("download: %w", err)
				}
			}
			result, err := installer.Init(ctx, components, hwProfile)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		},
		StackStatus: func(ctx context.Context) (json.RawMessage, error) {
			installer := stack.NewInstaller(&execRunner{}, dataDir).
				WithPodQuerier(&podQuerierAdapter{client: k3sClient})
			hwProfile := detectHWProfile(ctx, cat)
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
			// Enforce a 60-second timeout to prevent indefinite hangs.
			execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			out, err := exec.CommandContext(execCtx, parts[0], parts[1:]...).CombinedOutput()
			// Cap output to 1MB to prevent OOM on large outputs.
			const maxOutput = 1 << 20
			if len(out) > maxOutput {
				out = append(out[:maxOutput], []byte("\n... (output truncated)")...)
			}
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
		CatalogOverride: func(ctx context.Context, kind, name, content string) (json.RawMessage, error) {
			// Validate kind
			dir := knowledge.KindToDir(kind)
			if dir == "" {
				return nil, fmt.Errorf("unknown kind %q", kind)
			}
			// Validate override file basename to prevent path traversal.
			if err := validateOverlayAssetName(name); err != nil {
				return nil, err
			}
			// Validate YAML parses as the correct kind AND body kind matches param kind
			tmpCat := &knowledge.Catalog{}
			if err := tmpCat.ParseAssetPublic([]byte(content), "input"); err != nil {
				return nil, fmt.Errorf("invalid YAML: %w", err)
			}
			if bodyKind := tmpCat.ParsedKind(); bodyKind != kind {
				return nil, fmt.Errorf("kind mismatch: parameter is %q but YAML body is %q", kind, bodyKind)
			}
			// Inject _base_digest if factory has this asset
			finalContent := content
			if digest, ok := factoryDigests[name]; ok {
				finalContent = "_base_digest: " + digest + "\n" + content
			}
			// Write to overlay directory
			overlaySubDir := filepath.Join(dataDir, "catalog", dir)
			if err := os.MkdirAll(overlaySubDir, 0o755); err != nil {
				return nil, fmt.Errorf("create overlay dir: %w", err)
			}
			outPath := filepath.Join(overlaySubDir, name+".yaml")
			action := "created"
			if _, err := os.Stat(outPath); err == nil {
				action = "replaced"
			}
			if err := os.WriteFile(outPath, []byte(finalContent), 0o644); err != nil {
				return nil, fmt.Errorf("write overlay: %w", err)
			}
			result := map[string]string{
				"path":   outPath,
				"action": action,
			}
			if _, ok := factoryDigests[name]; ok {
				result["note"] = "overlay shadows factory asset, _base_digest injected"
			}
			return json.Marshal(result)
		},
		SystemStatus: func(ctx context.Context) (json.RawMessage, error) {
			status := map[string]json.RawMessage{}
			if hw, err := hal.Detect(ctx); err == nil {
				if b, e := json.Marshal(hw); e == nil {
					status["hardware"] = b
				}
			} else {
				return nil, fmt.Errorf("detect hardware: %w", err)
			}
			// Non-fatal: K3S may not be running
			pods, _ := rt.List(ctx)
			if pods == nil {
				pods = make([]*runtime.DeploymentStatus, 0)
			}
			if b, e := json.Marshal(pods); e == nil {
				status["deployments"] = b
			}
			if nativeRt != nil && nativeRt != rt {
				if nativePods, err := nativeRt.List(ctx); err == nil && len(nativePods) > 0 {
					if b, e := json.Marshal(nativePods); e == nil {
						status["native_deployments"] = b
					}
				}
			}
			if m, err := hal.CollectMetrics(ctx); err == nil {
				if b, e := json.Marshal(m); e == nil {
					status["metrics"] = b
				}
			}
			// Add hostname, version, and primary IP for device identification
			if hostname, err := os.Hostname(); err == nil {
				if b, e := json.Marshal(hostname); e == nil {
					status["hostname"] = b
				}
			}
			if b, e := json.Marshal(buildinfo.Version); e == nil {
				status["version"] = b
			}
			if b, e := json.Marshal(supportView.Status(ctx)); e == nil {
				status["support"] = b
			}
			if deps.OpenClawStatus != nil {
				if b, e := deps.OpenClawStatus(ctx); e == nil {
					status["openclaw"] = b
				}
			}
			return json.Marshal(status)
		},
		ListKnowledgeSummary: func(ctx context.Context) (json.RawMessage, error) {
			profilesRaw, err := json.Marshal(cat.HardwareProfiles)
			if err != nil {
				return nil, fmt.Errorf("marshal profiles: %w", err)
			}
			enginesRaw, err := json.Marshal(cat.EngineAssets)
			if err != nil {
				return nil, fmt.Errorf("marshal engines: %w", err)
			}
			modelsRaw, err := json.Marshal(cat.ModelAssets)
			if err != nil {
				return nil, fmt.Errorf("marshal models: %w", err)
			}

			var profiles []map[string]any
			var engines []map[string]any
			var models []map[string]any
			if err := json.Unmarshal(profilesRaw, &profiles); err != nil {
				return nil, fmt.Errorf("decode profiles: %w", err)
			}
			if err := json.Unmarshal(enginesRaw, &engines); err != nil {
				return nil, fmt.Errorf("decode engines: %w", err)
			}
			if err := json.Unmarshal(modelsRaw, &models); err != nil {
				return nil, fmt.Errorf("decode models: %w", err)
			}

			summary := map[string]any{
				"hardware_profiles": len(profiles),
				"engine_assets":     len(engines),
				"model_assets":      len(models),
			}

			profileNames := make([]string, 0, len(profiles))
			for _, hp := range profiles {
				if n, ok := hp["name"].(string); ok && n != "" {
					profileNames = append(profileNames, n)
					continue
				}
				if n, ok := hp["id"].(string); ok && n != "" {
					profileNames = append(profileNames, n)
				}
			}
			summary["profiles"] = profileNames

			engineNames := make([]string, 0, len(engines))
			for _, ea := range engines {
				if t, ok := ea["type"].(string); ok && t != "" {
					engineNames = append(engineNames, t)
					continue
				}
				if n, ok := ea["name"].(string); ok && n != "" {
					engineNames = append(engineNames, n)
					continue
				}
				if n, ok := ea["id"].(string); ok && n != "" {
					engineNames = append(engineNames, n)
				}
			}
			summary["engines"] = engineNames

			modelNames := make([]string, 0, len(models))
			for _, ma := range models {
				if n, ok := ma["name"].(string); ok && n != "" {
					modelNames = append(modelNames, n)
					continue
				}
				if n, ok := ma["id"].(string); ok && n != "" {
					modelNames = append(modelNames, n)
				}
			}
			summary["models"] = modelNames

			scenarioNames := make([]string, 0, len(cat.DeploymentScenarios))
			for _, ds := range cat.DeploymentScenarios {
				scenarioNames = append(scenarioNames, ds.Metadata.Name)
			}
			summary["deployment_scenarios"] = len(cat.DeploymentScenarios)
			summary["scenarios"] = scenarioNames

			return json.Marshal(summary)
		},
		CatalogStatus: func(ctx context.Context) (json.RawMessage, error) {
			factoryCat, _ := knowledge.LoadCatalog(catalog.FS)
			overlayDir := filepath.Join(dataDir, "catalog")
			var overlayCat *knowledge.Catalog
			var parseWarnings []string
			if info, e := os.Stat(overlayDir); e == nil && info.IsDir() {
				overlayCat, parseWarnings = knowledge.LoadCatalogLenient(os.DirFS(overlayDir))
			} else {
				overlayCat = &knowledge.Catalog{}
			}
			// Find shadowed assets
			factoryNames := knowledge.CollectNames(factoryCat)
			overlayNames := knowledge.CollectNames(overlayCat)
			type shadowEntry struct {
				Name  string `json:"name"`
				Kind  string `json:"kind"`
				Stale bool   `json:"stale"`
			}
			var shadowed []shadowEntry
			overlayDigests := knowledge.ExtractOverlayDigestsFromDir(overlayDir)
			for name := range overlayNames {
				if factoryNames[name] {
					stale := false
					if baseD, ok := overlayDigests[name]; ok {
						if factD, ok2 := factoryDigests[name]; ok2 && baseD != factD {
							stale = true
						}
					}
					shadowed = append(shadowed, shadowEntry{Name: name, Stale: stale})
				}
			}
			status := map[string]any{
				"factory_assets": catalogSize(factoryCat),
				"overlay_assets": catalogSize(overlayCat),
				"shadowed":       shadowed,
				"parse_warnings": parseWarnings,
			}
			return json.Marshal(status)
		},
		CatalogValidate: func(ctx context.Context) (json.RawMessage, error) {
			type issue struct {
				Engine   string `json:"engine"`
				Severity string `json:"severity"` // "error" or "warning"
				Field    string `json:"field"`
				Message  string `json:"message"`
			}
			var issues []issue

			knownRegistryPrefixes := []string{
				"docker.io/", "ghcr.io/", "nvcr.io/", "quay.io/",
				"registry.cn-", "harbor.", "cr.", "docker.1ms.run/",
			}

			for _, ea := range cat.EngineAssets {
				name := ea.Metadata.Name

				// Skip preinstalled engines (no image to validate)
				if ea.Source != nil && ea.Source.InstallType == "preinstalled" && ea.Image.Name == "" {
					continue
				}

				isLocal := ea.Image.Distribution == "local"

				// Check: container engines should have registries (unless local)
				if ea.Image.Name != "" && len(ea.Image.Registries) == 0 && !isLocal {
					issues = append(issues, issue{
						Engine:   name,
						Severity: "error",
						Field:    "image.registries",
						Message:  "container engine has no registries configured; pull will fail",
					})
				}

				// Check: image.name should not contain registry prefix
				if ea.Image.Name != "" {
					for _, prefix := range knownRegistryPrefixes {
						if strings.HasPrefix(ea.Image.Name, prefix) {
							issues = append(issues, issue{
								Engine:   name,
								Severity: "warning",
								Field:    "image.name",
								Message:  fmt.Sprintf("image name contains registry prefix %q; use short name in image.name and put full paths in registries", prefix),
							})
							break
						}
					}
				}

				// Check: single registry = single point of failure
				if ea.Image.Name != "" && len(ea.Image.Registries) == 1 && !isLocal {
					issues = append(issues, issue{
						Engine:   name,
						Severity: "warning",
						Field:    "image.registries",
						Message:  fmt.Sprintf("only one registry (%s); no fallback if it is unavailable", ea.Image.Registries[0]),
					})
				}

				// Check: local distribution should have a comment or clear name
				if isLocal && len(ea.Image.Registries) > 0 {
					issues = append(issues, issue{
						Engine:   name,
						Severity: "warning",
						Field:    "image.distribution",
						Message:  "distribution is 'local' but registries are configured; these registries will not be used for pull",
					})
				}
			}

			result := map[string]any{
				"total_engines": len(cat.EngineAssets),
				"issues":        issues,
				"issue_count":   len(issues),
			}
			return json.Marshal(result)
		},
	}
	return deps
}

func postProcessBenchmarkSave(ctx context.Context, db *state.DB, kStore *knowledge.Store, benchmarkID, configID, hardware, engine, model string, throughputTPS float64) {
	if err := writeBenchmarkValidation(ctx, db, benchmarkID, configID, hardware, engine, model, throughputTPS); err != nil {
		slog.Warn("benchmark validation: write failed", "error", err, "benchmark_id", benchmarkID)
	}
	refreshPerfVectors(ctx, kStore)
}

func writeBenchmarkValidation(ctx context.Context, db *state.DB, benchmarkID, configID, hardware, engine, model string, actualThroughput float64) error {
	if db == nil || benchmarkID == "" || configID == "" || actualThroughput <= 0 || hardware == "" || engine == "" || model == "" {
		return nil
	}

	predicted, err := lookupPredictedThroughput(ctx, db.RawDB(), hardware, engine, model)
	if err != nil {
		return err
	}
	if predicted <= 0 {
		return nil
	}

	deviation := ((actualThroughput - predicted) / predicted) * 100
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(benchmarkID+"|throughput_tps")))[:16]
	return db.InsertValidation(ctx, id, configID, hardware, engine, model, "throughput_tps", predicted, actualThroughput, deviation)
}

func lookupPredictedThroughput(ctx context.Context, db *sql.DB, hardware, engine, model string) (float64, error) {
	if db == nil {
		return 0, nil
	}

	var throughput sql.NullFloat64
	err := db.QueryRowContext(ctx, `
SELECT b.throughput_tps
FROM configurations c
JOIN benchmark_results b ON b.config_id = c.id
WHERE c.status = 'golden'
  AND c.hardware_id = ? AND c.engine_id = ? AND c.model_id = ?
ORDER BY b.throughput_tps DESC
LIMIT 1`, hardware, engine, model).Scan(&throughput)
	switch {
	case err == nil && throughput.Valid && throughput.Float64 > 0:
		return throughput.Float64, nil
	case err != nil && err != sql.ErrNoRows:
		return 0, fmt.Errorf("query golden throughput: %w", err)
	}

	var expectedPerf string
	err = db.QueryRowContext(ctx, `
SELECT expected_perf
FROM model_variants
WHERE model_id = ? AND engine_type = ?
  AND (
    hardware_id = ?
    OR hardware_id IN (SELECT id FROM hardware_profiles WHERE gpu_arch = ?)
  )
ORDER BY CASE WHEN hardware_id = ? THEN 0 ELSE 1 END
LIMIT 1`, model, engine, hardware, hardware, hardware).Scan(&expectedPerf)
	switch {
	case err == sql.ErrNoRows:
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("query expected throughput: %w", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(expectedPerf), &payload); err != nil {
		return 0, fmt.Errorf("parse expected throughput: %w", err)
	}

	rawTPS, ok := payload["tokens_per_second"]
	if !ok {
		return 0, nil
	}
	switch v := rawTPS.(type) {
	case float64:
		return v, nil
	case []any:
		if len(v) == 0 {
			return 0, nil
		}
		min := toFloat64(v[0])
		if len(v) == 1 {
			return min, nil
		}
		max := toFloat64(v[1])
		if max == 0 {
			return min, nil
		}
		return (min + max) / 2, nil
	default:
		return 0, nil
	}
}

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func refreshPerfVectors(ctx context.Context, kStore *knowledge.Store) {
	if kStore == nil {
		return
	}
	if err := kStore.RefreshPerfVectors(ctx); err != nil {
		slog.Warn("perf vectors: refresh failed", "error", err)
	}
}

// saveBenchmarkResult saves a benchmark result and its configuration to the DB.
// Returns (benchmarkID, configID) or error.
func saveBenchmarkResult(ctx context.Context, db *state.DB, hardware, engineID, model string,
	result *benchpkg.RunResult, concurrency, inputTokens, maxTokens int, notes string) (string, string, error) {

	configJSON, _ := json.Marshal(map[string]any{
		"concurrency":  concurrency,
		"max_tokens":   maxTokens,
		"input_tokens": inputTokens,
	})
	configHash := fmt.Sprintf("%x", sha256.Sum256(
		[]byte(hardware+"|"+engineID+"|"+model+"|"+string(configJSON))))

	existingCfg, err := db.FindConfigByHash(ctx, configHash)
	if err != nil {
		return "", "", fmt.Errorf("find config: %w", err)
	}
	if existingCfg == nil {
		existingCfg = &state.Configuration{
			ID: configHash[:16], HardwareID: hardware,
			EngineID: engineID, ModelID: model,
			Config: string(configJSON), ConfigHash: configHash,
			Status: "experiment", Source: "benchmark",
		}
		if err := db.InsertConfiguration(ctx, existingCfg); err != nil {
			return "", "", fmt.Errorf("create configuration: %w", err)
		}
	}

	benchmarkID := fmt.Sprintf("%x", sha256.Sum256(
		[]byte(existingCfg.ID+"|"+fmt.Sprintf("%d", time.Now().UnixNano()))))[:16]

	br := &state.BenchmarkResult{
		ID: benchmarkID, ConfigID: existingCfg.ID, Concurrency: concurrency,
		InputLenBucket:  tokenBucket(result.AvgInputTokens),
		OutputLenBucket: tokenBucket(result.AvgOutputTokens),
		Modality:        "text",
		TTFTP50ms:       result.TTFTP50ms, TTFTP95ms: result.TTFTP95ms, TTFTP99ms: result.TTFTP99ms,
		TPOTP50ms: result.TPOTP50ms, TPOTP95ms: result.TPOTP95ms,
		ThroughputTPS: result.ThroughputTPS, QPS: result.QPS,
		ErrorRate: result.ErrorRate, SampleCount: result.TotalRequests,
		DurationS: int(result.DurationMs / 1000), TestedAt: time.Now(),
		Stability: stabilityFromCV(result.TTFTCVPct),
		Notes:     notes,
	}
	if err := db.InsertBenchmarkResult(ctx, br); err != nil {
		return "", "", fmt.Errorf("save benchmark result: %w", err)
	}
	return benchmarkID, existingCfg.ID, nil
}

// maybeAutoPromote promotes a config to golden if its benchmark throughput beats
// the current golden by >5%. Returns (promoted, oldGoldenID).
func maybeAutoPromote(ctx context.Context, db *state.DB, newConfigID string, newThroughput float64, hardware, engine, model string) (bool, string) {
	goldenCfg, goldenBench, err := db.FindGoldenBenchmark(ctx, hardware, engine, model)
	if err != nil {
		slog.Warn("auto-promote: failed to query golden", "error", err)
		return false, ""
	}

	// No golden exists → promote this one directly
	if goldenCfg == nil {
		if err := db.UpdateConfigStatus(ctx, newConfigID, "golden"); err == nil {
			slog.Info("auto-promote: first golden config", "config_id", newConfigID)
			return true, ""
		}
		return false, ""
	}

	// Same config → skip
	if goldenCfg.ID == newConfigID {
		return false, ""
	}

	// Compare: new must beat golden by >5% to avoid noisy promotion
	if goldenBench != nil && newThroughput > goldenBench.ThroughputTPS*1.05 {
		if err := db.UpdateConfigStatus(ctx, goldenCfg.ID, "experiment"); err != nil {
			slog.Warn("auto-promote: failed to demote old golden", "config_id", goldenCfg.ID, "error", err)
			return false, ""
		}
		if err := db.UpdateConfigStatus(ctx, newConfigID, "golden"); err != nil {
			slog.Warn("auto-promote: failed to promote new golden", "config_id", newConfigID, "error", err)
			// Restore old golden status
			_ = db.UpdateConfigStatus(ctx, goldenCfg.ID, "golden")
			return false, ""
		}
		slog.Info("auto-promote: new golden config",
			"old_golden", goldenCfg.ID, "new_golden", newConfigID,
			"old_tps", goldenBench.ThroughputTPS, "new_tps", newThroughput)
		return true, goldenCfg.ID
	}
	return false, ""
}

// updatePerfOverlay writes benchmark observations outside the catalog merge path.
// Runtime overlays must not masquerade as model assets because same-name assets
// replace the embedded catalog on restart.
func updatePerfOverlay(dataDir, model, hardware, engine string, result *benchpkg.RunResult) {
	observationsDir := filepath.Join(dataDir, "observations", "models")
	if err := os.MkdirAll(observationsDir, 0o755); err != nil {
		slog.Warn("perf observations: mkdir failed", "error", err)
		return
	}

	// Sanitize model name for filename
	safeName := strings.ReplaceAll(model, "/", "_")
	safeName = strings.ReplaceAll(safeName, ":", "_")
	observationPath := filepath.Join(observationsDir, safeName+"-perf.json")

	observation := map[string]any{
		"model":          model,
		"hardware":       hardware,
		"engine":         engine,
		"throughput_tps": result.ThroughputTPS,
		"ttft_p50_ms":    result.TTFTP50ms,
		"ttft_p95_ms":    result.TTFTP95ms,
		"tpot_p50_ms":    result.TPOTP50ms,
		"qps":            result.QPS,
		"updated_at":     time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(observation, "", "  ")
	if err != nil {
		slog.Warn("perf observations: marshal failed", "error", err)
		return
	}
	if err := os.WriteFile(observationPath, data, 0o644); err != nil {
		slog.Warn("perf observations: write failed", "path", observationPath, "error", err)
		return
	}
	slog.Info("perf observation updated", "model", model, "path", observationPath, "throughput_tps", result.ThroughputTPS)
}

// tokenBucket converts a token count to a human-readable bucket string.
func tokenBucket(tokens int) string {
	switch {
	case tokens >= 128000:
		return "128K"
	case tokens >= 32000:
		return "32K"
	case tokens >= 8000:
		return "8K"
	case tokens >= 1000:
		return fmt.Sprintf("%dK", tokens/1000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

// stabilityFromCV derives a stability label from coefficient of variation (percentage).
func stabilityFromCV(cvPct float64) string {
	switch {
	case cvPct <= 15:
		return "stable"
	case cvPct <= 30:
		return "fluctuating"
	default:
		return "unstable"
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

func deploymentMatchesQuery(d *runtime.DeploymentStatus, query string) bool {
	if d == nil {
		return false
	}
	if d.Name == query {
		return true
	}
	modelName := ""
	engineName := ""
	if d.Labels != nil {
		modelName = d.Labels["aima.dev/model"]
		engineName = d.Labels["aima.dev/engine"]
	}
	if modelName == query {
		return true
	}
	if modelName != "" && engineName != "" {
		return knowledge.SanitizePodName(modelName+"-"+engineName) == query
	}
	return false
}

// dirContainsModelFiles reports whether path already points at a usable model,
// either as a model directory or a directly-addressable model file.
func dirContainsModelFiles(dir string) bool {
	return model.PathLooksUsable(dir, "")
}

func dirRequiresSingleFileModelPath(dir string) bool {
	if !model.PathLooksUsable(dir, "") {
		return false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		return false
	}
	return findModelFileInDir(dir) != ""
}

// findModelDir searches alternative well-known locations for a model directory.
// Returns the first path that contains model files, or "" if none found.
// Because the primary dataDir is user-specific (~/.aima), models downloaded by
// a different user (e.g. root via systemd) may be inaccessible to the current user.
// For paths we can read, we verify model files exist. For paths we can't read
// (e.g. /root/.aima when running as non-root), we accept them if the directory
// exists — Docker/K3S run as root and can access them.
func findModelDir(modelName, primaryDataDir, format, quantization string) string {
	parents := candidateModelParents(primaryDataDir)
	unreadableExact := make([]string, 0, len(parents))
	seen := make(map[string]bool)
	consider := func(path string, exact bool) string {
		if path == "" || seen[path] {
			return ""
		}
		seen[path] = true
		if model.PathLooksCompatible(path, format, quantization) {
			return path
		}
		if exact {
			if fi, err := os.Stat(path); err == nil && fi.IsDir() {
				if looksUnreadableModelDir(path) {
					unreadableExact = append(unreadableExact, path)
				}
			}
		}
		return ""
	}

	for _, parent := range parents {
		if found := consider(filepath.Join(parent, modelName), true); found != "" {
			return found
		}
	}

	for _, parent := range parents {
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !modelAliasMatches(entry.Name(), modelName) {
				continue
			}
			if found := consider(filepath.Join(parent, entry.Name()), false); found != "" {
				return found
			}
		}
	}

	if len(unreadableExact) > 0 {
		return unreadableExact[0]
	}
	return ""
}

func looksUnreadableModelDir(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(path, entry.Name()))
		if err != nil {
			return true
		}
		_ = f.Close()
		return false
	}
	return false
}

func candidateModelParents(primaryDataDir string) []string {
	parents := []string{
		filepath.Join(primaryDataDir, "models"),
		"/root/.aima/models",
		"/data/models",
		"/mnt/data/models",
	}
	if goruntime.GOOS == "linux" {
		if entries, err := os.ReadDir("/opt"); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				base := filepath.Join("/opt", entry.Name())
				parents = append(parents, filepath.Join(base, "models"))
				subEntries, err := os.ReadDir(base)
				if err != nil {
					continue
				}
				for _, sub := range subEntries {
					if sub.IsDir() {
						parents = append(parents, filepath.Join(base, sub.Name(), "models"))
					}
				}
			}
		}
	}
	uniq := make([]string, 0, len(parents))
	seen := make(map[string]bool)
	for _, parent := range parents {
		if parent == "" || seen[parent] {
			continue
		}
		seen[parent] = true
		uniq = append(uniq, parent)
	}
	return uniq
}

func modelAliasMatches(candidate, modelName string) bool {
	candidateNorm := normalizeModelAlias(candidate)
	modelNorm := normalizeModelAlias(modelName)
	if candidateNorm == "" || modelNorm == "" {
		return false
	}
	return candidateNorm == modelNorm ||
		strings.Contains(candidateNorm, modelNorm) ||
		strings.Contains(modelNorm, candidateNorm)
}

func normalizeModelAlias(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type deploymentFailureDetails struct {
	Message        string
	StartupMessage string
	ErrorLines     string
}

func summarizeDeploymentFailure(message, startupMessage, errorLines string) string {
	for _, candidate := range []string{message, startupMessage} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" && !isGenericDeploymentFailure(trimmed) {
			return trimmed
		}
	}
	if detail := summarizeErrorLines(errorLines); detail != "" {
		return detail
	}
	for _, candidate := range []string{message, startupMessage} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return "unknown startup failure"
}

func refineDeploymentFailure(
	ctx context.Context,
	deployName string,
	initial deploymentFailureDetails,
	statusFn func(context.Context, string) (json.RawMessage, error),
	logsFn func(context.Context, string, int) (string, error),
) string {
	best := summarizeDeploymentFailure(initial.Message, initial.StartupMessage, initial.ErrorLines)
	if !shouldRefineDeploymentFailure(best) {
		return best
	}

	current := initial
	tryRefine := func() bool {
		if statusFn != nil {
			if statusData, err := statusFn(ctx, deployName); err == nil {
				if refreshed, err := parseDeploymentFailureDetails(statusData); err == nil {
					current = mergeDeploymentFailureDetails(current, refreshed)
					for _, candidate := range []string{
						strings.TrimSpace(refreshed.Message),
						strings.TrimSpace(refreshed.StartupMessage),
						summarizeErrorLines(refreshed.ErrorLines),
						summarizeErrorLines(current.ErrorLines),
						summarizeDeploymentFailure(current.Message, current.StartupMessage, current.ErrorLines),
					} {
						if moreSpecificFailure(candidate, best) {
							best = candidate
						}
					}
				}
			}
		}
		if logsFn != nil {
			if logs, err := logsFn(ctx, deployName, 120); err == nil {
				if candidate := summarizeErrorLines(logs); moreSpecificFailure(candidate, best) {
					best = candidate
				}
			}
		}
		return !shouldRefineDeploymentFailure(best)
	}

	if tryRefine() {
		return best
	}

	select {
	case <-ctx.Done():
		return best
	case <-time.After(500 * time.Millisecond):
	}
	tryRefine()
	return best
}

func parseDeploymentFailureDetails(statusData json.RawMessage) (deploymentFailureDetails, error) {
	var details deploymentFailureDetails
	if err := json.Unmarshal(statusData, &struct {
		Message        *string `json:"message"`
		StartupMessage *string `json:"startup_message"`
		ErrorLines     *string `json:"error_lines"`
	}{
		Message:        &details.Message,
		StartupMessage: &details.StartupMessage,
		ErrorLines:     &details.ErrorLines,
	}); err != nil {
		return deploymentFailureDetails{}, err
	}
	return details, nil
}

func mergeDeploymentFailureDetails(base, overlay deploymentFailureDetails) deploymentFailureDetails {
	if moreSpecificFailure(overlay.Message, base.Message) || strings.TrimSpace(base.Message) == "" {
		base.Message = overlay.Message
	}
	if moreSpecificFailure(overlay.StartupMessage, base.StartupMessage) || strings.TrimSpace(base.StartupMessage) == "" {
		base.StartupMessage = overlay.StartupMessage
	}
	if moreSpecificFailure(summarizeErrorLines(overlay.ErrorLines), summarizeErrorLines(base.ErrorLines)) || strings.TrimSpace(base.ErrorLines) == "" {
		base.ErrorLines = overlay.ErrorLines
	}
	return base
}

func shouldRefineDeploymentFailure(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" || isGenericDeploymentFailure(msg) {
		return true
	}
	return strings.Contains(lower, "see root cause above") ||
		strings.Contains(lower, "failed core proc")
}

func moreSpecificFailure(candidate, best string) bool {
	return diagnosticLineScore(candidate) > diagnosticLineScore(best)
}

func resolvedQuantizationHint(resolved *knowledge.ResolvedConfig) string {
	if resolved == nil || resolved.Config == nil {
		return ""
	}
	if q, ok := resolved.Config["quantization"].(string); ok {
		return q
	}
	return ""
}

func isGenericDeploymentFailure(msg string) bool {
	switch strings.ToLower(strings.TrimSpace(msg)) {
	case "process exited before readiness", "unknown startup failure", "deployment metadata is stale; port is in use by another process":
		return true
	default:
		return false
	}
}

func summarizeErrorLines(errorLines string) string {
	lines := strings.Split(errorLines, "\n")
	bestLine := ""
	bestScore := 0
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		score := diagnosticLineScore(trimmed)
		if score > bestScore {
			bestLine = trimmed
			bestScore = score
		}
	}
	return bestLine
}

func diagnosticLineScore(line string) int {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case lower == "":
		return 0
	case isLowSignalErrorLine(line):
		return 0
	case strings.Contains(lower, "outofmemoryerror"), strings.Contains(lower, "out of memory"):
		return 130
	case strings.Contains(lower, "keyerror:"),
		strings.Contains(lower, "valueerror:"),
		strings.Contains(lower, "assertionerror:"),
		strings.Contains(lower, "typeerror:"),
		strings.Contains(lower, "indexerror:"),
		strings.Contains(lower, "filenotfounderror:"),
		strings.Contains(lower, "modulenotfounderror:"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "no such file"),
		strings.Contains(lower, "not found"):
		return 120
	case strings.Contains(lower, "see root cause above"),
		strings.Contains(lower, "failed core proc"),
		isGenericDeploymentFailure(line):
		return 20
	case strings.Contains(lower, "error:"),
		strings.Contains(lower, "exception"),
		strings.Contains(lower, "failed"),
		strings.Contains(lower, "cannot"),
		strings.Contains(lower, "panic"):
		return 80
	default:
		return 10
	}
}

func isLowSignalErrorLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case lower == "":
		return true
	case strings.HasPrefix(lower, "error in cpuinfo:"):
		return true
	default:
		return false
	}
}

// handlePowerSnapshot returns a JSON snapshot of current power/GPU metrics.
func handlePowerSnapshot(cat *knowledge.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx := r.Context()
		resp := map[string]any{"timestamp": time.Now().UTC()}

		metrics, err := hal.CollectMetrics(ctx)
		if err != nil || metrics == nil || metrics.GPU == nil {
			resp["available"] = false
		} else {
			resp["available"] = true
			resp["gpu"] = map[string]any{
				"power_draw_watts": metrics.GPU.PowerDrawWatts,
				"temperature_c":    metrics.GPU.TemperatureCelsius,
				"utilization_pct":  metrics.GPU.UtilizationPercent,
				"memory_used_mib":  metrics.GPU.MemoryUsedMiB,
				"memory_total_mib": metrics.GPU.MemoryTotalMiB,
			}
		}

		// Add TDP from hardware profile for context
		if hw, hwErr := hal.Detect(ctx); hwErr == nil && hw.GPU != nil {
			tdp := cat.FindHardwareTDP(knowledge.HardwareInfo{GPUArch: hw.GPU.Arch})
			if tdp > 0 {
				resp["tdp_watts"] = tdp
				if metrics != nil && metrics.GPU != nil && metrics.GPU.PowerDrawWatts > 0 {
					resp["power_utilization_pct"] = metrics.GPU.PowerDrawWatts / float64(tdp) * 100
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
