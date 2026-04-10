package central

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// CentralStore is the persistence interface for the central knowledge server.
// It abstracts over SQLite (edge/single-node) and PostgreSQL (cloud/multi-node).
type CentralStore interface {
	// Migrate creates or updates the database schema.
	Migrate(ctx context.Context) error

	// Close releases database resources.
	Close() error

	// --- Devices ---
	UpsertDevice(ctx context.Context, d Device) error
	ListDevices(ctx context.Context) ([]Device, error)

	// --- Configurations ---
	InsertConfiguration(ctx context.Context, c Configuration) error
	ConfigExistsByHash(ctx context.Context, hash string) (bool, error)
	QueryConfigurations(ctx context.Context, f ConfigFilter) ([]Configuration, error)
	ListConfigurationsForSync(ctx context.Context, f SyncFilter) ([]Configuration, error)

	// --- Benchmarks ---
	InsertBenchmark(ctx context.Context, b BenchmarkResult) error
	ListBenchmarksForSync(ctx context.Context, configIDs []string, since string) ([]BenchmarkResult, error)
	QueryBenchmarks(ctx context.Context, f BenchmarkFilter) ([]BenchmarkResult, error)

	// --- Knowledge Notes ---
	UpsertKnowledgeNote(ctx context.Context, n KnowledgeNote) error
	ListKnowledgeNotes(ctx context.Context) ([]KnowledgeNote, error)

	// --- Advisories ---
	InsertAdvisory(ctx context.Context, a Advisory) error
	ListAdvisories(ctx context.Context, f AdvisoryFilter) ([]Advisory, error)
	UpdateAdvisoryStatus(ctx context.Context, id string, update AdvisoryStatusUpdate) error
	ExpireAdvisories(ctx context.Context, before time.Time) (int, error)

	// --- Analysis Runs ---
	InsertAnalysisRun(ctx context.Context, r AnalysisRun) error
	UpdateAnalysisRun(ctx context.Context, id string, update AnalysisRunUpdate) error
	ListAnalysisRuns(ctx context.Context, limit int) ([]AnalysisRun, error)

	// --- Scenarios ---
	InsertScenario(ctx context.Context, s Scenario) error
	ListScenarios(ctx context.Context, f ScenarioFilter) ([]Scenario, error)

	// --- Stats ---
	Stats(ctx context.Context) (StoreStats, error)
	CoverageMatrix(ctx context.Context) ([]CoverageEntry, error)
}

// --- Domain types ---

type Device struct {
	ID              string `json:"id"`
	HardwareProfile string `json:"hardware_profile"`
	GPUArch         string `json:"gpu_arch"`
	LastSeen        string `json:"last_seen"`
}

type Configuration struct {
	ID            string `json:"id"`
	DeviceID      string `json:"device_id,omitempty"`
	Hardware      string `json:"hardware"`
	EngineType    string `json:"engine_type"`
	EngineVersion string `json:"engine_version,omitempty"`
	Model         string `json:"model"`
	Slot          string `json:"slot,omitempty"`
	Config        string `json:"config"`
	ConfigHash    string `json:"config_hash"`
	Status        string `json:"status"`
	DerivedFrom   string `json:"derived_from,omitempty"`
	Tags          string `json:"tags,omitempty"`
	Source        string `json:"source,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type BenchmarkResult struct {
	ID              string  `json:"id"`
	ConfigID        string  `json:"config_id"`
	DeviceID        string  `json:"device_id,omitempty"`
	Concurrency     int     `json:"concurrency"`
	InputLenBucket  string  `json:"input_len_bucket,omitempty"`
	OutputLenBucket string  `json:"output_len_bucket,omitempty"`
	Modality        string  `json:"modality,omitempty"`
	ThroughputTPS   float64 `json:"throughput_tps"`
	TTFTP50ms       float64 `json:"ttft_p50_ms"`
	TTFTP95ms       float64 `json:"ttft_p95_ms"`
	TTFTP99ms       float64 `json:"ttft_p99_ms"`
	TPOTP50ms       float64 `json:"tpot_p50_ms"`
	TPOTP95ms       float64 `json:"tpot_p95_ms"`
	QPS             float64 `json:"qps"`
	VRAMUsageMiB    int     `json:"vram_usage_mib"`
	RAMUsageMiB     int     `json:"ram_usage_mib"`
	PowerDrawWatts  float64 `json:"power_draw_watts"`
	GPUUtilPct      float64 `json:"gpu_utilization_pct"`
	CPUUsagePct     float64 `json:"cpu_usage_pct"`
	ErrorRate       float64 `json:"error_rate"`
	OOMOccurred     bool    `json:"oom_occurred"`
	Stability       string  `json:"stability,omitempty"`
	DurationS       int     `json:"duration_s"`
	SampleCount     int     `json:"sample_count"`
	AgentModel      string  `json:"agent_model,omitempty"`
	Notes           string  `json:"notes,omitempty"`
	TestedAt        string  `json:"tested_at"`
}

type KnowledgeNote struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Tags            string `json:"tags,omitempty"`
	HardwareProfile string `json:"hardware_profile,omitempty"`
	Model           string `json:"model,omitempty"`
	Engine          string `json:"engine,omitempty"`
	Content         string `json:"content"`
	Confidence      string `json:"confidence,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type Advisory struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	Severity       string          `json:"severity,omitempty"`
	TargetHardware string          `json:"target_hardware,omitempty"`
	TargetModel    string          `json:"target_model,omitempty"`
	TargetEngine   string          `json:"target_engine,omitempty"`
	ContentJSON    json.RawMessage `json:"content_json,omitempty"`
	Reasoning      string          `json:"reasoning,omitempty"`
	Confidence     string          `json:"confidence,omitempty"`
	BasedOnJSON    json.RawMessage `json:"based_on_json,omitempty"`
	AnalysisID     string          `json:"analysis_id,omitempty"`
	CreatedAt      string          `json:"created_at"`
	DeliveredAt    string          `json:"delivered_at,omitempty"`
	ValidatedAt    string          `json:"validated_at,omitempty"`

	// Compatibility fields kept for in-flight edge clients.
	Title    string `json:"title,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Hardware string `json:"hardware,omitempty"`
	Model    string `json:"model,omitempty"`
	Engine   string `json:"engine,omitempty"`
	Details  string `json:"details,omitempty"`
	Feedback string `json:"feedback,omitempty"`
	Accepted bool   `json:"accepted,omitempty"`
}

type AnalysisRun struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Status        string          `json:"status"`
	Summary       string          `json:"summary,omitempty"`
	InputJSON     json.RawMessage `json:"input_json,omitempty"`
	OutputJSON    json.RawMessage `json:"output_json,omitempty"`
	Advisories    json.RawMessage `json:"advisories,omitempty"`
	AdvisoryCount int             `json:"advisory_count,omitempty"`
	DurationMs    int             `json:"duration_ms,omitempty"`
	Error         string          `json:"error,omitempty"`
	StartedAt     string          `json:"started_at,omitempty"`
	CompletedAt   string          `json:"completed_at,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
}

type Scenario struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	HardwareProfile string `json:"hardware_profile"`
	ScenarioYAML    string `json:"scenario_yaml"`
	Source          string `json:"source,omitempty"`
	AdvisoryID      string `json:"advisory_id,omitempty"`
	Version         int    `json:"version"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`

	// Compatibility fields kept for in-flight edge clients.
	Hardware string `json:"hardware,omitempty"`
	Models   string `json:"models,omitempty"`
	Config   string `json:"config,omitempty"`
}

// --- Filter types ---

type ConfigFilter struct {
	Hardware string
	Engine   string
	Model    string
	Status   string
	Limit    int
}

type SyncFilter struct {
	Since    string
	Hardware string
	Limit    int
}

type BenchmarkFilter struct {
	ConfigID string
	Hardware string
	Model    string
	Limit    int
}

type AdvisoryFilter struct {
	ID       string
	Type     string
	Status   string
	Severity string
	Hardware string
	Model    string
	Engine   string
	Limit    int
}

type ScenarioFilter struct {
	Name       string
	Hardware   string
	Source     string
	AdvisoryID string
	Limit      int
}

type AdvisoryStatusUpdate struct {
	Status      string
	Feedback    string
	DeliveredAt string
	ValidatedAt string
}

type AnalysisRunUpdate struct {
	Status        string
	Summary       string
	InputJSON     json.RawMessage
	OutputJSON    json.RawMessage
	Advisories    json.RawMessage
	AdvisoryCount int
	DurationMs    int
	Error         string
	StartedAt     string
	CompletedAt   string
}

// --- Stats types ---

type StoreStats struct {
	Devices        int `json:"devices"`
	Configurations int `json:"configurations"`
	Benchmarks     int `json:"benchmarks"`
	KnowledgeNotes int `json:"knowledge_notes"`
	Advisories     int `json:"advisories"`
	Scenarios      int `json:"scenarios"`
}

type CoverageEntry struct {
	Hardware string `json:"hardware"`
	Engine   string `json:"engine"`
	Models   int    `json:"models"`
}

const (
	AdvisoryTypeConfigRecommend      = "config_recommend"
	AdvisoryTypeScenarioOptimization = "scenario_optimization"
	AdvisoryTypeScenarioGeneration   = "scenario_generation"
	AdvisoryTypeGapAlert             = "gap_alert"

	AdvisoryStatusPending   = "pending"
	AdvisoryStatusDelivered = "delivered"
	AdvisoryStatusValidated = "validated"
	AdvisoryStatusRejected  = "rejected"
	AdvisoryStatusExpired   = "expired"

	AnalysisTypeGapScan          = "gap_scan"
	AnalysisTypePatternDiscovery = "pattern_discovery"
	AnalysisTypeScenarioHealth   = "scenario_health"

	AnalysisStatusRunning   = "running"
	AnalysisStatusCompleted = "completed"
	AnalysisStatusFailed    = "failed"
)

// genID generates a short ID with a prefix, using SHA-256 of prefix + timestamp + random nonce.
func genID(prefix string) string {
	var nonce [4]byte
	rand.Read(nonce[:])
	data := fmt.Sprintf("%s-%d-%x", prefix, time.Now().UnixNano(), nonce)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%s_%x", prefix, h[:8])
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func normalizeAdvisory(a Advisory) Advisory {
	if a.TargetHardware == "" {
		a.TargetHardware = a.Hardware
	}
	if a.Hardware == "" {
		a.Hardware = a.TargetHardware
	}
	if a.TargetModel == "" {
		a.TargetModel = a.Model
	}
	if a.Model == "" {
		a.Model = a.TargetModel
	}
	if a.TargetEngine == "" {
		a.TargetEngine = a.Engine
	}
	if a.Engine == "" {
		a.Engine = a.TargetEngine
	}
	if len(a.ContentJSON) == 0 && a.Details != "" {
		a.ContentJSON = json.RawMessage(a.Details)
	}
	a.ContentJSON = safeRawJSON(a.ContentJSON, a.Details, `{}`)
	if a.Details == "" && len(a.ContentJSON) > 0 {
		a.Details = string(a.ContentJSON)
	}
	if len(a.BasedOnJSON) == 0 {
		a.BasedOnJSON = json.RawMessage(`[]`)
	}
	a.BasedOnJSON = safeRawJSON(a.BasedOnJSON, "", `[]`)
	if a.Reasoning == "" {
		a.Reasoning = a.Summary
	}
	if a.Summary == "" {
		a.Summary = a.Reasoning
	}
	if a.Status == "" {
		switch {
		case a.ValidatedAt != "":
			if a.Accepted {
				a.Status = AdvisoryStatusValidated
			} else {
				a.Status = AdvisoryStatusRejected
			}
		case a.DeliveredAt != "":
			a.Status = AdvisoryStatusDelivered
		case a.Feedback != "":
			if a.Accepted {
				a.Status = AdvisoryStatusValidated
			} else {
				a.Status = AdvisoryStatusRejected
			}
		default:
			a.Status = AdvisoryStatusPending
		}
	}
	a.Accepted = a.Status == AdvisoryStatusValidated
	if a.Confidence == "" {
		a.Confidence = "medium"
	}
	if a.CreatedAt == "" {
		a.CreatedAt = nowRFC3339()
	}
	return a
}

func advisoryTransitionAllowed(current, next string) bool {
	if current == "" {
		current = AdvisoryStatusPending
	}
	if next == "" || current == next {
		return true
	}
	switch current {
	case AdvisoryStatusPending:
		return next == AdvisoryStatusDelivered || next == AdvisoryStatusValidated || next == AdvisoryStatusRejected || next == AdvisoryStatusExpired
	case AdvisoryStatusDelivered:
		return next == AdvisoryStatusValidated || next == AdvisoryStatusRejected || next == AdvisoryStatusExpired
	case AdvisoryStatusValidated, AdvisoryStatusRejected, AdvisoryStatusExpired:
		return false
	default:
		return false
	}
}

func normalizeScenario(s Scenario) Scenario {
	if s.HardwareProfile == "" {
		s.HardwareProfile = s.Hardware
	}
	if s.Hardware == "" {
		s.Hardware = s.HardwareProfile
	}
	if s.ScenarioYAML == "" {
		s.ScenarioYAML = s.Config
	}
	if s.Config == "" {
		s.Config = s.ScenarioYAML
	}
	if s.Version <= 0 {
		s.Version = 1
	}
	if s.CreatedAt == "" {
		s.CreatedAt = nowRFC3339()
	}
	if s.UpdatedAt == "" {
		s.UpdatedAt = s.CreatedAt
	}
	return s
}

func normalizeAnalysisRun(r AnalysisRun) AnalysisRun {
	r.InputJSON = safeRawJSON(r.InputJSON, "", `{}`)
	r.OutputJSON = safeRawJSON(r.OutputJSON, "", `{}`)
	r.Advisories = safeRawJSON(r.Advisories, "", `[]`)
	if r.StartedAt == "" {
		if r.CreatedAt != "" {
			r.StartedAt = r.CreatedAt
		} else {
			r.StartedAt = nowRFC3339()
		}
	}
	if r.CreatedAt == "" {
		r.CreatedAt = r.StartedAt
	}
	if r.UpdatedAt == "" {
		if r.CompletedAt != "" {
			r.UpdatedAt = r.CompletedAt
		} else {
			r.UpdatedAt = r.StartedAt
		}
	}
	return r
}

func safeRawJSON(raw json.RawMessage, fallbackText, fallbackJSON string) json.RawMessage {
	if len(raw) > 0 && json.Valid(raw) {
		return raw
	}
	if fallbackText != "" {
		if encoded, err := json.Marshal(fallbackText); err == nil {
			return encoded
		}
	}
	if fallbackJSON == "" {
		return nil
	}
	return json.RawMessage(fallbackJSON)
}
