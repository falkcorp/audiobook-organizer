// file: internal/plugins/maintenance/file_intro_yield_test.go
// version: 1.0.2
// guid: 79b2a942-e84c-447f-a8c4-433000d6fd16
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/mediacore/tasks"
)

// The native transcribe-file-intros kind yields before its deadline (review M4):
// it stops starting batches inside fileIntroYieldMargin of the context's
// deadline, checkpoints a cursor and returns tasks.ErrContinueInPlace, and the
// next attempt continues the same run from that cursor. HEAD's shell never sets
// a margin or a checkpoint, so it never yields.

// memCheckpoint is the executor's checkpoint slot for one run: JSON in, JSON
// out, so a cursor that does not survive a round trip fails the test.
type memCheckpoint struct {
	mu    sync.Mutex
	raw   []byte
	saves int
}

func (c *memCheckpoint) save(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raw, c.saves = b, c.saves+1
	return nil
}

func (c *memCheckpoint) load(v any) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.raw == nil {
		return false, nil
	}
	return true, json.Unmarshal(c.raw, v)
}

func (c *memCheckpoint) cursor(t *testing.T) fileIntroCursor {
	t.Helper()
	var cur fileIntroCursor
	if ok, err := c.load(&cur); err != nil || !ok {
		t.Fatalf("no cursor was checkpointed: ok=%v err=%v", ok, err)
	}
	return cur
}

type yieldProgress struct {
	mu   sync.Mutex
	done []int
	tot  []int
}

func (p *yieldProgress) set(done, total int, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done, p.tot = append(p.done, done), append(p.tot, total)
}

func (p *yieldProgress) last() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.done) == 0 {
		return -1, -1
	}
	return p.done[len(p.done)-1], p.tot[len(p.tot)-1]
}

// yieldEnv is HEAD's env with a checkpoint slot and a yield margin: the shape
// the native shell builds from rc.
func (fx *introFx) yieldEnv(rep *fileIntroRep, ck *memCheckpoint, prog *yieldProgress, margin time.Duration) fileIntroEnv {
	fx.t.Helper()
	store := fx.p.deps.OpsStore()
	base := t6g4HeadBase(rep, true, headT6G4Store{OpsStore: store, deps: fx.p.deps}, headT6G4Writer{store: store})
	if ck != nil {
		base.SaveCheckpoint, base.LoadCheckpoint = ck.save, ck.load
	}
	if prog != nil {
		base.Progress = prog.set
	}
	return fileIntroEnv{t6g4Base: base, YieldMargin: margin}
}

// yieldFiles makes n audio files, one per book (every one a book's first file,
// so selection orders them by book id), and returns their paths.
func (fx *introFx) yieldFiles(n int) []string {
	fx.t.Helper()
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		b := fx.book(fmt.Sprintf("Batman Year %d", i+1))
		_, p := fx.file(b, "01.m4b", 1, fmt.Sprintf("yield-h%02d", i), false)
		fx.whisp.texts[p] = goodIntro
		paths = append(paths, p)
	}
	return paths
}

func (fx *introFx) yieldRun(ctx context.Context, env fileIntroEnv, onlyMissing bool) (fileIntroResult, error) {
	fx.t.Helper()
	return fx.p.runFileIntroTranscribeCore(ctx, fileIntroParams{OnlyMissing: &onlyMissing}, env)
}

func shortDeadline(t *testing.T) context.Context {
	t.Helper()
	// One minute left, against a five minute margin: under the margin from the first batch.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestFileIntroYield_CheckpointsACursorAndTheNextAttemptContinuesTheRun(t *testing.T) {
	for _, onlyMissing := range []bool{true, false} {
		t.Run(fmt.Sprintf("only_missing=%v", onlyMissing), func(t *testing.T) {
			fx := newIntroFx(t)
			fileIntroSlots = func() int { return 1 } // sequential: the prefix is exactly the first batch
			paths := fx.yieldFiles(2*fileIntroBatchGroups + 1)
			ck, prog := &memCheckpoint{}, &yieldProgress{}

			// Attempt 1: inside the margin from the start. The first batch always
			// runs (a yield with nothing done would fail the executor's progress
			// proof); every later one is left for the next attempt.
			rep := &fileIntroRep{id: "run-yield-1"}
			res, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(rep, ck, prog, fileIntroYieldMargin), onlyMissing)
			if !errors.Is(err, tasks.ErrContinueInPlace) {
				t.Fatalf("attempt 1 should yield, got %v", err)
			}
			first := fx.whisp.sentPaths()
			if len(first) != fileIntroBatchGroups {
				t.Fatalf("attempt 1 ran %d files, want exactly the first batch (%d)", len(first), fileIntroBatchGroups)
			}
			cur := ck.cursor(t)
			if cur.RunID != "run-yield-1" || cur.DoneBatches != 1 || cur.Last == nil || cur.Stats.Attempted != fileIntroBatchGroups {
				t.Fatalf("cursor: %+v", cur)
			}
			if cur.Stats.Done {
				t.Fatal("a yielded run's counters must not be marked done")
			}
			if res.Stats == nil || res.Stats.Done {
				t.Fatalf("result stats: %+v", res.Stats)
			}
			if d, tot := prog.last(); d != 1 || tot != 3 {
				t.Fatalf("progress after the yield is %d/%d, want the exact prefix 1/3", d, tot)
			}
			if st, _ := fx.s.GetFileIntroStats(); st == nil || st.Done {
				t.Fatalf("persisted stats after a yield: %+v", st)
			}

			// Attempt 2: a fresh deadline; the executor hands the cursor back.
			fx.whisp.mu.Lock()
			fx.whisp.calls = nil
			fx.whisp.mu.Unlock()
			prog2 := &yieldProgress{}
			rep2 := &fileIntroRep{id: "run-yield-1"}
			res2, err := fx.yieldRun(context.Background(), fx.yieldEnv(rep2, ck, prog2, fileIntroYieldMargin), onlyMissing)
			if err != nil {
				t.Fatalf("attempt 2: %v", err)
			}
			second := fx.whisp.sentPaths()
			if len(second) != len(paths)-fileIntroBatchGroups {
				t.Fatalf("attempt 2 ran %d files, want the remaining %d", len(second), len(paths)-fileIntroBatchGroups)
			}
			for p := range second {
				if first[p] != 0 {
					t.Fatalf("attempt 2 re-sent %s, which attempt 1 completed", p)
				}
			}
			if res2.RunID != "run-yield-1" || res2.Stats == nil || !res2.Stats.Done ||
				res2.Stats.Attempted != len(paths) || res2.Stats.RunOpID != "run-yield-1" {
				t.Fatalf("attempt 2 result: %+v stats=%+v", res2, res2.Stats)
			}
			if res2.Stats.Eligible != len(paths) || res2.Stats.Selected != len(paths) {
				t.Fatalf("the run's start-of-run figures must carry over: %+v", res2.Stats)
			}
			// The bar never went backwards across the attempts: attempt 2 started
			// at 1 and finished the run.
			prog2.mu.Lock()
			for i, d := range prog2.done {
				if d < 1 {
					t.Fatalf("attempt 2 reported %d/%d, below the 1 batch attempt 1 completed", d, prog2.tot[i])
				}
			}
			prog2.mu.Unlock()
			for _, b := range mustAllBooks(t, fx) {
				for _, f := range mustFiles(t, fx.s, b) {
					if f.IntroSourceHash != f.FileHash {
						t.Fatalf("file %s is not done after the run: %+v", f.ID, f)
					}
				}
			}
		})
	}
}

func mustAllBooks(t *testing.T, fx *introFx) []string {
	t.Helper()
	ids, err := fx.s.ListBookIDs()
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestFileIntroYield_ForcedRunSkipsTheCompletedPrefixByKey(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	paths := fx.yieldFiles(2*fileIntroBatchGroups + 1)
	ck := &memCheckpoint{}
	rep := &fileIntroRep{id: "run-yield-f"}
	if _, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(rep, ck, nil, fileIntroYieldMargin), false); !errors.Is(err, tasks.ErrContinueInPlace) {
		t.Fatalf("attempt 1: %v", err)
	}
	// A forced run's selection includes the files attempt 1 just finished; only
	// the cursor keeps attempt 2 from transcribing them again.
	fx.whisp.mu.Lock()
	fx.whisp.calls = nil
	fx.whisp.mu.Unlock()
	rep2 := &fileIntroRep{id: "run-yield-f"}
	if _, err := fx.yieldRun(context.Background(), fx.yieldEnv(rep2, ck, nil, fileIntroYieldMargin), false); err != nil {
		t.Fatal(err)
	}
	if got := len(fx.whisp.sentPaths()); got != len(paths)-fileIntroBatchGroups {
		t.Fatalf("forced attempt 2 sent %d files, want %d (the prefix is skipped)", got, len(paths)-fileIntroBatchGroups)
	}
}

func TestFileIntroYield_NoYieldWhenNothingIsLeftOrNoMarginOrNoCheckpoint(t *testing.T) {
	cases := []struct {
		name   string
		files  int
		margin time.Duration
		ckpt   bool
	}{
		// One batch only: the first batch runs, nothing remains to defer.
		{"single batch", fileIntroBatchGroups, fileIntroYieldMargin, true},
		// HEAD: no margin.
		{"margin 0 is HEAD", 2*fileIntroBatchGroups + 1, 0, true},
		// A yield cannot be resumed without a checkpoint to hold the cursor.
		{"no checkpoint hook", 2*fileIntroBatchGroups + 1, fileIntroYieldMargin, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newIntroFx(t)
			fileIntroSlots = func() int { return 1 }
			fx.yieldFiles(c.files)
			var ck *memCheckpoint
			if c.ckpt {
				ck = &memCheckpoint{}
			}
			rep := &fileIntroRep{id: "run-no-yield"}
			res, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(rep, ck, nil, c.margin), true)
			if err != nil {
				t.Fatalf("this run must complete, got %v", err)
			}
			if res.Stats == nil || !res.Stats.Done || res.Stats.Attempted != c.files {
				t.Fatalf("stats: %+v", res.Stats)
			}
			if ck != nil && ck.saves != 0 {
				t.Fatalf("a run that did not yield checkpointed %d times", ck.saves)
			}
		})
	}
}

func TestFileIntroYield_AFailedBatchFailsTheRunWithoutACursor(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fx.yieldFiles(2*fileIntroBatchGroups + 1)
	// The first batch ends the run (the pool is down), inside the margin: the
	// error is returned, and no batch completed, so there is nothing to yield.
	fx.whisp.transErr = &transcribe.TransportError{Endpoints: []string{"http://192.0.2.10:8000"}, Err: errors.New("connection refused"), Recognized: true}
	ck := &memCheckpoint{}
	rep := &fileIntroRep{id: "run-yield-err"}
	_, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(rep, ck, nil, fileIntroYieldMargin), true)
	if err == nil || errors.Is(err, tasks.ErrContinueInPlace) {
		t.Fatalf("a failed batch must fail the run, got %v", err)
	}
	if ck.saves != 0 {
		t.Fatalf("a failed run left a cursor (%d saves)", ck.saves)
	}
}

func TestFileIntroKey_IsTheSelectionOrderAndSkipThroughDropsAPrefix(t *testing.T) {
	cores := []database.BookFileCore{
		core("b1f2", "b1", "/lib/b1/02.m4b", "h-b1-2", 2),
		core("b1f1", "b1", "/lib/b1/01.m4b", "h-b1-1", 1),
		core("b2f1", "b2", "/lib/b2/01.m4b", "h-shared", 1),
		core("b3f2", "b3", "/lib/b3/02.m4b", "h-shared", 2),
		core("b3f1", "b3", "/lib/b3/01.m4b", "h-b3-1", 1),
		core("b4f2", "b4", "/lib/b4/02.m4b", "h-b4-2", 2),
		core("b4f1", "b4", "/lib/b4/01.m4b", "h-b4-1", 1),
	}
	sel := selectFileIntroWork(cores, fileIntroSelectOpts{onlyMissing: true, stat: func(string) bool { return true }})
	gs := sel.groups
	if len(gs) < 5 {
		t.Fatalf("fixture: %d groups", len(gs))
	}
	for i := range gs {
		for j := range gs {
			ki, kj := fileIntroKeyOf(gs[i]), fileIntroKeyOf(gs[j])
			if want := i <= j; ki.atOrBefore(kj) != want {
				t.Fatalf("key order disagrees with selection order at (%d,%d): %+v vs %+v", i, j, ki, kj)
			}
		}
	}
	for k := range gs {
		got := fileIntroSkipThrough(gs, fileIntroKeyOf(gs[k]))
		if len(got) != len(gs)-k-1 {
			t.Fatalf("skip through group %d left %d groups, want %d", k, len(got), len(gs)-k-1)
		}
	}
}

// yieldLoop is a fileIntroBatchLoop over n one-group batches whose batches are
// the given functions, on HEAD's loop runner with two workers.
func yieldLoop(n int, run func(i int) error) *fileIntroBatchLoop {
	batches := make([][]fileIntroGroup, n)
	for i := range batches {
		batches[i] = []fileIntroGroup{{Hash: fmt.Sprintf("h%d", i), Members: []fileIntroItem{{BookID: fmt.Sprintf("b%d", i)}}}}
	}
	return &fileIntroBatchLoop{
		batches: batches, slots: 2, yieldMargin: fileIntroYieldMargin,
		runBatch: func(_ context.Context, g []fileIntroGroup) (int, error) {
			var i int
			_, _ = fmt.Sscanf(g[0].Hash, "h%d", &i)
			return 1, run(i)
		},
		afterBatch: func(int) {},
		each:       t6g4RunEachHEAD(&fileIntroRep{}),
	}
}

func TestFileIntroBatchLoop_PrefixStopsAtTheFirstBatchLeftForTheNextAttempt(t *testing.T) {
	// Batch 1 is slow. Batch 0 finishes first (once batch 1 has begun), which
	// frees a worker for batch 2: that starts inside the margin with batch 0 done,
	// so it is skipped, and the slow batch 1 still completes.
	release, started1 := make(chan struct{}), make(chan struct{})
	l := yieldLoop(3, func(i int) error {
		switch i {
		case 0:
			<-started1 // batch 1 must have begun, or it would itself be skipped
		case 1:
			close(started1)
			<-release
		}
		return nil
	})
	go func() {
		for !l.yielding.Load() {
			time.Sleep(time.Millisecond)
		}
		close(release)
	}()
	prefix, yielded, err := l.run(shortDeadline(t))
	if err != nil || !yielded {
		t.Fatalf("want a clean yield, got yielded=%v err=%v", yielded, err)
	}
	// 0 and 1 completed (contiguous), 2 was skipped at the margin: it is NOT in the prefix.
	if prefix != 2 {
		t.Fatalf("prefix = %d, want 2", prefix)
	}
}

func TestFileIntroBatchLoop_ARealErrorBeatsAYieldAndStopsThePrefix(t *testing.T) {
	// Batch 0 completes, batch 1 fails after batch 2 was skipped at the margin:
	// the error is returned, never a yield, and the prefix stops before the failed batch.
	release, started1 := make(chan struct{}), make(chan struct{})
	boom := errors.New("whisper pool down")
	l := yieldLoop(3, func(i int) error {
		switch i {
		case 0:
			<-started1
		case 1:
			close(started1)
			<-release
			return boom
		}
		return nil
	})
	go func() {
		for !l.yielding.Load() {
			time.Sleep(time.Millisecond)
		}
		close(release)
	}()
	prefix, yielded, err := l.run(shortDeadline(t))
	if !errors.Is(err, boom) || yielded {
		t.Fatalf("a real error must win over a yield: yielded=%v err=%v", yielded, err)
	}
	if prefix != 1 {
		t.Fatalf("prefix = %d, want 1 (batch 0 only; batch 1 failed)", prefix)
	}
}

// A yield must leave a prefix of at least one batch (the executor's progress
// proof), so it waits for batch 0 itself: a slow batch 0 does not let later
// batches be skipped, and the loop does not yield while it is still running.
func TestFileIntroBatchLoop_NeverYieldsBeforeBatchZeroIsDone(t *testing.T) {
	ran2 := make(chan struct{})
	l := yieldLoop(3, func(i int) error {
		switch i {
		case 0:
			<-ran2 // batch 0 stays running until batch 2 has run its body
		case 2:
			close(ran2)
		}
		return nil
	})
	prefix, yielded, err := l.run(shortDeadline(t))
	if err != nil || yielded {
		t.Fatalf("a yield with batch 0 still running: yielded=%v err=%v", yielded, err)
	}
	if prefix != 3 {
		t.Fatalf("prefix = %d, want all 3 batches run", prefix)
	}
}
