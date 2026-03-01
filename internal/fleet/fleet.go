package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/proxy"
)

// Device represents an AIMA instance discovered on the LAN.
type Device struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	AddrV4   string   `json:"addr_v4"`
	Port     int      `json:"port"`
	Models   []string `json:"models"`
	Online   bool     `json:"online"`
	LastSeen string   `json:"last_seen"`
	Self     bool     `json:"self"`
}

// Registry tracks discovered AIMA devices.
type Registry struct {
	mu        sync.RWMutex
	devices   map[string]*Device
	localPort int
}

// NewRegistry creates a device registry.
func NewRegistry(localPort int) *Registry {
	return &Registry{
		devices:   make(map[string]*Device),
		localPort: localPort,
	}
}

// Update reconciles the registry with freshly discovered services.
func (r *Registry) Update(services []proxy.DiscoveredService) {
	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[string]bool)
	now := time.Now().UTC().Format(time.RFC3339)

	for _, svc := range services {
		addr := svc.AddrV4
		if addr == "" {
			addr = svc.Host
		}
		if addr == "" {
			continue
		}

		id := normalizeID(svc.Name)
		seen[id] = true

		self := svc.Port == r.localPort && proxy.IsLocalIP(addr)

		// Parse models from TXT records
		var models []string
		for _, info := range svc.Info {
			if strings.HasPrefix(info, "models=") {
				models = strings.Split(strings.TrimPrefix(info, "models="), ",")
			}
		}

		r.devices[id] = &Device{
			ID:       id,
			Name:     extractInstanceName(svc.Name),
			AddrV4:   addr,
			Port:     svc.Port,
			Models:   models,
			Online:   true,
			LastSeen: now,
			Self:     self,
		}
	}

	// Mark unseen devices as offline
	for id, d := range r.devices {
		if !seen[id] {
			d.Online = false
		}
	}
}

// List returns all known devices.
func (r *Registry) List() []*Device {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]*Device, 0, len(r.devices))
	for _, d := range r.devices {
		cp := *d
		list = append(list, &cp)
	}
	return list
}

// SetLocalPort updates the local port used for self-detection.
// Call before starting the discovery loop with the actual listen port.
func (r *Registry) SetLocalPort(port int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.localPort = port
}

// Get returns a device by ID, or nil if not found.
func (r *Registry) Get(id string) *Device {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.devices[id]
	if !ok {
		return nil
	}
	cp := *d
	return &cp
}

// StartDiscoveryLoop periodically discovers AIMA devices and updates the registry.
func (r *Registry) StartDiscoveryLoop(ctx context.Context, interval time.Duration) {
	doDiscover := func() {
		services, err := proxy.Discover(ctx, 3*time.Second)
		if err != nil {
			slog.Warn("fleet: mDNS discovery failed", "error", err)
			return
		}
		r.Update(services)
		if len(services) > 0 {
			slog.Debug("fleet: registry updated", "devices", len(services))
		}
		r.healthCheck()
	}

	doDiscover()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			doDiscover()
		}
	}
}

// healthCheck verifies TCP reachability of non-self devices and marks
// unreachable ones as offline. This handles cases where mDNS returns
// addresses on non-routable subnets (e.g., cross-subnet LAN IPs).
func (r *Registry) healthCheck() {
	r.mu.RLock()
	var targets []string
	for id, d := range r.devices {
		if !d.Self && d.Online {
			targets = append(targets, id)
		}
	}
	r.mu.RUnlock()

	if len(targets) == 0 {
		return
	}

	type result struct {
		id   string
		ok   bool
	}
	ch := make(chan result, len(targets))

	for _, id := range targets {
		r.mu.RLock()
		d := r.devices[id]
		addr := fmt.Sprintf("%s:%d", d.AddrV4, d.Port)
		r.mu.RUnlock()

		go func(id, addr string) {
			conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				ch <- result{id: id, ok: false}
				return
			}
			conn.Close()
			ch <- result{id: id, ok: true}
		}(id, addr)
	}

	for range targets {
		res := <-ch
		if !res.ok {
			r.mu.Lock()
			if d, ok := r.devices[res.id]; ok && d.Online {
				d.Online = false
				slog.Debug("fleet: device unreachable, marking offline", "id", res.id)
			}
			r.mu.Unlock()
		}
	}
}

// normalizeID converts an mDNS instance name to a lowercase device ID.
// It strips the _llm._tcp suffix, unescapes dns-sd dot escaping (\. → .),
// and removes the .local suffix common in macOS Bonjour hostnames.
func normalizeID(name string) string {
	if idx := strings.Index(name, "._llm._tcp"); idx > 0 {
		name = name[:idx]
	}
	name = strings.ReplaceAll(name, `\.`, ".")
	name = strings.TrimSuffix(name, ".local")
	return strings.ToLower(name)
}

func extractInstanceName(name string) string {
	if idx := strings.Index(name, "._llm._tcp"); idx > 0 {
		name = name[:idx]
	}
	name = strings.ReplaceAll(name, `\.`, ".")
	return strings.TrimSuffix(name, ".local")
}
