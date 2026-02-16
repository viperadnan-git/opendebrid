package scheduler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendebrid/opendebrid/internal/database/gen"
	"github.com/rs/zerolog/log"
)

type Scheduler struct {
	queries     *gen.Queries
	adapter     LoadBalancer
	localNodeID string
}

func NewScheduler(db *pgxpool.Pool, adapter LoadBalancer, localNodeID string) *Scheduler {
	return &Scheduler{
		queries:     gen.New(db),
		adapter:     adapter,
		localNodeID: localNodeID,
	}
}

func (s *Scheduler) SelectNode(ctx context.Context, req SelectRequest) (NodeSelection, error) {
	// 1. Get all online nodes from DB
	nodes, err := s.queries.ListOnlineNodes(ctx)
	if err != nil {
		return NodeSelection{}, fmt.Errorf("list nodes: %w", err)
	}

	// 2. Pre-filter: online, has engine, has disk
	var candidates []NodeInfo
	for _, n := range nodes {
		// Check if node has the requested engine
		var engines []string
		if err := json.Unmarshal([]byte(n.Engines), &engines); err != nil {
			continue
		}

		hasEngine := false
		for _, e := range engines {
			if e == req.Engine {
				hasEngine = true
				break
			}
		}
		if !hasEngine {
			continue
		}

		// Check disk space (skip if estimated size unknown)
		if req.EstimatedSize > 0 && n.DiskAvailable < req.EstimatedSize {
			continue
		}

		candidates = append(candidates, NodeInfo{
			ID:            n.ID,
			Endpoint:      n.FileEndpoint,
			IsLocal:       n.ID == s.localNodeID,
			Engines:       engines,
			DiskAvailable: n.DiskAvailable,
		})
	}

	if len(candidates) == 0 {
		return NodeSelection{}, fmt.Errorf("no eligible nodes for engine %q", req.Engine)
	}

	// 3. Delegate to adapter
	selection, err := s.adapter.SelectNode(ctx, req, candidates)
	if err != nil {
		return NodeSelection{}, err
	}

	log.Debug().
		Str("engine", req.Engine).
		Str("selected", selection.NodeID).
		Str("reason", selection.Reason).
		Int("candidates", len(candidates)).
		Msg("node selected")

	return selection, nil
}
