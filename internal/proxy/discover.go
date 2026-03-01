package proxy

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

// DiscoveredService represents an LLM service found via mDNS.
type DiscoveredService struct {
	Name   string   `json:"name"`
	Host   string   `json:"host"`
	AddrV4 string   `json:"addr_v4,omitempty"`
	Port   int      `json:"port"`
	Info   []string `json:"info,omitempty"`
}

// Discover scans for _llm._tcp.local services via mDNS.
// On macOS, uses native dns-sd command because the system mDNSResponder
// owns multicast and hashicorp/mdns queries fail intermittently.
func Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredService, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if runtime.GOOS == "darwin" {
		return discoverDarwin(ctx, timeout)
	}
	return discoverMDNS(ctx, timeout)
}

// discoverMDNS uses hashicorp/mdns for service discovery (Linux, Windows).
// It queries on all LAN interfaces in parallel so devices reachable via
// different NICs (WiFi, Ethernet) are all discovered.
func discoverMDNS(ctx context.Context, timeout time.Duration) ([]DiscoveredService, error) {
	ifaces := lanInterfaces()
	if len(ifaces) == 0 {
		// Fallback: single query on system default interface
		return discoverOnInterface(ctx, timeout, nil)
	}

	type ifaceResult struct {
		services []DiscoveredService
		err      error
	}
	ch := make(chan ifaceResult, len(ifaces))
	for _, iface := range ifaces {
		go func(iface *net.Interface) {
			svcs, err := discoverOnInterface(ctx, timeout, iface)
			ch <- ifaceResult{services: svcs, err: err}
		}(iface)
	}

	// Collect and deduplicate by Name
	seen := make(map[string]bool)
	var services []DiscoveredService
	var firstErr error
	for range ifaces {
		r := <-ch
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		for _, svc := range r.services {
			if !seen[svc.Name] {
				seen[svc.Name] = true
				services = append(services, svc)
			}
		}
	}

	if len(services) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return services, nil
}

// discoverOnInterface runs a single mDNS query on the given interface (nil = system default).
func discoverOnInterface(ctx context.Context, timeout time.Duration, iface *net.Interface) ([]DiscoveredService, error) {
	entriesCh := make(chan *mdns.ServiceEntry, 16)
	var services []DiscoveredService

	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entriesCh {
			if !strings.Contains(entry.Name, "_llm._tcp") {
				continue
			}
			svc := DiscoveredService{
				Name: entry.Name,
				Host: entry.Host,
				Port: entry.Port,
				Info: entry.InfoFields,
			}
			if entry.AddrV4 != nil {
				svc.AddrV4 = entry.AddrV4.String()
			}
			services = append(services, svc)
		}
	}()

	params := mdns.DefaultParams("_llm._tcp")
	params.Entries = entriesCh
	params.Timeout = timeout
	if iface != nil {
		params.Interface = iface
	}

	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := mdns.QueryContext(queryCtx, params); err != nil {
		close(entriesCh)
		<-done
		return nil, fmt.Errorf("mdns query: %w", err)
	}
	close(entriesCh)
	<-done

	return services, nil
}

// discoverDarwin uses macOS native dns-sd commands for service discovery.
// Step 1: dns-sd -B _llm._tcp (browse for instances)
// Step 2: dns-sd -L <instance> _llm._tcp local (resolve host:port per instance)
// Step 3: dns-sd -G v4 <host> (resolve IP per host)
// Steps 2-3 run in parallel across instances.
func discoverDarwin(ctx context.Context, timeout time.Duration) ([]DiscoveredService, error) {
	// Browse for _llm._tcp services
	browseCtx, browseCancel := context.WithTimeout(ctx, timeout)
	defer browseCancel()

	cmd := exec.CommandContext(browseCtx, "dns-sd", "-B", "_llm._tcp")
	out, _ := cmd.CombinedOutput() // killed by context timeout, output collected

	instances := parseDNSSDInstances(string(out))
	if len(instances) == 0 {
		return nil, nil
	}

	// Resolve all instances in parallel
	type result struct {
		svc *DiscoveredService
	}
	ch := make(chan result, len(instances))
	for _, inst := range instances {
		go func(name string) {
			ch <- result{svc: resolveDNSSDInstance(ctx, name)}
		}(inst)
	}

	var services []DiscoveredService
	for range instances {
		if r := <-ch; r.svc != nil {
			services = append(services, *r.svc)
		}
	}
	return services, nil
}

// resolveDNSSDInstance resolves a single service instance to host:port and IP.
func resolveDNSSDInstance(ctx context.Context, name string) *DiscoveredService {
	// dns-sd -L <instance> _llm._tcp local → host, port, TXT
	lCtx, lCancel := context.WithTimeout(ctx, 2*time.Second)
	defer lCancel()

	lCmd := exec.CommandContext(lCtx, "dns-sd", "-L", name, "_llm._tcp", "local")
	lOut, _ := lCmd.CombinedOutput()

	host, port, info := parseDNSSDLookup(string(lOut))
	if host == "" || port == 0 {
		return nil
	}

	// dns-sd -G v4 <host> → IP address
	gCtx, gCancel := context.WithTimeout(ctx, 2*time.Second)
	defer gCancel()

	gCmd := exec.CommandContext(gCtx, "dns-sd", "-G", "v4", host)
	gOut, _ := gCmd.CombinedOutput()

	addr := parseDNSSDGetAddr(string(gOut))
	if addr == "" {
		return nil
	}

	return &DiscoveredService{
		Name:   name + "._llm._tcp.local.",
		Host:   host,
		AddrV4: addr,
		Port:   port,
		Info:   info,
	}
}

// parseDNSSDInstances parses dns-sd -B output for instance names.
// Example line: "18:04:41.895  Add        2  12 local.               _llm._tcp.           Light-Salt"
func parseDNSSDInstances(output string) []string {
	var instances []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "Add") || !strings.Contains(line, "_llm._tcp.") {
			continue
		}
		idx := strings.LastIndex(line, "_llm._tcp.")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[idx+len("_llm._tcp."):])
		if name != "" && !seen[name] {
			seen[name] = true
			instances = append(instances, name)
		}
	}
	return instances
}

// parseDNSSDLookup parses dns-sd -L output for host, port, and TXT records.
// Example: "Light-Salt._llm._tcp.local. can be reached at Light-Salt.local.:6188 (interface 12)\n aima=1"
func parseDNSSDLookup(output string) (host string, port int, info []string) {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if idx := strings.Index(trimmed, "can be reached at "); idx >= 0 {
			rest := trimmed[idx+len("can be reached at "):]
			// rest = "Light-Salt.local.:6188 (interface 12)"
			if spaceIdx := strings.Index(rest, " "); spaceIdx > 0 {
				rest = rest[:spaceIdx]
			}
			h, p, err := net.SplitHostPort(rest)
			if err == nil {
				host = h
				port, _ = strconv.Atoi(p)
			}
		}
		// TXT records appear as key=value pairs
		if host != "" {
			for _, field := range strings.Fields(trimmed) {
				if strings.Contains(field, "=") {
					info = append(info, field)
				}
			}
		}
	}
	return
}

// parseDNSSDGetAddr parses dns-sd -G v4 output for an IPv4 address.
// Example: "18:04:43.100  Add     2 12 Light-Salt.local.   192.168.108.100   120"
func parseDNSSDGetAddr(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "Add") {
			continue
		}
		for _, field := range strings.Fields(trimmed) {
			if ip := net.ParseIP(field); ip != nil && ip.To4() != nil {
				return ip.String()
			}
		}
	}
	return ""
}
