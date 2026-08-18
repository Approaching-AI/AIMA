package openclaw

import "testing"

func TestAutoSyncEnabledForMode(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{"", true},
		{"auto", true},
		{"on", true},
		{"manual", false},
		{"off", false},
		{"false", false},
		{"0", false},
		{"no", false},
		{" MANUAL ", false},
	}
	for _, tt := range tests {
		if got := AutoSyncEnabledForMode(tt.mode); got != tt.want {
			t.Fatalf("AutoSyncEnabledForMode(%q) = %v, want %v", tt.mode, got, tt.want)
		}
	}
}
