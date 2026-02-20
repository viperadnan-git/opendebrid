package job

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

// Manager handles job and download lifecycle and persistence.
type Manager struct {
	queries *gen.Queries
}

func NewManager(db *pgxpool.Pool) *Manager {
	return &Manager{
		queries: gen.New(db),
	}
}

// --- Job (content) operations ---

func (m *Manager) CreateJobTx(ctx context.Context, tx pgx.Tx, nodeID, engine, engineJobID, url, storageKey, name string) (*gen.Job, error) {
	q := m.queries.WithTx(tx)
	job, err := q.CreateJob(ctx, gen.CreateJobParams{
		NodeID:      nodeID,
		Engine:      engine,
		EngineJobID: pgtype.Text{String: engineJobID, Valid: engineJobID != ""},
		Url:         url,
		StorageKey:  storageKey,
		Name:        name,
	})
	if err != nil {
		return nil, err
	}

	return &job, nil
}

func (m *Manager) CreateDownloadTx(ctx context.Context, tx pgx.Tx, userID, jobID string) (*gen.Download, error) {
	q := m.queries.WithTx(tx)
	dl, err := q.CreateDownload(ctx, gen.CreateDownloadParams{
		UserID: util.TextToUUID(userID),
		JobID:  util.TextToUUID(jobID),
	})
	if err != nil {
		return nil, err
	}
	return &dl, nil
}

func (m *Manager) GetJob(ctx context.Context, jobID string) (*gen.Job, error) {
	job, err := m.queries.GetJob(ctx, util.TextToUUID(jobID))
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *Manager) GetJobByStorageKey(ctx context.Context, storageKey string) (*gen.Job, error) {
	job, err := m.queries.GetJobByStorageKey(ctx, storageKey)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *Manager) UpdateJobStatus(ctx context.Context, jobID, status, engineJobID, errorMsg, fileLocation string) error {
	_, err := m.queries.UpdateJobStatus(ctx, gen.UpdateJobStatusParams{
		ID:      util.TextToUUID(jobID),
		Status:  status,
		Column3: engineJobID,
		Column4: errorMsg,
		Column5: fileLocation,
	})
	return err
}

func (m *Manager) FailJob(ctx context.Context, jobID, errorMsg string) error {
	return m.queries.FailJob(ctx, gen.FailJobParams{
		ID:           util.TextToUUID(jobID),
		ErrorMessage: pgtype.Text{String: errorMsg, Valid: errorMsg != ""},
	})
}

func (m *Manager) ResetJob(ctx context.Context, jobID, nodeID, url string) (*gen.Job, error) {
	job, err := m.queries.ResetJob(ctx, gen.ResetJobParams{
		ID:     util.TextToUUID(jobID),
		NodeID: nodeID,
		Url:    url,
	})
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *Manager) DeleteJob(ctx context.Context, jobID string) error {
	return m.queries.DeleteJob(ctx, util.TextToUUID(jobID))
}

func (m *Manager) ListStaleActiveJobs(ctx context.Context) ([]gen.Job, error) {
	return m.queries.ListStaleActiveJobs(ctx)
}

// --- Download (user request) operations ---

func (m *Manager) CreateDownload(ctx context.Context, userID, jobID string) (*gen.Download, error) {
	dl, err := m.queries.CreateDownload(ctx, gen.CreateDownloadParams{
		UserID: util.TextToUUID(userID),
		JobID:  util.TextToUUID(jobID),
	})
	if err != nil {
		return nil, err
	}
	return &dl, nil
}

func (m *Manager) GetDownloadWithJobByUser(ctx context.Context, downloadID, userID string) (*gen.GetDownloadWithJobByUserRow, error) {
	row, err := m.queries.GetDownloadWithJobByUser(ctx, gen.GetDownloadWithJobByUserParams{
		ID:     util.TextToUUID(downloadID),
		UserID: util.TextToUUID(userID),
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (m *Manager) ListDownloadsByUser(ctx context.Context, userID string, limit, offset int32) ([]gen.ListDownloadsByUserRow, error) {
	return m.queries.ListDownloadsByUser(ctx, gen.ListDownloadsByUserParams{
		UserID: util.TextToUUID(userID),
		Limit:  limit,
		Offset: offset,
	})
}

func (m *Manager) ListDownloadsByUserAndEngine(ctx context.Context, userID, engine string, limit, offset int32) ([]gen.ListDownloadsByUserAndEngineRow, error) {
	return m.queries.ListDownloadsByUserAndEngine(ctx, gen.ListDownloadsByUserAndEngineParams{
		UserID: util.TextToUUID(userID),
		Engine: engine,
		Limit:  limit,
		Offset: offset,
	})
}

func (m *Manager) FindDownloadByUserAndJobID(ctx context.Context, userID, jobID string) (*gen.Download, error) {
	dl, err := m.queries.FindDownloadByUserAndJobID(ctx, gen.FindDownloadByUserAndJobIDParams{
		UserID: util.TextToUUID(userID),
		JobID:  util.TextToUUID(jobID),
	})
	if err != nil {
		return nil, err
	}
	return &dl, nil
}

func (m *Manager) ListUserJobsWithDownloadCounts(ctx context.Context, userID string) ([]gen.ListUserJobsWithDownloadCountsRow, error) {
	return m.queries.ListUserJobsWithDownloadCounts(ctx, util.TextToUUID(userID))
}

func (m *Manager) GetDownloadWithJobAndCount(ctx context.Context, downloadID, userID string) (*gen.GetDownloadWithJobAndCountRow, error) {
	row, err := m.queries.GetDownloadWithJobAndCount(ctx, gen.GetDownloadWithJobAndCountParams{
		ID:     util.TextToUUID(downloadID),
		UserID: util.TextToUUID(userID),
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (m *Manager) DeleteDownload(ctx context.Context, downloadID string) error {
	return m.queries.DeleteDownload(ctx, util.TextToUUID(downloadID))
}
