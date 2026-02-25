package cli

import (
	"github.com/spf13/cobra"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/zeroclaw"
)

// App holds all wired dependencies for CLI commands.
type App struct {
	DB         *state.DB
	Catalog    *knowledge.Catalog
	K3S        *k3s.Client
	Proxy      *proxy.Server
	MCP        *mcp.Server
	Dispatcher *agent.Dispatcher
	ZeroClaw   *zeroclaw.Manager
	DataDir    string
	ToolDeps   *mcp.ToolDeps
}

// NewRootCmd creates the root aima command with all subcommands.
func NewRootCmd(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "aima",
		Short: "AI-Inference-Managed-by-AI",
		Long:  "AIMA manages AI inference on edge devices — hardware detection, knowledge-driven config, multi-model deployment.",
		SilenceUsage: true,
	}

	root.AddCommand(
		newInitCmd(app),
		newDeployCmd(app),
		newUndeployCmd(app),
		newStatusCmd(app),
		newModelCmd(app),
		newEngineCmd(app),
		newKnowledgeCmd(app),
		newAskCmd(app),
		newAgentCmd(app),
		newServeCmd(app),
	)

	return root
}
