-- name: CreateJob :one
INSERT INTO jobs (user_id, node_id, engine, engine_job_id, url, cache_key, status, name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1;

-- name: GetJobByUserAndID :one
SELECT * FROM jobs WHERE id = $1 AND user_id = $2;

-- name: ListJobsByUser :many
SELECT * FROM jobs
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListJobsByUserAndEngine :many
SELECT * FROM jobs
WHERE user_id = $1 AND engine = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: ListJobsByUserAndStatus :many
SELECT * FROM jobs
WHERE user_id = $1 AND status = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: CountJobsByUser :one
SELECT count(*) FROM jobs WHERE user_id = $1;

-- name: CountActiveJobsByUser :one
SELECT count(*) FROM jobs WHERE user_id = $1 AND status IN ('queued', 'active');

-- name: CountActiveJobsByNode :one
SELECT count(*) FROM jobs WHERE node_id = $1 AND status IN ('queued', 'active');

-- name: UpdateJobStatus :one
UPDATE jobs SET
    status = $2,
    engine_state = $3,
    engine_job_id = COALESCE(NULLIF($4, ''), engine_job_id),
    error_message = $5,
    file_location = COALESCE(NULLIF($6, ''), file_location)
WHERE id = $1
RETURNING *;

-- name: CompleteJob :one
UPDATE jobs SET
    status = 'completed',
    file_location = $2,
    completed_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ListJobsByEngine :many
SELECT * FROM jobs
WHERE engine = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListActiveJobs :many
SELECT * FROM jobs
WHERE status IN ('queued', 'active')
ORDER BY created_at ASC;

-- name: UpdateJobMeta :exec
UPDATE jobs SET
    name = COALESCE(NULLIF(@name::TEXT, ''), name),
    size = COALESCE(@size, size)
WHERE id = @id;

-- name: FindJobByUserAndCacheKey :one
SELECT * FROM jobs
WHERE user_id = $1 AND cache_key = $2
  AND status IN ('queued', 'active', 'completed')
ORDER BY created_at DESC LIMIT 1;

-- name: TouchJob :exec
UPDATE jobs SET updated_at = NOW() WHERE id = $1;

-- name: FindActiveSourceJob :one
SELECT * FROM jobs
WHERE cache_key = $1 AND status IN ('queued', 'active')
ORDER BY created_at ASC LIMIT 1;

-- name: CountSiblingJobs :one
SELECT count(*) FROM jobs
WHERE cache_key = $1 AND id != $2
  AND status IN ('queued', 'active', 'completed');

-- name: UpdateSiblingsEngineJobID :exec
UPDATE jobs SET engine_job_id = $2
WHERE cache_key = $1 AND status IN ('queued', 'active') AND node_id = $3;

-- name: CountCompletedJobsByUser :one
SELECT count(*) FROM jobs WHERE user_id = $1 AND status = 'completed';

-- name: CountAllActiveJobs :one
SELECT count(*) FROM jobs WHERE status IN ('queued', 'active');

-- name: FindCompletedJobByCacheKey :one
SELECT * FROM jobs
WHERE cache_key = $1 AND status = 'completed'
ORDER BY completed_at DESC LIMIT 1;

-- name: ListAllJobsByUser :many
SELECT * FROM jobs WHERE user_id = $1 ORDER BY created_at DESC;

-- name: ListStorageKeysByNode :many
SELECT DISTINCT cache_key FROM jobs
WHERE node_id = $1 AND status IN ('queued', 'active', 'completed');

-- name: CompleteSiblingJobs :exec
UPDATE jobs SET
    status = 'completed',
    file_location = $3,
    completed_at = NOW()
WHERE cache_key = $1 AND id != $2 AND status IN ('queued', 'active');

-- name: FailSiblingJobs :exec
UPDATE jobs SET
    status = 'failed',
    error_message = $3,
    file_location = NULL
WHERE cache_key = $1 AND id != $2 AND status IN ('queued', 'active');

-- name: DeleteJob :exec
DELETE FROM jobs WHERE id = $1;
