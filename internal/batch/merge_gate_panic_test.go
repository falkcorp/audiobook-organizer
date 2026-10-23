// file: internal/batch/merge_gate_panic_test.go
// version: 1.0.0
// guid: 7d2f9a14-6b83-4e05-91c7-a3e58d0b4f26
// last-edited: 2026-10-23

package batch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// panicModifyStore panics on the first write a restore makes.
type panicModifyStore struct{ *MockBookStore }

func (panicModifyStore) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	panic("store write exploded")
}

// panicPurger panics inside the purge.
type panicPurger struct{}

func (panicPurger) PurgeTrashedBookLocked(*database.Book) error { panic("purge exploded") }

// A gated item keeps the merge lock for its whole action, so a panic inside it
// must still release the lock: otherwise every later merge, combine and purge
// answers busy until the process restarts.
func TestGatedBatchItems_ReleaseTheMergeLockWhenTheActionPanics(t *testing.T) {
	restoreUpdates := map[string]any{"marked_for_deletion": false}
	cases := []struct {
		name string
		run  func()
	}{
		{"UpdateAudiobooks restore", func() {
			store := NewMockBookStore()
			store.books["a"] = trashedTestBook("a")
			NewBatchService(panicModifyStore{store}).UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{"a"}, Updates: restoreUpdates})
		}},
		{"ExecuteOperations update restore", func() {
			store := NewMockBookStore()
			store.books["a"] = trashedTestBook("a")
			NewBatchService(panicModifyStore{store}).ExecuteOperations(&BatchOperationsRequest{
				Operations: []BatchOperationItem{{ID: "a", Action: "update", Updates: restoreUpdates}}})
		}},
		{"ExecuteOperations restore", func() {
			store := NewMockBookStore()
			store.books["a"] = trashedTestBook("a")
			NewBatchService(panicModifyStore{store}).ExecuteOperations(&BatchOperationsRequest{
				Operations: []BatchOperationItem{{ID: "a", Action: "restore"}}})
		}},
		{"ExecuteOperations hard delete", func() {
			store := NewMockBookStore()
			store.books["a"] = trashedTestBook("a")
			NewBatchService(store, WithTrashPurger(panicPurger{})).ExecuteOperations(&BatchOperationsRequest{
				Operations: []BatchOperationItem{{ID: "a", Action: "delete", HardDelete: true}}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			func() {
				defer func() { require.NotNil(t, recover(), "the injected panic must reach the caller") }()
				tc.run()
			}()
			unlock, err := merge.TryLockMergeRMWWithin(merge.InteractiveMergeLockWait)
			require.NoError(t, err, "the merge lock was left held by the panicking action")
			unlock()
		})
	}
}
