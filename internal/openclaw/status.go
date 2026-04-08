package openclaw

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const defaultGatewayURL = "http://127.0.0.1:18789"

var gatewayListenRE = regexp.MustCompile(`ws://(?:127\.0\.0\.1|\[::1\]):([0-9]+)`)

// Status describes how completely AIMA is integrated with the local OpenClaw instance.
type Status struct {
	Connected      bool            `json:"connected"`
	GatewayLive    bool            `json:"gateway_live"`
	GatewayURL     string          `json:"gateway_url,omitempty"`
	ConfigPath     string          `json:"config_path"`
	ConfigExists   bool            `json:"config_exists"`
	ProxyAddr      string          `json:"proxy_addr"`
	AIMAConfigured bool            `json:"aima_configured"`
	ClaimNeeded    bool            `json:"claim_needed"`
	SyncReady      bool            `json:"sync_ready"`
	PluginDrift    bool            `json:"plugin_drift,omitempty"`
	MCPServer      *MCPServerEntry `json:"mcp_server,omitempty"`
	Expected       ModelSummary    `json:"expected"`
	Configured     ModelSummary    `json:"configured"`
	Claimable      ModelSummary    `json:"claimable,omitempty"`
	Issues         []string        `json:"issues,omitempty"`
}

// ModelSummary captures the OpenClaw-facing models that AIMA expects or has configured.
type ModelSummary struct {
	ChatModels      []string `json:"chat_models,omitempty"`
	VisionModels    []string `json:"vision_models,omitempty"`
	ImageToolModels []string `json:"image_tool_models,omitempty"`
	ASRModels       []string `json:"asr_models,omitempty"`
	TTSModel        string   `json:"tts_model,omitempty"`
	ImageGenModels  []string `json:"image_gen_models,omitempty"`
}

// Inspect returns a best-effort snapshot of the current OpenClaw wiring state.
func Inspect(ctx context.Context, deps *Deps) (*Status, error) {
	if deps == nil {
		return nil, fmt.Errorf("openclaw inspect: nil deps")
	}

	expectedResult, err := Sync(ctx, deps, true)
	if err != nil {
		return nil, fmt.Errorf("openclaw inspect dry-run: %w", err)
	}

	status := &Status{
		ConfigPath: deps.ConfigPath,
		ProxyAddr:  deps.ProxyAddr,
		GatewayURL: detectGatewayURL(deps.ConfigPath),
		Expected:   summarizeExpected(expectedResult),
	}
	pluginReady := true
	managed, managedErr := ReadManagedState(deps.ConfigPath)
	if managedErr != nil {
		status.Issues = append(status.Issues, managedErr.Error())
		managed = &ManagedState{Version: managedStateVersion}
	}

	if info, err := os.Stat(deps.ConfigPath); err == nil && !info.IsDir() {
		status.ConfigExists = true
	} else if err != nil && !os.IsNotExist(err) {
		status.Issues = append(status.Issues, fmt.Sprintf("inspect openclaw config: %v", err))
	}

	if status.ConfigExists {
		cfg, err := ReadConfig(deps.ConfigPath)
		if err != nil {
			status.Issues = append(status.Issues, err.Error())
		} else {
			status.Configured = summarizeConfigured(cfg, managed, deps.ProxyAddr)
			status.Claimable = summaryFromManagedState(cfg, claimableState(cfg, managed, status.Expected, deps.ProxyAddr))
			status.ClaimNeeded = summaryCount(status.Claimable) > 0
			status.AIMAConfigured = summaryCount(status.Configured) > 0
			status.MCPServer = inspectMCPServer(cfg, managed, expectedResult.MCPServer)
			pluginIssues := inspectManagedPlugins(deps.ConfigPath, cfg, desiredPluginRoots(expectedResult))
			if len(pluginIssues) > 0 {
				pluginReady = false
				status.PluginDrift = true
				status.Issues = append(status.Issues, pluginIssues...)
			}
		}
	}

	if status.GatewayURL != "" {
		live, err := probeGateway(ctx, status.GatewayURL)
		status.GatewayLive = live
		if err != nil {
			status.Issues = append(status.Issues, fmt.Sprintf("openclaw gateway unreachable at %s: %v", status.GatewayURL, err))
		}
	}

	mcpReady := status.MCPServer == nil || status.MCPServer.Registered
	status.SyncReady = summariesEqual(status.Expected, status.Configured) && mcpReady && pluginReady
	if status.ClaimNeeded {
		status.Issues = append(status.Issues, "legacy OpenClaw config points to the AIMA proxy but is not yet claimed; run openclaw with action=claim")
	}
	if status.MCPServer != nil {
		switch {
		case !status.ConfigExists:
			status.Issues = append(status.Issues, "openclaw.json not found; run openclaw with action=sync to register the AIMA MCP server")
		case !status.MCPServer.Registered:
			status.Issues = append(status.Issues, "mcp.servers.aima is missing from openclaw.json")
		case !status.MCPServer.Managed:
			status.Issues = append(status.Issues, "mcp.servers.aima exists but is not AIMA-managed; AIMA will preserve it")
		}
	}
	if summaryCount(status.Expected) > 0 {
		switch {
		case !status.ConfigExists:
			status.Issues = append(status.Issues, "openclaw.json not found; run openclaw with action=sync to export AIMA providers")
		case !status.AIMAConfigured && !status.ClaimNeeded:
			status.Issues = append(status.Issues, "AIMA providers are not present in openclaw.json")
		case !status.SyncReady:
			status.Issues = append(status.Issues, "openclaw.json is out of sync with the current ready AIMA backends")
		}
	}
	if summaryCount(status.Expected) == 0 && status.AIMAConfigured {
		status.Issues = append(status.Issues, "openclaw.json still contains AIMA providers, but no local ready AIMA backends are available")
	}

	status.Issues = uniqueSorted(status.Issues)
	status.Connected = status.GatewayLive && status.AIMAConfigured && status.SyncReady && !status.ClaimNeeded
	return status, nil
}

func inspectManagedPlugins(configPath string, cfg map[string]any, desired []string) []string {
	if len(desired) == 0 {
		return nil
	}
	plugins := lookupMap(cfg, "plugins")
	allowed := managedSet(stringArgs(plugins["allow"]))
	extensionsDir := filepath.Join(filepath.Dir(configPath), "extensions")
	var issues []string
	for _, id := range desired {
		if _, ok := allowed[id]; !ok {
			issues = append(issues, fmt.Sprintf("plugins.allow is missing AIMA-managed plugin %q", id))
		}
		if !pluginAssetsExist(extensionsDir, id) {
			issues = append(issues, fmt.Sprintf("AIMA-managed plugin %q is missing deployed assets under %s", id, filepath.Join(extensionsDir, id)))
		}
	}
	return issues
}

func pluginAssetsExist(extensionsDir, id string) bool {
	for _, name := range []string{"index.js", "openclaw.plugin.json", "package.json"} {
		info, err := os.Stat(filepath.Join(extensionsDir, id, name))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func summarizeExpected(result *SyncResult) ModelSummary {
	var summary ModelSummary
	if result == nil {
		return summary
	}

	for _, model := range result.LLMModels {
		summary.ChatModels = append(summary.ChatModels, model.ID)
	}
	for _, model := range result.VLMModels {
		summary.VisionModels = append(summary.VisionModels, model.ID)
		summary.ImageToolModels = append(summary.ImageToolModels, model.ID)
	}
	for _, model := range result.ASRModels {
		summary.ASRModels = append(summary.ASRModels, model.ID)
	}
	if result.TTSModel != nil {
		summary.TTSModel = result.TTSModel.ID
	}
	for _, model := range result.ImageGenModels {
		summary.ImageGenModels = append(summary.ImageGenModels, model.ID)
	}
	normalizeSummary(&summary)
	return summary
}

func summarizeConfigured(cfg map[string]any, managed *ManagedState, proxyAddr string) ModelSummary {
	var summary ModelSummary
	legacyLLM := managed == nil || managed.Empty()
	if legacyLLM {
		legacyLLM = legacyManagedHint(cfg, proxyAddr)
	}

	if managed != nil && managed.LLMProvider != "" {
		if provider := lookupMap(cfg, "models", "providers", managed.LLMProvider); providerManagedByAIMA(provider, proxyAddr) {
			for _, id := range providerModels(provider) {
				summary.ChatModels = append(summary.ChatModels, id)
			}
		}
	} else if legacyLLM {
		for _, providerID := range []string{aimaLLMProviderID, legacyLLMProviderID} {
			if provider := lookupMap(cfg, "models", "providers", providerID); providerManagedByAIMA(provider, proxyAddr) {
				for _, id := range providerModels(provider) {
					summary.ChatModels = append(summary.ChatModels, id)
				}
			}
		}
	}

	if media := lookupMap(cfg, "tools", "media"); media != nil {
		if managed != nil && len(managed.AudioModels) > 0 {
			summary.ASRModels = append(summary.ASRModels, managedMediaModels(media["audio"], managed.AudioModels, proxyAddr)...)
		}
		if managed != nil && len(managed.VisionModels) > 0 {
			summary.VisionModels = append(summary.VisionModels, managedMediaModels(media["image"], managed.VisionModels, proxyAddr)...)
		}
	}

	if managedOwnsImageModel(managed) {
		summary.ImageToolModels = append(summary.ImageToolModels, configuredAgentDefaultModelsForProviders(cfg, "imageModel", []string{managed.ImageModelProvider}, proxyAddr)...)
	}

	if managedOwnsTTS(managed) && currentTTSManagedByAIMA(cfg, proxyAddr) {
		if tts := lookupTTSProvider(cfg); tts != nil {
			summary.TTSModel = asString(tts["model"])
		}
	}

	if managedOwnsImageGeneration(managed) {
		summary.ImageGenModels = append(summary.ImageGenModels, configuredAgentDefaultModelsForProviders(cfg, "imageGenerationModel", []string{managed.ImageGenerationProvider}, proxyAddr)...)
	}

	normalizeSummary(&summary)
	return summary
}

func providerModels(provider map[string]any) []string {
	models, ok := provider["models"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(models))
	for _, raw := range models {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if id := asString(entry["id"]); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func mediaModels(section any, proxyAddr string) []string {
	entry, ok := section.(map[string]any)
	if !ok {
		return nil
	}
	models, ok := entry["models"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(models))
	for _, raw := range models {
		model, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if normalizeURL(asString(model["baseUrl"])) != normalizeURL(proxyAddr) {
			continue
		}
		if name := asString(model["model"]); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func managedMediaModels(section any, owned []string, proxyAddr string) []string {
	entry, ok := section.(map[string]any)
	if !ok {
		return nil
	}
	models, ok := entry["models"].([]any)
	if !ok {
		return nil
	}
	ownedSet := managedSet(owned)
	if len(ownedSet) == 0 {
		return nil
	}
	out := make([]string, 0, len(models))
	for _, raw := range models {
		model, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if normalizeURL(asString(model["baseUrl"])) != normalizeURL(proxyAddr) {
			continue
		}
		name := asString(model["model"])
		if _, ok := ownedSet[name]; !ok {
			continue
		}
		out = append(out, name)
	}
	return out
}

func configuredAgentDefaultModelsForProviders(cfg map[string]any, key string, providerIDs []string, proxyAddr string) []string {
	defaults := lookupMap(cfg, "agents", "defaults")
	if defaults == nil {
		return nil
	}
	refs := parseAgentModelRefs(defaults[key])
	if len(refs) == 0 {
		return nil
	}
	allowed := managedSet(providerIDs)
	if len(allowed) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if _, ok := allowed[ref.Provider]; !ok {
			continue
		}
		if provider := lookupMap(cfg, "models", "providers", ref.Provider); providerManagedByAIMA(provider, proxyAddr) {
			out = append(out, ref.Model)
		}
	}
	return out
}

type modelRef struct {
	Provider string
	Model    string
}

func parseAgentModelRefs(raw any) []modelRef {
	switch value := raw.(type) {
	case string:
		if ref := parseModelRef(value); ref != nil {
			return []modelRef{*ref}
		}
	case map[string]any:
		var refs []modelRef
		if ref := parseModelRef(asString(value["primary"])); ref != nil {
			refs = append(refs, *ref)
		}
		if fallbacks, ok := value["fallbacks"].([]any); ok {
			for _, rawFallback := range fallbacks {
				if ref := parseModelRef(asString(rawFallback)); ref != nil {
					refs = append(refs, *ref)
				}
			}
		}
		return refs
	}
	return nil
}

func parseModelRef(raw string) *modelRef {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	slash := strings.Index(raw, "/")
	if slash <= 0 || slash == len(raw)-1 {
		return nil
	}
	return &modelRef{
		Provider: strings.TrimSpace(raw[:slash]),
		Model:    strings.TrimSpace(raw[slash+1:]),
	}
}

func inspectMCPServer(cfg map[string]any, managed *ManagedState, desired *MCPServerEntry) *MCPServerEntry {
	if desired == nil {
		return nil
	}
	status := &MCPServerEntry{
		Name:    desired.Name,
		Command: desired.Command,
		Args:    append([]string(nil), desired.Args...),
	}
	servers := lookupMap(cfg, "mcp", "servers")
	if servers == nil {
		return status
	}
	raw, ok := servers[desired.Name]
	if !ok {
		return status
	}
	status.Registered = true
	entry, ok := raw.(map[string]any)
	if !ok {
		status.Managed = false
		status.Action = "preserved_unmanaged"
		status.Reason = fmt.Sprintf("existing mcp.servers.%s is not an object", desired.Name)
		return status
	}
	status.Command = asString(entry["command"])
	status.Args = stringArgs(entry["args"])
	status.Managed = managed != nil && managed.MCPServerName == desired.Name
	if status.Managed {
		status.Action = "managed"
	} else {
		status.Action = "preserved_unmanaged"
		status.Reason = fmt.Sprintf("existing mcp.servers.%s is not AIMA-managed", desired.Name)
	}
	return status
}

func summariesEqual(a, b ModelSummary) bool {
	return strings.Join(a.ChatModels, "\x00") == strings.Join(b.ChatModels, "\x00") &&
		strings.Join(a.VisionModels, "\x00") == strings.Join(b.VisionModels, "\x00") &&
		strings.Join(a.ImageToolModels, "\x00") == strings.Join(b.ImageToolModels, "\x00") &&
		strings.Join(a.ASRModels, "\x00") == strings.Join(b.ASRModels, "\x00") &&
		a.TTSModel == b.TTSModel &&
		strings.Join(a.ImageGenModels, "\x00") == strings.Join(b.ImageGenModels, "\x00")
}

func summaryCount(summary ModelSummary) int {
	count := len(summary.ChatModels) + len(summary.VisionModels) + len(summary.ImageToolModels) + len(summary.ASRModels) + len(summary.ImageGenModels)
	if summary.TTSModel != "" {
		count++
	}
	return count
}

func normalizeSummary(summary *ModelSummary) {
	if summary == nil {
		return
	}
	summary.ChatModels = uniqueSorted(summary.ChatModels)
	summary.VisionModels = uniqueSorted(summary.VisionModels)
	summary.ImageToolModels = uniqueSorted(summary.ImageToolModels)
	summary.ASRModels = uniqueSorted(summary.ASRModels)
	summary.ImageGenModels = uniqueSorted(summary.ImageGenModels)
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func lookupMap(cfg map[string]any, keys ...string) map[string]any {
	current := cfg
	for i, key := range keys {
		if current == nil {
			return nil
		}
		value, ok := current[key]
		if !ok {
			return nil
		}
		if i == len(keys)-1 {
			next, _ := value.(map[string]any)
			return next
		}
		next, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func asString(value any) string {
	s, _ := value.(string)
	return s
}

func normalizeURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func detectGatewayURL(configPath string) string {
	if raw := strings.TrimSpace(os.Getenv("OPENCLAW_GATEWAY_URL")); raw != "" {
		return normalizeURL(raw)
	}
	if port := strings.TrimSpace(os.Getenv("OPENCLAW_GATEWAY_PORT")); port != "" {
		return "http://127.0.0.1:" + port
	}

	logPath := filepath.Join(filepath.Dir(configPath), "logs", "gateway.log")
	data, err := os.ReadFile(logPath)
	if err == nil {
		matches := gatewayListenRE.FindAllStringSubmatch(string(data), -1)
		if len(matches) > 0 && len(matches[len(matches)-1]) > 1 {
			return "http://127.0.0.1:" + matches[len(matches)-1][1]
		}
	}
	return defaultGatewayURL
}

func probeGateway(ctx context.Context, gatewayURL string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalizeURL(gatewayURL)+"/health", nil)
	if err != nil {
		return false, err
	}

	client := &http.Client{Timeout: 600 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected health status %d", resp.StatusCode)
	}
	return true, nil
}
