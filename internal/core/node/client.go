package node

import (
	"context"
	"time"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
)

// HeartbeatInterval is the shared interval for both controller self-heartbeat
// and worker heartbeat pings.
const HeartbeatInterval = 30 * time.Second

// JobRef identifies a job across the system. Different operations use different
// fields: CancelJob uses JobID (for tracker), GetJobFiles/RemoveJob use StorageKey
// (for disk paths). EngineJobID is always the engine's internal reference.
type JobRef struct {
	Engine      string
	JobID       string
	StorageKey  string
	EngineJobID string
}

// NodeClient abstracts communication with a node (local or remote).
type NodeClient interface {
	NodeID() string
	DispatchJob(ctx context.Context, req DispatchRequest) (DispatchResponse, error)
	GetJobFiles(ctx context.Context, ref JobRef) ([]engine.FileInfo, error)
	CancelJob(ctx context.Context, ref JobRef) error
	RemoveJob(ctx context.Context, ref JobRef) error
}

type DispatchRequest struct {
	JobID      string
	Engine     string
	URL        string
	StorageKey string
	Options    map[string]string
}

type DispatchResponse struct {
	Accepted     bool
	EngineJobID  string
	FileLocation string
	Error        string
}
