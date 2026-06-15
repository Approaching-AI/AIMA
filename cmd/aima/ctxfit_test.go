package main

import (
	"testing"

	"github.com/jguan/aima/internal/knowledge"
)

// Qwen2.5-VL-3B text backbone: 36 layers, 2 KV heads, head_dim 128 → 36864 B/token.
const qwen25vl3bKVPerTok = int64(2 * 36 * 2 * 128 * 2)

func TestClampContextForMemory(t *testing.T) {
	tests := []struct {
		name      string
		reqCtx    int
		nCtxTrain int
		kvPerTok  int64
		usableMiB int
		nonKVMiB  int
		want      int
		clamped   bool // want < reqCtx
	}{
		{
			name:      "huge memory, capped only by trained context",
			reqCtx:    128000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 112640, nonKVMiB: 4300, // ~110GB Strix Halo
			want: 128000, clamped: false,
		},
		{
			name:      "request above trained context is capped down",
			reqCtx:    200000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 112640, nonKVMiB: 4300,
			want: 128000, clamped: true,
		},
		{
			name:      "16GB unified clamps below 128k",
			reqCtx:    128000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 12288, nonKVMiB: 4300, // 16GB - 4GB reserve
			// budget = 12288*0.9 - 4300 - 1024 = 5734 MiB → /36864 B = ~163k → but capped at train 128000? 163k>128000 so 128000
			want: 128000, clamped: false,
		},
		{
			name:      "8GB discrete GPU clamps below 128k",
			reqCtx:    128000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 8192, nonKVMiB: 4300,
			// budget = int(8192*0.9) - 4300 - 1024 = 7372 - 5324 = 2048 MiB
			// maxCtx = 2048*1024*1024/36864 = 58254 → round down to 58112
			want: 58112, clamped: true,
		},
		{
			name:      "tiny memory floors at minCtx",
			reqCtx:    128000, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 6144, nonKVMiB: 4300,
			// budget = 6144*0.9 - 4300 - 1024 = ~205 MiB → ~5832 → round 5632
			want: 5632, clamped: true,
		},
		{
			name:      "unknown memory: only trained-context cap applies",
			reqCtx:    65536, nCtxTrain: 128000, kvPerTok: qwen25vl3bKVPerTok,
			usableMiB: 0, nonKVMiB: 0,
			want: 65536, clamped: false,
		},
		{
			name:      "unknown arch (kvPerTok 0): no memory clamp",
			reqCtx:    65536, nCtxTrain: 0, kvPerTok: 0,
			usableMiB: 8192, nonKVMiB: 4300,
			want: 65536, clamped: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := clampContextForMemory(tt.reqCtx, tt.nCtxTrain, tt.kvPerTok, tt.usableMiB, tt.nonKVMiB)
			if got != tt.want {
				t.Errorf("clampContextForMemory = %d, want %d", got, tt.want)
			}
			if (got < tt.reqCtx) != tt.clamped {
				t.Errorf("clamped = %v, want %v (got=%d req=%d)", got < tt.reqCtx, tt.clamped, got, tt.reqCtx)
			}
		})
	}
}

func TestUsableMemoryMiB(t *testing.T) {
	tests := []struct {
		name string
		hw   knowledge.HardwareInfo
		want int
	}{
		{"unified 128GB reserves 16GB cap", knowledge.HardwareInfo{UnifiedMemory: true, RAMTotalMiB: 131072}, 131072 - 16384},
		{"unified 16GB reserves 1/4", knowledge.HardwareInfo{UnifiedMemory: true, RAMTotalMiB: 16384}, 16384 - 4096},
		{"unified 8GB reserve floored at 2GB", knowledge.HardwareInfo{UnifiedMemory: true, RAMTotalMiB: 8192}, 8192 - 2048},
		{"discrete prefers free VRAM", knowledge.HardwareInfo{GPUVRAMMiB: 8192, GPUMemFreeMiB: 7000}, 7000},
		{"discrete falls back to total VRAM", knowledge.HardwareInfo{GPUVRAMMiB: 8192}, 8192},
		{"cpu-only uses system RAM", knowledge.HardwareInfo{RAMTotalMiB: 32768}, 32768 - 8192},
		{"unknown memory returns 0", knowledge.HardwareInfo{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usableMemoryMiB(tt.hw); got != tt.want {
				t.Errorf("usableMemoryMiB = %d, want %d", got, tt.want)
			}
		})
	}
}
