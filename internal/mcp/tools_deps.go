package mcp

import (
	"context"
	"encoding/json"

	"github.com/jguan/aima/internal/engine"
)

// ToolDeps collects all dependencies that tool handlers need.
// Each field is a function provided by other packages at wiring time.
type ToolDeps struct {
	// Hardware (hal package)
	DetectHardware func(ctx context.Context) (json.RawMessage, error)
	CollectMetrics func(ctx context.Context) (json.RawMessage, error)

	// Model management
	ScanModels   func(ctx context.Context) (json.RawMessage, error)
	ListModels   func(ctx context.Context) (json.RawMessage, error)
	PullModel    func(ctx context.Context, name string) error
	ImportModel  func(ctx context.Context, path string) (json.RawMessage, error)
	GetModelInfo func(ctx context.Context, name string) (json.RawMessage, error)
	RemoveModel  func(ctx context.Context, name string, deleteFiles bool) error

	// Engine management
	ScanEngines   func(ctx context.Context, runtime string, autoImport bool) (json.RawMessage, error) // runtime: "auto" | "container" | "native"
	ListEngines   func(ctx context.Context) (json.RawMessage, error)
	GetEngineInfo func(ctx context.Context, name string) (json.RawMessage, error)
	PullEngine    func(ctx context.Context, name string, onProgress func(engine.ProgressEvent)) error
	ImportEngine  func(ctx context.Context, path string) error
	RemoveEngine  func(ctx context.Context, name string, deleteFiles bool) error

	// Deployment (runtime package)
	DeployApply  func(ctx context.Context, engine, model, slot string, configOverrides map[string]any, noPull bool) (json.RawMessage, error)
	DeployDryRun func(ctx context.Context, engine, model, slot string, configOverrides map[string]any) (json.RawMessage, error)
	DeployRun    func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool, onPhase func(phase, msg string), onEngineProgress func(engine.ProgressEvent)) (json.RawMessage, error)
	DeployDelete func(ctx context.Context, name string) error
	DeployStatus func(ctx context.Context, name string) (json.RawMessage, error)
	DeployList   func(ctx context.Context) (json.RawMessage, error)
	DeployLogs   func(ctx context.Context, name string, tailLines int) (string, error)

	// Knowledge
	ResolveConfig           func(ctx context.Context, model, engine string, overrides map[string]any) (json.RawMessage, error)
	SearchKnowledge         func(ctx context.Context, filter map[string]string) (json.RawMessage, error)
	SaveKnowledge           func(ctx context.Context, note json.RawMessage) error
	GeneratePod             func(ctx context.Context, model, engine, slot string, configOverrides map[string]any) (json.RawMessage, error)
	ListProfiles            func(ctx context.Context) (json.RawMessage, error)
	ListEngineAssets        func(ctx context.Context) (json.RawMessage, error)
	ListModelAssets         func(ctx context.Context) (json.RawMessage, error)
	ListPartitionStrategies func(ctx context.Context) (json.RawMessage, error)

	// Benchmark
	RecordBenchmark    func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	RunBenchmark       func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	RunBenchmarkMatrix func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	ListBenchmarks     func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	PromoteConfig      func(ctx context.Context, configID, status string) (json.RawMessage, error)

	// Knowledge query (enhanced — powered by SQLite relational queries)
	SearchConfigs      func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	CompareConfigs     func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	SimilarConfigs     func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	LineageConfigs     func(ctx context.Context, configID string) (json.RawMessage, error)
	GapsKnowledge      func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	AggregateKnowledge func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

	// Stack management
	StackPreflight func(ctx context.Context, tier string) (json.RawMessage, error)
	StackInit      func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error)
	StackStatus    func(ctx context.Context) (json.RawMessage, error)

	// Catalog overlay
	CatalogOverride func(ctx context.Context, kind, name, content string) (json.RawMessage, error)
	CatalogStatus   func(ctx context.Context) (json.RawMessage, error)
	CatalogValidate func(ctx context.Context) (json.RawMessage, error)

	// Deploy approval
	DeployApprove func(ctx context.Context, id int64) (json.RawMessage, error)

	// Agent
	DispatchAsk       func(ctx context.Context, query string, skipPerms bool, sessionID string) (json.RawMessage, string, error)
	AgentStatus       func(ctx context.Context) (json.RawMessage, error)
	RollbackList      func(ctx context.Context) (json.RawMessage, error)
	RollbackRestore   func(ctx context.Context, id int64) (json.RawMessage, error)
	SupportAskForHelp func(ctx context.Context, description, endpoint, inviteCode, workerCode, recoveryCode, referralCode string) (json.RawMessage, error)

	// System
	SystemStatus func(ctx context.Context) (json.RawMessage, error)
	GetConfig    func(ctx context.Context, key string) (string, error)
	SetConfig    func(ctx context.Context, key, value string) error

	// Knowledge (summary)
	ListKnowledgeSummary func(ctx context.Context) (json.RawMessage, error)

	// Knowledge export/import
	ExportKnowledge func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	ImportKnowledge func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

	// Fleet management
	FleetListDevices func(ctx context.Context) (json.RawMessage, error)
	FleetDeviceInfo  func(ctx context.Context, deviceID string) (json.RawMessage, error)
	FleetDeviceTools func(ctx context.Context, deviceID string) (json.RawMessage, error)
	FleetExecTool    func(ctx context.Context, deviceID, toolName string, params json.RawMessage) (json.RawMessage, error)

	// Patrol & Alerts (A2)
	PatrolStatus  func(ctx context.Context) (json.RawMessage, error)
	PatrolAlerts  func(ctx context.Context) (json.RawMessage, error)
	PatrolConfig  func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	PatrolActions func(ctx context.Context, limit int) (json.RawMessage, error)

	// Auto-tuning (A3)
	TuningStart   func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	TuningStatus  func(ctx context.Context) (json.RawMessage, error)
	TuningStop    func(ctx context.Context) (json.RawMessage, error)
	TuningResults func(ctx context.Context) (json.RawMessage, error)

	// Exploration runner
	ExploreStart        func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	ExploreStartAndWait func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	ExploreStatus       func(ctx context.Context, runID string) (json.RawMessage, error)
	ExploreStop         func(ctx context.Context, runID string) (json.RawMessage, error)
	ExploreResult       func(ctx context.Context, runID string) (json.RawMessage, error)

	// Validation (F5)
	ValidateKnowledge func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

	// Engine switch cost (A5/D5)
	EngineSwitchCost func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

	// Open questions (I6)
	OpenQuestions func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

	// Knowledge sync (K6)
	SyncPush   func(ctx context.Context) (json.RawMessage, error)
	SyncPull   func(ctx context.Context) (json.RawMessage, error)
	SyncStatus func(ctx context.Context) (json.RawMessage, error)

	// OpenClaw integration
	OpenClawSync   func(ctx context.Context, dryRun bool) (json.RawMessage, error)
	OpenClawStatus func(ctx context.Context) (json.RawMessage, error)
	OpenClawClaim  func(ctx context.Context, sections []string, dryRun bool) (json.RawMessage, error)

	// Scenario
	ScenarioList  func(ctx context.Context) (json.RawMessage, error)
	ScenarioShow  func(ctx context.Context, name string) (json.RawMessage, error)
	ScenarioApply func(ctx context.Context, name string, dryRun bool) (json.RawMessage, error)

	// Explorer
	ExplorerStatus  func(ctx context.Context) (json.RawMessage, error)
	ExplorerConfig  func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
	ExplorerTrigger func(ctx context.Context) (json.RawMessage, error)

	// Sync v2: advisory pull/push (v0.4 integration)
	SyncPullAdvisories   func(ctx context.Context) (json.RawMessage, error)
	SyncPullScenarios    func(ctx context.Context) (json.RawMessage, error)
	AdvisoryFeedback     func(ctx context.Context, advisoryID, status, reason string) (json.RawMessage, error)
	RequestAdvise        func(ctx context.Context, model, engine, intent string) (json.RawMessage, error)
	RequestScenario      func(ctx context.Context, hardware string, models []string, goal string) (json.RawMessage, error)
	ListCentralScenarios func(ctx context.Context, hardware, source string) (json.RawMessage, error)
}
