package proxy

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// DeploymentInfo is a proxy-local struct to avoid importing runtime package.
type DeploymentInfo struct {
	Name    string            `json:"name"`
	Phase   string            `json:"phase"`
	Ready   bool              `json:"ready"`
	Address string            `json:"address"`
	Labels  map[string]string `json:"labels"`
	Runtime string            `json:"runtime"`
}

// SyncBackends reconciles the proxy route table with the current deployment list.
// Ready deployments with an address are registered; disappeared deployments are removed.
func SyncBackends(s *Server, deployments []*DeploymentInfo) {
	// Build set of deployment names for fast lookup
	active := make(map[string]bool, len(deployments))

	for _, d := range deployments {
		model := d.Labels["aima.dev/model"]
		if model == "" {
			model = d.Name
		}
		upstreamModel := model
		if labelModel := strings.TrimSpace(d.Labels[LabelServedModel]); labelModel != "" {
			upstreamModel = labelModel
		}
		active[strings.ToLower(model)] = true

		if d.Ready && d.Address != "" {
			s.RegisterBackend(model, &Backend{
				ModelName:           model,
				UpstreamModel:       upstreamModel,
				EngineType:          d.Labels["aima.dev/engine"],
				Address:             d.Address,
				Ready:               true,
				ContextWindowTokens: contextWindowFromLabels(d.Labels),
			})
			continue
		}

		// Deployment exists but not ready: preserve existing route metadata
		// (address/basePath/remote), but mark it not ready.
		existing := s.ListBackends()
		if b, ok := existing[strings.ToLower(model)]; ok {
			engineType := d.Labels["aima.dev/engine"]
			if engineType == "" {
				engineType = b.EngineType
			}
			if strings.TrimSpace(d.Labels[LabelServedModel]) == "" {
				upstreamModel = backendUpstreamModel(b)
			}
			s.RegisterBackend(model, &Backend{
				ModelName:           model,
				UpstreamModel:       upstreamModel,
				EngineType:          engineType,
				Address:             b.Address,
				BasePath:            b.BasePath,
				Ready:               false,
				Remote:              b.Remote,
				ContextWindowTokens: preserveContextWindow(b.ContextWindowTokens, d.Labels),
			})
		} else {
			s.RegisterBackend(model, &Backend{
				ModelName:           model,
				UpstreamModel:       upstreamModel,
				EngineType:          d.Labels["aima.dev/engine"],
				Ready:               false,
				ContextWindowTokens: contextWindowFromLabels(d.Labels),
			})
		}
	}

	// Remove local backends that no longer have a deployment (skip remote backends)
	for name, b := range s.ListBackends() {
		if !active[strings.ToLower(name)] && !b.Remote {
			slog.Info("sync: removing stale backend", "model", name)
			s.RemoveBackend(name)
		}
	}
}

func contextWindowFromLabels(labels map[string]string) int {
	if len(labels) == 0 {
		return 0
	}
	raw := strings.TrimSpace(labels["aima.dev/context_window"])
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func preserveContextWindow(existing int, labels map[string]string) int {
	if value := contextWindowFromLabels(labels); value > 0 {
		return value
	}
	return existing
}

// StartSyncLoop runs SyncBackends immediately and then every interval until ctx is cancelled.
func StartSyncLoop(ctx context.Context, s *Server, listFn func(ctx context.Context) ([]*DeploymentInfo, error), interval time.Duration) {
	sync := func() {
		deployments, err := listFn(ctx)
		if err != nil {
			slog.Warn("sync: list deployments failed", "error", err)
			return
		}
		SyncBackends(s, deployments)
	}

	// Immediate first sync
	sync()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sync()
		}
	}
}
