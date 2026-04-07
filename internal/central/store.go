package central

import (
	"context"
	"crypto/sha256"
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
	UpdateAdvisoryFeedback(ctx context.Context, id string, feedback string, accepted bool) error

	// --- Analysis Runs ---
	InsertAnalysisRun(ctx context.Context, r AnalysisRun) error
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
	ID            string   `json:"id"`
	DeviceID      string   `json:"device_id,omitempty"`
	Hardware      string   `json:"hardware"`
	EngineType    string   `json:"engine_type"`
	EngineVersion string   `json:"engine_version,omitempty"`
	Model         string   `json:"model"`
	Slot          string   `json:"slot,omitempty"`
	Config        string   `json:"config"`
	ConfigHash    string   `json:"config_hash"`
	Status        string   `json:"status"`
	DerivedFrom   string   `json:"derived_from,omitempty"`
	Tags          string   `json:"tags,omitempty"`
	Source        string   `json:"source,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
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
	ID           string `json:"id"`
	Type         string `json:"type"`
	Severity     string `json:"severity"`
	Hardware     string `json:"hardware,omitempty"`
	Model        string `json:"model,omitempty"`
	Engine       string `json:"engine,omitempty"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	Details      string `json:"details"`
	Confidence   string `json:"confidence"`
	AnalysisID   string `json:"analysis_id,omitempty"`
	Feedback     string `json:"feedback,omitempty"`
	Accepted     bool   `json:"accepted"`
	CreatedAt    string `json:"created_at"`
}

type AnalysisRun struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	Summary       string `json:"summary,omitempty"`
	AdvisoryCount int    `json:"advisory_count"`
	DurationMs    int    `json:"duration_ms"`
	Error         string `json:"error,omitempty"`
	CreatedAt     string `json:"created_at"`
}

type Scenario struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Hardware    string `json:"hardware"`
	Models      string `json:"models"`
	Config      string `json:"config"`
	Source      string `json:"source,omitempty"`
	CreatedAt   string `json:"created_at"`
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
	Type     string
	Severity string
	Hardware string
	Limit    int
}

type ScenarioFilter struct {
	Hardware string
	Limit    int
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

// genID generates a short ID with a prefix, using SHA-256 of prefix + timestamp.
func genID(prefix string) string {
	data := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%s_%x", prefix, h[:6])
}
