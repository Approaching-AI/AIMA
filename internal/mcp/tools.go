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
		// Full categories
		"hardware.", "model.", "engine.", "deploy.",
		"system.", "scenario.", "fleet.", "discover.",
		"stack.", "catalog.", "openclaw.", "support.", "device.",
		// Selective knowledge tools (skip deep analytics, sync, internals)
		"knowledge.resolve", "knowledge.search", "knowledge.list",
		"knowledge.list_profiles", "knowledge.list_engines", "knowledge.list_models",
		"knowledge.generate_pod", "knowledge.validate",
		"knowledge.export", "knowledge.import",
		// Selective agent tools (skip patrol internals)
		"agent.ask", "agent.guide", "agent.status",
	},
	ProfilePatrol: {
		"hardware.metrics",
		"deploy.list", "deploy.status", "deploy.logs", "deploy.apply", "deploy.approve", "deploy.dry_run",
		"knowledge.resolve",
		"benchmark.run",
		"agent.patrol_status", "agent.alerts", "agent.patrol_config", "agent.patrol_actions",
	},
	ProfileExplorer: {
		"deploy.apply", "deploy.approve", "deploy.dry_run", "deploy.status", "deploy.list", "deploy.logs",
		"benchmark.", "explore.", "tuning.",
		"knowledge.resolve", "knowledge.search_configs", "knowledge.promote",
		"knowledge.save", "knowledge.validate",
		"hardware.detect", "hardware.metrics",
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

// isCommandAllowed checks if a command is in the whitelist.
func isCommandAllowed(command string) bool {
	// allowedExact lists commands that must match exactly (no extra arguments).
	allowedExact := []string{
		"cat /proc/cpuinfo",
	}

	// allowedNoArgs lists commands allowed only without arguments.
	allowedNoArgs := []string{
		"free",
	}

	// allowedWithSafeFlags maps commands to a set of permitted flag prefixes.
	// Only flags starting with one of these prefixes are accepted.
	allowedWithSafeFlags := map[string][]string{
		"nvidia-smi": {
			"-q", "--query", // query modes (--query-gpu, --query-compute-apps, etc.)
			"-L", "--list", // list GPUs
			"--format", // output format (csv, noheader, etc.)
			"--id",     // select GPU by ID
		},
		"df": {
			"-h", "--human", // human-readable
			"-T", "--type", // show filesystem type
			"-a", "--all", // show all filesystems
		},
		"uname": {
			"-a", "-s", "-r", "-m", "-n", "-v", "-p", "-o", // all flags are read-only
		},
	}

	// safeExactFlags maps commands to flags that must match exactly (not as prefix).
	// Use for short flags like "-i" that would otherwise match "-invalid".
	safeExactFlags := map[string]map[string]bool{
		"nvidia-smi": {
			"-i": true, // select GPU by index (read-only)
		},
	}

	// allowedKubectlSubcommands restricts kubectl to read-only operations.
	allowedKubectlSubcommands := map[string]bool{
		"get":      true,
		"describe": true,
		"logs":     true,
		"top":      true,
		"version":  true,
	}

	cmd := strings.TrimSpace(command)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return false
	}

	// kubectl: require subcommand to be in the safe list
	if parts[0] == "kubectl" {
		return len(parts) >= 2 && allowedKubectlSubcommands[parts[1]]
	}

	// Exact multi-word matches (no extra arguments allowed).
	for _, allowed := range allowedExact {
		if cmd == allowed {
			return true
		}
	}

	// Commands allowed without any arguments.
	for _, allowed := range allowedNoArgs {
		if cmd == allowed {
			return true
		}
	}

	// Commands with flag whitelisting: every flag must match a safe prefix or exact flag.
	if safePrefixes, ok := allowedWithSafeFlags[parts[0]]; ok {
		exactFlags := safeExactFlags[parts[0]] // may be nil
		for _, arg := range parts[1:] {
			if exactFlags[arg] {
				continue
			}
			if !hasAnySafePrefix(arg, safePrefixes) {
				return false
			}
		}
		return true
	}

	return false
}

// hasAnySafePrefix reports whether arg starts with any of the given prefixes.
func hasAnySafePrefix(arg string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(arg, p) {
			return true
		}
	}
	return false
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
	registerAgentTools(s, deps)
	registerIntegrationTools(s, deps)
}
