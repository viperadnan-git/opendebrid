package service

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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
	fullKey := cacheKeyPrefix(req.Engine, cacheKey) + cacheKey.Full()
	hasCacheKey := cacheKey.Value != ""

	// Compute storage key for deterministic directory
	storageKey := StorageKeyFromCacheKey(fullKey)

	if hasCacheKey {
		// 3. Same user already has a job with this cache key — just touch and return it
		existingJob, err := s.jobManager.FindJobByUserAndCacheKey(ctx, req.UserID, fullKey)
		if err == nil {
			jobID := uuidToStr(existingJob.ID)
			s.jobManager.TouchJob(ctx, jobID)
			log.Info().Str("cache_key", fullKey).Str("job_id", jobID).Msg("same user duplicate, returning existing job")
			return &AddDownloadResponse{
				JobID:    jobID,
				CacheHit: existingJob.Status == "completed",
				NodeID:   existingJob.NodeID,
				Status:   existingJob.Status,
			}, nil
		}

		// 4. Cache hit (completed by another user) — create completed job for this user
		cached, err := s.queries.FindCompletedJobByCacheKey(ctx, fullKey)
		if err == nil {
			initialName := defaultNameFromURL(req.URL)
			dbJob, err := s.jobManager.Create(ctx, req.UserID, cached.NodeID, req.Engine, "", req.URL, fullKey, initialName)
			if err != nil {
				return nil, fmt.Errorf("create cache-hit job: %w", err)
			}
			jobID := uuidToStr(dbJob.ID)
			s.jobManager.Complete(ctx, jobID, cached.FileLocation.String)
			log.Info().Str("cache_key", fullKey).Str("job_id", jobID).Msg("cache hit, created completed job for user")
			return &AddDownloadResponse{
				JobID:    jobID,
				CacheHit: true,
				NodeID:   cached.NodeID,
				Status:   "completed",
			}, nil
		}

		// 5. In-progress dedup (another user's active job) — piggyback
		sourceJob, err := s.jobManager.FindActiveSourceJob(ctx, fullKey)
		if err == nil {
			initialName := defaultNameFromURL(req.URL)
			engineJobID := sourceJob.EngineJobID.String
			dbJob, err := s.jobManager.Create(ctx, req.UserID, sourceJob.NodeID, req.Engine, engineJobID, req.URL, fullKey, initialName)
			if err != nil {
				return nil, fmt.Errorf("create piggyback job: %w", err)
			}
			jobID := uuidToStr(dbJob.ID)
			s.jobManager.UpdateStatus(ctx, jobID, "active", "", engineJobID, "", "")
			log.Info().Str("cache_key", fullKey).Str("job_id", jobID).Str("source_job", uuidToStr(sourceJob.ID)).Msg("in-progress dedup, piggybacking")
			return &AddDownloadResponse{
				JobID:  jobID,
				NodeID: sourceJob.NodeID,
				Status: "active",
			}, nil
		}
	}

	// 5. No match — select node and dispatch new download
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

	// 6. Create job record (default name from URL)
	initialName := defaultNameFromURL(req.URL)
	dbJob, err := s.jobManager.Create(ctx, req.UserID, selectedClient.NodeID(), req.Engine, "", req.URL, fullKey, initialName)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}

	jobID := uuidToStr(dbJob.ID)

	log.Debug().Str("job_id", jobID).Str("node", selectedClient.NodeID()).Str("cache_key", fullKey).Msg("dispatching job to node")

	// 7. Dispatch to node with storage key
	dispResp, err := selectedClient.DispatchJob(ctx, node.DispatchRequest{
		JobID:      jobID,
		Engine:     req.Engine,
		URL:        req.URL,
		CacheKey:   fullKey,
		StorageKey: storageKey,
		Options:    req.Options,
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

	// 8. Update job with engine job ID and set active
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

	sk := effectiveStorageKey(dbJob)
	files, err := client.GetJobFiles(ctx, dbJob.Engine, sk, dbJob.EngineJobID.String)
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

	sk := effectiveStorageKey(dbJob)
	files, err := client.GetJobFiles(ctx, dbJob.Engine, sk, dbJob.EngineJobID.String)
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

	// Only cancel engine download if no sibling jobs share it
	if dbJob.CacheKey != "" {
		siblings, _ := s.jobManager.CountSiblingJobs(ctx, dbJob.CacheKey, jobID)
		if siblings > 0 {
			return s.jobManager.UpdateStatus(ctx, jobID, "cancelled", "", "", "", "")
		}
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

	// Check if sibling jobs share the same cache key
	hasSiblings := false
	if dbJob.CacheKey != "" {
		siblings, _ := s.jobManager.CountSiblingJobs(ctx, dbJob.CacheKey, jobID)
		hasSiblings = siblings > 0
	}

	if !hasSiblings {
		sk := effectiveStorageKey(dbJob)
		if client, ok := s.nodeClients[dbJob.NodeID]; ok {
			// Cancel if still active
			if dbJob.EngineJobID.Valid && (dbJob.Status == "active" || dbJob.Status == "queued") {
				client.CancelJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String)
			}
			// Remove engine state + files
			client.RemoveJob(ctx, dbJob.Engine, sk, dbJob.EngineJobID.String)
		}
	}

	return s.jobManager.Delete(ctx, jobID)
}

// DeleteJobByID deletes a single job with file cleanup, without user ownership check.
func (s *DownloadService) DeleteJobByID(ctx context.Context, jobID string) error {
	dbJob, err := s.jobManager.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("job not found: %w", err)
	}

	hasSiblings := false
	if dbJob.CacheKey != "" {
		siblings, _ := s.jobManager.CountSiblingJobs(ctx, dbJob.CacheKey, jobID)
		hasSiblings = siblings > 0
	}

	if !hasSiblings {
		sk := effectiveStorageKey(dbJob)
		if client, ok := s.nodeClients[dbJob.NodeID]; ok {
			if dbJob.EngineJobID.Valid && (dbJob.Status == "active" || dbJob.Status == "queued") {
				client.CancelJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String)
			}
			client.RemoveJob(ctx, dbJob.Engine, sk, dbJob.EngineJobID.String)
		}
	}

	return s.jobManager.Delete(ctx, jobID)
}

// DeleteUser cleans up all jobs (cancel active, remove files) then deletes the user.
func (s *DownloadService) DeleteUser(ctx context.Context, userID string) error {
	jobs, err := s.queries.ListAllJobsByUser(ctx, strToUUID(userID))
	if err != nil {
		return fmt.Errorf("list user jobs: %w", err)
	}

	for _, j := range jobs {
		jobID := uuidToStr(j.ID)
		if err := s.DeleteJobByID(ctx, jobID); err != nil {
			log.Warn().Err(err).Str("job_id", jobID).Msg("failed to clean up job during user deletion")
		}
	}

	return s.queries.DeleteUser(ctx, strToUUID(userID))
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
			sk := effectiveStorageKey(dbJob)
			if client, ok := s.nodeClients[dbJob.NodeID]; ok {
				if files, err := client.GetJobFiles(ctx, dbJob.Engine, sk, status.EngineJobID); err == nil && len(files) > 0 {
					fileLocation = resolveFileLocation(files)
				}
			}
			s.jobManager.UpdateStatus(ctx, jobID, "completed", status.EngineState, newGID, "", fileLocation)
			s.jobManager.Complete(ctx, jobID, fileLocation)
			// Complete all sibling jobs sharing the same cache key
			if dbJob.CacheKey != "" {
				s.queries.CompleteSiblingJobs(ctx, gen.CompleteSiblingJobsParams{
					CacheKey:     dbJob.CacheKey,
					ID:           dbJob.ID,
					FileLocation: pgtype.Text{String: fileLocation, Valid: fileLocation != ""},
				})
			}
		}
	case engine.StateFailed:
		if dbJob.Status != "failed" {
			s.jobManager.UpdateStatus(ctx, jobID, "failed", status.EngineState, newGID, status.Error, "")
			// Fail all sibling jobs sharing the same cache key
			if dbJob.CacheKey != "" {
				s.queries.FailSiblingJobs(ctx, gen.FailSiblingJobsParams{
					CacheKey:     dbJob.CacheKey,
					ID:           dbJob.ID,
					ErrorMessage: pgtype.Text{String: status.Error, Valid: status.Error != ""},
				})
			}
			// Always remove download directory on failure
			sk := effectiveStorageKey(dbJob)
			if client, ok := s.nodeClients[dbJob.NodeID]; ok {
				client.RemoveJob(ctx, dbJob.Engine, sk, status.EngineJobID)
			}
		}
	default:
		if newGID != "" || dbJob.Status != "active" {
			s.jobManager.UpdateStatus(ctx, jobID, "active", status.EngineState, newGID, "", "")
		}
		// Propagate GID changes to sibling jobs
		if newGID != "" && dbJob.CacheKey != "" {
			s.queries.UpdateSiblingsEngineJobID(ctx, gen.UpdateSiblingsEngineJobIDParams{
				CacheKey:    dbJob.CacheKey,
				EngineJobID: pgtype.Text{String: status.EngineJobID, Valid: true},
				NodeID:      dbJob.NodeID,
			})
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

			// Sync to DB: handles completion, failure, siblings, directory cleanup
			job := ref.job
			s.syncStatusToDB(ctx, &job, &status)

			// Publish events for notifications / external subscribers
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
