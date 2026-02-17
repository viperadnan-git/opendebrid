package service

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

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

	// 5. Create job record (default name from URL)
	initialName := defaultNameFromURL(req.URL)
	dbJob, err := s.jobManager.Create(ctx, req.UserID, selectedClient.NodeID(), req.Engine, "", req.URL, fullKey, initialName)
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

	statuses, err := client.BatchGetJobStatus(ctx, []node.BatchStatusRequest{{
		JobID:       jobID,
		Engine:      dbJob.Engine,
		EngineJobID: dbJob.EngineJobID.String,
	}})
	if err != nil {
		return nil, err
	}

	status, ok := statuses[jobID]
	if !ok {
		return nil, fmt.Errorf("no status returned for job %q", jobID)
	}

	log.Debug().Str("job_id", jobID).Str("state", string(status.State)).Float64("progress", status.Progress).Int64("speed", status.Speed).Str("engine_job_id", status.EngineJobID).Msg("got job status")

	s.syncStatusToDB(ctx, dbJob, &status)

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

// JobWithStatus combines a DB job record with optional live status data.
type JobWithStatus struct {
	Job    gen.Job
	Status *engine.JobStatus // nil for terminal states (completed/failed/cancelled)
}

// ListByUserWithStatus returns jobs enriched with live progress for active/queued jobs.
// Groups batch status calls by node for efficiency: 1 call per node.
func (s *DownloadService) ListByUserWithStatus(ctx context.Context, userID string, limit, offset int32) ([]JobWithStatus, error) {
	jobs, err := s.jobManager.ListByUser(ctx, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	return s.enrichJobsWithStatus(ctx, jobs)
}

// ListByUserAndEngineWithStatus returns engine-filtered jobs enriched with live progress.
func (s *DownloadService) ListByUserAndEngineWithStatus(ctx context.Context, userID, engineName string, limit, offset int32) ([]JobWithStatus, error) {
	jobs, err := s.jobManager.ListByUserAndEngine(ctx, userID, engineName, limit, offset)
	if err != nil {
		return nil, err
	}
	return s.enrichJobsWithStatus(ctx, jobs)
}

func (s *DownloadService) enrichJobsWithStatus(ctx context.Context, jobs []gen.Job) ([]JobWithStatus, error) {
	result := make([]JobWithStatus, len(jobs))
	for i := range jobs {
		result[i] = JobWithStatus{Job: jobs[i]}
	}

	// Group active/queued jobs by nodeID
	type jobRef struct {
		index       int
		jobID       string
		engine      string
		engineJobID string
	}
	nodeGroups := make(map[string][]jobRef)
	for i, j := range jobs {
		if j.Status != "active" && j.Status != "queued" {
			continue
		}
		if !j.EngineJobID.Valid || j.EngineJobID.String == "" {
			continue
		}
		nodeGroups[j.NodeID] = append(nodeGroups[j.NodeID], jobRef{
			index:       i,
			jobID:       uuidToStr(j.ID),
			engine:      j.Engine,
			engineJobID: j.EngineJobID.String,
		})
	}

	// One batch call per node
	for nodeID, refs := range nodeGroups {
		client, ok := s.nodeClients[nodeID]
		if !ok {
			continue
		}

		reqs := make([]node.BatchStatusRequest, len(refs))
		for i, r := range refs {
			reqs[i] = node.BatchStatusRequest{
				JobID:       r.jobID,
				Engine:      r.engine,
				EngineJobID: r.engineJobID,
			}
		}

		statuses, err := client.BatchGetJobStatus(ctx, reqs)
		if err != nil {
			log.Warn().Err(err).Str("node", nodeID).Msg("batch status failed, skipping")
			continue
		}

		for _, ref := range refs {
			if status, ok := statuses[ref.jobID]; ok {
				statusCopy := status
				result[ref.index].Status = &statusCopy
				s.syncStatusToDB(ctx, &result[ref.index].Job, &statusCopy)
			}
		}
	}

	return result, nil
}

// syncStatusToDB persists state changes (GID updates, completion, failure) from live status to DB.
func (s *DownloadService) syncStatusToDB(ctx context.Context, dbJob *gen.Job, status *engine.JobStatus) {
	jobID := uuidToStr(dbJob.ID)

	newGID := ""
	if status.EngineJobID != dbJob.EngineJobID.String {
		newGID = status.EngineJobID
	}

	// Update name/size if engine resolved them
	if status.Name != "" || status.TotalSize > 0 {
		s.jobManager.UpdateMeta(ctx, jobID, status.Name, status.TotalSize)
	}

	switch status.State {
	case engine.StateCompleted:
		if dbJob.Status != "completed" {
			fileLocation := ""
			if client, ok := s.nodeClients[dbJob.NodeID]; ok {
				if files, err := client.GetJobFiles(ctx, dbJob.Engine, jobID, status.EngineJobID); err == nil && len(files) > 0 {
					fileLocation = resolveFileLocation(files)
				}
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
}

// RunStatusWatcher periodically polls active jobs and syncs their status.
// Run as a goroutine: go svc.RunStatusWatcher(ctx, 5*time.Second)
func (s *DownloadService) RunStatusWatcher(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollActiveJobs(ctx)
		}
	}
}

func (s *DownloadService) pollActiveJobs(ctx context.Context) {
	jobs, err := s.queries.ListActiveJobs(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("watcher: failed to list active jobs")
		return
	}
	if len(jobs) == 0 {
		return
	}

	// Group by nodeID
	type jobRef struct {
		job         gen.Job
		jobID       string
		engine      string
		engineJobID string
	}
	nodeGroups := make(map[string][]jobRef)
	for _, j := range jobs {
		if !j.EngineJobID.Valid || j.EngineJobID.String == "" {
			continue
		}
		nodeGroups[j.NodeID] = append(nodeGroups[j.NodeID], jobRef{
			job:         j,
			jobID:       uuidToStr(j.ID),
			engine:      j.Engine,
			engineJobID: j.EngineJobID.String,
		})
	}

	// One batch call per node
	for nodeID, refs := range nodeGroups {
		client, ok := s.nodeClients[nodeID]
		if !ok {
			continue
		}

		reqs := make([]node.BatchStatusRequest, len(refs))
		for i, r := range refs {
			reqs[i] = node.BatchStatusRequest{
				JobID:       r.jobID,
				Engine:      r.engine,
				EngineJobID: r.engineJobID,
			}
		}

		statuses, err := client.BatchGetJobStatus(ctx, reqs)
		if err != nil {
			log.Warn().Err(err).Str("node", nodeID).Msg("watcher: batch status failed")
			continue
		}

		for _, ref := range refs {
			status, ok := statuses[ref.jobID]
			if !ok {
				continue
			}

			payload := event.JobEvent{
				JobID:       ref.jobID,
				UserID:      uuidToStr(ref.job.UserID),
				NodeID:      nodeID,
				Engine:      ref.engine,
				CacheKey:    ref.job.CacheKey,
				EngineJobID: status.EngineJobID,
				Name:        status.Name,
				Progress:    status.Progress,
				Speed:       status.Speed,
				Size:        status.TotalSize,
			}

			switch status.State {
			case engine.StateCompleted:
				if ref.job.Status != "completed" {
					s.bus.Publish(ctx, event.Event{Type: event.EventJobCompleted, Payload: payload})
				}
			case engine.StateFailed:
				if ref.job.Status != "failed" {
					payload.Error = status.Error
					s.bus.Publish(ctx, event.Event{Type: event.EventJobFailed, Payload: payload})
				}
			case engine.StateCancelled:
				if ref.job.Status != "cancelled" {
					s.bus.Publish(ctx, event.Event{Type: event.EventJobCancelled, Payload: payload})
				}
			case engine.StateActive:
				s.bus.Publish(ctx, event.Event{Type: event.EventJobProgress, Payload: payload})
			}
		}
	}
}

// defaultNameFromURL extracts a human-readable name from a URL.
// Magnets: dn= parameter or info hash. HTTP: filename from path or hostname.
func defaultNameFromURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "magnet:") {
		u, err := url.Parse(rawURL)
		if err == nil {
			if dn := u.Query().Get("dn"); dn != "" {
				return dn
			}
			xt := u.Query().Get("xt")
			if strings.HasPrefix(xt, "urn:btih:") {
				return strings.TrimPrefix(xt, "urn:btih:")
			}
		}
		return rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	base := filepath.Base(u.Path)
	if base != "" && base != "." && base != "/" {
		return base
	}
	return u.Host
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
