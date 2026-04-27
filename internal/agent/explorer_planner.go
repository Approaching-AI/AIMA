package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	state "github.com/jguan/aima/internal"
)

// Planner generates exploration plans from device state.
type Planner interface {
	Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error)
}

// AnalyzablePlanner extends Planner with result analysis capability (PDCA Check+Act).
type AnalyzablePlanner interface {
	Planner
	Analyze(ctx context.Context) (verdict string, extraTasks []TaskSpec, tokens int, err error)
}

// FactRefreshablePlanner refreshes planner-visible fact documents from the
// latest device state before follow-up PDCA analysis.
type FactRefreshablePlanner interface {
	RefreshFacts(input PlanInput) error
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
	ComboFacts    []ComboFact   // authoritative model×engine execution facts for this run
	PendingWork   []PendingWork // derived durable obligations for ready combos
	Event         *ExplorerEvent
	SkipCombos    []SkipCombo // model+engine pairs already explored (prefill dedup for LLM)
}

// SkipCombo is a model+engine pair the LLM planner should not propose.
// When Engine is empty, the deny applies to the whole model.
type SkipCombo struct {
	Model  string `json:"model"`
	Engine string `json:"engine"`
	Reason string `json:"reason"` // "completed" or "failed:N"
}

// LocalModel describes a model installed on this device.
type LocalModel struct {
	Name           string `json:"name"`
	Format         string `json:"format"`                    // "safetensors", "gguf"
	Type           string `json:"type"`                      // "llm", "asr", "tts", "embedding", "reranker"
	SizeBytes      int64  `json:"size_bytes"`                // on-disk size (≈ VRAM needed for non-quantized)
	MaxContextLen  int    `json:"max_context_len,omitempty"` // model's max context window from catalog variant (0 = unknown)
	Family         string `json:"family,omitempty"`          // from catalog metadata.family (e.g. "qwen", "llama")
	ParameterCount string `json:"parameter_count,omitempty"` // from catalog metadata.parameter_count (e.g. "8B")
}

// LocalEngine describes an engine installed on this device with catalog metadata.
// The TunableParams field exposes startup.default_args from the engine YAML so
// that planners (especially LLM) know exactly which knobs can be adjusted.
type LocalEngine struct {
	Name                string         `json:"name"`
	Type                string         `json:"type"`
	Runtime             string         `json:"runtime"` // "native", "container"
	Artifact            string         `json:"artifact,omitempty"`
	Features            []string       `json:"features,omitempty"`
	Notes               string         `json:"notes,omitempty"`                 // e.g. "CPU+GPU hybrid MoE inference"
	TunableParams       map[string]any `json:"tunable_params,omitempty"`        // startup.default_args from engine YAML
	InternalArgs        []string       `json:"internal_args,omitempty"`         // startup.internal_args from engine YAML
	SupportedFormats    []string       `json:"supported_formats,omitempty"`     // model storage formats supported by engine YAML
	SupportedModelTypes []string       `json:"supported_model_types,omitempty"` // e.g. ["llm","embedding"] — empty = all
	HealthCheckPath     string         `json:"health_check_path,omitempty"`     // startup.health_check.path from engine YAML
}

// ComboFact is an authoritative execution fact for one local model×engine pair.
// Explorer should only schedule new work from facts marked ready.
type ComboFact struct {
	Model    string `json:"model"`
	Engine   string `json:"engine"`
	Runtime  string `json:"runtime,omitempty"`
	Artifact string `json:"artifact,omitempty"`
	Status   string `json:"status"` // "ready" | "blocked"
	Reason   string `json:"reason,omitempty"`
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

// PendingWork is a durable, executable obligation still open for a ready combo.
// It is derived from local configurations, benchmark_results, and exploration_runs.
type PendingWork struct {
	Model       string           `json:"model"`
	Engine      string           `json:"engine"`
	Kind        string           `json:"kind"` // "validate_baseline" | "validate_long_context" | "tune"
	Reason      string           `json:"reason"`
	Benchmark   BenchmarkSpec    `json:"benchmark,omitempty"`
	SearchSpace map[string][]any `json:"search_space,omitempty"`
	Priority    int              `json:"priority"`
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
	Kind        string           `json:"kind"` // "validate", "tune", "open_question"
	Hardware    string           `json:"hardware,omitempty"`
	Model       string           `json:"model"`
	Engine      string           `json:"engine"`
	SourceRef   string           `json:"source_ref,omitempty"`
	Params      map[string]any   `json:"params,omitempty"`
	SearchSpace map[string][]any `json:"search_space,omitempty"`
	Benchmark   BenchmarkSpec    `json:"benchmark,omitempty"`
	Reason      string           `json:"reason"`
	Priority    int              `json:"priority"`
	DependsOn   string           `json:"depends_on,omitempty"`
	Status      string           `json:"status,omitempty"` // "", "completed", "failed", "skipped", "skipped_tier_degraded"
}

// TaskSpec is an LLM-authored exploration task parsed from plan.md YAML.
// The LLM fills in all structured fields; Go transparently executes.
type TaskSpec struct {
	Kind         string           `yaml:"kind" json:"kind"` // "validate" | "tune"
	Model        string           `yaml:"model" json:"model"`
	Engine       string           `yaml:"engine" json:"engine"`
	EngineParams map[string]any   `yaml:"engine_params" json:"engine_params,omitempty"`
	SearchSpace  map[string][]any `yaml:"search_space" json:"search_space,omitempty"`
	Benchmark    BenchmarkSpec    `yaml:"benchmark" json:"benchmark"`
	Reason       string           `yaml:"reason" json:"reason"`
}

// BenchmarkSpec defines the benchmark matrix for one task.
type BenchmarkSpec struct {
	Concurrency      []int `yaml:"concurrency" json:"concurrency"`
	InputTokens      []int `yaml:"input_tokens" json:"input_tokens"`
	MaxTokens        []int `yaml:"max_tokens" json:"max_tokens"`
	RequestsPerCombo int   `yaml:"requests_per_combo" json:"requests_per_combo"`
}

// RecommendedConfig is an LLM-authored golden configuration from summary.md YAML.
type RecommendedConfig struct {
	Model        string         `yaml:"model" json:"model"`
	Engine       string         `yaml:"engine" json:"engine"`
	Hardware     string         `yaml:"hardware" json:"hardware"`
	EngineParams map[string]any `yaml:"engine_params" json:"engine_params,omitempty"`
	Performance  PerfSummary    `yaml:"performance" json:"performance"`
	Confidence   string         `yaml:"confidence" json:"confidence"` // "validated" | "tuned" | "provisional"
	Note         string         `yaml:"note" json:"note,omitempty"`
}

// PerfSummary captures key performance metrics with scenario context.
type PerfSummary struct {
	ThroughputTPS      float64 `yaml:"throughput_tps" json:"throughput_tps"`
	ThroughputScenario string  `yaml:"throughput_scenario,omitempty" json:"throughput_scenario,omitempty"`
	LatencyP50Ms       float64 `yaml:"latency_p50_ms" json:"latency_p50_ms"`
	LatencyScenario    string  `yaml:"latency_scenario,omitempty" json:"latency_scenario,omitempty"`
}

// RulePlanner generates plans using fixed priority rules (Tier 1).
type RulePlanner struct{}

func (p *RulePlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	var tasks []PlanTask
	seenCombos := make(map[string]struct{})
	appendTask := func(task PlanTask) {
		key := planTaskComboKey(task.Model, task.Engine)
		if key != "" {
			if _, exists := seenCombos[key]; exists {
				return
			}
			seenCombos[key] = struct{}{}
		}
		tasks = append(tasks, task)
	}
	defaultHardware := firstTaskHardware(input.Hardware.Profile, input.Hardware.GPUArch)
	localModels := toSet(input.LocalModels)
	localEngineTypes := localEngineTypeSet(input.LocalEngines)
	modelFormats := localModelFormatMap(input.LocalModels)
	modelTypes := localModelTypeMap(input.LocalModels)
	engineFormats := localEngineSupportedFormats(input.LocalEngines)
	engineModelTypes := localEngineSupportedModelTypes(input.LocalEngines)
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
			appendTask(PlanTask{
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
		appendTask(PlanTask{
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

	// Rule 3: pending work on already-ready combos. This keeps a combo on the
	// frontier until its durable obligations are actually closed, instead of
	// treating the first completed run as globally "done".
	pendingWork := append([]PendingWork(nil), input.PendingWork...)
	rand.Shuffle(len(pendingWork), func(i, j int) {
		pendingWork[i], pendingWork[j] = pendingWork[j], pendingWork[i]
	})
	sort.SliceStable(pendingWork, func(i, j int) bool {
		if pendingWork[i].Priority != pendingWork[j].Priority {
			return pendingWork[i].Priority < pendingWork[j].Priority
		}
		if pendingWork[i].Kind != pendingWork[j].Kind {
			return pendingWork[i].Kind < pendingWork[j].Kind
		}
		if pendingWork[i].Model != pendingWork[j].Model {
			return pendingWork[i].Model < pendingWork[j].Model
		}
		return pendingWork[i].Engine < pendingWork[j].Engine
	})
	for i, work := range pendingWork {
		if i >= 3 {
			break
		}
		taskKind := "validate"
		if work.Kind == "tune" {
			taskKind = "tune"
		}
		appendTask(PlanTask{
			Kind:        taskKind,
			Hardware:    defaultHardware,
			Model:       work.Model,
			Engine:      work.Engine,
			SearchSpace: cloneSearchSpace(work.SearchSpace),
			Benchmark:   work.Benchmark,
			Priority:    2 + i,
			Reason:      work.Reason,
		})
	}

	// Rule 4: knowledge gaps -- max 3 per cycle, filtered to local hardware,
	// filtered to locally available model+engine combos + format/type/VRAM compatibility
	var localGaps []GapEntry
	for _, g := range input.Gaps {
		if g.Hardware != defaultHardware && g.Hardware != "" {
			continue
		}
		if !isLocallyAvailable(g.Model, g.Engine, localModels, localEngineTypes) {
			continue
		}
		if !engineFormatCompatible(engineFormats[strings.ToLower(g.Engine)], modelFormats[g.Model]) {
			continue
		}
		// B24: skip models whose type is not supported by the engine
		if !engineSupportsModelTypeFromList(engineModelTypes[strings.ToLower(g.Engine)], modelTypes[strings.ToLower(g.Model)]) {
			continue
		}
		// B23: skip models that obviously won't fit in total VRAM
		if !modelFitsVRAM(g.Model, input.LocalModels, totalVRAMMiB) {
			continue
		}
		localGaps = append(localGaps, g)
	}
	rand.Shuffle(len(localGaps), func(i, j int) {
		localGaps[i], localGaps[j] = localGaps[j], localGaps[i]
	})
	sort.SliceStable(localGaps, func(i, j int) bool {
		return localGaps[i].BenchmarkCount < localGaps[j].BenchmarkCount
	})
	for i, gap := range localGaps {
		if i >= 3 {
			break
		}
		appendTask(PlanTask{
			Kind:     "validate",
			Hardware: firstTaskHardware(gap.Hardware, defaultHardware),
			Model:    gap.Model,
			Engine:   gap.Engine,
			Priority: 2 + i,
			// Pending work gets first claim on the round; gaps come after it.
			Reason: "knowledge gap (locally available)",
		})
	}

	// Rule 5: untested open questions (only if model+engine available locally)
	for _, q := range input.OpenQuestions {
		if q.Status != "untested" {
			continue
		}
		if !isLocallyAvailable(q.Model, q.Engine, localModels, localEngineTypes) {
			continue
		}
		appendTask(PlanTask{
			Kind:      "open_question",
			Hardware:  firstTaskHardware(q.Hardware, defaultHardware),
			Model:     q.Model,
			Engine:    q.Engine,
			SourceRef: q.ID,
			Priority:  5,
			Reason:    fmt.Sprintf("open question %s", q.ID),
		})
	}

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Priority < tasks[j].Priority })

	h := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), len(tasks))))
	id := fmt.Sprintf("%x", h)[:8]
	return &ExplorerPlan{
		ID:        id,
		Tier:      1,
		Tasks:     tasks,
		Reasoning: "rule-based",
	}, 0, nil
}

func hasHistoryFor(history []ExplorationRun, model, engine string) bool {
	for _, h := range history {
		if strings.EqualFold(h.ModelID, model) && strings.EqualFold(h.EngineID, engine) && h.Status == "completed" {
			return true
		}
	}
	return false
}

func firstTaskHardware(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneSearchSpace(space map[string][]any) map[string][]any {
	if len(space) == 0 {
		return nil
	}
	cloned := make(map[string][]any, len(space))
	for key, values := range space {
		if len(values) == 0 {
			continue
		}
		cp := make([]any, len(values))
		copy(cp, values)
		cloned[key] = cp
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func toSet(items []LocalModel) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[strings.ToLower(item.Name)] = true
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

// engineFormatCompatible checks the model format against engine YAML metadata.
// Empty fields are treated as unknown and allowed so older catalog entries stay compatible.
func engineFormatCompatible(supportedFormats []string, modelFormat string) bool {
	if modelFormat == "" || len(supportedFormats) == 0 {
		return true
	}
	for _, supported := range supportedFormats {
		if strings.EqualFold(supported, modelFormat) {
			return true
		}
	}
	return false
}

// modelFitsVRAM checks if a model can plausibly fit in total available VRAM.
// Uses model on-disk size as approximation of VRAM needed (conservative for quantized).
// B23: skip obviously impossible models (e.g., 360GB DeepSeek on 96GB VRAM).
func modelFitsVRAM(modelName string, models []LocalModel, totalVRAMMiB int) bool {
	if totalVRAMMiB <= 0 {
		return true // unknown VRAM — allow (best-effort)
	}
	for _, m := range models {
		if strings.EqualFold(m.Name, modelName) && m.SizeBytes > 0 {
			modelMiB := int(m.SizeBytes / (1024 * 1024))
			// Model weights + ~25% overhead for KV cache and activations
			needed := modelMiB + modelMiB/4
			return needed <= totalVRAMMiB
		}
	}
	return true // model not found in local list — allow
}

// engineSupportsModelTypeFromList checks if the engine's declared supported model
// types include the given model type. Returns true when the engine has no
// declared types (backwards-compatible permissive matching).
func engineSupportsModelTypeFromList(supportedTypes []string, modelType string) bool {
	if len(supportedTypes) == 0 {
		return true // engine doesn't declare types — allow all
	}
	if modelType == "" {
		return true // unknown model type — allow
	}
	for _, t := range supportedTypes {
		if strings.EqualFold(t, modelType) {
			return true
		}
	}
	return false
}

// localModelTypeMap builds a name→type map for type compatibility checks.
func localModelTypeMap(models []LocalModel) map[string]string {
	m := make(map[string]string, len(models))
	for _, model := range models {
		m[strings.ToLower(model.Name)] = model.Type
	}
	return m
}

func localEngineTypeSet(engines []LocalEngine) map[string]bool {
	s := make(map[string]bool, len(engines))
	for _, e := range engines {
		s[strings.ToLower(e.Type)] = true
		s[strings.ToLower(e.Name)] = true
	}
	return s
}

func localEngineSupportedModelTypes(engines []LocalEngine) map[string][]string {
	m := make(map[string][]string, len(engines))
	for _, e := range engines {
		m[strings.ToLower(e.Type)] = e.SupportedModelTypes
		m[strings.ToLower(e.Name)] = e.SupportedModelTypes
	}
	return m
}

func localEngineSupportedFormats(engines []LocalEngine) map[string][]string {
	m := make(map[string][]string, len(engines))
	for _, e := range engines {
		m[strings.ToLower(e.Type)] = e.SupportedFormats
		m[strings.ToLower(e.Name)] = e.SupportedFormats
	}
	return m
}

// isLocallyAvailable checks if the model and engine are present on this device.
// Empty local sets mean "no constraint" (backwards-compatible for tests).
func isLocallyAvailable(model, engine string, localModels, localEngines map[string]bool) bool {
	if len(localModels) > 0 && !localModels[strings.ToLower(model)] {
		return false
	}
	if len(localEngines) > 0 && engine != "" && !localEngines[strings.ToLower(engine)] {
		return false
	}
	return true
}

func planTaskComboKey(model, engine string) string {
	model = strings.TrimSpace(model)
	engine = strings.TrimSpace(engine)
	if model == "" && engine == "" {
		return ""
	}
	return model + "|" + engine
}

// ExplorationRun is re-exported from state for plan input convenience.
type ExplorationRun = state.ExplorationRun
