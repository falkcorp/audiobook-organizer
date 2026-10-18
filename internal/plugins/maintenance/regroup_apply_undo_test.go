// file: internal/plugins/maintenance/regroup_apply_undo_test.go
// version: 1.2.0
// guid: 6a2d9e51-0c47-4f83-b16e-2e9f7c34d0a8
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revertCombineOn finds the combine pointer the apply journaled on survivor
// (a review approval runs in no operation, so the combine minted its own
// operation id), checks it names journalID, and reverts that operation
// through the engine POST /operations/:id/revert runs.
func revertCombineOn(t *testing.T, store database.Store, survivor, journalID string) {
	t.Helper()
	rows, err := store.GetBookChanges(survivor)
	require.NoError(t, err)
	opID := ""
	for _, r := range rows {
		if r.ChangeType == undo.ChangeTypeMergeCombineJournal && !r.Voided {
			require.Empty(t, opID, "one combine, one pointer")
			require.Equal(t, journalID, r.NewValue)
			opID = r.OperationID
		}
	}
	require.NotEmpty(t, opID, "the approval's combine journaled an undo pointer on its survivor")
	res, err := audiobooks.NewRevertService(store).RevertOperation(opID)
	require.NoError(t, err)
	require.Equal(t, 1, res.RestoredTypes[undo.ChangeTypeMergeCombineJournal])
}

// latestJournal returns the id of the newest combine journal.
func latestJournal(t *testing.T, ms *merge.Service) string {
	t.Helper()
	js, err := ms.ListCombineJournals(1)
	require.NoError(t, err)
	require.Len(t, js, 1)
	require.Equal(t, merge.CombineJournalApplied, js[0].Status)
	return js[0].ID
}

type ownedFile struct {
	Book, Path  string
	Disc, Track int
	FP          string
}

func ownedFiles(t *testing.T, store database.Store, ids ...string) map[string]ownedFile {
	t.Helper()
	out := map[string]ownedFile{}
	for _, id := range ids {
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		for _, f := range files {
			out[f.ID] = ownedFile{f.BookID, f.FilePath, f.DiscNumber, f.TrackNumber, string(f.AcoustIDFingerprint)}
		}
	}
	return out
}

// An approved regroup.multidisc hold combines the folder and THEN stamps
// disc/track numbers. Reverting the combine's operation must reverse both: every member live again, owning
// its own file, with the pre-approval (unset) numbers and its fingerprint.
func TestApplyMultidisc_UndoRoundTrip(t *testing.T) {
	store := newApplyTestStore(t)
	paths := []string{"/lib/Undo Book/Undo_1.mp3", "/lib/Undo Book/Undo_2.mp3", "/lib/Undo Book/Undo_3.mp3"}
	ids, _ := seedNumberedBooks(t, store, paths)
	before := ownedFiles(t, store, ids...)

	ms := merge.NewService(store)
	apply := ApplyMultidisc(store, ms)
	require.NoError(t, apply(context.Background(),
		multidiscItemWithNumbers(t, "/lib/Undo Book", ids, paths, []int{0, 0, 0}, []int{1, 2, 3})))

	numbered, err := store.GetBookFiles(minID(ids...))
	require.NoError(t, err)
	require.Len(t, numbered, 3)
	for _, f := range numbered {
		require.NotZero(t, f.TrackNumber, "the apply stamped track numbers")
	}

	revertCombineOn(t, store, minID(ids...), latestJournal(t, ms))

	assert.Equal(t, before, ownedFiles(t, store, ids...), "files back on their books with pre-approval numbers and fingerprints")
	for i, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b)
		assert.False(t, b.IsSoftDeleted(), "%s restored", id)
		assert.Equal(t, paths[i], b.FilePath)
	}
	dbtest.AssertStoreInvariants(t, store)
}

// An approved duplicate-of hold combines folder debris into a canonical book
// outside the folder. All three are virtual single-file books, so the combine
// CREATES file rows (the canonical's own and one per debris). Reverting the
// combine's operation never deletes a book_file row: each debris row is
// repointed to the debris book whose audio it names, the canonical keeps its
// own, and each debris book's FilePath is restored.
func TestApplyDuplicateOf_UndoRoundTrip(t *testing.T) {
	store := newApplyTestStore(t)
	canonical := "zzz" + ulid.Make().String()
	debris1 := "aaa" + ulid.Make().String()
	debris2 := "aab" + ulid.Make().String()
	mkDupBook(t, store, canonical, "The Real Book", "/lib/real/book.mp3")
	mkDupBook(t, store, debris1, "Debris One", "/lib/junk/d1.mp3")
	mkDupBook(t, store, debris2, "Debris Two", "/lib/junk/d2.mp3")
	all := []string{canonical, debris1, debris2}
	before := ownedFiles(t, store, all...)

	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{
		debris1: {pendingCand(debris1, canonical)},
		debris2: {pendingCand(canonical, debris2)},
	}}
	ms := merge.NewService(store)
	require.NoError(t, ApplyDuplicateOf(store, ms, cands)(context.Background(),
		duplicateOfItem(t, "/lib/junk", []string{debris1, debris2})))

	cf, err := store.GetBookFiles(canonical)
	require.NoError(t, err)
	require.Len(t, cf, 3, "canonical owns its own and both debris files while combined")
	require.Empty(t, before, "virtual books: no file rows before the approval")
	rowAt := map[string]string{} // path -> the row the combine created for it
	for _, f := range cf {
		rowAt[f.FilePath] = f.ID
	}

	revertCombineOn(t, store, canonical, latestJournal(t, ms))

	after := ownedFiles(t, store, all...)
	require.Len(t, after, 3, "no book_file row deleted")
	for book, path := range map[string]string{canonical: "/lib/real/book.mp3", debris1: "/lib/junk/d1.mp3", debris2: "/lib/junk/d2.mp3"} {
		id := rowAt[path]
		require.NotEmpty(t, id, "the combine created a row for %s", path)
		assert.Equal(t, ownedFile{Book: book, Path: path}, after[id], "row %s repointed to the book whose audio it names", path)
	}
	for id, path := range map[string]string{canonical: "/lib/real/book.mp3", debris1: "/lib/junk/d1.mp3", debris2: "/lib/junk/d2.mp3"} {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b)
		assert.False(t, b.IsSoftDeleted(), "%s live", id)
		assert.Equal(t, path, b.FilePath, "%s FilePath restored", id)
	}
	dbtest.AssertStoreInvariants(t, store)
}
