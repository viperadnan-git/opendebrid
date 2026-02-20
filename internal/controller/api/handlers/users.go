package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/core/service"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

type UsersHandler struct {
	queries *gen.Queries
	svc     *service.DownloadService
}

func NewUsersHandler(db *pgxpool.Pool, svc *service.DownloadService) *UsersHandler {
	return &UsersHandler{queries: gen.New(db), svc: svc}
}

type SafeUser struct {
	ID       string `json:"id" doc:"User ID"`
	Username string `json:"username" doc:"Username"`
	Email    string `json:"email" doc:"Email"`
	Role     string `json:"role" doc:"User role"`
	IsActive bool   `json:"is_active" doc:"Whether user is active"`
}

type UserIDInput struct {
	ID string `path:"id" doc:"User ID"`
}

func (h *UsersHandler) List(ctx context.Context, input *ListDownloadsInput) (*DataOutput[[]SafeUser], error) {
	users, err := h.queries.ListUsers(ctx, gen.ListUsersParams{
		Limit:  int32(input.Limit),
		Offset: int32(input.Offset),
	})
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	result := make([]SafeUser, len(users))
	for i, u := range users {
		result[i] = SafeUser{
			ID:       util.UUIDToStr(u.ID),
			Username: u.Username,
			Email:    u.Email,
			Role:     u.Role,
			IsActive: u.IsActive,
		}
	}

	return OK(result), nil
}

func (h *UsersHandler) Delete(ctx context.Context, input *UserIDInput) (*MsgOutput, error) {
	if err := h.svc.DeleteUser(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}
	return Msg("deleted"), nil
}
