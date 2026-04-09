package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"

	state "github.com/jguan/aima/internal"
)

func imageSupportsPlatform(ea *knowledge.EngineAsset, platform string) bool {
	if ea == nil || ea.Image.Name == "" {
		return false
	}
	if platform == "" {
		return true
	}
	return len(ea.Image.Platforms) == 0 || stringInSliceFold(ea.Image.Platforms, platform)
}

func engineMatchesHardware(ea *knowledge.EngineAsset, hw knowledge.HardwareInfo) bool {
	if ea == nil {
		return false
	}
	arch := strings.TrimSpace(ea.Hardware.GPUArch)
	return arch == "" || arch == "*" || strings.EqualFold(arch, hw.GPUArch)
}

func engineSupportsPlatform(ea *knowledge.EngineAsset, platform string) bool {
	if ea == nil || platform == "" {
		return ea != nil
	}
	if ea.Source != nil && ea.Source.Supports(platform) {
		return true
	}
	return imageSupportsPlatform(ea, platform)
}

func engineCompatibleWithHost(ea *knowledge.EngineAsset, hw knowledge.HardwareInfo) bool {
	return engineMatchesHardware(ea, hw) && engineSupportsPlatform(ea, hw.Platform)
}

func preferredEngineRuntimeType(ea *knowledge.EngineAsset, platform string) string {
	if ea == nil {
		return "container"
	}

	recommendation := ea.Runtime.Default
	if platform != "" {
		if rec, ok := ea.Runtime.PlatformRecommendations[platform]; ok && rec != "" {
			recommendation = rec
		}
	}

	switch recommendation {
	case "native":
		if ea.Source != nil && (platform == "" || ea.Source.Supports(platform)) {
			return "native"
		}
		if imageSupportsPlatform(ea, platform) {
			return "container"
		}
	case "container":
		if imageSupportsPlatform(ea, platform) {
			return "container"
		}
		if ea.Source != nil && (platform == "" || ea.Source.Supports(platform)) {
			return "native"
		}
	}

	if ea.Source != nil && (platform == "" || ea.Source.Supports(platform)) {
		return "native"
	}
	if imageSupportsPlatform(ea, platform) {
		return "container"
	}
	return "container"
}

func requiresRootImportForK3S(inContainerd, inDocker, isRoot bool) bool {
	return inDocker && !inContainerd && !isRoot
}

func shouldFallbackToDockerRuntime(runtimeName string, hasPartition, inContainerd, inDocker, isRoot bool, dockerAvailable bool) bool {
	return runtimeName == "k3s" &&
		dockerAvailable &&
		!hasPartition &&
		requiresRootImportForK3S(inContainerd, inDocker, isRoot)
}

func k3sDockerImportHint(image string) string {
	return fmt.Sprintf("engine image %s exists in Docker but not in K3S containerd; import requires root (sudo docker save %s | sudo k3s ctr -n k8s.io images import -)", image, image)
}

func k3sDockerFallbackWarning(image string) string {
	return fmt.Sprintf("engine image %s is available in Docker but not K3S containerd; using Docker runtime because importing into containerd requires root", image)
}

func installedRuntimeTypesForEngine(installed []*state.Engine, engineName, engineType string) []string {
	keys := map[string]bool{
		strings.ToLower(engineName): true,
		strings.ToLower(engineType): true,
	}
	set := make(map[string]bool)
	for _, e := range installed {
		if e == nil {
			continue
		}
		if keys[strings.ToLower(e.ID)] || keys[strings.ToLower(e.Type)] {
			if e.RuntimeType != "" {
				set[e.RuntimeType] = true
			}
		}
	}
	runtimeTypes := make([]string, 0, len(set))
	for rt := range set {
		runtimeTypes = append(runtimeTypes, rt)
	}
	sort.Strings(runtimeTypes)
	return runtimeTypes
}

func defaultEngineAsset(cat *knowledge.Catalog, hw knowledge.HardwareInfo) *knowledge.EngineAsset {
	if cat == nil {
		return nil
	}
	if name := cat.DefaultEngine(); name != "" {
		if ea := cat.FindEngineByName(name, hw); engineCompatibleWithHost(ea, hw) {
			return ea
		}
	}
	for i := range cat.EngineAssets {
		ea := &cat.EngineAssets[i]
		if ea.Metadata.Default && engineCompatibleWithHost(ea, hw) {
			return ea
		}
	}
	for i := range cat.EngineAssets {
		ea := &cat.EngineAssets[i]
		if engineCompatibleWithHost(ea, hw) {
			return ea
		}
	}
	return nil
}

func preferredContainerImagesByTypeTag(cat *knowledge.Catalog, hw knowledge.HardwareInfo) map[string]map[string]bool {
	preferred := make(map[string]map[string]bool)
	if cat == nil {
		return preferred
	}
	for i := range cat.EngineAssets {
		ea := &cat.EngineAssets[i]
		if ea.Image.Name == "" || !engineCompatibleWithHost(ea, hw) {
			continue
		}
		key := scannedEngineDisplayKey("container", ea.Metadata.Type, ea.Image.Tag)
		if preferred[key] == nil {
			preferred[key] = make(map[string]bool)
		}
		preferred[key][strings.ToLower(ea.Image.Name)] = true
	}
	return preferred
}

func dedupeScannedEngines(images []*engine.EngineImage, preferred map[string]map[string]bool) []*engine.EngineImage {
	if len(images) < 2 {
		return images
	}

	out := make([]*engine.EngineImage, 0, len(images))
	seen := make(map[string]int)
	for _, img := range images {
		if img == nil {
			continue
		}
		key := scannedEngineDisplayKey(img.RuntimeType, img.Type, img.Tag)
		if key == "" {
			out = append(out, img)
			continue
		}
		if idx, ok := seen[key]; ok {
			out[idx] = preferScannedEngine(out[idx], img, preferred[key])
			continue
		}
		seen[key] = len(out)
		out = append(out, img)
	}
	return out
}

func scannedEngineDisplayKey(runtimeType, engineType, tag string) string {
	runtimeType = strings.TrimSpace(runtimeType)
	engineType = strings.TrimSpace(engineType)
	tag = strings.TrimSpace(tag)
	if runtimeType != "container" || engineType == "" || tag == "" {
		return ""
	}
	return runtimeType + "|" + strings.ToLower(engineType) + "|" + strings.ToLower(tag)
}

func preferScannedEngine(existing, candidate *engine.EngineImage, preferredImages map[string]bool) *engine.EngineImage {
	if existing == nil {
		return candidate
	}
	if candidate == nil {
		return existing
	}

	existingPreferred := preferredImages[strings.ToLower(existing.Image)]
	candidatePreferred := preferredImages[strings.ToLower(candidate.Image)]
	switch {
	case candidatePreferred && !existingPreferred:
		return candidate
	case existingPreferred && !candidatePreferred:
		return existing
	case existing.DockerOnly && !candidate.DockerOnly:
		return candidate
	case candidate.DockerOnly && !existing.DockerOnly:
		return existing
	default:
		return existing
	}
}
