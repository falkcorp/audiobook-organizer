// file: internal/database/signal_coverage_fpwin_test.go
// version: 1.1.0
// guid: 53baee6c-5cb2-4fa1-bbbc-5b0836472ef0
// last-edited: 2026-09-19

package database

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// windowCoverageCriteria matches fpwinFixture's pipeline, window set and tools.
var windowCoverageCriteria = WindowCoverageCriteria{
	Pipeline:      "ffpcm-s16le-11025-mono/v1",
	WindowSet:     "ws1",
	FpcalcVersion: "1.6.0",
	FFmpegVersion: "8.0.1",
}

// windowCoverageFixture is a store whose expected window census is known row
// by row. Book A has a missing row, so its present files are tier T1; book B
// has none, so its files are T2.
type windowCoverageFixture struct {
	store *PebbleStore
	// file IDs by role
	current, stalePipeline, tombOnly, none, tombAndStale, headOnly, missingWithWindow string
	t2Current, t2StaleTools, t2None                                                   string
}

func buildWindowCoverageFixture(t *testing.T) *windowCoverageFixture {
	t.Helper()
	s := setupTestPebbleStore(t)
	fx := &windowCoverageFixture{store: s}

	bookA, err := s.CreateBook(&Book{Title: "Window cov A", FilePath: "/test/wcov/a"})
	require.NoError(t, err)
	bookB, err := s.CreateBook(&Book{Title: "Window cov B", FilePath: "/test/wcov/b"})
	require.NoError(t, err)

	mk := func(bookID, name string, missing bool) string {
		f := &BookFile{BookID: bookID, FilePath: "/test/wcov/" + name + ".m4b", Format: "m4b", Missing: missing}
		require.NoError(t, s.CreateBookFile(f))
		return f.ID
	}
	fx.current = mk(bookA.ID, "a-current", false)
	fx.stalePipeline = mk(bookA.ID, "a-stale", false)
	fx.tombOnly = mk(bookA.ID, "a-tomb", false)
	fx.none = mk(bookA.ID, "a-none", false)
	fx.tombAndStale = mk(bookA.ID, "a-tomb-stale", false)
	fx.headOnly = mk(bookA.ID, "a-head", false)
	fx.missingWithWindow = mk(bookA.ID, "a-missing", true)
	fx.t2Current = mk(bookB.ID, "b-current", false)
	fx.t2StaleTools = mk(bookB.ID, "b-stale-tools", false)
	fx.t2None = mk(bookB.ID, "b-none", false)

	put := func(fileID string, kind FingerprintWindowKind, slot int, edit func(*FingerprintWindow)) {
		w := fpwinFixture(FileWindowRef(fileID), kind, slot, 1)
		if edit != nil {
			edit(w)
		}
		require.NoError(t, s.PutFingerprintWindow(w))
	}
	tomb := func(ref FingerprintWindowRef) {
		require.NoError(t, s.db.Set(fpwinFailKey(ref), []byte(`{"reason":"decode"}`), nil))
	}
	oldPipeline := func(w *FingerprintWindow) { w.Pipeline = "ffpcm-s16le-11025-mono/v0" }

	// current: all three ws1 slots, current pipeline and tools.
	for _, slot := range []int{1000, 5000, 9000} {
		put(fx.current, WindowKindWindow, slot, nil)
	}
	// stale: a superseded pipeline on every row.
	put(fx.stalePipeline, WindowKindWindow, 5000, oldPipeline)
	// one stale row next to a current one still counts as current.
	put(fx.current, WindowKindWhole, 0, oldPipeline)
	tomb(FileWindowRef(fx.tombOnly))
	tomb(FileWindowRef(fx.tombAndStale))
	put(fx.tombAndStale, WindowKindWindow, 1000, func(w *FingerprintWindow) { w.WindowSet = "ws0" })
	// A stored head row is only the intro again: not a window.
	put(fx.headOnly, WindowKindHead, 0, nil)
	put(fx.missingWithWindow, WindowKindWindow, 5000, nil)
	put(fx.t2Current, WindowKindWhole, 0, nil)
	put(fx.t2StaleTools, WindowKindWindow, 5000, func(w *FingerprintWindow) { w.FpcalcVersion = "1.5.1" })

	// Refs with no present row: an untracked candidate, an orphan f: window
	// (planted under the store, the way a crash between cascade stages would
	// leave one) and an orphan tombstone.
	pw := fpwinFixture(PathWindowRef("Author/Book/01.m4b"), WindowKindWindow, 5000, 2)
	require.NoError(t, s.PutFingerprintWindow(pw))
	orphan := fpwinFixture(FileWindowRef("gone-file"), WindowKindWindow, 5000, 3)
	raw, err := json.Marshal(orphan)
	require.NoError(t, err)
	require.NoError(t, s.db.Set(fpwinKey(orphan), raw, nil))
	tomb(FileWindowRef("gone-too"))
	tomb(PathWindowRef("Author/Other/01.m4b"))
	return fx
}

func TestFingerprintWindowCoverage_DeepCensus(t *testing.T) {
	fx := buildWindowCoverageFixture(t)
	cov, err := fx.store.GetFingerprintWindowCoverage(context.Background(), true, windowCoverageCriteria, 3)
	require.NoError(t, err)

	assert.True(t, cov.CurrencyEvaluated)
	assert.EqualValues(t, 9, cov.PresentFiles, "10 rows, one flagged missing")
	assert.EqualValues(t, 2, cov.WithCurrentWindows, "current + t2Current")
	assert.EqualValues(t, 3, cov.WithStaleWindowsOnly, "stalePipeline, tombAndStale (stale outranks tombstone), t2StaleTools")
	assert.EqualValues(t, 5, cov.WithWindows)
	assert.EqualValues(t, 1, cov.TombstonedNoWindows, "tombOnly")
	assert.EqualValues(t, 3, cov.NoWindows, "none, headOnly, t2None")
	assert.EqualValues(t, 2, cov.WithTombstoneAny)
	assert.EqualValues(t, 1, cov.WithStoredHeadRow)
	assert.Equal(t, cov.PresentFiles,
		cov.WithCurrentWindows+cov.WithStaleWindowsOnly+cov.TombstonedNoWindows+cov.NoWindows,
		"the four buckets must partition the present files")

	t1 := cov.Tiers[WindowTierT1]
	t2 := cov.Tiers[WindowTierT2]
	assert.EqualValues(t, 6, t1.PresentFiles)
	assert.EqualValues(t, 1, t1.WithCurrentWindows)
	assert.EqualValues(t, 3, t2.PresentFiles)
	assert.EqualValues(t, 1, t2.WithCurrentWindows)
	assert.EqualValues(t, 1, t2.WithStaleWindowsOnly)
	assert.EqualValues(t, 1, t2.NoWindows)

	assert.EqualValues(t, 1, cov.MissingRowsWithWindows)
	assert.EqualValues(t, 1, cov.OrphanFileRefs, "gone-file has windows and no row")
	assert.EqualValues(t, 1, cov.OrphanTombstones, "gone-too")
	assert.EqualValues(t, 1, cov.PathRefsWithWindows)
	assert.EqualValues(t, 1, cov.PathRefTombstones)
	assert.EqualValues(t, 12, cov.StoredWindowRows)
	assert.EqualValues(t, map[string]int64{"head": 1, "window": 9, "whole": 2}, cov.StoredRowsByKind)
	assert.EqualValues(t, 10, cov.WindowRowsByTools["fpcalc 1.6.0 / ffmpeg 8.0.1"])
	assert.EqualValues(t, 1, cov.WindowRowsByTools["fpcalc 1.5.1 / ffmpeg 8.0.1"])
	assert.NotContains(t, cov.Unavailable, "legacy_print_era", "the era split is in files.head_print_era")
	assert.Contains(t, cov.Unavailable, "tier_t0")
}

// The fast path reads keys only: it can say which files hold windows but not
// whether they are current, and must say so instead of reporting zero current.
func TestFingerprintWindowCoverage_FastPathIsKeysOnly(t *testing.T) {
	fx := buildWindowCoverageFixture(t)
	fx.store.WaitForWarmup()
	require.True(t, fx.store.IsMemReady())
	cov, err := fx.store.GetFingerprintWindowCoverage(context.Background(), false, windowCoverageCriteria, 2)
	require.NoError(t, err)
	assert.False(t, cov.CurrencyEvaluated)
	assert.EqualValues(t, 9, cov.PresentFiles)
	assert.EqualValues(t, 5, cov.WithWindows)
	assert.Zero(t, cov.WithCurrentWindows)
	assert.EqualValues(t, 1, cov.TombstonedNoWindows)
	assert.EqualValues(t, 3, cov.NoWindows)
	assert.Contains(t, cov.Unavailable, "with_current_windows")
	assert.Nil(t, cov.WindowRowsByTools)
	assert.Equal(t, cov.PresentFiles, cov.WithWindows+cov.TombstonedNoWindows+cov.NoWindows,
		"on the fast path WithWindows stands in for the two currency buckets")

	// Without memdb the fast path refuses rather than scanning Pebble.
	fx.store.UseMemDB = false
	_, err = fx.store.GetFingerprintWindowCoverage(context.Background(), false, windowCoverageCriteria, 2)
	require.ErrorIs(t, err, ErrMemDBNotReady)
	fx.store.UseMemDB = true
}

// With no tool versions known, currency falls back to pipeline + window set
// and says so, rather than calling every window stale.
func TestFingerprintWindowCoverage_UnknownToolVersionsAreNotStale(t *testing.T) {
	fx := buildWindowCoverageFixture(t)
	crit := windowCoverageCriteria
	crit.FpcalcVersion, crit.FFmpegVersion = "", ""
	cov, err := fx.store.GetFingerprintWindowCoverage(context.Background(), true, crit, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 3, cov.WithCurrentWindows, "t2StaleTools is current on pipeline alone")
	assert.Contains(t, cov.Unavailable, "tool_version_currency")
}

// TestFingerprintWindowCoverage_CensusEqualsBruteForce classifies every row by
// reading its windows one at a time through the public store API and compares
// that to the streamed census, at several pool widths.
func TestFingerprintWindowCoverage_CensusEqualsBruteForce(t *testing.T) {
	fx := buildWindowCoverageFixture(t)
	s := fx.store
	files, err := s.GetAllBookFilesCore()
	require.NoError(t, err)

	var want WindowCoverageBuckets
	for _, f := range files {
		if f.Missing {
			continue
		}
		ws, err := s.GetFingerprintWindows(FileWindowRef(f.ID))
		require.NoError(t, err)
		var hasAny, cur bool
		for _, w := range ws {
			require.False(t, w.Virtual, "stored windows never include the virtual legacy head")
			if w.Kind == WindowKindHead {
				continue
			}
			hasAny = true
			if w.Pipeline == windowCoverageCriteria.Pipeline && w.WindowSet == windowCoverageCriteria.WindowSet &&
				w.FpcalcVersion == windowCoverageCriteria.FpcalcVersion && w.FFmpegVersion == windowCoverageCriteria.FFmpegVersion {
				cur = true
			}
		}
		_, closer, gerr := s.db.Get(fpwinFailKey(FileWindowRef(f.ID)))
		tomb := gerr == nil
		if tomb {
			closer.Close()
		}
		want.PresentFiles++
		switch {
		case cur:
			want.WithCurrentWindows++
		case hasAny:
			want.WithStaleWindowsOnly++
		case tomb:
			want.TombstonedNoWindows++
		default:
			want.NoWindows++
		}
		if hasAny {
			want.WithWindows++
		}
	}
	require.EqualValues(t, 9, want.PresentFiles, "brute force must see the whole fixture")
	s.WaitForWarmup()
	require.True(t, s.IsMemReady(), "the fast arm would refuse instead of reading memdb")
	for _, workers := range []int{1, 2, 5, 16} {
		cov, err := s.GetFingerprintWindowCoverage(context.Background(), true, windowCoverageCriteria, workers)
		require.NoError(t, err)
		assert.Equal(t, "pebble", cov.Source, "deep reads rows from Pebble, like the deep book_file scan")
		assert.Equal(t, want, cov.WindowCoverageBuckets, "deep workers=%d", workers)

		fast, err := s.GetFingerprintWindowCoverage(context.Background(), false, windowCoverageCriteria, workers)
		require.NoError(t, err)
		assert.Equal(t, "memdb", fast.Source)
		fastWant := want
		fastWant.WithCurrentWindows, fastWant.WithStaleWindowsOnly = 0, 0
		assert.Equal(t, fastWant, fast.WindowCoverageBuckets, "fast workers=%d", workers)
	}
}

func TestFingerprintWindowCoverage_SharesTheDeepScanSlot(t *testing.T) {
	fx := buildWindowCoverageFixture(t)
	require.True(t, fx.store.TryAcquireDeepCoverageScan())
	_, err := fx.store.GetFingerprintWindowCoverage(context.Background(), true, windowCoverageCriteria, 2)
	require.ErrorIs(t, err, ErrDeepCoverageBusy)
	fx.store.ReleaseDeepCoverageScan()
	_, err = fx.store.GetFingerprintWindowCoverage(context.Background(), true, windowCoverageCriteria, 2)
	require.NoError(t, err)
	assert.False(t, fx.store.deepCoverageBusy.Load())
}
