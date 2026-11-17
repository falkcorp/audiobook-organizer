// file: internal/seriesdenumber/report_test.go
// version: 1.0.0
// guid: 08f7a3d1-c46e-4b92-a15d-6e30b9c8f27a
// last-edited: 2026-11-16

package seriesdenumber

import (
	"strings"
	"testing"
)

func TestRenderReport_EveryRowHasTheSameColumns(t *testing.T) {
	out := RenderReport([]ReportRow{
		{Kind: KindPlan, FromSeriesID: 4, FromName: "Court of Owls 05", IntoName: "Court of Owls", Position: 5, Books: 2, Shape: "trailing_bare", Confidence: "high", Eligible: true, Reason: "zero-padded position"},
		{Kind: KindBook, FromSeriesID: 4, BookID: "b1", NewSeriesID: "9", NewSeq: "5", Status: "planned"},
		{Kind: KindHeldBook, FromSeriesID: 4, BookID: "b2", OldSeq: "2", Status: "held: own_position_differs"},
	})
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("%d lines, want a header and 3 rows:\n%s", len(lines), out)
	}
	if lines[0] != ReportHeader {
		t.Fatalf("header = %q", lines[0])
	}
	for i, l := range lines {
		if n := len(strings.Split(l, "\t")); n != ReportColumns {
			t.Errorf("line %d has %d columns, want %d: %q", i, n, ReportColumns, l)
		}
	}
	if n := len(strings.Split(ReportHeader, "\t")); n != ReportColumns {
		t.Fatalf("ReportColumns = %d but the header has %d", ReportColumns, n)
	}
}

func TestRenderReport_NeutralisesTabsAndNewlines(t *testing.T) {
	out := RenderReport([]ReportRow{{Kind: KindPlan, FromName: "Broken\tName\nHere", Reason: "a\rb", BookID: "x\ty"}})
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2: an embedded newline split the row:\n%s", len(lines), out)
	}
	if n := len(strings.Split(lines[1], "\t")); n != ReportColumns {
		t.Fatalf("%d columns, want %d: an embedded tab shifted the row: %q", n, ReportColumns, lines[1])
	}
}

func TestSeqString(t *testing.T) {
	five := 5
	if SeqString(nil) != "" || SeqString(&five) != "5" {
		t.Fatalf("SeqString: %q %q", SeqString(nil), SeqString(&five))
	}
}
