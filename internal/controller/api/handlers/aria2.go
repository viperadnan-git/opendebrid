package handlers

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/opendebrid/opendebrid/internal/core/service"
)

type Aria2Handler struct {
	svc *service.DownloadService
}

func NewAria2Handler(svc *service.DownloadService) *Aria2Handler {
	return &Aria2Handler{svc: svc}
}

type AddDownloadInput struct {
	Body struct {
		URL     string            `json:"url" minLength:"1" doc:"Download URL (magnet, torrent, HTTP)"`
		Options map[string]string `json:"options,omitempty" doc:"Engine-specific options"`
	}
}

type AddDownloadBody struct {
	JobID    string `json:"job_id" doc:"Job ID"`
	CacheHit bool   `json:"cache_hit" doc:"Whether the file was already cached"`
	NodeID   string `json:"node_id" doc:"Node handling the download"`
	Status   string `json:"status" doc:"Job status"`
}

type AddDownloadOutput struct {
	Body AddDownloadBody
}

func (h *Aria2Handler) Add(ctx context.Context, input *AddDownloadInput) (*AddDownloadOutput, error) {
	userID := middleware.GetUserID(ctx)

	resp, err := h.svc.Add(ctx, service.AddDownloadRequest{
		URL:     input.Body.URL,
		Engine:  "aria2",
		UserID:  userID,
		Options: input.Body.Options,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &AddDownloadOutput{Body: AddDownloadBody{
		JobID:    resp.JobID,
		CacheHit: resp.CacheHit,
		NodeID:   resp.NodeID,
		Status:   resp.Status,
	}}, nil
}

type ListJobsInput struct {
	Limit  int `query:"limit" default:"20" minimum:"1" maximum:"100" doc:"Max results"`
	Offset int `query:"offset" default:"0" minimum:"0" doc:"Offset"`
}

type JobRow struct {
	ID           string     `json:"id" doc:"Job ID"`
	UserID       string     `json:"user_id" doc:"User ID"`
	NodeID       string     `json:"node_id" doc:"Node ID"`
	Engine       string     `json:"engine" doc:"Download engine"`
	EngineJobID  string     `json:"engine_job_id" doc:"Engine-internal job ID"`
	URL          string     `json:"url" doc:"Download URL"`
	CacheKey     string     `json:"cache_key" doc:"Cache key"`
	Status       string     `json:"status" doc:"Job status"`
	EngineState  string     `json:"engine_state" doc:"Engine state"`
	FileLocation string     `json:"file_location" doc:"File storage location"`
	ErrorMessage string     `json:"error_message,omitempty" doc:"Error message if failed"`
	CreatedAt    time.Time  `json:"created_at" doc:"Created timestamp"`
	UpdatedAt    time.Time  `json:"updated_at" doc:"Updated timestamp"`
	CompletedAt  *time.Time `json:"completed_at,omitempty" doc:"Completed timestamp"`
}

type ListJobsOutput struct {
	Body []any `json:"body" doc:"List of jobs"`
}

func (h *Aria2Handler) ListJobs(ctx context.Context, input *ListJobsInput) (*ListJobsOutput, error) {
	userID := middleware.GetUserID(ctx)

	jobs, err := h.svc.ListByUserAndEngine(ctx, userID, "aria2", int32(input.Limit), int32(input.Offset))
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &ListJobsOutput{Body: []any{jobs}}, nil
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

func (h *Aria2Handler) GetJob(ctx context.Context, input *JobIDInput) (*JobStatusOutput, error) {
	userID := middleware.GetUserID(ctx)

	status, err := h.svc.Status(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return &JobStatusOutput{Body: newJobStatusBody(status)}, nil
}

type FileInfoItem struct {
	Path        string `json:"path" doc:"File path"`
	Size        int64  `json:"size" doc:"File size in bytes"`
	StorageURI  string `json:"storage_uri" doc:"Storage URI"`
	ContentType string `json:"content_type" doc:"MIME content type"`
}

type FilesOutput struct {
	Body []any `json:"body" doc:"List of files"`
}

func (h *Aria2Handler) GetJobFiles(ctx context.Context, input *JobIDInput) (*FilesOutput, error) {
	userID := middleware.GetUserID(ctx)

	files, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return &FilesOutput{Body: []any{files}}, nil
}

type StatusBody struct {
	Status string `json:"status" doc:"Operation result"`
}

type StatusOutput struct {
	Body StatusBody
}

func (h *Aria2Handler) DeleteJob(ctx context.Context, input *JobIDInput) (*StatusOutput, error) {
	userID := middleware.GetUserID(ctx)

	if err := h.svc.Cancel(ctx, input.ID, userID); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &StatusOutput{Body: StatusBody{Status: "cancelled"}}, nil
}
