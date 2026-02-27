package engine

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// PullOptions configures an image pull operation.
type PullOptions struct {
	Image      string
	Tag        string
	Registries []string
	Runner     CommandRunner
}

// ImageExists reports whether a container image is already present in the local runtime.
// Tries crictl first, then docker. Returns false on any error.
func ImageExists(ctx context.Context, image, tag string, runner CommandRunner) bool {
	ref := image + ":" + tag
	if out, err := runner.Run(ctx, "crictl", "images", "--quiet", ref); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return true
	}
	if out, err := runner.Run(ctx, "docker", "images", "-q", ref); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return true
	}
	return false
}

// Pull downloads a container image, trying registries in order.
// Falls back from crictl to docker if crictl fails.
func Pull(ctx context.Context, opts PullOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("pull image %s:%s: %w", opts.Image, opts.Tag, err)
	}

	if len(opts.Registries) == 0 {
		return fmt.Errorf("pull image %s:%s: no registries configured", opts.Image, opts.Tag)
	}

	var lastErr error
	for _, registry := range opts.Registries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("pull image %s:%s: %w", opts.Image, opts.Tag, err)
		}

		ref := buildImageRef(registry, opts.Image, opts.Tag)

		// Try crictl first
		if _, err := opts.Runner.Run(ctx, "crictl", "pull", ref); err == nil {
			return nil
		}

		// Fallback to docker
		if _, err := opts.Runner.Run(ctx, "docker", "pull", ref); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return fmt.Errorf("pull image %s:%s: all registries failed: %w", opts.Image, opts.Tag, lastErr)
}

// buildImageRef constructs a full image reference from registry, image name, and tag.
// For registries that include a namespace (e.g., "registry.cn-hangzhou.aliyuncs.com/aima"),
// only the base image name is appended (not the full original path).
func buildImageRef(registry, image, tag string) string {
	// Extract just the base name from the image (e.g., "vllm-openai" from "vllm/vllm-openai")
	baseName := path.Base(image)

	// If registry already contains a path component (like "registry.cn/namespace"),
	// append just the base name. Otherwise, use the full image path.
	if strings.Contains(registry, "/") {
		return fmt.Sprintf("%s/%s:%s", registry, baseName, tag)
	}
	return fmt.Sprintf("%s/%s:%s", registry, image, tag)
}
