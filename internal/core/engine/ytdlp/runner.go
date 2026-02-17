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

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/rs/zerolog/log"
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
		state.Status = engine.StateFailed
		state.Error = fmt.Sprintf("pipe: %v", err)
		return
	}
	cmd.Stderr = cmd.Stdout

	state.Status = engine.StateActive
	state.EngineState = "downloading"

	if err := cmd.Start(); err != nil {
		state.Status = engine.StateFailed
		state.Error = fmt.Sprintf("start: %v", err)
		return
	}

	var lastError string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		log.Debug().Str("ytdlp", line).Msg("yt-dlp output")
		parseProgressLine(line, state)
		if strings.HasPrefix(line, "ERROR:") {
			lastError = strings.TrimPrefix(line, "ERROR: ")
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			state.Status = engine.StateCancelled
			return
		}
		state.Status = engine.StateFailed
		if lastError != "" {
			state.Error = lastError
		} else {
			state.Error = fmt.Sprintf("exit: %v", err)
		}
		return
	}

	// Scan for downloaded files
	state.Files = engine.ScanFiles(downloadDir)
	state.Status = engine.StateCompleted
	state.Progress = 1.0
	state.EngineState = "complete"
	log.Info().Str("url", url).Int("files", len(state.Files)).Msg("yt-dlp download complete")
}

func parseProgressLine(line string, state *jobState) {
	matches := progressRe.FindStringSubmatch(line)
	if len(matches) >= 4 {
		pct, _ := strconv.ParseFloat(matches[1], 64)
		state.Progress = pct / 100.0
		state.EngineState = "downloading"

		// Parse speed
		speedStr := matches[3]
		state.Speed = parseSpeed(speedStr)
	}

	if strings.Contains(line, "[Merger]") || strings.Contains(line, "Merging") {
		state.EngineState = "merging"
	}
	if strings.Contains(line, "[ExtractAudio]") || strings.Contains(line, "Post-process") {
		state.EngineState = "postprocessing"
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
