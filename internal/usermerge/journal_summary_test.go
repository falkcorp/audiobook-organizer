// file: internal/usermerge/journal_summary_test.go
// version: 0.1.0
// guid: 3f0526db-44bb-40cc-83c9-a7c126af2007
// last-edited: 2026-11-22

package usermerge

import (
	"encoding/json"
	"strings"
	"testing"
)

// F4-08 B10p: the merges list tells the SPA which applied merge still has its
// straggler sweep to finish, so it can offer Resume and hold Undo back. The
// summary carries the flag and nothing else of core's half.
func TestJournalSummary_SaysWhenTheStragglerSweepIsPending(t *testing.T) {
	for _, tc := range []struct {
		name string
		core *CoreJournal
		want bool
	}{
		{"pending", &CoreJournal{SweepPending: true, IdentityIDs: []string{"gotham-sso/batman-subject-0001"}}, true},
		{"finished", &CoreJournal{}, false},
		{"a journal from before the flip", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := &Journal{ID: "j1", Status: StatusApplied, Core: tc.core}
			sum := j.Summary()
			if sum.SweepPending != tc.want {
				t.Fatalf("SweepPending = %v, want %v", sum.SweepPending, tc.want)
			}
			raw, err := json.Marshal(sum)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(raw), `"sweep_pending":true`); got != tc.want {
				t.Fatalf("summary JSON %s: sweep_pending present = %v, want %v", raw, got, tc.want)
			}
			if strings.Contains(string(raw), "batman-subject-0001") {
				t.Fatalf("the summary carries a sign-in's subject id: %s", raw)
			}
		})
	}
}
