// file: internal/scanner/scan_rollup_test.go
// version: 1.0.0
// guid: 9f4a2d71-6b83-4c05-a1e7-3d92f8b04c6a
// last-edited: 2026-08-24

package scanner

import (
	"errors"
	"math"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

type fakeFileInfo struct {
	os.FileInfo
	mtime time.Time
	size  int64
}

func (f fakeFileInfo) ModTime() time.Time { return f.mtime }
func (f fakeFileInfo) Size() int64        { return f.size }

// statFrom builds a stat func over a fixed path -> (mtime, size) table. A path
// absent from the table returns a stat error.
func statFrom(m map[string][2]int64) func(string) (os.FileInfo, error) {
	return func(p string) (os.FileInfo, error) {
		v, ok := m[p]
		if !ok {
			return nil, &fs.PathError{Op: "stat", Path: p, Err: errors.New("no such file")}
		}
		return fakeFileInfo{mtime: time.Unix(v[0], 0), size: v[1]}, nil
	}
}

func entry(mtime, size int64) database.ScanCacheEntry {
	return database.ScanCacheEntry{Mtime: mtime, Size: size}
}

// The rescan-age gate is disabled by the LARGEST cutoff, not the smallest: the
// comparison is `mtime > cutoff`, so MaxInt64 never fires and 0 would gate every
// file with a post-epoch mtime -- i.e. all of them. rescanFreshCutoff returns
// MaxInt64 for exactly this reason. Getting it backwards here made the
// one-segment-changed test report a SKIP, which is how this comment came to
// exist.
const noFreshGate = int64(math.MaxInt64)

// THE BUG. Skipping is per file; processing is per book. A six-file book with
// one changed segment must be reprocessed in full -- its duration, size and
// chapter timeline are functions of the whole set, so reprocessing nothing (or
// only the changed file) leaves those aggregates describing the previous
// contents.
//
// Before the rollup this decision read books[idx].FilePath alone, so segment 1
// being unchanged skipped the entire book and the changed audio never landed.
func TestClassifySkipBookReprocessesAWholeBookWhenOneSegmentChanged(t *testing.T) {
	files := []string{"/b/s1.mp3", "/b/s2.mp3", "/b/s3.mp3", "/b/s4.mp3", "/b/s5.mp3", "/b/s6.mp3"}
	stat := map[string][2]int64{}
	cache := map[string]database.ScanCacheEntry{}
	for _, f := range files {
		stat[f] = [2]int64{100, 10}
		cache[f] = entry(100, 10)
	}
	// Segment 5 changed on disk since it was cached.
	stat["/b/s5.mp3"] = [2]int64{999, 42}

	skip, reason := classifySkipBook(files, cache, noFreshGate, statFrom(stat))
	require.False(t, skip,
		"the book was skipped although segment 5 changed; its duration and chapter aggregates now describe the previous contents")
	require.Equal(t, reasonChanged, reason,
		"the reported reason must be the file that refused to skip, so the summary names the cause")
}

// The converse, so the rollup cannot pass by never skipping anything.
func TestClassifySkipBookSkipsWhenEverySegmentIsUnchanged(t *testing.T) {
	files := []string{"/b/s1.mp3", "/b/s2.mp3", "/b/s3.mp3"}
	stat := map[string][2]int64{}
	cache := map[string]database.ScanCacheEntry{}
	for _, f := range files {
		stat[f] = [2]int64{100, 10}
		cache[f] = entry(100, 10)
	}

	skip, reason := classifySkipBook(files, cache, noFreshGate, statFrom(stat))
	require.True(t, skip, "a book whose every segment is unchanged must still be skippable")
	require.Equal(t, reasonUnchanged, reason)
}

// b0's constraint, pinned as a test rather than a comment. The per-file scan
// cache is a stepping stone to dropping Book.FilePath normalisation entirely, so
// the rollup must be CORRECT when that field is garbage -- not merely avoid
// mentioning it. A rollup that grouped by book path would re-introduce the exact
// dependency the schema change is paying to remove.
func TestClassifySkipBookIgnoresTheBookPath(t *testing.T) {
	files := []string{"/real/s1.mp3", "/real/s2.mp3"}
	stat := map[string][2]int64{
		"/real/s1.mp3": {100, 10},
		"/real/s2.mp3": {100, 10},
	}
	cache := map[string]database.ScanCacheEntry{
		"/real/s1.mp3": entry(100, 10),
		"/real/s2.mp3": entry(100, 10),
	}

	for _, bookPath := range []string{"", "/stale/wrong.mp3", "/does/not/exist"} {
		b := &Book{FilePath: bookPath, SegmentFiles: files}
		skip, reason := classifySkipBook(scanFileSetFor(b), cache, noFreshGate, statFrom(stat))
		require.Truef(t, skip,
			"the rollup changed its answer because Book.FilePath was %q; it must decide from the walk's file set alone", bookPath)
		require.Equal(t, reasonUnchanged, reason)
	}
}

// A stat failure is no evidence the file is unchanged, so it must force the
// re-read -- and it must be attributable, which is why reasonStatErr exists.
func TestClassifySkipBookTreatsAStatFailureAsUnskippable(t *testing.T) {
	files := []string{"/b/s1.mp3", "/b/gone.mp3"}
	cache := map[string]database.ScanCacheEntry{
		"/b/s1.mp3":   entry(100, 10),
		"/b/gone.mp3": entry(100, 10),
	}
	stat := map[string][2]int64{"/b/s1.mp3": {100, 10}} // gone.mp3 absent

	skip, reason := classifySkipBook(files, cache, noFreshGate, statFrom(stat))
	require.False(t, skip)
	require.Equal(t, reasonStatErr, reason)
}

// A cache-disabled run is decided ONCE for the book. Reporting cacheOff per file
// would count one book N times and inflate the run summary's re-read total.
func TestClassifySkipBookReportsCacheOffOncePerBook(t *testing.T) {
	files := []string{"/b/s1.mp3", "/b/s2.mp3", "/b/s3.mp3"}
	skip, reason := classifySkipBook(files, nil, noFreshGate, statFrom(nil))
	require.False(t, skip)
	require.Equal(t, reasonCacheOff, reason)
}

// A single-file book must behave exactly as it did before the rollup existed.
func TestScanFileSetForFallsBackToTheBookPath(t *testing.T) {
	require.Equal(t, []string{"/b/only.m4b"}, scanFileSetFor(&Book{FilePath: "/b/only.m4b"}))
	require.Nil(t, scanFileSetFor(&Book{}))
	require.Equal(t, []string{"/b/s1.mp3"},
		scanFileSetFor(&Book{FilePath: "/ignored", SegmentFiles: []string{"/b/s1.mp3"}}))
}

// An empty file set is a cache miss, not a skip. Skipping a book we know nothing
// about is the one answer that loses data silently.
func TestClassifySkipBookNeverSkipsAnEmptyFileSet(t *testing.T) {
	skip, reason := classifySkipBook(nil, map[string]database.ScanCacheEntry{}, noFreshGate, statFrom(nil))
	require.False(t, skip)
	require.Equal(t, reasonCacheMiss, reason)
}

// The rescan-age gate still applies THROUGH the rollup: a segment that changed
// moments ago is deferred rather than read half-written, exactly as the
// single-file path defers it. This is the one case where a book with a changed
// segment is legitimately skipped, and it must stay attributable as tooFresh
// rather than being reported as unchanged.
func TestClassifySkipBookHonoursTheRescanAgeGate(t *testing.T) {
	files := []string{"/b/s1.mp3", "/b/s2.mp3"}
	cache := map[string]database.ScanCacheEntry{
		"/b/s1.mp3": entry(100, 10),
		"/b/s2.mp3": entry(100, 10),
	}
	// s2 changed, and its mtime is above the cutoff, so it is still settling.
	stat := map[string][2]int64{
		"/b/s1.mp3": {100, 10},
		"/b/s2.mp3": {5000, 42},
	}

	skip, reason := classifySkipBook(files, cache, 4000, statFrom(stat))
	require.True(t, skip, "a segment that changed inside the rescan-age window must be deferred, not read half-written")
	require.Equal(t, reasonTooFresh, reason,
		"deferred work must not be reported as unchanged; tooFresh is broken out of the skip total on purpose")
}
