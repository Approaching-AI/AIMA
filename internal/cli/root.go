package cli

import (
	"github.com/spf13/cobra"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/fleet"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/openclaw"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/support"
)

// App holds all wired dependencies for CLI commands.
type App struct {
	DB            *state.DB
	Catalog       *knowledge.Catalog
	Proxy         *proxy.Server
	MCP           *mcp.Server
	ToolDeps      *mcp.ToolDeps
	OpenClaw      *openclaw.Deps
	FleetRegistry *fleet.Registry
	FleetClient   *fleet.Client
	Support       *support.Service
	LLMClient     *agent.OpenAIClient // Agent's LLM client — used to sync proxy API key.
	OpenBrowser   bool                // When true, the default (no-subcommand) invocation opens the UI in a browser.
	RemoteClient  *RemoteMCPClient    // Captures root --remote/--api-key so every subcommand can dispatch to a remote MCP endpoint.
	InviteCode    string              // Captures root --invite-code; persisted to support.invite_code on PersistentPreRun for the aima-service registration worker.
}

// NewRootCmd creates the root aima command with all subcommands.
func NewRootCmd(app *App) *cobra.Command {
	if app.RemoteClient == nil {
		app.RemoteClient = &RemoteMCPClient{}
	}
	root := &cobra.Command{
		Use:           "aima",
		Short:         "AI-Inference-Managed-by-AI",
		Long:          "AIMA manages AI inference on edge devices — hardware detection, knowledge-driven config, multi-model deployment.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			app.RemoteClient.Endpoint = envOrFlag(app.RemoteClient.Endpoint, "AIMA_REMOTE")
			app.RemoteClient.APIKey = envOrFlag(app.RemoteClient.APIKey, "AIMA_API_KEY")
			// Persist --invite-code so `aima serve`'s registration worker finds
			// it on first boot. Env var (AIMA_INVITE_CODE) is read directly by
			// the support package's resolveInviteCode and takes priority, so
			// only the explicit flag needs persistence here.
			if app.DB != nil && app.InviteCode != "" {
				_ = app.DB.SetConfig(cmd.Context(), support.ConfigInviteCode, app.InviteCode)
			}
		},
	}
	root.PersistentFlags().StringVar(&app.RemoteClient.Endpoint, "remote", "",
		"Remote `aima serve --mcp` endpoint (e.g. http://host:9090). When set, subcommands dispatch via MCP tools/call against the remote instead of running in-process. Falls back to AIMA_REMOTE.")
	root.PersistentFlags().StringVar(&app.RemoteClient.APIKey, "api-key", "",
		"Bearer API key for --remote. Falls back to AIMA_API_KEY.")
	root.PersistentFlags().StringVar(&app.InviteCode, "invite-code", "",
		"aima-service invite code for first-boot device registration. Persisted to support.invite_code. Falls back to AIMA_INVITE_CODE env var at registration time.")

	root.AddCommand(
		newRunCmd(app),
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
		newAskForHelpCmd(app),
		newAskCmd(app),
		newDeviceCmd(app),
		newAgentCmd(app),
		newConfigCmd(app),
		newServeCmd(app),
		newMCPCmd(app),
		newFleetCmd(app),
		newTUICmd(app),
		newExploreCmd(app),
		newTuningCmd(app),
		newOpenClawCmd(app),
		newScenarioCmd(app),
		newExplorerCmd(app),
		newOnboardingCmd(app),
		newVersionCmd(),
	)

	return root
}
