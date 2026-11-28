// file: internal/server/task_inputs_test.go
// version: 0.1.0
// guid: eb6c714b-30a6-4732-b2cc-903d60950801
// last-edited: 2026-11-28

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The server hands the books module every input it owns, over a real built
// server, and the repairs binding it returns carries the stores the fixers ask
// for (so *Server satisfies maintenance.FixerStores through it).
// Not parallel: repairsPermServer swaps the process-wide config.
func TestTaskInputs_OverARealServer(t *testing.T) {
	srv, _, _ := repairsPermServer(t, false, false)
	in, err := srv.TaskInputs()
	require.NoError(t, err)

	assert.NotNil(t, in.Maintenance.Jobs)
	assert.NotNil(t, in.Maintenance.G2Host)
	assert.NotNil(t, in.Maintenance.G3Host)
	assert.NotNil(t, in.Maintenance.G4Host)
	assert.NotNil(t, in.Maintenance.Ops)
	assert.NotNil(t, in.Maintenance.AuxStores)
	assert.NotNil(t, in.Maintenance.G7.UserMerge, "the user-merge input is the server's")
	assert.Nil(t, in.Maintenance.Scan, "a native kind stands the scan down through rc.StandDown, not the server's registry")
	assert.NotNil(t, in.Metafetch.Candidates)
	assert.NotNil(t, in.Metafetch.BuildSourceChain, "the booted server has the metadata fetch service")
	assert.NotNil(t, in.Deluge.Source)
	assert.NotNil(t, in.Deluge.Config)
	assert.NotNil(t, in.Acoustid.Plugin, "a build without the acoustid plugin still gets a plugin to no-op")
	assert.NotNil(t, in.Scheduler.Upgrader)
	assert.NotEmpty(t, in.Repairs.Fixers)
	assert.NotNil(t, in.Repairs.Series)
	assert.NotNil(t, in.Repairs.UserState)

	bound := in.Repairs.BindStores(context.Background())
	_, ok := repairs.StoresFrom[maintenanceplugin.FixerStores](bound)
	assert.True(t, ok, "BindStores binds no maintenance.FixerStores")
}

// A server without the metadata fetch service (HEAD tolerated that) still builds
// its inputs, and the kinds that need the service refuse with HEAD's message
// at their run, not at boot.
func TestTaskInputs_AbsentMetadataFetchServiceRefusesAtTheRun(t *testing.T) {
	srv, _, _ := repairsPermServer(t, false, false)
	srv.metadataFetchService = nil
	in, err := srv.TaskInputs()
	require.NoError(t, err)

	assert.Nil(t, in.Metafetch.BuildSourceChain)
	_, _, err = in.Metafetch.Candidates.GetCachedCandidates("book-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metafetch service not available")
	_, err = in.Scheduler.Upgrader.RunUpgrade(context.Background(), 1, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata fetch service not initialized")
	assert.False(t, srv.ScheduleHooks().HasMetadataFetchSvc(), "the schedule hook reads the same absence")
}

// A store that cannot serve a kind's source is a boot error naming the store,
// never a kind that fails at its first run.
func TestTaskInputs_AStoreWithoutACapabilityIsABootError(t *testing.T) {
	srv, _, _ := repairsPermServer(t, false, false)
	srv.store = &database.MockStore{}
	_, err := srv.TaskInputs()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot serve")
}

// AIBatchSubmitter is the untyped nil when AI parsing is off, so the caller's
// nil check holds, and a real submitter when it is on.
func TestAIBatchSubmitter_NilWhenDisabledSubmitterWhenEnabled(t *testing.T) {
	srv, _, _ := repairsPermServer(t, false, false)

	cfg := config.Snapshot()
	cfg.EnableAIParsing, cfg.OpenAIAPIKey = false, ""
	config.ReplaceForTest(cfg)
	require.True(t, srv.AIBatchSubmitter() == nil, "a disabled parser must be the untyped nil, not a typed nil in the interface")

	cfg.EnableAIParsing, cfg.OpenAIAPIKey = true, "sk-test-batman"
	config.ReplaceForTest(cfg)
	require.False(t, srv.AIBatchSubmitter() == nil)
}

// identificationPending reads the identification dirty set the way HEAD's
// identDirtySetNonEmpty does.
func TestIdentificationPending_FollowsTheDirtySet(t *testing.T) {
	srv, _, _ := repairsPermServer(t, false, false)
	store, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)

	pending, err := srv.identificationPending()
	require.NoError(t, err)
	assert.False(t, pending, "an empty dirty set is nothing to advance")

	_, err = store.MarkIdentDirty("book-knightfall")
	require.NoError(t, err)
	pending, err = srv.identificationPending()
	require.NoError(t, err)
	assert.True(t, pending, "a book in the dirty set is pending")

	// A store that does not expose the set answers true (the op refuses with
	// the reason in its log), and none answers false.
	srv.store = &database.MockStore{}
	pending, err = srv.identificationPending()
	require.NoError(t, err)
	assert.True(t, pending)
	srv.store = nil
	pending, err = srv.identificationPending()
	require.NoError(t, err)
	assert.False(t, pending)
}

// Every schedule hook is set and answers from the server.
func TestScheduleHooks_AreAllSetAndAnswerFromTheServer(t *testing.T) {
	srv, _, _ := repairsPermServer(t, false, false)
	h := srv.ScheduleHooks()
	require.NotNil(t, h.HasDedupPlugin)
	require.NotNil(t, h.HasActivitySvc)
	require.NotNil(t, h.HasMetadataFetchSvc)
	require.NotNil(t, h.IdentificationPending)
	require.NotNil(t, h.CollectionsRefreshPending)
	assert.True(t, h.HasMetadataFetchSvc())
	srv.metadataFetchService = nil
	assert.False(t, h.HasMetadataFetchSvc(), "the hook reads the service when asked, not when built")
}
