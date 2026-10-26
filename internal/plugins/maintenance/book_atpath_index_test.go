// file: internal/plugins/maintenance/book_atpath_index_test.go
// version: 1.3.0
// guid: 1b7e4c92-5a3d-4e68-9f02-c6d8a1b3e5f7
// last-edited: 2026-10-25

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type fakeAtPathVerifier struct {
	rep database.BookAtPathIndexReport
	err error
}

func (f fakeAtPathVerifier) VerifyBookAtPathIndex(context.Context) (database.BookAtPathIndexReport, error) {
	return f.rep, f.err
}

func TestBookAtPathVerify_CleanIndexPasses(t *testing.T) {
	rep := database.BookAtPathIndexReport{SentinelSet: true, BooksScanned: 3, IndexKeysScanned: 4, ExtraRowGone: 1}
	if err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{}); err != nil {
		t.Fatalf("extras alone must not fail the op: %v", err)
	}
}

func TestBookAtPathVerify_MissingLiveFails(t *testing.T) {
	rep := database.BookAtPathIndexReport{MissingLive: 2}
	err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "INCOMPLETE") {
		t.Fatalf("missing live keys must fail the op, got %v", err)
	}
}

// TestBookAtPathVerify_MarkersNotInSetFails: a marker on disk that the
// in-memory set lacks makes lookups fail open, so the op must fail and name
// the rebuild op, even when every other count is clean.
func TestBookAtPathVerify_MarkersNotInSetFails(t *testing.T) {
	rep := database.BookAtPathIndexReport{SentinelSet: true, BooksScanned: 3, IndexKeysScanned: 3,
		MarkersNotInSet: 1, SampleNotInSet: []string{"01BADROW"}}
	err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "missing from the in-memory") ||
		!strings.Contains(err.Error(), "maintenance.book-atpath-index-backfill") ||
		!strings.Contains(err.Error(), "01BADROW") {
		t.Fatalf("markers missing from the set must fail the op and name the rebuild, got %v", err)
	}
}

// TestBookAtPathVerify_MissingTrashedFails: BookIDsAtPath, which the purge asks
// before it removes a file, keeps trashed rows, so a trashed book with no key
// reads as a free path there. The verify op must fail on it and name the
// rebuild, even when every other count is clean.
func TestBookAtPathVerify_MissingTrashedFails(t *testing.T) {
	rep := database.BookAtPathIndexReport{SentinelSet: true, BooksScanned: 3, IndexKeysScanned: 2,
		MissingTrashed: 1, SampleMissingTrashed: []string{"01TRASHED /lib/a.m4b"}}
	err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "INCOMPLETE") ||
		!strings.Contains(err.Error(), "soft-deleted") ||
		!strings.Contains(err.Error(), "01TRASHED") ||
		!strings.Contains(err.Error(), "maintenance.book-atpath-index-backfill") {
		t.Fatalf("a trashed book with no key must fail the op and name the rebuild, got %v", err)
	}
}

func TestBookAtPathVerify_UndecodableRowsFail(t *testing.T) {
	rep := database.BookAtPathIndexReport{UndecodableRows: 1}
	if err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{}); err == nil {
		t.Fatal("an unchecked row must not be reported as a passing verify")
	}
}

func TestBookAtPathVerify_StoreErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{err: boom}, &fakeReporter{})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want wrapped boom", err)
	}
}

// The real store must satisfy both capability interfaces, or the ops refuse to
// run in production.
func TestBookAtPathOps_PebbleStoreSatisfiesCapabilities(t *testing.T) {
	var _ bookAtPathVerifier = (*database.PebbleStore)(nil)
	var _ bookAtPathRebuilder = (*database.PebbleStore)(nil)
}

// --- if_needed mode (04-P6 part 2) ---

// fakeAtPathBuilder stands in for the store's two passes. backfill and rebuild
// are each the pass's answer; nil records the call and returns the zero result.
type fakeAtPathBuilder struct {
	mu       sync.Mutex
	calls    []string
	backfill func(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error)
	rebuild  func(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error)
}

func (f *fakeAtPathBuilder) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeAtPathBuilder) BackfillBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
	f.record("backfill")
	if f.backfill == nil {
		return database.BookAtPathBackfillResult{}, nil
	}
	return f.backfill(ctx, progress)
}

func (f *fakeAtPathBuilder) RebuildBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
	f.record("rebuild")
	if f.rebuild == nil {
		return database.BookAtPathBackfillResult{}, nil
	}
	return f.rebuild(ctx, progress)
}

// atPathFakeStore puts a fakeAtPathBuilder behind fakeDeps.OpsStore so a test
// drives runBookAtPathIndexBackfill (params decode, mode switch, capability
// resolution). The embedded Store is nil and never called.
type atPathFakeStore struct {
	database.Store
	*fakeAtPathBuilder
}

func atPathPlugin(f *fakeAtPathBuilder) *Plugin {
	return New(fakeDeps{store: atPathFakeStore{fakeAtPathBuilder: f}})
}

// An operator's run (absent or empty params) is the forced rebuild and never
// the sentinel-gated pass; boot's {"if_needed": true} is the gated pass and
// never the rebuild.
func TestBookAtPathBackfill_ModeSelection(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "absent params rebuild", raw: nil, want: "rebuild"},
		{name: "empty object rebuilds", raw: json.RawMessage(`{}`), want: "rebuild"},
		{name: "if_needed false rebuilds", raw: json.RawMessage(`{"if_needed":false}`), want: "rebuild"},
		{name: "if_needed backfills", raw: json.RawMessage(`{"if_needed":true}`), want: "backfill"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAtPathBuilder{}
			if err := atPathPlugin(f).runBookAtPathIndexBackfill(context.Background(), tc.raw, &p6AttrReporter{}); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != 1 || f.calls[0] != tc.want {
				t.Fatalf("calls = %v, want [%s]", f.calls, tc.want)
			}
		})
	}
}

func TestBookAtPathBackfill_BadParamsFail(t *testing.T) {
	f := &fakeAtPathBuilder{}
	err := atPathPlugin(f).runBookAtPathIndexBackfill(context.Background(), json.RawMessage(`{"if_needed":`), &p6AttrReporter{})
	if err == nil {
		t.Fatal("malformed params must fail the run")
	}
	if len(f.calls) != 0 {
		t.Fatalf("malformed params reached the store: %v", f.calls)
	}
}

// The usual boot outcome: the sentinel is set, nothing to do, tagged no-op so
// the default activity feed hides it. A first build is not tagged.
func TestBookAtPathBackfill_IfNeededNoOpTag(t *testing.T) {
	for _, skipped := range []bool{true, false} {
		f := &fakeAtPathBuilder{backfill: func(context.Context, database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
			return database.BookAtPathBackfillResult{Skipped: skipped, Scanned: 5, Commits: 2}, nil
		}}
		rep := &p6AttrReporter{}
		if err := atPathPlugin(f).runBookAtPathIndexBackfill(context.Background(), json.RawMessage(`{"if_needed":true}`), rep); err != nil {
			t.Fatalf("skipped=%t: %v", skipped, err)
		}
		want := 0
		if skipped {
			want = 1
		}
		if got := rep.noOpLines(); got != want {
			t.Errorf("skipped=%t: no-op tagged lines = %d, want %d", skipped, got, want)
		}
		if hasLevel(rep, slog.LevelError) {
			t.Errorf("skipped=%t: a clean run logged an Error line", skipped)
		}
	}
}

// A cancel mid-build (shutdown, an operator) returns promptly with ctx's
// error, logged as an interruption, never a failure or a completion.
func TestBookAtPathBackfill_IfNeededCancelIsAnInterruption(t *testing.T) {
	started := make(chan struct{})
	f := &fakeAtPathBuilder{backfill: func(ctx context.Context, _ database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
		close(started)
		<-ctx.Done()
		return database.BookAtPathBackfillResult{Scanned: 3}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	rep := &p6AttrReporter{}
	done := make(chan error, 1)
	go func() {
		done <- atPathPlugin(f).runBookAtPathIndexBackfill(ctx, json.RawMessage(`{"if_needed":true}`), rep)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("the build never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the build did not return within 2s of its ctx being cancelled")
	}
	if hasLevel(rep, slog.LevelError) {
		t.Error("a cancelled build logged an Error line; it is an interruption")
	}
	if !hasLevel(rep, slog.LevelInfo) || rep.noOpLines() != 0 {
		t.Errorf("lines = %+v, want an untagged Info line recording the interruption", rep.lines)
	}
}

// The def's Timeout ends ctx with DeadlineExceeded. A build cut by it leaves
// the sentinel unset: a failure (Error, red row), not an interruption.
func TestBookAtPathBackfill_IfNeededTimeoutIsAFailure(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	f := &fakeAtPathBuilder{backfill: func(ctx context.Context, _ database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
		return database.BookAtPathBackfillResult{}, fmt.Errorf("chunk commit: %w", ctx.Err())
	}}
	rep := &p6AttrReporter{}
	err := atPathPlugin(f).runBookAtPathIndexBackfill(ctx, json.RawMessage(`{"if_needed":true}`), rep)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline wrapped", err)
	}
	if !hasLevel(rep, slog.LevelError) {
		t.Error("a timed-out build logged no Error line")
	}
	for _, l := range rep.lines {
		if strings.Contains(l.msg, "interrupted") {
			t.Errorf("a timed-out build was logged as an interruption: %q", l.msg)
		}
	}
}

func TestBookAtPathBackfill_IfNeededStoreErrorFails(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeAtPathBuilder{backfill: func(context.Context, database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
		return database.BookAtPathBackfillResult{Scanned: 4}, boom
	}}
	rep := &p6AttrReporter{}
	err := atPathPlugin(f).runBookAtPathIndexBackfill(context.Background(), json.RawMessage(`{"if_needed":true}`), rep)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "scanned 4") {
		t.Fatalf("err = %v, want the store error wrapped with the scanned count", err)
	}
	if !hasLevel(rep, slog.LevelError) {
		t.Error("a failed build logged no Error line")
	}
}

// Rows that cannot be decoded get no key, so LiveBookIDsAtPath refuses while
// they stay: the boot build fails red like the forced rebuild, and names the
// op to run.
func TestBookAtPathBackfill_IfNeededUndecodableFails(t *testing.T) {
	f := &fakeAtPathBuilder{backfill: func(context.Context, database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
		return database.BookAtPathBackfillResult{Scanned: 9, UndecodableRows: 2, SampleUndecodable: []string{"01BADROW"}}, nil
	}}
	rep := &p6AttrReporter{}
	err := atPathPlugin(f).runBookAtPathIndexBackfill(context.Background(), json.RawMessage(`{"if_needed":true}`), rep)
	if err == nil || !strings.Contains(err.Error(), "2 book row(s) cannot be decoded") ||
		!strings.Contains(err.Error(), "maintenance.book-atpath-index-backfill") {
		t.Fatalf("err = %v, want the undecodable failure naming the op", err)
	}
	if !hasLevel(rep, slog.LevelError) {
		t.Error("no Error line for undecodable rows")
	}
}

// noAtPathIndexStore is an ops store with no book_atpath: index and no Unwrap
// (not Pebble). Its embedded Store is nil and never called.
type noAtPathIndexStore struct{ database.Store }

// Boot enqueues the build on every start; on a store with no index it must not
// fail red every boot. It logs a Warn tagged no-op and succeeds. An operator's
// rebuild still fails.
func TestBookAtPathBackfill_StoreWithoutIndex(t *testing.T) {
	p := New(fakeDeps{store: noAtPathIndexStore{}})
	rep := &p6AttrReporter{}
	if err := p.runBookAtPathIndexBackfill(context.Background(), json.RawMessage(`{"if_needed":true}`), rep); err != nil {
		t.Fatalf("if_needed on a store with no index failed: %v", err)
	}
	if !hasLevel(rep, slog.LevelWarn) || rep.noOpLines() != 1 {
		t.Fatalf("lines = %+v, want one Warn tagged no-op", rep.lines)
	}
	for _, raw := range []string{``, `{}`} {
		err := p.runBookAtPathIndexBackfill(context.Background(), json.RawMessage(raw), &fakeReporter{})
		if err == nil || !strings.Contains(err.Error(), "has no book_atpath: index") {
			t.Errorf("%q on a store with no index: err = %v, want it to fail as before", raw, err)
		}
	}
}

// A nil ops store fails in both modes rather than panicking.
func TestBookAtPathBackfill_NilStoreFails(t *testing.T) {
	p := New(fakeDeps{})
	for _, raw := range []string{`{}`, `{"if_needed":true}`} {
		if err := p.runBookAtPathIndexBackfill(context.Background(), json.RawMessage(raw), &fakeReporter{}); err == nil {
			t.Errorf("%s with no store: want an error", raw)
		}
	}
}

// The def declares LivenessManual, so the build must stamp UpdateProgress from
// the store's callbacks, not only at start and end: a first build on a large
// library otherwise sits silent past the watchdog's 5 minutes. Both modes.
func TestBookAtPathBackfill_ManualLivenessStamps(t *testing.T) {
	if def := (&Plugin{}).bookAtPathIndexBackfillDef(); def.Liveness != sdk.LivenessManual {
		t.Fatalf("liveness = %v, want manual", def.Liveness)
	}
	for _, raw := range []string{`{"if_needed":true}`, `{}`} {
		f := &fakeAtPathBuilder{}
		report := func(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
			for _, n := range []int{10_000, 20_000, 30_000} {
				progress(n)
			}
			return database.BookAtPathBackfillResult{Scanned: 30_000, Commits: 3}, nil
		}
		f.backfill, f.rebuild = report, report
		rep := &progressCountingReporter{}
		if err := atPathPlugin(f).runBookAtPathIndexBackfill(context.Background(), json.RawMessage(raw), rep); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		// start + three store callbacks + end.
		if rep.stamp < 5 {
			t.Fatalf("%s: UpdateProgress calls = %d, want at least 5 (start, each callback, end)", raw, rep.stamp)
		}
	}
}

// The boot run, end to end through a decorated OpsStore (OpsStore() can be a
// Store-embedding decorator that hides every *PebbleStore method outside
// database.Store): the build resolves through the decorator, builds the index
// and LiveBookIDsAtPath answers from it. Moved here from the server package's
// TestStoreExposesBookAtPathBackfill when the boot goroutine and its resolver
// were deleted: the op's own AsCapability call is now the production path.
func TestBookAtPathBackfill_IfNeededThroughStoreDecorator(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()
	wrapped := &opChangeDecorator{Store: inner}
	if _, ok := any(wrapped).(bookAtPathBackfiller); ok {
		t.Fatal("a bare assertion resolves bookAtPathBackfiller through the decorator; " +
			"this test no longer reproduces the decorator shape")
	}
	created, err := inner.CreateBook(&database.Book{Title: "At Path", FilePath: "/lib/a"})
	if err != nil {
		t.Fatal(err)
	}
	rep := &p6AttrReporter{}
	if err := New(fakeDeps{store: wrapped}).runBookAtPathIndexBackfill(context.Background(),
		json.RawMessage(`{"if_needed":true}`), rep); err != nil {
		t.Fatalf("if_needed through the decorator: %v", err)
	}
	if rep.noOpLines() != 0 {
		t.Fatal("a first build was tagged no-op")
	}
	ids, err := wrapped.LiveBookIDsAtPath("/lib/a")
	if err != nil || len(ids) != 1 || ids[0] != created.ID {
		t.Fatalf("LiveBookIDsAtPath = %v, %v; want [%s]", ids, err, created.ID)
	}
	// The next boot: the sentinel is set, so a tagged no-op.
	rep = &p6AttrReporter{}
	if err := New(fakeDeps{store: wrapped}).runBookAtPathIndexBackfill(context.Background(),
		json.RawMessage(`{"if_needed":true}`), rep); err != nil {
		t.Fatal(err)
	}
	if rep.noOpLines() != 1 {
		t.Fatalf("a second if_needed run over a built index: lines = %+v, want one tagged no-op", rep.lines)
	}
}
