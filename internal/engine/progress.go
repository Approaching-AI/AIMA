package engine

import (
	"io"
	"sync"
	"time"
)

// ProgressEvent reports download/pull progress for engine operations.
type ProgressEvent struct {
	Phase      string // "resolving", "downloading", "extracting", "pulling", "importing", "complete", "already_available"
	Downloaded int64  // bytes downloaded so far (-1 = unknown)
	Total      int64  // total bytes (-1 = unknown)
	Speed      int64  // bytes/sec (0 = not yet calculated)
	Message    string // human-readable status
}

// progressReader wraps an io.Reader and calls onRead after each Read.
type progressReader struct {
	reader io.Reader
	onRead func(n int)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.onRead != nil {
		r.onRead(n)
	}
	return n, err
}

// progressTracker calculates download speed from periodic samples.
type progressTracker struct {
	mu         sync.Mutex
	downloaded int64
	total      int64
	startTime  time.Time
	lastTime   time.Time
	lastBytes  int64
	speed      int64 // smoothed bytes/sec
}

func newProgressTracker(total int64) *progressTracker {
	now := time.Now()
	return &progressTracker{
		total:     total,
		startTime: now,
		lastTime:  now,
	}
}

func (t *progressTracker) update(downloaded int64) ProgressEvent {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.downloaded = downloaded
	now := time.Now()
	elapsed := now.Sub(t.lastTime)

	// Update speed every 500ms to avoid jitter
	if elapsed >= 500*time.Millisecond {
		delta := downloaded - t.lastBytes
		if elapsed > 0 {
			instantSpeed := float64(delta) / elapsed.Seconds()
			if t.speed == 0 {
				t.speed = int64(instantSpeed)
			} else {
				// Exponential moving average (α=0.3)
				t.speed = int64(0.3*instantSpeed + 0.7*float64(t.speed))
			}
		}
		t.lastTime = now
		t.lastBytes = downloaded
	}

	return ProgressEvent{
		Phase:      "downloading",
		Downloaded: downloaded,
		Total:      t.total,
		Speed:      t.speed,
	}
}
