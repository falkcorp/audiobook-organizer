// file: internal/audiobooks/revert_retire_scope_test.go
// version: 1.0.0
// guid: 5b0e7c2d-41a6-4f93-8d17-c3a92e6f0b58
// last-edited: 2026-10-24

package audiobooks

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// runWithTimeout runs fn and fails the test, with every goroutine's stack, if
// it has not returned in d: a lock-order bug under the merge lock is a hang,
// and a hang must fail a test rather than stall the suite.
func runWithTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
		t.Fatalf("still running after %v (a deadlock):\n%s", d, buf.String())
	}
}

// newChainRehomeFx is the chain case: a holder and a new holder, the shell's
// rows retired into the holder by op, then the version rehomed to the new
// holder, and the old holder trashed (it was being retired itself).
func newChainRehomeFx(t *testing.T, op string) (*retireRevertFx, *database.Book) {
	t.Helper()
	fx := newRetireRevertFx(t)
	w := fx.writer(op)
	fx.retire(t, w, "ver-chain")
	newHolder, err := fx.st.CreateBook(&database.Book{Title: "Synthetic New Holder", FilePath: "/lib/syn/new"})
	require.NoError(t, err)
	require.NoError(t, w.RehomeRetiredVersion("ver-chain", newHolder.ID))
	trashBook(t, fx.st, fx.holder)
	return fx, newHolder
}

// A chain rehome reverts onto the TRASHED holder it left: the forward rehome
// refuses a trashed destination, the revert must not, and the preflight must say
// the same as the revert.
func TestRevertRetire_RehomeBackOntoATrashedOldHolder(t *testing.T) {
	fx, newHolder := newChainRehomeFx(t, "op-chain-trashed")

	// The rehome row is restorable. (The retire rows of the same op are
	// predicted "changed since" by a whole-operation preflight, because the
	// version they name is on the new holder until the rehome is reversed: that
	// is the preflight's known blind spot for a chain, with a live old holder
	// too, and is not about the holder's trash state.)
	report, err := undo.PreflightUndoConflicts(fx.st, "op-chain-trashed")
	require.NoError(t, err)
	assert.Empty(t, report.BookMissing, "%+v", report)
	for _, item := range append(append([]undo.UndoConflictItem{}, report.CheckFailed...), report.BookMissing...) {
		assert.NotEqual(t, undo.ChangeTypeRetiredVersionRehome, item.ChangeType, "the rehome onto a trashed old holder is restorable: %+v", item)
	}

	// The rehome row on its own: the version goes back to the trashed holder.
	changes, err := fx.st.GetOperationChanges("op-chain-trashed")
	require.NoError(t, err)
	var rehome *database.OperationChange
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeRetiredVersionRehome {
			rehome = c
		}
	}
	require.NotNil(t, rehome)
	require.NoError(t, NewRevertService(fx.st).revertChangeIn(rehome, nil))
	v, err := fx.st.GetBookVersion("ver-chain")
	require.NoError(t, err)
	require.NotNil(t, v)
	assert.Equal(t, fx.holder.ID, v.BookID)
	left, err := fx.st.GetRetiredBookFiles(newHolder.ID)
	require.NoError(t, err)
	assert.Empty(t, left)

	// And the whole operation reverts, with the old holder still trashed.
	res, err := NewRevertService(fx.st).RevertOperation("op-chain-trashed")
	require.NoError(t, err, "%+v", res)
	for _, id := range fx.ids() {
		assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, id).BookID)
	}
	h, err := fx.st.GetBookByID(fx.holder.ID)
	require.NoError(t, err)
	require.NotNil(t, h)
	assert.True(t, h.IsSoftDeleted(), "a revert does not un-trash the holder")
}

// The old holder was purged since: the revert recreates it from its purge
// snapshot before the version goes back, as it does for a shell.
func TestRevertRetire_RehomeBackOntoAPurgedOldHolderRecreatesIt(t *testing.T) {
	fx, _ := newChainRehomeFx(t, "op-chain-purged")
	snap, err := fx.st.GetBookByID(fx.holder.ID)
	require.NoError(t, err)
	require.NoError(t, fx.st.DeleteBook(fx.holder.ID))

	// Nothing can recreate it: refused, named, and nothing written.
	report, err := undo.PreflightUndoConflicts(fx.st, "op-chain-purged")
	require.NoError(t, err)
	require.NotEmpty(t, report.BookMissing, "%+v", report)
	res, err := NewRevertService(fx.st).RevertOperation("op-chain-purged")
	require.Error(t, err)
	require.NotNil(t, res)
	v, gerr := fx.st.GetBookVersion("ver-chain")
	require.NoError(t, gerr)
	require.NotNil(t, v)
	assert.NotEqual(t, fx.holder.ID, v.BookID, "the version did not move")

	store := &recreatingStore{PebbleStore: fx.st, snapshots: map[string]database.Book{fx.holder.ID: *snap}}
	report, err = undo.PreflightUndoConflicts(store, "op-chain-purged")
	require.NoError(t, err)
	assert.Empty(t, report.BookMissing, "a purged old holder a snapshot can recreate is restorable: %+v", report)
	res, err = NewRevertService(store).RevertOperation("op-chain-purged")
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, []string{fx.holder.ID}, store.recreated, "the old holder, once, before the rows ran")
	for _, id := range fx.ids() {
		assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, id).BookID)
	}
}

// newShellHeldVersionFx: the shell held a retired version of its own (an earlier
// retire op), and a later retire op retired the shell into a holder, taking its
// own rows AND chaining that version along (a rehome whose old holder is the
// shell). The shell is trashed, as a combine leaves it.
func newShellHeldVersionFx(t *testing.T, laterOp string) (*retireRevertFx, *database.BookFile) {
	t.Helper()
	fx := newRetireRevertFx(t)
	scratch, err := fx.st.CreateBook(&database.Book{Title: "Synthetic Scratch", FilePath: "/lib/syn/scratch"})
	require.NoError(t, err)
	older := &database.BookFile{BookID: scratch.ID, FilePath: "/lib/syn/scratch/01.mp3", FileHash: "hash-older", TrackNumber: 1}
	require.NoError(t, fx.st.CreateBookFile(older))
	trashBook(t, fx.st, scratch)
	untrashBook(t, fx.st, fx.shell)
	require.NoError(t, fx.writer("op-earlier").RetireVersion(repairs.RetireSpec{
		Shell: scratch.ID, Holder: fx.shell.ID, VersionID: "ver-older", FileIDs: []string{older.ID},
		Format: "mp3", Source: "retired_book", Now: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
	}))
	trashBook(t, fx.st, fx.shell)

	w := fx.writer(laterOp)
	fx.retire(t, w, "ver-later")
	require.NoError(t, w.RehomeRetiredVersion("ver-older", fx.holder.ID))
	return fx, older
}

func requireShellHoldsItsVersionAgain(t *testing.T, fx *retireRevertFx, older *database.BookFile) {
	t.Helper()
	v, err := fx.st.GetBookVersion("ver-older")
	require.NoError(t, err)
	require.NotNil(t, v)
	assert.Equal(t, fx.shell.ID, v.BookID, "the version the shell held is back on it")
	row, err := fx.st.GetBookFileByID(fx.shell.ID, older.ID)
	require.NoError(t, err)
	require.NotNil(t, row, "with its row")
	assert.True(t, row.Retired)
	for _, id := range fx.ids() {
		assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, id).BookID)
	}
	later, err := fx.st.GetBookVersion("ver-later")
	require.NoError(t, err)
	assert.Nil(t, later)
}

// A scoped revert for the shell restores the version the shell held before the
// retire chained it away, not only the shell's own rows.
func TestRevertOperationForBooks_ShellGetsBackTheVersionItHeld(t *testing.T) {
	fx, older := newShellHeldVersionFx(t, "op-later")
	res, err := NewRevertService(fx.st).RevertOperationForBooks("op-later", []string{fx.shell.ID})
	require.NoError(t, err, "%+v", res)
	requireShellHoldsItsVersionAgain(t, fx, older)
}

// The combine undo's reversal of the same retire judges and restores it too.
func TestRetireUndoer_ShellGetsBackTheVersionItHeld(t *testing.T) {
	fx, older := newShellHeldVersionFx(t, "op-later-plan")
	plan, err := NewRevertService(fx.st).retireUndoer().PlanRetireRevert("op-later-plan", fx.shell.ID)
	require.NoError(t, err)
	require.True(t, plan.Live)
	require.Empty(t, plan.Reasons)
	require.NoError(t, plan.Revert())
	requireShellHoldsItsVersionAgain(t, fx, older)
}

// A scoped revert names SHELLS only: the holder keys every retire row, and
// selecting by it would pick up all of them.
func TestRevertOperationForBooks_RefusesABookTheOperationNeverNamesAsAShell(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-holder"), "ver-holder")
	rs := NewRevertService(fx.st)

	res, err := rs.RevertOperationForBooks("op-holder", []string{fx.holder.ID})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), fx.holder.ID)
	assert.Contains(t, err.Error(), "shell")
	for _, id := range fx.ids() {
		f, gerr := fx.st.GetBookFileByID(fx.holder.ID, id)
		require.NoError(t, gerr)
		require.NotNil(t, f)
		assert.True(t, f.Retired, "nothing was reverted")
	}
	_, err = rs.RevertOperationForBooks("op-holder", []string{fx.shell.ID, "no-such-book"})
	require.Error(t, err, "one bad id refuses the whole scope")
	f, gerr := fx.st.GetBookFileByID(fx.holder.ID, fx.rows[0].ID)
	require.NoError(t, gerr)
	require.NotNil(t, f)
}

// A retire row that does not parse is refused by name by the combine undo's
// plan, never skipped: skipping it would restore the shell around a file that
// stays retired.
func TestRetireUndoer_UndecodableRetireRowRefusesByName(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-bad-row"), "ver-bad-row")
	require.NoError(t, fx.st.CreateOperationChange(&database.OperationChange{
		ID: "chg-bad", OperationID: "op-bad-row", BookID: fx.holder.ID, ChangeType: undo.ChangeTypeBookFileRetire,
		FieldName: "book_file:" + fx.rows[0].ID, OldValue: "{", NewValue: "{",
	}))
	plan, err := NewRevertService(fx.st).retireUndoer().PlanRetireRevert("op-bad-row", fx.shell.ID)
	require.NoError(t, err)
	require.True(t, plan.Live)
	require.NotEmpty(t, plan.Reasons)
	assert.Contains(t, plan.Reasons[0], "chg-bad")
	assert.Contains(t, plan.Reasons[0], "does not parse")
	require.Error(t, plan.Revert())
}

func TestRevertScope_RecordIDIsPerScopeAndNeverTheOperations(t *testing.T) {
	a := newRevertScope([]string{"b", "a"}).recordID("op")
	assert.Equal(t, "op#scope:a,b", a, "sorted: the same books are the same record whatever order they were named in")
	assert.NotEqual(t, "op", a)
	assert.NotEqual(t, a, newRevertScope([]string{"a"}).recordID("op"))
}

// A scoped run keeps an owed-settle record of its own: it does not read,
// rewrite or clear the intent a dead earlier run of the whole operation left.
func TestRevertOperationForBooks_ScopedRunLeavesTheWholeOperationsOwedRecord(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-record"), "ver-record")
	rs := NewRevertService(fx.st)
	rs.markSettlePending("op-record", "vg-syn-whole")
	require.True(t, rs.settlePendingGroups("op-record")["vg-syn-whole"], "the whole-operation intent is recorded")

	res, err := rs.RevertOperationForBooks("op-record", []string{fx.shell.ID})
	require.NoError(t, err, "%+v", res)
	assert.True(t, rs.settlePendingGroups("op-record")["vg-syn-whole"],
		"a scoped run must not clear the group intent the whole operation's run left")
	assert.Empty(t, rs.settlePendingGroups(newRevertScope([]string{fx.shell.ID}).recordID("op-record")),
		"and writes its own record only")
}

// The operation lock is keyed by the exact id. Striped by hash, two different
// operations could share a lock, and a combine undo (holding its own op's lock)
// reversing a retire op's rows would self-deadlock or never get the try-lock.
func TestOperationLocks_AreKeyedByTheExactOperationID(t *testing.T) {
	release := lockOperation("op-lock-0")
	for i := 1; i <= 300; i++ {
		other := fmt.Sprintf("op-lock-%d", i)
		rel, ok := tryLockOperation(other)
		require.True(t, ok, "a different operation's lock is never held by %q", "op-lock-0")
		rel()
	}
	_, ok := tryLockOperation("op-lock-0")
	require.False(t, ok, "the same operation is busy")
	release()
	rel, ok := tryLockOperation("op-lock-0")
	require.True(t, ok, "free once released")
	rel()
	opLocks.mu.Lock()
	defer opLocks.mu.Unlock()
	assert.Empty(t, opLocks.m, "no entry outlives its last holder")
}

// The combine undo's reversal try-locks the retire op: a busy lock is a
// retryable error and writes nothing, a free one reverses. It runs with the
// merge lock held and must never wait on it.
func TestRetireUndoer_BusyRetireOperationIsARetryableRefusal(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-busy"), "ver-busy")
	rs := NewRevertService(fx.st)
	plan, err := rs.retireUndoer().PlanRetireRevert("op-busy", fx.shell.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Reasons)

	release := lockOperation("op-busy")
	runWithTimeout(t, 30*time.Second, func() {
		err = plan.Revert()
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errRetireOpBusy), "%v", err)
	f, gerr := fx.st.GetBookFileByID(fx.holder.ID, fx.rows[0].ID)
	require.NoError(t, gerr)
	require.NotNil(t, f)
	assert.True(t, f.Retired, "nothing was written while the other revert ran")
	release()

	unlockMerge := rs.lockMerge() // the combine undo holds it for the whole call
	runWithTimeout(t, 30*time.Second, func() {
		err = plan.Revert()
	})
	unlockMerge()
	require.NoError(t, err, "free now: reverses, without taking the merge lock again")
	for _, id := range fx.ids() {
		assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, id).BookID)
	}
}

// countingStore counts GetOperationChanges per operation id.
type countingStore struct {
	*database.PebbleStore
	mu    sync.Mutex
	reads map[string]int
}

func (s *countingStore) GetOperationChanges(id string) ([]*database.OperationChange, error) {
	s.mu.Lock()
	s.reads[id]++
	s.mu.Unlock()
	return s.PebbleStore.GetOperationChanges(id)
}

func (s *countingStore) readsOf(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[id]
}

// The whole thing, with the real undoer inside a real UndoCombine: a combine of
// a shell that held a retired version of its own, then a later retire op that
// retired the shell into a holder (link written, version chained away, shell
// itself still in the trash), then a revert of the combine. The merge lock is
// held by UndoCombine for the whole call and the retire reversal runs under it;
// a lock-order bug is a hang, so it runs against a timeout.
func TestRevertCombine_EndToEndReversesALinkedRetireUnderTheMergeLock(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCrashFixture(t, store)
	mkWriter := func(op string) *repairs.Writer {
		return repairs.NewWriter(store, store, "fx", "bulk_update", "repairs-").WithJournal(store, store, op)
	}

	// The shell, absorbed later, already holds a retired version of its own.
	scratch, err := store.CreateBook(&database.Book{Title: "Synthetic Scratch", FilePath: "/tmp/crash/scratch"})
	require.NoError(t, err)
	older := &database.BookFile{BookID: scratch.ID, FilePath: "/tmp/crash/scratch/01.mp3", FileHash: "hash-e2e-older", TrackNumber: 1}
	require.NoError(t, store.CreateBookFile(older))
	trashBook(t, store, scratch)
	require.NoError(t, mkWriter("op-e2e-earlier").RetireVersion(repairs.RetireSpec{
		Shell: scratch.ID, Holder: f.a, VersionID: "ver-e2e-older", FileIDs: []string{older.ID},
		Format: "mp3", Source: "retired_book", Now: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
	}))

	const combineOp = "op-e2e-combine"
	res, err := merge.NewService(store).CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep, nil, merge.CombineOptions{OperationID: combineOp})
	require.NoError(t, err)
	require.NotEmpty(t, res.JournalID)
	shell, err := store.GetBookByID(f.a)
	require.NoError(t, err)
	require.True(t, shell.IsSoftDeleted(), "the combine trashed the shell")

	// Later: the shell is retired into a holder. The link is written first, the
	// version the shell held is chained to the holder (old holder: the TRASHED
	// shell), and the shell's file rows were already moved off by the combine.
	holder, err := store.CreateBook(&database.Book{Title: "Synthetic E2E Holder", FilePath: "/tmp/crash/holder"})
	require.NoError(t, err)
	const retireOp = "op-e2e-retire"
	w := mkWriter(retireOp)
	require.NoError(t, w.RetireVersion(repairs.RetireSpec{
		Shell: f.a, Holder: holder.ID, VersionID: "ver-e2e-new", CombineJournalID: res.JournalID,
	}))
	require.NoError(t, w.RehomeRetiredVersion("ver-e2e-older", holder.ID))
	links, err := merge.ListCombineRetireLinks(store, res.JournalID)
	require.NoError(t, err)
	require.Equal(t, []merge.CombineRetireLink{{ShellID: f.a, OpID: retireOp}}, links)

	counting := &countingStore{PebbleStore: store, reads: map[string]int{}}
	var result *RevertResult
	runWithTimeout(t, 90*time.Second, func() {
		result, err = NewRevertService(counting).RevertOperation(combineOp)
	})
	require.NoError(t, err, "%+v", result)

	shell, err = store.GetBookByID(f.a)
	require.NoError(t, err)
	require.NotNil(t, shell)
	assert.False(t, shell.IsSoftDeleted(), "the shell is restored")
	v, err := store.GetBookVersion("ver-e2e-older")
	require.NoError(t, err)
	require.NotNil(t, v)
	assert.Equal(t, f.a, v.BookID, "the version the shell held went back to it before the shell was restored")
	row, err := store.GetBookFileByID(f.a, older.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.True(t, row.Retired)
	onHolder, err := store.GetRetiredBookFiles(holder.ID)
	require.NoError(t, err)
	assert.Empty(t, onHolder)
	j, err := merge.NewService(store).GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, merge.CombineJournalUndone, j.Status)

	changes, err := store.GetOperationChanges(retireOp)
	require.NoError(t, err)
	for _, c := range changes {
		assert.NotNil(t, c.RevertedAt, "%s of the retire op is marked reverted", c.ChangeType)
	}
	assert.Equal(t, 1, counting.readsOf(retireOp), "the retire op is read once per link across the check and the reversal, not once per use")
}

// An organized holder with live files in the library keeps "organized" when a
// revert un-trashes it, whatever retired versions it holds outside the root: the
// state is decided on its live rows, as every other restore path decides it.
func TestRevertBookSoftDelete_RetiredVersionsDoNotDemoteAnOrganizedHolder(t *testing.T) {
	root := t.TempDir()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.WaitForWarmup()
	organized := "organized"
	holder, err := st.CreateBook(&database.Book{Title: "Synthetic Organized Holder", FilePath: filepath.Join(root, "holder"), LibraryState: &organized})
	require.NoError(t, err)
	require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: holder.ID, FilePath: filepath.Join(root, "holder", "01.mp3"), FileHash: "hash-live", TrackNumber: 1}))
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: "/old/newbooks/shell"})
	require.NoError(t, err)
	outside := &database.BookFile{BookID: shell.ID, FilePath: "/old/newbooks/shell/01.mp3", FileHash: "hash-outside", TrackNumber: 1}
	require.NoError(t, st.CreateBookFile(outside))
	trashBook(t, st, shell)
	require.NoError(t, repairs.NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-s8").RetireVersion(repairs.RetireSpec{
		Shell: shell.ID, Holder: holder.ID, VersionID: "ver-s8", FileIDs: []string{outside.ID}, Format: "mp3", Source: "retired_book",
	}))
	trashBook(t, st, holder)

	revertSoftDelete := func(id string) {
		t.Helper()
		require.NoError(t, NewRevertService(st).revertBookSoftDelete(&database.OperationChange{
			ID: "chg-" + id, OperationID: "op-s8-restore", BookID: id, ChangeType: undo.ChangeTypeBookSoftDelete, FieldName: "marked_for_deletion",
		}, undo.SoftDeleteStamps{}))
	}
	revertSoftDelete(holder.ID)
	got, err := st.GetBookByID(holder.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LibraryState)
	assert.Equal(t, "organized", *got.LibraryState, "the retired version outside the root says nothing about the holder's own files")
}

// A holder whose ONLY rows are retired versions falls back to them: judged on no
// rows at all it would be demoted for having none.
func TestRevertBookSoftDelete_HolderWithOnlyRetiredVersionsIsJudgedOnThem(t *testing.T) {
	root := t.TempDir()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.WaitForWarmup()
	organized := "organized"
	holder, err := st.CreateBook(&database.Book{Title: "Synthetic Only-Retired Holder", FilePath: filepath.Join(root, "holder"), LibraryState: &organized})
	require.NoError(t, err)
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: filepath.Join(root, "shell")})
	require.NoError(t, err)
	inside := &database.BookFile{BookID: shell.ID, FilePath: filepath.Join(root, "shell", "01.mp3"), FileHash: "hash-inside", TrackNumber: 1}
	require.NoError(t, st.CreateBookFile(inside))
	trashBook(t, st, shell)
	require.NoError(t, repairs.NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-s8b").RetireVersion(repairs.RetireSpec{
		Shell: shell.ID, Holder: holder.ID, VersionID: "ver-s8b", FileIDs: []string{inside.ID}, Format: "mp3", Source: "retired_book",
	}))
	trashBook(t, st, holder)

	require.NoError(t, NewRevertService(st).revertBookSoftDelete(&database.OperationChange{
		ID: "chg-only", OperationID: "op-s8b-restore", BookID: holder.ID, ChangeType: undo.ChangeTypeBookSoftDelete, FieldName: "marked_for_deletion",
	}, undo.SoftDeleteStamps{}))
	got, err := st.GetBookByID(holder.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LibraryState)
	assert.Equal(t, "organized", *got.LibraryState)
}
