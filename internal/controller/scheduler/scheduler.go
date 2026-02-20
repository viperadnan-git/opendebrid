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
	"golang.org/x/sync/singleflight"
)

const nodesCacheTTL = 10 * time.Second

// cachedNode holds a DB node with its engines pre-parsed from JSON.
type cachedNode struct {
	gen.Node
	engines []string
}

type Scheduler struct {
	queries     *gen.Queries
	localNodeID string
	strategy    Strategy

	mu          sync.Mutex
	cachedNodes []cachedNode
	cachedAt    time.Time
	sf          singleflight.Group
}

func NewScheduler(db *pgxpool.Pool, localNodeID string, strategy Strategy) *Scheduler {
	return &Scheduler{
		queries:     gen.New(db),
		localNodeID: localNodeID,
		strategy:    strategy,
	}
}

func (s *Scheduler) SelectNode(ctx context.Context, req service.NodeSelectRequest) (service.NodeSelection, error) {
	nodes, err := s.onlineNodes(ctx)
	if err != nil {
		return service.NodeSelection{}, fmt.Errorf("list nodes: %w", err)
	}

	// Pre-filter: has engine, has disk
	var candidates []Candidate
	for _, n := range nodes {
		hasEngine := false
		for _, e := range n.engines {
			if e == req.Engine {
				hasEngine = true
				break
			}
		}
		if !hasEngine {
			continue
		}

		if req.EstimatedSize > 0 && n.DiskAvailable < req.EstimatedSize {
			continue
		}

		candidates = append(candidates, Candidate{
			ID:            n.ID,
			Endpoint:      n.FileEndpoint,
			DiskAvailable: n.DiskAvailable,
			DiskTotal:     n.DiskTotal,
			Engines:       n.engines,
		})
	}

	if len(candidates) == 0 {
		return service.NodeSelection{}, fmt.Errorf("no eligible nodes for engine %q", req.Engine)
	}

	// If preferred node is among candidates, use it directly
	if req.PreferredNode != "" {
		for _, c := range candidates {
			if c.ID == req.PreferredNode {
				log.Debug().Str("engine", req.Engine).Str("selected", c.ID).Str("reason", "preferred").Msg("node selected")
				return service.NodeSelection{NodeID: c.ID, Endpoint: c.Endpoint}, nil
			}
		}
	}

	selected := s.strategy.Select(candidates)

	log.Debug().
		Str("engine", req.Engine).
		Str("selected", selected.ID).
		Int("candidates", len(candidates)).
		Msg("node selected")

	return service.NodeSelection{
		NodeID:   selected.ID,
		Endpoint: selected.Endpoint,
	}, nil
}

func (s *Scheduler) onlineNodes(ctx context.Context) ([]cachedNode, error) {
	s.mu.Lock()
	if s.cachedNodes != nil && time.Since(s.cachedAt) < nodesCacheTTL {
		nodes := s.cachedNodes
		s.mu.Unlock()
		return nodes, nil
	}
	s.mu.Unlock()

	v, err, _ := s.sf.Do("nodes", func() (any, error) {
		nodes, err := s.queries.ListOnlineNodes(ctx)
		if err != nil {
			return nil, err
		}
		cached := make([]cachedNode, 0, len(nodes))
		for _, n := range nodes {
			var engines []string
			if err := json.Unmarshal([]byte(n.Engines), &engines); err != nil {
				continue
			}
			cached = append(cached, cachedNode{Node: n, engines: engines})
		}

		s.mu.Lock()
		s.cachedNodes = cached
		s.cachedAt = time.Now()
		s.mu.Unlock()

		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]cachedNode), nil
}
