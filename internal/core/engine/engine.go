package engine

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/viperadnan-git/opendebrid/internal/core/process"
)

// Engine is the core download engine interface.
type Engine interface {
	Name() string
	Capabilities() Capabilities

	// Lifecycle
	Init(ctx context.Context, cfg EngineConfig) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Health(ctx context.Context) HealthStatus

	// Download operations
	Add(ctx context.Context, req AddRequest) (AddResponse, error)
	BatchStatus(ctx context.Context, engineJobIDs []string) (map[string]JobStatus, error)
	ListFiles(ctx context.Context, jobID, engineJobID string) ([]FileInfo, error)
	Cancel(ctx context.Context, engineJobID string) error
	Remove(ctx context.Context, jobID string, engineJobID string) error

	// Cache
	ResolveCacheKey(ctx context.Context, url string) (CacheKey, error)
}

// DaemonEngine is an optional interface engines can implement when they
// require an external daemon process (e.g. aria2c).
type DaemonEngine interface {
	Engine
	Daemon() process.Daemon
}

type EngineConfig struct {
	DownloadDir   string
	MaxConcurrent int
	Extra         map[string]string
}

type Capabilities struct {
	AcceptsSchemes    []string
	AcceptsMIME       []string
	SupportsPlaylist  bool
	SupportsStreaming bool
	SupportsInfo      bool
	Custom            map[string]bool
}

type CacheKey struct {
	Type  CacheKeyType
	Value string
}

func (c CacheKey) Full() string {
	if c.Value == "" {
		return ""
	}
	return string(c.Type) + ":" + c.Value
}

type CacheKeyType string

const (
	CacheKeyHash     CacheKeyType = "hash"
	CacheKeyURL      CacheKeyType = "url"
	CacheKeyCustomID CacheKeyType = "custom_id"
)

type AddRequest struct {
	JobID   string // DB job UUID, used as engine job ID and download directory name
	URL     string
	Options map[string]string
}

type AddResponse struct {
	EngineJobID string
	CacheKey    CacheKey
}

type JobStatus struct {
	EngineJobID    string
	Name           string
	State          JobState
	EngineState    string
	Progress       float64
	Speed          int64
	TotalSize      int64
	DownloadedSize int64
	ETA            time.Duration
	Error          string
	Extra          map[string]any
}

type JobState string

const (
	StateQueued    JobState = "queued"
	StateActive    JobState = "active"
	StateCompleted JobState = "completed"
	StateFailed    JobState = "failed"
	StateCancelled JobState = "cancelled"
)

type FileInfo struct {
	Path        string
	Size        int64
	StorageURI  string
	ContentType string
}

type HealthStatus struct {
	OK      bool
	Message string
	Latency time.Duration
}

// ScanFiles recursively lists all files under a directory and returns their metadata.
// Paths are relative to dir. Engines can use this directly or build on top of it.
func ScanFiles(dir string) []FileInfo {
	var files []FileInfo
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		var size int64
		if info, err := d.Info(); err == nil {
			size = info.Size()
		}
		files = append(files, FileInfo{
			Path:       rel,
			Size:       size,
			StorageURI: "file://" + path,
		})
		return nil
	})
	return files
}
