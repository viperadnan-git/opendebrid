package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/opendebrid/opendebrid/internal/core/engine/ytdlp"
)

type YtDlpHandler struct {
	engine *ytdlp.Engine
}

func NewYtDlpHandler(engine *ytdlp.Engine) *YtDlpHandler {
	return &YtDlpHandler{engine: engine}
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
