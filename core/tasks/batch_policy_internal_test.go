// file: core/tasks/batch_policy_internal_test.go
// version: 1.0.0
// guid: 5fb8d631-cfcc-475b-a25a-1bb02c3735de
// last-edited: 2026-11-10

package tasks

import "testing"

// TestBatchErrorPolicy_ItemsMode: the mapping the Batch runner (F2-03) passes
// to the item loop as ItemsOptions.OnError.
func TestBatchErrorPolicy_ItemsMode(t *testing.T) {
	var zero BatchErrorPolicy
	for _, c := range []struct {
		p    BatchErrorPolicy
		want ErrorMode
	}{{zero, Collect}, {BatchContinue, Collect}, {BatchStop, FailFast}} {
		if got := c.p.itemsMode(); got != c.want {
			t.Fatalf("%s.itemsMode() = %d, want %d", c.p, got, c.want)
		}
	}
}
