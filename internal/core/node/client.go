package node

import (
	"context"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
)

// NodeClient abstracts communication with a node (local or remote).
type NodeClient interface {
	NodeID() string
	DispatchJob(ctx context.Context, req DispatchRequest) (DispatchResponse, error)
	BatchGetJobStatus(ctx context.Context, reqs []BatchStatusRequest) (map[string]engine.JobStatus, error)
	GetJobFiles(ctx context.Context, engineName, jobID, engineJobID string) ([]engine.FileInfo, error)
	CancelJob(ctx context.Context, engineName, jobID, engineJobID string) error
	RemoveJob(ctx context.Context, engineName, jobID, engineJobID string) error
	Healthy() bool
}

// BatchStatusRequest identifies a single job for batch status fetching.
type BatchStatusRequest struct {
	JobID       string
	Engine      string
	EngineJobID string
}

type DispatchRequest struct {
	JobID      string
	Engine     string
	URL        string
	CacheKey   string
	StorageKey string
	Options    map[string]string
}

type DispatchResponse struct {
	Accepted    bool
	EngineJobID string
	Error       string
}
