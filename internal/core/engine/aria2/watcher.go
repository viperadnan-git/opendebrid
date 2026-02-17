package aria2

import (
	"context"
	"time"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/rs/zerolog/log"
)

// Watcher polls aria2 for job status updates and publishes events.
type Watcher struct {
	engine   *Engine
	bus      event.Bus
	interval time.Duration
	jobs     map[string]watchedJob // engineJobID -> info
}

type watchedJob struct {
	JobID    string
	UserID   string
	NodeID   string
	CacheKey string
}

func NewWatcher(eng *Engine, bus event.Bus, interval time.Duration) *Watcher {
	return &Watcher{
		engine:   eng,
		bus:      bus,
		interval: interval,
		jobs:     make(map[string]watchedJob),
	}
}

func (w *Watcher) Track(engineJobID string, info watchedJob) {
	w.jobs[engineJobID] = info
}

func (w *Watcher) Untrack(engineJobID string) {
	delete(w.jobs, engineJobID)
}

// Run starts polling. Call in a goroutine.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

func (w *Watcher) poll(ctx context.Context) {
	for engineJobID, info := range w.jobs {
		status, err := w.engine.Status(ctx, engineJobID)
		if err != nil {
			log.Debug().Err(err).Str("gid", engineJobID).Msg("watcher poll error")
			continue
		}

		// GID changed (e.g. .torrent HTTP download followed by actual torrent).
		// Re-key to the new GID and clean up the old one.
		if status.EngineJobID != engineJobID {
			log.Info().
				Str("old_gid", engineJobID).
				Str("new_gid", status.EngineJobID).
				Str("job_id", info.JobID).
				Msg("aria2 GID changed, updating")
			delete(w.jobs, engineJobID)
			w.jobs[status.EngineJobID] = info
			engineJobID = status.EngineJobID
			// Clean up the old metadata download from aria2
			_ = w.engine.client.RemoveDownloadResult(ctx, engineJobID)
		}

		payload := event.JobEvent{
			JobID:       info.JobID,
			UserID:      info.UserID,
			NodeID:      info.NodeID,
			Engine:      "aria2",
			CacheKey:    info.CacheKey,
			Progress:    status.Progress,
			Speed:       status.Speed,
			EngineJobID: status.EngineJobID,
		}

		switch status.State {
		case engine.StateActive:
			w.bus.Publish(ctx, event.Event{Type: event.EventJobProgress, Payload: payload})
		case engine.StateCompleted:
			w.bus.Publish(ctx, event.Event{Type: event.EventJobCompleted, Payload: payload})
			w.Untrack(status.EngineJobID)
		case engine.StateFailed:
			payload.Error = status.Error
			w.bus.Publish(ctx, event.Event{Type: event.EventJobFailed, Payload: payload})
			w.Untrack(status.EngineJobID)
		case engine.StateCancelled:
			w.bus.Publish(ctx, event.Event{Type: event.EventJobCancelled, Payload: payload})
			w.Untrack(status.EngineJobID)
		}
	}
}
