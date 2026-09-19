// file: internal/database/member_list_merge_test.go
// version: 1.1.0
// guid: 19523a7b-5688-4661-bc56-c23e5081d425
// last-edited: 2026-09-19

package database

import (
	"slices"
	"testing"
)

func TestMergeMemberListNoLoss(t *testing.T) {
	for _, tc := range []struct{ stored, incoming, want []string }{
		{[]string{"A", "B"}, []string{"B", "A"}, []string{"B", "A"}},           // pure reorder
		{[]string{"A", "B", "C"}, []string{"B", "A"}, []string{"B", "A", "C"}}, // stale list keeps C
		{[]string{"A"}, []string{"A", "D"}, []string{"A", "D"}},                // add
		{[]string{"A", "B"}, []string{"B", "B", "", "A"}, []string{"B", "A"}},  // dupes/empties
		{nil, []string{"X"}, []string{"X"}},
	} {
		if got := MergeMemberListNoLoss(tc.stored, tc.incoming); !slices.Equal(got, tc.want) {
			t.Errorf("MergeMemberListNoLoss(%v, %v) = %v, want %v", tc.stored, tc.incoming, got, tc.want)
		}
	}
}

func TestMemberListOmitted(t *testing.T) {
	canon := func(id string) string {
		if id == "loser" {
			return "winner"
		}
		return id
	}
	if got := MemberListOmitted([]string{"A", "B", "C"}, []string{"B", "A"}, nil); !slices.Equal(got, []string{"C"}) {
		t.Errorf("omitted = %v, want [C]", got)
	}
	if got := MemberListOmitted([]string{"A", "B"}, []string{"B", "A", "D"}, nil); len(got) != 0 {
		t.Errorf("reorder+add omitted = %v, want none", got)
	}
	if got := MemberListOmitted([]string{"loser"}, []string{"winner"}, canon); len(got) != 0 {
		t.Errorf("a loser named by its survivor counted as omitted: %v", got)
	}
}
