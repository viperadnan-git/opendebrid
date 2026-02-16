package engine

import (
	"context"
	"time"
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
	Status(ctx context.Context, engineJobID string) (JobStatus, error)
	ListFiles(ctx context.Context, engineJobID string) ([]FileInfo, error)
	Cancel(ctx context.Context, engineJobID string) error
	Remove(ctx context.Context, engineJobID string) error

	// Cache
	ResolveCacheKey(ctx context.Context, url string) (CacheKey, error)
}

type EngineConfig struct {
	DownloadDir   string
	MaxConcurrent int
	Extra         map[string]string
}

type Capabilities struct {
	AcceptsSchemes   []string
	AcceptsMIME      []string
	SupportsPlaylist bool
	SupportsStreaming bool
	SupportsInfo     bool
	Custom           map[string]bool
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
	URL     string
	Options map[string]string
}

type AddResponse struct {
	EngineJobID string
	CacheKey    CacheKey
}

type JobStatus struct {
	EngineJobID    string
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
