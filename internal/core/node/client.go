package node

import (
	"context"

	"github.com/opendebrid/opendebrid/internal/core/engine"
)

// NodeClient abstracts communication with a node (local or remote).
type NodeClient interface {
	NodeID() string
	DispatchJob(ctx context.Context, req DispatchRequest) (DispatchResponse, error)
	GetJobStatus(ctx context.Context, jobID string, engineJobID string) (engine.JobStatus, error)
	GetJobFiles(ctx context.Context, jobID string, engineJobID string) ([]engine.FileInfo, error)
	CancelJob(ctx context.Context, jobID string, engineJobID string) error
	Healthy() bool
}

type DispatchRequest struct {
	JobID    string
	Engine   string
	URL      string
	CacheKey string
	Options  map[string]string
}

type DispatchResponse struct {
	Accepted    bool
	EngineJobID string
	Error       string
}
