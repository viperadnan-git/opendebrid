package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

type CacheHandler struct {
	queries *gen.Queries
}

func NewCacheHandler(db *pgxpool.Pool) *CacheHandler {
	return &CacheHandler{queries: gen.New(db)}
}

type LRUInput struct {
	Limit int `query:"limit" default:"50" minimum:"1" maximum:"500" doc:"Max entries"`
}

func (h *CacheHandler) ListLRU(ctx context.Context, input *LRUInput) (*ListJobsOutput, error) {
	entries, err := h.queries.ListCacheByLRU(ctx, int32(input.Limit))
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}
	return &ListJobsOutput{Body: []any{entries}}, nil
}

type CleanupInput struct {
	Body struct {
		Count int32 `json:"count" minimum:"1" doc:"Number of entries to delete"`
	}
}

type CleanupBody struct {
	Deleted int `json:"deleted" doc:"Number of entries deleted"`
}

type CleanupOutput struct {
	Body CleanupBody
}

func (h *CacheHandler) Cleanup(ctx context.Context, input *CleanupInput) (*CleanupOutput, error) {
	entries, err := h.queries.ListCacheByLRU(ctx, input.Body.Count)
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	deleted := 0
	for _, e := range entries {
		if err := h.queries.DeleteCacheEntry(ctx, e.CacheKey); err == nil {
			deleted++
		}
	}

	return &CleanupOutput{Body: CleanupBody{Deleted: deleted}}, nil
}
