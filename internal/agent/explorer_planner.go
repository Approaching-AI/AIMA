package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	state "github.com/jguan/aima/internal"
)

// Planner generates exploration plans from device state.
type Planner interface {
	Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, error)
}

// PlanInput aggregates all context needed for plan generation.
type PlanInput struct {
	Hardware      HardwareInfo
	Gaps          []GapEntry
	ActiveDeploys []DeployStatus
	Advisories    []Advisory
	History       []ExplorationRun
	OpenQuestions []OpenQuestion
	LocalModels   []LocalModel  // models physically present on this device
	LocalEngines  []LocalEngine // engines installed on this device
	Event         *ExplorerEvent
}

// LocalModel describes a model installed on this device.
type LocalModel struct {
	Name      string `json:"name"`
	Format    string `json:"format"`     // "safetensors", "gguf"
	Type      string `json:"type"`       // "llm", "asr", "tts", "embedding", "reranker"
	SizeBytes int64  `json:"size_bytes"` // on-disk size (≈ VRAM needed for non-quantized)
}

// LocalEngine describes an engine installed on this device with catalog metadata.
// The TunableParams field exposes startup.default_args from the engine YAML so
// that planners (especially LLM) know exactly which knobs can be adjusted.
type LocalEngine struct {
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	Runtime       string         `json:"runtime"` // "native", "container"
	Features      []string       `json:"features,omitempty"`
	Notes         string         `json:"notes,omitempty"`         // e.g. "CPU+GPU hybrid MoE inference"
	TunableParams map[string]any `json:"tunable_params,omitempty"` // startup.default_args from engine YAML
}

type HardwareInfo struct {
	Profile  string
	GPUArch  string
	GPUCount int
	VRAMMiB  int
}

type DeployStatus struct {
	Model  string
	Engine string
	Status string
}

type GapEntry struct {
	Model          string
	Engine         string
	Hardware       string
	BenchmarkCount int
}

type Advisory struct {
	ID             string
	Type           string
	TargetHardware string
	TargetModel    string
	TargetEngine   string
	Config         map[string]any
	Confidence     string
	Reasoning      string
}

type OpenQuestion struct {
	ID       string
	Hardware string
	Model    string
	Engine   string
	Question string
	Status   string
}

// ExplorerPlan is an ordered list of exploration tasks.
// Named ExplorerPlan to avoid conflict with the existing ExplorationPlan type.
type ExplorerPlan struct {
	ID        string
	Tier      int
	Tasks     []PlanTask
	Reasoning string
}

// PlanTask is a single exploration unit.
type PlanTask struct {
	Kind      string         `json:"kind"` // "validate", "tune", "open_question"
	Hardware  string         `json:"hardware,omitempty"`
	Model     string         `json:"model"`
	Engine    string         `json:"engine"`
	SourceRef string         `json:"source_ref,omitempty"`
	Params    map[string]any `json:"params,omitempty"`
	Reason    string         `json:"reason"`
	Priority  int            `json:"priority"`
	DependsOn string         `json:"depends_on,omitempty"`
	Status    string         `json:"status,omitempty"` // "", "completed", "skipped_tier_degraded"
}

// RulePlanner generates plans using fixed priority rules (Tier 1).
type RulePlanner struct{}

func (p *RulePlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, error) {
	var tasks []PlanTask
	defaultHardware := firstTaskHardware(input.Hardware.Profile, input.Hardware.GPUArch)
	localModels := toSet(input.LocalModels)
	localEngineTypes := localEngineTypeSet(input.LocalEngines)
	modelFormats := localModelFormatMap(input.LocalModels)
	modelTypes := localModelTypeMap(input.LocalModels)
	totalVRAMMiB := input.Hardware.VRAMMiB * input.Hardware.GPUCount
	if totalVRAMMiB == 0 {
		totalVRAMMiB = input.Hardware.VRAMMiB // single GPU fallback
	}

	// Rule 1: deployed models without benchmarks -- highest priority
	for _, d := range input.ActiveDeploys {
		if d.Status != "running" {
			continue
		}
		if !hasHistoryFor(input.History, d.Model, d.Engine) {
			tasks = append(tasks, PlanTask{
				Kind:     "validate",
				Hardware: defaultHardware,
				Model:    d.Model,
				Engine:   d.Engine,
				Priority: 0,
				Reason:   "deployed without benchmark baseline",
			})
		}
	}

	// Rule 2: central advisories -- verify recommended configs
	for _, adv := range input.Advisories {
		tasks = append(tasks, PlanTask{
			Kind:      "validate",
			Hardware:  firstTaskHardware(adv.TargetHardware, defaultHardware),
			Model:     adv.TargetModel,
			Engine:    adv.TargetEngine,
			Params:    adv.Config,
			SourceRef: adv.ID,
			Priority:  1,
			Reason:    fmt.Sprintf("verify central advisory %s", adv.ID),
		})
	}

	// Rule 3: knowledge gaps -- max 3 per cycle, filtered to local hardware,
	// filtered to locally available model+engine combos + format/type/VRAM compatibility
	var localGaps []GapEntry
	for _, g := range input.Gaps {
		if g.Hardware != defaultHardware && g.Hardware != "" {
			continue
		}
		if !isLocallyAvailable(g.Model, g.Engine, localModels, localEngineTypes) {
			continue
		}
		if !engineFormatCompatible(g.Engine, modelFormats[g.Model]) {
			continue
		}
		// B24: skip non-LLM models for LLM-only engines
		if !engineSupportsModelType(g.Engine, modelTypes[g.Model]) {
			continue
		}
		// B23: skip models that obviously won't fit in total VRAM
		if !modelFitsVRAM(g.Model, input.LocalModels, totalVRAMMiB) {
			continue
		}
		localGaps = append(localGaps, g)
	}
	sort.Slice(localGaps, func(i, j int) bool { return localGaps[i].Model < localGaps[j].Model })
	for i, gap := range localGaps {
		if i >= 3 {
			break
		}
		tasks = append(tasks, PlanTask{
			Kind:     "validate",
			Hardware: firstTaskHardware(gap.Hardware, defaultHardware),
			Model:    gap.Model,
			Engine:   gap.Engine,
			Priority: 2 + i,
			Reason:   "knowledge gap (locally available)",
		})
	}

	// Rule 4: untested open questions (only if model+engine available locally)
	for _, q := range input.OpenQuestions {
		if q.Status != "untested" {
			continue
		}
		if !isLocallyAvailable(q.Model, q.Engine, localModels, localEngineTypes) {
			continue
		}
		tasks = append(tasks, PlanTask{
			Kind:      "open_question",
			Hardware:  firstTaskHardware(q.Hardware, defaultHardware),
			Model:     q.Model,
			Engine:    q.Engine,
			SourceRef: q.ID,
			Priority:  5,
			Reason:    fmt.Sprintf("open question %s", q.ID),
		})
	}

	// B11: Dedup against completed history
	tasks = deduplicateTasks(tasks, input.History)

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Priority < tasks[j].Priority })

	h := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), len(tasks))))
	id := fmt.Sprintf("%x", h)[:8]
	return &ExplorerPlan{
		ID:        id,
		Tier:      1,
		Tasks:     tasks,
		Reasoning: "rule-based",
	}, nil
}

func hasHistoryFor(history []ExplorationRun, model, engine string) bool {
	for _, h := range history {
		if h.ModelID == model && h.EngineID == engine && h.Status == "completed" {
			return true
		}
	}
	return false
}

// deduplicateTasks removes tasks whose model+engine already has a completed
// exploration run in history, OR has failed too many times (B11 + B22 fix).
func deduplicateTasks(tasks []PlanTask, history []ExplorationRun) []PlanTask {
	if len(history) == 0 {
		return tasks
	}
	filtered := tasks[:0]
	for _, t := range tasks {
		// Advisories (with SourceRef) always pass — central asked us to re-validate.
		if t.SourceRef != "" {
			filtered = append(filtered, t)
			continue
		}
		if hasHistoryFor(history, t.Model, t.Engine) {
			continue // already completed successfully
		}
		// B22: stop retrying after repeated failures (≥2 failed, 0 completed)
		if failCountFor(history, t.Model, t.Engine) >= 2 {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// failCountFor counts how many times a model+engine has failed in history.
func failCountFor(history []ExplorationRun, model, engine string) int {
	n := 0
	for _, h := range history {
		if h.ModelID == model && h.EngineID == engine && h.Status == "failed" {
			n++
		}
	}
	return n
}

func firstTaskHardware(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func toSet(items []LocalModel) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item.Name] = true
	}
	return s
}

// localModelFormatMap builds a name→format map for format compatibility checks.
func localModelFormatMap(models []LocalModel) map[string]string {
	m := make(map[string]string, len(models))
	for _, model := range models {
		m[model.Name] = model.Format
	}
	return m
}

// engineFormatCompatible returns true if a model's format is compatible with an engine type.
// llamacpp needs GGUF; vllm/sglang/sglang-kt need safetensors.
func engineFormatCompatible(engineType, modelFormat string) bool {
	if modelFormat == "" || engineType == "" {
		return true // unknown format — allow (best-effort)
	}
	switch engineType {
	case "llamacpp":
		return modelFormat == "gguf"
	case "vllm", "sglang", "sglang-kt":
		return modelFormat == "safetensors"
	default:
		return true // unknown engine — allow
	}
}

// modelFitsVRAM checks if a model can plausibly fit in total available VRAM.
// Uses model on-disk size as approximation of VRAM needed (conservative for quantized).
// B23: skip obviously impossible models (e.g., 360GB DeepSeek on 96GB VRAM).
func modelFitsVRAM(modelName string, models []LocalModel, totalVRAMMiB int) bool {
	if totalVRAMMiB <= 0 {
		return true // unknown VRAM — allow (best-effort)
	}
	for _, m := range models {
		if m.Name == modelName && m.SizeBytes > 0 {
			modelMiB := int(m.SizeBytes / (1024 * 1024))
			// Model weights + ~25% overhead for KV cache and activations
			needed := modelMiB + modelMiB/4
			return needed <= totalVRAMMiB
		}
	}
	return true // model not found in local list — allow
}

// engineSupportsModelType checks if an engine type can serve a model type.
// B24: sglang-kt/vllm/sglang only serve LLM; other types need specialized engines.
func engineSupportsModelType(engineType, modelType string) bool {
	if modelType == "" || modelType == "llm" {
		return true // unknown or LLM — always allowed
	}
	switch engineType {
	case "vllm", "sglang", "sglang-kt":
		return false // these engines only serve text LLMs
	default:
		return true // specialized engines decide themselves
	}
}

// localModelTypeMap builds a name→type map for type compatibility checks.
func localModelTypeMap(models []LocalModel) map[string]string {
	m := make(map[string]string, len(models))
	for _, model := range models {
		m[model.Name] = model.Type
	}
	return m
}

func localEngineTypeSet(engines []LocalEngine) map[string]bool {
	s := make(map[string]bool, len(engines))
	for _, e := range engines {
		s[e.Type] = true
		s[e.Name] = true
	}
	return s
}

// isLocallyAvailable checks if the model and engine are present on this device.
// Empty local sets mean "no constraint" (backwards-compatible for tests).
func isLocallyAvailable(model, engine string, localModels, localEngines map[string]bool) bool {
	if len(localModels) > 0 && !localModels[model] {
		return false
	}
	if len(localEngines) > 0 && engine != "" && !localEngines[engine] {
		return false
	}
	return true
}

// ExplorationRun is re-exported from state for plan input convenience.
type ExplorationRun = state.ExplorationRun
