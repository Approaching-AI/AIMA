package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
)

type containerCompatibilityPlan struct {
	RepairInitCommands []string
	DockerImageChanged bool
}

type dockerOnlyRunner struct {
	base engine.CommandRunner
}

func (r *dockerOnlyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "crictl" || (name == "k3s" && len(args) > 0 && args[0] == "crictl") {
		return nil, fmt.Errorf("crictl disabled for docker-only operation")
	}
	return r.base.Run(ctx, name, args...)
}

func (r *dockerOnlyRunner) Pipe(ctx context.Context, from, to []string) error {
	return r.base.Pipe(ctx, from, to)
}

func (r *dockerOnlyRunner) RunStream(ctx context.Context, onLine func(line string), name string, args ...string) error {
	if name == "crictl" || (name == "k3s" && len(args) > 0 && args[0] == "crictl") {
		return fmt.Errorf("crictl disabled for docker-only operation")
	}
	return r.base.RunStream(ctx, onLine, name, args...)
}

func shouldRunContainerCompatibilityProbe(resolved *knowledge.ResolvedConfig, runtimeName, modelPath string) bool {
	if resolved == nil || resolved.EngineImage == "" || resolved.CompatibilityProbe == "" || modelPath == "" {
		return false
	}
	if runtimeName != "docker" && runtimeName != "k3s" {
		return false
	}
	if resolved.Source != nil {
		return false
	}
	if resolved.ModelFormat != "" && !strings.EqualFold(resolved.ModelFormat, "safetensors") {
		return false
	}
	if fi, err := os.Stat(modelPath); err == nil && !fi.IsDir() {
		return false
	}
	return true
}

func prepareContainerCompatibility(
	ctx context.Context,
	runner engine.CommandRunner,
	allowAutoPull bool,
	runtimeName string,
	modelPath string,
	resolved *knowledge.ResolvedConfig,
) (containerCompatibilityPlan, error) {
	var plan containerCompatibilityPlan
	if !shouldRunContainerCompatibilityProbe(resolved, runtimeName, modelPath) {
		return plan, nil
	}
	if !dockerAvailableForCompatibilityProbe(ctx, runner) {
		slog.Warn("container compatibility probe skipped: Docker unavailable",
			"model", resolved.ModelName,
			"engine_image", resolved.EngineImage)
		return plan, nil
	}

	dockerReady, dockerChanged, err := ensureDockerImageForCompatibilityProbe(ctx, runner, allowAutoPull, resolved)
	if err != nil {
		slog.Warn("container compatibility probe setup failed",
			"model", resolved.ModelName,
			"engine_image", resolved.EngineImage,
			"error", err)
		return plan, nil
	}
	if !dockerReady {
		slog.Warn("container compatibility probe skipped: image unavailable in Docker",
			"model", resolved.ModelName,
			"engine_image", resolved.EngineImage)
		return plan, nil
	}
	plan.DockerImageChanged = dockerChanged

	trustRemoteCode := configBoolValue(resolved.Config, "trust_remote_code")
	probeOutput, probeErr := timedContainerCompatibilityProbe(ctx, runner, resolved.CompatibilityProbe, resolved.EngineImage, modelPath, trustRemoteCode, nil)
	if probeErr == nil {
		return plan, nil
	}

	probeSummary := summarizeCompatibilityProbeOutput(probeOutput, probeErr)
	if allowAutoPull && len(resolved.EngineRegistries) > 0 && !strings.EqualFold(resolved.EngineDistribution, "local") {
		if refreshErr := refreshDockerImageForCompatibilityProbe(ctx, runner, resolved); refreshErr == nil {
			plan.DockerImageChanged = true
			refreshedOutput, refreshedErr := timedContainerCompatibilityProbe(ctx, runner, resolved.CompatibilityProbe, resolved.EngineImage, modelPath, trustRemoteCode, nil)
			if refreshedErr == nil {
				slog.Info("container compatibility resolved after image refresh",
					"model", resolved.ModelName,
					"engine_image", resolved.EngineImage)
				return plan, nil
			}
			if refreshedSummary := summarizeCompatibilityProbeOutput(refreshedOutput, refreshedErr); moreSpecificFailure(refreshedSummary, probeSummary) || probeSummary == "" {
				probeSummary = refreshedSummary
			}
		} else {
			slog.Warn("container compatibility image refresh failed",
				"model", resolved.ModelName,
				"engine_image", resolved.EngineImage,
				"error", refreshErr)
		}
	}

	if len(resolved.RepairInitCommands) == 0 {
		return plan, fmt.Errorf("container compatibility check failed for %s with %s: %s", resolved.ModelName, resolved.EngineImage, probeSummary)
	}

	repairedOutput, repairedErr := timedContainerCompatibilityProbe(ctx, runner, resolved.CompatibilityProbe, resolved.EngineImage, modelPath, trustRemoteCode, resolved.RepairInitCommands)
	if repairedErr != nil {
		return plan, fmt.Errorf("container compatibility check failed for %s with %s: %s; auto-repair failed: %s",
			resolved.ModelName,
			resolved.EngineImage,
			probeSummary,
			summarizeCompatibilityProbeOutput(repairedOutput, repairedErr))
	}

	slog.Info("container compatibility auto-repair selected",
		"model", resolved.ModelName,
		"engine_image", resolved.EngineImage,
		"commands", len(resolved.RepairInitCommands))
	plan.RepairInitCommands = append([]string(nil), resolved.RepairInitCommands...)
	return plan, nil
}

func dockerAvailableForCompatibilityProbe(ctx context.Context, runner engine.CommandRunner) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := runner.Run(probeCtx, "docker", "version", "--format", "{{.Server.Version}}")
	return err == nil
}

func ensureDockerImageForCompatibilityProbe(ctx context.Context, runner engine.CommandRunner, allowAutoPull bool, resolved *knowledge.ResolvedConfig) (available bool, changed bool, err error) {
	if resolved == nil || resolved.EngineImage == "" {
		return false, false, nil
	}
	if engine.ImageExistsInDocker(ctx, resolved.EngineImage, runner) {
		return true, false, nil
	}
	if !allowAutoPull || len(resolved.EngineRegistries) == 0 || strings.EqualFold(resolved.EngineDistribution, "local") {
		return false, false, nil
	}
	if err := pullDockerImageForCompatibilityProbe(ctx, runner, resolved); err != nil {
		return false, false, err
	}
	return true, true, nil
}

func refreshDockerImageForCompatibilityProbe(ctx context.Context, runner engine.CommandRunner, resolved *knowledge.ResolvedConfig) error {
	if resolved == nil || resolved.EngineImage == "" {
		return nil
	}
	return pullDockerImageForCompatibilityProbe(ctx, runner, resolved)
}

func pullDockerImageForCompatibilityProbe(ctx context.Context, runner engine.CommandRunner, resolved *knowledge.ResolvedConfig) error {
	if resolved == nil {
		return fmt.Errorf("resolved config is nil")
	}
	if len(resolved.EngineRegistries) == 0 {
		return fmt.Errorf("no registries configured for %s", resolved.EngineImage)
	}
	imgName, imgTag := splitImageRef(resolved.EngineImage)
	if err := engine.Pull(ctx, engine.PullOptions{
		Image:          imgName,
		Tag:            imgTag,
		Registries:     resolved.EngineRegistries,
		Runner:         &dockerOnlyRunner{base: runner},
		ExpectedDigest: resolved.EngineDigest,
	}); err != nil {
		return err
	}
	return ensureDockerImageAlias(ctx, runner, resolved.EngineImage, resolved.EngineRegistries)
}

func ensureDockerImageAlias(ctx context.Context, runner engine.CommandRunner, image string, registries []string) error {
	if image == "" || engine.ImageExistsInDocker(ctx, image, runner) {
		return nil
	}
	name, tag := splitImageRef(image)
	for _, registry := range registries {
		pulledRef := buildRegistryImageRef(registry, name, tag)
		if !engine.ImageExistsInDocker(ctx, pulledRef, runner) {
			continue
		}
		if _, err := runner.Run(ctx, "docker", "tag", pulledRef, image); err != nil {
			return fmt.Errorf("docker tag %s %s: %w", pulledRef, image, err)
		}
		if engine.ImageExistsInDocker(ctx, image, runner) {
			return nil
		}
	}
	if engine.ImageExistsInDocker(ctx, image, runner) {
		return nil
	}
	return fmt.Errorf("image %s is not available under the expected local tag", image)
}

func buildRegistryImageRef(registry, image, tag string) string {
	registry = strings.TrimSuffix(registry, "/")
	image = strings.TrimSuffix(image, "/")
	baseName := filepath.Base(image)
	if registry == image || strings.HasSuffix(registry, "/"+baseName) {
		return fmt.Sprintf("%s:%s", registry, tag)
	}
	if strings.Contains(registry, "/") {
		return fmt.Sprintf("%s/%s:%s", registry, baseName, tag)
	}
	return fmt.Sprintf("%s/%s:%s", registry, image, tag)
}

func timedContainerCompatibilityProbe(
	ctx context.Context,
	runner engine.CommandRunner,
	probeType string,
	image string,
	modelPath string,
	trustRemoteCode bool,
	repairInitCommands []string,
) (string, error) {
	timeout := 2 * time.Minute
	if len(repairInitCommands) > 0 {
		timeout = 10 * time.Minute
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runContainerCompatibilityProbe(probeCtx, runner, probeType, image, modelPath, trustRemoteCode, repairInitCommands)
}

func runContainerCompatibilityProbe(
	ctx context.Context,
	runner engine.CommandRunner,
	probeType string,
	image string,
	modelPath string,
	trustRemoteCode bool,
	repairInitCommands []string,
) (string, error) {
	pythonCode, ok := compatibilityProbePython(probeType)
	if !ok {
		return "", fmt.Errorf("unknown compatibility probe %q", probeType)
	}

	args := []string{
		"run", "--rm",
		"--env", "AIMA_MODEL_PATH=/models",
		"--env", "AIMA_TRUST_REMOTE_CODE=" + strconv.FormatBool(trustRemoteCode),
		"--volume", modelPath + ":/models:ro",
	}
	if len(repairInitCommands) == 0 {
		args = append(args, "--entrypoint", "python3", image, "-c", pythonCode)
	} else {
		shellScript := strings.Join(append(append([]string(nil), repairInitCommands...), "python3 -c "+strconv.Quote(pythonCode)), " && ")
		args = append(args, "--entrypoint", "sh", image, "-c", shellScript)
	}

	out, err := runner.Run(ctx, "docker", args...)
	return string(out), err
}

func compatibilityProbePython(probeType string) (string, bool) {
	switch strings.TrimSpace(probeType) {
	case "transformers_autoconfig":
		return `import os, transformers; from pathlib import Path; from transformers import AutoConfig, AutoProcessor; model_path = os.environ.get("AIMA_MODEL_PATH", "/models"); trust_remote_code = os.environ.get("AIMA_TRUST_REMOTE_CODE", "false").lower() == "true"; cfg = AutoConfig.from_pretrained(model_path, local_files_only=True, trust_remote_code=trust_remote_code); model_dir = Path(model_path); processor_name = type(AutoProcessor.from_pretrained(model_path, local_files_only=True, trust_remote_code=trust_remote_code)).__name__ if any((model_dir / name).exists() for name in ("processor_config.json", "preprocessor_config.json")) else "none"; print("AIMA_COMPAT_OK transformers=%s model_type=%s processor=%s" % (transformers.__version__, getattr(cfg, "model_type", "unknown"), processor_name))`, true
	case "qwen_asr_autoconfig":
		return `import os, qwen_asr, transformers; from transformers import AutoConfig; model_path = os.environ.get("AIMA_MODEL_PATH", "/models"); trust_remote_code = os.environ.get("AIMA_TRUST_REMOTE_CODE", "false").lower() == "true"; cfg = AutoConfig.from_pretrained(model_path, local_files_only=True, trust_remote_code=trust_remote_code); print("AIMA_COMPAT_OK transformers=%s model_type=%s qwen_asr=%s" % (transformers.__version__, getattr(cfg, "model_type", "unknown"), getattr(qwen_asr, "__version__", "unknown")))`, true
	default:
		return "", false
	}
}

func configBoolValue(config map[string]any, key string) bool {
	if config == nil {
		return false
	}
	raw, ok := config[key]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && parsed
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case json.Number:
		n, err := v.Int64()
		return err == nil && n != 0
	default:
		return false
	}
}

func summarizeCompatibilityProbeOutput(output string, err error) string {
	if detail := summarizeErrorLines(output); detail != "" && detail != "unknown startup failure" {
		return detail
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	if err != nil {
		return err.Error()
	}
	return "compatibility probe failed"
}

func stringInSliceFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}
