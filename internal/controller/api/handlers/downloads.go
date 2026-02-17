package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/core/service"
)

type DownloadsHandler struct {
	svc *service.DownloadService
}

func NewDownloadsHandler(svc *service.DownloadService) *DownloadsHandler {
	return &DownloadsHandler{svc: svc}
}

type ListDownloadsInput struct {
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100" doc:"Max results"`
	Offset int    `query:"offset" default:"0" minimum:"0" doc:"Offset"`
	Engine string `query:"engine" doc:"Filter by engine (aria2, ytdlp)"`
}

func (h *DownloadsHandler) List(ctx context.Context, input *ListDownloadsInput) (*ListJobsOutput, error) {
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

func (h *DownloadsHandler) Get(ctx context.Context, input *JobIDInput) (*JobStatusOutput, error) {
	userID := middleware.GetUserID(ctx)

	status, err := h.svc.Status(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return &JobStatusOutput{Body: newJobStatusBody(status)}, nil
}
