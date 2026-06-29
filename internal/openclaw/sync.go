package openclaw

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jguan/aima/internal/openclaw/plugins"
	"github.com/jguan/aima/internal/openclaw/skills"
)

var deployedSkillRoots = []string{
	"aima-control",
}

var deployedPluginRoots = []string{
	"aima-local-audio",
	"aima-local-image",
	"aima-local-tts",
}

// SyncResult holds the categorized models ready for OpenClaw config generation.
type SyncResult struct {
	LLMModels        []ModelEntry    `json:"llmModels,omitempty"`
	VLMModels        []ModelEntry    `json:"vlmModels,omitempty"`
	ASRModels        []AudioEntry    `json:"asrModels,omitempty"`
	TTSModel         *TTSEntry       `json:"ttsModel,omitempty"`
	ImageGenModels   []ImageGenEntry `json:"imageGenModels,omitempty"`
	MCPServer        *MCPServerEntry `json:"mcpServer,omitempty"`
	ProxyAddr        string          `json:"proxyAddr"`
	APIKey           string          `json:"apiKey,omitempty"`
	ProxyReachable   bool            `json:"proxyReachable"`
	ProxyWarning     string          `json:"proxyWarning,omitempty"`
	SkipDefaultModel bool            `json:"skipDefaultModel,omitempty"`
	ConfigPath       string          `json:"configPath"`
	ConfigExists     bool            `json:"configExists"`
	Written          bool            `json:"written"`
}

// MCPServerEntry describes the stdio MCP server entry AIMA wants OpenClaw to use.
type MCPServerEntry struct {
	Name       string   `json:"name"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	Registered bool     `json:"registered"`
	Managed    bool     `json:"managed"`
	Action     string   `json:"action,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// ModelEntry represents an LLM/VLM model for OpenClaw's provider config.
type ModelEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Input         []string `json:"input"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
}

// AudioEntry represents an ASR model for OpenClaw's tools.media.audio.
type AudioEntry struct {
	ID string `json:"id"`
}

// TTSEntry represents a TTS model for OpenClaw's messages.tts.
type TTSEntry struct {
	ID string `json:"id"`
}

// ImageGenEntry represents an image generation model exposed through
// OpenClaw's image-generation provider wiring.
type ImageGenEntry struct {
	ID string `json:"id"`
}

// Sync reads deployed backends, categorizes by modality, and writes OpenClaw config.
func Sync(ctx context.Context, deps *Deps, dryRun bool) (*SyncResult, error) {
	backends := deps.Backends.ListBackends()

	result := &SyncResult{
		ProxyAddr:  deps.ProxyAddr,
		APIKey:     deps.proxyAPIKey(),
		ConfigPath: deps.ConfigPath,
		MCPServer:  desiredMCPServer(deps),
		// Skip touching OpenClaw's primary chat model only when explicitly disabled.
		SkipDefaultModel: deps.SetDefaultModel != nil && !*deps.SetDefaultModel,
	}

	// Preflight: the provider we write points OpenClaw's chat data plane at
	// deps.ProxyAddr (the `aima serve` proxy, default :6188). If serve is not
	// listening there — or an HTTP_PROXY env intercepts loopback — OpenClaw
	// fails every request with a connection error/timeout that looks nothing
	// like a config problem. Probe it now and surface a loud, actionable
	// warning instead of letting the partner discover it inside OpenClaw.
	result.ProxyReachable, result.ProxyWarning = probeProxyReachable(ctx, deps.ProxyAddr, deps.proxyAPIKey())
	if !result.ProxyReachable {
		slog.Warn("openclaw sync: AIMA proxy preflight failed",
			"proxy", deps.ProxyAddr, "detail", result.ProxyWarning)
	}

	// Read managed state up front so the categorization loop can skip models the
	// user has revoked from sync (ExcludedModels). The same state is reused for
	// the merge below.
	managed, err := ReadManagedState(deps.ConfigPath)
	if err != nil {
		return result, fmt.Errorf("openclaw sync: %w", err)
	}

	var ttsIDs []string

	for _, b := range backends {
		if !b.Ready || b.Remote {
			continue
		}
		if managed.IsExcluded(b.ModelName) {
			continue // user revoked this model from OpenClaw sync
		}

		modelType := strings.TrimSpace(deps.Catalog.ModelType(b.ModelName))
		if modelType == "" {
			modelType = strings.TrimSpace(b.ModelType)
		}
		switch modelType {
		case "llm", "vlm":
			ctxWindow := b.ContextWindowTokens // prefer actual deployment config
			if ctxWindow <= 0 {
				ctxWindow = deps.Catalog.ModelContextWindow(b.ModelName) // fallback to catalog
			}
			entry := ModelEntry{
				ID:            b.ModelName,
				Name:          formatDisplayName(b.ModelName, modelType),
				ContextWindow: ctxWindow,
				MaxTokens:     defaultMaxTokens(ctxWindow),
			}
			if modelType == "vlm" {
				entry.Input = []string{"text", "image"}
			} else {
				entry.Input = []string{"text"}
			}
			if deps.Catalog.ModelChatProvider(b.ModelName) {
				result.LLMModels = append(result.LLMModels, entry)
			} else {
				result.VLMModels = append(result.VLMModels, entry)
			}

		case "asr":
			result.ASRModels = append(result.ASRModels, AudioEntry{ID: b.ModelName})

		case "tts":
			ttsIDs = append(ttsIDs, b.ModelName)

		case "image_gen":
			result.ImageGenModels = append(result.ImageGenModels, ImageGenEntry{ID: b.ModelName})

		default:
			slog.Debug("openclaw sync: skipping model with unknown type",
				"model", b.ModelName, "type", modelType)
		}
	}
	sort.Slice(result.LLMModels, func(i, j int) bool { return result.LLMModels[i].ID < result.LLMModels[j].ID })
	sort.Slice(result.VLMModels, func(i, j int) bool { return result.VLMModels[i].ID < result.VLMModels[j].ID })
	sort.Slice(result.ASRModels, func(i, j int) bool { return result.ASRModels[i].ID < result.ASRModels[j].ID })
	sort.Slice(result.ImageGenModels, func(i, j int) bool { return result.ImageGenModels[i].ID < result.ImageGenModels[j].ID })
	sort.Strings(ttsIDs)
	if len(ttsIDs) > 0 {
		result.TTSModel = &TTSEntry{ID: ttsIDs[0]}
	}

	// Read existing config (may not exist yet)
	existing, err := ReadConfig(deps.ConfigPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && !os.IsNotExist(err) {
			return result, fmt.Errorf("openclaw sync: %w", err)
		}
		existing = make(map[string]any)
	} else {
		result.ConfigExists = true
	}

	merged, nextManaged := MergeAIMAConfigWithState(existing, managed, result)
	// Preserve user revocations across the reconcile: MergeAIMAConfigWithState
	// rebuilds nextManaged from "what AIMA wrote", which never includes them.
	nextManaged.ExcludedModels = managed.ExcludedModels
	if dryRun {
		return result, nil
	}
	if err := WriteConfig(deps.ConfigPath, merged); err != nil {
		return result, err
	}
	if err := WriteManagedState(deps.ConfigPath, nextManaged); err != nil {
		return result, err
	}
	result.Written = true

	stateDir := filepath.Dir(deps.ConfigPath)
	// Deploy AIMA skills to ~/.openclaw/skills/
	skillsDir := filepath.Join(stateDir, "skills")
	if err := DeploySkills(skillsDir); err != nil {
		slog.Warn("openclaw sync: failed to deploy skills", "err", err)
	}
	pluginsDir := filepath.Join(stateDir, "extensions")
	if err := deployPluginsWithRoots(pluginsDir, desiredPluginRoots(result)); err != nil {
		return result, fmt.Errorf("openclaw sync: deploy plugins: %w", err)
	}

	slog.Info("openclaw sync complete",
		"llm", len(result.LLMModels),
		"vlm", len(result.VLMModels),
		"asr", len(result.ASRModels),
		"tts", result.TTSModel != nil,
		"image_gen", len(result.ImageGenModels),
		"config", deps.ConfigPath)

	return result, nil
}

// probeProxyReachable checks whether the AIMA serve proxy is actually reachable
// at proxyAddr — the address the OpenClaw provider written by this sync points at
// for the chat data plane. It runs two probes so the warning can name the real
// failure mode:
//
//  1. direct (no proxy): is `aima serve` listening at all? The MCP control plane
//     (`aima mcp`) does NOT open this port, so wiring only the MCP server leaves
//     nothing serving :6188 and every OpenClaw request dies with a connection error.
//  2. env-proxy: mirrors clients that honor HTTP_PROXY/HTTPS_PROXY (OpenClaw runs on
//     Node, which does). If the direct probe succeeds but this one fails, an env
//     proxy is intercepting loopback and OpenClaw will time out even though curl
//     (which bypasses the proxy for localhost) works.
//
// Returns (reachable, warning). reachable is true only when both probes succeed.
// A malformed/empty address is treated as reachable (no false alarm).
func probeProxyReachable(ctx context.Context, proxyAddr, apiKey string) (bool, string) {
	addr := strings.TrimSpace(proxyAddr)
	if addr == "" {
		return true, ""
	}
	probeURL := strings.TrimRight(addr, "/") + "/models"

	probe := func(proxyFn func(*http.Request) (*url.URL, error)) error {
		reqCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, probeURL, nil)
		if err != nil {
			return nil // can't build probe — don't raise a false alarm
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		client := &http.Client{Transport: &http.Transport{Proxy: proxyFn}}
		defer client.CloseIdleConnections()
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		// Any HTTP status (200/401/404/...) proves serve is listening.
		resp.Body.Close()
		return nil
	}

	if err := probe(nil); err != nil {
		return false, fmt.Sprintf("AIMA proxy not reachable at %s — nothing is serving this port, so OpenClaw will fail every request with a connection error/timeout. Start the data plane with `aima serve` (note: the MCP server `aima mcp` does NOT open this port). Underlying error: %v", addr, err)
	}
	if err := probe(http.ProxyFromEnvironment); err != nil {
		return false, fmt.Sprintf("AIMA proxy at %s is reachable directly but NOT through your HTTP_PROXY/HTTPS_PROXY environment — OpenClaw runs on Node and routes loopback through that proxy, so it will time out even though curl works. Set NO_PROXY=127.0.0.1,localhost,::1 (and lowercase no_proxy) for the OpenClaw process, or unset the proxy. Underlying error: %v", addr, err)
	}
	return true, ""
}

func desiredMCPServer(deps *Deps) *MCPServerEntry {
	if deps == nil {
		return nil
	}
	command := strings.TrimSpace(deps.MCPCommand)
	if command == "" {
		command = "aima"
	}
	return &MCPServerEntry{
		Name:    aimaMCPServerID,
		Command: command,
		Args:    []string{"mcp", "--profile", "operator"},
	}
}

// DeploySkills copies embedded AIMA skills to the target directory.
// Existing files are overwritten to keep skills in sync with the binary.
func DeploySkills(targetDir string) error {
	return deployEmbeddedRoots(skills.FS, targetDir, deployedSkillRoots, true)
}

// DeployPlugins copies embedded AIMA OpenClaw plugins to the target directory.
func DeployPlugins(targetDir string) error {
	return deployPluginsWithRoots(targetDir, deployedPluginRoots)
}

func deployPluginsWithRoots(targetDir string, roots []string) error {
	return deployEmbeddedRoots(plugins.FS, targetDir, roots, true)
}

func desiredPluginRoots(result *SyncResult) []string {
	if result == nil {
		return nil
	}
	roots := make([]string, 0, 3)
	if len(result.ASRModels) > 0 {
		roots = append(roots, "aima-local-audio")
	}
	if len(result.ImageGenModels) > 0 {
		roots = append(roots, "aima-local-image")
	}
	if result.TTSModel != nil {
		roots = append(roots, "aima-local-tts")
	}
	return roots
}

func deployEmbeddedRoots(embedded fs.FS, targetDir string, roots []string, pruneStale bool) error {
	if pruneStale {
		if err := pruneEmbeddedRoots(embedded, targetDir, roots); err != nil {
			return err
		}
	}
	for _, root := range roots {
		if err := fs.WalkDir(embedded, root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			dest := filepath.Join(targetDir, path)
			if d.IsDir() {
				return os.MkdirAll(dest, 0755)
			}
			data, err := fs.ReadFile(embedded, path)
			if err != nil {
				return fmt.Errorf("read embedded asset %s: %w", path, err)
			}
			perm := os.FileMode(0644)
			if strings.HasSuffix(path, ".sh") {
				perm = 0755
			}
			return os.WriteFile(dest, data, perm)
		}); err != nil {
			return err
		}
	}
	return nil
}

func pruneEmbeddedRoots(embedded fs.FS, targetDir string, keepRoots []string) error {
	entries, err := fs.ReadDir(embedded, ".")
	if err != nil {
		return fmt.Errorf("read embedded root entries: %w", err)
	}
	keep := make(map[string]struct{}, len(keepRoots))
	for _, root := range keepRoots {
		keep[root] = struct{}{}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, ok := keep[name]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(targetDir, name)); err != nil {
			return fmt.Errorf("remove stale embedded root %s: %w", name, err)
		}
	}
	return nil
}

// formatDisplayName creates a human-readable display name from a model ID.
// e.g. "qwen3-8b" -> "Qwen3 8B (AIMA)"
func formatDisplayName(modelName, modelType string) string {
	parts := strings.Split(modelName, "-")
	for i, p := range parts {
		if len(p) > 0 {
			// Capitalize size suffixes (b, m, etc.)
			upper := strings.ToUpper(p)
			if isSizeSuffix(upper) {
				parts[i] = upper
			} else {
				// Capitalize first letter
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
	}
	name := strings.Join(parts, " ")

	suffix := "AIMA"
	if modelType == "vlm" {
		suffix = "AIMA VLM"
	}
	return fmt.Sprintf("%s (%s)", name, suffix)
}

// isSizeSuffix returns true for common model size suffixes like "8b", "0.6b".
func isSizeSuffix(s string) bool {
	if len(s) < 2 {
		return false
	}
	return s[len(s)-1] == 'B' && (s[0] >= '0' && s[0] <= '9')
}

// defaultMaxTokens returns a reasonable maxTokens based on context window.
// This is the maximum output tokens OpenClaw will allow per request.
func defaultMaxTokens(contextWindow int) int {
	if contextWindow <= 0 {
		return 4096
	}
	// Use half the context window for output (other half reserved for input),
	// with a floor of 1024 tokens.
	max := contextWindow / 2
	if max < 1024 {
		return 1024
	}
	return max
}
