package handlers

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

type UsersHandler struct {
	queries *gen.Queries
}

func NewUsersHandler(db *pgxpool.Pool) *UsersHandler {
	return &UsersHandler{queries: gen.New(db)}
}

func (h *UsersHandler) List(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if limit <= 0 {
		limit = 20
	}

	users, err := h.queries.ListUsers(c.Request().Context(), gen.ListUsersParams{
		Limit:  int32(limit),
		Offset: int32(offset),
	})
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "LIST_ERROR", err.Error())
	}

	// Strip passwords from response
	type safeUser struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
		Role     string `json:"role"`
		IsActive bool   `json:"is_active"`
	}
	result := make([]safeUser, len(users))
	for i, u := range users {
		result[i] = safeUser{
			ID:       pgUUIDToString(u.ID),
			Username: u.Username,
			Email:    u.Email.String,
			Role:     u.Role,
			IsActive: u.IsActive,
		}
	}

	return response.Success(c, http.StatusOK, result)
}

func (h *UsersHandler) Delete(c echo.Context) error {
	userID := c.Param("id")
	uid := pgUUID(userID)

	if err := h.queries.DeleteUser(c.Request().Context(), uid); err != nil {
		return response.Error(c, http.StatusInternalServerError, "DELETE_ERROR", err.Error())
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "deleted"})
}
