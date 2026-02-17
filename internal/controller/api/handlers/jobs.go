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
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/opendebrid/opendebrid/internal/core/service"
	"github.com/opendebrid/opendebrid/internal/database/gen"
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

// Shared types

type AddJobInput struct {
	Body struct {
		URL     string            `json:"url" minLength:"1" doc:"Download URL (magnet, torrent, HTTP)"`
		Engine  string            `json:"engine" enum:"aria2,ytdlp" doc:"Download engine"`
		Options map[string]string `json:"options,omitempty" doc:"Engine-specific options"`
	}
}

type AddJobBody struct {
	JobID    string `json:"job_id" doc:"Job ID"`
	CacheHit bool   `json:"cache_hit" doc:"Whether the file was already cached"`
	NodeID   string `json:"node_id" doc:"Node handling the download"`
	Status   string `json:"status" doc:"Job status"`
}

type AddJobOutput struct {
	Body AddJobBody
}

type ListJobsInput struct {
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100" doc:"Max results"`
	Offset int    `query:"offset" default:"0" minimum:"0" doc:"Offset"`
	Engine string `query:"engine" doc:"Filter by engine (aria2, ytdlp)"`
}

type ListJobsOutput struct {
	Body []any `json:"body" doc:"List of jobs"`
}

type JobIDInput struct {
	ID string `path:"id" doc:"Job ID"`
}

type JobStatusBody struct {
	EngineJobID    string         `json:"engine_job_id" doc:"Engine-internal job ID"`
	State          string         `json:"state" doc:"Job state (queued, active, completed, failed, cancelled)"`
	EngineState    string         `json:"engine_state" doc:"Engine-specific state"`
	Progress       float64        `json:"progress" doc:"Download progress (0-1)"`
	Speed          int64          `json:"speed" doc:"Download speed in bytes/sec"`
	TotalSize      int64          `json:"total_size" doc:"Total file size in bytes"`
	DownloadedSize int64          `json:"downloaded_size" doc:"Downloaded bytes"`
	ETA            int64          `json:"eta" doc:"Estimated time remaining in nanoseconds"`
	Error          string         `json:"error,omitempty" doc:"Error message"`
	Extra          map[string]any `json:"extra,omitempty" doc:"Engine-specific extra data"`
}

func newJobStatusBody(s *engine.JobStatus) JobStatusBody {
	return JobStatusBody{
		EngineJobID:    s.EngineJobID,
		State:          string(s.State),
		EngineState:    s.EngineState,
		Progress:       s.Progress,
		Speed:          s.Speed,
		TotalSize:      s.TotalSize,
		DownloadedSize: s.DownloadedSize,
		ETA:            int64(s.ETA),
		Error:          s.Error,
		Extra:          s.Extra,
	}
}

type JobStatusOutput struct {
	Body JobStatusBody
}

type FilesOutput struct {
	Body []any `json:"body" doc:"List of files"`
}

type GenerateLinkInput struct {
	ID   string `path:"id" doc:"Job ID"`
	Body struct {
		Path string `json:"path" minLength:"1" doc:"File path to generate link for"`
	}
}

type GenerateLinkBody struct {
	URL       string    `json:"url" doc:"Download URL"`
	Token     string    `json:"token" doc:"Download token"`
	ExpiresAt time.Time `json:"expires_at" doc:"Link expiry time"`
}

type GenerateLinkOutput struct {
	Body GenerateLinkBody
}

type StatusBody struct {
	Status string `json:"status" doc:"Operation result"`
}

type StatusOutput struct {
	Body StatusBody
}

// Handlers

func (h *JobsHandler) Add(ctx context.Context, input *AddJobInput) (*AddJobOutput, error) {
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

	return &AddJobOutput{Body: AddJobBody{
		JobID:    resp.JobID,
		CacheHit: resp.CacheHit,
		NodeID:   resp.NodeID,
		Status:   resp.Status,
	}}, nil
}

func (h *JobsHandler) List(ctx context.Context, input *ListJobsInput) (*ListJobsOutput, error) {
	userID := middleware.GetUserID(ctx)

	var jobs any
	var err error

	if input.Engine != "" {
		jobs, err = h.svc.ListByUserAndEngine(ctx, userID, input.Engine, int32(input.Limit), int32(input.Offset))
	} else {
		jobs, err = h.svc.ListByUser(ctx, userID, int32(input.Limit), int32(input.Offset))
	}
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &ListJobsOutput{Body: []any{jobs}}, nil
}

func (h *JobsHandler) Get(ctx context.Context, input *JobIDInput) (*JobStatusOutput, error) {
	userID := middleware.GetUserID(ctx)

	status, err := h.svc.Status(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return &JobStatusOutput{Body: newJobStatusBody(status)}, nil
}

func (h *JobsHandler) Files(ctx context.Context, input *JobIDInput) (*FilesOutput, error) {
	userID := middleware.GetUserID(ctx)

	files, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return &FilesOutput{Body: []any{files}}, nil
}

func (h *JobsHandler) GenerateLink(ctx context.Context, input *GenerateLinkInput) (*GenerateLinkOutput, error) {
	userID := middleware.GetUserID(ctx)

	files, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound("job not found")
	}

	var storagePath string
	for _, f := range files {
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

	return &GenerateLinkOutput{Body: GenerateLinkBody{
		URL:       url,
		Token:     link.Token,
		ExpiresAt: expiry,
	}}, nil
}

func (h *JobsHandler) Delete(ctx context.Context, input *JobIDInput) (*StatusOutput, error) {
	userID := middleware.GetUserID(ctx)

	if err := h.svc.Cancel(ctx, input.ID, userID); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &StatusOutput{Body: StatusBody{Status: "cancelled"}}, nil
}

func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
