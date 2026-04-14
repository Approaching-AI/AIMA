package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jguan/aima/internal/mcp"
)

// sseWrite sends a single SSE event with a raw data string.
func sseWrite(w http.ResponseWriter, f http.Flusher, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	f.Flush()
}

// sseJSON sends a single SSE event with a JSON-marshaled payload.
func sseJSON(w http.ResponseWriter, f http.Flusher, event string, v any) {
	b, _ := json.Marshal(v)
	sseWrite(w, f, event, string(b))
}

// scanEngineResult holds the parsed output of a single discovered engine.
type scanEngineResult struct {
	Type        string `json:"type"`
	Image       string `json:"image,omitempty"`
	RuntimeType string `json:"runtime"`
}

// scanModelResult holds the parsed output of a single discovered model.
type scanModelResult struct {
	Name      string `json:"name"`
	Format    string `json:"format,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

const onboardingCentralSyncTimeout = 2 * time.Second

func parseOnboardingCentralSyncCounts(raw json.RawMessage) (int, int) {
	var nested struct {
		KnowledgeImport struct {
			Imported struct {
				Configurations   int `json:"configurations"`
				BenchmarkResults int `json:"benchmark_results"`
			} `json:"imported"`
		} `json:"knowledge_import"`
	}
	if err := json.Unmarshal(raw, &nested); err == nil {
		if nested.KnowledgeImport.Imported.Configurations != 0 || nested.KnowledgeImport.Imported.BenchmarkResults != 0 {
			return nested.KnowledgeImport.Imported.Configurations, nested.KnowledgeImport.Imported.BenchmarkResults
		}
	}

	var legacy struct {
		Configurations int `json:"configurations_imported"`
		Benchmarks     int `json:"benchmarks_imported"`
	}
	if err := json.Unmarshal(raw, &legacy); err == nil {
		return legacy.Configurations, legacy.Benchmarks
	}
	return 0, 0
}

// handleOnboardingScan returns an HTTP handler that triggers engine scan + model scan +
// Central sync in parallel, streaming results to the client via SSE.
func handleOnboardingScan(ac *appContext, deps *mcp.ToolDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireOnboardingMutation(ac, w, r) {
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ctx := r.Context()

		// Mutex protects writes to the ResponseWriter; SSE requires serialized writes.
		var mu sync.Mutex
		send := func(event string, v any) {
			mu.Lock()
			defer mu.Unlock()
			sseJSON(w, flusher, event, v)
		}

		type engineCollected struct {
			engines []scanEngineResult
			err     error
		}
		type modelCollected struct {
			models []scanModelResult
			err    error
		}
		type centralCollected struct {
			connected       bool
			configsPulled   int
			benchmarkPulled int
			err             error
		}

		engineCh := make(chan engineCollected, 1)
		modelCh := make(chan modelCollected, 1)
		centralCh := make(chan centralCollected, 1)

		// Engine scan goroutine
		go func() {
			send("scan_start", map[string]string{"phase": "engines"})
			var result engineCollected
			if deps.ScanEngines == nil {
				result.err = fmt.Errorf("engine scan not available")
				engineCh <- result
				return
			}
			raw, err := deps.ScanEngines(ctx, "auto", false)
			if err != nil {
				result.err = err
				engineCh <- result
				return
			}
			// Parse the JSON array of scanned engines
			var engines []struct {
				Type        string `json:"type"`
				Image       string `json:"image"`
				RuntimeType string `json:"runtime_type"`
			}
			if err := json.Unmarshal(raw, &engines); err != nil {
				result.err = fmt.Errorf("parse engine scan result: %w", err)
				engineCh <- result
				return
			}
			for _, e := range engines {
				entry := scanEngineResult{
					Type:        e.Type,
					Image:       e.Image,
					RuntimeType: e.RuntimeType,
				}
				result.engines = append(result.engines, entry)
				send("engine_found", entry)
			}
			send("scan_progress", map[string]any{
				"phase":  "engines",
				"status": "complete",
				"count":  len(result.engines),
			})
			engineCh <- result
		}()

		// Model scan goroutine
		go func() {
			send("scan_start", map[string]string{"phase": "models"})
			var result modelCollected
			if deps.ScanModels == nil {
				result.err = fmt.Errorf("model scan not available")
				modelCh <- result
				return
			}
			raw, err := deps.ScanModels(ctx)
			if err != nil {
				result.err = err
				modelCh <- result
				return
			}
			var models []struct {
				Name      string `json:"name"`
				Format    string `json:"format"`
				SizeBytes int64  `json:"size_bytes"`
			}
			if err := json.Unmarshal(raw, &models); err != nil {
				result.err = fmt.Errorf("parse model scan result: %w", err)
				modelCh <- result
				return
			}
			for _, m := range models {
				entry := scanModelResult{
					Name:      m.Name,
					Format:    m.Format,
					SizeBytes: m.SizeBytes,
				}
				result.models = append(result.models, entry)
				send("model_found", entry)
			}
			send("scan_progress", map[string]any{
				"phase":  "models",
				"status": "complete",
				"count":  len(result.models),
			})
			modelCh <- result
		}()

		// Central sync goroutine (non-fatal — offline is OK)
		go func() {
			send("scan_start", map[string]string{"phase": "central_sync"})
			var result centralCollected
			if deps.SyncPull == nil {
				result.err = fmt.Errorf("central sync not available")
				centralCh <- result
				return
			}
			syncCtx, cancel := context.WithTimeout(ctx, onboardingCentralSyncTimeout)
			defer cancel()
			raw, err := deps.SyncPull(syncCtx)
			if err != nil {
				result.err = err
				send("central_synced", map[string]any{
					"connected": false,
					"error":     err.Error(),
				})
				centralCh <- result
				return
			}
			result.connected = true
			result.configsPulled, result.benchmarkPulled = parseOnboardingCentralSyncCounts(raw)
			send("central_synced", map[string]any{
				"connected":         true,
				"configs_pulled":    result.configsPulled,
				"benchmarks_pulled": result.benchmarkPulled,
			})
			centralCh <- result
		}()

		// Collect all results (wait for all 3 goroutines)
		var engineCount, modelCount int
		var centralConnected bool

		for i := 0; i < 3; i++ {
			select {
			case <-ctx.Done():
				slog.Info("onboarding scan: client disconnected")
				return
			case er := <-engineCh:
				if er.err != nil {
					slog.Warn("onboarding scan: engine scan failed", "error", er.err)
				}
				engineCount = len(er.engines)
			case mr := <-modelCh:
				if mr.err != nil {
					slog.Warn("onboarding scan: model scan failed", "error", mr.err)
				}
				modelCount = len(mr.models)
			case cr := <-centralCh:
				if cr.err != nil {
					slog.Debug("onboarding scan: central sync failed (offline is OK)", "error", cr.err)
				}
				centralConnected = cr.connected
			}
		}

		send("scan_complete", map[string]any{
			"engines":           engineCount,
			"models":            modelCount,
			"central_connected": centralConnected,
		})
	}
}
