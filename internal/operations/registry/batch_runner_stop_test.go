// file: internal/operations/registry/batch_runner_stop_test.go
// version: 1.0.1
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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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
			_, _, err := runBatch(ctx, batchConfig{opID: "op", job: job, rc: rc, commitEvery: -1, resumable: tc.resumable})
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
func (w *memTitles) RepointBookFile(context.Context, string, string, undo.BookFileLocation, undo.BookFileLocation) error {
	return errors.New("unused")
}
func (w *memTitles) ModifyBookAuthors(context.Context, string, func([]database.BookAuthor) ([]database.BookAuthor, error)) (ops.Changed, error) {
	return ops.Changed{}, errors.New("unused")
}
func (w *memTitles) Effect(context.Context, ops.EffectSpec, func() ([]ops.HistoryRow, error)) error {
	return errors.New("unused")
}

// S3: a Fixer apply interrupted after some rows were written lists every
// applied row -- from both attempts -- when the resumed run finishes, and
// AfterApply gets them all.
func TestFixerAppliedSurvivesResume(t *testing.T) {
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
			if block && row.ID >= itemKey(20) {
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
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, _ database.ProgressSnapshot) bool { return len(bitsSet(l)) == 2 }})
	if !errors.Is(err, errWorldStopped) {
		t.Fatalf("first attempt: %v", err)
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
		t.Fatalf("applied %d rows, AfterApply got %d; want all 30 across the resume", len(res.Applied), len(afterApply))
	}
}
