package handlers

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/core/service"
)

type DownloadsHandler struct {
	svc *service.DownloadService
}

func NewDownloadsHandler(svc *service.DownloadService) *DownloadsHandler {
	return &DownloadsHandler{svc: svc}
}

func (h *DownloadsHandler) List(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if limit <= 0 {
		limit = 20
	}

	engine := c.QueryParam("engine")
	var jobs interface{}
	var err error

	if engine != "" {
		jobs, err = h.svc.ListByUserAndEngine(c.Request().Context(), userID, engine, int32(limit), int32(offset))
	} else {
		jobs, err = h.svc.ListByUser(c.Request().Context(), userID, int32(limit), int32(offset))
	}
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "LIST_ERROR", err.Error())
	}

	return response.Success(c, http.StatusOK, jobs)
}

func (h *DownloadsHandler) Get(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	jobID := c.Param("id")

	status, err := h.svc.Status(c.Request().Context(), jobID, userID)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}

	return response.Success(c, http.StatusOK, status)
}
