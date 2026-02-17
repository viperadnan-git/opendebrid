package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
)

type RoundRobin struct {
	counter atomic.Uint64
}

func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

func (r *RoundRobin) Name() string { return "round-robin" }

func (r *RoundRobin) SelectNode(_ context.Context, candidates []NodeInfo) (NodeSelection, error) {
	if len(candidates) == 0 {
		return NodeSelection{}, fmt.Errorf("no candidate nodes available")
	}

	idx := r.counter.Add(1) - 1
	selected := candidates[idx%uint64(len(candidates))]

	return NodeSelection{
		NodeID:   selected.ID,
		Endpoint: selected.Endpoint,
		Reason:   fmt.Sprintf("round-robin (index %d)", idx),
	}, nil
}
