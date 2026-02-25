package knowledge

import (
	"fmt"
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
	Engine      string
	EngineImage string
	ModelPath   string
	ModelName   string
	Slot        string
	Config      map[string]any
	Provenance  map[string]string
	Partition   *PartitionSlot
	Command     []string
	HealthCheck *HealthCheck
}

// Resolve finds the best config by merging L0 (engine defaults) -> model variant defaults -> L1 (user overrides).
// L2 (knowledge notes from DB) is not applied here; it is merged by the caller when available.
func (c *Catalog) Resolve(hw HardwareInfo, modelName, engineType string, userOverrides map[string]any) (*ResolvedConfig, error) {
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
	// Try specific hardware_profile match first, then wildcard
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
