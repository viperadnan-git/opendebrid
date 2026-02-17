-- name: CreateDownloadLink :one
INSERT INTO download_links (user_id, download_id, file_path, token, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetDownloadLinkByToken :one
SELECT * FROM download_links WHERE token = $1 AND expires_at > NOW();

-- name: IncrementLinkAccess :exec
UPDATE download_links SET access_count = access_count + 1 WHERE token = $1;

-- name: DeleteExpiredLinks :exec
DELETE FROM download_links WHERE expires_at < NOW();
