// file: internal/operations/registry/batch_runner_stop_test.go
// version: 1.2.0
// guid: c8ee28eb-1a14-43aa-9e77-8473ae796708
// last-edited: 2026-10-13

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
	"slices"
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

// memTitles is a minimal ops.Writer over an in-memory title map.
type memTitles struct {
	mu     sync.Mutex
	titles map[string]string
}

func (w *memTitles) ModifyBook(_ context.Context, id string, fn func(*database.Book) error) (ops.Changed, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := &database.Book{ID: id, Title: w.titles[id]}
	if err := fn(b); err != nil {
		return ops.Changed{}, err
	}
	w.titles[id] = b.Title
	return ops.Changed{Fields: []string{"title"}}, nil
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
// must report them applied, not changed since plan.
func TestFixerAppliedSurvivesResume(t *testing.T) {
	for _, blockAt := range []int{20, 25} {
		t.Run(fmt.Sprint("block_at_", blockAt), func(t *testing.T) { fixerResumeCase(t, blockAt) })
	}
}

func fixerResumeCase(t *testing.T, blockAt int) {
	titles := map[string]string{}
	var rows []ops.Row
	for i := range 30 {
		id := itemKey(i)
		titles[id] = "l " + id
		rows = append(rows, ops.Row{ID: id, Inputs: map[string]string{"title": "l " + id},
			Current: map[string]string{"title": "l " + id}, Proposed: map[string]string{"title": "1 " + id}})
	}
	var afterApply []string
	block := true
	blocked := make(chan struct{})
	def := ops.Fixer("test.fixer", ops.FixerSpec[ops.NoParams, string]{
		Common:     ops.Common{Title: "f", Effects: ops.Writes(ops.ResBooks), Permission: auth.PermSettingsManage},
		Candidates: func(*ops.RC, ops.NoParams) (ops.Source[string], error) { return ops.Source[string]{}, nil },
		Load: func(_ *ops.RC, _ ops.NoParams, id string) (string, bool, error) {
			t, ok := titles[id]
			return t, ok, nil
		},
		Evaluate: func(_ *ops.ItemRC, _ ops.NoParams, title string) (ops.Row, error) {
			id := title[2:]
			return ops.Row{ID: id, Inputs: map[string]string{"title": title}, Current: map[string]string{"title": title},
				Proposed: map[string]string{"title": "1 " + id}}, nil
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
	w := &memTitles{titles: titles}
	open := func() (*ops.BatchJob, *ops.RC, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		rc := ops.NewRC(ops.RunEnv{Context: ctx, OpID: "op", DefID: "test.fixer", Mode: ops.Live, Writer: w})
		job, err := ops.Compile(def).OpenBatch(rc, params)
		if err != nil {
			t.Fatal(err)
		}
		// Each row is its own partition (Row.ID), so chunks are 10 rows in order.
		job.ChunkSize, job.Workers = 10, 1
		return job, rc, cancel
	}
	store := newMemLedgerStore(t)
	job, rc, cancel := open()
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
	job2, rc2, cancel2 := open()
	defer cancel2()
	_, raw, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	var res ops.FixerResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	want := make([]string, 30)
	for i := range want {
		want[i] = itemKey(i)
	}
	if !slices.Equal(res.Applied, want) || !slices.Equal(afterApply, want) {
		var missing []string
		for _, id := range want {
			if !slices.Contains(res.Applied, id) {
				missing = append(missing, id)
			}
		}
		t.Fatalf("applied %d rows, AfterApply got %d; want all 30 across the resume; missing %v",
			len(res.Applied), len(afterApply), missing)
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
