package knowledge

import (
	"fmt"
	"math"
	"strings"
)

// HardwareInfo describes the detected hardware for config resolution.
// Zero-valued fields mean "unknown" and are skipped during validation,
// ensuring backward compatibility with callers that only set GPUArch/CPUArch.
type HardwareInfo struct {
	GPUArch         string
	GPUVRAMMiB      int  // Per-GPU VRAM (0 = unknown, skip VRAM checks)
	GPUCount        int  // Number of GPUs
	UnifiedMemory   bool // GPU shares system RAM (Apple M-series, GB10, AMD APU)
	CPUArch         string
	CPUCores        int    // Physical CPU core count
	RAMTotalMiB     int    // Total system RAM
	GPUModel        string // GPU model name from detection (e.g. "RTX 4060") — for gpu_model variant matching
	HardwareProfile string // Name of a matching HardwareProfile, if known
	Platform        string // "linux/amd64", "darwin/arm64", etc.
	RuntimeType     string // "k3s" or "native"
	SwapTotalMiB    int    // Total swap space (0 = unknown or none)
	TDPWatts        int    // hardware TDP from profile (0 = unknown)
	// Dynamic fields from runtime metrics (0 = not collected, graceful degradation)
	GPUMemUsedMiB int // Currently used GPU memory
	GPUMemFreeMiB int // Currently free GPU memory
	RAMAvailMiB   int // Currently available system RAM
}

// PartitionSlot holds the resource allocation for a single deployment slot.
type PartitionSlot struct {
	Name            string
	GPUCount        int
	GPUMemoryMiB    int
	GPUCoresPercent int
	CPUCores        int
	RAMMiB          int
}

// ResolvedConfig is the merged output of the L0-L2 config resolution process.
type ResolvedConfig struct {
	Engine                string
	EngineImage           string
	ModelPath             string
	ModelName             string
	ModelFormat           string
	Slot                  string
	Config                map[string]any
	Provenance            map[string]string
	Partition             *PartitionSlot
	Command               []string
	PortSpecs             []StartupPort
	InitCommands          []string          // pre-commands to run before main server (from engine YAML)
	CompatibilityProbe    string            // container compatibility probe declared by engine YAML
	RepairInitCommands    []string          // model-variant repair commands to prepend when compatibility probe needs self-heal
	ExtraVolumes          []ContainerVolume // additional host volumes to mount (from engine YAML)
	HealthCheck           *HealthCheck
	Warmup                *WarmupConfig     // post-healthcheck warmup config (nil = no warmup)
	Source                *EngineSource     // native binary source info (nil if container-only)
	Env                   map[string]string // Extra env vars for the container (from engine YAML)
	WorkDir               string            // Working directory for native process (from engine YAML)
	GPUResourceName       string            // K8s resource name, e.g. "nvidia.com/gpu" (empty = no GPU resource request)
	RuntimeClassName      string            // K8s runtimeClassName for GPU containers, e.g. "nvidia" (from hardware profile)
	RuntimeRecommendation string            // "native" or "container" or "" — from engine's platform_recommendations
	CPUArch               string            // CPU architecture (e.g. "amd64", "arm64") — for platform-specific paths
	Container             *ContainerAccess  // vendor-specific container access (devices, env, volumes, security) from hardware profile
	EngineRegistries      []string          // container image registries from engine YAML (for pre-pull fallback)
	EngineDigest          string            // OCI content digest from engine YAML (for pull verification)
	EngineDistribution    string            // "registry" (default) or "local" — local-only engines cannot be pulled remotely

	// Time estimates (zero = unknown, graceful degradation)
	ColdStartSMin int // engine cold start lower bound (seconds)
	ColdStartSMax int // engine cold start upper bound (seconds)
	StartupTimeS  int // model-specific startup time estimate (seconds)

	// Power estimates (zero = unknown)
	EnginePowerWattsMin int // engine typical power draw lower bound
	EnginePowerWattsMax int // engine typical power draw upper bound

	// Resource estimates (zero = unknown)
	EstimatedVRAMMiB int               // expected VRAM usage from model variant
	ResourceEstimate *ResourceEstimate // full cost(path, R) estimate

	// Amplifier info (from engine selection)
	AmplifierScore float64 // performance multiplier of selected engine
	OffloadPath    bool    // true if engine was selected via effective_R offload

	// Performance reference (K4 — historical data or YAML estimate)
	PerformanceRef *PerformanceReference
}

// PerformanceReference attaches known performance data to a resolved config.
type PerformanceReference struct {
	ThroughputTPS float64 `json:"throughput_tps,omitempty"`
	TTFTMsP95     float64 `json:"ttft_ms_p95,omitempty"`
	PowerWatts    float64 `json:"power_watts,omitempty"`
	Source        string  `json:"source"` // "benchmark", "yaml_estimate", "unknown"
	BenchmarkID   string  `json:"benchmark_id,omitempty"`
}

// ResourceEstimate is the full cost(path, R) output for a deployment.
type ResourceEstimate struct {
	VRAMMiB    int `json:"vram_mib"`
	RAMMiB     int `json:"ram_mib"`
	CPUCores   int `json:"cpu_cores"`
	DiskMiB    int `json:"disk_mib"`
	PowerWatts int `json:"power_watts"`
}

// Resolve finds the best config by merging L0 (engine defaults) -> model variant defaults -> L1 (user overrides).
// L2 (knowledge notes from DB) is not applied here; it is merged by the caller when available.
func (c *Catalog) Resolve(hw HardwareInfo, modelName, engineType string, userOverrides map[string]any, opts ...ResolveOption) (*ResolvedConfig, error) {
	var ropts resolveOpts
	for _, o := range opts {
		o(&ropts)
	}

	var partitionName string
	if pn, ok := userOverrides["partition"]; ok {
		partitionName = fmt.Sprint(pn)
	}
	partition := c.findPartitionByName(hw, partitionName)
	slot := pickSlot(partition, userOverrides)

	// Slot-level GPU limits should constrain engine/variant selection, not only pod generation.
	selectionHW := hw
	selectionHW.GPUCount = availableGPUCount(hw, slot)

	// Auto-detect engine from model variants when not specified
	if engineType == "" {
		inferred, err := c.InferEngineType(modelName, selectionHW, opts...)
		if err != nil {
			return nil, err
		}
		engineType = inferred
	}

	engine, err := c.findEngine(engineType, selectionHW)
	if err != nil {
		return nil, err
	}

	model, variant, err := c.findModelVariant(modelName, engineType, engine, selectionHW)
	if err != nil {
		return nil, err
	}

	// Format compatibility: reject early if model format is not supported by engine.
	// This prevents dry-run from reporting fit=true for incompatible combos (e.g. AWQ on Ascend).
	if variant != nil && variant.Format != "" && len(engine.Metadata.SupportedFormats) > 0 {
		supported := false
		for _, f := range engine.Metadata.SupportedFormats {
			if strings.EqualFold(f, variant.Format) {
				supported = true
				break
			}
		}
		if !supported {
			return nil, fmt.Errorf("model format %q not supported by engine %s (supported: %v)",
				variant.Format, engine.Metadata.Name, engine.Metadata.SupportedFormats)
		}
	}

	config := make(map[string]any)
	provenance := make(map[string]string)

	// L0: Engine default_args
	for k, v := range engine.Startup.DefaultArgs {
		config[k] = v
		provenance[k] = "L0"
	}

	// L0 (model variant layer): model variant default_config overrides engine defaults
	if variant != nil {
		for k, v := range variant.DefaultConfig {
			config[k] = v
			provenance[k] = "L0"
		}
	}

	// L2c: Golden config from benchmark-promoted optimal (between L0 and L1)
	if ropts.GoldenConfig != nil {
		hwKey := hw.HardwareProfile
		if hwKey == "" {
			hwKey = hw.GPUArch
		}
		goldenOverrides := ropts.GoldenConfig(hwKey, engineType, model.Metadata.Name)
		for k, v := range goldenOverrides {
			config[k] = v
			provenance[k] = "L2c"
		}
	}

	// L1: User overrides (model_path, partition, slot are handled separately)
	for k, v := range userOverrides {
		if k == "model_path" || k == "partition" || k == "slot" {
			continue
		}
		config[k] = v
		provenance[k] = "L1"
	}

	resolved := &ResolvedConfig{
		Engine:             engineType,
		ModelName:          model.Metadata.Name,
		ModelFormat:        variant.Format,
		Slot:               slot.Name,
		Config:             config,
		Provenance:         provenance,
		Partition:          slot,
		Command:            engine.Startup.Command,
		PortSpecs:          engine.Startup.Ports,
		InitCommands:       engine.Startup.InitCommands,
		CompatibilityProbe: engine.Startup.CompatibilityProbe,
		ExtraVolumes:       engine.Startup.ExtraVolumes,
		Env:                engine.Startup.Env,
		WorkDir:            engine.Startup.WorkDir,
		HealthCheck:        &engine.Startup.HealthCheck,
		Source:             engine.Source,
		EngineRegistries:   engine.Image.Registries,
		EngineDigest:       engine.Image.Digest,
		EngineDistribution: engine.Image.Distribution,
	}
	if engine.Image.Name != "" {
		resolved.EngineImage = engine.Image.Name
		if engine.Image.Tag != "" {
			resolved.EngineImage += ":" + engine.Image.Tag
		}
	}
	if engine.Startup.Warmup.Enabled {
		resolved.Warmup = &engine.Startup.Warmup
	}

	// Set GPU resource name, runtimeClassName, CPU arch, and container access from hardware profile
	resolved.GPUResourceName = c.findGPUResourceName(hw)
	resolved.RuntimeClassName = c.findRuntimeClassName(hw)
	resolved.CPUArch = hw.CPUArch
	resolved.Container = c.findContainerAccess(hw)

	// Set runtime recommendation from engine's platform_recommendations
	if rec, ok := engine.Runtime.PlatformRecommendations[hw.Platform]; ok {
		resolved.RuntimeRecommendation = rec
	} else {
		resolved.RuntimeRecommendation = engine.Runtime.Default
	}

	// Time estimates from engine
	if len(engine.TimeConstraints.ColdStartS) >= 2 {
		resolved.ColdStartSMin = engine.TimeConstraints.ColdStartS[0]
		resolved.ColdStartSMax = engine.TimeConstraints.ColdStartS[1]
	}

	// Power estimates from engine
	if len(engine.PowerConstraints.TypicalDrawWatts) >= 2 {
		resolved.EnginePowerWattsMin = engine.PowerConstraints.TypicalDrawWatts[0]
		resolved.EnginePowerWattsMax = engine.PowerConstraints.TypicalDrawWatts[1]
	}

	// Amplifier info from selected engine
	mult := engine.Amplifier.PerformanceMultiplier
	if mult <= 0 {
		mult = 1.0
	}
	resolved.AmplifierScore = mult

	// Model variant estimates + resource estimation
	if variant != nil {
		perf := variant.ParsedExpectedPerf()
		resolved.StartupTimeS = perf.StartupTimeS
		resolved.RepairInitCommands = append([]string(nil), variant.Compatibility.RepairInitCommands...)
		if perf.VRAMMiB > 0 {
			resolved.EstimatedVRAMMiB = perf.VRAMMiB
		} else if variant.Hardware.VRAMMinMiB > 0 {
			resolved.EstimatedVRAMMiB = variant.Hardware.VRAMMinMiB
		}
		resolved.ResourceEstimate = estimateResources(engine, variant, hw)
	}

	// Set ModelPath from user overrides or default pattern
	if mp, ok := userOverrides["model_path"]; ok {
		resolved.ModelPath = fmt.Sprint(mp)
	}

	return resolved, nil
}

func (c *Catalog) findEngine(engineType string, hw HardwareInfo) (*EngineAsset, error) {
	compatible := func(ea *EngineAsset) bool {
		if hw.RuntimeType != "native" {
			return true
		}
		if ea.Source == nil {
			return false
		}
		return ea.Source.Supports(hw.Platform)
	}

	// Prefer exact metadata.name match, then metadata.type.
	// Within each class, prefer exact gpu_arch match over wildcard.
	var nameWildcard, typeWildcard *EngineAsset
	for i := range c.EngineAssets {
		ea := &c.EngineAssets[i]
		if !compatible(ea) {
			continue
		}
		if strings.EqualFold(ea.Metadata.Name, engineType) {
			if ea.Hardware.GPUArch == hw.GPUArch {
				return ea, nil
			}
			if ea.Hardware.GPUArch == "*" && nameWildcard == nil {
				nameWildcard = ea
			}
		}
		if strings.EqualFold(ea.Metadata.Type, engineType) {
			if ea.Hardware.GPUArch == hw.GPUArch {
				return ea, nil
			}
			if ea.Hardware.GPUArch == "*" && typeWildcard == nil {
				typeWildcard = ea
			}
		}
	}
	if nameWildcard != nil {
		return nameWildcard, nil
	}
	if typeWildcard != nil {
		return typeWildcard, nil
	}
	return nil, fmt.Errorf("no engine asset for type %q gpu_arch %q", engineType, hw.GPUArch)
}

// engineCandidate holds an engine option with its amplifier score for ranking.
type engineCandidate struct {
	engineType string
	multiplier float64
	coldStartS int  // engine cold start upper bound (0 = unknown)
	offload    bool // selected via effective_R (offload path)
	exactArch  bool // true if gpu_arch matched exactly (not wildcard)
}

// GoldenConfigFunc returns the golden (L2c) config overrides for a hardware/engine/model triple.
// Returns nil map if no golden config exists (graceful degradation).
type GoldenConfigFunc func(hardware, engine, model string) map[string]any

// ResolveOption configures optional constraints for engine selection.
type ResolveOption func(*resolveOpts)

type resolveOpts struct {
	MaxColdStartS int
	GoldenConfig  GoldenConfigFunc
}

// WithMaxColdStart filters engines whose cold start exceeds the given seconds.
func WithMaxColdStart(s int) ResolveOption {
	return func(o *resolveOpts) { o.MaxColdStartS = s }
}

// WithGoldenConfig injects L2c (benchmark-promoted optimal) config lookup into the resolve chain.
func WithGoldenConfig(fn GoldenConfigFunc) ResolveOption {
	return func(o *resolveOpts) { o.GoldenConfig = fn }
}

// InferEngineType picks the best engine for a model on the given hardware.
// Priority: collect all candidates that can run (format + VRAM fit or offload),
// then rank by amplifier.performance_multiplier (descending), cold_start as tiebreaker.
func (c *Catalog) InferEngineType(modelName string, hw HardwareInfo, opts ...ResolveOption) (string, error) {
	var ropts resolveOpts
	for _, o := range opts {
		o(&ropts)
	}
	for _, ma := range c.ModelAssets {
		if !strings.EqualFold(ma.Metadata.Name, modelName) {
			continue
		}
		var candidates []engineCandidate

		for _, v := range ma.Variants {
			if v.Hardware.GPUArch != hw.GPUArch && v.Hardware.GPUArch != "*" {
				continue
			}
			// GPU count filter: skip variants requiring more GPUs than available
			if v.Hardware.GPUCountMin > 0 && hw.GPUCount > 0 && hw.GPUCount < v.Hardware.GPUCountMin {
				continue
			}
			engine, err := c.findEngine(v.Engine, hw)
			if err != nil {
				continue
			}

			fitsRawVRAM := hw.GPUVRAMMiB == 0 || v.Hardware.VRAMMinMiB == 0 || v.Hardware.VRAMMinMiB <= hw.GPUVRAMMiB

			// Get cold start upper bound for filtering and tiebreaking
			var coldStartMax int
			if len(engine.TimeConstraints.ColdStartS) >= 2 {
				coldStartMax = engine.TimeConstraints.ColdStartS[1]
			}

			exact := v.Hardware.GPUArch == hw.GPUArch

			if fitsRawVRAM {
				mult := engine.Amplifier.PerformanceMultiplier
				if mult <= 0 {
					mult = 1.0
				}
				candidates = append(candidates, engineCandidate{
					engineType: v.Engine,
					multiplier: mult,
					coldStartS: coldStartMax,
					exactArch:  exact,
				})
				continue
			}

			// Doesn't fit raw VRAM — check effective_R with offload
			if engine.Amplifier.ExtendsResourceBoundary && engine.Amplifier.EffectiveVRAMMultiplier > 1.0 {
				effVRAM := effectiveVRAM(hw, engine.Amplifier.EffectiveVRAMMultiplier)
				if v.Hardware.VRAMMinMiB <= effVRAM {
					mult := engine.Amplifier.PerformanceMultiplier
					if mult <= 0 {
						mult = 1.0
					}
					candidates = append(candidates, engineCandidate{
						engineType: v.Engine,
						multiplier: mult,
						coldStartS: coldStartMax,
						offload:    true,
						exactArch:  exact,
					})
				}
			}
		}

		if len(candidates) == 0 {
			return "", fmt.Errorf("no compatible engine for model %q on gpu_arch %q (vram %d MiB)", modelName, hw.GPUArch, hw.GPUVRAMMiB)
		}

		// Filter: max cold start constraint
		if ropts.MaxColdStartS > 0 {
			var filtered []engineCandidate
			for _, c := range candidates {
				if c.coldStartS == 0 || c.coldStartS <= ropts.MaxColdStartS {
					filtered = append(filtered, c)
				}
			}
			if len(filtered) > 0 {
				candidates = filtered
			}
			// If all filtered out, keep all candidates (graceful degradation)
		}

		// Rank: exact arch > wildcard, then highest multiplier, then non-offload > offload,
		// then lower cold_start as final tiebreaker.
		best := candidates[0]
		for _, c := range candidates[1:] {
			if c.exactArch && !best.exactArch {
				best = c
			} else if c.exactArch == best.exactArch {
				if c.multiplier > best.multiplier {
					best = c
				} else if c.multiplier == best.multiplier {
					if !c.offload && best.offload {
						best = c
					} else if c.offload == best.offload && c.coldStartS > 0 && best.coldStartS > 0 && c.coldStartS < best.coldStartS {
						best = c
					}
				}
			}
		}
		return best.engineType, nil
	}
	return "", fmt.Errorf("model %q not found in catalog", modelName)
}

// effectiveVRAM computes expanded VRAM when an engine supports CPU/RAM offload.
func effectiveVRAM(hw HardwareInfo, vramMultiplier float64) int {
	if hw.RAMTotalMiB == 0 {
		return hw.GPUVRAMMiB
	}
	ramContribution := int(float64(hw.RAMTotalMiB) * (vramMultiplier - 1.0) / vramMultiplier)
	return hw.GPUVRAMMiB + ramContribution
}

// ResolveVariantForPull finds the best model variant for downloading on the given hardware.
// It composes InferEngineType + findModelVariant to avoid duplicating matching logic
// in call sites. Returns (ma, nil, engineType, err) when the model exists but no variant
// matches, allowing the caller to fall back to global sources.
func (c *Catalog) ResolveVariantForPull(modelName string, hw HardwareInfo) (*ModelAsset, *ModelVariant, string, error) {
	engineType, err := c.InferEngineType(modelName, hw)
	if err != nil {
		// Model found but no compatible engine — return model asset for global source fallback.
		for i := range c.ModelAssets {
			if strings.EqualFold(c.ModelAssets[i].Metadata.Name, modelName) {
				return &c.ModelAssets[i], nil, "", err
			}
		}
		return nil, nil, "", err
	}
	engine, ferr := c.findEngine(engineType, hw)
	if ferr != nil {
		return nil, nil, engineType, ferr
	}
	ma, variant, err := c.findModelVariant(modelName, engineType, engine, hw)
	if err != nil {
		return ma, nil, engineType, err
	}
	return ma, variant, engineType, nil
}

// FindEngineByName looks up an engine asset using a flexible name match.
// Priority: exact metadata.name → metadata.type with hardware preference → image name substring.
// Returns nil if no catalog asset matches.
func (c *Catalog) FindEngineByName(name string, hw HardwareInfo) *EngineAsset {
	nameLower := strings.ToLower(name)

	// Pass 1: exact metadata.name
	for i := range c.EngineAssets {
		if strings.ToLower(c.EngineAssets[i].Metadata.Name) == nameLower {
			return &c.EngineAssets[i]
		}
	}

	// Pass 2: metadata.type with hardware preference
	var typeMatch *EngineAsset
	for i := range c.EngineAssets {
		ea := &c.EngineAssets[i]
		if strings.ToLower(ea.Metadata.Type) != nameLower {
			continue
		}
		if typeMatch == nil {
			typeMatch = ea
		}
		if strings.EqualFold(ea.Hardware.GPUArch, hw.GPUArch) {
			return ea
		}
	}
	if typeMatch != nil {
		return typeMatch
	}

	// Pass 3: image name substring
	for i := range c.EngineAssets {
		if strings.Contains(strings.ToLower(c.EngineAssets[i].Image.Name), nameLower) {
			return &c.EngineAssets[i]
		}
	}

	return nil
}

// findGPUResourceName looks up the K8s GPU resource name from hardware profiles.
// MatchHardwareProfile finds the best matching hardware profile for the given hardware.
// Matching priority: exact profile name > arch+VRAM closest match > first arch match.
func (c *Catalog) MatchHardwareProfile(hw HardwareInfo) *HardwareProfile {
	// Priority 1: exact match by HardwareProfile name
	if hw.HardwareProfile != "" {
		for i := range c.HardwareProfiles {
			if c.HardwareProfiles[i].Metadata.Name == hw.HardwareProfile {
				return &c.HardwareProfiles[i]
			}
		}
	}

	// Priority 2: arch match — if multiple, prefer closest VRAM match
	var bestMatch *HardwareProfile
	bestDelta := -1
	for i := range c.HardwareProfiles {
		hp := &c.HardwareProfiles[i]
		if hp.Hardware.GPU.Arch != hw.GPUArch {
			continue
		}
		if hw.GPUVRAMMiB == 0 {
			// No VRAM info — return first arch match
			return hp
		}
		delta := hw.GPUVRAMMiB - hp.Hardware.GPU.VRAMMiB
		if delta < 0 {
			delta = -delta
		}
		if bestMatch == nil || delta < bestDelta {
			bestMatch = hp
			bestDelta = delta
		}
	}
	return bestMatch
}

// findHardwareProfileFor returns the best matching profile, using MatchHardwareProfile.
func (c *Catalog) findHardwareProfileFor(hw HardwareInfo) *HardwareProfile {
	return c.MatchHardwareProfile(hw)
}

// Returns "" if not specified (no GPU resource request in pod spec).
func (c *Catalog) findGPUResourceName(hw HardwareInfo) string {
	if hp := c.findHardwareProfileFor(hw); hp != nil && hp.Hardware.GPU.ResourceName != "" {
		return hp.Hardware.GPU.ResourceName
	}
	return ""
}

// findContainerAccess looks up vendor-specific container access config from hardware profiles.
func (c *Catalog) findContainerAccess(hw HardwareInfo) *ContainerAccess {
	if hp := c.findHardwareProfileFor(hw); hp != nil && hp.Container != nil {
		return hp.Container
	}
	return nil
}

// findRuntimeClassName looks up the K8s runtimeClassName from hardware profiles.
// Returns "" if not specified (no runtimeClassName in pod spec).
func (c *Catalog) findRuntimeClassName(hw HardwareInfo) string {
	if hp := c.findHardwareProfileFor(hw); hp != nil {
		return hp.Hardware.GPU.RuntimeClassName
	}
	return ""
}

// FindHardwareTDP returns the TDP (watts) for the hardware profile matching
// the given hardware. Returns 0 if no matching profile or TDP is not set.
func (c *Catalog) FindHardwareTDP(hw HardwareInfo) int {
	if hp := c.findHardwareProfileFor(hw); hp != nil {
		return hp.Constraints.TDPWatts
	}
	return 0
}

func platformInList(platform string, platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, p := range platforms {
		if p == platform {
			return true
		}
	}
	return false
}

func (c *Catalog) findModelVariant(modelName, engineQuery string, engine *EngineAsset, hw HardwareInfo) (*ModelAsset, *ModelVariant, error) {
	type rankedVariant struct {
		variant *ModelVariant
		rank    int
	}

	matchRank := func(v *ModelVariant) int {
		if engine != nil && strings.EqualFold(v.Engine, engine.Metadata.Name) {
			return 0
		}
		if strings.EqualFold(v.Engine, engineQuery) {
			return 1
		}
		if engine != nil && strings.EqualFold(v.Engine, engine.Metadata.Type) {
			return 2
		}
		return -1
	}

	for i := range c.ModelAssets {
		ma := &c.ModelAssets[i]
		if !strings.EqualFold(ma.Metadata.Name, modelName) {
			continue
		}
		// Find best variant: gpu_arch+gpu_model > gpu_arch > wildcard.
		// Filter by VRAM and unified_memory when hardware info is available.
		var gpuModelMatch, archMatch, wildcardMatch *rankedVariant
		for j := range ma.Variants {
			v := &ma.Variants[j]
			rank := matchRank(v)
			if rank < 0 {
				continue
			}
			// VRAM filter: skip variants requiring more VRAM than available (per-GPU)
			if hw.GPUVRAMMiB > 0 && v.Hardware.VRAMMinMiB > 0 && v.Hardware.VRAMMinMiB > hw.GPUVRAMMiB {
				continue
			}
			// GPU count filter: skip variants requiring more GPUs than available
			if v.Hardware.GPUCountMin > 0 && hw.GPUCount > 0 && hw.GPUCount < v.Hardware.GPUCountMin {
				continue
			}
			// Unified memory filter: skip mismatched variants
			if v.Hardware.UnifiedMemory != nil && hw.GPUVRAMMiB > 0 {
				if *v.Hardware.UnifiedMemory != hw.UnifiedMemory {
					continue
				}
			}
			if v.Hardware.GPUArch == hw.GPUArch {
				// GPU model match: highest priority (e.g. "RTX 4060" vs "RTX 4090")
				if v.Hardware.GPUModel != "" && hw.GPUModel != "" &&
					strings.Contains(strings.ToUpper(hw.GPUModel), strings.ToUpper(v.Hardware.GPUModel)) {
					if gpuModelMatch == nil || rank < gpuModelMatch.rank {
						gpuModelMatch = &rankedVariant{variant: v, rank: rank}
					}
				}
				if archMatch == nil || rank < archMatch.rank {
					archMatch = &rankedVariant{variant: v, rank: rank}
				}
			}
			if v.Hardware.GPUArch == "*" && (wildcardMatch == nil || rank < wildcardMatch.rank) {
				wildcardMatch = &rankedVariant{variant: v, rank: rank}
			}
		}
		if gpuModelMatch != nil {
			return ma, gpuModelMatch.variant, nil
		}
		if archMatch != nil {
			return ma, archMatch.variant, nil
		}
		if wildcardMatch != nil {
			return ma, wildcardMatch.variant, nil
		}
		return nil, nil, fmt.Errorf("no variant of model %q for engine %q gpu_arch %q (vram %d MiB)", modelName, engineQuery, hw.GPUArch, hw.GPUVRAMMiB)
	}
	return nil, nil, fmt.Errorf("model %q not found in catalog", modelName)
}

func (c *Catalog) findPartitionByName(hw HardwareInfo, name string) *PartitionStrategy {
	if name != "" {
		for i := range c.PartitionStrategies {
			if strings.EqualFold(c.PartitionStrategies[i].Metadata.Name, name) {
				return &c.PartitionStrategies[i]
			}
		}
	}
	return c.findPartition(hw)
}

func (c *Catalog) findPartition(hw HardwareInfo) *PartitionStrategy {
	// Try specific hardware_profile match first, then wildcard.
	// Only considers single_model strategies (or those with no workload_pattern) —
	// dual_model and other non-default patterns must be requested explicitly.
	var wildcard *PartitionStrategy
	profileName := hw.HardwareProfile
	if profileName == "" {
		if hp := c.findHardwareProfileFor(hw); hp != nil {
			profileName = hp.Metadata.Name
		}
	}

	for i := range c.PartitionStrategies {
		ps := &c.PartitionStrategies[i]
		// Skip strategies designed for non-default workload patterns (e.g. dual_model).
		if ps.Target.WorkloadPattern != "" && ps.Target.WorkloadPattern != "single_model" {
			continue
		}
		if ps.Target.HardwareProfile == profileName && profileName != "" {
			return ps
		}
		if ps.Target.HardwareProfile == "*" {
			wildcard = ps
		}
	}
	return wildcard
}

func pickSlot(ps *PartitionStrategy, overrides map[string]any) *PartitionSlot {
	if ps == nil {
		return &PartitionSlot{Name: "default"}
	}

	slotName := "primary"
	if s, ok := overrides["slot"]; ok {
		slotName = fmt.Sprint(s)
	}

	for _, sd := range ps.Slots {
		if strings.EqualFold(sd.Name, slotName) {
			return &PartitionSlot{
				Name:            sd.Name,
				GPUCount:        sd.GPU.Count,
				GPUMemoryMiB:    sd.GPU.MemoryMiB,
				GPUCoresPercent: sd.GPU.CoresPercent,
				CPUCores:        sd.CPU.Cores,
				RAMMiB:          sd.RAM.MiB,
			}
		}
	}

	// Slot name not found; return the first non-system slot
	for _, sd := range ps.Slots {
		if !strings.EqualFold(sd.Name, "system_reserved") {
			return &PartitionSlot{
				Name:            sd.Name,
				GPUCount:        sd.GPU.Count,
				GPUMemoryMiB:    sd.GPU.MemoryMiB,
				GPUCoresPercent: sd.GPU.CoresPercent,
				CPUCores:        sd.CPU.Cores,
				RAMMiB:          sd.RAM.MiB,
			}
		}
	}

	return &PartitionSlot{Name: "default"}
}

func availableGPUCount(hw HardwareInfo, slot *PartitionSlot) int {
	slotCount := 0
	if slot != nil {
		slotCount = slot.GPUCount
	}

	switch {
	case hw.GPUCount > 0 && slotCount > 0:
		if slotCount < hw.GPUCount {
			return slotCount
		}
		return hw.GPUCount
	case slotCount > 0:
		return slotCount
	default:
		return hw.GPUCount
	}
}

// FallbackEngine is the engine type used when no better match is found.
// All code should reference this constant instead of hardcoding "llamacpp".
const FallbackEngine = "llamacpp"

// FormatToEngine returns the engine type for a given model file format,
// derived from the catalog's engine assets (supported_formats field).
// Returns "" if no engine declares support for the format.
func (c *Catalog) FormatToEngine(format string) string {
	lower := strings.ToLower(format)
	for _, ea := range c.EngineAssets {
		for _, f := range ea.Metadata.SupportedFormats {
			if strings.EqualFold(f, lower) {
				return ea.Metadata.Type
			}
		}
	}
	return ""
}

// DefaultEngine returns the fallback engine type from the catalog.
// Priority: explicit default: true in metadata, then first wildcard gpu_arch engine.
func (c *Catalog) DefaultEngine() string {
	for _, ea := range c.EngineAssets {
		if ea.Metadata.Default {
			return ea.Metadata.Type
		}
	}
	for _, ea := range c.EngineAssets {
		if ea.Hardware.GPUArch == "*" {
			return ea.Metadata.Type
		}
	}
	return FallbackEngine
}

// ScanMetadata holds model metadata collected during filesystem scan,
// used to build intelligent synthetic ModelAssets when no YAML exists.
type ScanMetadata struct {
	Name         string
	Type         string
	Family       string
	ParamCount   string
	Format       string
	SizeBytes    int64
	TotalParams  int64
	ActiveParams int64
	Quantization string
	ModelClass   string
}

// bytesPerParam returns the memory bytes per parameter for a quantization format.
func bytesPerParam(quantization string) float64 {
	switch strings.ToLower(quantization) {
	case "fp32":
		return 4.0
	case "fp16", "bf16":
		return 2.0
	case "fp8", "int8":
		return 1.0
	case "int5", "int6":
		return 0.75
	case "int4", "nf4":
		return 0.5
	default:
		return 2.0 // conservative: assume FP16
	}
}

// estimateVRAMMiB estimates GPU memory requirements from scan metadata.
// Returns 0 when insufficient data is available (graceful degradation).
func estimateVRAMMiB(meta ScanMetadata) int {
	var weightsMiB int
	switch {
	case meta.SizeBytes > 0:
		weightsMiB = int(meta.SizeBytes / (1024 * 1024))
	case meta.TotalParams > 0:
		bpp := bytesPerParam(meta.Quantization)
		weightsMiB = int(float64(meta.TotalParams) * bpp / (1024 * 1024))
	default:
		return 0
	}
	overheadMiB := weightsMiB / 4
	if overheadMiB < 1024 {
		overheadMiB = 1024
	}
	return weightsMiB + overheadMiB
}

// inferTP calculates the minimum tensor_parallel_size needed.
func inferTP(estimatedVRAM int, hw HardwareInfo) int {
	if hw.GPUVRAMMiB == 0 || hw.GPUCount == 0 || estimatedVRAM == 0 {
		return 1
	}
	perGPU := hw.GPUVRAMMiB
	if hw.UnifiedMemory && hw.RAMTotalMiB > 0 {
		osReserve := 16384
		if hw.RAMTotalMiB < 65536 {
			osReserve = 8192
		}
		perGPU = hw.RAMTotalMiB - osReserve
	}
	if perGPU <= 0 {
		return 1
	}
	if estimatedVRAM <= int(float64(perGPU)*0.85) {
		return 1
	}
	if hw.GPUCount <= 1 {
		return 1
	}
	needed := (estimatedVRAM + int(float64(perGPU)*0.80) - 1) / int(float64(perGPU)*0.80)
	tp := nextPowerOf2(needed)
	if tp > hw.GPUCount {
		tp = hw.GPUCount
	}
	return tp
}

func nextPowerOf2(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p *= 2
	}
	return p
}

func ceilDiv(n, d int) int {
	if n <= 0 || d <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

// inferGMU calculates a safe gpu_memory_utilization value.
// Returns 0 when hardware info is insufficient (let engine defaults apply).
func inferGMU(estimatedVRAM int, hw HardwareInfo) float64 {
	if hw.UnifiedMemory && hw.RAMTotalMiB > 0 {
		osReserve := 16384
		if hw.RAMTotalMiB < 65536 {
			osReserve = 8192
		}
		gmu := float64(hw.RAMTotalMiB-osReserve) / float64(hw.RAMTotalMiB)
		if gmu > 0.85 {
			gmu = 0.85
		}
		if gmu < 0.30 {
			gmu = 0.30
		}
		return math.Floor(gmu*100) / 100
	}
	if hw.GPUVRAMMiB > 0 && estimatedVRAM > 0 {
		gmu := float64(estimatedVRAM) / (float64(hw.GPUVRAMMiB) * 0.95)
		if gmu > 0.90 {
			gmu = 0.90
		}
		if gmu < 0.50 {
			gmu = 0.50
		}
		return math.Floor(gmu*100) / 100
	}
	return 0
}

// inferMaxModelLen returns a conservative max_model_len based on estimated VRAM.
func inferMaxModelLen(estimatedVRAM int) int {
	switch {
	case estimatedVRAM < 4096:
		return 2048
	case estimatedVRAM < 8192:
		return 4096
	case estimatedVRAM < 32768:
		return 8192
	default:
		return 16384
	}
}

// BuildSyntheticModelAsset creates a ModelAsset from scan-detected metadata
// for models that have no YAML catalog entry. When hardware info is available,
// generates variants with VRAM estimates, TP, and GMU to prevent OOM.
// Falls back to wildcard variants when hardware is unknown.
func (c *Catalog) BuildSyntheticModelAsset(meta ScanMetadata, hw HardwareInfo, requestedEngines ...string) ModelAsset {
	if meta.Type == "" {
		meta.Type = "llm"
	}
	inferredEngineType := c.FormatToEngine(meta.Format)
	if inferredEngineType == "" {
		inferredEngineType = c.DefaultEngine()
	}

	estimatedVRAM := estimateVRAMMiB(meta)
	defaultEngine := c.DefaultEngine()

	var variants []ModelVariant
	var targetedHW *ModelVariantHardware
	var targetedCfg map[string]any

	// When hardware is known, generate a targeted variant with resource estimates
	if hw.GPUArch != "" && hw.GPUVRAMMiB > 0 && estimatedVRAM > 0 {
		tp := inferTP(estimatedVRAM, hw)
		gmu := inferGMU(estimatedVRAM, hw)
		maxLen := inferMaxModelLen(estimatedVRAM)
		perGPUVRAM := estimatedVRAM
		if tp > 1 {
			perGPUVRAM = ceilDiv(estimatedVRAM, tp)
		}

		hwSpec := ModelVariantHardware{
			GPUArch:    hw.GPUArch,
			VRAMMinMiB: perGPUVRAM,
		}
		if hw.UnifiedMemory {
			um := true
			hwSpec.UnifiedMemory = &um
		}
		if tp > 1 {
			hwSpec.GPUCountMin = tp
		}

		cfg := map[string]any{"max_model_len": maxLen}
		if gmu > 0 {
			cfg["gpu_memory_utilization"] = gmu
		}
		if tp > 1 {
			cfg["tensor_parallel_size"] = tp
		}
		targetedHWCopy := hwSpec
		targetedHW = &targetedHWCopy
		targetedCfg = cfg

		variants = append(variants, ModelVariant{
			Name:          meta.Name + "-" + hw.GPUArch + "-auto",
			Hardware:      hwSpec,
			Engine:        inferredEngineType,
			Format:        meta.Format,
			DefaultConfig: cfg,
			ExpectedPerformance: map[string]any{
				"vram_mib": estimatedVRAM,
				"notes":    "auto-estimated from scan metadata",
			},
		})
	}

	// Wildcard fallback (always present)
	wildcardHW := ModelVariantHardware{GPUArch: "*"}
	if estimatedVRAM > 0 {
		wildcardHW.VRAMMinMiB = estimatedVRAM
	}
	variants = append(variants, ModelVariant{
		Name:     meta.Name + "-auto",
		Hardware: wildcardHW,
		Engine:   inferredEngineType,
		Format:   meta.Format,
	})

	if inferredEngineType != defaultEngine {
		fbHW := ModelVariantHardware{GPUArch: "*"}
		if estimatedVRAM > 0 {
			fbHW.VRAMMinMiB = estimatedVRAM
		}
		variants = append(variants, ModelVariant{
			Name:     meta.Name + "-auto-fallback",
			Hardware: fbHW,
			Engine:   defaultEngine,
			Format:   meta.Format,
		})
	}

	for _, re := range requestedEngines {
		if re == "" || re == inferredEngineType || re == defaultEngine {
			continue
		}
		if targetedHW != nil {
			cfg := make(map[string]any, len(targetedCfg))
			for k, v := range targetedCfg {
				cfg[k] = v
			}
			hwSpec := *targetedHW
			variants = append(variants, ModelVariant{
				Name:          meta.Name + "-" + hw.GPUArch + "-" + re + "-auto",
				Hardware:      hwSpec,
				Engine:        re,
				Format:        meta.Format,
				DefaultConfig: cfg,
				ExpectedPerformance: map[string]any{
					"vram_mib": estimatedVRAM,
					"notes":    "auto-estimated from scan metadata",
				},
			})
		}
		reHW := ModelVariantHardware{GPUArch: "*"}
		if estimatedVRAM > 0 {
			reHW.VRAMMinMiB = estimatedVRAM
		}
		variants = append(variants, ModelVariant{
			Name:     meta.Name + "-" + re,
			Hardware: reHW,
			Engine:   re,
			Format:   meta.Format,
		})
	}

	return ModelAsset{
		Kind: "model_asset",
		Metadata: ModelMetadata{
			Name:           meta.Name,
			Type:           meta.Type,
			Family:         meta.Family,
			ParameterCount: meta.ParamCount,
		},
		Storage: ModelStorage{
			Formats: []string{meta.Format},
		},
		Variants:  variants,
		synthetic: true,
	}
}

// RegisterModel appends a ModelAsset to the catalog if no asset with the
// same name already exists. Safe for concurrent use.
func (c *Catalog) RegisterModel(ma ModelAsset) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.ModelAssets {
		if strings.EqualFold(existing.Metadata.Name, ma.Metadata.Name) {
			return
		}
	}
	c.ModelAssets = append(c.ModelAssets, ma)
}

// HasSyntheticModel reports whether the catalog currently contains a synthetic
// model asset with the given name.
func (c *Catalog) HasSyntheticModel(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.ModelAssets {
		if strings.EqualFold(c.ModelAssets[i].Metadata.Name, name) {
			return c.ModelAssets[i].synthetic
		}
	}
	return false
}

// UpsertSyntheticModel stores a synthetic model asset, replacing an older
// synthetic entry with the same name while leaving catalog-backed assets intact.
func (c *Catalog) UpsertSyntheticModel(ma ModelAsset) {
	ma.synthetic = true

	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.ModelAssets {
		if !strings.EqualFold(c.ModelAssets[i].Metadata.Name, ma.Metadata.Name) {
			continue
		}
		if c.ModelAssets[i].synthetic {
			c.ModelAssets[i] = ma
		}
		return
	}
	c.ModelAssets = append(c.ModelAssets, ma)
}

// estimateResources computes cost(path, R) — the full resource consumption estimate.
func estimateResources(engine *EngineAsset, variant *ModelVariant, hw HardwareInfo) *ResourceEstimate {
	perf := variant.ParsedExpectedPerf()
	est := &ResourceEstimate{
		VRAMMiB: perf.VRAMMiB,
		RAMMiB:  perf.RAMMiB,
	}
	if est.VRAMMiB == 0 {
		est.VRAMMiB = variant.Hardware.VRAMMinMiB
	}
	if est.RAMMiB == 0 {
		est.RAMMiB = 2048 // default engine process overhead
	}

	est.CPUCores = perf.CPUCores
	if est.CPUCores == 0 {
		est.CPUCores = 4 // reasonable default
	}

	est.DiskMiB = perf.DiskMiB

	// Power from engine typical draw midpoint
	if len(engine.PowerConstraints.TypicalDrawWatts) >= 2 {
		est.PowerWatts = (engine.PowerConstraints.TypicalDrawWatts[0] + engine.PowerConstraints.TypicalDrawWatts[1]) / 2
	}

	return est
}

// FitReport describes how well a resolved config fits the actual hardware.
type FitReport struct {
	Fit         bool           // true if config can run (possibly with adjustments)
	Warnings    []string       // non-fatal issues
	Adjustments map[string]any // suggested config overrides (e.g. gpu_memory_utilization)
	Reason      string         // if Fit==false, why
}

// gmuKeys lists the config keys that control GPU memory fraction across engines.
// vLLM uses gpu_memory_utilization, SGLang uses mem_fraction_static.
var gmuKeys = []string{"gpu_memory_utilization", "mem_fraction_static"}

// CheckFit validates a resolved config against hardware capabilities and runtime state.
// Static layer: VRAM sufficiency (already handled by variant filtering in findModelVariant).
// Dynamic layer: adjusts gpu_memory_utilization based on available GPU memory.
// Zero-valued hw fields are skipped (graceful degradation when metrics unavailable).
func CheckFit(resolved *ResolvedConfig, hw HardwareInfo) *FitReport {
	r := &FitReport{Fit: true, Adjustments: make(map[string]any)}

	// Unified memory guard: GPU allocation directly reduces available system memory.
	// Enforce minimum OS reserve to prevent starvation / swap thrashing.
	if hw.UnifiedMemory && hw.RAMTotalMiB > 0 {
		const (
			minReserveLargeMiB = 16384 // ≥64GB systems reserve 16GB for OS
			minReserveSmallMiB = 8192  // <64GB systems reserve 8GB for OS
			largeSystemMiB     = 65536 // 64GB threshold
		)
		reserveMiB := minReserveLargeMiB
		if hw.RAMTotalMiB < largeSystemMiB {
			reserveMiB = minReserveSmallMiB
		}

		for _, key := range gmuKeys {
			val, ok := resolved.Config[key]
			if !ok {
				continue
			}
			gmu := toFloat64(val)
			if gmu <= 0 {
				continue
			}

			allocMiB := int(float64(hw.RAMTotalMiB) * gmu)
			remainMiB := hw.RAMTotalMiB - allocMiB

			if remainMiB < reserveMiB {
				maxSafe := math.Floor(float64(hw.RAMTotalMiB-reserveMiB)/float64(hw.RAMTotalMiB)*100) / 100
				if maxSafe < 0.1 {
					r.Fit = false
					r.Reason = fmt.Sprintf("unified memory: %s=%.2f leaves only %d MiB for OS (need at least %d MiB)",
						key, gmu, remainMiB, reserveMiB)
					return r
				}
				r.Adjustments[key] = maxSafe
				r.Warnings = append(r.Warnings, fmt.Sprintf(
					"unified memory: %s %.2f -> %.2f (OS available %d -> %d MiB, total %d MiB)",
					key, gmu, maxSafe, remainMiB,
					hw.RAMTotalMiB-int(float64(hw.RAMTotalMiB)*maxSafe), hw.RAMTotalMiB))
			}
			break // each engine uses only one gmu parameter
		}
	}

	// Dynamic layer: adjust memory fraction based on free GPU memory.
	// Checks both vLLM (gpu_memory_utilization) and SGLang (mem_fraction_static).
	if hw.GPUMemFreeMiB > 0 {
		totalVRAM := hw.GPUVRAMMiB
		if totalVRAM == 0 {
			totalVRAM = hw.GPUMemFreeMiB + hw.GPUMemUsedMiB
		}
		if totalVRAM > 0 {
			for _, key := range gmuKeys {
				gmu, ok := resolved.Config[key]
				if !ok {
					continue
				}
				currentGMU := toFloat64(gmu)
				if currentGMU <= 0 {
					continue
				}
				safetyMiB := 512
				if hw.UnifiedMemory {
					safetyMiB = 4096 // unified memory needs larger dynamic safety margin
				}
				maxSafeGMU := float64(hw.GPUMemFreeMiB-safetyMiB) / float64(totalVRAM)
				if maxSafeGMU < 0.1 {
					r.Fit = false
					r.Reason = fmt.Sprintf("GPU memory insufficient: only %.1f%% usable (need ≥10%%); %d MiB free / %d MiB total, %d MiB safety reserve",
						maxSafeGMU*100, hw.GPUMemFreeMiB, totalVRAM, safetyMiB)
					return r
				}
				if currentGMU > maxSafeGMU {
					adjusted := math.Floor(maxSafeGMU*100) / 100
					r.Adjustments[key] = adjusted
					r.Warnings = append(r.Warnings, fmt.Sprintf(
						"%s: %.2f -> %.2f (GPU %d/%d MiB free)",
						key, currentGMU, adjusted, hw.GPUMemFreeMiB, totalVRAM))
				}
				break // each engine uses only one gmu parameter
			}
		}
	}

	// GPU count check: tensor_parallel_size must not exceed the GPUs available to this slot.
	if gpuCount := availableGPUCount(hw, resolved.Partition); gpuCount > 0 {
		if tp, ok := resolved.Config["tensor_parallel_size"]; ok {
			tpSize := int(toFloat64(tp))
			if tpSize > gpuCount {
				r.Fit = false
				r.Reason = fmt.Sprintf("tensor_parallel_size=%d exceeds available GPU count=%d", tpSize, gpuCount)
				return r
			}
		}
	}

	// Power budget check: warn if engine typical power may exceed hardware TDP
	if hw.TDPWatts > 0 && resolved.EnginePowerWattsMax > 0 {
		if resolved.EnginePowerWattsMin > hw.TDPWatts {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"engine minimum power draw (%d W) exceeds hardware TDP (%d W)",
				resolved.EnginePowerWattsMin, hw.TDPWatts))
		} else if resolved.EnginePowerWattsMax > hw.TDPWatts {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"engine power draw may reach %d W, exceeding hardware TDP (%d W)",
				resolved.EnginePowerWattsMax, hw.TDPWatts))
		}
	}

	// RAM sufficiency check: reject if estimated RAM exceeds available
	if resolved.ResourceEstimate != nil && hw.RAMAvailMiB > 0 && resolved.ResourceEstimate.RAMMiB > 0 {
		if resolved.ResourceEstimate.RAMMiB > hw.RAMAvailMiB {
			r.Fit = false
			r.Reason = fmt.Sprintf("insufficient RAM: need %d MiB, available %d MiB",
				resolved.ResourceEstimate.RAMMiB, hw.RAMAvailMiB)
			return r
		}
	}

	// RAM check
	if hw.RAMAvailMiB > 0 && hw.RAMAvailMiB < 2048 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("low available RAM: %d MiB", hw.RAMAvailMiB))
	}

	// Unified memory + swap warning: swap prevents clean OOM-kill,
	// leading to swap thrashing instead when gmu is high.
	if hw.UnifiedMemory && hw.SwapTotalMiB > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"unified memory system has swap enabled (%d MiB); high gmu may cause swap thrashing instead of clean OOM-kill",
			hw.SwapTotalMiB))
	}

	return r
}

func toFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return 0
	}
}
