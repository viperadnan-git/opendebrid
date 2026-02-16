package job

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendebrid/opendebrid/internal/core/event"
	"github.com/opendebrid/opendebrid/internal/database/gen"
	"github.com/rs/zerolog/log"
)

// Manager handles job lifecycle and persistence.
type Manager struct {
	queries *gen.Queries
	bus     event.Bus
}

func NewManager(db *pgxpool.Pool, bus event.Bus) *Manager {
	return &Manager{
		queries: gen.New(db),
		bus:     bus,
	}
}

func (m *Manager) Create(ctx context.Context, userID, nodeID, engine, engineJobID, url, cacheKey string) (*gen.Job, error) {
	job, err := m.queries.CreateJob(ctx, gen.CreateJobParams{
		UserID:      textToUUID(userID),
		NodeID:      nodeID,
		Engine:      engine,
		EngineJobID: pgtype.Text{String: engineJobID, Valid: engineJobID != ""},
		Url:         url,
		CacheKey:    cacheKey,
		Status:      "queued",
	})
	if err != nil {
		return nil, err
	}

	m.bus.Publish(ctx, event.Event{
		Type: event.EventJobCreated,
		Payload: event.JobEvent{
			JobID:    uuidToStr(job.ID),
			UserID:   userID,
			NodeID:   nodeID,
			Engine:   engine,
			CacheKey: cacheKey,
		},
	})

	return &job, nil
}

func (m *Manager) UpdateStatus(ctx context.Context, jobID, status, engineState, engineJobID, errorMsg, fileLocation string) error {
	_, err := m.queries.UpdateJobStatus(ctx, gen.UpdateJobStatusParams{
		ID:          textToUUID(jobID),
		Status:      status,
		EngineState: pgtype.Text{String: engineState, Valid: engineState != ""},
		Column4:     engineJobID,
		ErrorMessage: pgtype.Text{String: errorMsg, Valid: errorMsg != ""},
		Column6:     fileLocation,
	})
	return err
}

func (m *Manager) Complete(ctx context.Context, jobID, fileLocation string) error {
	_, err := m.queries.CompleteJob(ctx, gen.CompleteJobParams{
		ID:           textToUUID(jobID),
		FileLocation: pgtype.Text{String: fileLocation, Valid: fileLocation != ""},
	})
	return err
}

func (m *Manager) GetJob(ctx context.Context, jobID string) (*gen.Job, error) {
	job, err := m.queries.GetJob(ctx, textToUUID(jobID))
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *Manager) GetJobForUser(ctx context.Context, jobID, userID string) (*gen.Job, error) {
	job, err := m.queries.GetJobByUserAndID(ctx, gen.GetJobByUserAndIDParams{
		ID:     textToUUID(jobID),
		UserID: textToUUID(userID),
	})
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *Manager) ListByUser(ctx context.Context, userID string, limit, offset int32) ([]gen.Job, error) {
	return m.queries.ListJobsByUser(ctx, gen.ListJobsByUserParams{
		UserID: textToUUID(userID),
		Limit:  limit,
		Offset: offset,
	})
}

func (m *Manager) ListByUserAndEngine(ctx context.Context, userID, engine string, limit, offset int32) ([]gen.Job, error) {
	return m.queries.ListJobsByUserAndEngine(ctx, gen.ListJobsByUserAndEngineParams{
		UserID: textToUUID(userID),
		Engine: engine,
		Limit:  limit,
		Offset: offset,
	})
}

func (m *Manager) CountActiveByUser(ctx context.Context, userID string) (int64, error) {
	return m.queries.CountActiveJobsByUser(ctx, textToUUID(userID))
}

func (m *Manager) Delete(ctx context.Context, jobID string) error {
	return m.queries.DeleteJob(ctx, textToUUID(jobID))
}

// SetupEventHandlers subscribes to job events for status updates.
func (m *Manager) SetupEventHandlers() {
	m.bus.Subscribe(event.EventJobProgress, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok || payload.EngineJobID == "" {
			return nil
		}
		// Update engine_job_id when it changes (e.g. .torrent HTTP → torrent GID)
		return m.UpdateStatus(ctx, payload.JobID, "active", "", payload.EngineJobID, "", "")
	})

	m.bus.Subscribe(event.EventJobCompleted, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok {
			return nil
		}
		log.Info().Str("job_id", payload.JobID).Msg("job completed")
		return m.Complete(ctx, payload.JobID, "")
	})

	m.bus.Subscribe(event.EventJobFailed, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok {
			return nil
		}
		log.Warn().Str("job_id", payload.JobID).Str("error", payload.Error).Msg("job failed")
		return m.UpdateStatus(ctx, payload.JobID, "failed", "", "", payload.Error, "")
	})

	m.bus.Subscribe(event.EventJobCancelled, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok {
			return nil
		}
		return m.UpdateStatus(ctx, payload.JobID, "cancelled", "", "", "", "")
	})
}

func textToUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	cleaned := ""
	for _, c := range s {
		if c != '-' {
			cleaned += string(c)
		}
	}
	if len(cleaned) != 32 {
		return u
	}
	for i := 0; i < 16; i++ {
		b := hexVal(cleaned[i*2])<<4 | hexVal(cleaned[i*2+1])
		u.Bytes[i] = b
	}
	u.Valid = true
	return u
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

func uuidToStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	const hex = "0123456789abcdef"
	buf := make([]byte, 36)
	pos := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			buf[pos] = '-'
			pos++
		}
		buf[pos] = hex[b[i]>>4]
		buf[pos+1] = hex[b[i]&0x0f]
		pos += 2
	}
	return string(buf)
}
