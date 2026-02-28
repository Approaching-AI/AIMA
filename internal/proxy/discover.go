package proxy

import (
	"context"
	"fmt"
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
func Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredService, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	entriesCh := make(chan *mdns.ServiceEntry, 16)
	var services []DiscoveredService

	// Collect results in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entriesCh {
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
