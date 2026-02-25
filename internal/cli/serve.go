package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func newServeCmd(app *App) *cobra.Command {
	var (
		addr    string
		mcpAddr string
		mcpMod  bool
		apiKey  string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the AIMA server",
		Long:  "Start the HTTP proxy server (OpenAI-compatible API) and optionally the MCP server.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// Apply API key authentication if configured
			if apiKey != "" {
				app.Proxy.SetAPIKey(apiKey)
				slog.Info("API key authentication enabled")
			}

			errCh := make(chan error, 2)

			// Start HTTP proxy server
			go func() {
				slog.Info("starting proxy server", "addr", addr)
				errCh <- app.Proxy.Start(ctx)
			}()

			// Start MCP server if requested (on a separate port)
			if mcpMod {
				go func() {
					slog.Info("starting MCP server (HTTP)", "addr", mcpAddr)
					mux := http.NewServeMux()
					var handler http.Handler = app.MCP
					if apiKey != "" {
						handler = apiKeyAuth(apiKey, handler)
					}
					mux.Handle("/mcp", handler)
					server := &http.Server{Addr: mcpAddr, Handler: mux}
					go func() {
						<-ctx.Done()
						server.Shutdown(context.Background())
					}()
					errCh <- server.ListenAndServe()
				}()
			}

			// Wait for context cancellation or error
			select {
			case <-ctx.Done():
				slog.Info("shutting down")
				app.Proxy.Shutdown(context.Background())
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
	cmd.Flags().StringVar(&addr, "addr", ":8080", "Proxy server listen address")
	cmd.Flags().StringVar(&mcpAddr, "mcp-addr", ":9090", "MCP server listen address")
	cmd.Flags().BoolVar(&mcpMod, "mcp", false, "Also serve MCP protocol over HTTP")
	cmd.Flags().StringVar(&apiKey, "api-key", defaultKey, "API key for authentication (or set AIMA_API_KEY env)")

	return cmd
}

// apiKeyAuth wraps an HTTP handler with Bearer token authentication.
func apiKeyAuth(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+key {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
