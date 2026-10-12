// file: internal/operations/registry/zombie_fence_test.go
// version: 1.4.0
// guid: 2bc00ec8-5dde-4047-bac7-1fe9b4d5ba53
// last-edited: 2026-10-11

package registry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// zombieHarness is a started registry with a short abandon grace, a stepped
// liveness clock and a watchdog that only runs when the test asks.
type zombieHarness struct {
	r     *registry.Registry
	store *fakeStore
	clk   *stepClock
}

func newZombieHarness(t *testing.T, workers int, logger *slog.Logger) *zombieHarness {
	t.Helper()
	if logger == nil {
		logger = slog.Default()
	}
	store := newFakeStore()
	clk := newStepClock()
	r := registry.NewWithOptions(store, logger, workers, registry.Options{
		AbandonGrace:     50 * time.Millisecond,
		LivenessNow:      clk.Now,
		WatchdogInterval: time.Hour,
		// High enough that Gate 2b (abandoned cap) never holds a run back,
		// so the gate under test is the one doing the blocking.
		AbandonedCap: 100,
	})
	return &zombieHarness{r: r, store: store, clk: clk}
}

// start starts the registry and registers a bounded Shutdown on cleanup,
// after releasing any stubborn runs (so the drain does not wait them out).
func (h *zombieHarness) start(t *testing.T, release ...func()) {
	t.Helper()
	h.r.Start(t.Context())
	t.Cleanup(func() {
		for _, rel := range release {
			rel()
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.r.Shutdown(sctx)
	})
}

// stubbornRun is a Run that ignores ctx on its first call (it becomes a
// zombie when canceled) and records a "write" through the run fence on every
// loop. Later calls return at once.
type stubbornRun struct {
	calls    atomic.Int32
	writes   atomic.Int64
	started  chan struct{}
	release  chan struct{}
	relOnce  sync.Once
	finished chan struct{}
}

func newStubbornRun() *stubbornRun {
	return &stubbornRun{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
}

func (s *stubbornRun) releaseRun() { s.relOnce.Do(func() { close(s.release) }) }

func (s *stubbornRun) run(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
	if s.calls.Add(1) > 1 {
		return nil
	}
	defer close(s.finished)
	close(s.started)
	fence := registry.FenceFromContext(ctx)
	for {
		select {
		case <-s.release:
			return nil
		default:
		}
		// A recording writer: every write checks the fence first, like
		// repairs.Writer.beat.
		if fence.Check() == nil {
			s.writes.Add(1)
		}
		time.Sleep(time.Millisecond)
	}
}

// zombify enqueues def, waits for it to start, cancels it and waits until it
// is a zombie (listed by Zombies()). Since 05-PR4 a zombie is v3 "stopping":
// its row keeps reading "running" until its goroutine returns, and only then
// is its final status ("canceled" here) written.
func (h *zombieHarness) zombify(t *testing.T, defID string, s *stubbornRun) string {
	t.Helper()
	id, err := h.r.EnqueueOp(t.Context(), defID, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	recv(t, s.started, "first run to start")
	if err := h.r.Cancel(id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	h.awaitZombie(t, id)
	return id
}

// awaitZombie waits until id is a zombie and checks its row still reads
// "running" (stopping mirrors as running; nothing terminal is written while
// the goroutine lives).
func (h *zombieHarness) awaitZombie(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.r.ZombieCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.r.ZombieCount() != 1 {
		t.Fatalf("ZombieCount = %d, want 1", h.r.ZombieCount())
	}
	if got := h.store.statusOf(id); got != "running" {
		t.Fatalf("zombie row = %q while its goroutine lives, want running (v3 stopping)", got)
	}
}

// stayQueued runs n dispatch passes and fails if id leaves "queued".
func (h *zombieHarness) stayQueued(t *testing.T, id string, n int) {
	t.Helper()
	for range n {
		h.r.DispatchCycleForTest(t.Context())
		time.Sleep(2 * time.Millisecond)
		if got := h.store.statusOf(id); got != "queued" {
			t.Fatalf("op %s is %q while the zombie lives, want queued", id, got)
		}
	}
}

func TestFence_NoWriteAfterCancel(t *testing.T) {
	h := newZombieHarness(t, 2, nil)
	s := newStubbornRun()
	def := makeValidDef("test.fence-no-write")
	def.Run = s.run
	if err := h.r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	h.start(t, s.releaseRun)
	id, err := h.r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	deadline := time.Now().Add(5 * time.Second)
	for s.writes.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := h.r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	atCancel := s.writes.Load()
	if atCancel < 3 {
		t.Fatalf("writes before cancel = %d, want >= 3 (counted)", atCancel)
	}
	// At most the one write already past its fence check when Cancel
	// revoked can still land; after that, nothing.
	time.Sleep(20 * time.Millisecond)
	settled := s.writes.Load()
	if settled > atCancel+1 {
		t.Fatalf("writes went from %d at cancel to %d: the fence did not stop the run", atCancel, settled)
	}
	time.Sleep(200 * time.Millisecond)
	if got := s.writes.Load(); got != settled {
		t.Fatalf("writes rose from %d to %d after Cancel returned", settled, got)
	}
	if h.r.ZombieCount() != 1 {
		t.Errorf("ZombieCount = %d, want 1 (the run ignores ctx)", h.r.ZombieCount())
	}
}

// Mutation check: put releaseRunHandle back in the genuine-abandonment branch
// of executeRun (freeing the key at the grace) and step (c) goes red.
func TestZombie_HoldsKeyUntilGoroutineExits(t *testing.T) {
	h := newZombieHarness(t, 2, nil)
	s := newStubbornRun()
	def := makeValidDef("test.zombie-key")
	def.ConcurrencyKey = "k"
	def.Run = s.run
	if err := h.r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	h.start(t, s.releaseRun)

	first := h.zombify(t, def.ID, s)
	// (a) the row still reads "running" (v3 stopping, checked by zombify);
	// the dedupe skips it anyway (c).
	// (b) the zombie is listed with its key.
	z := h.r.Zombies()
	if len(z) != 1 || z[0].OpID != first || z[0].Key != "k" || z[0].DefID != def.ID || z[0].Since.IsZero() {
		t.Fatalf("Zombies() = %+v, want one entry for %s holding k", z, first)
	}
	// (c) a second enqueue gets a new id and stays queued behind the key.
	second, err := h.r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("second enqueue returned the zombie's id %s (C-3 dedupe regression)", first)
	}
	h.stayQueued(t, second, 20)
	if n := s.calls.Load(); n != 1 {
		t.Fatalf("Run called %d times while the zombie lives, want 1", n)
	}
	// (d) once the goroutine exits the key is released, and the second op
	// is claimed by the very next dispatch pass.
	s.releaseRun()
	recv(t, s.finished, "zombie goroutine to return")
	deadline := time.Now().Add(5 * time.Second)
	for h.r.ZombieCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := h.r.ZombieCount(); n != 0 {
		t.Fatalf("ZombieCount = %d after exit, want 0", n)
	}
	// The final status decided at abandonment lands when the goroutine
	// returns, before the key is released.
	if got := h.store.statusOf(first); got != "canceled" {
		t.Fatalf("first row = %q after the zombie exited, want canceled", got)
	}
	// Ask who holds k, not whether k is held: the background dispatcher can
	// claim second (and with it k) the moment the zombie frees it, while
	// second's row still reads "queued" until its worker's compare-and-set.
	// A key-set check could not tell that fresh claim from the zombie still
	// holding k, and failed about 1 run in 20.
	for key, holder := range h.r.HeldConcurrencyKeysForTest() {
		if holder == first {
			t.Fatalf("key %q still held by the zombie %s after its goroutine exited", key, first)
		}
	}
	h.r.DispatchCycleForTest(t.Context())
	if !h.r.IsRunning(second) && h.store.statusOf(second) == "queued" {
		t.Fatalf("op %s still unclaimed one dispatch pass after the zombie exited", second)
	}
	awaitStatus(t, h.store, second, "completed", 5*time.Second)
}

func TestZombie_WriteSetStillBlocks(t *testing.T) {
	h := newZombieHarness(t, 2, nil)
	s := newStubbornRun()
	za := makeValidDef("test.zombie-writes-a")
	za.Writes = []registry.Resource{registry.ResBooks}
	za.Run = s.run
	b := makeValidDef("test.zombie-writes-b")
	b.Writes = []registry.Resource{registry.ResBooks}
	for _, d := range []registry.OperationDef{za, b} {
		if err := h.r.RegisterOp(d); err != nil {
			t.Fatal(err)
		}
	}
	h.start(t, s.releaseRun)
	h.zombify(t, za.ID, s)
	idB, err := h.r.EnqueueOp(t.Context(), b.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.stayQueued(t, idB, 20)
	s.releaseRun()
	awaitStatus(t, h.store, idB, "completed", 5*time.Second)
}

func TestZombie_FreesWorkerSlot(t *testing.T) {
	h := newZombieHarness(t, 1, nil)
	s := newStubbornRun()
	za := makeValidDef("test.zombie-slot-a")
	za.Run = s.run
	b := makeValidDef("test.zombie-slot-b")
	for _, d := range []registry.OperationDef{za, b} {
		if err := h.r.RegisterOp(d); err != nil {
			t.Fatal(err)
		}
	}
	h.start(t, s.releaseRun)
	h.zombify(t, za.ID, s)
	idB, err := h.r.EnqueueOp(t.Context(), b.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, h.store, idB, "completed", 5*time.Second)
	if n := h.r.ZombieCount(); n != 1 {
		t.Errorf("ZombieCount = %d, want 1: b must have run while the zombie was alive", n)
	}
}

func TestZombie_ScanKeyUnchanged(t *testing.T) {
	h := newZombieHarness(t, 2, nil)
	s := newStubbornRun()
	def := makeValidDef(registry.ScanStandDownDefIDForTest)
	def.ConcurrencyKey = registry.ScanStandDownDefIDForTest
	def.ResumePolicy = registry.ResumeRestart
	def.Run = s.run
	if err := h.r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	h.start(t, s.releaseRun)
	zombieID := h.zombify(t, def.ID, s)
	resumed, err := h.r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.stayQueued(t, resumed, 20)
	held := h.r.HeldConcurrencyKeysForTest()
	if len(held) != 1 || held[registry.ScanStandDownDefIDForTest] != zombieID {
		t.Fatalf("held keys = %v, want exactly {%s: %s} (the zombie)", held, registry.ScanStandDownDefIDForTest, zombieID)
	}
	s.releaseRun()
	awaitStatus(t, h.store, resumed, "completed", 5*time.Second)
}

func TestZombie_CancelTwiceIsNoop(t *testing.T) {
	h := newZombieHarness(t, 2, nil)
	s := newStubbornRun()
	def := makeValidDef("test.zombie-cancel-twice")
	def.Run = s.run
	if err := h.r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	h.start(t, s.releaseRun)
	id := h.zombify(t, def.ID, s)
	for range 2 {
		if err := h.r.Cancel(id); err != nil {
			t.Fatalf("second cancel of a zombie: %v, want nil", err)
		}
	}
	if got := h.store.statusOf(id); got != "canceled" {
		t.Errorf("status = %q after repeated cancel, want canceled", got)
	}
	if n := h.r.ZombieCount(); n != 1 {
		t.Errorf("ZombieCount = %d, want 1", n)
	}
}

// The zombie a user is most likely to press Retry on is a scan a stand-down
// quiesced: its final status is interrupted_quiesced. Until its goroutine
// returns its row reads "running" (05-PR4), and Retry must still answer
// ErrOpActive, not "not interrupted".
func TestZombie_RetryRefused(t *testing.T) {
	h := newZombieHarness(t, 2, nil)
	s := newStubbornRun()
	def := makeValidDef(registry.ScanStandDownDefIDForTest)
	def.ResumePolicy = registry.ResumeRestart
	def.Run = s.run
	if err := h.r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	h.start(t, s.releaseRun)
	id, err := h.r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "scan to start")
	acquired := make(chan func(), 1)
	go func() {
		rel, aerr := h.r.AcquireScanStandDown(t.Context(), "holder-op", "test")
		if aerr != nil {
			rel = func() {}
		}
		acquired <- rel
	}()
	h.awaitZombie(t, id)
	if err := h.r.RetryInterrupted(t.Context(), id, "tester"); !errors.Is(err, registry.ErrOpActive) {
		t.Fatalf("retry of a live zombie = %v, want ErrOpActive", err)
	}
	if n := s.calls.Load(); n != 1 {
		t.Fatalf("Run called %d times, want 1", n)
	}
	s.releaseRun()
	rel := <-acquired
	rel()
}

// A user Cancel of a zombie must reach its row: the zombie handle only means
// "do not cancel the context twice", not "nothing left to do". Since 05-PR4 a
// zombie's row reads "running" (v3 stopping) until its goroutine returns, for
// a scan quiesced by a stand-down too, and a holder that gives up while the
// zombie lives no longer re-queues it in place (resumeQuiescedOp only takes an
// interrupted_quiesced row). Either way the user's cancel must win over the
// quiesce outcome decided at abandonment, and the scan must not run again.
func TestZombie_CancelReachesALiveRow(t *testing.T) {
	for _, holderGivesUp := range []bool{false, true} {
		t.Run(map[bool]string{false: "holder still waiting", true: "holder gave up"}[holderGivesUp], func(t *testing.T) {
			h := newZombieHarness(t, 2, nil)
			s := newStubbornRun()
			def := makeValidDef(registry.ScanStandDownDefIDForTest)
			def.ResumePolicy = registry.ResumeRestart
			def.Run = s.run
			if err := h.r.RegisterOp(def); err != nil {
				t.Fatal(err)
			}
			h.start(t, s.releaseRun)
			id, err := h.r.EnqueueOp(t.Context(), def.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			recv(t, s.started, "scan to start")

			actx, acancel := context.WithCancel(t.Context())
			defer acancel()
			acquired := make(chan func(), 1)
			go func() {
				rel, aerr := h.r.AcquireScanStandDown(actx, "holder-op", "test")
				if aerr != nil {
					rel = func() {}
				}
				acquired <- rel
			}()
			h.awaitZombie(t, id)
			if holderGivesUp {
				acancel()
				(<-acquired)()
				// The release found the row still "running" and left it.
				h.r.DispatchCycleForTest(t.Context())
				if got := h.store.statusOf(id); got != "running" {
					t.Fatalf("status after the holder gave up = %q, want running (no re-queue while the zombie lives)", got)
				}
			}

			if err := h.r.Cancel(id); err != nil {
				t.Fatalf("cancel of a zombie with a live row: %v", err)
			}
			if got := h.store.statusOf(id); got != "canceled" {
				t.Fatalf("status after cancel = %q, want canceled (the user's cancel was lost)", got)
			}

			// Let the zombie exit and the holder release: nothing may bring
			// the canceled scan back.
			s.releaseRun()
			if !holderGivesUp {
				(<-acquired)()
			}
			deadline := time.Now().Add(5 * time.Second)
			for h.r.ZombieCount() != 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if n := h.r.ZombieCount(); n != 0 {
				t.Fatalf("ZombieCount = %d after release, want 0", n)
			}
			for range 20 {
				h.r.DispatchCycleForTest(t.Context())
				time.Sleep(2 * time.Millisecond)
			}
			if got := h.store.statusOf(id); got != "canceled" {
				t.Errorf("status after the zombie exited = %q, want canceled", got)
			}
			if n := s.calls.Load(); n != 1 {
				t.Errorf("Run called %d times, want 1: the canceled scan ran again", n)
			}
		})
	}
}

// TestZombie_QuiescedScanRequeuedAfterExit: a scan a stand-down quiesced, whose
// goroutine outlives the grace, gets interrupted_quiesced only when its
// goroutine returns (05-PR4). If the holder already released by then, the
// release found a "running" row and could not re-queue it; the exit monitor
// must hand the scan back to the stand-down, or it would sit quiesced until
// the next boot. Mutation check: drop the resumeDroppedScanOnRelease call in
// executeRun's exit monitor and the scan never runs again.
func TestZombie_QuiescedScanRequeuedAfterExit(t *testing.T) {
	h := newZombieHarness(t, 2, nil)
	s := newStubbornRun()
	def := makeValidDef(registry.ScanStandDownDefIDForTest)
	def.ResumePolicy = registry.ResumeRestart
	def.Run = s.run
	if err := h.r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	h.start(t, s.releaseRun)
	id, err := h.r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "scan to start")
	actx, acancel := context.WithCancel(t.Context())
	acquired := make(chan func(), 1)
	go func() {
		rel, aerr := h.r.AcquireScanStandDown(actx, "holder-op", "test")
		if aerr != nil {
			rel = func() {}
		}
		acquired <- rel
	}()
	h.awaitZombie(t, id)
	acancel()
	(<-acquired)()
	if got := h.store.statusOf(id); got != "running" {
		t.Fatalf("status after the holder released = %q, want running", got)
	}
	s.releaseRun()
	awaitStatus(t, h.store, id, "completed", 5*time.Second)
	if n := s.calls.Load(); n != 2 {
		t.Fatalf("Run called %d times, want 2 (the quiesced scan resumed once)", n)
	}
}

// A zombie still alive when Shutdown times out is "stopping" with a final
// status decided at abandonment ("canceled": the user canceled it). Shutdown
// writes that status, since the goroutine will not get to. Mutation check:
// drop the zombieFinal write in Shutdown's slotReleased branch and the row is
// left "running" (the boot sweep would then decide it); drop the whole
// slotReleased branch and it is stamped interrupted_dropped from the
// ResumePolicy -- a stopped run made resumable.
func TestZombie_ShutdownTimeoutKeepsTerminalStatus(t *testing.T) {
	h := newZombieHarness(t, 2, nil)
	s := newStubbornRun()
	defer s.releaseRun()
	def := makeValidDef("test.zombie-shutdown")
	def.Run = s.run
	if err := h.r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	h.r.Start(t.Context())
	id := h.zombify(t, def.ID, s)
	sctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := h.r.Shutdown(sctx); err == nil {
		t.Fatal("Shutdown returned nil; the zombie should have held the drain until the context expired")
	}
	if got := h.store.statusOf(id); got != "canceled" {
		t.Fatalf("zombie row = %q after shutdown timeout, want canceled", got)
	}
}

// lockedBuffer is a bytes.Buffer safe for the registry's goroutines to log
// into while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines(substr string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

func TestZombie_WarnAfterTenMinutes(t *testing.T) {
	logs := &lockedBuffer{}
	h := newZombieHarness(t, 2, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	s := newStubbornRun()
	def := makeValidDef("test.zombie-warn")
	def.Run = s.run
	if err := h.r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	h.start(t, s.releaseRun)
	id := h.zombify(t, def.ID, s)
	const marker = "zombie op goroutine still alive"
	step := func(d time.Duration, want int) {
		t.Helper()
		h.clk.Advance(d)
		h.r.WatchdogCycleForTest()
		if got := logs.lines(marker); len(got) != want {
			t.Fatalf("after advancing %s: %d zombie warnings, want %d: %q", d, len(got), want, got)
		}
	}
	step(9*time.Minute+59*time.Second, 0) // +9m59s
	step(time.Second, 1)                  // +10m
	step(30*time.Second, 1)               // +10m30s
	step(30*time.Second, 2)               // +11m
	for _, l := range logs.lines(marker) {
		if !strings.Contains(l, "level=WARN") || !strings.Contains(l, id) || !strings.Contains(l, def.ID) {
			t.Errorf("warning lacks level, op id or def id: %q", l)
		}
	}
}

// fenceProbeRun hands its fence to the test, then blocks until ctx ends and
// reports the fence as the run saw it at that moment.
type fenceProbeRun struct {
	fence     chan registry.Fence
	atDone    chan fenceAtDone
	waitFence bool // return once the fence is revoked, not on ctx.Done
}

// fenceAtDone is the fence as the run saw it when it observed the stop.
type fenceAtDone struct {
	revoked bool  // Fence.Revoked(): the explicit revoke had landed
	check   error // Fence.Check(): what a fenced writer would get
}

func newFenceProbe() *fenceProbeRun {
	return &fenceProbeRun{fence: make(chan registry.Fence, 1), atDone: make(chan fenceAtDone, 1)}
}

func (p *fenceProbeRun) run(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
	f := registry.FenceFromContext(ctx)
	p.fence <- f
	if p.waitFence {
		for !f.Revoked() {
			select {
			case <-ctx.Done():
				p.atDone <- fenceAtDone{revoked: f.Revoked(), check: f.Check()}
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		p.atDone <- fenceAtDone{revoked: true, check: f.Check()}
		return nil
	}
	<-ctx.Done()
	p.atDone <- fenceAtDone{revoked: f.Revoked(), check: f.Check()}
	return ctx.Err()
}

func TestFence_RevokedOnEveryStopPath(t *testing.T) {
	type stopCase struct {
		name string
		// prep adjusts the def; stop stops the run once it has started.
		prep func(*registry.OperationDef, *fenceProbeRun)
		stop func(t *testing.T, h *zombieHarness, id string)
		// ordered: the explicit revoke must be visible when the run sees
		// ctx.Done. Timeout fires inside context.WithTimeout with no code of
		// ours running, so its revoke necessarily follows the cancel and is
		// only asserted to land; Check() still refuses at ctx.Done on every
		// path, timeout included, because the fence is bound to the run
		// context. (The deterministic revoke-before-cancel check is
		// TestCancelPaths_RevokeBeforeCancel in fence_internal_test.go.)
		ordered bool
	}
	cases := []stopCase{
		{name: "user cancel", ordered: true,
			stop: func(t *testing.T, h *zombieHarness, id string) {
				if err := h.r.Cancel(id); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "timeout", ordered: false,
			prep: func(d *registry.OperationDef, _ *fenceProbeRun) { d.Timeout = 20 * time.Millisecond },
			stop: func(*testing.T, *zombieHarness, string) {}},
		{name: "watchdog kill", ordered: true,
			prep: func(d *registry.OperationDef, _ *fenceProbeRun) { d.ProgressTimeout = time.Minute },
			stop: func(_ *testing.T, h *zombieHarness, _ string) {
				h.clk.Advance(2 * time.Minute)
				h.r.WatchdogCycleForTest()
			}},
		{name: "stand-down quiesce", ordered: true,
			prep: func(d *registry.OperationDef, _ *fenceProbeRun) {
				d.ID = registry.ScanStandDownDefIDForTest
				d.ResumePolicy = registry.ResumeRestart
			},
			stop: func(t *testing.T, h *zombieHarness, _ string) {
				go func() {
					if rel, err := h.r.AcquireScanStandDown(t.Context(), "holder-op", "test"); err == nil {
						rel()
					}
				}()
			}},
		{name: "shutdown", ordered: true,
			stop: func(_ *testing.T, h *zombieHarness, _ string) {
				go func() {
					sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = h.r.Shutdown(sctx)
				}()
			}},
		{name: "lost stand-down lease", ordered: true,
			prep: func(_ *registry.OperationDef, p *fenceProbeRun) { p.waitFence = true },
			stop: func(t *testing.T, h *zombieHarness, id string) {
				// The op holds no lease: a renewal reports it lost.
				if h.r.RenewScanStandDown(id) {
					t.Fatal("renewal of an unregistered holder succeeded")
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newZombieHarness(t, 2, nil)
			p := newFenceProbe()
			def := makeValidDef("test.fence-stop")
			def.Run = p.run
			if tc.prep != nil {
				tc.prep(&def, p)
			}
			if err := h.r.RegisterOp(def); err != nil {
				t.Fatal(err)
			}
			h.start(t)
			id, err := h.r.EnqueueOp(t.Context(), def.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			var fence registry.Fence
			select {
			case fence = <-p.fence:
			case <-time.After(5 * time.Second):
				t.Fatal("run did not start")
			}
			if fence.Revoked() || fence.Epoch() == 0 {
				t.Fatalf("fresh fence: revoked=%v epoch=%d, want valid with an epoch", fence.Revoked(), fence.Epoch())
			}
			tc.stop(t, h, id)
			var at fenceAtDone
			select {
			case at = <-p.atDone:
			case <-time.After(5 * time.Second):
				t.Fatal("run never saw the stop")
			}
			if !errors.Is(at.check, registry.ErrFenced) {
				t.Fatalf("Check() when the run saw the stop = %v, want ErrFenced", at.check)
			}
			if tc.ordered && !at.revoked {
				t.Fatal("the run saw ctx.Done() before its fence was revoked")
			}
			deadline := time.Now().Add(time.Second)
			for !fence.Revoked() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !fence.Revoked() {
				t.Fatal("fence not revoked after the stop")
			}
			if !errors.Is(fence.Check(), registry.ErrFenced) {
				t.Fatalf("Check() = %v, want ErrFenced", fence.Check())
			}
		})
	}
}

func TestFence_ZeroValueIsNeverRevoked(t *testing.T) {
	f := registry.FenceFromContext(context.Background())
	if f.Revoked() || f.Check() != nil || f.Epoch() != 0 {
		t.Fatalf("zero fence: revoked=%v check=%v epoch=%d", f.Revoked(), f.Check(), f.Epoch())
	}
}
