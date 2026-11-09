// file: internal/plugins/maintenance/folder_books_progress_owner_test.go
// version: 1.0.1
// guid: 53d21039-f771-45b3-b5d6-0eee5d1ae953
// last-edited: 2026-11-09

package maintenance

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// seedListener gives a non-admin user listening progress on bookID: a book
// state with progress and a stored position, the shape BookHasUserProgress
// reads.
func (f *fragFixture) seedListener(t *testing.T, bookID string) string {
	t.Helper()
	u, err := f.s.CreateUser("dick.grayson", "dick.grayson@example.invalid", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: bookID,
		Status: database.UserBookStatusInProgress, ProgressPct: 42, TotalListenedSeconds: 900}))
	files, err := f.s.GetBookFiles(bookID)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	require.NoError(t, f.s.SetUserPosition(u.ID, bookID, files[0].ID, 900))
	return u.ID
}

// Owner decision 2026-11-05 ("ignore the progress, reset them all to 0"):
// listening progress on a folder-book is a hold the owner may resolve. A plain
// apply still refuses the row; the owner's apply retires the folder-book with
// the progress left on it (kept, not carried), and the new books start at 0.
func TestFolderBooksOwner_ListeningProgressIsTheOwnersToResolve(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	fb := f.seedOwnerRow(t, ownerShelf{})
	uid := f.seedListener(t, fb)

	res := f.fbPlan(t, fbFixerID, "op-plan")
	require.Len(t, res.Rows, 1)
	row := res.Rows[0]
	var progress *bool
	for _, h := range row.Holds {
		if h.Kind == fbSkipProgress {
			ok := h.OwnerOK
			progress = &ok
			require.Contains(t, h.Why, fb)
			require.Contains(t, h.Why, "start at 0")
		}
	}
	require.NotNil(t, progress, "the progress hold is listed: %+v", row.Holds)
	require.True(t, *progress, "listening progress is owner-resolvable")
	require.True(t, row.OwnerApplicable, "%s", row.SkipReason)
	require.Zero(t, res.Applicable, "a plain apply never takes it")

	plain := f.fbApply(t, "op-plan", "op-plain", []string{row.RowID})
	require.Zero(t, plain.Applied)
	require.True(t, f.live(t, "fb"))

	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fb"), "the folder-book is retired")

	st, err := f.s.GetUserBookState(uid, fb)
	require.NoError(t, err)
	require.NotNil(t, st, "the progress is kept on the retired folder-book, not erased")
	require.InDelta(t, 42, st.ProgressPct, 0.001)
	for _, title := range []string{"Dark Victory", "Knightfall"} {
		b := f.liveAtDir(t, filepath.Join("Alfred Pennyworth", title))
		require.NotNil(t, b, title)
		nst, err := f.s.GetUserBookState(uid, b.ID)
		require.NoError(t, err)
		require.True(t, nst == nil || nst.ProgressPct == 0, "%s starts at 0: %+v", title, nst)
	}
}

// An iTunes id on the folder-book is still a hard hold, and it wins over
// listening progress on the same row.
func TestFolderBooksOwner_ITunesIDStaysHardWithProgress(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	fb := f.seedOwnerRow(t, ownerShelf{})
	f.seedListener(t, fb)
	_, err := f.s.ModifyBook(fb, func(b *database.Book) error {
		pid := "00000000000000AB"
		b.ITunesPersistentID = &pid
		return nil
	})
	require.NoError(t, err)
	res := f.fbPlan(t, fbFixerID, "op-plan")
	require.Len(t, res.Rows, 1)
	row := res.Rows[0]
	require.False(t, row.OwnerApplicable)
	require.NotEmpty(t, fbHardHoldWhy(row.Holds, fbSkipITunesID), "%+v", row.Holds)
}
