package onboarding

import (
	"context"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
)

// Deps bundles everything the onboarding business functions need. ToolDeps
// provides the standard MCP tool closures (ScanEngines/ScanModels/StackStatus
// /StackInit/StackPreflight/SyncPull/DeployRun/GetConfig/SetConfig/...);
// BuildHardwareInfo and DetectHWProfile are injected here because they live as
// package-private helpers inside cmd/aima and cannot be imported.
type Deps struct {
	ToolDeps *mcp.ToolDeps

	// BuildHardwareInfo wraps cmd/aima/resolve.go:buildHardwareInfo.
	BuildHardwareInfo func(ctx context.Context) knowledge.HardwareInfo
	// DetectHWProfile wraps cmd/aima/infra.go:detectHWProfile.
	DetectHWProfile func(ctx context.Context) string
	// ListRunningServices returns already-registered inference backends that
	// can be used directly by onboarding.
	ListRunningServices func(ctx context.Context) []RunningService

	// Catalog + SQLite + knowledge store used by Recommend.
	Cat    *knowledge.Catalog
	DB     *state.DB
	KStore *knowledge.Store

	// FirstRunPolicy controls first-run recommendation guardrails. Production
	// wiring loads catalog/onboarding-policy.yaml; nil disables policy
	// guardrails for tests or custom embeddings that do not provide one.
	FirstRunPolicy *FirstRunPolicy
}
