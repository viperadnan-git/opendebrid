-- name: CreateJob :one
INSERT INTO jobs (node_id, engine, engine_job_id, url, storage_key, name)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1;

-- name: GetJobByStorageKey :one
SELECT * FROM jobs WHERE storage_key = $1;

-- name: ListJobs :many
SELECT * FROM jobs ORDER BY created_at DESC;

-- name: UpdateJobStatus :one
UPDATE jobs SET
    status = $2,
    engine_job_id = CASE WHEN $3::text != '' THEN $3::text ELSE engine_job_id END,
    error_message = CASE WHEN $4::text != '' THEN $4::text ELSE error_message END,
    file_location = CASE WHEN $5::text != '' THEN $5::text ELSE file_location END
WHERE id = $1
RETURNING *;

-- name: CompleteJob :one
UPDATE jobs SET
    status = 'completed',
    engine_job_id = $2,
    name = CASE WHEN $3 != '' THEN $3 ELSE name END,
    size = CASE WHEN $4 > 0 THEN $4 ELSE size END,
    completed_at = NOW()
WHERE id = $1
RETURNING *;

-- name: FailJob :exec
UPDATE jobs SET
    status = 'failed',
    error_message = $2,
    file_location = NULL
WHERE id = $1;

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
WHERE id = $1 AND status = 'failed'
RETURNING *;

-- name: DeleteJob :exec
DELETE FROM jobs WHERE id = $1;

-- name: ListStaleActiveJobs :many
SELECT * FROM jobs
WHERE status IN ('queued', 'active')
  AND updated_at < NOW() - INTERVAL '5 minutes'
ORDER BY created_at ASC;

-- name: ListStorageKeysByNode :many
SELECT storage_key FROM jobs
WHERE node_id = $1 AND status IN ('queued', 'active', 'completed', 'inactive');

-- name: BatchUpdateJobProgress :exec
UPDATE jobs SET
    status = 'active',
    progress = u.progress,
    speed = u.speed,
    downloaded_size = u.downloaded_size,
    name = CASE WHEN u.name != '' THEN u.name ELSE jobs.name END,
    size = CASE WHEN u.size > 0 THEN u.size ELSE jobs.size END,
    engine_job_id = CASE WHEN u.engine_job_id != '' THEN u.engine_job_id ELSE jobs.engine_job_id END
FROM (
    SELECT
        unnest($1::uuid[]) AS id,
        unnest($2::float8[]) AS progress,
        unnest($3::bigint[]) AS speed,
        unnest($4::bigint[]) AS downloaded_size,
        unnest($5::text[]) AS name,
        unnest($6::bigint[]) AS size,
        unnest($7::text[]) AS engine_job_id
) AS u
WHERE jobs.id = u.id;

-- name: BatchCompleteJobs :exec
UPDATE jobs SET
    status = 'completed',
    engine_job_id = CASE WHEN u.engine_job_id != '' THEN u.engine_job_id ELSE jobs.engine_job_id END,
    name = CASE WHEN u.name != '' THEN u.name ELSE jobs.name END,
    size = CASE WHEN u.size > 0 THEN u.size ELSE jobs.size END,
    completed_at = NOW()
FROM (
    SELECT
        unnest($1::uuid[]) AS id,
        unnest($2::text[]) AS engine_job_id,
        unnest($3::text[]) AS name,
        unnest($4::bigint[]) AS size
) AS u
WHERE jobs.id = u.id;

-- name: BatchFailJobs :many
UPDATE jobs SET
    status = 'failed',
    error_message = u.error_message,
    file_location = NULL
FROM (
    SELECT
        unnest($1::uuid[]) AS id,
        unnest($2::text[]) AS error_message
) AS u
WHERE jobs.id = u.id
RETURNING jobs.id, jobs.engine, jobs.storage_key, u.error_message;

-- name: MarkNodeActiveJobsFailed :exec
UPDATE jobs SET status = 'failed', error_message = 'node went offline'
WHERE node_id = $1 AND status IN ('queued', 'active');

-- name: MarkNodeCompletedJobsInactive :exec
UPDATE jobs SET status = 'inactive'
WHERE node_id = $1 AND status = 'completed' AND file_location LIKE 'file://%';

-- name: RestoreNodeInactiveJobs :exec
UPDATE jobs SET status = 'completed'
WHERE node_id = $1 AND status = 'inactive';

-- name: RestoreNodeInactiveJobsWithKeys :execrows
UPDATE jobs SET status = 'completed'
WHERE node_id = $1 AND status = 'inactive'
  AND storage_key = ANY(@storage_keys::text[]);

-- name: FailNodeInactiveJobsMissingKeys :execrows
-- Fails any inactive jobs still remaining after RestoreNodeInactiveJobsWithKeys
-- has already restored the ones with confirmed disk files.
UPDATE jobs SET status = 'failed',
  error_message = 'files not found on node after restart',
  file_location = NULL
WHERE node_id = $1 AND status = 'inactive';

-- name: RestoreStaleInactiveJobs :exec
UPDATE jobs SET status = 'completed'
WHERE status = 'inactive'
  AND node_id IN (SELECT id FROM nodes WHERE is_online = true)
  AND updated_at < NOW() - INTERVAL '2 minutes';
