-- Enable pgcrypto for gen_random_uuid() fallback
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ========================
-- Users & Authentication
-- ========================
CREATE TABLE users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username    TEXT UNIQUE NOT NULL,
    email       TEXT UNIQUE,
    password    TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    api_key     UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_api_key ON users(api_key);

-- ========================
-- Nodes
-- ========================
CREATE TABLE nodes (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    grpc_endpoint   TEXT,
    file_endpoint   TEXT NOT NULL,
    engines         JSONB NOT NULL DEFAULT '[]',
    is_controller   BOOLEAN NOT NULL DEFAULT false,
    is_online       BOOLEAN NOT NULL DEFAULT false,
    disk_total      BIGINT NOT NULL DEFAULT 0,
    disk_available  BIGINT NOT NULL DEFAULT 0,
    last_heartbeat  TIMESTAMPTZ,
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata        JSONB DEFAULT '{}'
);

-- ========================
-- Jobs (Download Tasks)
-- ========================
CREATE TABLE jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id         TEXT NOT NULL REFERENCES nodes(id),
    engine          TEXT NOT NULL,
    engine_job_id   TEXT,
    url             TEXT NOT NULL,
    cache_key       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'active', 'completed', 'failed', 'cancelled')),
    name            TEXT NOT NULL DEFAULT '',
    size            BIGINT,
    engine_state    TEXT,
    file_location   TEXT,
    error_message   TEXT,
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_jobs_user ON jobs(user_id);
CREATE INDEX idx_jobs_cache ON jobs(cache_key);
CREATE INDEX idx_jobs_node ON jobs(node_id);
CREATE INDEX idx_jobs_status ON jobs(status);
CREATE INDEX idx_jobs_engine ON jobs(engine);

-- ========================
-- Cache Registry
-- ========================
CREATE TABLE cache_entries (
    cache_key       TEXT PRIMARY KEY,
    job_id          UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    node_id         TEXT NOT NULL REFERENCES nodes(id),
    engine          TEXT NOT NULL,
    file_location   TEXT NOT NULL,
    total_size      BIGINT DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_accessed   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    access_count    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_cache_node ON cache_entries(node_id);
CREATE INDEX idx_cache_accessed ON cache_entries(last_accessed);

-- ========================
-- Signed Download Links
-- ========================
CREATE TABLE download_links (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    job_id      UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    file_path   TEXT NOT NULL,
    token       TEXT UNIQUE NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    access_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_download_links_token ON download_links(token);
CREATE INDEX idx_download_links_expires ON download_links(expires_at);

-- ========================
-- App Settings (key-value)
-- ========================
CREATE TABLE settings (
    key         TEXT PRIMARY KEY,
    value       JSONB NOT NULL,
    description TEXT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO settings (key, value, description) VALUES
    ('jwt_secret', 'null', 'JWT signing secret - auto-generated on first boot if null'),
    ('auth_token', 'null', 'Shared token for worker registration - auto-generated on first boot if null'),
    ('link_expiry_minutes', '60', 'Default download link expiry in minutes'),
    ('per_user_concurrent_downloads', '5', 'Max concurrent downloads per user'),
    ('per_node_concurrent_downloads', '10', 'Max concurrent downloads per node'),
    ('min_disk_free_bytes', '1073741824', 'Minimum free disk space per node (1GB)'),
    ('lru_cleanup_enabled', 'false', 'Enable LRU-based automatic cleanup'),
    ('default_load_balancer', '"round-robin"', 'Load balancing adapter name'),
    ('registration_enabled', 'true', 'Allow new user registration');

-- ========================
-- Triggers
-- ========================
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_jobs_updated_at BEFORE UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
