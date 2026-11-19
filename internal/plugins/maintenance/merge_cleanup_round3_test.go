// file: internal/plugins/maintenance/merge_cleanup_round3_test.go
// version: 1.0.4
// guid: 7e985540-891e-4d66-9ce1-fd86d2879faa
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Round 3 of the merge-cleanup review: a dead end never says "stands alone"
// while a live book of the same version group exists or a trashed anchor still
// points on; a swapped title/author is held whichever way round it is stored;
// a repoint row shows and fingerprints its target; and the other losers of one
// dead end are held together. Every name and title is invented.

// mcTrash puts a fixture book in the trash after it was made (a book that
// holds retired versions has to exist live first).
func mcTrash(t *testing.T, x *mcFixture, name string) {
	t.Helper()
	_, err := x.st.ModifyBook(x.ids[name], func(b *database.Book) error {
		b.MarkedForDeletion = retiredPtr(true)
		b.MarkedForDeletionAt = retiredPtr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
		return nil
	})
	require.NoError(t, err)
}

func mcGroupedDeadEnd(x *mcFixture, group string, loserFlag *bool) {
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: group, primary: mcTrue(), trashed: true})
	x.mk("loser", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: group, primary: loserFlag})
	x.point("loser", "dead")
}

// ---- R2-M1: a live book of the dead hop's group ----

func TestMergeCleanupDeadEndGroupWithALiveMemberButNoPrimaryIsHeldNotStandAlone(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(x *mcFixture)
	}{
		{"every live member carries an explicit false flag", func(x *mcFixture) {
			x.mk("sibling", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse()})
		}},
		{"two live members carry no flag, so none can be told apart", func(x *mcFixture) {
			x.mk("sibling", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g"})
			x.mk("twin", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newMCFixture(t)
			// The group's primary (the dead hop) is in the trash and the loser
			// is itself a member whose flag is false.
			mcGroupedDeadEnd(x, "g", mcFalse())
			c.build(x)
			r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
			require.Empty(t, r.Proposed["merged_into_book_id"], "nothing is proposed")
			require.NotContains(t, strings.Join(r.Evidence, " "), "stands alone")
			require.Contains(t, r.SkipReason, x.ids["sibling"], "the live book the walk did not reach is named")
			require.Contains(t, r.BookIDs, x.ids["sibling"])
		})
	}

	t.Run("the loser is itself the group's primary and a live sibling exists", func(t *testing.T) {
		x := newMCFixture(t)
		// An explicit true on the loser makes it the incumbent, which the walk
		// skips (it is the loser): the sibling is still a live book of the group.
		mcGroupedDeadEnd(x, "g", mcTrue())
		x.mk("sibling", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse()})
		wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
	})

	t.Run("a candidate reached another way does not clear a live member the group never named", func(t *testing.T) {
		x := newMCFixture(t)
		mcGroupedDeadEnd(x, "g", mcFalse())
		x.mk("sibling", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse()})
		x.mk("holder", mcHushSpec(30000))
		retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder"], "rh", filepath.Join(x.root, "retired", "h.m4b"))
		r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointGroupNoPrimary)
		require.Empty(t, r.Proposed["merged_into_book_id"])
	})

	t.Run("the live member is itself the holder: one candidate, not held", func(t *testing.T) {
		x := newMCFixture(t)
		mcGroupedDeadEnd(x, "g", mcFalse())
		x.mk("sibling", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse()})
		retireRowOnto(t, x.st, x.ids["dead"], x.ids["sibling"], "rs", filepath.Join(x.root, "retired", "s.m4b"))
		r := x.plan()["loser"]
		require.Equal(t, mcKindRepoint, r.Class)
		require.Equal(t, x.ids["sibling"], r.Proposed["merged_into_book_id"], "the holder is the one candidate")
		for _, h := range r.Holds {
			// The version-group fixer owns the group, which holds the row here; the
			// sibling is the candidate, not a book the walk missed.
			require.NotEqual(t, mcSkipRepointGroupNoPrimary, h.Kind)
		}
	})

	t.Run("a live primary is followed even beside another live non-primary member", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue()})
		x.mk("sibling", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse()})
		x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse(), trashed: true})
		x.mk("loser", mcHushSpec(30000))
		x.point("loser", "dead")
		r := wantRow(t, x.plan(), "loser", mcKindRepoint, "")
		require.Equal(t, x.ids["heir"], r.Proposed["merged_into_book_id"])
	})

	t.Run("a group with no live member left still lets the loser stand alone", func(t *testing.T) {
		x := newMCFixture(t)
		x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue(), trashed: true})
		x.mk("also-dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse(), trashed: true})
		x.mk("loser", mcHushSpec(30000))
		x.point("loser", "dead")
		r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
		require.Contains(t, strings.Join(r.Evidence, " "), "stands alone")
	})
}

// ---- R2-M1: a trashed anchor that still points on ----

func TestMergeCleanupDeadEndTrashedHolderMergedIntoALiveBookIsFollowed(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("z", mcHushSpec(30000))
	// The holder of the dead hop's retired versions is itself in the trash, but
	// its pointer leads on to a live book, as the loser's own chain would be walked.
	x.mk("holder", mcHushSpec(30000))
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder"], "rt", filepath.Join(x.root, "retired", "t.m4b"))
	mcTrash(t, x, "holder")
	x.point("holder", "z")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, "")
	require.Equal(t, x.ids["z"], r.Proposed["merged_into_book_id"])
	require.Contains(t, strings.Join(r.Evidence, " "), "in the trash and merged into "+x.ids["z"])
	require.NotContains(t, strings.Join(r.Evidence, " "), "stands alone")
}

func TestMergeCleanupDeadEndTrashedGroupMemberMergedIntoALiveBookIsFollowed(t *testing.T) {
	x := newMCFixture(t)
	x.mk("z", mcHushSpec(30000))
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcTrue(), trashed: true})
	// The group read leaves out a soft-deleted member, so the member is in the
	// trash by library state, which the read returns.
	x.mk("member", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse(), state: "deleted"})
	x.point("member", "z")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, "")
	require.Equal(t, x.ids["z"], r.Proposed["merged_into_book_id"])
	require.Contains(t, strings.Join(r.Evidence, " "), "in the trash and merged into "+x.ids["z"])
}

func TestMergeCleanupDeadEndTrashedAnchorWhosePointerLeadsNowhereLetsTheLoserStandAlone(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("gone", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("holder", mcHushSpec(30000))
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder"], "rt2", filepath.Join(x.root, "retired", "t2.m4b"))
	mcTrash(t, x, "holder")
	x.point("holder", "gone")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	ev := strings.Join(r.Evidence, " ")
	require.Contains(t, ev, x.ids["holder"], "the anchor that was followed is named")
	require.Equal(t, 1, strings.Count(ev, x.ids["holder"]), "an anchor is listed once")
}

func TestMergeCleanupDeadEndAnchorNamedByTwoRoutesIsListedOnce(t *testing.T) {
	x := newMCFixture(t)
	// The holder of the dead hop's retired versions is also a member of its
	// group that was merged into a dead end: two routes name it.
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcTrue(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	x.mk("gone", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("holder", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g", primary: mcFalse()})
	x.point("holder", "gone")
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder"], "rt3", filepath.Join(x.root, "retired", "t3.m4b"))
	// The holder is a live book with a file whose own chain ends dead, so it is a
	// live copy, held; naming it twice lists it once.
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipDeadEndShared)
	require.Equal(t, x.ids["holder"], r.Current["dead_end_live_copies"])
	require.Equal(t, 1, strings.Count(r.SkipReason, x.ids["holder"]))
}

func TestMergeCleanupDeadEndTrashedHolderWithNoPointerLeadsNowhere(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	x.mk("holder", mcHushSpec(30000))
	retireRowOnto(t, x.st, x.ids["dead"], x.ids["holder"], "rt4", filepath.Join(x.root, "retired", "t4.m4b"))
	mcTrash(t, x, "holder")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	require.Empty(t, r.Proposed["merged_into_book_id"])
	require.Contains(t, strings.Join(r.Evidence, " "), x.ids["holder"], "the trashed holder is named as followed")
}

func TestMergeCleanupDeadEndDoesNotCountTheLosersOwnChainAsFollowed(t *testing.T) {
	x := newMCFixture(t)
	// L -> hop (in the trash by library state, still pointing on, in group g) ->
	// dead. The group read returns the hop, which carries a pointer; it is the
	// loser's own chain, not an anchor the walk had to follow.
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, trashed: true})
	x.mk("hop", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, group: "g", primary: mcFalse(), state: "deleted"})
	x.point("hop", "dead")
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "hop")
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	ev := strings.Join(r.Evidence, " ")
	require.Contains(t, ev, "stands alone")
	require.NotContains(t, ev, "every holder", "no anchor was followed: the only book looked at is the chain itself")
}

// A repoint says the loser and the target are one work. When the signals read
// the target as a fragment of the loser the hold is right but the words are not
// "not the same work": a swap of the keeper is what the pair needs.
func TestMergeCleanupRepointTargetThatIsAFragmentIsHeldWithWordsThatSayFragment(t *testing.T) {
	x := newMCFixture(t)
	x.mk("heir", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000), group: "g-f", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(9000), group: "g-f", primary: mcFalse(), trashed: true})
	x.mk("loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipRepointNotSameWork)
	require.Contains(t, r.SkipReason, "fragment of the loser")
	require.NotContains(t, r.SkipReason, "do not call the loser")
	require.Equal(t, x.ids["heir"], r.Proposed["merged_into_book_id"])
}

func TestMergeCleanupDeadEndOnAMissingBookSaysItReadNothing(t *testing.T) {
	x := newMCFixture(t)
	x.mk("loser", mcHushSpec(30000))
	_, err := x.st.ModifyBook(x.ids["loser"], func(b *database.Book) error {
		b.MergedIntoBookID = retiredPtr("01HZZZZZZZZZZZZZZZZZZZZZZZ")
		b.IsPrimaryVersion = mcFalse()
		return nil
	})
	require.NoError(t, err)
	r := wantRow(t, x.plan(), "loser", mcKindUnmerge, "")
	ev := strings.Join(r.Evidence, " ")
	require.Contains(t, ev, "no longer exists")
	require.NotContains(t, ev, "no holder of its retired versions", "nothing was checked, so it is not claimed")
}

// ---- R2-M2: a swapped title/author stored one way round ----

func TestMergeCleanupPlanHoldsAOneWaySwappedPairForTheSwapFixer(t *testing.T) {
	// The production shape: the junk "read by ..." title and the real title in
	// the author field, against a twin that carries the real title. Only one
	// credit of the pair is the other's title.
	for _, c := range []struct {
		name          string
		loser, target mcSpec
	}{
		{"the loser is the swapped one",
			mcSpec{title: "read by Alfred Pennyworth", authors: []string{"Hush"}, files: one(40000)},
			mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)}},
		{"the target is the swapped one",
			mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(40000)},
			mcSpec{title: "read by Alfred Pennyworth", authors: []string{"Hush"}, files: one(30000)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newMCFixture(t)
			x.mk("target", c.target)
			x.mk("loser", c.loser)
			x.point("loser", "target")
			r := wantRow(t, x.plan(), "loser", mcClassNoVerdict, mcSkipTitleAuthorSwapped)
			require.Empty(t, r.Proposed["merged_into_book_id"])
			require.Contains(t, r.SkipReason, "Fix swapped title and author")
		})
	}
}

// ---- R2-m1: a repoint row shows and fingerprints its target ----

func TestMergeCleanupRepointRowShowsItsTarget(t *testing.T) {
	x := newMCFixture(t)
	x.mk("heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-c", primary: mcTrue(), asin: "B000HEIR01"})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-c", primary: mcFalse(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, "")
	require.Equal(t, "Hush", r.Current["repoint_target_title"])
	require.Equal(t, "Selina Kyle", r.Current["repoint_target_credits"])
	require.Equal(t, "30000", r.Current["repoint_target_runtime_seconds"])
	require.Equal(t, "B000HEIR01", r.Current["repoint_target_asin"])
	require.Equal(t, "1", r.Current["repoint_target_files"])
	require.NotEmpty(t, r.Current["repoint_signals"], "what the verdict read of the pair")
	require.Contains(t, strings.Join(r.Evidence, " "), "signals against the repoint target")
	found := false
	for _, m := range r.Members {
		if m.Role == "repoint_target" {
			found = true
			require.Equal(t, x.ids["heir"], m.BookID)
			require.Equal(t, "Hush", m.Title)
			require.Equal(t, 1, m.Files)
		}
	}
	require.True(t, found)
}

func TestMergeCleanupReplanSeesAChangeToTheRepointTarget(t *testing.T) {
	x := newMCFixture(t)
	x.mk("heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-c", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-c", primary: mcFalse(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	planned := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, planned.Class)

	// A hash on the target's file moves nothing the row shows and not the verdict.
	files, err := x.st.GetBookFiles(x.ids["heir"])
	require.NoError(t, err)
	f := files[0]
	f.FileHash = "a-new-hash"
	require.NoError(t, x.st.UpdateBookFile(f.ID, &f))
	r, err := x.f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, mcKindRepoint, r.Class)
	require.Equal(t, planned.Skipped, r.Skipped)
	require.NotEqual(t, planned.Fingerprint, r.Fingerprint, "the repoint target's files are part of what the owner approved")
}

// A size on the target's file row is read by nothing the row shows, so only the
// file signature carries it into the fingerprint.
func TestMergeCleanupReplanSeesATargetFileSizeThatNothingElseShows(t *testing.T) {
	x := newMCFixture(t)
	x.mk("heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-s", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-s", primary: mcFalse(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	planned := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, planned.Class)

	files, err := x.st.GetBookFiles(x.ids["heir"])
	require.NoError(t, err)
	f := files[0]
	f.FileSize += 4096
	require.NoError(t, x.st.UpdateBookFile(f.ID, &f))
	r, err := x.f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, planned.Current, r.Current, "premise: the row shows nothing different")
	require.NotEqual(t, planned.Fingerprint, r.Fingerprint)
}

// ---- R2-m2: the other losers of one dead end ----

func TestMergeCleanupCoLosersOfOneDeadEndAreHeldTogether(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser-a", mcHushSpec(30000))
	x.mk("loser-b", mcHushSpec(30000))
	x.point("loser-b", "dead")
	// A third copy reaches the dead end through another merged book.
	x.mk("loser-c", mcHushSpec(30000))
	x.mk("middle", mcHushSpec(30000))
	x.point("loser-c", "middle")
	x.point("middle", "dead")
	rows := x.plan()
	for _, name := range []string{"loser-a", "loser-b", "loser-c", "middle"} {
		r := wantRow(t, rows, name, mcKindUnmerge, mcSkipDeadEndShared)
		require.NotEmpty(t, r.Current["dead_end_other_losers"], name)
		require.NotContains(t, r.Current["dead_end_other_losers"], x.ids[name], "a row does not count itself")
		require.Equal(t, "", r.Proposed["merged_into_book_id"], "the proposal is still shown beside the hold")
		require.Contains(t, r.SkipReason, "several live copies of one work")
	}
	require.Contains(t, rows["loser-a"].Current["dead_end_other_losers"], x.ids["loser-b"])
	require.Contains(t, rows["loser-a"].Current["dead_end_other_losers"], x.ids["loser-c"])

	// Replan builds its own run (no listing handed to it) and decides the same.
	r, err := x.f.Replan(context.Background(), nil, rows["loser-b"], &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, mcSkipDeadEndShared, r.Skipped)
	require.Equal(t, rows["loser-b"].Fingerprint, r.Fingerprint)
}

func TestMergeCleanupADeadEndsLoneLoserIsNotHeldAndNeitherIsOneWithAnEmptyMate(t *testing.T) {
	x := newMCFixture(t)
	mcDeadEnd(x, "dead", "loser", mcHushSpec(30000))
	// Mates that are not copies of the work: no file, in the trash.
	x.mk("shell", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}})
	x.point("shell", "dead")
	x.mk("trashed-mate", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), trashed: true})
	x.point("trashed-mate", "dead")
	// In the trash by library state but not soft-deleted: the listing still
	// hands it over, so only the index's own trash check keeps it out.
	x.mk("state-trashed-mate", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), state: "deleted"})
	x.point("state-trashed-mate", "dead")
	// And a loser of another dead end.
	mcDeadEnd(x, "other-dead", "other-loser", mcHushSpec(30000))
	rows := x.plan()
	r := wantRow(t, rows, "loser", mcKindUnmerge, "")
	require.Empty(t, r.Current["dead_end_other_losers"])
	wantRow(t, rows, "other-loser", mcKindUnmerge, "")
}

// How many books carry the target's hash is read off other books' rows, not the
// target's own, so neither the row nor the file signature shows it: only the
// side signature carries it into the fingerprint.
func TestMergeCleanupReplanSeesAHashAnotherBookNowSharesWithTheRepointTarget(t *testing.T) {
	x := newMCFixture(t)
	x.mk("heir", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: []mcFile{{secs: 30000, size: 30000 * 8000, hash: "heir-hash"}}, group: "g-h", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-h", primary: mcFalse(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	planned := x.plan()["loser"]
	require.Equal(t, mcKindRepoint, planned.Class)

	x.mk("other", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: []mcFile{{secs: 100, size: 800000, hash: "heir-hash"}}})
	r, err := x.f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, mcKindRepoint, r.Class)
	require.Equal(t, planned.Current, r.Current, "premise: the row shows nothing different")
	require.NotEqual(t, planned.Fingerprint, r.Fingerprint)
}
