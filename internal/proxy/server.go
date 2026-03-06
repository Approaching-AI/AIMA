package proxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultPort is the default listen port for the AIMA proxy server.
const DefaultPort = 6188

// Backend represents a running inference engine.
type Backend struct {
	ModelName  string `json:"model_name"`
	EngineType string `json:"engine_type"`
	Address    string `json:"address"`
	BasePath   string `json:"base_path"`
	Ready      bool   `json:"ready"`
	Remote     bool   `json:"remote"` // true = discovered via mDNS, not a local deployment
}

func cloneBackend(b *Backend) *Backend {
	if b == nil {
		return nil
	}
	cp := *b
	return &cp
}

// Server is the HTTP inference proxy.
type Server struct {
	addr        string
	apiKey      string
	routes      map[string]*Backend
	mu          sync.RWMutex
	server      *http.Server
	extraRoutes func(*http.ServeMux)
}

// Option configures Server.
type Option func(*Server)

func WithAddr(addr string) Option {
	return func(s *Server) { s.addr = addr }
}

func WithAPIKey(key string) Option {
	return func(s *Server) { s.apiKey = key }
}

func WithExtraRoutes(fn func(*http.ServeMux)) Option {
	return func(s *Server) { s.extraRoutes = fn }
}

// SetAddr configures the listen address. Must be called before Start.
func (s *Server) SetAddr(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addr = addr
}

// SetAPIKey configures API key authentication. Safe to call while server is running.
func (s *Server) SetAPIKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiKey = key
}

// APIKey returns the configured API key (empty string if none).
func (s *Server) APIKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.apiKey
}

// SetExtraRoutes configures additional routes to register on the mux. Must be called before Start.
func (s *Server) SetExtraRoutes(fn func(*http.ServeMux)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extraRoutes = fn
}

func NewServer(opts ...Option) *Server {
	s := &Server{
		addr:   fmt.Sprintf(":%d", DefaultPort),
		routes: make(map[string]*Backend),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// RegisterBackend adds or updates a model route.
func (s *Server) RegisterBackend(model string, backend *Backend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[strings.ToLower(model)] = cloneBackend(backend)
}

// RemoveBackend removes a model route.
func (s *Server) RemoveBackend(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.routes, strings.ToLower(model))
}

// ListBackends returns a copy of all registered backends.
func (s *Server) ListBackends() map[string]*Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*Backend, len(s.routes))
	for k, v := range s.routes {
		result[k] = cloneBackend(v)
	}
	return result
}

// Start starts the HTTP server (blocking).
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.mu.Lock()
	s.server = srv
	s.mu.Unlock()

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("proxy listen: %w", err)
	}
	defer ln.Close()
	slog.Info("proxy server starting", "addr", ln.Addr().String())

	// Watch for context cancellation
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("proxy serve: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.RLock()
	srv := s.server
	s.mu.RUnlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// handler builds the HTTP mux. Exported for testing via handler().
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleInference)
	mux.HandleFunc("/v1/completions", s.handleInference)
	mux.HandleFunc("/v1/embeddings", s.handleInference)

	if s.extraRoutes != nil {
		s.extraRoutes(mux)
	}

	var h http.Handler = mux
	// Always wrap with API key middleware — reads key dynamically so
	// SetAPIKey() takes effect immediately on a running server.
	h = s.apiKeyMiddleware(h)
	return corsMiddleware(h)
}

// apiKeyMiddleware reads the API key from s on each request, enabling hot-reload.
// When no key is configured, all requests pass through.
// The /health endpoint is always exempt for load balancer probes.
func (s *Server) apiKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || strings.HasPrefix(r.URL.Path, "/ui/") || (r.URL.Path == "/" && r.Method == "GET") {
			next.ServeHTTP(w, r)
			return
		}
		key := s.APIKey()
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !CheckBearerAuth(r.Header.Get("Authorization"), key) {
			slog.Warn("unauthorized request", "remote_addr", r.RemoteAddr, "path", r.URL.Path)
			WriteJSONError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CheckBearerAuth validates a Bearer token from the Authorization header.
// The scheme comparison is case-insensitive per RFC 7235; the token comparison
// is constant-time to prevent timing attacks.
func CheckBearerAuth(authHeader, expectedKey string) bool {
	// Parse scheme and token, tolerating extra whitespace.
	authHeader = strings.TrimSpace(authHeader)
	if len(authHeader) < 7 {
		return false
	}
	scheme := authHeader[:6]
	if !strings.EqualFold(scheme, "bearer") {
		return false
	}
	token := strings.TrimSpace(authHeader[6:])
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expectedKey)) == 1
}

// WriteJSONError writes a consistent OpenAI-compatible JSON error response.
func WriteJSONError(w http.ResponseWriter, statusCode int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": message,
			"type":    errType,
		},
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	backends := s.ListBackends()
	ready := 0
	for _, b := range backends {
		if b.Ready {
			ready++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"ready_models": ready,
		"total_models": len(backends),
		"can_infer":    ready > 0,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	backends := s.ListBackends()
	models := make([]map[string]any, 0, len(backends))
	for _, b := range backends {
		models = append(models, map[string]any{
			"model_name":  b.ModelName,
			"engine_type": b.EngineType,
			"ready":       b.Ready,
			"remote":      b.Remote,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"models": models,
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	backends := s.ListBackends()
	data := make([]map[string]string, 0, len(backends))
	for _, b := range backends {
		if !b.Ready || b.Address == "" {
			continue
		}
		data = append(data, map[string]string{
			"id":       b.ModelName,
			"object":   "model",
			"owned_by": "aima",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (s *Server) handleInference(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Read and buffer the body so we can parse model and still forward it (10MB limit)
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		WriteJSONError(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return
	}
	r.Body.Close()

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON in request body")
		return
	}

	backend := s.resolveBackend(req.Model)
	if backend == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"message": fmt.Sprintf("model %q not found; available models: %s", req.Model, s.availableModels()),
				"type":    "model_not_found",
			},
		})
		return
	}
	if !backend.Ready || backend.Address == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"message": fmt.Sprintf("model %q is not ready", req.Model),
				"type":    "service_unavailable",
			},
		})
		return
	}

	// Determine the target path: basePath + suffix from original request
	// e.g., request to /v1/chat/completions with basePath=/v1 → forward to /v1/chat/completions
	targetPath := s.buildTargetPath(backend.BasePath, r.URL.Path)

	target := &url.URL{
		Scheme: "http",
		Host:   backend.Address,
	}

	proxy := &httputil.ReverseProxy{
		Director: func(outReq *http.Request) {
			outReq.URL.Scheme = target.Scheme
			outReq.URL.Host = target.Host
			outReq.URL.Path = targetPath
			outReq.Host = target.Host
			outReq.Body = io.NopCloser(bytes.NewReader(body))
			outReq.ContentLength = int64(len(body))
		},
		FlushInterval: -1, // flush immediately for SSE
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("X-Aima-Model", backend.ModelName)
			resp.Header.Set("X-Aima-Engine", backend.EngineType)
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, outReq *http.Request, err error) {
			slog.Warn("proxy backend error", "backend", backend.Address, "error", err)
			WriteJSONError(rw, http.StatusBadGateway, "backend_error",
				fmt.Sprintf("backend %s unreachable: %v", backend.Address, err))
		},
	}

	proxy.ServeHTTP(w, r)

	slog.Info("proxy request",
		"method", r.Method,
		"path", r.URL.Path,
		"model", req.Model,
		"backend", backend.Address,
		"latency", time.Since(start),
	)
}

// resolveBackend finds the backend for a model name.
func (s *Server) resolveBackend(model string) *Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if b, ok := s.routes[strings.ToLower(model)]; ok {
		return cloneBackend(b)
	}
	return nil
}

// buildTargetPath constructs the forwarding path.
// For request path /v1/chat/completions:
//   - basePath="" → /v1/chat/completions (keep original)
//   - basePath="/v1" → /v1/chat/completions (basePath + suffix after /v1)
func (s *Server) buildTargetPath(basePath, requestPath string) string {
	if basePath == "" {
		return requestPath
	}
	// Strip the /v1 prefix from the request path, then prepend basePath
	suffix := strings.TrimPrefix(requestPath, "/v1")
	return basePath + suffix
}

func (s *Server) availableModels() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	models := make([]string, 0, len(s.routes))
	for k := range s.routes {
		models = append(models, k)
	}
	if len(models) == 0 {
		return "(none)"
	}
	return strings.Join(models, ", ")
}

// corsMiddleware adds CORS headers, restricted to loopback origins to prevent CSRF.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isLoopbackOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isLoopbackOrigin returns true if the origin is a localhost/127.0.0.1/[::1] address.
func isLoopbackOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
