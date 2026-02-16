-- name: LookupCache :one
SELECT * FROM cache_entries WHERE cache_key = $1;

-- name: InsertCacheEntry :one
INSERT INTO cache_entries (cache_key, job_id, node_id, engine, file_location, total_size)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (cache_key) DO UPDATE SET
    last_accessed = NOW(),
    access_count = cache_entries.access_count + 1
RETURNING *;

-- name: TouchCache :exec
UPDATE cache_entries SET
    last_accessed = NOW(),
    access_count = access_count + 1
WHERE cache_key = $1;

-- name: DeleteCacheEntry :exec
DELETE FROM cache_entries WHERE cache_key = $1;

-- name: DeleteCacheByNode :exec
DELETE FROM cache_entries WHERE node_id = $1;

-- name: ListCacheByLRU :many
SELECT * FROM cache_entries
ORDER BY last_accessed ASC
LIMIT $1;
