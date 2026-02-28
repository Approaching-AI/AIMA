package proxy

import (
	"fmt"
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

	service, err := mdns.NewMDNSService(hostname, "_llm._tcp", "", "", cfg.Port, nil, txt)
	if err != nil {
		return nil, fmt.Errorf("mdns service: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, fmt.Errorf("mdns server: %w", err)
	}

	return &MDNSAdvertiser{server: server}, nil
}

// Shutdown stops the mDNS advertiser.
func (a *MDNSAdvertiser) Shutdown() error {
	if a.server == nil {
		return nil
	}
	return a.server.Shutdown()
}
