// file: internal/scanner/skip_classify_test.go
// version: 1.0.0
// guid: 8f2a1c47-6b3d-4e59-9a0f-2d7c4b8e1a63
// last-edited: 2026-08-24

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// classifySkipFile is the instrumented form of shouldSkipFile. These tests pin
// the REASON, not just the boolean -- the boolean was already covered by
// incremental_test.go and unit_test.go, and it was the missing reason that made
// an incremental scan unobservable.
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
		{"unchanged skips", path, 100, 200, cache, false, reasonUnchanged},
		{"nil cache is cache-off", path, 100, 200, nil, false, reasonCacheOff},
		{"absent path is cache-miss", "/lib/new.m4b", 1, 2, cache, false, reasonCacheMiss},
		{"different mtime is changed", path, 999, 200, cache, false, reasonChanged},
		{"different size is changed", path, 100, 999, cache, false, reasonChanged},
		{"needs-rescan is dirty", "/lib/dirty.m4b", 100, 200, cache, false, reasonDirty},
	}
	// The unchanged case is the only skip; fix its expectation here so the
	// table above stays a single shape.
	tests[0].wantSkip = true

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := classifySkipFile(tc.path, tc.mtime, tc.size, tc.cache)
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
	skip, reason := classifySkipFile("/lib/both.m4b", 999, 888, cache)
	if skip {
		t.Fatal("a dirty entry must never be skipped")
	}
	if reason != reasonDirty {
		t.Errorf("reason = %v, want reasonDirty (a forced rescan must not be reported as a plain mtime change)", reason)
	}
}

// shouldSkipFile delegates to classifySkipFile, so the two can never disagree.
// This is the guard that keeps the 14 existing shouldSkipFile call sites honest
// if classifySkipFile is ever edited.
func TestShouldSkipFile_AgreesWithClassify(t *testing.T) {
	cache := map[string]database.ScanCacheEntry{
		"/lib/a.m4b": {Mtime: 1, Size: 2, NeedsRescan: false},
		"/lib/b.m4b": {Mtime: 1, Size: 2, NeedsRescan: true},
	}
	cases := []struct {
		path        string
		mtime, size int64
		cache       map[string]database.ScanCacheEntry
	}{
		{"/lib/a.m4b", 1, 2, cache},
		{"/lib/a.m4b", 9, 2, cache},
		{"/lib/b.m4b", 1, 2, cache},
		{"/lib/missing.m4b", 1, 2, cache},
		{"/lib/a.m4b", 1, 2, nil},
	}
	for _, c := range cases {
		want, _ := classifySkipFile(c.path, c.mtime, c.size, c.cache)
		if got := shouldSkipFile(c.path, c.mtime, c.size, c.cache); got != want {
			t.Errorf("shouldSkipFile(%s) = %v, classifySkipFile = %v", c.path, got, want)
		}
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
}

type skipCounterSnapshot struct {
	unchanged, cacheOff, cacheMiss, changed, dirty int64
}

func snapshotSkipCounters() skipCounterSnapshot {
	return skipCounterSnapshot{
		unchanged: skipUnchangedCount.Load(),
		cacheOff:  readCacheOffCount.Load(),
		cacheMiss: readCacheMissCount.Load(),
		changed:   readChangedCount.Load(),
		dirty:     readDirtyCount.Load(),
	}
}
