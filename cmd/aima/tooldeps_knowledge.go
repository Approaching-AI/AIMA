package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jguan/aima/catalog"
	"github.com/jguan/aima/internal/buildinfo"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"

	state "github.com/jguan/aima/internal"
)

// buildKnowledgeDeps wires knowledge.*, catalog.*, export/import, and knowledge summary tools.
func buildKnowledgeDeps(ac *appContext, deps *mcp.ToolDeps) {
	cat := ac.cat
	db := ac.db
	kStore := ac.kStore
	rt := ac.rt
	dataDir := ac.dataDir
	factoryDigests := ac.digests

	deps.ResolveConfig = func(ctx context.Context, modelName, engineType string, overrides map[string]any) (json.RawMessage, error) {
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		rd, err := resolveDeployment(ctx, cat, db, kStore, hwInfo, modelName, engineType, "", overrides, dataDir)
		if err != nil {
			return nil, err
		}
		return json.Marshal(rd.Resolved)
	}
	deps.SearchKnowledge = func(ctx context.Context, filter map[string]string) (json.RawMessage, error) {
		nf := state.NoteFilter{
			HardwareProfile: filter["hardware"],
			Model:           filter["model"],
			Engine:          filter["engine"],
		}
		notes, err := db.SearchNotes(ctx, nf)
		if err != nil {
			return nil, err
		}
		return json.Marshal(notes)
	}
	deps.SaveKnowledge = func(ctx context.Context, note json.RawMessage) error {
		var n state.KnowledgeNote
		if err := json.Unmarshal(note, &n); err != nil {
			return fmt.Errorf("parse knowledge note: %w", err)
		}
		return db.InsertNote(ctx, &n)
	}
	deps.GeneratePod = func(ctx context.Context, modelName, engineType, slot string, configOverrides map[string]any) (json.RawMessage, error) {
		hwInfo := buildHardwareInfo(ctx, cat, rt.Name())
		overrides := make(map[string]any, len(configOverrides)+1)
		for k, v := range configOverrides {
			overrides[k] = v
		}
		if slot != "" {
			overrides["slot"] = slot
		}
		goldenOpt := knowledge.WithGoldenConfig(func(hardware, engine, model string) map[string]any {
			return queryGoldenOverrides(ctx, kStore, hardware, engine, model)
		})
		resolved, _, err := resolveWithFallback(ctx, cat, db, hwInfo, modelName, engineType, overrides, dataDir, goldenOpt)
		if err != nil {
			return nil, err
		}
		podYAML, err := knowledge.GeneratePod(resolved)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(podYAML), nil
	}
	deps.ListProfiles = func(ctx context.Context) (json.RawMessage, error) {
		profiles, err := kStore.ListHardwareProfiles(ctx)
		if err != nil {
			return json.Marshal(cat.HardwareProfiles) // fallback to in-memory
		}
		return json.Marshal(profiles)
	}
	deps.ListEngineAssets = func(ctx context.Context) (json.RawMessage, error) {
		assets, err := kStore.ListEngineAssets(ctx)
		if err != nil {
			return json.Marshal(cat.EngineAssets) // fallback to in-memory
		}
		return json.Marshal(assets)
	}
	deps.ListModelAssets = func(ctx context.Context) (json.RawMessage, error) {
		return json.Marshal(cat.ModelAssets)
	}
	deps.ListPartitionStrategies = func(ctx context.Context) (json.RawMessage, error) {
		return json.Marshal(cat.PartitionStrategies)
	}

	// Knowledge query (enhanced -- SQLite relational queries)
	deps.SearchConfigs = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p knowledge.SearchParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse search params: %w", err)
		}
		result, err := kStore.Search(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.CompareConfigs = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p knowledge.CompareParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse compare params: %w", err)
		}
		result, err := kStore.Compare(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.SimilarConfigs = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p knowledge.SimilarParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse similar params: %w", err)
		}
		result, err := kStore.Similar(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.LineageConfigs = func(ctx context.Context, configID string) (json.RawMessage, error) {
		result, err := kStore.Lineage(ctx, configID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.GapsKnowledge = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p knowledge.GapsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse gaps params: %w", err)
		}
		result, err := kStore.Gaps(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.AggregateKnowledge = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p knowledge.AggregateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse aggregate params: %w", err)
		}
		result, err := kStore.Aggregate(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}

	// Knowledge export/import
	deps.ExportKnowledge = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Hardware   string `json:"hardware"`
			Model      string `json:"model"`
			Engine     string `json:"engine"`
			OutputPath string `json:"output_path"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse export params: %w", err)
		}

		configs, err := db.ListConfigurations(ctx, p.Hardware, p.Model, p.Engine)
		if err != nil {
			return nil, fmt.Errorf("list configurations: %w", err)
		}

		var configIDs []string
		for _, c := range configs {
			configIDs = append(configIDs, c.ID)
		}

		// Only fetch benchmarks for matched configs.
		// When a filter is active but matches no configs, return empty benchmarks
		// instead of falling through to an unfiltered query.
		hasFilter := p.Hardware != "" || p.Model != "" || p.Engine != ""
		var benchmarks []*state.BenchmarkResult
		if len(configIDs) > 0 || !hasFilter {
			benchmarks, err = db.ListBenchmarkResults(ctx, configIDs, 0)
			if err != nil {
				return nil, fmt.Errorf("list benchmarks: %w", err)
			}
		}

		notes, err := db.SearchNotes(ctx, state.NoteFilter{
			HardwareProfile: p.Hardware,
			Model:           p.Model,
			Engine:          p.Engine,
		})
		if err != nil {
			return nil, fmt.Errorf("search notes: %w", err)
		}

		export := map[string]any{
			"schema_version": 1,
			"exported_at":    time.Now().UTC().Format(time.RFC3339),
			"aima_version":   buildinfo.Version,
			"filter":         map[string]string{"hardware": p.Hardware, "model": p.Model, "engine": p.Engine},
			"data": map[string]any{
				"configurations":    configs,
				"benchmark_results": benchmarks,
				"knowledge_notes":   notes,
			},
			"stats": map[string]int{
				"configurations":    len(configs),
				"benchmark_results": len(benchmarks),
				"knowledge_notes":   len(notes),
			},
		}

		exportJSON, err := json.MarshalIndent(export, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal export: %w", err)
		}

		if p.OutputPath != "" {
			if err := os.WriteFile(p.OutputPath, exportJSON, 0644); err != nil {
				return nil, fmt.Errorf("write export file: %w", err)
			}
			return json.Marshal(map[string]any{
				"path":  p.OutputPath,
				"stats": export["stats"],
			})
		}

		return exportJSON, nil
	}

	deps.ImportKnowledge = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			InputPath string `json:"input_path"`
			Conflict  string `json:"conflict"`
			DryRun    bool   `json:"dry_run"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse import params: %w", err)
		}
		if p.Conflict == "" {
			p.Conflict = "skip"
		}

		data, err := os.ReadFile(p.InputPath)
		if err != nil {
			return nil, fmt.Errorf("read import file: %w", err)
		}

		var envelope struct {
			SchemaVersion int `json:"schema_version"`
			Data          struct {
				Configurations   []*state.Configuration   `json:"configurations"`
				BenchmarkResults []*state.BenchmarkResult `json:"benchmark_results"`
				KnowledgeNotes   []*state.KnowledgeNote   `json:"knowledge_notes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, fmt.Errorf("parse import JSON: %w", err)
		}
		if envelope.SchemaVersion != 1 {
			return nil, fmt.Errorf("unsupported schema version %d (expected 1)", envelope.SchemaVersion)
		}

		imported := map[string]int{"configurations": 0, "benchmark_results": 0, "knowledge_notes": 0}
		skipped := 0
		var errors []string

		rawDB := db.RawDB()
		tx, err := rawDB.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("begin transaction: %w", err)
		}
		defer tx.Rollback()

		// All reads and writes go through tx to avoid deadlock
		// (db uses SetMaxOpenConns(1), so db.GetConfiguration would block).

		// Import configurations
		for _, c := range envelope.Data.Configurations {
			var exists int
			tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM configurations WHERE id = ?`, c.ID).Scan(&exists)
			if exists > 0 && p.Conflict == "skip" {
				skipped++
				continue
			}
			if p.DryRun {
				imported["configurations"]++
				continue
			}
			if exists > 0 {
				tx.ExecContext(ctx, `DELETE FROM configurations WHERE id = ?`, c.ID)
			}
			tagsJSON, _ := json.Marshal(c.Tags)
			var derivedFrom sql.NullString
			if c.DerivedFrom != "" {
				derivedFrom = sql.NullString{String: c.DerivedFrom, Valid: true}
			}
			_, insertErr := tx.ExecContext(ctx,
				`INSERT INTO configurations (id, hardware_id, engine_id, model_id, partition_slot,
					config, config_hash, derived_from, status, tags, source, device_id)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.ID, c.HardwareID, c.EngineID, c.ModelID, c.Slot,
				c.Config, c.ConfigHash, derivedFrom, c.Status, string(tagsJSON), c.Source, c.DeviceID)
			if insertErr != nil {
				errors = append(errors, fmt.Sprintf("config %s: %v", c.ID, insertErr))
				continue
			}
			imported["configurations"]++
		}

		// Import benchmark results
		for _, b := range envelope.Data.BenchmarkResults {
			var exists int
			tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM benchmark_results WHERE id = ?`, b.ID).Scan(&exists)
			if exists > 0 && p.Conflict == "skip" {
				skipped++
				continue
			}
			if p.DryRun {
				imported["benchmark_results"]++
				continue
			}
			if exists > 0 {
				tx.ExecContext(ctx, `DELETE FROM benchmark_results WHERE id = ?`, b.ID)
			}
			_, insertErr := tx.ExecContext(ctx,
				`INSERT INTO benchmark_results (id, config_id, concurrency, input_len_bucket, output_len_bucket, modality,
					ttft_ms_p50, ttft_ms_p95, ttft_ms_p99, tpot_ms_p50, tpot_ms_p95,
					throughput_tps, qps, vram_usage_mib, ram_usage_mib, power_draw_watts, gpu_utilization_pct,
					error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				b.ID, b.ConfigID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
				b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms,
				b.ThroughputTPS, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB, b.PowerDrawWatts, b.GPUUtilPct,
				b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, b.TestedAt, b.AgentModel, b.Notes)
			if insertErr != nil {
				errors = append(errors, fmt.Sprintf("benchmark %s: %v", b.ID, insertErr))
				continue
			}
			imported["benchmark_results"]++
		}

		// Import knowledge notes
		for _, n := range envelope.Data.KnowledgeNotes {
			var exists int
			tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_notes WHERE id = ?`, n.ID).Scan(&exists)
			if exists > 0 && p.Conflict == "skip" {
				skipped++
				continue
			}
			if p.DryRun {
				imported["knowledge_notes"]++
				continue
			}
			if exists > 0 {
				tx.ExecContext(ctx, `DELETE FROM knowledge_notes WHERE id = ?`, n.ID)
			}
			tagsJSON, _ := json.Marshal(n.Tags)
			_, insertErr := tx.ExecContext(ctx,
				`INSERT INTO knowledge_notes (id, title, tags, hardware_profile, model, engine, content, confidence)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				n.ID, n.Title, string(tagsJSON), n.HardwareProfile, n.Model, n.Engine, n.Content, n.Confidence)
			if insertErr != nil {
				errors = append(errors, fmt.Sprintf("note %s: %v", n.ID, insertErr))
				continue
			}
			imported["knowledge_notes"]++
		}

		// If any inserts failed, rollback the entire transaction
		if len(errors) > 0 {
			return json.Marshal(map[string]any{
				"imported": map[string]int{"configurations": 0, "benchmark_results": 0, "knowledge_notes": 0},
				"skipped":  skipped,
				"errors":   errors,
				"dry_run":  p.DryRun,
			})
		}

		if !p.DryRun {
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit import: %w", err)
			}
			if imported["benchmark_results"] > 0 {
				refreshPerfVectors(ctx, kStore)
			}
		}

		return json.Marshal(map[string]any{
			"imported": imported,
			"skipped":  skipped,
			"dry_run":  p.DryRun,
		})
	}

	deps.ListKnowledgeSummary = func(ctx context.Context) (json.RawMessage, error) {
		profilesRaw, err := json.Marshal(cat.HardwareProfiles)
		if err != nil {
			return nil, fmt.Errorf("marshal profiles: %w", err)
		}
		enginesRaw, err := json.Marshal(cat.EngineAssets)
		if err != nil {
			return nil, fmt.Errorf("marshal engines: %w", err)
		}
		modelsRaw, err := json.Marshal(cat.ModelAssets)
		if err != nil {
			return nil, fmt.Errorf("marshal models: %w", err)
		}

		var profiles []map[string]any
		var engines []map[string]any
		var models []map[string]any
		if err := json.Unmarshal(profilesRaw, &profiles); err != nil {
			return nil, fmt.Errorf("decode profiles: %w", err)
		}
		if err := json.Unmarshal(enginesRaw, &engines); err != nil {
			return nil, fmt.Errorf("decode engines: %w", err)
		}
		if err := json.Unmarshal(modelsRaw, &models); err != nil {
			return nil, fmt.Errorf("decode models: %w", err)
		}

		summary := map[string]any{
			"hardware_profiles":    len(profiles),
			"engine_assets":        len(engines),
			"model_assets":         len(models),
			"partition_strategies": len(cat.PartitionStrategies),
		}

		profileNames := make([]string, 0, len(profiles))
		for _, hp := range profiles {
			if n, ok := hp["name"].(string); ok && n != "" {
				profileNames = append(profileNames, n)
				continue
			}
			if n, ok := hp["id"].(string); ok && n != "" {
				profileNames = append(profileNames, n)
			}
		}
		summary["profiles"] = profileNames

		engineNames := make([]string, 0, len(engines))
		for _, ea := range engines {
			if t, ok := ea["type"].(string); ok && t != "" {
				engineNames = append(engineNames, t)
				continue
			}
			if n, ok := ea["name"].(string); ok && n != "" {
				engineNames = append(engineNames, n)
				continue
			}
			if n, ok := ea["id"].(string); ok && n != "" {
				engineNames = append(engineNames, n)
			}
		}
		summary["engines"] = engineNames

		modelNames := make([]string, 0, len(models))
		for _, ma := range models {
			if n, ok := ma["name"].(string); ok && n != "" {
				modelNames = append(modelNames, n)
				continue
			}
			if n, ok := ma["id"].(string); ok && n != "" {
				modelNames = append(modelNames, n)
			}
		}
		summary["models"] = modelNames

		partitionNames := make([]string, 0, len(cat.PartitionStrategies))
		for _, ps := range cat.PartitionStrategies {
			partitionNames = append(partitionNames, ps.Metadata.Name)
		}
		summary["partitions"] = partitionNames

		scenarioNames := make([]string, 0, len(cat.DeploymentScenarios))
		for _, ds := range cat.DeploymentScenarios {
			scenarioNames = append(scenarioNames, ds.Metadata.Name)
		}
		summary["deployment_scenarios"] = len(cat.DeploymentScenarios)
		summary["scenarios"] = scenarioNames

		return json.Marshal(summary)
	}

	deps.CatalogOverride = func(ctx context.Context, kind, name, content string) (json.RawMessage, error) {
		// Validate kind
		dir := knowledge.KindToDir(kind)
		if dir == "" {
			return nil, fmt.Errorf("unknown kind %q", kind)
		}
		// Validate override file basename to prevent path traversal.
		if err := validateOverlayAssetName(name); err != nil {
			return nil, err
		}
		// Validate YAML parses as the correct kind AND body kind matches param kind
		tmpCat := &knowledge.Catalog{}
		if err := tmpCat.ParseAssetPublic([]byte(content), "input"); err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		if bodyKind := tmpCat.ParsedKind(); bodyKind != kind {
			return nil, fmt.Errorf("kind mismatch: parameter is %q but YAML body is %q", kind, bodyKind)
		}
		// Inject _base_digest if factory has this asset
		finalContent := content
		if digest, ok := factoryDigests[name]; ok {
			finalContent = "_base_digest: " + digest + "\n" + content
		}
		// Write to overlay directory
		overlaySubDir := filepath.Join(dataDir, "catalog", dir)
		if err := os.MkdirAll(overlaySubDir, 0o755); err != nil {
			return nil, fmt.Errorf("create overlay dir: %w", err)
		}
		outPath := filepath.Join(overlaySubDir, name+".yaml")
		action := "created"
		if _, err := os.Stat(outPath); err == nil {
			action = "replaced"
		}
		if err := os.WriteFile(outPath, []byte(finalContent), 0o644); err != nil {
			return nil, fmt.Errorf("write overlay: %w", err)
		}
		result := map[string]string{
			"path":   outPath,
			"action": action,
		}
		if _, ok := factoryDigests[name]; ok {
			result["note"] = "overlay shadows factory asset, _base_digest injected"
		}
		return json.Marshal(result)
	}

	deps.CatalogStatus = func(ctx context.Context) (json.RawMessage, error) {
		factoryCat, _ := knowledge.LoadCatalog(catalog.FS)
		overlayDir := filepath.Join(dataDir, "catalog")
		var overlayCat *knowledge.Catalog
		var parseWarnings []string
		if info, e := os.Stat(overlayDir); e == nil && info.IsDir() {
			overlayCat, parseWarnings = knowledge.LoadCatalogLenient(os.DirFS(overlayDir))
		} else {
			overlayCat = &knowledge.Catalog{}
		}
		// Find shadowed assets
		factoryNames := knowledge.CollectNames(factoryCat)
		overlayNames := knowledge.CollectNames(overlayCat)
		type shadowEntry struct {
			Name  string `json:"name"`
			Kind  string `json:"kind"`
			Stale bool   `json:"stale"`
		}
		var shadowed []shadowEntry
		overlayDigests := knowledge.ExtractOverlayDigestsFromDir(overlayDir)
		for name := range overlayNames {
			if factoryNames[name] {
				stale := false
				if baseD, ok := overlayDigests[name]; ok {
					if factD, ok2 := factoryDigests[name]; ok2 && baseD != factD {
						stale = true
					}
				}
				shadowed = append(shadowed, shadowEntry{Name: name, Stale: stale})
			}
		}
		status := map[string]any{
			"factory_assets": catalogSize(factoryCat),
			"overlay_assets": catalogSize(overlayCat),
			"shadowed":       shadowed,
			"parse_warnings": parseWarnings,
		}
		return json.Marshal(status)
	}

	deps.CatalogValidate = func(ctx context.Context) (json.RawMessage, error) {
		type issue struct {
			Engine   string `json:"engine"`
			Severity string `json:"severity"` // "error" or "warning"
			Field    string `json:"field"`
			Message  string `json:"message"`
		}
		var issues []issue

		knownRegistryPrefixes := []string{
			"docker.io/", "ghcr.io/", "nvcr.io/", "quay.io/",
			"registry.cn-", "harbor.", "cr.", "docker.1ms.run/",
		}

		for _, ea := range cat.EngineAssets {
			name := ea.Metadata.Name

			// Skip preinstalled engines (no image to validate)
			if ea.Source != nil && ea.Source.InstallType == "preinstalled" && ea.Image.Name == "" {
				continue
			}

			isLocal := ea.Image.Distribution == "local"

			// Check: container engines should have registries (unless local)
			if ea.Image.Name != "" && len(ea.Image.Registries) == 0 && !isLocal {
				issues = append(issues, issue{
					Engine:   name,
					Severity: "error",
					Field:    "image.registries",
					Message:  "container engine has no registries configured; pull will fail",
				})
			}

			// Check: image.name should not contain registry prefix
			if ea.Image.Name != "" {
				for _, prefix := range knownRegistryPrefixes {
					if strings.HasPrefix(ea.Image.Name, prefix) {
						issues = append(issues, issue{
							Engine:   name,
							Severity: "warning",
							Field:    "image.name",
							Message:  fmt.Sprintf("image name contains registry prefix %q; use short name in image.name and put full paths in registries", prefix),
						})
						break
					}
				}
			}

			// Check: single registry = single point of failure
			if ea.Image.Name != "" && len(ea.Image.Registries) == 1 && !isLocal {
				issues = append(issues, issue{
					Engine:   name,
					Severity: "warning",
					Field:    "image.registries",
					Message:  fmt.Sprintf("only one registry (%s); no fallback if it is unavailable", ea.Image.Registries[0]),
				})
			}

			// Check: local distribution should have a comment or clear name
			if isLocal && len(ea.Image.Registries) > 0 {
				issues = append(issues, issue{
					Engine:   name,
					Severity: "warning",
					Field:    "image.distribution",
					Message:  "distribution is 'local' but registries are configured; these registries will not be used for pull",
				})
			}
		}

		result := map[string]any{
			"total_engines": len(cat.EngineAssets),
			"issues":        issues,
			"issue_count":   len(issues),
		}
		return json.Marshal(result)
	}
}
