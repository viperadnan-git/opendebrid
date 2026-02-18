-- name: CreateJob :one
INSERT INTO jobs (node_id, engine, engine_job_id, url, cache_key, name)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1;

-- name: GetJobByCacheKey :one
SELECT * FROM jobs WHERE cache_key = $1;

-- name: UpdateJobStatus :one
UPDATE jobs SET
    status = $2,
    engine_job_id = COALESCE(NULLIF($3, ''), engine_job_id),
    error_message = $4,
    file_location = COALESCE(NULLIF($5, ''), file_location)
WHERE id = $1
RETURNING *;

-- name: CompleteJob :one
UPDATE jobs SET
    status = 'completed',
    engine_job_id = COALESCE(NULLIF($2, ''), engine_job_id),
    completed_at = NOW()
WHERE id = $1
RETURNING *;

-- name: FailJob :exec
UPDATE jobs SET
    status = 'failed',
    error_message = $2,
    file_location = NULL
WHERE id = $1;

-- name: ListStorageKeysByNode :many
SELECT DISTINCT cache_key FROM jobs
WHERE node_id = $1 AND status IN ('queued', 'active', 'completed');

-- name: ResetJob :one
UPDATE jobs SET
    status = 'queued',
    node_id = $2,
    url = $3,
    engine_job_id = NULL,
    error_message = NULL,
    file_location = NULL,
    progress = 0,
    speed = 0,
    downloaded_size = 0,
    completed_at = NULL
WHERE id = $1 AND status IN ('failed', 'cancelled')
RETURNING *;

-- name: DeleteJob :exec
DELETE FROM jobs WHERE id = $1;

-- name: ListStaleActiveJobs :many
SELECT * FROM jobs
WHERE status IN ('queued', 'active')
  AND updated_at < NOW() - INTERVAL '5 minutes'
ORDER BY created_at ASC;

-- name: BatchUpdateJobProgress :exec
UPDATE jobs AS j SET
    progress = u.progress,
    speed = u.speed,
    downloaded_size = u.downloaded_size,
    name = COALESCE(NULLIF(u.name, ''), j.name),
    size = COALESCE(NULLIF(u.size, 0), j.size),
    engine_job_id = COALESCE(NULLIF(u.engine_job_id, ''), j.engine_job_id),
    status = 'active'
FROM (
    SELECT
        unnest(@ids::uuid[]) AS id,
        unnest(@progress::double precision[]) AS progress,
        unnest(@speed::bigint[]) AS speed,
        unnest(@downloaded_size::bigint[]) AS downloaded_size,
        unnest(@name::text[]) AS name,
        unnest(@size::bigint[]) AS size,
        unnest(@engine_job_id::text[]) AS engine_job_id
) AS u
WHERE j.id = u.id AND j.status IN ('queued', 'active');
