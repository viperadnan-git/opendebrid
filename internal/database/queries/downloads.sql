-- name: CreateDownload :one
INSERT INTO downloads (user_id, job_id)
VALUES ($1, $2)
RETURNING *;

-- name: GetDownload :one
SELECT * FROM downloads WHERE id = $1;

-- name: GetDownloadByUserAndID :one
SELECT * FROM downloads WHERE id = $1 AND user_id = $2;

-- name: GetDownloadWithJob :one
SELECT
    d.id AS download_id,
    d.user_id,
    d.job_id,
    d.created_at AS download_created_at,
    j.node_id,
    j.engine,
    j.engine_job_id,
    j.url,
    j.cache_key,
    j.status,
    j.name,
    j.size,
    j.file_location,
    j.error_message,
    j.metadata,
    j.created_at AS job_created_at,
    j.updated_at AS job_updated_at,
    j.completed_at
FROM downloads d
JOIN jobs j ON j.id = d.job_id
WHERE d.id = $1;

-- name: GetDownloadWithJobByUser :one
SELECT
    d.id AS download_id,
    d.user_id,
    d.job_id,
    d.created_at AS download_created_at,
    j.node_id,
    j.engine,
    j.engine_job_id,
    j.url,
    j.cache_key,
    j.status,
    j.name,
    j.size,
    j.file_location,
    j.error_message,
    j.metadata,
    j.created_at AS job_created_at,
    j.updated_at AS job_updated_at,
    j.completed_at
FROM downloads d
JOIN jobs j ON j.id = d.job_id
WHERE d.id = $1 AND d.user_id = $2;

-- name: ListDownloadsByUser :many
SELECT
    d.id AS download_id,
    d.user_id,
    d.job_id,
    d.created_at AS download_created_at,
    j.node_id,
    j.engine,
    j.engine_job_id,
    j.url,
    j.cache_key,
    j.status,
    j.name,
    j.size,
    j.file_location,
    j.error_message,
    j.metadata,
    j.created_at AS job_created_at,
    j.updated_at AS job_updated_at,
    j.completed_at
FROM downloads d
JOIN jobs j ON j.id = d.job_id
WHERE d.user_id = $1
ORDER BY d.created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListDownloadsByUserAndEngine :many
SELECT
    d.id AS download_id,
    d.user_id,
    d.job_id,
    d.created_at AS download_created_at,
    j.node_id,
    j.engine,
    j.engine_job_id,
    j.url,
    j.cache_key,
    j.status,
    j.name,
    j.size,
    j.file_location,
    j.error_message,
    j.metadata,
    j.created_at AS job_created_at,
    j.updated_at AS job_updated_at,
    j.completed_at
FROM downloads d
JOIN jobs j ON j.id = d.job_id
WHERE d.user_id = $1 AND j.engine = $2
ORDER BY d.created_at DESC
LIMIT $3 OFFSET $4;

-- name: CountDownloadsByUser :one
SELECT count(*) FROM downloads WHERE user_id = $1;

-- name: CountActiveDownloadsByUser :one
SELECT count(*) FROM downloads d
JOIN jobs j ON j.id = d.job_id
WHERE d.user_id = $1 AND j.status IN ('queued', 'active');

-- name: CountCompletedDownloadsByUser :one
SELECT count(*) FROM downloads d
JOIN jobs j ON j.id = d.job_id
WHERE d.user_id = $1 AND j.status = 'completed';

-- name: FindDownloadByUserAndJobID :one
SELECT * FROM downloads
WHERE user_id = $1 AND job_id = $2;

-- name: CountDownloadsByJob :one
SELECT count(*) FROM downloads WHERE job_id = $1;

-- name: ListAllDownloadsByUser :many
SELECT * FROM downloads WHERE user_id = $1 ORDER BY created_at DESC;

-- name: DeleteDownload :exec
DELETE FROM downloads WHERE id = $1;
