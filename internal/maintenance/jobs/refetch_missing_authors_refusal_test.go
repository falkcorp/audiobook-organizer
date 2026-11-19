// file: internal/maintenance/jobs/refetch_missing_authors_refusal_test.go
// version: 1.0.0
// guid: b470331b-0526-4b25-a898-b69aa208f906
// last-edited: 2026-11-19

package jobs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// id3AlbumArtist is the smallest file taglib reads an ALBUMARTIST from: an
// ID3v2.3 tag holding one TPE2 frame, followed by one empty MPEG frame header
// so the file is not only a tag.
func id3AlbumArtist(t *testing.T, dir, artist string) string {
	t.Helper()
	text := append([]byte{0}, artist...) // encoding 0 = ISO-8859-1
	frame := append([]byte("TPE2"), byte(len(text)>>24), byte(len(text)>>16), byte(len(text)>>8), byte(len(text)), 0, 0)
	frame = append(frame, text...)
	n := len(frame)
	tag := append([]byte("ID3\x03\x00\x00"), byte(n>>21&0x7f), byte(n>>14&0x7f), byte(n>>7&0x7f), byte(n&0x7f))
	tag = append(tag, frame...)
	path := filepath.Join(dir, "Year One.mp3")
	require.NoError(t, os.WriteFile(path, append(tag, 0xFF, 0xFB, 0x90, 0x00), 0o600))
	return path
}

// A store that cannot commit a book's row and credits together is refused ONCE,
// before the first book's resolve could create an author row for a book that
// would then fail: no author is created and the run returns the sentence.
func TestRefetchMissingAuthorsJob_ApplyOnAStoreWithoutTheCreditsCommitIsRefusedBeforeAnyAuthorIsCreated(t *testing.T) {
	path := id3AlbumArtist(t, t.TempDir(), "Bruce Wayne")
	created := 0
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(int, int) ([]database.BookCore, error) {
			return []database.BookCore{{ID: "b1", Title: "Year One", FilePath: path}}, nil
		},
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return nil, nil },
		CreateAuthorFunc: func(name string) (*database.Author, error) {
			created++
			return &database.Author{ID: 1, Name: name}, nil
		},
	}
	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err)

	err = j.Run(context.Background(), store, &noopReporter{}, false)

	require.ErrorIs(t, err, database.ErrBookCreditsUnsupported)
	require.Zero(t, created, "an author row was created by a run that could not credit it")
}

// The refusal belongs to the apply: a preview writes nothing and needs no
// capability, so it still counts the book it would fill.
func TestRefetchMissingAuthorsJob_PreviewOnAStoreWithoutTheCreditsCommitStillRuns(t *testing.T) {
	path := id3AlbumArtist(t, t.TempDir(), "Bruce Wayne")
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(int, int) ([]database.BookCore, error) {
			return []database.BookCore{{ID: "b1", Title: "Year One", FilePath: path}}, nil
		},
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return nil, nil },
	}
	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err)

	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, true))
}
