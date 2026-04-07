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
	ID           string
	Type         string
	TargetModel  string
	TargetEngine string
	Config       map[string]any
	Confidence   string
	Reasoning    string
}

type OpenQuestion struct {
	ID       string
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
	Kind      string         `json:"kind"`      // "validate", "tune", "open_question", "compare"
	Model     string         `json:"model"`
	Engine    string         `json:"engine"`
	Params    map[string]any `json:"params,omitempty"`
	Reason    string         `json:"reason"`
	Priority  int            `json:"priority"`
	DependsOn string         `json:"depends_on,omitempty"`
}

// RulePlanner generates plans using fixed priority rules (Tier 1).
type RulePlanner struct{}

func (p *RulePlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, error) {
	var tasks []PlanTask

	// Rule 1: deployed models without benchmarks -- highest priority
	for _, d := range input.ActiveDeploys {
		if d.Status != "running" {
			continue
		}
		if !hasHistoryFor(input.History, d.Model, d.Engine) {
			tasks = append(tasks, PlanTask{
				Kind:     "validate",
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
			Kind:     "validate",
			Model:    adv.TargetModel,
			Engine:   adv.TargetEngine,
			Params:   adv.Config,
			Priority: 1,
			Reason:   fmt.Sprintf("verify central advisory %s", adv.ID),
		})
	}

	// Rule 3: knowledge gaps -- max 3 per cycle, sorted by model name (stable)
	sorted := make([]GapEntry, len(input.Gaps))
	copy(sorted, input.Gaps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Model < sorted[j].Model })
	for i, gap := range sorted {
		if i >= 3 {
			break
		}
		tasks = append(tasks, PlanTask{
			Kind:     "validate",
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
			Kind:     "open_question",
			Model:    q.Model,
			Engine:   q.Engine,
			Priority: 5,
			Reason:   fmt.Sprintf("open question %s", q.ID),
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

// ExplorationRun is re-exported from state for plan input convenience.
type ExplorationRun = state.ExplorationRun
