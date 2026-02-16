package handlers

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

type SettingsHandler struct {
	queries *gen.Queries
}

func NewSettingsHandler(db *pgxpool.Pool) *SettingsHandler {
	return &SettingsHandler{queries: gen.New(db)}
}

func (h *SettingsHandler) List(c echo.Context) error {
	settings, err := h.queries.ListSettings(c.Request().Context())
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "LIST_ERROR", err.Error())
	}
	return response.Success(c, http.StatusOK, settings)
}

func (h *SettingsHandler) Get(c echo.Context) error {
	key := c.Param("key")
	setting, err := h.queries.GetSetting(c.Request().Context(), key)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", "setting not found")
	}
	return response.Success(c, http.StatusOK, setting)
}

func (h *SettingsHandler) Update(c echo.Context) error {
	key := c.Param("key")

	var req struct {
		Value       any    `json:"value"`
		Description string `json:"description"`
	}
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "INVALID_INPUT", "invalid request body")
	}

	// Serialize value to JSON bytes
	var valueStr string
	switch v := req.Value.(type) {
	case string:
		valueStr = `"` + v + `"`
	default:
		valueStr = "null"
	}

	setting, err := h.queries.UpsertSetting(c.Request().Context(), gen.UpsertSettingParams{
		Key:         key,
		Value:       valueStr,
		Description: pgtype.Text{String: req.Description, Valid: req.Description != ""},
	})
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "UPDATE_ERROR", err.Error())
	}

	return response.Success(c, http.StatusOK, setting)
}
