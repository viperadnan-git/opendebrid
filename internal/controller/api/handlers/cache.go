package handlers

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

type CacheHandler struct {
	queries *gen.Queries
}

func NewCacheHandler(db *pgxpool.Pool) *CacheHandler {
	return &CacheHandler{queries: gen.New(db)}
}

// ListLRU returns cache entries ordered by least recently accessed.
func (h *CacheHandler) ListLRU(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 {
		limit = 50
	}

	entries, err := h.queries.ListCacheByLRU(c.Request().Context(), int32(limit))
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "LIST_ERROR", err.Error())
	}
	return response.Success(c, http.StatusOK, entries)
}

// Cleanup deletes the N least recently accessed cache entries.
func (h *CacheHandler) Cleanup(c echo.Context) error {
	var req struct {
		Count int32 `json:"count"`
	}
	if err := c.Bind(&req); err != nil || req.Count <= 0 {
		return response.Error(c, http.StatusBadRequest, "INVALID_INPUT", "count must be > 0")
	}

	entries, err := h.queries.ListCacheByLRU(c.Request().Context(), req.Count)
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "LIST_ERROR", err.Error())
	}

	deleted := 0
	for _, e := range entries {
		if err := h.queries.DeleteCacheEntry(c.Request().Context(), e.CacheKey); err == nil {
			deleted++
		}
	}

	return response.Success(c, http.StatusOK, map[string]int{"deleted": deleted})
}
