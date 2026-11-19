// file: internal/plugins/maintenance/merge_cleanup_round4_test.go
// version: 1.0.0
// guid: 5c0d9e3a-6f21-4b7d-9a84-2e1f7b3c8d56
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
