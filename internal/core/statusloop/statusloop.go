package statusloop

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
)

// Sink receives job status updates.
// The remote implementation sends these over gRPC; the local implementation
// calls controller handlers directly, skipping the network layer.
type Sink interface {
	PushStatuses(ctx context.Context, nodeID string, reports []StatusReport) error
}

// StatusReport is a status update for a single job.
type StatusReport struct {
	JobID          string
	EngineJobID    string
	Status         string
	Progress       float64
	Speed          int64
	TotalSize      int64
	DownloadedSize int64
	Name           string
	Error          string
}

// Tracker tracks active jobs for status polling.
type Tracker struct {
	mu   sync.RWMutex
	jobs map[string]TrackedJob
}

// TrackedJob is a job being tracked for status updates.
type TrackedJob struct {
	JobID       string
	Engine      string
	EngineJobID string
}

func NewTracker() *Tracker {
	return &Tracker{jobs: make(map[string]TrackedJob)}
}

func (t *Tracker) Add(jobID, engineName, engineJobID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.jobs[jobID] = TrackedJob{
		JobID:       jobID,
		Engine:      engineName,
		EngineJobID: engineJobID,
	}
}

func (t *Tracker) Remove(jobID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.jobs, jobID)
}

func (t *Tracker) List() []TrackedJob {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]TrackedJob, 0, len(t.jobs))
	for _, j := range t.jobs {
		result = append(result, j)
	}
	return result
}

// Run starts the status push loop, polling engines every interval.
func Run(ctx context.Context, sink Sink, nodeID string, registry *engine.Registry, tracker *Tracker, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobs := tracker.List()
			if len(jobs) == 0 {
				continue
			}

			statuses := poll(ctx, registry, jobs, tracker)
			if len(statuses) == 0 {
				continue
			}

			if err := sink.PushStatuses(ctx, nodeID, statuses); err != nil {
				log.Warn().Err(err).Msg("status push failed")
			}
		}
	}
}

// poll queries local engines for job status and returns status reports.
func poll(ctx context.Context, registry *engine.Registry,
	jobs []TrackedJob, tracker *Tracker) []StatusReport {

	// Group by engine
	type engineGroup struct {
		engineJobIDs []string
		jobIDs       []string
	}
	groups := make(map[string]*engineGroup)
	for _, j := range jobs {
		g, ok := groups[j.Engine]
		if !ok {
			g = &engineGroup{}
			groups[j.Engine] = g
		}
		g.engineJobIDs = append(g.engineJobIDs, j.EngineJobID)
		g.jobIDs = append(g.jobIDs, j.JobID)
	}

	var result []StatusReport
	for engineName, g := range groups {
		eng, err := registry.Get(engineName)
		if err != nil {
			continue
		}
		statuses, err := eng.BatchStatus(ctx, g.engineJobIDs)
		if err != nil {
			log.Warn().Err(err).Str("engine", engineName).Msg("batch status failed")
			continue
		}
		for i, engineJobID := range g.engineJobIDs {
			status, ok := statuses[engineJobID]
			if !ok {
				continue
			}

			result = append(result, StatusReport{
				JobID:          g.jobIDs[i],
				EngineJobID:    status.EngineJobID,
				Status:         string(status.State),
				Progress:       status.Progress,
				Speed:          status.Speed,
				TotalSize:      status.TotalSize,
				DownloadedSize: status.DownloadedSize,
				Name:           status.Name,
				Error:          status.Error,
			})

			// Remove completed/failed/cancelled from tracker
			if status.State == engine.StateCompleted || status.State == engine.StateFailed || status.State == engine.StateCancelled {
				tracker.Remove(g.jobIDs[i])
			}
		}
	}
	return result
}
