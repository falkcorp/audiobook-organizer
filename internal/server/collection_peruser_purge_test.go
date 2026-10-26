// file: internal/server/collection_peruser_purge_test.go
// version: 1.2.0
// guid: 7c2a9e54-1b38-4d60-8f17-a5e3b9d0c682
// last-edited: 2026-10-25

package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// fakePurgeStore is an in-memory collectionPurgeStore. UpdateCollection bumps
// Version like the real store; conflictOn names ids whose write is refused.
type fakePurgeStore struct {
	cols       []database.Collection
	writes     int
	conflictOn map[string]bool
	// failOn names ids whose write the store refuses with an ordinary error (a
	// blank-name row failing validation, say), in the order they are listed.
	failOn map[string]error
}

func (f *fakePurgeStore) ListCollections(t string, _, _ int) ([]database.Collection, int, error) {
	var out []database.Collection
	for _, c := range f.cols {
		if t == "" || c.Type == t {
			cp := c
			out = append(out, cp)
		}
	}
	return out, len(out), nil
}

func (f *fakePurgeStore) UpdateCollection(col *database.Collection) error {
	if f.conflictOn[col.ID] {
		return fmt.Errorf("collection %s: %w", col.ID, database.ErrCollectionVersionConflict)
	}
	if err := f.failOn[col.ID]; err != nil {
		return err
	}
	for i := range f.cols {
		if f.cols[i].ID == col.ID {
			f.writes++
			cp := *col
			cp.Version = f.cols[i].Version + 1
			f.cols[i] = cp
			return nil
		}
	}
	return errors.New("not found")
}

func (f *fakePurgeStore) byID(id string) database.Collection {
	for _, c := range f.cols {
		if c.ID == id {
			return c
		}
	}
	return database.Collection{}
}

type purgeLogRecorder struct{ lines, warns []string }

func (r *purgeLogRecorder) Info(msg string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(msg, args...))
}

func (r *purgeLogRecorder) Warn(msg string, args ...any) {
	r.warns = append(r.warns, fmt.Sprintf(msg, args...))
}

func purgeFixture() *fakePurgeStore {
	dyn := func(id, query string) database.Collection {
		return database.Collection{ID: id, Name: id, Type: database.CollectionTypeDynamic,
			Query: query, CreatedByUserID: "alice", Version: 1, MaterializedBookIDs: []string{"b1", "b2"}}
	}
	return &fakePurgeStore{cols: []database.Collection{
		dyn("finished", "read_status:finished"),
		dyn("recent", "last_played:>-30d"),
		dyn("negated", "-read_status:unstarted"),
		dyn("progress", "progress_pct:>50"),
		dyn("plain", "title:Alpha"),
		dyn("relative-plain", "date_added:>-7d"),
		// Queries that no longer parse. Nobody can say they read no per-user state,
		// and a list they hold may be a creator's: the purge fails closed and
		// clears them, the same answer every read path gives (an unparseable
		// per-user query is per-viewer).
		dyn("unparseable-per-user", `read_status:finished "unterminated`),
		dyn("unparseable-plain", `title:"unterminated`),
		{ID: "empty", Name: "empty", Type: database.CollectionTypeDynamic, Query: "read_status:finished", Version: 1},
		{ID: "static", Name: "static", Type: database.CollectionTypeStatic, BookIDs: []string{"b1"}, Version: 1,
			MaterializedBookIDs: []string{"b9"}},
	}}
}

// The stored list of a per-user collection is the creator's listening state; the
// purge removes it, and only it, from every per-user dynamic collection.
func TestPurgePerViewerCollectionMembers(t *testing.T) {
	store := purgeFixture()
	rec := &purgeLogRecorder{}

	res, err := purgePerViewerCollectionMembers(store, rec)
	require.NoError(t, err)
	assert.Equal(t, collectionPurgeResult{Cleared: 6}, res)

	for _, id := range []string{"finished", "recent", "negated", "progress", "unparseable-per-user", "unparseable-plain"} {
		assert.Empty(t, store.byID(id).MaterializedBookIDs, "%s: the creator's stored members are gone", id)
		assert.Equal(t, "alice", store.byID(id).CreatedByUserID, "%s: attribution is untouched", id)
	}
	for _, id := range []string{"plain", "relative-plain"} {
		assert.Equal(t, []string{"b1", "b2"}, store.byID(id).MaterializedBookIDs, "%s: a query without a per-user field keeps its list", id)
	}
	assert.Equal(t, []string{"b9"}, store.byID("static").MaterializedBookIDs, "a static collection is never touched")
	assert.Equal(t, 6, store.writes, "only rows that held a list are written")
	require.Len(t, rec.lines, 1)
	assert.Contains(t, rec.lines[0], "cleared the stored member list of 6")
}

func TestPurgePerViewerCollectionMembersIsIdempotent(t *testing.T) {
	store := purgeFixture()
	_, err := purgePerViewerCollectionMembers(store, &purgeLogRecorder{})
	require.NoError(t, err)
	writes := store.writes

	rec := &purgeLogRecorder{}
	res, err := purgePerViewerCollectionMembers(store, rec)
	require.NoError(t, err)
	assert.Equal(t, collectionPurgeResult{}, res)
	assert.Equal(t, writes, store.writes, "a second run writes nothing")
	assert.Empty(t, rec.lines, "and says nothing")
}

func TestPurgePerViewerCollectionMembersSkipsAConflictAndRetriesNextBoot(t *testing.T) {
	store := purgeFixture()
	store.conflictOn = map[string]bool{"finished": true}

	res, err := purgePerViewerCollectionMembers(store, &purgeLogRecorder{})
	require.NoError(t, err)
	assert.Equal(t, collectionPurgeResult{Cleared: 5, Conflicts: 1}, res)
	assert.NotEmpty(t, store.byID("finished").MaterializedBookIDs, "the conflicted row is left for the next boot")

	store.conflictOn = nil
	res, err = purgePerViewerCollectionMembers(store, &purgeLogRecorder{})
	require.NoError(t, err)
	assert.Equal(t, collectionPurgeResult{Cleared: 1}, res)
	assert.Empty(t, store.byID("finished").MaterializedBookIDs)
}

// One row the store refuses must not stop the run: the rows listed after it are
// still cleared, the failure is counted and logged with its id, and the next boot
// retries it. (The purge used to return at the first non-conflict error, so a
// single bad row, listed early by name, left every row after it holding a
// creator's list on every boot.)
func TestPurgePerViewerCollectionMembersContinuesPastARowTheStoreRefuses(t *testing.T) {
	store := purgeFixture()
	// "finished" is listed first; "progress" and the unparseable rows come after it.
	store.failOn = map[string]error{"finished": errors.New("collection: name required")}
	rec := &purgeLogRecorder{}

	res, err := purgePerViewerCollectionMembers(store, rec)
	require.NoError(t, err, "a refused row is counted and logged, it does not fail the run")
	assert.Equal(t, collectionPurgeResult{Cleared: 5, Failed: 1}, res)
	assert.NotEmpty(t, store.byID("finished").MaterializedBookIDs, "the refused row is left for the next boot")
	for _, id := range []string{"recent", "negated", "progress", "unparseable-per-user", "unparseable-plain"} {
		assert.Empty(t, store.byID(id).MaterializedBookIDs, "%s: a row after the bad one is still cleared", id)
	}
	require.Len(t, rec.warns, 1)
	assert.Contains(t, rec.warns[0], "finished")
	assert.Contains(t, rec.warns[0], "name required")
	require.Len(t, rec.lines, 1)
	assert.Contains(t, rec.lines[0], "1 on a write failure")

	store.failOn = nil
	res, err = purgePerViewerCollectionMembers(store, &purgeLogRecorder{})
	require.NoError(t, err)
	assert.Equal(t, collectionPurgeResult{Cleared: 1}, res, "the next boot clears it")
}

// A purge that works against the real store, not only the fake: the stored list
// really is gone after a round trip through Pebble.
func TestPurgePerViewerCollectionMembersAgainstTheRealStore(t *testing.T) {
	ps, err := database.NewPebbleStoreInMemory(t.TempDir() + "/c.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	// The store now drops a per-viewer list on every write, so a LEGACY row (the
	// thing the purge exists for) can only be seeded with that classifier off.
	database.SetCollectionPerViewerClassifier(nil)
	created, err := ps.CreateCollection(&database.Collection{
		Name: "Finished", Type: database.CollectionTypeDynamic, Query: "read_status:finished",
		CreatedByUserID: "alice", MaterializedBookIDs: []string{"b1"},
	})
	database.SetCollectionPerViewerClassifier(search.QueryMayUsePerUserField)
	require.NoError(t, err)
	legacy, err := ps.GetCollection(created.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"b1"}, legacy.MaterializedBookIDs, "fixture: the legacy row holds a creator's list")

	res, err := purgePerViewerCollectionMembers(ps, &purgeLogRecorder{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Cleared)
	got, err := ps.GetCollection(created.ID)
	require.NoError(t, err)
	assert.Empty(t, got.MaterializedBookIDs)
	assert.Equal(t, "read_status:finished", got.Query)
}

// The server's store drops a per-viewer collection's stored list on every write,
// but only because internal/search registered its detector with the store at
// init. An unregistered store keeps the list (a silent fail-open), so this proves
// the registration holds in the server's own build, through the real store, for
// both the create and the update path, and for a query that does not parse.
func TestProductionStoreScrubsPerViewerMembers(t *testing.T) {
	ps, err := database.NewPebbleStoreInMemory(t.TempDir() + "/c.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	for name, query := range map[string]string{
		"per-user":    "read_status:finished",
		"unparseable": `read_status:finished "unterminated`,
	} {
		created, err := ps.CreateCollection(&database.Collection{
			Name: name, Type: database.CollectionTypeDynamic, Query: query,
			MaterializedBookIDs: []string{"b1"},
		})
		require.NoError(t, err)
		got, _ := ps.GetCollection(created.ID)
		assert.Empty(t, got.MaterializedBookIDs, "%s: create stored a list", name)

		got.MaterializedBookIDs = []string{"b1", "b2"}
		require.NoError(t, ps.UpdateCollection(got))
		got, _ = ps.GetCollection(created.ID)
		assert.Empty(t, got.MaterializedBookIDs, "%s: update stored a list", name)
	}

	plain, err := ps.CreateCollection(&database.Collection{
		Name: "plain", Type: database.CollectionTypeDynamic, Query: "title:Alpha",
		MaterializedBookIDs: []string{"b1"},
	})
	require.NoError(t, err)
	got, _ := ps.GetCollection(plain.ID)
	assert.Equal(t, []string{"b1"}, got.MaterializedBookIDs, "a query with no per-user field keeps its list")
}
