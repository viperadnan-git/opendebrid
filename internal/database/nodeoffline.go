package database

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

// MarkNodeOffline sets a node as offline and updates its jobs:
// active/queued jobs are marked failed, completed jobs are marked inactive.
func MarkNodeOffline(ctx context.Context, q *gen.Queries, nodeID string) {
	if err := q.SetNodeOffline(ctx, nodeID); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("failed to mark node offline")
	}
	MarkNodeJobsForOffline(ctx, q, nodeID)
}

// MarkNodeJobsForOffline marks active jobs as failed and completed jobs as inactive
// for a node that has already been set offline (e.g. by MarkStaleNodesOffline).
func MarkNodeJobsForOffline(ctx context.Context, q *gen.Queries, nodeID string) {
	nid := pgtype.Text{String: nodeID, Valid: true}
	if err := q.MarkNodeActiveJobsFailed(ctx, nid); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("failed to mark active jobs failed")
	}
	if err := q.MarkNodeCompletedJobsInactive(ctx, nid); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("failed to mark completed jobs inactive")
	}
}
