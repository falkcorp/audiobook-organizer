// file: internal/server/collection_owner_backfill_test.go
// version: 1.1.0
// guid: 91a4c7e2-5d38-4b6f-8c10-e3f7a2d9b564
// last-edited: 2026-10-18

package server

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
)

// fakeOwnerStore is an in-memory collectionOwnerStore. UpdateCollection bumps
// Version like the real store; conflictOn names ids whose write is refused.
type fakeOwnerStore struct {
	cols       []database.Collection
	users      []database.User
	writes     int
	conflictOn map[string]bool
}

func (f *fakeOwnerStore) ListCollections(t string, _, _ int) ([]database.Collection, int, error) {
	var out []database.Collection
	for _, c := range f.cols {
		if t == "" || c.Type == t {
			out = append(out, c)
		}
	}
	return out, len(out), nil
}

func (f *fakeOwnerStore) ListUsers() ([]database.User, error) { return f.users, nil }

func (f *fakeOwnerStore) UpdateCollection(col *database.Collection) error {
	if f.conflictOn[col.ID] {
		return fmt.Errorf("collection %s: %w", col.ID, database.ErrCollectionVersionConflict)
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

func (f *fakeOwnerStore) byID(id string) database.Collection {
	for _, c := range f.cols {
		if c.ID == id {
			return c
		}
	}
	return database.Collection{}
}

func ownerBackfillUsers() []database.User {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return []database.User{
		// Earliest admin, but deactivated: never the owner.
		{ID: "carol", Username: "carol", Roles: []string{auth.SeedRoleAdmin}, Status: "inactive", CreatedAt: t0.Add(-time.Hour)},
		{ID: "alice", Username: "alice", Roles: []string{auth.SeedRoleAdmin}, Status: "active", CreatedAt: t0},
		{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active", CreatedAt: t0.Add(time.Hour)},
	}
}

func ownerBackfillCollections() []database.Collection {
	dyn := func(id, owner string) database.Collection {
		return database.Collection{ID: id, Name: id, Type: database.CollectionTypeDynamic,
			Query: "title:x", CreatedByUserID: owner, Version: 1,
			MaterializedBookIDs: []string{"b9"}}
	}
	return []database.Collection{
		dyn("dyn-empty", ""),
		dyn("dyn-local", "_local"),
		dyn("dyn-gone", "deleted-user"),
		dyn("dyn-live", "bob"),
		{ID: "static-empty", Name: "static-empty", Type: database.CollectionTypeStatic,
			BookIDs: []string{"b1"}, Version: 1},
	}
}

func TestBackfillCollectionOwners(t *testing.T) {
	store := &fakeOwnerStore{users: ownerBackfillUsers(), cols: ownerBackfillCollections()}

	res, err := backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Reassigned)
	assert.Zero(t, res.Rematerialized, "no rematerializer was given")
	assert.False(t, res.NoAdmin)

	for _, id := range []string{"dyn-empty", "dyn-local", "dyn-gone"} {
		c := store.byID(id)
		assert.Equal(t, "alice", c.CreatedByUserID,
			"%s: the first ACTIVE admin (carol is earlier but inactive)", id)
		assert.Equal(t, 2, c.Version, "%s: written through the update path, so Version bumped", id)
		assert.Equal(t, []string{"b9"}, c.MaterializedBookIDs, "%s: members untouched without an index", id)
	}
	live := store.byID("dyn-live")
	assert.Equal(t, "bob", live.CreatedByUserID, "a live owner is never reassigned")
	assert.Equal(t, 1, live.Version)
	st := store.byID("static-empty")
	assert.Equal(t, "", st.CreatedByUserID, "a static collection is never touched")
	assert.Equal(t, 1, st.Version)

	// Second run: nothing needs an owner, nothing is written.
	writesBefore := store.writes
	res, err = backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, collectionOwnerBackfillResult{}, res)
	assert.Equal(t, writesBefore, store.writes, "a second run must change nothing")
}

func TestBackfillCollectionOwners_FirstAdminTieBreak(t *testing.T) {
	same := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeOwnerStore{
		users: []database.User{
			{ID: "admin-b", Username: "b", Roles: []string{auth.SeedRoleAdmin}, Status: "active", CreatedAt: same},
			{ID: "admin-a", Username: "a", Roles: []string{auth.SeedRoleAdmin}, Status: "active", CreatedAt: same},
		},
		cols: []database.Collection{{ID: "c1", Name: "c1", Type: database.CollectionTypeDynamic,
			Query: "title:x", Version: 1}},
	}
	_, err := backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "admin-a", store.byID("c1").CreatedByUserID,
		"equal CreatedAt is broken by the smaller id")
}

func TestBackfillCollectionOwners_NoAdminLeavesRows(t *testing.T) {
	store := &fakeOwnerStore{
		users: []database.User{{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active"}},
		cols:  ownerBackfillCollections(),
	}
	res, err := backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.True(t, res.NoAdmin)
	assert.Zero(t, res.Reassigned)
	assert.Zero(t, store.writes)
	assert.Equal(t, "", store.byID("dyn-empty").CreatedByUserID)
}

func TestBackfillCollectionOwners_NothingToDoWritesNothing(t *testing.T) {
	store := &fakeOwnerStore{users: ownerBackfillUsers(), cols: []database.Collection{
		{ID: "s", Name: "s", Type: database.CollectionTypeStatic, Version: 1},
	}}
	res, err := backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, collectionOwnerBackfillResult{}, res)
	assert.Zero(t, store.writes)
}

func TestBackfillCollectionOwners_Rematerialize(t *testing.T) {
	store := &fakeOwnerStore{users: ownerBackfillUsers(), cols: ownerBackfillCollections()}
	var asUsers []string
	remat := func(col *database.Collection, ownerID string) ([]string, error) {
		asUsers = append(asUsers, ownerID)
		if col.ID == "dyn-gone" {
			return nil, playlist.ErrSearchIndexUnavailable
		}
		return []string{"b1", "b2"}, nil
	}
	res, err := backfillCollectionOwners(store, remat, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Reassigned)
	assert.Equal(t, 2, res.Rematerialized)
	assert.Equal(t, []string{"alice", "alice", "alice"}, asUsers, "evaluated as the NEW owner")
	assert.Equal(t, []string{"b1", "b2"}, store.byID("dyn-empty").MaterializedBookIDs)
	assert.Equal(t, []string{"b9"}, store.byID("dyn-gone").MaterializedBookIDs,
		"a failed evaluation leaves the stored members for the next materialize")
	assert.Equal(t, "alice", store.byID("dyn-gone").CreatedByUserID, "the owner is still assigned")
}

func TestBackfillCollectionOwners_ConflictIsSkippedNotFatal(t *testing.T) {
	store := &fakeOwnerStore{
		users: ownerBackfillUsers(), cols: ownerBackfillCollections(),
		conflictOn: map[string]bool{"dyn-local": true},
	}
	res, err := backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Reassigned)
	assert.Equal(t, 1, res.Conflicts)
	assert.Equal(t, "_local", store.byID("dyn-local").CreatedByUserID, "left for the next boot")

	store.conflictOn = nil
	res, err = backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Reassigned, "the next run finishes the skipped one")
}

// TestBackfillCollectionOwners_RealStore runs the same shape against a real
// PebbleStore, so Version bumping and the user listing are the store's own.
func TestBackfillCollectionOwners_RealStore(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db.pebble"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	admin, err := store.CreateUser("alice", "a@example.test", "bcrypt", "hash", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	_, err = store.CreateUser("bob", "b@example.test", "bcrypt", "hash", []string{"viewer"}, "active")
	require.NoError(t, err)

	mk := func(name, typ, query, owner string) *database.Collection {
		col, cerr := store.CreateCollection(&database.Collection{
			Name: name, Type: typ, Query: query, CreatedByUserID: owner,
		})
		require.NoError(t, cerr)
		return col
	}
	local := mk("Local", database.CollectionTypeDynamic, "title:x", "_local")
	gone := mk("Gone", database.CollectionTypeDynamic, "title:x", "deleted-user")
	static := mk("Static", database.CollectionTypeStatic, "", "")

	res, err := backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Reassigned)

	for _, id := range []string{local.ID, gone.ID} {
		got, gerr := store.GetCollection(id)
		require.NoError(t, gerr)
		assert.Equal(t, admin.ID, got.CreatedByUserID)
		assert.Equal(t, 2, got.Version)
	}
	got, err := store.GetCollection(static.ID)
	require.NoError(t, err)
	assert.Equal(t, "", got.CreatedByUserID)
	assert.Equal(t, 1, got.Version)

	res, err = backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, collectionOwnerBackfillResult{}, res)
}

// recordingActivityWriter stands in for the system activity log.
type recordingActivityWriter struct{ lines []string }

func (r *recordingActivityWriter) AddSystemActivityLog(source, level, message string) error {
	r.lines = append(r.lines, source+"|"+level+"|"+message)
	return nil
}

// A per-user query on a collection with no usable owner is NOT handed to the
// first admin: that would publish the admin's listening state for a query the
// admin never wrote. It stays ownerless, is listed in one warning, and the run
// stays idempotent in what it writes.
func TestBackfillCollectionOwners_SkipsPerUserQueriesAndListsThem(t *testing.T) {
	perUser := func(id, name, owner, query string) database.Collection {
		return database.Collection{ID: id, Name: name, Type: database.CollectionTypeDynamic,
			Query: query, CreatedByUserID: owner, Version: 1, MaterializedBookIDs: []string{"b9"}}
	}
	cols := append(ownerBackfillCollections(),
		perUser("pu-empty", "Unfinished\nforged line", "", "read_status:unstarted"),
		perUser("pu-local", "Recent", "_local", "title:x AND last_played:>-30d"),
		perUser("pu-gone", "Halfway", "deleted-user", "-progress_pct:>50"),
		perUser("pu-live", "Bob's", "bob", "read_status:finished"), // owned: untouched, not listed
	)
	store := &fakeOwnerStore{users: ownerBackfillUsers(), cols: cols}
	act := &recordingActivityWriter{}
	log := logger.NewWithActivityLog("collection-owner-backfill", act)

	res, err := backfillCollectionOwners(store, nil, log)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Reassigned, "the three title-only orphans still get the admin")
	assert.Equal(t, 3, res.SkippedPerUser)
	for _, id := range []string{"pu-empty", "pu-local", "pu-gone"} {
		c := store.byID(id)
		assert.Equal(t, map[string]string{"pu-empty": "", "pu-local": "_local", "pu-gone": "deleted-user"}[id],
			c.CreatedByUserID, "%s: left exactly as it was", id)
		assert.Equal(t, 1, c.Version, "%s: not written", id)
		assert.Equal(t, []string{"b9"}, c.MaterializedBookIDs, "%s: stored members untouched", id)
	}
	assert.Equal(t, "bob", store.byID("pu-live").CreatedByUserID)
	assert.Equal(t, "alice", store.byID("dyn-empty").CreatedByUserID)

	// ONE warning, naming ids and names, reaching the activity log.
	var warnings []string
	for _, l := range act.lines {
		if strings.Contains(l, "|warn|") {
			warnings = append(warnings, l)
		}
	}
	require.Len(t, warnings, 1, "lines: %v", act.lines)
	w := warnings[0]
	assert.True(t, strings.HasPrefix(w, "collection-owner-backfill|"), w)
	for _, want := range []string{"3 dynamic collection(s)", "pu-empty", "pu-local", "pu-gone", "Recent", "Halfway",
		"last_played, progress_pct, read_status"} {
		assert.Contains(t, w, want)
	}
	assert.NotContains(t, w, "pu-live", "an owned collection is not listed")
	assert.NotContains(t, w, "dyn-", "assigned collections are not listed")
	assert.NotContains(t, w, "\n", "a collection name cannot forge a second log line")

	// Idempotent: nothing more is written, and the same collections are listed again.
	writes := store.writes
	act.lines = nil
	res, err = backfillCollectionOwners(store, nil, log)
	require.NoError(t, err)
	assert.Equal(t, collectionOwnerBackfillResult{SkippedPerUser: 3}, res)
	assert.Equal(t, writes, store.writes, "a second run writes nothing")
	require.Len(t, act.lines, 1, "the listing repeats until an owner takes them: %v", act.lines)
}

// Per-user orphans are listed even when there is no admin to give anything to.
func TestBackfillCollectionOwners_PerUserOrphansListedWithoutAdmin(t *testing.T) {
	store := &fakeOwnerStore{
		users: []database.User{{ID: "bob", Username: "bob", Roles: []string{"viewer"}, Status: "active"}},
		cols: []database.Collection{{ID: "pu", Name: "Orphan", Type: database.CollectionTypeDynamic,
			Query: "read_status:finished", Version: 1}},
	}
	act := &recordingActivityWriter{}
	res, err := backfillCollectionOwners(store, nil, logger.NewWithActivityLog("collection-owner-backfill", act))
	require.NoError(t, err)
	assert.Equal(t, 1, res.SkippedPerUser)
	assert.False(t, res.NoAdmin, "nothing needed an admin")
	require.Len(t, act.lines, 1)
	assert.Contains(t, act.lines[0], "pu (Orphan)")
}

// The listing is capped so one install with many cannot write an unbounded line.
func TestUnownedPerUserWarning_CapsTheList(t *testing.T) {
	var cols []database.Collection
	for i := 0; i < maxListedUnowned+7; i++ {
		cols = append(cols, database.Collection{ID: fmt.Sprintf("id%03d", i), Name: "n"})
	}
	w := unownedPerUserWarning(cols)
	assert.Contains(t, w, fmt.Sprintf("%d dynamic collection(s)", maxListedUnowned+7))
	assert.Contains(t, w, "and 7 more")
	assert.Contains(t, w, "id000")
	assert.NotContains(t, w, fmt.Sprintf("id%03d", maxListedUnowned))
}

// Same shape against a real PebbleStore: the per-user orphan is skipped, the
// ordinary one is assigned, and a second run changes nothing.
func TestBackfillCollectionOwners_RealStoreSkipsPerUser(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db.pebble"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	admin, err := store.CreateUser("alice", "a@example.test", "bcrypt", "hash", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	mk := func(name, query string) *database.Collection {
		col, cerr := store.CreateCollection(&database.Collection{
			Name: name, Type: database.CollectionTypeDynamic, Query: query, CreatedByUserID: "deleted-user"})
		require.NoError(t, cerr)
		return col
	}
	plain := mk("Plain", "title:x")
	pu := mk("PerUser", "read_status:finished")

	res, err := backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, collectionOwnerBackfillResult{Reassigned: 1, SkippedPerUser: 1}, res)
	got, err := store.GetCollection(plain.ID)
	require.NoError(t, err)
	assert.Equal(t, admin.ID, got.CreatedByUserID)
	got, err = store.GetCollection(pu.ID)
	require.NoError(t, err)
	assert.Equal(t, "deleted-user", got.CreatedByUserID)
	assert.Equal(t, 1, got.Version, "not written")

	res, err = backfillCollectionOwners(store, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, collectionOwnerBackfillResult{SkippedPerUser: 1}, res)
}
