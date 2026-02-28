package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"
)

// Catalog holds all knowledge assets loaded from embedded YAML files.
type Catalog struct {
	HardwareProfiles    []HardwareProfile
	PartitionStrategies []PartitionStrategy
	EngineAssets        []EngineAsset
	ModelAssets         []ModelAsset
	StackComponents     []StackComponent
}

// --- Hardware Profile ---

type HardwareProfile struct {
	Kind     string              `yaml:"kind"`
	Metadata HardwareMetadata    `yaml:"metadata"`
	Hardware HardwareSpec        `yaml:"hardware"`
	Constraints HardwareConstraints `yaml:"constraints"`
	Partition HardwarePartition   `yaml:"partition"`
}

type HardwareMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type HardwareSpec struct {
	GPU           GPUSpec  `yaml:"gpu"`
	CPU           CPUSpec  `yaml:"cpu"`
	RAM           RAMSpec  `yaml:"ram"`
	UnifiedMemory bool    `yaml:"unified_memory"`
}

type GPUSpec struct {
	Arch              string `yaml:"arch"`
	VRAMMiB           int    `yaml:"vram_mib"`
	ComputeCapability string `yaml:"compute_capability"`
	CUDACores         int    `yaml:"cuda_cores"`
	ResourceName      string `yaml:"resource_name,omitempty"`     // K8s GPU resource name, e.g. "nvidia.com/gpu", "amd.com/gpu"
	RuntimeClassName  string `yaml:"runtime_class_name,omitempty"` // K8s runtimeClassName for GPU containers, e.g. "nvidia"
}

type CPUSpec struct {
	Arch    string  `yaml:"arch"`
	Cores   int     `yaml:"cores"`
	FreqGHz float64 `yaml:"freq_ghz"`
}

type RAMSpec struct {
	TotalMiB     int `yaml:"total_mib"`
	BandwidthGbps int `yaml:"bandwidth_gbps"`
}

type HardwareConstraints struct {
	TDPWatts   int    `yaml:"tdp_watts"`
	PowerModes []int  `yaml:"power_modes"`
	Cooling    string `yaml:"cooling"`
}

type HardwarePartition struct {
	GPUTools []string `yaml:"gpu_tools"`
	CPUTools []string `yaml:"cpu_tools"`
}

// --- Engine Asset ---

// EngineSource describes how to obtain an engine binary for native runtime.
type EngineSource struct {
	Binary    string            `yaml:"binary,omitempty"    json:"binary,omitempty"`
	Platforms []string          `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Download  map[string]string `yaml:"download,omitempty"  json:"download,omitempty"`
	Mirror    map[string]string `yaml:"mirror,omitempty"    json:"mirror,omitempty"`
}

// Supports reports whether this source supports the given platform (e.g. "linux/amd64").
func (s *EngineSource) Supports(platform string) bool {
	for _, p := range s.Platforms {
		if p == platform {
			return true
		}
	}
	return false
}

// EngineRuntime provides runtime selection guidance for engine deployment.
type EngineRuntime struct {
	Default                 string            `yaml:"default,omitempty"                  json:"default,omitempty"`
	PlatformRecommendations map[string]string `yaml:"platform_recommendations,omitempty" json:"platform_recommendations,omitempty"`
}

type EngineAsset struct {
	Kind             string           `yaml:"kind"              json:"kind"`
	Metadata         EngineMetadata   `yaml:"metadata"          json:"metadata"`
	Image            EngineImage      `yaml:"image"             json:"image"`
	Hardware         EngineHardware   `yaml:"hardware"          json:"hardware"`
	Startup          EngineStartup    `yaml:"startup"           json:"startup"`
	API              EngineAPI        `yaml:"api"               json:"api"`
	Amplifier        EngineAmplifier  `yaml:"amplifier"         json:"amplifier"`
	PartitionHints   PartitionHints   `yaml:"partition_hints"   json:"partition_hints"`
	TimeConstraints  TimeConstraints  `yaml:"time_constraints"  json:"time_constraints"`
	PowerConstraints PowerConstraints `yaml:"power_constraints" json:"power_constraints"`
	Runtime          EngineRuntime    `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Patterns         []string         `yaml:"patterns,omitempty" json:"patterns,omitempty"`
	Source           *EngineSource    `yaml:"source,omitempty"  json:"source,omitempty"`
}

type EngineMetadata struct {
	Name    string `yaml:"name"    json:"name"`
	Type    string `yaml:"type"    json:"type"`
	Version string `yaml:"version" json:"version"`
}

type EngineImage struct {
	Name         string   `yaml:"name"           json:"name"`
	Tag          string   `yaml:"tag"            json:"tag"`
	SizeApproxMB int      `yaml:"size_approx_mb" json:"size_approx_mb"`
	Platforms    []string `yaml:"platforms"      json:"platforms"`
	Registries   []string `yaml:"registries"     json:"registries"`
}

type EngineHardware struct {
	GPUArch    string `yaml:"gpu_arch"     json:"gpu_arch"`
	VRAMMinMiB int    `yaml:"vram_min_mib" json:"vram_min_mib"`
}

type EngineStartup struct {
	Command     []string          `yaml:"command"      json:"command"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	DefaultArgs map[string]any    `yaml:"default_args" json:"default_args"`
	HealthCheck HealthCheck       `yaml:"health_check" json:"health_check"`
	Warmup      WarmupConfig      `yaml:"warmup"       json:"warmup"`
}

type HealthCheck struct {
	Path     string `yaml:"path"      json:"path"`
	TimeoutS int    `yaml:"timeout_s" json:"timeout_s"`
}

// WarmupConfig describes how to warm up an engine after health check passes.
type WarmupConfig struct {
	Enabled   bool   `yaml:"enabled"    json:"enabled"`
	Prompt    string `yaml:"prompt"     json:"prompt"`
	MaxTokens int    `yaml:"max_tokens" json:"max_tokens"`
	TimeoutS  int    `yaml:"timeout_s"  json:"timeout_s"`
}

type EngineAPI struct {
	Protocol string `yaml:"protocol"  json:"protocol"`
	BasePath string `yaml:"base_path" json:"base_path"`
}

type EngineAmplifier struct {
	Features          []string        `yaml:"features"           json:"features"`
	PerformanceGain   string          `yaml:"performance_gain"   json:"performance_gain"`
	ResourceExpansion map[string]bool `yaml:"resource_expansion" json:"resource_expansion"`
}

type PartitionHints struct {
	MinGPUMemoryMiB            int `yaml:"min_gpu_memory_mib"           json:"min_gpu_memory_mib"`
	RecommendedGPUCoresPercent int `yaml:"recommended_gpu_cores_percent" json:"recommended_gpu_cores_percent"`
}

type TimeConstraints struct {
	ColdStartS   []int `yaml:"cold_start_s"   json:"cold_start_s"`
	ModelSwitchS []int `yaml:"model_switch_s" json:"model_switch_s"`
}

type PowerConstraints struct {
	TypicalDrawWatts []int `yaml:"typical_draw_watts" json:"typical_draw_watts"`
}

// --- Model Asset ---

type ModelAsset struct {
	Kind     string        `yaml:"kind"`
	Metadata ModelMetadata `yaml:"metadata"`
	Storage  ModelStorage  `yaml:"storage"`
	Variants []ModelVariant `yaml:"variants"`
}

type ModelMetadata struct {
	Name           string `yaml:"name"`
	Type           string `yaml:"type"`
	Family         string `yaml:"family"`
	ParameterCount string `yaml:"parameter_count"`
}

type ModelStorage struct {
	Formats            []string      `yaml:"formats"`
	DefaultPathPattern string        `yaml:"default_path_pattern"`
	Sources            []ModelSource `yaml:"sources"`
}

type ModelSource struct {
	Type   string `yaml:"type"`
	Repo   string `yaml:"repo"`
	Path   string `yaml:"path"`
	Format string `yaml:"format,omitempty"` // e.g. "gguf", "safetensors" — used to pick correct source for engine
}

type ModelVariant struct {
	Name     string              `yaml:"name"`
	Hardware ModelVariantHardware `yaml:"hardware"`
	Engine   string              `yaml:"engine"`
	Format   string              `yaml:"format"`
	Source   *ModelSource        `yaml:"source,omitempty"` // variant-specific download source; overrides global sources when present
	DefaultConfig map[string]any `yaml:"default_config"`
	ExpectedPerformance map[string]any `yaml:"expected_performance"`
}

type ModelVariantHardware struct {
	GPUArch       string `yaml:"gpu_arch"`
	VRAMMinMiB    int    `yaml:"vram_min_mib"`
	UnifiedMemory *bool  `yaml:"unified_memory,omitempty"`
}

// --- Stack Component ---

type StackConditions struct {
	SkipProfiles     []string `yaml:"skip_profiles,omitempty"`
	RequiredProfiles []string `yaml:"required_profiles,omitempty"`
}

type StackComponent struct {
	Kind          string               `yaml:"kind"`
	Metadata      StackMetadata        `yaml:"metadata"`
	Compatibility StackCompatibility   `yaml:"compatibility"`
	Source        StackSource          `yaml:"source"`
	Install       StackInstall         `yaml:"install"`
	Verify        StackVerify          `yaml:"verify"`
	Conditions    *StackConditions     `yaml:"conditions,omitempty"`
	Profiles      map[string]StackProfile `yaml:"profiles,omitempty"`
	Registries    map[string]any       `yaml:"registries,omitempty"`   // container registry mirror config (written as-is to registries.yaml)
	OpenQuestions []StackQuestion      `yaml:"open_questions,omitempty"`
}

type StackMetadata struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
}

type StackCompatibility struct {
	AIMAMin string `yaml:"aima_min"`
}

type StackSource struct {
	Binary         string            `yaml:"binary,omitempty"`
	Chart          string            `yaml:"chart,omitempty"`
	Airgap         string            `yaml:"airgap,omitempty"`           // airgap image tar filename (stored in dist/)
	Platforms      []string          `yaml:"platforms"`
	Download       map[string]string `yaml:"download,omitempty"`         // platform → URL
	Mirror         map[string]string `yaml:"mirror,omitempty"`           // platform → fallback URL
	AirgapDownload map[string]string `yaml:"airgap_download,omitempty"` // platform → airgap tar URL
	AirgapMirror   map[string]string `yaml:"airgap_mirror,omitempty"`   // platform → airgap tar mirror URL
}

type StackInstall struct {
	Method      string            `yaml:"method"`
	Daemon      bool              `yaml:"daemon,omitempty"`
	Subcommand  string            `yaml:"subcommand,omitempty"`   // daemon ExecStart subcommand (default "server")
	ServiceType string            `yaml:"service_type,omitempty"` // systemd Type= (default "notify")
	Priority    int               `yaml:"priority,omitempty"`     // lower = installed first (default 0)
	Args        []StackArg        `yaml:"args,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
	Helm        *StackHelm        `yaml:"helm,omitempty"`
}

type StackArg struct {
	Flag      string `yaml:"flag"`
	Rationale string `yaml:"rationale"`
	Source    string `yaml:"source"`
	Verified  bool   `yaml:"verified"`
}

type StackHelm struct {
	Chart     string         `yaml:"chart"`
	Namespace string         `yaml:"namespace"`
	Values    map[string]any `yaml:"values,omitempty"`
}

type StackVerify struct {
	Command        string           `yaml:"command"`
	ReadyCondition string           `yaml:"ready_condition"`
	TimeoutS       int              `yaml:"timeout_s"`
	Pods           []StackVerifyPod `yaml:"pods,omitempty"`
}

type StackVerifyPod struct {
	Namespace string `yaml:"namespace"`
	Label     string `yaml:"label"`
	MinReady  int    `yaml:"min_ready"`
}

type StackProfile struct {
	ExtraArgs []StackArg `yaml:"extra_args,omitempty"`
	ExtraEnv  map[string]string `yaml:"extra_env,omitempty"`
}

type StackQuestion struct {
	Question   string `yaml:"question"`
	Hypothesis string `yaml:"hypothesis"`
	TestMethod string `yaml:"test_method"`
}

// --- Partition Strategy ---

type PartitionStrategy struct {
	Kind     string             `yaml:"kind"`
	Metadata PartitionMetadata  `yaml:"metadata"`
	Target   PartitionTarget    `yaml:"target"`
	Slots    []PartitionSlotDef `yaml:"slots"`
}

type PartitionMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type PartitionTarget struct {
	HardwareProfile string `yaml:"hardware_profile"`
	WorkloadPattern string `yaml:"workload_pattern"`
}

type PartitionSlotDef struct {
	Name string         `yaml:"name"`
	GPU  SlotGPU        `yaml:"gpu"`
	CPU  SlotCPU        `yaml:"cpu"`
	RAM  SlotRAM        `yaml:"ram"`
	Note string         `yaml:"note,omitempty"`
}

type SlotGPU struct {
	MemoryMiB    int `yaml:"memory_mib"`
	CoresPercent int `yaml:"cores_percent"`
}

type SlotCPU struct {
	Cores int `yaml:"cores"`
}

type SlotRAM struct {
	MiB int `yaml:"mib"`
}

// LoadCatalog loads and parses all YAML knowledge assets from an fs.FS.
func LoadCatalog(fsys fs.FS) (*Catalog, error) {
	cat := &Catalog{}

	dirs := []string{"hardware", "engines", "models", "partitions", "stack"}
	for _, dir := range dirs {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			// Directory doesn't exist: skip silently
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			path := dir + "/" + entry.Name()
			data, err := fs.ReadFile(fsys, path)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			if err := cat.parseAsset(data, path); err != nil {
				return nil, err
			}
		}
	}
	return cat, nil
}

// kindProbe extracts just the "kind" field from YAML without full parsing.
type kindProbe struct {
	Kind string `yaml:"kind"`
}

func (cat *Catalog) parseAsset(data []byte, path string) error {
	var probe kindProbe
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	switch probe.Kind {
	case "hardware_profile":
		var hp HardwareProfile
		if err := yaml.Unmarshal(data, &hp); err != nil {
			return fmt.Errorf("parse hardware profile %s: %w", path, err)
		}
		cat.HardwareProfiles = append(cat.HardwareProfiles, hp)
	case "engine_asset":
		var ea EngineAsset
		if err := yaml.Unmarshal(data, &ea); err != nil {
			return fmt.Errorf("parse engine asset %s: %w", path, err)
		}
		cat.EngineAssets = append(cat.EngineAssets, ea)
	case "model_asset":
		var ma ModelAsset
		if err := yaml.Unmarshal(data, &ma); err != nil {
			return fmt.Errorf("parse model asset %s: %w", path, err)
		}
		cat.ModelAssets = append(cat.ModelAssets, ma)
	case "partition_strategy":
		var ps PartitionStrategy
		if err := yaml.Unmarshal(data, &ps); err != nil {
			return fmt.Errorf("parse partition strategy %s: %w", path, err)
		}
		cat.PartitionStrategies = append(cat.PartitionStrategies, ps)
	case "stack_component":
		var sc StackComponent
		if err := yaml.Unmarshal(data, &sc); err != nil {
			return fmt.Errorf("parse stack component %s: %w", path, err)
		}
		cat.StackComponents = append(cat.StackComponents, sc)
	default:
		// Unknown kind: skip silently
	}
	return nil
}

// LoadToSQLite loads a parsed Catalog into SQLite relational tables.
// It clears all static knowledge tables first, then inserts fresh data.
// Dynamic tables (configurations, benchmark_results, etc.) are untouched.
func LoadToSQLite(ctx context.Context, db *sql.DB, cat *Catalog) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin load tx: %w", err)
	}
	defer tx.Rollback()

	// Clear static tables (child tables first for FK)
	for _, t := range []string{
		"engine_hardware_compat", "engine_features", "model_variants",
		"partition_strategies", "engine_assets", "model_assets", "hardware_profiles",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return fmt.Errorf("clear %s: %w", t, err)
		}
	}

	// Build hardware profile ID → gpu_arch map for variant matching
	hwByArch := make(map[string][]string) // gpu_arch → []profile_id
	var hwIDs []string

	for _, hp := range cat.HardwareProfiles {
		id := hp.Metadata.Name
		hwIDs = append(hwIDs, id)
		hwByArch[hp.Hardware.GPU.Arch] = append(hwByArch[hp.Hardware.GPU.Arch], id)

		powerJSON, _ := json.Marshal(hp.Constraints.PowerModes)
		toolsJSON, _ := json.Marshal(hp.Partition.GPUTools)
		rawYAML, _ := yaml.Marshal(hp)

		_, err := tx.ExecContext(ctx,
			`INSERT INTO hardware_profiles (id, name, gpu_arch, gpu_vram_mib, gpu_compute_cap, cpu_arch, cpu_cores, ram_mib, unified_memory, tdp_watts, power_modes, gpu_tools, raw_yaml)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, hp.Metadata.Name, hp.Hardware.GPU.Arch, hp.Hardware.GPU.VRAMMiB,
			hp.Hardware.GPU.ComputeCapability, hp.Hardware.CPU.Arch, hp.Hardware.CPU.Cores,
			hp.Hardware.RAM.TotalMiB, hp.Hardware.UnifiedMemory, hp.Constraints.TDPWatts,
			string(powerJSON), string(toolsJSON), string(rawYAML))
		if err != nil {
			return fmt.Errorf("insert hardware_profile %s: %w", id, err)
		}
	}

	for _, ea := range cat.EngineAssets {
		id := ea.Metadata.Name
		var csMin, csMax, pwMin, pwMax int
		if len(ea.TimeConstraints.ColdStartS) >= 2 {
			csMin, csMax = ea.TimeConstraints.ColdStartS[0], ea.TimeConstraints.ColdStartS[1]
		}
		if len(ea.PowerConstraints.TypicalDrawWatts) >= 2 {
			pwMin, pwMax = ea.PowerConstraints.TypicalDrawWatts[0], ea.PowerConstraints.TypicalDrawWatts[1]
		}
		rawYAML, _ := yaml.Marshal(ea)

		_, err := tx.ExecContext(ctx,
			`INSERT INTO engine_assets (id, type, version, image_name, image_tag, image_size_mb, api_protocol, cold_start_s_min, cold_start_s_max, power_watts_min, power_watts_max, perf_gain_desc, raw_yaml)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, ea.Metadata.Type, ea.Metadata.Version, ea.Image.Name, ea.Image.Tag,
			ea.Image.SizeApproxMB, ea.API.Protocol, csMin, csMax, pwMin, pwMax,
			ea.Amplifier.PerformanceGain, string(rawYAML))
		if err != nil {
			return fmt.Errorf("insert engine_asset %s: %w", id, err)
		}

		// Engine features
		for _, feat := range ea.Amplifier.Features {
			if feat == "" {
				continue
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO engine_features (engine_id, feature) VALUES (?, ?)`, id, feat)
			if err != nil {
				return fmt.Errorf("insert engine_feature %s/%s: %w", id, feat, err)
			}
		}

		// Engine-hardware compatibility
		cpuOffload := ea.Amplifier.ResourceExpansion["cpu_offload"]
		ssdOffload := ea.Amplifier.ResourceExpansion["ssd_offload"]
		npuOffload := ea.Amplifier.ResourceExpansion["npu_offload"]

		if ea.Hardware.GPUArch == "*" {
			// Universal engine: compatible with all hardware profiles
			for _, hwID := range hwIDs {
				_, err := tx.ExecContext(ctx,
					`INSERT INTO engine_hardware_compat (engine_id, hardware_id, vram_min_mib, cpu_offload, ssd_offload, npu_offload, min_gpu_mem_mib, recommended_cores_pct)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					id, hwID, ea.Hardware.VRAMMinMiB, cpuOffload, ssdOffload, npuOffload,
					ea.PartitionHints.MinGPUMemoryMiB, ea.PartitionHints.RecommendedGPUCoresPercent)
				if err != nil {
					return fmt.Errorf("insert engine_hardware_compat %s/%s: %w", id, hwID, err)
				}
			}
		} else {
			// Match by gpu_arch
			for _, hwID := range hwByArch[ea.Hardware.GPUArch] {
				_, err := tx.ExecContext(ctx,
					`INSERT INTO engine_hardware_compat (engine_id, hardware_id, vram_min_mib, cpu_offload, ssd_offload, npu_offload, min_gpu_mem_mib, recommended_cores_pct)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					id, hwID, ea.Hardware.VRAMMinMiB, cpuOffload, ssdOffload, npuOffload,
					ea.PartitionHints.MinGPUMemoryMiB, ea.PartitionHints.RecommendedGPUCoresPercent)
				if err != nil {
					return fmt.Errorf("insert engine_hardware_compat %s/%s: %w", id, hwID, err)
				}
			}
		}
	}

	for _, ma := range cat.ModelAssets {
		id := ma.Metadata.Name
		formatsJSON, _ := json.Marshal(ma.Storage.Formats)
		sourcesJSON, _ := json.Marshal(ma.Storage.Sources)
		rawYAML, _ := yaml.Marshal(ma)

		_, err := tx.ExecContext(ctx,
			`INSERT INTO model_assets (id, name, type, family, param_count, formats, sources, raw_yaml)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, ma.Metadata.Name, ma.Metadata.Type, ma.Metadata.Family,
			ma.Metadata.ParameterCount, string(formatsJSON), string(sourcesJSON), string(rawYAML))
		if err != nil {
			return fmt.Errorf("insert model_asset %s: %w", id, err)
		}

		// Model variants
		for _, v := range ma.Variants {
			configJSON, _ := json.Marshal(v.DefaultConfig)
			perfJSON, _ := json.Marshal(v.ExpectedPerformance)

			// Find matching hardware profiles by gpu_arch
			var matchedHWIDs []string
			if v.Hardware.GPUArch == "*" {
				matchedHWIDs = hwIDs
			} else {
				matchedHWIDs = hwByArch[v.Hardware.GPUArch]
			}

			for _, hwID := range matchedHWIDs {
				variantID := v.Name
				if len(matchedHWIDs) > 1 {
					variantID = v.Name + "-" + hwID
				}
				_, err := tx.ExecContext(ctx,
					`INSERT INTO model_variants (id, model_id, hardware_id, engine_type, format, default_config, expected_perf, vram_min_mib)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					variantID, id, hwID, v.Engine, v.Format,
					string(configJSON), string(perfJSON), v.Hardware.VRAMMinMiB)
				if err != nil {
					return fmt.Errorf("insert model_variant %s: %w", variantID, err)
				}
			}
		}
	}

	for _, ps := range cat.PartitionStrategies {
		id := ps.Metadata.Name
		slotsJSON, _ := json.Marshal(ps.Slots)
		rawYAML, _ := yaml.Marshal(ps)

		// hardware_id: use target.hardware_profile, or "*" for wildcard
		hwID := ps.Target.HardwareProfile

		_, err := tx.ExecContext(ctx,
			`INSERT INTO partition_strategies (id, hardware_id, workload_pattern, slots, raw_yaml)
			 VALUES (?, ?, ?, ?, ?)`,
			id, hwID, ps.Target.WorkloadPattern, string(slotsJSON), string(rawYAML))
		if err != nil {
			return fmt.Errorf("insert partition_strategy %s: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit load tx: %w", err)
	}
	return nil
}
