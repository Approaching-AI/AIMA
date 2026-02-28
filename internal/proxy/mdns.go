package proxy

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/hashicorp/mdns"
)

// MDNSConfig configures the mDNS advertiser.
type MDNSConfig struct {
	Port     int
	Instance string   // defaults to os.Hostname()
	Models   []string // TXT record: models=a,b,c
}

// MDNSAdvertiser wraps a running mDNS server.
type MDNSAdvertiser struct {
	server *mdns.Server // non-macOS: hashicorp/mdns
	cmd    *exec.Cmd    // macOS: dns-sd -R subprocess
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

	txt := []string{"aima=1"}
	if len(cfg.Models) > 0 {
		txt = append(txt, "models="+strings.Join(cfg.Models, ","))
	}

	// On macOS, use native dns-sd command because the system mDNSResponder
	// owns port 5353 and hashicorp/mdns server cannot respond to queries.
	if runtime.GOOS == "darwin" {
		return startMDNSDarwin(hostname, cfg.Port, txt)
	}

	service, err := mdns.NewMDNSService(hostname, "_llm._tcp", "", "", cfg.Port, lanIPs(), txt)
	if err != nil {
		return nil, fmt.Errorf("mdns service: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, fmt.Errorf("mdns server: %w", err)
	}

	return &MDNSAdvertiser{server: server}, nil
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

// lanIPs returns non-loopback, non-virtual IPv4 addresses for mDNS advertisement.
// It skips container/overlay networks (10.x, 172.16-31.x) to advertise the real LAN IP.
func lanIPs() []net.IP {
	var ips []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		// Skip down, loopback, and common virtual interfaces
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
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
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() {
				continue
			}
			// Skip container/overlay networks: 10.0.0.0/8, 172.16.0.0/12
			if ip4[0] == 10 || (ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) {
				continue
			}
			ips = append(ips, ip4)
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
	if a.server != nil {
		return a.server.Shutdown()
	}
	return nil
}
