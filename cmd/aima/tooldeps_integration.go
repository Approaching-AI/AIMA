package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"

	state "github.com/jguan/aima/internal"
)

// buildIntegrationDeps wires scenario, sync, openclaw questions,
// engine switch cost, and validation tools.
func buildIntegrationDeps(ac *appContext, deps *mcp.ToolDeps) {
	cat := ac.cat
	db := ac.db

	deps.ScenarioList = func(ctx context.Context) (json.RawMessage, error) {
		type entry struct {
			Name            string   `json:"name"`
			Description     string   `json:"description"`
			Target          string   `json:"target"`
			Deployments     int      `json:"deployments"`
			Modalities      []string `json:"modalities"`
			HasAlternatives bool     `json:"has_alternatives"`
			Verified        bool     `json:"verified"`
			VerifiedDate    string   `json:"verified_date,omitempty"`
		}
		var list []entry
		for _, ds := range cat.DeploymentScenarios {
			// Collect unique modalities across all deployments
			seen := make(map[string]bool)
			var mods []string
			for _, d := range ds.Deployments {
				for _, m := range d.Modalities {
					if !seen[m] {
						seen[m] = true
						mods = append(mods, m)
					}
				}
			}
			e := entry{
				Name:            ds.Metadata.Name,
				Description:     ds.Metadata.Description,
				Target:          ds.Target.HardwareProfile,
				Deployments:     len(ds.Deployments),
				Modalities:      mods,
				HasAlternatives: len(ds.AlternativeConfigs) > 0,
			}
			if ds.Verified != nil {
				e.Verified = true
				e.VerifiedDate = ds.Verified.Date
			}
			list = append(list, e)
		}
		return json.Marshal(list)
	}

	deps.ScenarioShow = func(ctx context.Context, name string) (json.RawMessage, error) {
		for i := range cat.DeploymentScenarios {
			if cat.DeploymentScenarios[i].Metadata.Name == name {
				ds := &cat.DeploymentScenarios[i]
				return json.Marshal(map[string]any{
					"name":                ds.Metadata.Name,
					"description":         ds.Metadata.Description,
					"target":              ds.Target,
					"deployments":         ds.Deployments,
					"post_deploy":         ds.PostDeploy,
					"integrations":        ds.Integrations,
					"verified":            ds.Verified,
					"open_questions":      ds.OpenQuestions,
					"memory_budget":       ds.MemoryBudget,
					"startup_order":       ds.StartupOrder,
					"alternative_configs": ds.AlternativeConfigs,
				})
			}
		}
		names := make([]string, 0, len(cat.DeploymentScenarios))
		for _, ds := range cat.DeploymentScenarios {
			names = append(names, ds.Metadata.Name)
		}
		return nil, fmt.Errorf("scenario %q not found (available: %v)", name, names)
	}

	deps.ScenarioApply = func(ctx context.Context, name string, dryRun bool) (json.RawMessage, error) {
		return applyScenario(ctx, cat, ac.rt.Name(), deps, name, dryRun)
	}


	// Knowledge sync (K6)
	syncHTTPClient := &http.Client{Timeout: 30 * time.Second}
	deps.SyncPush = func(ctx context.Context) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured — use system.config set central.endpoint <url>")
		}
		// Export local knowledge
		exportData, err := deps.ExportKnowledge(ctx, json.RawMessage(`{}`))
		if err != nil {
			return nil, fmt.Errorf("export failed: %w", err)
		}
		// Transform export envelope to central's IngestPayload format.
		var exportEnvelope struct {
			Stats map[string]int `json:"stats"`
			Data  struct {
				Configurations   []json.RawMessage `json:"configurations"`
				BenchmarkResults []json.RawMessage `json:"benchmark_results"`
				KnowledgeNotes   []json.RawMessage `json:"knowledge_notes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(exportData, &exportEnvelope); err != nil {
			return nil, fmt.Errorf("parse export data: %w", err)
		}

		deviceID, _ := deps.GetConfig(ctx, "device.id")
		hwTarget := edgeHardwareTarget(ctx, ac)

		ingestPayload, err := json.Marshal(map[string]any{
			"schema_version":  1,
			"device_id":       deviceID,
			"gpu_arch":        hwTarget.GPUArch,
			"configurations":  exportEnvelope.Data.Configurations,
			"benchmarks":      exportEnvelope.Data.BenchmarkResults,
			"knowledge_notes": exportEnvelope.Data.KnowledgeNotes,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal ingest payload: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/v1/ingest",
			strings.NewReader(string(ingestPayload)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("push to central: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d: %s", resp.StatusCode, string(body))
		}
		_ = db.SetSyncTimestamp(ctx, "push")
		return json.Marshal(map[string]any{
			"status":           "pushed",
			"protocol_version": "v2-edge",
			"endpoint":         endpoint,
			"device_id":        deviceID,
			"hardware_profile": hwTarget.HardwareProfile,
			"gpu_arch":         hwTarget.GPUArch,
			"export_stats":     exportEnvelope.Stats,
			"ingest_result":    json.RawMessage(body),
		})
	}

	deps.SyncPull = func(ctx context.Context) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured — use system.config set central.endpoint <url>")
		}
		since, _ := db.GetSyncTimestamp(ctx, "pull")
		syncURL := endpoint + "/api/v1/sync"
		if since != "" {
			syncURL += "?since=" + since
		}
		req, err := http.NewRequestWithContext(ctx, "GET", syncURL, nil)
		if err != nil {
			return nil, err
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("pull from central: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d", resp.StatusCode)
		}

		syncData, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50 MiB max
		if err != nil {
			return nil, fmt.Errorf("read central response: %w", err)
		}

		tmpFile, err := os.CreateTemp("", "aima-sync-*.json")
		if err != nil {
			return nil, fmt.Errorf("create temp file: %w", err)
		}
		tmpPath := tmpFile.Name()
		defer os.Remove(tmpPath)
		if _, err := tmpFile.Write(syncData); err != nil {
			tmpFile.Close()
			return nil, fmt.Errorf("write temp file: %w", err)
		}
		tmpFile.Close()

		importParams, _ := json.Marshal(map[string]any{
			"input_path": tmpPath,
			"conflict":   "skip",
		})
		result, err := deps.ImportKnowledge(ctx, importParams)
		if err != nil {
			return nil, fmt.Errorf("import pulled knowledge: %w", err)
		}
		_ = db.SetSyncTimestamp(ctx, "pull")

		// Sync v2: also pull advisories and publish to EventBus
		advisories, scenarios, advisoryEvents, scenarioEvents := pullAdvisoriesToEventBus(ctx, ac, deps)

		var imported any
		if err := json.Unmarshal(result, &imported); err != nil {
			imported = json.RawMessage(result)
		}

		hwTarget := edgeHardwareTarget(ctx, ac)
		return json.Marshal(map[string]any{
			"status":           "pulled",
			"protocol_version": "v2-edge",
			"endpoint":         endpoint,
			"since":            since,
			"hardware_profile": hwTarget.HardwareProfile,
			"gpu_arch":         hwTarget.GPUArch,
			"knowledge_import": imported,
			"advisories": map[string]any{
				"count":            len(advisories),
				"published_events": advisoryEvents,
				"filters": map[string]string{
					"hardware": hwTarget.MatchValue,
					"status":   "pending",
				},
				"items": advisories,
			},
			"scenarios": map[string]any{
				"count":            len(scenarios),
				"published_events": scenarioEvents,
				"filters": map[string]string{
					"hardware": hwTarget.MatchValue,
				},
				"items": scenarios,
			},
		})
	}

	deps.SyncStatus = func(ctx context.Context) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		pushAt, _ := db.GetSyncTimestamp(ctx, "push")
		pullAt, _ := db.GetSyncTimestamp(ctx, "pull")
		connected := false
		if endpoint != "" {
			req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/api/v1/stats", nil)
			if err == nil {
				resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
				if err == nil {
					resp.Body.Close()
					connected = resp.StatusCode == http.StatusOK
				}
			}
		}
		return json.Marshal(map[string]any{
			"endpoint":  endpoint,
			"connected": connected,
			"last_push": pushAt,
			"last_pull": pullAt,
		})
	}

	// Sync v2: advisory pull, scenario requests, feedback (v0.4 integration)
	deps.SyncPullAdvisories = func(ctx context.Context) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
		hwTarget := edgeHardwareTarget(ctx, ac)
		u := endpoint + "/api/v1/advisories"
		params := url.Values{}
		if hwTarget.MatchValue != "" {
			params.Set("hardware", hwTarget.MatchValue)
		}
		params.Set("status", "pending")
		if len(params) > 0 {
			u += "?" + params.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("pull advisories: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, fmt.Errorf("read advisories response: %w", err)
		}
		items, err := normalizeCentralAdvisoryList(body)
		if err != nil {
			return nil, fmt.Errorf("normalize advisories: %w", err)
		}
		items = coerceDeliveredAdvisories(items, "pending")
		items = filterNormalizedAdvisories(items, hwTarget.MatchValue, "pending")
		return json.Marshal(items)
	}

	deps.SyncPullScenarios = func(ctx context.Context) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
		hwTarget := edgeHardwareTarget(ctx, ac)
		u := endpoint + "/api/v1/scenarios"
		params := url.Values{}
		if hwTarget.MatchValue != "" {
			params.Set("hardware", hwTarget.MatchValue)
		}
		if len(params) > 0 {
			u += "?" + params.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("pull scenarios: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, fmt.Errorf("read scenarios response: %w", err)
		}
		items, err := normalizeCentralScenarioList(body, "")
		if err != nil {
			return nil, fmt.Errorf("normalize scenarios: %w", err)
		}
		items = filterNormalizedScenarios(items, hwTarget.MatchValue)
		return json.Marshal(items)
	}

	deps.AdvisoryFeedback = func(ctx context.Context, advisoryID, feedbackStatus, reason string) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
		normalizedStatus, accepted, err := normalizeFeedbackStatus(feedbackStatus)
		if err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(map[string]any{
			"feedback": reason,
			"accepted": accepted,
		})
		req, err := http.NewRequestWithContext(ctx, "POST",
			endpoint+"/api/v1/advisories/"+advisoryID+"/feedback",
			strings.NewReader(string(payload)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("send advisory feedback: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d: %s", resp.StatusCode, string(body))
		}
		return json.Marshal(map[string]any{
			"advisory_id":        advisoryID,
			"requested_status":   feedbackStatus,
			"normalized_status":  normalizedStatus,
			"accepted":           accepted,
			"protocol_version":   "v2-edge",
			"feedback_submitted": true,
		})
	}

	deps.RequestAdvise = func(ctx context.Context, model, engine, intent string) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
		hwTarget := edgeHardwareTarget(ctx, ac)
		payload, _ := json.Marshal(map[string]any{
			"action":           "recommend",
			"hardware":         hwTarget.MatchValue,
			"hardware_profile": hwTarget.HardwareProfile,
			"hardware_info":    hwTarget.Info,
			"model":            model,
			"engine":           engine,
			"goal":             intent,
			"intent":           intent,
		})
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/v1/advise",
			strings.NewReader(string(payload)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request advise: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d: %s", resp.StatusCode, string(body))
		}
		return normalizeAdviseResponse(body, map[string]any{
			"hardware":         hwTarget.MatchValue,
			"hardware_profile": hwTarget.HardwareProfile,
			"model":            model,
			"engine":           engine,
			"intent":           intent,
		})
	}

	deps.RequestScenario = func(ctx context.Context, hardware string, models []string, goal string) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
		payload, _ := json.Marshal(map[string]any{
			"hardware": hardware,
			"models":   models,
			"goal":     goal,
		})
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/v1/scenarios/generate",
			strings.NewReader(string(payload)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request scenario: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d: %s", resp.StatusCode, string(body))
		}
		return normalizeScenarioGenerateResponse(body, map[string]any{
			"hardware": hardware,
			"models":   models,
			"goal":     goal,
		})
	}

	deps.ListCentralScenarios = func(ctx context.Context, hardware, source string) (json.RawMessage, error) {
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
		u := endpoint + "/api/v1/scenarios"
		params := url.Values{}
		if hardware != "" {
			params.Set("hardware", hardware)
		}
		if source != "" {
			params.Set("source", source)
		}
		if len(params) > 0 {
			u += "?" + params.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := syncHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list central scenarios: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("central returned %d: %s", resp.StatusCode, string(body))
		}
		items, err := normalizeCentralScenarioList(body, source)
		if err != nil {
			return nil, fmt.Errorf("normalize central scenarios: %w", err)
		}
		if hardware != "" {
			items = filterNormalizedScenarios(items, hardware)
		}
		return json.Marshal(items)
	}


	deps.ValidateKnowledge = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Hardware string `json:"hardware"`
			Engine   string `json:"engine"`
			Model    string `json:"model"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		results, err := db.ListValidations(ctx, p.Hardware, p.Engine, p.Model)
		if err != nil {
			return nil, err
		}
		return json.Marshal(results)
	}

	deps.EngineSwitchCost = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			CurrentEngine string `json:"current_engine"`
			TargetEngine  string `json:"target_engine"`
			Hardware      string `json:"hardware"`
			Model         string `json:"model"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		// Look up engines from catalog for cold_start data
		hwInfo := knowledge.HardwareInfo{GPUArch: p.Hardware}
		currentEngine := cat.FindEngineByName(p.CurrentEngine, hwInfo)
		targetEngine := cat.FindEngineByName(p.TargetEngine, hwInfo)

		result := map[string]any{
			"current_engine": p.CurrentEngine,
			"target_engine":  p.TargetEngine,
		}

		if targetEngine != nil && len(targetEngine.TimeConstraints.ColdStartS) >= 2 {
			result["switch_time_s"] = targetEngine.TimeConstraints.ColdStartS[1]
		}

		// Amplifier comparison
		currentMult := 1.0
		targetMult := 1.0
		if currentEngine != nil && currentEngine.Amplifier.PerformanceMultiplier > 0 {
			currentMult = currentEngine.Amplifier.PerformanceMultiplier
		}
		if targetEngine != nil && targetEngine.Amplifier.PerformanceMultiplier > 0 {
			targetMult = targetEngine.Amplifier.PerformanceMultiplier
		}
		result["current_multiplier"] = currentMult
		result["target_multiplier"] = targetMult

		if targetMult > currentMult*1.1 {
			result["recommendation"] = "switch"
			result["reason"] = fmt.Sprintf("target %.1fx vs current %.1fx performance multiplier (>10%% gain)", targetMult, currentMult)
		} else {
			result["recommendation"] = "stay"
			result["reason"] = fmt.Sprintf("target %.1fx vs current %.1fx — gain insufficient to justify switch cost", targetMult, currentMult)
		}
		return json.Marshal(result)
	}

	deps.OpenQuestions = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Action      string `json:"action"`
			Status      string `json:"status"`
			ID          string `json:"id"`
			Result      string `json:"result"`
			Hardware    string `json:"hardware"`
			Model       string `json:"model"`
			Engine      string `json:"engine"`
			Endpoint    string `json:"endpoint"`
			RequestedBy string `json:"requested_by"`
			Concurrency int    `json:"concurrency"`
			Rounds      int    `json:"rounds"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		switch p.Action {
		case "resolve":
			if p.ID == "" {
				return nil, fmt.Errorf("id required for resolve action")
			}
			status := "confirmed"
			if p.Status != "" {
				status = p.Status
			}
			if err := db.ResolveOpenQuestion(ctx, p.ID, status, p.Result, p.Hardware); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"status": "resolved", "id": p.ID})
		case "run", "validate":
			// NOTE: explorationMgr is wired in run() after buildToolDeps;
			// this closure is overwritten there with the live reference.
			return nil, fmt.Errorf("exploration manager unavailable (not yet wired)")
		default:
			questions, err := db.ListOpenQuestions(ctx, p.Status)
			if err != nil {
				return nil, err
			}
			if questions == nil {
				questions = []map[string]any{}
			}
			return json.Marshal(questions)
		}
	}
}

// pullAdvisoriesToEventBus fetches advisories and scenarios from central
// and publishes them as events on the EventBus for Explorer processing.
func pullAdvisoriesToEventBus(ctx context.Context, ac *appContext, deps *mcp.ToolDeps) (advisories, scenarios []json.RawMessage, advisoryEvents, scenarioEvents int) {
	if ac.eventBus == nil {
		return nil, nil, 0, 0
	}

	// Pull advisories
	if deps.SyncPullAdvisories != nil {
		data, err := deps.SyncPullAdvisories(ctx)
		if err == nil {
			seen := make(map[string]struct{})
			var pulled []json.RawMessage
			if json.Unmarshal(data, &pulled) == nil {
				for _, adv := range pulled {
					id := edgePayloadID(adv)
					if id != "" {
						if _, ok := seen[id]; ok {
							continue
						}
						seen[id] = struct{}{}
					}
					ac.eventBus.Publish(agent.ExplorerEvent{
						Type:     agent.EventCentralAdvisory,
						Advisory: adv,
					})
					advisories = append(advisories, adv)
					advisoryEvents++
				}
			}
		} else {
			slog.Debug("pull advisories failed", "error", err)
		}
	}

	// Pull scenarios
	if deps.SyncPullScenarios != nil {
		data, err := deps.SyncPullScenarios(ctx)
		if err == nil {
			seen := make(map[string]struct{})
			var pulled []json.RawMessage
			if json.Unmarshal(data, &pulled) == nil {
				for _, scn := range pulled {
					id := edgePayloadID(scn)
					if id != "" {
						if _, ok := seen[id]; ok {
							continue
						}
						seen[id] = struct{}{}
					}
					ac.eventBus.Publish(agent.ExplorerEvent{
						Type:     agent.EventCentralScenario,
						Advisory: scn,
					})
					scenarios = append(scenarios, scn)
					scenarioEvents++
				}
			}
		} else {
			slog.Debug("pull scenarios failed", "error", err)
		}
	}
	return advisories, scenarios, advisoryEvents, scenarioEvents
}

type edgeHardwareMatch struct {
	MatchValue      string
	HardwareProfile string
	GPUArch         string
	Info            map[string]any
}

func edgeHardwareTarget(ctx context.Context, ac *appContext) edgeHardwareMatch {
	hw := buildHardwareInfo(ctx, ac.cat, ac.rt.Name())
	match := hw.HardwareProfile
	if match == "" {
		match = hw.GPUArch
	}
	return edgeHardwareMatch{
		MatchValue:      match,
		HardwareProfile: hw.HardwareProfile,
		GPUArch:         hw.GPUArch,
		Info: map[string]any{
			"gpu_arch":      hw.GPUArch,
			"gpu_model":     hw.GPUModel,
			"gpu_vram_mib":  hw.GPUVRAMMiB,
			"gpu_count":     hw.GPUCount,
			"cpu_arch":      hw.CPUArch,
			"cpu_cores":     hw.CPUCores,
			"ram_total_mib": hw.RAMTotalMiB,
		},
	}
}

func normalizeFeedbackStatus(status string) (normalized string, accepted bool, err error) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "validated", "accepted":
		return "validated", true, nil
	case "rejected":
		return "rejected", false, nil
	default:
		return "", false, fmt.Errorf("unsupported advisory feedback status %q: use validated or rejected", status)
	}
}

func normalizeAdviseResponse(body []byte, request map[string]any) (json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err == nil {
		result := map[string]any{
			"protocol_version": "v2-edge",
			"request":          request,
		}
		if raw, ok := payload["recommendation"]; ok && len(raw) > 0 {
			result["recommendation"] = json.RawMessage(raw)
		} else {
			result["recommendation"] = json.RawMessage(body)
		}
		if raw, ok := payload["advisory"]; ok && len(raw) > 0 {
			normalized, err := normalizeCentralAdvisory(raw)
			if err != nil {
				return nil, err
			}
			result["advisory"] = json.RawMessage(normalized)
		}
		return json.Marshal(result)
	}
	return json.Marshal(map[string]any{
		"protocol_version": "v2-edge",
		"request":          request,
		"recommendation":   json.RawMessage(body),
	})
}

func normalizeScenarioGenerateResponse(body []byte, request map[string]any) (json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err == nil {
		result := map[string]any{
			"protocol_version": "v2-edge",
			"request":          request,
		}
		if raw, ok := payload["scenario"]; ok && len(raw) > 0 {
			result["scenario"] = json.RawMessage(raw)
		}
		if raw, ok := payload["stored"]; ok && len(raw) > 0 {
			normalized, err := normalizeCentralScenario(raw)
			if err != nil {
				return nil, err
			}
			result["stored"] = json.RawMessage(normalized)
		}
		if len(result) > 2 {
			return json.Marshal(result)
		}
	}
	return json.Marshal(map[string]any{
		"protocol_version": "v2-edge",
		"request":          request,
		"scenario":         json.RawMessage(body),
	})
}

func normalizeCentralAdvisoryList(body []byte) ([]json.RawMessage, error) {
	items, err := decodeRawList(body, "advisories", "items")
	if err != nil {
		return nil, err
	}
	normalized := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		entry, err := normalizeCentralAdvisory(item)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, entry)
	}
	return normalized, nil
}

func normalizeCentralScenarioList(body []byte, sourceFilter string) ([]json.RawMessage, error) {
	items, err := decodeRawList(body, "scenarios", "items")
	if err != nil {
		return nil, err
	}
	normalized := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		entry, err := normalizeCentralScenario(item)
		if err != nil {
			return nil, err
		}
		if sourceFilter != "" && !edgeScenarioMatchesSource(entry, sourceFilter) {
			continue
		}
		normalized = append(normalized, entry)
	}
	return normalized, nil
}

func decodeRawList(body []byte, keys ...string) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return []json.RawMessage{}, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return nil, err
		}
		return items, nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, err
	}
	for _, key := range keys {
		raw, ok := envelope[key]
		if !ok || len(raw) == 0 {
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err == nil {
			return items, nil
		}
	}
	return []json.RawMessage{}, nil
}

func normalizeCentralAdvisory(raw json.RawMessage) (json.RawMessage, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	result := map[string]any{
		"id":              stringField(payload, "id"),
		"type":            normalizeAdvisoryType(stringField(payload, "type")),
		"status":          advisoryStatus(payload),
		"confidence":      firstNonEmptyString(stringField(payload, "confidence"), "medium"),
		"target_hardware": firstNonEmptyString(stringField(payload, "target_hardware"), stringField(payload, "hardware")),
		"target_model":    firstNonEmptyString(stringField(payload, "target_model"), stringField(payload, "model")),
		"target_engine":   firstNonEmptyString(stringField(payload, "target_engine"), stringField(payload, "engine")),
	}
	if title := stringField(payload, "title"); title != "" {
		result["title"] = title
	}
	if summary := stringField(payload, "summary"); summary != "" {
		result["summary"] = summary
	}
	if reasoning := firstNonEmptyString(stringField(payload, "reasoning"), stringField(payload, "summary")); reasoning != "" {
		result["reasoning"] = reasoning
	}
	if createdAt := stringField(payload, "created_at"); createdAt != "" {
		result["created_at"] = createdAt
	}
	if deliveredAt := stringField(payload, "delivered_at"); deliveredAt != "" {
		result["delivered_at"] = deliveredAt
	}
	if validatedAt := stringField(payload, "validated_at"); validatedAt != "" {
		result["validated_at"] = validatedAt
	}
	if feedback := stringField(payload, "feedback"); feedback != "" {
		result["feedback"] = feedback
	}
	if content := advisoryContent(payload); len(content) > 0 {
		result["content"] = json.RawMessage(content)
	}
	return json.Marshal(result)
}

func normalizeCentralScenario(raw json.RawMessage) (json.RawMessage, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	result := map[string]any{
		"id":               stringField(payload, "id"),
		"name":             stringField(payload, "name"),
		"hardware_profile": firstNonEmptyString(stringField(payload, "hardware_profile"), stringField(payload, "hardware")),
		"source":           stringField(payload, "source"),
	}
	if description := stringField(payload, "description"); description != "" {
		result["description"] = description
	}
	if version := numericField(payload, "version"); version > 0 {
		result["version"] = version
	} else {
		result["version"] = 1
	}
	if createdAt := stringField(payload, "created_at"); createdAt != "" {
		result["created_at"] = createdAt
	}
	if updatedAt := stringField(payload, "updated_at"); updatedAt != "" {
		result["updated_at"] = updatedAt
	}
	if models := decodeEmbeddedJSON(payload["models"]); models != nil {
		result["models"] = models
	}
	if scenario := firstRawJSON(payload["scenario"], payload["scenario_yaml"], payload["config"]); len(scenario) > 0 {
		result["scenario"] = json.RawMessage(scenario)
	}
	return json.Marshal(result)
}

func advisoryStatus(payload map[string]any) string {
	if status := stringField(payload, "status"); status != "" {
		return canonicalAdvisoryStatus(status)
	}
	if accepted, ok := payload["accepted"].(bool); ok {
		if accepted {
			return "validated"
		}
		if stringField(payload, "feedback") != "" {
			return "rejected"
		}
	}
	return "pending"
}

func canonicalAdvisoryStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "delivered", "validated", "rejected", "expired":
		return strings.ToLower(strings.TrimSpace(status))
	case "accepted":
		return "validated"
	case "reject", "declined":
		return "rejected"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func advisoryContent(payload map[string]any) json.RawMessage {
	if raw := firstRawJSON(payload["content"], payload["content_json"]); len(raw) > 0 {
		return raw
	}
	legacy := map[string]any{}
	if details := stringField(payload, "details"); details != "" {
		legacy["details"] = details
	}
	if summary := stringField(payload, "summary"); summary != "" {
		legacy["summary"] = summary
	}
	if title := stringField(payload, "title"); title != "" {
		legacy["title"] = title
	}
	if len(legacy) == 0 {
		return nil
	}
	raw, _ := json.Marshal(legacy)
	return raw
}

func normalizeAdvisoryType(typ string) string {
	switch typ {
	case "recommendation":
		return "config_recommend"
	case "optimization":
		return "scenario_optimization"
	case "gap":
		return "gap_alert"
	default:
		return typ
	}
}

func edgeScenarioMatchesSource(raw json.RawMessage, source string) bool {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	return strings.EqualFold(stringField(payload, "source"), source)
}

func edgeAdvisoryMatches(raw json.RawMessage, hardware, status string) bool {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	if hardware != "" {
		target := firstNonEmptyString(stringField(payload, "target_hardware"), stringField(payload, "hardware"))
		if target != "" && !strings.EqualFold(target, hardware) {
			return false
		}
	}
	if status != "" && !strings.EqualFold(stringField(payload, "status"), status) {
		return false
	}
	return true
}

func edgeScenarioMatchesHardware(raw json.RawMessage, hardware string) bool {
	if hardware == "" {
		return true
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	target := firstNonEmptyString(stringField(payload, "hardware_profile"), stringField(payload, "hardware"))
	if target == "" {
		return true
	}
	return strings.EqualFold(target, hardware)
}

func filterNormalizedAdvisories(items []json.RawMessage, hardware, status string) []json.RawMessage {
	filtered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if edgeAdvisoryMatches(item, hardware, status) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func coerceDeliveredAdvisories(items []json.RawMessage, requestedStatus string) []json.RawMessage {
	if !strings.EqualFold(requestedStatus, "pending") {
		return items
	}
	normalized := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var payload map[string]any
		if err := json.Unmarshal(item, &payload); err != nil {
			normalized = append(normalized, item)
			continue
		}
		if strings.EqualFold(stringField(payload, "status"), "delivered") {
			payload["status"] = "pending"
			if remapped, err := json.Marshal(payload); err == nil {
				item = remapped
			}
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func filterNormalizedScenarios(items []json.RawMessage, hardware string) []json.RawMessage {
	filtered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if edgeScenarioMatchesHardware(item, hardware) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func edgePayloadID(raw json.RawMessage) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return stringField(payload, "id")
}

func decodeEmbeddedJSON(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any, map[string]any:
		return x
	case string:
		var decoded any
		if err := json.Unmarshal([]byte(x), &decoded); err == nil {
			return decoded
		}
		return x
	default:
		return x
	}
}

func firstRawJSON(values ...any) json.RawMessage {
	for _, value := range values {
		switch x := value.(type) {
		case nil:
			continue
		case json.RawMessage:
			if len(x) != 0 {
				return x
			}
		case []byte:
			if len(x) != 0 {
				return json.RawMessage(x)
			}
		case string:
			if strings.TrimSpace(x) == "" {
				continue
			}
			if json.Valid([]byte(x)) {
				return json.RawMessage(x)
			}
			raw, _ := json.Marshal(x)
			return raw
		default:
			raw, err := json.Marshal(x)
			if err == nil && len(raw) != 0 {
				return raw
			}
		}
	}
	return nil
}

func stringField(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	switch v := payload[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func numericField(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	switch v := payload[key].(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// suppress "imported and not used" for packages only referenced in struct tags
var _ = strconv.Itoa
var _ state.DB
var _ = slog.Info
