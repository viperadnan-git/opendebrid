package node

import (
	"context"
	"fmt"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
)

// LocalNodeClient dispatches jobs directly to local engines.
type LocalNodeClient struct {
	nodeID   string
	registry *engine.Registry
}

func NewLocalNodeClient(nodeID string, registry *engine.Registry) *LocalNodeClient {
	return &LocalNodeClient{
		nodeID:   nodeID,
		registry: registry,
	}
}

func (c *LocalNodeClient) NodeID() string { return c.nodeID }

func (c *LocalNodeClient) DispatchJob(ctx context.Context, req DispatchRequest) (DispatchResponse, error) {
	eng, err := c.registry.Get(req.Engine)
	if err != nil {
		return DispatchResponse{Error: err.Error()}, err
	}

	resp, err := eng.Add(ctx, engine.AddRequest{
		JobID:   req.JobID,
		URL:     req.URL,
		Options: req.Options,
	})
	if err != nil {
		return DispatchResponse{Error: err.Error()}, fmt.Errorf("engine add: %w", err)
	}

	return DispatchResponse{
		Accepted:    true,
		EngineJobID: resp.EngineJobID,
	}, nil
}

func (c *LocalNodeClient) BatchGetJobStatus(ctx context.Context, reqs []BatchStatusRequest) (map[string]engine.JobStatus, error) {
	// Group requests by engine
	type engineGroup struct {
		engineJobIDs []string
		jobIDs       []string // parallel to engineJobIDs
	}
	groups := make(map[string]*engineGroup)
	for _, r := range reqs {
		g, ok := groups[r.Engine]
		if !ok {
			g = &engineGroup{}
			groups[r.Engine] = g
		}
		g.engineJobIDs = append(g.engineJobIDs, r.EngineJobID)
		g.jobIDs = append(g.jobIDs, r.JobID)
	}

	result := make(map[string]engine.JobStatus, len(reqs))
	for engineName, g := range groups {
		eng, err := c.registry.Get(engineName)
		if err != nil {
			continue
		}
		statuses, err := eng.BatchStatus(ctx, g.engineJobIDs)
		if err != nil {
			continue
		}
		// Map engine job ID results back to job IDs
		for i, engineJobID := range g.engineJobIDs {
			if s, ok := statuses[engineJobID]; ok {
				result[g.jobIDs[i]] = s
			}
		}
	}
	return result, nil
}

func (c *LocalNodeClient) GetJobFiles(ctx context.Context, engineName, jobID string, engineJobID string) ([]engine.FileInfo, error) {
	eng, err := c.registry.Get(engineName)
	if err != nil {
		return nil, err
	}
	return eng.ListFiles(ctx, jobID, engineJobID)
}

func (c *LocalNodeClient) CancelJob(ctx context.Context, engineName, _ string, engineJobID string) error {
	eng, err := c.registry.Get(engineName)
	if err != nil {
		return err
	}
	return eng.Cancel(ctx, engineJobID)
}

func (c *LocalNodeClient) RemoveJob(ctx context.Context, engineName, jobID string, engineJobID string) error {
	eng, err := c.registry.Get(engineName)
	if err != nil {
		return err
	}
	return eng.Remove(ctx, jobID, engineJobID)
}

func (c *LocalNodeClient) Healthy() bool { return true }
