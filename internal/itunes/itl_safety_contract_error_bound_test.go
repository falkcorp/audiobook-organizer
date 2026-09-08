// file: internal/itunes/itl_safety_contract_error_bound_test.go
// version: 1.0.0
// guid: 28d11da4-9f82-4023-b217-64a569932ad7
// last-edited: 2026-09-08
//
// ContractVerdict.Error() used to format every violation into one string. A
// systematically bad write-back produces one violation per mhoh block, and on
// 2026-09-07 that rendered a single 9,558,930-byte error that the write-back
// batcher logged into the activity database.

package itunes

import (
	"strings"
	"testing"
)

// verdictWithViolations builds a failing verdict carrying n violations on one
// guard, mirroring the prod shape (location-form rejecting every block).
func verdictWithViolations(n int) ContractVerdict {
	viols := make([]Violation, 0, n)
	for i := range n {
		viols = append(viols, Violation{
			Offset:  i * 4,
			Chunk:   "mhoh",
			Message: "0x0B contains staging marker '.itunes-writeback/': \"file://localhost/W:/audiobook-organizer/.itunes-writeback/iTunes Media/Audiobooks/Some Author/Some Book/01 Track.m4b\"",
		})
	}
	return ContractVerdict{
		Pass: false,
		Results: []GuardResult{{
			Guard:      "location-form",
			Violations: viols,
		}},
	}
}

func TestContractVerdictError_IsBoundedOnTheProdWorstCase(t *testing.T) {
	// ~100k violations is the shape that produced the 9.5 MB string.
	got := verdictWithViolations(100_000).Error()

	if len(got) > 16<<10 {
		t.Fatalf("Error() rendered %d bytes; it is a log string and must stay bounded", len(got))
	}
	if !strings.HasPrefix(got, "ITLSafetyContract REJECTED write:") {
		t.Fatalf("prefix changed, callers match on it: %q", got[:min(60, len(got))])
	}
	if !strings.Contains(got, "more violation(s)") {
		t.Fatal("a truncated verdict must say how many violations it did not list")
	}
	if !strings.Contains(got, "location-form") {
		t.Fatal("the failing guard must still be named")
	}
}

func TestContractVerdictError_ListsSmallVerdictsInFull(t *testing.T) {
	// Below the cap nothing is elided — the common case must not regress into
	// "and 3 more" when it could simply say them.
	got := verdictWithViolations(3).Error()

	if strings.Contains(got, "more violation(s)") {
		t.Fatalf("a 3-violation verdict was truncated: %q", got)
	}
	if n := strings.Count(got, "location-form@"); n != 3 {
		t.Fatalf("listed %d violations, want all 3", n)
	}
}

func TestContractVerdictError_PassingVerdictIsEmpty(t *testing.T) {
	// Callers do `if e := v.Error(); e != ""`, so this contract is load-bearing.
	if got := (ContractVerdict{Pass: true}).Error(); got != "" {
		t.Fatalf("passing verdict returned %q, want empty", got)
	}
}
