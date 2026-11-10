// file: core/tasks/taskstest/fake.go
// version: 1.0.0
// guid: 1e34b09c-9bdb-4abe-aef8-4d7c6678f250
// last-edited: 2026-11-10

package taskstest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// ErrUnsupported is what a FakeRun method returns for executor behaviour it
// does not model (children, stand-downs, lanes, writes).
var ErrUnsupported = errors.New("taskstest: not modelled by FakeRun")

// FakeRun is an in-memory run handle for testing code that takes a *tasks.RC
// without an executor: identity, a pause gate, a CPU-slot budget, a
// parallelism cap, a ref-lock table, named checkpoints and a progress log.
// Build it with NewFakeRun and pass RC() to the code under test. It is safe
// for concurrent use. Name coined by F2-01; the executor (F2-03) is the real
// implementation.
type FakeRun struct {
	ctx  context.Context
	kind tasks.Kind

	mu          sync.Mutex
	cond        *sync.Cond
	paused      bool
	cap         int
	slots       int // 0 = unlimited
	inUse       int
	maxInUse    int
	cpuAcquires int
	checkpoints map[string][]byte
	ckptErr     error
	progress    []ProgressEvent
	held        map[*fakeHold][]entity.Ref
}

type fakeHold struct{ done chan struct{} }

// ProgressEvent is one ItemsProgress delivery a FakeRun recorded.
type ProgressEvent struct {
	Done, Total int64
	Current     string
}

// NewFakeRun returns a run of kind whose context is ctx.
func NewFakeRun(ctx context.Context, kind tasks.Kind) *FakeRun {
	f := &FakeRun{ctx: ctx, kind: kind, checkpoints: map[string][]byte{}, held: map[*fakeHold][]entity.Ref{}}
	f.cond = sync.NewCond(&f.mu)
	return f
}

// RC wraps f in a *tasks.RC.
func (f *FakeRun) RC() *tasks.RC { return tasks.NewRC(f) }

// SetCap sets the operator parallelism cap (0 = none).
func (f *FakeRun) SetCap(n int) { f.mu.Lock(); f.cap = n; f.mu.Unlock() }

// SetCPUSlots limits concurrent AcquireCPU holders to n (0 = unlimited).
func (f *FakeRun) SetCPUSlots(n int) { f.mu.Lock(); f.slots = n; f.mu.Unlock() }

// Pause closes the pause gate: AwaitUnpaused blocks until Unpause.
func (f *FakeRun) Pause() { f.mu.Lock(); f.paused = true; f.mu.Unlock() }

// Unpause opens the pause gate.
func (f *FakeRun) Unpause() {
	f.mu.Lock()
	f.paused = false
	f.mu.Unlock()
	f.cond.Broadcast()
}

// FailCheckpoints makes every Checkpoint return err (nil restores success).
func (f *FakeRun) FailCheckpoints(err error) { f.mu.Lock(); f.ckptErr = err; f.mu.Unlock() }

// SetCheckpoint stores v under name, as an earlier attempt would have.
func (f *FakeRun) SetCheckpoint(name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.checkpoints[name] = raw
	f.mu.Unlock()
	return nil
}

// CheckpointJSON returns the raw checkpoint stored under name.
func (f *FakeRun) CheckpointJSON(name string) (json.RawMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.checkpoints[name]
	return raw, ok
}

// Progress returns the ItemsProgress deliveries, in order.
func (f *FakeRun) Progress() []ProgressEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProgressEvent(nil), f.progress...)
}

// CPUStats returns how many CPU slots were acquired and the most held at once.
func (f *FakeRun) CPUStats() (acquires, maxInUse int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cpuAcquires, f.maxInUse
}

// Context is the run context.
func (f *FakeRun) Context() context.Context { return f.ctx }

// RunID is a fixed ID derived from the kind.
func (f *FakeRun) RunID() tasks.RunID { return tasks.RunID("fake-" + string(f.kind)) }

// Kind is the run's kind.
func (f *FakeRun) Kind() tasks.Kind { return f.kind }

// Mode is ModeLive.
func (f *FakeRun) Mode() tasks.Mode { return tasks.ModeLive }

// Trigger is TriggerManual.
func (f *FakeRun) Trigger() tasks.Trigger { return tasks.TriggerManual }

// Actor is a fixed person.
func (f *FakeRun) Actor() tasks.Actor { return tasks.Actor{UserID: "bruce", AuthMethod: "session"} }

// Log discards.
func (f *FakeRun) Log() *slog.Logger { return slog.New(slog.DiscardHandler) }

// Writer is nil (a ReadOnly run).
func (f *FakeRun) Writer() tasks.Writer { return nil }

// Resumed reports whether any checkpoint is stored.
func (f *FakeRun) Resumed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.checkpoints) > 0
}

// Deadline is the context's deadline, or the zero time.
func (f *FakeRun) Deadline() time.Time {
	d, _ := f.ctx.Deadline()
	return d
}

// ShouldYield is always false.
func (f *FakeRun) ShouldYield() bool { return false }

// Phase does nothing.
func (f *FakeRun) Phase(string) (end func()) { return func() {} }

// Checkpoint JSON-encodes v under name.
func (f *FakeRun) Checkpoint(name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ckptErr != nil {
		return f.ckptErr
	}
	f.checkpoints[name] = raw
	return nil
}

// Restore decodes the checkpoint stored under name into v.
func (f *FakeRun) Restore(name string, v any) (bool, error) {
	f.mu.Lock()
	raw, ok := f.checkpoints[name]
	f.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, v)
}

// ReportProgress records nothing beyond liveness.
func (f *FakeRun) ReportProgress(int64, tasks.Total) {}

// Note does nothing.
func (f *FakeRun) Note(string) {}

// Touch does nothing.
func (f *FakeRun) Touch() {}

// Enqueue is not modelled.
func (f *FakeRun) Enqueue(tasks.Kind, any, ...tasks.StartOption) (tasks.RunRef, error) {
	return tasks.RunRef{}, ErrUnsupported
}

// Await is not modelled.
func (f *FakeRun) Await(tasks.RunRef) (tasks.Outcome, error) { return tasks.Outcome{}, ErrUnsupported }

// StandDown is not modelled.
func (f *FakeRun) StandDown(tasks.Key) (tasks.Hold, error) { return nil, ErrUnsupported }

// Lane is not modelled.
func (f *FakeRun) Lane(context.Context, tasks.LaneID) (tasks.LaneLease, error) {
	return nil, ErrUnsupported
}

// LockRefs takes refs in the fake's table: all-or-wait, cancellable through
// ctx, with tasks.Overlaps as the conflict rule.
func (f *FakeRun) LockRefs(ctx context.Context, refs ...entity.Ref) (release func(), err error) {
	for {
		f.mu.Lock()
		var wait chan struct{}
	scan:
		for h, rs := range f.held {
			for _, have := range rs {
				for _, want := range refs {
					if tasks.Overlaps(have, want) {
						wait = h.done
						break scan
					}
				}
			}
		}
		if wait == nil {
			h := &fakeHold{done: make(chan struct{})}
			f.held[h] = refs
			f.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					f.mu.Lock()
					delete(f.held, h)
					f.mu.Unlock()
					close(h.done)
				})
			}, nil
		}
		f.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// CapWorkers applies SetCap.
func (f *FakeRun) CapWorkers(declared int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cap > 0 {
		return min(declared, f.cap)
	}
	return declared
}

// AwaitUnpaused blocks while the gate is closed, or until ctx is done.
func (f *FakeRun) AwaitUnpaused(ctx context.Context) error {
	stop := context.AfterFunc(ctx, f.wake)
	defer stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.paused {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		f.cond.Wait()
	}
	return ctx.Err()
}

// wake broadcasts under the lock, so a waiter that has checked ctx but not
// yet parked cannot miss it.
func (f *FakeRun) wake() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cond.Broadcast()
}

// AcquireCPU takes one slot, waiting while SetCPUSlots' limit is reached.
func (f *FakeRun) AcquireCPU(ctx context.Context) (release func(), err error) {
	stop := context.AfterFunc(ctx, f.wake)
	defer stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.slots > 0 && f.inUse >= f.slots {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		f.cond.Wait()
	}
	f.inUse++
	f.cpuAcquires++
	f.maxInUse = max(f.maxInUse, f.inUse)
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.inUse--
			f.mu.Unlock()
			f.cond.Broadcast()
		})
	}, nil
}

// RenewHold succeeds: the fake holds no stand-down.
func (f *FakeRun) RenewHold(context.Context) error { return nil }

// ItemsProgress records the delivery.
func (f *FakeRun) ItemsProgress(done, total int64, current string) {
	f.mu.Lock()
	f.progress = append(f.progress, ProgressEvent{Done: done, Total: total, Current: current})
	f.mu.Unlock()
}
