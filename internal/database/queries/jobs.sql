-- name: CreateJob :one
INSERT INTO jobs (user_id, node_id, engine, engine_job_id, url, cache_key, status)
VALUES ($1, $2, $3, $4, $5, $6, $7)
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

-- name: DeleteJob :exec
DELETE FROM jobs WHERE id = $1;
