// file: internal/itunes/service/regroup_plan_test.go
// version: 1.0.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8a
// last-edited: 2026-06-20

package itunesservice

import (
	"reflect"
	"testing"
)

// snap is a tiny builder: pidLoc maps "PID"->"file@book", books lists IDs with
// a FileCount (other meta defaults).
func mkSnap(pidLoc map[string]PIDLoc, books map[string]BookMeta) Snapshot {
	return Snapshot{PIDLoc: pidLoc, Books: books}
}

func actionByTitle(p RegroupPlan, title string) GroupAction {
	for _, a := range p.Groups {
		if a.Title == title {
			return a
		}
	}
	return GroupAction{}
}

func TestPlanRegroup_FragmentationMerge(t *testing.T) {
	// One book's PIDs scattered across 3 fragment books → consolidate onto the
	// best survivor; the two emptied fragments are projected for deletion.
	groups := []HealGroup{{Title: "The Book", PIDs: []string{"p1", "p2", "p3"}}}
	snap := mkSnap(
		map[string]PIDLoc{
			"p1": {FileID: "f1", BookID: "B1"},
			"p2": {FileID: "f2", BookID: "B2"},
			"p3": {FileID: "f3", BookID: "B3"},
		},
		map[string]BookMeta{
			"B1": {ID: "B1", FileCount: 1, EnrichScore: 5}, // richest → survivor
			"B2": {ID: "B2", FileCount: 1},
			"B3": {ID: "B3", FileCount: 1},
		},
	)
	p := PlanRegroup(groups, snap)
	a := actionByTitle(p, "The Book")
	if a.Target != "B1" || a.FreshBook {
		t.Fatalf("target = %q fresh=%v, want B1 existing", a.Target, a.FreshBook)
	}
	if len(a.Moves) != 2 {
		t.Fatalf("moves = %d, want 2 (p2,p3 onto B1)", len(a.Moves))
	}
	if !reflect.DeepEqual(p.DeleteBooks, []string{"B2", "B3"}) {
		t.Fatalf("DeleteBooks = %v, want [B2 B3]", p.DeleteBooks)
	}
}

func TestPlanRegroup_AlreadyCorrect(t *testing.T) {
	groups := []HealGroup{{Title: "Solo", PIDs: []string{"p1", "p2"}}}
	snap := mkSnap(
		map[string]PIDLoc{"p1": {FileID: "f1", BookID: "B1"}, "p2": {FileID: "f2", BookID: "B1"}},
		map[string]BookMeta{"B1": {ID: "B1", FileCount: 2}},
	)
	p := PlanRegroup(groups, snap)
	a := actionByTitle(p, "Solo")
	if len(a.Moves) != 0 || a.FreshBook {
		t.Fatalf("want no moves and existing target, got moves=%d fresh=%v", len(a.Moves), a.FreshBook)
	}
	if p.AlreadyCorrect != 1 || len(p.DeleteBooks) != 0 {
		t.Fatalf("AlreadyCorrect=%d deletes=%v, want 1 and none", p.AlreadyCorrect, p.DeleteBooks)
	}
}

func TestPlanRegroup_OverMergeSplit_ExclusiveClaim(t *testing.T) {
	// THE advisor case: book B holds all of G1 (p1..p3) AND 2 of G2 (q1,q2);
	// G2's q3 lives on C. Without exclusive claim B would win survivor for BOTH
	// groups and the over-merge would persist (just retitled). With it, G1 (sorted
	// first) claims B, and G2 is forced off B onto C — the over-merge is SPLIT.
	groups := []HealGroup{
		{Title: "Alpha", PIDs: []string{"p1", "p2", "p3"}},
		{Title: "Beta", PIDs: []string{"q1", "q2", "q3"}},
	}
	snap := mkSnap(
		map[string]PIDLoc{
			"p1": {FileID: "fp1", BookID: "B"}, "p2": {FileID: "fp2", BookID: "B"}, "p3": {FileID: "fp3", BookID: "B"},
			"q1": {FileID: "fq1", BookID: "B"}, "q2": {FileID: "fq2", BookID: "B"},
			"q3": {FileID: "fq3", BookID: "C"},
		},
		map[string]BookMeta{
			"B": {ID: "B", FileCount: 5, EnrichScore: 9}, // would win both naively
			"C": {ID: "C", FileCount: 1},
		},
	)
	p := PlanRegroup(groups, snap)
	alpha := actionByTitle(p, "Alpha")
	beta := actionByTitle(p, "Beta")
	if alpha.Target != "B" {
		t.Fatalf("Alpha target = %q, want B", alpha.Target)
	}
	if beta.Target == "B" {
		t.Fatalf("Beta also targeted B — over-merge NOT split")
	}
	if beta.Target != "C" || beta.FreshBook {
		t.Fatalf("Beta target = %q fresh=%v, want C existing", beta.Target, beta.FreshBook)
	}
	// Beta must pull its 2 files off B onto C.
	if len(beta.Moves) != 2 {
		t.Fatalf("Beta moves = %d, want 2 (q1,q2 off B)", len(beta.Moves))
	}
	for _, m := range beta.Moves {
		if m.From != "B" {
			t.Errorf("Beta move %+v: want From=B", m)
		}
	}
	// B keeps Alpha's 3 files → not empty; C gains 2 → not empty.
	if len(p.DeleteBooks) != 0 {
		t.Fatalf("DeleteBooks = %v, want none", p.DeleteBooks)
	}
}

func TestPlanRegroup_OverMergeSplit_LoserGoesFresh(t *testing.T) {
	// Both groups live ONLY on B (no other holder). G1 claims B; G2 has no free
	// holder → fresh book, pulling its files off B.
	groups := []HealGroup{
		{Title: "Alpha", PIDs: []string{"p1", "p2"}},
		{Title: "Beta", PIDs: []string{"q1", "q2"}},
	}
	snap := mkSnap(
		map[string]PIDLoc{
			"p1": {FileID: "fp1", BookID: "B"}, "p2": {FileID: "fp2", BookID: "B"},
			"q1": {FileID: "fq1", BookID: "B"}, "q2": {FileID: "fq2", BookID: "B"},
		},
		map[string]BookMeta{"B": {ID: "B", FileCount: 4, EnrichScore: 9}},
	)
	p := PlanRegroup(groups, snap)
	beta := actionByTitle(p, "Beta")
	if !beta.FreshBook || beta.Target != "" {
		t.Fatalf("Beta should go fresh, got fresh=%v target=%q", beta.FreshBook, beta.Target)
	}
	if len(beta.Moves) != 2 {
		t.Fatalf("Beta moves = %d, want 2 onto fresh book", len(beta.Moves))
	}
	if p.FreshBooks != 1 {
		t.Fatalf("FreshBooks = %d, want 1", p.FreshBooks)
	}
	if len(p.DeleteBooks) != 0 {
		t.Fatalf("DeleteBooks = %v, want none (B keeps Alpha)", p.DeleteBooks)
	}
}

func TestPlanRegroup_VersionEntangledSkipped(t *testing.T) {
	groups := []HealGroup{{Title: "Risky", PIDs: []string{"p1", "p2"}}}
	snap := mkSnap(
		map[string]PIDLoc{"p1": {FileID: "f1", BookID: "B1"}, "p2": {FileID: "f2", BookID: "B2"}},
		map[string]BookMeta{
			"B1": {ID: "B1", FileCount: 1, VersionGroupID: "vg1", HasNonPrimaryMembers: true},
			"B2": {ID: "B2", FileCount: 1},
		},
	)
	p := PlanRegroup(groups, snap)
	a := actionByTitle(p, "Risky")
	if !a.Entangled || len(a.Moves) != 0 {
		t.Fatalf("want entangled skip with no moves, got %+v", a)
	}
	if p.EntangledSkipped != 1 || len(p.DeleteBooks) != 0 {
		t.Fatalf("EntangledSkipped=%d deletes=%v, want 1 and none", p.EntangledSkipped, p.DeleteBooks)
	}
}

func TestPlanRegroup_EntangledButAlreadyCorrect_NotSkipped(t *testing.T) {
	// All of the group's PIDs are already on ONE book that happens to be version-
	// entangled. Zero moves → entanglement is irrelevant → AlreadyCorrect, NOT
	// skipped. (Regression guard for the check-ordering bug that falsely skipped
	// ~95% of groups merely for touching a version-grouped book.)
	groups := []HealGroup{{Title: "Solo", PIDs: []string{"p1", "p2"}}}
	snap := mkSnap(
		map[string]PIDLoc{"p1": {FileID: "f1", BookID: "B1"}, "p2": {FileID: "f2", BookID: "B1"}},
		map[string]BookMeta{"B1": {ID: "B1", FileCount: 2, VersionGroupID: "vg1", HasNonPrimaryMembers: true}},
	)
	p := PlanRegroup(groups, snap)
	if p.AlreadyCorrect != 1 || p.EntangledSkipped != 0 {
		t.Fatalf("AlreadyCorrect=%d EntangledSkipped=%d, want 1/0", p.AlreadyCorrect, p.EntangledSkipped)
	}
}

func TestPlanRegroup_UnresolvedPIDs(t *testing.T) {
	// p3 is in the XML group but absent from the DB → counted unresolved; the
	// resolved p1,p2 still consolidate.
	groups := []HealGroup{{Title: "Partial", PIDs: []string{"p1", "p2", "p3"}}}
	snap := mkSnap(
		map[string]PIDLoc{"p1": {FileID: "f1", BookID: "B1"}, "p2": {FileID: "f2", BookID: "B2"}},
		map[string]BookMeta{"B1": {ID: "B1", FileCount: 1}, "B2": {ID: "B2", FileCount: 1}},
	)
	p := PlanRegroup(groups, snap)
	if p.PIDsResolved != 2 || p.PIDsUnresolved != 1 {
		t.Fatalf("resolved=%d unresolved=%d, want 2 and 1", p.PIDsResolved, p.PIDsUnresolved)
	}
	a := actionByTitle(p, "Partial")
	if len(a.Unresolved) != 1 || a.Unresolved[0] != "p3" {
		t.Fatalf("Unresolved = %v, want [p3]", a.Unresolved)
	}
}

func TestPlanRegroup_CompletenessMetrics(t *testing.T) {
	// "Complete": both PIDs present on a 2-file book. "Partial": only 1 of 3 PIDs
	// present, on a single-file book — a lone chapter of a multi-track book.
	groups := []HealGroup{
		{Title: "Complete", PIDs: []string{"a1", "a2"}},
		{Title: "Partial", PIDs: []string{"b1", "b2", "b3"}},
	}
	snap := mkSnap(
		map[string]PIDLoc{
			"a1": {FileID: "fa1", BookID: "BA"}, "a2": {FileID: "fa2", BookID: "BA"},
			"b1": {FileID: "fb1", BookID: "BB"}, // b2, b3 unresolved (not imported)
		},
		map[string]BookMeta{
			"BA": {ID: "BA", FileCount: 2},
			"BB": {ID: "BB", FileCount: 1},
		},
	)
	p := PlanRegroup(groups, snap)
	if p.CompleteGroups != 1 || p.PartialGroups != 1 {
		t.Fatalf("complete=%d partial=%d, want 1/1", p.CompleteGroups, p.PartialGroups)
	}
	if p.SingleFileChapterBooks != 1 {
		t.Fatalf("single-file-chapter books=%d, want 1 (BB)", p.SingleFileChapterBooks)
	}
	// Both are "already-correct" by the move metric (resolved PIDs each on one book)
	// — proving already-correct ≠ complete.
	if p.AlreadyCorrect != 2 {
		t.Fatalf("already-correct=%d, want 2 (partial still counts as no-move)", p.AlreadyCorrect)
	}
}

func TestPlanRegroup_Deterministic(t *testing.T) {
	groups := []HealGroup{
		{Title: "Alpha", PIDs: []string{"p1", "p2", "p3"}},
		{Title: "Beta", PIDs: []string{"q1", "q2", "q3"}},
	}
	snap := mkSnap(
		map[string]PIDLoc{
			"p1": {FileID: "fp1", BookID: "B"}, "p2": {FileID: "fp2", BookID: "B"}, "p3": {FileID: "fp3", BookID: "B"},
			"q1": {FileID: "fq1", BookID: "B"}, "q2": {FileID: "fq2", BookID: "B"}, "q3": {FileID: "fq3", BookID: "C"},
		},
		map[string]BookMeta{"B": {ID: "B", FileCount: 5, EnrichScore: 9}, "C": {ID: "C", FileCount: 1}},
	)
	first := PlanRegroup(groups, snap)
	for range 20 {
		if !reflect.DeepEqual(first, PlanRegroup(groups, snap)) {
			t.Fatal("PlanRegroup is non-deterministic")
		}
	}
}
