package proxy

import (
	"fmt"
	"net"
	"os"
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
	server *mdns.Server
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
	if a.server == nil {
		return nil
	}
	return a.server.Shutdown()
}
