// file: internal/plugins/maintenance/dedupe_book_file_rows_report_test.go
// version: 1.0.0
// guid: 5e3b8c71-2d94-4f0a-a6c7-91b4e2d0f358
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const dupeReportHeader = "book_id\ttitle\trows\tdistinct\tdup_rows\thas_fingerprint_on_dupe"

// readDupeReport returns the report's header line and its data lines, each
// split on tabs.
func readDupeReport(t *testing.T, path string) (string, [][]string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	var rows [][]string
	for _, l := range lines[1:] {
		rows = append(rows, strings.Split(l, "\t"))
	}
	return lines[0], rows
}

func TestWriteDupeRowsReport_HeaderAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "r.tsv") // parent dir must be created
	err := writeDupeRowsReport(path, []dupeReportRow{
		{BookID: "b1", Title: "One", Rows: 4, Distinct: 1, DupRows: 3, DupHasFP: true},
		{BookID: "b2", Title: "Two", Rows: 2, Distinct: 1, DupRows: 1},
	})
	if err != nil {
		t.Fatalf("writeDupeRowsReport: %v", err)
	}
	header, rows := readDupeReport(t, path)
	if header != dupeReportHeader {
		t.Fatalf("header = %q, want %q", header, dupeReportHeader)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d data lines, want 2", len(rows))
	}
	want := [][]string{{"b1", "One", "4", "1", "3", "true"}, {"b2", "Two", "2", "1", "1", "false"}}
	for i := range want {
		if strings.Join(rows[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}
}

// A title is user data. A tab, newline or carriage return in it must not shift
// the columns or split the row.
func TestWriteDupeRowsReport_TitleWithTabDoesNotBreakColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.tsv")
	if err := writeDupeRowsReport(path, []dupeReportRow{
		{BookID: "b1", Title: "Tab\there\nand\rthere", Rows: 2, Distinct: 1, DupRows: 1},
	}); err != nil {
		t.Fatalf("writeDupeRowsReport: %v", err)
	}
	_, rows := readDupeReport(t, path)
	if len(rows) != 1 || len(rows[0]) != 6 {
		t.Fatalf("rows = %q, want exactly 1 row of 6 fields", rows)
	}
}

// Zero affected books still writes the header, so "ran and found nothing" is
// distinguishable from "never ran".
func TestWriteDupeRowsReport_ZeroRowsWritesHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.tsv")
	if err := writeDupeRowsReport(path, nil); err != nil {
		t.Fatalf("writeDupeRowsReport: %v", err)
	}
	header, rows := readDupeReport(t, path)
	if header != dupeReportHeader || len(rows) != 0 {
		t.Fatalf("header=%q rows=%d, want header only", header, len(rows))
	}
}

// newDupeReportStore opens a warmed PebbleStore. Warmup is required: PASS 1 reads
// the memdb projection, and writes made before warmup publishes are not in it.
func newDupeReportStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return s
}

func runDupeReport(t *testing.T, s *database.PebbleStore, params DedupeBookFileRowsParams) [][]string {
	t.Helper()
	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(params)
	if err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{}); err != nil {
		t.Fatalf("runDedupeBookFileRows: %v", err)
	}
	header, rows := readDupeReport(t, params.ReportPath)
	if header != dupeReportHeader {
		t.Fatalf("header = %q", header)
	}
	return rows
}

// The dry run writes one row per affected book with the right shape and
// deletes nothing.
func TestDedupeBookFileRows_DryRunWritesReport(t *testing.T) {
	s := newDupeReportStore(t)
	ids := seedDupBooks(t, s, 3, 4)

	rows := runDupeReport(t, s, DedupeBookFileRowsParams{ReportPath: filepath.Join(t.TempDir(), "r.tsv")})
	if len(rows) != 3 {
		t.Fatalf("got %d report rows, want 3: %q", len(rows), rows)
	}
	titles := map[string]bool{}
	for _, r := range rows {
		if len(r) != 6 || r[2] != "4" || r[3] != "1" || r[4] != "3" || r[5] != "false" {
			t.Errorf("row %q, want rows=4 distinct=1 dup_rows=3 fp=false", r)
		}
		titles[r[1]] = true
	}
	if !titles["Dup Book 00"] {
		t.Errorf("titles %v: the book title lookup did not reach the report", titles)
	}
	for _, id := range ids {
		files, err := s.GetBookFiles(id)
		if err != nil || len(files) != 4 {
			t.Fatalf("book %s after dry run: %d rows (err %v), want 4 untouched", id, len(files), err)
		}
	}
}

// Workers finish out of order; the file must still be sorted so a dry run and
// the apply after it can be diffed.
func TestDedupeBookFileRows_ReportRowsAreSortedByBookID(t *testing.T) {
	s := newDupeReportStore(t)
	seedDupBooks(t, s, 6, 3)

	rows := runDupeReport(t, s, DedupeBookFileRowsParams{ReportPath: filepath.Join(t.TempDir(), "r.tsv")})
	if len(rows) != 6 {
		t.Fatalf("got %d rows, want 6", len(rows))
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r[0]
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("report book_ids not ascending: %v", ids)
	}
}

// ANTI-OVER-SUPPRESSION in the other direction: a writer that emitted a row for
// EVERY book would pass the tests above. Clean books must be absent.
func TestDedupeBookFileRows_ReportOmitsBooksWithNoDuplicates(t *testing.T) {
	s := newDupeReportStore(t)
	seedDupBooks(t, s, 3, 2)
	clean := map[string]bool{}
	for i := range 2 {
		bk, err := s.CreateBook(&database.Book{Title: "Clean " + string(rune('A'+i))})
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		if err := s.CreateBookFile(&database.BookFile{BookID: bk.ID, FilePath: "/lib/clean/" + bk.ID + ".m4b", Duration: 60}); err != nil {
			t.Fatalf("CreateBookFile: %v", err)
		}
		clean[bk.ID] = true
	}

	rows := runDupeReport(t, s, DedupeBookFileRowsParams{ReportPath: filepath.Join(t.TempDir(), "r.tsv")})
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want exactly the 3 duplicated books: %q", len(rows), rows)
	}
	for _, r := range rows {
		if clean[r[0]] {
			t.Errorf("clean book %s appeared in the report", r[0])
		}
	}
}

// has_fingerprint_on_dupe describes the REDUNDANT rows. rankKeeper keeps a
// fingerprinted row, so one fingerprint among twins is false (it survives) and
// two are true (one is on a row an apply would remove).
func TestDedupeBookFileRows_ReportFlagsFingerprintOnRedundantRow(t *testing.T) {
	s := newDupeReportStore(t)
	mk := func(title string, fps ...[]byte) string {
		bk, err := s.CreateBook(&database.Book{Title: title})
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		for _, fp := range fps {
			if err := s.CreateBookFile(&database.BookFile{
				BookID: bk.ID, FilePath: "/lib/fp/" + title + ".m4b", Duration: 60, AcoustIDFingerprint: fp,
			}); err != nil {
				t.Fatalf("CreateBookFile: %v", err)
			}
		}
		return bk.ID
	}
	both := mk("both", []byte{1, 2}, []byte{3, 4})
	one := mk("one", []byte{1, 2}, nil)

	rows := runDupeReport(t, s, DedupeBookFileRowsParams{ReportPath: filepath.Join(t.TempDir(), "r.tsv")})
	got := map[string]string{}
	for _, r := range rows {
		got[r[0]] = r[5]
	}
	if got[both] != "true" || got[one] != "false" {
		t.Fatalf("has_fingerprint_on_dupe: both=%q one=%q, want true/false", got[both], got[one])
	}
}
