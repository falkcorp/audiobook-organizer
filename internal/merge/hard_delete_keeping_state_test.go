// file: internal/merge/hard_delete_keeping_state_test.go
// version: 1.1.0
// guid: 91c4e7b2-5a38-4d60-b7f1-3e8a0d9c2b64
// last-edited: 2026-10-26

package merge

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// eventStore records, in order, the re-read of the carry target that
// CarryTargetPrecheck makes under the merge lock.
type eventStore struct {
	*database.PebbleStore
	target string
	mu     sync.Mutex
	events *[]string
}

func (s *eventStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.target {
		s.mu.Lock()
		*s.events = append(*s.events, "read-target")
		s.mu.Unlock()
	}
	return s.PebbleStore.GetBookByID(id)
}

type hdFixture struct {
	s      *database.PebbleStore
	events []string
	st     *eventStore
	user   *database.User
}

// newHDFixture: "keep" is the listed primary of g1, "doomed" a non-listed
// copy in g1; with withState, one reader has 40% on doomed.
func newHDFixture(t *testing.T, withState bool) *hdFixture {
	t.Helper()
	s, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	g := "g1"
	_, err = s.CreateBook(&database.Book{ID: "keep", Title: "Year One", Format: "m4b", VersionGroupID: &g,
		IsPrimaryVersion: new(true), LibraryState: new("organized")})
	require.NoError(t, err)
	_, err = s.CreateBook(&database.Book{ID: "doomed", Title: "Knightfall", Format: "m4b", VersionGroupID: &g,
		IsPrimaryVersion: new(false), LibraryState: new("imported")})
	require.NoError(t, err)
	u, err := s.CreateUser("listener", "listener@example.invalid", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	if withState {
		require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: "doomed",
			Status: database.UserBookStatusInProgress, ProgressPct: 40, LastActivityAt: time.Now()}))
	}
	f := &hdFixture{s: s, user: u}
	f.st = &eventStore{PebbleStore: s, target: "keep", events: &f.events}
	return f
}

func (f *hdFixture) doomed(t *testing.T) *database.Book {
	t.Helper()
	b, err := f.s.GetBookByID("doomed")
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}

func (f *hdFixture) pct(t *testing.T, bookID string) int {
	t.Helper()
	st, err := f.s.GetUserBookState(f.user.ID, bookID)
	require.NoError(t, err)
	if st == nil {
		return 0
	}
	return st.ProgressPct
}

var errPrecheck = errors.New("test precheck refuses")

// With state to carry: the precheck runs under the merge lock BEFORE the
// target is re-read and before the carry; its refusal is a
// *PrecheckRefusedError (ErrCarryPrecheckRefused and the precheck's own
// error both reachable, PrecheckCause giving it back bare), nothing moves and
// del does not run.
func TestHardDeleteKeepingUserState_CarryBranchPrecheckRefusal(t *testing.T) {
	f := newHDFixture(t, true)
	delRan := false

	carriedTo, err := HardDeleteKeepingUserState(f.s, f.st, f.doomed(t),
		func() error { f.events = append(f.events, "precheck"); return errPrecheck },
		func() error { delRan = true; return nil })

	require.Empty(t, carriedTo)
	require.ErrorIs(t, err, ErrCarryPrecheckRefused)
	require.ErrorIs(t, err, errPrecheck)
	require.NotErrorIs(t, err, ErrStateMovedBookKept)
	require.Same(t, errPrecheck, PrecheckCause(err), "the cause comes back unwrapped")
	var pe *PrecheckRefusedError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, "doomed", pe.DoomedID)
	require.Equal(t, "keep", pe.KeepID)
	require.False(t, delRan)
	require.Equal(t, []string{"precheck"}, f.events, "the target was never re-read: the caller's refusal comes first")
	require.Equal(t, 40, f.pct(t, "doomed"), "the source keeps its state")
	require.Zero(t, f.pct(t, "keep"), "nothing was carried")
}

// The order of a passing run is: precheck, then the target re-read, then del.
func TestHardDeleteKeepingUserState_CarryBranchOrder(t *testing.T) {
	f := newHDFixture(t, true)

	carriedTo, err := HardDeleteKeepingUserState(f.s, f.st, f.doomed(t),
		func() error { f.events = append(f.events, "precheck"); return nil },
		func() error { f.events = append(f.events, "del"); return f.s.DeleteBook("doomed") })

	require.NoError(t, err)
	require.Equal(t, "keep", carriedTo)
	require.Equal(t, []string{"precheck", "read-target", "del"}, f.events)
	require.Equal(t, 40, f.pct(t, "keep"), "the state moved to the listed copy")
}

// A precheck that passes does not hide the target's own refusal.
func TestHardDeleteKeepingUserState_TargetCheckStillRuns(t *testing.T) {
	f := newHDFixture(t, true)

	_, err := HardDeleteKeepingUserState(f.s, f.st, f.doomed(t),
		func() error {
			// ABS stops listing the target between the choice and the lock.
			_, merr := f.s.ModifyBook("keep", func(b *database.Book) error { b.IsPrimaryVersion = new(false); return nil })
			return merr
		},
		func() error { t.Fatal("del must not run"); return nil })

	require.ErrorIs(t, err, ErrCarryPrecheckRefused)
	require.ErrorIs(t, err, ErrBookHasListeningState)
	require.NotNil(t, PrecheckCause(err), "the target's refusal is the precheck cause CarryStateThenHardDelete wrapped")
	require.Equal(t, 40, f.pct(t, "doomed"))
}

// With no state the precheck runs under the merge lock just before del and a
// refusal is wrapped the same way as on the carry branch.
func TestHardDeleteKeepingUserState_NoStateBranchPrecheck(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		f := newHDFixture(t, false)
		delRan := false

		carriedTo, err := HardDeleteKeepingUserState(f.s, f.st, f.doomed(t),
			func() error { return errPrecheck },
			func() error { delRan = true; return nil })

		require.Empty(t, carriedTo)
		require.ErrorIs(t, err, ErrCarryPrecheckRefused, "wrapped like the carry branch")
		require.ErrorIs(t, err, errPrecheck)
		require.Same(t, errPrecheck, PrecheckCause(err))
		require.False(t, delRan, "del did not run")
	})
	t.Run("passes", func(t *testing.T) {
		f := newHDFixture(t, false)
		var order []string

		_, err := HardDeleteKeepingUserState(f.s, f.st, f.doomed(t),
			func() error { order = append(order, "precheck"); return nil },
			func() error { order = append(order, "del"); return nil })

		require.NoError(t, err)
		require.Equal(t, []string{"precheck", "del"}, order)
	})
	t.Run("del's own error is not a precheck refusal", func(t *testing.T) {
		f := newHDFixture(t, false)
		boom := errors.New("del failed")

		_, err := HardDeleteKeepingUserState(f.s, f.st, f.doomed(t),
			func() error { return nil }, func() error { return boom })

		require.ErrorIs(t, err, boom)
		require.NotErrorIs(t, err, ErrCarryPrecheckRefused)
		require.Nil(t, PrecheckCause(err))
	})
}

// A del that fails after the carry completed leaves the book kept and its
// state moved: ErrStateMovedBookKept, with carriedTo set, and the text and
// errors.Is of del's own error intact.
func TestHardDeleteKeepingUserState_LateDelFailureSaysTheStateMoved(t *testing.T) {
	f := newHDFixture(t, true)
	delErr := errors.New("delete refused late")

	carriedTo, err := HardDeleteKeepingUserState(f.s, f.st, f.doomed(t),
		func() error { return nil }, func() error { return delErr })

	require.Equal(t, "keep", carriedTo)
	require.ErrorIs(t, err, ErrStateMovedBookKept)
	require.ErrorIs(t, err, delErr)
	require.NotErrorIs(t, err, ErrCarryPrecheckRefused)
	require.Contains(t, err.Error(), "after its state moved to keep")
	require.Equal(t, 40, f.pct(t, "keep"))
}

// nil precheck: the old behavior (carry, then del).
func TestHardDeleteKeepingUserState_NilPrecheckUnchanged(t *testing.T) {
	f := newHDFixture(t, true)

	carriedTo, err := HardDeleteKeepingUserState(f.s, f.st, f.doomed(t), nil,
		func() error { return f.s.DeleteBook("doomed") })

	require.NoError(t, err)
	require.Equal(t, "keep", carriedTo)
	require.Equal(t, 40, f.pct(t, "keep"))
}
