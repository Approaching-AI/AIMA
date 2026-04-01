package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/knowledge"
)

// handlePowerSnapshot returns a JSON snapshot of current power/GPU metrics.
func handlePowerSnapshot(cat *knowledge.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx := r.Context()
		resp := map[string]any{"timestamp": time.Now().UTC()}

		metrics, err := hal.CollectMetrics(ctx)
		if err != nil || metrics == nil || metrics.GPU == nil {
			resp["available"] = false
		} else {
			resp["available"] = true
			resp["gpu"] = map[string]any{
				"power_draw_watts": metrics.GPU.PowerDrawWatts,
				"temperature_c":    metrics.GPU.TemperatureCelsius,
				"utilization_pct":  metrics.GPU.UtilizationPercent,
				"memory_used_mib":  metrics.GPU.MemoryUsedMiB,
				"memory_total_mib": metrics.GPU.MemoryTotalMiB,
			}
		}

		// Add TDP from hardware profile for context
		if hw, hwErr := hal.Detect(ctx); hwErr == nil && hw.GPU != nil {
			tdp := cat.FindHardwareTDP(knowledge.HardwareInfo{GPUArch: hw.GPU.Arch})
			if tdp > 0 {
				resp["tdp_watts"] = tdp
				if metrics != nil && metrics.GPU != nil && metrics.GPU.PowerDrawWatts > 0 {
					resp["power_utilization_pct"] = metrics.GPU.PowerDrawWatts / float64(tdp) * 100
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
