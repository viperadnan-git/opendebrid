package job

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

// Manager handles job and download lifecycle and persistence.
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

// --- Job (content) operations ---

func (m *Manager) CreateJobTx(ctx context.Context, tx pgx.Tx, nodeID, engine, engineJobID, url, cacheKey, name string) (*gen.Job, error) {
	q := m.queries.WithTx(tx)
	job, err := q.CreateJob(ctx, gen.CreateJobParams{
		NodeID:      nodeID,
		Engine:      engine,
		EngineJobID: pgtype.Text{String: engineJobID, Valid: engineJobID != ""},
		Url:         url,
		CacheKey:    cacheKey,
		Name:        name,
	})
	if err != nil {
		return nil, err
	}

	_ = m.bus.Publish(ctx, event.Event{
		Type: event.EventJobCreated,
		Payload: event.JobEvent{
			JobID:    uuidToStr(job.ID),
			NodeID:   nodeID,
			Engine:   engine,
			CacheKey: cacheKey,
		},
	})

	return &job, nil
}

func (m *Manager) CreateDownloadTx(ctx context.Context, tx pgx.Tx, userID, jobID string) (*gen.Download, error) {
	q := m.queries.WithTx(tx)
	dl, err := q.CreateDownload(ctx, gen.CreateDownloadParams{
		UserID: textToUUID(userID),
		JobID:  textToUUID(jobID),
	})
	if err != nil {
		return nil, err
	}
	return &dl, nil
}

func (m *Manager) GetJob(ctx context.Context, jobID string) (*gen.Job, error) {
	job, err := m.queries.GetJob(ctx, textToUUID(jobID))
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *Manager) GetJobByCacheKey(ctx context.Context, cacheKey string) (*gen.Job, error) {
	job, err := m.queries.GetJobByCacheKey(ctx, cacheKey)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *Manager) UpdateJobStatus(ctx context.Context, jobID, status, engineJobID, errorMsg, fileLocation string) error {
	_, err := m.queries.UpdateJobStatus(ctx, gen.UpdateJobStatusParams{
		ID:           textToUUID(jobID),
		Status:       status,
		Column3:      engineJobID,
		ErrorMessage: pgtype.Text{String: errorMsg, Valid: errorMsg != ""},
		Column5:      fileLocation,
	})
	return err
}

func (m *Manager) CompleteJob(ctx context.Context, jobID, engineJobID, fileLocation string) error {
	_, err := m.queries.CompleteJob(ctx, gen.CompleteJobParams{
		ID:           textToUUID(jobID),
		Column2:      engineJobID,
		FileLocation: pgtype.Text{String: fileLocation, Valid: fileLocation != ""},
	})
	return err
}

func (m *Manager) FailJob(ctx context.Context, jobID, errorMsg string) error {
	return m.queries.FailJob(ctx, gen.FailJobParams{
		ID:           textToUUID(jobID),
		ErrorMessage: pgtype.Text{String: errorMsg, Valid: errorMsg != ""},
	})
}

func (m *Manager) UpdateJobMeta(ctx context.Context, jobID, name string, size int64) error {
	return m.queries.UpdateJobMeta(ctx, gen.UpdateJobMetaParams{
		ID:   textToUUID(jobID),
		Name: name,
		Size: pgtype.Int8{Int64: size, Valid: size > 0},
	})
}

func (m *Manager) DeleteJob(ctx context.Context, jobID string) error {
	return m.queries.DeleteJob(ctx, textToUUID(jobID))
}

func (m *Manager) ListActiveJobs(ctx context.Context) ([]gen.Job, error) {
	return m.queries.ListActiveJobs(ctx)
}

// --- Download (user request) operations ---

func (m *Manager) CreateDownload(ctx context.Context, userID, jobID string) (*gen.Download, error) {
	dl, err := m.queries.CreateDownload(ctx, gen.CreateDownloadParams{
		UserID: textToUUID(userID),
		JobID:  textToUUID(jobID),
	})
	if err != nil {
		return nil, err
	}
	return &dl, nil
}

func (m *Manager) GetDownloadWithJobByUser(ctx context.Context, downloadID, userID string) (*gen.GetDownloadWithJobByUserRow, error) {
	row, err := m.queries.GetDownloadWithJobByUser(ctx, gen.GetDownloadWithJobByUserParams{
		ID:     textToUUID(downloadID),
		UserID: textToUUID(userID),
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (m *Manager) ListDownloadsByUser(ctx context.Context, userID string, limit, offset int32) ([]gen.ListDownloadsByUserRow, error) {
	return m.queries.ListDownloadsByUser(ctx, gen.ListDownloadsByUserParams{
		UserID: textToUUID(userID),
		Limit:  limit,
		Offset: offset,
	})
}

func (m *Manager) ListDownloadsByUserAndEngine(ctx context.Context, userID, engine string, limit, offset int32) ([]gen.ListDownloadsByUserAndEngineRow, error) {
	return m.queries.ListDownloadsByUserAndEngine(ctx, gen.ListDownloadsByUserAndEngineParams{
		UserID: textToUUID(userID),
		Engine: engine,
		Limit:  limit,
		Offset: offset,
	})
}

func (m *Manager) FindDownloadByUserAndJobID(ctx context.Context, userID, jobID string) (*gen.Download, error) {
	dl, err := m.queries.FindDownloadByUserAndJobID(ctx, gen.FindDownloadByUserAndJobIDParams{
		UserID: textToUUID(userID),
		JobID:  textToUUID(jobID),
	})
	if err != nil {
		return nil, err
	}
	return &dl, nil
}

func (m *Manager) ListUserJobsWithDownloadCounts(ctx context.Context, userID string) ([]gen.ListUserJobsWithDownloadCountsRow, error) {
	return m.queries.ListUserJobsWithDownloadCounts(ctx, textToUUID(userID))
}

func (m *Manager) GetDownloadWithJobAndCount(ctx context.Context, downloadID, userID string) (*gen.GetDownloadWithJobAndCountRow, error) {
	row, err := m.queries.GetDownloadWithJobAndCount(ctx, gen.GetDownloadWithJobAndCountParams{
		ID:     textToUUID(downloadID),
		UserID: textToUUID(userID),
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (m *Manager) DeleteDownload(ctx context.Context, downloadID string) error {
	return m.queries.DeleteDownload(ctx, textToUUID(downloadID))
}

// SetupEventHandlers subscribes to job events for status updates.
func (m *Manager) SetupEventHandlers() {
	m.bus.Subscribe(event.EventJobProgress, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok || payload.EngineJobID == "" {
			return nil
		}
		if err := m.UpdateJobStatus(ctx, payload.JobID, "active", payload.EngineJobID, "", ""); err != nil {
			return err
		}
		if payload.Name != "" || payload.Size > 0 {
			return m.UpdateJobMeta(ctx, payload.JobID, payload.Name, payload.Size)
		}
		return nil
	})

	m.bus.Subscribe(event.EventJobCompleted, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok {
			return nil
		}
		log.Info().Str("job_id", payload.JobID).Msg("job completed")
		return m.CompleteJob(ctx, payload.JobID, "", "")
	})

	m.bus.Subscribe(event.EventJobFailed, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok {
			return nil
		}
		log.Warn().Str("job_id", payload.JobID).Str("error", payload.Error).Msg("job failed")
		return m.FailJob(ctx, payload.JobID, payload.Error)
	})

	m.bus.Subscribe(event.EventJobCancelled, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok {
			return nil
		}
		return m.UpdateJobStatus(ctx, payload.JobID, "cancelled", "", "", "")
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
