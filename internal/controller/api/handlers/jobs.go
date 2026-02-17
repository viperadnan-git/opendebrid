package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/controller/api/middleware"
	"github.com/viperadnan-git/opendebrid/internal/core/service"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

type JobsHandler struct {
	svc         *service.DownloadService
	queries     *gen.Queries
	linkExpiry  time.Duration
	fileBaseURL string
}

func NewJobsHandler(svc *service.DownloadService, db *pgxpool.Pool, linkExpiry time.Duration, fileBaseURL string) *JobsHandler {
	return &JobsHandler{
		svc:         svc,
		queries:     gen.New(db),
		linkExpiry:  linkExpiry,
		fileBaseURL: fileBaseURL,
	}
}

// --- Input types ---

type AddJobInput struct {
	Body struct {
		URL     string            `json:"url" minLength:"1" doc:"Download URL (magnet, torrent, HTTP)"`
		Engine  string            `json:"engine" enum:"aria2,ytdlp" doc:"Download engine"`
		Options map[string]string `json:"options,omitempty" doc:"Engine-specific options"`
	}
}

type ListJobsInput struct {
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100" doc:"Max results"`
	Offset int    `query:"offset" default:"0" minimum:"0" doc:"Offset"`
	Engine string `query:"engine" doc:"Filter by engine (aria2, ytdlp)"`
}

type JobIDInput struct {
	ID string `path:"id" doc:"Job ID"`
}

type GenerateLinkInput struct {
	ID   string `path:"id" doc:"Job ID"`
	Body struct {
		Path string `json:"path" minLength:"1" doc:"File path to generate link for"`
	}
}

// --- DTO types ---

type AddJobDTO struct {
	JobID    string `json:"job_id" doc:"Job ID"`
	CacheHit bool   `json:"cache_hit" doc:"Whether the file was already cached"`
	NodeID   string `json:"node_id" doc:"Node handling the download"`
	Status   string `json:"status" doc:"Job status"`
}

type JobDTO struct {
	ID        string `json:"id" doc:"Job ID"`
	URL       string `json:"url" doc:"Download URL"`
	Engine    string `json:"engine" doc:"Download engine"`
	Status    string `json:"status" doc:"Job status"`
	NodeID    string `json:"node_id" doc:"Node ID"`
	Error     string `json:"error,omitempty" doc:"Error message"`
	CreatedAt string `json:"created_at" doc:"Creation time"`

	// Live progress fields (populated for active/queued jobs)
	Progress       *float64 `json:"progress,omitempty" doc:"Download progress (0-1)"`
	Speed          *int64   `json:"speed,omitempty" doc:"Download speed in bytes/sec"`
	TotalSize      *int64   `json:"total_size,omitempty" doc:"Total file size in bytes"`
	DownloadedSize *int64   `json:"downloaded_size,omitempty" doc:"Downloaded bytes"`
	ETA            *int64   `json:"eta,omitempty" doc:"Estimated time remaining in seconds"`
	EngineState    *string  `json:"engine_state,omitempty" doc:"Engine-specific state"`
}

type JobStatusDTO struct {
	EngineJobID    string         `json:"engine_job_id" doc:"Engine-internal job ID"`
	State          string         `json:"state" doc:"Job state"`
	EngineState    string         `json:"engine_state" doc:"Engine-specific state"`
	Progress       float64        `json:"progress" doc:"Download progress (0-1)"`
	Speed          int64          `json:"speed" doc:"Download speed in bytes/sec"`
	TotalSize      int64          `json:"total_size" doc:"Total file size in bytes"`
	DownloadedSize int64          `json:"downloaded_size" doc:"Downloaded bytes"`
	ETA            int64          `json:"eta" doc:"Estimated time remaining in nanoseconds"`
	Error          string         `json:"error,omitempty" doc:"Error message"`
	Extra          map[string]any `json:"extra,omitempty" doc:"Engine-specific extra data"`
}

type FilesDTO struct {
	Status string       `json:"status" doc:"Job status"`
	Files  []FileDTO    `json:"files" doc:"List of files"`
}

type FileDTO struct {
	Path string `json:"path" doc:"Relative file path"`
	Size int64  `json:"size" doc:"File size in bytes"`
}

type LinkDTO struct {
	URL       string    `json:"url" doc:"Download URL"`
	Token     string    `json:"token" doc:"Download token"`
	ExpiresAt time.Time `json:"expires_at" doc:"Link expiry time"`
}

// --- Handlers ---

func (h *JobsHandler) Add(ctx context.Context, input *AddJobInput) (*DataOutput[AddJobDTO], error) {
	userID := middleware.GetUserID(ctx)

	resp, err := h.svc.Add(ctx, service.AddDownloadRequest{
		URL:     input.Body.URL,
		Engine:  input.Body.Engine,
		UserID:  userID,
		Options: input.Body.Options,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return OK(AddJobDTO{
		JobID:    resp.JobID,
		CacheHit: resp.CacheHit,
		NodeID:   resp.NodeID,
		Status:   resp.Status,
	}), nil
}

func (h *JobsHandler) List(ctx context.Context, input *ListJobsInput) (*DataOutput[[]JobDTO], error) {
	userID := middleware.GetUserID(ctx)

	var results []service.JobWithStatus
	var err error

	if input.Engine != "" {
		results, err = h.svc.ListByUserAndEngineWithStatus(ctx, userID, input.Engine, int32(input.Limit), int32(input.Offset))
	} else {
		results, err = h.svc.ListByUserWithStatus(ctx, userID, int32(input.Limit), int32(input.Offset))
	}
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	dtos := make([]JobDTO, len(results))
	for i, r := range results {
		dtos[i] = JobDTO{
			ID:        pgUUIDToString(r.Job.ID),
			URL:       r.Job.Url,
			Engine:    r.Job.Engine,
			Status:    r.Job.Status,
			NodeID:    r.Job.NodeID,
			Error:     r.Job.ErrorMessage.String,
			CreatedAt: r.Job.CreatedAt.Time.Format(time.RFC3339),
		}
		if r.Status != nil {
			dtos[i].Progress = &r.Status.Progress
			dtos[i].Speed = &r.Status.Speed
			dtos[i].TotalSize = &r.Status.TotalSize
			dtos[i].DownloadedSize = &r.Status.DownloadedSize
			eta := int64(r.Status.ETA.Seconds())
			dtos[i].ETA = &eta
			dtos[i].EngineState = &r.Status.EngineState
		}
	}

	return OK(dtos), nil
}

func (h *JobsHandler) Get(ctx context.Context, input *JobIDInput) (*DataOutput[JobStatusDTO], error) {
	userID := middleware.GetUserID(ctx)

	status, err := h.svc.Status(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return OK(JobStatusDTO{
		EngineJobID:    status.EngineJobID,
		State:          string(status.State),
		EngineState:    status.EngineState,
		Progress:       status.Progress,
		Speed:          status.Speed,
		TotalSize:      status.TotalSize,
		DownloadedSize: status.DownloadedSize,
		ETA:            int64(status.ETA),
		Error:          status.Error,
		Extra:          status.Extra,
	}), nil
}

func (h *JobsHandler) Files(ctx context.Context, input *JobIDInput) (*DataOutput[FilesDTO], error) {
	userID := middleware.GetUserID(ctx)

	result, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	files := make([]FileDTO, len(result.Files))
	for i, f := range result.Files {
		files[i] = FileDTO{Path: f.Path, Size: f.Size}
	}

	return OK(FilesDTO{Status: result.Status, Files: files}), nil
}

func (h *JobsHandler) GenerateLink(ctx context.Context, input *GenerateLinkInput) (*DataOutput[LinkDTO], error) {
	userID := middleware.GetUserID(ctx)

	result, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound("job not found")
	}

	if result.Status != "completed" {
		return nil, huma.Error400BadRequest("download links are only available for completed jobs")
	}

	var storagePath string
	for _, f := range result.Files {
		if f.Path == input.Body.Path {
			storagePath = strings.TrimPrefix(f.StorageURI, "file://")
			break
		}
	}
	if storagePath == "" {
		return nil, huma.Error404NotFound("file not found")
	}

	token, err := generateToken()
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to generate token")
	}

	expiry := time.Now().Add(h.linkExpiry)
	link, err := h.queries.CreateDownloadLink(ctx, gen.CreateDownloadLinkParams{
		UserID:    pgUUID(userID),
		JobID:     pgUUID(input.ID),
		FilePath:  storagePath,
		Token:     token,
		ExpiresAt: pgtype.Timestamptz{Time: expiry, Valid: true},
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to create download link")
	}

	filename := filepath.Base(input.Body.Path)
	url := h.fileBaseURL + "/dl/" + link.Token + "/" + filename

	return OK(LinkDTO{
		URL:       url,
		Token:     link.Token,
		ExpiresAt: expiry,
	}), nil
}

func (h *JobsHandler) Delete(ctx context.Context, input *JobIDInput) (*MsgOutput, error) {
	userID := middleware.GetUserID(ctx)

	if err := h.svc.Delete(ctx, input.ID, userID); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return Msg("deleted"), nil
}

func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
