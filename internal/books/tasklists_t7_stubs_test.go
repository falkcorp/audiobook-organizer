// file: internal/books/tasklists_t7_stubs_test.go
// version: 1.0.0
// guid: 5c2e9a17-84d3-4f60-b1a8-0e7d3c64f952
// last-edited: 2026-11-28

package books

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// TestF5_NoT7AdapterStubRemains is the flip's gate on the server t7 host adapter
// (internal/server/books_t7_adapter.go): every method of the adapter refuses
// until a group builds it, and the door leaves out every kind that reaches one,
// so the flip cannot ship with a stub still behind a kind HEAD runs. It is
// skipped while the predicate is off, like TestF5_NoHEADScheduledKindIsNotYetBuilt,
// and it reads the shipped value: it does not call SetTasksFlipForTest.
// Mutant: build one adapter method and leave the rest, with the predicate on
// (killed by FAIL TestF5_NoT7AdapterStubRemains, listing the stubs left).
func TestF5_NoT7AdapterStubRemains(t *testing.T) {
	if !tasksFlipOn() {
		t.Skip("the tasks flip is off: the t7 adapter may still hold stubs; this test bites when the switch commit turns it on")
	}
	stubs := (&server.Server{}).BooksT7Stubs()
	require.Empty(t, stubs, "the tasks flip is on and %d method(s) of the t7 host adapter still refuse", len(stubs))
}
