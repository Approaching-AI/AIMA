package runtime

import "github.com/jguan/aima/internal/knowledge"

// ResourceSlot abstracts GPU/CPU/RAM resource allocation with pluggable backends.
type ResourceSlot interface {
	GPUAllocation() GPUAlloc
	CPUAllocation() CPUAlloc
	RAMAllocation() RAMAlloc
	Backend() string
}

// GPUAlloc describes GPU resource allocation.
type GPUAlloc struct {
	Count        int
	MemoryMiB    int
	CoresPercent int
	ResourceName string // K8s resource name
}

// CPUAlloc describes CPU resource allocation.
type CPUAlloc struct {
	Cores int
}

// RAMAlloc describes memory resource allocation.
type RAMAlloc struct {
	MiB int
}

// EngineParamSlot implements ResourceSlot using engine config parameters (current behavior).
type EngineParamSlot struct {
	partition *knowledge.PartitionSlot
}

// NewEngineParamSlot wraps a PartitionSlot as a ResourceSlot.
func NewEngineParamSlot(p *knowledge.PartitionSlot) *EngineParamSlot {
	if p == nil {
		p = &knowledge.PartitionSlot{Name: "default"}
	}
	return &EngineParamSlot{partition: p}
}

func (s *EngineParamSlot) GPUAllocation() GPUAlloc {
	return GPUAlloc{
		Count:        s.partition.GPUCount,
		MemoryMiB:    s.partition.GPUMemoryMiB,
		CoresPercent: s.partition.GPUCoresPercent,
	}
}

func (s *EngineParamSlot) CPUAllocation() CPUAlloc {
	return CPUAlloc{Cores: s.partition.CPUCores}
}

func (s *EngineParamSlot) RAMAllocation() RAMAlloc {
	return RAMAlloc{MiB: s.partition.RAMMiB}
}

func (s *EngineParamSlot) Backend() string { return "engine_param" }

// HAMiSlot implements ResourceSlot using HAMi vGPU allocation.
type HAMiSlot struct {
	gpuCount     int
	gpuMemoryMiB int
	cpuCores     int
	ramMiB       int
	resourceName string
}

// NewHAMiSlot creates a HAMi-backed ResourceSlot.
func NewHAMiSlot(gpuCount, gpuMemoryMiB, cpuCores, ramMiB int, resourceName string) *HAMiSlot {
	return &HAMiSlot{
		gpuCount:     gpuCount,
		gpuMemoryMiB: gpuMemoryMiB,
		cpuCores:     cpuCores,
		ramMiB:       ramMiB,
		resourceName: resourceName,
	}
}

func (s *HAMiSlot) GPUAllocation() GPUAlloc {
	return GPUAlloc{
		Count:        s.gpuCount,
		MemoryMiB:    s.gpuMemoryMiB,
		ResourceName: s.resourceName,
	}
}

func (s *HAMiSlot) CPUAllocation() CPUAlloc { return CPUAlloc{Cores: s.cpuCores} }
func (s *HAMiSlot) RAMAllocation() RAMAlloc { return RAMAlloc{MiB: s.ramMiB} }
func (s *HAMiSlot) Backend() string         { return "hami" }

// MIGSlot is a stub for NVIDIA MIG partitioning (future).
type MIGSlot struct{}

func (s *MIGSlot) GPUAllocation() GPUAlloc { return GPUAlloc{} }
func (s *MIGSlot) CPUAllocation() CPUAlloc { return CPUAlloc{} }
func (s *MIGSlot) RAMAllocation() RAMAlloc { return RAMAlloc{} }
func (s *MIGSlot) Backend() string         { return "mig" }

// MPSSlot is a stub for NVIDIA MPS sharing (future).
type MPSSlot struct{}

func (s *MPSSlot) GPUAllocation() GPUAlloc { return GPUAlloc{} }
func (s *MPSSlot) CPUAllocation() CPUAlloc { return CPUAlloc{} }
func (s *MPSSlot) RAMAllocation() RAMAlloc { return RAMAlloc{} }
func (s *MPSSlot) Backend() string         { return "mps" }
