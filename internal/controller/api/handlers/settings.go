package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

type SettingsHandler struct {
	queries *gen.Queries
}

func NewSettingsHandler(db *pgxpool.Pool) *SettingsHandler {
	return &SettingsHandler{queries: gen.New(db)}
}

func (h *SettingsHandler) List(ctx context.Context, input *EmptyInput) (*ListJobsOutput, error) {
	settings, err := h.queries.ListSettings(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}
	return &ListJobsOutput{Body: []any{settings}}, nil
}

type SettingKeyInput struct {
	Key string `path:"key" doc:"Setting key"`
}

func (h *SettingsHandler) Get(ctx context.Context, input *SettingKeyInput) (*struct{ Body any }, error) {
	setting, err := h.queries.GetSetting(ctx, input.Key)
	if err != nil {
		return nil, huma.Error404NotFound("setting not found")
	}
	return &struct{ Body any }{Body: setting}, nil
}

type UpdateSettingInput struct {
	Key  string `path:"key" doc:"Setting key"`
	Body struct {
		Value       any    `json:"value" doc:"New value"`
		Description string `json:"description,omitempty" doc:"Setting description"`
	}
}

func (h *SettingsHandler) Update(ctx context.Context, input *UpdateSettingInput) (*struct{ Body any }, error) {
	var valueStr string
	switch v := input.Body.Value.(type) {
	case string:
		valueStr = `"` + v + `"`
	default:
		valueStr = "null"
	}

	setting, err := h.queries.UpsertSetting(ctx, gen.UpsertSettingParams{
		Key:         input.Key,
		Value:       valueStr,
		Description: pgtype.Text{String: input.Body.Description, Valid: input.Body.Description != ""},
	})
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return &struct{ Body any }{Body: setting}, nil
}
