-- name: UpsertNode :one
INSERT INTO nodes (id, grpc_endpoint, file_endpoint, engines, is_controller, is_online, disk_total, disk_available)
VALUES ($1, $2, $3, $4, $5, true, $6, $7)
ON CONFLICT (id) DO UPDATE SET
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

-- name: MarkStaleNodesOffline :many
UPDATE nodes SET is_online = false WHERE is_online = true AND last_heartbeat < NOW() - INTERVAL '90 seconds'
RETURNING id;

-- name: MarkWorkerNodesOffline :many
-- Marks all non-controller worker nodes offline on controller startup.
-- Workers must re-register via heartbeat reconnect.
UPDATE nodes SET is_online = false
WHERE is_controller = false AND is_online = true
RETURNING id;

-- name: DeleteStaleNodes :exec
DELETE FROM nodes WHERE is_online = false AND last_heartbeat < NOW() - INTERVAL '1 hour';

-- name: GetAdminStats :one
SELECT
    (SELECT count(*) FROM users) AS total_users,
    (SELECT count(*) FROM jobs WHERE status IN ('queued', 'active')) AS active_jobs,
    count(*) AS total_nodes,
    count(*) FILTER (WHERE is_online = true) AS online_nodes,
    COALESCE(SUM(disk_total) FILTER (WHERE is_online = true), 0)::bigint AS disk_total,
    COALESCE(SUM(disk_available) FILTER (WHERE is_online = true), 0)::bigint AS disk_available
FROM nodes;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = $1;
