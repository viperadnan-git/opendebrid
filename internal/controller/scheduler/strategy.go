package scheduler

import "sync/atomic"

// Candidate is a pre-filtered node eligible for selection.
type Candidate struct {
	ID            string
	Endpoint      string
	DiskAvailable int64
	DiskTotal     int64
	Engines       []string
}

// Strategy picks one node from a pre-filtered list of candidates.
// Implementations can assume len(candidates) > 0.
type Strategy interface {
	Select(candidates []Candidate) Candidate
}

// RoundRobin selects candidates in rotating order.
type RoundRobin struct {
	counter atomic.Uint64
}

func NewRoundRobin() *RoundRobin { return &RoundRobin{} }

func (rr *RoundRobin) Select(candidates []Candidate) Candidate {
	idx := rr.counter.Add(1) - 1
	return candidates[idx%uint64(len(candidates))]
}
