package node

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/statusloop"
)

// NodeServer is the canonical engine-execution layer shared by both the controller's
// local node and the worker. It holds an engine registry, a status tracker, and
// knows the base download directory for computing relative file paths.
//
// NodeServer implements NodeClient for the local case (controller running engines
// directly). RemoteNodeClient is the counterpart for remote workers.
type NodeServer struct {
	nodeID      string
	registry    *engine.Registry
	tracker     *statusloop.Tracker
	downloadDir string
}

func NewNodeServer(nodeID string, registry *engine.Registry, tracker *statusloop.Tracker, downloadDir string) *NodeServer {
	return &NodeServer{
		nodeID:      nodeID,
		registry:    registry,
		tracker:     tracker,
		downloadDir: strings.TrimRight(downloadDir, "/"),
	}
}

func (s *NodeServer) NodeID() string { return s.nodeID }

func (s *NodeServer) DispatchJob(ctx context.Context, req DispatchRequest) (DispatchResponse, error) {
	eng, err := s.registry.Get(req.Engine)
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

	s.tracker.Add(req.JobID, req.Engine, resp.EngineJobID)

	absPath := filepath.Join(eng.DownloadDir(), req.StorageKey)
	relPath := strings.TrimPrefix(absPath, s.downloadDir+"/")
	log.Info().Str("job_id", req.JobID).Str("engine", req.Engine).Msg("job dispatched")
	return DispatchResponse{
		Accepted:     true,
		EngineJobID:  resp.EngineJobID,
		FileLocation: "file://" + relPath,
	}, nil
}

func (s *NodeServer) GetJobFiles(ctx context.Context, ref JobRef) ([]engine.FileInfo, error) {
	eng, err := s.registry.Get(ref.Engine)
	if err != nil {
		return nil, err
	}
	return eng.ListFiles(ctx, ref.StorageKey, ref.EngineJobID)
}

func (s *NodeServer) CancelJob(ctx context.Context, ref JobRef) error {
	eng, err := s.registry.Get(ref.Engine)
	if err != nil {
		return err
	}
	if err := eng.Cancel(ctx, ref.EngineJobID); err != nil {
		return err
	}
	s.tracker.Remove(ref.JobID)
	return nil
}

// RemoveJob removes engine files for a storage key.
// Tracker cleanup is not needed here — active jobs are cleaned up by CancelJob,
// and terminal jobs are cleaned up by the status loop's poll function.
func (s *NodeServer) RemoveJob(ctx context.Context, ref JobRef) error {
	eng, err := s.registry.Get(ref.Engine)
	if err != nil {
		return err
	}
	return eng.Remove(ctx, ref.StorageKey, ref.EngineJobID)
}

func (s *NodeServer) ResolveCacheKey(ctx context.Context, engineName, url string) (engine.CacheKey, error) {
	eng, err := s.registry.Get(engineName)
	if err != nil {
		return engine.CacheKey{}, err
	}
	return eng.ResolveCacheKey(ctx, url)
}
