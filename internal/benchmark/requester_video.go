package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VideoGenRequester implements Requester for text-to-video (and image-to-video) benchmarks.
type VideoGenRequester struct {
	Model         string
	Prompt        string
	Width         int
	Height        int
	DurationS     float64
	FPS           int
	Steps         int
	GuidanceScale float64
	InputImageURL string // non-empty = I2V mode
	APIKey        string
	Timeout       time.Duration
	PollInterval  time.Duration
}

func (r *VideoGenRequester) Modality() string    { return "video_gen" }
func (r *VideoGenRequester) WarmupRequests() int  { return 1 }

func (r *VideoGenRequester) Do(ctx context.Context, endpoint string, seq int) (*Sample, error) {
	durationS := r.DurationS
	if durationS == 0 {
		durationS = 5
	}
	fps := r.FPS
	if fps == 0 {
		fps = 16
	}
	steps := r.Steps
	if steps == 0 {
		steps = 30
	}
	pollInterval := r.PollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload := map[string]any{
		"prompt":              r.Prompt,
		"width":               r.Width,
		"height":              r.Height,
		"duration":            durationS,
		"fps":                 fps,
		"num_inference_steps": steps,
	}
	if r.GuidanceScale > 0 {
		payload["guidance_scale"] = r.GuidanceScale
	}
	if r.Model != "" {
		payload["model"] = r.Model
	}
	if r.InputImageURL != "" {
		payload["image"] = r.InputImageURL
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("marshal video request: %w", err)}, nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("create video request: %w", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	start := time.Now()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
		return &Sample{Seq: seq, LatencyMs: latencyMs, Error: fmt.Errorf("send video request: %w", err)}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
		return &Sample{Seq: seq, LatencyMs: latencyMs, Error: fmt.Errorf("read video response: %w", err)}, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
		return &Sample{Seq: seq, LatencyMs: latencyMs, Error: fmt.Errorf("video HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 512))}, nil
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Non-JSON response — treat as synchronous completion (raw video data)
		latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
		s := r.makeSample(seq, latencyMs, durationS, fps, steps)
		return &s, nil
	}

	// Check if synchronous completion
	if isVideoCompleted(result) {
		latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
		s := r.makeSample(seq, latencyMs, durationS, fps, steps)
		return &s, nil
	}

	// Check for async job ID
	jobID := extractJobID(result)
	if jobID == "" {
		// No job ID and not completed — treat as synchronous
		latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
		s := r.makeSample(seq, latencyMs, durationS, fps, steps)
		return &s, nil
	}

	// Async mode: poll for completion
	pollURL := strings.TrimRight(endpoint, "/") + "/status/" + jobID
	sample := r.pollForCompletion(ctx, pollURL, pollInterval, start)
	if sample.Error != nil {
		sample.Seq = seq
		return &sample, nil
	}
	s := r.makeSample(seq, sample.LatencyMs, durationS, fps, steps)
	return &s, nil
}

func (r *VideoGenRequester) pollForCompletion(ctx context.Context, pollURL string, interval time.Duration, start time.Time) Sample {
	for {
		select {
		case <-ctx.Done():
			latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
			return Sample{LatencyMs: latencyMs, Error: fmt.Errorf("video poll timeout: %w", ctx.Err())}
		case <-time.After(interval):
		}

		req, err := http.NewRequestWithContext(ctx, "GET", pollURL, nil)
		if err != nil {
			latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
			return Sample{LatencyMs: latencyMs, Error: fmt.Errorf("create poll request: %w", err)}
		}
		if r.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+r.APIKey)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
			return Sample{LatencyMs: latencyMs, Error: fmt.Errorf("poll video status: %w", err)}
		}

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		resp.Body.Close()
		if err != nil {
			latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
			return Sample{LatencyMs: latencyMs, Error: fmt.Errorf("read poll response: %w", err)}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
			return Sample{LatencyMs: latencyMs, Error: fmt.Errorf("poll HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 512))}
		}

		var result map[string]any
		if err := json.Unmarshal(respBody, &result); err != nil {
			continue
		}

		status, _ := result["status"].(string)
		switch status {
		case "completed":
			latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
			return Sample{LatencyMs: latencyMs}
		case "failed":
			latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
			errMsg, _ := result["error"].(string)
			if errMsg == "" {
				errMsg = "video generation failed"
			}
			return Sample{LatencyMs: latencyMs, Error: fmt.Errorf("video job failed: %s", errMsg)}
		}
		// Still processing — continue polling
	}
}

func (r *VideoGenRequester) makeSample(seq int, latencyMs, durationS float64, fps, steps int) Sample {
	return Sample{
		Seq:             seq,
		LatencyMs:       latencyMs,
		VideoDurationS:  durationS,
		FramesGenerated: int(durationS * float64(fps)),
		FPS:             fps,
		VideoWidthPx:    r.Width,
		VideoHeightPx:   r.Height,
		VideoSteps:      steps,
	}
}

func isVideoCompleted(resp map[string]any) bool {
	// Check explicit status field
	if status, ok := resp["status"].(string); ok {
		return status == "completed"
	}
	// Check for video data (e.g. "video_url", "video", "output")
	for _, key := range []string{"video_url", "video", "output", "data"} {
		if _, ok := resp[key]; ok {
			return true
		}
	}
	return false
}

func extractJobID(resp map[string]any) string {
	// Try "job_id" first, then "id"
	if id, ok := resp["job_id"].(string); ok && id != "" {
		return id
	}
	if id, ok := resp["id"].(string); ok && id != "" {
		return id
	}
	return ""
}
