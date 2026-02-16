-- name: GetSetting :one
SELECT * FROM settings WHERE key = $1;

-- name: UpsertSetting :one
INSERT INTO settings (key, value, description)
VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET
    value = EXCLUDED.value,
    updated_at = NOW()
RETURNING *;

-- name: ListSettings :many
SELECT * FROM settings ORDER BY key;
