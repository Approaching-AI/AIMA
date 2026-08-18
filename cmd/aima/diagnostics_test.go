package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jguan/aima/internal/mcp"
)

func TestExportDiagnosticsInlineRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	deps := &mcp.ToolDeps{
		DetectHardware: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true,"token":"device-token","nested":{"api_key":"sk-secret"}}`), nil
		},
		GetConfig: func(ctx context.Context, key string) (string, error) {
			switch key {
			case "api_key":
				return "local-secret", nil
			case "llm.endpoint":
				return "http://localhost:6188/v1", nil
			default:
				return "", nil
			}
		},
		DeployList: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`[{"name":"qwen3-4b-llamacpp","model":"qwen3-4b"}]`), nil
		},
		DeployLogs: func(ctx context.Context, name string, tailLines int) (string, error) {
			if tailLines != 5 {
				t.Fatalf("tailLines = %d, want 5", tailLines)
			}
			return "ready\n20 tokens/s\nAuthorization: Bearer sk-secret\napi_key=sk-secret\n{\"token\":\"json-token\",\"access_token\":\"json-access-token\"}\ntoken = spaced-token\n", nil
		},
	}

	raw, err := exportDiagnostics(ctx, &appContext{dataDir: tmp}, deps, json.RawMessage(`{"inline":true,"include_logs":true,"log_lines":5}`))
	if err != nil {
		t.Fatalf("exportDiagnostics: %v", err)
	}
	text := string(raw)
	for _, secret := range []string{"device-token", "sk-secret", "local-secret", "json-token", "json-access-token", "spaced-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostics leaked %q:\n%s", secret, text)
		}
	}
	for _, want := range []string{`"telemetry_free": true`, `"secrets_redacted": true`, `"llm.endpoint": "http://localhost:6188/v1"`, "ready", "20 tokens/s", "[redacted]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, text)
		}
	}
}

func TestDiagnosticsRedactionKeepsPrivacyMarkers(t *testing.T) {
	value := redactValue(map[string]any{
		"secrets_redacted": true,
		"api_key":          "sk-secret",
	}, "").(map[string]any)

	if value["secrets_redacted"] != true {
		t.Fatalf("secrets_redacted = %v, want true", value["secrets_redacted"])
	}
	if value["api_key"] != "***" {
		t.Fatalf("api_key = %v, want ***", value["api_key"])
	}
}

func TestExportDiagnosticsWritesDefaultLocalFile(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	deps := &mcp.ToolDeps{
		DetectHardware: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}

	raw, err := exportDiagnostics(ctx, &appContext{dataDir: tmp}, deps, json.RawMessage(`{"include_logs":false}`))
	if err != nil {
		t.Fatalf("exportDiagnostics: %v", err)
	}
	var summary struct {
		Path          string `json:"path"`
		TelemetryFree bool   `json:"telemetry_free"`
		IncludedLogs  bool   `json:"included_logs"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if !summary.TelemetryFree {
		t.Fatal("summary telemetry_free = false")
	}
	if summary.IncludedLogs {
		t.Fatal("summary included_logs = true, want false")
	}
	if !strings.HasPrefix(summary.Path, tmp) {
		t.Fatalf("path = %q, want under %q", summary.Path, tmp)
	}
	data, err := os.ReadFile(summary.Path)
	if err != nil {
		t.Fatalf("read diagnostics file: %v", err)
	}
	if !strings.Contains(string(data), `"sent_to_network": false`) {
		t.Fatalf("diagnostics file missing local-only privacy marker:\n%s", string(data))
	}
}

func TestBuildDiagnosticsBundleRecordsSectionErrors(t *testing.T) {
	ctx := context.Background()
	deps := &mcp.ToolDeps{
		DetectHardware: func(ctx context.Context) (json.RawMessage, error) {
			return nil, context.Canceled
		},
	}

	bundle := buildDiagnosticsBundle(ctx, &appContext{dataDir: "/tmp/aima"}, deps, time.Unix(0, 0).UTC(), false, 80)
	sections := bundle["sections"].(map[string]any)
	hardware := sections["hardware"].(map[string]any)
	if hardware["error"] != "context canceled" {
		t.Fatalf("hardware error = %v, want context canceled", hardware["error"])
	}
}

func TestServiceContextReportsStaleOverlayHint(t *testing.T) {
	tmp := t.TempDir()
	overlayDir := tmp + string(os.PathSeparator) + "catalog" + string(os.PathSeparator) + "user" + string(os.PathSeparator) + "models"
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	overlayFile := overlayDir + string(os.PathSeparator) + "demo.patch.yaml"
	if err := os.WriteFile(overlayFile, []byte("kind: model_asset_patch\nmetadata:\n  name: demo\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	newer := time.Now().Add(5 * time.Minute)
	if err := os.Chtimes(overlayFile, newer, newer); err != nil {
		t.Fatalf("chtimes overlay: %v", err)
	}

	status := serviceContextStatus(&appContext{
		dataDir:         tmp,
		catalogLoadedAt: time.Now(),
	})
	if status["overlay_newer_than_catalog"] != true {
		t.Fatalf("overlay_newer_than_catalog = %v, want true; status=%v", status["overlay_newer_than_catalog"], status)
	}
	if status["reload_hint"] == "" {
		t.Fatalf("reload_hint missing: %v", status)
	}
}
