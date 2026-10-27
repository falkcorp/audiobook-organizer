// file: internal/operations/registry/continue_in_place_guards_test.go
// version: 1.0.0
// guid: 0b7e4d92-3c51-4a86-8e1f-6d2a9c5b7f13
// last-edited: 2026-10-26

package registry_test

// The guards around ErrContinueInPlace: an attempt that yields without progress
// fails instead of looping, a yield during shutdown stays paused, and the old
// attempt's reporter is joined before its row is re-queued.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// terminalStatuses is the status of every op.terminal published for opID, in order.
func (b *t06TermBus) terminalStatuses(opID string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, ev := range b.events {
		if ev.name == "op.terminal" && ev.payload["op_id"] == opID {
			st, _ := ev.payload["status"].(string)
			out = append(out, st)
		}
	}
	return out
}

// resumedCreated counts the op.created events published for opID by a re-queue
// (resumed=true), as opposed to the enqueue's own.
func (b *t06TermBus) resumedCreated(opID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, ev := range b.events {
		if ev.name == "op.created" && ev.payload["op_id"] == opID && ev.payload["resumed"] == true {
			n++
		}
	}
	return n
}

// opLogText is the op's stored log lines joined.
func opLogText(t *testing.T, store *database.PebbleStore, id string) string {
	t.Helper()
	rows, err := store.GetOpLogsV2(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, r := range rows {
		sb.WriteString(r.Message)
		sb.WriteString("\n")
	}
	return sb.String()
}

// pauseLine is the op-log line the registry writes when it honours a yield.
const pauseLine = "continuing in place under the same operation id"

// assertNotQuiesced fails if the run was treated as a yield at any point: a v3
// stop reason of quiesce, an event carrying it, or the pause's own log line.
func assertNotQuiesced(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	if rec := runV3(t, store, id); rec.StopReason == state.StopQuiesce {
		t.Errorf("v3 stop reason = %q: a stopped run was recorded as a quiesce", rec.StopReason)
	}
	evts, err := store.ListRunEventsV3(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evts {
		if e.Reason == state.StopQuiesce || (e.To == state.V3Interrupted && e.Event == "v2_write") {
			t.Errorf("v3 event %+v: a stopped run was recorded as a quiesce / interrupted write before it ended", e)
		}
	}
	if logs := opLogText(t, store, id); strings.Contains(logs, pauseLine) {
		t.Errorf("op log %q carries the pause line: the stopped run was handled as a yield", logs)
	}
}

// A yield from an attempt that made no progress is a failure with its reason,
// never a restart. The first attempt DOES make progress, so the row's high-water
// progress is above zero and the registry's infinite-restart check is off: only
// the progress check stands between the second attempt and an endless loop.
// TouchLiveness is not progress.
func TestContinueInPlace_YieldWithoutProgressFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		quiet func(registry.Reporter)
	}{
		{"silent", func(registry.Reporter) {}},
		{"liveness only", registry.TouchLiveness},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, store := newV3OtelRig(t, registry.Options{})
			id := "test.continue-no-progress-" + strings.ReplaceAll(tc.name, " ", "-")
			def := makeValidDef(id)
			def.ResumePolicy = registry.ResumeRestart
			var attempts atomic.Int32
			def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
				if attempts.Add(1) == 1 {
					_ = rep.UpdateProgress(2, 10, "")
				} else {
					tc.quiet(rep)
				}
				return fmt.Errorf("more to do: %w", registry.ErrContinueInPlace)
			}
			if err := g.reg.RegisterOp(def); err != nil {
				t.Fatal(err)
			}
			g.reg.Start(t.Context())
			opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
			if err != nil {
				t.Fatal(err)
			}
			awaitV2(t, store, opID, "failed")
			time.Sleep(150 * time.Millisecond)
			if got := attempts.Load(); got != 2 {
				t.Errorf("Run was called %d times, want 2: the yield without progress must not be restarted", got)
			}
			row, _ := store.GetOperationV2(opID)
			if row == nil || row.ErrorMessage == nil || !strings.Contains(*row.ErrorMessage, "made no progress") {
				t.Fatalf("v2 row = %+v, want failed with the no-progress message", row)
			}
			if !strings.Contains(*row.ErrorMessage, "continue in place") {
				t.Errorf("message %q does not name the sentinel", *row.ErrorMessage)
			}
		})
	}
}

// orderStore records the order of the writes the in-place continue cares about
// and can inject a delay or a hook.
type orderStore struct {
	*database.PebbleStore
	mu       sync.Mutex
	seq      []string
	delayCur int           // UpdateOpProgressV2 with this current is delayed...
	delay    time.Duration // ...by this long
	onPause  func()        // run once, just before a pause is written
}

func (s *orderStore) note(x string) {
	s.mu.Lock()
	s.seq = append(s.seq, x)
	s.mu.Unlock()
}

func (s *orderStore) order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seq...)
}

func (s *orderStore) UpdateOpProgressV2(id string, cur, total int, msg string) error {
	if s.delay > 0 && cur == s.delayCur {
		time.Sleep(s.delay)
	}
	err := s.PebbleStore.UpdateOpProgressV2(id, cur, total, msg)
	s.note(fmt.Sprintf("progress:%d", cur))
	return err
}

func (s *orderStore) ResetOperationV2ForResume(id string) error {
	s.note("reset")
	return s.PebbleStore.ResetOperationV2ForResume(id)
}

func (s *orderStore) UpdateOperationV2StatusIf(id, from, status string, startedAt, completedAt *time.Time, errMsg *string) (bool, error) {
	if status == "interrupted_quiesced" {
		s.mu.Lock()
		f := s.onPause
		s.onPause = nil
		s.mu.Unlock()
		if f != nil {
			f()
		}
	}
	return s.PebbleStore.UpdateOperationV2StatusIf(id, from, status, startedAt, completedAt, errMsg)
}

// newOrderRig is a registry on an orderStore with a recording bus.
func newOrderRig(t *testing.T) (*registry.Registry, *orderStore, *t06TermBus) {
	t.Helper()
	store := &orderStore{PebbleStore: newV3Store(t)}
	bus := &t06TermBus{}
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		Bus:              bus,
		AbandonGrace:     50 * time.Millisecond,
		WatchdogInterval: time.Hour,
		SweepInterval:    time.Hour,
		AbandonedCap:     100,
	})
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(sctx)
	})
	return r, store, bus
}

// The old attempt's reporter is joined before the row is re-queued: its terminal
// flush writes the attempt's last progress, and landing after the re-queue it
// could overwrite the resumed attempt's. The flush is delayed here so the order
// is not left to timing.
func TestContinueInPlace_ReporterJoinedBeforeRequeue(t *testing.T) {
	r, store, _ := newOrderRig(t)
	store.delayCur, store.delay = 7, 300*time.Millisecond
	const id = "test.continue-join-order"
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	var attempts atomic.Int32
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		if attempts.Add(1) == 1 {
			_ = rep.UpdateProgress(7, 10, "") // flushed lazily: only the terminal flush writes it
			return fmt.Errorf("more to do: %w", registry.ErrContinueInPlace)
		}
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	opID, err := r.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store.PebbleStore, opID, "completed")

	seq := store.order()
	reset, flush := -1, -1
	for i, e := range seq {
		switch e {
		case "reset":
			if reset < 0 {
				reset = i
			}
		case "progress:7":
			flush = i
		}
	}
	if reset < 0 || flush < 0 {
		t.Fatalf("write order %v lacks the re-queue or the old attempt's final flush", seq)
	}
	if flush > reset {
		t.Errorf("write order %v: the old attempt's final progress flush landed AFTER the row was re-queued", seq)
	}
}

// A yield that reaches the worker while the registry is shutting down leaves the
// row paused for the next boot's resume sweep: no re-queue, no op.created. The
// flag flips inside the pause's own status write, the gap between the worker's
// decision and its re-queue that the guard exists for.
func TestContinueInPlace_YieldDuringShutdownStaysPaused(t *testing.T) {
	r, store, bus := newOrderRig(t)
	var once sync.Once
	store.onPause = func() {
		once.Do(func() {
			sctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_ = r.Shutdown(sctx) // sets the flag at its start; the run is already released, so it returns
		})
	}
	const id = "test.continue-shutdown-guard"
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	var attempts atomic.Int32
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		attempts.Add(1)
		_ = rep.UpdateProgress(3, 10, "")
		return fmt.Errorf("more to do: %w", registry.ErrContinueInPlace)
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	opID, err := r.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store.PebbleStore, opID, "interrupted_quiesced")
	time.Sleep(300 * time.Millisecond)
	if st := v2Status(t, store.PebbleStore, opID); st != "interrupted_quiesced" {
		t.Errorf("status = %q, want interrupted_quiesced: a yield during shutdown must stay paused", st)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("Run was called %d times, want 1", got)
	}
	if n := bus.resumedCreated(opID); n != 0 {
		t.Errorf("%d resumed op.created events, want 0: nothing was re-queued", n)
	}
	row, _ := store.GetOperationV2(opID)
	if row == nil || row.ResumeCount != 0 {
		t.Errorf("row = %+v, want resume_count 0", row)
	}
	// The pause could not be re-queued, so it is announced as the settled status it is.
	if got := bus.terminalStatuses(opID); len(got) != 1 || got[0] != "interrupted_quiesced" {
		t.Errorf("op.terminal statuses = %v, want [interrupted_quiesced]", got)
	}
}
