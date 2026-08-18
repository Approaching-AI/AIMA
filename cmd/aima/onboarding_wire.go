package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jguan/aima/catalog"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/onboarding"
)

// buildOnboardingDepsStruct composes an *onboarding.Deps from the cmd/aima
// appContext and MCP ToolDeps. The `BuildHardwareInfo` and `DetectHWProfile`
// closures are how we expose cmd/aima's package-private helpers to the new
// internal/onboarding package (which cannot import cmd/aima).
func buildOnboardingDepsStruct(ac *appContext, deps *mcp.ToolDeps) *onboarding.Deps {
	obDeps := &onboarding.Deps{
		ToolDeps:       deps,
		FirstRunPolicy: loadOnboardingFirstRunPolicy(),
	}
	if ac != nil {
		obDeps.Cat = ac.cat
		obDeps.DB = ac.db
		obDeps.KStore = ac.kStore

		rtName := ""
		if ac.rt != nil {
			rtName = ac.rt.Name()
		}
		cat := ac.cat

		obDeps.BuildHardwareInfo = func(ctx context.Context) knowledge.HardwareInfo {
			return buildHardwareInfo(ctx, cat, rtName)
		}
		obDeps.DetectHWProfile = func(ctx context.Context) string {
			if cat == nil {
				return ""
			}
			return detectHWProfile(ctx, cat)
		}
		if ac.proxy != nil {
			obDeps.ListRunningServices = func(ctx context.Context) []onboarding.RunningService {
				_ = ctx
				backends := ac.proxy.ListBackends()
				services := make([]onboarding.RunningService, 0, len(backends))
				for key, b := range backends {
					if b == nil {
						continue
					}
					model := strings.TrimSpace(b.ModelName)
					if model == "" {
						model = key
					}
					status := "not_ready"
					if b.Ready {
						status = "ready"
					}
					source := "proxy_backend"
					if b.Remote {
						source = "remote"
					}
					if b.External {
						source = "external"
					}
					services = append(services, onboarding.RunningService{
						Name:                model,
						Model:               model,
						UpstreamModel:       b.UpstreamModel,
						Engine:              b.EngineType,
						Endpoint:            defaultLLMEndpoint(),
						BackendEndpoint:     proxyBackendEndpoint(b.Address, b.BasePath),
						Source:              source,
						Status:              status,
						Ready:               b.Ready,
						ParameterCount:      b.ParameterCount,
						ContextWindowTokens: b.ContextWindowTokens,
					})
				}
				return services
			}
		}
	}
	return obDeps
}

func proxyBackendEndpoint(address, basePath string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	basePath = strings.TrimSpace(basePath)
	if basePath == "" || basePath == "/" {
		return strings.TrimRight(address, "/")
	}
	return strings.TrimRight(address, "/") + "/" + strings.TrimLeft(strings.TrimRight(basePath, "/"), "/")
}

func loadOnboardingFirstRunPolicy() *onboarding.FirstRunPolicy {
	raw, err := catalog.FS.ReadFile("onboarding-policy.yaml")
	if err != nil {
		panic(fmt.Errorf("load onboarding policy: %w", err))
	}
	policy, err := onboarding.ParseFirstRunPolicyYAML(raw)
	if err != nil {
		panic(fmt.Errorf("parse onboarding policy: %w", err))
	}
	return policy
}
