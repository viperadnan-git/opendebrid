package service

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/opendebrid/opendebrid/internal/core/event"
	"github.com/opendebrid/opendebrid/internal/core/job"
	"github.com/opendebrid/opendebrid/internal/core/node"
	"github.com/opendebrid/opendebrid/internal/database/gen"
	"github.com/rs/zerolog/log"
)

type DownloadService struct {
	registry    *engine.Registry
	nodeClients map[string]node.NodeClient
	jobManager  *job.Manager
	queries     *gen.Queries
	bus         event.Bus
}

func NewDownloadService(
	registry *engine.Registry,
	nodeClients map[string]node.NodeClient,
	jobManager *job.Manager,
	db *pgxpool.Pool,
	bus event.Bus,
) *DownloadService {
	return &DownloadService{
		registry:    registry,
		nodeClients: nodeClients,
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

	// 4. Select node (for now, use first available)
	var selectedClient node.NodeClient
	if req.PreferNode != "" {
		if c, ok := s.nodeClients[req.PreferNode]; ok {
			selectedClient = c
		}
	}
	if selectedClient == nil {
		for _, c := range s.nodeClients {
			if c.Healthy() {
				selectedClient = c
				break
			}
		}
	}
	if selectedClient == nil {
		return nil, fmt.Errorf("no healthy nodes available")
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

	status, err := client.GetJobStatus(ctx, jobID, dbJob.EngineJobID.String)
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
			if files, err := client.GetJobFiles(ctx, jobID, engineJobID); err == nil && len(files) > 0 {
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

func (s *DownloadService) ListFiles(ctx context.Context, jobID, userID string) ([]engine.FileInfo, error) {
	dbJob, err := s.jobManager.GetJobForUser(ctx, jobID, userID)
	if err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}

	client, ok := s.nodeClients[dbJob.NodeID]
	if !ok {
		return nil, fmt.Errorf("node %q not available", dbJob.NodeID)
	}

	return client.GetJobFiles(ctx, jobID, dbJob.EngineJobID.String)
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

	if err := client.CancelJob(ctx, jobID, dbJob.EngineJobID.String); err != nil {
		return err
	}

	return s.jobManager.UpdateStatus(ctx, jobID, "cancelled", "", "", "", "")
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
