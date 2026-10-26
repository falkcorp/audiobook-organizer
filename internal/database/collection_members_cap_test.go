// file: internal/database/collection_members_cap_test.go
// version: 1.0.0
// guid: d4f7a2c9-6e13-4b58-a0d1-3c8b5e9f7a46
// last-edited: 2026-10-26

package database

import (
	"fmt"
	"testing"
)

func TestCapMembers(t *testing.T) {
	ids := make([]string, MaxMaterializedMembers+7)
	for i := range ids {
		ids[i] = fmt.Sprintf("b%d", i)
	}
	kept, total := CapMembers(ids)
	if len(kept) != MaxMaterializedMembers || total != len(ids) {
		t.Fatalf("over the bound: kept %d total %d, want %d and %d", len(kept), total, MaxMaterializedMembers, len(ids))
	}
	if kept[0] != "b0" || kept[len(kept)-1] != fmt.Sprintf("b%d", MaxMaterializedMembers-1) {
		t.Errorf("the first %d ids are kept in order", MaxMaterializedMembers)
	}
	kept[0] = "changed"
	if ids[0] != "b0" {
		t.Errorf("the cut list shares the full list's backing array")
	}
	kept[0] = "b0"

	exact := ids[:MaxMaterializedMembers]
	if kept, total := CapMembers(exact); len(kept) != MaxMaterializedMembers || total != 0 {
		t.Errorf("at the bound: kept %d total %d, want %d and 0 (nothing cut)", len(kept), total, MaxMaterializedMembers)
	}
	if kept, total := CapMembers(nil); kept != nil || total != 0 {
		t.Errorf("nil: kept %v total %d", kept, total)
	}
}

func TestScrubPerViewerMembersClearsTheTotalToo(t *testing.T) {
	SetCollectionPerViewerClassifier(func(string) bool { return true })
	t.Cleanup(func() { SetCollectionPerViewerClassifier(nil) })
	col := &Collection{Type: CollectionTypeDynamic, Query: "x", MaterializedBookIDs: []string{"b1"}, MaterializedTotal: 12000}
	if !scrubPerViewerMembers(col) || len(col.MaterializedBookIDs) != 0 || col.MaterializedTotal != 0 {
		t.Errorf("after the scrub: ids %v total %d", col.MaterializedBookIDs, col.MaterializedTotal)
	}
}
