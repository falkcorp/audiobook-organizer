// file: internal/reconcile/reconcile_saveresult_test.go
// version: 1.0.0
// guid: 9a4e1c73-2b85-4f60-8d19-3e7c0a5b6f24
// last-edited: 2026-08-23

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// The reconcile result used to be written straight onto a v1 operations row
// with UpdateOperationResultData, whose error was discarded. It now goes through
// an injected SaveResultFunc, and the two writes deliberately treat failure
// differently:
//
//   - the FINAL write carries the payload the /latest endpoint reads back, so
//     losing it means a scan that succeeded shows an empty preview. Its error
//     fails the op.
//   - the INTERIM write is a mid-run progress snapshot that the final write
//     supersedes. Its error is logged and the run continues.
//
// That distinction was documented but enforced by nothing: reverting either
// branch to `_ =` broke no test. These two pin it.

// errSaveFailed stands in for a store write that fails.
var errSaveFailed = errors.New("store unavailable")

// A not-found book is skipped rather than aborting the loop, so one bogus match
// is enough to reach the final write without populating the store at all.
func TestExecuteReconcile_FinalResultWriteFailureFailsTheOp(t *testing.T) {
	store := newFakeStore()
	calls := 0
	saveResult := func(any) error {
		calls++
		return errSaveFailed
	}

	err := ExecuteReconcile(
		context.Background(), store, "op-1", saveResult,
		[]ReconcileApplyItem{{BookID: "missing-book", NewPath: "/nope"}},
		logger.New("reconcile-test"),
	)

	if err == nil {
		t.Fatal("a failed FINAL result write must fail the op: the preview payload " +
			"is gone and the UI would show an empty result for a scan that ran")
	}
	if !errors.Is(err, errSaveFailed) {
		t.Errorf("the underlying cause must be wrapped, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected exactly one save attempt, got %d", calls)
	}
}

// Empty matches is the only path that reaches the interim write.
func TestExecuteReconcile_InterimResultWriteFailureDoesNotFailTheRun(t *testing.T) {
	store := newFakeStore()
	saveResult := func(any) error { return errSaveFailed }

	err := ExecuteReconcile(
		context.Background(), store, "op-1", saveResult,
		nil, logger.New("reconcile-test"),
	)

	if err != nil {
		t.Fatalf("an interim snapshot write is advisory; its failure must not fail "+
			"the run, got %v", err)
	}
}

// applyFakeStore is the apply path's minimum: a book to move, a change record
// to write, and the update. newFakeStore implements neither
// CreateOperationChange nor a populated byID, so a cleanly-applying match needs
// its own double rather than a shared one bent to fit.
type applyFakeStore struct {
	database.Store
	book    *database.Book
	changes []*database.OperationChange
}

func (f *applyFakeStore) GetBookByID(string) (*database.Book, error) { return f.book, nil }

func (f *applyFakeStore) CreateOperationChange(c *database.OperationChange) error {
	f.changes = append(f.changes, c)
	return nil
}

func (f *applyFakeStore) UpdateBook(_ string, b *database.Book) (*database.Book, error) {
	return b, nil
}

// Control: a match that applies CLEANLY must succeed and round-trip its payload.
// Without this, the failure assertions above could be passing on the fake's
// shortcomings rather than on the write, and the final-write branch would never
// be seen returning nil.
func TestExecuteReconcile_SucceedsWhenTheResultWriteSucceeds(t *testing.T) {
	// The apply path stats NewPath, so it has to exist on disk.
	newPath := filepath.Join(t.TempDir(), "moved.m4b")
	if err := os.WriteFile(newPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &applyFakeStore{book: &database.Book{ID: "b1", FilePath: "/old/path.m4b"}}

	var got []byte
	saveResult := func(payload any) error {
		got, _ = payload.(json.RawMessage)
		return nil
	}

	err := ExecuteReconcile(
		context.Background(), store, "op-1", saveResult,
		[]ReconcileApplyItem{{BookID: "b1", NewPath: newPath}},
		logger.New("reconcile-test"),
	)
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	// A real round-trip, not a nil check: the payload must report the apply.
	if !strings.Contains(string(got), `"applied":1`) {
		t.Errorf("result payload must carry the applied count, got %s", got)
	}
	// And the undo record must have been written under the op id the caller
	// passed — that id is the only thing linking the change to the operation.
	if len(store.changes) != 1 || store.changes[0].OperationID != "op-1" {
		t.Errorf("expected one change recorded under op-1, got %+v", store.changes)
	}
}
