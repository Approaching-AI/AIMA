package knowledge

import (
	"fmt"
	"strings"
)

// HardwareInfo describes the detected hardware for config resolution.
type HardwareInfo struct {
	GPUArch         string
	CPUArch         string
	HardwareProfile string // Name of a matching HardwareProfile, if known
	Platform        string // "linux/amd64", "darwin/arm64", etc.
	RuntimeType     string // "k3s" or "native"
}

// PartitionSlot holds the resource allocation for a single deployment slot.
type PartitionSlot struct {
	Name            string
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
	HealthCheck     *HealthCheck
	Warmup          *WarmupConfig // post-healthcheck warmup config (nil = no warmup)
	Source          *EngineSource // native binary source info (nil if container-only)
	GPUResourceName       string // K8s resource name, e.g. "nvidia.com/gpu" (default if empty)
	RuntimeClassName      string // K8s runtimeClassName for GPU containers, e.g. "nvidia" (from hardware profile)
	RuntimeRecommendation string // "native" or "container" or "" — from engine's platform_recommendations
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

	model, variant, err := c.findModelVariant(modelName, engineType, hw.GPUArch)
	if err != nil {
		return nil, err
	}

	partition := c.findPartition(hw)
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

	// L1: User overrides
	for k, v := range userOverrides {
		config[k] = v
		provenance[k] = "L1"
	}

	resolved := &ResolvedConfig{
		Engine:      engineType,
		EngineImage: engine.Image.Name + ":" + engine.Image.Tag,
		ModelName:   model.Metadata.Name,
		Slot:        slot.Name,
		Config:      config,
		Provenance:  provenance,
		Partition:   slot,
		Command:     engine.Startup.Command,
		HealthCheck: &engine.Startup.HealthCheck,
		Source:      engine.Source,
	}
	if engine.Startup.Warmup.Enabled {
		resolved.Warmup = &engine.Startup.Warmup
	}

	// Set GPU resource name and runtimeClassName from hardware profile
	resolved.GPUResourceName = c.findGPUResourceName(hw)
	resolved.RuntimeClassName = c.findRuntimeClassName(hw)

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
// Priority: exact gpu_arch match first, then wildcard.
func (c *Catalog) InferEngineType(modelName string, hw HardwareInfo) (string, error) {
	for _, ma := range c.ModelAssets {
		if ma.Metadata.Name != modelName {
			continue
		}
		// First pass: exact gpu_arch match
		for _, v := range ma.Variants {
			if v.Hardware.GPUArch == hw.GPUArch {
				if _, err := c.findEngine(v.Engine, hw); err == nil {
					return v.Engine, nil
				}
			}
		}
		// Second pass: wildcard variant
		for _, v := range ma.Variants {
			if v.Hardware.GPUArch == "*" {
				if _, err := c.findEngine(v.Engine, hw); err == nil {
					return v.Engine, nil
				}
			}
		}
		return "", fmt.Errorf("no compatible engine for model %q on gpu_arch %q", modelName, hw.GPUArch)
	}
	return "", fmt.Errorf("model %q not found in catalog", modelName)
}

// findGPUResourceName looks up the K8s GPU resource name from hardware profiles.
// Falls back to "nvidia.com/gpu" if not specified.
func (c *Catalog) findGPUResourceName(hw HardwareInfo) string {
	for _, hp := range c.HardwareProfiles {
		if hp.Hardware.GPU.Arch == hw.GPUArch && hp.Hardware.GPU.ResourceName != "" {
			return hp.Hardware.GPU.ResourceName
		}
	}
	return "nvidia.com/gpu"
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

func (c *Catalog) findModelVariant(modelName, engineType, gpuArch string) (*ModelAsset, *ModelVariant, error) {
	for i := range c.ModelAssets {
		ma := &c.ModelAssets[i]
		if ma.Metadata.Name != modelName {
			continue
		}
		// Find best variant: exact gpu_arch+engine match first, then wildcard
		var wildcard *ModelVariant
		for j := range ma.Variants {
			v := &ma.Variants[j]
			if v.Engine != engineType {
				continue
			}
			if v.Hardware.GPUArch == gpuArch {
				return ma, v, nil
			}
			if v.Hardware.GPUArch == "*" {
				wildcard = v
			}
		}
		if wildcard != nil {
			return ma, wildcard, nil
		}
		// Model found but no variant matches engine+arch
		return nil, nil, fmt.Errorf("no variant of model %q for engine %q gpu_arch %q", modelName, engineType, gpuArch)
	}
	return nil, nil, fmt.Errorf("model %q not found in catalog", modelName)
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
				GPUMemoryMiB:    sd.GPU.MemoryMiB,
				GPUCoresPercent: sd.GPU.CoresPercent,
				CPUCores:        sd.CPU.Cores,
				RAMMiB:          sd.RAM.MiB,
			}
		}
	}

	return &PartitionSlot{Name: "default"}
}

// formatEngineMap maps model file formats to the preferred engine type.
var formatEngineMap = map[string]string{
	"safetensors": "vllm",
	"gguf":        "llamacpp",
}

// BuildSyntheticModelAsset creates a ModelAsset from scan-detected metadata
// for models that have no YAML catalog entry. Uses wildcard gpu_arch="*"
// so it matches any hardware, and relies on engine L0 defaults for config.
func BuildSyntheticModelAsset(name, modelType, family, paramCount, format string) ModelAsset {
	if modelType == "" {
		modelType = "llm"
	}
	engineType := "llamacpp" // most permissive fallback
	if et, ok := formatEngineMap[strings.ToLower(format)]; ok {
		engineType = et
	}

	variants := []ModelVariant{{
		Name:     name + "-auto",
		Hardware: ModelVariantHardware{GPUArch: "*"},
		Engine:   engineType,
		Format:   format,
	}}
	// Add llamacpp fallback when primary is a container-only engine (e.g. vllm).
	// InferEngineType tries each variant's engine via findEngine; if vllm is
	// unavailable (native runtime, no Source), it falls through to llamacpp.
	if engineType != "llamacpp" {
		variants = append(variants, ModelVariant{
			Name:     name + "-auto-fallback",
			Hardware: ModelVariantHardware{GPUArch: "*"},
			Engine:   "llamacpp",
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
		if existing.Metadata.Name == ma.Metadata.Name {
			return
		}
	}
	c.ModelAssets = append(c.ModelAssets, ma)
}
