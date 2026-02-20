package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/controller/api/middleware"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

type StatsHandler struct {
	queries *gen.Queries
}

func NewStatsHandler(db *pgxpool.Pool) *StatsHandler {
	return &StatsHandler{queries: gen.New(db)}
}

type UserStats struct {
	ActiveJobs    int64 `json:"active_jobs"`
	CompletedJobs int64 `json:"completed_jobs"`
	TotalJobs     int64 `json:"total_jobs"`
}

type AdminStats struct {
	TotalUsers    int64 `json:"total_users"`
	OnlineNodes   int64 `json:"online_nodes"`
	TotalNodes    int64 `json:"total_nodes"`
	ActiveJobs    int64 `json:"active_jobs"`
	DiskTotal     int64 `json:"disk_total"`
	DiskAvailable int64 `json:"disk_available"`
}

type StatsDTO struct {
	User  UserStats   `json:"user"`
	Admin *AdminStats `json:"admin,omitempty"`
}

func (h *StatsHandler) Get(ctx context.Context, _ *EmptyInput) (*DataOutput[StatsDTO], error) {
	userID := middleware.GetUserID(ctx)
	uid := util.TextToUUID(userID)

	userStats, err := h.queries.GetUserDownloadStats(ctx, uid)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to fetch user stats")
	}

	dto := StatsDTO{
		User: UserStats{
			ActiveJobs:    userStats.Active,
			CompletedJobs: userStats.Completed,
			TotalJobs:     userStats.Total,
		},
	}

	if middleware.GetUserRole(ctx) == "admin" {
		admin, err := h.queries.GetAdminStats(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to fetch admin stats")
		}

		dto.Admin = &AdminStats{
			TotalUsers:    admin.TotalUsers,
			OnlineNodes:   admin.OnlineNodes,
			TotalNodes:    admin.TotalNodes,
			ActiveJobs:    admin.ActiveJobs,
			DiskTotal:     admin.DiskTotal,
			DiskAvailable: admin.DiskAvailable,
		}
	}

	return OK(dto), nil
}
