package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/runtime"

	state "github.com/jguan/aima/internal"
	"gopkg.in/yaml.v3"
)

type llmSettings struct {
	Endpoint    string
	Model       string
	APIKey      string
	UserAgent   string
	ExtraParams map[string]any
}

func defaultLLMEndpoint() string {
	return fmt.Sprintf("http://localhost:%d/v1", proxy.DefaultPort)
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

func applyLLMSettings(llmClient *agent.OpenAIClient, settings llmSettings) {
	if llmClient == nil {
		return
	}
	llmClient.SetEndpoint(settings.Endpoint)
	llmClient.SetModel(settings.Model)
	llmClient.SetAPIKey(settings.APIKey)
	llmClient.SetUserAgent(settings.UserAgent)
	llmClient.SetExtraParams(settings.ExtraParams)
}

func reloadLLMSettings(ctx context.Context, db *state.DB, llmClient *agent.OpenAIClient, localAPIKey string) llmSettings {
	settings := loadLLMSettings(ctx, db)
	if settings.APIKey == "" && strings.TrimSpace(localAPIKey) != "" && agent.IsLoopbackEndpoint(settings.Endpoint) {
		settings.APIKey = localAPIKey
	}
	applyLLMSettings(llmClient, settings)
	return settings
}

func agentAvailable(ctx context.Context, llmClient *agent.OpenAIClient) bool {
	if llmClient == nil {
		return false
	}
	return llmClient.Available(ctx)
}

func buildAgentStatusPayload(ctx context.Context, llmClient *agent.OpenAIClient, toolMode string, activeRuns int) (json.RawMessage, error) {
	payload := map[string]any{
		"agent_available":         false,
		"agent_tool_mode":         toolMode,
		"active_exploration_runs": activeRuns,
	}
	if llmClient == nil {
		payload["llm_route"] = agent.RouteStatus{Error: "no LLM client configured"}
		return json.Marshal(payload)
	}

	statusCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	route := llmClient.RouteStatus(statusCtx)
	payload["agent_available"] = route.Available
	payload["llm_route"] = route
	return json.Marshal(payload)
}

func loadLLMSettings(ctx context.Context, db *state.DB) llmSettings {
	settings := llmSettings{
		Endpoint: defaultLLMEndpoint(),
	}
	if endpoint := os.Getenv("AIMA_LLM_ENDPOINT"); endpoint != "" {
		settings.Endpoint = agent.EnsureHTTPScheme(endpoint)
	} else if v, err := db.GetConfig(ctx, "llm.endpoint"); err == nil && v != "" {
		settings.Endpoint = agent.EnsureHTTPScheme(v)
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
	} else if agent.IsLoopbackEndpoint(settings.Endpoint) {
		// When targeting local proxy with no explicit LLM key,
		// fall back to proxy API key so agent can self-authenticate.
		if v, err := db.GetConfig(ctx, "api_key"); err == nil && v != "" {
			settings.APIKey = v
		}
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
		models := proxy.QueryRemoteStatus(ctx, addr, svc.Port, apiKey)
		bestModel, ok := proxy.BestAdvertisedModel(models)
		if !ok || strings.TrimSpace(bestModel.ID) == "" {
			continue
		}
		baseURL := fmt.Sprintf("http://%s:%d/v1", addr, svc.Port)
		slog.Debug("fleet LLM discovery: candidate", "addr", baseURL, "models", models)
		endpoints = append(endpoints, agent.FleetEndpoint{
			BaseURL:             baseURL,
			Model:               bestModel.ID,
			ParameterCount:      bestModel.ParameterCount,
			ContextWindowTokens: bestModel.ContextWindowTokens,
		})
	}
	sort.SliceStable(endpoints, func(i, j int) bool {
		return proxy.BetterAdvertisedModel(
			proxy.AdvertisedModel{
				ID:                  endpoints[i].Model,
				ParameterCount:      endpoints[i].ParameterCount,
				ContextWindowTokens: endpoints[i].ContextWindowTokens,
				Remote:              true,
			},
			proxy.AdvertisedModel{
				ID:                  endpoints[j].Model,
				ParameterCount:      endpoints[j].ParameterCount,
				ContextWindowTokens: endpoints[j].ContextWindowTokens,
				Remote:              true,
			},
		)
	})
	return endpoints
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
			if !deployAutoPullAllowed(ctx) {
				name := "engine binary"
				if src != nil && strings.TrimSpace(src.Binary) != "" {
					name = src.Binary
				}
				return "", fmt.Errorf("%s not found locally and auto-pull is disabled", name)
			}
			return bm.Resolve(ctx, src)
		}),
		runtime.WithNativeEngineAssets(engineAssets),
	)
}

func isLightweightInvocation() bool {
	for _, a := range os.Args[1:] {
		switch a {
		case "-h", "--help", "help", "completion":
			return true
		case "version":
			return true
		}
	}
	// No subcommand at all -> full init (auto-serve with browser open).
	return false
}

func defaultRootArgs(args []string) []string {
	if len(args) <= 1 {
		return []string{"serve"}
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
