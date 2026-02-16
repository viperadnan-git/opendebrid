package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
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

func (h *FilesHandler) Browse(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	files, err := h.svc.ListFiles(c.Request().Context(), jobID, userID)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}

	return response.Success(c, http.StatusOK, files)
}

func (h *FilesHandler) GenerateLink(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	var req struct {
		Path string `json:"path"`
	}
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "INVALID_INPUT", "invalid request body")
	}
	if req.Path == "" {
		return response.Error(c, http.StatusBadRequest, "MISSING_PATH", "path is required")
	}

	// List files to find the StorageURI for the requested path
	files, err := h.svc.ListFiles(c.Request().Context(), jobID, userID)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", "job not found")
	}

	var storagePath string
	for _, f := range files {
		if f.Path == req.Path {
			storagePath = strings.TrimPrefix(f.StorageURI, "file://")
			break
		}
	}
	if storagePath == "" {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", "file not found")
	}

	// Generate random token
	token, err := generateToken()
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "INTERNAL", "failed to generate token")
	}

	expiry := time.Now().Add(h.linkExpiry)
	link, err := h.queries.CreateDownloadLink(c.Request().Context(), gen.CreateDownloadLinkParams{
		UserID:    strToUUID(userID),
		JobID:     strToUUID(jobID),
		FilePath:  storagePath,
		Token:     token,
		ExpiresAt: pgtype.Timestamptz{Time: expiry, Valid: true},
	})
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "INTERNAL", "failed to create download link")
	}

	filename := filepath.Base(req.Path)
	url := h.fileBaseURL + "/dl/" + link.Token + "/" + filename

	return response.Success(c, http.StatusOK, map[string]any{
		"url":        url,
		"token":      link.Token,
		"expires_at": expiry,
	})
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
	for i := 0; i < 16; i++ {
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
