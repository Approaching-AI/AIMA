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
	Name                string            `json:"name"`
	Model               string            `json:"model"`
	Engine              string            `json:"engine,omitempty"`
	Phase               string            `json:"phase"`
	Status              string            `json:"status,omitempty"`
	Ready               bool              `json:"ready"`
	Address             string            `json:"address"`
	Runtime             string            `json:"runtime"`
	ServedModel         string            `json:"served_model,omitempty"`
	ParameterCount      string            `json:"parameter_count,omitempty"`
	ContextWindowTokens int               `json:"context_window_tokens,omitempty"`
	Labels              map[string]string `json:"labels,omitempty"`
}

// SyncBackends reconciles the proxy route table with the current deployment list.
// Ready deployments with an address are registered; disappeared deployments are removed.
func SyncBackends(s *Server, deployments []*DeploymentInfo) {
	// Build set of deployment names for fast lookup
	active := make(map[string]bool, len(deployments))

	for _, d := range deployments {
		model := strings.TrimSpace(d.Model)
		if model == "" {
			model = strings.TrimSpace(d.Labels["aima.dev/model"])
		}
		if model == "" {
			model = d.Name
		}
		upstreamModel := strings.TrimSpace(d.ServedModel)
		if upstreamModel == "" {
			if labelModel := strings.TrimSpace(d.Labels[LabelServedModel]); labelModel != "" {
				upstreamModel = labelModel
			}
		}
		if upstreamModel == "" {
			upstreamModel = model
		}
		active[strings.ToLower(model)] = true

		if d.Ready && d.Address != "" {
			s.RegisterBackend(model, &Backend{
				ModelName:           model,
				UpstreamModel:       upstreamModel,
				EngineType:          engineTypeFromDeployment(d),
				Address:             d.Address,
				Ready:               true,
				ParameterCount:      parameterCountFromDeployment(d),
				ContextWindowTokens: contextWindowFromDeployment(d),
			})
			continue
		}

		// Deployment exists but not ready: preserve existing route metadata
		// (address/basePath/remote), but mark it not ready.
		existing := s.ListBackends()
		if b, ok := existing[strings.ToLower(model)]; ok {
			engineType := engineTypeFromDeployment(d)
			if engineType == "" {
				engineType = b.EngineType
			}
			if strings.TrimSpace(d.ServedModel) == "" && strings.TrimSpace(d.Labels[LabelServedModel]) == "" {
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
				ParameterCount:      preserveParameterCount(b.ParameterCount, d),
				ContextWindowTokens: preserveContextWindow(b.ContextWindowTokens, d),
			})
		} else {
			s.RegisterBackend(model, &Backend{
				ModelName:           model,
				UpstreamModel:       upstreamModel,
				EngineType:          engineTypeFromDeployment(d),
				Ready:               false,
				ParameterCount:      parameterCountFromDeployment(d),
				ContextWindowTokens: contextWindowFromDeployment(d),
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

func contextWindowFromDeployment(d *DeploymentInfo) int {
	if d == nil {
		return 0
	}
	if d.ContextWindowTokens > 0 {
		return d.ContextWindowTokens
	}
	return contextWindowFromLabels(d.Labels)
}

func preserveContextWindow(existing int, d *DeploymentInfo) int {
	if value := contextWindowFromDeployment(d); value > 0 {
		return value
	}
	return existing
}

func parameterCountFromLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	return strings.TrimSpace(labels[LabelParameterCount])
}

func parameterCountFromDeployment(d *DeploymentInfo) string {
	if d == nil {
		return ""
	}
	if value := strings.TrimSpace(d.ParameterCount); value != "" {
		return value
	}
	return parameterCountFromLabels(d.Labels)
}

func preserveParameterCount(existing string, d *DeploymentInfo) string {
	if value := parameterCountFromDeployment(d); value != "" {
		return value
	}
	return strings.TrimSpace(existing)
}

func engineTypeFromDeployment(d *DeploymentInfo) string {
	if d == nil {
		return ""
	}
	if value := strings.TrimSpace(d.Engine); value != "" {
		return value
	}
	return strings.TrimSpace(d.Labels["aima.dev/engine"])
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
