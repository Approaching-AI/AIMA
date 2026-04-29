package proxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultPort is the default listen port for the AIMA proxy server.
const DefaultPort = 6188

// LabelServedModel stores the upstream model identifier expected by the backend.
// The proxy keeps routing by AIMA's canonical model name, but rewrites forwarded
// requests to this model when the engine expects a different served name.
const LabelServedModel = "aima.dev/served-model"

// LabelParameterCount stores the model parameter count used for agent ranking.
const LabelParameterCount = "aima.dev/parameter_count"

// Backend represents a running inference engine.
type Backend struct {
	ModelName           string            `json:"model_name"`
	UpstreamModel       string            `json:"upstream_model,omitempty"`
	EngineType          string            `json:"engine_type"`
	Address             string            `json:"address"`
	BasePath            string            `json:"base_path"`
	Ready               bool              `json:"ready"`
	Remote              bool              `json:"remote"` // true = discovered via mDNS, not a local deployment
	External            bool              `json:"external"`
	PathOverrides       map[string]string `json:"path_overrides,omitempty"`
	ParameterCount      string            `json:"parameter_count,omitempty"`
	ContextWindowTokens int               `json:"context_window_tokens,omitempty"`
}

func cloneBackend(b *Backend) *Backend {
	if b == nil {
		return nil
	}
	cp := *b
	if b.PathOverrides != nil {
		cp.PathOverrides = make(map[string]string, len(b.PathOverrides))
		for k, v := range b.PathOverrides {
			cp.PathOverrides[k] = v
		}
	}
	return &cp
}

func backendUpstreamModel(b *Backend) string {
	if b == nil {
		return ""
	}
	if model := strings.TrimSpace(b.UpstreamModel); model != "" {
		return model
	}
	return strings.TrimSpace(b.ModelName)
}

// Server is the HTTP inference proxy.
type Server struct {
	addr            string
	apiKey          string
	routes          map[string]*Backend
	mu              sync.RWMutex
	server          *http.Server
	extraRoutes     func(*http.ServeMux)
	requestRewriter func(path, contentType, model, engineType string, body []byte) []byte
	onReady         func(addr string)
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

func WithRequestRewriter(fn func(path, contentType, model, engineType string, body []byte) []byte) Option {
	return func(s *Server) { s.requestRewriter = fn }
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

// SetRequestRewriter installs a request body rewrite hook for inference requests.
// The hook may return the original body unchanged.
func (s *Server) SetRequestRewriter(fn func(path, contentType, model, engineType string, body []byte) []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestRewriter = fn
}

// SetOnReady registers a callback invoked once the server is listening.
// The callback receives the resolved listen address (e.g. "127.0.0.1:6188").
// Must be called before Start.
func (s *Server) SetOnReady(fn func(addr string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onReady = fn
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

	// Notify that the server is ready to accept connections.
	s.mu.RLock()
	onReady := s.onReady
	s.mu.RUnlock()
	if onReady != nil {
		go onReady(ln.Addr().String())
	}

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
	for _, path := range []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/embeddings",
	} {
		mux.HandleFunc(path, s.handleInference)
	}
	if s.extraRoutes == nil {
		// OpenClaw installs protocol-aware handlers for these paths. Only use the
		// generic passthrough fallback when no extra route set is mounted.
		for _, path := range []string{
			"/v1/audio/speech",
			"/v1/tts",
			"/v1/audio/transcriptions",
			"/v1/images/generations",
		} {
			mux.HandleFunc(path, s.handleInference)
		}
	}

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
	for _, b := range rankedBackends(backends, false) {
		models = append(models, map[string]any{
			"model_name":            b.ModelName,
			"engine_type":           b.EngineType,
			"ready":                 b.Ready,
			"remote":                b.Remote,
			"external":              b.External,
			"parameter_count":       b.ParameterCount,
			"context_window_tokens": b.ContextWindowTokens,
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
	for _, b := range rankedBackends(backends, true) {
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

func rankedBackends(backends map[string]*Backend, readyOnly bool) []*Backend {
	items := make([]*Backend, 0, len(backends))
	for _, b := range backends {
		if readyOnly && (!b.Ready || b.Address == "") {
			continue
		}
		items = append(items, b)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return BetterAdvertisedModel(
			AdvertisedModel{
				ID:                  items[i].ModelName,
				ParameterCount:      items[i].ParameterCount,
				ContextWindowTokens: items[i].ContextWindowTokens,
				Remote:              items[i].Remote,
			},
			AdvertisedModel{
				ID:                  items[j].ModelName,
				ParameterCount:      items[j].ParameterCount,
				ContextWindowTokens: items[j].ContextWindowTokens,
				Remote:              items[j].Remote,
			},
		)
	})
	return items
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

	model, err := extractModelFromRequest(r.Header.Get("Content-Type"), body)
	if err != nil {
		WriteJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	backend := s.resolveBackend(model)
	if backend == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"message": fmt.Sprintf("model %q not found; available models: %s", model, s.availableModels()),
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
				"message": fmt.Sprintf("model %q is not ready", model),
				"type":    "service_unavailable",
			},
		})
		return
	}

	s.mu.RLock()
	requestRewriter := s.requestRewriter
	s.mu.RUnlock()
	if requestRewriter != nil {
		body = requestRewriter(r.URL.Path, r.Header.Get("Content-Type"), model, backend.EngineType, body)
	}
	if upstreamModel := backendUpstreamModel(backend); upstreamModel != "" && model != upstreamModel {
		body = rewriteModelInBody(r.Header.Get("Content-Type"), body, upstreamModel)
	}

	// Determine the target path: basePath + suffix from original request
	// e.g., request to /v1/chat/completions with basePath=/v1 → forward to /v1/chat/completions
	targetPath := s.buildTargetPath(backend.BasePath, r.URL.Path)
	if override := strings.TrimSpace(backend.PathOverrides[r.URL.Path]); override != "" {
		targetPath = override
	}

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
		"model", model,
		"backend", backend.Address,
		"latency", time.Since(start),
	)
}

func extractModelFromRequest(contentType string, body []byte) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("invalid content type")
	}

	switch mediaType {
	case "", "application/json":
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return "", fmt.Errorf("invalid JSON in request body")
		}
		return req.Model, nil
	case "application/x-www-form-urlencoded":
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", fmt.Errorf("invalid form body")
		}
		return values.Get("model"), nil
	case "multipart/form-data":
		boundary := params["boundary"]
		if boundary == "" {
			return "", fmt.Errorf("multipart request missing boundary")
		}
		reader := multipart.NewReader(bytes.NewReader(body), boundary)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", fmt.Errorf("invalid multipart form body")
			}
			if part.FormName() != "model" {
				part.Close()
				continue
			}
			data, err := io.ReadAll(io.LimitReader(part, 4096))
			part.Close()
			if err != nil {
				return "", fmt.Errorf("failed to read model form field")
			}
			return strings.TrimSpace(string(data)), nil
		}
		return "", fmt.Errorf("missing model field in request body")
	default:
		return "", fmt.Errorf("unsupported content type %q", mediaType)
	}
}

func rewriteModelInBody(contentType string, body []byte, model string) []byte {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil && strings.TrimSpace(contentType) != "" {
		return body
	}

	switch mediaType {
	case "", "application/json":
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			return body
		}
		req["model"] = model
		out, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return out
	case "application/x-www-form-urlencoded":
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return body
		}
		values.Set("model", model)
		return []byte(values.Encode())
	default:
		return body
	}
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
