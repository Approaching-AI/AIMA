package cli

import "testing"

func TestOpenClawAutoSyncEnabled(t *testing.T) {
	tests := []struct {
		name   string
		flag   bool
		env    string
		envSet bool
		want   bool
	}{
		{"default on", false, "", false, true},
		{"flag disables", true, "", false, false},
		{"env manual disables", false, "manual", true, false},
		{"env off disables", false, "off", true, false},
		{"env false disables", false, "false", true, false},
		{"env 0 disables", false, "0", true, false},
		{"env auto stays on", false, "auto", true, true},
		{"env empty stays on", false, "", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envSet {
				t.Setenv("AIMA_OPENCLAW_SYNC", tt.env)
			}
			if got := openClawAutoSyncEnabled(tt.flag); got != tt.want {
				t.Errorf("openClawAutoSyncEnabled(flag=%v, env=%q) = %v, want %v", tt.flag, tt.env, got, tt.want)
			}
		})
	}
}
