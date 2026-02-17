package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/core/service"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

type FilesHandler struct {
	svc         *service.DownloadService
	queries     *gen.Queries
	linkExpiry  time.Duration
	fileBaseURL string
}

func NewFilesHandler(svc *service.DownloadService, db *pgxpool.Pool, linkExpiry time.Duration, fileBaseURL string) *FilesHandler {
	return &FilesHandler{
		svc:         svc,
		queries:     gen.New(db),
		linkExpiry:  linkExpiry,
		fileBaseURL: fileBaseURL,
	}
}

func (h *FilesHandler) Browse(ctx context.Context, input *JobIDInput) (*FilesOutput, error) {
	userID := middleware.GetUserID(ctx)

	files, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	return &FilesOutput{Body: []any{files}}, nil
}

type GenerateLinkInput struct {
	ID   string `path:"id" doc:"Job ID"`
	Body struct {
		Path string `json:"path" minLength:"1" doc:"File path to generate link for"`
	}
}

type GenerateLinkBody struct {
	URL       string    `json:"url" doc:"Download URL"`
	Token     string    `json:"token" doc:"Download token"`
	ExpiresAt time.Time `json:"expires_at" doc:"Link expiry time"`
}

type GenerateLinkOutput struct {
	Body GenerateLinkBody
}

func (h *FilesHandler) GenerateLink(ctx context.Context, input *GenerateLinkInput) (*GenerateLinkOutput, error) {
	userID := middleware.GetUserID(ctx)

	files, err := h.svc.ListFiles(ctx, input.ID, userID)
	if err != nil {
		return nil, huma.Error404NotFound("job not found")
	}

	var storagePath string
	for _, f := range files {
		if f.Path == input.Body.Path {
			storagePath = strings.TrimPrefix(f.StorageURI, "file://")
			break
		}
	}
	if storagePath == "" {
		return nil, huma.Error404NotFound("file not found")
	}

	token, err := generateToken()
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to generate token")
	}

	expiry := time.Now().Add(h.linkExpiry)
	link, err := h.queries.CreateDownloadLink(ctx, gen.CreateDownloadLinkParams{
		UserID:    strToUUID(userID),
		JobID:     strToUUID(input.ID),
		FilePath:  storagePath,
		Token:     token,
		ExpiresAt: pgtype.Timestamptz{Time: expiry, Valid: true},
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to create download link")
	}

	filename := filepath.Base(input.Body.Path)
	url := h.fileBaseURL + "/dl/" + link.Token + "/" + filename

	return &GenerateLinkOutput{Body: GenerateLinkBody{
		URL:       url,
		Token:     link.Token,
		ExpiresAt: expiry,
	}}, nil
}

func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func strToUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	cleaned := ""
	for _, c := range s {
		if c != '-' {
			cleaned += string(c)
		}
	}
	if len(cleaned) != 32 {
		return u
	}
	for i := range 16 {
		b := hexVal(cleaned[i*2])<<4 | hexVal(cleaned[i*2+1])
		u.Bytes[i] = b
	}
	u.Valid = true
	return u
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
