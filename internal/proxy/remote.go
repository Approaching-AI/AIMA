package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// SyncRemoteBackends discovers remote aima instances and registers their models.
// Local backends (Remote==false) always take priority — remote models with the
// same name are skipped. localPort is the proxy's own listen port; services
// on a local IP with the same port are skipped to prevent self-discovery loops.
// The proxy's API key is forwarded to remote /v1/models queries so that
// authenticated peers respond correctly.
func SyncRemoteBackends(ctx context.Context, s *Server, services []DiscoveredService, localPort int) {
	apiKey := s.APIKey()
	// Collect local model names (Remote==false)
	localModels := make(map[string]bool)
	for name, b := range s.ListBackends() {
		if !b.Remote {
			localModels[strings.ToLower(name)] = true
		}
	}

	// Track which remote models are still alive this round
	alive := make(map[string]bool)

	for _, svc := range services {
		addr := svc.AddrV4
		if addr == "" {
			addr = svc.Host
		}
		if addr == "" {
			slog.Debug("remote: skipping service with no address", "name", svc.Name)
			continue
		}

		slog.Debug("remote: processing service", "name", svc.Name, "addr", addr, "port", svc.Port)

		// Skip self: same port on a local interface address
		if svc.Port == localPort && isLocalIP(addr) {
			slog.Debug("remote: skipping self", "addr", addr, "port", svc.Port)
			continue
		}

		models := QueryRemoteStatus(ctx, addr, svc.Port, apiKey)
		for _, model := range models {
			// Local always wins
			if localModels[strings.ToLower(model.ID)] {
				slog.Debug("remote: skipping model (local exists)", "model", model.ID, "remote", addr)
				continue
			}

			alive[strings.ToLower(model.ID)] = true
			address := fmt.Sprintf("%s:%d", addr, svc.Port)
			s.RegisterBackend(model.ID, &Backend{
				ModelName:           model.ID,
				EngineType:          "remote",
				Address:             address,
				Ready:               true,
				Remote:              true,
				ParameterCount:      model.ParameterCount,
				ContextWindowTokens: model.ContextWindowTokens,
			})
			slog.Info("remote: registered model", "model", model.ID, "address", address)
		}
	}

	// Clean stale remote backends not seen this round
	for name, b := range s.ListBackends() {
		if b.Remote && !b.External && !alive[strings.ToLower(name)] {
			slog.Info("remote: removing stale backend", "model", name)
			s.RemoveBackend(name)
		}
	}
}

// StartRemoteDiscoveryLoop periodically discovers remote aima instances
// and syncs their models into the local proxy. localPort is the proxy's
// own listen port, used to filter out self-discovery.
func StartRemoteDiscoveryLoop(ctx context.Context, s *Server, interval time.Duration, localPort int) {
	doSync := func() {
		services, err := Discover(ctx, 3*time.Second)
		if err != nil {
			slog.Warn("remote: mDNS discovery failed", "error", err)
			return
		}
		if len(services) > 0 {
			slog.Info("remote: discovered services", "count", len(services))
		}
		SyncRemoteBackends(ctx, s, services, localPort)
	}

	// Immediate first sync
	doSync()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			doSync()
		}
	}
}

// QueryRemoteStatus fetches /status from a remote aima instance and returns
// ready models with ranking metadata. Falls back to /v1/models when /status is
// unavailable or returns an unexpected payload.
func QueryRemoteStatus(ctx context.Context, addr string, port int, apiKey string) []AdvertisedModel {
	url := fmt.Sprintf("http://%s:%d/status", addr, port)

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("remote: failed to query status", "url", url, "error", err)
		return advertisedModelsFromIDs(QueryRemoteModels(ctx, addr, port, apiKey))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return advertisedModelsFromIDs(QueryRemoteModels(ctx, addr, port, apiKey))
	}

	var result struct {
		Models []struct {
			ModelName           string `json:"model_name"`
			Ready               *bool  `json:"ready"`
			Remote              bool   `json:"remote"`
			ParameterCount      string `json:"parameter_count"`
			ContextWindowTokens int    `json:"context_window_tokens"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Debug("remote: failed to parse status response", "url", url, "error", err)
		return advertisedModelsFromIDs(QueryRemoteModels(ctx, addr, port, apiKey))
	}

	models := make([]AdvertisedModel, 0, len(result.Models))
	for _, m := range result.Models {
		if m.Ready != nil && !*m.Ready {
			continue
		}
		if strings.TrimSpace(m.ModelName) == "" {
			continue
		}
		models = append(models, AdvertisedModel{
			ID:                  m.ModelName,
			ParameterCount:      m.ParameterCount,
			ContextWindowTokens: m.ContextWindowTokens,
			Remote:              m.Remote,
		})
	}
	SortAdvertisedModels(models)
	if len(models) == 0 {
		return advertisedModelsFromIDs(QueryRemoteModels(ctx, addr, port, apiKey))
	}
	return models
}

// QueryRemoteModels fetches /v1/models from a remote aima instance.
// apiKey is sent as Bearer token when non-empty so authenticated peers respond.
// Returns nil on any error (non-fatal).
func QueryRemoteModels(ctx context.Context, addr string, port int, apiKey string) []string {
	url := fmt.Sprintf("http://%s:%d/v1/models", addr, port)

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("remote: failed to query models", "url", url, "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	// Parse OpenAI-compatible /v1/models response
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Debug("remote: failed to parse models response", "url", url, "error", err)
		return nil
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models
}

func advertisedModelsFromIDs(ids []string) []AdvertisedModel {
	models := make([]AdvertisedModel, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		models = append(models, AdvertisedModel{ID: id})
	}
	SortAdvertisedModels(models)
	return models
}

// IsLocalIP checks if addr belongs to the local machine.
func IsLocalIP(addr string) bool { return isLocalIP(addr) }

// isLocalIP checks if addr belongs to the local machine.
func isLocalIP(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipnet.IP.Equal(ip) {
			return true
		}
	}
	return false
}
