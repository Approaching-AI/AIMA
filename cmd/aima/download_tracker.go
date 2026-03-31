package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	downloadFinishedRetention = 30 * time.Second
	downloadActiveTTL         = 45 * time.Second
	downloadKeepAliveInterval = 5 * time.Second
)

// DownloadProgress represents the current state of an engine or model download.
type DownloadProgress struct {
	ID         string `json:"id"`
	Type       string `json:"type"` // "engine" | "model"
	Name       string `json:"name"`
	Phase      string `json:"phase"`
	Downloaded int64  `json:"downloaded"` // bytes
	Total      int64  `json:"total"`      // bytes, -1 = unknown
	Speed      int64  `json:"speed"`      // bytes/sec
	Message    string `json:"message"`
	Error      string `json:"error,omitempty"`
	StartedAt  int64  `json:"started_at"`            // unix ms
	UpdatedAt  int64  `json:"updated_at"`            // unix ms
	FinishedAt int64  `json:"finished_at,omitempty"` // unix ms
}

// DownloadTracker persists download progress under dataDir/downloads so every
// aima process observes the same state.
type DownloadTracker struct {
	dir string
	mu  sync.Mutex
}

func NewDownloadTracker(dir string) *DownloadTracker {
	return &DownloadTracker{dir: dir}
}

func newByteProgressReporter(tracker *DownloadTracker, id, phase string) func(downloaded, total int64) {
	lastSpeed := int64(0)
	lastBytes := int64(0)
	lastTime := time.Now()
	return func(downloaded, total int64) {
		now := time.Now()
		elapsed := now.Sub(lastTime).Seconds()
		if elapsed > 0.5 {
			lastSpeed = int64(float64(downloaded-lastBytes) / elapsed)
			lastTime = now
			lastBytes = downloaded
		}
		tracker.Update(id, phase, "", downloaded, total, lastSpeed)
	}
}

func (t *DownloadTracker) Start(id, typ, name string) {
	now := time.Now().UnixMilli()
	t.store(&DownloadProgress{
		ID:        id,
		Type:      typ,
		Name:      name,
		Phase:     "starting",
		Total:     -1,
		StartedAt: now,
		UpdatedAt: now,
	})
}

func (t *DownloadTracker) Update(id, phase, message string, downloaded, total, speed int64) {
	t.mutate(id, func(e *DownloadProgress) {
		if phase != "" {
			e.Phase = phase
		}
		if message != "" {
			e.Message = message
		}
		if downloaded >= 0 {
			e.Downloaded = downloaded
		}
		if total >= 0 {
			e.Total = total
		}
		if speed >= 0 {
			e.Speed = speed
		}
	})
}

func (t *DownloadTracker) Touch(id string) {
	t.mutate(id, func(e *DownloadProgress) {})
}

func (t *DownloadTracker) KeepAlive(id string, stop <-chan struct{}) {
	ticker := time.NewTicker(downloadKeepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			t.Touch(id)
		}
	}
}

func (t *DownloadTracker) Finish(id string, err error) {
	t.mutate(id, func(e *DownloadProgress) {
		e.FinishedAt = time.Now().UnixMilli()
		if err != nil {
			e.Phase = "failed"
			e.Error = err.Error()
			return
		}
		e.Phase = "complete"
		if e.Total >= 0 {
			e.Downloaded = e.Total
		}
	})
}

func (t *DownloadTracker) List() []*DownloadProgress {
	t.mu.Lock()
	defer t.mu.Unlock()

	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("download tracker: read dir failed", "dir", t.dir, "error", err)
		}
		return nil
	}

	now := time.Now()
	result := make([]*DownloadProgress, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(t.dir, entry.Name())
		progress, ok := t.readFile(path)
		if !ok {
			continue
		}
		if t.expired(progress, now) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				slog.Warn("download tracker: cleanup failed", "path", path, "error", err)
			}
			continue
		}
		result = append(result, progress)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].StartedAt > result[j].StartedAt
	})
	return result
}

func (t *DownloadTracker) mutate(id string, mutate func(*DownloadProgress)) {
	t.mu.Lock()
	defer t.mu.Unlock()

	progress, ok := t.readByID(id)
	if !ok {
		return
	}
	mutate(progress)
	progress.UpdatedAt = time.Now().UnixMilli()
	t.write(progress)
}

func (t *DownloadTracker) store(progress *DownloadProgress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.write(progress)
}

func (t *DownloadTracker) expired(progress *DownloadProgress, now time.Time) bool {
	updatedAt := unixMillis(progress.UpdatedAt)
	switch progress.Phase {
	case "complete", "failed":
		finishedAt := unixMillis(progress.FinishedAt)
		if finishedAt.IsZero() {
			finishedAt = updatedAt
		}
		return !finishedAt.IsZero() && now.Sub(finishedAt) > downloadFinishedRetention
	default:
		return !updatedAt.IsZero() && now.Sub(updatedAt) > downloadActiveTTL
	}
}

func (t *DownloadTracker) readByID(id string) (*DownloadProgress, bool) {
	return t.readFile(t.pathForID(id))
}

func (t *DownloadTracker) readFile(path string) (*DownloadProgress, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("download tracker: read failed", "path", path, "error", err)
		}
		return nil, false
	}
	var progress DownloadProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		slog.Warn("download tracker: parse failed", "path", path, "error", err)
		return nil, false
	}
	return &progress, true
}

func (t *DownloadTracker) write(progress *DownloadProgress) {
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		slog.Warn("download tracker: mkdir failed", "dir", t.dir, "error", err)
		return
	}

	data, err := json.Marshal(progress)
	if err != nil {
		slog.Warn("download tracker: marshal failed", "id", progress.ID, "error", err)
		return
	}

	tmp, err := os.CreateTemp(t.dir, ".download-*.tmp")
	if err != nil {
		slog.Warn("download tracker: create temp failed", "dir", t.dir, "error", err)
		return
	}
	tmpPath := tmp.Name()
	targetPath := t.pathForID(progress.ID)
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		slog.Warn("download tracker: write temp failed", "path", tmpPath, "error", err)
		return
	}
	if err := tmp.Close(); err != nil {
		slog.Warn("download tracker: close temp failed", "path", tmpPath, "error", err)
		return
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		slog.Warn("download tracker: rename failed", "from", tmpPath, "to", targetPath, "error", err)
	}
}

func (t *DownloadTracker) pathForID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(t.dir, fmt.Sprintf("%x.json", sum[:8]))
}

func unixMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
