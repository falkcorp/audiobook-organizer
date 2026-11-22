// file: core/internal/storagemigrate/extend_test.go
// version: 1.0.0
// guid: 2c7a41d8-95b3-4e0f-8d61-0b9f3e5a7c14
// last-edited: 2026-11-22

package storagemigrate_test

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/startprogress"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// extensions records the runner's requests for start time and fails the test
// if two ever run at once: ExtendStart is not safe for concurrent use, so the
// runner must serialise every request, whichever goroutine raised it.
type extensions struct {
	t      *testing.T
	mu     sync.Mutex
	steps  []string
	inside atomic.Int32
}

func (x *extensions) call(step string) {
	if x.inside.Add(1) != 1 {
		x.t.Error("ExtendStart called concurrently")
	}
	defer x.inside.Add(-1)
	x.mu.Lock()
	x.steps = append(x.steps, step)
	x.mu.Unlock()
}

// count is how many requests so far name kind (a step-name suffix such as
// " plan" or " verify").
func (x *extensions) count(kind string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	n := 0
	for _, s := range x.steps {
		if strings.HasSuffix(s, kind) {
			n++
		}
	}
	return n
}

func (x *extensions) total() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.steps)
}

// TestDataMigrationExtendsAfterEveryChunkByDefault: with no ExtendEvery the
// runner asks for start time after every committed chunk, so the longest
// silence of the chunk loop is one chunk's own time (at most ChunkItems items
// or ChunkBytes bytes), however much one Step costs. 64 rows in chunks of four
// are 16 chunks: the start and one per chunk. This migration has no Verify, so
// there is nothing to extend for there.
func TestDataMigrationExtendsAfterEveryChunkByDefault(t *testing.T) {
	e := newEnv(t, 64)
	x := &extensions{t: t}
	r := e.runner(storagemigrate.Options{ChunkItems: 4, ExtendStart: x.call}, recount(sequential()))
	e.mustBoot(r)
	if got := x.count(" verify"); got != 0 {
		t.Errorf("verify requests = %d, want 0 (no Verify): %q", got, x.steps)
	}
	chunks := 0
	for _, s := range x.steps {
		if strings.Contains(s, " chunk ") {
			chunks++
		}
	}
	if chunks != 16 {
		t.Errorf("chunk requests = %d, want 16 (one per committed chunk): %q", chunks, x.steps)
	}
}

// keysReader is a storage.Reader that yields n keys per Scan and then, when
// hold is set, blocks until its context ends: a source whose read stalls.
type keysReader struct {
	n    int
	hold bool
}

func (k keysReader) Get(context.Context, []byte) ([]byte, error) { return nil, storage.ErrNotFound }

func (k keysReader) NewIterator(context.Context, storage.ScanOptions) (*storage.Iterator, error) {
	return nil, errors.New("not used by the plan scan")
}

func (k keysReader) Scan(ctx context.Context, _ storage.ScanOptions) iter.Seq2[storage.KV, error] {
	return func(yield func(storage.KV, error) bool) {
		for i := range k.n {
			if !yield(storage.KV{Key: item(i)}, nil) {
				return
			}
		}
		if k.hold {
			<-ctx.Done()
			yield(storage.KV{}, context.Cause(ctx))
		}
	}
}

// TestPlanScanExtendsPerKeysRead: the plan's keys-only sampling scan reads
// every key of the source once, so its requests for start time scale with the
// source, one per PlanExtendKeys keys, and an empty or short source asks for
// none of its own.
func TestPlanScanExtendsPerKeysRead(t *testing.T) {
	e := newEnv(t, 1)
	scan := func(keys int) int {
		x := &extensions{t: t}
		r := e.runner(storagemigrate.Options{ExtendStart: x.call, PlanExtendKeys: 100}, recount(sequential()))
		if err := r.PlanScan(t.Context(), keysReader{n: keys}, "gotham.recount", itemsSource()); err != nil {
			t.Fatal(err)
		}
		return x.count(" plan")
	}
	if got := scan(99); got != 0 {
		t.Errorf("99 keys asked %d times, want 0", got)
	}
	small, large := scan(1000), scan(5000)
	if small != 10 || large != 50 {
		t.Fatalf("1000 keys asked %d times and 5000 keys %d, want 10 and 50", small, large)
	}
}

// TestPlanScanThatStallsStopsExtending: a scan that stops reading stops asking.
// No timer extends for it, so under a start budget the boot is killed instead
// of being kept alive: the test's own short budget ends the scan, and the
// requests made before the stall are all there are.
func TestPlanScanThatStallsStopsExtending(t *testing.T) {
	e := newEnv(t, 1)
	x := &extensions{t: t}
	r := e.runner(storagemigrate.Options{ExtendStart: x.call, PlanExtendKeys: 100}, recount(sequential()))
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	err := r.PlanScan(ctx, keysReader{n: 250, hold: true}, "gotham.recount", itemsSource())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the start budget's deadline", err)
	}
	if got := x.count(" plan"); got != 2 {
		t.Fatalf("a scan that read 250 keys then stalled asked %d times, want 2 (at keys 100 and 200): %q", got, x.steps)
	}
}

// verifying is a recount migration with verify as its Verify.
func verifying(verify func(context.Context, storage.Reader) (datamigrate.VerifyReport, error)) datamigrate.DataMigration {
	m := recount(sequential())
	m.Verify = verify
	return m
}

// TestVerifyExtendsPerProgressUnit: Verify is handed a context whose
// startprogress.Report asks for start time, from whichever goroutine raises
// it, one request per unit, never two at once (the runner's extendMu), plus
// one before Verify starts. More units are more requests.
func TestVerifyExtendsPerProgressUnit(t *testing.T) {
	run := func(workers, perWorker int) int {
		e := newEnv(t, 4)
		x := &extensions{t: t}
		r := e.runner(storagemigrate.Options{ExtendStart: x.call}, verifying(func(ctx context.Context, _ storage.Reader) (datamigrate.VerifyReport, error) {
			var wg sync.WaitGroup
			for range workers {
				wg.Go(func() {
					for range perWorker {
						startprogress.Report(ctx)
					}
				})
			}
			wg.Wait()
			return datamigrate.VerifyReport{}, nil
		}))
		e.mustBoot(r)
		return x.count(" verify")
	}
	if got := run(1, 3); got != 1+3 {
		t.Errorf("3 units on one goroutine: %d verify requests, want 4", got)
	}
	if got := run(4, 25); got != 1+4*25 {
		t.Errorf("100 units over 4 workers: %d verify requests, want 101", got)
	}
}

// waitBounds is how long a stall test waits for its migration to reach the
// stall, or for the boot to end after it: far longer than a loaded -race run
// needs, short enough that a lost wake-up fails the test instead of hanging it.
const waitBounds = 10 * time.Second

// bootInBackground boots r under ctx and reports the boot's error on the
// returned channel (buffered, so a test that gave up does not leak the goroutine).
func (e *env) bootInBackground(ctx context.Context, r *storagemigrate.Runner) <-chan error {
	done := make(chan error, 1)
	go func() {
		db, err := e.boot(ctx, r)
		if db != nil {
			err = errors.Join(err, db.Close())
		}
		done <- err
	}()
	return done
}

// awaitStall waits for stalled to close. If the boot ends first (its deadline
// fired before the migration got there, as on a loaded machine) or nothing
// happens within waitBounds, the test fails with that, not with a hang.
func awaitStall(t *testing.T, stalled <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-stalled:
	case err := <-done:
		t.Fatalf("the boot ended before the migration stalled: %v", err)
	case <-time.After(waitBounds):
		t.Fatal("the migration never reached its stall")
	}
}

// awaitBoot is the boot's error once it ends, bounded by waitBounds.
func awaitBoot(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(waitBounds):
		t.Fatal("the boot did not end after its deadline")
		return nil
	}
}

// TestVerifyThatStallsStopsExtending: a Verify that raises two units and then
// blocks gets no further extension, however long it blocks: the boot is left to
// its start budget (the test's short deadline), not kept alive by the runner.
func TestVerifyThatStallsStopsExtending(t *testing.T) {
	e := newEnv(t, 4)
	x := &extensions{t: t}
	stalled := make(chan struct{})
	r := e.runner(storagemigrate.Options{ExtendStart: x.call}, verifying(func(ctx context.Context, _ storage.Reader) (datamigrate.VerifyReport, error) {
		startprogress.Report(ctx)
		startprogress.Report(ctx)
		close(stalled)
		<-ctx.Done()
		return datamigrate.VerifyReport{}, context.Cause(ctx)
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
	defer cancel()
	done := e.bootInBackground(ctx, r)
	awaitStall(t, stalled, done)
	atStall := x.total()
	time.Sleep(200 * time.Millisecond)
	if got := x.total(); got != atStall {
		t.Fatalf("the stalled Verify was extended %d more times, want none", got-atStall)
	}
	if err := awaitBoot(t, done); err == nil {
		t.Fatal("the boot succeeded though Verify never returned")
	}
	if got := x.count(" verify"); got != 3 {
		t.Fatalf("verify requests = %d, want 3 (before Verify and its two units): %q", got, x.steps)
	}
}

// TestChunkLoopThatStallsStopsExtending: a Step that commits three chunks and
// then blocks gets no further extension, however long it blocks. The boot has
// asked once at its start and once per committed chunk (a chunk's items are
// not a progress unit of their own: the chunk is), nothing during the stall,
// and ends on its deadline.
func TestChunkLoopThatStallsStopsExtending(t *testing.T) {
	const chunkItems, committedChunks = 4, 3
	e := newEnv(t, 64)
	x := &extensions{t: t}
	stalled := make(chan struct{})
	var steps atomic.Int32
	m := recount(sequential())
	step := m.Step
	m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		if steps.Add(1) > chunkItems*committedChunks {
			close(stalled)
			<-ctx.Done()
			return context.Cause(ctx)
		}
		return step(ctx, kv, w)
	}
	r := e.runner(storagemigrate.Options{ChunkItems: chunkItems, ExtendStart: x.call}, m)
	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
	defer cancel()
	done := e.bootInBackground(ctx, r)
	awaitStall(t, stalled, done)
	chunks := x.count(" chunk 1") + x.count(" chunk 2") + x.count(" chunk 3")
	atStall := x.total()
	time.Sleep(200 * time.Millisecond)
	if got := x.total(); got != atStall {
		t.Fatalf("the stalled chunk loop was extended %d more times, want none: %q", got-atStall, x.steps)
	}
	if want := committedChunks + 1; atStall != want || chunks != committedChunks {
		t.Fatalf("requests = %d (chunk requests %d), want %d (the start and one per committed chunk): %q", atStall, chunks, want, x.steps)
	}
	err := awaitBoot(t, done)
	if err == nil {
		t.Fatal("the boot succeeded though a Step never returned")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("boot error = %v, want the start budget's deadline", err)
	}
}
