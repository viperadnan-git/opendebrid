package node

import (
	"context"
	"fmt"

	"github.com/opendebrid/opendebrid/internal/core/engine"
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

func (c *LocalNodeClient) GetJobStatus(ctx context.Context, _ string, engineJobID string) (engine.JobStatus, error) {
	// We need to find the right engine - for now iterate
	for _, name := range c.registry.List() {
		eng, _ := c.registry.Get(name)
		status, err := eng.Status(ctx, engineJobID)
		if err == nil {
			return status, nil
		}
	}
	return engine.JobStatus{}, fmt.Errorf("job not found: %s", engineJobID)
}

func (c *LocalNodeClient) GetJobFiles(ctx context.Context, _ string, engineJobID string) ([]engine.FileInfo, error) {
	for _, name := range c.registry.List() {
		eng, _ := c.registry.Get(name)
		files, err := eng.ListFiles(ctx, engineJobID)
		if err == nil {
			return files, nil
		}
	}
	return nil, fmt.Errorf("job not found: %s", engineJobID)
}

func (c *LocalNodeClient) CancelJob(ctx context.Context, _ string, engineJobID string) error {
	for _, name := range c.registry.List() {
		eng, _ := c.registry.Get(name)
		if err := eng.Cancel(ctx, engineJobID); err == nil {
			return nil
		}
	}
	return fmt.Errorf("job not found: %s", engineJobID)
}

func (c *LocalNodeClient) Healthy() bool { return true }
