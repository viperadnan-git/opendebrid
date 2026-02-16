package handlers

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

type NodesHandler struct {
	queries *gen.Queries
}

func NewNodesHandler(db *pgxpool.Pool) *NodesHandler {
	return &NodesHandler{queries: gen.New(db)}
}

func (h *NodesHandler) List(c echo.Context) error {
	nodes, err := h.queries.ListNodes(c.Request().Context())
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "LIST_ERROR", err.Error())
	}
	return response.Success(c, http.StatusOK, nodes)
}

func (h *NodesHandler) Get(c echo.Context) error {
	nodeID := c.Param("id")
	node, err := h.queries.GetNode(c.Request().Context(), nodeID)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", "node not found")
	}
	return response.Success(c, http.StatusOK, node)
}

func (h *NodesHandler) Delete(c echo.Context) error {
	nodeID := c.Param("id")
	if err := h.queries.DeleteNode(c.Request().Context(), nodeID); err != nil {
		return response.Error(c, http.StatusInternalServerError, "DELETE_ERROR", err.Error())
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "deleted"})
}
