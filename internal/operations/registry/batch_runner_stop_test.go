// file: internal/operations/registry/batch_runner_stop_test.go
// version: 1.4.0
// guid: c8ee28eb-1a14-43aa-9e77-8473ae796708
// last-edited: 2026-10-14

package registry

// How a batch run stops (05-PR5 review): a fenced write ends the run instead
// of counting as a failure, a completed chunk is delivered even while the run
// stops, a panicking item is an item error, a shutdown of a resumable run
// skips Finish, an interrupted item never completes its chunk, and a Fixer's
// applied rows and an AppendOnly run's chunk count survive a resume.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The reviewer's probe: 40 items, chunk 10, one worker, every item from 15
// on refused with ErrFenced. Before the fix this ended err=nil, Done=40,
// ledger [0 1 2 3] -- a fenced run recorded "completed".
func TestFencedItemStopsRunWithoutRecordingChunk(t *testing.T) {
	store := newMemLedgerStore(t)
	job, rc, cancel := openJob(t, seq(40), batchOpts{size: 10, workers: 1}, func(_ *ops.ItemRC, i int) error {
		if i >= 15 {
			return fmt.Errorf("write refused: %w", ops.ErrFenced)
		}
		return nil
	})
	defer cancel()
	sum, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
	if !errors.Is(err, ops.ErrFenced) {
		t.Fatalf("err = %v, want the run to end fenced", err)
	}
	led, _ := store.GetLedgerV3("op")
	if !slices.Equal(bitsSet(*led), []int{0}) || sum.Done != 10 || sum.Failed != 0 {
		t.Fatalf("ledger %v done %d failed %d: the fenced chunk must stay unfinished", bitsSet(*led), sum.Done, sum.Failed)
	}
}

// A fence revoked between items (the registry's revoke, no item error at
// all) stops the run before the next item.
func TestFenceRevokedBetweenItemsStopsRun(t *testing.T) {
	store := newMemLedgerStore(t)
	fence, revoke := opswriter.NewFence(1, nil)
	var after atomic.Int64
	var revoked atomic.Bool
	job, rc, cancel := openJob(t, seq(40), batchOpts{size: 10, workers: 1}, func(_ *ops.ItemRC, i int) error {
		if revoked.Load() {
			after.Add(1)
		}
		if i == 14 {
			revoked.Store(true)
			revoke()
		}
		return nil
	})
	defer cancel()
	ctx := opswriter.WithFence(rc.Context(), fence)
	_, _, err := runBatch(ctx, batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
	if !errors.Is(err, ops.ErrFenced) || after.Load() != 0 {
		t.Fatalf("err %v, %d items ran after the revoke", err, after.Load())
	}
	led, _ := store.GetLedgerV3("op")
	if !slices.Equal(bitsSet(*led), []int{0}) {
		t.Fatalf("ledger %v, want [0]", bitsSet(*led))
	}
}

// The window between the registry's revoke and its cancel: the last item of
// chunk 0 sees its write refused, swallows it and returns nil, and the cancel
// lands a moment later. The chunk is not recorded (the fence is checked after
// every item, not only after a failed one), and the Summary Finish gets says
// Canceled (it is built after the runner waits for the cancel to land).
func TestRevokeBeforeCancelWindow(t *testing.T) {
	store := newMemLedgerStore(t)
	fence, revoke := opswriter.NewFence(1, nil)
	w := previewWriter(fence)
	var sum atomic.Pointer[ops.Summary]
	var stopRun context.CancelFunc
	job, rc, cancel := openJob(t, seq(20), batchOpts{size: 10, workers: 1,
		finish: func(_ *ops.RC, s ops.Summary) { sum.Store(&s) }},
		func(irc *ops.ItemRC, i int) error {
			if i == 9 {
				revoke()
				go func() { time.Sleep(30 * time.Millisecond); stopRun() }()
				_ = tryWrite(irc, w) // refused, and swallowed by the item
			}
			return nil
		})
	stopRun = cancel
	defer cancel()
	ctx := opswriter.WithFence(rc.Context(), fence)
	_, _, err := runBatch(ctx, batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
	if !errors.Is(err, ops.ErrFenced) {
		t.Fatalf("err = %v, want ErrFenced", err)
	}
	if led, _ := store.GetLedgerV3("op"); led != nil && len(bitsSet(*led)) != 0 {
		t.Fatalf("ledger %v: the chunk whose last item ran fenced was recorded", bitsSet(*led))
	}
	if s := sum.Load(); s == nil || !s.Canceled {
		t.Fatalf("Finish summary %+v: want Canceled", s)
	}
}

// nopBooks is a book store nothing is ever written to: previewWriter's
// writes stop at the fence check or the plan line.
type nopBooks struct{}

func (nopBooks) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	return nil, errors.New("nopBooks: unexpected write")
}

// previewWriter is a real opswriter.Writer on fence, in Preview mode: a write
// is refused once the fence is revoked (and flagged on the item's outcome,
// ops.Tally.Refused), and otherwise records a plan line and returns
// ErrPreview.
func previewWriter(f opswriter.Fence) *opswriter.Writer {
	return opswriter.New(opswriter.Config{OpID: "op", Mode: ops.Preview, Books: nopBooks{}, Fence: f.Check})
}

// runPreviewWriter is previewWriter built the way runV3 builds the run's
// Writer: with the run's refusal latch, so a refused write made with a
// context that is not the item's still reaches the runner.
func runPreviewWriter(rc *ops.RC, f opswriter.Fence) *opswriter.Writer {
	return opswriter.New(opswriter.Config{OpID: "op", Mode: ops.Preview, Books: nopBooks{}, Fence: f.Check,
		RunLatch: ops.RefusalLatchFrom(rc.Context())})
}

// S-A (final review): a refused write made with a context that does not
// descend from the item's -- the run-level rc an op closure captured, or
// context.Background() -- cannot be flagged on the item, so it is latched on
// the run and the runner fails closed after the item: the chunk whose last
// item swallowed it at a registry stop is not recorded.
func TestUnattributedRefusalFailsRun(t *testing.T) {
	for _, name := range []string{"run_rc", "background"} {
		t.Run(name, func(t *testing.T) {
			for range 20 {
				store := newMemLedgerStore(t)
				fence, revoke := opswriter.NewFence(1, nil)
				var w atomic.Pointer[opswriter.Writer]
				var runRC atomic.Pointer[ops.RC]
				var stopRun context.CancelFunc
				var refused atomic.Bool
				job, rc, cancel := openJob(t, seq(10), batchOpts{size: 5, workers: 1}, func(_ *ops.ItemRC, i int) error {
					if i == 4 {
						revoke()
						stopRun()
						ctx := context.Background()
						if name == "run_rc" {
							ctx = runRC.Load().Context()
						}
						_, err := w.Load().ModifyBook(ctx, "b1", func(*database.Book) error { return nil })
						refused.Store(errors.Is(err, ops.ErrFenced))
					}
					return nil // swallowed
				})
				stopRun = cancel
				runRC.Store(rc)
				w.Store(runPreviewWriter(rc, fence))
				ctx := opswriter.WithFence(rc.Context(), fence)
				_, _, err := runBatch(ctx, batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
				cancel()
				if !refused.Load() {
					t.Fatal("setup: the write was not refused")
				}
				if !errors.Is(err, ops.ErrFenced) {
					t.Fatalf("err = %v, want ErrFenced", err)
				}
				if led, _ := store.GetLedgerV3("op"); led != nil && slices.Contains(bitsSet(*led), 0) {
					t.Fatal("a chunk whose item swallowed a refused write on a non-item context was recorded")
				}
			}
		})
	}
}

// N-B (final review): a write a goroutine makes on its item's context after
// the item returned is refused (ErrItemReturned), and with the run not
// stopped it neither fails the run nor lands on the next item's outcome.
func TestLateWriteDoesNotFlagNextItem(t *testing.T) {
	store := newMemLedgerStore(t)
	fence, _ := opswriter.NewFence(1, nil)
	var w atomic.Pointer[opswriter.Writer]
	lateErr := make(chan error, 1)
	job, rc, cancel := openJob(t, seq(4), batchOpts{size: 4, workers: 1, timeout: time.Minute}, func(irc *ops.ItemRC, i int) error {
		switch i {
		case 0:
			ictx := irc.Context()
			go func() {
				time.Sleep(20 * time.Millisecond)
				_, err := w.Load().ModifyBook(ictx, "b1", func(*database.Book) error { return nil })
				lateErr <- err
			}()
		case 1:
			time.Sleep(80 * time.Millisecond)
		}
		return nil
	})
	defer cancel()
	w.Store(runPreviewWriter(rc, fence))
	ctx := opswriter.WithFence(rc.Context(), fence)
	_, _, err := runBatch(ctx, batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
	if err != nil {
		t.Fatalf("a late write on a returned item's context failed the run: %v", err)
	}
	if le := <-lateErr; !errors.Is(le, ops.ErrItemReturned) {
		t.Fatalf("late write err = %v, want ErrItemReturned", le)
	}
}

// tryWrite makes one write through w with the item's context.
func tryWrite(irc *ops.ItemRC, w *opswriter.Writer) error {
	_, err := w.ModifyBook(irc.Context(), "b1", func(*database.Book) error { return nil })
	return err
}

// The fenced counterpart of TestCompletedChunkDeliveredWhenRunStops, at a
// registry stop (revoke, then cancel) during a chunk's last item:
//   - the item finished without a refused write: it settled, so the chunk
//     is recorded and a resume does not redo it (S1 holds under a fence);
//   - the item's write was refused and the item swallowed the error: the
//     Writer noted the refusal, so the chunk is dropped and the run fails.
func TestFencedStopChunkDelivery(t *testing.T) {
	for _, swallow := range []bool{false, true} {
		t.Run(fmt.Sprint("swallowed_refusal=", swallow), func(t *testing.T) {
			for range 20 {
				store := newMemLedgerStore(t)
				fence, revoke := opswriter.NewFence(1, nil)
				w := previewWriter(fence)
				var stopRun context.CancelFunc
				job, rc, cancel := openJob(t, seq(10), batchOpts{size: 5, workers: 1}, func(irc *ops.ItemRC, i int) error {
					if i == 4 {
						revoke() // the registry's order: revoke, then cancel
						stopRun()
						if swallow {
							_ = tryWrite(irc, w)
						}
					}
					return nil
				})
				stopRun = cancel
				ctx := opswriter.WithFence(rc.Context(), fence)
				_, _, err := runBatch(ctx, batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
				cancel()
				led, _ := store.GetLedgerV3("op")
				recorded := led != nil && slices.Contains(bitsSet(*led), 0)
				if swallow {
					if !errors.Is(err, ops.ErrFenced) || recorded {
						t.Fatalf("swallowed refusal: err %v, chunk recorded %v; want ErrFenced and dropped", err, recorded)
					}
				} else if err == nil || !recorded {
					t.Fatalf("clean chunk at a fenced stop: err %v, chunk recorded %v; want a stop error and recorded", err, recorded)
				}
			}
		})
	}
}

// S1: a chunk whose every item settled is delivered even when the run stops
// right then. The old select dropped it about half the time.
func TestCompletedChunkDeliveredWhenRunStops(t *testing.T) {
	for range 40 {
		store := newMemLedgerStore(t)
		var stopRun context.CancelFunc
		job, rc, cancel := openJob(t, seq(10), batchOpts{size: 5, workers: 1}, func(_ *ops.ItemRC, i int) error {
			if i == 4 {
				stopRun() // the last item of chunk 0 completes as the run stops
			}
			return nil
		})
		stopRun = cancel
		_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		led, _ := store.GetLedgerV3("op")
		if !slices.Contains(bitsSet(*led), 0) {
			t.Fatal("a completed chunk was dropped because the run was stopping")
		}
	}
}

// S2: a panic in Item is that item's error, not a crashed server.
func TestItemPanicIsAnItemError(t *testing.T) {
	job, rc, cancel := openJob(t, seq(30), batchOpts{size: 10, workers: 3}, func(_ *ops.ItemRC, i int) error {
		if i == 7 {
			panic("item 7 exploded")
		}
		return nil
	})
	defer cancel()
	sum, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, commitEvery: -1})
	if err != nil || sum.Done != 30 || sum.Failed != 1 {
		t.Fatalf("err %v done %d failed %d", err, sum.Done, sum.Failed)
	}
}

// panicKeySet is an ItemSet whose Key panics at one index.
type panicKeySet struct {
	ops.ItemSet
	at int
}

func (p panicKeySet) Key(i int) string {
	if i == p.at {
		panic("key exploded")
	}
	return p.ItemSet.Key(i)
}

// S2 (re-review): a panic in an op callback that runs on the runner's own
// goroutines -- Label in a worker, Pages or Key in the producer -- fails the
// run with an error instead of crashing the process, and records no chunk
// that did not finish.
func TestCallbackPanicFailsRun(t *testing.T) {
	cases := map[string]struct {
		opts batchOpts
		wrap func(*ops.BatchJob)
	}{
		"label in worker": {opts: batchOpts{size: 5, workers: 2, label: func(i int) string {
			if i == 12 {
				panic("label exploded")
			}
			return itemKey(i)
		}}},
		"pages in producer": {opts: batchOpts{size: 5, workers: 2, order: ops.AppendOnly}, wrap: func(j *ops.BatchJob) {
			pages := j.Pages
			j.Pages = func(ctx context.Context, after string, limit int) (ops.ItemSet, string, error) {
				if after != "" {
					panic("pages exploded")
				}
				return pages(ctx, after, limit)
			}
		}},
		"key in producer": {opts: batchOpts{size: 5, workers: 2, order: ops.AppendOnly}, wrap: func(j *ops.BatchJob) {
			pages := j.Pages
			j.Pages = func(ctx context.Context, after string, limit int) (ops.ItemSet, string, error) {
				set, next, err := pages(ctx, after, limit)
				if after != "" && set != nil {
					set = panicKeySet{ItemSet: set, at: 2}
				}
				return set, next, err
			}
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			store := newMemLedgerStore(t)
			job, rc, cancel := openJob(t, seq(30), c.opts, func(*ops.ItemRC, int) error { return nil })
			defer cancel()
			if c.wrap != nil {
				c.wrap(job)
			}
			sum, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
			if err == nil || !strings.Contains(err.Error(), "panicked") {
				t.Fatalf("err = %v, want a panicked error", err)
			}
			if sum.Done == 30 {
				t.Fatal("a run whose callback panicked counted every item")
			}
		})
	}
}

// S5: a Shutdown of a resumable run skips Finish; the resumed attempt runs it.
// A user cancel, and a shutdown of a run that will not resume, still run it.
func TestShutdownSkipsFinishOnlyWhenResumable(t *testing.T) {
	cases := []struct {
		name      string
		cause     error
		resumable bool
		finish    bool
	}{
		{"shutdown, resumable", ErrShutdown, true, false},
		{"shutdown, dropped on resume", ErrShutdown, false, true},
		{"user cancel, resumable", nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var finished atomic.Bool
			ctx, stop := context.WithCancelCause(context.Background())
			defer stop(nil)
			job, rc, cancel := openJob(t, seq(20), batchOpts{size: 5, workers: 1,
				finish: func(*ops.RC, ops.Summary) { finished.Store(true) }},
				func(irc *ops.ItemRC, i int) error {
					if i == 7 {
						stop(tc.cause)
						<-irc.Context().Done()
						return irc.Context().Err()
					}
					return nil
				})
			defer cancel()
			resumes := func() bool { return tc.resumable && errors.Is(context.Cause(ctx), ErrShutdown) }
			_, _, err := runBatch(ctx, batchConfig{opID: "op", job: job, rc: rc, commitEvery: -1, resumes: resumes})
			if err == nil {
				t.Fatal("a stopped run returned nil")
			}
			if finished.Load() != tc.finish {
				t.Fatalf("Finish ran = %v, want %v", finished.Load(), tc.finish)
			}
		})
	}
}

// S6 mutation guard: an item cut short by a stop is not settled, so the chunk
// it ends must not be recorded -- even when it is the chunk's last item.
func TestInterruptedItemLeavesChunkUnfinished(t *testing.T) {
	store := newMemLedgerStore(t)
	var stopRun context.CancelFunc
	job, rc, cancel := openJob(t, seq(8), batchOpts{size: 4, workers: 1}, func(irc *ops.ItemRC, i int) error {
		if i == 3 {
			stopRun()
			<-irc.Context().Done()
			return irc.Context().Err()
		}
		return nil
	})
	stopRun = cancel
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	led, _ := store.GetLedgerV3("op")
	if len(bitsSet(*led)) != 0 || led.Items.Failed != 0 {
		t.Fatalf("an interrupted item settled its chunk: bits %v failed %d", bitsSet(*led), led.Items.Failed)
	}
}

// N3: an AppendOnly run's chunk count is persisted, so Chunks.Done does not
// go backwards across a resume (merged ranges cannot be counted back).
func TestAppendOnlyChunkCountSurvivesResume(t *testing.T) {
	store := newMemLedgerStore(t)
	items := seq(60)
	job, rc, cancel := openJob(t, items, batchOpts{order: ops.AppendOnly, size: 10, workers: 1},
		func(irc *ops.ItemRC, i int) error {
			if i == 35 {
				<-irc.Context().Done()
				return irc.Context().Err()
			}
			return nil
		})
	defer cancel()
	var atCrash int
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, p database.ProgressSnapshot) bool {
			if p.Chunks.Done == 3 {
				atCrash = p.Chunks.Done
				return true
			}
			return false
		}})
	if !errors.Is(err, errWorldStopped) || atCrash != 3 {
		t.Fatalf("first attempt: %v at %d", err, atCrash)
	}
	if led, _ := store.GetLedgerV3("op"); len(led.Ranges) != 1 || led.ChunksDone != 3 {
		t.Fatalf("ledger = %+v: three merged chunks, counted 3", led)
	}
	store.newAttempt()
	job2, rc2, cancel2 := openJob(t, items, batchOpts{order: ops.AppendOnly, size: 10, workers: 1},
		func(*ops.ItemRC, int) error { return nil })
	defer cancel2()
	sum, _, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	prev := 0
	for _, c := range store.commitCopy() {
		if c.Progress.Chunks.Done < prev || c.Progress.Chunks.Done < 3 {
			t.Fatalf("Chunks.Done went backwards across the resume: %d after %d", c.Progress.Chunks.Done, prev)
		}
		prev = c.Progress.Chunks.Done
	}
	if sum.ChunksDone != 6 || sum.ChunksTotal != 6 || sum.Done != 60 {
		t.Fatalf("summary = %+v", sum)
	}
}

// memTitles is a minimal ops.Writer over an in-memory title map. It keeps
// the history a real Writer would: the books each attempt wrote (wrote),
// and, from nextAttempt on, the books every earlier attempt wrote (earlier),
// which is what Wrote reports.
type memTitles struct {
	mu      sync.Mutex
	titles  map[string]string
	wrote   map[string]bool
	earlier map[string]bool
}

func (w *memTitles) ModifyBook(_ context.Context, id string, fn func(*database.Book) error) (ops.Changed, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := &database.Book{ID: id, Title: w.titles[id]}
	if err := fn(b); err != nil {
		return ops.Changed{}, err
	}
	w.titles[id] = b.Title
	if w.wrote == nil {
		w.wrote = map[string]bool{}
	}
	w.wrote[id] = true
	return ops.Changed{Fields: []string{"title"}}, nil
}

// foreignWrite sets a title the way someone outside the run would: no
// history under the run.
func (w *memTitles) foreignWrite(id, title string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.titles[id] = title
}

// nextAttempt starts a new attempt: what the attempts so far wrote becomes
// the earlier attempts' history.
func (w *memTitles) nextAttempt() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.earlier = maps.Clone(w.wrote)
}

func (w *memTitles) Wrote(_ context.Context, subjects []ops.Subject) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(subjects) == 0 {
		return false, nil
	}
	for _, s := range subjects {
		if s.Type != ops.SubjectBook || !w.earlier[s.ID] {
			return false, nil
		}
	}
	return true, nil
}
func (w *memTitles) ModifyBookFile(context.Context, string, string, func(*database.BookFile) error) (ops.Changed, error) {
	return ops.Changed{}, errors.New("unused")
}
func (w *memTitles) RepointBookFile(context.Context, string, string, database.BookFileLocation, database.BookFileLocation) error {
	return errors.New("unused")
}
func (w *memTitles) ModifyBookAuthors(context.Context, string, func([]database.BookAuthor) ([]database.BookAuthor, error)) (ops.Changed, error) {
	return ops.Changed{}, errors.New("unused")
}
func (w *memTitles) Effect(context.Context, ops.EffectSpec, func() ([]ops.HistoryRow, error)) error {
	return errors.New("unused")
}

// TestFixerAppliedSurvivesResume: rows a Fixer applied before a crash are
// still listed in Applied and handed to AfterApply when the resumed run
// finishes. blockAt 20 crashes on a chunk boundary (rows 0-19 are in two
// completed chunks); blockAt 25 crashes inside chunk 2 after rows 20-24 were
// written, so the resume re-runs rows whose fingerprint the write changed and
// must report them applied (the run's history for them is there), not
// changed since plan.
//
// The foreign cases (05-PR5b): between the attempts someone else sets rows
// the run never wrote to exactly their Proposed values. Their fingerprint
// moved and every Proposed value holds, but there is no history under the
// run for them, so they are skipped as changed since plan: not in Applied,
// not handed to AfterApply. In the blockAt 25 one, rows 20-24 (written by
// the run) are still reported applied next to them.
func TestFixerAppliedSurvivesResume(t *testing.T) {
	cases := []struct {
		name    string
		blockAt int
		foreign []int
	}{
		{"block_at_20", 20, nil},
		{"block_at_25", 25, nil},
		{"block_at_20_foreign_20_24", 20, []int{20, 21, 22, 23, 24}},
		{"block_at_25_foreign_25_27", 25, []int{25, 26, 27}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { fixerResumeCase(t, c.blockAt, c.foreign) })
	}
}

func fixerResumeCase(t *testing.T, blockAt int, foreign []int) {
	titles := map[string]string{}
	var rows []ops.Row
	for i := range 30 {
		id := itemKey(i)
		titles[id] = "l " + id
		rows = append(rows, ops.Row{ID: id, Subjects: ops.Books(id), Inputs: map[string]string{"title": "l " + id},
			Current: map[string]string{"title": "l " + id}, Proposed: map[string]string{"title": "1 " + id}})
	}
	var afterApply []string
	block := true
	blocked := make(chan struct{})
	w := &memTitles{titles: titles}
	def := ops.Fixer("test.fixer", ops.FixerSpec[ops.NoParams, string]{
		Common:     ops.Common{Title: "f", Effects: ops.Writes(ops.ResBooks), Permission: auth.PermSettingsManage},
		Candidates: func(*ops.RC, ops.NoParams) (ops.Source[string], error) { return ops.Source[string]{}, nil },
		Load: func(_ *ops.RC, _ ops.NoParams, id string) (string, bool, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			t, ok := titles[id]
			return t, ok, nil
		},
		Evaluate: func(_ *ops.ItemRC, _ ops.NoParams, title string) (ops.Row, error) {
			id := title[2:]
			return ops.Row{ID: id, Subjects: ops.Books(id), Inputs: map[string]string{"title": title},
				Current: map[string]string{"title": title}, Proposed: map[string]string{"title": "1 " + id}}, nil
		},
		Apply: func(irc *ops.ItemRC, w ops.Writer, _ ops.NoParams, row ops.Row) error {
			if block && row.ID == itemKey(blockAt) {
				close(blocked)
				<-irc.Context().Done()
				return irc.Context().Err()
			}
			_, err := w.ModifyBook(irc.Context(), row.ID, func(b *database.Book) error { b.Title = row.Proposed["title"]; return nil })
			return err
		},
		AfterApply: func(_ *ops.RC, applied []string) error { afterApply = applied; return nil },
	})
	params, _ := json.Marshal(ops.FixerApply{Rows: rows})
	open := func(resumed bool) (*ops.BatchJob, *ops.RC, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		rc := ops.NewRC(ops.RunEnv{Context: ctx, OpID: "op", DefID: "test.fixer", Mode: ops.Live, Writer: w, Resumed: resumed})
		job, err := ops.Compile(def).OpenBatch(rc, params)
		if err != nil {
			t.Fatal(err)
		}
		// Each row is its own partition (Row.ID), so chunks are 10 rows in order.
		job.ChunkSize, job.Workers = 10, 1
		return job, rc, cancel
	}
	store := newMemLedgerStore(t)
	job, rc, cancel := open(false)
	defer cancel()
	// The world stops once chunks 0 and 1 are committed AND the worker is
	// parked on row blockAt, so rows before it in chunk 2 are written but
	// chunk 2 is never recorded.
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, _ database.ProgressSnapshot) bool {
			if len(bitsSet(l)) != 2 {
				return false
			}
			<-blocked
			return true
		}})
	if !errors.Is(err, errWorldStopped) {
		t.Fatalf("first attempt: %v", err)
	}
	if led, _ := store.GetLedgerV3("op"); slices.Contains(bitsSet(*led), 2) {
		t.Fatal("chunk 2 was recorded; the case does not test an unfinished chunk")
	}
	block = false
	w.nextAttempt()
	for _, i := range foreign {
		w.foreignWrite(itemKey(i), "1 "+itemKey(i))
	}
	job2, rc2, cancel2 := open(true)
	defer cancel2()
	_, raw, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	var res ops.FixerResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := range 30 {
		if !slices.Contains(foreign, i) {
			want = append(want, itemKey(i))
		}
	}
	if !slices.Equal(res.Applied, want) || !slices.Equal(afterApply, want) {
		var missing []string
		for _, id := range want {
			if !slices.Contains(res.Applied, id) {
				missing = append(missing, id)
			}
		}
		t.Fatalf("applied %d rows, AfterApply got %d; want %d across the resume (foreign %v); missing %v; applied %v",
			len(res.Applied), len(afterApply), len(want), foreign, missing, res.Applied)
	}
}

// Marks are written once, each in the commit that records its chunk, and
// Finish gets every one: there is no cap to drop past. (They used to ride,
// all of them, in every ledger value, capped at 50,000.)
func TestMarksWrittenOnceAndAllReachFinish(t *testing.T) {
	const n = 300
	store := newMemLedgerStore(t)
	var sum atomic.Pointer[ops.Summary]
	job, rc, cancel := openJob(t, seq(n), batchOpts{size: 7, workers: 3,
		finish: func(_ *ops.RC, s ops.Summary) { sum.Store(&s) }},
		func(irc *ops.ItemRC, i int) error {
			irc.Mark("seen")
			if i%2 == 0 {
				irc.Mark("even")
			}
			return nil
		})
	defer cancel()
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1}); err != nil {
		t.Fatal(err)
	}
	s := sum.Load()
	if s == nil || len(s.Marked("seen")) != n || len(s.Marked("even")) != n/2 {
		t.Fatalf("Finish marks: %v", s)
	}
	written := map[string]int{}
	for _, c := range store.commitCopy() {
		for name, keys := range c.Marks {
			for _, k := range keys {
				written[name+"/"+k]++
			}
		}
	}
	if len(written) != n+n/2 {
		t.Fatalf("%d distinct marks written, want %d", len(written), n+n/2)
	}
	for k, times := range written {
		if times != 1 {
			t.Fatalf("mark %s written %d times, want once", k, times)
		}
	}
}

// N-2 (final review): a run that starts its ledger over (ResumeFromZero)
// drops the marks the abandoned ledger stored, so the new Finish sees only
// its own.
func TestStartOverDropsOldMarks(t *testing.T) {
	store := newMemLedgerStore(t)
	fromZero := ops.ResumeFromZero
	run := func(name string) ops.Summary {
		var sum atomic.Pointer[ops.Summary]
		job, rc, cancel := openJob(t, seq(12), batchOpts{size: 4, workers: 2, resume: &fromZero,
			finish: func(_ *ops.RC, s ops.Summary) { sum.Store(&s) }},
			func(irc *ops.ItemRC, i int) error {
				if i%3 == 0 {
					irc.Mark(name)
				}
				return nil
			})
		defer cancel()
		store.newAttempt()
		if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1}); err != nil {
			t.Fatal(err)
		}
		return *sum.Load()
	}
	run("first")
	if got, _ := store.ListMarksV3("op"); len(got["first"]) != 4 {
		t.Fatalf("first run stored marks %v", got)
	}
	second := run("second")
	got, _ := store.ListMarksV3("op")
	if len(got["first"]) != 0 || len(got["second"]) != 4 {
		t.Fatalf("marks after starting over = %v, want only the second run's", got)
	}
	if len(second.Marked("first")) != 0 {
		t.Fatalf("the new run's Finish saw the old marks: %v", second.Marks)
	}
}

// N-4 (final review): FixerResult.Applied and AfterApply's list are sorted by
// row id, whatever order four workers finish the rows in (here the reverse
// of id order).
func TestFixerAppliedSortedByRowID(t *testing.T) {
	const n = 12
	titles := map[string]string{}
	var mu sync.Mutex
	var rows []ops.Row
	for i := range n {
		id := itemKey(i)
		titles[id] = "l " + id
		rows = append(rows, ops.Row{ID: id, Inputs: map[string]string{"title": "l " + id},
			Current: map[string]string{"title": "l " + id}, Proposed: map[string]string{"title": "1 " + id}})
	}
	var afterApply []string
	def := ops.Fixer("test.fixer", ops.FixerSpec[ops.NoParams, string]{
		Common:     ops.Common{Title: "f", Effects: ops.Writes(ops.ResBooks), Permission: auth.PermSettingsManage},
		Candidates: func(*ops.RC, ops.NoParams) (ops.Source[string], error) { return ops.Source[string]{}, nil },
		Load: func(_ *ops.RC, _ ops.NoParams, id string) (string, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			t, ok := titles[id]
			return t, ok, nil
		},
		Evaluate: func(_ *ops.ItemRC, _ ops.NoParams, title string) (ops.Row, error) {
			id := title[2:]
			return ops.Row{ID: id, Inputs: map[string]string{"title": title}, Current: map[string]string{"title": title},
				Proposed: map[string]string{"title": "1 " + id}}, nil
		},
		Apply: func(_ *ops.ItemRC, _ ops.Writer, _ ops.NoParams, row ops.Row) error {
			i, _ := strconv.Atoi(row.ID)
			time.Sleep(time.Duration(n-i) * 2 * time.Millisecond) // low ids finish last
			mu.Lock()
			titles[row.ID] = row.Proposed["title"]
			mu.Unlock()
			return nil
		},
		AfterApply: func(_ *ops.RC, applied []string) error { afterApply = applied; return nil },
	})
	params, _ := json.Marshal(ops.FixerApply{Rows: rows})
	rc := ops.NewRC(ops.RunEnv{Context: context.Background(), OpID: "op", DefID: "test.fixer", Mode: ops.Live, Writer: &memTitles{titles: map[string]string{}}})
	job, err := ops.Compile(def).OpenBatch(rc, params)
	if err != nil {
		t.Fatal(err)
	}
	job.ChunkSize, job.Workers = 1, 4
	_, raw, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: newMemLedgerStore(t), commitEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	var res ops.FixerResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != n || !slices.IsSorted(res.Applied) || !slices.Equal(afterApply, res.Applied) {
		t.Fatalf("Applied %v, AfterApply %v: want all %d, sorted by row id", res.Applied, afterApply, n)
	}
}
