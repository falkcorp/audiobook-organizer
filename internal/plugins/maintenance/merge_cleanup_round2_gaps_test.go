// file: internal/plugins/maintenance/merge_cleanup_round2_gaps_test.go
// version: 1.0.0
// guid: d2a7f64b-0e19-4c83-b5d1-93e6a8c0f472
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Cases the round-2 mutation run showed no test pinned: each was a rule the
// code had and nothing checked. Every name and title is invented.

func mcHoldKinds(holds []repairs.RowHold) []string {
	var out []string
	for _, h := range holds {
		out = append(out, h.Kind)
	}
	return out
}

// A lone file row that is off disk is not a runtime. ComputeBookRuntime falls
// back to the book's own Duration for a one-row book, and that duration was
// recorded when the file existed.
func TestMergeCleanupALoneMissingFileRowIsNotARuntimeFromTheBooksDuration(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcSpec{title: "Hush", authors: []string{"Selina Kyle"},
		files: []mcFile{{secs: 30000, size: 30000 * 8000, missing: true}}})
	x.mk("loser", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000)})
	d := 30000
	_, err := x.st.ModifyBook(x.ids["target"], func(b *database.Book) error { b.Duration = &d; return nil })
	require.NoError(t, err)
	x.point("loser", "target")

	// Held, not an unheld unmerge on a runtime read from the book's own field.
	wantRow(t, x.plan(), "loser", mcKindUnmerge, mcSkipRuntimeIncomplete)
}

func TestMergeCleanupRepointTargetWithSeveralUnreliableFactsListsEveryHold(t *testing.T) {
	x := newMCFixture(t)
	// The heir has a placeholder title and a credit naming an author row that
	// does not exist: two reasons its facts are not evidence.
	x.mk("heir", mcSpec{title: "Unknown Title", authors: []string{"Selina Kyle"}, dangling: 1, files: one(30000), group: "g-u", primary: mcTrue()})
	x.mk("dead", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000), group: "g-u", primary: mcFalse(), trashed: true})
	x.mk("loser", mcHushSpec(30000))
	x.point("loser", "dead")
	r := wantRow(t, x.plan(), "loser", mcKindRepoint, mcSkipDanglingAuthor)
	kinds := mcHoldKinds(r.Holds)
	require.Contains(t, kinds, mcSkipDanglingAuthor)
	require.Contains(t, kinds, mcSkipPlaceholder, "the second reason is listed too: %+v", r.Holds)
}

func TestMergeCleanupBrokenSyncRedirectDoesNotHoldARowThatProposesNothing(t *testing.T) {
	x := newMCFixture(t)
	x.mk("ok-t", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(35000)})
	x.mk("ok-l", mcSpec{title: "Year One", authors: []string{"Bruce Wayne"}, files: one(36000)})
	x.point("ok-l", "ok-t")
	_, err := x.st.MintOrGetSyncID(x.ids["ok-l"])
	require.NoError(t, err)
	x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcBrokenSyncStore{Store: x.st, real: x.st}, root: x.root, vetoes: x.vetoes}})
	x.f.vgProbe = func(context.Context, string) (int, error) { return 1, nil }
	r := wantRow(t, x.plan(), "ok-l", mcClassNoVerdict, mcSkipMergeLooksCorrect)
	require.Contains(t, r.Flags, mcFlagSyncBroken, "the census still counts it")
	require.Equal(t, []string{mcSkipMergeLooksCorrect}, mcHoldKinds(r.Holds), "a row that proposes nothing is not held a second time")
}

// An error row's read failure is usually a file or a chain, not the sync
// identity: its sync flags are still read, so the census split adds up.
func TestMergeCleanupAnErrorRowStillCarriesItsSyncFlags(t *testing.T) {
	t.Run("a redirect", func(t *testing.T) {
		x := newMCFixture(t)
		x.wrongWork("bad", "bad-t")
		_, err := x.st.MintOrGetSyncID(x.ids["bad"])
		require.NoError(t, err)
		require.NoError(t, x.st.RecordSyncMerge(x.ids["bad"], x.ids["bad-t"]))
		x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcFailFilesStore{Store: x.st, fail: map[string]bool{x.ids["bad"]: true}}, root: x.root, vetoes: x.vetoes}})
		x.f.vgProbe = func(context.Context, string) (int, error) { return 1, nil }
		r := wantRow(t, x.plan(), "bad", mcClassError, mcSkipError)
		require.Contains(t, r.Flags, mcFlagSyncRedirect)
		require.NotContains(t, r.Flags, mcFlagSyncBroken)
	})
	t.Run("a broken chain", func(t *testing.T) {
		x := newMCFixture(t)
		x.wrongWork("bad", "bad-t")
		_, err := x.st.MintOrGetSyncID(x.ids["bad"])
		require.NoError(t, err)
		x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcFailFilesStore{Store: mcBrokenSyncStore{Store: x.st, real: x.st}, fail: map[string]bool{x.ids["bad"]: true}}, root: x.root, vetoes: x.vetoes}})
		x.f.vgProbe = func(context.Context, string) (int, error) { return 1, nil }
		r := wantRow(t, x.plan(), "bad", mcClassError, mcSkipError)
		require.Contains(t, r.Flags, mcFlagSyncBroken)
		require.NotContains(t, r.Flags, mcFlagSyncRedirect)
	})
}

// The fingerprint is what Replan compares to see that nothing moved, so a fact
// the verdict read has to be in it.
func TestMergeCleanupReplanSeesAHashAnotherBookNowShares(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	files, err := x.st.GetBookFiles(x.ids["loser"])
	require.NoError(t, err)
	f := files[0]
	f.FileHash = "shared-hash"
	require.NoError(t, x.st.UpdateBookFile(f.ID, &f))
	planned := x.plan()["loser"]

	// Another book now holds the same hash: the loser's file is no longer
	// unique, which HASH_EQ weighs. Nothing the row displays has moved.
	x.mk("other", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: []mcFile{{secs: 100, size: 800000, hash: "shared-hash"}}})
	r, err := x.f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.NotEqual(t, planned.Fingerprint, r.Fingerprint)
}

func TestMergeCleanupReplanSeesAnOriginalHashOnAMultiFileBook(t *testing.T) {
	x := newMCFixture(t)
	x.mk("target", mcHushSpec(30000))
	x.mk("loser", mcSpec{title: "The Long Halloween", authors: []string{"Bruce Wayne"},
		files: []mcFile{{secs: 20000, size: 20000 * 8000}, {secs: 20000, size: 20000 * 8000}}})
	x.point("loser", "target")
	planned := x.plan()["loser"]

	files, err := x.st.GetBookFiles(x.ids["loser"])
	require.NoError(t, err)
	f := files[0]
	f.OriginalFileHash = "an-original-hash"
	require.NoError(t, x.st.UpdateBookFile(f.ID, &f))
	r, err := x.f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.NotEqual(t, planned.Fingerprint, r.Fingerprint, "a multi-file book's side signature has no single hash: the file rows carry it")
}
