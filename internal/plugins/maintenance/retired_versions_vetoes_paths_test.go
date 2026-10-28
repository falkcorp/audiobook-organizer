// file: internal/plugins/maintenance/retired_versions_vetoes_paths_test.go
// version: 1.0.3
// guid: 7e2c4d91-a508-4b36-9f1d-c63a08e5b274
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
)

// rowCacheDeps is fakeDeps whose owner-veto readers share one RowCache, as
// the Server's do.
type rowCacheDeps struct {
	fakeDeps
	rows *decisions.RowCache
}

func (d rowCacheDeps) OwnerVetoes() OwnerVetoReader { return NewOwnerVetoReader(d.store, d.rows) }

// sharedRows points the fixture's fixer at readers that share one RowCache.
func (rf *retiredFixture) sharedRows() {
	rf.withDeps(rowCacheDeps{fakeDeps: fakeDeps{store: rf.st}, rows: decisions.NewRowCache()})
}

// loadCountingVetoes counts the loads of the readers it wraps.
type loadCountingVetoes struct {
	OwnerVetoReader
	loads *atomic.Int32
}

func (c loadCountingVetoes) Load(ctx context.Context) error {
	c.loads.Add(1)
	return c.OwnerVetoReader.Load(ctx)
}

// loadCountingDeps is fakeDeps whose owner-veto readers count their loads.
type loadCountingDeps struct {
	fakeDeps
	loads *atomic.Int32
}

func (d loadCountingDeps) OwnerVetoes() OwnerVetoReader {
	return loadCountingVetoes{OwnerVetoReader: NewOwnerVetoReader(d.store, nil), loads: d.loads}
}

// A re-plan of a book that left the trash answers "gone" before it reads the
// owner's rejects, so it pays no load; a book still in the trash loads them
// exactly once.
func TestRetiredVersions_ReplanLoadsTheRejectsOnlyAfterTheTrashCheck(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("shell", "g", 20, rf.file("g/shell.mp3"))
	rf.trashed("left", "g", 20, rf.file("g/left.mp3"))
	planned := rf.plan()
	loads := &atomic.Int32{}
	rf.withDeps(loadCountingDeps{fakeDeps: fakeDeps{store: rf.st}, loads: loads})

	b, err := rf.st.GetBookByID(rf.ids["left"])
	require.NoError(t, err)
	b.MarkedForDeletion, b.MarkedForDeletionAt = retiredPtr(false), nil
	_, err = rf.st.UpdateBook(b.ID, b)
	require.NoError(t, err)

	gone, err := rf.f.Replan(context.Background(), nil, planned[rf.ids["left"]], &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, retCatGone, gone.Skipped)
	require.Zero(t, loads.Load(), "a gone row reads no rejects")

	_, err = rf.f.Replan(context.Background(), nil, planned[rf.ids["shell"]], &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, int32(1), loads.Load(), "a row still in the trash loads the rejects once")
}

// A veto on (A, shell) holds the shell's row whenever A's work ended up in the
// shell's target, however it got there: retired onto it, merged into it, or
// absorbed by it. After that the veto's ends resolve to the target and the
// shell's own pair is (shell, target), which the unresolved names never match,
// so only a veto that keeps each end's path finds it. The last case is the
// control: an A that went somewhere else does not hold the row.
func TestRetiredVersions_PlanCollapsedVetoHoldsTheRowWhoseTargetTookTheOtherEnd(t *testing.T) {
	cases := []struct {
		name string
		// arrange moves A (trashed, no group) and the shell's graph.
		arrange func(rf *retiredFixture)
		held    bool
	}{
		{"book retired into the target", func(rf *retiredFixture) {
			rf.retireInto("A", "primary")
			rf.purge("A")
		}, true},
		{"book retired into the target, row kept", func(rf *retiredFixture) {
			rf.retireInto("A", "primary")
		}, true},
		{"book merged into the target", func(rf *retiredFixture) {
			rf.mergedInto("A", "primary")
		}, true},
		{"book absorbed by the target (sync redirect, as a combine writes)", func(rf *retiredFixture) {
			ids := database.AsSyncIdentityStore(rf.st)
			require.NotNil(rf.t, ids)
			_, err := ids.MintOrGetSyncID(rf.ids["A"])
			require.NoError(rf.t, err)
			require.NoError(rf.t, ids.RecordSyncMerge(rf.ids["A"], rf.ids["primary"]))
		}, true},
		{"book retired into the target and the shell merged into it too", func(rf *retiredFixture) {
			rf.retireInto("A", "primary")
			rf.purge("A")
			rf.mergedInto("shell", "primary")
		}, true},
		{"book retired into another book", func(rf *retiredFixture) {
			rf.retireInto("A", "elsewhere")
			rf.purge("A")
		}, false},
		{"book merged into another book", func(rf *retiredFixture) {
			rf.mergedInto("A", "elsewhere")
		}, false},
		{"book left alone", func(rf *retiredFixture) {}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rf := newRetiredFixture(t)
			rf.live("primary", "g", rf.file("g/primary.mp3"))
			rf.live("elsewhere", "")
			rf.trashed("shell", "g", 30, rf.file("g/shell.mp3"))
			rf.trashed("A", "", 30)
			rf.reject("A", "shell", decisions.FamilySameWork)
			tc.arrange(rf)

			r := rf.plan()[rf.ids["shell"]]
			if tc.held {
				require.Equal(t, retCatVetoed, r.Class, r.Reason)
				require.Equal(t, decisions.FamilySameWork, r.Current["owner_veto_family"])
				require.False(t, r.Applicable())
			} else {
				require.Equal(t, retCatWouldRetire, r.Class, r.Reason)
			}
		})
	}
}

// retireTwice leaves shell with retired versions in two holders, each still
// holding a row: the shell cannot be resolved to one book.
func (rf *retiredFixture) retireTwice(shell, holderA, holderB string) {
	rf.t.Helper()
	rf.retireInto(shell, holderA)
	rf.retireInto(shell, holderB)
}

// An id that cannot be resolved holds the rows that touch it and no others, and
// every such id is reported: in the row, and once in the plan's log.
func TestRetiredVersions_PlanHoldsRowsThatTouchAnUnresolvableID(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("y1", "g", 30, rf.file("g/y1.mp3"))
	rf.trashed("y2", "g", 30, rf.file("g/y2.mp3"))
	rf.live("other-primary", "g2", rf.file("g2/primary.mp3"))
	rf.trashed("w", "g2", 30, rf.file("g2/w.mp3"))
	rf.live("h1", "")
	rf.live("h2", "")
	rf.live("h3", "")
	rf.live("h4", "")
	// Two shells whose retired versions name two holders each.
	rf.trashed("s1", "", 30)
	rf.trashed("s2", "", 30)
	rf.retireTwice("s1", "h1", "h2")
	rf.retireTwice("s2", "h3", "h4")
	// Rejects against the primary of group g, from both ambiguous shells.
	rf.reject("s1", "primary", decisions.FamilySameWork)
	rf.reject("s2", "primary", decisions.FamilySameCopy)

	rep := &fakeReporter{}
	rows, err := rf.f.Plan(context.Background(), nil, rep)
	require.NoError(t, err)
	byID := map[string]int{}
	for i, r := range rows {
		byID[r.RowID] = i
	}
	// A re-plan of a held row, nothing changed, is the same row: the ids in its
	// reason come out in a fixed order.
	y1 := rows[byID[rf.ids["y1"]]]
	again, err := rf.f.Replan(context.Background(), nil, y1, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, y1.Fingerprint, again.Fingerprint, "no changed_since_plan flap")
	require.Equal(t, y1.Reason, again.Reason)

	for _, name := range []string{"y1", "y2"} {
		r := rows[byID[rf.ids[name]]]
		require.Equal(t, retCatVetoUnresolvable, r.Class, "%s: %s", name, r.Reason)
		require.Equal(t, retCatVetoUnresolvable, r.Skipped, name)
		require.False(t, r.Applicable(), name)
		require.Equal(t, "review", string(r.Risk), name)
		require.Equal(t, retVetoUnresolvable, r.Current["owner_veto_family"], name)
		for _, s := range []string{"s1", "s2"} {
			require.Contains(t, r.Reason, rf.ids[s], "%s: every bad id is named, not only the first", name)
			require.Contains(t, r.Current["owner_veto_unresolved_ids"], rf.ids[s], name)
		}
	}
	require.Equal(t, retCatWouldRetire, rows[byID[rf.ids["w"]]].Class, "a row that touches none of them is planned as usual")

	var logged string
	for _, l := range rep.logs {
		if strings.Contains(l, retCatVetoUnresolvable) {
			logged += l
		}
	}
	require.Contains(t, logged, rf.ids["s1"])
	require.Contains(t, logged, rf.ids["s2"])

	// A definite veto on the same pair wins over an unresolvable one.
	rf.reject("primary", "y1", decisions.FamilySameWork)
	require.Equal(t, retCatVetoed, rf.plan()[rf.ids["y1"]].Class)
}

// A merge chain that loops cannot be resolved either: the row it touches is
// held, never read as "no veto".
func TestRetiredVersions_PlanHoldsRowsThatTouchAMergeCycle(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("y", "g", 30, rf.file("g/y.mp3"))
	rf.trashed("a", "", 30)
	rf.trashed("b", "", 30)
	rf.mergedInto("a", "b")
	rf.mergedInto("b", "a")
	rf.reject("a", "primary", decisions.FamilySameWork)

	r := rf.plan()[rf.ids["y"]]
	require.Equal(t, retCatVetoUnresolvable, r.Class, r.Reason)
	require.Contains(t, r.Current["owner_veto_unresolved_ids"], rf.ids["a"])
}

// A shell that was retired, reverted (its row went back, the version stayed,
// holding nothing) and retired again into another book resolves to that book:
// the emptied version is not a second holder.
func TestRetiredVersions_PlanRevertedThenRetiredAgainIsNotAmbiguous(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("h1", "")
	rf.live("h2", "g", rf.file("g/h2.mp3"))
	rf.trashed("y", "g", 30, rf.file("g/y.mp3"))
	rf.trashed("s", "", 30)
	ver, row := rf.retireInto("s", "h1")
	rf.unretire("s", "h1", ver, row)
	rf.retireInto("s", "h2")
	rf.reject("s", "y", decisions.FamilySameWork)

	r := rf.plan()[rf.ids["y"]]
	require.Equal(t, retCatVetoed, r.Class, "s resolves to h2, the target of y: %s", r.Reason)
	require.Equal(t, decisions.FamilySameWork, r.Current["owner_veto_family"])
}

// The raw rows of the decision log are cached between the readers of a long-
// lived owner, but every reader resolves the ids afresh: a book that was merged
// or retired after the plan changes what a stored veto means, and the re-plan
// sees it; so does a reject recorded after the plan.
func TestRetiredVersions_ReplanSeesWhatChangedAfterThePlanThroughASharedRowCache(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.sharedRows()
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("y", "g", 30, rf.file("g/y.mp3"))
	rf.trashed("late", "g", 30, rf.file("g/late.mp3"))
	rf.live("a", "")
	rf.reject("a", "y", decisions.FamilySameWork)

	rows := rf.plan()
	planned := rows[rf.ids["y"]]
	require.Equal(t, retCatWouldRetire, planned.Class, "a is a separate live book: the veto is not about this pair yet")

	// 1. a is merged into the target after the plan: its work now lives in the
	// target, so retiring y onto it joins the pair the owner rejected.
	rf.trash("a")
	rf.mergedInto("a", "primary")
	again, err := rf.f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, retCatVetoed, again.Class, again.Reason)
	require.NotEqual(t, planned.Fingerprint, again.Fingerprint)

	// 2. A reject recorded after that replan, on a row the cache has already
	// been filled for, is seen by the next one.
	latePlanned := rows[rf.ids["late"]]
	require.Equal(t, retCatWouldRetire, latePlanned.Class)
	rf.rejectShell("late")
	lateAgain, err := rf.f.Replan(context.Background(), nil, latePlanned, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, retCatVetoed, lateAgain.Class)
}

// The owner's rejects are resolved against the books as they are when each row
// is applied, not once for the whole run. Joker and Penguin are two shells of
// Batman's group, rejected as the same work; neither row is vetoed in the plan.
// Once the first is retired into Batman, the owner's "these two are not the same"
// reaches the other row's pair (the retired shell now IS Batman), so that row
// must be held, whichever goes first. A snapshot resolved before the first
// retire would apply both and drop the reject.
func TestRetiredApply_RejectIsResolvedAgainstTheBooksAsTheyAreAtEachRow(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("batman", "g", rf.file("g/batman.mp3"))
	rf.trashed("joker", "g", 30, rf.file("g/joker.mp3"))
	rf.trashed("penguin", "g", 30, rf.file("g/penguin.mp3"))
	rf.reject("joker", "penguin", decisions.FamilySameWork)

	res := rf.planAll()
	ids := rf.applicableIDs(res)
	require.ElementsMatch(t, []string{rf.ids["joker"], rf.ids["penguin"]}, ids, "neither row is vetoed in the plan")

	out := rf.applyRows(rf.f, res, ids, false, "op-two-shells")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	require.Equal(t, 1, out.ChangedSincePlan, "%v %+v", out.ByOutcome, out.Rows)
	require.Zero(t, out.Failed)
}
