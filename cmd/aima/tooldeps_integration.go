package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"

	state "github.com/jguan/aima/internal"
)

// buildIntegrationDeps wires scenario, app, sync, power, openclaw questions,
// engine switch cost, validation, and power history tools.
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

	// App management (D4)
	deps.AppRegister = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Name            string          `json:"name"`
			InferenceNeeds  json.RawMessage `json:"inference_needs"`
			ResourceBudget  json.RawMessage `json:"resource_budget"`
			TimeConstraints json.RawMessage `json:"time_constraints"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Name == "" {
			return nil, fmt.Errorf("name required")
		}
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(p.Name)))[:16]
		specBytes, _ := json.Marshal(map[string]any{
			"name":             p.Name,
			"inference_needs":  json.RawMessage(p.InferenceNeeds),
			"resource_budget":  json.RawMessage(p.ResourceBudget),
			"time_constraints": json.RawMessage(p.TimeConstraints),
		})
		if err := db.InsertApp(ctx, id, p.Name, string(specBytes)); err != nil {
			return nil, err
		}

		// Parse inference needs and record dependencies
		var needs []struct {
			Type        string `json:"type"`
			Model       string `json:"model"`
			Required    bool   `json:"required"`
			Performance string `json:"performance"`
		}
		if p.InferenceNeeds != nil {
			_ = json.Unmarshal(p.InferenceNeeds, &needs)
		}
		for _, need := range needs {
			_ = db.UpsertAppDependency(ctx, id, need.Type, need.Model, "", false)
		}

		return json.Marshal(map[string]any{"id": id, "name": p.Name, "status": "registered", "dependencies": len(needs)})
	}

	deps.AppProvision = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		apps, err := db.ListApps(ctx)
		if err != nil {
			return nil, err
		}
		// Find the app
		var appSpec map[string]any
		var appID string
		for _, a := range apps {
			if a["name"] == p.Name {
				appID, _ = a["id"].(string)
				if specRaw, ok := a["spec"].(json.RawMessage); ok {
					_ = json.Unmarshal(specRaw, &appSpec)
				}
				break
			}
		}
		if appID == "" {
			return nil, fmt.Errorf("app %q not found", p.Name)
		}

		// Parse inference needs from spec
		var needs []struct {
			Type        string `json:"type"`
			Model       string `json:"model"`
			Required    bool   `json:"required"`
			Performance string `json:"performance"`
		}
		if needsRaw, ok := appSpec["inference_needs"]; ok {
			needsBytes, _ := json.Marshal(needsRaw)
			_ = json.Unmarshal(needsBytes, &needs)
		}

		// Check existing deployments
		deploys, _ := deps.DeployList(ctx)
		var deployList []map[string]any
		_ = json.Unmarshal(deploys, &deployList)

		report := make([]map[string]any, 0, len(needs))
		allSatisfied := true
		for _, need := range needs {
			satisfied := false
			deployName := ""
			// Check if already deployed
			for _, d := range deployList {
				dModel, _ := d["model"].(string)
				if need.Model != "" && strings.Contains(dModel, need.Model) {
					satisfied = true
					deployName, _ = d["name"].(string)
					break
				}
			}
			_ = db.UpsertAppDependency(ctx, appID, need.Type, need.Model, deployName, satisfied)
			if !satisfied && need.Required {
				allSatisfied = false
			}
			report = append(report, map[string]any{
				"type": need.Type, "model": need.Model, "satisfied": satisfied,
				"deploy_name": deployName, "required": need.Required,
			})
		}

		status := "provisioned"
		if !allSatisfied {
			status = "partial"
		}
		_ = db.UpdateAppStatus(ctx, appID, status)

		return json.Marshal(map[string]any{
			"app": p.Name, "status": status, "dependencies": report,
		})
	}

	deps.AppList = func(ctx context.Context) (json.RawMessage, error) {
		apps, err := db.ListApps(ctx)
		if err != nil {
			return nil, err
		}
		if apps == nil {
			apps = []map[string]any{}
		}
		return json.Marshal(apps)
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
			Data struct {
				Configurations   []json.RawMessage `json:"configurations"`
				BenchmarkResults []json.RawMessage `json:"benchmark_results"`
				KnowledgeNotes   []json.RawMessage `json:"knowledge_notes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(exportData, &exportEnvelope); err != nil {
			return nil, fmt.Errorf("parse export data: %w", err)
		}

		hwInfo, _ := hal.Detect(ctx)
		deviceID, _ := deps.GetConfig(ctx, "device.id")
		gpuArch := ""
		if hwInfo != nil && hwInfo.GPU != nil {
			gpuArch = hwInfo.GPU.Arch
		}

		ingestPayload, err := json.Marshal(map[string]any{
			"schema_version":  1,
			"device_id":       deviceID,
			"gpu_arch":        gpuArch,
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
			"status":   "pushed",
			"endpoint": endpoint,
			"result":   json.RawMessage(body),
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

		syncData, err := io.ReadAll(resp.Body)
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
		return result, nil
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

	// Power mode (S3)
	deps.PowerMode = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Action string `json:"action"`
			Mode   string `json:"mode"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		hw, err := hal.Detect(ctx)
		if err != nil {
			return nil, err
		}
		// Look up power modes from hardware profile
		var powerModes []int
		var tdpWatts int
		gpuArch := ""
		if hw.GPU != nil {
			gpuArch = hw.GPU.Arch
		}
		for _, hp := range cat.HardwareProfiles {
			if hp.Hardware.GPU.Arch == gpuArch {
				powerModes = hp.Constraints.PowerModes
				tdpWatts = hp.Constraints.TDPWatts
				break
			}
		}
		result := map[string]any{
			"gpu_arch":    gpuArch,
			"tdp_watts":   tdpWatts,
			"power_modes": powerModes,
		}
		if hw.GPU != nil {
			result["current_power_draw_watts"] = hw.GPU.PowerDrawWatts
			result["power_limit_watts"] = hw.GPU.PowerLimitWatts
		}
		return json.Marshal(result)
	}

	deps.PowerHistory = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.From == "" {
			p.From = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		}
		if p.To == "" {
			p.To = time.Now().UTC().Format(time.RFC3339)
		}
		results, err := db.QueryPowerHistory(ctx, p.From, p.To, 60)
		if err != nil {
			return nil, err
		}
		return json.Marshal(results)
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

// suppress "imported and not used" for packages only referenced in struct tags
var _ = strconv.Itoa
var _ state.DB
var _ = slog.Info
