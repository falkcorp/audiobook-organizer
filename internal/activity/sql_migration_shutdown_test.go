// file: internal/activity/sql_migration_shutdown_test.go
// version: 1.0.0
// guid: 1a8f6c25-0e39-4d71-b8a4-7c3d5e02f96b
// last-edited: 2026-09-07

package activity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// newMigrationStarterForTest builds a starter over real Pebble and SQLite
// activity stores, in the pre-migration state (readSecondary=false) that makes
// Start actually launch the backfill.
func newMigrationStarterForTest(t *testing.T) *sqlMigrationStarter {
	t.Helper()

	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	sqlStore, err := database.OpenSQLiteActivityStore(filepath.Join(t.TempDir(), "activity.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlStore.Close() })

	mig := database.NewMigratingActivityStore(database.NewPebbleActivityStore(db), sqlStore, false)
	return &sqlMigrationStarter{mig: mig}
}

// TestSQLMigrationStarter_ImplementsStopper pins the interface the fix depends
// on. serviceregistry.Container.Stop skips anything that is not a Stopper, so
// without this method the backfill goroutine is unreachable by shutdown no
// matter what the goroutine itself does.
func TestSQLMigrationStarter_ImplementsStopper(t *testing.T) {
	var _ serviceregistry.Stopper = (*sqlMigrationStarter)(nil)
}

// TestSQLMigrationStarter_StopInterruptsTheSettleDelay is the regression guard
// for the shutdown hang, and for the use-after-close panic behind it.
//
// Start waits sqlMigrationSettleDelay (60s) before beginning the backfill. That
// wait used to be a plain time.Sleep on a goroutine holding no context, so a
// restart in the first minute after boot could not reach it at all: shutdown
// proceeded, the shared Pebble handle closed, and the goroutine woke up to scan
// a closed store — which Pebble answers with a panic, not an error, on a
// goroutine with no recover.
//
// Stop must therefore return promptly rather than after the full delay. The
// generous bound is deliberate: this asserts "interrupted", not "fast", and the
// pre-fix code takes the whole 60 seconds.
func TestSQLMigrationStarter_StopInterruptsTheSettleDelay(t *testing.T) {
	s := newMigrationStarterForTest(t)
	require.NoError(t, s.Start(context.Background()))

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_ = s.Stop(context.Background())
		done <- time.Since(start)
	}()

	select {
	case took := <-done:
		if took >= sqlMigrationSettleDelay {
			t.Fatalf("Stop took %s — it waited out the settle delay instead of interrupting it", took)
		}
		t.Logf("Stop returned in %s (settle delay is %s)", took.Round(time.Millisecond), sqlMigrationSettleDelay)
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return within 10s — the backfill goroutine is not cancellable, " +
			"so shutdown cannot wait for it and the store closes underneath it")
	}
}

// TestSQLMigrationStarter_StopIsSafeWhenStartDidNothing covers the early-return
// paths (nil wrapper, already-migrated, unexpected backend types). Those leave
// cancel nil, and Container.Stop calls Stop regardless of what Start decided.
func TestSQLMigrationStarter_StopIsSafeWhenStartDidNothing(t *testing.T) {
	s := &sqlMigrationStarter{mig: nil}
	require.NoError(t, s.Start(context.Background()))
	require.NoError(t, s.Stop(context.Background()), "Stop must tolerate a Start that returned early")

	// Stop is also reachable twice in principle; it must not panic.
	require.NoError(t, s.Stop(context.Background()))
}
