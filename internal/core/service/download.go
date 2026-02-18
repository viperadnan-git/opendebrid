package service

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/core/job"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	"github.com/viperadnan-git/opendebrid/internal/core/statusloop"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
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
	registry     *engine.Registry
	nodeClients  *node.NodeClientStore
	scheduler    NodeSelector
	jobManager   *job.Manager
	queries      *gen.Queries
	bus          event.Bus
	pool         *pgxpool.Pool
	localTracker *statusloop.Tracker // tracks jobs dispatched to the local (controller) node
}

// computeETA calculates estimated time remaining from size, downloaded, and speed.
func computeETA(totalSize, downloadedSize, speed int64) time.Duration {
	if speed <= 0 || totalSize <= 0 {
		return 0
	}
	remaining := totalSize - downloadedSize
	if remaining <= 0 {
		return 0
	}
	return time.Duration(remaining/speed) * time.Second
}

func NewDownloadService(
	registry *engine.Registry,
	nodeClients *node.NodeClientStore,
	scheduler NodeSelector,
	jobManager *job.Manager,
	db *pgxpool.Pool,
	bus event.Bus,
	localTracker *statusloop.Tracker,
) *DownloadService {
	return &DownloadService{
		registry:     registry,
		nodeClients:  nodeClients,
		scheduler:    scheduler,
		jobManager:   jobManager,
		queries:      gen.New(db),
		bus:          bus,
		pool:         db,
		localTracker: localTracker,
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
	DownloadID string
	JobID      string
	CacheHit   bool
	NodeID     string
	Status     string
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

	var storageKey string

	if hasCacheKey {
		// 3. Check if a job already exists for this cache key
		existingJob, err := s.jobManager.GetJobByCacheKey(ctx, fullKey)
		if err == nil {
			jobID := uuidToStr(existingJob.ID)

			// Same-user dedup: check if this user already has a download for this job
			existingDL, err := s.jobManager.FindDownloadByUserAndJobID(ctx, req.UserID, jobID)
			if err == nil {
				dlID := uuidToStr(existingDL.ID)
				log.Info().Str("cache_key", fullKey).Str("download_id", dlID).Msg("same user duplicate, returning existing download")
				return &AddDownloadResponse{
					DownloadID: dlID,
					JobID:      jobID,
					CacheHit:   existingJob.Status == "completed",
					NodeID:     existingJob.NodeID,
					Status:     existingJob.Status,
				}, nil
			}

			// Different user or new download — create download linking to existing job
			if existingJob.Status == "completed" || existingJob.Status == "active" || existingJob.Status == "queued" {
				dl, err := s.jobManager.CreateDownload(ctx, req.UserID, jobID)
				if err != nil {
					return nil, fmt.Errorf("create download for existing job: %w", err)
				}
				dlID := uuidToStr(dl.ID)
				log.Info().Str("cache_key", fullKey).Str("download_id", dlID).Str("job_id", jobID).Msg("attached to existing job")
				return &AddDownloadResponse{
					DownloadID: dlID,
					JobID:      jobID,
					CacheHit:   existingJob.Status == "completed",
					NodeID:     existingJob.NodeID,
					Status:     existingJob.Status,
				}, nil
			}
			// Job exists but is failed/cancelled — reuse it below
		}
	}

	// 4. Select node and dispatch
	selection, err := s.scheduler.SelectNode(ctx, NodeSelectRequest{
		Engine:        req.Engine,
		PreferredNode: req.PreferNode,
	})
	if err != nil {
		return nil, fmt.Errorf("select node: %w", err)
	}

	selectedClient, ok := s.nodeClients.Get(selection.NodeID)
	if !ok {
		return nil, fmt.Errorf("node %q selected but not connected", selection.NodeID)
	}

	// 5. Create or reuse job + ensure download exists
	var jobID, dlID string

	// Try to reuse a failed/cancelled job with the same cache key
	if hasCacheKey {
		existingJob, err := s.jobManager.GetJobByCacheKey(ctx, fullKey)
		if err == nil && (existingJob.Status == "failed" || existingJob.Status == "cancelled") {
			resetJob, err := s.jobManager.ResetJob(ctx, uuidToStr(existingJob.ID), selectedClient.NodeID(), req.URL)
			if err == nil {
				jobID = uuidToStr(resetJob.ID)
				// Ensure this user has a download record for the reset job
				dl, err := s.jobManager.CreateDownload(ctx, req.UserID, jobID)
				if err != nil {
					// Download may already exist from a previous attempt
					existingDL, findErr := s.jobManager.FindDownloadByUserAndJobID(ctx, req.UserID, jobID)
					if findErr != nil {
						return nil, fmt.Errorf("create download for reset job: %w", err)
					}
					dlID = uuidToStr(existingDL.ID)
				} else {
					dlID = uuidToStr(dl.ID)
				}
				log.Debug().Str("job_id", jobID).Msg("reused failed/cancelled job")
			}
		}
	}

	// No reusable job — create a new one
	if jobID == "" {
		initialName := defaultNameFromURL(req.URL)
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		dbJob, err := s.jobManager.CreateJobTx(ctx, tx, selectedClient.NodeID(), req.Engine, "", req.URL, fullKey, initialName)
		if err != nil {
			return nil, fmt.Errorf("create job: %w", err)
		}
		jobID = uuidToStr(dbJob.ID)

		dl, err := s.jobManager.CreateDownloadTx(ctx, tx, req.UserID, jobID)
		if err != nil {
			return nil, fmt.Errorf("create download: %w", err)
		}
		dlID = uuidToStr(dl.ID)

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit tx: %w", err)
		}
	}

	log.Debug().Str("job_id", jobID).Str("download_id", dlID).Str("node", selectedClient.NodeID()).Str("cache_key", fullKey).Msg("dispatching job to node")

	// 6. Dispatch to node with storage key
	storageKey = StorageKeyFromCacheKey(fullKey)
	dispResp, err := selectedClient.DispatchJob(ctx, node.DispatchRequest{
		JobID:      jobID,
		Engine:     req.Engine,
		URL:        req.URL,
		CacheKey:   fullKey,
		StorageKey: storageKey,
		Options:    req.Options,
	})
	if err != nil {
		_ = s.jobManager.UpdateJobStatus(ctx, jobID, "failed", "", err.Error(), "")
		return nil, fmt.Errorf("dispatch job: %w", err)
	}
	if !dispResp.Accepted {
		errMsg := dispResp.Error
		if errMsg == "" {
			errMsg = "node rejected job"
		}
		_ = s.jobManager.UpdateJobStatus(ctx, jobID, "failed", "", errMsg, "")
		return nil, fmt.Errorf("dispatch job: %s", errMsg)
	}

	// 7. Update job with engine job ID, file_location, and set active
	_ = s.jobManager.UpdateJobStatus(ctx, jobID, "active", dispResp.EngineJobID, "", dispResp.FileLocation)

	// 8. For local dispatch: track for status push
	if _, isLocal := selectedClient.(*node.LocalNodeClient); isLocal && s.localTracker != nil {
		s.localTracker.Add(jobID, req.Engine, dispResp.EngineJobID)
	}

	log.Debug().Str("job_id", jobID).Str("engine_job_id", dispResp.EngineJobID).Msg("job dispatched successfully")

	return &AddDownloadResponse{
		DownloadID: dlID,
		JobID:      jobID,
		NodeID:     selectedClient.NodeID(),
		Status:     "active",
	}, nil
}

func (s *DownloadService) Status(ctx context.Context, downloadID, userID string) (*engine.JobStatus, error) {
	row, err := s.jobManager.GetDownloadWithJobByUser(ctx, downloadID, userID)
	if err != nil {
		return nil, fmt.Errorf("download not found: %w", err)
	}
	return &engine.JobStatus{
		EngineJobID:    row.EngineJobID.String,
		State:          engine.JobState(row.Status),
		Name:           row.Name,
		TotalSize:      row.Size.Int64,
		Progress:       row.Progress,
		Speed:          row.Speed,
		DownloadedSize: row.DownloadedSize,
		ETA:            computeETA(row.Size.Int64, row.DownloadedSize, row.Speed),
		Error:          row.ErrorMessage.String,
	}, nil
}

// ListFilesResult contains the files and the job status.
type ListFilesResult struct {
	Files  []engine.FileInfo
	Status string
}

func (s *DownloadService) ListFiles(ctx context.Context, downloadID, userID string) (*ListFilesResult, error) {
	row, err := s.jobManager.GetDownloadWithJobByUser(ctx, downloadID, userID)
	if err != nil {
		return nil, fmt.Errorf("download not found: %w", err)
	}

	client, ok := s.nodeClients.Get(row.NodeID)
	if !ok {
		return nil, fmt.Errorf("node %q not available", row.NodeID)
	}

	sk := StorageKeyFromCacheKey(row.CacheKey)
	files, err := client.GetJobFiles(ctx, row.Engine, sk, row.EngineJobID.String)
	if err != nil {
		return nil, err
	}

	return &ListFilesResult{Files: files, Status: row.Status}, nil
}

func (s *DownloadService) Delete(ctx context.Context, downloadID, userID string) error {
	row, err := s.jobManager.GetDownloadWithJobAndCount(ctx, downloadID, userID)
	if err != nil {
		return fmt.Errorf("download not found: %w", err)
	}
	jobID := uuidToStr(row.JobID)
	dbJob := jobFromCountRow(row)

	if row.DownloadCount <= 1 {
		// Last download — clean up engine + files + job
		sk := effectiveStorageKey(&dbJob)
		if client, ok := s.nodeClients.Get(dbJob.NodeID); ok {
			if dbJob.EngineJobID.Valid && (dbJob.Status == "active" || dbJob.Status == "queued") {
				if err := client.CancelJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String); err != nil {
					log.Warn().Err(err).Str("job_id", jobID).Msg("failed to cancel engine job during delete")
				}
			}
			if err := client.RemoveJob(ctx, dbJob.Engine, sk, dbJob.EngineJobID.String); err != nil {
				log.Warn().Err(err).Str("job_id", jobID).Msg("failed to remove job files during delete")
			}
		}
		if s.localTracker != nil {
			s.localTracker.Remove(jobID)
		}
		// Delete job (cascades to downloads + download_links)
		return s.jobManager.DeleteJob(ctx, jobID)
	}

	// Other users still need this job — just remove this user's download
	return s.jobManager.DeleteDownload(ctx, downloadID)
}

// DeleteJobByID deletes a job with file cleanup, without user ownership check.
func (s *DownloadService) DeleteJobByID(ctx context.Context, jobID string) error {
	dbJob, err := s.jobManager.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("job not found: %w", err)
	}

	sk := effectiveStorageKey(dbJob)
	if client, ok := s.nodeClients.Get(dbJob.NodeID); ok {
		if dbJob.EngineJobID.Valid && (dbJob.Status == "active" || dbJob.Status == "queued") {
			if err := client.CancelJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String); err != nil {
				log.Warn().Err(err).Str("job_id", jobID).Msg("failed to cancel engine job during delete")
			}
		}
		if err := client.RemoveJob(ctx, dbJob.Engine, sk, dbJob.EngineJobID.String); err != nil {
			log.Warn().Err(err).Str("job_id", jobID).Msg("failed to remove job files during delete")
		}
	}
	if s.localTracker != nil {
		s.localTracker.Remove(jobID)
	}

	// Delete job (cascades to downloads + download_links)
	return s.jobManager.DeleteJob(ctx, jobID)
}

// DeleteUser cleans up all downloads (cancel active, remove files) then deletes the user.
func (s *DownloadService) DeleteUser(ctx context.Context, userID string) error {
	// Single query fetches all user downloads with per-job download counts
	rows, err := s.jobManager.ListUserJobsWithDownloadCounts(ctx, userID)
	if err != nil {
		return fmt.Errorf("list user downloads: %w", err)
	}

	for _, row := range rows {
		dlID := uuidToStr(row.DownloadID)
		jobID := uuidToStr(row.JobID)

		if row.DownloadCount <= 1 {
			// Last download — clean up job entirely
			if err := s.DeleteJobByID(ctx, jobID); err != nil {
				log.Warn().Err(err).Str("job_id", jobID).Msg("failed to clean up job during user deletion")
			}
		} else {
			// Other users still need this job
			if err := s.jobManager.DeleteDownload(ctx, dlID); err != nil {
				log.Warn().Err(err).Str("download_id", dlID).Msg("failed to delete download during user deletion")
			}
		}
	}

	return s.queries.DeleteUser(ctx, strToUUID(userID))
}

// DownloadWithJob is the combined view for API responses.
type DownloadWithJob = gen.ListDownloadsByUserRow

// DownloadWithStatus combines a download+job row with optional live status data.
type DownloadWithStatus struct {
	Download DownloadWithJob
	Status   *engine.JobStatus
}

// ListByUserWithStatus returns downloads with progress data from DB.
func (s *DownloadService) ListByUserWithStatus(ctx context.Context, userID string, limit, offset int32) ([]DownloadWithStatus, error) {
	rows, err := s.jobManager.ListDownloadsByUser(ctx, userID, limit, offset)
	if err != nil {
		return nil, err
	}

	result := make([]DownloadWithStatus, len(rows))
	for i, r := range rows {
		result[i] = DownloadWithStatus{
			Download: r,
			Status: &engine.JobStatus{
				State:          engine.JobState(r.Status),
				Progress:       r.Progress,
				Speed:          r.Speed,
				DownloadedSize: r.DownloadedSize,
				TotalSize:      r.Size.Int64,
				ETA:            computeETA(r.Size.Int64, r.DownloadedSize, r.Speed),
			},
		}
	}
	return result, nil
}

// ListByUserAndEngineWithStatus returns engine-filtered downloads with progress data from DB.
func (s *DownloadService) ListByUserAndEngineWithStatus(ctx context.Context, userID, engineName string, limit, offset int32) ([]DownloadWithStatus, error) {
	rows, err := s.jobManager.ListDownloadsByUserAndEngine(ctx, userID, engineName, limit, offset)
	if err != nil {
		return nil, err
	}

	result := make([]DownloadWithStatus, len(rows))
	for i, r := range rows {
		result[i] = DownloadWithStatus{
			Download: DownloadWithJob(r),
			Status: &engine.JobStatus{
				State:          engine.JobState(r.Status),
				Progress:       r.Progress,
				Speed:          r.Speed,
				DownloadedSize: r.DownloadedSize,
				TotalSize:      r.Size.Int64,
				ETA:            computeETA(r.Size.Int64, r.DownloadedSize, r.Speed),
			},
		}
	}
	return result, nil
}

// RunReconciliation periodically checks for stale jobs and marks them as failed.
// This is a fallback for jobs that stop receiving status updates from workers.
func (s *DownloadService) RunReconciliation(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileStaleJobs(ctx)
		}
	}
}

// reconcileStaleJobs marks jobs as failed if no status update received in 5 minutes.
// The staleness check is done in SQL to avoid fetching all active jobs.
func (s *DownloadService) reconcileStaleJobs(ctx context.Context) {
	jobs, err := s.jobManager.ListStaleActiveJobs(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("reconcile: failed to list stale jobs")
		return
	}

	for _, j := range jobs {
		jobID := uuidToStr(j.ID)
		log.Warn().Str("job_id", jobID).Msg("reconcile: marking stale job as failed")
		_ = s.jobManager.FailJob(ctx, jobID, "stale - no status updates")
	}
}

// jobFromCountRow extracts a gen.Job from a download+job+count joined row.
func jobFromCountRow(r *gen.GetDownloadWithJobAndCountRow) gen.Job {
	return gen.Job{
		ID:           r.JobID,
		NodeID:       r.NodeID,
		Engine:       r.Engine,
		EngineJobID:  r.EngineJobID,
		Url:          r.Url,
		CacheKey:     r.CacheKey,
		Status:       r.Status,
		Name:         r.Name,
		Size:         r.Size,
		FileLocation: r.FileLocation,
		ErrorMessage: r.ErrorMessage,
		Metadata:     r.Metadata,
		CreatedAt:    r.JobCreatedAt,
		UpdatedAt:    r.JobUpdatedAt,
		CompletedAt:  r.CompletedAt,
	}
}


// defaultNameFromURL extracts a human-readable name from a URL.
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

