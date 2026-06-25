package main

import (
	"context"
	"fmt"
	"testing"
)

type testOpenClawSyncConfig struct {
	value string
	found bool
}

func (s testOpenClawSyncConfig) GetConfig(context.Context, string) (string, error) {
	if !s.found {
		return "", fmt.Errorf("not found")
	}
	return s.value, nil
}

func TestOpenClawImplicitSyncAllowed(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		store testOpenClawSyncConfig
		want  bool
	}{
		{name: "default on", want: true},
		{name: "env manual disables", env: "manual", want: false},
		{name: "config manual disables", store: testOpenClawSyncConfig{value: "manual", found: true}, want: false},
		{name: "config auto enables", store: testOpenClawSyncConfig{value: "auto", found: true}, want: true},
		{name: "missing config keeps default", store: testOpenClawSyncConfig{found: false}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AIMA_OPENCLAW_SYNC", tt.env)
			if got := openClawImplicitSyncAllowed(context.Background(), tt.store); got != tt.want {
				t.Fatalf("openClawImplicitSyncAllowed = %v, want %v", got, tt.want)
			}
		})
	}
}
