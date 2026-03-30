package cli

import (
	"testing"

	"github.com/jguan/aima/internal/mcp"
)

func TestIsLoopbackListenAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "ipv4 loopback", addr: "127.0.0.1:6188", want: true},
		{name: "localhost", addr: "localhost:6188", want: true},
		{name: "ipv6 loopback", addr: "[::1]:6188", want: true},
		{name: "all interfaces shorthand", addr: ":6188", want: false},
		{name: "all interfaces ipv4", addr: "0.0.0.0:6188", want: false},
		{name: "lan address", addr: "192.168.1.2:6188", want: false},
		{name: "invalid", addr: "bad-addr", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLoopbackListenAddr(tt.addr); got != tt.want {
				t.Fatalf("isLoopbackListenAddr(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestValidateServeSecurity(t *testing.T) {
	tests := []struct {
		name          string
		addr          string
		mcpAddr       string
		mcpEnabled    bool
		apiKey        string
		allowInsecure bool
		wantErr       bool
	}{
		{
			name:       "loopback without key allowed",
			addr:       "127.0.0.1:6188",
			mcpAddr:    "127.0.0.1:9090",
			mcpEnabled: false,
			wantErr:    false,
		},
		{
			name:       "wildcard without key rejected",
			addr:       ":6188",
			mcpAddr:    "127.0.0.1:9090",
			mcpEnabled: false,
			wantErr:    true,
		},
		{
			name:       "wildcard with key allowed",
			addr:       ":6188",
			mcpAddr:    "127.0.0.1:9090",
			mcpEnabled: false,
			apiKey:     "secret",
			wantErr:    false,
		},
		{
			name:          "wildcard without key allowed by flag",
			addr:          ":6188",
			mcpAddr:       "127.0.0.1:9090",
			mcpEnabled:    false,
			allowInsecure: true,
			wantErr:       false,
		},
		{
			name:       "mcp non-loopback without key rejected",
			addr:       "127.0.0.1:6188",
			mcpAddr:    "0.0.0.0:9090",
			mcpEnabled: true,
			wantErr:    true,
		},
		{
			name:       "mcp non-loopback with key allowed",
			addr:       "127.0.0.1:6188",
			mcpAddr:    "0.0.0.0:9090",
			mcpEnabled: true,
			apiKey:     "secret",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServeSecurity(tt.addr, tt.mcpAddr, tt.mcpEnabled, tt.apiKey, tt.allowInsecure)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateServeSecurity() error = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveMCPProfile(t *testing.T) {
	tests := []struct {
		name       string
		mcpEnabled bool
		profile    string
		want       mcp.Profile
		wantErr    bool
	}{
		{name: "empty profile", mcpEnabled: false, profile: "", want: mcp.ProfileFull},
		{name: "valid profile", mcpEnabled: true, profile: "operator", want: mcp.ProfileOperator},
		{name: "requires mcp", mcpEnabled: false, profile: "operator", wantErr: true},
		{name: "invalid profile", mcpEnabled: true, profile: "bad", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMCPProfile(tt.mcpEnabled, tt.profile)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveMCPProfile() error = %v, wantErr=%v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("resolveMCPProfile() = %q, want %q", got, tt.want)
			}
		})
	}
}
