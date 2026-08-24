// file: internal/database/series_scan_truncation_test.go
// version: 1.0.0
// guid: 6c1f9a72-3d84-4b05-9e27-8a5d61b3f9c4
// last-edited: 2026-08-24

package database

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"
	"github.com/stretchr/testify/require"
)

// Fault injection for the two delete-guard scans, covering the ONE failure mode
// the rest of the suite cannot reach.
//
// Both getBooksBySeriesIDFull and getAllSeriesBookRefCountsPebble end with an
// iter.Error() check. Every other assertion about them survives that check
// being deleted, because a Pebble iterator over an intact database never sets
// an error -- the loop exits on end-of-range either way. A mutation run on
// 2026-08-24 confirmed it: removing iter.Error() from getBooksBySeriesIDFull
// left the entire package green (mutant M8, the only survivor of eight).
//
// It was written up as "cannot be triggered without fault-injection machinery
// this code does not have." That was WRONG, and this file is the correction:
// newPebbleStore already takes a vfs.FS -- the seam that NewPebbleStoreInMemory
// uses for speed -- and Pebble ships vfs/errorfs. The machinery was already
// here, one argument away. A survivor should be re-examined for a missing seam
// before it is written off as unkillable.
//
// Why the check matters: a truncated scan and a complete one are
// indistinguishable without it. Both leave the loop with whatever rows were
// read so far, and returning those with a nil error tells a caller that deletes
// on the strength of the answer that it saw everything.

// newInjector builds a Toggle that fails file reads and opens once switched
// on, sharing one Injector between the seed store and the reopened one so a
// single On()/Off() controls both.
func newInjector() *errorfs.Toggle {
	return &errorfs.Toggle{Injector: errorfs.InjectorFunc(func(op errorfs.Op) error {
		switch op.Kind {
		case errorfs.OpFileRead, errorfs.OpFileReadAt, errorfs.OpOpen:
			return errorfs.ErrInjected
		}
		return nil
	})}
}

// truncatingStoreWithFixture seeds a series' worth of books through a normal
// store, flushes them to sstables, closes it, then opens a SECOND handle on
// the same in-memory FS with its block cache disabled.
//
// The reopen-with-no-cache is load-bearing, not cosmetic, on two counts:
//   - Pebble serves reads from its block cache after a flush, and the cache
//     is a fresh instance per pebble.Open call -- so a scan run against the
//     same *pebble.DB that did the writing never touches the filesystem
//     again, and no amount of file-read injection can reach it.
//   - Even a fresh Open still isn't enough on its own: this package's memdb
//     warmup does a full scan of the store on open, which populates a
//     default-sized cache before the test ever gets to flip the injector on.
//     A zero-size pebble.Cache defeats that too -- every read, warmup's
//     included, goes to the (wrapped) filesystem.
//
// The second handle is built directly with pebble.Open rather than through
// newPebbleStore, because newPebbleStore has no seam for a cache override and
// adding one would be production-code scope creep for a test's sake. Only
// the db field is populated: both target methods (the Pebble arm of the
// merge getter, and the Pebble-only ref counter) read nothing else off
// *PebbleStore, and UseMemDB stays false so neither one touches memdb.
func truncatingStoreWithFixture(t *testing.T, seriesID int) (*PebbleStore, *errorfs.Toggle) {
	t.Helper()

	toggle := newInjector()
	fs := errorfs.Wrap(vfs.NewMem(), toggle)

	seed, err := newPebbleStore("/truncation-test", fs)
	require.NoError(t, err)
	seed.WaitForWarmup()

	yes := true
	for i := 0; i < 12; i++ {
		seq := i
		_, err := seed.CreateBook(&Book{
			Title:            "trunc-" + string(rune('a'+i)),
			FilePath:         "/trunc/" + string(rune('a'+i)),
			SeriesID:         &seriesID,
			SeriesSequence:   &seq,
			IsPrimaryVersion: &yes,
		})
		require.NoError(t, err)
	}
	require.NoError(t, seed.db.Flush(), "rows must reach sstables before the reopen")
	require.NoError(t, seed.Close())

	noCache := pebble.NewCache(0)
	defer noCache.Unref()
	db, err := pebble.Open("/truncation-test", &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest,
		FS:                 fs,
		Cache:              noCache,
	})
	require.NoError(t, err)
	p := &PebbleStore{db: db}
	t.Cleanup(func() {
		toggle.Off() // Close() reads; leaving injection on breaks cleanup.
		_ = db.Close()
	})
	return p, toggle
}

// TestGetBooksBySeriesIDAllVersions_RefusesOnATruncatedScan kills mutant M8 on
// the merge getter. This is the scan a series merge repoints books from before
// DeleteSeries runs, so a silently short answer strands every book it missed.
func TestGetBooksBySeriesIDAllVersions_RefusesOnATruncatedScan(t *testing.T) {
	// Two independent handles, not one reused after toggling the fault off.
	// A DB that has just surfaced a real read error is not something this
	// test should trust for a follow-up "recovery" read either -- and under
	// the full package suite it measurably doesn't: Pebble kept reporting the
	// same backing file as errored on a SECOND read after Off(), evidently
	// caching failure state on that file beyond the single injected call.
	// That's Pebble's own retry policy, not a property of the code under
	// test, so the control below proves the point a different way: a
	// SEPARATE store, seeded identically, that never saw the fault at all.
	failing, toggle := truncatingStoreWithFixture(t, 7001)
	const seriesID = 7001

	// Force the Pebble arm. With a healthy memdb the getter never reaches the
	// scan under test.
	failing.UseMemDB = false

	// Injection goes on BEFORE the first read of this fresh store. A read
	// beforehand would populate the block cache and the assertion below
	// would pass from cache, having exercised nothing.
	toggle.On()
	defer toggle.Off() // Close() reads; must not still be injecting.

	_, err := failing.GetBooksBySeriesIDAllVersions(seriesID)
	require.Error(t, err,
		"a scan cut short by an I/O error must not return its partial list with a nil error; "+
			"the caller repoints books off this list and then deletes the series")
	require.Contains(t, err.Error(), "truncated",
		"the error must name truncation, so an operator can tell it from a decode failure")

	// Control, not merely a second failure mode: proves the error above came
	// from the injected fault and not from a structurally broken scan.
	healthy, _ := truncatingStoreWithFixture(t, 7001)
	healthy.UseMemDB = false
	after, err := healthy.GetBooksBySeriesIDAllVersions(seriesID)
	require.NoError(t, err)
	require.Len(t, after, 12, "the scan must succeed against an unfaulted store")
}

// TestGetAllSeriesBookRefCounts_RefusesOnATruncatedScan is the same assertion
// for the unfiltered reference counter. Undercounting is worse here than a
// short list: a series absent from the map is the affirmative "referenced by
// nothing, safe to delete" signal three callers act on.
func TestGetAllSeriesBookRefCounts_RefusesOnATruncatedScan(t *testing.T) {
	// See the sibling test above for why this uses two independent handles
	// rather than toggling the fault off on the one that already saw it.
	failing, toggle := truncatingStoreWithFixture(t, 7002)
	const seriesID = 7002

	toggle.On()
	defer toggle.Off() // Close() reads; must not still be injecting.

	_, err := failing.getAllSeriesBookRefCountsPebble()
	require.Error(t, err,
		"a truncated count that returns nil error reports 'referenced by nothing' for "+
			"every series whose rows were never reached")
	require.Contains(t, err.Error(), "truncated")

	healthy, _ := truncatingStoreWithFixture(t, 7002)
	after, err := healthy.getAllSeriesBookRefCountsPebble()
	require.NoError(t, err)
	require.Equal(t, 12, after[seriesID], "the count must succeed against an unfaulted store")
}
