// file: internal/plugins/maintenance/dedupe_book_file_rows_crossfolder_test.go
// version: 1.1.0
// guid: 5bb37ed8-73c4-429c-8168-cc4e2fb52f0e
// last-edited: 2026-09-26

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestPlanCrossFolderPurges_Gates pins every gate of the owner-approved rule:
// a MISSING row is deleted only when EXACTLY ONE present row in the same book
// has the same basename and the same size, and nothing under books/itunes/** is
// ever touched. Every refusal carries a reason, because the preview is what an
// owner approves an apply from.
func TestPlanCrossFolderPurges_Gates(t *testing.T) {
	const size = 1000
	missing := database.BookFile{ID: "m", FilePath: "/lib/old/Ch 01.mp3", FileSize: size}
	twinA := database.BookFile{ID: "a", FilePath: "/lib/new/Ch 01.mp3", FileSize: size}
	twinB := database.BookFile{ID: "b", FilePath: "/lib/other/Ch 01.mp3", FileSize: size}
	other := database.BookFile{ID: "o", FilePath: "/lib/new/Ch 02.mp3", FileSize: size}

	cases := []struct {
		name       string
		candidate  database.BookFile
		all        []database.BookFile
		present    map[string]bool
		wantPurge  bool
		wantTwin   string
		wantReason string // substring of the skip reason
	}{
		{
			name: "one twin: delete", candidate: missing,
			all:       []database.BookFile{missing, twinA, other},
			present:   map[string]bool{twinA.FilePath: true, other.FilePath: true},
			wantPurge: true, wantTwin: "a",
		},
		{
			name: "zero twins: skip", candidate: missing,
			all:        []database.BookFile{missing, other},
			present:    map[string]bool{other.FilePath: true},
			wantReason: "no present row in the book has the same basename",
		},
		{
			name: "two twins: skip, ambiguous", candidate: missing,
			all:        []database.BookFile{missing, twinA, twinB},
			present:    map[string]bool{twinA.FilePath: true, twinB.FilePath: true},
			wantReason: "2 present rows share the basename and size",
		},
		{
			name: "size mismatch: skip", candidate: missing,
			all: []database.BookFile{missing,
				{ID: "a", FilePath: twinA.FilePath, FileSize: size + 1}},
			present:    map[string]bool{twinA.FilePath: true},
			wantReason: "none has size 1000",
		},
		{
			name: "basename differs only by case: skip (case-sensitive)", candidate: missing,
			all: []database.BookFile{missing,
				{ID: "a", FilePath: "/lib/new/ch 01.mp3", FileSize: size}},
			present:    map[string]bool{"/lib/new/ch 01.mp3": true},
			wantReason: "no present row in the book has the same basename",
		},
		{
			name: "twin whose file is also missing does not count", candidate: missing,
			all:        []database.BookFile{missing, twinA},
			present:    map[string]bool{},
			wantReason: "no present row in the book has the same basename",
		},
		{
			name:      "missing row under the iTunes tree: skip",
			candidate: database.BookFile{ID: "m", FilePath: "/mnt/books/itunes/Author/Ch 01.mp3", FileSize: size},
			all: []database.BookFile{
				{ID: "m", FilePath: "/mnt/books/itunes/Author/Ch 01.mp3", FileSize: size}, twinA},
			present:    map[string]bool{twinA.FilePath: true},
			wantReason: "row is under the iTunes tree",
		},
		{
			name: "twin under the iTunes tree: skip", candidate: missing,
			all: []database.BookFile{missing,
				{ID: "a", FilePath: "/mnt/Books/iTunes/Author/Ch 01.mp3", FileSize: size}},
			present:    map[string]bool{"/mnt/Books/iTunes/Author/Ch 01.mp3": true},
			wantReason: "twin is under the iTunes tree",
		},
		{
			name:      "no recorded size: skip",
			candidate: database.BookFile{ID: "m", FilePath: missing.FilePath},
			all: []database.BookFile{{ID: "m", FilePath: missing.FilePath},
				{ID: "a", FilePath: twinA.FilePath}},
			present:    map[string]bool{twinA.FilePath: true},
			wantReason: "no recorded size",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			purges, skips := planCrossFolderPurges("book", []database.BookFile{tc.candidate}, tc.all, tc.present)
			if tc.wantPurge {
				if len(purges) != 1 || len(skips) != 0 {
					t.Fatalf("purges=%v skips=%v, want exactly one purge", purges, skips)
				}
				if purges[0].TwinRowID != tc.wantTwin || purges[0].Action != decisionWouldDelete {
					t.Fatalf("purge = %+v, want twin %q action %q", purges[0], tc.wantTwin, decisionWouldDelete)
				}
				return
			}
			if len(purges) != 0 || len(skips) != 1 {
				t.Fatalf("purges=%v skips=%v, want exactly one skip", purges, skips)
			}
			if !strings.Contains(skips[0].Reason, tc.wantReason) {
				t.Fatalf("skip reason = %q, want it to contain %q", skips[0].Reason, tc.wantReason)
			}
		})
	}
}

// A present row is never a candidate, whatever twins it has.
func TestPlanCrossFolderPurges_PresentRowIsNeverACandidate(t *testing.T) {
	a := database.BookFile{ID: "a", FilePath: "/lib/x/f.mp3", FileSize: 5}
	b := database.BookFile{ID: "b", FilePath: "/lib/y/f.mp3", FileSize: 5}
	present := map[string]bool{a.FilePath: true, b.FilePath: true}
	purges, skips := planCrossFolderPurges("book", []database.BookFile{a, b}, []database.BookFile{a, b}, present)
	if len(purges)+len(skips) != 0 {
		t.Fatalf("present rows produced decisions: purges=%v skips=%v", purges, skips)
	}
}

// TestVerifyCrossFolderPurge_ApplyTimeGates pins the apply-time re-stat: the
// row's path must still be absent, and the twin must still be a regular file of
// the recorded size.
func TestVerifyCrossFolderPurge_ApplyTimeGates(t *testing.T) {
	dir := t.TempDir()
	twin := filepath.Join(dir, "new", "f.mp3")
	gone := filepath.Join(dir, "old", "f.mp3")
	cfWrite(t, twin, 4)
	d := bookFileRowDecision{Path: gone, TwinPath: twin, Size: 4}

	if ok, why := verifyCrossFolderPurge(d, os.Stat); !ok {
		t.Fatalf("clean case refused: %s", why)
	}

	// The missing file reappears between the plan and the apply.
	cfWrite(t, gone, 4)
	if ok, why := verifyCrossFolderPurge(d, os.Stat); ok || !strings.Contains(why, "exists again") {
		t.Fatalf("reappeared file: ok=%v why=%q, want a refusal", ok, why)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	// The twin changed size on disk.
	cfWrite(t, twin, 9)
	if ok, why := verifyCrossFolderPurge(d, os.Stat); ok || !strings.Contains(why, "twin size on disk is 9") {
		t.Fatalf("size drift: ok=%v why=%q, want a refusal", ok, why)
	}

	// The twin vanished.
	if err := os.Remove(twin); err != nil {
		t.Fatal(err)
	}
	if ok, why := verifyCrossFolderPurge(d, os.Stat); ok || !strings.Contains(why, "twin file is not readable") {
		t.Fatalf("missing twin: ok=%v why=%q, want a refusal", ok, why)
	}
}

func cfWrite(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// crossFolderFixture is one book whose chapter 1 was moved from old/ to new/
// without its old row being retired: new/ch1 and new/ch2 are present, the
// old/ch1 row points at nothing. The book's stored duration is deliberately
// stale so a test can tell that the apply recomputed it.
type crossFolderFixture struct {
	store              *database.PebbleStore
	bookID             string
	missingID, twinID  string
	missingPath, twin  string
	reportPath, others string
}

func newCrossFolderFixture(t *testing.T) crossFolderFixture {
	t.Helper()
	s := newDupeReportStore(t) // warmed PebbleStore; skips in -short
	dir := t.TempDir()
	fx := crossFolderFixture{store: s,
		missingPath: filepath.Join(dir, "old", "Ch 01.mp3"),
		twin:        filepath.Join(dir, "new", "Ch 01.mp3"),
		others:      filepath.Join(dir, "new", "Ch 02.mp3"),
		reportPath:  filepath.Join(t.TempDir(), "report.tsv"),
	}
	cfWrite(t, fx.twin, 1000)
	cfWrite(t, fx.others, 2000)
	fx.bookID = cfSeedBook(t, s, "Cross Folder Book", filepath.Join(dir, "new"))
	fx.twinID = cfSeedRow(t, s, fx.bookID, fx.twin, 1000, 600, "")
	cfSeedRow(t, s, fx.bookID, fx.others, 2000, 900, "")
	fx.missingID = cfSeedRow(t, s, fx.bookID, fx.missingPath, 1000, 600, "")
	cfSetStaleDuration(t, s, fx.bookID, 99999)
	return fx
}

func cfSeedBook(t *testing.T, s *database.PebbleStore, title, path string) string {
	t.Helper()
	b, err := s.CreateBook(&database.Book{Title: title, FilePath: path})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	return b.ID
}

func cfSeedRow(t *testing.T, s *database.PebbleStore, bookID, path string, size int64, dur int, hash string) string {
	t.Helper()
	f := &database.BookFile{BookID: bookID, FilePath: path, FileSize: size, Duration: dur, FileHash: hash}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	return f.ID
}

func cfSetStaleDuration(t *testing.T, s *database.PebbleStore, bookID string, d int) {
	t.Helper()
	b, err := s.GetBookByID(bookID)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	b.Duration = &d
	if _, err := s.UpdateBook(bookID, b); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}
}

func cfRowIDs(t *testing.T, s *database.PebbleStore, bookID string) map[string]bool {
	t.Helper()
	files, err := s.GetBookFiles(bookID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	out := map[string]bool{}
	for _, f := range files {
		out[f.ID] = true
	}
	return out
}

func cfRunDedupe(t *testing.T, s *database.PebbleStore, params DedupeBookFileRowsParams) *concurrentReporter {
	t.Helper()
	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(params)
	rep := &concurrentReporter{}
	if err := p.runDedupeBookFileRows(context.Background(), raw, rep); err != nil {
		t.Fatalf("runDedupeBookFileRows: %v", err)
	}
	return rep
}

func cfBookDuration(t *testing.T, s *database.PebbleStore, bookID string) int {
	t.Helper()
	b, err := s.GetBookByID(bookID)
	if err != nil || b == nil || b.Duration == nil {
		t.Fatalf("GetBookByID: %v (book %+v)", err, b)
	}
	return *b.Duration
}

// The preview lists the row it would delete, with its twin, and writes nothing.
func TestCrossFolder_PreviewWritesNothing(t *testing.T) {
	fx := newCrossFolderFixture(t)
	rep := cfRunDedupe(t, fx.store, DedupeBookFileRowsParams{
		CrossFolder: true, BookIDs: []string{fx.bookID}, ReportPath: fx.reportPath,
	})
	if ids := cfRowIDs(t, fx.store, fx.bookID); len(ids) != 3 {
		t.Fatalf("preview changed the row count to %d, want 3", len(ids))
	}
	if got := cfBookDuration(t, fx.store, fx.bookID); got != 99999 {
		t.Fatalf("preview recomputed the book (duration %d); it must write nothing", got)
	}
	logs := rep.loggedText()
	for _, want := range []string{"cross_folder would_delete", "row=" + fx.missingID, "twin=" + fx.twinID, fx.twin} {
		if !strings.Contains(logs, want) {
			t.Errorf("preview log is missing %q:\n%s", want, logs)
		}
	}
	raw, err := os.ReadFile(decisionsReportPath(fx.reportPath))
	if err != nil {
		t.Fatalf("decisions report: %v", err)
	}
	if !strings.Contains(string(raw), "cross_folder\twould_delete\t"+fx.bookID+"\t"+fx.missingID) {
		t.Fatalf("decisions report does not list the row:\n%s", raw)
	}
}

// TestCrossFolder_ApplyDeletesAndRecomputes is the recompute test: after the
// missing row goes, the book's stored duration is recomputed from the rows
// that survive, the same way the same-folder path does it.
func TestCrossFolder_ApplyDeletesAndRecomputes(t *testing.T) {
	fx := newCrossFolderFixture(t)
	rep := cfRunDedupe(t, fx.store, DedupeBookFileRowsParams{
		Apply: true, CrossFolder: true, BookIDs: []string{fx.bookID}, ReportPath: fx.reportPath,
	})
	// The op's own recompute ran (the store's delete hook also recomputes, so
	// the counter is what proves this path calls it, as the same-folder path does).
	if !strings.Contains(rep.loggedText(), "recomputed 1 books") ||
		!strings.Contains(rep.loggedText(), "cross_folder deleted") {
		t.Fatalf("summary does not report the deletion and recompute:\n%s", rep.loggedText())
	}
	ids := cfRowIDs(t, fx.store, fx.bookID)
	if ids[fx.missingID] || !ids[fx.twinID] || len(ids) != 2 {
		t.Fatalf("rows after apply = %v, want the twin and chapter 2 only", ids)
	}
	if got := cfBookDuration(t, fx.store, fx.bookID); got != 1500 {
		t.Fatalf("book duration = %d after apply, want 1500 recomputed from the surviving rows", got)
	}
	// The row is journaled so the deletion can be replayed.
	changes, err := fx.store.GetBookChanges(fx.bookID)
	if err != nil {
		t.Fatalf("GetBookChanges: %v", err)
	}
	journaled := false
	for _, c := range changes {
		if c.ChangeType == "book_file_delete" && c.FieldName == fx.missingID {
			journaled = true
		}
	}
	if !journaled {
		t.Fatal("the deleted row has no book_file_delete journal entry")
	}
}

// Cross-folder mode is off by default: the same book, without the flag, keeps
// its missing row.
func TestCrossFolder_OffByDefault(t *testing.T) {
	fx := newCrossFolderFixture(t)
	cfRunDedupe(t, fx.store, DedupeBookFileRowsParams{Apply: true, BookIDs: []string{fx.bookID}, ReportPath: fx.reportPath})
	if !cfRowIDs(t, fx.store, fx.bookID)[fx.missingID] {
		t.Fatal("the missing row was deleted without cross_folder:true")
	}
}

// The missing file reappears between the plan and the apply-time re-stat: the
// row must survive and the decision must say why.
func TestCrossFolder_FileReappearsAtApplyTime(t *testing.T) {
	fx := newCrossFolderFixture(t)
	orig := crossFolderApplyStat
	t.Cleanup(func() { crossFolderApplyStat = orig })
	crossFolderApplyStat = func(p string) (os.FileInfo, error) {
		if p == fx.missingPath {
			cfWrite(t, p, 1000) // it comes back just before the delete
		}
		return os.Stat(p)
	}
	rep := cfRunDedupe(t, fx.store, DedupeBookFileRowsParams{
		Apply: true, CrossFolder: true, BookIDs: []string{fx.bookID}, ReportPath: fx.reportPath,
	})
	if !cfRowIDs(t, fx.store, fx.bookID)[fx.missingID] {
		t.Fatal("the row was deleted although its file reappeared at apply time")
	}
	if !strings.Contains(rep.loggedText(), "exists again") {
		t.Fatalf("no skip reason logged:\n%s", rep.loggedText())
	}
}

// book_ids scopes the run: an identical book outside the scope is untouched.
func TestCrossFolder_BookIDsScope(t *testing.T) {
	fx := newCrossFolderFixture(t)
	dir := t.TempDir()
	twin := filepath.Join(dir, "new", "Ch 01.mp3")
	cfWrite(t, twin, 1000)
	otherBook := cfSeedBook(t, fx.store, "Out Of Scope", filepath.Join(dir, "new"))
	cfSeedRow(t, fx.store, otherBook, twin, 1000, 600, "")
	outMissing := cfSeedRow(t, fx.store, otherBook, filepath.Join(dir, "old", "Ch 01.mp3"), 1000, 600, "")

	cfRunDedupe(t, fx.store, DedupeBookFileRowsParams{
		Apply: true, CrossFolder: true, BookIDs: []string{fx.bookID}, ReportPath: fx.reportPath,
	})
	if cfRowIDs(t, fx.store, fx.bookID)[fx.missingID] {
		t.Fatal("the in-scope missing row survived")
	}
	if !cfRowIDs(t, fx.store, otherBook)[outMissing] {
		t.Fatal("a book outside book_ids was modified")
	}
}

// remove_row_ids: a present extra row is removed only with an identical-hash
// present twin or confirmed_duplicate; the file on disk always stays.
func TestRemoveRowIDs_Gates(t *testing.T) {
	s := newDupeReportStore(t)
	dir := t.TempDir()
	report := filepath.Join(t.TempDir(), "r.tsv")
	ch := filepath.Join(dir, "Ch 31.mp3")
	extra := filepath.Join(dir, "The Testament.mp3")
	cfWrite(t, ch, 100)
	cfWrite(t, extra, 110)
	book := cfSeedBook(t, s, "The Testament", dir)
	cfSeedRow(t, s, book, ch, 100, 600, "hash-ch31")
	extraID := cfSeedRow(t, s, book, extra, 110, 650, "hash-extra")
	cfSetStaleDuration(t, s, book, 99999)

	// No hash twin, not confirmed: preview and apply both refuse.
	rep := cfRunDedupe(t, s, DedupeBookFileRowsParams{Apply: true, RemoveRowIDs: []string{extraID}, ReportPath: report})
	if !cfRowIDs(t, s, book)[extraID] {
		t.Fatal("row removed without a hash twin or confirmed_duplicate")
	}
	if !strings.Contains(rep.loggedText(), "no other present row has an identical file hash") {
		t.Fatalf("missing skip reason:\n%s", rep.loggedText())
	}

	// Confirmed, preview: listed, nothing written.
	rep = cfRunDedupe(t, s, DedupeBookFileRowsParams{RemoveRowIDs: []string{extraID}, ConfirmedDuplicate: true, ReportPath: report})
	if !cfRowIDs(t, s, book)[extraID] || cfBookDuration(t, s, book) != 99999 {
		t.Fatal("preview wrote")
	}
	if !strings.Contains(rep.loggedText(), "remove_row would_delete") {
		t.Fatalf("preview did not list the row:\n%s", rep.loggedText())
	}

	// Confirmed, apply: row gone, file kept, duration recomputed.
	rep = cfRunDedupe(t, s, DedupeBookFileRowsParams{Apply: true, RemoveRowIDs: []string{extraID}, ConfirmedDuplicate: true, ReportPath: report})
	if !strings.Contains(rep.loggedText(), "1 deleted, recomputed 1 books") {
		t.Fatalf("summary does not report the deletion and recompute:\n%s", rep.loggedText())
	}
	if cfRowIDs(t, s, book)[extraID] {
		t.Fatal("confirmed row survived the apply")
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatalf("the file on disk was touched: %v", err)
	}
	if got := cfBookDuration(t, s, book); got != 600 {
		t.Fatalf("book duration = %d, want 600 recomputed", got)
	}
}

// An identical-hash present twin is enough without confirmation; a book is
// never emptied.
func TestRemoveRowIDs_HashTwinAndNeverEmptiesABook(t *testing.T) {
	s := newDupeReportStore(t)
	dir := t.TempDir()
	report := filepath.Join(t.TempDir(), "r.tsv")
	a := filepath.Join(dir, "a.mp3")
	b := filepath.Join(dir, "b.mp3")
	cfWrite(t, a, 10)
	cfWrite(t, b, 10)
	book := cfSeedBook(t, s, "Hash Twins", dir)
	aID := cfSeedRow(t, s, book, a, 10, 60, "same")
	bID := cfSeedRow(t, s, book, b, 10, 60, "same")

	// Both named: each one's only twin is itself named, so neither qualifies.
	cfRunDedupe(t, s, DedupeBookFileRowsParams{Apply: true, RemoveRowIDs: []string{aID, bID}, ReportPath: report})
	if ids := cfRowIDs(t, s, book); !ids[aID] || !ids[bID] {
		t.Fatalf("named rows justified each other: %v", ids)
	}

	cfRunDedupe(t, s, DedupeBookFileRowsParams{Apply: true, RemoveRowIDs: []string{bID}, ReportPath: report})
	if ids := cfRowIDs(t, s, book); !ids[aID] || ids[bID] {
		t.Fatalf("rows after hash-twin removal = %v, want only %s", ids, aID)
	}

	// The last row, even confirmed, is never removed.
	cfRunDedupe(t, s, DedupeBookFileRowsParams{Apply: true, RemoveRowIDs: []string{aID}, ConfirmedDuplicate: true, ReportPath: report})
	if !cfRowIDs(t, s, book)[aID] {
		t.Fatal("the book's last row was removed")
	}
}

// A scoped book is visited even when no candidate filter would pick it, so a
// missing row with no same-name row anywhere still gets its skip reason; and a
// scoped preview lists the exact-duplicate rows the same apply would delete,
// not only the cross-folder ones.
func TestCrossFolder_ScopedPreviewListsEveryDecision(t *testing.T) {
	s := newDupeReportStore(t)
	dir := t.TempDir()
	report := filepath.Join(t.TempDir(), "r.tsv")
	present := filepath.Join(dir, "new", "Ch 01.mp3")
	cfWrite(t, present, 10)
	book := cfSeedBook(t, s, "Scoped", filepath.Join(dir, "new"))
	cfSeedRow(t, s, book, present, 10, 60, "")
	cfSeedRow(t, s, book, present, 10, 60, "") // exact duplicate path
	lonely := cfSeedRow(t, s, book, filepath.Join(dir, "old", "Renamed.mp3"), 10, 60, "")

	off := false
	rep := cfRunDedupe(t, s, DedupeBookFileRowsParams{
		CrossFolder: true, PruneSuperseded: &off, BookIDs: []string{book}, ReportPath: report,
	})
	logs := rep.loggedText()
	if !strings.Contains(logs, "cross_folder skip: book="+book+" row="+lonely) ||
		!strings.Contains(logs, "no present row in the book has the same basename") {
		t.Fatalf("no zero-twin skip for the scoped book's missing row:\n%s", logs)
	}
	if !strings.Contains(logs, "exact_duplicate would_delete") {
		t.Fatalf("the preview does not list the exact-duplicate row it would delete:\n%s", logs)
	}
	if n := len(cfRowIDs(t, s, book)); n != 3 {
		t.Fatalf("preview changed the row count to %d", n)
	}
}

// Identical hash but a different recorded size is not a hash twin.
func TestRemoveRowIDs_HashTwinNeedsSameSize(t *testing.T) {
	s := newDupeReportStore(t)
	dir := t.TempDir()
	report := filepath.Join(t.TempDir(), "r.tsv")
	a := filepath.Join(dir, "a.mp3")
	b := filepath.Join(dir, "b.mp3")
	cfWrite(t, a, 10)
	cfWrite(t, b, 11)
	book := cfSeedBook(t, s, "Size Differs", dir)
	cfSeedRow(t, s, book, a, 10, 60, "same")
	bID := cfSeedRow(t, s, book, b, 11, 60, "same")
	cfRunDedupe(t, s, DedupeBookFileRowsParams{Apply: true, RemoveRowIDs: []string{bID}, ReportPath: report})
	if !cfRowIDs(t, s, book)[bID] {
		t.Fatal("a row was removed on a hash match with a different size")
	}
}
