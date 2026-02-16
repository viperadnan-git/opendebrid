package aria2

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/opendebrid/opendebrid/internal/core/engine"
)

type Engine struct {
	client      *Client
	downloadDir string
	maxConcurrent int
}

func New() *Engine {
	return &Engine{}
}

func (e *Engine) Name() string { return "aria2" }

func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		AcceptsSchemes:   []string{"magnet", "http", "https", "ftp"},
		AcceptsMIME:      []string{"application/x-bittorrent"},
		SupportsPlaylist: false,
		SupportsStreaming: false,
		SupportsInfo:     false,
	}
}

func (e *Engine) Init(_ context.Context, cfg engine.EngineConfig) error {
	rpcURL := cfg.Extra["rpc_url"]
	if rpcURL == "" {
		rpcURL = "http://localhost:6800/jsonrpc"
	}
	e.client = NewClient(rpcURL, cfg.Extra["rpc_secret"])
	e.downloadDir = cfg.DownloadDir
	e.maxConcurrent = cfg.MaxConcurrent

	if err := os.MkdirAll(e.downloadDir, 0o755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}
	return nil
}

func (e *Engine) Start(_ context.Context) error {
	// aria2 daemon is managed by the process manager, not the engine
	return nil
}

func (e *Engine) Stop(_ context.Context) error {
	return nil
}

func (e *Engine) Health(ctx context.Context) engine.HealthStatus {
	start := time.Now()
	version, err := e.client.GetVersion(ctx)
	latency := time.Since(start)
	if err != nil {
		return engine.HealthStatus{OK: false, Message: err.Error(), Latency: latency}
	}
	return engine.HealthStatus{OK: true, Message: "aria2 " + version, Latency: latency}
}

func (e *Engine) Add(ctx context.Context, req engine.AddRequest) (engine.AddResponse, error) {
	opts := map[string]string{
		"dir": e.downloadDir,
	}
	for k, v := range req.Options {
		opts[k] = v
	}

	gid, err := e.client.AddURI(ctx, []string{req.URL}, opts)
	if err != nil {
		return engine.AddResponse{}, fmt.Errorf("aria2 add: %w", err)
	}

	cacheKey, _ := e.ResolveCacheKey(ctx, req.URL)

	return engine.AddResponse{
		EngineJobID: gid,
		CacheKey:    cacheKey,
	}, nil
}

func (e *Engine) Status(ctx context.Context, engineJobID string) (engine.JobStatus, error) {
	s, err := e.client.TellStatus(ctx, engineJobID)
	if err != nil {
		return engine.JobStatus{}, fmt.Errorf("aria2 status: %w", err)
	}

	state, engineState := mapStatus(s.Status)
	total, _ := strconv.ParseInt(s.TotalLength, 10, 64)
	completed, _ := strconv.ParseInt(s.CompletedLength, 10, 64)
	speed, _ := strconv.ParseInt(s.DownloadSpeed, 10, 64)

	var progress float64
	if total > 0 {
		progress = float64(completed) / float64(total)
	}

	var eta time.Duration
	if speed > 0 && total > completed {
		eta = time.Duration((total-completed)/speed) * time.Second
	}

	js := engine.JobStatus{
		EngineJobID:    engineJobID,
		State:          state,
		EngineState:    engineState,
		Progress:       progress,
		Speed:          speed,
		TotalSize:      total,
		DownloadedSize: completed,
		ETA:            eta,
		Extra:          make(map[string]any),
	}

	if s.ErrorMessage != "" {
		js.Error = s.ErrorMessage
	}
	if s.InfoHash != "" {
		js.Extra["info_hash"] = s.InfoHash
	}
	if s.NumSeeders != "" {
		js.Extra["seeders"] = s.NumSeeders
	}

	return js, nil
}

func (e *Engine) ListFiles(ctx context.Context, engineJobID string) ([]engine.FileInfo, error) {
	s, err := e.client.TellStatus(ctx, engineJobID)
	if err != nil {
		return nil, fmt.Errorf("aria2 list files: %w", err)
	}

	var files []engine.FileInfo
	for _, f := range s.Files {
		size, _ := strconv.ParseInt(f.Length, 10, 64)
		if f.Path == "" {
			continue
		}
		relPath, _ := filepath.Rel(e.downloadDir, f.Path)
		files = append(files, engine.FileInfo{
			Path:       relPath,
			Size:       size,
			StorageURI: "file://" + f.Path,
		})
	}
	return files, nil
}

func (e *Engine) Cancel(ctx context.Context, engineJobID string) error {
	if err := e.client.ForceRemove(ctx, engineJobID); err != nil {
		return fmt.Errorf("aria2 cancel: %w", err)
	}
	return nil
}

func (e *Engine) Remove(ctx context.Context, engineJobID string) error {
	// Try to remove active download first, ignore error if already stopped
	_ = e.client.ForceRemove(ctx, engineJobID)
	// Clean up result
	_ = e.client.RemoveDownloadResult(ctx, engineJobID)
	return nil
}

func (e *Engine) ResolveCacheKey(_ context.Context, rawURL string) (engine.CacheKey, error) {
	// Magnet links: extract info_hash
	if strings.HasPrefix(rawURL, "magnet:") {
		u, err := url.Parse(rawURL)
		if err == nil {
			xt := u.Query().Get("xt")
			// xt=urn:btih:<hash>
			if strings.HasPrefix(xt, "urn:btih:") {
				hash := strings.ToLower(strings.TrimPrefix(xt, "urn:btih:"))
				return engine.CacheKey{Type: engine.CacheKeyHash, Value: hash}, nil
			}
		}
	}

	// HTTP/FTP: SHA256 of normalized URL
	h := sha256.Sum256([]byte(rawURL))
	return engine.CacheKey{
		Type:  engine.CacheKeyURL,
		Value: fmt.Sprintf("%x", h),
	}, nil
}
