// file: internal/plugins/maintenance/opchange_book_index_test.go
// version: 1.4.0
// guid: 58b6fa0d-49f4-40bf-b1fe-a55af85a5098
// last-edited: 2026-10-15

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

type fakeOpChangeRebuilder struct {
	res     database.OpChangeByBookBackfillResult
	err     error
	rep     database.OpChangeByBookIndexReport
	rebuilt *bool
	// ensure is EnsureOpChangeByBookIndex's answer; nil records the call
	// in ensured and returns the zero result.
	ensure  func(ctx context.Context) (database.OpChangeByBookEnsureResult, error)
	ensured *bool
}

func (f fakeOpChangeRebuilder) EnsureOpChangeByBookIndex(ctx context.Context, _ database.OpChangeIndexProgress) (database.OpChangeByBookEnsureResult, error) {
	if f.ensured != nil {
		*f.ensured = true
	}
	if f.ensure == nil {
		return database.OpChangeByBookEnsureResult{}, nil
	}
	return f.ensure(ctx)
}

// opChangeFakeStore puts a fakeOpChangeRebuilder behind fakeDeps.OpsStore, so
// a test can drive runOpChangeBookIndexRebuild (params decode, mode switch,
// capability resolution) without a real store. The embedded Store is nil and
// is never called.
type opChangeFakeStore struct {
	database.Store
	fakeOpChangeRebuilder
}

func opChangePlugin(f fakeOpChangeRebuilder) *Plugin {
	return New(fakeDeps{store: opChangeFakeStore{fakeOpChangeRebuilder: f}})
}

// trustedNoOpEnsure is the usual boot outcome: backfill already done, verify
// clean, nothing rebuilt.
func trustedNoOpEnsure(context.Context) (database.OpChangeByBookEnsureResult, error) {
	return database.OpChangeByBookEnsureResult{
		Backfill: database.OpChangeByBookBackfillResult{Skipped: true},
		Verify:   database.OpChangeByBookIndexReport{SentinelSet: true, Rows: 4, Indexable: 4},
		Trusted:  true,
	}, nil
}

func (f fakeOpChangeRebuilder) VerifyOpChangeByBookIndex(context.Context, database.OpChangeIndexProgress) (database.OpChangeByBookIndexReport, error) {
	return f.rep, nil
}

func (f fakeOpChangeRebuilder) RebuildOpChangeByBookIndex(context.Context, database.OpChangeIndexProgress) (database.OpChangeByBookBackfillResult, error) {
	if f.rebuilt != nil {
		*f.rebuilt = true
	}
	return f.res, f.err
}

func TestOpchangeBookIndexRebuild_PreviewWritesNothing(t *testing.T) {
	rebuilt := false
	r := fakeOpChangeRebuilder{rep: database.OpChangeByBookIndexReport{Rows: 5, MissingEntries: 2}, rebuilt: &rebuilt}
	if err := previewOpChangeBookIndex(context.Background(), r, &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	if rebuilt {
		t.Fatal("preview rebuilt the index")
	}
}

func TestOpchangeBookIndexRebuild_CleanRunPasses(t *testing.T) {
	r := fakeOpChangeRebuilder{res: database.OpChangeByBookBackfillResult{Scanned: 10, Indexed: 9, Commits: 2}}
	if err := rebuildOpChangeBookIndex(context.Background(), r, &fakeReporter{}); err != nil {
		t.Fatalf("clean rebuild failed: %v", err)
	}
}

func TestOpchangeBookIndexRebuild_UndecodableFails(t *testing.T) {
	r := fakeOpChangeRebuilder{res: database.OpChangeByBookBackfillResult{Scanned: 10, Indexed: 8, Undecodable: 1}}
	err := rebuildOpChangeBookIndex(context.Background(), r, &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "cannot be decoded") {
		t.Fatalf("undecodable rows must fail the op, got %v", err)
	}
}

func TestOpchangeBookIndexRebuild_ErrorWrapped(t *testing.T) {
	boom := errors.New("boom")
	r := fakeOpChangeRebuilder{res: database.OpChangeByBookBackfillResult{Scanned: 4}, err: boom}
	err := rebuildOpChangeBookIndex(context.Background(), r, &fakeReporter{})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "full scan") {
		t.Fatalf("err = %v, want the wrapped cause and the fallback note", err)
	}
}

// TestOpchangeBookIndexRebuild_RealStore runs the op body against a real
// Pebble store and checks the rebuilt index serves GetBookChanges.
func TestOpchangeBookIndexRebuild_RealStore(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateOperationChange(&database.OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	r, ok := database.AsCapability[opChangeIndexRebuilder](store)
	if !ok {
		t.Fatal("*PebbleStore does not satisfy opChangeIndexRebuilder")
	}
	if err := rebuildOpChangeBookIndex(context.Background(), r, &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBookChanges("b1")
	if err != nil || len(got) != 1 {
		t.Fatalf("GetBookChanges after rebuild = %v, %v", got, err)
	}
}

// progressCountingReporter counts UpdateProgress calls (the liveness stamps a
// LivenessManual op owes).
type progressCountingReporter struct {
	fakeReporter
	mu    sync.Mutex
	stamp int
}

func (r *progressCountingReporter) UpdateProgress(_, _ int, _ string) error {
	r.mu.Lock()
	r.stamp++
	r.mu.Unlock()
	return nil
}

// TestOpchangeBookIndexRebuild_ManualLivenessStamps: the op declares
// LivenessManual, so the rebuild must stamp UpdateProgress from the store's
// per-chunk callback, not only at start and end.
func TestOpchangeBookIndexRebuild_ManualLivenessStamps(t *testing.T) {
	if def := (&Plugin{}).opChangeBookIndexRebuildDef(); def.Liveness != sdk.LivenessManual {
		t.Fatalf("liveness = %v, want manual", def.Liveness)
	}
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateOperationChange(&database.OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	rep := &progressCountingReporter{}
	r, _ := database.AsCapability[opChangeIndexRebuilder](store)
	if err := rebuildOpChangeBookIndex(context.Background(), r, rep); err != nil {
		t.Fatal(err)
	}
	// start + one chunk + end.
	if rep.stamp < 3 {
		t.Fatalf("UpdateProgress calls = %d, want at least 3 (start, per chunk, end)", rep.stamp)
	}
}

// --- ensure mode (04-P7) ---

// Test 1: with no mode, the op previews whatever the params look like: no
// rebuild, no ensure. ensure:false is the same as omitting it.
func TestOpchangeBookIndexOp_OmittedModePreviews(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"ensure":false}`} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			rebuilt, ensured := false, false
			p := opChangePlugin(fakeOpChangeRebuilder{rebuilt: &rebuilt, ensured: &ensured,
				rep: database.OpChangeByBookIndexReport{Rows: 3, MissingEntries: 1}})
			if err := p.runOpChangeBookIndexRebuild(context.Background(), json.RawMessage(raw), &fakeReporter{}); err != nil {
				t.Fatal(err)
			}
			if rebuilt || ensured {
				t.Fatalf("params %q wrote: rebuilt=%t ensured=%t, want a preview", raw, rebuilt, ensured)
			}
		})
	}
}

// ensure picks the ensure path and nothing else; combined with dry_run in
// either spelling or value it is refused before anything runs.
func TestOpchangeBookIndexOp_EnsureModeSelection(t *testing.T) {
	rebuilt, ensured := false, false
	p := opChangePlugin(fakeOpChangeRebuilder{rebuilt: &rebuilt, ensured: &ensured, ensure: trustedNoOpEnsure})
	if err := p.runOpChangeBookIndexRebuild(context.Background(), json.RawMessage(`{"ensure":true}`), &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	if !ensured || rebuilt {
		t.Fatalf("ensure=true: ensured=%t rebuilt=%t, want the ensure only", ensured, rebuilt)
	}
	for _, raw := range []string{`{"ensure":true,"dry_run":true}`, `{"ensure":true,"dry_run":false}`, `{"ensure":true,"dryRun":false}`} {
		rebuilt, ensured = false, false
		err := p.runOpChangeBookIndexRebuild(context.Background(), json.RawMessage(raw), &fakeReporter{})
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Errorf("%s: err = %v, want the conflict refused", raw, err)
		}
		if rebuilt || ensured {
			t.Errorf("%s: ran (ensured=%t rebuilt=%t) despite the conflict", raw, ensured, rebuilt)
		}
	}
}

// Test 2: a dirty verify the ensure answered with a rebuild is a success,
// logged as real work (not tagged no-op).
func TestOpchangeBookIndexEnsure_DirtyVerifyRebuiltPasses(t *testing.T) {
	rep := &p6AttrReporter{}
	r := fakeOpChangeRebuilder{ensure: func(context.Context) (database.OpChangeByBookEnsureResult, error) {
		return database.OpChangeByBookEnsureResult{
			Backfill: database.OpChangeByBookBackfillResult{Skipped: true},
			Verify:   database.OpChangeByBookIndexReport{SentinelSet: true, Rows: 5, MissingEntries: 2},
			Rebuilt:  true,
			Rebuild:  database.OpChangeByBookBackfillResult{Scanned: 5, Indexed: 5},
			Trusted:  true,
		}, nil
	}}
	if err := ensureOpChangeBookIndex(context.Background(), r, rep); err != nil {
		t.Fatalf("a rebuilt, trusted index failed the op: %v", err)
	}
	if rep.noOpLines() != 0 {
		t.Fatal("a run that rebuilt the index was tagged no-op")
	}
}

// Test 3: an ensure that ends without trust, or fails, fails the op with an
// Error line: a red row, not a quiet completion.
func TestOpchangeBookIndexEnsure_UntrustedFails(t *testing.T) {
	boom := errors.New("synthetic verify failure")
	for name, ensure := range map[string]func(context.Context) (database.OpChangeByBookEnsureResult, error){
		"rebuild did not fix it": func(context.Context) (database.OpChangeByBookEnsureResult, error) {
			return database.OpChangeByBookEnsureResult{
				Verify:  database.OpChangeByBookIndexReport{SentinelSet: true, MissingEntries: 3},
				Rebuilt: true,
			}, nil
		},
		"store error": func(context.Context) (database.OpChangeByBookEnsureResult, error) {
			return database.OpChangeByBookEnsureResult{}, boom
		},
	} {
		t.Run(name, func(t *testing.T) {
			rep := &p6AttrReporter{}
			err := ensureOpChangeBookIndex(context.Background(), fakeOpChangeRebuilder{ensure: ensure}, rep)
			if err == nil {
				t.Fatal("an untrusted index completed the op")
			}
			if name == "store error" && !errors.Is(err, boom) {
				t.Errorf("err = %v, want the store's cause wrapped", err)
			}
			if !hasLevel(rep, slog.LevelError) {
				t.Error("no Error line for an untrusted index")
			}
		})
	}
}

// Undecodable rows: the index is trusted, but GetBookChanges fails for every
// book, so the op fails as the rebuild mode does.
func TestOpchangeBookIndexEnsure_UndecodableFails(t *testing.T) {
	r := fakeOpChangeRebuilder{ensure: func(context.Context) (database.OpChangeByBookEnsureResult, error) {
		return database.OpChangeByBookEnsureResult{
			Backfill: database.OpChangeByBookBackfillResult{Skipped: true},
			Verify:   database.OpChangeByBookIndexReport{SentinelSet: true, Rows: 3, Undecodable: 1},
			Trusted:  true,
		}, nil
	}}
	err := ensureOpChangeBookIndex(context.Background(), r, &p6AttrReporter{})
	if err == nil || !strings.Contains(err.Error(), "cannot be decoded") {
		t.Fatalf("err = %v, want the undecodable rows to fail the op", err)
	}
}

// After a rebuild, the rebuild's own undecodable count is the current one:
// the verify ran before the rebuild marked anything, and here it saw none.
func TestOpchangeBookIndexEnsure_RebuildUndecodableFails(t *testing.T) {
	r := fakeOpChangeRebuilder{ensure: func(context.Context) (database.OpChangeByBookEnsureResult, error) {
		return database.OpChangeByBookEnsureResult{
			Verify:  database.OpChangeByBookIndexReport{SentinelSet: true, Rows: 3, MissingEntries: 1},
			Rebuilt: true,
			Rebuild: database.OpChangeByBookBackfillResult{Scanned: 3, Indexed: 2, Undecodable: 1},
			Trusted: true,
		}, nil
	}}
	err := ensureOpChangeBookIndex(context.Background(), r, &p6AttrReporter{})
	if err == nil || !strings.Contains(err.Error(), "1 journal row(s) cannot be decoded") {
		t.Fatalf("err = %v, want the rebuild's undecodable row to fail the op", err)
	}
}

// The usual boot outcome (already built, verify clean) logs no untagged Info
// line, so it adds nothing to the default activity feed.
func TestOpchangeBookIndexEnsure_AlreadyTrustedIsNoOp(t *testing.T) {
	rep := &p6AttrReporter{}
	if err := ensureOpChangeBookIndex(context.Background(), fakeOpChangeRebuilder{ensure: trustedNoOpEnsure}, rep); err != nil {
		t.Fatal(err)
	}
	if rep.noOpLines() == 0 {
		t.Fatal("no no-op tagged summary")
	}
	for _, l := range rep.lines {
		if l.level >= slog.LevelInfo && !l.noOp {
			t.Errorf("no-op ensure logged an untagged %s line %q", l.level, l.msg)
		}
	}
}

// Test 4: a cancel mid-run returns promptly with ctx's error, logged as an
// interruption (Info), never as a failure or a completion.
func TestOpchangeBookIndexEnsure_CancelIsAnInterruption(t *testing.T) {
	started := make(chan struct{})
	r := fakeOpChangeRebuilder{ensure: func(ctx context.Context) (database.OpChangeByBookEnsureResult, error) {
		close(started)
		<-ctx.Done()
		// The store wraps ctx's error (lockOpChangeIdxRun, the backfill).
		return database.OpChangeByBookEnsureResult{}, fmt.Errorf("waiting for another opchange_by_book index pass: %w", ctx.Err())
	}}
	ctx, cancel := context.WithCancel(context.Background())
	rep := &p6AttrReporter{}
	done := make(chan error, 1)
	go func() { done <- ensureOpChangeBookIndex(ctx, r, rep) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ensure did not return within 2s of its ctx being cancelled")
	}
	if hasLevel(rep, slog.LevelError) {
		t.Error("a cancelled ensure logged an Error line; it is an interruption")
	}
	for _, l := range rep.lines {
		if strings.Contains(l.msg, "trusted") && !strings.Contains(l.msg, "interrupted") {
			t.Errorf("a cancelled ensure logged %q", l.msg)
		}
	}
	if !hasLevel(rep, slog.LevelInfo) {
		t.Error("no Info line recording the interruption")
	}
}

// Step 4: a boot ensure and a manual rebuild never run at once. Both are this
// def (one ConcurrencyKey in the registry); underneath, the store's index-pass
// slot holds the ensure until the rebuild returns. Real store: a rebuild
// parked inside its first chunk callback holds the slot.
func TestOpchangeBookIndexEnsure_WaitsForAManualRebuild(t *testing.T) {
	def := (&Plugin{}).opChangeBookIndexRebuildDef()
	if def.ConcurrencyKey == "" {
		t.Fatal("the def has no ConcurrencyKey; a boot ensure and a manual rebuild could be dispatched together")
	}
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateOperationChange(&database.OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	inRebuild, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	rebuildDone := make(chan error, 1)
	go func() {
		_, err := store.RebuildOpChangeByBookIndex(context.Background(), func(phase string, _ int) {
			if phase == "rebuild" {
				once.Do(func() { close(inRebuild); <-release })
			}
		})
		rebuildDone <- err
	}()
	<-inRebuild

	p := New(fakeDeps{store: store})
	ensureDone := make(chan error, 1)
	go func() {
		ensureDone <- p.runOpChangeBookIndexRebuild(context.Background(), json.RawMessage(`{"ensure":true}`), &fakeReporter{})
	}()
	select {
	case err := <-ensureDone:
		t.Fatalf("the ensure finished (%v) while a manual rebuild held the index slot", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-rebuildDone; err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	select {
	case err := <-ensureDone:
		if err != nil {
			t.Fatalf("ensure after the rebuild: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the ensure never ran after the rebuild released the slot")
	}
	if !store.OpChangeByBookIndexUsable() {
		t.Fatal("index not usable after the rebuild and the ensure")
	}
}

// The same, cancelled while it waits: it returns promptly, as on shutdown.
func TestOpchangeBookIndexEnsure_CancelWhileWaitingReturnsPromptly(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateOperationChange(&database.OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	inRebuild, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	rebuildDone := make(chan error, 1)
	go func() {
		_, err := store.RebuildOpChangeByBookIndex(context.Background(), func(phase string, _ int) {
			if phase == "rebuild" {
				once.Do(func() { close(inRebuild); <-release })
			}
		})
		rebuildDone <- err
	}()
	<-inRebuild
	defer func() { close(release); <-rebuildDone }()

	ctx, cancel := context.WithCancel(context.Background())
	rep := &p6AttrReporter{}
	ensureDone := make(chan error, 1)
	go func() {
		ensureDone <- New(fakeDeps{store: store}).runOpChangeBookIndexRebuild(ctx, json.RawMessage(`{"ensure":true}`), rep)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-ensureDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting ensure did not return within 2s of its ctx being cancelled")
	}
	if hasLevel(rep, slog.LevelError) {
		t.Error("a cancelled ensure logged an Error line")
	}
}

// The def's Timeout ends ctx with DeadlineExceeded. A run cut by it leaves the
// index untrusted, so it is a failure (Error, red row), not an interruption.
func TestOpchangeBookIndexEnsure_TimeoutIsAFailure(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r := fakeOpChangeRebuilder{ensure: func(ctx context.Context) (database.OpChangeByBookEnsureResult, error) {
		return database.OpChangeByBookEnsureResult{}, fmt.Errorf("opchange_by_book backfill: %w", ctx.Err())
	}}
	rep := &p6AttrReporter{}
	err := ensureOpChangeBookIndex(ctx, r, rep)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline wrapped", err)
	}
	if !hasLevel(rep, slog.LevelError) {
		t.Error("a timed-out ensure logged no Error line")
	}
	for _, l := range rep.lines {
		if strings.Contains(l.msg, "interrupted") {
			t.Errorf("a timed-out ensure was logged as an interruption: %q", l.msg)
		}
	}
}

// opChangeDecorator embeds the Store INTERFACE, as a store decorator does, so
// only database.Store's methods are promoted; Unwrap is how AsCapability sees
// through it. Moved here from the server package's
// TestStoreExposesOpChangeIndexBackfill when the boot goroutine and its
// resolver were deleted: the op's own resolution is now the production path.
type opChangeDecorator struct{ database.Store }

func (d *opChangeDecorator) Unwrap() database.Store { return d.Store }

// The boot run, end to end through a decorated OpsStore: the ensure resolves
// through the decorator, builds and verifies the index, trusts it, and
// GetBookChanges through the decorator reads it.
func TestOpchangeBookIndexEnsure_ThroughStoreDecorator(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()
	wrapped := &opChangeDecorator{Store: inner}
	if _, ok := any(wrapped).(opChangeIndexRebuilder); ok {
		t.Fatal("a bare assertion resolves opChangeIndexRebuilder through the decorator; " +
			"this test no longer reproduces the decorator shape")
	}
	if err := inner.CreateOperationChange(&database.OperationChange{
		OperationID: "op1", BookID: "book-a", ChangeType: "metadata_update", FieldName: "title",
	}); err != nil {
		t.Fatal(err)
	}
	if inner.OpChangeByBookIndexUsable() {
		t.Fatal("index usable before any ensure; the test proves nothing")
	}
	rep := &p6AttrReporter{}
	if err := New(fakeDeps{store: wrapped}).runOpChangeBookIndexRebuild(context.Background(),
		json.RawMessage(`{"ensure":true}`), rep); err != nil {
		t.Fatalf("ensure through the decorator: %v", err)
	}
	if !inner.OpChangeByBookIndexUsable() {
		t.Fatal("the ensure ran but the index is not usable")
	}
	if rep.noOpLines() != 0 {
		t.Fatal("a first build was tagged no-op")
	}
	got, err := wrapped.GetBookChanges("book-a")
	if err != nil || len(got) != 1 || got[0].OperationID != "op1" {
		t.Fatalf("GetBookChanges through the decorator = %+v, %v; want the one op1 row", got, err)
	}
	// A second boot's run: built and clean, so a tagged no-op.
	rep = &p6AttrReporter{}
	if err := New(fakeDeps{store: wrapped}).runOpChangeBookIndexRebuild(context.Background(),
		json.RawMessage(`{"ensure":true}`), rep); err != nil {
		t.Fatal(err)
	}
	if rep.noOpLines() == 0 {
		t.Fatal("a second ensure over a clean index was not tagged no-op")
	}
}

func hasLevel(rep *p6AttrReporter, level slog.Level) bool {
	rep.mu.Lock()
	defer rep.mu.Unlock()
	for _, l := range rep.lines {
		if l.level == level {
			return true
		}
	}
	return false
}
