package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// EngineImage represents a locally available engine container image.
type EngineImage struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Image     string `json:"image"`
	Tag       string `json:"tag"`
	SizeBytes int64  `json:"size_bytes"`
	Platform  string `json:"platform"`
	Available bool   `json:"available"`
}

// Store is the persistence interface for engine records.
type Store interface {
	InsertEngine(ctx context.Context, e *StoreEngine) error
	GetEngine(ctx context.Context, id string) (*StoreEngine, error)
	ListEngines(ctx context.Context) ([]*StoreEngine, error)
	DeleteEngine(ctx context.Context, id string) error
}

// StoreEngine is the persistence representation of an engine.
type StoreEngine struct {
	ID        string
	Type      string
	Image     string
	Tag       string
	SizeBytes int64
	Platform  string
	Available bool
}

// CommandRunner abstracts shell command execution for testability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ScanOptions configures engine image scanning.
type ScanOptions struct {
	EngineAssets map[string][]string // engine type -> known image names
	Runner       CommandRunner
}

// Scan discovers container images that match known engine types.
func Scan(ctx context.Context, opts ScanOptions) ([]*EngineImage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan engine images: %w", err)
	}

	images, err := listImages(ctx, opts.Runner)
	if err != nil {
		return nil, fmt.Errorf("scan engine images: %w", err)
	}

	return matchImages(images, opts.EngineAssets), nil
}

type imageInfo struct {
	id   string
	repo string // image name without tag
	tag  string
	size int64
}

func listImages(ctx context.Context, runner CommandRunner) ([]imageInfo, error) {
	// Try crictl first (K3S containerd)
	images, err := listCrictlImages(ctx, runner)
	if err == nil {
		return images, nil
	}

	// Fallback to docker
	images, err = listDockerImages(ctx, runner)
	if err != nil {
		return nil, fmt.Errorf("neither crictl nor docker available")
	}

	return images, nil
}

func listCrictlImages(ctx context.Context, runner CommandRunner) ([]imageInfo, error) {
	output, err := runner.Run(ctx, "crictl", "images", "-o", "json")
	if err != nil {
		return nil, err
	}

	var result struct {
		Images []struct {
			ID       string   `json:"id"`
			RepoTags []string `json:"repoTags"`
			Size     string   `json:"size"`
		} `json:"images"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse crictl output: %w", err)
	}

	var images []imageInfo
	for _, img := range result.Images {
		size, _ := strconv.ParseInt(img.Size, 10, 64)
		for _, tag := range img.RepoTags {
			repo, tagStr := splitImageTag(tag)
			images = append(images, imageInfo{
				id:   img.ID,
				repo: repo,
				tag:  tagStr,
				size: size,
			})
		}
	}

	return images, nil
}

func listDockerImages(ctx context.Context, runner CommandRunner) ([]imageInfo, error) {
	output, err := runner.Run(ctx, "docker", "images", "--format", "{{json .}}", "--no-trunc")
	if err != nil {
		return nil, err
	}

	var images []imageInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		var img struct {
			Repository string `json:"Repository"`
			Tag        string `json:"Tag"`
			ID         string `json:"ID"`
			Size       string `json:"Size"`
		}
		if err := json.Unmarshal([]byte(line), &img); err != nil {
			continue
		}
		images = append(images, imageInfo{
			id:   img.ID,
			repo: img.Repository,
			tag:  img.Tag,
		})
	}

	return images, nil
}

func matchImages(images []imageInfo, engineAssets map[string][]string) []*EngineImage {
	var matched []*EngineImage

	for engineType, knownImages := range engineAssets {
		for _, img := range images {
			for _, knownImage := range knownImages {
				if img.repo == knownImage {
					matched = append(matched, &EngineImage{
						ID:        img.id,
						Type:      engineType,
						Image:     img.repo,
						Tag:       img.tag,
						SizeBytes: img.size,
						Available: true,
					})
					break
				}
			}
		}
	}

	return matched
}

func splitImageTag(ref string) (repo, tag string) {
	// Handle format "repo:tag"
	if idx := strings.LastIndex(ref, ":"); idx != -1 {
		// Make sure the colon is not inside a port number (check if after last /)
		slashIdx := strings.LastIndex(ref, "/")
		if idx > slashIdx {
			return ref[:idx], ref[idx+1:]
		}
	}
	return ref, "latest"
}
