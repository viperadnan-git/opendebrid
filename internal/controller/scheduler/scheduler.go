package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/service"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

const nodesCacheTTL = 10 * time.Second

type Scheduler struct {
	queries     *gen.Queries
	adapter     LoadBalancer
	localNodeID string

	mu          sync.Mutex
	cachedNodes []gen.Node
	cachedAt    time.Time
}

func NewScheduler(db *pgxpool.Pool, adapter LoadBalancer, localNodeID string) *Scheduler {
	return &Scheduler{
		queries:     gen.New(db),
		adapter:     adapter,
		localNodeID: localNodeID,
	}
}

func (s *Scheduler) SelectNode(ctx context.Context, req service.NodeSelectRequest) (service.NodeSelection, error) {
	nodes, err := s.onlineNodes(ctx)
	if err != nil {
		return service.NodeSelection{}, fmt.Errorf("list nodes: %w", err)
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
		return service.NodeSelection{}, fmt.Errorf("no eligible nodes for engine %q", req.Engine)
	}

	// 3. If preferred node is among candidates, use it directly
	if req.PreferredNode != "" {
		for _, c := range candidates {
			if c.ID == req.PreferredNode {
				log.Debug().Str("engine", req.Engine).Str("selected", c.ID).Str("reason", "preferred node").Msg("node selected")
				return service.NodeSelection{NodeID: c.ID, Endpoint: c.Endpoint}, nil
			}
		}
	}

	// 4. Delegate to adapter
	selection, err := s.adapter.SelectNode(ctx, candidates)
	if err != nil {
		return service.NodeSelection{}, err
	}

	log.Debug().
		Str("engine", req.Engine).
		Str("selected", selection.NodeID).
		Str("reason", selection.Reason).
		Int("candidates", len(candidates)).
		Msg("node selected")

	return service.NodeSelection{
		NodeID:   selection.NodeID,
		Endpoint: selection.Endpoint,
	}, nil
}

func (s *Scheduler) onlineNodes(ctx context.Context) ([]gen.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cachedNodes != nil && time.Since(s.cachedAt) < nodesCacheTTL {
		return s.cachedNodes, nil
	}
	nodes, err := s.queries.ListOnlineNodes(ctx)
	if err != nil {
		return nil, err
	}
	s.cachedNodes = nodes
	s.cachedAt = time.Now()
	return nodes, nil
}
