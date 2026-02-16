package handlers

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/core/engine/ytdlp"
	"github.com/opendebrid/opendebrid/internal/core/service"
)

type YtDlpHandler struct {
	svc    *service.DownloadService
	engine *ytdlp.Engine
}

func NewYtDlpHandler(svc *service.DownloadService, engine *ytdlp.Engine) *YtDlpHandler {
	return &YtDlpHandler{svc: svc, engine: engine}
}

func (h *YtDlpHandler) Add(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())

	var req struct {
		URL     string            `json:"url"`
		Options map[string]string `json:"options"`
	}
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "INVALID_INPUT", "invalid request body")
	}
	if req.URL == "" {
		return response.Error(c, http.StatusBadRequest, "MISSING_URL", "url is required")
	}

	resp, err := h.svc.Add(c.Request().Context(), service.AddDownloadRequest{
		URL:     req.URL,
		Engine:  "ytdlp",
		UserID:  userID,
		Options: req.Options,
	})
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "ADD_ERROR", err.Error())
	}

	status := http.StatusCreated
	if resp.CacheHit {
		status = http.StatusOK
	}
	return response.Success(c, status, resp)
}

func (h *YtDlpHandler) Info(c echo.Context) error {
	var req struct {
		URL string `json:"url"`
	}
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "INVALID_INPUT", "invalid request body")
	}
	if req.URL == "" {
		return response.Error(c, http.StatusBadRequest, "MISSING_URL", "url is required")
	}

	info, err := h.engine.Info(c.Request().Context(), req.URL)
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "INFO_ERROR", err.Error())
	}

	return response.Success(c, http.StatusOK, info)
}

func (h *YtDlpHandler) ListJobs(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if limit <= 0 {
		limit = 20
	}

	jobs, err := h.svc.ListByUserAndEngine(c.Request().Context(), userID, "ytdlp", int32(limit), int32(offset))
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "LIST_ERROR", err.Error())
	}

	return response.Success(c, http.StatusOK, jobs)
}

func (h *YtDlpHandler) GetJob(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	status, err := h.svc.Status(c.Request().Context(), jobID, userID)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}

	return response.Success(c, http.StatusOK, status)
}

func (h *YtDlpHandler) GetJobFiles(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	files, err := h.svc.ListFiles(c.Request().Context(), jobID, userID)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}

	return response.Success(c, http.StatusOK, files)
}

func (h *YtDlpHandler) DeleteJob(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	if err := h.svc.Cancel(c.Request().Context(), jobID, userID); err != nil {
		return response.Error(c, http.StatusInternalServerError, "CANCEL_ERROR", err.Error())
	}

	return response.Success(c, http.StatusOK, map[string]string{"status": "cancelled"})
}
