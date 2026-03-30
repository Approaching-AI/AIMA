package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
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
				slog.Info("API key authentication enabled")
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

			if app.Support != nil {
				go func() {
					if err := app.Support.RunBackground(ctx); err != nil && !errors.Is(err, context.Canceled) {
						slog.Warn("support supervisor stopped", "error", err)
					}
				}()
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

	return cmd
}

func resolveMCPProfile(mcpEnabled bool, profile string) (mcp.Profile, error) {
	if profile == "" {
		return mcp.ProfileFull, nil
	}
	if !mcpEnabled {
		return mcp.ProfileFull, fmt.Errorf("--mcp-profile requires --mcp")
	}
	p := mcp.Profile(profile)
	if !mcp.IsValidProfile(p) {
		return mcp.ProfileFull, fmt.Errorf("unknown MCP profile %q; valid profiles: operator, patrol, explorer", profile)
	}
	return p, nil
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

// apiKeyAuth wraps an HTTP handler with dynamic Bearer token authentication.
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
