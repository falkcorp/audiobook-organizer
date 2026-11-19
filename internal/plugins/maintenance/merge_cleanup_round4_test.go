// file: internal/plugins/maintenance/merge_cleanup_round4_test.go
// version: 1.2.2
// guid: 5c0d9e3a-6f21-4b7d-9a84-2e1f7b3c8d56
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Round 4 of the merge-cleanup review: a trashed group member is read from the
// trash, not from the group read that leaves it out; a live holder or member
// whose own chain ends dead is a live copy; the group is accounted for by its
// primary alone; the dead-end index is built by a pool; and a dead-end unmerge's
// preview shows the group untouched. Every name and title is invented.

func mcHush(group string, primary *bool) mcSpec {
	return mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: group, primary: primary}
}

// ---- R3-M1: a soft-deleted group member that points on ----

func TestMergeCleanupDeadEndSoftDeletedGroupMemberMergedIntoALiveBookIsFollowed(t *testing.T) {
	x := newMCFixture(t)
	x.mk("z", mcHushSpec(30000))
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue(), trashed: true})
	x.mk("member", mcHush("g", mcFalse()))
	// MarkedForDeletion, not a library state: GetBooksByVersionGroup drops it.
	mcTrash(t, x, "member")
	x.point("member", "z")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, "")
	require.Equal(t, x.ids["z"], r.Proposed["merged_into_book_id"])
	require.Contains(t, strings.Join(r.Evidence, " "), "in the trash and merged into "+x.ids["z"])
	require.NotContains(t, strings.Join(r.Evidence, " "), "stands alone")
}

func TestMergeCleanupDeadEndSoftDeletedGroupMemberWithNoPointerIsNotAnAnchor(t *testing.T) {
	x := newMCFixture(t)
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue(), trashed: true})
	x.mk("member", mcHush("g", mcFalse()))
	mcTrash(t, x, "member")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.NotContains(t, strings.Join(r.Evidence, " "), x.ids["member"])
}

func TestMergeCleanupDeadEndSoftDeletedGroupMemberOfAnotherGroupIsNotAnAnchor(t *testing.T) {
	x := newMCFixture(t)
	x.mk("z", mcHushSpec(30000))
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue(), trashed: true})
	x.mk("elsewhere", mcHush("other", mcFalse()))
	mcTrash(t, x, "elsewhere")
	x.point("elsewhere", "z")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
}

func TestMergeCleanupDeadEndSoftDeletedGroupMemberWhoseChainIsDeadLeadsNowhere(t *testing.T) {
	x := newMCFixture(t)
	x.mk("gone", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue(), trashed: true})
	x.mk("member", mcHush("g", mcFalse()))
	mcTrash(t, x, "member")
	x.point("member", "gone")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Contains(t, strings.Join(r.Evidence, " "), x.ids["member"], "the trashed member was followed and found dead")
}

// ---- R3-M2: a live group member merged into a different dead book ----

func TestMergeCleanupDeadEndLiveGroupMemberMergedIntoAnotherDeadBookIsALiveCopy(t *testing.T) {
	x := newMCFixture(t)
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue(), trashed: true})
	x.mk("dead2", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("member", mcHush("g", mcFalse()))
	x.point("member", "dead2")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipDeadEndShared)
	require.Contains(t, r.SkipReason, x.ids["member"])
	require.Equal(t, x.ids["member"], r.Current["dead_end_live_copies"])
}

// ---- R3-m1: the holder is the group's primary ----

func TestMergeCleanupDeadEndHolderThatIsTheGroupsPrimaryBesideASiblingRepoints(t *testing.T) {
	x := newMCFixture(t)
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse(), trashed: true})
	x.mk("heir", mcHush("g", mcTrue()))
	x.mk("sibling", mcHush("g", mcFalse()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	// The holder of the dead hop's retired versions is the group's primary, so
	// the holder route reaches it before the group route names it.
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["heir"], "rp", filepath.Join(x.root, "retired", "p.m4b"))
	r := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, r.Class)
	require.Equal(t, x.ids["heir"], r.Proposed["merged_into_book_id"], "the primary is the one candidate: %s", r.SkipReason)
	for _, h := range r.Holds {
		require.NotEqual(t, mcSkipRepointGroupNoPrimary, h.Kind, "the group has a usable primary")
	}
}

// The group's primary is itself a merged book: it is accounted for by where its
// own chain ends, so a live end means the group has a usable primary and its
// live sibling is that end's neighbour, not a book the walk missed.
func TestMergeCleanupDeadEndPrimaryThatIsMergedIntoALiveBookAccountsForTheGroup(t *testing.T) {
	x := newMCFixture(t)
	x.mk("z", mcHushSpec(30000))
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse(), trashed: true})
	// The primary's survivor is in the trash and points on, so the primary can
	// still be elected, and its chain ends at the live book z.
	x.mk("mid", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.point("mid", "z")
	x.mk("heir", mcHush("g", mcTrue()))
	x.pointRaw("heir", "mid")
	x.mk("sibling", mcHush("g", mcFalse()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, r.Class, r.SkipReason)
	require.Equal(t, x.ids["z"], r.Proposed["merged_into_book_id"], "the primary's own live end is the one candidate: %s", r.SkipReason)
	for _, h := range r.Holds {
		require.NotEqual(t, mcSkipRepointGroupNoPrimary, h.Kind)
	}
}

// A primary that leads nowhere live does not account for the group: its live
// sibling is still a place the loser may belong.
func TestMergeCleanupDeadEndPrimaryThatLeadsNowhereLeavesItsSiblingUnaccountedFor(t *testing.T) {
	x := newMCFixture(t)
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse(), trashed: true})
	x.mk("dead2", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("heir", mcHush("g", mcTrue()))
	x.pointRaw("heir", "dead2")
	x.mk("sibling", mcHush("g", mcFalse()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
	require.Contains(t, r.SkipReason, x.ids["sibling"])
}

// ---- R3-n2: both routes treat a live member the same ----

func TestMergeCleanupDeadEndMergedMemberReachingALiveBookDoesNotClearALiveSibling(t *testing.T) {
	x := newMCFixture(t)
	x.mk("z", mcHushSpec(30000))
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse(), trashed: true})
	x.mk("member", mcHush("g", mcFalse()))
	mcTrash(t, x, "member")
	x.point("member", "z")
	x.mk("sibling", mcHush("g", mcFalse()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	// With no primary, the live sibling is a second place the loser may belong,
	// whether the other live end was reached by a holder or by a merged member.
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
	require.Contains(t, r.SkipReason, x.ids["sibling"])
	require.Empty(t, r.Proposed["merged_into_book_id"])
}

// ---- R3-n3: the dead-end unmerge preview leaves the group as it is ----

func TestMergeCleanupDeadEndUnmergePreviewShowsTheGroupUnchanged(t *testing.T) {
	for _, group := range []string{"", "g"} {
		t.Run("group "+group, func(t *testing.T) {
			x := newMCFixture(t)
			if group == "" {
				mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
			} else {
				mcGroupedDeadEnd(x, group, mcFalse())
			}
			r := x.plan()["loser"]
			require.Equal(t, mcKindUnmerge, r.Class)
			require.Equal(t, "", r.Proposed["merged_into_book_id"])
			require.Equal(t, "unchanged", r.Proposed["version_group_id"])
			require.Equal(t, "unchanged", r.Proposed["is_primary_version"])
			// The loser stays in its group, so the group gate reads it that way
			// (R4-m1): a loser that would be its group's only live member with no
			// primary is held for a hand-off, and is never said to leave.
			if group == "" {
				require.Equal(t, "group_less", r.Current["group_effect"])
				return
			}
			require.Equal(t, "stays_in_group", r.Current["group_effect"])
			require.Contains(t, r.Flags, mcFlagGroupNeedsHandoff)
			hold := false
			for _, h := range r.Holds {
				hold = hold || h.Kind == mcSkipGroupNeedsHandoff
			}
			require.True(t, hold, "%+v", r.Holds)
		})
	}
	t.Run("a wrong-work unmerge still leaves its group", func(t *testing.T) {
		x := newMCFixture(t)
		x.wrongWork("loser", "target")
		r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
		require.Equal(t, "", r.Proposed["version_group_id"])
		require.Equal(t, "unset", r.Proposed["is_primary_version"])
	})
}

// ---- R3-m2: the dead-end index ----

func TestMergeCleanupDeadEndIndexOfManyLosersIsTheSameEveryRun(t *testing.T) {
	x := newMCFixture(t)
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	const n = 40
	for i := range n {
		name := fmt.Sprintf("loser-%02d", i)
		x.mk(name, mcHushSpec(30000))
		x.point(name, "dead")
	}
	for range 3 {
		rows := x.plan()
		for i := range n {
			name := fmt.Sprintf("loser-%02d", i)
			r := wantRow(t, rows, name, mcKindUnmerge, mcSkipDeadEndShared)
			mates := strings.Split(r.Current["dead_end_other_losers"], ",")
			require.Len(t, mates, n-1, name)
			require.NotContains(t, mates, x.ids[name])
			require.True(t, sortedStrings(mates), "mates are sorted")
		}
	}
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

func TestMergeCleanupDeadEndIndexStopsOnACancelledContext(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	run, err := x.f.newRun(context.Background(), &fakeReporter{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = run.deadEndIndex(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

// ---- R4-m1: a loop's head stays in its group too ----

func TestMergeCleanupCycleHeadStaysInItsGroup(t *testing.T) {
	x := newMCFixture(t)
	x.mk("a", mcHush("g", mcFalse()))
	x.mk("b", mcHush("g", mcFalse()))
	x.point("a", "b")
	x.point("b", "a")
	// Only the head is unmerged; the other member waits for a re-plan.
	rows := x.plan()
	head := rows["a"].Current["cycle_head"]
	name := "a"
	if head == x.ids["b"] {
		name = "b"
	}
	r := rows[name]
	require.Equal(t, mcKindUnmerge, r.Class, name)
	require.Equal(t, "stays_in_group", r.Current["group_effect"])
	require.Contains(t, r.Flags, mcFlagGroupNeedsHandoff)
}

// ---- R4-M1: a live merged hop on the chain is a source of anchors too ----

// L -> B -> T. B is live, merged into the trashed T, has no file of its own,
// and sits in group G beside the live primary A. L's content went to B, and B's
// group holds the live copy A of the same work, so the loser does not stand
// alone: it repoints to A.
func TestMergeCleanupDeadEndFilelessLiveHopWhoseGroupHoldsALiveBookIsFollowed(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse()})
	x.point("b", "t")
	x.mk("a", mcHush("g", mcTrue()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "b")
	r := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
	require.Equal(t, x.ids["a"], r.Proposed["merged_into_book_id"])
	require.NotContains(t, strings.Join(r.Evidence, " "), "stands alone")
}

// The same hop with no usable primary in its group holds the loser.
func TestMergeCleanupDeadEndFilelessLiveHopWhoseGroupHasNoPrimaryHolds(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse()})
	x.point("b", "t")
	x.mk("a", mcHush("g", mcFalse()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "b")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
	require.Contains(t, r.SkipReason, x.ids["a"])
}

// L -> B -> T. B is live and merged, has no file because its files were retired
// onto the live holder H. The holder is read from B, not only from trashed hops.
func TestMergeCleanupDeadEndFilelessLiveHopWhoseFilesWereRetiredOntoALiveHolder(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("h", mcHushSpec(30000))
	retireRowOnto(t, x.st, x.ids["b"], x.ids["h"], "rb", filepath.Join(x.root, "retired", "b.m4b"))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "b")
	r := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
	require.Equal(t, x.ids["h"], r.Proposed["merged_into_book_id"])
}

// With nothing behind the file-less hop (no group, no holder) the loser really
// does stand alone: the control that the new reads do not hold every chain.
func TestMergeCleanupDeadEndFilelessLiveHopWithNothingBehindItStillStandsAlone(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "b")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Contains(t, strings.Join(r.Evidence, " "), "stands alone")
}

// A dead end held by another live copy does not also say the loser stands alone.
func TestMergeCleanupDeadEndHeldBesideALiveCopyDoesNotSayItStandsAlone(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("gone", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("holder", mcHushSpec(30000))
	x.point("holder", "gone")
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder"], "rd", filepath.Join(x.root, "retired", "d.m4b"))
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipDeadEndShared)
	require.NotContains(t, strings.Join(r.Evidence, " "), "stands alone")
	require.Contains(t, strings.Join(r.Evidence, " "), "no live book is reachable from it")
}

// A cancelled caller does not poison the index for the callers after it.
func TestMergeCleanupDeadEndIndexIsNotCachedFromACancelledCaller(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	run, err := x.f.newRun(context.Background(), &fakeReporter{})
	require.NoError(t, err)
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = run.deadEndIndex(dead)
	require.ErrorIs(t, err, context.Canceled)
	idx, err := run.deadEndIndex(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{x.ids["loser"]}, mcCoLoserIDs(idx[x.ids["dead"]]))
}

// An expired deadline is a caller's own stop too, not a fact about the library.
func TestMergeCleanupDeadEndIndexIsNotCachedFromAnExpiredCaller(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	run, err := x.f.newRun(context.Background(), &fakeReporter{})
	require.NoError(t, err)
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = run.deadEndIndex(expired)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	idx, err := run.deadEndIndex(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{x.ids["loser"]}, mcCoLoserIDs(idx[x.ids["dead"]]))
}

func mcCoLoserIDs(cs []mcCoLoser) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.id)
	}
	return out
}

// ---- R5-M1: every other book that enters the dead end is a source too ----

// L2 -> T beside B -> T, where B is live, has no file and sits in group G with
// the live primary P. L2 and B were merged into the same dead book, so B's group
// holds the live copy of L2's work: L2 repoints to P rather than standing alone.
func TestMergeCleanupDeadEndFilelessCoLoserWhoseGroupHoldsALivePrimaryIsFollowed(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse()})
	x.point("b", "t")
	x.mk("p", mcHush("g", mcTrue()))
	x.mk("l2", mcHushSpec(30000))
	x.point("l2", "t")
	r := x.plan()["l2"]
	require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
	require.Equal(t, x.ids["p"], r.Proposed["merged_into_book_id"])
	require.NotContains(t, strings.Join(r.Evidence, " "), "stands alone")
}

// The same co-loser whose files were retired onto a live holder: the holder is
// read from the co-loser.
func TestMergeCleanupDeadEndFilelessCoLoserWhoseFilesWereRetiredOntoALiveHolder(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("h", mcHushSpec(30000))
	retireRowOnto(t, x.st, x.ids["b"], x.ids["h"], "rb", filepath.Join(x.root, "retired", "b.m4b"))
	x.mk("l2", mcHushSpec(30000))
	x.point("l2", "t")
	r := x.plan()["l2"]
	require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
	require.Equal(t, x.ids["h"], r.Proposed["merged_into_book_id"])
}

// A co-loser in the trash (a library state and a soft delete are both tried):
// it took content to the dead book too, and its group's live primary holds it.
func TestMergeCleanupDeadEndTrashedCoLoserWhoseGroupHoldsALivePrimaryIsFollowed(t *testing.T) {
	for _, soft := range []bool{false, true} {
		name := "library state"
		if soft {
			name = "soft deleted"
		}
		t.Run(name, func(t *testing.T) {
			x := newMCFixture(t)
			x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
			x.mk("x", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse(), trashed: !soft})
			x.point("x", "t")
			if soft {
				mcTrash(t, x, "x")
			}
			x.mk("p", mcHush("g", mcTrue()))
			x.mk("loser", mcHushSpec(30000))
			x.point("loser", "t")
			r := x.plan()["loser"]
			require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
			require.Equal(t, x.ids["p"], r.Proposed["merged_into_book_id"])
		})
	}
}

// L -> T beside L3 -> X -> T, where L3 is live with no file and X is trashed in
// a group with the live primary P: the trashed hop on a co-loser's chain is a
// source.
func TestMergeCleanupDeadEndTrashedHopOnACoLosersChainIsASource(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("x", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse(), trashed: true})
	x.point("x", "t")
	x.mk("l3", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("l3", "x")
	x.mk("p", mcHush("g", mcTrue()))
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	r := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, r.Class, "%s", r.SkipReason)
	require.Equal(t, x.ids["p"], r.Proposed["merged_into_book_id"])
}

// A co-loser in the loser's OWN group does not turn the loser into a repoint at
// its group's primary: the loser stays in its group and the hand-off gate owns it.
func TestMergeCleanupDeadEndCoLoserInTheLosersOwnGroupIsNotReadThroughTheGroup(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse()})
	x.point("b", "t")
	x.mk("p", mcHush("g", mcTrue()))
	x.mk("loser", mcHush("g", mcFalse()))
	x.point("loser", "t")
	r := x.plan()["loser"]
	require.Equal(t, mcKindUnmerge, r.Class, "%s", r.SkipReason)
	require.Empty(t, r.Proposed["merged_into_book_id"])
}

// A file-less, group-less, holder-less co-loser contributes nothing.
func TestMergeCleanupDeadEndBareFilelessCoLoserStillLeavesTheLoserAlone(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("b", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("b", "t")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "t")
	wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
}

// The sources of one dead book are computed once and shared by every loser.
func TestMergeCleanupDeadEndSourcesAreComputedOncePerDeadBook(t *testing.T) {
	x := newMCFixture(t)
	x.mk("t", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	for i := 0; i < 5; i++ {
		n := fmt.Sprintf("l%d", i)
		x.mk(n, mcHushSpec(30000+i))
		x.point(n, "t")
	}
	run, err := x.f.newRun(context.Background(), &fakeReporter{})
	require.NoError(t, err)
	a, err := run.deadEndSources(context.Background(), x.ids["t"])
	require.NoError(t, err)
	b, err := run.deadEndSources(context.Background(), x.ids["t"])
	require.NoError(t, err)
	require.Len(t, a, 5)
	require.Same(t, &a[0], &b[0], "the second call reads the cached list")
}

// ---- R5-m1: the loser's own flag counts at the hand-off gate ----

func TestMergeCleanupDeadEndLoserThatIsItsGroupsPrimaryIsNotHeldForAHandoff(t *testing.T) {
	for _, c := range []struct {
		name string
		flag *bool
		held bool
	}{
		{"nil flag reads as primary", nil, false},
		{"explicit true flag", mcTrue(), false},
		{"explicit false flag", mcFalse(), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newMCFixture(t)
			x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
			x.mk("loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: c.flag})
			x.pointRaw("loser", "dead")
			r := x.plan()["loser"]
			require.Equal(t, mcKindUnmerge, r.Class, "%+v", r.Holds)
			require.Equal(t, "stays_in_group", r.Current["group_effect"])
			held := false
			for _, h := range r.Holds {
				held = held || h.Kind == mcSkipGroupNeedsHandoff
			}
			require.Equal(t, c.held, held, "%+v", r.Holds)
			require.Equal(t, c.held, slices.Contains(r.Flags, mcFlagGroupNeedsHandoff))
		})
	}
}

// ---- R5-n1 / R5-n2: the dead-end index and its failures ----

// failOnceStore fails the first full listing and answers every later one.
type failOnceStore struct {
	mergeCleanupReader
	calls atomic.Int32
}

func (s *failOnceStore) GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error) {
	if s.calls.Add(1) == 1 {
		return nil, errors.New("the listing is unavailable")
	}
	return s.mergeCleanupReader.GetAllBooksCoreComplete(limit, offset)
}

// A build that fails for a reason other than the caller's context is the run's
// snapshot answer: every later row reports the same error, it is not retried.
func TestMergeCleanupDeadEndIndexFailureIsCachedForTheRun(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	run, err := x.f.newRun(context.Background(), &fakeReporter{})
	require.NoError(t, err)
	run.merged = nil
	run.store = &failOnceStore{mergeCleanupReader: run.store}
	_, err = run.deadEndIndex(context.Background())
	require.ErrorContains(t, err, "the listing is unavailable")
	_, err = run.deadEndIndex(context.Background())
	require.ErrorContains(t, err, "the listing is unavailable", "the failed build is not retried")
}

// A caller waiting on a build that another caller's cancel stopped retries with
// its own context.
func TestMergeCleanupDeadEndIndexWaiterOfACancelledBuildRetriesWithItsOwnContext(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	run, err := x.f.newRun(context.Background(), &fakeReporter{})
	require.NoError(t, err)
	// A build that the first caller's cancel already stopped, still in the cache.
	cancelled := &onceVal[map[string][]mcCoLoser]{}
	cancelled.once.Do(func() { cancelled.err = context.Canceled })
	run.deadEnds.Store("", cancelled)
	idx, err := run.deadEndIndex(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{x.ids["loser"]}, mcCoLoserIDs(idx[x.ids["dead"]]))
	// And the stale entry was replaced, not left to fail the next caller.
	idx, err = run.deadEndIndex(context.Background())
	require.NoError(t, err)
	require.Len(t, idx[x.ids["dead"]], 1)
}
