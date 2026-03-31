package openclaw

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jguan/aima/internal/openclaw/skills"
)

// SyncResult holds the categorized models ready for OpenClaw config generation.
type SyncResult struct {
	LLMModels      []ModelEntry    `json:"llmModels,omitempty"`
	ASRModels      []AudioEntry    `json:"asrModels,omitempty"`
	TTSModel       *TTSEntry       `json:"ttsModel,omitempty"`
	ImageGenModels []ImageGenEntry `json:"imageGenModels,omitempty"`
	MCPServer      *MCPServerEntry `json:"mcpServer,omitempty"`
	ProxyAddr      string          `json:"proxyAddr"`
	APIKey         string          `json:"apiKey,omitempty"`
	ConfigPath     string          `json:"configPath"`
	ConfigExists   bool            `json:"configExists"`
	Written        bool            `json:"written"`
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
	}
	var ttsIDs []string

	for _, b := range backends {
		if !b.Ready || b.Remote {
			continue
		}

		modelType := deps.Catalog.ModelType(b.ModelName)
		switch modelType {
		case "llm", "vlm":
			entry := ModelEntry{
				ID:            b.ModelName,
				Name:          formatDisplayName(b.ModelName, modelType),
				ContextWindow: deps.Catalog.ModelContextWindow(b.ModelName),
				MaxTokens:     defaultMaxTokens(deps.Catalog.ModelContextWindow(b.ModelName)),
			}
			if modelType == "vlm" {
				entry.Input = []string{"text", "image"}
			} else {
				entry.Input = []string{"text"}
			}
			result.LLMModels = append(result.LLMModels, entry)

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

	managed, err := ReadManagedState(deps.ConfigPath)
	if err != nil {
		return result, fmt.Errorf("openclaw sync: %w", err)
	}

	merged, nextManaged := MergeAIMAConfigWithState(existing, managed, result)
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

	// Deploy AIMA skills to ~/.openclaw/skills/
	skillsDir := filepath.Join(filepath.Dir(deps.ConfigPath), "skills")
	if err := DeploySkills(skillsDir); err != nil {
		slog.Warn("openclaw sync: failed to deploy skills", "err", err)
	}

	slog.Info("openclaw sync complete",
		"llm", len(result.LLMModels),
		"asr", len(result.ASRModels),
		"tts", result.TTSModel != nil,
		"image_gen", len(result.ImageGenModels),
		"config", deps.ConfigPath)

	return result, nil
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
	return fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dest := filepath.Join(targetDir, path)
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		data, err := skills.FS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded skill %s: %w", path, err)
		}
		perm := os.FileMode(0644)
		if strings.HasSuffix(path, ".sh") {
			perm = 0755
		}
		return os.WriteFile(dest, data, perm)
	})
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
func defaultMaxTokens(contextWindow int) int {
	if contextWindow <= 0 {
		return 4096
	}
	// Default to 1/4 of context window, capped at 8192
	max := contextWindow / 4
	if max > 8192 {
		return 8192
	}
	if max < 1024 {
		return 1024
	}
	return max
}
