package ytdlp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/opendebrid/opendebrid/internal/core/engine"
)

type Engine struct {
	binary        string
	downloadDir   string
	defaultFormat string
	maxConcurrent int

	mu   sync.RWMutex
	jobs map[string]*jobState // engineJobID -> state
}

func New() *Engine {
	return &Engine{
		jobs: make(map[string]*jobState),
	}
}

func (e *Engine) Name() string { return "ytdlp" }

func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		AcceptsSchemes:   []string{"http", "https"},
		SupportsPlaylist: true,
		SupportsInfo:     true,
	}
}

func (e *Engine) Init(_ context.Context, cfg engine.EngineConfig) error {
	e.binary = cfg.Extra["binary"]
	if e.binary == "" {
		e.binary = "yt-dlp"
	}
	e.downloadDir = cfg.DownloadDir
	e.maxConcurrent = cfg.MaxConcurrent
	e.defaultFormat = cfg.Extra["default_format"]

	// Check binary exists
	if _, err := exec.LookPath(e.binary); err != nil {
		return fmt.Errorf("yt-dlp binary not found: %w", err)
	}

	return os.MkdirAll(e.downloadDir, 0o755)
}

func (e *Engine) Start(_ context.Context) error { return nil }
func (e *Engine) Stop(_ context.Context) error  { return nil }

func (e *Engine) Health(ctx context.Context) engine.HealthStatus {
	start := time.Now()
	cmd := exec.CommandContext(ctx, e.binary, "--version")
	out, err := cmd.Output()
	latency := time.Since(start)
	if err != nil {
		return engine.HealthStatus{OK: false, Message: err.Error(), Latency: latency}
	}
	return engine.HealthStatus{
		OK:      true,
		Message: "yt-dlp " + string(out[:len(out)-1]),
		Latency: latency,
	}
}

func (e *Engine) Add(ctx context.Context, req engine.AddRequest) (engine.AddResponse, error) {
	jobID := req.JobID
	jobDir := filepath.Join(e.downloadDir, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return engine.AddResponse{}, fmt.Errorf("create job dir: %w", err)
	}

	// Create a context detached from the HTTP request so the download
	// survives after the response is sent. Cancel is stored for explicit cancellation.
	dlCtx, dlCancel := context.WithCancel(context.Background())

	state := &jobState{
		JobID:       jobID,
		URL:         req.URL,
		DownloadDir: jobDir,
		Status:      engine.StateQueued,
		Done:        make(chan struct{}),
		cancel:      dlCancel,
	}

	e.mu.Lock()
	e.jobs[jobID] = state
	e.mu.Unlock()

	format := e.defaultFormat
	if f, ok := req.Options["format"]; ok {
		format = f
	}

	// Run download in background goroutine
	go runDownload(dlCtx, e.binary, req.URL, jobDir, format, state)

	return engine.AddResponse{
		EngineJobID: jobID,
	}, nil
}

func (e *Engine) Status(_ context.Context, engineJobID string) (engine.JobStatus, error) {
	e.mu.RLock()
	state, ok := e.jobs[engineJobID]
	e.mu.RUnlock()
	if !ok {
		return engine.JobStatus{}, fmt.Errorf("job %q not found", engineJobID)
	}

	return engine.JobStatus{
		EngineJobID:    engineJobID,
		State:          state.Status,
		EngineState:    state.EngineState,
		Progress:       state.Progress,
		Speed:          state.Speed,
		TotalSize:      state.TotalSize,
		DownloadedSize: state.Downloaded,
		Error:          state.Error,
	}, nil
}

func (e *Engine) ListFiles(_ context.Context, engineJobID string) ([]engine.FileInfo, error) {
	e.mu.RLock()
	state, ok := e.jobs[engineJobID]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("job %q not found", engineJobID)
	}

	// Re-scan directory for any new files
	if len(state.Files) == 0 {
		state.Files = engine.ScanFiles(state.DownloadDir)
	}
	return state.Files, nil
}

func (e *Engine) Cancel(_ context.Context, engineJobID string) error {
	e.mu.Lock()
	state, ok := e.jobs[engineJobID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %q not found", engineJobID)
	}
	state.Status = engine.StateCancelled
	if state.cancel != nil {
		state.cancel()
	}
	return nil
}

func (e *Engine) Remove(_ context.Context, engineJobID string) error {
	e.mu.Lock()
	state, ok := e.jobs[engineJobID]
	delete(e.jobs, engineJobID)
	e.mu.Unlock()
	if !ok {
		return nil
	}
	// Clean up files
	os.RemoveAll(state.DownloadDir)
	return nil
}

func (e *Engine) ResolveCacheKey(_ context.Context, rawURL string) (engine.CacheKey, error) {
	return engine.CacheKey{Type: engine.CacheKeyURL, Value: rawURL}, nil
}

// Info extracts metadata without downloading (for the /info endpoint).
func (e *Engine) Info(ctx context.Context, url string) (*InfoJSON, error) {
	return extractInfo(ctx, e.binary, url)
}
