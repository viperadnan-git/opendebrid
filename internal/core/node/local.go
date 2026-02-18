package node

import (
	"context"
	"fmt"
	"path/filepath"

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
		JobID:      req.JobID,
		StorageKey: req.StorageKey,
		URL:        req.URL,
		Options:    req.Options,
	})
	if err != nil {
		return DispatchResponse{Error: err.Error()}, fmt.Errorf("engine add: %w", err)
	}

	return DispatchResponse{
		Accepted:     true,
		EngineJobID:  resp.EngineJobID,
		FileLocation: "file://" + filepath.Join(eng.DownloadDir(), req.StorageKey),
	}, nil
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
