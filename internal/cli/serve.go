package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/openclaw"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/support"
)

func newServeCmd(app *App) *cobra.Command {
	var (
		addr            string
		mcpAddr         string
		mcpMod          bool
		mcpProfile      string
		apiKey          string
		mdnsEnabled     bool
		discoverEnabled bool
		allowInsecure   bool
		staticBackends  []string
		noOpenClawSync  bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the AIMA server",
		Long:  "Start the HTTP proxy server (OpenAI-compatible API) and optionally the MCP server.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// Apply listen address from flag
			app.Proxy.SetAddr(addr)

			// If no API key from flag/env, try persistent config (SQLite)
			if apiKey == "" && app.ToolDeps != nil && app.ToolDeps.GetConfig != nil {
				if stored, err := app.ToolDeps.GetConfig(ctx, "api_key"); err == nil && stored != "" {
					apiKey = stored
					slog.Info("loaded API key from persistent config")
				}
			}

			if err := validateServeSecurity(addr, mcpAddr, mcpMod, apiKey, allowInsecure); err != nil {
				return err
			}
			profile, err := resolveMCPProfile(mcpMod, mcpProfile)
			if err != nil {
				return err
			}

			if !allowInsecure && apiKey == "" && (!isLoopbackListenAddr(addr) || (mcpMod && !isLoopbackListenAddr(mcpAddr))) {
				slog.Warn("starting without API key on non-loopback address; this is insecure")
			}

			// Apply API key authentication if configured
			if apiKey != "" {
				app.Proxy.SetAPIKey(apiKey)
				if app.FleetClient != nil {
					app.FleetClient.SetAPIKey(apiKey)
				}
				// Sync proxy API key to LLM client when it targets the local proxy,
				// so the agent can authenticate with its own proxy.
				if app.LLMClient != nil && app.LLMClient.IsLocalEndpoint() {
					app.LLMClient.SetAPIKey(apiKey)
					slog.Info("synced proxy API key to agent LLM client (local endpoint)")
				}
				slog.Info("API key authentication enabled")
			}

			if err := registerStaticBackends(app.Proxy, staticBackends); err != nil {
				return err
			}

			// Start backend sync loop (reconcile proxy routes with deployments)
			if app.ToolDeps != nil && app.ToolDeps.DeployList != nil {
				listFn := func(ctx context.Context) ([]*proxy.DeploymentInfo, error) {
					raw, err := app.ToolDeps.DeployList(ctx)
					if err != nil {
						return nil, err
					}
					var deps []*proxy.DeploymentInfo
					if err := json.Unmarshal(raw, &deps); err != nil {
						return nil, fmt.Errorf("unmarshal deployments: %w", err)
					}
					return deps, nil
				}
				go proxy.StartSyncLoop(ctx, app.Proxy, listFn, 5*time.Second)
			}
			if app.OpenClaw != nil && openClawAutoSyncEnabled(noOpenClawSync) {
				go openclaw.StartSyncLoop(ctx, app.OpenClaw, 10*time.Second)
			} else if app.OpenClaw != nil {
				slog.Info("openclaw auto-sync loop disabled; sync only via `aima openclaw sync`")
			}

			// Auto-reconcile local assets on startup so Explorer, onboarding, and
			// UI views see locally available engines and models even after an
			// upgrade that skips the first-run onboarding scan.
			if app.ToolDeps != nil && (app.ToolDeps.ScanEngines != nil || app.ToolDeps.ScanModels != nil || app.ToolDeps.ScanExternalServices != nil) {
				go func() {
					scanCtx, scanCancel := context.WithTimeout(ctx, 30*time.Second)
					defer scanCancel()
					runStartupAssetReconcile(scanCtx, app.ToolDeps)
				}()
			}

			if app.Support != nil {
				// Auto-register this edge with aima-service on first boot (or retry
				// after a prior failure). Runs in the background with exponential
				// backoff; does not block server startup, so offline edges still
				// serve local traffic while waiting for network recovery.
				go app.Support.StartRegistrationWorker(ctx, support.BootstrapOptions{})

				go func() {
					if err := app.Support.RunBackground(ctx); err != nil && !errors.Is(err, context.Canceled) {
						slog.Warn("support supervisor stopped", "error", err)
					}
				}()
			}

			// Auto-open browser when launched without subcommand (double-click).
			if app.OpenBrowser {
				app.Proxy.SetOnReady(func(listenAddr string) {
					url := fmt.Sprintf("http://127.0.0.1:%d/ui/", parsePort(addr))
					fmt.Fprintf(os.Stderr, "\n  AIMA is running at: %s\n\n", url)
					openBrowser(url)
				})
			}

			errCh := make(chan error, 2)

			// Start HTTP proxy server
			go func() {
				slog.Info("starting proxy server", "addr", addr)
				errCh <- app.Proxy.Start(ctx)
			}()

			// Start mDNS advertiser (non-fatal on failure)
			if mdnsEnabled {
				port := parsePort(addr)
				models := backendModelNames(app.Proxy)
				adv, err := proxy.StartMDNS(proxy.MDNSConfig{Port: port, Models: models})
				if err != nil {
					slog.Warn("mDNS broadcast failed (non-fatal)", "error", err)
				} else {
					slog.Info("mDNS broadcasting", "service", "_llm._tcp", "port", port)
					defer adv.Shutdown()
				}
			}

			// Start remote discovery loop (find other aima instances via mDNS)
			if discoverEnabled {
				actualPort := parsePort(addr)
				go proxy.StartRemoteDiscoveryLoop(ctx, app.Proxy, 10*time.Second, actualPort)
				if app.FleetRegistry != nil {
					app.FleetRegistry.SetLocalPort(actualPort)
					go app.FleetRegistry.StartDiscoveryLoop(ctx, 10*time.Second)
				}
			}

			// Start MCP server if requested (on a separate port)
			if mcpMod {
				if profile != mcp.ProfileFull {
					app.MCP.SetProfile(profile)
					slog.Info("MCP tool profile active", "profile", string(profile))
				}
				go func() {
					slog.Info("starting MCP server (HTTP)", "addr", mcpAddr)
					mux := http.NewServeMux()
					var handler http.Handler = app.MCP
					// Wrap with dynamic API key auth (reads from proxy on each request)
					handler = apiKeyAuth(app.Proxy.APIKey, handler)
					mux.Handle("/mcp", handler)
					server := &http.Server{
						Addr:              mcpAddr,
						Handler:           mux,
						ReadHeaderTimeout: 10 * time.Second,
					}
					go func() {
						<-ctx.Done()
						shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer shutdownCancel()
						server.Shutdown(shutdownCtx)
					}()
					errCh <- server.ListenAndServe()
				}()
			}

			// Wait for context cancellation or error
			select {
			case <-ctx.Done():
				slog.Info("shutting down")
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutdownCancel()
				app.Proxy.Shutdown(shutdownCtx)
				return nil
			case err := <-errCh:
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					return fmt.Errorf("server error: %w", err)
				}
				return nil
			}
		},
	}

	defaultKey := os.Getenv("AIMA_API_KEY")
	cmd.Flags().StringVar(&addr, "addr", fmt.Sprintf("127.0.0.1:%d", proxy.DefaultPort), "Proxy server listen address")
	cmd.Flags().StringVar(&mcpAddr, "mcp-addr", "127.0.0.1:9090", "MCP server listen address")
	cmd.Flags().BoolVar(&mcpMod, "mcp", false, "Also serve MCP protocol over HTTP")
	cmd.Flags().StringVar(&mcpProfile, "mcp-profile", "", "MCP tool profile: operator, patrol, explorer (default: all tools)")
	cmd.Flags().StringVar(&apiKey, "api-key", defaultKey, "API key for authentication (or set AIMA_API_KEY env)")
	cmd.Flags().BoolVar(&mdnsEnabled, "mdns", true, "Enable mDNS service broadcast")
	cmd.Flags().BoolVar(&discoverEnabled, "discover", false, "Discover remote inference services via mDNS")
	cmd.Flags().BoolVar(&allowInsecure, "allow-insecure-no-auth", false, "Allow non-loopback listen addresses without API key (NOT recommended)")
	cmd.Flags().StringArrayVar(&staticBackends, "backend", nil, "Static backend registration: model=http://host:port[/base],engine=vllm,upstream=served,param=35B,context=32768")
	cmd.Flags().BoolVar(&noOpenClawSync, "no-openclaw-sync", false, "Disable the automatic OpenClaw config sync loop; sync only on explicit `aima openclaw sync` (or set AIMA_OPENCLAW_SYNC=manual)")

	return cmd
}

func runStartupAssetReconcile(ctx context.Context, deps *mcp.ToolDeps) {
	if deps == nil {
		return
	}
	if deps.ScanEngines != nil {
		if _, err := deps.ScanEngines(ctx, "auto", false); err != nil {
			slog.Warn("startup engine scan failed (non-fatal)", "error", err)
		} else {
			slog.Info("startup engine scan completed")
		}
	}
	if deps.ScanModels != nil {
		if _, err := deps.ScanModels(ctx); err != nil {
			slog.Warn("startup model scan failed (non-fatal)", "error", err)
		} else {
			slog.Info("startup model scan completed")
		}
	}
	if deps.ScanExternalServices != nil {
		if _, err := deps.ScanExternalServices(ctx); err != nil {
			slog.Warn("startup external service scan failed (non-fatal)", "error", err)
		} else {
			slog.Info("startup external service scan completed")
		}
	}
}

func registerStaticBackends(server *proxy.Server, specs []string) error {
	for _, spec := range specs {
		model, backend, err := parseStaticBackendSpec(spec)
		if err != nil {
			return err
		}
		server.RegisterBackend(model, backend)
		slog.Info("static backend registered", "model", model, "addr", backend.Address, "engine", backend.EngineType)
	}
	return nil
}

func parseStaticBackendSpec(spec string) (string, *proxy.Backend, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", nil, fmt.Errorf("empty static backend spec")
	}

	parts := strings.Split(spec, ",")
	model, rawTarget, ok := strings.Cut(strings.TrimSpace(parts[0]), "=")
	if !ok || strings.TrimSpace(model) == "" || strings.TrimSpace(rawTarget) == "" {
		return "", nil, fmt.Errorf("static backend %q must start with model=http://host:port[/base]", spec)
	}
	model = strings.TrimSpace(model)
	target := strings.TrimSpace(rawTarget)
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return "", nil, fmt.Errorf("static backend %q has invalid target %q", spec, rawTarget)
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	backend := &proxy.Backend{
		ModelName:     model,
		UpstreamModel: model,
		Address:       parsed.Host,
		BasePath:      basePath,
		Ready:         true,
		Remote:        true,
	}

	for _, rawPart := range parts[1:] {
		key, value, ok := strings.Cut(strings.TrimSpace(rawPart), "=")
		if !ok {
			return "", nil, fmt.Errorf("static backend %q has malformed option %q", spec, rawPart)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "engine":
			backend.EngineType = value
		case "upstream", "served_model", "served-model":
			backend.UpstreamModel = value
		case "base_path", "base-path":
			backend.BasePath = strings.TrimRight(value, "/")
		case "param", "params", "parameter_count", "parameter-count":
			backend.ParameterCount = value
		case "context", "context_window", "context-window":
			contextWindow, err := strconv.Atoi(value)
			if err != nil || contextWindow < 0 {
				return "", nil, fmt.Errorf("static backend %q has invalid context value %q", spec, value)
			}
			backend.ContextWindowTokens = contextWindow
		default:
			return "", nil, fmt.Errorf("static backend %q has unknown option %q", spec, key)
		}
	}

	return model, backend, nil
}

func resolveMCPProfile(mcpEnabled bool, profile string) (mcp.Profile, error) {
	if profile == "" {
		return mcp.ProfileFull, nil
	}
	if !mcpEnabled {
		return mcp.ProfileFull, fmt.Errorf("--mcp-profile requires --mcp")
	}
	return parseMCPProfile(profile)
}

func parseMCPProfile(profile string) (mcp.Profile, error) {
	p := mcp.Profile(profile)
	if !mcp.IsValidProfile(p) {
		return mcp.ProfileFull, fmt.Errorf("unknown MCP profile %q; valid profiles: operator, patrol, explorer", profile)
	}
	return p, nil
}

// openClawAutoSyncEnabled reports whether `aima serve` should run the background
// OpenClaw config sync loop. A5: a partner can disable it (and drive sync only via
// explicit `aima openclaw sync`) with the --no-openclaw-sync flag or, at the
// product level, AIMA_OPENCLAW_SYNC=manual|off|false|0|no.
func openClawAutoSyncEnabled(noFlag bool) bool {
	if noFlag {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AIMA_OPENCLAW_SYNC"))) {
	case "manual", "off", "false", "0", "no":
		return false
	}
	return true
}

func validateServeSecurity(addr, mcpAddr string, mcpEnabled bool, apiKey string, allowInsecure bool) error {
	if apiKey != "" || allowInsecure {
		return nil
	}
	if !isLoopbackListenAddr(addr) {
		return fmt.Errorf("refusing insecure proxy listen address %q without API key; set --api-key or pass --allow-insecure-no-auth", addr)
	}
	if mcpEnabled && !isLoopbackListenAddr(mcpAddr) {
		return fmt.Errorf("refusing insecure MCP listen address %q without API key; set --api-key or pass --allow-insecure-no-auth", mcpAddr)
	}
	return nil
}

func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		// Empty host means all interfaces (e.g. ":6188"), not loopback-only.
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// parsePort extracts the port number from an address like ":6188" or "0.0.0.0:6188".
func parsePort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return proxy.DefaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return proxy.DefaultPort
	}
	return port
}

// backendModelNames returns the list of currently registered model names.
func backendModelNames(s *proxy.Server) []string {
	backends := s.ListBackends()
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	return names
}

// openBrowser opens the given URL in the user's default browser.
// Fire-and-forget: errors are silently ignored (e.g. headless Linux).
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return
	}
	_ = cmd.Start()
}

// keyFn is called on each request, enabling hot-reload of the API key.
// When keyFn returns empty string, all requests pass through.
func apiKeyAuth(keyFn func() string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyFn()
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !proxy.CheckBearerAuth(r.Header.Get("Authorization"), key) {
			slog.Warn("MCP unauthorized request", "remote_addr", r.RemoteAddr)
			proxy.WriteJSONError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
