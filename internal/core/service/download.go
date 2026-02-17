package service

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/core/job"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
	"github.com/rs/zerolog/log"
)

// NodeSelector picks a node for a given engine and optional preferences.
type NodeSelector interface {
	SelectNode(ctx context.Context, req NodeSelectRequest) (NodeSelection, error)
}

type NodeSelectRequest struct {
	Engine        string
	EstimatedSize int64
	PreferredNode string
}

type NodeSelection struct {
	NodeID   string
	Endpoint string
}

type DownloadService struct {
	registry    *engine.Registry
	nodeClients map[string]node.NodeClient
	scheduler   NodeSelector
	jobManager  *job.Manager
	queries     *gen.Queries
	bus         event.Bus
}

func NewDownloadService(
	registry *engine.Registry,
	nodeClients map[string]node.NodeClient,
	scheduler NodeSelector,
	jobManager *job.Manager,
	db *pgxpool.Pool,
	bus event.Bus,
) *DownloadService {
	return &DownloadService{
		registry:    registry,
		nodeClients: nodeClients,
		scheduler:   scheduler,
		jobManager:  jobManager,
		queries:     gen.New(db),
		bus:         bus,
	}
}

type AddDownloadRequest struct {
	URL        string
	Engine     string
	UserID     string
	Options    map[string]string
	PreferNode string
}

type AddDownloadResponse struct {
	JobID    string
	CacheHit bool
	NodeID   string
	Status   string
}

func (s *DownloadService) Add(ctx context.Context, req AddDownloadRequest) (*AddDownloadResponse, error) {
	log.Debug().Str("engine", req.Engine).Str("url", req.URL).Str("user", req.UserID).Msg("add download request")

	// 1. Validate engine
	eng, err := s.registry.Get(req.Engine)
	if err != nil {
		return nil, fmt.Errorf("unknown engine %q: %w", req.Engine, err)
	}

	// 2. Resolve cache key
	cacheKey, _ := eng.ResolveCacheKey(ctx, req.URL)
	fullKey := req.Engine + ":" + cacheKey.Full()

	// 3. Check cache
	if fullKey != req.Engine+":" {
		cached, err := s.queries.LookupCache(ctx, fullKey)
		if err == nil {
			// Cache hit - touch and return existing job
			s.queries.TouchCache(ctx, fullKey)
			s.bus.Publish(ctx, event.Event{
				Type: event.EventCacheHit,
				Payload: event.CacheEvent{
					CacheKey: fullKey,
					JobID:    uuidToStr(cached.JobID),
					NodeID:   cached.NodeID,
				},
			})
			log.Info().Str("cache_key", fullKey).Msg("cache hit")
			return &AddDownloadResponse{
				JobID:    uuidToStr(cached.JobID),
				CacheHit: true,
				NodeID:   cached.NodeID,
				Status:   "completed",
			}, nil
		}
	}

	// 4. Select node via scheduler
	selection, err := s.scheduler.SelectNode(ctx, NodeSelectRequest{
		Engine:        req.Engine,
		PreferredNode: req.PreferNode,
	})
	if err != nil {
		return nil, fmt.Errorf("select node: %w", err)
	}

	selectedClient, ok := s.nodeClients[selection.NodeID]
	if !ok {
		return nil, fmt.Errorf("node %q selected but not connected", selection.NodeID)
	}

	// 5. Create job record
	dbJob, err := s.jobManager.Create(ctx, req.UserID, selectedClient.NodeID(), req.Engine, "", req.URL, fullKey)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}

	jobID := uuidToStr(dbJob.ID)

	log.Debug().Str("job_id", jobID).Str("node", selectedClient.NodeID()).Str("cache_key", fullKey).Msg("dispatching job to node")

	// 6. Dispatch to node
	dispResp, err := selectedClient.DispatchJob(ctx, node.DispatchRequest{
		JobID:    jobID,
		Engine:   req.Engine,
		URL:      req.URL,
		CacheKey: fullKey,
		Options:  req.Options,
	})
	if err != nil {
		s.jobManager.UpdateStatus(ctx, jobID, "failed", "", "", err.Error(), "")
		return nil, fmt.Errorf("dispatch job: %w", err)
	}
	if !dispResp.Accepted {
		errMsg := dispResp.Error
		if errMsg == "" {
			errMsg = "node rejected job"
		}
		s.jobManager.UpdateStatus(ctx, jobID, "failed", "", "", errMsg, "")
		return nil, fmt.Errorf("dispatch job: %s", errMsg)
	}

	// 7. Update job with engine job ID and set active
	s.jobManager.UpdateStatus(ctx, jobID, "active", "", dispResp.EngineJobID, "", "")
	log.Debug().Str("job_id", jobID).Str("engine_job_id", dispResp.EngineJobID).Msg("job dispatched successfully")

	return &AddDownloadResponse{
		JobID:  jobID,
		NodeID: selectedClient.NodeID(),
		Status: "active",
	}, nil
}

func (s *DownloadService) Status(ctx context.Context, jobID, userID string) (*engine.JobStatus, error) {
	dbJob, err := s.jobManager.GetJobForUser(ctx, jobID, userID)
	if err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}
	return s.statusForJob(ctx, dbJob)
}

// StatusByID fetches live status for a job without user filtering (for internal/admin use).
func (s *DownloadService) StatusByID(ctx context.Context, jobID string) (*engine.JobStatus, error) {
	dbJob, err := s.jobManager.GetJob(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}
	return s.statusForJob(ctx, dbJob)
}

func (s *DownloadService) statusForJob(ctx context.Context, dbJob *gen.Job) (*engine.JobStatus, error) {
	jobID := uuidToStr(dbJob.ID)

	client, ok := s.nodeClients[dbJob.NodeID]
	if !ok {
		return nil, fmt.Errorf("node %q not available", dbJob.NodeID)
	}

	log.Debug().Str("job_id", jobID).Str("engine_job_id", dbJob.EngineJobID.String).Str("node", dbJob.NodeID).Msg("fetching job status from node")

	status, err := client.GetJobStatus(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String)
	if err != nil {
		return nil, err
	}

	log.Debug().Str("job_id", jobID).Str("state", string(status.State)).Float64("progress", status.Progress).Int64("speed", status.Speed).Str("engine_job_id", status.EngineJobID).Msg("got job status")

	// Sync state changes back to the DB (GID changes, completion, failure).
	newGID := ""
	if status.EngineJobID != dbJob.EngineJobID.String {
		newGID = status.EngineJobID
		log.Debug().Str("job_id", jobID).Str("old_gid", dbJob.EngineJobID.String).Str("new_gid", newGID).Msg("GID changed")
	}
	switch status.State {
	case engine.StateCompleted:
		if dbJob.Status != "completed" {
			// Resolve file location from engine
			fileLocation := ""
			engineJobID := status.EngineJobID
			if files, err := client.GetJobFiles(ctx, dbJob.Engine, jobID, engineJobID); err == nil && len(files) > 0 {
				fileLocation = resolveFileLocation(files)
			}
			s.jobManager.UpdateStatus(ctx, jobID, "completed", status.EngineState, newGID, "", fileLocation)
			s.jobManager.Complete(ctx, jobID, fileLocation)
		}
	case engine.StateFailed:
		if dbJob.Status != "failed" {
			s.jobManager.UpdateStatus(ctx, jobID, "failed", status.EngineState, newGID, status.Error, "")
		}
	default:
		if newGID != "" || dbJob.Status != "active" {
			s.jobManager.UpdateStatus(ctx, jobID, "active", status.EngineState, newGID, "", "")
		}
	}

	return &status, nil
}

// ListFilesResult contains the files and the job status.
type ListFilesResult struct {
	Files  []engine.FileInfo
	Status string
}

func (s *DownloadService) ListFiles(ctx context.Context, jobID, userID string) (*ListFilesResult, error) {
	dbJob, err := s.jobManager.GetJobForUser(ctx, jobID, userID)
	if err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}

	client, ok := s.nodeClients[dbJob.NodeID]
	if !ok {
		return nil, fmt.Errorf("node %q not available", dbJob.NodeID)
	}

	files, err := client.GetJobFiles(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String)
	if err != nil {
		return nil, err
	}

	return &ListFilesResult{Files: files, Status: dbJob.Status}, nil
}

// ListFilesByID fetches files for a job without user filtering (for internal/web use).
func (s *DownloadService) ListFilesByID(ctx context.Context, jobID string) (*ListFilesResult, error) {
	dbJob, err := s.jobManager.GetJob(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}

	client, ok := s.nodeClients[dbJob.NodeID]
	if !ok {
		return nil, fmt.Errorf("node %q not available", dbJob.NodeID)
	}

	files, err := client.GetJobFiles(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String)
	if err != nil {
		return nil, err
	}

	return &ListFilesResult{Files: files, Status: dbJob.Status}, nil
}

func (s *DownloadService) Cancel(ctx context.Context, jobID, userID string) error {
	dbJob, err := s.jobManager.GetJobForUser(ctx, jobID, userID)
	if err != nil {
		return fmt.Errorf("job not found: %w", err)
	}

	client, ok := s.nodeClients[dbJob.NodeID]
	if !ok {
		return fmt.Errorf("node %q not available", dbJob.NodeID)
	}

	if err := client.CancelJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String); err != nil {
		return err
	}

	return s.jobManager.UpdateStatus(ctx, jobID, "cancelled", "", "", "", "")
}

func (s *DownloadService) Delete(ctx context.Context, jobID, userID string) error {
	dbJob, err := s.jobManager.GetJobForUser(ctx, jobID, userID)
	if err != nil {
		return fmt.Errorf("job not found: %w", err)
	}

	if client, ok := s.nodeClients[dbJob.NodeID]; ok {
		// Cancel if still active
		if dbJob.EngineJobID.Valid && (dbJob.Status == "active" || dbJob.Status == "queued") {
			client.CancelJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String)
		}
		// Remove engine state + files (uses jobID for directory cleanup even if engineJobID is unset)
		client.RemoveJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String)
	}

	// Delete cache entry and job record from DB
	if dbJob.CacheKey != "" {
		s.queries.DeleteCacheEntry(ctx, dbJob.CacheKey)
	}
	return s.jobManager.Delete(ctx, jobID)
}

func (s *DownloadService) ListByUser(ctx context.Context, userID string, limit, offset int32) ([]gen.Job, error) {
	return s.jobManager.ListByUser(ctx, userID, limit, offset)
}

func (s *DownloadService) ListByUserAndEngine(ctx context.Context, userID, engineName string, limit, offset int32) ([]gen.Job, error) {
	return s.jobManager.ListByUserAndEngine(ctx, userID, engineName, limit, offset)
}

// resolveFileLocation determines the StorageURI for a completed job's files.
// Single file: returns its StorageURI directly.
// Multiple files: returns the common parent directory as file:// URI.
func resolveFileLocation(files []engine.FileInfo) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		return files[0].StorageURI
	}
	// Multiple files: find common directory from StorageURIs
	first := files[0].StorageURI
	prefix := first
	for _, f := range files[1:] {
		for !strings.HasPrefix(f.StorageURI, prefix) {
			prefix = prefix[:strings.LastIndex(prefix, "/")]
		}
	}
	// Ensure it ends with / for directory
	if !strings.HasSuffix(prefix, "/") {
		prefix = path.Dir(strings.TrimPrefix(prefix, "file://"))
		return "file://" + prefix
	}
	return prefix
}
