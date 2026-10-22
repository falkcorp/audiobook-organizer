// file: internal/plugins/maintenance/retire_carry_history_test.go
// version: 1.0.0
// guid: 3e8b1c74-9a52-4d06-b7f1-c20d5a96e183
// last-edited: 2026-10-21

package maintenance

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// End-to-end histories of retires and reverts into one survivor: real plan,
// real apply, real RevertOperation, and the lists compared in full after every
// step. They are the two traces an independent review found a list order wrong
// in (a re-retire after a revert, and a survivor borrowed by two operations),
// plus the partial-failure and concurrent-revert shapes. Every fixture is
// synthetic.

// historyStep is one step of a history: a retire of the listed children under
// operation op, or (kids nil) the revert of operation op.
type historyStep struct {
	op   string
	kids []int
}

// runHistory seeds nKids children and a collection and a playlist laid out as
// layout (layoutOf), runs the steps and returns both lists' final order and
// the order they started in. Every revert must fail nothing and report nothing
// restored by index: the order is exact because the recorded neighbours place
// the book, not because a position was guessed.
func runHistory(t *testing.T, layout []string, nKids int, steps []historyStep) (gotCol, gotPl, want []string) {
	t.Helper()
	f := hcSeed(t)
	parent := f.ids["parent"]
	kids := make([]string, nKids)
	for i := range kids {
		n := fmt.Sprint(i + 1)
		kids[i] = f.hcChild(t, "c"+n, "Stray "+n, "hash-p0"+n)
	}
	want = layoutOf(layout, kids, parent)
	col := f.rcCollection(t, "Shared", want...)
	pl := f.rcPlaylist(t, "Shared list", want...)
	plans := 0
	for _, s := range steps {
		if s.kids == nil {
			rr, err := audiobooks.NewRevertService(f.s).RevertOperation(s.op)
			require.NoError(t, err, "revert %s", s.op)
			require.Zero(t, rr.Failed, "revert %s: %+v", s.op, rr)
			require.Zero(t, rr.RestoredByIndex, "revert %s: %+v", s.op, rr)
			require.Empty(t, rr.Kept, "revert %s", s.op)
			continue
		}
		plans++
		plan := fmt.Sprintf("plan-%d", plans)
		f.plan(t, plan)
		rows := make([]string, len(s.kids))
		for j, k := range s.kids {
			rows[j] = hcRowID(parent, kids[k])
		}
		out := f.apply(t, plan, s.op, rows, nil)
		require.Equal(t, len(rows), out.Applied, "retire %s: %+v", s.op, out.Rows)
	}
	return f.rcMembers(t, col), f.rcPlaylistMembers(t, pl), want
}

// An operation retires a book into a survivor, the operation is reverted, and
// the same books are retired again under a new operation: the survivor's
// journal then holds two retires of the same pair, and the reverts that follow
// in the wrong order must still find every book's slot.
func TestRetireCarry_ReRetireAfterRevertEndToEnd(t *testing.T) {
	t.Parallel()
	gotCol, gotPl, want := runHistory(t, []string{"k0", "k1", "k2", "k3", "P", "k4"}, 5, []historyStep{
		{"op0", []int{2}}, {"op1", []int{3, 1}}, {"op2", []int{4, 0}}, {"op1", nil},
		{"op3", []int{3, 1}}, {"op2", nil}, {"op3", nil}, {"op0", nil},
	})
	require.Equal(t, want, gotCol)
	require.Equal(t, want, gotPl)
}

// The survivor stands in for a book of the first operation and stays listed
// for the second to borrow; the first operation is reverted while the second
// still depends on the survivor, and the survivor leaves with the last
// operation that borrowed it, whatever order the operations went in.
func TestRetireCarry_BorrowedSurvivorEndToEnd(t *testing.T) {
	t.Parallel()
	gotCol, gotPl, want := runHistory(t, []string{"k0", "k1", "k2", "k3"}, 4, []historyStep{
		{"op0", []int{0}}, {"op1", []int{1, 2}}, {"op0", nil},
		{"op2", []int{0, 3}}, {"op1", nil}, {"op2", nil},
	})
	require.Equal(t, want, gotCol)
	require.Equal(t, want, gotPl)
}

// flakyListStore fails the next UpdateCollectionBooks of the collection named
// in failOn, once per arm, and otherwise writes through.
type flakyListStore struct {
	database.Store
	members database.CollectionMembershipStore
	failOn  atomic.Value // string
}

func newFlakyListStore(s *database.PebbleStore) *flakyListStore {
	return &flakyListStore{Store: s, members: s}
}

func (s *flakyListStore) Unwrap() database.Store { return s.Store }

func (s *flakyListStore) AddBooksToCollection(id string, bookIDs []string) (*database.Collection, error) {
	return s.members.AddBooksToCollection(id, bookIDs)
}

func (s *flakyListStore) RemoveBookFromCollection(id, bookID string) (*database.Collection, error) {
	return s.members.RemoveBookFromCollection(id, bookID)
}

func (s *flakyListStore) UpdateCollectionBooks(id string, fn func([]string) ([]string, error)) (*database.Collection, error) {
	if v, _ := s.failOn.Load().(string); v == id {
		s.failOn.Store("")
		return nil, fmt.Errorf("injected write failure for collection %s", id)
	}
	return s.members.UpdateCollectionBooks(id, fn)
}

// A row that fails to restore leaves its survivor-side marker standing, so the
// operations reverted in between still see the survivor as borrowed, and the
// retry puts every list back exactly. Before the marker was tied to its row,
// the failed revert stamped it, the second operation took the survivor out, and
// the retry was refused with "no longer listed where the retire put it".
func TestRetireCarry_FailedRowLeavesItsMarkerStandingForTheRetry(t *testing.T) {
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

	flaky := newFlakyListStore(f.s)
	flaky.failOn.Store(col)
	rs := audiobooks.NewRevertService(flaky)
	rr, err := rs.RevertOperation("op0")
	require.Error(t, err)
	require.Equal(t, 1, rr.Failed, "%+v", rr)
	require.Equal(t, []string{parent, "x"}, f.rcMembers(t, col), "the failed row wrote nothing")

	markerStanding := func(op string) bool {
		changes, err := f.s.GetOperationChanges(op)
		require.NoError(t, err)
		n := 0
		for _, c := range changes {
			if k, _, ok := undo.UserContentFromField(c.FieldName); ok && undo.IsMarker(k) && c.RevertedAt == nil {
				n++
			}
		}
		return n == 1
	}
	require.True(t, markerStanding("op0"), "the marker of the failed row stands")

	// The second operation is reverted while the first is still owed: the
	// survivor stays, the book goes back beside it.
	rr, err = rs.RevertOperation("op1")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, []string{parent, k1, "x"}, f.rcMembers(t, col), "the survivor stays for op0, which still borrows it")

	rr, err = rs.RevertOperation("op0")
	require.NoError(t, err, "the retry is not refused: %+v", rr)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Zero(t, rr.RestoredByIndex, "%+v", rr)
	require.Equal(t, want, f.rcMembers(t, col))
	require.False(t, markerStanding("op0"))
	_, err = rs.RevertOperation("op0")
	require.ErrorContains(t, err, "already been reverted")
}

// Two operations that share a survivor are reverted at the same time: each
// reads the other's marker, and without the survivor's lock both could see the
// other standing and both leave the survivor listed. Whichever order they
// really run in, both lists end exact and the survivor out. Run under -race.
func TestRetireCarry_ConcurrentRevertsOfOperationsSharingASurvivor(t *testing.T) {
	t.Parallel()
	const rounds = 24
	for i := range rounds {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			f := hcSeed(t)
			parent := f.ids["parent"]
			k := []string{
				f.hcChild(t, "c1", "Stray 1", "hash-p01"),
				f.hcChild(t, "c2", "Stray 2", "hash-p02"),
				f.hcChild(t, "c3", "Stray 3", "hash-p03"),
			}
			want := []string{k[0], k[1], "x", k[2]}
			col := f.rcCollection(t, "Shared", want...)
			pl := f.rcPlaylist(t, "Shared list", want...)
			f.plan(t, "plan")
			ops := [][]int{{0}, {1}, {2}}
			for n, ks := range ops {
				out := f.apply(t, "plan", fmt.Sprintf("op%d", n), []string{hcRowID(parent, k[ks[0]])}, nil)
				require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			}
			var wg sync.WaitGroup
			errs := make([]error, len(ops))
			results := make([]*audiobooks.RevertResult, len(ops))
			for n := range ops {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[n], errs[n] = audiobooks.NewRevertService(f.s).RevertOperation(fmt.Sprintf("op%d", n))
				}()
			}
			wg.Wait()
			for n := range ops {
				require.NoError(t, errs[n], "op%d", n)
				require.Zero(t, results[n].Failed, "op%d: %+v", n, results[n])
			}
			require.Equal(t, want, f.rcMembers(t, col))
			require.Equal(t, want, f.rcPlaylistMembers(t, pl))
		})
	}
}
