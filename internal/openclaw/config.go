package openclaw

import (
	"encoding/json"
	"fmt"
	"os"
)

// ReadConfig reads and parses openclaw.json into a generic map.
func ReadConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read openclaw config: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
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
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write openclaw config: %w", err)
	}
	return nil
}

// MergeAIMAConfig merges AIMA-generated provider config into the existing
// OpenClaw config. Only touches paths managed by AIMA:
//   - models.providers.vllm (LLM/VLM models)
//   - tools.media.audio (ASR models)
//   - tools.media.image (image generation models)
//   - messages.tts (TTS model)
func MergeAIMAConfig(existing map[string]any, result *SyncResult) map[string]any {
	if existing == nil {
		existing = make(map[string]any)
	}

	// --- LLM/VLM: models.providers.vllm ---
	if len(result.LLMModels) > 0 {
		models := ensureMap(existing, "models")
		providers := ensureMap(models, "providers")

		modelEntries := make([]any, len(result.LLMModels))
		for i, m := range result.LLMModels {
			entry := map[string]any{
				"id":            m.ID,
				"name":          m.Name,
				"input":         m.Input,
				"contextWindow": m.ContextWindow,
				"maxTokens":     m.MaxTokens,
				"cost":          map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
			}
			modelEntries[i] = entry
		}
		providers["vllm"] = map[string]any{
			"baseUrl": result.ProxyAddr,
			"apiKey":  result.APIKey,
			"api":     "openai-completions",
			"models":  modelEntries,
		}
	} else {
		// No LLM models: remove vllm provider if it points to AIMA
		removeAIMAProvider(existing, result.ProxyAddr)
	}

	// --- ASR: tools.media.audio ---
	if len(result.ASRModels) > 0 {
		tools := ensureMap(existing, "tools")
		media := ensureMap(tools, "media")

		audioModels := make([]any, len(result.ASRModels))
		for i, m := range result.ASRModels {
			audioModels[i] = map[string]any{
				"provider": "openai",
				"model":    m.ID,
				"baseUrl":  result.ProxyAddr,
			}
		}
		media["audio"] = map[string]any{
			"enabled": true,
			"models":  audioModels,
		}
	}

	// --- Image Generation: tools.media.image ---
	if len(result.ImageGenModels) > 0 {
		tools := ensureMap(existing, "tools")
		media := ensureMap(tools, "media")

		imageModels := make([]any, len(result.ImageGenModels))
		for i, m := range result.ImageGenModels {
			imageModels[i] = map[string]any{
				"provider": "openai",
				"model":    m.ID,
				"baseUrl":  result.ProxyAddr,
			}
		}
		media["image"] = map[string]any{
			"enabled": true,
			"models":  imageModels,
		}
	}

	// --- TTS: messages.tts ---
	if result.TTSModel != nil {
		messages := ensureMap(existing, "messages")
		messages["tts"] = map[string]any{
			"provider": "openai",
			"openai": map[string]any{
				"model":   result.TTSModel.ID,
				"baseUrl": result.ProxyAddr,
				"voice":   "default",
			},
		}
	}

	return existing
}

// removeAIMAProvider removes the vllm provider if its baseUrl matches AIMA's proxy.
func removeAIMAProvider(cfg map[string]any, proxyAddr string) {
	models, ok := cfg["models"].(map[string]any)
	if !ok {
		return
	}
	providers, ok := models["providers"].(map[string]any)
	if !ok {
		return
	}
	vllm, ok := providers["vllm"].(map[string]any)
	if !ok {
		return
	}
	if baseURL, _ := vllm["baseUrl"].(string); baseURL == proxyAddr {
		delete(providers, "vllm")
	}
}

// ensureMap returns cfg[key] as map[string]any, creating it if needed.
func ensureMap(cfg map[string]any, key string) map[string]any {
	v, ok := cfg[key].(map[string]any)
	if !ok {
		v = make(map[string]any)
		cfg[key] = v
	}
	return v
}
