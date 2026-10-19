// file: internal/server/collection_refresh_op_test.go
// version: 1.2.0
// guid: 9a4e1c27-5d83-4b60-8f12-0c7b3d6e9a58
// last-edited: 2026-10-18

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// The refresh op is registered in a booted server, is a previewed writer gated on
// collections.manage, and an omitted mode is a preview: only the hourly task's
// explicit dry_run=false writes.
func TestCollectionRefreshOp_IsRegisteredAsAGatedPreviewWriter(t *testing.T) {
	srv, ids, _ := bootRegisteredOpIDs(t)
	require.True(t, ids[collectionRefreshOpID], "the op must be registered")

	def, ok := srv.opRegistry.Def(collectionRefreshOpID)
	require.True(t, ok)
	assert.False(t, def.Preview.IsZero(), "a writer declares its preview")
	assert.True(t, canWrite(def))
	assert.Equal(t, []auth.Permission{auth.PermCollectionsManage}, def.Permissions)
	assert.Equal(t, collectionRefreshOpID, def.ConcurrencyKey, "two refreshes never run at once")

	for raw, want := range map[string]bool{`{}`: true, ``: true, `{"dry_run":true}`: true, `{"dry_run":false}`: false} {
		var p collectionRefreshParams
		if raw != "" {
			require.NoError(t, json.Unmarshal([]byte(raw), &p))
		}
		got, err := opmode.ResolveDryRun(collectionRefreshOpID, p.DryRun, p.DryRunCamel)
		require.NoError(t, err)
		assert.Equal(t, want, got, "params %q", raw)
	}
}

type refreshOpReporter struct {
	mu       sync.Mutex
	logs     []string
	progress int
}

func (r *refreshOpReporter) UpdateProgress(int, int, string) error {
	r.mu.Lock()
	r.progress++
	r.mu.Unlock()
	return nil
}
func (r *refreshOpReporter) Log(_ slog.Level, m string, _ ...slog.Attr) error {
	r.mu.Lock()
	r.logs = append(r.logs, m)
	r.mu.Unlock()
	return nil
}
func (r *refreshOpReporter) Logger() *slog.Logger                       { return slog.Default() }
func (r *refreshOpReporter) Checkpoint(any) error                       { return nil }
func (r *refreshOpReporter) IsCanceled() bool                           { return false }
func (r *refreshOpReporter) Trigger(context.Context, string, any) error { return nil }
func (r *refreshOpReporter) SetCurrentItem(string)                      {}
func (r *refreshOpReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, opsregistry.Reporter) error) error {
	return fn(ctx, r)
}

// With the search index closed (a test server never opens one) the op finishes
// cleanly with ONE log line, instead of failing and warning per collection, and
// the scheduler's precondition says there is nothing to do.
func TestCollectionRefreshOp_ClosedIndexIsACleanNoOp(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	for _, name := range []string{"one", "two", "three"} {
		_, err := store.CreateCollection(&database.Collection{Name: name, Type: database.CollectionTypeDynamic,
			Query: "date_added:>-30d", CreatedByUserID: "alice", MaterializedBookIDs: []string{"b9"}})
		require.NoError(t, err)
	}
	srv := newTestServer(t, store)
	require.Nil(t, srv.SearchIndex(), "this fixture has no open index")

	pending, err := srv.collectionsRefreshPending()
	require.NoError(t, err)
	assert.False(t, pending, "no open index: the tick has nothing to do")

	rep := &refreshOpReporter{}
	err = srv.runCollectionRefreshOp(context.Background(), json.RawMessage(`{"dry_run":false}`), rep)
	require.NoError(t, err, "a closed index is not a failed op")
	require.Len(t, rep.logs, 1, "one line for the run, not one per collection")
	assert.Contains(t, rep.logs[0], "search index not open")
	assert.Contains(t, rep.logs[0], "3 relative-date")
	assert.NotContains(t, rep.logs[0], "dry run", "a real run carries no dry-run prefix")
	assert.Positive(t, rep.progress, "a manual-liveness op must report progress")

	cols, _, err := store.ListCollections("", 0, 0)
	require.NoError(t, err)
	for _, c := range cols {
		assert.Equal(t, []string{"b9"}, c.MaterializedBookIDs, "%s untouched", c.Name)
		assert.EqualValues(t, 1, c.Version, "%s not rewritten", c.Name)
	}
}

// A dry run that meets a closed index says so in the same "dry run: " style as
// the normal path's summary line.
func TestCollectionRefreshOp_ClosedIndexDryRunIsPrefixed(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.CreateCollection(&database.Collection{Name: "one", Type: database.CollectionTypeDynamic,
		Query: "date_added:>-30d", CreatedByUserID: "alice"})
	require.NoError(t, err)
	srv := newTestServer(t, store)
	require.Nil(t, srv.SearchIndex(), "this fixture has no open index")

	rep := &refreshOpReporter{}
	require.NoError(t, srv.runCollectionRefreshOp(context.Background(), json.RawMessage(`{"dry_run":true}`), rep))
	require.Len(t, rep.logs, 1)
	assert.True(t, strings.HasPrefix(rep.logs[0], "dry run: search index not open"), rep.logs[0])
}

// The server must hand the scheduler its collectionsRefreshPending hook. A nil
// hook means "always enqueue", so a dropped wiring line would silently bring
// back the hourly enqueue; with no open index the real hook says false.
func TestSchedulerDeps_WiresCollectionsRefreshPending(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	srv := newTestServer(t, store)

	deps := srv.schedulerDeps()
	require.NotNil(t, deps.CollectionsRefreshPending, "nil would mean always enqueue")
	pending, err := deps.CollectionsRefreshPending()
	require.NoError(t, err)
	assert.False(t, pending, "this is the server's hook: a closed index has nothing to do")
}
