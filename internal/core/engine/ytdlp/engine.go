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

const defaultMaxDownloadTime = 6 * time.Hour

type Engine struct {
	binary          string
	downloadDir     string
	defaultFormat   string
	maxConcurrent   int
	maxDownloadTime time.Duration

	mu   sync.RWMutex
	jobs map[string]*jobState // engineJobID -> state
}

func New() *Engine {
	return &Engine{
		jobs: make(map[string]*jobState),
	}
}

func (e *Engine) Name() string        { return "ytdlp" }
func (e *Engine) DownloadDir() string { return e.downloadDir }

func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		AcceptsSchemes:   []string{"http", "https"},
		SupportsPlaylist: true,
		SupportsInfo:     true,
	}
}

// Config holds yt-dlp-specific configuration passed via EngineConfig.Extra.
type Config struct {
	Binary          string
	DefaultFormat   string
	MaxDownloadTime time.Duration
}

func (e *Engine) Init(_ context.Context, cfg engine.EngineConfig) error {
	var ycfg Config
	if c, ok := cfg.Extra.(Config); ok {
		ycfg = c
	}
	e.binary = ycfg.Binary
	if e.binary == "" {
		e.binary = "yt-dlp"
	}
	e.downloadDir = cfg.DownloadDir
	e.maxConcurrent = cfg.MaxConcurrent
	e.defaultFormat = ycfg.DefaultFormat
	e.maxDownloadTime = ycfg.MaxDownloadTime
	if e.maxDownloadTime <= 0 {
		e.maxDownloadTime = defaultMaxDownloadTime
	}

	// Check binary exists
	if _, err := exec.LookPath(e.binary); err != nil {
		return fmt.Errorf("yt-dlp binary not found: %w", err)
	}

	return os.MkdirAll(e.downloadDir, 0o755)
}

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
	// survives after the response is sent. Timeout prevents stalled downloads
	// from hanging forever. Cancel is stored for explicit cancellation.
	dlCtx, dlCancel := context.WithTimeout(context.Background(), e.maxDownloadTime)

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
		snap := state.snapshot()
		result[id] = engine.JobStatus{
			EngineJobID:    id,
			Name:           snap.Name,
			State:          snap.Status,
			EngineState:    snap.EngineState,
			Progress:       snap.Progress,
			Speed:          snap.Speed,
			TotalSize:      snap.TotalSize,
			DownloadedSize: snap.Downloaded,
			Error:          snap.Error,
		}
		if snap.Status == engine.StateCompleted || snap.Status == engine.StateFailed {
			delete(e.jobs, id)
		}
	}

	return result, nil
}

func (e *Engine) ListFiles(_ context.Context, storageKey, _ string) ([]engine.FileInfo, error) {
	jobDir := filepath.Join(e.downloadDir, storageKey)
	return engine.ScanFiles(jobDir)
}

func (e *Engine) Cancel(_ context.Context, engineJobID string) error {
	e.mu.RLock()
	state, ok := e.jobs[engineJobID]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("job %q not found", engineJobID)
	}
	state.mu.Lock()
	state.Status = engine.StateFailed
	state.mu.Unlock()
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
