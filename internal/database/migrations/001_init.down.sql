DROP TRIGGER IF EXISTS trg_jobs_updated_at ON jobs;
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP FUNCTION IF EXISTS update_updated_at();
DROP TABLE IF EXISTS download_links;
DROP TABLE IF EXISTS downloads;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS settings;
