package cli

import (
	"github.com/spf13/cobra"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/fleet"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
)

// App holds all wired dependencies for CLI commands.
type App struct {
	DB            *state.DB
	Catalog       *knowledge.Catalog
	Proxy         *proxy.Server
	MCP           *mcp.Server
	ToolDeps      *mcp.ToolDeps
	FleetRegistry *fleet.Registry
}

// NewRootCmd creates the root aima command with all subcommands.
func NewRootCmd(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "aima",
		Short: "AI-Inference-Managed-by-AI",
		Long:  "AIMA manages AI inference on edge devices — hardware detection, knowledge-driven config, multi-model deployment.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newInitCmd(app),
		newHalCmd(app),
		newDeployCmd(app),
		newUndeployCmd(app),
		newStatusCmd(app),
		newModelCmd(app),
		newEngineCmd(app),
		newKnowledgeCmd(app),
		newCatalogCmd(app),
		newBenchmarkCmd(app),
		newAskCmd(app),
		newAgentCmd(app),
		newServeCmd(app),
		newDiscoverCmd(app),
		newFleetCmd(app),
		newVersionCmd(),
	)

	return root
}
