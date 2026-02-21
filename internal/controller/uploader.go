package controller

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/storage"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

const (
	controllerUploadMaxRetries = 3
	controllerUploadMaxConc    = 2
)

// controllerUploader handles background uploads for jobs completed on the controller node.
// Unlike the worker uploader (which uses gRPC), this calls the DB directly.
// Uploads are triggered on-demand from job completion or startup reconciliation.
type controllerUploader struct {
	queries     *gen.Queries
	provider    storage.StorageProvider
	nodeID      string
	downloadDir string
	sem         chan struct{} // concurrency limiter
}

func newControllerUploader(queries *gen.Queries, provider storage.StorageProvider, nodeID, downloadDir string) *controllerUploader {
	return &controllerUploader{
		queries:     queries,
		provider:    provider,
		nodeID:      nodeID,
		downloadDir: downloadDir,
		sem:         make(chan struct{}, controllerUploadMaxConc),
	}
}

func (u *controllerUploader) isActive() bool {
	return u.provider != nil && u.provider.Name() != "local"
}

// UploadPending claims all pending/failed uploads for this node and uploads each in background.
func (u *controllerUploader) UploadPending(ctx context.Context) {
	if !u.isActive() {
		return
	}
	rows, err := u.queries.ClaimPendingUploads(ctx, util.ToText(u.nodeID))
	if err != nil {
		log.Warn().Err(err).Msg("controller uploader: failed to claim pending uploads")
		return
	}
	if len(rows) == 0 {
		return
	}
	log.Debug().Int("count", len(rows)).Msg("controller uploader: starting uploads")
	for _, row := range rows {
		go u.upload(ctx, util.UUIDToStr(row.ID), row.Engine, row.StorageKey)
	}
}

func (u *controllerUploader) upload(ctx context.Context, jobID, engine, storageKey string) {
	u.sem <- struct{}{}        // acquire
	defer func() { <-u.sem }() // release

	localDir := filepath.Join(u.downloadDir, engine, storageKey)
	if _, err := os.Stat(localDir); os.IsNotExist(err) {
		log.Warn().Str("job_id", jobID).Str("local_dir", localDir).Msg("controller uploader: local directory not found")
		u.setStatus(ctx, jobID, "failed")
		return
	}

	log.Debug().Str("job_id", jobID).Str("storage_key", storageKey).Msg("controller uploader: starting upload")
	start := time.Now()

	var remoteURI string
	var lastErr error
	backoffs := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second}

	for attempt := 0; attempt <= controllerUploadMaxRetries; attempt++ {
		if ctx.Err() != nil {
			return
		}
		remoteURI, lastErr = u.provider.Upload(ctx, localDir, storageKey)
		if lastErr == nil {
			break
		}
		log.Warn().Err(lastErr).Str("job_id", jobID).Int("attempt", attempt+1).Msg("controller uploader: upload attempt failed")
		if attempt < controllerUploadMaxRetries {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoffs[attempt]):
			}
		}
	}
	if lastErr != nil {
		log.Warn().Err(lastErr).Str("job_id", jobID).Dur("duration", time.Since(start)).Msg("controller uploader: all retries exhausted")
		u.setStatus(ctx, jobID, "failed")
		return
	}

	// Atomically update file_location, unlink node, mark uploaded
	jobUUID := util.TextToUUID(jobID)
	if _, err := u.queries.CompleteUpload(ctx, gen.CompleteUploadParams{
		RemoteUri: pgtype.Text{String: remoteURI, Valid: true},
		JobID:     jobUUID,
	}); err != nil {
		log.Error().Err(err).Str("job_id", jobID).Msg("controller uploader: failed to complete upload in DB")
		return
	}

	// Update existing download_links to use the remote URI
	oldPrefix := engine + "/" + storageKey
	if err := u.queries.UpdateDownloadLinksStorageURI(ctx, gen.UpdateDownloadLinksStorageURIParams{
		NewPrefix: remoteURI,
		OldPrefix: oldPrefix,
		JobID:     jobUUID,
	}); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("controller uploader: failed to update download links")
	}

	log.Info().Str("job_id", jobID).Str("remote_uri", remoteURI).Dur("duration", time.Since(start)).Msg("controller uploader: upload completed")

	// Clean up local files
	if err := os.RemoveAll(localDir); err != nil {
		log.Warn().Err(err).Str("local_dir", localDir).Msg("controller uploader: failed to remove local files after upload")
	}
}

func (u *controllerUploader) setStatus(ctx context.Context, jobID, status string) {
	if err := u.queries.SetUploadStatus(ctx, gen.SetUploadStatusParams{
		UploadStatus: status,
		JobID:        util.TextToUUID(jobID),
	}); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Str("status", status).Msg("controller uploader: failed to set upload_status")
	}
}
