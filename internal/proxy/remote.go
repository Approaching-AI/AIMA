package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// SyncRemoteBackends discovers remote aima instances and registers their models.
// Local backends (Remote==false) always take priority — remote models with the
// same name are skipped. localPort is the proxy's own listen port; services
// on a local IP with the same port are skipped to prevent self-discovery loops.
func SyncRemoteBackends(ctx context.Context, s *Server, services []DiscoveredService, localPort int) {
	// Collect local model names (Remote==false)
	localModels := make(map[string]bool)
	for name, b := range s.ListBackends() {
		if !b.Remote {
			localModels[name] = true
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
			continue
		}

		// Skip self: same port on a local interface address
		if svc.Port == localPort && isLocalIP(addr) {
			slog.Debug("remote: skipping self", "addr", addr, "port", svc.Port)
			continue
		}

		models := queryRemoteModels(ctx, addr, svc.Port)
		for _, model := range models {
			// Local always wins
			if localModels[model] {
				slog.Debug("remote: skipping model (local exists)", "model", model, "remote", addr)
				continue
			}

			alive[model] = true
			address := fmt.Sprintf("%s:%d", addr, svc.Port)
			s.RegisterBackend(model, &Backend{
				ModelName:  model,
				EngineType: "remote",
				Address:    address,
				Ready:      true,
				Remote:     true,
			})
			slog.Info("remote: registered model", "model", model, "address", address)
		}
	}

	// Clean stale remote backends not seen this round
	for name, b := range s.ListBackends() {
		if b.Remote && !alive[name] {
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

// queryRemoteModels fetches /v1/models from a remote aima instance.
// Returns nil on any error (non-fatal).
func queryRemoteModels(ctx context.Context, addr string, port int) []string {
	url := fmt.Sprintf("http://%s:%d/v1/models", addr, port)

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil
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
