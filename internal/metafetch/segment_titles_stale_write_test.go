// file: internal/metafetch/segment_titles_stale_write_test.go
// version: 1.0.0
// guid: 2b9d6f14-c3a7-4e58-9f01-d8e6a4b7c250
// last-edited: 2026-09-14

package metafetch

import (
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// recordSegmentPatch is a MockStore.PatchBookFileFieldsFunc that records each
// patch generateSegmentTitles sends as the book_file row it would produce
// (ID, BookID and the patched track/count/title only).
func recordSegmentPatch(out *[]database.BookFile) func(string, string, database.BookFileFieldPatch) (*database.BookFile, *database.BookFile, error) {
	return func(bookID, fileID string, p database.BookFileFieldPatch) (*database.BookFile, *database.BookFile, error) {
		row := database.BookFile{ID: fileID, BookID: bookID}
		if p.TrackNumber != nil {
			row.TrackNumber = *p.TrackNumber
		}
		if p.TrackCount != nil {
			row.TrackCount = *p.TrackCount
		}
		if p.Title != nil {
			row.Title = *p.Title
		}
		*out = append(*out, row)
		return &row, &row, nil
	}
}

// segHookStore runs hook once, right after generateSegmentTitles has read the
// book's files and before it writes any of them.
type segHookStore struct {
	*database.PebbleStore
	once sync.Once
	hook func()
}

func (s *segHookStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	files, err := s.PebbleStore.GetBookFiles(bookID)
	if err == nil {
		s.once.Do(s.hook)
	}
	return files, err
}

// A book_file column another writer commits after the titles pass read the
// rows (enrich-book-files' Duration here) must survive the titles write. The
// old code wrote each whole stale row back with UpdateBookFile.
func TestGenerateSegmentTitles_KeepsFileFieldsWrittenAfterTheRead(t *testing.T) {
	inner, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	book, err := inner.CreateBook(&database.Book{Title: "Book", FilePath: "/library/book", Format: "mp3"})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, inner.CreateBookFile(&database.BookFile{
			BookID: book.ID, FilePath: fmt.Sprintf("/library/book/%02d.mp3", i+1), Format: "mp3",
		}))
	}

	wantDur := map[string]int{}
	hs := &segHookStore{PebbleStore: inner}
	hs.hook = func() {
		files, err := inner.GetBookFiles(book.ID)
		require.NoError(t, err)
		for i := range files {
			f := files[i]
			f.Duration = 3600 + i
			require.NoError(t, inner.UpdateBookFile(f.ID, &f))
		}
		after, err := inner.GetBookFiles(book.ID)
		require.NoError(t, err)
		for _, f := range after {
			require.NotZero(t, f.Duration)
			wantDur[f.ID] = f.Duration
		}
	}

	svc := NewService(hs)
	require.NoError(t, svc.generateSegmentTitles(book.ID, "Book"))

	got, err := inner.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	tracks := map[int]bool{}
	for _, f := range got {
		require.Equal(t, wantDur[f.ID], f.Duration, "file %s: concurrent Duration write reverted", f.ID)
		require.NotEmpty(t, f.Title, "file %s: title not written", f.ID)
		require.Equal(t, 2, f.TrackCount)
		tracks[f.TrackNumber] = true
	}
	require.Equal(t, map[int]bool{1: true, 2: true}, tracks)
}
