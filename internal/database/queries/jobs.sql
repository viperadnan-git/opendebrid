-- name: CreateJob :one
INSERT INTO jobs (node_id, engine, engine_job_id, url, cache_key, name)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1;

-- name: GetJobByCacheKey :one
SELECT * FROM jobs WHERE cache_key = $1;

-- name: ListActiveJobs :many
SELECT * FROM jobs
WHERE status IN ('queued', 'active')
ORDER BY created_at ASC;

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

-- name: DeleteJob :exec
DELETE FROM jobs WHERE id = $1;

-- name: BatchUpdateJobProgress :exec
UPDATE jobs AS j SET
    progress = u.progress,
    speed = u.speed,
    downloaded_size = u.downloaded_size,
    name = COALESCE(NULLIF(u.name, ''), j.name),
    size = COALESCE(NULLIF(u.size, 0), j.size),
    status = 'active'
FROM (
    SELECT
        unnest(@ids::uuid[]) AS id,
        unnest(@progress::double precision[]) AS progress,
        unnest(@speed::bigint[]) AS speed,
        unnest(@downloaded_size::bigint[]) AS downloaded_size,
        unnest(@name::text[]) AS name,
        unnest(@size::bigint[]) AS size
) AS u
WHERE j.id = u.id AND j.status IN ('queued', 'active');
