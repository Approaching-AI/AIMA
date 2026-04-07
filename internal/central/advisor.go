package central

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// stripCodeFence removes markdown code fences that LLMs often wrap around JSON.
func stripCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "```") {
		if i := strings.Index(trimmed, "\n"); i != -1 {
			trimmed = trimmed[i+1:]
		}
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	return trimmed
}

// LLMCompleter is the interface for LLM completions used by the Advisor.
type LLMCompleter interface {
	Complete(ctx context.Context, systemPrompt string, userPrompt string) (string, error)
}

// Advisor provides AI-powered recommendations using LLM + knowledge store context.
type Advisor struct {
	store CentralStore
	llm   LLMCompleter
}

// NewAdvisor creates an Advisor backed by the given store and LLM.
func NewAdvisor(store CentralStore, llm LLMCompleter) *Advisor {
	return &Advisor{store: store, llm: llm}
}

// RecommendRequest is the input for engine/config recommendation.
type RecommendRequest struct {
	Hardware        string `json:"hardware,omitempty"`
	HardwareProfile string `json:"hardware_profile,omitempty"`
	Model           string `json:"model"`
	Goal            string `json:"goal,omitempty"` // e.g., "low-latency", "high-throughput"
}

// RecommendResponse is the structured LLM output for recommendations.
type RecommendResponse struct {
	Engine       string            `json:"engine"`
	Config       map[string]any    `json:"config"`
	Quantization string            `json:"quantization,omitempty"`
	Reasoning    string            `json:"reasoning"`
	Confidence   string            `json:"confidence"`
	Alternatives []json.RawMessage `json:"alternatives,omitempty"`
}

// Recommend queries the knowledge store for context and asks the LLM for a recommendation.
func (a *Advisor) Recommend(ctx context.Context, req RecommendRequest) (*RecommendResponse, *Advisory, error) {
	hardware := req.Hardware
	if hardware == "" {
		hardware = req.HardwareProfile
	}

	// Gather context from store
	configs, _ := a.store.QueryConfigurations(ctx, ConfigFilter{
		Hardware: hardware,
		Model:    req.Model,
		Limit:    20,
	})
	benchmarks, _ := a.store.QueryBenchmarks(ctx, BenchmarkFilter{
		Hardware: hardware,
		Model:    req.Model,
		Limit:    20,
	})

	userPrompt := a.buildRecommendPrompt(req, configs, benchmarks)

	rawLLM, err := a.llm.Complete(ctx, promptRecommend, userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("llm complete: %w", err)
	}
	raw := stripCodeFence(rawLLM)

	var resp RecommendResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, nil, fmt.Errorf("parse llm response: %w", err)
	}

	// Store advisory
	basedOnJSON, _ := json.Marshal(map[string]any{
		"configuration_count": len(configs),
		"benchmark_count":     len(benchmarks),
	})
	adv := Advisory{
		ID:             genID("adv"),
		Type:           AdvisoryTypeConfigRecommend,
		Status:         AdvisoryStatusPending,
		Severity:       "info",
		TargetHardware: hardware,
		TargetModel:    req.Model,
		TargetEngine:   resp.Engine,
		ContentJSON:    json.RawMessage(raw),
		Reasoning:      resp.Reasoning,
		Confidence:     resp.Confidence,
		BasedOnJSON:    basedOnJSON,
		Title:          fmt.Sprintf("Recommendation: %s on %s", req.Model, hardware),
		Summary:        resp.Reasoning,
		Hardware:       hardware,
		Model:          req.Model,
		Engine:         resp.Engine,
		Details:        raw,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	_ = a.store.InsertAdvisory(ctx, adv)

	return &resp, &adv, nil
}

// OptimizeRequest is the input for optimization suggestions.
type OptimizeRequest struct {
	ConfigID string `json:"config_id"`
	Hardware string `json:"hardware"`
	Model    string `json:"model"`
	Engine   string `json:"engine"`
}

// OptimizeResponse is the structured LLM output for optimizations.
type OptimizeResponse struct {
	Optimizations []json.RawMessage `json:"optimizations"`
	Reasoning     string            `json:"reasoning"`
	Confidence    string            `json:"confidence"`
}

// OptimizeScenario suggests optimizations for an existing deployment.
func (a *Advisor) OptimizeScenario(ctx context.Context, req OptimizeRequest) (*OptimizeResponse, *Advisory, error) {
	configs, _ := a.store.QueryConfigurations(ctx, ConfigFilter{
		Hardware: req.Hardware,
		Model:    req.Model,
		Engine:   req.Engine,
		Limit:    10,
	})
	benchmarks, _ := a.store.QueryBenchmarks(ctx, BenchmarkFilter{
		Hardware: req.Hardware,
		Model:    req.Model,
		Limit:    20,
	})

	userPrompt := a.buildOptimizePrompt(req, configs, benchmarks)

	rawLLM, err := a.llm.Complete(ctx, promptOptimize, userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("llm complete: %w", err)
	}
	raw := stripCodeFence(rawLLM)

	var resp OptimizeResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, nil, fmt.Errorf("parse llm response: %w", err)
	}

	basedOnJSON, _ := json.Marshal(map[string]any{
		"config_id":           req.ConfigID,
		"configuration_count": len(configs),
		"benchmark_count":     len(benchmarks),
	})
	adv := Advisory{
		ID:             genID("adv"),
		Type:           AdvisoryTypeScenarioOptimization,
		Status:         AdvisoryStatusPending,
		Severity:       "info",
		TargetHardware: req.Hardware,
		TargetModel:    req.Model,
		TargetEngine:   req.Engine,
		ContentJSON:    json.RawMessage(raw),
		Reasoning:      resp.Reasoning,
		Confidence:     resp.Confidence,
		BasedOnJSON:    basedOnJSON,
		Title:          fmt.Sprintf("Optimization: %s/%s on %s", req.Engine, req.Model, req.Hardware),
		Summary:        resp.Reasoning,
		Hardware:       req.Hardware,
		Model:          req.Model,
		Engine:         req.Engine,
		Details:        raw,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	_ = a.store.InsertAdvisory(ctx, adv)

	return &resp, &adv, nil
}

// GenerateScenarioRequest is the input for multi-model scenario generation.
type GenerateScenarioRequest struct {
	Hardware        string   `json:"hardware,omitempty"`
	HardwareProfile string   `json:"hardware_profile,omitempty"`
	Models          []string `json:"models"`
	Goal            string   `json:"goal,omitempty"`
}

// GenerateScenarioResponse is the structured LLM output for scenario generation.
type GenerateScenarioResponse struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Deployments []json.RawMessage `json:"deployments"`
	TotalVRAM   int               `json:"total_vram_mib"`
	Reasoning   string            `json:"reasoning"`
	Confidence  string            `json:"confidence"`
}

// GenerateScenario creates a multi-model deployment plan.
func (a *Advisor) GenerateScenario(ctx context.Context, req GenerateScenarioRequest) (*GenerateScenarioResponse, *Scenario, error) {
	hardware := req.Hardware
	if hardware == "" {
		hardware = req.HardwareProfile
	}

	// Gather context for each model
	var allConfigs []Configuration
	for _, model := range req.Models {
		configs, _ := a.store.QueryConfigurations(ctx, ConfigFilter{
			Hardware: hardware,
			Model:    model,
			Limit:    5,
		})
		allConfigs = append(allConfigs, configs...)
	}

	userPrompt := a.buildScenarioPrompt(req, allConfigs)

	rawLLM, err := a.llm.Complete(ctx, promptGenerateScenario, userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("llm complete: %w", err)
	}
	raw := stripCodeFence(rawLLM)

	var resp GenerateScenarioResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, nil, fmt.Errorf("parse llm response: %w", err)
	}

	modelsJSON, _ := json.Marshal(req.Models)
	// Convert json.RawMessage deployments to []any for proper YAML serialization
	var deployments []any
	for _, d := range resp.Deployments {
		var obj any
		if err := json.Unmarshal(d, &obj); err == nil {
			deployments = append(deployments, obj)
		}
	}
	scenarioYAMLBytes, err := yaml.Marshal(map[string]any{
		"name":        resp.Name,
		"description": resp.Description,
		"goal":        req.Goal,
		"hardware":    hardware,
		"deployments": deployments,
		"total_vram":  resp.TotalVRAM,
		"reasoning":   resp.Reasoning,
		"confidence":  resp.Confidence,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal scenario yaml: %w", err)
	}
	scenario := Scenario{
		ID:              genID("scn"),
		Name:            resp.Name,
		Description:     resp.Description,
		HardwareProfile: hardware,
		ScenarioYAML:    string(scenarioYAMLBytes),
		Source:          "generated",
		Version:         1,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		Hardware:        hardware,
		Models:          string(modelsJSON),
		Config:          raw,
	}
	_ = a.store.InsertScenario(ctx, scenario)

	return &resp, &scenario, nil
}

// --- prompt builders ---

func (a *Advisor) buildRecommendPrompt(req RecommendRequest, configs []Configuration, benchmarks []BenchmarkResult) string {
	hardware := req.Hardware
	if hardware == "" {
		hardware = req.HardwareProfile
	}
	prompt := fmt.Sprintf("Hardware: %s\nModel: %s\n", hardware, req.Model)
	if req.Goal != "" {
		prompt += fmt.Sprintf("Goal: %s\n", req.Goal)
	}
	if len(configs) > 0 {
		prompt += fmt.Sprintf("\nExisting configurations (%d):\n", len(configs))
		for _, c := range configs {
			prompt += fmt.Sprintf("- Engine: %s, Status: %s, Config: %s\n", c.EngineType, c.Status, c.Config)
		}
	}
	if len(benchmarks) > 0 {
		prompt += fmt.Sprintf("\nBenchmark results (%d):\n", len(benchmarks))
		for _, b := range benchmarks {
			prompt += fmt.Sprintf("- ConfigID: %s, Throughput: %.1f tps, TTFT_p50: %.1f ms, VRAM: %d MiB\n",
				b.ConfigID, b.ThroughputTPS, b.TTFTP50ms, b.VRAMUsageMiB)
		}
	}
	return prompt
}

func (a *Advisor) buildOptimizePrompt(req OptimizeRequest, configs []Configuration, benchmarks []BenchmarkResult) string {
	prompt := fmt.Sprintf("Hardware: %s\nModel: %s\nEngine: %s\n", req.Hardware, req.Model, req.Engine)
	if req.ConfigID != "" {
		prompt += fmt.Sprintf("Current config ID: %s\n", req.ConfigID)
	}
	if len(configs) > 0 {
		prompt += fmt.Sprintf("\nExisting configurations (%d):\n", len(configs))
		for _, c := range configs {
			prompt += fmt.Sprintf("- ID: %s, Status: %s, Config: %s\n", c.ID, c.Status, c.Config)
		}
	}
	if len(benchmarks) > 0 {
		prompt += fmt.Sprintf("\nBenchmark results (%d):\n", len(benchmarks))
		for _, b := range benchmarks {
			prompt += fmt.Sprintf("- ConfigID: %s, Throughput: %.1f tps, TTFT_p50: %.1f ms, VRAM: %d MiB, GPU_Util: %.0f%%\n",
				b.ConfigID, b.ThroughputTPS, b.TTFTP50ms, b.VRAMUsageMiB, b.GPUUtilPct)
		}
	}
	return prompt
}

func (a *Advisor) buildScenarioPrompt(req GenerateScenarioRequest, configs []Configuration) string {
	hardware := req.Hardware
	if hardware == "" {
		hardware = req.HardwareProfile
	}
	modelsStr := ""
	for i, m := range req.Models {
		if i > 0 {
			modelsStr += ", "
		}
		modelsStr += m
	}
	prompt := fmt.Sprintf("Hardware: %s\nModels to deploy: %s\n", hardware, modelsStr)
	if req.Goal != "" {
		prompt += fmt.Sprintf("Goal: %s\n", req.Goal)
	}
	if len(configs) > 0 {
		prompt += fmt.Sprintf("\nKnown configurations for these models (%d):\n", len(configs))
		for _, c := range configs {
			prompt += fmt.Sprintf("- Model: %s, Engine: %s, Config: %s\n", c.Model, c.EngineType, c.Config)
		}
	}
	return prompt
}
