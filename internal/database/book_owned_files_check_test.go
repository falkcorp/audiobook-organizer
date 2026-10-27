// file: internal/database/book_owned_files_check_test.go
// version: 1.2.1
// guid: 0d52b8f6-93a1-4e7c-a4b0-1f6e8c3d7952
// last-edited: 2026-10-27

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// CheckBookOwnsNoFiles must refuse exactly the books DeleteBook refuses on
// book_file rows. The case that told the two apart: a book holding only
// retired rows (GetBookFiles returns none of them, DeleteBook counts them).
func TestCheckBookOwnsNoFiles_AgreesWithDeleteBook(t *testing.T) {
	type tc struct {
		name        string
		setup       func(t *testing.T, fx *retiredFixture) string // returns the book id to check
		wantRefused bool
		// wantHolds: the refusal also wraps ErrBookHoldsRetiredVersions, which
		// is so only when every row is retired (a live row is the more basic
		// reason and wins).
		wantHolds bool
		wantInMsg []string
	}
	cases := []tc{
		{"no rows", func(t *testing.T, fx *retiredFixture) string {
			b, err := fx.ps.CreateBook(&Book{Title: "The Killing Joke", FilePath: "/lib/Gotham/ret/plain"})
			require.NoError(t, err)
			return b.ID
		}, false, false, nil},
		{"live rows only", func(t *testing.T, fx *retiredFixture) string {
			fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/ret/live/01.mp3"})
			return fx.shell.ID
		}, true, false, []string{"soft-delete it instead, or move its files to another book first"}},
		{"retired rows only", func(t *testing.T, fx *retiredFixture) string {
			x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/ret/ro/01.mp3"})
			require.NoError(t, fx.retire(t, x))
			return fx.holder.ID
		}, true, true, []string{"retired", "cannot be moved"}},
		{"live and retired rows", func(t *testing.T, fx *retiredFixture) string {
			x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/ret/mix/01.mp3"})
			require.NoError(t, fx.retire(t, x))
			fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/Gotham/ret/mix/live.mp3"})
			return fx.holder.ID
		}, true, false, []string{"1 live and 1 retired", "cannot be moved"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newRetiredFixture(t)
			id := c.setup(t, fx)

			check := CheckBookOwnsNoFiles(fx.ps, id, "hard delete")
			delErr := fx.ps.DeleteBook(id)

			if c.wantRefused {
				require.ErrorIs(t, check, ErrBookOwnsFiles)
				require.ErrorIs(t, delErr, ErrBookOwnsFiles, "the check must predict DeleteBook's refusal")
				require.Equal(t, c.wantHolds, errors.Is(check, ErrBookHoldsRetiredVersions),
					"ErrBookHoldsRetiredVersions is set exactly when every row is retired: %v", check)
				require.Contains(t, check.Error(), "hard delete "+id+": ")
				for _, s := range c.wantInMsg {
					require.Contains(t, check.Error(), s)
				}
			} else {
				require.NoError(t, check)
				require.NoError(t, delErr)
			}
		})
	}

	t.Run("the live-only reader misses what DeleteBook counts", func(t *testing.T) {
		fx := newRetiredFixture(t)
		x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/ret/gap/01.mp3"})
		require.NoError(t, fx.retire(t, x))
		live, err := fx.ps.GetBookFiles(fx.holder.ID)
		require.NoError(t, err)
		require.Empty(t, live, "GetBookFiles skips retired rows, which is why it cannot stand in for DeleteBook's count")
		require.ErrorIs(t, CheckBookOwnsNoFiles(fx.ps, fx.holder.ID, "hard delete"), ErrBookOwnsFiles)
	})
}

type noRetiredReader struct{}

type failingRetiredReader struct{ RetiredBookFileReader }

func (failingRetiredReader) GetBookFilesIncludingRetired(string) ([]BookFile, error) {
	return nil, errors.New("pebble closed")
}

// A store that cannot answer, or an answer that fails, is not proof either
// way: the check fails closed with an error that is NOT ErrBookOwnsFiles.
func TestCheckBookOwnsNoFiles_FailsClosed(t *testing.T) {
	err := CheckBookOwnsNoFiles(noRetiredReader{}, "b1", "hard delete")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "cannot be checked")

	err = CheckBookOwnsNoFiles(failingRetiredReader{}, "b1", "hard delete")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "pebble closed")

	require.Error(t, CheckBookOwnsNoFiles(nil, "b1", "hard delete"))
}
