package scheduler

import "context"

// LoadBalancer determines which node should handle a new download job.
type LoadBalancer interface {
	Name() string
	SelectNode(ctx context.Context, candidates []NodeInfo) (NodeSelection, error)
}

type NodeInfo struct {
	ID            string
	Endpoint      string
	IsLocal       bool
	Engines       []string
	DiskAvailable int64
	ActiveJobs    int
	MaxConcurrent int
}

type NodeSelection struct {
	NodeID   string
	Endpoint string
	Reason   string
}
