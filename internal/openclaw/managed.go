package openclaw

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const managedStateVersion = 1

// ManagedState records the config fragments AIMA currently owns inside
// openclaw.json. This avoids guessing ownership from user-editable values.
type ManagedState struct {
	Version                 int      `json:"version"`
	LLMProvider             string   `json:"llm_provider,omitempty"`
	MediaProvider           string   `json:"media_provider,omitempty"`
	AudioModels             []string `json:"audio_models,omitempty"`
	VisionModels            []string `json:"vision_models,omitempty"`
	TTSModel                string   `json:"tts_model,omitempty"`
	ImageGenerationProvider string   `json:"image_generation_provider,omitempty"`
	ImageGenerationModels   []string `json:"image_generation_models,omitempty"`
	MCPServerName           string   `json:"mcp_server_name,omitempty"`
}

func ManagedStatePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "aima-openclaw-managed.json")
}

func ReadManagedState(configPath string) (*ManagedState, error) {
	path := ManagedStatePath(configPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ManagedState{Version: managedStateVersion}, nil
		}
		return nil, fmt.Errorf("read openclaw managed state: %w", err)
	}
	var state ManagedState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse openclaw managed state: %w", err)
	}
	normalizeManagedState(&state)
	return &state, nil
}

func WriteManagedState(configPath string, state *ManagedState) error {
	path := ManagedStatePath(configPath)
	normalizeManagedState(state)
	if state == nil || state.Empty() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove openclaw managed state: %w", err)
		}
		return nil
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal openclaw managed state: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create openclaw managed state dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write openclaw managed state: %w", err)
	}
	return nil
}

func (s *ManagedState) Empty() bool {
	if s == nil {
		return true
	}
	return s.LLMProvider == "" &&
		s.MediaProvider == "" &&
		len(s.AudioModels) == 0 &&
		len(s.VisionModels) == 0 &&
		s.TTSModel == "" &&
		s.ImageGenerationProvider == "" &&
		len(s.ImageGenerationModels) == 0 &&
		s.MCPServerName == ""
}

func normalizeManagedState(state *ManagedState) {
	if state == nil {
		return
	}
	state.Version = managedStateVersion
	if state.ImageGenerationProvider == openAIImageProviderID {
		state.MediaProvider = ""
	}
	state.AudioModels = uniqueSorted(state.AudioModels)
	state.VisionModels = uniqueSorted(state.VisionModels)
	state.ImageGenerationModels = uniqueSorted(state.ImageGenerationModels)
	if state.MediaProvider != "" && len(state.AudioModels) == 0 && len(state.VisionModels) == 0 {
		state.MediaProvider = ""
	}
}

func legacyManagedHint(cfg map[string]any, proxyAddr string) bool {
	return providerManagedByAIMA(lookupMap(cfg, "models", "providers", aimaLLMProviderID), proxyAddr) ||
		providerManagedByAIMA(lookupMap(cfg, "models", "providers", legacyLLMProviderID), proxyAddr)
}

func managedSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		out[value] = struct{}{}
	}
	return out
}

func managedOwnsTTS(state *ManagedState) bool {
	return state != nil && state.TTSModel != ""
}

func managedOwnsMediaProvider(state *ManagedState) bool {
	return state != nil && state.MediaProvider != ""
}

func managedOwnsImageGeneration(state *ManagedState) bool {
	return state != nil && state.ImageGenerationProvider != ""
}
