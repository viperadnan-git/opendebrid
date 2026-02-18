package service

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/core/job"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
	"golang.org/x/sync/errgroup"
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

type statusCacheEntry struct {
	status engine.JobStatus
	at     time.Time
}

type DownloadService struct {
	registry    *engine.Registry
	nodeClients map[string]node.NodeClient
	scheduler   NodeSelector
	jobManager  *job.Manager
	queries     *gen.Queries
	bus         event.Bus
	pool        *pgxpool.Pool

	statusMu    sync.RWMutex
	statusCache map[string]statusCacheEntry // jobID -> cached status
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
		pool:        db,
		statusCache: make(map[string]statusCacheEntry),
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
			// Job exists but is failed/cancelled — fall through to create a new job
		}
	}

	// 4. No usable job — select node and dispatch new download
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

	// 5. Create job + download atomically in a transaction
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

	jobID := uuidToStr(dbJob.ID)

	dl, err := s.jobManager.CreateDownloadTx(ctx, tx, req.UserID, jobID)
	if err != nil {
		return nil, fmt.Errorf("create download: %w", err)
	}
	dlID := uuidToStr(dl.ID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	log.Debug().Str("job_id", jobID).Str("download_id", dlID).Str("node", selectedClient.NodeID()).Str("cache_key", fullKey).Msg("dispatching job to node")

	// 7. Dispatch to node with storage key
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

	// 8. Update job with engine job ID and set active
	_ = s.jobManager.UpdateJobStatus(ctx, jobID, "active", dispResp.EngineJobID, "", "")
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
	dbJob := jobFromJoinedRow(row)
	return s.statusForJob(ctx, &dbJob)
}

// StatusByID fetches live status for a job without user filtering (for internal/admin use).
func (s *DownloadService) StatusByID(ctx context.Context, jobID string) (*engine.JobStatus, error) {
	dbJob, err := s.jobManager.GetJob(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}
	return s.statusForJob(ctx, dbJob)
}

const statusCacheTTL = 5 * time.Second

func (s *DownloadService) statusForJob(ctx context.Context, dbJob *gen.Job) (*engine.JobStatus, error) {
	jobID := uuidToStr(dbJob.ID)

	// Terminal states — return from DB, no RPC needed
	if dbJob.Status == "completed" || dbJob.Status == "failed" || dbJob.Status == "cancelled" {
		return &engine.JobStatus{
			EngineJobID: dbJob.EngineJobID.String,
			State:       engine.JobState(dbJob.Status),
			Name:        dbJob.Name,
			TotalSize:   dbJob.Size.Int64,
			Error:       dbJob.ErrorMessage.String,
		}, nil
	}

	// Check cache first
	s.statusMu.RLock()
	if entry, ok := s.statusCache[jobID]; ok && time.Since(entry.at) < statusCacheTTL {
		s.statusMu.RUnlock()
		st := entry.status
		return &st, nil
	}
	s.statusMu.RUnlock()

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

	// Cache the result
	s.statusMu.Lock()
	s.statusCache[jobID] = statusCacheEntry{status: status, at: time.Now()}
	s.statusMu.Unlock()

	return &status, nil
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
	dbJob := jobFromJoinedRow(row)

	client, ok := s.nodeClients[dbJob.NodeID]
	if !ok {
		return nil, fmt.Errorf("node %q not available", dbJob.NodeID)
	}

	sk := effectiveStorageKey(&dbJob)
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

func (s *DownloadService) Cancel(ctx context.Context, downloadID, userID string) error {
	row, err := s.jobManager.GetDownloadWithJobAndCount(ctx, downloadID, userID)
	if err != nil {
		return fmt.Errorf("download not found: %w", err)
	}
	jobID := uuidToStr(row.JobID)

	if row.DownloadCount > 1 {
		// Other users still need this job — just remove this user's download
		return s.jobManager.DeleteDownload(ctx, downloadID)
	}

	// This is the last download — cancel engine + clean up
	client, ok := s.nodeClients[row.NodeID]
	if !ok {
		return fmt.Errorf("node %q not available", row.NodeID)
	}

	if err := client.CancelJob(ctx, row.Engine, jobID, row.EngineJobID.String); err != nil {
		return err
	}

	if err := s.jobManager.UpdateJobStatus(ctx, jobID, "cancelled", "", "", ""); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("failed to mark job cancelled")
	}
	return s.jobManager.DeleteDownload(ctx, downloadID)
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
		if client, ok := s.nodeClients[dbJob.NodeID]; ok {
			if dbJob.EngineJobID.Valid && (dbJob.Status == "active" || dbJob.Status == "queued") {
				if err := client.CancelJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String); err != nil {
					log.Warn().Err(err).Str("job_id", jobID).Msg("failed to cancel engine job during delete")
				}
			}
			if err := client.RemoveJob(ctx, dbJob.Engine, sk, dbJob.EngineJobID.String); err != nil {
				log.Warn().Err(err).Str("job_id", jobID).Msg("failed to remove job files during delete")
			}
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
	if client, ok := s.nodeClients[dbJob.NodeID]; ok {
		if dbJob.EngineJobID.Valid && (dbJob.Status == "active" || dbJob.Status == "queued") {
			if err := client.CancelJob(ctx, dbJob.Engine, jobID, dbJob.EngineJobID.String); err != nil {
				log.Warn().Err(err).Str("job_id", jobID).Msg("failed to cancel engine job during delete")
			}
		}
		if err := client.RemoveJob(ctx, dbJob.Engine, sk, dbJob.EngineJobID.String); err != nil {
			log.Warn().Err(err).Str("job_id", jobID).Msg("failed to remove job files during delete")
		}
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

// ListByUserWithStatus returns downloads enriched with live progress for active/queued jobs.
func (s *DownloadService) ListByUserWithStatus(ctx context.Context, userID string, limit, offset int32) ([]DownloadWithStatus, error) {
	rows, err := s.jobManager.ListDownloadsByUser(ctx, userID, limit, offset)
	if err != nil {
		return nil, err
	}

	items := make([]downloadRow, len(rows))
	for i, r := range rows {
		items[i] = downloadRow{
			DownloadID:  uuidToStr(r.DownloadID),
			JobID:       uuidToStr(r.JobID),
			NodeID:      r.NodeID,
			Engine:      r.Engine,
			EngineJobID: r.EngineJobID.String,
			EngineValid: r.EngineJobID.Valid,
			Status:      r.Status,
		}
	}
	statuses := s.batchFetchStatuses(ctx, items)

	result := make([]DownloadWithStatus, len(rows))
	for i, r := range rows {
		result[i] = DownloadWithStatus{Download: r}
		if st, ok := statuses[items[i].JobID]; ok {
			stCopy := st
			result[i].Status = &stCopy
		}
	}
	return result, nil
}

// ListByUserAndEngineWithStatus returns engine-filtered downloads enriched with live progress.
func (s *DownloadService) ListByUserAndEngineWithStatus(ctx context.Context, userID, engineName string, limit, offset int32) ([]DownloadWithStatus, error) {
	rows, err := s.jobManager.ListDownloadsByUserAndEngine(ctx, userID, engineName, limit, offset)
	if err != nil {
		return nil, err
	}
	items := make([]downloadRow, len(rows))
	for i, r := range rows {
		items[i] = downloadRow{
			DownloadID:  uuidToStr(r.DownloadID),
			JobID:       uuidToStr(r.JobID),
			NodeID:      r.NodeID,
			Engine:      r.Engine,
			EngineJobID: r.EngineJobID.String,
			EngineValid: r.EngineJobID.Valid,
			Status:      r.Status,
		}
	}
	statuses := s.batchFetchStatuses(ctx, items)
	result := make([]DownloadWithStatus, len(rows))
	for i, r := range rows {
		result[i] = DownloadWithStatus{Download: DownloadWithJob(r)}
		if st, ok := statuses[items[i].JobID]; ok {
			stCopy := st
			result[i].Status = &stCopy
		}
	}
	return result, nil
}

type downloadRow struct {
	DownloadID  string
	JobID       string
	NodeID      string
	Engine      string
	EngineJobID string
	EngineValid bool
	Status      string
}

func (s *DownloadService) batchFetchStatuses(ctx context.Context, items []downloadRow) map[string]engine.JobStatus {
	type jobRef struct {
		index       int
		jobID       string
		engine      string
		engineJobID string
	}
	nodeGroups := make(map[string][]jobRef)
	for i, item := range items {
		if item.Status != "active" && item.Status != "queued" {
			continue
		}
		if !item.EngineValid || item.EngineJobID == "" {
			continue
		}
		nodeGroups[item.NodeID] = append(nodeGroups[item.NodeID], jobRef{
			index:       i,
			jobID:       item.JobID,
			engine:      item.Engine,
			engineJobID: item.EngineJobID,
		})
	}

	// Serve from status cache where possible; collect cache misses for RPC.
	result := make(map[string]engine.JobStatus)
	missGroups := make(map[string][]jobRef)

	s.statusMu.RLock()
	for nodeID, refs := range nodeGroups {
		for _, ref := range refs {
			if entry, ok := s.statusCache[ref.jobID]; ok && time.Since(entry.at) < statusCacheTTL {
				result[ref.jobID] = entry.status
			} else {
				missGroups[nodeID] = append(missGroups[nodeID], ref)
			}
		}
	}
	s.statusMu.RUnlock()

	if len(missGroups) == 0 {
		return result
	}

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	for nodeID, refs := range missGroups {
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

		g.Go(func() error {
			statuses, err := client.BatchGetJobStatus(gctx, reqs)
			if err != nil {
				log.Warn().Err(err).Str("node", nodeID).Msg("batch status failed, skipping")
				return nil // non-fatal
			}
			mu.Lock()
			for _, ref := range refs {
				if status, ok := statuses[ref.jobID]; ok {
					result[ref.jobID] = status
				}
			}
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return result
}

// syncStatusToDB persists state changes from live status to the job row.
func (s *DownloadService) syncStatusToDB(ctx context.Context, dbJob *gen.Job, status *engine.JobStatus) {
	jobID := uuidToStr(dbJob.ID)

	newGID := ""
	if status.EngineJobID != dbJob.EngineJobID.String {
		newGID = status.EngineJobID
	}

	// Update name/size if engine resolved them
	if status.Name != "" || status.TotalSize > 0 {
		if err := s.jobManager.UpdateJobMeta(ctx, jobID, status.Name, status.TotalSize); err != nil {
			log.Warn().Err(err).Str("job_id", jobID).Msg("failed to update job meta")
		}
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
			if err := s.jobManager.CompleteJob(ctx, jobID, newGID, fileLocation); err != nil {
				log.Warn().Err(err).Str("job_id", jobID).Msg("failed to complete job")
			}
		}
	case engine.StateFailed:
		if dbJob.Status != "failed" {
			if err := s.jobManager.FailJob(ctx, jobID, status.Error); err != nil {
				log.Warn().Err(err).Str("job_id", jobID).Msg("failed to mark job as failed")
			}
			// Remove download directory on failure
			sk := effectiveStorageKey(dbJob)
			if client, ok := s.nodeClients[dbJob.NodeID]; ok {
				if err := client.RemoveJob(ctx, dbJob.Engine, sk, status.EngineJobID); err != nil {
					log.Warn().Err(err).Str("job_id", jobID).Msg("failed to remove job files on failure")
				}
			}
		}
	default:
		if newGID != "" || dbJob.Status != "active" {
			if err := s.jobManager.UpdateJobStatus(ctx, jobID, "active", newGID, "", ""); err != nil {
				log.Warn().Err(err).Str("job_id", jobID).Msg("failed to update job status")
			}
		}
	}
}

// RunStatusWatcher periodically polls active jobs and syncs their status.
// Uses a fast interval when active jobs exist, slow interval when idle.
func (s *DownloadService) RunStatusWatcher(ctx context.Context, baseInterval time.Duration) {
	idleInterval := 5 * baseInterval
	interval := baseInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			changed := s.pollActiveJobs(ctx)
			if changed > 0 {
				interval = baseInterval
			} else {
				interval = idleInterval
			}
			timer.Reset(interval)
		}
	}
}

// pollActiveJobs returns the count of jobs that had a state change.
func (s *DownloadService) pollActiveJobs(ctx context.Context) int {
	jobs, err := s.jobManager.ListActiveJobs(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("watcher: failed to list active jobs")
		return 0
	}
	if len(jobs) == 0 {
		return 0
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

	// Parallel batch calls across nodes
	type nodeResult struct {
		nodeID   string
		refs     []jobRef
		statuses map[string]engine.JobStatus
	}
	var mu sync.Mutex
	var results []nodeResult
	g, gctx := errgroup.WithContext(ctx)
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

		g.Go(func() error {
			statuses, err := client.BatchGetJobStatus(gctx, reqs)
			if err != nil {
				log.Warn().Err(err).Str("node", nodeID).Msg("watcher: batch status failed")
				return nil // non-fatal
			}
			mu.Lock()
			results = append(results, nodeResult{nodeID: nodeID, refs: refs, statuses: statuses})
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()

	// Populate status cache from watcher results so listing reads serve from cache.
	now := time.Now()
	s.statusMu.Lock()
	for _, nr := range results {
		for _, ref := range nr.refs {
			if status, ok := nr.statuses[ref.jobID]; ok {
				s.statusCache[ref.jobID] = statusCacheEntry{status: status, at: now}
			}
		}
	}
	s.statusMu.Unlock()

	// Process results sequentially (DB writes + event publishing)
	changed := 0
	for _, nr := range results {
		for _, ref := range nr.refs {
			status, ok := nr.statuses[ref.jobID]
			if !ok {
				continue
			}

			j := ref.job
			s.syncStatusToDB(ctx, &j, &status)

			payload := event.JobEvent{
				JobID:       ref.jobID,
				NodeID:      nr.nodeID,
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
					changed++
					if err := s.bus.Publish(ctx, event.Event{Type: event.EventJobCompleted, Payload: payload}); err != nil {
						log.Warn().Err(err).Str("job_id", ref.jobID).Msg("watcher: failed to publish completed event")
					}
				}
			case engine.StateFailed:
				if ref.job.Status != "failed" {
					changed++
					payload.Error = status.Error
					if err := s.bus.Publish(ctx, event.Event{Type: event.EventJobFailed, Payload: payload}); err != nil {
						log.Warn().Err(err).Str("job_id", ref.jobID).Msg("watcher: failed to publish failed event")
					}
				}
			case engine.StateCancelled:
				if ref.job.Status != "cancelled" {
					changed++
					if err := s.bus.Publish(ctx, event.Event{Type: event.EventJobCancelled, Payload: payload}); err != nil {
						log.Warn().Err(err).Str("job_id", ref.jobID).Msg("watcher: failed to publish cancelled event")
					}
				}
			case engine.StateActive:
				changed++
				if err := s.bus.Publish(ctx, event.Event{Type: event.EventJobProgress, Payload: payload}); err != nil {
					log.Warn().Err(err).Str("job_id", ref.jobID).Msg("watcher: failed to publish progress event")
				}
			}
		}
	}
	return changed
}

// jobFromJoinedRow extracts a gen.Job from a download+job joined row.
func jobFromJoinedRow(r *gen.GetDownloadWithJobByUserRow) gen.Job {
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

// resolveFileLocation determines the StorageURI for a completed job's files.
func resolveFileLocation(files []engine.FileInfo) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		return files[0].StorageURI
	}
	first := files[0].StorageURI
	prefix := first
	for _, f := range files[1:] {
		for !strings.HasPrefix(f.StorageURI, prefix) {
			prefix = prefix[:strings.LastIndex(prefix, "/")]
		}
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix = path.Dir(strings.TrimPrefix(prefix, "file://"))
		return "file://" + prefix
	}
	return prefix
}
