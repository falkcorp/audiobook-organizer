// file: internal/server/duplicates_ops_core_test.go
// version: 1.0.0
// guid: 002d8c52-6acf-4ef8-8293-e2b427783e7c
// last-edited: 2026-11-18

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// dedupCallLog records, in order, what a core asked of its store, writer and
// sinks. The order is part of the contract: the cached duplicate list must be
// dropped only after the write it makes stale, and the candidate rows only
// after the merge that orphaned them.
type dedupCallLog struct {
	calls   []string
	emitted []string // "<opID>|<entryType>|<summary>"
	sources []string // the source of each emitted row, in order
	flushed []string
	order   []string // "flush" and "emit" in call order
}

func (l *dedupCallLog) add(format string, args ...any) {
	l.calls = append(l.calls, fmt.Sprintf(format, args...))
}

func (l *dedupCallLog) FlushActivityOp(opID string) {
	l.flushed = append(l.flushed, opID)
	l.order = append(l.order, "flush")
}
func (l *dedupCallLog) EmitActivityInfo(opID, entryType, source string, summary string, _ ...string) {
	l.emitted = append(l.emitted, opID+"|"+entryType+"|"+summary)
	l.sources = append(l.sources, source)
	l.order = append(l.order, "emit")
}
func (l *dedupCallLog) LogActivityBatch(string, string, string, activity.BatchItem) {}

func dedupTestEnv(live bool, opID string, results *[]any) t7Env {
	e := t7Env{
		Live: live,
		OpID: opID,
		Log:  func(slog.Level, string, ...slog.Attr) {},
	}
	if results != nil {
		e.SetResult = func(v any) error { *results = append(*results, v); return nil }
	}
	return e
}

// ---- scans -----------------------------------------------------------------

type fakeDedupScanStore struct {
	userPreferenceReader
	log     *dedupCallLog
	series  dedup.SeriesScanResult
	books   dedup.BookScanResult
	scanErr error
}

func (f *fakeDedupScanStore) GetUserPreference(string) (*database.UserPreference, error) {
	return nil, nil
}

func (f *fakeDedupScanStore) ScanBookDuplicates(context.Context, map[string]bool, dedup.ProgressReporter) (dedup.BookScanResult, error) {
	return f.books, f.scanErr
}

func (f *fakeDedupScanStore) ScanSeriesDuplicates(context.Context, dedup.ProgressReporter) (dedup.SeriesScanResult, error) {
	return f.series, f.scanErr
}

func (f *fakeDedupScanStore) GetAllAuthors() ([]database.Author, error) {
	return []database.Author{{ID: 1, Name: "Bruce Wayne"}, {ID: 2, Name: "Bruce Wayne"}}, nil
}

func (f *fakeDedupScanStore) GetAllAuthorBookCounts() (map[int]int, error) {
	return map[int]int{1: 3, 2: 1}, nil
}

func (f *fakeDedupScanStore) FilterReviewedAuthorGroups(g []dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup {
	f.log.add("FilterReviewedAuthorGroups(%d)", len(g))
	return g
}

type recordedCacheSet struct {
	key string
	ttl time.Duration
	val gin.H
}

type fakeDedupCache struct{ sets []recordedCacheSet }

func (c *fakeDedupCache) SetWithTTL(k string, v gin.H, ttl time.Duration) {
	c.sets = append(c.sets, recordedCacheSet{k, ttl, v})
}
func (c *fakeDedupCache) Set(k string, v gin.H) { c.sets = append(c.sets, recordedCacheSet{k, 0, v}) }
func (c *fakeDedupCache) InvalidateAll()        {}

func TestDedupScanCores_ReturnTheResultAndPublishOnlyWhenWired(t *testing.T) {
	scan := &fakeDedupScanStore{log: &dedupCallLog{}}
	scan.series = dedup.SeriesScanResult{
		Groups:      []dedup.SeriesDupGroup{{Name: "Year One", Count: 2, Series: []dedup.SeriesWithBooks{{ID: 7}, {ID: 9}}}},
		TotalSeries: 40,
	}
	scan.books = dedup.BookScanResult{Groups: []dedup.BookDupGroup{{}, {}}, TotalDuplicates: 5}

	t.Run("native env: no cache, no activity, result returned", func(t *testing.T) {
		env := dedupScanEnv{t7Env: dedupTestEnv(true, "run-1", nil), Store: scan}
		sr, err := seriesDedupScanCore(context.Background(), env)
		if err != nil {
			t.Fatal(err)
		}
		if sr.Count != 1 || sr.TotalSeries != 40 || len(sr.Groups) != 1 {
			t.Fatalf("series result = %+v", sr)
		}
		br, err := bookDedupScanCore(context.Background(), env)
		if err != nil {
			t.Fatal(err)
		}
		if br.GroupCount != 2 || br.DuplicateCount != 5 {
			t.Fatalf("book result = %+v", br)
		}
		ar, err := authorDedupScanCore(context.Background(), env)
		if err != nil {
			t.Fatal(err)
		}
		if ar.Count != len(ar.Groups) || ar.Count != 1 {
			t.Fatalf("author result = %+v (two identical names are one group)", ar)
		}
		if got := scan.log.calls; len(got) != 1 || got[0] != "FilterReviewedAuthorGroups(1)" {
			t.Fatalf("author scan must filter reviewed groups exactly once; calls = %v", got)
		}
	})

	t.Run("HEAD env: cache keys, ttls and the activity row", func(t *testing.T) {
		cache := &fakeDedupCache{}
		act := &dedupCallLog{}
		env := dedupScanEnv{t7Env: dedupTestEnv(true, "op-9", nil), Store: scan, Cache: cache, Activity: act}
		if _, err := seriesDedupScanCore(context.Background(), env); err != nil {
			t.Fatal(err)
		}
		if _, err := bookDedupScanCore(context.Background(), env); err != nil {
			t.Fatal(err)
		}
		if _, err := authorDedupScanCore(context.Background(), env); err != nil {
			t.Fatal(err)
		}
		want := []struct {
			key  string
			ttl  time.Duration
			keys []string
		}{
			{"series-duplicates", 0, []string{"count", "groups", "total_series"}},
			{"book-dedup-scan", 30 * time.Minute, []string{"duplicate_count", "group_count", "groups"}},
			{"author-duplicates", 30 * time.Minute, []string{"count", "groups"}},
		}
		if len(cache.sets) != len(want) {
			t.Fatalf("cache writes = %d, want %d", len(cache.sets), len(want))
		}
		for i, w := range want {
			got := cache.sets[i]
			var keys []string
			for k := range got.val {
				keys = append(keys, k)
			}
			if got.key != w.key || got.ttl != w.ttl || !sameStrings(keys, w.keys) {
				t.Errorf("cache write %d = {%s %v %v}, want {%s %v %v}", i, got.key, got.ttl, keys, w.key, w.ttl, w.keys)
			}
		}
		if len(act.emitted) != 3 || !strings.HasPrefix(act.emitted[0], "op-9|dedup.series-scan|Series duplicate scan found 1 groups (of 40 total series)") {
			t.Errorf("activity rows = %v", act.emitted)
		}
	})

	t.Run("empty op id writes no activity row", func(t *testing.T) {
		act := &dedupCallLog{}
		env := dedupScanEnv{t7Env: dedupTestEnv(true, "", nil), Store: scan, Activity: act}
		if _, err := seriesDedupScanCore(context.Background(), env); err != nil {
			t.Fatal(err)
		}
		if len(act.emitted)+len(act.flushed) != 0 {
			t.Fatalf("activity written for an empty op id: %v %v", act.emitted, act.flushed)
		}
	})

	t.Run("a failed scan returns the error and publishes nothing", func(t *testing.T) {
		boom := &fakeDedupScanStore{log: &dedupCallLog{}, scanErr: errors.New("index offline")}
		cache := &fakeDedupCache{}
		env := dedupScanEnv{t7Env: dedupTestEnv(true, "op", nil), Store: boom, Cache: cache}
		if _, err := seriesDedupScanCore(context.Background(), env); err == nil || !strings.Contains(err.Error(), "index offline") {
			t.Fatalf("err = %v", err)
		}
		if _, err := bookDedupScanCore(context.Background(), env); err == nil {
			t.Fatal("book scan swallowed its error")
		}
		if len(cache.sets) != 0 {
			t.Fatalf("a failed scan cached %v", cache.sets)
		}
	})
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// ---- book merge / combine --------------------------------------------------

type fakeBookMergeWriter struct {
	*dedupCallLog
	mergeRes *merge.Result
	skipped  []SkippedMergeLoser
	mergeErr error
}

func (w *fakeBookMergeWriter) MergeBooks(opID, keepID string, ids []string) (*merge.Result, []SkippedMergeLoser, error) {
	w.add("MergeBooks(%s,%s,%v)", opID, keepID, ids)
	return w.mergeRes, w.skipped, w.mergeErr
}
func (w *fakeBookMergeWriter) CleanupMergeCandidates(ids []string) { w.add("Cleanup(%v)", ids) }
func (w *fakeBookMergeWriter) InvalidateDedupCache()               { w.add("Invalidate") }

func TestBookMergeCore_OrderAndPayload(t *testing.T) {
	t.Run("merged losers only, cache dropped before the candidate clean-up", func(t *testing.T) {
		log := &dedupCallLog{}
		w := &fakeBookMergeWriter{
			dedupCallLog: log,
			mergeRes:     &merge.Result{PrimaryID: "bk-1", SoftDeleted: 2},
			skipped:      []SkippedMergeLoser{{BookID: "bk-3", Reason: SkipReasonAlreadyTrashed}},
		}
		var results []any
		p := dedup.BookMergeParams{KeepID: "bk-1", MergeIDs: []string{"bk-2", "bk-3", "bk-4"}}
		got, err := bookMergeCore(context.Background(), p, bookMergeEnv{t7Env: dedupTestEnv(true, "op-m", &results), Writer: w})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			"MergeBooks(op-m,bk-1,[bk-2 bk-3 bk-4])",
			"Invalidate",
			"Cleanup([bk-2 bk-4])",
		}
		if !reflect.DeepEqual(log.calls, want) {
			t.Fatalf("calls = %v\nwant    %v", log.calls, want)
		}
		if got.Merged != 2 || got.KeepID != "bk-1" || got.Failed || len(got.Skipped) != 1 {
			t.Fatalf("payload = %+v", got)
		}
		if len(results) != 1 || !reflect.DeepEqual(results[0], got) {
			t.Fatalf("stored result = %v, want the returned payload", results)
		}
		if len(log.emitted) != 1 || !strings.HasPrefix(log.emitted[0], "op-m|dedup.book-merge|") {
			t.Fatalf("activity = %v", log.emitted)
		}
	})

	t.Run("a failed merge stores a failed payload and cleans nothing", func(t *testing.T) {
		log := &dedupCallLog{}
		w := &fakeBookMergeWriter{dedupCallLog: log, mergeErr: &merge.PartialMergeError{Err: errors.New("sibling move landed, delete failed")}}
		var results []any
		p := dedup.BookMergeParams{KeepID: "bk-1", MergeIDs: []string{"bk-2"}}
		got, err := bookMergeCore(context.Background(), p, bookMergeEnv{t7Env: dedupTestEnv(true, "op-m", &results), Writer: w})
		if err == nil {
			t.Fatal("merge error swallowed")
		}
		if !got.Failed || !got.Partial {
			t.Fatalf("payload = %+v, want Failed and Partial", got)
		}
		if len(results) != 1 {
			t.Fatalf("failure payload stored %d times", len(results))
		}
		if len(log.calls) != 1 || len(log.emitted) != 0 {
			t.Fatalf("a failed merge touched the cache, candidates or activity: %v %v", log.calls, log.emitted)
		}
	})

	t.Run("native env without SetResult still returns the payload", func(t *testing.T) {
		w := &fakeBookMergeWriter{dedupCallLog: &dedupCallLog{}, mergeRes: &merge.Result{SoftDeleted: 1}}
		p := dedup.BookMergeParams{KeepID: "bk-1", MergeIDs: []string{"bk-2"}}
		got, err := bookMergeCore(context.Background(), p, bookMergeEnv{t7Env: dedupTestEnv(true, "", nil), Writer: w})
		if err != nil || got.Merged != 1 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

type fakeBookCombineWriter struct {
	*dedupCallLog
	res *merge.CombineResult
	err error
}

func (w *fakeBookCombineWriter) CombineBooks(_ context.Context, opID string, ids []string, keep string, _ *merge.CombineOverride) (*merge.CombineResult, error) {
	w.add("Combine(%s,%v,%s)", opID, ids, keep)
	return w.res, w.err
}
func (w *fakeBookCombineWriter) CleanupMergeCandidates(ids []string) { w.add("Cleanup(%v)", ids) }
func (w *fakeBookCombineWriter) InvalidateDedupCache()               { w.add("Invalidate") }

func TestBookCombineCore(t *testing.T) {
	p := dedup.BookCombineParams{KeepID: "bk-1", MergeIDs: []string{"bk-2", "bk-3"}}

	t.Run("validate refuses a request with no survivor or nothing to absorb", func(t *testing.T) {
		for _, bad := range []dedup.BookCombineParams{{}, {KeepID: "bk-1"}, {MergeIDs: []string{"bk-2"}}} {
			err := bookCombineValidate(bad)
			if err == nil || !strings.Contains(err.Error(), "keep_id and at least one merge_id are required") {
				t.Errorf("bookCombineValidate(%+v) = %v", bad, err)
			}
		}
		if err := bookCombineValidate(p); err != nil {
			t.Errorf("valid params refused: %v", err)
		}
	})

	t.Run("success passes the survivor last and cleans the absorbed books", func(t *testing.T) {
		log := &dedupCallLog{}
		w := &fakeBookCombineWriter{dedupCallLog: log, res: &merge.CombineResult{PrimaryID: "bk-1", FilesMoved: 4, BooksDeleted: 2}}
		got, err := bookCombineCore(context.Background(), p, bookCombineEnv{t7Env: dedupTestEnv(true, "op-c", nil), Writer: w})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"Combine(op-c,[bk-2 bk-3 bk-1],bk-1)", "Invalidate", "Cleanup([bk-2 bk-3])"}
		if !reflect.DeepEqual(log.calls, want) {
			t.Fatalf("calls = %v, want %v", log.calls, want)
		}
		if got.PrimaryID != "bk-1" || got.FilesMoved != 4 || got.Failed || got.RetryLater {
			t.Fatalf("payload = %+v", got)
		}
	})

	t.Run("retry-later is a result, not a failure, and cleans nothing", func(t *testing.T) {
		log := &dedupCallLog{}
		w := &fakeBookCombineWriter{dedupCallLog: log, err: fmt.Errorf("moving: %w", database.ErrBookFileConflict)}
		var results []any
		got, err := bookCombineCore(context.Background(), p, bookCombineEnv{t7Env: dedupTestEnv(true, "op-c", &results), Writer: w})
		if err != nil {
			t.Fatalf("retry-later returned an error: %v", err)
		}
		if !got.RetryLater || got.Failed || got.RetryReason == "" {
			t.Fatalf("payload = %+v", got)
		}
		if len(log.calls) != 1 || len(results) != 1 {
			t.Fatalf("calls %v results %v: retry-later must not invalidate or clean", log.calls, results)
		}
	})

	t.Run("an incomplete combine names the op that undoes it", func(t *testing.T) {
		log := &dedupCallLog{}
		w := &fakeBookCombineWriter{dedupCallLog: log, err: &merge.CombineIncompleteError{JournalID: "j-1", OperationID: "op-c", Err: errors.New("disk")}}
		got, err := bookCombineCore(context.Background(), p, bookCombineEnv{t7Env: dedupTestEnv(true, "op-c", nil), Writer: w})
		if err == nil {
			t.Fatal("incomplete combine returned no error")
		}
		if !got.Failed || !got.Incomplete || got.JournalID != "j-1" || !strings.Contains(got.Error, "Revert operation op-c") {
			t.Fatalf("payload = %+v", got)
		}
		if len(log.calls) != 1 {
			t.Fatalf("calls = %v", log.calls)
		}
	})
}

// ---- series dedup ----------------------------------------------------------

type fakeSeriesDedup struct {
	*dedupCallLog
	preview, apply dedup.SeriesDedupResult
	applyErr       error
	gotScan        dedup.ScanStandDownController
}

func (f *fakeSeriesDedup) PreviewSeriesDedup(context.Context, string, dedup.ProgressReporter) (dedup.SeriesDedupResult, error) {
	f.add("Preview")
	return f.preview, nil
}

func (f *fakeSeriesDedup) ApplySeriesDedup(_ context.Context, opID string, scan dedup.ScanStandDownController, _ dedup.ProgressReporter) (dedup.SeriesDedupResult, error) {
	f.add("Apply(%s)", opID)
	f.gotScan = scan
	return f.apply, f.applyErr
}
func (f *fakeSeriesDedup) InvalidateDedupCache() { f.add("Invalidate") }

type stubScan struct{}

func (stubScan) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	return func() {}, nil
}
func (stubScan) RenewScanStandDown(string) bool { return true }

func TestSeriesDedupCore(t *testing.T) {
	t.Run("a preview reads through the store, builds no writer and writes no row", func(t *testing.T) {
		f := &fakeSeriesDedup{dedupCallLog: &dedupCallLog{}, preview: dedup.SeriesDedupResult{TotalMerged: 3, DryRun: true}}
		// Writer is nil on purpose: any write a preview made would panic.
		got, err := seriesDedupCore(context.Background(), seriesDedupEnv{t7Env: dedupTestEnv(false, "op-d", nil), Store: f})
		if err != nil || got.TotalMerged != 3 {
			t.Fatalf("got %+v, %v", got, err)
		}
		if !reflect.DeepEqual(f.calls, []string{"Preview"}) {
			t.Fatalf("calls = %v", f.calls)
		}
	})

	t.Run("an apply hands the stand-down to the engine and invalidates after it", func(t *testing.T) {
		f := &fakeSeriesDedup{dedupCallLog: &dedupCallLog{}, apply: dedup.SeriesDedupResult{TotalMerged: 2}}
		scan := stubScan{}
		got, err := seriesDedupCore(context.Background(), seriesDedupEnv{t7Env: dedupTestEnv(true, "op-d", nil), Store: f, Writer: f, Scan: scan})
		if err != nil || got.TotalMerged != 2 {
			t.Fatalf("got %+v, %v", got, err)
		}
		if !reflect.DeepEqual(f.calls, []string{"Apply(op-d)", "Invalidate"}) {
			t.Fatalf("calls = %v", f.calls)
		}
		if f.gotScan != dedup.ScanStandDownController(scan) {
			t.Fatal("the engine did not get the env's stand-down controller")
		}
		if len(f.emitted) != 1 || !strings.HasPrefix(f.emitted[0], "op-d|dedup.series-dedup|") {
			t.Fatalf("activity = %v", f.emitted)
		}
	})

	t.Run("an apply that lost its lease still invalidates the list", func(t *testing.T) {
		f := &fakeSeriesDedup{dedupCallLog: &dedupCallLog{}, applyErr: errors.New("lease lost")}
		_, err := seriesDedupCore(context.Background(), seriesDedupEnv{t7Env: dedupTestEnv(true, "op-d", nil), Store: f, Writer: f, Scan: stubScan{}})
		if err == nil {
			t.Fatal("error swallowed")
		}
		if !reflect.DeepEqual(f.calls, []string{"Apply(op-d)", "Invalidate"}) {
			t.Fatalf("calls = %v", f.calls)
		}
	})

	t.Run("an apply that kept a series fails, naming the ids first", func(t *testing.T) {
		f := &fakeSeriesDedup{dedupCallLog: &dedupCallLog{}, apply: dedup.SeriesDedupResult{TotalMerged: 1, LeftIDs: []int{11, 12}, Errors: []string{"delete refused"}}}
		_, err := seriesDedupCore(context.Background(), seriesDedupEnv{t7Env: dedupTestEnv(true, "op-d", nil), Store: f, Writer: f, Scan: stubScan{}})
		if err == nil || !strings.HasPrefix(err.Error(), SeriesDedupFailedIDsPrefix+"11 12]") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a preview's predicted errors are not a failure", func(t *testing.T) {
		f := &fakeSeriesDedup{dedupCallLog: &dedupCallLog{}, preview: dedup.SeriesDedupResult{DryRun: true, Errors: []string{"would refuse"}}}
		if _, err := seriesDedupCore(context.Background(), seriesDedupEnv{t7Env: dedupTestEnv(false, "op-d", nil), Store: f}); err != nil {
			t.Fatalf("a preview failed on a prediction: %v", err)
		}
	})
}

// ---- series prune / merge --------------------------------------------------

type fakeSeriesPrune struct {
	*dedupCallLog
	err error
}

func (f *fakeSeriesPrune) PruneSeries(_ context.Context, opID string, _ operations.ProgressReporter) error {
	f.add("PruneSeries(%s)", opID)
	return f.err
}

func TestSeriesPruneCore(t *testing.T) {
	ok := &fakeSeriesPrune{dedupCallLog: &dedupCallLog{}}
	if err := seriesPruneCore(context.Background(), seriesPruneEnv{t7Env: dedupTestEnv(true, "op-p", nil), Writer: ok}); err != nil {
		t.Fatal(err)
	}
	if len(ok.emitted) != 1 || !strings.HasSuffix(ok.emitted[0], "|Series prune completed") {
		t.Fatalf("activity = %v", ok.emitted)
	}
	bad := &fakeSeriesPrune{dedupCallLog: &dedupCallLog{}, err: errors.New("refused 3 deletes")}
	err := seriesPruneCore(context.Background(), seriesPruneEnv{t7Env: dedupTestEnv(true, "op-p", nil), Writer: bad})
	if err == nil || err.Error() != "refused 3 deletes" {
		t.Fatalf("err = %v, want the engine's error unchanged", err)
	}
	if len(bad.emitted) != 1 || !strings.HasSuffix(bad.emitted[0], "|Series prune failed: refused 3 deletes") {
		t.Fatalf("activity = %v", bad.emitted)
	}
}

func TestSeriesMergeCore(t *testing.T) {
	t.Run("merge: locks honoured by default, controller passed, list invalidated", func(t *testing.T) {
		w := &fakeSeriesMerge{dedupCallLog: &dedupCallLog{}, res: dedup.SeriesMergeResult{MergedCount: 2}}
		p := dedup.SeriesMergeParams{KeepID: 5, MergeIDs: []int{6, 7}, CustomName: "Year One"}
		scan := stubScan{}
		if err := seriesMergeCore(context.Background(), p, seriesMergeEnv{t7Env: dedupTestEnv(true, "op-s", nil), Writer: w, Scan: scan}); err != nil {
			t.Fatal(err)
		}
		if !w.opts.HonorLocks || w.opts.Scan != dedup.ScanStandDownController(scan) {
			t.Fatalf("opts = %+v", w.opts)
		}
		if !reflect.DeepEqual(w.calls, []string{"MergeSeries(op-s,5,[6 7],Year One)", "Invalidate"}) {
			t.Fatalf("calls = %v", w.calls)
		}
		off := false
		p.HonorLocks = &off
		_ = seriesMergeCore(context.Background(), p, seriesMergeEnv{t7Env: dedupTestEnv(true, "op-s", nil), Writer: w, Scan: scan})
		if w.opts.HonorLocks {
			t.Fatal("an explicit honor_locks=false was overridden")
		}
	})

	t.Run("merge: an engine error still invalidates", func(t *testing.T) {
		w := &fakeSeriesMerge{dedupCallLog: &dedupCallLog{}, err: errors.New("cancelled")}
		err := seriesMergeCore(context.Background(), dedup.SeriesMergeParams{KeepID: 5, MergeIDs: []int{6}}, seriesMergeEnv{t7Env: dedupTestEnv(true, "op-s", nil), Writer: w})
		if err == nil || !reflect.DeepEqual(w.calls, []string{"MergeSeries(op-s,5,[6],)", "Invalidate"}) {
			t.Fatalf("err %v calls %v", err, w.calls)
		}
		if len(w.emitted) != 0 {
			t.Fatalf("a failed merge announced %v", w.emitted)
		}
	})

	t.Run("merge: refused deletes fail the run and say INCOMPLETE", func(t *testing.T) {
		w := &fakeSeriesMerge{dedupCallLog: &dedupCallLog{}, res: dedup.SeriesMergeResult{MergedCount: 1, LeftIDs: []int{7}, Errors: []string{"delete refused"}}}
		err := seriesMergeCore(context.Background(), dedup.SeriesMergeParams{KeepID: 5, MergeIDs: []int{6, 7}}, seriesMergeEnv{t7Env: dedupTestEnv(true, "op-s", nil), Writer: w})
		if err == nil || !strings.HasPrefix(err.Error(), SeriesMergeFailedIDsPrefix+"7]") {
			t.Fatalf("err = %v", err)
		}
		if len(w.emitted) != 1 || !strings.Contains(w.emitted[0], "Series merge INCOMPLETE") {
			t.Fatalf("activity = %v", w.emitted)
		}
	})
}

type fakeSeriesMerge struct {
	*dedupCallLog
	res  dedup.SeriesMergeResult
	err  error
	opts dedup.MergeSeriesOptions
}

func (f *fakeSeriesMerge) MergeSeries(_ context.Context, opID string, keep int, ids []int, name string, _ dedup.ProgressReporter, o dedup.MergeSeriesOptions) (dedup.SeriesMergeResult, error) {
	f.add("MergeSeries(%s,%d,%v,%s)", opID, keep, ids, name)
	f.opts = o
	return f.res, f.err
}
func (f *fakeSeriesMerge) InvalidateDedupCache() { f.add("Invalidate") }

// ---- series normalize ------------------------------------------------------

type fakeNormalize struct {
	*dedupCallLog
	affected   []string
	normErr    error
	books      map[string]*database.Book
	organize   map[string]error
	writeBack  []string
	writeBackN int
}

func (f *fakeNormalize) GetBookByID(id string) (*database.Book, error) {
	b, ok := f.books[id]
	if !ok {
		return nil, errors.New("store down")
	}
	return b, nil
}
func (f *fakeNormalize) NormalizeSeries(context.Context, string) ([]string, error) {
	f.add("Normalize")
	return f.affected, f.normErr
}
func (f *fakeNormalize) WriteBackBooks(_ context.Context, _ string, ids []string, _ bool, _ int, _ operations.ProgressReporter, _ func(string)) error {
	f.writeBackN++
	f.writeBack = ids
	return nil
}
func (f *fakeNormalize) InvalidateSeriesCache() { f.add("InvalidateSeries") }
func (f *fakeNormalize) OrganizeBook(b *database.Book, _ string) error {
	f.add("Organize(%s)", b.ID)
	return f.organize[b.ID]
}

func TestSeriesNormalizeCore(t *testing.T) {
	newFake := func() *fakeNormalize {
		return &fakeNormalize{
			dedupCallLog: &dedupCallLog{},
			affected:     []string{"b1", "b2", "b3", "b4", "b5"},
			books: map[string]*database.Book{
				"b1": {ID: "b1"}, "b2": {ID: "b2"}, "b3": {ID: "b3"}, "b4": nil, // b4 does not resolve; b5 errors
			},
			organize: map[string]error{
				"b2": organizer.ErrAuthorUnresolved,
				"b3": fmt.Errorf("x: %w", organizer.ErrDestinationConflictUnresolved),
			},
		}
	}
	run := func(f *fakeNormalize) error {
		return seriesNormalizeCore(context.Background(), seriesNormalizeEnv{t7Env: dedupTestEnv(true, "op-n", nil), Store: f, Writer: f})
	}

	t.Run("unresolved and conflicting books are refusals, the rest are failures", func(t *testing.T) {
		f := newFake()
		err := run(f)
		if err == nil || !strings.Contains(err.Error(), "2 of 5 affected books were not organized") {
			t.Fatalf("err = %v (b4 unresolved and b5 unreadable are failures; b2 and b3 are refusals)", err)
		}
		// Organize ran for b1, b2, b3 only: b4 is nil and b5 failed to load.
		var organized []string
		for _, c := range f.calls {
			if strings.HasPrefix(c, "Organize(") {
				organized = append(organized, c)
			}
		}
		if !reflect.DeepEqual(organized, []string{"Organize(b1)", "Organize(b2)", "Organize(b3)"}) {
			t.Fatalf("organized = %v", organized)
		}
		if f.writeBackN != 1 || !reflect.DeepEqual(f.writeBack, f.affected) {
			t.Fatalf("write-back ran %d times over %v", f.writeBackN, f.writeBack)
		}
		if len(f.emitted) != 0 || len(f.flushed) != 1 {
			t.Fatalf("a failed run must flush but not announce success: emitted %v flushed %v", f.emitted, f.flushed)
		}
	})

	t.Run("a normalize error still organizes what it collected, then fails", func(t *testing.T) {
		f := newFake()
		f.affected = []string{"b1"}
		f.normErr = errors.New("rename failed for series 4")
		err := run(f)
		if err == nil || !strings.Contains(err.Error(), "rename failed for series 4") {
			t.Fatalf("err = %v", err)
		}
		if !dedupHasCall(f.calls, "Organize(b1)") || f.writeBackN != 1 {
			t.Fatalf("partial work discarded: calls %v write-back %d", f.calls, f.writeBackN)
		}
	})

	t.Run("a clean run with nothing affected touches neither cache nor files", func(t *testing.T) {
		f := newFake()
		f.affected = nil
		if err := run(f); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.calls, []string{"Normalize"}) || f.writeBackN != 0 {
			t.Fatalf("calls %v write-back %d", f.calls, f.writeBackN)
		}
		if len(f.emitted) != 1 {
			t.Fatalf("activity = %v", f.emitted)
		}
	})

	t.Run("a clean run invalidates the series cache before organizing", func(t *testing.T) {
		f := newFake()
		f.affected = []string{"b1"}
		if err := run(f); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.calls, []string{"Normalize", "InvalidateSeries", "Organize(b1)"}) {
			t.Fatalf("calls = %v", f.calls)
		}
	})

	t.Run("cancellation joins the normalize error with ctx.Err", func(t *testing.T) {
		f := newFake()
		f.normErr = errors.New("rename failed for series 4")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := seriesNormalizeCore(ctx, seriesNormalizeEnv{t7Env: dedupTestEnv(true, "op-n", nil), Store: f, Writer: f})
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "rename failed for series 4") {
			t.Fatalf("err = %v", err)
		}
		if dedupHasCall(f.calls, "Organize(b1)") {
			t.Fatal("organized after cancel")
		}
	})
}

func dedupHasCall(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---- native scan controller ------------------------------------------------

func TestScanController_AcquireRenewRelease(t *testing.T) {
	var finished []error
	renews := 0
	alive := true
	c := &t7ScanController{acquire: func(_ context.Context, reason string) (t7Hold, error) {
		if reason != "series dedup" {
			t.Errorf("reason = %q", reason)
		}
		return t7Hold{
			Renew:  func() bool { renews++; return alive },
			Finish: func(err error) error { finished = append(finished, err); return nil },
		}, nil
	}}
	if c.RenewScanStandDown("op") {
		t.Fatal("renewed a hold that was never acquired")
	}
	release, err := c.AcquireScanStandDown(context.Background(), "op", "series dedup")
	if err != nil {
		t.Fatal(err)
	}
	if !c.RenewScanStandDown("op") || renews != 1 {
		t.Fatalf("renew = false or not delegated (renews=%d)", renews)
	}
	alive = false
	if c.RenewScanStandDown("op") {
		t.Fatal("renew reported held after the lease was lost")
	}
	release()
	if len(finished) != 1 || finished[0] != nil {
		t.Fatalf("finish calls = %v", finished)
	}

	denied := &t7ScanController{acquire: func(context.Context, string) (t7Hold, error) { return t7Hold{}, errors.New("busy") }}
	if _, err := denied.AcquireScanStandDown(context.Background(), "op", "x"); err == nil {
		t.Fatal("a refused stand-down returned no error")
	}
	if denied.RenewScanStandDown("op") {
		t.Fatal("renewed after a refused acquire")
	}
}

// t7Announce is the one place a run's activity row is written: nothing without a
// writer or a run id, otherwise the batch is flushed and then the row emitted
// under the caller's source.
func TestAnnounceActivity(t *testing.T) {
	t.Run("a nil writer is tolerated", func(t *testing.T) {
		t7Announce(nil, "op-1", "x.y", "src", "done")
	})
	t.Run("an empty run id writes nothing", func(t *testing.T) {
		l := &dedupCallLog{}
		t7Announce(l, "", "x.y", "src", "done")
		require.Empty(t, l.order)
	})
	t.Run("flushes first, then emits under the source", func(t *testing.T) {
		l := &dedupCallLog{}
		t7Announce(l, "op-1", "x.y", "src", "done")
		require.Equal(t, []string{"flush", "emit"}, l.order)
		require.Equal(t, []string{"op-1|x.y|done"}, l.emitted)
		require.Equal(t, []string{"src"}, l.sources)
	})
}
