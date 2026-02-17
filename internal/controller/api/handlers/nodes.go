package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendebrid/opendebrid/internal/database/gen"
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

func (h *NodesHandler) List(ctx context.Context, input *EmptyInput) (*ListJobsOutput, error) {
	nodes, err := h.queries.ListNodes(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}
	return &ListJobsOutput{Body: []any{nodes}}, nil
}

func (h *NodesHandler) Get(ctx context.Context, input *NodeIDInput) (*struct{ Body any }, error) {
	node, err := h.queries.GetNode(ctx, input.ID)
	if err != nil {
		return nil, huma.Error404NotFound("node not found")
	}
	return &struct{ Body any }{Body: node}, nil
}

func (h *NodesHandler) Delete(ctx context.Context, input *NodeIDInput) (*StatusOutput, error) {
	if err := h.queries.DeleteNode(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError(err.Error())
	}
	return &StatusOutput{Body: StatusBody{Status: "deleted"}}, nil
}
