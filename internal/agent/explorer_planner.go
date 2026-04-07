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
	Event         *ExplorerEvent
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
	// sorted by model name (stable)
	var localGaps []GapEntry
	for _, g := range input.Gaps {
		if g.Hardware == defaultHardware || g.Hardware == "" {
			localGaps = append(localGaps, g)
		}
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
			Reason:   "knowledge gap",
		})
	}

	// Rule 4: untested open questions
	for _, q := range input.OpenQuestions {
		if q.Status != "untested" {
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

func firstTaskHardware(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ExplorationRun is re-exported from state for plan input convenience.
type ExplorationRun = state.ExplorationRun
