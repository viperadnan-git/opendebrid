package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/core/engine/ytdlp"
	"github.com/opendebrid/opendebrid/internal/core/service"
)

type YtDlpHandler struct {
	svc    *service.DownloadService
	engine *ytdlp.Engine
}

func NewYtDlpHandler(svc *service.DownloadService, engine *ytdlp.Engine) *YtDlpHandler {
	return &YtDlpHandler{svc: svc, engine: engine}
}

func (h *YtDlpHandler) Add(ctx context.Context, input *AddDownloadInput) (*AddDownloadOutput, error) {
	userID := middleware.GetUserID(ctx)

	resp, err := h.svc.Add(ctx, service.AddDownloadRequest{
		URL:     input.Body.URL,
		Engine:  "ytdlp",
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

type InfoInput struct {
	Body struct {
		URL string `json:"url" minLength:"1" doc:"URL to inspect"`
	}
}

type InfoOutput struct {
	Body any
}

func (h *YtDlpHandler) Info(ctx context.Context, input *InfoInput) (*InfoOutput, error) {
	info, err := h.engine.Info(ctx, input.Body.URL)
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &InfoOutput{Body: info}, nil
}

func (h *YtDlpHandler) ListJobs(ctx context.Context, input *ListJobsInput) (*ListJobsOutput, error) {
	userID := middleware.GetUserID(ctx)

	jobs, err := h.svc.ListByUserAndEngine(ctx, userID, "ytdlp", int32(input.Limit), int32(input.Offset))
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &ListJobsOutput{Body: []any{jobs}}, nil
}

func (h *YtDlpHandler) GetJob(ctx context.Context, input *JobIDInput) (*JobStatusOutput, error) {
	userID := middleware.GetUserID(ctx)

	status, err := h.svc.Status(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return &JobStatusOutput{Body: newJobStatusBody(status)}, nil
}

func (h *YtDlpHandler) GetJobFiles(ctx context.Context, input *JobIDInput) (*FilesOutput, error) {
	userID := middleware.GetUserID(ctx)

	files, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return &FilesOutput{Body: []any{files}}, nil
}

func (h *YtDlpHandler) DeleteJob(ctx context.Context, input *JobIDInput) (*StatusOutput, error) {
	userID := middleware.GetUserID(ctx)

	if err := h.svc.Cancel(ctx, input.ID, userID); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &StatusOutput{Body: StatusBody{Status: "cancelled"}}, nil
}
