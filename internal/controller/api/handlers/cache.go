package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
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

type CacheEntryDTO struct {
	CacheKey     string `json:"cache_key" doc:"Cache key"`
	JobID        string `json:"job_id" doc:"Job ID"`
	NodeID       string `json:"node_id" doc:"Node ID"`
	Engine       string `json:"engine" doc:"Engine"`
	FileLocation string `json:"file_location" doc:"File location"`
	TotalSize    int64  `json:"total_size" doc:"Total size in bytes"`
	AccessCount  int32  `json:"access_count" doc:"Access count"`
}

type CleanupInput struct {
	Body struct {
		Count int32 `json:"count" minimum:"1" doc:"Number of entries to delete"`
	}
}

type CleanupDTO struct {
	Deleted int `json:"deleted" doc:"Number of entries deleted"`
}

func (h *CacheHandler) ListLRU(ctx context.Context, input *LRUInput) (*DataOutput[[]CacheEntryDTO], error) {
	entries, err := h.queries.ListCacheByLRU(ctx, int32(input.Limit))
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	dtos := make([]CacheEntryDTO, len(entries))
	for i, e := range entries {
		dtos[i] = CacheEntryDTO{
			CacheKey:     e.CacheKey,
			JobID:        pgUUIDToString(e.JobID),
			NodeID:       e.NodeID,
			Engine:       e.Engine,
			FileLocation: e.FileLocation,
			TotalSize:    e.TotalSize.Int64,
			AccessCount:  e.AccessCount,
		}
	}

	return OK(dtos), nil
}

func (h *CacheHandler) Cleanup(ctx context.Context, input *CleanupInput) (*DataOutput[CleanupDTO], error) {
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

	return OK(CleanupDTO{Deleted: deleted}), nil
}
