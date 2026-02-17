-- name: UpsertNode :one
INSERT INTO nodes (id, name, grpc_endpoint, file_endpoint, engines, is_controller, is_online, disk_total, disk_available)
VALUES ($1, $2, $3, $4, $5, $6, true, $7, $8)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    grpc_endpoint = EXCLUDED.grpc_endpoint,
    file_endpoint = EXCLUDED.file_endpoint,
    engines = EXCLUDED.engines,
    is_online = true,
    disk_total = EXCLUDED.disk_total,
    disk_available = EXCLUDED.disk_available,
    last_heartbeat = NOW()
RETURNING *;

-- name: GetNode :one
SELECT * FROM nodes WHERE id = $1;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY registered_at;

-- name: ListOnlineNodes :many
SELECT * FROM nodes WHERE is_online = true;

-- name: UpdateNodeHeartbeat :exec
UPDATE nodes SET
    is_online = true,
    disk_total = $2,
    disk_available = $3,
    last_heartbeat = NOW()
WHERE id = $1;

-- name: SetNodeOffline :exec
UPDATE nodes SET is_online = false WHERE id = $1;

-- name: MarkStaleNodesOffline :exec
UPDATE nodes SET is_online = false WHERE is_online = true AND last_heartbeat < NOW() - INTERVAL '90 seconds';

-- name: DeleteStaleNodes :exec
DELETE FROM nodes WHERE is_online = false AND last_heartbeat < NOW() - INTERVAL '1 hour';

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = $1;
