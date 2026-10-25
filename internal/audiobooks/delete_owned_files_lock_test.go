// file: internal/audiobooks/delete_owned_files_lock_test.go
// version: 1.0.0
// guid: c41e7a92-6d05-4b38-8e1f-2a9d5b073c64
// last-edited: 2026-10-25

package audiobooks

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// gainsFileAfterFirstCheckStore adds a book_file row to bookID right after the
// FIRST GetBookFiles of it returns: a file that lands after DeleteAudiobook's
// up-front check and before the merge lock the carry runs under.
type gainsFileAfterFirstCheckStore struct {
	*database.PebbleStore
	t      *testing.T
	bookID string

	mu    sync.Mutex
	calls int
}

func (s *gainsFileAfterFirstCheckStore) GetBookFiles(id string) ([]database.BookFile, error) {
	rows, err := s.PebbleStore.GetBookFiles(id)
	if id == s.bookID {
		s.mu.Lock()
		s.calls++
		first := s.calls == 1
		s.mu.Unlock()
		if first {
			require.NoError(s.t, s.PebbleStore.CreateBookFile(&database.BookFile{
				BookID: id, FilePath: "/x/" + id + "-gained.m4b", Format: "m4b",
			}))
		}
	}
	return rows, err
}

// A file row that lands after the up-front check refuses the hard delete in
// the precheck under the merge lock, BEFORE the carry moves any state and
// before the hash is blocked. Without the re-check the progress moved to the
// listed copy and only then did DeleteBook refuse: the book stayed, its
// progress did not.
func TestDeleteAudiobook_HardDeleteFileGainedBeforeTheLockMovesNothing(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "copy", "g1", false, false)
	hash := "h-gains-file"
	_, err := store.ModifyBook("copy", func(b *database.Book) error { b.FileHash = &hash; return nil })
	require.NoError(t, err)
	u := purgeSeedProgress(t, store, "copy")
	wrapped := &gainsFileAfterFirstCheckStore{PebbleStore: store, t: t, bookID: "copy"}
	svc := NewAudiobookService(wrapped)

	res, err := svc.DeleteAudiobook(context.Background(), "copy", &DeleteAudiobookOptions{BlockHash: true})

	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Nil(t, res)
	require.True(t, strings.HasPrefix(err.Error(), "hard delete copy: "), "same wording as the up-front check, not the carry's wrapper: %v", err)
	require.Contains(t, err.Error(), "soft-delete it instead, or move its files to another book first")
	require.GreaterOrEqual(t, wrapped.calls, 2, "the files were checked again under the merge lock")
	require.False(t, bookGone(t, store, "copy"))
	require.Equal(t, 40, userPct(t, store, u.ID, "copy"), "the source keeps its progress")
	require.Zero(t, userPct(t, store, u.ID, "keep"), "nothing was carried to the listed copy")
	blocked, err := store.IsHashBlocked(hash)
	require.NoError(t, err)
	require.False(t, blocked, "a refused delete blocks no hash")
}
