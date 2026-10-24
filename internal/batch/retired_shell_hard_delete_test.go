// file: internal/batch/retired_shell_hard_delete_test.go
// version: 1.0.0
// guid: b14cac72-dfef-4461-8a91-97acbdcfe369
// last-edited: 2026-10-24

package batch

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newShellStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	for _, id := range []string{"holder", "shell"} {
		_, err := s.CreateBook(&database.Book{ID: id, Title: id, FilePath: "/lib/" + id})
		require.NoError(t, err)
	}
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "holder-f", BookID: "holder", FilePath: "/lib/holder/a.mp3"}))
	return s
}

func retireShell(t *testing.T, s *database.PebbleStore) {
	t.Helper()
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "shell-f", BookID: "shell", FilePath: "/lib/shell/a.mp3"}))
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	_, err := s.CreateBookVersion(&database.BookVersion{ID: "rv-shell", BookID: "holder", Status: database.BookVersionStatusRetired,
		Source: "retired_book", RetiredFromBookID: "shell", RetireOpID: "op-test", RetiredAt: &at})
	require.NoError(t, err)
	require.NoError(t, s.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{"shell-f"}, SourceBookID: "shell"}}, "holder", "rv-shell"))
}

// A batch hard delete refuses a retire shell, which owns no live row, and
// leaves the book and the holder's retired row alone.
func TestHardDelete_RefusesARetiredShell(t *testing.T) {
	s := newShellStore(t)
	retireShell(t, s)
	err := NewBatchService(s).hardDeleteLocked("shell", nil)
	require.ErrorIs(t, err, database.ErrBookIsRetiredShell)
	gone, _ := s.GetBookByID("shell")
	require.NotNil(t, gone, "the shell must not be deleted")
	held, err := s.GetRetiredBookFiles("holder")
	require.NoError(t, err)
	require.Len(t, held, 1)
}

// lateRetireStore retires the shell after the first caller-level check of it
// has passed, as the retired-versions repair does from another goroutine.
type lateRetireStore struct {
	*database.PebbleStore
	calls atomic.Int32
	once  sync.Once
}

func (l *lateRetireStore) GetRetiredVersionsFromBook(id string) ([]database.BookVersion, error) {
	vs, err := l.PebbleStore.GetRetiredVersionsFromBook(id)
	if id == "shell" && l.calls.Add(1) == 1 {
		l.once.Do(func() {
			// Not t.Fatal: this may run off the test goroutine.
			if e := l.retire(); e != nil {
				l.calls.Store(-1000)
			}
		})
	}
	return vs, err
}

func (l *lateRetireStore) retire() error {
	if err := l.PebbleStore.CreateBookFile(&database.BookFile{ID: "shell-f", BookID: "shell", FilePath: "/lib/shell/a.mp3"}); err != nil {
		return err
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := l.PebbleStore.CreateBookVersion(&database.BookVersion{ID: "rv-shell", BookID: "holder", Status: database.BookVersionStatusRetired,
		Source: "retired_book", RetiredFromBookID: "shell", RetireOpID: "op-late", RetiredAt: &at}); err != nil {
		return err
	}
	return l.PebbleStore.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{"shell-f"}, SourceBookID: "shell"}}, "holder", "rv-shell")
}

// A shell retired after the first check is refused by the check made under the
// merge lock, in the same words as the first ("hard delete <id>: ..."),
// not by DeleteBook's backstop, whose wording names no action the user took.
// Removing the under-lock check changes that wording, which is what fails.
func TestHardDelete_ShellRetiredAfterTheFirstCheckIsRefusedUnderTheLock(t *testing.T) {
	s := newShellStore(t)
	w := &lateRetireStore{PebbleStore: s}

	err := NewBatchService(w).hardDeleteLocked("shell", nil)
	require.GreaterOrEqual(t, w.calls.Load(), int32(2), "the under-lock check never ran")
	require.ErrorIs(t, err, database.ErrBookIsRetiredShell)
	require.Contains(t, err.Error(), "hard delete shell: ", "refused by the in-lock check, not DeleteBook's backstop: %v", err)
	gone, _ := s.GetBookByID("shell")
	require.NotNil(t, gone)
	held, err := s.GetRetiredBookFiles("holder")
	require.NoError(t, err)
	require.Len(t, held, 1)
}
