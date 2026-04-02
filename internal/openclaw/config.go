package openclaw

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	json5 "github.com/yosuke-furukawa/json5/encoding/json5"
)

const (
	aimaLLMProviderID         = "aima"
	aimaMCPServerID           = "aima"
	aimaMediaProviderID       = "aima-media"
	legacyLLMProviderID       = "vllm"
	aimaImageGenProviderID    = "aima-imagegen"
	legacyImageGenProviderID  = "openai" // pre-v0.2.1: image gen used "openai" provider
	localOpenAIAPIKeyFallback = "local"
)

// ReadConfig reads and parses openclaw.json into a generic map.
func ReadConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read openclaw config: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err == nil {
		return cfg, nil
	}
	if err := json5.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse openclaw config: %w", err)
	}
	return cfg, nil
}

// WriteConfig writes the config map back to openclaw.json with indentation.
func WriteConfig(path string, cfg map[string]any) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal openclaw config: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create openclaw config dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write openclaw config: %w", err)
	}
	return nil
}

// MergeAIMAConfig merges AIMA-generated provider config into the existing
// OpenClaw config using explicit ownership tracked in ManagedState.
func MergeAIMAConfig(existing map[string]any, result *SyncResult) map[string]any {
	merged, _ := MergeAIMAConfigWithState(existing, nil, result)
	return merged
}

func MergeAIMAConfigWithState(existing map[string]any, managed *ManagedState, result *SyncResult) (map[string]any, *ManagedState) {
	if existing == nil {
		existing = make(map[string]any)
	}
	if result == nil {
		return existing, &ManagedState{Version: managedStateVersion}
	}
	next := &ManagedState{Version: managedStateVersion}

	mergeLLMProvider(existing, managed, next, result)
	next.AudioModels = mergeMediaModels(existing, "audio", audioIDs(result.ASRModels), managedSet(managedAudioModels(managed)), result.ProxyAddr, false)
	if len(next.AudioModels) > 0 {
		if audio := lookupMap(existing, "tools", "media", "audio"); audio != nil {
			audio["echoTranscript"] = true
		}
	}
	next.VisionModels = mergeMediaModels(existing, "image", modelIDs(result.VLMModels), managedSet(managedVisionModels(managed)), result.ProxyAddr, false)
	mergeImageModel(existing, managed, next, result)
	mergeTTS(existing, managed, next, result)
	mergeImageGeneration(existing, managed, next, result)
	ensureAudioAuthProvider(existing, managed, next, result)
	mergeLocalMediaProvider(existing, managed, next, result)
	mergeMCPServer(existing, managed, next, result)
	mergePluginAllowlist(existing, managed, next, result)

	pruneEmptyMaps(existing)
	normalizeManagedState(next)
	return existing, next
}

func mergeLLMProvider(cfg map[string]any, managed, next *ManagedState, result *SyncResult) {
	providers := ensureMap(ensureMap(cfg, "models"), "providers")
	if len(result.LLMModels) > 0 {
		providers[aimaLLMProviderID] = buildProviderConfig(result.ProxyAddr, directToolAPIKey(result.APIKey), buildLLMProviderModels(result.LLMModels))
		next.LLMProvider = aimaLLMProviderID
	} else {
		delete(providers, aimaLLMProviderID)
	}
	if legacyLLMProviderOwned(managed, cfg, result.ProxyAddr) {
		delete(providers, legacyLLMProviderID)
	}
}

func mergeTTS(cfg map[string]any, managed, next *ManagedState, result *SyncResult) {
	ownsTTS := managedOwnsTTS(managed)
	if result.TTSModel == nil {
		if ownsTTS {
			removeAIMATTS(cfg, managed, result.ProxyAddr)
		}
		return
	}
	if !canManageTTS(cfg, managed) {
		return
	}

	env := ensureMap(cfg, "env")
	env["OPENAI_TTS_BASE_URL"] = result.ProxyAddr
	messages := ensureMap(cfg, "messages")
	messages["tts"] = map[string]any{
		"provider": "openai",
		"providers": map[string]any{
			"openai": map[string]any{
				"apiKey":  directToolAPIKey(result.APIKey),
				"baseUrl": result.ProxyAddr,
				"model":   result.TTSModel.ID,
				"voice":   "default",
			},
		},
	}
	next.TTSModel = result.TTSModel.ID
}

func canManageTTS(cfg map[string]any, managed *ManagedState) bool {
	if managedOwnsTTS(managed) {
		return true
	}
	return lookupMap(cfg, "messages", "tts") == nil
}

func mergeImageModel(cfg map[string]any, managed, next *ManagedState, result *SyncResult) {
	desired := uniqueSorted(modelIDs(result.VLMModels))
	if len(desired) == 0 {
		if managedOwnsImageModel(managed) {
			removeAgentDefaultModelIfManaged(cfg, "imageModel", managed.ImageModelProvider)
		}
		return
	}
	if !canManageImageModel(cfg, managed, result) {
		return
	}
	setAgentDefaultModel(cfg, "imageModel", aimaMediaProviderID, desired)
	next.ImageModelProvider = aimaMediaProviderID
	next.ImageModelModels = desired
}

func canManageImageModel(cfg map[string]any, managed *ManagedState, result *SyncResult) bool {
	if managedOwnsImageModel(managed) {
		return true
	}
	if legacyImageModelOwned(cfg, result) {
		return true
	}
	return !hasAgentDefaultModel(cfg, "imageModel")
}

func legacyImageModelOwned(cfg map[string]any, result *SyncResult) bool {
	if result == nil {
		return false
	}
	expected := uniqueSorted(modelIDs(result.VLMModels))
	if len(expected) == 0 {
		return false
	}
	for _, providerID := range []string{aimaLLMProviderID, aimaMediaProviderID, legacyLLMProviderID} {
		provider := lookupMap(cfg, "models", "providers", providerID)
		if !providerManagedByAIMA(provider, result.ProxyAddr) {
			continue
		}
		configured := configuredAgentDefaultModelsForProviders(cfg, "imageModel", []string{providerID}, result.ProxyAddr)
		if stringSlicesEqual(uniqueSorted(configured), expected) {
			return true
		}
	}
	return false
}

func mergeImageGeneration(cfg map[string]any, managed, next *ManagedState, result *SyncResult) {
	ownsImageGen := managedOwnsImageGeneration(managed)
	if len(result.ImageGenModels) == 0 {
		if ownsImageGen {
			removeImageGeneration(cfg, managed)
		}
		return
	}
	legacyOwned := managed != nil && managed.ImageGenerationProvider == legacyImageGenProviderID
	if !legacyOwned {
		legacyOwned = legacyImageGenerationOwned(cfg, result)
	}
	if !canManageImageGeneration(cfg, managed, result) {
		return
	}

	providers := ensureMap(ensureMap(cfg, "models"), "providers")
	providers[aimaImageGenProviderID] = buildProviderConfig(result.ProxyAddr, directToolAPIKey(result.APIKey), []any{})
	setAgentDefaultModel(cfg, "imageGenerationModel", aimaImageGenProviderID, imageGenIDs(result.ImageGenModels))
	next.ImageGenerationProvider = aimaImageGenProviderID
	next.ImageGenerationModels = imageGenIDs(result.ImageGenModels)

	// Clean up legacy "openai" provider if it was used for image gen.
	if legacyOwned {
		removeProviderIfPresent(cfg, legacyImageGenProviderID)
		removeAgentDefaultModelIfManaged(cfg, "imageGenerationModel", legacyImageGenProviderID)
	}
}

func canManageImageGeneration(cfg map[string]any, managed *ManagedState, result *SyncResult) bool {
	if managedOwnsImageGeneration(managed) {
		return true
	}
	if legacyImageGenerationOwned(cfg, result) {
		return true
	}
	if hasAgentDefaultModel(cfg, "imageGenerationModel") {
		return false
	}
	return lookupMap(cfg, "models", "providers", aimaImageGenProviderID) == nil
}

func legacyImageGenerationOwned(cfg map[string]any, result *SyncResult) bool {
	if result == nil || len(result.ImageGenModels) == 0 {
		return false
	}
	provider := lookupMap(cfg, "models", "providers", legacyImageGenProviderID)
	if !providerManagedByAIMA(provider, result.ProxyAddr) {
		return false
	}
	expected := uniqueSorted(imageGenIDs(result.ImageGenModels))
	if !stringSlicesEqual(uniqueSorted(providerModels(provider)), expected) {
		return false
	}
	configured := configuredAgentDefaultModelsForProviders(cfg, "imageGenerationModel", []string{legacyImageGenProviderID}, result.ProxyAddr)
	return stringSlicesEqual(uniqueSorted(configured), expected)
}

// ensureAudioAuthProvider ensures models.providers.openai has an apiKey when
// ASR audio models are deployed. OpenClaw's transcription pipeline resolves
// the API key via resolveApiKeyForProvider('openai') and silently skips
// transcription when no key is found.
func ensureAudioAuthProvider(cfg map[string]any, managed, next *ManagedState, result *SyncResult) {
	const providerID = "openai"
	ownsAudioAuth := managed != nil && managed.AudioAuthProvider == providerID

	if len(next.AudioModels) == 0 {
		if ownsAudioAuth {
			provider := lookupMap(cfg, "models", "providers", providerID)
			if providerManagedByAIMA(provider, result.ProxyAddr) {
				removeProviderIfPresent(cfg, providerID)
			}
		}
		return
	}

	existing := lookupMap(cfg, "models", "providers", providerID)
	if existing != nil {
		if _, hasKey := existing["apiKey"]; !hasKey {
			existing["apiKey"] = directToolAPIKey(result.APIKey)
		}
		if ownsAudioAuth || providerManagedByAIMA(existing, result.ProxyAddr) {
			next.AudioAuthProvider = providerID
		}
		return
	}

	providers := ensureMap(ensureMap(cfg, "models"), "providers")
	providers[providerID] = map[string]any{
		"apiKey":  directToolAPIKey(result.APIKey),
		"baseUrl": result.ProxyAddr,
		"models":  []any{},
	}
	next.AudioAuthProvider = providerID
}

func removeImageGeneration(cfg map[string]any, managed *ManagedState) {
	if !managedOwnsImageGeneration(managed) {
		return
	}
	removeAgentDefaultModelIfManaged(cfg, "imageGenerationModel", managed.ImageGenerationProvider)
	removeProviderIfPresent(cfg, managed.ImageGenerationProvider)
}

func legacyLLMProviderOwned(managed *ManagedState, cfg map[string]any, proxyAddr string) bool {
	if managed != nil && managed.LLMProvider == legacyLLMProviderID {
		return true
	}
	return providerManagedByAIMA(lookupMap(cfg, "models", "providers", legacyLLMProviderID), proxyAddr)
}

func managedAudioModels(managed *ManagedState) []string {
	if managed == nil {
		return nil
	}
	return managed.AudioModels
}

func managedVisionModels(managed *ManagedState) []string {
	if managed == nil {
		return nil
	}
	return managed.VisionModels
}

func buildProviderConfig(baseURL, apiKey string, models []any) map[string]any {
	cfg := map[string]any{
		"baseUrl": baseURL,
		"api":     "openai-completions",
	}
	if models != nil {
		cfg["models"] = models
	}
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		cfg["apiKey"] = trimmed
	}
	return cfg
}

func buildLLMProviderModels(models []ModelEntry) []any {
	out := make([]any, 0, len(models))
	for _, m := range models {
		out = append(out, map[string]any{
			"id":            m.ID,
			"name":          m.Name,
			"input":         m.Input,
			"contextWindow": m.ContextWindow,
			"maxTokens":     m.MaxTokens,
			"cost":          map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		})
	}
	return out
}

func buildLocalMediaProviderModels(result *SyncResult) []any {
	if result == nil {
		return nil
	}
	out := make([]any, 0, len(result.ASRModels)+len(result.VLMModels))
	for _, model := range result.ASRModels {
		out = append(out, map[string]any{
			"id":            model.ID,
			"name":          formatDisplayName(model.ID, "asr"),
			"input":         []string{"text"},
			"contextWindow": 8192,
			"maxTokens":     1024,
			"cost":          zeroCost(),
		})
	}
	for _, model := range result.VLMModels {
		out = append(out, map[string]any{
			"id":            model.ID,
			"name":          model.Name,
			"input":         append([]string(nil), model.Input...),
			"contextWindow": model.ContextWindow,
			"maxTokens":     model.MaxTokens,
			"cost":          zeroCost(),
		})
	}
	return out
}

func modelIDs(models []ModelEntry) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func audioIDs(models []AudioEntry) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func imageGenIDs(models []ImageGenEntry) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func mergeMediaModels(cfg map[string]any, key string, desired []string, owned map[string]struct{}, proxyAddr string, allowLegacy bool) []string {
	existing := lookupMap(cfg, "tools", "media")
	section := map[string]any(nil)
	if existing != nil {
		section = copyMap(existing[key])
	}
	preserved := keepUnmanagedMediaModels(section, owned, proxyAddr, allowLegacy)
	preserved = dropDesiredMediaDuplicates(preserved, desired, proxyAddr)
	if len(desired) == 0 && len(preserved) == 0 {
		removeMediaSection(cfg, key)
		return nil
	}

	tools := ensureMap(cfg, "tools")
	media := ensureMap(tools, "media")
	if section == nil {
		section = make(map[string]any)
	}
	section["enabled"] = true
	models := make([]any, 0, len(desired)+len(preserved))
	for _, id := range desired {
		models = append(models, map[string]any{
			"provider": "openai",
			"model":    id,
			"baseUrl":  proxyAddr,
		})
	}
	models = append(models, preserved...)
	section["models"] = models
	media[key] = section
	return desired
}

func dropDesiredMediaDuplicates(models []any, desired []string, proxyAddr string) []any {
	if len(models) == 0 || len(desired) == 0 {
		return models
	}
	desiredSet := managedSet(desired)
	if len(desiredSet) == 0 {
		return models
	}
	out := make([]any, 0, len(models))
	for _, raw := range models {
		entry, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		if normalizeURL(asString(entry["baseUrl"])) == normalizeURL(proxyAddr) {
			if _, ok := desiredSet[asString(entry["model"])]; ok {
				continue
			}
		}
		out = append(out, raw)
	}
	return out
}

func mergeLocalMediaProvider(cfg map[string]any, managed, next *ManagedState, result *SyncResult) {
	if result == nil {
		return
	}
	needsProvider := len(next.AudioModels) > 0 || len(next.VisionModels) > 0
	provider := lookupMap(cfg, "models", "providers", aimaMediaProviderID)
	if !needsProvider {
		if managedOwnsMediaProvider(managed) && providerManagedByAIMA(provider, result.ProxyAddr) {
			removeProviderIfPresent(cfg, aimaMediaProviderID)
		}
		return
	}
	if provider != nil && !managedOwnsMediaProvider(managed) {
		return
	}
	providers := ensureMap(ensureMap(cfg, "models"), "providers")
	providers[aimaMediaProviderID] = buildProviderConfig(result.ProxyAddr, directToolAPIKey(result.APIKey), buildLocalMediaProviderModels(result))
	next.MediaProvider = aimaMediaProviderID
}

func keepUnmanagedMediaModels(section map[string]any, owned map[string]struct{}, proxyAddr string, allowLegacy bool) []any {
	if section == nil {
		return nil
	}
	models, ok := section["models"].([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(models))
	for _, raw := range models {
		entry, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		if isManagedMediaEntry(entry, owned, proxyAddr, allowLegacy) {
			continue
		}
		out = append(out, raw)
	}
	return out
}

func isManagedMediaEntry(entry map[string]any, owned map[string]struct{}, proxyAddr string, allowLegacy bool) bool {
	if owned != nil {
		if _, ok := owned[asString(entry["model"])]; ok {
			return true
		}
	}
	if normalizeURL(asString(entry["baseUrl"])) != normalizeURL(proxyAddr) {
		return false
	}
	return allowLegacy
}

func removeMediaSection(cfg map[string]any, key string) {
	tools, ok := cfg["tools"].(map[string]any)
	if !ok {
		return
	}
	media, ok := tools["media"].(map[string]any)
	if !ok {
		return
	}
	delete(media, key)
}

func removeAIMATTS(cfg map[string]any, managed *ManagedState, proxyAddr string) {
	if managedOwnsTTS(managed) || currentTTSManagedByAIMA(cfg, proxyAddr) {
		if messages, ok := cfg["messages"].(map[string]any); ok {
			delete(messages, "tts")
		}
	}
	if env, ok := cfg["env"].(map[string]any); ok {
		if managedOwnsTTS(managed) || normalizeURL(asString(env["OPENAI_TTS_BASE_URL"])) == normalizeURL(proxyAddr) {
			delete(env, "OPENAI_TTS_BASE_URL")
		}
	}
}

func currentTTSManagedByAIMA(cfg map[string]any, proxyAddr string) bool {
	openai := lookupTTSProvider(cfg)
	if openai == nil {
		return false
	}
	if normalizeURL(asString(openai["baseUrl"])) == normalizeURL(proxyAddr) {
		return true
	}
	env, _ := cfg["env"].(map[string]any)
	return normalizeURL(asString(env["OPENAI_TTS_BASE_URL"])) == normalizeURL(proxyAddr)
}

func lookupTTSProvider(cfg map[string]any) map[string]any {
	if provider := lookupMap(cfg, "messages", "tts", "providers", "openai"); provider != nil {
		return provider
	}
	return lookupMap(cfg, "messages", "tts", "openai")
}

func providerManagedByAIMA(provider map[string]any, proxyAddr string) bool {
	if provider == nil {
		return false
	}
	return normalizeURL(asString(provider["baseUrl"])) == normalizeURL(proxyAddr)
}

func directToolAPIKey(apiKey string) string {
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		return trimmed
	}
	return localOpenAIAPIKeyFallback
}

func zeroCost() map[string]any {
	return map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}
}

func copyMap(raw any) map[string]any {
	src, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func lookupAgentDefault(cfg map[string]any, key string) any {
	defaults := lookupMap(cfg, "agents", "defaults")
	if defaults == nil {
		return nil
	}
	return defaults[key]
}

func hasAgentDefaultModel(cfg map[string]any, key string) bool {
	defaults := lookupMap(cfg, "agents", "defaults")
	if defaults == nil {
		return false
	}
	_, ok := defaults[key]
	return ok
}

func setAgentDefaultModel(cfg map[string]any, key, providerID string, modelIDs []string) {
	if len(modelIDs) == 0 {
		return
	}
	defaults := ensureMap(ensureMap(cfg, "agents"), "defaults")
	refs := make([]string, 0, len(modelIDs))
	for _, id := range modelIDs {
		refs = append(refs, providerID+"/"+id)
	}
	value := map[string]any{"primary": refs[0]}
	if len(refs) > 1 {
		fallbacks := make([]any, 0, len(refs)-1)
		for _, ref := range refs[1:] {
			fallbacks = append(fallbacks, ref)
		}
		value["fallbacks"] = fallbacks
	}
	defaults[key] = value
}

func removeAgentDefaultModelIfManaged(cfg map[string]any, key, providerID string) {
	agents, ok := cfg["agents"].(map[string]any)
	if !ok {
		return
	}
	defaults, ok := agents["defaults"].(map[string]any)
	if !ok {
		return
	}
	refs := parseAgentModelRefs(defaults[key])
	if len(refs) == 0 {
		delete(defaults, key)
		return
	}
	for _, ref := range refs {
		if ref.Provider != providerID {
			return
		}
	}
	delete(defaults, key)
}

func removeProviderIfPresent(cfg map[string]any, providerID string) {
	models, ok := cfg["models"].(map[string]any)
	if !ok {
		return
	}
	providers, ok := models["providers"].(map[string]any)
	if !ok {
		return
	}
	delete(providers, providerID)
}

func mergeMCPServer(cfg map[string]any, managed, next *ManagedState, result *SyncResult) {
	status := result.MCPServer
	if status == nil {
		return
	}
	if strings.TrimSpace(status.Command) == "" {
		status.Action = "skipped"
		status.Reason = "aima mcp command unavailable"
		return
	}

	servers := ensureMap(ensureMap(cfg, "mcp"), "servers")
	name := status.Name
	if name == "" {
		name = aimaMCPServerID
	}
	existingRaw, exists := servers[name]
	owns := managed != nil && managed.MCPServerName == name
	if exists && !owns {
		existing, ok := existingRaw.(map[string]any)
		if !ok {
			status.Registered = true
			status.Managed = false
			status.Action = "preserved_unmanaged"
			status.Reason = fmt.Sprintf("existing mcp.servers.%s is not an object", name)
			return
		}
		status.Registered = true
		status.Managed = false
		status.Command = asString(existing["command"])
		status.Args = stringArgs(existing["args"])
		status.Action = "preserved_unmanaged"
		status.Reason = fmt.Sprintf("existing mcp.servers.%s is not AIMA-managed", name)
		return
	}

	servers[name] = map[string]any{
		"command": status.Command,
		"args":    append([]string(nil), status.Args...),
	}
	next.MCPServerName = name
	status.Registered = true
	status.Managed = true
	status.Action = "managed"
}

func mergePluginAllowlist(cfg map[string]any, managed, next *ManagedState, result *SyncResult) {
	desired := desiredPluginRoots(result)
	plugins := ensureMap(cfg, "plugins")
	existing := stringArgs(plugins["allow"])
	owned := managedSet(managedPluginAllow(managed))
	seen := make(map[string]struct{}, len(existing)+len(desired))
	allow := make([]string, 0, len(existing)+len(desired))
	for _, id := range existing {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := owned[id]; ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		allow = append(allow, id)
	}
	for _, id := range desired {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		allow = append(allow, id)
	}
	if len(allow) == 0 {
		delete(plugins, "allow")
		next.PluginAllow = nil
		return
	}
	plugins["allow"] = allow
	next.PluginAllow = append([]string(nil), desired...)
}

func stringArgs(value any) []string {
	switch raw := value.(type) {
	case []string:
		return append([]string(nil), raw...)
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func ensureMap(cfg map[string]any, key string) map[string]any {
	v, ok := cfg[key].(map[string]any)
	if !ok {
		v = make(map[string]any)
		cfg[key] = v
	}
	return v
}

func pruneEmptyMaps(cfg map[string]any) {
	prunePath(cfg, "models", "providers")
	prunePath(cfg, "models")
	prunePath(cfg, "mcp", "servers")
	prunePath(cfg, "mcp")
	prunePath(cfg, "plugins")
	prunePath(cfg, "tools", "media")
	prunePath(cfg, "tools")
	prunePath(cfg, "messages")
	prunePath(cfg, "env")
	prunePath(cfg, "agents", "defaults")
	prunePath(cfg, "agents")
}

func prunePath(cfg map[string]any, keys ...string) {
	if len(keys) == 0 {
		return
	}
	if len(keys) == 1 {
		if child, ok := cfg[keys[0]].(map[string]any); ok && len(child) == 0 {
			delete(cfg, keys[0])
		}
		return
	}
	child, ok := cfg[keys[0]].(map[string]any)
	if !ok {
		return
	}
	prunePath(child, keys[1:]...)
	if len(child) == 0 {
		delete(cfg, keys[0])
	}
}
