// file: internal/plugins/maintenance/merge_cleanup_repoint_test.go
// version: 1.1.0
// guid: b4e1c7d2-9a35-4f08-8c6e-d3a05f1b72e9
// last-edited: 2026-11-19

package maintenance

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Repoint rows (a merge chain that ends nowhere live) and the rule that a
// repoint says "these two are one work": it gets the signal check and the
// owner's rejects, and a walk that could not prove nothing is reachable never
// says the book stands alone. Every name and title is invented.

// mcDeadEnd makes a trashed book and a loser pointing at it.
func mcDeadEnd(x *mcFixture, dead, loser string, loserSpec mcSpec) {
	x.mk(dead, mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk(loser, loserSpec)
	x.point(loser, dead)
}

func mcHushSpec(secs int) mcSpec {
	return mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(secs)}
}

func TestMergeCleanupDeadEndHolderThatLoopsIsHeldNotStandAlone(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("holder-a", mcHushSpec(30000))
	x.mk("holder-b", mcHushSpec(30000))
	x.point("holder-a", "holder-b")
	x.point("holder-b", "holder-a")
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder-a"], "rl", filepath.Join(x.root, "retired", "l.m4b"))
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointAmbiguous)
	require.Contains(t, r.SkipReason, "cannot be shown")
	require.Contains(t, r.SkipReason, x.ids["holder-a"])
	require.Empty(t, r.Proposed["merged_into_book_id"])
	require.NotContains(t, strings.Join(r.Evidence, " "), "stands alone", "the walk did not prove nothing is reachable")
	require.NotContains(t, strings.Join(r.Evidence, " "), "no live book is reachable")
}

// A holder that is itself a live book with a file, merged into a book that is
// also in the trash, is a live copy of the work: the loser is not the only one
// left, so it is held rather than said to stand alone (R3-M2).
func TestMergeCleanupDeadEndLiveHolderWhoseChainIsDeadIsALiveCopyAndHolds(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("gone", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("holder", mcHushSpec(30000))
	x.point("holder", "gone")
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder"], "rd", filepath.Join(x.root, "retired", "d.m4b"))
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipDeadEndShared)
	require.Contains(t, r.SkipReason, x.ids["holder"], "the live copy is named")
	require.Equal(t, x.ids["holder"], r.Current["dead_end_live_copies"])
	require.Empty(t, r.Current["dead_end_other_losers"], "it is not a loser of the same dead end")
}

// A live holder with no file of its own is not a copy of the work: it leads
// nowhere, as before.
func TestMergeCleanupDeadEndLiveHolderWithNoFileWhoseChainIsDeadLeadsNowhere(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("gone", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("holder", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("holder", "gone")
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder"], "rdn", filepath.Join(x.root, "retired", "dn.m4b"))
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	ev := strings.Join(r.Evidence, " ")
	require.Contains(t, ev, x.ids["holder"])
	require.Contains(t, ev, "a merged book with no file of its own")
}

func TestMergeCleanupRepointToADifferentWorkIsHeld(t *testing.T) {
	x := newMCFixture(t)
	// The loser's dead end has a live primary, but it is another book by
	// another author with another title and another runtime.
	x.mk("heir", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"}, files: one(40000), group: "g-r", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-r", primary: mcFalse(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointNotSameWork)
	require.Equal(t, x.ids["heir"], r.Proposed["merged_into_book_id"], "the proposal is shown beside its hold")
	require.Contains(t, r.SkipReason, "unmerge", "the verdict against the target is named")
	require.Contains(t, r.SkipReason, "TITLE_DIFF")
}

func TestMergeCleanupRepointHoldsOnTheTargetsOwnSkip(t *testing.T) {
	x := newMCFixture(t)
	// The heir is a title-author-swapped twin of the loser: that is not a
	// repoint target and it names the repair that owns it.
	x.mk("heir", mcSpec{title: "Selina Kyle", authors: []string{"Hush"}, files: one(30000), group: "g-s", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-s", primary: mcFalse(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipTitleAuthorSwapped)
	require.Contains(t, r.SkipReason, x.ids["heir"])
}

func TestMergeCleanupRepointToTheSameWorkIsClean(t *testing.T) {
	x := newMCFixture(t)
	x.mk("heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-c", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-c", primary: mcFalse(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, "")
	require.Equal(t, x.ids["heir"], r.Proposed["merged_into_book_id"])
}

func TestMergeCleanupRepointHonoursTheOwnersReject(t *testing.T) {
	build := func(veto *mcFakeVetoes) (*mcFixture, map[string]string) {
		x := newMCFixture(t)
		x.mk("heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-o", primary: mcTrue()})
		x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-o", primary: mcFalse(), trashed: true})
		x.mk("loser", mcHushSpec(30000))
		x.point("loser", "dead")
		if veto != nil {
			veto.pairs = map[string]bool{x.ids["loser"] + "|" + x.ids["heir"]: true}
			x.vetoes = veto
			x.rebuild()
		}
		return x, nil
	}
	t.Run("a reject of the pair holds the repoint", func(t *testing.T) {
		x, _ := build(&mcFakeVetoes{})
		r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipOwnerSaysDistinct)
		require.Contains(t, r.Flags, mcFlagOwnerSaysDistinct)
		require.Contains(t, r.SkipReason, "repoint says they are one work")
	})
	t.Run("a reject that cannot be resolved holds the repoint", func(t *testing.T) {
		x, _ := build(nil)
		x.vetoes = &mcFakeVetoes{unresolvable: true}
		x.rebuild()
		wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipVetoUnresolvable)
	})
	t.Run("no reject leaves it applicable", func(t *testing.T) {
		x, _ := build(nil)
		wantRow(t, x.plan(), "loser", mcKindRepoint, "")
	})
}
