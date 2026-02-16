-- name: CreateUser :one
INSERT INTO users (username, email, password, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: GetUserByAPIKey :one
SELECT * FROM users WHERE api_key = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: UpdateUser :one
UPDATE users SET
    username = COALESCE(NULLIF($2, ''), username),
    email = COALESCE($3, email),
    role = COALESCE(NULLIF($4, ''), role),
    is_active = COALESCE($5, is_active)
WHERE id = $1
RETURNING *;

-- name: RegenerateAPIKey :one
UPDATE users SET api_key = gen_random_uuid()
WHERE id = $1
RETURNING *;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: GetUserCount :one
SELECT count(*) FROM users WHERE role = 'admin';
