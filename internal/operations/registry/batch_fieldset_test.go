// file: internal/operations/registry/batch_fieldset_test.go
// version: 1.1.1
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-09-02

// batch_fieldset_test.go is the TDD regression test for C1 (C1 = "give the
// registry a real book-aware store for dep evaluation").
//
// Before the fix: batchFire called AllSatisfied(OpsV2DepAdapter{r.store}, ...)
// and OpsV2DepAdapter.GetBookByID always returned (nil,nil), so every
// ReqFieldSet requirement evaluated as "unmet" — subjects were re-bucketed
// forever and the dedup op never fired.
//
// After the fix: batchFire calls AllSatisfied(r.combinedDepStore(), ...) and
// r.combinedDepStore() delegates GetBookByID to r.depBookStore, which returns
// a real *database.Book. Subjects whose book has book_sig_v1 set are
// dispatched; subjects whose book has book_sig_v1 nil/empty stay bucketed.
//
// Note: I2 (per-subject dep-completion notifications for batched ops) is
// covered by the unit tests for subjectsFromParams in deps_test.go, which
// verify both the v1 single-subject shape and the batched {"subjects":[...]}
// shape that worker.go iterates over after a batch op completes.

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// fakeBookStore is a controllable DepBookStore for C1 tests.
// Callers populate books before the test runs; GetBookByID returns the stored
// value, or (nil, nil) when absent (simulating a book without book_sig_v1).
type fakeBookStore struct {
	books map[string]*database.Book
}

func newFakeBookStore() *fakeBookStore {
	return &fakeBookStore{books: make(map[string]*database.Book)}
}

// setBook registers a book returned by GetBookByID.
func (f *fakeBookStore) setBook(b *database.Book) {
	f.books[b.ID] = b
}

func (f *fakeBookStore) GetBookByID(id string) (*database.Book, error) {
	return f.books[id], nil
}

func (f *fakeBookStore) BookFiles(_ string) ([]string, error) {
	return nil, nil // AllFiles requirements are not tested here
}

// bookWithSig builds a minimal *database.Book whose book_sig_v1 is set.
func bookWithSig(id, sig string) *database.Book {
	return &database.Book{ID: id, BookSigV1: &sig}
}

// bookWithoutSig builds a minimal *database.Book with book_sig_v1 == nil.
func bookWithoutSig(id string) *database.Book {
	return &database.Book{ID: id}
}

// batchFieldSetDef returns a Batchable def that requires book_sig_v1 to be set.
func batchFieldSetDef(id string, bw, bmw time.Duration) registry.OperationDef {
	return registry.OperationDef{
		ID:           id,
		Liveness:     registry.LivenessManual,
		Plugin:       "test",
		DisplayName:  "FieldSet Batch Test",
		Run:          func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil },
		ResumePolicy: registry.ResumeDrop,
		Batchable:    true,
		BatchWindow:  bw,
		BatchMaxWait: bmw,
		Requires: []registry.Requirement{
			{Kind: registry.ReqFieldSet, Field: "book_sig_v1"},
		},
	}
}

// newBatchRegistryWithBookStore creates a Registry wired with a real DepBookStore,
// proving the C1 fix: batchFire routes to combinedDepStore instead of the
// always-nil OpsV2DepAdapter.
func newBatchRegistryWithBookStore(store *fakeStore, bs registry.DepBookStore) *registry.Registry {
	r := registry.NewWithOptions(store, slog.Default(), 1, registry.Options{})
	r.SetDepBookStore(bs)
	return r
}

// TestBatch_FieldSet_ReadySubjectDispatched verifies C1:
//
//   - A batchable op requires {ReqFieldSet, Field:"book_sig_v1"}.
//   - Subject "book-a" has book_sig_v1 SET in the fake book store.
//   - Subject "book-b" has book_sig_v1 NOT set (nil).
//
// Expected after batchFire:
//   - Exactly one dispatched OperationV2Row carrying only "book-a".
//   - "book-b" is re-bucketed (stays in the batch bucket).
//
// This test FAILS if batchFire still uses OpsV2DepAdapter (which always
// returns nil from GetBookByID → both subjects stay bucketed).
func TestBatch_FieldSet_ReadySubjectDispatched(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	bs := newFakeBookStore()
	bs.setBook(bookWithSig("book-a", "sig-abc"))
	bs.setBook(bookWithoutSig("book-b"))

	const defID = "test.fieldset-batch"
	r := newBatchRegistryWithBookStore(store, bs)
	def := batchFieldSetDef(defID, 50*time.Millisecond, 5*time.Second)
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	ctx := t.Context()
	r.Start(ctx)
	defer func() {
		if err := r.Shutdown(ctx); err != nil {
			t.Logf("Shutdown: %v", err)
		}
	}()

	// Enqueue both subjects. Both get bucketed (requirements not checked at
	// enqueue in the Batchable path — batchFire decides readiness).
	paramsA, _ := json.Marshal(map[string]any{"book_id": "book-a"})
	paramsB, _ := json.Marshal(map[string]any{"book_id": "book-b"})
	if _, err := r.EnqueueOp(ctx, defID, json.RawMessage(paramsA)); err != nil {
		t.Fatalf("EnqueueOp book-a: %v", err)
	}
	if _, err := r.EnqueueOp(ctx, defID, json.RawMessage(paramsB)); err != nil {
		t.Fatalf("EnqueueOp book-b: %v", err)
	}

	// Wait for batchFire to dispatch. The batch window is 50ms so this
	// should complete well within 2 seconds. Accept queued OR completed since
	// the single worker may run the op before pollForOp first sees it.
	var dispatched database.OperationV2Row
	found, ok := pollForOp(t, store, func(op database.OperationV2Row) bool {
		return op.DefID == defID && (op.Status == "queued" || op.Status == "completed")
	}, 2*time.Second)
	if !ok {
		t.Fatal("batchFire did not dispatch any op within 2s — C1 bug: all subjects were re-bucketed (book store not wired)")
	}
	dispatched = found

	// The dispatched row must carry only book-a (book-b lacks book_sig_v1).
	subs := subjectsFromParams(t, dispatched.Params)
	if len(subs) != 1 {
		t.Fatalf("expected 1 subject in dispatched op, got %d: %+v", len(subs), subs)
	}
	if subs[0].ID != "book-a" {
		t.Fatalf("expected dispatched subject ID %q, got %q", "book-a", subs[0].ID)
	}
	if subs[0].Type != "book" {
		t.Fatalf("expected subject type %q, got %q", "book", subs[0].Type)
	}

	// book-b must still be bucketed — poll to confirm it does NOT get dispatched.
	// We give it a short window (200ms) because if re-bucketing fired it would
	// show up quickly; absence after 200ms means it correctly stayed bucketed.
	time.Sleep(200 * time.Millisecond)
	store.mu.Lock()
	bucketKey := defID + ":book:book-b"
	_, stillBucketed := store.batchBucket[bucketKey]
	store.mu.Unlock()
	if !stillBucketed {
		t.Errorf("expected book-b to remain bucketed (book_sig_v1 not set), but it was cleared")
	}

	// Confirm only one op was dispatched total.
	total := countOpsWithDef(store, defID)
	if total != 1 {
		t.Errorf("expected exactly 1 dispatched op row, got %d", total)
	}
}
