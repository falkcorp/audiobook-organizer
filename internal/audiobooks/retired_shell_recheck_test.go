// file: internal/audiobooks/retired_shell_recheck_test.go
// version: 1.3.0
// guid: 451c02fe-0c9c-4bd1-9bcd-2b1f5484b71e
// last-edited: 2026-10-24

package audiobooks

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// retireMeanwhileStore retires the shell DURING the operation under test, the
// way the retired-versions repair does from another goroutine: right after the
// after-th lookup of the shell's retired versions (each caller-level shell
// check makes exactly one when the shell has none), a late file row lands on
// the shell and is retired onto the holder. Every check up to that lookup
// answered "not a shell"; only a check made AFTER it can refuse, so a test
// built on it fails when the later check is removed. (The first check in each
// path passes because a shell must own a live row to be retired, and the
// pre-checks of every path refuse a book that owns one: this is the one
// interleaving a retire can land in.)
type retireMeanwhileStore struct {
	*database.PebbleStore
	shell, holder string
	after         int32
	calls         atomic.Int32
	once          sync.Once
	mu            sync.Mutex
	err           error
}

func (s *retireMeanwhileStore) GetRetiredVersionsFromBook(shellID string) ([]database.BookVersion, error) {
	vs, err := s.PebbleStore.GetRetiredVersionsFromBook(shellID)
	if shellID == s.shell && s.calls.Add(1) == s.after {
		s.once.Do(s.retire)
	}
	return vs, err
}

func (s *retireMeanwhileStore) retire() {
	fail := func(err error) {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}
	if err := s.PebbleStore.CreateBookFile(&database.BookFile{ID: s.shell + "-late", BookID: s.shell, FilePath: "/lib/" + s.shell + "/late.mp3"}); err != nil {
		fail(err)
		return
	}
	rows, err := s.PebbleStore.GetBookFiles(s.shell)
	if err != nil || len(rows) != 1 {
		fail(errors.New("the late row was not written"))
		return
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := s.PebbleStore.CreateBookVersion(&database.BookVersion{ID: "rv-" + s.shell, BookID: s.holder,
		Status: database.BookVersionStatusRetired, Source: "retired_book", RetiredFromBookID: s.shell, RetireOpID: "op-late", RetiredAt: &at}); err != nil {
		fail(err)
		return
	}
	if err := s.PebbleStore.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rows[0].ID}, SourceBookID: s.shell}}, s.holder, "rv-"+s.shell); err != nil {
		fail(err)
	}
}

// fired requires that the retire ran (the test is about what the operation
// does after it) and that it succeeded.
func (s *retireMeanwhileStore) fired(t *testing.T) {
	t.Helper()
	require.EqualValues(t, s.after, min(s.calls.Load(), s.after), "the operation never made check number %d", s.after)
	require.GreaterOrEqual(t, s.calls.Load(), s.after, "the operation made fewer than %d shell checks, so the retire never landed", s.after)
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NoError(t, s.err)
	held, err := s.PebbleStore.GetRetiredBookFiles(s.holder)
	require.NoError(t, err)
	require.Len(t, held, 1, "the shell's late row must be retired by the end")
}

func liveHolder(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	_, err := store.CreateBook(&database.Book{ID: id, Title: id, FilePath: "/lib/" + id})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: id + "-f", BookID: id, FilePath: "/lib/" + id + "/a.mp3"}))
}

// The stateless purge re-checks the shell under the merge lock, before it
// writes the tombstone: a shell retired after the nightly purge's own check is
// kept (skipped_retired_shell) with no tombstone, not purged and not failed.
func TestPurge_ShellRetiredAfterTheFirstCheckIsRefusedUnderTheLock(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	_ = svc
	liveHolder(t, store, "holder")
	softDeleted(t, store, "shell", "/lib/shell")
	w := &retireMeanwhileStore{PebbleStore: store, shell: "shell", holder: "holder", after: 1}
	days := 0
	res, err := NewAudiobookService(w).PurgeSoftDeletedBooks(context.Background(), false, &days)
	require.NoError(t, err)
	w.fired(t)
	require.Equal(t, 1, res.SkippedRetiredShell, "%+v", res)
	require.Zero(t, res.Purged, "%+v", res)
	require.Empty(t, res.Errors, "a shell kept for its retired rows is not an error: %+v", res)
	require.NotNil(t, mustBook(t, store, "shell"))
	tomb, _ := store.GetBookTombstone("shell")
	require.Nil(t, tomb, "the tombstone is written only for a book that will be deleted")
}

// A purge that carries listening state to a live sibling re-checks the shell
// in its own hold of the lock, before anything moves: a shell retired after
// the stateless check keeps its users' progress where it is.
func TestPurge_ShellRetiredBeforeTheCarryKeepsItsProgress(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "shell", "g1", true, false)
	u := purgeSeedProgress(t, store, "shell")
	w := &retireMeanwhileStore{PebbleStore: store, shell: "shell", holder: "keep", after: 2}
	days := 0
	res, err := NewAudiobookService(w).PurgeSoftDeletedBooks(context.Background(), false, &days)
	require.NoError(t, err)
	w.fired(t)
	require.Equal(t, 1, res.SkippedRetiredShell, "%+v", res)
	require.Zero(t, res.Purged+res.CarriedToSibling, "%+v", res)
	require.Empty(t, res.Errors, "%+v", res)
	require.Equal(t, 40, userPct(t, store, u.ID, "shell"), "the progress stays on the shell")
	require.Zero(t, userPct(t, store, u.ID, "keep"), "nothing was carried")
	require.NotNil(t, mustBook(t, store, "shell"))
}

// "Discard progress and purge" re-checks the shell under the lock BEFORE it
// clears any state: a shell retired after the first check keeps its progress.
func TestDiscardProgressAndPurge_ShellRetiredAfterTheFirstCheckKeepsItsProgress(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	liveHolder(t, store, "holder")
	softDeleted(t, store, "shell", "/lib/shell")
	u := purgeSeedProgress(t, store, "shell")
	w := &retireMeanwhileStore{PebbleStore: store, shell: "shell", holder: "holder", after: 1}
	svc := NewAudiobookService(w)
	svc.auditOverride = &fakeRecorder{}
	_, err := svc.DiscardProgressAndPurge(context.Background(), "shell", "owner")
	w.fired(t)
	require.ErrorIs(t, err, merge.ErrBookIsRetiredShell)
	require.Equal(t, 40, userPct(t, store, u.ID, "shell"), "no state was discarded before the refusal")
	require.NotNil(t, mustBook(t, store, "shell"))
}

// The live hard delete re-checks the shell under the merge lock BEFORE it
// blocks the hash: a shell retired after the first check is refused with the
// hash left unblocked.
func TestDeleteAudiobook_LiveShellRetiredAfterTheFirstCheckIsNotHashBlocked(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	liveHolder(t, store, "holder")
	hash := "late-retire-hash"
	_, err := store.CreateBook(&database.Book{ID: "shell", Title: "shell", FilePath: "/lib/shell", FileHash: &hash})
	require.NoError(t, err)
	w := &retireMeanwhileStore{PebbleStore: store, shell: "shell", holder: "holder", after: 1}
	_, err = NewAudiobookService(w).DeleteAudiobook(context.Background(), "shell", &DeleteAudiobookOptions{BlockHash: true})
	w.fired(t)
	require.ErrorIs(t, err, merge.ErrBookIsRetiredShell)
	blocked, berr := store.IsHashBlocked(hash)
	require.NoError(t, berr)
	require.False(t, blocked, "the hash must not be blocked for a delete that was refused")
	require.NotNil(t, mustBook(t, store, "shell"))
}

// The live-book branch of the hard delete refuses a retire shell that is not
// in the trash (only reachable by un-trashing one, which the restore paths now
// refuse), before it blocks the hash.
func TestDeleteAudiobook_LiveRetiredShellIsRefusedBeforeTheHashBlock(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	liveHolder(t, store, "holder")
	hash := "live-shell-hash"
	_, err := store.CreateBook(&database.Book{ID: "shell", Title: "shell", FilePath: "/lib/shell", FileHash: &hash})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "shell-f", BookID: "shell", FilePath: "/lib/shell/a.mp3"}))
	retireShellInto(t, store, "shell", "holder")

	_, err = NewAudiobookService(store).DeleteAudiobook(context.Background(), "shell", &DeleteAudiobookOptions{BlockHash: true})
	require.ErrorIs(t, err, merge.ErrBookIsRetiredShell)
	blocked, berr := store.IsHashBlocked(hash)
	require.NoError(t, berr)
	require.False(t, blocked)
	require.NotNil(t, mustBook(t, store, "shell"))
	held, err := store.GetRetiredBookFiles("holder")
	require.NoError(t, err)
	require.Len(t, held, 1)
}

// RestoreAudiobook says "not found" (ErrAudiobookNotFound) only for a missing
// book. A failed retired-rows lookup (the shell check fails closed) is any
// other error, so the handler can answer 500 instead of 404, and the book stays
// in the trash.
func TestRestoreAudiobook_MissingIsNotFoundAndALookupFailureIsNot(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "doomed", "/lib/doomed")

	_, err := NewAudiobookService(store).RestoreAudiobook(context.Background(), "no-such-book")
	require.ErrorIs(t, err, ErrAudiobookNotFound)

	_, err = NewAudiobookService(failingRetiredReads{store}).RestoreAudiobook(context.Background(), "doomed")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAudiobookNotFound, "a failed lookup is not 'not found'")
	require.NotErrorIs(t, err, merge.ErrBookIsRetiredShell)
	require.True(t, mustBook(t, store, "doomed").IsSoftDeleted(), "the book stays in the trash")
}

// A trashed book that holds only RETIRED rows (a holder trashed after a shell
// was retired onto it) owns files as far as DeleteBook is concerned. The purge
// keeps it as owning files, reported as such, instead of writing a tombstone
// and failing the delete as a purge error on every run.
func TestPurge_TrashedHolderOfOnlyRetiredRowsIsKeptAsOwningFiles(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	_, err := store.CreateBook(&database.Book{ID: "holder", Title: "holder", FilePath: "/lib/holder"})
	require.NoError(t, err)
	softDeleted(t, store, "shell", "/lib/shell")
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "shell-f", BookID: "shell", FilePath: "/lib/shell/a.mp3"}))
	retireShellInto(t, store, "shell", "holder")
	// The holder is trashed afterwards (the store refuses a trashed holder).
	yes, at := true, time.Now().Add(-time.Hour)
	_, err = store.ModifyBook("holder", func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &at
		return nil
	})
	require.NoError(t, err)
	live, err := store.GetBookFiles("holder")
	require.NoError(t, err)
	require.Empty(t, live, "fixture: the holder owns no LIVE row")

	days := 0
	res, err := NewAudiobookService(store).PurgeSoftDeletedBooks(context.Background(), false, &days)
	require.NoError(t, err)
	require.Equal(t, 1, res.SkippedOwnsFiles, "%+v", res)
	require.Contains(t, res.SkippedOwnsFilesIDs, "holder")
	require.Empty(t, res.Errors, "kept as owning files, not a purge failure: %+v", res)
	require.NotNil(t, mustBook(t, store, "holder"))
	tomb, _ := store.GetBookTombstone("holder")
	require.Nil(t, tomb, "no tombstone for a book that is not deleted")
}

// holderOfOnlyRetiredRows builds a book that owns no live row but has a
// retired version's rows keyed under it (a shell was retired onto it), and
// seeds a listening position on it. DeleteBook refuses such a book
// (ErrBookOwnsFiles), so every operation that writes anything before the
// delete must refuse it first.
func holderOfOnlyRetiredRows(t *testing.T, store *database.PebbleStore, hash *string) (userID string) {
	t.Helper()
	_, err := store.CreateBook(&database.Book{ID: "holder", Title: "holder", FilePath: "/lib/holder", FileHash: hash})
	require.NoError(t, err)
	softDeleted(t, store, "shell", "/lib/shell")
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "shell-f", BookID: "shell", FilePath: "/lib/shell/a.mp3"}))
	retireShellInto(t, store, "shell", "holder")
	live, err := store.GetBookFiles("holder")
	require.NoError(t, err)
	require.Empty(t, live, "fixture: the holder owns no LIVE row")
	return purgeSeedProgress(t, store, "holder").ID
}

// "Discard progress and purge" on a trashed holder of only retired rows is
// refused BEFORE any user's listening state is discarded: the delete would
// refuse afterwards (ErrBookOwnsFiles) and the progress would already be gone.
func TestDiscardProgressAndPurge_HolderOfOnlyRetiredRowsKeepsItsProgress(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	uid := holderOfOnlyRetiredRows(t, store, nil)
	yes, at := true, time.Now().Add(-time.Hour)
	_, err := store.ModifyBook("holder", func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &at
		return nil
	})
	require.NoError(t, err)
	svc := NewAudiobookService(store)
	svc.auditOverride = &fakeRecorder{}
	_, err = svc.DiscardProgressAndPurge(context.Background(), "holder", "owner")
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Equal(t, 40, userPct(t, store, uid, "holder"), "no state was discarded before the refusal")
	require.NotNil(t, mustBook(t, store, "holder"))
}

// A live hard delete of a holder of only retired rows is refused before the
// hash block and before the user state is carried to another version.
func TestDeleteAudiobook_LiveHolderOfOnlyRetiredRowsIsRefusedBeforeAnyWrite(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	hash := "holder-hash"
	uid := holderOfOnlyRetiredRows(t, store, &hash)
	_, err := NewAudiobookService(store).DeleteAudiobook(context.Background(), "holder", &DeleteAudiobookOptions{BlockHash: true})
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	blocked, berr := store.IsHashBlocked(hash)
	require.NoError(t, berr)
	require.False(t, blocked, "the hash must not be blocked for a delete that was refused")
	require.Equal(t, 40, userPct(t, store, uid, "holder"))
	require.NotNil(t, mustBook(t, store, "holder"))
}
