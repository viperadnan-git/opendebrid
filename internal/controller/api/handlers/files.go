package handlers

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/core/fileserver"
	"github.com/opendebrid/opendebrid/internal/core/service"
)

type FilesHandler struct {
	svc         *service.DownloadService
	signer      *fileserver.Signer
	linkExpiry  time.Duration
	fileBaseURL string
}

func NewFilesHandler(svc *service.DownloadService, signer *fileserver.Signer, linkExpiry time.Duration, fileBaseURL string) *FilesHandler {
	return &FilesHandler{
		svc:         svc,
		signer:      signer,
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

	// Verify user has access to this job
	if _, err := h.svc.Status(c.Request().Context(), jobID, userID); err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", "job not found")
	}

	expiry := time.Now().Add(h.linkExpiry)
	token := h.signer.Sign(jobID, req.Path, userID, expiry)
	url := h.fileBaseURL + "/d/" + token

	return response.Success(c, http.StatusOK, map[string]any{
		"url":        url,
		"token":      token,
		"expires_at": expiry,
	})
}
