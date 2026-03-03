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
	GPUVRAMMiB      int  // Total GPU VRAM (0 = unknown, skip VRAM checks)
	GPUCount        int  // Number of GPUs
	UnifiedMemory   bool // GPU shares system RAM (Apple M-series, GB10, AMD APU)
	CPUArch         string
	CPUCores        int // Physical CPU core count
	RAMTotalMiB     int // Total system RAM
	HardwareProfile string // Name of a matching HardwareProfile, if known
	Platform        string // "linux/amd64", "darwin/arm64", etc.
	RuntimeType     string // "k3s" or "native"
	SwapTotalMiB int // Total swap space (0 = unknown or none)
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
	Engine          string
	EngineImage     string
	ModelPath       string
	ModelName       string
	Slot            string
	Config          map[string]any
	Provenance      map[string]string
	Partition       *PartitionSlot
	Command         []string
	InitCommands    []string       // pre-commands to run before main server (from engine YAML)
	ExtraVolumes    []ContainerVolume // additional host volumes to mount (from engine YAML)
	HealthCheck     *HealthCheck
	Warmup          *WarmupConfig // post-healthcheck warmup config (nil = no warmup)
	Source          *EngineSource // native binary source info (nil if container-only)
	Env                   map[string]string // Extra env vars for the container (from engine YAML)
	GPUResourceName       string            // K8s resource name, e.g. "nvidia.com/gpu" (empty = no GPU resource request)
	RuntimeClassName      string            // K8s runtimeClassName for GPU containers, e.g. "nvidia" (from hardware profile)
	RuntimeRecommendation string            // "native" or "container" or "" — from engine's platform_recommendations
	CPUArch               string            // CPU architecture (e.g. "amd64", "arm64") — for platform-specific paths
	Container             *ContainerAccess  // vendor-specific container access (devices, env, volumes, security) from hardware profile
	EngineRegistries      []string          // container image registries from engine YAML (for pre-pull fallback)
}

// Resolve finds the best config by merging L0 (engine defaults) -> model variant defaults -> L1 (user overrides).
// L2 (knowledge notes from DB) is not applied here; it is merged by the caller when available.
func (c *Catalog) Resolve(hw HardwareInfo, modelName, engineType string, userOverrides map[string]any) (*ResolvedConfig, error) {
	// Auto-detect engine from model variants when not specified
	if engineType == "" {
		inferred, err := c.InferEngineType(modelName, hw)
		if err != nil {
			return nil, err
		}
		engineType = inferred
	}

	engine, err := c.findEngine(engineType, hw)
	if err != nil {
		return nil, err
	}

	model, variant, err := c.findModelVariant(modelName, engineType, hw)
	if err != nil {
		return nil, err
	}

	var partitionName string
	if pn, ok := userOverrides["partition"]; ok {
		partitionName = fmt.Sprint(pn)
	}
	partition := c.findPartitionByName(hw, partitionName)
	slot := pickSlot(partition, userOverrides)

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

	// L1: User overrides (model_path, partition, slot are handled separately)
	for k, v := range userOverrides {
		if k == "model_path" || k == "partition" || k == "slot" {
			continue
		}
		config[k] = v
		provenance[k] = "L1"
	}

	resolved := &ResolvedConfig{
		Engine:           engineType,
		EngineImage:      engine.Image.Name + ":" + engine.Image.Tag,
		ModelName:        model.Metadata.Name,
		Slot:             slot.Name,
		Config:           config,
		Provenance:       provenance,
		Partition:        slot,
		Command:          engine.Startup.Command,
		InitCommands:     engine.Startup.InitCommands,
		ExtraVolumes:     engine.Startup.ExtraVolumes,
		Env:              engine.Startup.Env,
		HealthCheck:      &engine.Startup.HealthCheck,
		Source:           engine.Source,
		EngineRegistries: engine.Image.Registries,
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

	// Set ModelPath from user overrides or default pattern
	if mp, ok := userOverrides["model_path"]; ok {
		resolved.ModelPath = fmt.Sprint(mp)
	}

	return resolved, nil
}

func (c *Catalog) findEngine(engineType string, hw HardwareInfo) (*EngineAsset, error) {
	// Prefer exact gpu_arch match, then wildcard
	var wildcard *EngineAsset
	for i := range c.EngineAssets {
		ea := &c.EngineAssets[i]
		if ea.Metadata.Type != engineType {
			continue
		}

		// Native runtime: engine must have source and platform must match
		if hw.RuntimeType == "native" {
			if ea.Source == nil {
				continue
			}
			if !platformInList(hw.Platform, ea.Source.Platforms) {
				continue
			}
		}

		if ea.Hardware.GPUArch == hw.GPUArch {
			return ea, nil
		}
		if ea.Hardware.GPUArch == "*" {
			wildcard = ea
		}
	}
	if wildcard != nil {
		return wildcard, nil
	}
	return nil, fmt.Errorf("no engine asset for type %q gpu_arch %q", engineType, hw.GPUArch)
}

// InferEngineType picks the best engine for a model on the given hardware.
// Priority: exact gpu_arch match first, then wildcard. Skips variants whose
// VRAM requirements exceed the detected GPU VRAM.
func (c *Catalog) InferEngineType(modelName string, hw HardwareInfo) (string, error) {
	for _, ma := range c.ModelAssets {
		if !strings.EqualFold(ma.Metadata.Name, modelName) {
			continue
		}
		// First pass: exact gpu_arch match
		for _, v := range ma.Variants {
			if v.Hardware.GPUArch != hw.GPUArch {
				continue
			}
			if hw.GPUVRAMMiB > 0 && v.Hardware.VRAMMinMiB > 0 && v.Hardware.VRAMMinMiB > hw.GPUVRAMMiB {
				continue
			}
			if _, err := c.findEngine(v.Engine, hw); err == nil {
				return v.Engine, nil
			}
		}
		// Second pass: wildcard variant
		for _, v := range ma.Variants {
			if v.Hardware.GPUArch != "*" {
				continue
			}
			if hw.GPUVRAMMiB > 0 && v.Hardware.VRAMMinMiB > 0 && v.Hardware.VRAMMinMiB > hw.GPUVRAMMiB {
				continue
			}
			if _, err := c.findEngine(v.Engine, hw); err == nil {
				return v.Engine, nil
			}
		}
		return "", fmt.Errorf("no compatible engine for model %q on gpu_arch %q (vram %d MiB)", modelName, hw.GPUArch, hw.GPUVRAMMiB)
	}
	return "", fmt.Errorf("model %q not found in catalog", modelName)
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
	ma, variant, err := c.findModelVariant(modelName, engineType, hw)
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
// Returns "" if not specified (no GPU resource request in pod spec).
func (c *Catalog) findGPUResourceName(hw HardwareInfo) string {
	for _, hp := range c.HardwareProfiles {
		if hp.Hardware.GPU.Arch == hw.GPUArch && hp.Hardware.GPU.ResourceName != "" {
			return hp.Hardware.GPU.ResourceName
		}
	}
	return ""
}

// findContainerAccess looks up vendor-specific container access config from hardware profiles.
func (c *Catalog) findContainerAccess(hw HardwareInfo) *ContainerAccess {
	for _, hp := range c.HardwareProfiles {
		if hp.Hardware.GPU.Arch == hw.GPUArch && hp.Container != nil {
			return hp.Container
		}
	}
	return nil
}

// findRuntimeClassName looks up the K8s runtimeClassName from hardware profiles.
// Returns "" if not specified (no runtimeClassName in pod spec).
func (c *Catalog) findRuntimeClassName(hw HardwareInfo) string {
	for _, hp := range c.HardwareProfiles {
		if hp.Hardware.GPU.Arch == hw.GPUArch {
			return hp.Hardware.GPU.RuntimeClassName
		}
	}
	return ""
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

func (c *Catalog) findModelVariant(modelName, engineType string, hw HardwareInfo) (*ModelAsset, *ModelVariant, error) {
	for i := range c.ModelAssets {
		ma := &c.ModelAssets[i]
		if !strings.EqualFold(ma.Metadata.Name, modelName) {
			continue
		}
		// Find best variant: exact gpu_arch+engine match first, then wildcard.
		// Filter by VRAM and unified_memory when hardware info is available.
		var exactMatch, wildcardMatch *ModelVariant
		for j := range ma.Variants {
			v := &ma.Variants[j]
			if v.Engine != engineType {
				continue
			}
			// VRAM filter: skip variants requiring more VRAM than available
			if hw.GPUVRAMMiB > 0 && v.Hardware.VRAMMinMiB > 0 && v.Hardware.VRAMMinMiB > hw.GPUVRAMMiB {
				continue
			}
			// Unified memory filter: skip mismatched variants
			if v.Hardware.UnifiedMemory != nil && hw.GPUVRAMMiB > 0 {
				if *v.Hardware.UnifiedMemory != hw.UnifiedMemory {
					continue
				}
			}
			if v.Hardware.GPUArch == hw.GPUArch {
				exactMatch = v
				break // exact arch + passes filters = best possible
			}
			if v.Hardware.GPUArch == "*" && wildcardMatch == nil {
				wildcardMatch = v
			}
		}
		if exactMatch != nil {
			return ma, exactMatch, nil
		}
		if wildcardMatch != nil {
			return ma, wildcardMatch, nil
		}
		return nil, nil, fmt.Errorf("no variant of model %q for engine %q gpu_arch %q (vram %d MiB)", modelName, engineType, hw.GPUArch, hw.GPUVRAMMiB)
	}
	return nil, nil, fmt.Errorf("model %q not found in catalog", modelName)
}

func (c *Catalog) findPartitionByName(hw HardwareInfo, name string) *PartitionStrategy {
	if name != "" {
		for i := range c.PartitionStrategies {
			if c.PartitionStrategies[i].Metadata.Name == name {
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
		// Try to find a matching hardware profile by gpu arch
		for _, hp := range c.HardwareProfiles {
			if hp.Hardware.GPU.Arch == hw.GPUArch {
				profileName = hp.Metadata.Name
				break
			}
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
		if sd.Name == slotName {
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
		if sd.Name != "system_reserved" {
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

// BuildSyntheticModelAsset creates a ModelAsset from scan-detected metadata
// for models that have no YAML catalog entry. Uses wildcard gpu_arch="*"
// so it matches any hardware, and relies on engine L0 defaults for config.
// The catalog is used to derive the engine type from the model's file format.
func (c *Catalog) BuildSyntheticModelAsset(name, modelType, family, paramCount, format string) ModelAsset {
	if modelType == "" {
		modelType = "llm"
	}
	engineType := c.FormatToEngine(format)
	if engineType == "" {
		engineType = c.DefaultEngine()
	}

	defaultEngine := c.DefaultEngine()
	variants := []ModelVariant{{
		Name:     name + "-auto",
		Hardware: ModelVariantHardware{GPUArch: "*"},
		Engine:   engineType,
		Format:   format,
	}}
	// Add fallback variant when primary is a container-only engine (e.g. vllm).
	// InferEngineType tries each variant's engine via findEngine; if vllm is
	// unavailable (native runtime, no Source), it falls through to the default engine.
	if engineType != defaultEngine {
		variants = append(variants, ModelVariant{
			Name:     name + "-auto-fallback",
			Hardware: ModelVariantHardware{GPUArch: "*"},
			Engine:   defaultEngine,
			Format:   format,
		})
	}

	return ModelAsset{
		Kind: "model_asset",
		Metadata: ModelMetadata{
			Name:           name,
			Type:           modelType,
			Family:         family,
			ParameterCount: paramCount,
		},
		Storage: ModelStorage{
			Formats: []string{format},
		},
		Variants: variants,
	}
}

// RegisterModel appends a ModelAsset to the catalog if no asset with the
// same name already exists.
func (c *Catalog) RegisterModel(ma ModelAsset) {
	for _, existing := range c.ModelAssets {
		if strings.EqualFold(existing.Metadata.Name, ma.Metadata.Name) {
			return
		}
	}
	c.ModelAssets = append(c.ModelAssets, ma)
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
					r.Reason = fmt.Sprintf("GPU memory insufficient: %d MiB free, need > %d MiB", hw.GPUMemFreeMiB, safetyMiB)
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
