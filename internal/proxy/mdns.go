package proxy

import (
	"bytes"
	"fmt"
	stdlog "log"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/hashicorp/mdns"
)

// mdnsLogFilter drops lines from hashicorp/mdns that correspond to malformed
// packets emitted by other devices on the LAN (e.g. Windows/iOS bonjour quirks
// around the DNS truncated bit). These are routine on shared networks and do
// not indicate any problem with our advertiser.
type mdnsLogFilter struct{}

var mdnsSuppressedSubstrings = [][]byte{
	[]byte("Failed to handle query"),
	[]byte("truncated bit"),
	[]byte("Failed to unpack packet"),
}

func (mdnsLogFilter) Write(p []byte) (int, error) {
	for _, needle := range mdnsSuppressedSubstrings {
		if bytes.Contains(p, needle) {
			return len(p), nil
		}
	}
	// Forward anything else to slog at Debug — we don't want mdns chatter in
	// normal logs, but keep it reachable for troubleshooting.
	slog.Debug("mdns", "line", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func newMDNSLogger() *stdlog.Logger {
	return stdlog.New(mdnsLogFilter{}, "", 0)
}

// MDNSConfig configures the mDNS advertiser.
type MDNSConfig struct {
	Port     int
	Instance string   // defaults to os.Hostname()
	Models   []string // TXT record: models=a,b,c
}

// MDNSAdvertiser wraps running mDNS servers (one per LAN interface).
type MDNSAdvertiser struct {
	servers []*mdns.Server // non-macOS: one per LAN interface
	cmd     *exec.Cmd      // macOS: dns-sd -R subprocess
}

// StartMDNS advertises an _llm._tcp.local service via mDNS.
func StartMDNS(cfg MDNSConfig) (*MDNSAdvertiser, error) {
	hostname := cfg.Instance
	if hostname == "" {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("mdns hostname: %w", err)
		}
		hostname = h
	}

	txt := []string{"aima=1", "api=v1"}
	if len(cfg.Models) > 0 {
		txt = append(txt, "models="+strings.Join(cfg.Models, ","))
	}

	// On macOS, use native dns-sd command because the system mDNSResponder
	// owns port 5353 and hashicorp/mdns server cannot respond to queries.
	if runtime.GOOS == "darwin" {
		return startMDNSDarwin(hostname, cfg.Port, txt)
	}

	ips := lanIPs()
	service, err := mdns.NewMDNSService(hostname, "_llm._tcp", "", "", cfg.Port, ips, txt)
	if err != nil {
		return nil, fmt.Errorf("mdns service: %w", err)
	}

	// Bind to all LAN interfaces so mDNS works across WiFi ↔ Ethernet switches.
	ifaces := lanInterfaces()
	if len(ifaces) == 0 {
		// Fallback: single server on system default interface
		server, err := mdns.NewServer(&mdns.Config{Zone: service, Logger: newMDNSLogger()})
		if err != nil {
			return nil, fmt.Errorf("mdns server: %w", err)
		}
		return &MDNSAdvertiser{servers: []*mdns.Server{server}}, nil
	}

	var servers []*mdns.Server
	for _, iface := range ifaces {
		server, err := mdns.NewServer(&mdns.Config{Zone: service, Iface: iface, Logger: newMDNSLogger()})
		if err != nil {
			slog.Debug("mdns: skip interface for advertise", "iface", iface.Name, "error", err)
			continue
		}
		slog.Debug("mdns: advertising on interface", "iface", iface.Name)
		servers = append(servers, server)
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("mdns: no multicast listeners on any LAN interface")
	}
	return &MDNSAdvertiser{servers: servers}, nil
}

// startMDNSDarwin registers the service via macOS native dns-sd command.
func startMDNSDarwin(instance string, port int, txt []string) (*MDNSAdvertiser, error) {
	args := []string{"-R", instance, "_llm._tcp", "local", strconv.Itoa(port)}
	args = append(args, txt...)
	cmd := exec.Command("dns-sd", args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("dns-sd register: %w", err)
	}
	return &MDNSAdvertiser{cmd: cmd}, nil
}

// isContainerInterface returns true if the interface name matches known
// container/overlay network patterns (Docker, Kubernetes, etc).
func isContainerInterface(name string) bool {
	lower := strings.ToLower(name)
	// Docker: docker0, vethXXX, br-XXX
	// Kubernetes: cni0, flannel.1, cali*, weave
	// libvirt: virbr*
	prefixes := []string{"docker", "veth", "br-", "cni", "flannel", "cali", "weave", "virbr"}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// lanInterfaces returns up, multicast-capable interfaces that have at least
// one private IPv4 address, excluding container/overlay interfaces by name.
func lanInterfaces() []*net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []*net.Interface
	for i := range ifaces {
		iface := &ifaces[i]
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		if isContainerInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() && ip4.IsPrivate() {
				result = append(result, iface)
				break
			}
		}
	}
	return result
}

// lanIPs returns non-loopback, private IPv4 addresses for mDNS advertisement,
// excluding addresses on container/overlay interfaces.
func lanIPs() []net.IP {
	var ips []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isContainerInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() && ip4.IsPrivate() {
				ips = append(ips, ip4)
			}
		}
	}
	return ips
}

// Shutdown stops the mDNS advertiser.
func (a *MDNSAdvertiser) Shutdown() error {
	if a.cmd != nil {
		err := a.cmd.Process.Kill()
		a.cmd.Wait() // reap zombie process
		return err
	}
	var firstErr error
	for _, s := range a.servers {
		if err := s.Shutdown(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
