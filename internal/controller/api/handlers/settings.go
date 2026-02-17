package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

type SettingsHandler struct {
	queries *gen.Queries
}

func NewSettingsHandler(db *pgxpool.Pool) *SettingsHandler {
	return &SettingsHandler{queries: gen.New(db)}
}

type SettingDTO struct {
	Key         string `json:"key" doc:"Setting key"`
	Value       string `json:"value" doc:"Setting value"`
	Description string `json:"description,omitempty" doc:"Setting description"`
}

type SettingKeyInput struct {
	Key string `path:"key" doc:"Setting key"`
}

type UpdateSettingInput struct {
	Key  string `path:"key" doc:"Setting key"`
	Body struct {
		Value       any    `json:"value" doc:"New value"`
		Description string `json:"description,omitempty" doc:"Setting description"`
	}
}

func (h *SettingsHandler) List(ctx context.Context, _ *EmptyInput) (*DataOutput[[]SettingDTO], error) {
	settings, err := h.queries.ListSettings(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	dtos := make([]SettingDTO, len(settings))
	for i, s := range settings {
		dtos[i] = SettingDTO{
			Key:         s.Key,
			Value:       s.Value,
			Description: s.Description.String,
		}
	}

	return OK(dtos), nil
}

func (h *SettingsHandler) Get(ctx context.Context, input *SettingKeyInput) (*DataOutput[SettingDTO], error) {
	s, err := h.queries.GetSetting(ctx, input.Key)
	if err != nil {
		return nil, huma.Error404NotFound("setting not found")
	}

	return OK(SettingDTO{
		Key:         s.Key,
		Value:       s.Value,
		Description: s.Description.String,
	}), nil
}

func (h *SettingsHandler) Update(ctx context.Context, input *UpdateSettingInput) (*DataOutput[SettingDTO], error) {
	var valueStr string
	switch v := input.Body.Value.(type) {
	case string:
		valueStr = `"` + v + `"`
	default:
		valueStr = "null"
	}

	s, err := h.queries.UpsertSetting(ctx, gen.UpsertSettingParams{
		Key:         input.Key,
		Value:       valueStr,
		Description: pgtype.Text{String: input.Body.Description, Valid: input.Body.Description != ""},
	})
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return OK(SettingDTO{
		Key:         s.Key,
		Value:       s.Value,
		Description: s.Description.String,
	}), nil
}
