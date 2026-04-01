package runtime

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// readTail reads the last n lines from a file by seeking from the end,
// avoiding reading the entire file into memory for large log files.
// For small files (< 256KB), uses a forward scan for reliability on Windows
// where stat size may lag behind writes from child processes.
func readTail(path string, n int) (string, error) {
	if n <= 0 {
		n = 100
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat log: %w", err)
	}
	size := stat.Size()

	// For small files or when stat reports size 0 (Windows edge case with
	// unflushed child process writes), use a simple forward scan.
	const seekThreshold = 256 * 1024 // 256KB
	if size < seekThreshold {
		return readTailForward(f, n)
	}

	// Large files: seek from end to avoid reading gigabytes of logs.
	const initialChunk = 64 * 1024 // 64KB
	chunkSize := int64(initialChunk)

	for {
		offset := size - chunkSize
		if offset < 0 {
			offset = 0
			chunkSize = size
		}

		buf := make([]byte, chunkSize)
		nRead, err := f.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read log: %w", err)
		}
		buf = buf[:nRead]

		// Count newlines from the end
		lineCount := 0
		cutPos := len(buf)
		for i := len(buf) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				lineCount++
				if lineCount > n {
					cutPos = i + 1
					break
				}
			}
		}

		if lineCount > n || offset == 0 {
			result := strings.TrimRight(string(buf[cutPos:]), "\n\r")
			return result, nil
		}

		// Need more data — double chunk size
		chunkSize *= 2
		if chunkSize > size {
			chunkSize = size
		}
	}
}

// readTailForward reads an already-opened file line by line, keeping the last n lines.
// Used for small files where the seek optimization isn't needed.
func readTailForward(f *os.File, n int) (string, error) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return "", fmt.Errorf("read log: %w", err)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// enrichNativeProgress reads log tail and matches engine patterns to set progress fields.
func (r *NativeRuntime) enrichNativeProgress(ds *DeploymentStatus, logPath string, labels map[string]string) string {
	engineName := ""
	if labels != nil {
		engineName = labels["aima.dev/engine"]
	}
	asset := findEngineAsset(r.engineAssets, engineName)

	if asset != nil && len(asset.TimeConstraints.ColdStartS) >= 2 {
		ds.EstimatedTotalS = asset.TimeConstraints.ColdStartS[1]
	}

	tailLines := 50
	if ds.Phase == "failed" {
		tailLines = 5
	}

	logs, err := readTail(logPath, tailLines)
	if err != nil || logs == "" {
		return ""
	}

	if ds.Phase == "failed" {
		ds.ErrorLines = logs
	}

	if asset == nil || asset.Startup.LogPatterns == nil {
		return ""
	}

	if errMsg := DetectStartupError(logs, asset.Startup.LogPatterns); errMsg != "" {
		ds.StartupMessage = errMsg
		return errMsg
	}

	if ds.Phase == "starting" || (ds.Phase == "running" && !ds.Ready) {
		sp := DetectStartupProgress(logs, asset.Startup.LogPatterns)
		if sp.Progress > 0 {
			ds.StartupPhase = sp.Phase
			ds.StartupProgress = sp.Progress
			ds.StartupMessage = sp.Message
		}
	}
	return ""
}

func (r *NativeRuntime) ensureNativeStartingStatus(ds *DeploymentStatus, startTime time.Time, portBound bool, labels map[string]string) {
	if ds == nil || ds.Ready || ds.Phase == "failed" || ds.Phase == "stopped" {
		return
	}
	ds.Phase = "starting"
	if ds.EstimatedTotalS == 0 {
		ds.EstimatedTotalS = r.nativeEstimatedTotalS(labels)
	}
	if ds.StartupPhase == "" {
		if portBound {
			ds.StartupPhase = "loading_model"
		} else {
			ds.StartupPhase = "initializing"
		}
	}
	if inferred := inferNativeStartupProgress(time.Since(startTime), ds.EstimatedTotalS, portBound); inferred > ds.StartupProgress {
		ds.StartupProgress = inferred
	}
	if ds.StartupMessage == "" {
		ds.StartupMessage = formatPhaseName(ds.StartupPhase)
	}
}

func (r *NativeRuntime) nativeEstimatedTotalS(labels map[string]string) int {
	engineName := ""
	if labels != nil {
		engineName = labels["aima.dev/engine"]
	}
	asset := findEngineAsset(r.engineAssets, engineName)
	if asset != nil && len(asset.TimeConstraints.ColdStartS) >= 2 {
		return asset.TimeConstraints.ColdStartS[1]
	}
	return 0
}

func inferNativeStartupProgress(elapsed time.Duration, estimatedTotalS int, portBound bool) int {
	elapsedS := int(elapsed / time.Second)
	if elapsedS < 0 {
		elapsedS = 0
	}
	if estimatedTotalS <= 0 {
		if portBound {
			return 35
		}
		return 5
	}
	if portBound {
		progress := 25 + (elapsedS * 65 / estimatedTotalS)
		if progress > 90 {
			progress = 90
		}
		if progress < 25 {
			progress = 25
		}
		return progress
	}
	progress := 5 + (elapsedS * 20 / estimatedTotalS)
	if progress > 25 {
		progress = 25
	}
	if progress < 5 {
		progress = 5
	}
	return progress
}
