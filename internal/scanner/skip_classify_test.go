// file: internal/scanner/skip_classify_test.go
// version: 1.2.0
// guid: 8f2a1c47-6b3d-4e59-9a0f-2d7c4b8e1a63
// last-edited: 2026-08-24

package scanner

import (
	"math"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// gateOff is the freshCutoff value that disables the rescan-age gate: the
// comparison is `mtime > cutoff`, so the largest possible cutoff can never fire.
// Tests that predate the gate use it to assert the mtime/size/NeedsRescan
// comparison on its own.
const gateOff = int64(math.MaxInt64)

// classifySkipFile is the instrumented skip decision. These tests pin the
// REASON, not just the boolean -- it was the missing reason that made an
// incremental scan unobservable.
func TestClassifySkipFile_Reasons(t *testing.T) {
	const path = "/lib/book.m4b"
	cache := map[string]database.ScanCacheEntry{
		path:             {Mtime: 100, Size: 200, NeedsRescan: false},
		"/lib/dirty.m4b": {Mtime: 100, Size: 200, NeedsRescan: true},
	}

	tests := []struct {
		name       string
		path       string
		mtime      int64
		size       int64
		cache      map[string]database.ScanCacheEntry
		wantSkip   bool
		wantReason skipReason
	}{
		{"unchanged skips", path, 100, 200, cache, true, reasonUnchanged},
		{"nil cache is cache-off", path, 100, 200, nil, false, reasonCacheOff},
		{"absent path is cache-miss", "/lib/new.m4b", 1, 2, cache, false, reasonCacheMiss},
		{"different mtime is changed", path, 999, 200, cache, false, reasonChanged},
		{"different size is changed", path, 100, 999, cache, false, reasonChanged},
		{"needs-rescan is dirty", "/lib/dirty.m4b", 100, 200, cache, false, reasonDirty},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := classifySkipFile(tc.path, tc.mtime, tc.size, tc.cache, gateOff)
			if skip != tc.wantSkip {
				t.Errorf("skip = %v, want %v", skip, tc.wantSkip)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %v, want %v", reason, tc.wantReason)
			}
		})
	}
}

// A dirty entry whose mtime ALSO changed must be attributed to the forced
// rescan, not to the mtime change: the user asking for a rescan is the reason a
// reader of the summary would care about, and reporting it as "changed" would
// make forced rescans invisible in the counts.
func TestClassifySkipFile_DirtyWinsOverChanged(t *testing.T) {
	cache := map[string]database.ScanCacheEntry{
		"/lib/both.m4b": {Mtime: 100, Size: 200, NeedsRescan: true},
	}
	skip, reason := classifySkipFile("/lib/both.m4b", 999, 888, cache, gateOff)
	if skip {
		t.Fatal("a dirty entry must never be skipped")
	}
	if reason != reasonDirty {
		t.Errorf("reason = %v, want reasonDirty (a forced rescan must not be reported as a plain mtime change)", reason)
	}
}

// ── rescan-age gate ────────────────────────────────────────────────────────

// The gate must apply to exactly one branch. These are the two "unless" clauses
// the gate was specified with, plus discovery: if any of them regressed, a new
// or explicitly-forced file would silently wait a full period.
func TestClassifySkipFile_GateAppliesOnlyToChanged(t *testing.T) {
	const cutoff = int64(1000)
	// mtime 5000 is well inside the window, so anything the gate touches would
	// be deferred.
	const freshMtime = int64(5000)

	cache := map[string]database.ScanCacheEntry{
		"/lib/known.m4b": {Mtime: 100, Size: 200, NeedsRescan: false},
		"/lib/dirty.m4b": {Mtime: 100, Size: 200, NeedsRescan: true},
	}

	t.Run("changed and fresh is deferred", func(t *testing.T) {
		skip, reason := classifySkipFile("/lib/known.m4b", freshMtime, 200, cache, cutoff)
		if !skip || reason != reasonTooFresh {
			t.Errorf("skip=%v reason=%v, want true/reasonTooFresh", skip, reason)
		}
	})

	t.Run("new file is read immediately despite being fresh", func(t *testing.T) {
		skip, reason := classifySkipFile("/lib/brand-new.m4b", freshMtime, 200, cache, cutoff)
		if skip || reason != reasonCacheMiss {
			t.Errorf("skip=%v reason=%v, want false/reasonCacheMiss (the gate must never delay discovery)", skip, reason)
		}
	})

	t.Run("forced per-file rescan bypasses the gate", func(t *testing.T) {
		skip, reason := classifySkipFile("/lib/dirty.m4b", freshMtime, 999, cache, cutoff)
		if skip || reason != reasonDirty {
			t.Errorf("skip=%v reason=%v, want false/reasonDirty (NeedsRescan must outrank the gate)", skip, reason)
		}
	})

	t.Run("force_update run never consults the gate", func(t *testing.T) {
		skip, reason := classifySkipFile("/lib/known.m4b", freshMtime, 200, nil, cutoff)
		if skip || reason != reasonCacheOff {
			t.Errorf("skip=%v reason=%v, want false/reasonCacheOff (a full sweep must re-read everything)", skip, reason)
		}
	})

	t.Run("unchanged is still unchanged, not too-fresh", func(t *testing.T) {
		// A fresh mtime that MATCHES the cache is not a change at all, so the
		// gate must not claim it: attributing it to too-fresh would report
		// deferred work that does not exist.
		fresh := map[string]database.ScanCacheEntry{
			"/lib/quiet.m4b": {Mtime: freshMtime, Size: 200, NeedsRescan: false},
		}
		skip, reason := classifySkipFile("/lib/quiet.m4b", freshMtime, 200, fresh, cutoff)
		if !skip || reason != reasonUnchanged {
			t.Errorf("skip=%v reason=%v, want true/reasonUnchanged", skip, reason)
		}
	})
}

// The boundary. `mtime > cutoff` defers; a file whose mtime is EXACTLY the
// cutoff has aged the full period and must be re-read. This is the case that
// separates `>` from `>=` -- nothing else in the suite does.
func TestClassifySkipFile_GateBoundaryIsExclusive(t *testing.T) {
	const cutoff = int64(1000)
	cache := map[string]database.ScanCacheEntry{
		"/lib/b.m4b": {Mtime: 100, Size: 200},
	}

	t.Run("exactly at the cutoff is re-read", func(t *testing.T) {
		skip, reason := classifySkipFile("/lib/b.m4b", cutoff, 200, cache, cutoff)
		if skip || reason != reasonChanged {
			t.Errorf("skip=%v reason=%v, want false/reasonChanged at mtime == cutoff", skip, reason)
		}
	})

	t.Run("one second newer is deferred", func(t *testing.T) {
		skip, reason := classifySkipFile("/lib/b.m4b", cutoff+1, 200, cache, cutoff)
		if !skip || reason != reasonTooFresh {
			t.Errorf("skip=%v reason=%v, want true/reasonTooFresh at mtime == cutoff+1", skip, reason)
		}
	})

	t.Run("one second older is re-read", func(t *testing.T) {
		skip, reason := classifySkipFile("/lib/b.m4b", cutoff-1, 200, cache, cutoff)
		if skip || reason != reasonChanged {
			t.Errorf("skip=%v reason=%v, want false/reasonChanged at mtime == cutoff-1", skip, reason)
		}
	})
}

// rescanFreshCutoff's disabled case returns MaxInt64, not 0. Returning 0 would
// invert the gate and defer every file with a post-epoch mtime -- i.e. the
// entire library -- while still reading as "disabled" at the call site.
func TestRescanFreshCutoff(t *testing.T) {
	now := time.Unix(1_000_000, 0)

	if got := rescanFreshCutoff(now, 144); got != 1_000_000-144*3600 {
		t.Errorf("cutoff = %d, want %d", got, 1_000_000-144*3600)
	}
	for _, disabled := range []int{0, -1, -144} {
		if got := rescanFreshCutoff(now, disabled); got != math.MaxInt64 {
			t.Errorf("rescanFreshCutoff(_, %d) = %d, want MaxInt64 (gate off)", disabled, got)
		}
	}

	// The property that matters: with the gate off, a file modified in the
	// future is still not deferred.
	cache := map[string]database.ScanCacheEntry{"/x.m4b": {Mtime: 1, Size: 1}}
	skip, reason := classifySkipFile("/x.m4b", math.MaxInt64-1, 2, cache, rescanFreshCutoff(now, 0))
	if skip || reason != reasonChanged {
		t.Errorf("skip=%v reason=%v, want false/reasonChanged when the gate is disabled", skip, reason)
	}
}

// recordSkipDecision must route every reason to its own counter. A switch that
// silently drops a reason would under-report that population without failing
// anything else.
func TestRecordSkipDecision_RoutesEachReason(t *testing.T) {
	before := snapshotSkipCounters()
	recordSkipDecision(reasonUnchanged)
	recordSkipDecision(reasonCacheOff)
	recordSkipDecision(reasonCacheMiss)
	recordSkipDecision(reasonChanged)
	recordSkipDecision(reasonDirty)
	recordSkipDecision(reasonTooFresh)
	after := snapshotSkipCounters()

	if d := after.unchanged - before.unchanged; d != 1 {
		t.Errorf("unchanged delta = %d, want 1", d)
	}
	if d := after.cacheOff - before.cacheOff; d != 1 {
		t.Errorf("cacheOff delta = %d, want 1", d)
	}
	if d := after.cacheMiss - before.cacheMiss; d != 1 {
		t.Errorf("cacheMiss delta = %d, want 1", d)
	}
	if d := after.changed - before.changed; d != 1 {
		t.Errorf("changed delta = %d, want 1", d)
	}
	if d := after.dirty - before.dirty; d != 1 {
		t.Errorf("dirty delta = %d, want 1", d)
	}
	if d := after.tooFresh - before.tooFresh; d != 1 {
		t.Errorf("tooFresh delta = %d, want 1", d)
	}
}

type skipCounterSnapshot struct {
	unchanged, cacheOff, cacheMiss, changed, dirty, tooFresh int64
}

func snapshotSkipCounters() skipCounterSnapshot {
	return skipCounterSnapshot{
		unchanged: skipUnchangedCount.Load(),
		cacheOff:  readCacheOffCount.Load(),
		cacheMiss: readCacheMissCount.Load(),
		changed:   readChangedCount.Load(),
		dirty:     readDirtyCount.Load(),
		tooFresh:  skipTooFreshCount.Load(),
	}
}

// skipOnly is the boolean half of classifySkipFile with the rescan-age gate
// disabled. It exists ONLY for tests written before the gate, which assert the
// mtime/size/NeedsRescan comparison and have nothing to say about freshness.
// Production code always calls classifySkipFile directly and reads the reason —
// there is deliberately no production forwarder, because one that dropped the
// cutoff on the floor would disable the gate wherever it was used.
func skipOnly(filePath string, mtime, size int64, cache map[string]database.ScanCacheEntry) bool {
	skip, _ := classifySkipFile(filePath, mtime, size, cache, gateOff)
	return skip
}
