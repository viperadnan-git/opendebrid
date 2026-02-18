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

	var lastError string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		log.Debug().Str("ytdlp", line).Msg("yt-dlp output")
		parseProgressLine(line, state)
		// Extract filename from destination line
		if dest, ok := strings.CutPrefix(line, "[download] Destination: "); ok {
			state.mu.Lock()
			if state.Name == "" {
				state.Name = filepath.Base(dest)
			}
			state.mu.Unlock()
		}
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

	// Scan for downloaded files
	files := engine.ScanFiles(downloadDir)
	state.mu.Lock()
	state.Files = files
	state.Status = engine.StateCompleted
	state.Progress = 1.0
	state.EngineState = "complete"
	state.mu.Unlock()
	log.Info().Str("url", url).Int("files", len(files)).Msg("yt-dlp download complete")
}

func parseProgressLine(line string, state *jobState) {
	matches := progressRe.FindStringSubmatch(line)
	if len(matches) >= 4 {
		pct, _ := strconv.ParseFloat(matches[1], 64)
		speed := parseSpeed(matches[3])
		state.mu.Lock()
		state.Progress = pct / 100.0
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

// extractInfo runs yt-dlp --dump-json to get metadata without downloading.
func extractInfo(ctx context.Context, binary, url string) (*InfoJSON, error) {
	cmd := exec.CommandContext(ctx, binary, "--dump-json", "--no-warnings", "--no-download", url)
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
