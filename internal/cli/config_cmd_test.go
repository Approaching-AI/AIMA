package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/mcp"
)

func TestConfigSetMasksSecrets(t *testing.T) {
	tests := []struct {
		key     string
		value   string
		wantOut string // substring expected in stdout
	}{
		{"api_key", "super-secret-123", "api_key = ***"},
		{"llm.api_key", "sk-abcdef", "llm.api_key = ***"},
		{"llm.endpoint", "http://localhost:8080/v1", "llm.endpoint = http://localhost:8080/v1"},
		{"llm.model", "qwen3-8b", "llm.model = qwen3-8b"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			app := testApp(t)
			var stored string
			app.ToolDeps.SetConfig = func(ctx context.Context, key, value string) error {
				stored = value
				return nil
			}

			root := NewRootCmd(app)
			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetArgs([]string{"config", "set", tt.key, tt.value})

			if err := root.Execute(); err != nil {
				t.Fatalf("config set %s: %v", tt.key, err)
			}
			if stored != tt.value {
				t.Errorf("stored = %q, want %q (raw value must reach SetConfig)", stored, tt.value)
			}
			out := buf.String()
			if !strings.Contains(out, tt.wantOut) {
				t.Errorf("stdout = %q, want substring %q", out, tt.wantOut)
			}
			// Verify secret value is NOT in output for sensitive keys
			if strings.Contains(tt.key, "api_key") && strings.Contains(out, tt.value) {
				t.Errorf("stdout contains raw secret %q for key %s", tt.value, tt.key)
			}
		})
	}
}

func TestConfigGetMasksSecrets(t *testing.T) {
	tests := []struct {
		key      string
		dbValue  string
		wantOut  string
		wantHide string // must NOT appear in output
	}{
		{"api_key", "super-secret-123", "***", "super-secret-123"},
		{"llm.api_key", "sk-abcdef", "***", "sk-abcdef"},
		{"llm.endpoint", "http://localhost:8080/v1", "http://localhost:8080/v1", ""},
		{"llm.model", "qwen3-8b", "qwen3-8b", ""},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			app := testApp(t)
			app.ToolDeps.GetConfig = func(ctx context.Context, key string) (string, error) {
				if key == tt.key {
					return tt.dbValue, nil
				}
				return "", fmt.Errorf("not found")
			}

			root := NewRootCmd(app)
			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetArgs([]string{"config", "get", tt.key})

			if err := root.Execute(); err != nil {
				t.Fatalf("config get %s: %v", tt.key, err)
			}
			out := strings.TrimSpace(buf.String())
			if out != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out, tt.wantOut)
			}
			if tt.wantHide != "" && strings.Contains(buf.String(), tt.wantHide) {
				t.Errorf("stdout contains raw secret %q for key %s", tt.wantHide, tt.key)
			}
		})
	}
}

func TestConfigSubcommands(t *testing.T) {
	app := testApp(t)
	app.ToolDeps.GetConfig = func(ctx context.Context, key string) (string, error) {
		return "", nil
	}
	app.ToolDeps.SetConfig = func(ctx context.Context, key, value string) error {
		return nil
	}

	root := NewRootCmd(app)
	var configCmd *mcp.Tool
	_ = configCmd // just checking registration

	// Verify config command exists with get/set subcommands
	for _, c := range root.Commands() {
		if c.Name() == "config" {
			subs := make(map[string]bool)
			for _, sub := range c.Commands() {
				subs[sub.Name()] = true
			}
			if !subs["get"] {
				t.Error("config missing 'get' subcommand")
			}
			if !subs["set"] {
				t.Error("config missing 'set' subcommand")
			}
			return
		}
	}
	t.Error("'config' command not found in root")
}
