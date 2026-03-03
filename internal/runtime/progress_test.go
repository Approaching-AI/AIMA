package runtime

import (
	"testing"

	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
)

func TestDetectStartupProgress(t *testing.T) {
	vllmPatterns := &knowledge.StartupLogPatterns{
		Phases: []knowledge.StartupPhasePattern{
			{Name: "loading_weights", Pattern: "Loading model weights", Progress: 40},
			{Name: "cuda_graphs", Pattern: `Capturing CUDA graph.*?(\d+)%`, ProgressRegexGroup: 1, ProgressBase: 40, ProgressRange: 50},
			{Name: "ready", Pattern: "Application startup complete|Uvicorn running", Progress: 100},
		},
		Errors: []knowledge.StartupErrorPattern{
			{Pattern: "OutOfMemoryError|CUDA out of memory", Message: "GPU memory insufficient"},
		},
	}

	tests := []struct {
		name     string
		logText  string
		patterns *knowledge.StartupLogPatterns
		wantPhase    string
		wantProgress int
	}{
		{
			name:         "nil patterns",
			logText:      "some log output",
			patterns:     nil,
			wantPhase:    "",
			wantProgress: 0,
		},
		{
			name:         "no match",
			logText:      "INFO: Starting server...\nINFO: Initializing",
			patterns:     vllmPatterns,
			wantPhase:    "",
			wantProgress: 0,
		},
		{
			name:         "loading weights only",
			logText:      "INFO: Loading model weights took 5.2s",
			patterns:     vllmPatterns,
			wantPhase:    "loading_weights",
			wantProgress: 40,
		},
		{
			name:         "cuda graphs partial",
			logText:      "Loading model weights\nCapturing CUDA graph for batch size 1: 30%",
			patterns:     vllmPatterns,
			wantPhase:    "cuda_graphs",
			wantProgress: 55, // 40 + (30 * 50 / 100)
		},
		{
			name:         "cuda graphs 100%",
			logText:      "Loading model weights\nCapturing CUDA graph: 100%",
			patterns:     vllmPatterns,
			wantPhase:    "cuda_graphs",
			wantProgress: 90, // 40 + (100 * 50 / 100)
		},
		{
			name:         "ready",
			logText:      "Loading model weights\nCapturing CUDA graph: 100%\nApplication startup complete",
			patterns:     vllmPatterns,
			wantPhase:    "ready",
			wantProgress: 100,
		},
		{
			name:    "sglang server starting",
			logText: "loading weights\nThe server is fired up",
			patterns: &knowledge.StartupLogPatterns{
				Phases: []knowledge.StartupPhasePattern{
					{Name: "loading_weights", Pattern: "loading weights", Progress: 40},
					{Name: "server_starting", Pattern: "The server is fired up", Progress: 80},
					{Name: "ready", Pattern: "Application startup complete", Progress: 100},
				},
			},
			wantPhase:    "server_starting",
			wantProgress: 80,
		},
		{
			name:    "llamacpp ready",
			logText: "llm_load_print_meta: general.architecture\nHTTP server listening on 0.0.0.0:8080",
			patterns: &knowledge.StartupLogPatterns{
				Phases: []knowledge.StartupPhasePattern{
					{Name: "loading_model", Pattern: "llm_load_print_meta|loading model", Progress: 50},
					{Name: "ready", Pattern: "HTTP server listening|server listening", Progress: 100},
				},
			},
			wantPhase:    "ready",
			wantProgress: 100,
		},
		{
			name:         "multiple cuda graph updates takes last",
			logText:      "Loading model weights\nCapturing CUDA graph: 10%\nCapturing CUDA graph: 50%\nCapturing CUDA graph: 80%",
			patterns:     vllmPatterns,
			wantPhase:    "cuda_graphs",
			wantProgress: 80, // 40 + (80 * 50 / 100)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectStartupProgress(tt.logText, tt.patterns)
			if got.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Phase, tt.wantPhase)
			}
			if got.Progress != tt.wantProgress {
				t.Errorf("progress = %d, want %d", got.Progress, tt.wantProgress)
			}
		})
	}
}

func TestDetectStartupError(t *testing.T) {
	patterns := &knowledge.StartupLogPatterns{
		Errors: []knowledge.StartupErrorPattern{
			{Pattern: "OutOfMemoryError|CUDA out of memory", Message: "GPU memory insufficient"},
			{Pattern: "ImportError|ModuleNotFoundError", Message: "Missing Python dependency"},
		},
	}

	tests := []struct {
		name    string
		logText string
		want    string
	}{
		{"no error", "INFO: Server started", ""},
		{"OOM", "torch.cuda.OutOfMemoryError: CUDA out of memory", "GPU memory insufficient"},
		{"import error", "ModuleNotFoundError: No module named 'librosa'", "Missing Python dependency"},
		{"nil patterns", "some log", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := patterns
			if tt.name == "nil patterns" {
				p = nil
			}
			got := DetectStartupError(tt.logText, p)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectK3SPhaseFromConditions(t *testing.T) {
	tests := []struct {
		name             string
		conditions       []k3s.PodCondition
		containerRunning bool
		wantPhase        string
		wantProgress     int
	}{
		{
			name:             "container running",
			conditions:       []k3s.PodCondition{{Type: "PodScheduled", Status: "True"}},
			containerRunning: true,
			wantPhase:        "initializing",
			wantProgress:     20,
		},
		{
			name:             "not scheduled",
			conditions:       nil,
			containerRunning: false,
			wantPhase:        "scheduling",
			wantProgress:     2,
		},
		{
			name:             "scheduled not running",
			conditions:       []k3s.PodCondition{{Type: "PodScheduled", Status: "True"}},
			containerRunning: false,
			wantPhase:        "pulling_image",
			wantProgress:     10,
		},
		{
			name:             "scheduled false",
			conditions:       []k3s.PodCondition{{Type: "PodScheduled", Status: "False"}},
			containerRunning: false,
			wantPhase:        "scheduling",
			wantProgress:     2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phase, progress := DetectK3SPhaseFromConditions(tt.conditions, tt.containerRunning)
			if phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", phase, tt.wantPhase)
			}
			if progress != tt.wantProgress {
				t.Errorf("progress = %d, want %d", progress, tt.wantProgress)
			}
		})
	}
}

func TestFormatPhaseName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"loading_weights", "Loading weights..."},
		{"cuda_graphs", "Cuda graphs..."},
		{"ready", "Ready..."},
		{"pulling_image", "Pulling image..."},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := formatPhaseName(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
