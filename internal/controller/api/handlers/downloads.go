package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/controller/api/middleware"
	"github.com/viperadnan-git/opendebrid/internal/core/service"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

type DownloadsHandler struct {
	svc *service.DownloadService
}

func NewDownloadsHandler(svc *service.DownloadService) *DownloadsHandler {
	return &DownloadsHandler{svc: svc}
}

// --- Input types ---

type AddDownloadInput struct {
	Body struct {
		URL     string            `json:"url" minLength:"1" doc:"Download URL (magnet, torrent, HTTP)"`
		Engine  string            `json:"engine" enum:"aria2,ytdlp" doc:"Download engine"`
		Options map[string]string `json:"options,omitempty" doc:"Engine-specific options"`
	}
}

type ListDownloadsInput struct {
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100" doc:"Max results"`
	Offset int    `query:"offset" default:"0" minimum:"0" doc:"Offset"`
	Engine string `query:"engine" doc:"Filter by engine (aria2, ytdlp)"`
}

type DownloadIDInput struct {
	ID string `path:"id" doc:"Download ID"`
}

type GenerateLinkInput struct {
	ID   string `path:"id" doc:"Download ID"`
	Body struct {
		Path string `json:"path" minLength:"1" doc:"File path to generate link for"`
	}
}

// --- DTO types ---

type AddDownloadDTO struct {
	DownloadID string `json:"download_id" doc:"Download ID"`
	CacheHit   bool   `json:"cache_hit" doc:"Whether the file was already cached"`
	NodeID     string `json:"node_id" doc:"Node handling the download"`
	Status     string `json:"status" doc:"Job status"`
}

type DownloadDTO struct {
	ID        string `json:"id" doc:"Download ID"`
	Name      string `json:"name" doc:"Download name"`
	URL       string `json:"url" doc:"Download URL"`
	Engine    string `json:"engine" doc:"Download engine"`
	Status    string `json:"status" doc:"Job status"`
	NodeID    string `json:"node_id" doc:"Node ID"`
	Error     string `json:"error,omitempty" doc:"Error message"`
	CreatedAt string `json:"created_at" doc:"Creation time"`

	Size           *int64  `json:"size,omitempty" doc:"Total file size in bytes (null when unknown)"`
	Progress       float64 `json:"progress" doc:"Download progress 0-1"`
	Speed          int64   `json:"speed" doc:"Download speed in bytes/sec"`
	DownloadedSize int64   `json:"downloaded_size" doc:"Downloaded bytes"`
	ETA            int64   `json:"eta" doc:"Estimated time remaining in seconds"`
	CachedAt       *string `json:"cached_at,omitempty" doc:"Time the job completed (null if not yet done)"`
}

type DownloadDetailDTO struct {
	DownloadDTO
	Files []FileDTO `json:"files" doc:"Files in this download (empty while in progress)"`
}

type FilesDTO struct {
	Status string    `json:"status" doc:"Job status"`
	Files  []FileDTO `json:"files" doc:"List of files"`
}

type FileDTO struct {
	Path string `json:"path" doc:"Relative file path"`
	Size int64  `json:"size" doc:"File size in bytes"`
}

type LinkDTO struct {
	URL       string    `json:"url" doc:"Download URL"`
	Token     string    `json:"token" doc:"Download token"`
	ExpiresAt time.Time `json:"expires_at" doc:"Link expiry time"`
}

// toDownloadDTO builds a DownloadDTO from the common fields present on all
// download query rows (ListDownloadsByUserRow, GetDownloadWithJobByUserRow).
func toDownloadDTO(
	downloadID pgtype.UUID,
	name, url, engine, status, nodeID string,
	errorMsg pgtype.Text,
	downloadCreatedAt, completedAt pgtype.Timestamptz,
	size pgtype.Int8,
	progress float64,
	speed, downloadedSize int64,
) DownloadDTO {
	dto := DownloadDTO{
		ID:        util.UUIDToStr(downloadID),
		Name:      name,
		URL:       url,
		Engine:    engine,
		Status:    status,
		NodeID:    nodeID,
		Error:     errorMsg.String,
		CreatedAt: downloadCreatedAt.Time.Format(time.RFC3339),
	}
	if size.Valid {
		dto.Size = &size.Int64
	}
	if completedAt.Valid {
		t := completedAt.Time.Format(time.RFC3339)
		dto.CachedAt = &t
	}
	switch status {
	case "completed":
		dto.Progress = 1.0
		if size.Valid {
			dto.DownloadedSize = size.Int64
		}
	default:
		dto.Progress = progress
		dto.Speed = speed
		dto.DownloadedSize = downloadedSize
		if speed > 0 && size.Valid && downloadedSize < size.Int64 {
			dto.ETA = (size.Int64 - downloadedSize) / speed
		}
	}
	return dto
}

// --- Handlers ---

func (h *DownloadsHandler) Add(ctx context.Context, input *AddDownloadInput) (*DataOutput[AddDownloadDTO], error) {
	userID := middleware.GetUserID(ctx)

	resp, err := h.svc.Add(ctx, service.AddDownloadRequest{
		URL:     input.Body.URL,
		Engine:  input.Body.Engine,
		UserID:  userID,
		Options: input.Body.Options,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return OK(AddDownloadDTO{
		DownloadID: resp.DownloadID,
		CacheHit:   resp.CacheHit,
		NodeID:     resp.NodeID,
		Status:     resp.Status,
	}), nil
}

func (h *DownloadsHandler) List(ctx context.Context, input *ListDownloadsInput) (*DataOutput[[]DownloadDTO], error) {
	userID := middleware.GetUserID(ctx)

	var results []service.DownloadWithStatus
	var err error

	if input.Engine != "" {
		results, err = h.svc.ListByUserAndEngineWithStatus(ctx, userID, input.Engine, int32(input.Limit), int32(input.Offset))
	} else {
		results, err = h.svc.ListByUserWithStatus(ctx, userID, int32(input.Limit), int32(input.Offset))
	}
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	dtos := make([]DownloadDTO, len(results))
	for i, r := range results {
		d := r.Download
		dtos[i] = toDownloadDTO(
			d.DownloadID, d.Name, d.Url, d.Engine, d.Status, d.NodeID,
			d.ErrorMessage, d.DownloadCreatedAt, d.CompletedAt,
			d.Size, d.Progress, d.Speed, d.DownloadedSize,
		)
	}

	return OK(dtos), nil
}

func (h *DownloadsHandler) Get(ctx context.Context, input *DownloadIDInput) (*DataOutput[DownloadDetailDTO], error) {
	userID := middleware.GetUserID(ctx)

	var (
		row    *gen.GetDownloadWithJobByUserRow
		rowErr error
		files  *service.ListFilesResult
	)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		row, rowErr = h.svc.GetDownload(ctx, input.ID, userID)
	}()
	go func() {
		defer wg.Done()
		files, _ = h.svc.ListFiles(ctx, input.ID, userID)
	}()
	wg.Wait()

	if rowErr != nil {
		return nil, huma.Error404NotFound(rowErr.Error())
	}
	dto := DownloadDetailDTO{
		DownloadDTO: toDownloadDTO(
			row.DownloadID, row.Name, row.Url, row.Engine, row.Status, row.NodeID,
			row.ErrorMessage, row.DownloadCreatedAt, row.CompletedAt,
			row.Size, row.Progress, row.Speed, row.DownloadedSize,
		),
		Files: []FileDTO{},
	}

	if files != nil {
		fileDTOs := make([]FileDTO, len(files.Files))
		for i, f := range files.Files {
			fileDTOs[i] = FileDTO{Path: f.Path, Size: f.Size}
		}
		dto.Files = fileDTOs
	}

	return OK(dto), nil
}

func (h *DownloadsHandler) Files(ctx context.Context, input *DownloadIDInput) (*DataOutput[FilesDTO], error) {
	userID := middleware.GetUserID(ctx)

	result, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	files := make([]FileDTO, len(result.Files))
	for i, f := range result.Files {
		files[i] = FileDTO{Path: f.Path, Size: f.Size}
	}

	return OK(FilesDTO{Status: result.Status, Files: files}), nil
}

func (h *DownloadsHandler) GenerateLink(ctx context.Context, input *GenerateLinkInput) (*DataOutput[LinkDTO], error) {
	userID := middleware.GetUserID(ctx)

	result, err := h.svc.GenerateLink(ctx, service.GenerateLinkRequest{
		DownloadID: input.ID,
		UserID:     userID,
		FilePath:   input.Body.Path,
	})
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}

	return OK(LinkDTO{
		URL:       result.URL,
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
	}), nil
}

func (h *DownloadsHandler) Retry(ctx context.Context, input *DownloadIDInput) (*DataOutput[AddDownloadDTO], error) {
	userID := middleware.GetUserID(ctx)

	row, err := h.svc.GetDownload(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound("download not found")
	}
	if row.Status != "failed" && row.Status != "inactive" {
		return nil, huma.Error400BadRequest("only failed or inactive downloads can be retried")
	}

	resp, err := h.svc.Add(ctx, service.AddDownloadRequest{
		URL:    row.Url,
		Engine: row.Engine,
		UserID: userID,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return OK(AddDownloadDTO{
		DownloadID: resp.DownloadID,
		CacheHit:   resp.CacheHit,
		NodeID:     resp.NodeID,
		Status:     resp.Status,
	}), nil
}

func (h *DownloadsHandler) Delete(ctx context.Context, input *DownloadIDInput) (*MsgOutput, error) {
	userID := middleware.GetUserID(ctx)
	log.Debug().Str("download_id", input.ID).Str("user_id", userID).Msg("delete download request")

	if err := h.svc.Delete(ctx, input.ID, userID); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	return Msg("deleted"), nil
}
