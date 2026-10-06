// file: internal/database/bookfiles_complete_test.go
// version: 1.0.0
// guid: 4acc5892-228f-48a8-8d76-a53a58f7c9c9
// last-edited: 2026-10-06

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetAllBookFilesCoreComplete_RefusesShortMemdbAndFallsThrough: the
// complete book-file listing (the chapter-fragment fixer's whole-library
// apply re-check) never answers from a memdb that lost a book_file row. The
// memdb getter refuses; PebbleStore falls through to the authoritative scan,
// whose answer holds the lost row. The plain getter keeps serving the short
// memdb answer (it is on hot paths), which is what this guard exists to avoid.
func TestGetAllBookFilesCoreComplete_RefusesShortMemdbAndFallsThrough(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	t.Cleanup(cleanup)
	p, ok := store.(*PebbleStore)
	require.True(t, ok)
	b, err := store.CreateBook(&Book{Title: "Complete Files", FilePath: "/lib/complete-files"})
	require.NoError(t, err)
	bf := &BookFile{BookID: b.ID, FilePath: "/lib/complete-files/01.mp3", FileSize: 1234, Duration: 60}
	require.NoError(t, store.CreateBookFile(bf))
	p.WaitForWarmup()
	require.True(t, p.IsMemReady())

	_, err = p.mem().GetAllBookFilesCoreComplete()
	require.NoError(t, err, "control: an intact memdb answers")

	func() {
		txn := p.mem().db.Txn(true)
		defer txn.Commit()
		raw, err := txn.First(memTableBookFiles, memIdxID, bf.ID)
		require.NoError(t, err)
		require.NotNil(t, raw, "fixture check: the row must be resident in memdb first")
		require.NoError(t, txn.Delete(memTableBookFiles, raw))
	}()
	p.mem().recordLostRows(memTableBookFiles, 1)

	_, err = p.mem().GetAllBookFilesCoreComplete()
	require.ErrorIs(t, err, ErrMemdbIncomplete)

	ids := func(rows []BookFileCore) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	short, err := p.GetAllBookFilesCore()
	require.NoError(t, err)
	require.NotContains(t, ids(short), bf.ID, "fixture check: the plain getter is the short answer")

	got, err := p.GetAllBookFilesCoreComplete()
	require.NoError(t, err, "PebbleStore falls through rather than refusing")
	require.Contains(t, ids(got), bf.ID, "the lost row is in the authoritative answer")
}
