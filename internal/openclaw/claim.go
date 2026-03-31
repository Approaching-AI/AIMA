package openclaw

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	claimSectionAll      = "all"
	claimSectionLLM      = "llm"
	claimSectionASR      = "asr"
	claimSectionVision   = "vision"
	claimSectionTTS      = "tts"
	claimSectionImageGen = "image_gen"
)

var claimSectionOrder = []string{
	claimSectionLLM,
	claimSectionASR,
	claimSectionVision,
	claimSectionTTS,
	claimSectionImageGen,
}

type ClaimOptions struct {
	DryRun   bool
	Sections []string
}

type ClaimResult struct {
	ConfigPath        string       `json:"configPath"`
	ManagedStatePath  string       `json:"managedStatePath"`
	RequestedSections []string     `json:"requestedSections,omitempty"`
	Detected          ModelSummary `json:"detected,omitempty"`
	Claimed           ModelSummary `json:"claimed,omitempty"`
	Written           bool         `json:"written"`
	Issues            []string     `json:"issues,omitempty"`
}

func Claim(ctx context.Context, deps *Deps, opts ClaimOptions) (*ClaimResult, error) {
	if deps == nil {
		return nil, fmt.Errorf("openclaw claim: nil deps")
	}
	expectedResult, err := Sync(ctx, deps, true)
	if err != nil {
		return nil, fmt.Errorf("openclaw claim: %w", err)
	}
	expected := summarizeExpected(expectedResult)

	sections, err := NormalizeClaimSections(opts.Sections)
	if err != nil {
		return nil, fmt.Errorf("openclaw claim: %w", err)
	}

	cfg, err := ReadConfig(deps.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &ClaimResult{
				ConfigPath:        deps.ConfigPath,
				ManagedStatePath:  ManagedStatePath(deps.ConfigPath),
				RequestedSections: sections,
				Issues:            []string{"openclaw.json not found; nothing to claim"},
			}, nil
		}
		return nil, fmt.Errorf("openclaw claim: %w", err)
	}

	managed, err := ReadManagedState(deps.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("openclaw claim: %w", err)
	}

	detectedState := claimableState(cfg, managed, expected, deps.ProxyAddr)
	claimedState := selectClaimSections(detectedState, sections)

	result := &ClaimResult{
		ConfigPath:        deps.ConfigPath,
		ManagedStatePath:  ManagedStatePath(deps.ConfigPath),
		RequestedSections: sections,
		Detected:          summaryFromManagedState(cfg, detectedState),
		Claimed:           summaryFromManagedState(cfg, claimedState),
	}
	if summaryCount(result.Claimed) == 0 {
		result.Issues = append(result.Issues, "no claimable OpenClaw config matched the requested sections")
		result.Issues = uniqueSorted(result.Issues)
		return result, nil
	}
	if opts.DryRun {
		return result, nil
	}

	next := mergeManagedStates(managed, claimedState)
	if err := WriteManagedState(deps.ConfigPath, next); err != nil {
		return nil, err
	}
	result.Written = true
	return result, nil
}

func NormalizeClaimSections(sections []string) ([]string, error) {
	if len(sections) == 0 {
		return append([]string(nil), claimSectionOrder...), nil
	}
	seen := make(map[string]struct{}, len(sections))
	out := make([]string, 0, len(sections))
	for _, section := range sections {
		for _, raw := range strings.Split(section, ",") {
			name := canonicalClaimSection(raw)
			if name == "" {
				return nil, fmt.Errorf("unsupported claim section %q", raw)
			}
			if name == claimSectionAll {
				return append([]string(nil), claimSectionOrder...), nil
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), claimSectionOrder...), nil
	}
	return out, nil
}

func canonicalClaimSection(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", claimSectionAll:
		return claimSectionAll
	case claimSectionLLM:
		return claimSectionLLM
	case claimSectionASR, "audio":
		return claimSectionASR
	case claimSectionVision, "image":
		return claimSectionVision
	case claimSectionTTS:
		return claimSectionTTS
	case claimSectionImageGen, "imagegen", "image-gen":
		return claimSectionImageGen
	default:
		return ""
	}
}

func claimableState(cfg map[string]any, managed *ManagedState, expected ModelSummary, proxyAddr string) *ManagedState {
	candidate := detectLegacyState(cfg, proxyAddr)
	if candidate == nil {
		return &ManagedState{Version: managedStateVersion}
	}

	candidate = limitClaimableState(cfg, candidate, expected)
	next := &ManagedState{
		Version:                 managedStateVersion,
		LLMProvider:             candidate.LLMProvider,
		MediaProvider:           candidate.MediaProvider,
		AudioModels:             append([]string(nil), candidate.AudioModels...),
		VisionModels:            append([]string(nil), candidate.VisionModels...),
		TTSModel:                candidate.TTSModel,
		ImageGenerationProvider: candidate.ImageGenerationProvider,
		ImageGenerationModels:   append([]string(nil), candidate.ImageGenerationModels...),
	}
	if managed != nil {
		if managed.LLMProvider != "" {
			next.LLMProvider = ""
		}
		if managedOwnsMediaProvider(managed) {
			next.MediaProvider = ""
		}
		next.AudioModels = subtractStrings(next.AudioModels, managed.AudioModels)
		next.VisionModels = subtractStrings(next.VisionModels, managed.VisionModels)
		if managedOwnsTTS(managed) {
			next.TTSModel = ""
		}
		if managedOwnsImageGeneration(managed) {
			next.ImageGenerationProvider = ""
			next.ImageGenerationModels = nil
		}
	}
	normalizeManagedState(next)
	return next
}

func limitClaimableState(cfg map[string]any, candidate *ManagedState, expected ModelSummary) *ManagedState {
	if candidate == nil {
		return &ManagedState{Version: managedStateVersion}
	}
	limited := &ManagedState{
		Version:                 managedStateVersion,
		LLMProvider:             candidate.LLMProvider,
		MediaProvider:           candidate.MediaProvider,
		AudioModels:             intersectStrings(candidate.AudioModels, expected.ASRModels),
		VisionModels:            intersectStrings(candidate.VisionModels, expected.VisionModels),
		TTSModel:                candidate.TTSModel,
		ImageGenerationProvider: candidate.ImageGenerationProvider,
		ImageGenerationModels:   append([]string(nil), candidate.ImageGenerationModels...),
	}

	if candidate.LLMProvider != "" {
		configuredModels := uniqueSorted(providerModels(lookupMap(cfg, "models", "providers", candidate.LLMProvider)))
		if !stringSlicesEqual(configuredModels, expected.ChatModels) {
			limited.LLMProvider = ""
		}
	}
	if limited.TTSModel == "" || limited.TTSModel != expected.TTSModel {
		limited.TTSModel = ""
	}
	if len(limited.AudioModels) == 0 && len(limited.VisionModels) == 0 {
		limited.MediaProvider = ""
	}
	if limited.ImageGenerationProvider != "" {
		limited.ImageGenerationModels = uniqueSorted(limited.ImageGenerationModels)
		if !stringSlicesEqual(limited.ImageGenerationModels, expected.ImageGenModels) {
			limited.ImageGenerationProvider = ""
			limited.ImageGenerationModels = nil
		}
	}

	normalizeManagedState(limited)
	return limited
}

func detectLegacyState(cfg map[string]any, proxyAddr string) *ManagedState {
	state := &ManagedState{Version: managedStateVersion}
	if providerManagedByAIMA(lookupMap(cfg, "models", "providers", aimaLLMProviderID), proxyAddr) {
		state.LLMProvider = aimaLLMProviderID
	} else if providerManagedByAIMA(lookupMap(cfg, "models", "providers", legacyLLMProviderID), proxyAddr) {
		state.LLMProvider = legacyLLMProviderID
	}
	state.AudioModels = mediaModels(lookupMap(cfg, "tools", "media", "audio"), proxyAddr)
	state.VisionModels = mediaModels(lookupMap(cfg, "tools", "media", "image"), proxyAddr)
	if currentTTSManagedByAIMA(cfg, proxyAddr) {
		if tts := lookupTTSProvider(cfg); tts != nil {
			state.TTSModel = asString(tts["model"])
		}
	}
	if state.ImageGenerationProvider == "" &&
		providerManagedByAIMA(lookupMap(cfg, "models", "providers", openAIImageProviderID), proxyAddr) &&
		(len(state.AudioModels) > 0 || len(state.VisionModels) > 0) {
		state.MediaProvider = openAIImageProviderID
	}
	if providerManagedByAIMA(lookupMap(cfg, "models", "providers", openAIImageProviderID), proxyAddr) {
		models := configuredAgentDefaultModelsForProviders(cfg, "imageGenerationModel", []string{openAIImageProviderID}, proxyAddr)
		if len(models) > 0 {
			state.ImageGenerationProvider = openAIImageProviderID
			state.ImageGenerationModels = models
		}
	}
	normalizeManagedState(state)
	return state
}

func selectClaimSections(state *ManagedState, sections []string) *ManagedState {
	if state == nil {
		return &ManagedState{Version: managedStateVersion}
	}
	allowed := managedSet(sections)
	selected := &ManagedState{Version: managedStateVersion}
	if _, ok := allowed[claimSectionLLM]; ok {
		selected.LLMProvider = state.LLMProvider
	}
	if _, ok := allowed[claimSectionASR]; ok {
		selected.MediaProvider = state.MediaProvider
		selected.AudioModels = append([]string(nil), state.AudioModels...)
	}
	if _, ok := allowed[claimSectionVision]; ok {
		selected.MediaProvider = state.MediaProvider
		selected.VisionModels = append([]string(nil), state.VisionModels...)
	}
	if _, ok := allowed[claimSectionTTS]; ok {
		selected.TTSModel = state.TTSModel
	}
	if _, ok := allowed[claimSectionImageGen]; ok {
		selected.ImageGenerationProvider = state.ImageGenerationProvider
		selected.ImageGenerationModels = append([]string(nil), state.ImageGenerationModels...)
	}
	normalizeManagedState(selected)
	return selected
}

func mergeManagedStates(existing, extra *ManagedState) *ManagedState {
	next := &ManagedState{Version: managedStateVersion}
	if existing != nil {
		next.LLMProvider = existing.LLMProvider
		next.MediaProvider = existing.MediaProvider
		next.AudioModels = append(next.AudioModels, existing.AudioModels...)
		next.VisionModels = append(next.VisionModels, existing.VisionModels...)
		next.TTSModel = existing.TTSModel
		next.ImageGenerationProvider = existing.ImageGenerationProvider
		next.ImageGenerationModels = append(next.ImageGenerationModels, existing.ImageGenerationModels...)
	}
	if extra != nil {
		if extra.LLMProvider != "" {
			next.LLMProvider = extra.LLMProvider
		}
		if extra.MediaProvider != "" {
			next.MediaProvider = extra.MediaProvider
		}
		next.AudioModels = append(next.AudioModels, extra.AudioModels...)
		next.VisionModels = append(next.VisionModels, extra.VisionModels...)
		if extra.TTSModel != "" {
			next.TTSModel = extra.TTSModel
		}
		if extra.ImageGenerationProvider != "" {
			next.ImageGenerationProvider = extra.ImageGenerationProvider
		}
		next.ImageGenerationModels = append(next.ImageGenerationModels, extra.ImageGenerationModels...)
	}
	normalizeManagedState(next)
	return next
}

func summaryFromManagedState(cfg map[string]any, state *ManagedState) ModelSummary {
	var summary ModelSummary
	if state == nil {
		return summary
	}
	if state.LLMProvider != "" {
		summary.ChatModels = append(summary.ChatModels, providerModels(lookupMap(cfg, "models", "providers", state.LLMProvider))...)
	}
	summary.ASRModels = append(summary.ASRModels, state.AudioModels...)
	summary.VisionModels = append(summary.VisionModels, state.VisionModels...)
	summary.TTSModel = state.TTSModel
	summary.ImageGenModels = append(summary.ImageGenModels, state.ImageGenerationModels...)
	normalizeSummary(&summary)
	return summary
}

func subtractStrings(values, remove []string) []string {
	if len(values) == 0 {
		return nil
	}
	blocked := managedSet(remove)
	if len(blocked) == 0 {
		return append([]string(nil), values...)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := blocked[value]; ok {
			continue
		}
		out = append(out, value)
	}
	return uniqueSorted(out)
}

func intersectStrings(values, allowed []string) []string {
	if len(values) == 0 || len(allowed) == 0 {
		return nil
	}
	allowedSet := managedSet(allowed)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := allowedSet[value]; ok {
			out = append(out, value)
		}
	}
	return uniqueSorted(out)
}

func stringSlicesEqual(a, b []string) bool {
	a = uniqueSorted(a)
	b = uniqueSorted(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
