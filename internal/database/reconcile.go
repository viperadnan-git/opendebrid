package database

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

// ReconcileResult holds the outcome of a node startup reconciliation.
type ReconcileResult struct {
	ValidKeys     []string
	RestoredCount int64
	FailedCount   int64
}

// ReconcileNodeOnStartup performs verified restoration of inactive jobs.
// diskKeys is the set of storage keys actually found on the node's filesystem.
// It restores inactive jobs whose files are confirmed on disk, fails the rest,
// and returns the set of valid storage keys for orphan cleanup.
//
// Callers must ensure MarkNodeActiveJobsFailed has already been called for this
// node (e.g. via Register or MarkNodeJobsForOffline) before invoking this.
func ReconcileNodeOnStartup(ctx context.Context, q *gen.Queries, nodeID string, diskKeys []string) ReconcileResult {
	var result ReconcileResult

	// Restore inactive jobs whose storage key directories exist on disk.
	restored, err := q.RestoreNodeInactiveJobsWithKeys(ctx, gen.RestoreNodeInactiveJobsWithKeysParams{
		NodeID:      nodeID,
		StorageKeys: diskKeys,
	})
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("reconcile: failed to restore inactive jobs with keys")
	}
	result.RestoredCount = restored

	// Fail any remaining inactive jobs (files missing from disk).
	failed, err := q.FailNodeInactiveJobsMissingKeys(ctx, nodeID)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("reconcile: failed to fail inactive jobs missing keys")
	}
	result.FailedCount = failed

	// Fetch the valid storage keys for orphan directory cleanup.
	validKeys, err := q.ListStorageKeysByNode(ctx, nodeID)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("reconcile: failed to list valid storage keys")
	}
	result.ValidKeys = validKeys

	log.Info().
		Str("node_id", nodeID).
		Int64("restored", restored).
		Int64("failed", failed).
		Int("valid_keys", len(validKeys)).
		Msg("node reconciliation complete")

	return result
}
