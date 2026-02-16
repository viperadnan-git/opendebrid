package handlers

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/core/service"
)

type Aria2Handler struct {
	svc *service.DownloadService
}

func NewAria2Handler(svc *service.DownloadService) *Aria2Handler {
	return &Aria2Handler{svc: svc}
}

func (h *Aria2Handler) Add(c echo.Context) error {
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
		Engine:  "aria2",
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

func (h *Aria2Handler) ListJobs(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if limit <= 0 {
		limit = 20
	}

	jobs, err := h.svc.ListByUserAndEngine(c.Request().Context(), userID, "aria2", int32(limit), int32(offset))
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "LIST_ERROR", err.Error())
	}

	return response.Success(c, http.StatusOK, jobs)
}

func (h *Aria2Handler) GetJob(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	status, err := h.svc.Status(c.Request().Context(), jobID, userID)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}

	return response.Success(c, http.StatusOK, status)
}

func (h *Aria2Handler) GetJobFiles(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	files, err := h.svc.ListFiles(c.Request().Context(), jobID, userID)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}

	return response.Success(c, http.StatusOK, files)
}

func (h *Aria2Handler) DeleteJob(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	if err := h.svc.Cancel(c.Request().Context(), jobID, userID); err != nil {
		return response.Error(c, http.StatusInternalServerError, "CANCEL_ERROR", err.Error())
	}

	return response.Success(c, http.StatusOK, map[string]string{"status": "cancelled"})
}
