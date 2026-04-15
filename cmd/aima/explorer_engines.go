package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/runtime"

	state "github.com/jguan/aima/internal"
)

func gatherExplorerLocalEngines(
	ctx context.Context,
	cat *knowledge.Catalog,
	db *state.DB,
	defaultRt, nativeRt, dockerRt, k3sRt runtime.Runtime,
	dataDir string,
) ([]agent.LocalEngine, error) {
	if cat == nil || db == nil || defaultRt == nil {
		return nil, nil
	}

	installed, err := db.ListEngines(ctx)
	if err != nil {
		return nil, err
	}

	hwInfo := buildHardwareInfo(ctx, cat, defaultRt.Name())
	candidateTypes := make(map[string]bool)
	installedByType := make(map[string][]*state.Engine)
	for _, inst := range installed {
		if inst == nil || !inst.Available {
			continue
		}
		engineType := strings.TrimSpace(inst.Type)
		if engineType == "" {
			continue
		}
		candidateTypes[engineType] = true
		installedByType[engineType] = append(installedByType[engineType], inst)
	}

	result := make([]agent.LocalEngine, 0, len(candidateTypes))
	for engineType := range candidateTypes {
		asset := cat.FindEngineByName(engineType, hwInfo)
		if asset == nil {
			continue
		}
		if !engineSupportsPlatform(asset, hwInfo.Platform) {
			continue
		}
		requiredRuntime := preferredEngineRuntimeType(asset, hwInfo.Platform)
		installedEngine := selectExplorerInstalledEngine(installedByType[engineType], requiredRuntime)
		imageRef := explorerInstalledImageRef(installedEngine)
		if imageRef == "" {
			imageRef = engineImageRef(asset)
		}
		imageInDocker := imageRef != "" && engine.ImageExistsInDocker(ctx, imageRef, &execRunner{})
		imageInContainerd := imageRef != "" && engine.ImageExistsInContainerd(ctx, imageRef, &execRunner{})
		nativeInstalled := explorerNativeEngineAvailable(asset, installedEngine, dataDir)
		if !explorerEngineAssetDeployable(
			requiredRuntime,
			engineRuntimeRecommendation(asset, hwInfo.Platform),
			defaultRt,
			nativeRt,
			dockerRt,
			k3sRt,
			nativeInstalled,
			imageInDocker,
			imageInContainerd,
			os.Getuid() == 0,
		) {
			continue
		}

		artifact := explorerInstalledArtifact(installedEngine)
		if artifact == "" {
			artifact = imageRef
		}
		result = append(result, agent.LocalEngine{
			Name:                firstNonEmpty(asset.Metadata.Name, engineType),
			Type:                firstNonEmpty(asset.Metadata.Type, engineType),
			Runtime:             requiredRuntime,
			Artifact:            artifact,
			Features:            asset.Amplifier.Features,
			Notes:               asset.Amplifier.PerformanceGain,
			TunableParams:       asset.Startup.DefaultArgs,
			InternalArgs:        asset.Startup.InternalArgs,
			SupportedModelTypes: asset.Metadata.SupportedModelTypes,
			HealthCheckPath:     asset.Startup.HealthCheck.Path,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Type == result[j].Type {
			return result[i].Name < result[j].Name
		}
		return result[i].Type < result[j].Type
	})
	return result, nil
}

func gatherExplorerComboFacts(
	ctx context.Context,
	cat *knowledge.Catalog,
	db *state.DB,
	kStore *knowledge.Store,
	defaultRt, nativeRt, dockerRt, k3sRt runtime.Runtime,
	dataDir string,
	hardware agent.HardwareInfo,
	models []agent.LocalModel,
	engines []agent.LocalEngine,
) ([]agent.ComboFact, error) {
	if cat == nil || db == nil || defaultRt == nil {
		return nil, nil
	}

	hwInfo := buildHardwareInfo(ctx, cat, defaultRt.Name())
	if hardware.Profile != "" {
		hwInfo.HardwareProfile = hardware.Profile
	}

	// D4: Parallelize combo fact resolution — the serial 16×4 resolve loop was 34s.
	type indexedFact struct {
		idx  int
		fact agent.ComboFact
	}

	// Build work items with stable indices for deterministic output order.
	type workItem struct {
		idx    int
		model  agent.LocalModel
		engine agent.LocalEngine
	}
	var items []workItem
	for _, model := range models {
		if strings.TrimSpace(model.Name) == "" {
			continue
		}
		for _, localEngine := range engines {
			if strings.TrimSpace(localEngine.Type) == "" {
				continue
			}
			items = append(items, workItem{idx: len(items), model: model, engine: localEngine})
		}
	}

	results := make([]indexedFact, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // limit concurrency to avoid overwhelming subprocess/db

	for i, item := range items {
		wg.Add(1)
		go func(i int, item workItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			fact := agent.ComboFact{
				Model:    item.model.Name,
				Engine:   item.engine.Type,
				Runtime:  item.engine.Runtime,
				Artifact: item.engine.Artifact,
				Status:   "blocked",
			}

			// Check on-disk model format against engine's supported formats.
			// The resolver may find a catalog variant with a compatible format
			// (e.g. gguf variant for llamacpp), but the actual files on disk
			// may differ (e.g. safetensors). Block early to avoid wasted work.
			if reason := explorerFormatBlockReason(item.model.Format, item.engine.Type, cat, hwInfo); reason != "" {
				fact.Reason = reason
				results[i] = indexedFact{idx: i, fact: fact}
				return
			}

			// Modality check: engine must support the model's type (llm, embedding, tts, etc.)
			if reason := explorerModalityBlockReason(item.engine.SupportedModelTypes, item.model.Type, item.engine.Type); reason != "" {
				fact.Reason = reason
				results[i] = indexedFact{idx: i, fact: fact}
				return
			}

			rd, err := resolveDeployment(ctx, cat, db, kStore, hwInfo, item.model.Name, item.engine.Type, "", nil, dataDir)
			if err != nil {
				fact.Reason = err.Error()
				results[i] = indexedFact{idx: i, fact: fact}
				return
			}
			if rd.Fit != nil && !rd.Fit.Fit {
				fact.Reason = rd.Fit.Reason
				results[i] = indexedFact{idx: i, fact: fact}
				return
			}

			selectedRt, err := pickRuntimeForDeployment(rd.Resolved.RuntimeRecommendation, k3sRt, dockerRt, nativeRt, defaultRt, false)
			if err != nil || selectedRt == nil {
				if err != nil {
					fact.Reason = err.Error()
				} else {
					fact.Reason = "no runtime available"
				}
				results[i] = indexedFact{idx: i, fact: fact}
				return
			}

			fact.Runtime = selectedRt.Name()
			if resolvedArtifact := explorerResolvedArtifactRef(rd.Resolved); resolvedArtifact != "" {
				fact.Artifact = resolvedArtifact
			}
			if reason := explorerResolvedArtifactBlockReason(ctx, rd.Resolved, selectedRt.Name(), dockerRt != nil, dataDir); reason != "" {
				fact.Reason = reason
				results[i] = indexedFact{idx: i, fact: fact}
				return
			}
			if _, err := resolveLocalModelPathNoPull(item.model.Name, rd.Resolved, dataDir); err != nil {
				fact.Reason = err.Error()
				results[i] = indexedFact{idx: i, fact: fact}
				return
			}

			fact.Status = "ready"
			fact.Reason = "resolver and local no-pull runtime checks passed"
			results[i] = indexedFact{idx: i, fact: fact}
		}(i, item)
	}
	wg.Wait()

	facts := make([]agent.ComboFact, len(results))
	for i, r := range results {
		facts[i] = r.fact
	}
	return facts, nil
}

func explorerEngineAssetDeployable(
	requiredRuntime string,
	recommendation string,
	defaultRt, nativeRt, dockerRt, k3sRt runtime.Runtime,
	nativeInstalled bool,
	imageInDocker bool,
	imageInContainerd bool,
	isRoot bool,
) bool {
	switch requiredRuntime {
	case "native":
		return nativeInstalled
	case "container":
		return explorerContainerRuntimeAvailable(recommendation, defaultRt, nativeRt, dockerRt, k3sRt, imageInDocker, imageInContainerd, isRoot)
	default:
		return false
	}
}

func explorerContainerRuntimeAvailable(
	recommendation string,
	defaultRt, nativeRt, dockerRt, k3sRt runtime.Runtime,
	imageInDocker bool,
	imageInContainerd bool,
	isRoot bool,
) bool {
	selectedRt, err := pickRuntimeForDeployment(recommendation, k3sRt, dockerRt, nativeRt, defaultRt, false)
	if err != nil || selectedRt == nil {
		return false
	}

	switch selectedRt.Name() {
	case "k3s":
		if imageInContainerd {
			return true
		}
		if shouldFallbackToDockerRuntime("k3s", false, imageInContainerd, imageInDocker, isRoot, dockerRt != nil) {
			return true
		}
		return imageInDocker && !requiresRootImportForK3S(imageInContainerd, imageInDocker, isRoot)
	case "docker":
		return imageInDocker
	default:
		return false
	}
}

func engineRuntimeRecommendation(asset *knowledge.EngineAsset, platform string) string {
	if asset == nil {
		return ""
	}
	if rec, ok := asset.Runtime.PlatformRecommendations[platform]; ok && rec != "" {
		return rec
	}
	return asset.Runtime.Default
}

func engineImageRef(asset *knowledge.EngineAsset) string {
	if asset == nil || asset.Image.Name == "" {
		return ""
	}
	if asset.Image.Tag == "" {
		return asset.Image.Name
	}
	return asset.Image.Name + ":" + asset.Image.Tag
}

func selectExplorerInstalledEngine(entries []*state.Engine, requiredRuntime string) *state.Engine {
	for _, entry := range entries {
		if entry == nil || !entry.Available {
			continue
		}
		if requiredRuntime == "" || entry.RuntimeType == requiredRuntime {
			return entry
		}
	}
	for _, entry := range entries {
		if entry != nil && entry.Available {
			return entry
		}
	}
	return nil
}

func explorerInstalledArtifact(entry *state.Engine) string {
	if entry == nil {
		return ""
	}
	if entry.RuntimeType == "native" {
		return entry.BinaryPath
	}
	return explorerInstalledImageRef(entry)
}

func explorerInstalledImageRef(entry *state.Engine) string {
	if entry == nil || entry.Image == "" {
		return ""
	}
	if entry.Tag == "" {
		return entry.Image
	}
	return entry.Image + ":" + entry.Tag
}

func explorerNativeEngineAvailable(asset *knowledge.EngineAsset, installed *state.Engine, dataDir string) bool {
	if asset != nil && asset.Source != nil && explorerNativeSourceAvailable(asset.Source, dataDir) {
		return true
	}
	if installed == nil || installed.BinaryPath == "" {
		return false
	}
	distDir := filepath.Join(dataDir, "dist", goruntime.GOOS+"-"+goruntime.GOARCH)
	if !strings.HasPrefix(installed.BinaryPath, distDir+string(filepath.Separator)) {
		return false
	}
	_, err := os.Stat(installed.BinaryPath)
	return err == nil
}

func explorerNativeSourceAvailable(source *knowledge.EngineSource, dataDir string) bool {
	if source == nil {
		return false
	}
	distDir := filepath.Join(dataDir, "dist", goruntime.GOOS+"-"+goruntime.GOARCH)
	for _, candidate := range explorerBinaryCandidates(source.Binary) {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(distDir, candidate)); err == nil {
			return true
		}
	}
	if source.Probe != nil {
		for _, path := range source.Probe.Paths {
			if _, err := os.Stat(path); err == nil {
				return true
			}
		}
	}
	if source.Binary != "" {
		if _, err := exec.LookPath(source.Binary); err == nil {
			return true
		}
	}
	return false
}

func explorerBinaryCandidates(binary string) []string {
	if binary == "" {
		return nil
	}
	if goruntime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(binary), ".exe") {
		return []string{binary, binary + ".exe"}
	}
	return []string{binary}
}

func explorerResolvedArtifactRef(resolved *knowledge.ResolvedConfig) string {
	if resolved == nil {
		return ""
	}
	if resolved.EngineImage != "" {
		return resolved.EngineImage
	}
	if resolved.Source != nil && resolved.Source.Binary != "" {
		return resolved.Source.Binary
	}
	if len(resolved.Command) > 0 {
		return resolved.Command[0]
	}
	return ""
}

func explorerResolvedArtifactBlockReason(ctx context.Context, resolved *knowledge.ResolvedConfig, runtimeName string, dockerAvailable bool, dataDir string) string {
	if resolved == nil {
		return "resolved config missing"
	}
	switch runtimeName {
	case "docker":
		ref := normalizedImageRef(resolved.EngineImage)
		if ref == "" {
			return "resolved config has no docker image"
		}
		if !engine.ImageExistsInDocker(ctx, ref, &execRunner{}) {
			return fmt.Sprintf("resolved image %s not present locally and exploration deploy uses no-pull", ref)
		}
		return ""
	case "k3s":
		ref := normalizedImageRef(resolved.EngineImage)
		if ref == "" {
			return "resolved config has no k3s image"
		}
		inContainerd := engine.ImageExistsInContainerd(ctx, ref, &execRunner{})
		if inContainerd {
			return ""
		}
		inDocker := engine.ImageExistsInDocker(ctx, ref, &execRunner{})
		if shouldFallbackToDockerRuntime("k3s", false, inContainerd, inDocker, os.Getuid() == 0, dockerAvailable) {
			return ""
		}
		if inDocker && requiresRootImportForK3S(inContainerd, inDocker, os.Getuid() == 0) {
			return k3sDockerImportHint(ref)
		}
		return fmt.Sprintf("resolved image %s not present in containerd and no no-pull fallback exists", ref)
	case "native":
		if resolved.Source != nil && explorerNativeSourceAvailable(resolved.Source, dataDir) {
			return ""
		}
		if len(resolved.Command) > 0 {
			if _, err := exec.LookPath(resolved.Command[0]); err == nil {
				return ""
			}
		}
		return fmt.Sprintf("native binary %s not available locally and exploration deploy uses no-pull", explorerResolvedArtifactRef(resolved))
	default:
		return fmt.Sprintf("unsupported runtime %s", runtimeName)
	}
}

func normalizedImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.Contains(ref, ":") {
		return ref
	}
	return ref + ":latest"
}

// explorerFormatBlockReason checks if a model's on-disk format is compatible
// with the engine's supported formats. Returns a non-empty reason string if
// the combo should be blocked, or "" if compatible (or check cannot be performed).
func explorerFormatBlockReason(modelFormat, engineType string, cat *knowledge.Catalog, hwInfo knowledge.HardwareInfo) string {
	if modelFormat == "" {
		return ""
	}
	ea := cat.FindEngineByName(engineType, hwInfo)
	if ea == nil || len(ea.Metadata.SupportedFormats) == 0 {
		return ""
	}
	for _, f := range ea.Metadata.SupportedFormats {
		if strings.EqualFold(f, modelFormat) {
			return ""
		}
	}
	return fmt.Sprintf("on-disk model format %q incompatible with engine %s (supported: %v)",
		modelFormat, engineType, ea.Metadata.SupportedFormats)
}

// explorerModalityBlockReason checks if an engine supports the model's modality type.
// Returns a non-empty reason string if the combo should be blocked, or "" if compatible.
// Empty supportedTypes means no constraint. Empty modelType is treated as unknown
// and blocked so unresolved scans do not pollute the ready frontier.
func explorerModalityBlockReason(supportedTypes []string, modelType, engineType string) string {
	if len(supportedTypes) == 0 {
		return ""
	}
	if strings.TrimSpace(modelType) == "" {
		return fmt.Sprintf("model type unknown: engine %s requires one of %v", engineType, supportedTypes)
	}
	for _, t := range supportedTypes {
		if strings.EqualFold(t, modelType) {
			return ""
		}
	}
	return fmt.Sprintf("modality mismatch: engine %s does not support model type %q (supported: %v)",
		engineType, modelType, supportedTypes)
}
