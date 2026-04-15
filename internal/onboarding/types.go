// Package onboarding centralizes the business logic that powers AIMA's first-run
// wizard. It is consumed both by HTTP SSE handlers in cmd/aima and (in later
// steps of the refactor) by MCP tools and CLI wrappers, so that human and AI
// operators walk the exact same code path.
package onboarding

import "time"

// Event is a single progress record produced by an asynchronous onboarding
// action (scan / init / deploy). HTTP handlers stream events out via SSE;
// MCP/CLI callers collect them into an events[] slice and return once.
type Event struct {
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

// EventSink receives events as they are produced, enabling real-time streaming
// (e.g. SSE). RunScan/RunInit/RunDeploy still return the full events slice, so
// non-streaming callers (MCP tool, CLI) pass a nil sink.
type EventSink func(Event)

// GPU is a single GPU entry in the onboarding status response.
type GPU struct {
	Name    string `json:"name"`
	VRAMMiB int    `json:"vram_mib"`
	Count   int    `json:"count"`
	Arch    string `json:"arch"`
}

// CPU describes the host CPU in the onboarding status response.
type CPU struct {
	Model string `json:"model"`
	Cores int    `json:"cores"`
}

// Hardware aggregates hardware info for the onboarding status response.
type Hardware struct {
	GPU          []GPU  `json:"gpu"`
	CPU          CPU    `json:"cpu"`
	RAMMiB       int    `json:"ram_mib"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	ProfileMatch string `json:"profile_match"`
}

// StackStatusInfo describes the stack readiness for onboarding.
type StackStatusInfo struct {
	Docker                 string `json:"docker"`
	K3S                    string `json:"k3s"`
	NeedsInit              bool   `json:"needs_init"`
	InitTierRecommendation string `json:"init_tier_recommendation"`
	CanAutoInit            bool   `json:"can_auto_init"`
	InitBlockedReason      string `json:"init_blocked_reason,omitempty"`
}

// VersionInfo holds version check results.
type VersionInfo struct {
	Current             string `json:"current"`
	Latest              string `json:"latest,omitempty"`
	UpgradeAvailable    bool   `json:"upgrade_available"`
	ReleaseURL          string `json:"release_url,omitempty"`
	ReleaseNotesSummary string `json:"release_notes_summary,omitempty"`
}

// StatusResult is the full onboarding status response.
type StatusResult struct {
	OnboardingCompleted bool            `json:"onboarding_completed"`
	Hardware            Hardware        `json:"hardware"`
	StackStatus         StackStatusInfo `json:"stack_status"`
	Version             VersionInfo     `json:"version"`
}

// ScanEngineEntry describes one discovered engine (binary/image).
type ScanEngineEntry struct {
	Type        string `json:"type"`
	Image       string `json:"image,omitempty"`
	RuntimeType string `json:"runtime"`
}

// ScanModelEntry describes one discovered local model.
type ScanModelEntry struct {
	Name      string `json:"name"`
	Format    string `json:"format,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// ScanResult is the aggregated outcome of the parallel scan phase.
type ScanResult struct {
	Engines          []ScanEngineEntry `json:"engines"`
	Models           []ScanModelEntry  `json:"models"`
	CentralConnected bool              `json:"central_connected"`
	ConfigsPulled    int               `json:"configs_pulled,omitempty"`
	BenchmarksPulled int               `json:"benchmarks_pulled,omitempty"`
}

// RecommendedVariant mirrors the JSON shape previously served by the wizard.
type RecommendedVariant struct {
	Name           string `json:"name"`
	Format         string `json:"format"`
	Quantization   string `json:"quantization,omitempty"`
	PrecisionLabel string `json:"precision_label,omitempty"`
	VRAMReqMiB     int    `json:"vram_required_mib,omitempty"`
	GPUCountMin    int    `json:"gpu_count_min,omitempty"`
	DiskSizeMiB    int    `json:"disk_size_mib,omitempty"`
}

// RecommendedEngine describes the preferred engine for a recommendation.
type RecommendedEngine struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	Image      string `json:"image,omitempty"`
	ColdStartS []int  `json:"cold_start_s,omitempty"`
}

// RecommendedEngineStatus describes whether the engine is already installed.
type RecommendedEngineStatus struct {
	Available     bool `json:"available"`
	Installed     bool `json:"installed"`
	NeedsDownload bool `json:"needs_download"`
	NeedsBuild    bool `json:"needs_build"`
}

// RecommendedPerformance records expected perf for a recommendation.
type RecommendedPerformance struct {
	Source         string     `json:"source"`
	TokensPerSec   [2]float64 `json:"tokens_per_second,omitempty"`
	TTFTMs         [2]float64 `json:"ttft_ms,omitempty"`
	ThroughputNote string     `json:"throughput_note,omitempty"`
}

// RecommendedGolden describes golden-config availability.
type RecommendedGolden struct {
	Exists bool   `json:"exists"`
	Source string `json:"source,omitempty"`
}

// RecommendedModelStatus captures local availability + download hints.
type RecommendedModelStatus struct {
	LocalAvailable     bool   `json:"local_available"`
	DownloadSource     string `json:"download_source,omitempty"`
	DownloadRepo       string `json:"download_repo,omitempty"`
	EstDownloadTimeMin int    `json:"estimated_download_time_min,omitempty"`
}

// ModelRecommendation is a single scored recommendation card.
type ModelRecommendation struct {
	ModelName    string `json:"model_name"`
	ModelType    string `json:"model_type"`
	Family       string `json:"family"`
	ParamCount   string `json:"parameter_count"`
	ActiveParams string `json:"active_params,omitempty"`
	ReleasedAt   string `json:"released_at,omitempty"`

	Variant *RecommendedVariant `json:"variant,omitempty"`
	Engine  *RecommendedEngine  `json:"engine,omitempty"`

	EngineStatus RecommendedEngineStatus `json:"engine_status"`
	Performance  RecommendedPerformance  `json:"performance"`
	GoldenConfig RecommendedGolden       `json:"golden_config"`
	ModelStatus  RecommendedModelStatus  `json:"model_status"`
	FitScore     int                     `json:"fit_score"`
	Reason       string                  `json:"recommendation_reason"`
	FitWarnings  []string                `json:"fit_warnings,omitempty"`
	HardwareFit  bool                    `json:"hardware_fit"`
}

// RecommendResult is the complete recommend payload.
type RecommendResult struct {
	HardwareProfile string                `json:"hardware_profile"`
	GPUArch         string                `json:"gpu_arch"`
	GPUVRAMMiB      int                   `json:"gpu_vram_mib"`
	GPUCount        int                   `json:"gpu_count"`
	TotalModels     int                   `json:"total_models_evaluated"`
	Recommendations []ModelRecommendation `json:"recommendations"`
}

// InitResult describes the final state after stack init finishes.
type InitResult struct {
	AllReady    bool            `json:"all_ready"`
	StackStatus StackStatusInfo `json:"stack_status"`
	Tier        string          `json:"tier,omitempty"`
}

// DeployResult is the final outcome of an onboarding deploy action.
type DeployResult struct {
	Name     string `json:"name,omitempty"`
	Model    string `json:"model"`
	Engine   string `json:"engine"`
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
}
