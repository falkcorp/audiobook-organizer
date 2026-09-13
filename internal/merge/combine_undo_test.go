// file: internal/merge/combine_undo_test.go
// version: 1.0.1
// guid: 0c6e2f94-7b1d-4a58-9e3c-5d8a1f27b640
// last-edited: 2026-09-13

package merge

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bookView is the part of a book row the combine/undo round trip must restore.
type bookView struct {
	Title, FilePath  string
	Narrator         string
	AuthorID         int
	VersionGroupID   string
	Primary, SoftDel bool
}

func viewBook(t *testing.T, store database.Store, id string) bookView {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b, "book %s must exist", id)
	v := bookView{Title: b.Title, FilePath: b.FilePath, SoftDel: b.IsSoftDeleted()}
	if b.Narrator != nil {
		v.Narrator = *b.Narrator
	}
	if b.AuthorID != nil {
		v.AuthorID = *b.AuthorID
	}
	if b.VersionGroupID != nil {
		v.VersionGroupID = *b.VersionGroupID
	}
	if b.IsPrimaryVersion != nil {
		v.Primary = *b.IsPrimaryVersion
	}
	return v
}

type fileView struct {
	BookID, Path string
	Disc, Track  int
}

// viewFiles maps file id -> owner/path/numbers across the given books.
func viewFiles(t *testing.T, store database.Store, bookIDs ...string) map[string]fileView {
	t.Helper()
	out := map[string]fileView{}
	for _, id := range bookIDs {
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		for _, f := range files {
			out[f.ID] = fileView{BookID: f.BookID, Path: f.FilePath, Disc: f.DiscNumber, Track: f.TrackNumber}
		}
	}
	return out
}

func lockFields(t *testing.T, store database.Store, id string) []string {
	t.Helper()
	states, err := store.GetMetadataFieldStates(id)
	require.NoError(t, err)
	var out []string
	for _, st := range states {
		if st.OverrideLocked {
			out = append(out, st.Field)
		}
	}
	slices.Sort(out)
	return out
}

type undoFixture struct {
	survivor, absA, absB string
	pid                  string
	user                 *database.User
	aSync, sSync         string
}

// seedUndoFixture builds every shape the combine touches:
//   - survivor: VIRTUAL single-file book (ensureOwnFile creates its row);
//   - absA: two real file rows with disc/track numbers, an iTunes PID, a
//     version group it is primary of, a sync identity, and listening progress
//     further along than the survivor's;
//   - absB: VIRTUAL single-file book (attachVirtualFile creates its row).
func seedUndoFixture(t *testing.T, store database.Store) undoFixture {
	t.Helper()
	f := undoFixture{
		survivor: ulid.Make().String(),
		absA:     ulid.Make().String(),
		absB:     ulid.Make().String(),
		pid:      "PID-UNDO-" + ulid.Make().String(),
	}
	vg := "vg-" + ulid.Make().String()
	yes := true
	narr := "Old Narrator"
	_, err := store.CreateBook(&database.Book{ID: f.survivor, Title: "Survivor", Format: "mp3", FilePath: "/tmp/undo/s.mp3", Narrator: &narr})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: f.absA, Title: "Part A", Format: "mp3", FilePath: "/tmp/undo/a", VersionGroupID: &vg, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: f.absB, Title: "Part B", Format: "mp3", FilePath: "/tmp/undo/b.mp3"})
	require.NoError(t, err)
	for i, p := range []string{"/tmp/undo/a/01.mp3", "/tmp/undo/a/02.mp3"} {
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			ID: ulid.Make().String(), BookID: f.absA, FilePath: p, Format: "mp3", DiscNumber: 2, TrackNumber: i + 7,
		}))
	}
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: f.pid, BookID: f.absA}))

	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)
	f.aSync, err = ids.MintOrGetSyncID(f.absA)
	require.NoError(t, err)
	f.sSync, err = ids.MintOrGetSyncID(f.survivor)
	require.NoError(t, err)

	f.user = seedSyncUser(t, store)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.absA, Status: "in_progress", ProgressPct: 40, LastActivityAt: at}))
	require.NoError(t, store.SetUserPosition(f.user.ID, f.absA, "seg-a", 1234))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.survivor, Status: "in_progress", ProgressPct: 10, LastActivityAt: at.Add(-time.Hour)}))
	require.NoError(t, store.SetUserPosition(f.user.ID, f.survivor, "seg-s", 55))
	return f
}

func progressView(t *testing.T, store database.Store, userID, bookID string) (int, string, []string) {
	t.Helper()
	st, err := store.GetUserBookState(userID, bookID)
	require.NoError(t, err)
	pos, err := store.ListUserPositionsForBook(userID, bookID)
	require.NoError(t, err)
	var segs []string
	for _, p := range pos {
		segs = append(segs, fmt.Sprintf("%s@%v", p.SegmentID, p.PositionSeconds))
	}
	slices.Sort(segs)
	if st == nil {
		return -1, "", segs
	}
	return st.ProgressPct, st.Status, segs
}

// TestCombineUndo_RoundTripRestoresExactly is the core contract: combine then
// undo returns files (owner, path, disc/track), book fields (incl. override
// title/narrator/author and their lock rows), version group, external IDs, the
// ABS redirect and per-user progress to the pre-combine state.
func TestCombineUndo_RoundTripRestoresExactly(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	all := []string{f.survivor, f.absA, f.absB}

	preBooks := map[string]bookView{}
	for _, id := range all {
		preBooks[id] = viewBook(t, store, id)
	}
	preFiles := viewFiles(t, store, all...)
	preLocks := lockFields(t, store, f.survivor)
	preAProg, preAStatus, preASegs := progressView(t, store, f.user.ID, f.absA)
	preSProg, preSStatus, preSSegs := progressView(t, store, f.user.ID, f.survivor)

	ms := NewService(store)
	res, err := ms.CombineBooks(all, f.survivor, &CombineOverride{Title: "Combined", Narrator: "New Narrator", Author: "Jane Undo"})
	require.NoError(t, err)
	require.NotEmpty(t, res.JournalID)
	assert.Equal(t, 2, res.BooksDeleted)

	// Post-combine: shells soft-deleted, survivor owns 4 files, PID moved,
	// redirect recorded, override applied.
	for _, id := range []string{f.absA, f.absB} {
		assert.True(t, viewBook(t, store, id).SoftDel, "%s soft-deleted", id)
	}
	sFiles, err := store.GetBookFiles(f.survivor)
	require.NoError(t, err)
	require.Len(t, sFiles, 4)
	owner, err := store.GetBookByExternalID("itunes", f.pid)
	require.NoError(t, err)
	assert.Equal(t, f.survivor, owner)
	ids := database.AsSyncIdentityStore(store)
	item, err := ids.ResolveSyncItem(f.aSync)
	require.NoError(t, err)
	assert.Equal(t, f.sSync, item.SyncID, "absorbed id redirects to the survivor while combined")
	assert.Equal(t, "Combined", viewBook(t, store, f.survivor).Title)

	// The regroup multidisc apply stamps numbers AFTER the combine. Simulate
	// it so the test proves undo restores the pre-combine numbers.
	for i := range sFiles {
		sFiles[i].DiscNumber, sFiles[i].TrackNumber = 9, 90+i
		require.NoError(t, store.UpdateBookFile(sFiles[i].ID, &sFiles[i]))
	}

	undo, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{f.absA, f.absB}, undo.RestoredBooks)
	assert.Empty(t, undo.Warnings)

	for _, id := range all {
		assert.Equal(t, preBooks[id], viewBook(t, store, id), "book %s restored exactly", id)
	}
	assert.Equal(t, preFiles, viewFiles(t, store, all...), "file rows restored exactly (created rows gone, moved rows back with their numbers)")
	assert.Equal(t, preLocks, lockFields(t, store, f.survivor), "override lock rows rolled back")
	owner, err = store.GetBookByExternalID("itunes", f.pid)
	require.NoError(t, err)
	assert.Equal(t, f.absA, owner, "PID mapping back on its original book")

	item, err = ids.ResolveSyncItem(f.aSync)
	require.NoError(t, err)
	assert.Equal(t, f.aSync, item.SyncID, "redirect cleared: the restored book resolves to itself")
	sItem, err := ids.ResolveSyncItem(f.sSync)
	require.NoError(t, err)
	assert.NotContains(t, sItem.MergedFrom, f.aSync)

	aProg, aStatus, aSegs := progressView(t, store, f.user.ID, f.absA)
	assert.Equal(t, []any{preAProg, preAStatus, preASegs}, []any{aProg, aStatus, aSegs}, "absorbed progress restored")
	sProg, sStatus, sSegs := progressView(t, store, f.user.ID, f.survivor)
	assert.Equal(t, []any{preSProg, preSStatus, preSSegs}, []any{sProg, sStatus, sSegs}, "survivor progress restored")

	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, CombineJournalUndone, j.Status)
	_, err = ms.UndoCombine(res.JournalID)
	var refused *CombineUndoRefusedError
	require.ErrorAs(t, err, &refused, "an undone combine cannot be undone twice")

	dbtest.AssertStoreInvariants(t, store)
}

// TestCombineUndo_RefusesAfterLaterMergeTouchedFiles: once a later combine
// re-merges the survivor (moving the same file rows again), undoing the FIRST
// combine must refuse, name why, and write nothing. Undoing newest-first then
// unwinds both.
func TestCombineUndo_RefusesAfterLaterMergeTouchedFiles(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	other := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: other, Title: "Other", Format: "mp3", FilePath: "/tmp/undo/o.mp3"})
	require.NoError(t, err)

	ms := NewService(store)
	first, err := ms.CombineBooks([]string{f.survivor, f.absA}, f.survivor, nil)
	require.NoError(t, err)
	second, err := ms.CombineBooks([]string{other, f.survivor}, other, nil)
	require.NoError(t, err)

	before := viewFiles(t, store, other, f.survivor, f.absA)
	_, err = ms.UndoCombine(first.JournalID)
	var refused *CombineUndoRefusedError
	require.ErrorAs(t, err, &refused)
	assert.NotEmpty(t, refused.Reasons)
	joined := fmt.Sprint(refused.Reasons)
	assert.Contains(t, joined, "no longer on survivor", "names the moved files")
	assert.Contains(t, joined, "deleted or merged away", "names the re-merged survivor")
	assert.Equal(t, before, viewFiles(t, store, other, f.survivor, f.absA), "a refused undo writes nothing")
	assert.True(t, viewBook(t, store, f.absA).SoftDel)
	j, err := ms.GetCombineJournal(first.JournalID)
	require.NoError(t, err)
	assert.Equal(t, CombineJournalApplied, j.Status, "a refused undo leaves the journal undoable")

	_, err = ms.UndoCombine(second.JournalID)
	require.NoError(t, err)
	_, err = ms.UndoCombine(first.JournalID)
	require.NoError(t, err, "after the later combine is undone, the first one undoes cleanly")
	assert.False(t, viewBook(t, store, f.absA).SoftDel)
	aFiles, err := store.GetBookFiles(f.absA)
	require.NoError(t, err)
	assert.Len(t, aFiles, 2)
	dbtest.AssertStoreInvariants(t, store)
}

// TestCombineUndo_RefusesWhenAbsorbedRestoredOrFileDeleted covers the other
// "changed since" shapes: a file row deleted after the combine, and an
// absorbed book restored from trash by something else.
func TestCombineUndo_RefusesWhenAbsorbedRestoredOrFileDeleted(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	ms := NewService(store)
	res, err := ms.CombineBooks([]string{f.survivor, f.absA}, f.survivor, nil)
	require.NoError(t, err)

	files, err := store.GetBookFiles(f.survivor)
	require.NoError(t, err)
	var aFile string
	for _, fl := range files {
		if fl.FilePath == "/tmp/undo/a/01.mp3" {
			aFile = fl.ID
		}
	}
	require.NotEmpty(t, aFile)
	require.NoError(t, store.DeleteBookFile(aFile))
	b, err := store.GetBookByID(f.absA)
	require.NoError(t, err)
	b.MarkedForDeletion, b.MarkedForDeletionAt = nil, nil
	_, err = store.UpdateBook(b.ID, b)
	require.NoError(t, err)

	_, err = ms.UndoCombine(res.JournalID)
	var refused *CombineUndoRefusedError
	require.ErrorAs(t, err, &refused)
	joined := fmt.Sprint(refused.Reasons)
	assert.Contains(t, joined, aFile)
	assert.Contains(t, joined, "restored since")
}

// TestCombineUndo_ConcurrentUndosAndCombinesShareTheLock runs many undos of the
// same journal alongside unrelated combines. Under mergeSerializeMu exactly one
// undo may win; every other sees the journal already undone. Run with -race.
func TestCombineUndo_ConcurrentUndosAndCombinesShareTheLock(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	ms := NewService(store)
	res, err := ms.CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)

	type pair struct{ a, b string }
	var pairs []pair
	for i := range 4 {
		p := pair{ulid.Make().String(), ulid.Make().String()}
		for _, id := range []string{p.a, p.b} {
			_, err := store.CreateBook(&database.Book{ID: id, Title: "P", Format: "mp3", FilePath: fmt.Sprintf("/tmp/undo/c%d-%s.mp3", i, id)})
			require.NoError(t, err)
		}
		pairs = append(pairs, p)
	}

	const undoers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, refusals := 0, 0
	for range undoers {
		wg.Go(func() {
			_, err := ms.UndoCombine(res.JournalID)
			var refused *CombineUndoRefusedError
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case assert.ErrorAs(t, err, &refused):
				refusals++
			}
		})
	}
	for _, p := range pairs {
		wg.Go(func() {
			_, err := ms.CombineBooks([]string{p.a, p.b}, p.a, nil)
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	assert.Equal(t, 1, wins, "exactly one undo applies")
	assert.Equal(t, undoers-1, refusals)
	for _, id := range []string{f.absA, f.absB} {
		assert.False(t, viewBook(t, store, id).SoftDel)
	}
	dbtest.AssertStoreInvariants(t, store)
}

// rawFailStore fails every raw-key write, i.e. the journal.
type rawFailStore struct{ database.Store }

func (rawFailStore) SetRaw(string, []byte) error { return fmt.Errorf("injected SetRaw failure") }

// TestCombine_RefusedWhenJournalCannotBeWritten: the journal is written before
// the first mutation, and a combine that cannot be journaled does not happen.
func TestCombine_RefusedWhenJournalCannotBeWritten(t *testing.T) {
	real := setupTestStore(t)
	f := seedUndoFixture(t, real)
	before := viewFiles(t, real, f.survivor, f.absA)

	ms := NewService(rawFailStore{real})
	_, err := ms.CombineBooks([]string{f.survivor, f.absA}, f.survivor, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "undo journal could not be written")
	assert.Equal(t, before, viewFiles(t, real, f.survivor, f.absA), "nothing moved")
	assert.False(t, viewBook(t, real, f.absA).SoftDel, "nothing deleted")
}

// TestCombineUndo_ReattachedRowGoesBackToItsOutsideOwner: a VIRTUAL absorbed
// book whose FilePath is already a file row owned by a book OUTSIDE the
// combine gets that row reattached (moved) to the survivor. Undo must move it
// back to that outside owner, once, and refuse if the owner is gone.
func TestCombineUndo_ReattachedRowGoesBackToItsOutsideOwner(t *testing.T) {
	store := setupTestStore(t)
	survivor, virt, outside := ulid.Make().String(), ulid.Make().String(), ulid.Make().String()
	shared := "/tmp/undo/shared-" + outside + ".mp3"
	for _, b := range []*database.Book{
		{ID: survivor, Title: "S", Format: "mp3", FilePath: "/tmp/undo/rs-" + survivor + ".mp3"},
		{ID: virt, Title: "V", Format: "mp3", FilePath: shared},
		{ID: outside, Title: "O", Format: "mp3", FilePath: "/tmp/undo/ro-" + outside},
	} {
		_, err := store.CreateBook(b)
		require.NoError(t, err)
	}
	rowID := ulid.Make().String()
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: rowID, BookID: outside, FilePath: shared, Format: "mp3", TrackNumber: 4}))
	before := viewFiles(t, store, survivor, virt, outside)

	ms := NewService(store)
	res, err := ms.CombineBooks([]string{survivor, virt}, survivor, nil)
	require.NoError(t, err)
	moved := viewFiles(t, store, survivor)
	require.Contains(t, moved, rowID, "the outside owner's row was reattached to the survivor")

	_, err = ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, before, viewFiles(t, store, survivor, virt, outside), "row back on its outside owner exactly once")

	// Same shape, but the outside owner is deleted before the undo: refuse.
	res2, err := ms.CombineBooks([]string{survivor, virt}, survivor, nil)
	require.NoError(t, err)
	require.NoError(t, store.DeleteBook(outside))
	_, err = ms.UndoCombine(res2.JournalID)
	var refused *CombineUndoRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Contains(t, fmt.Sprint(refused.Reasons), outside)
}
