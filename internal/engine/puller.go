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
	Image          string
	Tag            string
	Registries     []string
	SizeHintMB     int                 // from engine YAML size_approx_mb, for progress estimation
	OnProgress     func(ProgressEvent) // called with pull progress (may be nil)
	Runner         CommandRunner
	ExpectedDigest string // OCI content digest e.g. "sha256:abc123..." (optional)
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

	var attemptErrs []string
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
		crictlErr := error(nil)
		if _, err := runCrictl(ctx, opts.Runner, "pull", ref); err == nil {
			if err := VerifyImageDigest(ctx, opts.Runner, ref, opts.ExpectedDigest); err != nil {
				attemptErrs = append(attemptErrs, fmt.Sprintf("%s (crictl verification: %v)", ref, err))
				continue
			}
			if opts.OnProgress != nil {
				opts.OnProgress(ProgressEvent{Phase: "complete", Message: "image pulled via crictl"})
			}
			return nil
		} else {
			crictlErr = err
		}

		// Fallback to docker. When progress is requested, prefer streaming output.
		var dockerErr error
		if opts.OnProgress != nil {
			agg := newDockerPullAggregator(opts.OnProgress, int64(opts.SizeHintMB)*1024*1024)
			err := opts.Runner.RunStream(ctx, agg.onLine, "docker", "pull", ref)
			if err == nil {
				if err = VerifyImageDigest(ctx, opts.Runner, ref, opts.ExpectedDigest); err == nil {
					opts.OnProgress(ProgressEvent{Phase: "complete", Message: "image pulled via docker"})
					return nil
				}
			}
			dockerErr = err
		} else {
			if _, err := opts.Runner.Run(ctx, "docker", "pull", ref); err == nil {
				if err = VerifyImageDigest(ctx, opts.Runner, ref, opts.ExpectedDigest); err == nil {
					return nil
				}
				dockerErr = err
			} else {
				dockerErr = err
			}
		}
		attemptErrs = append(attemptErrs, fmt.Sprintf("%s (crictl: %v; docker: %v)", ref, crictlErr, dockerErr))
	}

	return fmt.Errorf("pull image %s:%s: all registries failed: %s", opts.Image, opts.Tag, strings.Join(attemptErrs, "; "))
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

// TagDockerImage aliases src to dst in the Docker store. Used to alias a
// compatible locally-present tag to the catalog-pinned tag so deploy can
// reference the pinned name without re-pulling multi-GB of bytes. No-op if
// src and dst are identical.
func TagDockerImage(ctx context.Context, src, dst string, runner CommandRunner) error {
	if src == dst || src == "" || dst == "" {
		return nil
	}
	if _, err := runner.Run(ctx, "docker", "tag", src, dst); err != nil {
		return fmt.Errorf("docker tag %s %s: %w", src, dst, err)
	}
	return nil
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

// VerifyImageDigest checks the local image's repository digests against the
// expected immutable OCI digest. An expected digest is a strict requirement:
// missing inspection evidence and mismatches both return an error.
func VerifyImageDigest(ctx context.Context, runner CommandRunner, ref, expectedDigest string) error {
	expectedDigest = strings.TrimSpace(expectedDigest)
	if expectedDigest == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify image digest for %s: %w", ref, err)
	}

	var actualDigests []string
	var inspectErrs []string
	seen := make(map[string]struct{})
	addDigests := func(repoDigests []string) bool {
		matched := false
		for _, repoDigest := range repoDigests {
			actual := repositoryDigest(repoDigest)
			if actual == "" {
				continue
			}
			if _, ok := seen[actual]; !ok {
				seen[actual] = struct{}{}
				actualDigests = append(actualDigests, actual)
			}
			if strings.EqualFold(actual, expectedDigest) {
				matched = true
			}
		}
		return matched
	}

	// Try docker inspect first.
	out, err := runner.Run(ctx, "docker", "inspect", "--format", "{{json .RepoDigests}}", ref)
	if err == nil {
		var digests []string
		if jsonErr := json.Unmarshal(out, &digests); jsonErr != nil {
			inspectErrs = append(inspectErrs, fmt.Sprintf("docker inspect output: %v", jsonErr))
		} else if addDigests(digests) {
			slog.Info("image digest verified", "ref", ref, "digest", expectedDigest)
			return nil
		}
	} else {
		inspectErrs = append(inspectErrs, fmt.Sprintf("docker inspect: %v", err))
	}

	// Try crictl inspecti as fallback.
	out, err = runCrictl(ctx, runner, "inspecti", ref)
	if err == nil {
		var info struct {
			Status struct {
				RepoDigests []string `json:"repoDigests"`
			} `json:"status"`
		}
		if jsonErr := json.Unmarshal(out, &info); jsonErr != nil {
			inspectErrs = append(inspectErrs, fmt.Sprintf("crictl inspect output: %v", jsonErr))
		} else if addDigests(info.Status.RepoDigests) {
			slog.Info("image digest verified", "ref", ref, "digest", expectedDigest)
			return nil
		}
	} else {
		inspectErrs = append(inspectErrs, fmt.Sprintf("crictl inspect: %v", err))
	}

	detail := "no digest evidence reported"
	if len(actualDigests) > 0 {
		detail = "found " + strings.Join(actualDigests, ", ")
	}
	if len(inspectErrs) > 0 {
		detail += "; " + strings.Join(inspectErrs, "; ")
	}
	return fmt.Errorf("image digest mismatch for %s: expected %s, %s", ref, expectedDigest, detail)
}

func repositoryDigest(repoDigest string) string {
	repoDigest = strings.TrimSpace(repoDigest)
	if idx := strings.LastIndex(repoDigest, "@"); idx >= 0 {
		return strings.TrimSpace(repoDigest[idx+1:])
	}
	if strings.Contains(repoDigest, ":") && !strings.Contains(repoDigest, "/") {
		return repoDigest
	}
	return ""
}

// buildImageRef constructs a full image reference from registry, image name, and tag.
// For registries that include a namespace (e.g., "registry.cn-hangzhou.aliyuncs.com/aima"),
// only the base image name is appended (not the full original path).
func buildImageRef(registry, image, tag string) string {
	registry = strings.TrimSuffix(registry, "/")
	image = strings.TrimSuffix(image, "/")
	baseName := path.Base(image)

	// Many catalog entries already carry a fully qualified repository path
	// (for example "docker.io/vllm/vllm-openai"). In that case, don't append
	// the image name again.
	if registry == image || strings.HasSuffix(registry, "/"+baseName) {
		return fmt.Sprintf("%s:%s", registry, tag)
	}

	// Namespace-style registries (for example "registry.cn-hangzhou.aliyuncs.com/aima")
	// want only the image basename appended.
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
