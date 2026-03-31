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
	"sync"

	"gopkg.in/yaml.v3"
)

// Catalog holds all knowledge assets loaded from embedded YAML files.
type Catalog struct {
	mu                  sync.Mutex
	HardwareProfiles    []HardwareProfile
	PartitionStrategies []PartitionStrategy
	EngineAssets        []EngineAsset
	RawEngineAssets     []EngineAsset // unresolved engine assets before profile inheritance/template expansion
	ModelAssets         []ModelAsset
	StackComponents     []StackComponent
	DeploymentScenarios []DeploymentScenario
	EngineProfiles      map[string]*EngineProfile // name -> profile (loaded from engines/profiles/)
}

// EngineProfile captures the shared identity of an engine type.
// Assets reference a profile via `_profile: <name>` and inherit all zero-value fields.
type EngineProfile struct {
	Kind           string          `yaml:"kind"`
	Metadata       ProfileMeta     `yaml:"metadata"`
	Startup        EngineStartup   `yaml:"startup"`
	API            EngineAPI       `yaml:"api"`
	Amplifier      EngineAmplifier `yaml:"amplifier"`
	PartitionHints PartitionHints  `yaml:"partition_hints"`
}

type ProfileMeta struct {
	Name             string   `yaml:"name"`
	VersionDefault   string   `yaml:"version_default"`
	SupportedFormats []string `yaml:"supported_formats"`
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
	GPU           GPUSpec `yaml:"gpu"`
	CPU           CPUSpec `yaml:"cpu"`
	RAM           RAMSpec `yaml:"ram"`
	UnifiedMemory bool    `yaml:"unified_memory"`
}

type GPUSpec struct {
	Arch             string `yaml:"arch"`
	VRAMMiB          int    `yaml:"vram_mib"`
	ComputeID        string `yaml:"compute_id"`
	ComputeUnits     int    `yaml:"compute_units"`
	ResourceName     string `yaml:"resource_name,omitempty"`      // K8s GPU resource name, e.g. "nvidia.com/gpu", "amd.com/gpu"
	RuntimeClassName string `yaml:"runtime_class_name,omitempty"` // K8s runtimeClassName for GPU containers, e.g. "nvidia"
}

type CPUSpec struct {
	Arch    string  `yaml:"arch"`
	Cores   int     `yaml:"cores"`
	FreqGHz float64 `yaml:"freq_ghz"`
}

type RAMSpec struct {
	TotalMiB      int `yaml:"total_mib"`
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
	Devices            []string           `yaml:"devices,omitempty"`
	Env                map[string]string  `yaml:"env,omitempty"`
	PartitionRemoveEnv []string           `yaml:"partition_remove_env,omitempty"`
	Volumes            []ContainerVolume  `yaml:"volumes,omitempty"`
	Security           *ContainerSecurity `yaml:"security,omitempty"`
	DockerRuntime      string             `yaml:"docker_runtime,omitempty"` // --runtime flag (e.g. "ascend")
	NetworkMode        string             `yaml:"network_mode,omitempty"`   // "host" for --network host
	ShmSize            string             `yaml:"shm_size,omitempty"`       // --shm-size (e.g. "500g")
	Init               bool               `yaml:"init,omitempty"`           // --init flag
}

type ContainerVolume struct {
	Name      string `yaml:"name"       json:"name"`
	HostPath  string `yaml:"host_path"  json:"host_path"`
	MountPath string `yaml:"mount_path" json:"mount_path"`
	ReadOnly  bool   `yaml:"read_only,omitempty"  json:"read_only,omitempty"`
}

type ContainerSecurity struct {
	Privileged         bool  `yaml:"privileged,omitempty"`
	RunAsUser          *int  `yaml:"run_as_user,omitempty"`
	SupplementalGroups []int `yaml:"supplemental_groups,omitempty"`
}

// --- Engine Asset ---

// EngineSourceProbe describes how to detect a pre-installed engine binary.
type EngineSourceProbe struct {
	Paths           []string `yaml:"paths,omitempty"            json:"paths,omitempty"`
	VersionCommand  []string `yaml:"version_command,omitempty"  json:"version_command,omitempty"`
	VersionPattern  string   `yaml:"version_pattern,omitempty"  json:"version_pattern,omitempty"`
	FallbackVersion string   `yaml:"fallback_version,omitempty" json:"fallback_version,omitempty"`
}

// EngineSource describes how to obtain an engine binary for native runtime.
type EngineSource struct {
	Binary          string              `yaml:"binary,omitempty"           json:"binary,omitempty"`
	Platforms       []string            `yaml:"platforms,omitempty"        json:"platforms,omitempty"`
	Download        map[string]string   `yaml:"download,omitempty"         json:"download,omitempty"`
	Mirror          map[string][]string `yaml:"mirror,omitempty"           json:"mirror,omitempty"`
	SHA256          map[string]string   `yaml:"sha256,omitempty"           json:"sha256,omitempty"`
	InstallType     string              `yaml:"install_type,omitempty"     json:"install_type,omitempty"`
	Probe           *EngineSourceProbe  `yaml:"probe,omitempty"            json:"probe,omitempty"`
	URLTemplate     string              `yaml:"url_template,omitempty"     json:"url_template,omitempty"`
	PlatformFiles   map[string]string   `yaml:"platform_files,omitempty"   json:"platform_files,omitempty"`
	MirrorTemplates []string            `yaml:"mirror_templates,omitempty" json:"mirror_templates,omitempty"`
}

// Supports reports whether this source supports the given platform (e.g. "linux/amd64").
func (s *EngineSource) Supports(platform string) bool {
	if s == nil {
		return false
	}
	if s.InstallType == "preinstalled" && len(s.Platforms) == 0 {
		return true
	}
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
	Profile          string           `yaml:"_profile,omitempty" json:"-"`
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
	OpenQuestions    []StackQuestion  `yaml:"open_questions,omitempty" json:"open_questions,omitempty"`
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
	Digest       string   `yaml:"digest,omitempty" json:"digest,omitempty"`
	Distribution string   `yaml:"distribution,omitempty" json:"distribution,omitempty"` // "registry" (default) or "local"
}

type EngineHardware struct {
	GPUArch    string `yaml:"gpu_arch"     json:"gpu_arch"`
	VRAMMinMiB int    `yaml:"vram_min_mib" json:"vram_min_mib"`
}

type EngineStartup struct {
	Command      []string            `yaml:"command"                    json:"command"`
	InitCommands []string            `yaml:"init_commands,omitempty"    json:"init_commands,omitempty"`
	Env          map[string]string   `yaml:"env,omitempty"              json:"env,omitempty"`
	WorkDir      string              `yaml:"work_dir,omitempty"         json:"work_dir,omitempty"`
	Ports        []StartupPort       `yaml:"ports,omitempty"            json:"ports,omitempty"`
	DefaultArgs  map[string]any      `yaml:"default_args"               json:"default_args"`
	HealthCheck  HealthCheck         `yaml:"health_check"               json:"health_check"`
	Warmup       WarmupConfig        `yaml:"warmup"                     json:"warmup"`
	ExtraVolumes []ContainerVolume   `yaml:"extra_volumes,omitempty"    json:"extra_volumes,omitempty"`
	LogPatterns  *StartupLogPatterns `yaml:"log_patterns,omitempty"   json:"log_patterns,omitempty"`
}

// StartupPort describes a named listening port that should be supplied to the
// engine command. This keeps port flag shape in YAML instead of hardcoding it
// in Go or embedding literal port values in startup.command.
type StartupPort struct {
	Name      string `yaml:"name"                  json:"name"`
	Flag      string `yaml:"flag,omitempty"        json:"flag,omitempty"`
	ConfigKey string `yaml:"config_key,omitempty"  json:"config_key,omitempty"`
	Primary   bool   `yaml:"primary,omitempty"     json:"primary,omitempty"`
}

type StartupLogPatterns struct {
	Phases []StartupPhasePattern `yaml:"phases" json:"phases"`
	Errors []StartupErrorPattern `yaml:"errors" json:"errors"`
}

type StartupPhasePattern struct {
	Name               string `yaml:"name"                             json:"name"`
	Pattern            string `yaml:"pattern"                          json:"pattern"`
	Progress           int    `yaml:"progress,omitempty"               json:"progress,omitempty"`
	ProgressRegexGroup int    `yaml:"progress_regex_group,omitempty"   json:"progress_regex_group,omitempty"`
	ProgressBase       int    `yaml:"progress_base,omitempty"          json:"progress_base,omitempty"`
	ProgressRange      int    `yaml:"progress_range,omitempty"         json:"progress_range,omitempty"`
}

type StartupErrorPattern struct {
	Pattern string `yaml:"pattern" json:"pattern"`
	Message string `yaml:"message" json:"message"`
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
	Features                []string        `yaml:"features"                    json:"features"`
	PerformanceGain         string          `yaml:"performance_gain"            json:"performance_gain"`
	ResourceExpansion       map[string]bool `yaml:"resource_expansion"          json:"resource_expansion"`
	PerformanceMultiplier   float64         `yaml:"performance_multiplier"      json:"performance_multiplier"`
	ExtendsResourceBoundary bool            `yaml:"extends_resource_boundary"   json:"extends_resource_boundary"`
	EffectiveVRAMMultiplier float64         `yaml:"effective_vram_multiplier"   json:"effective_vram_multiplier"`
	OffloadConfigKey        string          `yaml:"offload_config_key"          json:"offload_config_key,omitempty"`
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
	Kind     string         `yaml:"kind"`
	Metadata ModelMetadata  `yaml:"metadata"`
	Storage  ModelStorage   `yaml:"storage"`
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
	Type         string `yaml:"type"`
	Repo         string `yaml:"repo"`
	Path         string `yaml:"path"`
	Format       string `yaml:"format,omitempty"`       // e.g. "gguf", "safetensors" — used to pick correct source for engine
	Quantization string `yaml:"quantization,omitempty"` // e.g. "gptq", "fp8" — used to pick the correct repo/source for the resolved variant
}

type ModelVariant struct {
	Name                string               `yaml:"name"`
	Hardware            ModelVariantHardware `yaml:"hardware"`
	Engine              string               `yaml:"engine"`
	Format              string               `yaml:"format"`
	Source              *ModelSource         `yaml:"source,omitempty"` // variant-specific download source; overrides global sources when present
	DefaultConfig       map[string]any       `yaml:"default_config"`
	ExpectedPerformance map[string]any       `yaml:"expected_performance"`
}

// ExpectedPerf holds structured performance estimates extracted from a variant's
// ExpectedPerformance map. Zero-valued fields mean "not specified".
type ExpectedPerf struct {
	StartupTimeS    int        // model loading time (seconds)
	ColdStartTimeS  int        // full cold start time (seconds)
	TokensPerSecond [2]float64 // [min, max] throughput estimate
	VRAMMiB         int        // expected VRAM usage
	RAMMiB          int        // engine process RAM overhead
	CPUCores        int        // recommended CPU allocation
	DiskMiB         int        // model file size on disk
}

// ParsedExpectedPerf extracts structured performance fields from the variant's
// ExpectedPerformance map. Missing or non-numeric fields produce zero values.
func (v *ModelVariant) ParsedExpectedPerf() ExpectedPerf {
	var p ExpectedPerf
	if v.ExpectedPerformance == nil {
		return p
	}
	p.StartupTimeS = int(toFloat64(v.ExpectedPerformance["startup_time_s"]))
	p.ColdStartTimeS = int(toFloat64(v.ExpectedPerformance["cold_start_time_s"]))
	p.VRAMMiB = int(toFloat64(v.ExpectedPerformance["vram_mib"]))
	p.RAMMiB = int(toFloat64(v.ExpectedPerformance["ram_mib"]))
	p.CPUCores = int(toFloat64(v.ExpectedPerformance["cpu_cores"]))
	p.DiskMiB = int(toFloat64(v.ExpectedPerformance["disk_mib"]))
	if tps, ok := v.ExpectedPerformance["tokens_per_second"]; ok {
		if arr, ok := tps.([]any); ok && len(arr) >= 2 {
			p.TokensPerSecond[0] = toFloat64(arr[0])
			p.TokensPerSecond[1] = toFloat64(arr[1])
		}
	}
	return p
}

type ModelVariantHardware struct {
	GPUArch       string `yaml:"gpu_arch"`
	GPUModel      string `yaml:"gpu_model,omitempty"`
	VRAMMinMiB    int    `yaml:"vram_min_mib"`
	GPUCountMin   int    `yaml:"gpu_count_min,omitempty"` // minimum GPUs required (0 = any; typically matches tensor_parallel_size)
	UnifiedMemory *bool  `yaml:"unified_memory,omitempty"`
}

// --- Stack Component ---

type StackConditions struct {
	SkipProfiles     []string `yaml:"skip_profiles,omitempty"`
	RequiredProfiles []string `yaml:"required_profiles,omitempty"`
}

type StackComponent struct {
	Kind          string                  `yaml:"kind"`
	Metadata      StackMetadata           `yaml:"metadata"`
	Compatibility StackCompatibility      `yaml:"compatibility"`
	Source        StackSource             `yaml:"source"`
	Install       StackInstall            `yaml:"install"`
	Verify        StackVerify             `yaml:"verify"`
	Conditions    *StackConditions        `yaml:"conditions,omitempty"`
	Profiles      map[string]StackProfile `yaml:"profiles,omitempty"`
	Registries    map[string]any          `yaml:"registries,omitempty"` // container registry mirror config (written as-is to registries.yaml)
	OpenQuestions []StackQuestion         `yaml:"open_questions,omitempty"`
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
	Binary          string              `yaml:"binary,omitempty"`
	Chart           string              `yaml:"chart,omitempty"`
	Archive         string              `yaml:"archive,omitempty"`          // archive filename (e.g. docker-27.5.1.tgz)
	ExtractBinaries []string            `yaml:"extract_binaries,omitempty"` // paths within archive to extract (e.g. "docker/dockerd")
	Airgap          string              `yaml:"airgap,omitempty"`           // airgap image tar filename (stored in dist/)
	Platforms       []string            `yaml:"platforms"`
	Download        map[string]string   `yaml:"download,omitempty"`        // platform → URL
	Mirror          map[string][]string `yaml:"mirror,omitempty"`          // platform → fallback URLs (tried in order)
	SHA256          map[string]string   `yaml:"sha256,omitempty"`          // platform → expected SHA-256 hex digest
	AirgapDownload  map[string]string   `yaml:"airgap_download,omitempty"` // platform → airgap tar URL
	AirgapMirror    map[string][]string `yaml:"airgap_mirror,omitempty"`   // platform → airgap tar mirror URLs (tried in order)
	AirgapSHA256    map[string]string   `yaml:"airgap_sha256,omitempty"`   // platform → expected SHA-256 hex digest for airgap tar
}

type StackInstall struct {
	Method       string            `yaml:"method"`
	Daemon       bool              `yaml:"daemon,omitempty"`
	Subcommand   string            `yaml:"subcommand,omitempty"`   // daemon ExecStart subcommand (default "server")
	ServiceType  string            `yaml:"service_type,omitempty"` // systemd Type= (default "notify")
	Priority     int               `yaml:"priority,omitempty"`     // lower = installed first (default 0)
	Tier         string            `yaml:"tier,omitempty"`         // "docker" or "k3s" — used for tiered init filtering
	Args         []StackArg        `yaml:"args,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	Helm         *StackHelm        `yaml:"helm,omitempty"`
	SystemdUnits []SystemdUnit     `yaml:"systemd_units,omitempty"` // multiple systemd services (archive method)
	PostInstall  []string          `yaml:"post_install,omitempty"`  // commands to run after install (non-fatal on failure)
}

// SystemdUnit describes a systemd service unit to create during archive installation.
type SystemdUnit struct {
	Name  string `yaml:"name"`
	Exec  string `yaml:"exec"`
	Type  string `yaml:"type,omitempty"`  // systemd Type= (default "simple")
	After string `yaml:"after,omitempty"` // systemd After= dependency
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
	ExtraArgs []StackArg        `yaml:"extra_args,omitempty"`
	ExtraEnv  map[string]string `yaml:"extra_env,omitempty"`
}

type StackQuestion struct {
	Question   string `yaml:"question"`
	Hypothesis string `yaml:"hypothesis"`
	TestMethod string `yaml:"test_method"`
	Status     string `yaml:"status,omitempty"`
	Finding    string `yaml:"finding,omitempty"`
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
	Name string  `yaml:"name"`
	GPU  SlotGPU `yaml:"gpu"`
	CPU  SlotCPU `yaml:"cpu"`
	RAM  SlotRAM `yaml:"ram"`
	Note string  `yaml:"note,omitempty"`
}

type SlotGPU struct {
	Count        int `yaml:"count"`
	MemoryMiB    int `yaml:"memory_mib"`
	CoresPercent int `yaml:"cores_percent"`
}

type SlotCPU struct {
	Cores int `yaml:"cores"`
}

type SlotRAM struct {
	MiB int `yaml:"mib"`
}

// --- Deployment Scenario ---

type DeploymentScenario struct {
	Kind               string                `yaml:"kind"`
	Metadata           ScenarioMetadata      `yaml:"metadata"`
	Target             ScenarioTarget        `yaml:"target"`
	Deployments        []ScenarioDeployment  `yaml:"deployments"`
	PostDeploy         []ScenarioAction      `yaml:"post_deploy,omitempty"`
	Integrations       map[string]any        `yaml:"integrations,omitempty"`
	Verified           *ScenarioVerification `yaml:"verified,omitempty"`
	OpenQuestions      []StackQuestion       `yaml:"open_questions,omitempty"`
	MemoryBudget       map[string]any        `yaml:"memory_budget,omitempty"`
	StartupOrder       []ScenarioStartupStep `yaml:"startup_order,omitempty"`
	AlternativeConfigs []ScenarioAlternative `yaml:"alternative_configs,omitempty"`
}

type ScenarioMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type ScenarioTarget struct {
	HardwareProfile   string `yaml:"hardware_profile"`
	PartitionStrategy string `yaml:"partition_strategy,omitempty"`
}

type ScenarioDeployment struct {
	Model      string         `yaml:"model"`
	Engine     string         `yaml:"engine"`
	Slot       string         `yaml:"slot,omitempty"`
	Role       string         `yaml:"role,omitempty"`
	Modalities []string       `yaml:"modalities,omitempty"`
	Config     map[string]any `yaml:"config,omitempty"`
	Notes      string         `yaml:"notes,omitempty"`
}

type ScenarioAction struct {
	Action      string `yaml:"action"`
	Description string `yaml:"description,omitempty"`
}

type ScenarioVerification struct {
	Date     string            `yaml:"date"`
	Hardware string            `yaml:"hardware"`
	Results  map[string]string `yaml:"results,omitempty"`
	Notes    string            `yaml:"notes,omitempty"`
}

type ScenarioStartupStep struct {
	Step     int    `yaml:"step"`
	Model    string `yaml:"model"`
	WaitFor  string `yaml:"wait_for"`
	TimeoutS int    `yaml:"timeout_s"`
	Notes    string `yaml:"notes,omitempty"`
}

type ScenarioAlternative struct {
	Name        string               `yaml:"name"`
	Description string               `yaml:"description"`
	Replace     []ScenarioDeployment `yaml:"replace"`
}

// LoadCatalog loads and parses all YAML knowledge assets from an fs.FS.
func LoadCatalog(fsys fs.FS) (*Catalog, error) {
	cat := &Catalog{
		EngineProfiles: make(map[string]*EngineProfile),
	}

	// Phase 1: Load engine profiles first (engines/profiles/*.yaml)
	if entries, err := fs.ReadDir(fsys, "engines/profiles"); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			path := "engines/profiles/" + entry.Name()
			data, err := fs.ReadFile(fsys, path)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			var probe kindProbe
			if err := yaml.Unmarshal(data, &probe); err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
			if probe.Kind == "engine_profile" {
				var ep EngineProfile
				if err := yaml.Unmarshal(data, &ep); err != nil {
					return nil, fmt.Errorf("parse engine profile %s: %w", path, err)
				}
				cat.EngineProfiles[ep.Metadata.Name] = &ep
			}
		}
	}

	// Phase 2: Load all assets (profiles subdir already handled, skip it)
	dirs := []string{"hardware", "engines", "models", "partitions", "stack", "scenarios"}
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
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			if err := cat.parseAsset(data, path); err != nil {
				return nil, err
			}
		}
	}

	// Phase 3: Merge profiles into assets and expand URL templates
	for _, warning := range finalizeEngineAssets(cat) {
		slog.Warn(warning)
	}

	return cat, nil
}

// mergeEngineProfile fills zero-value fields in the asset from the profile.
// Asset-specified values always win (override). Profile provides defaults.
func mergeEngineProfile(ea *EngineAsset, p *EngineProfile) {
	// Metadata: inherit version_default and supported_formats if asset has none
	if ea.Metadata.Version == "" && p.Metadata.VersionDefault != "" {
		ea.Metadata.Version = p.Metadata.VersionDefault
	}
	if len(ea.Metadata.SupportedFormats) == 0 {
		ea.Metadata.SupportedFormats = p.Metadata.SupportedFormats
	}

	// Startup: field-by-field merge
	mergeStartup(&ea.Startup, &p.Startup)

	// API
	if ea.API.Protocol == "" {
		ea.API.Protocol = p.API.Protocol
	}
	if ea.API.BasePath == "" {
		ea.API.BasePath = p.API.BasePath
	}

	// Amplifier
	mergeAmplifier(&ea.Amplifier, &p.Amplifier)

	// PartitionHints
	if ea.PartitionHints.MinGPUMemoryMiB == 0 {
		ea.PartitionHints.MinGPUMemoryMiB = p.PartitionHints.MinGPUMemoryMiB
	}
	if ea.PartitionHints.RecommendedGPUCoresPercent == 0 {
		ea.PartitionHints.RecommendedGPUCoresPercent = p.PartitionHints.RecommendedGPUCoresPercent
	}
}

func mergeStartup(dst, src *EngineStartup) {
	if len(dst.Command) == 0 {
		dst.Command = src.Command
	}
	if len(dst.InitCommands) == 0 {
		dst.InitCommands = src.InitCommands
	}
	if len(dst.Ports) == 0 {
		dst.Ports = src.Ports
	}
	if dst.DefaultArgs == nil {
		dst.DefaultArgs = src.DefaultArgs
	} else if src.DefaultArgs != nil {
		// Key-level merge: profile provides defaults, asset overrides per-key
		merged := make(map[string]any, len(src.DefaultArgs))
		for k, v := range src.DefaultArgs {
			merged[k] = v
		}
		for k, v := range dst.DefaultArgs {
			merged[k] = v
		}
		dst.DefaultArgs = merged
	}
	if dst.Env == nil {
		dst.Env = src.Env
	} else if src.Env != nil {
		merged := make(map[string]string, len(src.Env))
		for k, v := range src.Env {
			merged[k] = v
		}
		for k, v := range dst.Env {
			merged[k] = v
		}
		dst.Env = merged
	}
	if dst.WorkDir == "" {
		dst.WorkDir = src.WorkDir
	}
	if dst.HealthCheck.Path == "" {
		dst.HealthCheck.Path = src.HealthCheck.Path
	}
	if dst.HealthCheck.TimeoutS == 0 {
		dst.HealthCheck.TimeoutS = src.HealthCheck.TimeoutS
	}
	// Bool zero values cannot distinguish "unset" from "explicitly false".
	// Profile true is inherited unless the asset already enabled warmup itself.
	if !dst.Warmup.Enabled && src.Warmup.Enabled {
		dst.Warmup.Enabled = true
	}
	if dst.Warmup.Prompt == "" {
		dst.Warmup.Prompt = src.Warmup.Prompt
	}
	if dst.Warmup.MaxTokens == 0 {
		dst.Warmup.MaxTokens = src.Warmup.MaxTokens
	}
	if dst.Warmup.TimeoutS == 0 {
		dst.Warmup.TimeoutS = src.Warmup.TimeoutS
	}
	if len(dst.ExtraVolumes) == 0 {
		dst.ExtraVolumes = src.ExtraVolumes
	}
	if dst.LogPatterns == nil {
		dst.LogPatterns = src.LogPatterns
	}
}

func mergeAmplifier(dst, src *EngineAmplifier) {
	if len(dst.Features) == 0 {
		dst.Features = src.Features
	}
	if dst.PerformanceGain == "" {
		dst.PerformanceGain = src.PerformanceGain
	}
	if dst.ResourceExpansion == nil {
		dst.ResourceExpansion = src.ResourceExpansion
	}
	if dst.PerformanceMultiplier == 0 {
		dst.PerformanceMultiplier = src.PerformanceMultiplier
	}
	if dst.EffectiveVRAMMultiplier == 0 {
		dst.EffectiveVRAMMultiplier = src.EffectiveVRAMMultiplier
	}
	if dst.OffloadConfigKey == "" {
		dst.OffloadConfigKey = src.OffloadConfigKey
	}
	// ExtendsResourceBoundary: bool zero is false, can't distinguish "not set" from "explicitly false".
	// Convention: if profile says true and asset doesn't override, inherit true.
	if src.ExtendsResourceBoundary && !dst.ExtendsResourceBoundary {
		dst.ExtendsResourceBoundary = true
	}
}

// expandURLTemplate expands url_template + platform_files into Download/Mirror maps.
// If Download already has entries (legacy format), this is a no-op for backward compat.
func expandURLTemplate(src *EngineSource, version string) {
	if src.URLTemplate == "" || len(src.PlatformFiles) == 0 {
		return
	}
	// Only expand if Download is empty (template takes priority over legacy)
	if len(src.Download) > 0 {
		return
	}

	src.Download = make(map[string]string, len(src.PlatformFiles))
	src.Mirror = make(map[string][]string, len(src.PlatformFiles))

	for platform, platformFile := range src.PlatformFiles {
		// Replace {version} and {platform_file} in URL template
		primaryURL := src.URLTemplate
		primaryURL = strings.ReplaceAll(primaryURL, "{version}", version)
		primaryURL = strings.ReplaceAll(primaryURL, "{platform_file}", platformFile)
		src.Download[platform] = primaryURL

		// Expand mirror templates: {url} is replaced with the full primary URL
		if len(src.MirrorTemplates) > 0 {
			mirrors := make([]string, 0, len(src.MirrorTemplates))
			for _, tmpl := range src.MirrorTemplates {
				mirrorURL := strings.ReplaceAll(tmpl, "{url}", primaryURL)
				mirrors = append(mirrors, mirrorURL)
			}
			src.Mirror[platform] = mirrors
		}
	}
}

func finalizeEngineAssets(cat *Catalog) []string {
	rawAssets := cat.RawEngineAssets
	if len(rawAssets) == 0 && len(cat.EngineAssets) > 0 {
		rawAssets = cat.EngineAssets
	}
	if len(rawAssets) == 0 {
		cat.EngineAssets = nil
		return nil
	}

	finalized := make([]EngineAsset, 0, len(rawAssets))
	var warnings []string
	for _, raw := range rawAssets {
		ea := cloneEngineAsset(raw)
		if ea.Profile != "" {
			if p, ok := cat.EngineProfiles[ea.Profile]; ok {
				mergeEngineProfile(&ea, p)
			} else {
				warnings = append(warnings, fmt.Sprintf("engine %s: unknown profile %q", ea.Metadata.Name, ea.Profile))
			}
		}
		if ea.Source != nil {
			expandURLTemplate(ea.Source, ea.Metadata.Version)
		}
		finalized = append(finalized, ea)
	}
	cat.EngineAssets = finalized
	return warnings
}

func cloneEngineAsset(src EngineAsset) EngineAsset {
	dst := src

	dst.Metadata.SupportedFormats = append([]string(nil), src.Metadata.SupportedFormats...)
	dst.Image.Platforms = append([]string(nil), src.Image.Platforms...)
	dst.Image.Registries = append([]string(nil), src.Image.Registries...)
	dst.Startup.Command = append([]string(nil), src.Startup.Command...)
	dst.Startup.InitCommands = append([]string(nil), src.Startup.InitCommands...)
	dst.Startup.Env = cloneStringMap(src.Startup.Env)
	dst.Startup.DefaultArgs = cloneAnyMap(src.Startup.DefaultArgs)
	dst.Startup.ExtraVolumes = append([]ContainerVolume(nil), src.Startup.ExtraVolumes...)
	if src.Startup.LogPatterns != nil {
		logPatterns := *src.Startup.LogPatterns
		logPatterns.Phases = append([]StartupPhasePattern(nil), src.Startup.LogPatterns.Phases...)
		logPatterns.Errors = append([]StartupErrorPattern(nil), src.Startup.LogPatterns.Errors...)
		dst.Startup.LogPatterns = &logPatterns
	}
	dst.Amplifier.Features = append([]string(nil), src.Amplifier.Features...)
	dst.Amplifier.ResourceExpansion = cloneBoolMap(src.Amplifier.ResourceExpansion)
	dst.TimeConstraints.ColdStartS = append([]int(nil), src.TimeConstraints.ColdStartS...)
	dst.TimeConstraints.ModelSwitchS = append([]int(nil), src.TimeConstraints.ModelSwitchS...)
	dst.PowerConstraints.TypicalDrawWatts = append([]int(nil), src.PowerConstraints.TypicalDrawWatts...)
	dst.Runtime.PlatformRecommendations = cloneStringMap(src.Runtime.PlatformRecommendations)
	dst.Patterns = append([]string(nil), src.Patterns...)
	dst.OpenQuestions = append([]StackQuestion(nil), src.OpenQuestions...)
	if src.Source != nil {
		source := *src.Source
		source.Platforms = append([]string(nil), src.Source.Platforms...)
		source.Download = cloneStringMap(src.Source.Download)
		source.SHA256 = cloneStringMap(src.Source.SHA256)
		source.PlatformFiles = cloneStringMap(src.Source.PlatformFiles)
		source.MirrorTemplates = append([]string(nil), src.Source.MirrorTemplates...)
		if src.Source.Mirror != nil {
			source.Mirror = make(map[string][]string, len(src.Source.Mirror))
			for k, v := range src.Source.Mirror {
				source.Mirror[k] = append([]string(nil), v...)
			}
		}
		if src.Source.Probe != nil {
			probe := *src.Source.Probe
			probe.Paths = append([]string(nil), src.Source.Probe.Paths...)
			probe.VersionCommand = append([]string(nil), src.Source.Probe.VersionCommand...)
			source.Probe = &probe
		}
		dst.Source = &source
	}

	return dst
}

func cloneStringMap[T ~string](src map[string]T) map[string]T {
	if src == nil {
		return nil
	}
	dst := make(map[string]T, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneBoolMap(src map[string]bool) map[string]bool {
	if src == nil {
		return nil
	}
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = normalizeValue(v)
	}
	return dst
}

// normalizeMap recursively converts map[interface{}]interface{} values (from YAML)
// to map[string]interface{} so the map is JSON-safe.
func normalizeMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	for k, v := range m {
		m[k] = normalizeValue(v)
	}
	return m
}

func normalizeValue(v any) any {
	switch val := v.(type) {
	case map[interface{}]interface{}:
		m := make(map[string]interface{}, len(val))
		for k, v2 := range val {
			m[fmt.Sprint(k)] = normalizeValue(v2)
		}
		return m
	case map[string]interface{}:
		for k, v2 := range val {
			val[k] = normalizeValue(v2)
		}
		return val
	case []interface{}:
		for i, v2 := range val {
			val[i] = normalizeValue(v2)
		}
		return val
	default:
		return v
	}
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
	case "engine_profile":
		var ep EngineProfile
		if err := yaml.Unmarshal(data, &ep); err != nil {
			return fmt.Errorf("parse engine profile %s: %w", path, err)
		}
		if cat.EngineProfiles == nil {
			cat.EngineProfiles = make(map[string]*EngineProfile)
		}
		cat.EngineProfiles[ep.Metadata.Name] = &ep
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
		var raw EngineAsset
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse raw engine asset %s: %w", path, err)
		}
		cat.RawEngineAssets = append(cat.RawEngineAssets, raw)
	case "model_asset":
		var ma ModelAsset
		if err := yaml.Unmarshal(data, &ma); err != nil {
			return fmt.Errorf("parse model asset %s: %w", path, err)
		}
		// YAML unmarshal produces map[interface{}]interface{} for nested maps;
		// normalize to map[string]interface{} so json.Marshal works later.
		for i := range ma.Variants {
			ma.Variants[i].DefaultConfig = normalizeMap(ma.Variants[i].DefaultConfig)
			ma.Variants[i].ExpectedPerformance = normalizeMap(ma.Variants[i].ExpectedPerformance)
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
	case "deployment_scenario":
		var ds DeploymentScenario
		if err := yaml.Unmarshal(data, &ds); err != nil {
			return fmt.Errorf("parse deployment scenario %s: %w", path, err)
		}
		for i := range ds.Deployments {
			ds.Deployments[i].Config = normalizeMap(ds.Deployments[i].Config)
		}
		cat.DeploymentScenarios = append(cat.DeploymentScenarios, ds)
	default:
		// Unknown kind: skip silently
	}
	return nil
}

// ParseAssetPublic is an exported wrapper around parseAsset for validation.
func (cat *Catalog) ParseAssetPublic(data []byte, path string) error {
	return cat.parseAsset(data, path)
}

// ParsedKind returns the kind of asset that was parsed into this catalog.
// Returns "" if no assets were parsed or multiple kinds were parsed.
func (cat *Catalog) ParsedKind() string {
	var kinds []string
	if len(cat.EngineProfiles) > 0 {
		kinds = append(kinds, "engine_profile")
	}
	if len(cat.HardwareProfiles) > 0 {
		kinds = append(kinds, "hardware_profile")
	}
	if len(cat.EngineAssets) > 0 {
		kinds = append(kinds, "engine_asset")
	}
	if len(cat.ModelAssets) > 0 {
		kinds = append(kinds, "model_asset")
	}
	if len(cat.PartitionStrategies) > 0 {
		kinds = append(kinds, "partition_strategy")
	}
	if len(cat.StackComponents) > 0 {
		kinds = append(kinds, "stack_component")
	}
	if len(cat.DeploymentScenarios) > 0 {
		kinds = append(kinds, "deployment_scenario")
	}
	if len(kinds) == 1 {
		return kinds[0]
	}
	return ""
}

// LoadCatalogLenient loads YAML assets like LoadCatalog but continues on
// per-file errors instead of failing the entire load. Returns successfully
// parsed assets plus a list of warning messages for files that failed.
func LoadCatalogLenient(fsys fs.FS) (*Catalog, []string) {
	cat := &Catalog{
		EngineProfiles: make(map[string]*EngineProfile),
	}
	var warnings []string

	// Phase 1: Load engine profiles
	if entries, err := fs.ReadDir(fsys, "engines/profiles"); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			path := "engines/profiles/" + entry.Name()
			data, err := fs.ReadFile(fsys, path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("read %s: %v", path, err))
				continue
			}
			var probe kindProbe
			if err := yaml.Unmarshal(data, &probe); err != nil {
				warnings = append(warnings, fmt.Sprintf("parse %s: %v", path, err))
				continue
			}
			if probe.Kind == "engine_profile" {
				var ep EngineProfile
				if err := yaml.Unmarshal(data, &ep); err != nil {
					warnings = append(warnings, fmt.Sprintf("parse engine profile %s: %v", path, err))
					continue
				}
				cat.EngineProfiles[ep.Metadata.Name] = &ep
			}
		}
	}

	// Phase 2: Load all assets
	dirs := []string{"hardware", "engines", "models", "partitions", "stack", "scenarios"}
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

	// Phase 3: Merge profiles + expand URL templates
	warnings = append(warnings, finalizeEngineAssets(cat)...)

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
	dirs := []string{"hardware", "engines", "engines/profiles", "models", "partitions", "stack", "scenarios"}
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
	base.RawEngineAssets = mergeSlice(rawEngineAssets(base), rawEngineAssets(overlay), func(v EngineAsset) string { return v.Metadata.Name })
	base.ModelAssets = mergeSlice(base.ModelAssets, overlay.ModelAssets, func(v ModelAsset) string { return v.Metadata.Name })
	base.PartitionStrategies = mergeSlice(base.PartitionStrategies, overlay.PartitionStrategies, func(v PartitionStrategy) string { return v.Metadata.Name })
	base.StackComponents = mergeSlice(base.StackComponents, overlay.StackComponents, func(v StackComponent) string { return v.Metadata.Name })
	base.DeploymentScenarios = mergeSlice(base.DeploymentScenarios, overlay.DeploymentScenarios, func(v DeploymentScenario) string { return v.Metadata.Name })
	base.EngineProfiles = mergeEngineProfiles(base.EngineProfiles, overlay.EngineProfiles)
	_ = finalizeEngineAssets(base)
	return base
}

func rawEngineAssets(cat *Catalog) []EngineAsset {
	if len(cat.RawEngineAssets) > 0 {
		return cat.RawEngineAssets
	}
	return cat.EngineAssets
}

func mergeEngineProfiles(base, overlay map[string]*EngineProfile) map[string]*EngineProfile {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	merged := make(map[string]*EngineProfile, len(base)+len(overlay))
	for name, profile := range base {
		merged[name] = profile
	}
	for name, profile := range overlay {
		merged[name] = profile
	}
	return merged
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
	for name := range cat.EngineProfiles {
		names[name] = true
	}
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
	for _, v := range cat.DeploymentScenarios {
		names[v.Metadata.Name] = true
	}
	return names
}

func extractOverlayDigests(fsys fs.FS) map[string]string {
	if fsys == nil {
		return nil
	}
	digests := make(map[string]string)
	dirs := []string{"hardware", "engines", "engines/profiles", "models", "partitions", "stack", "scenarios"}
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
	case "engine_profile":
		return "engines/profiles"
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
	case "deployment_scenario":
		return "scenarios"
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
					`INSERT INTO model_variants (id, model_id, hardware_id, engine_type, format, default_config, expected_perf, vram_min_mib, gpu_count_min)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					variantID, id, hwID, v.Engine, v.Format,
					string(configJSON), string(perfJSON), v.Hardware.VRAMMinMiB, v.Hardware.GPUCountMin)
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
