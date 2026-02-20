package database

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockID is an arbitrary constant used with pg_advisory_lock to prevent
// concurrent migration runs (e.g. multiple controller instances starting at once).
const migrationLockID = 48716983

// Migrate runs all pending migrations. Simple sequential approach using
// a migrations tracking table. Uses a PostgreSQL advisory lock to prevent
// concurrent execution across multiple instances.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// Acquire advisory lock — blocks until exclusive
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn for migration lock: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	// Create migrations tracking table
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	// Check current version
	var currentVersion int
	err = pool.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	// Read and apply migrations
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Collect up migrations in order
	type migration struct {
		version  int
		filename string
	}
	var migrations []migration
	for _, entry := range entries {
		var version int
		var direction string
		_, err := fmt.Sscanf(entry.Name(), "%03d_%s", &version, &direction)
		if err != nil {
			continue
		}
		// Only apply .up.sql files
		if len(entry.Name()) < 7 || entry.Name()[len(entry.Name())-7:] != ".up.sql" {
			continue
		}
		if version > currentVersion {
			migrations = append(migrations, migration{version: version, filename: entry.Name()})
		}
	}

	for _, m := range migrations {
		sql, err := migrationsFS.ReadFile("migrations/" + m.filename)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.filename, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for migration %d: %w", m.version, err)
		}

		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %d: %w", m.version, err)
		}

		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}

		log.Info().Int("version", m.version).Str("file", m.filename).Msg("applied migration")
	}

	return nil
}

// MigrateDown rolls back the last applied migration.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	var currentVersion int
	err := pool.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	if currentVersion == 0 {
		log.Info().Msg("no migrations to roll back")
		return nil
	}

	// Find matching down file
	filename := fmt.Sprintf("%03d_init.down.sql", currentVersion)
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var found string
	for _, entry := range entries {
		var version int
		var rest string
		if _, err := fmt.Sscanf(entry.Name(), "%03d_%s", &version, &rest); err != nil {
			continue
		}
		if version == currentVersion && len(entry.Name()) >= 9 && entry.Name()[len(entry.Name())-9:] == ".down.sql" {
			found = entry.Name()
			break
		}
	}
	if found == "" {
		return fmt.Errorf("no down migration found for version %d (expected %s)", currentVersion, filename)
	}

	sql, err := migrationsFS.ReadFile("migrations/" + found)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", found, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for rollback %d: %w", currentVersion, err)
	}

	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("rollback migration %d: %w", currentVersion, err)
	}

	if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", currentVersion); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("remove migration record %d: %w", currentVersion, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit rollback %d: %w", currentVersion, err)
	}

	log.Info().Int("version", currentVersion).Str("file", found).Msg("rolled back migration")
	return nil
}
