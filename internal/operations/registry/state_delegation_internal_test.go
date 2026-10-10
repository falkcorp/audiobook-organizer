// file: internal/operations/registry/state_delegation_internal_test.go
// version: 1.0.0
// guid: e01798b1-4fa6-4d50-af2d-757e73ceebb7
// last-edited: 2026-10-10

package registry

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// delegationProbe is every known status plus strings the table does not
// list, so the fallbacks (unknown is live, unknown interrupted_* is settled)
// are compared too.
func delegationProbe() []string {
	out := []string{"", "banana", "interrupting", "cancelled", "interrupted_future", "interruptedx"}
	for _, s := range state.All() {
		out = append(out, string(s))
	}
	return out
}

// TestLegacyDelegates asserts the pre-existing classifiers answer exactly what
// the state table says, for every status. Each one used to carry its own list,
// and those lists had already disagreed with each other.
func TestLegacyDelegates(t *testing.T) {
	for _, s := range delegationProbe() {
		p := state.Classify(s)
		if got := database.IsTerminalV2Status(s); got != p.Terminal {
			t.Errorf("database.IsTerminalV2Status(%q) = %v, state Terminal = %v", s, got, p.Terminal)
		}
		if got := isTerminalStatus(s); got != p.Terminal {
			t.Errorf("isTerminalStatus(%q) = %v, state Terminal = %v", s, got, p.Terminal)
		}
		if got := IsTerminalStatus(s); got != p.Settled {
			t.Errorf("IsTerminalStatus(%q) = %v, state Settled = %v", s, got, p.Settled)
		}
		if got, want := IsInterruptedStatus(s), state.IsInterrupted(s); got != want {
			t.Errorf("IsInterruptedStatus(%q) = %v, state.IsInterrupted = %v", s, got, want)
		}
		if got, want := isInterruptedStatus(s), state.IsInterrupted(s); got != want {
			t.Errorf("isInterruptedStatus(%q) = %v, state.IsInterrupted = %v", s, got, want)
		}
	}
}

// TestDiscardableStatusesMatchTable pins Discard's store allowlist to the
// table's Discardable column, and to the exact set it held before it was
// derived, so a table edit cannot silently widen what Discard deletes.
func TestDiscardableStatusesMatchTable(t *testing.T) {
	want := []string{
		"completed", "failed", "canceled", "interrupted_dropped",
		"interrupted_quiesced", "interrupted_ask", "interrupted_restart", "interrupted",
	}
	got := slices.Clone(discardableStatuses)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("discardableStatuses = %v, want %v", got, want)
	}
	for _, s := range discardableStatuses {
		if !state.Classify(s).Discardable {
			t.Errorf("%q is in discardableStatuses but not Discardable in the table", s)
		}
	}
}
