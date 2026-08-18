package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/jguan/aima/internal/buildinfo"
	"github.com/jguan/aima/internal/mcp"
)

type diagnosticsExportParams struct {
	OutputPath  string `json:"output_path"`
	Inline      bool   `json:"inline"`
	IncludeLogs *bool  `json:"include_logs"`
	LogLines    int    `json:"log_lines"`
}

var sensitiveLogAssignmentRE = regexp.MustCompile(`(?i)(^|[^a-z0-9_])"?(authorization|api[_-]?key|apikey|password|secret|invite_code|worker_code|recovery_code|[a-z0-9]+_token|token)"?\s*[:=]`)

func exportDiagnostics(ctx context.Context, ac *appContext, deps *mcp.ToolDeps, rawParams json.RawMessage) (json.RawMessage, error) {
	params := diagnosticsExportParams{LogLines: 80}
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, fmt.Errorf("parse diagnostics params: %w", err)
		}
	}
	if params.LogLines <= 0 {
		params.LogLines = 80
	}
	includeLogs := true
	if params.IncludeLogs != nil {
		includeLogs = *params.IncludeLogs
	}

	generatedAt := time.Now().UTC()
	bundle := buildDiagnosticsBundle(ctx, ac, deps, generatedAt, includeLogs, params.LogLines)
	bundleJSON, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal diagnostics bundle: %w", err)
	}

	if params.Inline || strings.TrimSpace(params.OutputPath) == "-" {
		return bundleJSON, nil
	}

	outputPath := strings.TrimSpace(params.OutputPath)
	if outputPath == "" {
		outputPath = defaultDiagnosticsOutputPath(ac, generatedAt)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return nil, fmt.Errorf("create diagnostics dir: %w", err)
	}
	if err := os.WriteFile(outputPath, bundleJSON, 0o600); err != nil {
		return nil, fmt.Errorf("write diagnostics: %w", err)
	}

	summary := map[string]any{
		"path":             outputPath,
		"bytes":            len(bundleJSON),
		"telemetry_free":   true,
		"secrets_redacted": true,
		"included_logs":    includeLogs,
		"log_lines":        params.LogLines,
	}
	return json.Marshal(summary)
}

func buildDiagnosticsBundle(ctx context.Context, ac *appContext, deps *mcp.ToolDeps, generatedAt time.Time, includeLogs bool, logLines int) map[string]any {
	dataDir := ""
	if ac != nil {
		dataDir = ac.dataDir
	}
	bundle := map[string]any{
		"schema_version": 1,
		"generated_at":   generatedAt.Format(time.RFC3339),
		"aima_version":   buildinfo.Version,
		"privacy": map[string]any{
			"telemetry_free":   true,
			"sent_to_network":  false,
			"secrets_redacted": true,
			"collection_scope": "local_only_no_health_probes",
		},
		"host": map[string]any{
			"goos":     goruntime.GOOS,
			"goarch":   goruntime.GOARCH,
			"data_dir": redactHomePath(dataDir),
		},
		"service_context": serviceContextStatus(ac),
		"sections":        map[string]any{},
	}

	sections := bundle["sections"].(map[string]any)
	if deps == nil {
		sections["error"] = "tool dependencies unavailable"
		return bundle
	}

	if deps.DetectHardware != nil {
		sections["hardware"] = diagnosticsRawSection(deps.DetectHardware(ctx))
	}
	if deps.CollectMetrics != nil {
		sections["metrics"] = diagnosticsRawSection(deps.CollectMetrics(ctx))
	}
	if deps.StackStatus != nil {
		sections["stack_status"] = diagnosticsRawSection(deps.StackStatus(ctx))
	}
	if deps.CatalogStatus != nil {
		sections["catalog_status"] = diagnosticsRawSection(deps.CatalogStatus(ctx))
	}
	if deps.ListKnowledgeSummary != nil {
		sections["knowledge_summary"] = diagnosticsRawSection(deps.ListKnowledgeSummary(ctx))
	}
	if deps.GetConfig != nil {
		sections["config"] = diagnosticsConfigSection(ctx, deps.GetConfig)
	}
	if deps.DeployList != nil {
		deployRaw, deployErr := deps.DeployList(ctx)
		sections["deployments"] = diagnosticsRawSection(deployRaw, deployErr)
		if includeLogs && deployErr == nil && deps.DeployLogs != nil {
			sections["deployment_logs"] = diagnosticsDeploymentLogs(ctx, deployRaw, deps.DeployLogs, logLines)
		}
	}
	sections["omitted_sections"] = []string{
		"system.status",
		"agent.status",
		"onboarding.status",
	}

	return redactValue(bundle, "").(map[string]any)
}

func defaultDiagnosticsOutputPath(ac *appContext, generatedAt time.Time) string {
	dataDir := "."
	if ac != nil && strings.TrimSpace(ac.dataDir) != "" {
		dataDir = ac.dataDir
	}
	name := "aima-diagnostics-" + generatedAt.Format("20060102-150405") + ".json"
	return filepath.Join(dataDir, "diagnostics", name)
}

func diagnosticsRawSection(raw json.RawMessage, err error) any {
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{
			"parse_error": err.Error(),
			"raw":         redactString(string(raw)),
		}
	}
	return redactValue(value, "")
}

func diagnosticsConfigSection(ctx context.Context, get func(context.Context, string) (string, error)) map[string]any {
	result := map[string]any{}
	for _, key := range strings.Split(mcp.SupportedConfigKeysString(), ", ") {
		if key == "" {
			continue
		}
		value, err := get(ctx, key)
		if err != nil || value == "" {
			continue
		}
		if mcp.IsSensitiveConfigKey(key) {
			result[key] = "***"
			continue
		}
		result[key] = redactString(value)
	}
	return result
}

type diagnosticsDeployLogsFunc func(ctx context.Context, name string, tailLines int) (string, error)

func diagnosticsDeploymentLogs(ctx context.Context, deployRaw json.RawMessage, logs diagnosticsDeployLogsFunc, logLines int) []map[string]any {
	names := diagnosticsDeploymentNames(deployRaw)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		entry := map[string]any{"name": name}
		text, err := logs(ctx, name, logLines)
		if err != nil {
			entry["error"] = err.Error()
		} else {
			entry["tail"] = redactString(text)
		}
		out = append(out, entry)
	}
	return out
}

func diagnosticsDeploymentNames(raw json.RawMessage) []string {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	names := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		for _, key := range []string{"name", "deployment", "id"} {
			name, _ := item[key].(string)
			name = strings.TrimSpace(name)
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
				break
			}
		}
	}
	return names
}

func redactValue(value any, key string) any {
	if shouldRedactKey(key) {
		return "***"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = redactValue(v, k)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = redactValue(v, key)
		}
		return out
	case string:
		return redactString(typed)
	default:
		return value
	}
}

func shouldRedactKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	if strings.HasSuffix(k, "_redacted") || k == "redacted" {
		return false
	}
	if k == "token" || strings.HasSuffix(k, "_token") || strings.HasSuffix(k, ".token") {
		return true
	}
	for _, marker := range []string{
		"api_key",
		"apikey",
		"authorization",
		"password",
		"secret",
		"invite_code",
		"worker_code",
		"recovery_code",
		"bearer",
	} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return false
}

func redactString(value string) string {
	if value == "" {
		return value
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if shouldRedactLine(line) {
			lines[i] = "[redacted]"
			continue
		}
		lines[i] = redactHomePathFragments(line)
	}
	return strings.Join(lines, "\n")
}

func shouldRedactLine(line string) bool {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "authorization:") || strings.Contains(lower, "bearer ") {
		return true
	}
	if sensitiveLogAssignmentRE.MatchString(line) {
		return true
	}
	for _, marker := range []string{
		"api_key",
		"apikey",
		"password",
		"secret",
		"invite_code",
		"worker_code",
		"recovery_code",
	} {
		if strings.Contains(lower, marker) && (strings.Contains(lower, ":") || strings.Contains(lower, "=")) {
			return true
		}
	}
	return false
}

func redactHomePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	prefix := home + string(os.PathSeparator)
	if strings.HasPrefix(path, prefix) {
		return "~" + string(os.PathSeparator) + strings.TrimPrefix(path, prefix)
	}
	return path
}

func redactHomePathFragments(value string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return value
	}
	return strings.ReplaceAll(value, home, "~")
}
