package ytdlp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
)

var progressRe = regexp.MustCompile(`\[download\]\s+(\d+\.?\d*)%\s+of\s+~?\s*(\S+)\s+at\s+(\S+)`)

// runDownload executes yt-dlp and tracks progress.
func runDownload(ctx context.Context, binary string, url string, downloadDir string, format string, state *jobState) {
	defer close(state.Done)

	args := []string{
		"--no-warnings",
		"--newline",
		"--progress",
		// Print title and size before each video starts so we can populate state
		// from the single yt-dlp process without a separate metadata fetch.
		"--print", "before_dl:OPENDEBRID_NAME=%(playlist_title,title)s",
		"--print", "before_dl:OPENDEBRID_SIZE=%(filesize,filesize_approx|0)s",
		"-o", filepath.Join(downloadDir, "%(title)s.%(ext)s"),
	}
	if format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, url)

	cmd := exec.CommandContext(ctx, binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		state.mu.Lock()
		state.Status = engine.StateFailed
		state.Error = fmt.Sprintf("pipe: %v", err)
		state.mu.Unlock()
		return
	}
	cmd.Stderr = cmd.Stdout

	state.mu.Lock()
	state.Status = engine.StateActive
	state.EngineState = "downloading"
	state.mu.Unlock()

	if err := cmd.Start(); err != nil {
		state.mu.Lock()
		state.Status = engine.StateFailed
		state.Error = fmt.Sprintf("start: %v", err)
		state.mu.Unlock()
		return
	}

	// completedBytes and currentTrackAnnounced are only accessed by this goroutine.
	// completedBytes accumulates the announced sizes of fully finished tracks so that
	// overall progress can be computed as (completedBytes + currentFileProgress) / TotalSize.
	var completedBytes, currentTrackAnnounced int64

	var lastError string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		log.Debug().Str("ytdlp", line).Msg("yt-dlp output")

		if name, ok := strings.CutPrefix(line, "OPENDEBRID_NAME="); ok {
			state.nameOnce.Do(func() {
				state.mu.Lock()
				state.Name = name
				state.mu.Unlock()
			})
			continue
		}
		if sizeStr, ok := strings.CutPrefix(line, "OPENDEBRID_SIZE="); ok {
			if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil && size > 0 {
				// A new track is starting — the previous one (if any) is done.
				completedBytes += currentTrackAnnounced
				currentTrackAnnounced = size
				atomic.AddInt64(&state.TotalSize, size)
			}
			continue
		}

		parseProgressLine(line, state, completedBytes)
		if strings.HasPrefix(line, "ERROR:") {
			lastError = strings.TrimPrefix(line, "ERROR: ")
		}
	}

	if err := cmd.Wait(); err != nil {
		state.mu.Lock()
		if ctx.Err() != nil {
			state.Status = engine.StateCancelled
		} else if lastError != "" {
			state.Status = engine.StateFailed
			state.Error = lastError
		} else {
			state.Status = engine.StateFailed
			state.Error = fmt.Sprintf("exit: %v", err)
		}
		state.mu.Unlock()
		return
	}

	// Compute actual size from downloaded files — always more accurate than the
	// pre-download estimate (especially for playlists or approximate sizes).
	files := engine.ScanFiles(downloadDir)
	var totalSize int64
	for _, f := range files {
		totalSize += f.Size
	}
	atomic.StoreInt64(&state.TotalSize, totalSize)
	state.mu.Lock()
	state.Files = files
	state.Status = engine.StateCompleted
	state.Progress = 1.0
	state.Downloaded = totalSize
	state.EngineState = "complete"
	state.mu.Unlock()
	log.Info().Str("url", url).Int("files", len(files)).Msg("yt-dlp download complete")
}

func parseProgressLine(line string, state *jobState, completedBytes int64) {
	matches := progressRe.FindStringSubmatch(line)
	if len(matches) >= 4 {
		pct, _ := strconv.ParseFloat(matches[1], 64)
		currentFileSize := parseFileSize(matches[2])
		speed := parseSpeed(matches[3])

		currentFileDownloaded := int64(float64(currentFileSize) * pct / 100.0)
		downloaded := completedBytes + currentFileDownloaded

		totalSize := atomic.LoadInt64(&state.TotalSize)
		progress := pct / 100.0 // fallback: per-file progress when total is unknown
		if totalSize > 0 {
			progress = float64(downloaded) / float64(totalSize)
		}

		state.mu.Lock()
		state.Progress = progress
		state.Downloaded = downloaded
		state.EngineState = "downloading"
		state.Speed = speed
		state.mu.Unlock()
	}

	if strings.Contains(line, "[Merger]") || strings.Contains(line, "Merging") {
		state.mu.Lock()
		state.EngineState = "merging"
		state.mu.Unlock()
	}
	if strings.Contains(line, "[ExtractAudio]") || strings.Contains(line, "Post-process") {
		state.mu.Lock()
		state.EngineState = "postprocessing"
		state.mu.Unlock()
	}
}

func parseFileSize(s string) int64 {
	s = strings.TrimSpace(s)
	s = strings.ToUpper(s)
	multiplier := float64(1)
	switch {
	case strings.HasSuffix(s, "GIB"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GIB")
	case strings.HasSuffix(s, "MIB"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MIB")
	case strings.HasSuffix(s, "KIB"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "KIB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	default:
		return 0
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(val * multiplier)
}

func parseSpeed(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "Unknown" || s == "" {
		return 0
	}

	multiplier := float64(1)
	s = strings.ToUpper(s)
	switch {
	case strings.HasSuffix(s, "GIB/S"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GIB/S")
	case strings.HasSuffix(s, "MIB/S"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MIB/S")
	case strings.HasSuffix(s, "KIB/S"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "KIB/S")
	case strings.HasSuffix(s, "B/S"):
		s = strings.TrimSuffix(s, "B/S")
	default:
		return 0
	}

	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(val * multiplier)
}

// extractInfo runs yt-dlp --dump-single-json to get full metadata without downloading.
// Works for both single videos and playlists.
func extractInfo(ctx context.Context, binary, url string) (*InfoJSON, error) {
	cmd := exec.CommandContext(ctx, binary, "--dump-single-json", "--no-warnings", url)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp info: %w", err)
	}

	var info InfoJSON
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("parse info: %w", err)
	}
	return &info, nil
}
