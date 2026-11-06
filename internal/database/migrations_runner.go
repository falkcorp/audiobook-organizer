// file: internal/database/migrations_runner.go
// version: 1.0.1
// guid: 6c1acba7-c650-40dc-abf4-87f9cc8d5b35
// last-edited: 2026-11-06

package database

import (
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// migrationRunnerLog logs through internal/logger, which sanitizes values, rather than
// calling log/slog directly (TestGuard_NoDirectSlogCalls).
var migrationRunnerLog = logger.New("database.migrations")

// MigrationFunc represents a migration operation.
//
// # Idempotency contract (MANDATORY — enforced by TestMigrationUpFunctionsAreIdempotent)
//
// An Up function MUST be idempotent: running it a second time against a store it
// has already been applied to must succeed and must leave the database
// byte-identical. RunMigrations will not knowingly replay an Up — a durable
// applied-migration record short-circuits it — but the record write itself can
// be lost to a crash on a store that cannot batch, so a replay is possible and
// must be harmless.
//
// In practice this means: read the current state and skip rows that already have
// the new shape, rather than rewriting unconditionally. An unconditional rewrite
// fails the enforcement test even when the values are unchanged, because a fresh
// UpdatedAt is itself a mutation — and an unconditional rewrite is precisely the
// shape that makes a replayed migration destructive.
type MigrationFunc func(store migrationStore) error

// Migration represents a single database migration
type Migration struct {
	Version     int
	Description string
	Up          MigrationFunc
	Down        MigrationFunc // Optional rollback
}

// MigrationRecord tracks applied migrations
type MigrationRecord struct {
	Version     int       `json:"version"`
	Description string    `json:"description"`
	AppliedAt   time.Time `json:"applied_at"`
}

// DatabaseVersion stores the current schema version
type DatabaseVersion struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunMigrations applies all pending migrations
func RunMigrations(store migrationStore) error {
	currentVersion, err := getCurrentVersion(store)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	migrationRunnerLog.Info("current database version: %d", currentVersion)

	// Find migrations to apply
	pendingMigrations := []Migration{}
	for _, m := range migrations {
		if m.Version > currentVersion {
			pendingMigrations = append(pendingMigrations, m)
		}
	}

	if len(pendingMigrations) == 0 {
		migrationRunnerLog.Info("database is up to date: version %d", currentVersion)
		return nil
	}

	migrationRunnerLog.Info("applying %d migrations", len(pendingMigrations))

	// Apply each migration
	for _, m := range pendingMigrations {
		// Replay safety (DB-02). An applied-migration record for a version the
		// schema counter has not reached means a previous boot ran this Up to
		// completion and then died before the version write landed. Re-running
		// Up in that state is exactly the replay this guard exists to prevent:
		// treat the migration as applied and only advance the version.
		recorded, err := migrationAlreadyRecorded(store, m.Version)
		if err != nil {
			return fmt.Errorf("failed to check migration record %d: %w", m.Version, err)
		}
		if recorded {
			migrationRunnerLog.Info("migration %d already recorded; advancing version without re-running: %s",
				m.Version, logger.SanitizeLogValue(m.Description))
			if err := setVersion(store, m.Version); err != nil {
				return fmt.Errorf("failed to update version to %d: %w", m.Version, err)
			}
			continue
		}

		migrationRunnerLog.Info("applying migration %d: %s", m.Version, logger.SanitizeLogValue(m.Description))

		// Each applied migration is real progress: give it its own start-time
		// budget so a long run of migrations is not killed by TimeoutStartSec.
		extendStartTimeout()
		if err := m.Up(store); err != nil {
			return fmt.Errorf("migration %d failed: %w", m.Version, err)
		}

		// Record the migration and advance the version as ONE durable write
		// wherever the store can batch, so no crash window separates them.
		if err := commitMigrationBookkeeping(store, m); err != nil {
			return err
		}

		migrationRunnerLog.Info("migration %d completed", m.Version)
	}

	migrationRunnerLog.Info("all migrations completed: version %d", pendingMigrations[len(pendingMigrations)-1].Version)

	return nil
}
