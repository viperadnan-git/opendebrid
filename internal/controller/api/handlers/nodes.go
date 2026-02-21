package handlers

import (
	"context"
	"encoding/json"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

type NodesHandler struct {
	queries *gen.Queries
}

func NewNodesHandler(db *pgxpool.Pool) *NodesHandler {
	return &NodesHandler{queries: gen.New(db)}
}

type NodeIDInput struct {
	ID string `path:"id" doc:"Node ID"`
}

type NodeDTO struct {
	ID            string   `json:"id" doc:"Node ID"`
	Engines       []string `json:"engines" doc:"Supported engines"`
	IsOnline      bool     `json:"is_online" doc:"Whether node is online"`
	IsController  bool     `json:"is_controller" doc:"Whether node is the controller"`
	DiskTotal     int64    `json:"disk_total" doc:"Total disk bytes"`
	DiskAvailable int64    `json:"disk_available" doc:"Available disk bytes"`
}

func (h *NodesHandler) List(ctx context.Context, _ *EmptyInput) (*DataOutput[[]NodeDTO], error) {
	nodes, err := h.queries.ListNodes(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}

	dtos := make([]NodeDTO, len(nodes))
	for i, n := range nodes {
		var engines []string
		_ = json.Unmarshal([]byte(n.Engines), &engines)
		dtos[i] = NodeDTO{
			ID:            n.ID,
			Engines:       engines,
			IsOnline:      n.IsOnline,
			IsController:  n.IsController,
			DiskTotal:     n.DiskTotal,
			DiskAvailable: n.DiskAvailable,
		}
	}

	return OK(dtos), nil
}

func (h *NodesHandler) Get(ctx context.Context, input *NodeIDInput) (*DataOutput[NodeDTO], error) {
	n, err := h.queries.GetNode(ctx, input.ID)
	if err != nil {
		return nil, huma.Error404NotFound("node not found")
	}

	var engines []string
	_ = json.Unmarshal([]byte(n.Engines), &engines)

	return OK(NodeDTO{
		ID:            n.ID,
		Engines:       engines,
		IsOnline:      n.IsOnline,
		IsController:  n.IsController,
		DiskTotal:     n.DiskTotal,
		DiskAvailable: n.DiskAvailable,
	}), nil
}

func (h *NodesHandler) Delete(ctx context.Context, input *NodeIDInput) (*MsgOutput, error) {
	n, err := h.queries.GetNode(ctx, input.ID)
	if err != nil {
		return nil, huma.Error404NotFound("node not found")
	}
	if n.IsOnline {
		return nil, huma.Error409Conflict("node is online; take it offline before deleting")
	}
	if err := h.queries.FailNodeJobsForDeletion(ctx, util.ToText(input.ID)); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}
	if err := h.queries.DeleteNode(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}
	return Msg("deleted"), nil
}
