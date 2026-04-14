package main

import (
	"encoding/json"
	"testing"
)

func TestParseOnboardingCentralSyncCounts(t *testing.T) {
	tests := []struct {
		name      string
		raw       json.RawMessage
		wantCfg   int
		wantBench int
	}{
		{
			name:      "nested sync payload",
			raw:       json.RawMessage(`{"knowledge_import":{"imported":{"configurations":3,"benchmark_results":5}}}`),
			wantCfg:   3,
			wantBench: 5,
		},
		{
			name:      "legacy flat payload",
			raw:       json.RawMessage(`{"configurations_imported":2,"benchmarks_imported":4}`),
			wantCfg:   2,
			wantBench: 4,
		},
		{
			name:      "unknown payload",
			raw:       json.RawMessage(`{"status":"pulled"}`),
			wantCfg:   0,
			wantBench: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCfg, gotBench := parseOnboardingCentralSyncCounts(tt.raw)
			if gotCfg != tt.wantCfg || gotBench != tt.wantBench {
				t.Fatalf("parseOnboardingCentralSyncCounts() = (%d, %d), want (%d, %d)", gotCfg, gotBench, tt.wantCfg, tt.wantBench)
			}
		})
	}
}
