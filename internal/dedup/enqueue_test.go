// file: internal/dedup/enqueue_test.go
// version: 1.0.0
// guid: 0e7a4c2b-8d31-4f6a-b59e-1c3d7f2a9e64
// last-edited: 2026-10-16

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type recordingEnqueuer struct {
	defIDs []string
	params []any
	err    error
}

func (r *recordingEnqueuer) EnqueueOp(_ context.Context, defID string, params any, _ ...registry.EnqueueOption) (string, error) {
	r.defIDs = append(r.defIDs, defID)
	r.params = append(r.params, params)
	return "", r.err
}

func TestEnqueueCheckBook_SendsSingleBookParams(t *testing.T) {
	enq := &recordingEnqueuer{}
	if err := EnqueueCheckBook(t.Context(), enq, "book-1"); err != nil {
		t.Fatalf("EnqueueCheckBook: %v", err)
	}
	if len(enq.defIDs) != 1 || enq.defIDs[0] != "dedup.check-book" {
		t.Fatalf("defIDs = %v, want [dedup.check-book]", enq.defIDs)
	}
	raw, err := json.Marshal(enq.params[0])
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if string(raw) != `{"book_id":"book-1"}` {
		t.Fatalf("params = %s, want {\"book_id\":\"book-1\"}", raw)
	}
}

func TestEnqueueCheckBook_WrapsEnqueueError(t *testing.T) {
	sentinel := errors.New("registry full")
	err := EnqueueCheckBook(t.Context(), &recordingEnqueuer{err: sentinel}, "book-1")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping %v", err, sentinel)
	}
}

// Two calls inside one batch window must reach the op as a single run that
// carries both books: the helper's params shape is what the registry's batch
// engine turns into subjects.
func TestEnqueueCheckBook_TwoBooksCoalesceIntoOneRun(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStoreInMemory: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	type subjectsParams struct {
		Subjects []database.OpSubject `json:"subjects"`
	}
	runs := make(chan []string, 4)
	reg := registry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	def := registry.OperationDef{
		ID:           CheckBookOpID,
		Liveness:     registry.LivenessManual,
		Plugin:       "test",
		DisplayName:  "check-book coalescing test",
		ResumePolicy: registry.ResumeDrop,
		Batchable:    true,
		BatchWindow:  100 * time.Millisecond,
		BatchMaxWait: 5 * time.Second,
		Run: func(_ context.Context, raw json.RawMessage, _ registry.Reporter) error {
			var p subjectsParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			ids := make([]string, 0, len(p.Subjects))
			for _, s := range p.Subjects {
				ids = append(ids, s.Type+":"+s.ID)
			}
			sort.Strings(ids)
			runs <- ids
			return nil
		},
	}
	if err := reg.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	ctx := t.Context()
	reg.Start(ctx)
	t.Cleanup(func() { _ = reg.Shutdown(context.Background()) })

	for _, id := range []string{"book-a", "book-b"} {
		if err := EnqueueCheckBook(ctx, reg, id); err != nil {
			t.Fatalf("EnqueueCheckBook(%s): %v", id, err)
		}
	}

	select {
	case got := <-runs:
		if len(got) != 2 || got[0] != "book:book-a" || got[1] != "book:book-b" {
			t.Fatalf("run subjects = %v, want [book:book-a book:book-b]", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the batched run")
	}
	select {
	case extra := <-runs:
		t.Fatalf("second run %v: the two enqueues did not coalesce", extra)
	case <-time.After(300 * time.Millisecond):
	}
}
