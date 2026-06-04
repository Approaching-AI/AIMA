package proxy

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// AdvertisedModel is the minimal model metadata needed for agent-side selection.
type AdvertisedModel struct {
	ID                  string `json:"id"`
	ModelType           string `json:"model_type,omitempty"`
	EngineType          string `json:"engine_type,omitempty"`
	ParameterCount      string `json:"parameter_count,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
	Remote              bool   `json:"remote,omitempty"`
}

var sizeTokenPattern = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*([bm])\b`)

// SortAdvertisedModels orders models for agent selection: stronger first, then
// larger context windows, then local before remote, then name for stability.
func SortAdvertisedModels(models []AdvertisedModel) {
	sort.SliceStable(models, func(i, j int) bool {
		return BetterAdvertisedModel(models[i], models[j])
	})
}

// BestAdvertisedModel returns the highest-priority model from the slice.
func BestAdvertisedModel(models []AdvertisedModel) (AdvertisedModel, bool) {
	var best AdvertisedModel
	found := false
	for _, candidate := range models {
		if !IsChatCapableAdvertisedModel(candidate) {
			continue
		}
		if !found {
			best = candidate
			found = true
			continue
		}
		if BetterAdvertisedModel(candidate, best) {
			best = candidate
		}
	}
	return best, found
}

// BetterAdvertisedModel reports whether a should be preferred over b.
func BetterAdvertisedModel(a, b AdvertisedModel) bool {
	aChat := IsChatCapableAdvertisedModel(a)
	bChat := IsChatCapableAdvertisedModel(b)
	if aChat != bChat {
		return aChat && !bChat
	}
	aScore := modelStrengthScore(a.ID, a.ParameterCount)
	bScore := modelStrengthScore(b.ID, b.ParameterCount)
	if aScore != bScore {
		return aScore > bScore
	}
	if a.ContextWindowTokens != b.ContextWindowTokens {
		return a.ContextWindowTokens > b.ContextWindowTokens
	}
	if a.Remote != b.Remote {
		return !a.Remote && b.Remote
	}
	return strings.ToLower(strings.TrimSpace(a.ID)) < strings.ToLower(strings.TrimSpace(b.ID))
}

// IsChatCapableAdvertisedModel reports whether a model can be used by the
// text/tool-calling agent. Empty type is accepted for generic OpenAI-compatible
// endpoints, then conservative name/engine hints filter common non-chat assets.
func IsChatCapableAdvertisedModel(model AdvertisedModel) bool {
	switch strings.ToLower(strings.TrimSpace(model.ModelType)) {
	case "llm", "vlm", "chat", "text":
		return true
	case "asr", "tts", "image_gen", "image", "video_gen", "embedding", "reranker":
		return false
	}
	if hasNonChatHint(model.EngineType) || hasNonChatHint(model.ID) {
		return false
	}
	return true
}

func hasNonChatHint(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return false
	}
	if strings.Contains(raw, "diffusers") || strings.Contains(raw, "embedding") || strings.Contains(raw, "rerank") {
		return true
	}
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for _, token := range tokens {
		switch token {
		case "asr", "tts", "stt", "whisper", "speech", "image", "img", "diffusion":
			return true
		}
	}
	return false
}

func modelStrengthScore(modelID, parameterCount string) float64 {
	if score := parseParameterCountScore(parameterCount); score > 0 {
		return score
	}
	return parseParameterCountScore(modelID)
}

func parseParameterCountScore(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	matches := sizeTokenPattern.FindAllStringSubmatch(strings.ToUpper(raw), -1)
	best := 0.0
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil || value <= 0 {
			continue
		}
		switch match[2] {
		case "M":
			value /= 1000
		case "B":
			// already in billions
		default:
			continue
		}
		if value > best {
			best = value
		}
	}
	if strings.Contains(strings.ToUpper(raw), "<1B") {
		return 0.999
	}
	return best
}
