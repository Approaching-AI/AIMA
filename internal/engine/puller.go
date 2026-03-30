package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
)

// PullOptions configures an image pull operation.
type PullOptions struct {
	Image      string
	Tag        string
	Registries []string
	SizeHintMB int                  // from engine YAML size_approx_mb, for progress estimation
	OnProgress func(ProgressEvent)  // called with pull progress (may be nil)
	Runner     CommandRunner
}

// ImageExists reports whether a container image is already present in the local runtime.
// Tries crictl (with K3S fallback) first, then docker. Returns false on any error.
func ImageExists(ctx context.Context, image, tag string, runner CommandRunner) bool {
	ref := image + ":" + tag
	if out, err := runCrictl(ctx, runner, "images", "--quiet", ref); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return true
	}
	if out, err := runner.Run(ctx, "docker", "images", "-q", ref); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return true
	}
	return false
}

// Pull downloads a container image, trying registries in order.
// Falls back from crictl to docker if crictl fails.
// When opts.OnProgress is set and docker is used, parses docker's JSON progress output.
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

		if opts.OnProgress != nil {
			opts.OnProgress(ProgressEvent{
				Phase:   "pulling",
				Message: fmt.Sprintf("pulling %s", ref),
			})
		}

		// Try crictl first (with K3S fallback) — no streaming progress available
		if _, err := runCrictl(ctx, opts.Runner, "pull", ref); err == nil {
			if opts.OnProgress != nil {
				opts.OnProgress(ProgressEvent{Phase: "complete", Message: "image pulled via crictl"})
			}
			return nil
		}

		// Fallback to docker with progress parsing
		if opts.OnProgress != nil {
			agg := newDockerPullAggregator(opts.OnProgress, int64(opts.SizeHintMB)*1024*1024)
			err := opts.Runner.RunStream(ctx, agg.onLine, "docker", "pull", ref)
			if err == nil {
				opts.OnProgress(ProgressEvent{Phase: "complete", Message: "image pulled via docker"})
				return nil
			}
			lastErr = err
		} else {
			if _, err := opts.Runner.Run(ctx, "docker", "pull", ref); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
	}

	return fmt.Errorf("pull image %s:%s: all registries failed: %w", opts.Image, opts.Tag, lastErr)
}

// ImageExistsInContainerd checks whether image exists in containerd (K3S) store only.
// Unlike ImageExists, this does NOT fall back to Docker.
func ImageExistsInContainerd(ctx context.Context, image string, runner CommandRunner) bool {
	ref := image
	if !strings.Contains(ref, ":") {
		ref += ":latest"
	}
	out, err := runCrictl(ctx, runner, "images", "--quiet", ref)
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// ImageExistsInDocker checks whether image exists in Docker store.
func ImageExistsInDocker(ctx context.Context, image string, runner CommandRunner) bool {
	ref := image
	if !strings.Contains(ref, ":") {
		ref += ":latest"
	}
	out, err := runner.Run(ctx, "docker", "images", "-q", ref)
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// ImportDockerToContainerd transfers an image from Docker store to K3S containerd.
// Uses runner.Pipe to stream docker save stdout into k3s ctr import stdin.
// Requires root privileges (containerd socket is root-owned).
func ImportDockerToContainerd(ctx context.Context, image string, runner CommandRunner) error {
	err := runner.Pipe(ctx,
		[]string{"docker", "save", image},
		[]string{"k3s", "ctr", "-n", "k8s.io", "images", "import", "-"},
	)
	if err != nil {
		return fmt.Errorf("import %s: %w", image, err)
	}
	return nil
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

// dockerPullProgress is the JSON structure docker outputs per layer during pull.
type dockerPullProgress struct {
	Status         string `json:"status"`
	ID             string `json:"id"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
}

// dockerPullAggregator aggregates per-layer docker pull progress into a single ProgressEvent.
type dockerPullAggregator struct {
	mu         sync.Mutex
	layers     map[string]*layerProgress
	sizeHint   int64 // from YAML, used when docker doesn't report total
	onProgress func(ProgressEvent)
}

type layerProgress struct {
	current int64
	total   int64
}

func newDockerPullAggregator(onProgress func(ProgressEvent), sizeHint int64) *dockerPullAggregator {
	return &dockerPullAggregator{
		layers:     make(map[string]*layerProgress),
		sizeHint:   sizeHint,
		onProgress: onProgress,
	}
}

func (a *dockerPullAggregator) onLine(line string) {
	var p dockerPullProgress
	if err := json.Unmarshal([]byte(line), &p); err != nil {
		// Not JSON — might be plain text progress from older docker versions
		slog.Debug("docker pull non-JSON output", "line", line)
		return
	}

	if p.ID == "" {
		// Status-only messages like "Pulling from library/xxx", "Digest: sha256:xxx"
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	switch p.Status {
	case "Downloading":
		if a.layers[p.ID] == nil {
			a.layers[p.ID] = &layerProgress{}
		}
		a.layers[p.ID].current = p.ProgressDetail.Current
		a.layers[p.ID].total = p.ProgressDetail.Total
	case "Download complete", "Pull complete":
		if lp, ok := a.layers[p.ID]; ok {
			lp.current = lp.total
		}
	case "Already exists":
		// Layer already cached, skip
		return
	default:
		return
	}

	// Aggregate across all layers
	var totalDown, totalSize int64
	for _, lp := range a.layers {
		totalDown += lp.current
		if lp.total > 0 {
			totalSize += lp.total
		}
	}

	// Use sizeHint if docker doesn't report per-layer totals
	if totalSize == 0 && a.sizeHint > 0 {
		totalSize = a.sizeHint
	}

	a.onProgress(ProgressEvent{
		Phase:      "pulling",
		Downloaded: totalDown,
		Total:      totalSize,
	})
}
