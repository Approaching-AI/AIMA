package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Profile controls which tools are visible in tools/list responses.
// Profile only affects discovery (tools/list); tools/call can still invoke any registered tool.
type Profile string

const (
	// ProfileFull exposes all registered tools (default, backward compatible).
	ProfileFull Profile = ""
	// ProfileOperator exposes tools needed by external AI agents for day-to-day operations.
	ProfileOperator Profile = "operator"
	// ProfilePatrol exposes the minimal set used by the internal patrol/healer loop.
	ProfilePatrol Profile = "patrol"
	// ProfileExplorer exposes tools for exploration and tuning agents.
	ProfileExplorer Profile = "explorer"
)

// profileIncludes maps each profile to its include patterns.
// Strings ending with "." are prefix matches; others are exact matches.
var profileIncludes = map[Profile][]string{
	ProfileOperator: {
		"hardware.", "model.", "engine.", "deploy.",
		"system.", "fleet.", "scenario.",
		"catalog.list",
		"benchmark.run", "benchmark.list",
		"knowledge.resolve", "knowledge.search", "knowledge.promote",
		"agent.ask", "agent.status", "agent.rollback",
		"openclaw", "support",
	},
	ProfilePatrol: {
		"hardware.metrics",
		"deploy.list", "deploy.status", "deploy.logs", "deploy.apply",
		"deploy.approve", "deploy.dry_run",
		"knowledge.resolve",
		"benchmark.run",
		"patrol",
	},
	ProfileExplorer: {
		"hardware.detect", "hardware.metrics",
		"deploy.apply", "deploy.approve", "deploy.dry_run", "deploy.status",
		"deploy.list", "deploy.logs", "deploy.delete",
		"benchmark.run", "benchmark.record", "benchmark.list",
		"knowledge.resolve", "knowledge.search", "knowledge.promote", "knowledge.save",
		"explore", "tuning", "explorer",
		"central.advise",
	},
}

// IsValidProfile returns true if p is a recognized profile name.
func IsValidProfile(p Profile) bool {
	switch p {
	case ProfileFull, ProfileOperator, ProfilePatrol, ProfileExplorer:
		return true
	}
	return false
}

// ProfileMatches reports whether the given tool name is included in the profile.
// Returns true for ProfileFull (empty string) — all tools match.
func ProfileMatches(p Profile, toolName string) bool {
	patterns, ok := profileIncludes[p]
	if !ok {
		return true // unknown or empty profile = show all
	}
	for _, pat := range patterns {
		if strings.HasSuffix(pat, ".") {
			if strings.HasPrefix(toolName, pat) {
				return true
			}
		} else if toolName == pat {
			return true
		}
	}
	return false
}

// validConfigKeys is the whitelist for system.config get/set.
var supportedConfigKeys = []string{
	"api_key",
	"llm.endpoint",
	"llm.model",
	"llm.api_key",
	"llm.user_agent",
	"llm.extra_params",
	"central.endpoint",
	"central.api_key",
	"support.enabled",
	"support.endpoint",
	"support.invite_code",
	"support.worker_code",
}

var validConfigKeys = func() map[string]bool {
	m := make(map[string]bool, len(supportedConfigKeys))
	for _, k := range supportedConfigKeys {
		m[k] = true
	}
	return m
}()

var sensitiveConfigKeys = map[string]bool{
	"api_key":             true,
	"llm.api_key":         true,
	"central.api_key":     true,
	"support.invite_code": true,
	"support.worker_code": true,
}

// IsValidConfigKey reports whether key is a recognized configuration key.
func IsValidConfigKey(key string) bool {
	return validConfigKeys[key]
}

// IsSensitiveConfigKey reports whether key should be masked in user-visible output.
func IsSensitiveConfigKey(key string) bool {
	return sensitiveConfigKeys[key]
}

// SupportedConfigKeysString returns the config whitelist in CLI/error-message order.
func SupportedConfigKeysString() string {
	return strings.Join(supportedConfigKeys, ", ")
}

// schema helpers for JSON Schema generation
func noParamsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func schema(properties string, required ...string) json.RawMessage {
	req := "[]"
	if len(required) > 0 {
		parts := make([]string, len(required))
		for i, r := range required {
			parts[i] = `"` + r + `"`
		}
		req = "[" + strings.Join(parts, ",") + "]"
	}
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s}`, properties, req))
}

// RegisterAllTools registers the complete set of MCP tools.
func RegisterAllTools(s *Server, deps *ToolDeps) {
	registerHardwareTools(s, deps)
	registerModelTools(s, deps)
	registerEngineTools(s, deps)
	registerDeployTools(s, deps)
	registerKnowledgeTools(s, deps)
	registerBenchmarkTools(s, deps)
	registerSystemTools(s, deps)
	registerCatalogTools(s, deps)
	registerCentralTools(s, deps)
	registerDataTools(s, deps)
	registerAgentTools(s, deps)
	registerAutomationTools(s, deps)
	registerFleetTools(s, deps)
	registerScenarioTools(s, deps)
	registerOpenClawTools(s, deps)
	registerStackTools(s, deps)
}
