// file: internal/plugins/maintenance/retire_carry_shared_list_test.go
// version: 1.1.0
// guid: 8d2f6b19-4c07-4e53-a1d8-7b95e03c6f42
// last-edited: 2026-10-22

package maintenance

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// End-to-end shapes the list lock, the atomic list-and-stamp write and the
// multi-survivor order graph exist for: real plan, real apply, real
// RevertOperation over a real in-memory PebbleStore, with a store wrapper
// injecting the one failure or pause each test needs. Every fixture is
// synthetic.

// hookStore wraps the fixture's store and injects, per test:
//
//   - nonAtomic: UpdateCollectionBooksAndMark is the list write followed by a
//     separate MarkOperationChangesReverted, the window a build that wrote the
//     two apart left behind when the process died between them (the revert no
//     longer writes that way; a list in that state is what the retry repairs);
//   - failMarkOp: the next marker stamp of that operation fails, once. On the
//     atomic path it fails the whole call before anything is written, on the
//     playlist's atomic path too;
//   - armRetire: from then on the first GetCollection (retireEntered) and the
//     first UpdateCollectionBooks (retireWrote) are signalled; the reverts under
//     test write through the atomic call, so only a retire's carry reaches them;
//   - pauseAfterRead: the first GetBookChanges of pauseBook returns its rows
//     only after pauseAfterRead.resume is closed, having signalled
//     pauseAfterRead.read once it has read them, so the reader decides from a
//     journal that is stale by the time it acts;
//   - barrierBook: the first two GetBookChanges of that book wait until both
//     have arrived (or 600 ms pass), then read, so two concurrent readers both
//     see the journal as it was before either wrote.
type hookStore struct {
	database.Store
	members database.CollectionMembershipStore
	atomic  database.CollectionMembershipMarkStore

	nonAtomic  bool
	failMarkOp atomic.Value // string

	armRetire     atomic.Bool
	retireEntered chan struct{}
	retireWrote   chan struct{}
	enteredOnce   sync.Once
	wroteOnce     sync.Once

	pauseBook      string
	pauseAfterRead *readPause
	pauseOnce      sync.Once

	barrierBook string
	arrived     atomic.Int32
	released    chan struct{}
	releaseOnce sync.Once
}

type readPause struct {
	read   chan struct{}
	resume chan struct{}
}

func newHookStore(s *database.PebbleStore) *hookStore {
	return &hookStore{Store: s, members: s, atomic: s, released: make(chan struct{}),
		retireEntered: make(chan struct{}), retireWrote: make(chan struct{})}
}

func (s *hookStore) GetCollection(id string) (*database.Collection, error) {
	if s.armRetire.Load() {
		s.enteredOnce.Do(func() { close(s.retireEntered) })
	}
	return s.Store.GetCollection(id)
}

func (s *hookStore) Unwrap() database.Store { return s.Store }

func (s *hookStore) AddBooksToCollection(id string, bookIDs []string) (*database.Collection, error) {
	return s.members.AddBooksToCollection(id, bookIDs)
}

func (s *hookStore) RemoveBookFromCollection(id, bookID string) (*database.Collection, error) {
	return s.members.RemoveBookFromCollection(id, bookID)
}

func (s *hookStore) UpdateCollectionBooks(id string, fn func([]string) ([]string, error)) (*database.Collection, error) {
	if s.armRetire.Load() {
		s.wroteOnce.Do(func() { close(s.retireWrote) })
	}
	return s.members.UpdateCollectionBooks(id, fn)
}

func (s *hookStore) UpdateUserPlaylistAndMark(pl *database.UserPlaylist, mark database.OperationMark) error {
	if v, _ := s.failMarkOp.Load().(string); v == mark.OperationID {
		s.failMarkOp.Store("")
		return errors.New("injected atomic playlist-and-stamp failure")
	}
	return s.Store.(database.UserPlaylistMarkStore).UpdateUserPlaylistAndMark(pl, mark)
}

func (s *hookStore) UpdateCollectionBooksAndMark(id string, fn func([]string) ([]string, error), mark database.OperationMark) (*database.Collection, error) {
	if !s.nonAtomic {
		if v, _ := s.failMarkOp.Load().(string); v == mark.OperationID {
			s.failMarkOp.Store("")
			return nil, errors.New("injected atomic list-and-stamp failure")
		}
		return s.atomic.UpdateCollectionBooksAndMark(id, fn, mark)
	}
	col, err := s.members.UpdateCollectionBooks(id, fn)
	if err != nil {
		return col, err
	}
	return col, s.MarkOperationChangesReverted(mark.OperationID, mark.ChangeIDs)
}

func (s *hookStore) MarkOperationChangesReverted(op string, ids []string) error {
	if v, _ := s.failMarkOp.Load().(string); v == op {
		s.failMarkOp.Store("")
		return errors.New("injected marker stamp failure")
	}
	return s.Store.MarkOperationChangesReverted(op, ids)
}

func (s *hookStore) GetBookChanges(bookID string) ([]*database.OperationChange, error) {
	if s.barrierBook != "" && bookID == s.barrierBook && s.arrived.Add(1) <= 2 {
		if s.arrived.Load() >= 2 {
			s.releaseOnce.Do(func() { close(s.released) })
		}
		select {
		case <-s.released:
		case <-time.After(600 * time.Millisecond):
		}
	}
	rows, err := s.Store.GetBookChanges(bookID)
	if s.pauseAfterRead != nil && bookID == s.pauseBook {
		s.pauseOnce.Do(func() {
			close(s.pauseAfterRead.read)
			<-s.pauseAfterRead.resume
		})
	}
	return rows, err
}

// useStore makes the fixture's plugin read and write through st.
func (f *fragFixture) useStore(t *testing.T, st database.Store) {
	t.Helper()
	sd, ok := f.p.deps.(scanDeps)
	require.True(t, ok, "fixture deps are %T, not scanDeps", f.p.deps)
	sd.fakeDeps.store = st
	f.p.deps = sd
}

// markersStanding counts the survivor-side markers of op not yet stamped.
func (f *fragFixture) markersStanding(t *testing.T, op string) int {
	t.Helper()
	changes, err := f.s.GetOperationChanges(op)
	require.NoError(t, err)
	n := 0
	for _, c := range changes {
		if k, _, ok := undo.UserContentFromField(c.FieldName); ok && undo.IsMarker(k) && c.RevertedAt == nil {
			n++
		}
	}
	return n
}

// Two survivors share one collection and their adjacent strays are retired
// into different survivors by different operations. Reverting the older
// operation first used to put a book on the wrong side of the other survivor
// with no note: the order of the books around a survivor depends on the
// journals of both survivors, and the order graph reads them.
func TestRetireCarry_TwoSurvivorsSharingAListRevertExactly(t *testing.T) {
	t.Parallel()
	for _, order := range [][]string{{"op1", "op2"}, {"op2", "op1"}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			t.Parallel()
			f := hcSeed(t)
			p1 := f.ids["parent"]
			p2 := f.book(t, "parent2", "Beta Omnibus", f.path("lib/Beta Omnibus"), nil)
			for i := 1; i <= 3; i++ {
				role := fmt.Sprintf("q%02d", i)
				p := f.file(t, fmt.Sprintf("lib/Beta Omnibus/track-%02d.mp3", i), 3000)
				f.row(t, role, p2, p, fmt.Sprintf("track-%02d.mp3", i), 3000, 600, i)
				f.hcSetHash(t, "parent2", role, "hash-"+role)
			}
			x := f.hcChild(t, "c1", "Stray 1", "hash-p01")
			y := f.hcChild(t, "d1", "Stray D", "hash-q01")
			want := []string{p1, x, y, p2}
			col := f.rcCollection(t, "Shared", want...)
			pl := f.rcPlaylist(t, "Shared list", want...)

			f.plan(t, "plan-1")
			out := f.apply(t, "plan-1", "op1", []string{hcRowID(p1, x)}, nil)
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			f.plan(t, "plan-2")
			out = f.apply(t, "plan-2", "op2", []string{hcRowID(p2, y)}, nil)
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)

			for _, op := range order {
				rr, err := audiobooks.NewRevertService(f.s).RevertOperation(op)
				require.NoError(t, err)
				require.Zero(t, rr.Failed, "%s: %+v", op, rr)
				require.Zero(t, rr.RestoredByIndex, "%s: %+v", op, rr)
				require.Empty(t, rr.Kept, "%s", op)
			}
			require.Equal(t, want, f.rcMembers(t, col))
			require.Equal(t, want, f.rcPlaylistMembers(t, pl))
		})
	}
}

// A list written by an older build whose stamp was lost (it wrote the list and
// the stamp apart and died between them; nonAtomic stands for that build). The
// second operation is reverted, then the first is retried by the current
// build: it finds its book already listed, sees the survivor was borrowed by it
// and by nothing standing now, and takes the survivor out, naming the repair.
// Without it the survivor stayed in the list for good.
func TestRetireCarry_ListWrittenWithoutItsStampIsRepairedByTheRetry(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	parent := f.ids["parent"]
	k0 := f.hcChild(t, "c1", "Stray 1", "hash-p01")
	k1 := f.hcChild(t, "c2", "Stray 2", "hash-p02")
	want := []string{k0, k1, "x"}
	col := f.rcCollection(t, "Shared", want...)
	f.plan(t, "plan")
	for i, kid := range []string{k0, k1} {
		out := f.apply(t, "plan", fmt.Sprintf("op%d", i), []string{hcRowID(parent, kid)}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	}
	require.Equal(t, []string{parent, "x"}, f.rcMembers(t, col))

	st := newHookStore(f.s)
	st.nonAtomic = true
	st.failMarkOp.Store("op0")
	rr, err := audiobooks.NewRevertService(st).RevertOperation("op0")
	require.Error(t, err)
	require.Equal(t, 1, rr.Failed, "%+v", rr)
	require.Equal(t, []string{k0, parent, "x"}, f.rcMembers(t, col), "the list write landed, the survivor stays for op1")
	require.Equal(t, 1, f.markersStanding(t, "op0"), "the stamp was lost")

	st.nonAtomic = false // the current build from here on
	rs := audiobooks.NewRevertService(st)
	rr, err = rs.RevertOperation("op1")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, []string{k0, parent, k1, "x"}, f.rcMembers(t, col), "op0's marker stands, so the survivor stays")

	rr, err = rs.RevertOperation("op0")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, 1, rr.AlreadyRestored, "%+v", rr)
	require.Equal(t, 1, rr.SurvivorsReleased, "%+v", rr)
	require.Len(t, rr.Kept, 1, "the survivor's release is named: %+v", rr)
	require.Contains(t, rr.Kept[0], "taken out of the list")
	require.Contains(t, rr.Summary(), "1 survivor(s) taken out of a list they were left in")
	require.Equal(t, want, f.rcMembers(t, col), "the survivor leaves with the last borrower")
	require.Zero(t, f.markersStanding(t, "op0"))
}

// With the atomic batch a failing stamp fails the list write too: nothing is
// written, the marker stands, and the retry does the whole row.
func TestRetireCarry_AtomicListAndStampFailureWritesNothing(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	parent := f.ids["parent"]
	k0 := f.hcChild(t, "c1", "Stray 1", "hash-p01")
	k1 := f.hcChild(t, "c2", "Stray 2", "hash-p02")
	want := []string{k0, k1, "x"}
	col := f.rcCollection(t, "Shared", want...)
	f.plan(t, "plan")
	for i, kid := range []string{k0, k1} {
		out := f.apply(t, "plan", fmt.Sprintf("op%d", i), []string{hcRowID(parent, kid)}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	}

	st := newHookStore(f.s)
	st.failMarkOp.Store("op0")
	rs := audiobooks.NewRevertService(st)
	rr, err := rs.RevertOperation("op0")
	require.Error(t, err)
	require.Equal(t, 1, rr.Failed, "%+v", rr)
	require.Equal(t, []string{parent, "x"}, f.rcMembers(t, col), "list and stamp are one write: nothing landed")
	require.Equal(t, 1, f.markersStanding(t, "op0"))

	_, err = rs.RevertOperation("op1")
	require.NoError(t, err)
	rr, err = rs.RevertOperation("op0")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.AlreadyRestored, "%+v", rr)
	require.Empty(t, rr.Kept)
	require.Equal(t, want, f.rcMembers(t, col))
	require.Zero(t, f.markersStanding(t, "op0"))
	require.Zero(t, f.markersStanding(t, "op1"))
}

// The same for a playlist: the playlist and the stamp are one write.
func TestRetireCarry_AtomicPlaylistAndStampFailureWritesNothing(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	parent := f.ids["parent"]
	k0 := f.hcChild(t, "c1", "Stray 1", "hash-p01")
	k1 := f.hcChild(t, "c2", "Stray 2", "hash-p02")
	want := []string{k0, k1, "x"}
	pl := f.rcPlaylist(t, "Shared list", want...)
	f.plan(t, "plan")
	for i, kid := range []string{k0, k1} {
		out := f.apply(t, "plan", fmt.Sprintf("op%d", i), []string{hcRowID(parent, kid)}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	}

	st := newHookStore(f.s)
	st.failMarkOp.Store("op0")
	rs := audiobooks.NewRevertService(st)
	rr, err := rs.RevertOperation("op0")
	require.Error(t, err)
	require.Equal(t, 1, rr.Failed, "%+v", rr)
	require.Equal(t, []string{parent, "x"}, f.rcPlaylistMembers(t, pl), "playlist and stamp are one write: nothing landed")
	require.Equal(t, 1, f.markersStanding(t, "op0"))

	_, err = rs.RevertOperation("op1")
	require.NoError(t, err)
	rr, err = rs.RevertOperation("op0")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.AlreadyRestored, "%+v", rr)
	require.Empty(t, rr.Kept)
	require.Equal(t, want, f.rcPlaylistMembers(t, pl))
	require.Zero(t, f.markersStanding(t, "op0"))
	require.Zero(t, f.markersStanding(t, "op1"))
}

// Two operations sharing a survivor are reverted at the same moment, and the
// store holds both readers of the survivor's journal until both have read, so
// without the list lock each reads the journal as it was before either wrote
// and sees the other's marker standing: both keep the survivor and it stays in
// the list for good. With the lock the second reads after the first wrote.
// Deterministic: removing the lock fails this every run.
func TestRetireCarry_ConcurrentRevertsReadTheJournalAfterTheOtherWrote(t *testing.T) {
	// Not parallel: a retire anywhere in the package holds the merge lock
	// the reverts also take, which would delay a reader past the barrier.
	f := hcSeed(t)
	parent := f.ids["parent"]
	k0 := f.hcChild(t, "c1", "Stray 1", "hash-p01")
	k1 := f.hcChild(t, "c2", "Stray 2", "hash-p02")
	want := []string{k0, k1, "x"}
	col := f.rcCollection(t, "Shared", want...)
	pl := f.rcPlaylist(t, "Shared list", want...)
	f.plan(t, "plan")
	for i, kid := range []string{k0, k1} {
		out := f.apply(t, "plan", fmt.Sprintf("op%d", i), []string{hcRowID(parent, kid)}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	}

	st := newHookStore(f.s)
	st.barrierBook = parent
	var wg sync.WaitGroup
	errs := make([]error, 2)
	results := make([]*audiobooks.RevertResult, 2)
	for n := range 2 {
		wg.Go(func() {
			results[n], errs[n] = audiobooks.NewRevertService(st).RevertOperation(fmt.Sprintf("op%d", n))
		})
	}
	wg.Wait()
	for n := range 2 {
		require.NoError(t, errs[n], "op%d", n)
		require.Zero(t, results[n].Failed, "op%d: %+v", n, results[n])
	}
	require.Equal(t, want, f.rcMembers(t, col), "the survivor leaves with the last revert")
	require.Equal(t, want, f.rcPlaylistMembers(t, pl))
}

// A revert of one operation has read the survivor's journal and is about to
// write the list when a retire of another operation into the same survivor
// runs. Without the list lock the retire completes first and the revert then
// writes from a journal that no longer describes the list: it swaps the survivor
// out for the first book although the retire's marker says the survivor now
// stands in for the second, so both leave the list. With the lock the retire
// waits for the revert and carries into the list it left.
func TestRetireCarry_ARetireWaitsForARevertIntoTheSameSurvivor(t *testing.T) {
	// Not parallel: a retire anywhere in the package holds the merge lock
	// the reverts also take, which would delay a reader past the barrier.
	f := hcSeed(t)
	parent := f.ids["parent"]
	k0 := f.hcChild(t, "c1", "Stray 1", "hash-p01")
	k1 := f.hcChild(t, "c2", "Stray 2", "hash-p02")
	want := []string{k0, k1, "x"}
	col := f.rcCollection(t, "Shared", want...)
	f.plan(t, "plan")
	out := f.apply(t, "plan", "op0", []string{hcRowID(parent, k0)}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.Equal(t, []string{parent, k1, "x"}, f.rcMembers(t, col))

	st := newHookStore(f.s)
	st.pauseBook = parent
	st.pauseAfterRead = &readPause{read: make(chan struct{}), resume: make(chan struct{})}
	f.useStore(t, st)

	var wg sync.WaitGroup
	var rr *audiobooks.RevertResult
	var rerr error
	wg.Go(func() {
		rr, rerr = audiobooks.NewRevertService(st).RevertOperation("op0")
	})
	select {
	case <-st.pauseAfterRead.read:
	case <-time.After(10 * time.Second):
		close(st.pauseAfterRead.resume)
		t.Fatal("the revert never read the survivor's journal")
	}
	var applied *repairs.ApplyResult
	st.armRetire.Store(true)
	wg.Go(func() {
		applied = f.apply(t, "plan", "op1", []string{hcRowID(parent, k1)}, nil)
	})
	// The retire is known to be in its carry once it reads the collection, a
	// moment before it takes the list lock. Without the lock it goes on to
	// write the list at once; with it, it waits for the revert, which is held
	// until the retire has shown which.
	select {
	case <-st.retireEntered:
	case <-time.After(10 * time.Second):
		close(st.pauseAfterRead.resume)
		t.Fatal("the retire never reached its carry")
	}
	select {
	case <-st.retireWrote:
	case <-time.After(250 * time.Millisecond):
	}
	close(st.pauseAfterRead.resume)
	wg.Wait()

	require.NoError(t, rerr, "%+v", rr)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, 1, applied.Applied, "%+v", applied.Rows)
	require.Equal(t, []string{k0, parent, "x"}, f.rcMembers(t, col), "op1 stands, so the survivor is listed where it stands for the second book")

	_, err := audiobooks.NewRevertService(st).RevertOperation("op1")
	require.NoError(t, err)
	require.Equal(t, want, f.rcMembers(t, col), "the survivor leaves with the last operation")
}
