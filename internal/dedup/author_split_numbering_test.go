// file: internal/dedup/author_split_numbering_test.go
// version: 1.0.0
// guid: 02e50424-99ee-46c0-8575-c944c7a33941
// last-edited: 2026-09-03

package dedup

import "testing"

// TestSplitCompositeAuthorName_RejectsNumberedComposites documents WHY the two
// composite-split call sites carry no CleanAuthorNameForCreation gate, and
// fails if that stops being true.
//
// The split scans in internal/plugins/maintenance/author.go and
// internal/scheduler/extra_ops.go both create an author row per part. Adding
// the creation gate there looks obviously right and is in fact useless: this
// splitter already refuses to split a composite carrying chapter numbering, so
// the gate could never fire for that class. It would, however, start dropping
// PUBLISHER parts ("Penguin Books / Bob Jones"), which is a different defect
// and out of scope for the numbering repair.
//
// If the splitter is ever loosened to accept numbered parts, this test fails
// and those two call sites need the gate for real.
func TestSplitCompositeAuthorName_RejectsNumberedComposites(t *testing.T) {
	for _, in := range []string{
		"001_Celestia / Kevin J Anderson",
		"01 Aftermath / Kevin J Anderson",
		"00 Prologue / Bob Jones",
		"001 of 301 / Bob Jones",
		"001-147 Kevin J Anderson / Bob Jones",
		"1 Pushing Ice, Alastair Reynolds",
	} {
		if parts := SplitCompositeAuthorName(in); len(parts) != 0 {
			t.Errorf("SplitCompositeAuthorName(%q) = %v; a numbered composite now splits, so the "+
				"split call sites need a CleanAuthorNameForCreation gate", in, parts)
		}
	}
	// The control: an ordinary composite still splits, so the assertion above
	// is about numbering and not about the splitter refusing everything.
	if parts := SplitCompositeAuthorName("Alice Smith / Bob Jones"); len(parts) != 2 {
		t.Fatalf("control composite did not split: got %v", parts)
	}
}
