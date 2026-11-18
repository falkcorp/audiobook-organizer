// file: internal/plugins/maintenance/merge_cleanup_census_test.go
// version: 1.0.0
// guid: 6c9d0e41-27b8-4a53-b1f6-e84a3d09c5f7
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The census splits rows by where the merge pointer came from and by whether a
// sync redirect points away. The splits only mean something if every row, a
// retired shell and an error row included, is in them. Names are invented.

var mcOriginFlags = []string{mcFlagOriginMatch4, mcFlagOriginPartial, mcFlagOriginOther, mcFlagOriginUnknown}

// mcOriginFlagsOf are the origin flags a row carries.
func mcOriginFlagsOf(r repairs.Row) []string {
	var out []string
	for _, f := range r.Flags {
		if slices.Contains(mcOriginFlags, f) {
			out = append(out, f)
		}
	}
	return out
}

func TestMergeCleanupOriginFlagsAddUpToEveryRow(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("w", "w-t")    // a MATCH-4-shaped pointer (explicit false primary)
	x.fragment("f", "f-t", "") // the same
	x.mk("raw-t", mcHushSpec(30000))
	x.mk("raw", mcHushSpec(30000))
	x.pointRaw("raw", "raw-t") // a pointer with the primary flag untouched: another writer
	// A retired shell.
	x.wrongWork("shell", "shell-t")
	x.mk("holder", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(1000)})
	retireRowOnto(t, x.st, x.ids["shell"], x.ids["holder"], "rs", filepath.Join(x.root, "retired", "s.m4b"))
	// An error row whose book reads fine: the origin is still classified.
	x.wrongWork("bad", "bad-t")
	// An error row whose pointed-at book cannot be read: its origin is unknown.
	x.wrongWork("blind", "blind-t")

	x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcBlindStore{
		Store:    mcFailFilesStore{Store: x.st, fail: map[string]bool{x.ids["bad"]: true, x.ids["blind"]: true}},
		failBook: x.ids["blind-t"], failFor: x.ids["blind"]}, root: x.root, vetoes: x.vetoes}})
	x.f.vgProbe = func(context.Context, string) (int, error) { return 1, nil }

	rows := x.plan()
	require.Equal(t, mcClassError, rows["bad"].Class)
	require.Equal(t, mcClassError, rows["blind"].Class)
	require.Equal(t, mcClassNoVerdict, rows["shell"].Class)
	require.Equal(t, mcSkipRetiredShell, rows["shell"].Skipped)

	total := map[string]int{}
	for name, r := range rows {
		got := mcOriginFlagsOf(r)
		require.Len(t, got, 1, "row %s must carry exactly one origin flag (got %v)", name, got)
		total[got[0]]++
	}
	sum := 0
	for _, n := range total {
		sum += n
	}
	require.Equal(t, len(rows), sum, "the origin split adds up to the rows: %v", total)
	require.Contains(t, rows["raw"].Flags, mcFlagOriginOther)
	require.Contains(t, rows["shell"].Flags, mcFlagOriginPartial, "a retired shell is classified like any pointer")
	require.Contains(t, rows["bad"].Flags, mcFlagOriginPartial, "an error row whose book still reads keeps its origin")
	require.Contains(t, rows["blind"].Flags, mcFlagOriginUnknown, "an unreadable origin is its own bucket, not a guess")
	require.NotContains(t, rows["blind"].Flags, mcFlagOriginOther)
}

// mcBlindStore fails the read of one book, but only for a caller that is
// classifying another: the chain walk of "failFor" cannot read failBook.
type mcBlindStore struct {
	database.Store
	failBook, failFor string
}

func (s mcBlindStore) Unwrap() database.Store { return s.Store }
func (s mcBlindStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.failBook {
		return nil, errors.New("synthetic read failure of the pointed-at book")
	}
	return s.Store.GetBookByID(id)
}

// A retired shell and a sync redirect: the redirect flag is on the shell too.
func TestMergeCleanupRetiredShellCarriesTheSyncFlag(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("shell", "shell-t")
	x.mk("holder", mcSpec{title: "Knightfall", authors: []string{"Bruce Wayne"}, files: one(1000)})
	retireRowOnto(t, x.st, x.ids["shell"], x.ids["holder"], "rs", filepath.Join(x.root, "retired", "s.m4b"))
	_, err := x.st.MintOrGetSyncID(x.ids["shell"])
	require.NoError(t, err)
	require.NoError(t, x.st.RecordSyncMerge(x.ids["shell"], x.ids["shell-t"]))
	r := wantRow(t, x.plan(), "shell", mcClassNoVerdict, mcSkipRetiredShell)
	require.Contains(t, r.Flags, mcFlagSyncRedirect)
}

// brokenSyncStore resolves every sync item to a broken redirect chain.
type mcBrokenSyncStore struct {
	database.Store
	real database.SyncIdentityStore
}

func (s mcBrokenSyncStore) Unwrap() database.Store { return s.Store }
func (s mcBrokenSyncStore) MintOrGetSyncID(id string) (string, error) {
	return s.real.MintOrGetSyncID(id)
}
func (s mcBrokenSyncStore) GetSyncIDForBook(id string) (string, bool, error) {
	return s.real.GetSyncIDForBook(id)
}
func (s mcBrokenSyncStore) ResolveSyncItem(string) (*database.SyncItem, error) {
	return nil, fmt.Errorf("starting at some item: %w", database.ErrSyncRedirectChainBroken)
}
func (s mcBrokenSyncStore) RepointSyncItem(a, b string) error { return s.real.RepointSyncItem(a, b) }
func (s mcBrokenSyncStore) RecordSyncMerge(a, b string) error { return s.real.RecordSyncMerge(a, b) }
func (s mcBrokenSyncStore) ListSyncAliases(id string) ([]string, error) {
	return s.real.ListSyncAliases(id)
}

func TestMergeCleanupBrokenSyncRedirectIsAHoldNotAnErrorRow(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	x.wrongWork("calm", "calm-target")
	_, err := x.st.MintOrGetSyncID(x.ids["loser"])
	require.NoError(t, err)
	x.f = newMergeCleanupFixer(&Plugin{deps: fakeDeps{store: mcBrokenSyncStore{Store: x.st, real: x.st}, root: x.root, vetoes: x.vetoes}})
	x.f.vgProbe = func(context.Context, string) (int, error) { return 1, nil }
	rows := x.plan()
	r := wantRow(t, rows, "loser", mcKindUnmerge, mcSkipSyncBroken)
	require.Contains(t, r.Flags, mcFlagSyncBroken)
	require.Contains(t, r.SkipReason, "cannot be followed")
	require.Equal(t, "broken", r.Current["sync_redirect"])
	require.NotContains(t, r.Flags, mcFlagSyncRedirect, "a chain that cannot be followed is not a known redirect")
	wantRow(t, rows, "calm", mcKindUnmerge, "") // no sync identity: nothing to resolve
}

func TestMergeCleanupCycleHeadRanksOnKnownSeconds(t *testing.T) {
	x := newMCFixture(t)
	// "partial" has 50000 s measured and one file never measured: a lower
	// bound, not a runtime. "whole" is 30000 s measured end to end.
	x.mk("partial", mcSpec{title: "Hush", authors: []string{"Selina Kyle"},
		files: []mcFile{{secs: 50000, size: 50000 * 8000}, {secs: -1, size: 1000}}})
	x.mk("whole", mcSpec{title: "Hush", authors: []string{"Selina Kyle"}, files: one(30000)})
	x.point("partial", "whole")
	x.point("whole", "partial")
	rows := x.plan()
	require.Equal(t, x.ids["whole"], rows["whole"].Current["cycle_head"], "a lower bound does not outrank a measured runtime")
	wantRow(t, rows, "whole", mcKindUnmerge, "")
	wantRow(t, rows, "partial", mcKindUnmerge, mcSkipCycleNotHead)
}

func TestMergeCleanupSummaryOnlySaysWouldPropose(t *testing.T) {
	x := newMCFixture(t)
	x.wrongWork("loser", "target")
	rep := &fakeReporter{}
	_, err := x.f.Plan(context.Background(), nil, rep)
	require.NoError(t, err)
	var line string
	for _, l := range rep.logs {
		if strings.Contains(l, "merge-cleanup preview") {
			line = l
		}
	}
	require.NotEmpty(t, line)
	require.Contains(t, line, "1 would propose a repair")
	require.NotContains(t, line, "would be written", "the preview writes nothing")
}
