// file: internal/database/startup_notify_test.go
// version: 2.0.0
// guid: df9cf9a0-3bbf-446b-8d35-f26e8d704219
// last-edited: 2026-11-08

package database

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// stepLog is an injected start extender that records the steps it was asked
// to extend for, in call order. It is safe for concurrent use, as the real
// app.ExtendStart is.
type stepLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *stepLog) extend(step string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, step)
}

func (l *stepLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.steps...)
}

// injectStartExtender installs a recording extender for the test and removes
// it afterwards, so no test leaks one into the next.
func injectStartExtender(t *testing.T) *stepLog {
	t.Helper()
	l := &stepLog{}
	SetStartExtender(l.extend)
	t.Cleanup(func() { SetStartExtender(nil) })
	return l
}

// TestRunMigrationsExtendsStartTimeoutBeforeEachUp: every migration that is
// actually applied asks for more start time BEFORE its Up runs, so a long run
// of migrations under Type=notify is not killed at TimeoutStartSec. A
// migration only advanced past (already recorded) does no work and asks for
// nothing.
func TestRunMigrationsExtendsStartTimeoutBeforeEachUp(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	l := injectStartExtender(t)

	var seenInUp [][]string
	up := func(migrationStore) error {
		// The extension must already have been requested when Up starts.
		seenInUp = append(seenInUp, l.snapshot())
		return nil
	}
	recorded := Migration{Version: 9101, Description: "startup-notify recorded", Up: up}
	swapMigrationRegistry(t, []Migration{
		recorded,
		{Version: 9102, Description: "startup-notify probe a", Up: up},
		{Version: 9103, Description: "startup-notify probe b", Up: up},
	})
	require.NoError(t, setVersion(store, 9100))
	require.NoError(t, recordMigration(store, recorded))

	require.NoError(t, RunMigrations(store))
	require.Equal(t, [][]string{
		{"migration 9102"},
		{"migration 9102", "migration 9103"},
	}, seenInUp, "one extension per applied migration, requested before its Up; none for the recorded one")
	require.Equal(t, []string{"migration 9102", "migration 9103"}, l.snapshot())
}

// TestInitializeStoreExtendsStartTimeoutBeforeOpen: opening the store (which
// can replay a large WAL) is preceded by an extension.
func TestInitializeStoreExtendsStartTimeoutBeforeOpen(t *testing.T) {
	l := injectStartExtender(t)
	swapMigrationRegistry(t, nil)
	store, err := InitializeStore("pebble", filepath.Join(t.TempDir(), "s.pebble"))
	require.NoError(t, err)
	t.Cleanup(func() {
		SetGlobalStore(nil)
		_ = store.Close()
	})
	require.Equal(t, []string{"store open"}, l.snapshot())
}

// TestExtendStartTimeoutWithoutExtenderIsNoOp: with no extender injected (every
// command but serve, child mode, tests) nothing is requested and nothing fails.
func TestExtendStartTimeoutWithoutExtenderIsNoOp(t *testing.T) {
	SetStartExtender(nil)
	extendStartTimeout("store open")
}

// TestSetStartExtenderReplacesAndRemoves: the last injected func is the one
// called, and nil removes it.
func TestSetStartExtenderReplacesAndRemoves(t *testing.T) {
	first, second := &stepLog{}, &stepLog{}
	t.Cleanup(func() { SetStartExtender(nil) })
	SetStartExtender(first.extend)
	SetStartExtender(second.extend)
	extendStartTimeout("a")
	SetStartExtender(nil)
	extendStartTimeout("b")
	require.Empty(t, first.snapshot())
	require.Equal(t, []string{"a"}, second.snapshot())
}
