package handlers

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/controller/api/middleware"
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
	uid := pgUUID(userID)

	activeJobs, _ := h.queries.CountActiveDownloadsByUser(ctx, uid)
	completedJobs, _ := h.queries.CountCompletedDownloadsByUser(ctx, uid)
	totalJobs, _ := h.queries.CountDownloadsByUser(ctx, uid)

	dto := StatsDTO{
		User: UserStats{
			ActiveJobs:    activeJobs,
			CompletedJobs: completedJobs,
			TotalJobs:     totalJobs,
		},
	}

	if middleware.GetUserRole(ctx) == "admin" {
		totalUsers, _ := h.queries.CountUsers(ctx)
		onlineNodes, _ := h.queries.CountOnlineNodes(ctx)
		totalNodes, _ := h.queries.CountAllNodes(ctx)
		allActive, _ := h.queries.CountAllActiveJobs(ctx)
		disk, _ := h.queries.SumNodeDisk(ctx)

		dto.Admin = &AdminStats{
			TotalUsers:    totalUsers,
			OnlineNodes:   onlineNodes,
			TotalNodes:    totalNodes,
			ActiveJobs:    allActive,
			DiskTotal:     disk.Total,
			DiskAvailable: disk.Available,
		}
	}

	return OK(dto), nil
}
