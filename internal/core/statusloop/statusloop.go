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

// UpdateEngineJobID updates the engine job ID for a tracked job (e.g. aria2 GID transition).
func (t *Tracker) UpdateEngineJobID(jobID, newEngineJobID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if j, ok := t.jobs[jobID]; ok {
		j.EngineJobID = newEngineJobID
		t.jobs[jobID] = j
	}
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

	// Poll each engine concurrently
	type engineResult struct {
		reports []StatusReport
	}
	results := make([]engineResult, len(groups))

	var wg sync.WaitGroup
	idx := 0
	for engineName, g := range groups {
		eng, err := registry.Get(engineName)
		if err != nil {
			idx++
			continue
		}
		wg.Add(1)
		go func(i int, eName string, eng engine.Engine, g *engineGroup) {
			defer wg.Done()
			statuses, err := eng.BatchStatus(ctx, g.engineJobIDs)
			if err != nil {
				log.Warn().Err(err).Str("engine", eName).Msg("batch status failed")
				return
			}
			var reports []StatusReport
			for j, engineJobID := range g.engineJobIDs {
				status, ok := statuses[engineJobID]
				if !ok {
					continue
				}

				if status.EngineJobID != "" && status.EngineJobID != engineJobID {
					tracker.UpdateEngineJobID(g.jobIDs[j], status.EngineJobID)
				}

				reports = append(reports, StatusReport{
					JobID:          g.jobIDs[j],
					EngineJobID:    status.EngineJobID,
					Status:         string(status.State),
					Progress:       status.Progress,
					Speed:          status.Speed,
					TotalSize:      status.TotalSize,
					DownloadedSize: status.DownloadedSize,
					Name:           status.Name,
					Error:          status.Error,
				})

				if status.State == engine.StateCompleted || status.State == engine.StateFailed {
					tracker.Remove(g.jobIDs[j])
				}
			}
			results[i] = engineResult{reports: reports}
		}(idx, engineName, eng, g)
		idx++
	}
	wg.Wait()

	// Merge results
	var result []StatusReport
	for _, r := range results {
		result = append(result, r.reports...)
	}
	return result
}
