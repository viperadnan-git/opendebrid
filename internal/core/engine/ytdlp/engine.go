package ytdlp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
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
	dirKey := req.StorageKey

	// If a download for this storage key already exists, piggyback on it
	e.mu.RLock()
	if existing, ok := e.jobs[dirKey]; ok {
		e.mu.RUnlock()
		return engine.AddResponse{
			EngineJobID: existing.JobID,
		}, nil
	}
	e.mu.RUnlock()

	jobDir := filepath.Join(e.downloadDir, dirKey)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return engine.AddResponse{}, fmt.Errorf("create job dir: %w", err)
	}

	// Create a context detached from the HTTP request so the download
	// survives after the response is sent. Cancel is stored for explicit cancellation.
	dlCtx, dlCancel := context.WithCancel(context.Background())

	state := &jobState{
		JobID:       dirKey,
		URL:         req.URL,
		DownloadDir: jobDir,
		Status:      engine.StateQueued,
		Done:        make(chan struct{}),
		cancel:      dlCancel,
	}

	e.mu.Lock()
	// Double-check after acquiring write lock
	if existing, ok := e.jobs[dirKey]; ok {
		e.mu.Unlock()
		dlCancel()
		return engine.AddResponse{
			EngineJobID: existing.JobID,
		}, nil
	}
	e.jobs[dirKey] = state
	e.mu.Unlock()

	format := e.defaultFormat
	if f, ok := req.Options["format"]; ok {
		format = f
	}

	// Run download in background goroutine
	go runDownload(dlCtx, e.binary, req.URL, jobDir, format, state)

	return engine.AddResponse{
		EngineJobID: dirKey,
	}, nil
}

func (e *Engine) BatchStatus(_ context.Context, engineJobIDs []string) (map[string]engine.JobStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	result := make(map[string]engine.JobStatus, len(engineJobIDs))
	for _, id := range engineJobIDs {
		state, ok := e.jobs[id]
		if !ok {
			continue
		}
		result[id] = engine.JobStatus{
			EngineJobID:    id,
			Name:           state.Name,
			State:          state.Status,
			EngineState:    state.EngineState,
			Progress:       state.Progress,
			Speed:          state.Speed,
			TotalSize:      state.TotalSize,
			DownloadedSize: state.Downloaded,
			Error:          state.Error,
		}
		// Clean up terminal jobs from memory — files remain on disk
		if state.Status == engine.StateCompleted || state.Status == engine.StateFailed || state.Status == engine.StateCancelled {
			delete(e.jobs, id)
		}
	}
	return result, nil
}

func (e *Engine) ListFiles(_ context.Context, storageKey, _ string) ([]engine.FileInfo, error) {
	jobDir := filepath.Join(e.downloadDir, storageKey)
	return engine.ScanFiles(jobDir), nil
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

func (e *Engine) Remove(_ context.Context, storageKey, _ string) error {
	e.mu.Lock()
	delete(e.jobs, storageKey)
	e.mu.Unlock()
	_ = os.RemoveAll(filepath.Join(e.downloadDir, storageKey))
	return nil
}

func (e *Engine) ResolveCacheKey(_ context.Context, rawURL string) (engine.CacheKey, error) {
	return engine.CacheKey{Type: engine.CacheKeyURL, Value: rawURL}, nil
}

// Info extracts metadata without downloading (for the /info endpoint).
func (e *Engine) Info(ctx context.Context, url string) (*InfoJSON, error) {
	return extractInfo(ctx, e.binary, url)
}
