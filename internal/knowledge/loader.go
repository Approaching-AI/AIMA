package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
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
	Kind        string              `yaml:"kind"`
	Metadata    HardwareMetadata    `yaml:"metadata"`
	Hardware    HardwareSpec        `yaml:"hardware"`
	Constraints HardwareConstraints `yaml:"constraints"`
	Partition   HardwarePartition   `yaml:"partition"`
	Container   *ContainerAccess    `yaml:"container,omitempty"`
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
	ComputeID    string `yaml:"compute_id"`
	ComputeUnits int    `yaml:"compute_units"`
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

// ContainerAccess describes vendor-specific container access requirements
// (devices, env vars, volumes, security) for GPU containers. Lives in
// hardware profile YAML so adding a new GPU vendor = YAML only, no Go code.
type ContainerAccess struct {
	Devices  []string          `yaml:"devices,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
	Volumes  []ContainerVolume `yaml:"volumes,omitempty"`
	Security *ContainerSecurity `yaml:"security,omitempty"`
}

type ContainerVolume struct {
	Name      string `yaml:"name"`
	HostPath  string `yaml:"host_path"`
	MountPath string `yaml:"mount_path"`
	ReadOnly  bool   `yaml:"read_only,omitempty"`
}

type ContainerSecurity struct {
	Privileged         bool  `yaml:"privileged,omitempty"`
	RunAsUser          *int  `yaml:"run_as_user,omitempty"`
	SupplementalGroups []int `yaml:"supplemental_groups,omitempty"`
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
	Name             string   `yaml:"name"              json:"name"`
	Type             string   `yaml:"type"              json:"type"`
	Version          string   `yaml:"version"           json:"version"`
	Default          bool     `yaml:"default,omitempty" json:"default,omitempty"`
	SupportedFormats []string `yaml:"supported_formats,omitempty" json:"supported_formats,omitempty"`
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
	Command      []string          `yaml:"command"                    json:"command"`
	InitCommands []string          `yaml:"init_commands,omitempty"    json:"init_commands,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"              json:"env,omitempty"`
	DefaultArgs  map[string]any    `yaml:"default_args"               json:"default_args"`
	HealthCheck  HealthCheck       `yaml:"health_check"               json:"health_check"`
	Warmup       WarmupConfig      `yaml:"warmup"                     json:"warmup"`
	ExtraVolumes []EngineVolume    `yaml:"extra_volumes,omitempty"    json:"extra_volumes,omitempty"`
}

// EngineVolume describes a host path to mount into the container.
type EngineVolume struct {
	Name      string `yaml:"name"       json:"name"`
	HostPath  string `yaml:"host_path"  json:"host_path"`
	MountPath string `yaml:"mount_path" json:"mount_path"`
	ReadOnly  bool   `yaml:"read_only"  json:"read_only"`
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

// ParseAssetPublic is an exported wrapper around parseAsset for validation.
func (cat *Catalog) ParseAssetPublic(data []byte, path string) error {
	return cat.parseAsset(data, path)
}

// LoadCatalogLenient loads YAML assets like LoadCatalog but continues on
// per-file errors instead of failing the entire load. Returns successfully
// parsed assets plus a list of warning messages for files that failed.
func LoadCatalogLenient(fsys fs.FS) (*Catalog, []string) {
	cat := &Catalog{}
	var warnings []string

	dirs := []string{"hardware", "engines", "models", "partitions", "stack"}
	for _, dir := range dirs {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			path := dir + "/" + entry.Name()
			data, err := fs.ReadFile(fsys, path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("read %s: %v", path, err))
				continue
			}
			if err := cat.parseAsset(data, path); err != nil {
				warnings = append(warnings, fmt.Sprintf("parse %s: %v", path, err))
				continue
			}
		}
	}
	return cat, warnings
}

// overlayProbe extracts _base_digest from an overlay YAML before full parsing.
type overlayProbe struct {
	Kind       string `yaml:"kind"`
	BaseDigest string `yaml:"_base_digest"`
}

// ComputeDigests walks an fs.FS and computes SHA256 digests of each YAML file,
// keyed by the asset's metadata.name. Used to detect overlay staleness.
func ComputeDigests(fsys fs.FS) map[string]string {
	digests := make(map[string]string)
	dirs := []string{"hardware", "engines", "models", "partitions", "stack"}
	for _, dir := range dirs {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			data, err := fs.ReadFile(fsys, dir+"/"+entry.Name())
			if err != nil {
				continue
			}
			name := extractName(data)
			if name == "" {
				continue
			}
			h := sha256.Sum256(data)
			digests[name] = "sha256:" + hex.EncodeToString(h[:])
		}
	}
	return digests
}

// nameProbe extracts just the metadata.name from YAML.
type nameProbe struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

func extractName(data []byte) string {
	var p nameProbe
	if err := yaml.Unmarshal(data, &p); err != nil {
		return ""
	}
	return p.Metadata.Name
}

// MergeCatalog merges overlay into base. Overlay assets with the same
// metadata.name replace the base asset; new names are appended.
// Returns the mutated base catalog.
func MergeCatalog(base, overlay *Catalog) *Catalog {
	base.HardwareProfiles = mergeSlice(base.HardwareProfiles, overlay.HardwareProfiles, func(v HardwareProfile) string { return v.Metadata.Name })
	base.EngineAssets = mergeSlice(base.EngineAssets, overlay.EngineAssets, func(v EngineAsset) string { return v.Metadata.Name })
	base.ModelAssets = mergeSlice(base.ModelAssets, overlay.ModelAssets, func(v ModelAsset) string { return v.Metadata.Name })
	base.PartitionStrategies = mergeSlice(base.PartitionStrategies, overlay.PartitionStrategies, func(v PartitionStrategy) string { return v.Metadata.Name })
	base.StackComponents = mergeSlice(base.StackComponents, overlay.StackComponents, func(v StackComponent) string { return v.Metadata.Name })
	return base
}

// MergeCatalogWithDigests merges overlay into base and checks for staleness.
// factoryDigests maps asset metadata.name → SHA256 digest of the factory YAML.
// overlayFS is the overlay filesystem used to extract _base_digest from overlay files.
// Returns the merged catalog and any staleness warnings.
func MergeCatalogWithDigests(base, overlay *Catalog, factoryDigests map[string]string, overlayFS fs.FS) (*Catalog, []string) {
	// Collect overlay _base_digest values from the raw YAML files
	overlayDigests := extractOverlayDigests(overlayFS)

	// Collect overlay asset names (before merge) to check staleness
	overlayNames := CollectNames(overlay)

	// Merge
	base = MergeCatalog(base, overlay)

	// Check staleness for each overlay asset that shadows a factory asset
	var warnings []string
	for name := range overlayNames {
		factoryD, inFactory := factoryDigests[name]
		if !inFactory {
			continue // new asset, no staleness concern
		}
		overlayBaseD, hasBaseDigest := overlayDigests[name]
		if !hasBaseDigest {
			slog.Info("overlay shadows factory asset (no _base_digest, staleness unknown)", "asset", name)
			continue
		}
		if overlayBaseD != factoryD {
			w := fmt.Sprintf("overlay %q is stale: factory YAML changed (overlay base_digest=%s, factory=%s) — review recommended", name, overlayBaseD, factoryD)
			warnings = append(warnings, w)
		}
	}
	return base, warnings
}

// CollectNames returns a set of all metadata.name values in the catalog.
func CollectNames(cat *Catalog) map[string]bool {
	names := make(map[string]bool)
	for _, v := range cat.HardwareProfiles {
		names[v.Metadata.Name] = true
	}
	for _, v := range cat.EngineAssets {
		names[v.Metadata.Name] = true
	}
	for _, v := range cat.ModelAssets {
		names[v.Metadata.Name] = true
	}
	for _, v := range cat.PartitionStrategies {
		names[v.Metadata.Name] = true
	}
	for _, v := range cat.StackComponents {
		names[v.Metadata.Name] = true
	}
	return names
}

func extractOverlayDigests(fsys fs.FS) map[string]string {
	if fsys == nil {
		return nil
	}
	digests := make(map[string]string)
	dirs := []string{"hardware", "engines", "models", "partitions", "stack"}
	for _, dir := range dirs {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			data, err := fs.ReadFile(fsys, dir+"/"+entry.Name())
			if err != nil {
				continue
			}
			var probe overlayProbe
			if err := yaml.Unmarshal(data, &probe); err != nil {
				continue
			}
			name := extractName(data)
			if name != "" && probe.BaseDigest != "" {
				digests[name] = probe.BaseDigest
			}
		}
	}
	return digests
}

// ExtractOverlayDigestsFromDir reads overlay _base_digest values from a directory path.
func ExtractOverlayDigestsFromDir(dir string) map[string]string {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return extractOverlayDigests(os.DirFS(dir))
}

// mergeSlice merges overlay items into base by key. Same key = replace, new key = append.
func mergeSlice[T any](base, overlay []T, key func(T) string) []T {
	if len(overlay) == 0 {
		return base
	}
	idx := make(map[string]int, len(base))
	for i, v := range base {
		idx[key(v)] = i
	}
	for _, v := range overlay {
		if i, ok := idx[key(v)]; ok {
			base[i] = v // replace
		} else {
			base = append(base, v)
		}
	}
	return base
}

// KindToDir maps YAML kind values to catalog subdirectory names.
func KindToDir(kind string) string {
	switch kind {
	case "engine_asset":
		return "engines"
	case "model_asset":
		return "models"
	case "hardware_profile":
		return "hardware"
	case "partition_strategy":
		return "partitions"
	case "stack_component":
		return "stack"
	default:
		return ""
	}
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
			`INSERT INTO hardware_profiles (id, name, gpu_arch, gpu_vram_mib, gpu_compute_id, cpu_arch, cpu_cores, ram_mib, unified_memory, tdp_watts, power_modes, gpu_tools, raw_yaml)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, hp.Metadata.Name, hp.Hardware.GPU.Arch, hp.Hardware.GPU.VRAMMiB,
			hp.Hardware.GPU.ComputeID, hp.Hardware.CPU.Arch, hp.Hardware.CPU.Cores,
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
