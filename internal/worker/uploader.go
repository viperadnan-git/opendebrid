package worker

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/storage"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
)

const (
	uploadMaxRetries = 3
	uploadMaxConc    = 2
)

// Uploader handles background uploads of completed jobs to remote storage.
// Uploads are triggered on-demand from job completion or startup reconciliation.
type Uploader struct {
	client      pb.NodeServiceClient
	provider    storage.StorageProvider
	nodeID      string
	downloadDir string
	sem         chan struct{} // concurrency limiter
}

func newUploader(client pb.NodeServiceClient, provider storage.StorageProvider, nodeID, downloadDir string) *Uploader {
	return &Uploader{
		client:      client,
		provider:    provider,
		nodeID:      nodeID,
		downloadDir: downloadDir,
		sem:         make(chan struct{}, uploadMaxConc),
	}
}

func (u *Uploader) isActive() bool {
	return u.provider != nil && u.provider.Name() != "local"
}

// UploadPending claims all pending/failed uploads via gRPC and uploads each in background.
// Called on startup reconciliation and after push with completed jobs.
func (u *Uploader) UploadPending(ctx context.Context) {
	if !u.isActive() {
		return
	}
	resp, err := u.client.ListPendingUploads(ctx, &pb.ListPendingUploadsRequest{
		NodeId: u.nodeID,
	})
	if err != nil {
		log.Warn().Err(err).Msg("uploader: failed to list pending uploads")
		return
	}
	if len(resp.Jobs) == 0 {
		return
	}
	log.Debug().Int("count", len(resp.Jobs)).Msg("uploader: starting uploads")
	for _, job := range resp.Jobs {
		go u.upload(ctx, job)
	}
}

func (u *Uploader) upload(ctx context.Context, job *pb.PendingUploadJob) {
	u.sem <- struct{}{}        // acquire
	defer func() { <-u.sem }() // release

	localDir := filepath.Join(u.downloadDir, job.Engine, job.StorageKey)
	if _, err := os.Stat(localDir); os.IsNotExist(err) {
		log.Warn().Str("job_id", job.JobId).Str("local_dir", localDir).Msg("uploader: local directory not found")
		u.reportUpload(ctx, job.JobId, "", "local directory not found: "+localDir)
		return
	}

	log.Debug().Str("job_id", job.JobId).Str("storage_key", job.StorageKey).Msg("uploader: starting upload")
	start := time.Now()

	var remoteURI string
	var lastErr error
	backoffs := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second}

	for attempt := 0; attempt <= uploadMaxRetries; attempt++ {
		if ctx.Err() != nil {
			return
		}
		remoteURI, lastErr = u.provider.Upload(ctx, localDir, job.StorageKey)
		if lastErr == nil {
			break
		}
		log.Warn().Err(lastErr).Str("job_id", job.JobId).Int("attempt", attempt+1).Msg("uploader: upload attempt failed")
		if attempt < uploadMaxRetries {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoffs[attempt]):
			}
		}
	}
	if lastErr != nil {
		log.Warn().Err(lastErr).Str("job_id", job.JobId).Dur("duration", time.Since(start)).Msg("uploader: all retries exhausted")
		u.reportUpload(ctx, job.JobId, "", lastErr.Error())
		return
	}

	log.Info().Str("job_id", job.JobId).Str("remote_uri", remoteURI).Dur("duration", time.Since(start)).Msg("uploader: upload completed")
	u.reportUpload(ctx, job.JobId, remoteURI, "")

	// Clean up local files after successful upload.
	if err := os.RemoveAll(localDir); err != nil {
		log.Warn().Err(err).Str("local_dir", localDir).Msg("uploader: failed to remove local files after upload")
	}
}

func (u *Uploader) reportUpload(ctx context.Context, jobID, remoteURI, uploadErr string) {
	if _, err := u.client.ReportUpload(ctx, &pb.ReportUploadRequest{
		NodeId:    u.nodeID,
		JobId:     jobID,
		RemoteUri: remoteURI,
		Error:     uploadErr,
	}); err != nil {
		log.Warn().Err(err).Str("job_id", jobID).Msg("uploader: failed to report upload result to controller")
	}
}
