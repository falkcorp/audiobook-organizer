// file: internal/plugins/maintenance/rewrite_path_prefix_reason_test.go
// version: 1.0.0
// guid: 5f2a86c1-3de4-4b79-8a05-91c7d3e64b2f
// last-edited: 2026-09-20

package maintenance

import (
	"strings"
	"testing"
)

// Reproduces the prod symptom of 2026-09-20. checkRewriteBook stops at the
// FIRST failing field, so its verdict is book-level, but the report has one
// line per FIELD. recordBook used to stamp that one reason onto every line, so
// the Paolini rename dry run printed
//
//	new: .../02_Eldest_002_of_349.mp3
//	reason: "file does not exist on disk: .../02_Eldest_001_of_349.mp3"
//
// for every refused row — each line naming file 001 whatever file the line was
// actually about. Anyone debugging a genuine per-file refusal would be sent to
// the wrong file.
func TestRecordBook_ReasonNamingAPathGoesOnlyOnTheRowThatCausedIt(t *testing.T) {
	const missing = "/lib/Author/Book/001.mp3"
	bp := &rewriteBookPlan{
		BookID:     "book1",
		Bucket:     "target-missing",
		Reason:     "file does not exist on disk: " + missing,
		CauseRowID: "row1",
		CauseField: "book_file.file_path",
		Changes: []rewriteFieldChange{
			{Field: "book_file.file_path", RowID: "row1", Old: "/old/001.mp3", New: missing},
			{Field: "book_file.file_path", RowID: "row2", Old: "/old/002.mp3", New: "/lib/Author/Book/002.mp3"},
			{Field: "book_file.file_path", RowID: "row3", Old: "/old/003.mp3", New: "/lib/Author/Book/003.mp3"},
		},
	}

	var plan rewritePathPrefixPlan
	plan.recordBook(bp)

	if len(plan.all) != 3 {
		t.Fatalf("recorded %d decisions, want one per field (3)", len(plan.all))
	}
	for _, d := range plan.all {
		if d.Bucket != "target-missing" {
			t.Fatalf("row %s lost the book's bucket: %q", d.RowID, d.Bucket)
		}
		if d.RowID == "row1" {
			if d.Reason != bp.Reason {
				t.Fatalf("the causing row must carry the real reason, got %q", d.Reason)
			}
			continue
		}
		// The regression: a path in the reason of a row that is not about it.
		if strings.Contains(d.Reason, missing) {
			t.Fatalf("row %s (new=%s) cites an unrelated path: %q", d.RowID, d.New, d.Reason)
		}
		if !strings.Contains(d.Reason, "row1") {
			t.Fatalf("row %s should point at the causing row, got %q", d.RowID, d.Reason)
		}
	}
}

// A book-level verdict that names no path is still safe to repeat on every row,
// and must be, or those rows would lose their explanation entirely.
func TestRecordBook_PathlessVerdictRepeatsOnEveryRow(t *testing.T) {
	bp := &rewriteBookPlan{
		BookID: "book2",
		Bucket: "rewritable",
		Reason: "would rewrite",
		// No CauseRowID: no single field produced this.
		Changes: []rewriteFieldChange{
			{Field: "book.file_path", RowID: "book2", Old: "/old", New: "/new"},
			{Field: "book_file.file_path", RowID: "rowA", Old: "/old/a.mp3", New: "/new/a.mp3"},
		},
	}

	var plan rewritePathPrefixPlan
	plan.recordBook(bp)

	for _, d := range plan.all {
		if d.Reason != "would rewrite" {
			t.Fatalf("row %s lost its reason: %q", d.RowID, d.Reason)
		}
	}
}

// A single-field book has nothing to disambiguate: its one row is the cause.
func TestRecordBook_SingleFieldKeepsTheVerbatimReason(t *testing.T) {
	bp := &rewriteBookPlan{
		BookID:     "book3",
		Bucket:     "collision",
		Reason:     "book other already occupies /new",
		CauseRowID: "book3",
		CauseField: "book.file_path",
		Changes: []rewriteFieldChange{
			{Field: "book.file_path", RowID: "book3", Old: "/old", New: "/new"},
		},
	}

	var plan rewritePathPrefixPlan
	plan.recordBook(bp)

	if len(plan.all) != 1 || plan.all[0].Reason != bp.Reason {
		t.Fatalf("single-field book lost its reason: %+v", plan.all)
	}
}
