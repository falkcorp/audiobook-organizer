// file: internal/plugins/maintenance/fragment_consolidation_fixer_test.go
// version: 1.12.0
// guid: 8e2d5b19-6a4c-4f37-b1d8-2c9e7a3f5d60
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// fragFixture seeds a real PebbleStore and real files under a temp root.
type fragFixture struct {
	s    *database.PebbleStore
	root string
	ops  *planOps
	p    *Plugin
	// ids names the seeded books by role.
	ids map[string]string
	// rowIDs names the seeded book_file rows by role.
	rowIDs map[string]string
}

// newFragStore seeds a real PebbleStore. Unlike newSeriesPhantomStore it
// does NOT skip under -short: CI runs the short suite, and these are the only
// tests of the fixer's writes.
func newFragStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return s
}

func newFragFixture(t *testing.T) *fragFixture {
	t.Helper()
	// The root is resolved (macOS temp dirs sit behind the /var symlink), so
	// the guard's symlink resolution compares like with like.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := &fragFixture{s: newFragStore(t), root: root, ids: map[string]string{}, rowIDs: map[string]string{}}
	f.ops = &planOps{rows: map[string]*database.OperationV2Row{}}
	f.p = &Plugin{deps: scanDeps{fakeDeps: fakeDeps{store: f.s}, scan: &scriptedScan{renewsLeft: -1}, ops: f.ops}, standDownWait: noWait}
	withRoot(t, f.root)
	return f
}

func (f *fragFixture) path(rel string) string { return filepath.Join(f.root, rel) }

func (f *fragFixture) file(t *testing.T, rel string, size int) string {
	t.Helper()
	p := f.path(rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, make([]byte, size), 0o644))
	return p
}

// book creates a book whose import path-change row names importPath.
func (f *fragFixture) book(t *testing.T, role, title, importPath string, seriesID *int) string {
	t.Helper()
	b, err := f.s.CreateBook(&database.Book{Title: title, FilePath: importPath, SeriesID: seriesID})
	require.NoError(t, err)
	f.ids[role] = b.ID
	return b.ID
}

func (f *fragFixture) row(t *testing.T, role, bookID, path, orig string, size int64, dur, track int) {
	t.Helper()
	bf := &database.BookFile{BookID: bookID, FilePath: path, OriginalFilename: orig, FileSize: size, Duration: dur, TrackNumber: track}
	require.NoError(t, f.s.CreateBookFile(bf))
	f.rowIDs[role] = bf.ID
}

// organize simulates the in-place organize that stranded the parent: the
// fragment's file moves on disk and the book and its row follow.
func (f *fragFixture) organize(t *testing.T, bookID, from, to string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(to), 0o755))
	require.NoError(t, os.Rename(from, to))
	_, err := f.s.ModifyBook(bookID, func(b *database.Book) error { b.FilePath = to; return nil })
	require.NoError(t, err)
	rows, err := f.s.GetBookFiles(bookID)
	require.NoError(t, err)
	for i := range rows {
		rows[i].FilePath = to
		require.NoError(t, f.s.UpdateBookFile(rows[i].ID, &rows[i]))
	}
}

// seed builds one fixture per class:
//
//	moved       Eldest: parent rows 01-03, 03 organized away under fragment F
//	copy        Suns: parent rows 01-02 present, fragment G imported FROM 02
//	unproven    Suns: fragment H named 01.mp3 at the same size, no link
//	no-parent   Loose: three short "Loose 0N" books, no parent
//	gate        Long: three hour-long "Long 0N" books
//	itunes      books/itunes Eldest twin: moved shape under the frozen tree
//	doctor who  "Doctor Who - Loose": no-parent shape, manual only
func (f *fragFixture) seed(t *testing.T) {
	t.Helper()
	// moved
	e1 := f.file(t, "lib/Eldest/01.mp3", 101)
	e2 := f.file(t, "lib/Eldest/02.mp3", 102)
	e3 := f.file(t, "lib/Eldest/03.mp3", 103)
	parent := f.book(t, "parent", "Eldest", f.path("lib/Eldest"), nil)
	f.row(t, "p01", parent, e1, "01.mp3", 101, 600, 1)
	f.row(t, "p02", parent, e2, "02.mp3", 102, 600, 2)
	f.row(t, "p03", parent, e3, "03.mp3", 103, 600, 3)
	frag := f.book(t, "fragF", "03", e3, nil)
	f.row(t, "f03", frag, e3, "03.mp3", 103, 600, 0)
	f.organize(t, frag, e3, f.path("lib/Eldest/03/03/03.mp3"))

	// copy (proven) + unproven
	s1 := f.file(t, "lib/Suns/01.mp3", 201)
	s2 := f.file(t, "lib/Suns/02.mp3", 202)
	suns := f.book(t, "suns", "Scattered Suns", f.path("lib/Suns"), nil)
	f.row(t, "s01", suns, s1, "01.mp3", 201, 600, 1)
	f.row(t, "s02", suns, s2, "02.mp3", 202, 600, 2)
	g := f.book(t, "fragG", "02", s2, nil)
	gCopy := f.file(t, "lib/Suns2/02.mp3", 202)
	f.row(t, "g02", g, s2, "02.mp3", 202, 600, 0)
	_, err := f.s.ModifyBook(g, func(b *database.Book) error { b.FilePath = gCopy; return nil })
	require.NoError(t, err)
	rows, err := f.s.GetBookFiles(g)
	require.NoError(t, err)
	rows[0].FilePath = gCopy
	require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
	hPath := f.file(t, "lib/Elsewhere/01.mp3", 201)
	h := f.book(t, "fragH", "01", hPath, nil)
	// A different duration: name and size alone, unproven.
	f.row(t, "h01", h, hPath, "01.mp3", 201, 590, 0)

	// no-parent
	for i, n := range []string{"03", "01", "02"} {
		p := f.file(t, "lib/Loose/Loose "+n+".mp3", 300+i)
		f.book(t, "loose"+n, "Loose "+n, p, nil)
		f.row(t, "l"+n, f.ids["loose"+n], p, "Loose "+n+".mp3", int64(300+i), 300, 0)
		f.organized(t, f.ids["loose"+n])
	}
	// duration gate
	for i, n := range []string{"01", "02", "03"} {
		p := f.file(t, "lib/Long/Long "+n+".mp3", 400+i)
		f.book(t, "long"+n, "Long "+n, p, nil)
		f.row(t, "lg"+n, f.ids["long"+n], p, "Long "+n+".mp3", int64(400+i), 3600, 0)
	}
	// iTunes: the moved shape under the frozen tree.
	i1 := f.file(t, "books/itunes/Eldest/01.mp3", 501)
	i2 := f.file(t, "books/itunes/Eldest/02.mp3", 502)
	it := f.book(t, "itunesParent", "Eldest (iTunes)", f.path("books/itunes/Eldest"), nil)
	f.row(t, "i01", it, i1, "01.mp3", 501, 600, 1)
	f.row(t, "i02", it, i2, "02.mp3", 502, 600, 2)
	itFrag := f.book(t, "itunesFrag", "02", i2, nil)
	f.row(t, "if02", itFrag, i2, "02.mp3", 502, 600, 0)
	f.organize(t, itFrag, i2, f.path("lib/Eldest iTunes/02/02.mp3"))
	// held: the parent's 02 is gone, and so is the fragment's own file.
	hd1 := f.file(t, "lib/Held/01.mp3", 701)
	hd2 := f.path("lib/Held/02.mp3")
	heldParent := f.book(t, "heldParent", "Held", f.path("lib/Held"), nil)
	f.row(t, "hp01", heldParent, hd1, "01.mp3", 701, 600, 1)
	f.row(t, "hp02", heldParent, hd2, "02.mp3", 702, 600, 2)
	hf := f.book(t, "heldFrag", "02", hd2, nil)
	gone := f.path("lib/Held/02/02.mp3")
	f.row(t, "hf02", hf, gone, "02.mp3", 702, 600, 0)
	_, err = f.s.ModifyBook(hf, func(b *database.Book) error { b.FilePath = gone; return nil })
	require.NoError(t, err)
	// Doctor Who: the no-parent shape.
	for i, n := range []string{"01", "02", "03"} {
		p := f.file(t, "lib/Doctor Who - Loose/Part "+n+".mp3", 600+i)
		f.book(t, "dw"+n, "Part "+n, p, nil)
		f.row(t, "dw"+n, f.ids["dw"+n], p, "Part "+n+".mp3", int64(600+i), 300, 0)
	}
}

// organized marks a book organized (the survivor election reads it).
func (f *fragFixture) organized(t *testing.T, bookID string) {
	t.Helper()
	st := "organized"
	_, err := f.s.ModifyBook(bookID, func(b *database.Book) error { b.LibraryState = &st; return nil })
	require.NoError(t, err)
}

func (f *fragFixture) plan(t *testing.T, opID string) *repairs.PlanResult {
	t.Helper()
	params, err := json.Marshal(repairs.PlanParams{FixerID: fragFixerID})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsPlan(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.PlanResult)
	require.True(t, ok)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	f.ops.mu.Lock()
	f.ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}
	f.ops.mu.Unlock()
	return res
}

func (f *fragFixture) apply(t *testing.T, planOpID, opID string, rowIDs []string, resume *repairs.ApplyCheckpoint) *repairs.ApplyResult {
	t.Helper()
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fragFixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &no, Resume: resume})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsApply(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	return res
}

func findRow(t *testing.T, res *repairs.PlanResult, id string) repairs.Row {
	t.Helper()
	for _, r := range res.Rows {
		if r.RowID == id {
			return r
		}
	}
	t.Fatalf("row %s not in plan", id)
	return repairs.Row{}
}

func (f *fragFixture) live(t *testing.T, role string) bool {
	t.Helper()
	b, err := f.s.GetBookByID(f.ids[role])
	require.NoError(t, err)
	require.NotNil(t, b)
	return !b.IsSoftDeleted()
}

func (f *fragFixture) fileRow(t *testing.T, bookRole, rowRole string) *database.BookFile {
	t.Helper()
	r, err := f.s.GetBookFileByID(f.ids[bookRole], f.rowIDs[rowRole])
	require.NoError(t, err)
	return r
}

// TestFragmentFixer_PlanClassifiesEveryShape is the per-class table.
func TestFragmentFixer_PlanClassifiesEveryShape(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")

	cases := []struct {
		name, rowID, class, skipped string
		books                       []string
		evidence                    string
	}{
		{"moved", "moved:" + f.ids["parent"], fragClassMoved, "", []string{"parent", "fragF"}, fragEvImportPath},
		{"copy proven", "copy:" + f.ids["suns"], fragClassCopy, "", []string{"suns", "fragG"}, fragEvImportPath},
		{"copy unproven", fragRowCopyUnproven + ":" + f.ids["suns"], fragClassCopy, fragSkipCopyUnproven, []string{"suns", "fragH"}, fragEvNameSize},
		{"no-parent", noParentRowID(f.path("lib/Loose"), "loose"), fragClassNoParent, "", []string{"loose01", "loose02", "loose03"}, ""},
		{"duration gate", noParentRowID(f.path("lib/Long"), "long"), fragClassNoParent, fragSkipDurationGate, []string{"long01", "long02", "long03"}, ""},
		{"itunes", "moved:" + f.ids["itunesParent"], fragClassManual, repairs.SkipITunes, []string{"itunesParent", "itunesFrag"}, ""},
		{"ghost: fragment file missing, one proven parent", "ghost:" + f.ids["heldParent"], fragClassGhost, "", []string{"heldFrag", "heldParent"}, fragEvImportPath},
		{"doctor who", noParentRowID(f.path("lib/Doctor Who - Loose"), ""), fragClassManual, repairs.SkipOwnerManual, []string{"dw01", "dw02", "dw03"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := findRow(t, res, tc.rowID)
			require.Equal(t, tc.class, r.Class)
			require.Equal(t, tc.skipped, r.Skipped, r.SkipReason)
			var want []string
			for _, role := range tc.books {
				want = append(want, f.ids[role])
			}
			require.ElementsMatch(t, want, r.BookIDs)
			require.Len(t, r.Members, len(want), "every book of the row is listed for the UI")
			if tc.evidence != "" {
				require.Contains(t, r.Evidence[0], tc.evidence)
			}
		})
	}
	require.Equal(t, 2, res.ByClass[fragClassManual])
	require.Equal(t, 1, res.ByClass[fragClassMoved])
	require.Equal(t, 2, res.ByClass[fragClassCopy], "proven and unproven copies are separate rows")
}

// TestFragmentFixer_ApplyThenUndoRoundTrip applies every applicable row and
// reverts the apply operation, checking every step comes back.
func TestFragmentFixer_ApplyThenUndoRoundTrip(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	var ids []string
	for _, r := range res.Rows {
		if r.Applicable() {
			ids = append(ids, r.RowID)
		}
	}
	require.Len(t, ids, 4, "moved, proven copy, no-parent and ghost")
	before03 := *f.fileRow(t, "parent", "p03")
	beforeHp02 := *f.fileRow(t, "heldParent", "hp02")

	out := f.apply(t, "op-plan", "op-apply", ids, nil)
	require.Equal(t, 4, out.Applied, "%+v", out.Rows)

	// ghost: the fragment is retired into the parent; neither book's missing
	// row is touched (nothing is repointed, nothing is deleted).
	require.False(t, f.live(t, "heldFrag"))
	hp02 := f.fileRow(t, "heldParent", "hp02")
	require.Equal(t, beforeHp02.FilePath, hp02.FilePath)
	require.Equal(t, beforeHp02.Missing, hp02.Missing)
	require.NotNil(t, f.fileRow(t, "heldFrag", "hf02"), "the ghost keeps its own (missing) row")

	// moved: the parent's row names the file's real path; F is retired but
	// keeps its row.
	p03 := f.fileRow(t, "parent", "p03")
	require.Equal(t, f.path("lib/Eldest/03/03/03.mp3"), p03.FilePath)
	require.False(t, p03.Missing)
	require.False(t, f.live(t, "fragF"))
	require.NotNil(t, f.fileRow(t, "fragF", "f03"), "no book_file row is ever deleted")
	// copy: G retired, H (unproven) and the parent untouched.
	require.False(t, f.live(t, "fragG"))
	require.True(t, f.live(t, "fragH"))
	// no-parent: the lowest id survives with every row, in chapter order.
	loose := []string{f.ids["loose01"], f.ids["loose02"], f.ids["loose03"]}
	survivor := loose[0]
	for _, id := range loose {
		if id < survivor {
			survivor = id
		}
	}
	rows, err := f.s.GetBookFiles(survivor)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	track := map[string]int{}
	for _, r := range rows {
		track[filepath.Base(r.FilePath)] = r.TrackNumber
	}
	require.Equal(t, map[string]int{"Loose 01.mp3": 1, "Loose 02.mp3": 2, "Loose 03.mp3": 3}, track)
	for _, n := range []string{"01", "02", "03"} {
		if f.ids["loose"+n] != survivor {
			require.False(t, f.live(t, "loose"+n))
		}
	}
	sb, err := f.s.GetBookByID(survivor)
	require.NoError(t, err)
	require.Equal(t, "Loose", sb.Title, "titled from the folder")
	require.Equal(t, f.path("lib/Loose"), sb.FilePath, "a multi-file book's path is its folder")
	survivorPath := ""
	for _, n := range []string{"01", "02", "03"} {
		if f.ids["loose"+n] == survivor {
			survivorPath = f.path("lib/Loose/Loose " + n + ".mp3")
		}
	}
	// hands-off rows untouched.
	require.True(t, f.live(t, "itunesFrag"))
	require.True(t, f.live(t, "dw01"))

	// Every step is restorable and the preflight sees no conflict.
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	require.NotEmpty(t, changes)
	for i := range changes {
		require.True(t, undo.IsRestorable(changes[i]), "%s %s", changes[i].ChangeType, changes[i].FieldName)
	}
	report, err := undo.PreflightUndoConflicts(f.s, "op-apply")
	require.NoError(t, err)
	require.Empty(t, report.CheckFailed, "%+v", report.CheckFailed)
	require.Empty(t, report.BookMissing)
	require.Zero(t, report.NotRestorable, "%v", report.NotRestorableTypes)

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)

	after03 := f.fileRow(t, "parent", "p03")
	require.Equal(t, before03.FilePath, after03.FilePath)
	require.Equal(t, before03.Missing, after03.Missing)
	for _, role := range []string{"fragF", "fragG", "loose01", "loose02", "loose03", "heldFrag"} {
		require.True(t, f.live(t, role), "%s restored", role)
	}
	for _, n := range []string{"01", "02", "03"} {
		r, err := f.s.GetBookFileByID(f.ids["loose"+n], f.rowIDs["l"+n])
		require.NoError(t, err)
		require.NotNil(t, r, "row l%s is back on its own book", n)
		require.Zero(t, r.TrackNumber)
	}
	sb, err = f.s.GetBookByID(survivor)
	require.NoError(t, err)
	require.NotEqual(t, "Loose", sb.Title, "title restored")
	require.Equal(t, survivorPath, sb.FilePath, "book path restored")
}

// TestFragmentFixer_ResumesAPartiallyAppliedRow: a run cut off after the
// repoint (fragment not yet retired) re-plans to the same fingerprint, and
// the next apply finishes the row instead of reporting it changed.
func TestFragmentFixer_ResumesAPartiallyAppliedRow(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	movedID := "moved:" + f.ids["parent"]

	// The first half of the row, as an interrupted run left it.
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-cut")
	was := *f.fileRow(t, "parent", "p03")
	frag := f.fileRow(t, "fragF", "f03")
	require.NoError(t, w.RepointBookFile(f.ids["parent"], was.ID, undo.LocationOf(&was),
		undo.BookFileLocation{Path: frag.FilePath, Hash: frag.FileHash, Size: frag.FileSize}))

	out := f.apply(t, "op-plan", "op-apply", []string{movedID}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fragF"))
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	for _, c := range changes {
		require.NotEqual(t, undo.ChangeTypeBookFileRepoint, c.ChangeType, "the finished repoint is not written twice")
	}
}

// TestFragmentFixer_ResumesFromCheckpoint: rows a checkpoint says were
// settled are reported as settled and not applied again.
// TestFragmentFixer_ResumesAPartiallyAppliedGroup: a no-parent run cut off
// after one row moved and the survivor was retitled finishes on the next
// apply.
func TestFragmentFixer_ResumesAPartiallyAppliedGroup(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	id := noParentRowID(f.path("lib/Loose"), "loose")
	row := findRow(t, res, id)
	survivor := row.Proposed["survivor"]
	var other, otherRow string
	for _, n := range []string{"01", "02", "03"} {
		if f.ids["loose"+n] != survivor {
			other, otherRow = f.ids["loose"+n], f.rowIDs["l"+n]
			break
		}
	}
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-cut")
	require.NoError(t, w.MoveBookFiles([]string{otherRow}, other, survivor))
	// The cut-off run also retitled the survivor and pointed its path at the
	// folder: the survivor's own row is found by its stored id, not its path.
	_, err := f.s.ModifyBook(survivor, func(b *database.Book) error { b.Title = "Loose"; b.FilePath = f.path("lib/Loose"); return nil })
	require.NoError(t, err)

	out := f.apply(t, "op-plan", "op-apply", []string{id}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	rows, err := f.s.GetBookFiles(survivor)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.False(t, f.live(t, "loose01") && f.live(t, "loose02") && f.live(t, "loose03"))
}

// TestFragmentFixer_RefusesAFileAnotherBookNowOwns: the strict ownership
// re-check at apply time refuses a row whose file some other book claims.
func TestFragmentFixer_RefusesAFileAnotherBookNowOwns(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	moved := f.path("lib/Eldest/03/03/03.mp3")
	intruder := f.book(t, "intruder", "Someone else", moved, nil)
	f.row(t, "x", intruder, moved, "03.mp3", 103, 600, 0)

	out := f.apply(t, "op-plan", "op-apply", []string{"moved:" + f.ids["parent"]}, nil)
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	require.True(t, f.live(t, "fragF"), "nothing written")
}

// TestFragmentFixer_RetiredOwnerDoesNotBlock: a soft-deleted book that still
// names the fragment's path (retired books keep their rows) is history, not
// a live owner; the row applies.
func TestFragmentFixer_RetiredOwnerDoesNotBlock(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	moved := f.path("lib/Eldest/03/03/03.mp3")
	retired := f.book(t, "retired", "Someone else, retired", moved, nil)
	f.row(t, "x", retired, moved, "03.mp3", 103, 600, 0)
	_, err := f.s.ModifyBook(retired, func(b *database.Book) error { yes := true; b.MarkedForDeletion = &yes; return nil })
	require.NoError(t, err)

	out := f.apply(t, "op-plan", "op-apply", []string{"moved:" + f.ids["parent"]}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fragF"))
}

func TestFragmentFixer_ResumesFromCheckpoint(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	copyID := "copy:" + f.ids["suns"]
	movedID := "moved:" + f.ids["parent"]
	cp := &repairs.ApplyCheckpoint{Settled: []repairs.RowResult{{RowID: copyID, Outcome: repairs.OutcomeApplied}}}

	out := f.apply(t, "op-plan", "op-apply", []string{copyID, movedID}, cp)
	require.Equal(t, 2, out.Applied)
	require.True(t, f.live(t, "fragG"), "the checkpointed row was not applied again")
	require.False(t, f.live(t, "fragF"), "the open row was applied")
}

// looseGroup seeds n short "<name> 0N" fragment books in dir; organized
// says which of them (1-based) are organized. It returns the book ids.
func (f *fragFixture) looseGroup(t *testing.T, dir, name string, n int, organized func(i int) bool) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= n; i++ {
		stem := name + " 0" + string(rune('0'+i))
		p := f.file(t, filepath.Join(dir, stem+".mp3"), 800+i)
		id := f.book(t, dir+stem, stem, p, nil)
		f.row(t, dir+stem, id, p, stem+".mp3", int64(800+i), 300, 0)
		if organized(i) {
			f.organized(t, id)
		}
		ids = append(ids, id)
	}
	return ids
}

func rowWithBooks(t *testing.T, res *repairs.PlanResult, ids []string) repairs.Row {
	t.Helper()
	for _, r := range res.Rows {
		if len(r.BookIDs) != len(ids) {
			continue
		}
		match := true
		for _, id := range ids {
			if !contains(r.BookIDs, id) {
				match = false
			}
		}
		if match {
			return r
		}
	}
	t.Fatalf("no row with books %v", ids)
	return repairs.Row{}
}

// TestFragmentFixer_MovedNameSizeOnlyIsUnproven (H2): a moved match resting
// on the original name and size alone is a skipped moved-unproven row; the
// same match with the fragment imported from the parent row's folder is a
// proven moved row.
func TestFragmentFixer_MovedNameSizeOnlyIsUnproven(t *testing.T) {
	seed := func(t *testing.T, f *fragFixture, importRel string) {
		x1 := f.file(t, "lib/X/01.mp3", 101)
		parent := f.book(t, "parent", "X", f.path("lib/X"), nil)
		f.row(t, "p01", parent, x1, "01.mp3", 101, 600, 1)
		f.row(t, "p02", parent, f.path("lib/X/02.mp3"), "02.mp3", 102, 600, 2) // gone from disk
		imp := f.file(t, importRel, 102)
		frag := f.book(t, "frag", "02", imp, nil)
		// The same duration as the parent row: on a constant-bitrate file the
		// size already fixes it, so it proves nothing more.
		f.row(t, "f02", frag, imp, "02.mp3", 102, 600, 0)
		f.organize(t, frag, imp, f.path("lib/Y/02/02.mp3"))
	}
	t.Run("name, size and duration only", func(t *testing.T) {
		f := newFragFixture(t)
		seed(t, f, "lib/Elsewhere/02.mp3")
		res := f.plan(t, "op-plan")
		r := findRow(t, res, fragRowMovedUnproven+":"+f.ids["parent"])
		require.Equal(t, fragClassMoved, r.Class)
		require.Equal(t, fragSkipMovedUnproven, r.Skipped)
		require.False(t, r.Applicable())
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied)
		p02 := f.fileRow(t, "parent", "p02")
		require.Equal(t, f.path("lib/X/02.mp3"), p02.FilePath, "not repointed")
	})
	t.Run("imported from the parent row's folder", func(t *testing.T) {
		f := newFragFixture(t)
		seed(t, f, "lib/X/02 (copy).mp3")
		res := f.plan(t, "op-plan")
		r := findRow(t, res, "moved:"+f.ids["parent"])
		require.True(t, r.Applicable(), r.SkipReason)
		require.Contains(t, r.Evidence[0], fragEvNameSizeFolder)
	})
}

// TestFragmentFixer_GhostNeedsOneProvenParent: a fragment whose own file is
// gone is a ghost only when exactly one parent row claims it by proof. Name
// and size alone, or two candidate parents, keep it held for a person.
func TestFragmentFixer_GhostNeedsOneProvenParent(t *testing.T) {
	t.Run("name and size only", func(t *testing.T) {
		f := newFragFixture(t)
		p1 := f.file(t, "lib/P/01.mp3", 801)
		parent := f.book(t, "parent", "P", f.path("lib/P"), nil)
		f.row(t, "p01", parent, p1, "01.mp3", 801, 600, 1)
		f.row(t, "p02", parent, f.path("lib/P/02.mp3"), "02.mp3", 802, 600, 2)
		gone := f.path("lib/Q/02/02.mp3")
		frag := f.book(t, "frag", "02", gone, nil)
		f.row(t, "f02", frag, gone, "02.mp3", 802, 600, 0)
		r := findRow(t, f.plan(t, "op-plan"), "held:"+frag)
		require.Equal(t, fragClassHeld, r.Class)
		require.Equal(t, fragSkipFilesMissing, r.Skipped)
		require.Contains(t, r.Evidence[0], fragEvNameSize)
	})
	t.Run("two candidate parents", func(t *testing.T) {
		f := newFragFixture(t)
		gone := f.path("lib/R/02.mp3")
		for _, n := range []string{"A", "B"} {
			p := f.book(t, "parent"+n, "R "+n, f.path("lib/R"+n), nil)
			f.row(t, "p"+n+"01", p, f.file(t, "lib/R"+n+"/01.mp3", 901), "01.mp3", 901, 600, 1)
			f.row(t, "p"+n+"02", p, gone, "02.mp3", 902, 600, 2)
		}
		frag := f.book(t, "frag", "02", gone, nil)
		f.row(t, "f02", frag, f.path("lib/R/02/02.mp3"), "02.mp3", 902, 600, 0)
		r := findRow(t, f.plan(t, "op-plan"), "held:"+frag)
		require.Equal(t, fragClassHeld, r.Class)
		require.Equal(t, fragSkipFilesMissing, r.Skipped)
		require.Len(t, r.Members, 3, "both candidate parents listed")
	})
}

// TestFragmentFixer_SameParentRowClaims: one parent row claimed by several
// fragments is not ambiguous for the claimants that prove their claim.
func TestFragmentFixer_SameParentRowClaims(t *testing.T) {
	seed := func(t *testing.T, f *fragFixture) (parent string) {
		t1 := f.file(t, "lib/T/01.mp3", 1001)
		t2 := f.file(t, "lib/T/02.mp3", 1002)
		parent = f.book(t, "parent", "Twice", f.path("lib/T"), nil)
		f.row(t, "t01", parent, t1, "01.mp3", 1001, 600, 1)
		f.row(t, "t02", parent, t2, "02.mp3", 1002, 600, 2)
		// J: imported FROM the parent's 02 (proven), its copy present elsewhere.
		j := f.book(t, "fragJ", "02", t2, nil)
		jCopy := f.file(t, "lib/T copy/02.mp3", 1002)
		f.row(t, "j02", j, t2, "02.mp3", 1002, 600, 0)
		_, err := f.s.ModifyBook(j, func(b *database.Book) error { b.FilePath = jCopy; return nil })
		require.NoError(t, err)
		rows, err := f.s.GetBookFiles(j)
		require.NoError(t, err)
		rows[0].FilePath = jCopy
		require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
		return parent
	}
	t.Run("proven and unproven claimant", func(t *testing.T) {
		f := newFragFixture(t)
		parent := seed(t, f)
		// K: named 02.mp3 at the parent row's size, no link (unproven).
		kPath := f.file(t, "lib/Elsewhere/02.mp3", 1002)
		k := f.book(t, "fragK", "02", kPath, nil)
		f.row(t, "k02", k, kPath, "02.mp3", 1002, 590, 0)
		res := f.plan(t, "op-plan")
		proven := findRow(t, res, "copy:"+parent)
		require.True(t, proven.Applicable(), proven.SkipReason)
		require.ElementsMatch(t, []string{parent, f.ids["fragJ"]}, proven.BookIDs)
		unproven := findRow(t, res, fragRowCopyUnproven+":"+parent)
		require.Equal(t, fragSkipCopyUnproven, unproven.Skipped)
		require.ElementsMatch(t, []string{parent, k}, unproven.BookIDs)
		for _, r := range res.Rows {
			require.NotEqual(t, fragClassAmbiguous, r.Class, "%s: %s", r.RowID, r.SkipReason)
		}
		out := f.apply(t, "op-plan", "op-apply", []string{proven.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		require.False(t, f.live(t, "fragJ"))
		require.True(t, f.live(t, "fragK"))
	})
	t.Run("two proven claimants retire together", func(t *testing.T) {
		f := newFragFixture(t)
		parent := seed(t, f)
		// L: a second copy imported from the same parent row, file gone (ghost).
		l := f.book(t, "fragL", "02", f.path("lib/T/02.mp3"), nil)
		gone := f.path("lib/L/02/02.mp3")
		f.row(t, "l02", l, gone, "02.mp3", 1002, 600, 0)
		_, err := f.s.ModifyBook(l, func(b *database.Book) error { b.FilePath = gone; return nil })
		require.NoError(t, err)
		res := f.plan(t, "op-plan")
		copyRow := findRow(t, res, "copy:"+parent)
		require.ElementsMatch(t, []string{parent, f.ids["fragJ"]}, copyRow.BookIDs)
		ghost := findRow(t, res, "ghost:"+parent)
		require.True(t, ghost.Applicable(), ghost.SkipReason)
		require.ElementsMatch(t, []string{parent, l}, ghost.BookIDs)
		for _, r := range res.Rows {
			require.NotEqual(t, fragClassAmbiguous, r.Class, "%s: %s", r.RowID, r.SkipReason)
		}
	})
	t.Run("two present proven claimants on a gone parent row: one is repointed, the other waits", func(t *testing.T) {
		f := newFragFixture(t)
		g1 := f.file(t, "lib/G/01.mp3", 1101)
		gone := f.path("lib/G/02.mp3")
		parent := f.book(t, "parent", "Gone", f.path("lib/G"), nil)
		f.row(t, "g01", parent, g1, "01.mp3", 1101, 600, 1)
		f.row(t, "g02", parent, gone, "02.mp3", 1102, 600, 2)
		var frags []string
		for _, n := range []string{"A", "B"} {
			fr := f.book(t, "frag"+n, "02", gone, nil)
			f.row(t, n+"02", fr, gone, "02.mp3", 1102, 600, 0)
			f.organize(t, fr, f.file(t, "lib/tmp"+n+"/02.mp3", 1102), f.path("lib/G"+n+"/02/02.mp3"))
			frags = append(frags, fr)
		}
		res := f.plan(t, "op-plan")
		moved := findRow(t, res, "moved:"+parent)
		require.True(t, moved.Applicable(), moved.SkipReason)
		require.Len(t, moved.BookIDs, 2, "one fragment per gone row")
		other := frags[1]
		if moved.BookIDs[0] == other || moved.BookIDs[1] == other {
			other = frags[0]
		}
		waiting := findRow(t, res, "ambiguous:"+other)
		require.Equal(t, fragSkipAmbiguous, waiting.Skipped)
		require.Contains(t, waiting.SkipReason, "plan again")
		out := f.apply(t, "op-plan", "op-apply", []string{moved.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		// Next plan: the parent has the file again (at the first fragment's
		// path), so the other is a copy; its import path names the row's OLD
		// path, so by today's rules it is an unproven one.
		res2 := f.plan(t, "op-plan-2")
		copyRow := findRow(t, res2, fragRowCopyUnproven+":"+parent)
		require.Contains(t, copyRow.BookIDs, other)
	})
	t.Run("a parent retired since the plan is refused at apply", func(t *testing.T) {
		f := newFragFixture(t)
		parent := seed(t, f)
		res := f.plan(t, "op-plan")
		proven := findRow(t, res, "copy:"+parent)
		_, err := f.s.ModifyBook(parent, func(b *database.Book) error { yes := true; b.MarkedForDeletion = &yes; return nil })
		require.NoError(t, err)
		out := f.apply(t, "op-plan", "op-apply", []string{proven.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "fragJ"), "never folded into a dead book")
	})
	t.Run("two unproven claimants stay ambiguous", func(t *testing.T) {
		f := newFragFixture(t)
		parent := seed(t, f)
		var ks []string
		for _, n := range []string{"K", "M"} {
			p := f.file(t, "lib/Elsewhere "+n+"/02.mp3", 1002)
			k := f.book(t, "frag"+n, "02", p, nil)
			f.row(t, n+"02", k, p, "02.mp3", 1002, 590, 0)
			ks = append(ks, k)
		}
		res := f.plan(t, "op-plan")
		for _, k := range ks {
			r := findRow(t, res, "ambiguous:"+k)
			require.Equal(t, fragSkipAmbiguous, r.Skipped)
		}
		proven := findRow(t, res, "copy:"+parent)
		require.ElementsMatch(t, []string{parent, f.ids["fragJ"]}, proven.BookIDs, "the proven claimant still pairs")
	})
}

// TestFragmentFixer_SurvivorMustBeOrganizedAndPrimary (H3).
func TestFragmentFixer_SurvivorMustBeOrganizedAndPrimary(t *testing.T) {
	f := newFragFixture(t)
	none := f.looseGroup(t, "lib/NoneOrg", "Chap", 3, func(int) bool { return false })
	last := f.looseGroup(t, "lib/LastOrg", "Chap", 3, func(i int) bool { return i == 3 })
	res := f.plan(t, "op-plan")

	r := rowWithBooks(t, res, none)
	require.Equal(t, fragSkipNoSurvivor, r.Skipped, r.SkipReason)

	r = rowWithBooks(t, res, last)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Equal(t, last[2], r.Proposed["survivor"], "the only organized member, not the lowest id")
}

// TestFragmentFixer_ChapterOrderMustBeKnown (H1): two files at one position
// skip the group rather than guess an order.
func TestFragmentFixer_ChapterOrderMustBeKnown(t *testing.T) {
	f := newFragFixture(t)
	var ids []string
	for i, stem := range []string{"Part 1", "Part 01", "Part 2"} {
		p := f.file(t, "lib/Dup/"+stem+".mp3", 900+i)
		id := f.book(t, stem, stem, p, nil)
		f.row(t, stem, id, p, stem+".mp3", int64(900+i), 300, 0)
		f.organized(t, id)
		ids = append(ids, id)
	}
	r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
	require.Equal(t, fragSkipTrackOrder, r.Skipped, r.SkipReason)
}

// TestFragmentFixer_DiscFoldersFormOneGroup (M5): CD1/CD2 siblings are one
// group, ordered disc first.
func TestFragmentFixer_DiscFoldersFormOneGroup(t *testing.T) {
	f := newFragFixture(t)
	var ids []string
	for _, rel := range []string{"CD2/01", "CD1/02", "CD2/02", "CD1/01"} {
		p := f.file(t, "lib/Saga/"+rel+".mp3", 300)
		id := f.book(t, rel, filepath.Base(rel), p, nil)
		f.row(t, rel, id, p, filepath.Base(rel)+".mp3", 300, 300, 0)
		f.organized(t, id)
		ids = append(ids, id)
	}
	res := f.plan(t, "op-plan")
	r := rowWithBooks(t, res, ids)
	require.True(t, r.Applicable(), r.SkipReason)
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	rows, err := f.s.GetBookFiles(r.Proposed["survivor"])
	require.NoError(t, err)
	track := map[string]int{}
	for _, row := range rows {
		rel, err := filepath.Rel(f.path("lib/Saga"), row.FilePath)
		require.NoError(t, err)
		track[rel] = row.TrackNumber
	}
	require.Equal(t, map[string]int{"CD1/01.mp3": 1, "CD1/02.mp3": 2, "CD2/01.mp3": 3, "CD2/02.mp3": 4}, track)
}

// TestFragmentFixer_FolderPathOnlyWhenExact (M4): a folder holding audio
// that is not the group's is never made the survivor's path.
func TestFragmentFixer_FolderPathOnlyWhenExact(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.file(t, "lib/Loose/Bonus interview.mp3", 10)
	r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Loose"), "loose"))
	require.True(t, r.Applicable(), r.SkipReason)
	require.NotContains(t, r.Proposed, "book_path")
}

// TestFragmentFixer_PathProvenCopyComparesSizeOnDisk (L1).
func TestFragmentFixer_PathProvenCopyComparesSizeOnDisk(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	require.NoError(t, os.WriteFile(f.path("lib/Suns2/02.mp3"), make([]byte, 999), 0o644))
	res := f.plan(t, "op-plan")
	for _, r := range res.Rows {
		require.NotEqual(t, "copy:"+f.ids["suns"], r.RowID, "no proven copy row")
	}
	r := findRow(t, res, fragRowCopyUnproven+":"+f.ids["suns"])
	require.Contains(t, r.BookIDs, f.ids["fragG"])
	require.False(t, r.Applicable())
}

// TestFragmentFixer_SymlinkIntoITunesIsManual (L2).
func TestFragmentFixer_SymlinkIntoITunesIsManual(t *testing.T) {
	f := newFragFixture(t)
	require.NoError(t, os.MkdirAll(f.path("books/itunes/Real"), 0o755))
	require.NoError(t, os.MkdirAll(f.path("lib"), 0o755))
	require.NoError(t, os.Symlink(f.path("books/itunes/Real"), f.path("lib/Link")))
	ids := f.looseGroup(t, "lib/Link", "Chap", 3, func(int) bool { return true })
	r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
	require.Equal(t, fragClassManual, r.Class)
	require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
}

// TestFragmentFixer_ITunesIDMakesFragmentManual (H5): a fragment carrying an
// iTunes persistent id is never retired (the purge would queue an iTunes
// remove for it).
func TestFragmentFixer_ITunesIDMakesFragmentManual(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	pid := "ABCDEF0123456789"
	_, err := f.s.ModifyBook(f.ids["fragF"], func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
	require.NoError(t, err)
	row, err := f.s.GetBookFileByID(f.ids["loose02"], f.rowIDs["l02"])
	require.NoError(t, err)
	row.ITunesPersistentID = "0123456789ABCDEF"
	require.NoError(t, f.s.UpdateBookFile(row.ID, row))

	res := f.plan(t, "op-plan")
	for _, id := range []string{"moved:" + f.ids["parent"], noParentRowID(f.path("lib/Loose"), "loose")} {
		r := findRow(t, res, id)
		require.Equal(t, fragClassManual, r.Class, id)
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
	}
}

// TestFragmentFixer_RetireIsAMerge (H4, H5, M7): the retired fragment names
// its parent, loses its path, hands over its external ids and every user's
// position; the revert puts every one of them back and crowns it again.
func TestFragmentFixer_RetireIsAMerge(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	fragPath := f.path("lib/Eldest/03/03/03.mp3")
	require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "audible", ExternalID: "B0FRAG", BookID: frag}))
	u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserPosition(u.ID, frag, f.rowIDs["f03"], 100))

	f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply", []string{"moved:" + parent}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	b, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted())
	require.NotNil(t, b.MergedIntoBookID)
	require.Equal(t, parent, *b.MergedIntoBookID)
	require.Empty(t, b.FilePath, "the purge must not delete the parent's file")
	require.NotNil(t, b.IsPrimaryVersion)
	require.False(t, *b.IsPrimaryVersion)
	owner, err := f.s.GetBookByExternalID("audible", "B0FRAG")
	require.NoError(t, err)
	require.Equal(t, parent, owner)
	pos, err := f.s.ListUserPositionsForBook(u.ID, frag)
	require.NoError(t, err)
	require.Empty(t, pos, "the fragment's position followed")
	pos, err = f.s.ListUserPositionsForBook(u.ID, parent)
	require.NoError(t, err)
	require.Len(t, pos, 1)
	require.InDelta(t, 1300, pos[0].PositionSeconds, 0.01, "chapter 3 starts after two 600 s chapters")

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	b, err = f.s.GetBookByID(frag)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted())
	require.Nil(t, b.MergedIntoBookID)
	require.Equal(t, fragPath, b.FilePath)
	require.True(t, b.IsPrimaryVersion == nil || *b.IsPrimaryVersion, "crowned again")
	owner, err = f.s.GetBookByExternalID("audible", "B0FRAG")
	require.NoError(t, err)
	require.Equal(t, frag, owner)
	pos, err = f.s.ListUserPositionsForBook(u.ID, frag)
	require.NoError(t, err)
	require.Len(t, pos, 1)
	require.InDelta(t, 100, pos[0].PositionSeconds, 0.01)
}

// TestFragmentFixer_RetiredStaysRetiredWhenRepointRevertIsRefused (M2).
func TestFragmentFixer_RetiredStaysRetiredWhenRepointRevertIsRefused(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply", []string{"moved:" + f.ids["parent"]}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	// Someone moves the parent's row on: the repoint can no longer be undone.
	row := f.fileRow(t, "parent", "p03")
	row.FilePath = f.path("lib/Eldest/elsewhere.mp3")
	require.NoError(t, f.s.UpdateBookFile(row.ID, row))

	_, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.Error(t, err, "the repoint is refused")
	require.False(t, f.live(t, "fragF"), "restoring it would leave two live books on one file")
	// Nothing else of the retired book comes back either: a path restored
	// onto a book that stays retired is a path the purge deletes.
	b, err := f.s.GetBookByID(f.ids["fragF"])
	require.NoError(t, err)
	require.Empty(t, b.FilePath)
	require.NotNil(t, b.MergedIntoBookID)
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	for _, c := range changes {
		if c.BookID == f.ids["fragF"] {
			require.Nil(t, c.RevertedAt, "%s of the retired fragment was reverted", c.ChangeType)
		}
	}
}

// TestFragmentFixer_UndoLastApplyRefusesARepairsBatch (M2).
func TestFragmentFixer_UndoLastApplyRefusesARepairsBatch(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	r := findRow(t, res, noParentRowID(f.path("lib/Loose"), "loose"))
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	_, err := metafetch.NewService(f.s).UndoLastApply(r.Proposed["survivor"])
	require.ErrorIs(t, err, metafetch.ErrApplyUndoneFromOperation)
}

// TestFragmentFixer_SurvivorTitleChangedAfterPlan (M3).
func TestFragmentFixer_SurvivorTitleChangedAfterPlan(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	r := findRow(t, res, noParentRowID(f.path("lib/Loose"), "loose"))
	_, err := f.s.ModifyBook(r.Proposed["survivor"], func(b *database.Book) error { b.Title = "Renamed by hand"; return nil })
	require.NoError(t, err)
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	rows, err := f.s.GetBookFiles(r.Proposed["survivor"])
	require.NoError(t, err)
	require.Len(t, rows, 1, "nothing moved")
}

// TestFragmentFixer_ReplanReformsAnAbandonedGroup (M6): a run cut off after
// moving one member's row is re-attributed by a NEW plan, which re-forms the
// same group instead of stranding the emptied member.
func TestFragmentFixer_ReplanReformsAnAbandonedGroup(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	id := noParentRowID(f.path("lib/Loose"), "loose")
	survivor := findRow(t, res, id).Proposed["survivor"]
	var other, otherRow string
	for _, n := range []string{"01", "02", "03"} {
		if f.ids["loose"+n] != survivor {
			other, otherRow = f.ids["loose"+n], f.rowIDs["l"+n]
			break
		}
	}
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-cut")
	require.NoError(t, w.MoveBookFiles([]string{otherRow}, other, survivor))

	res2 := f.plan(t, "op-plan2")
	r := findRow(t, res2, id)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Contains(t, r.BookIDs, other, "the emptied member is still in the group")
	out := f.apply(t, "op-plan2", "op-apply", []string{id}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	rows, err := f.s.GetBookFiles(survivor)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	b, err := f.s.GetBookByID(other)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted(), "the emptied member is retired, not stranded")
}

// TestFragmentFixer_JournalDedupesOnResume (M1): a resumed run journals a
// step it already journaled once, not twice.
func TestFragmentFixer_JournalDedupesOnResume(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	for i := 0; i < 2; i++ {
		w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-x")
		require.NoError(t, w.Journal(f.ids["fragF"], undo.ChangeTypeBookMergedInto, "merged_into_book_id", "", f.ids["parent"]))
	}
	changes, err := f.s.GetOperationChanges("op-x")
	require.NoError(t, err)
	require.Len(t, changes, 1)
}

// TestFragmentFixer_ReplanReformsAGroupCutMidRetire (M6): a run cut off
// after every row moved and one member was retired re-forms the whole group
// on a new plan (the retired member included), and the apply finishes it.
func TestFragmentFixer_ReplanReformsAGroupCutMidRetire(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	id := noParentRowID(f.path("lib/Loose"), "loose")
	survivor := findRow(t, res, id).Proposed["survivor"]
	var others, otherRows []string
	for _, n := range []string{"01", "02", "03"} {
		if f.ids["loose"+n] != survivor {
			others = append(others, f.ids["loose"+n])
			otherRows = append(otherRows, f.rowIDs["l"+n])
		}
	}
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-cut")
	for i := range others {
		require.NoError(t, w.MoveBookFiles([]string{otherRows[i]}, others[i], survivor))
	}
	// The first member was retired before the cut.
	_, err := f.s.ModifyBook(others[0], func(b *database.Book) error {
		yes := true
		b.MarkedForDeletion = &yes
		b.MergedIntoBookID = &survivor
		b.FilePath = ""
		return nil
	})
	require.NoError(t, err)

	res2 := f.plan(t, "op-plan2")
	r := findRow(t, res2, id)
	require.True(t, r.Applicable(), r.SkipReason)
	require.ElementsMatch(t, []string{survivor, others[0], others[1]}, r.BookIDs)
	out := f.apply(t, "op-plan2", "op-apply", []string{id}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	rows, err := f.s.GetBookFiles(survivor)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for _, o := range others {
		b, err := f.s.GetBookByID(o)
		require.NoError(t, err)
		require.True(t, b.IsSoftDeleted(), "%s retired, not stranded", o)
	}
}

// TestFragmentFixer_StrandedMemberIsListed (M6): an emptied member whose
// group no longer re-forms is listed as a held row, not dropped.
func TestFragmentFixer_StrandedMemberIsListed(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	survivor := findRow(t, res, noParentRowID(f.path("lib/Loose"), "loose")).Proposed["survivor"]
	var others, otherRows []string
	for _, n := range []string{"01", "02", "03"} {
		if f.ids["loose"+n] != survivor {
			others = append(others, f.ids["loose"+n])
			otherRows = append(otherRows, f.rowIDs["l"+n])
		}
	}
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-cut")
	require.NoError(t, w.MoveBookFiles([]string{otherRows[0]}, others[0], survivor))
	// The third member left the group some other way: two are not a group.
	_, err := f.s.ModifyBook(others[1], func(b *database.Book) error { yes := true; b.MarkedForDeletion = &yes; return nil })
	require.NoError(t, err)

	r := findRow(t, f.plan(t, "op-plan2"), "stranded:"+others[0])
	require.Equal(t, fragClassHeld, r.Class)
	require.Equal(t, fragSkipStranded, r.Skipped)
}

// TestFragmentFixer_CopyNeedsPathOrHash: a copy whose parent file is still on
// disk is not retired on name, size and duration alone.
func TestFragmentFixer_CopyNeedsPathOrHash(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	h, err := f.s.GetBookFileByID(f.ids["fragH"], f.rowIDs["h01"])
	require.NoError(t, err)
	h.Duration = 600 // now equal to the parent row's
	require.NoError(t, f.s.UpdateBookFile(h.ID, h))
	r := findRow(t, f.plan(t, "op-plan"), fragRowCopyUnproven+":"+f.ids["suns"])
	require.Contains(t, r.BookIDs, f.ids["fragH"])
	require.Equal(t, fragSkipCopyUnproven, r.Skipped)
}

// TestFragmentFixer_RevertCrownsARetiredPrimary (M7): the hand-off promoted a
// sibling when the primary fragment retired; the revert crowns the fragment
// again and demotes the sibling.
func TestFragmentFixer_RevertCrownsARetiredPrimary(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag := f.ids["fragF"]
	group := "vg-frag"
	yes, no := true, false
	_, err := f.s.ModifyBook(frag, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &yes
		return nil
	})
	require.NoError(t, err)
	sibPath := f.file(t, "lib/Sibling/Sibling edition.m4b", 5000)
	sib := f.book(t, "sibling", "Eldest, another edition", sibPath, nil)
	f.row(t, "sib", sib, sibPath, "Sibling edition.m4b", 5000, 36000, 0)
	f.organized(t, sib)
	_, err = f.s.ModifyBook(sib, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &no
		return nil
	})
	require.NoError(t, err)

	f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply", []string{"moved:" + f.ids["parent"]}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	sb, err := f.s.GetBookByID(sib)
	require.NoError(t, err)
	require.True(t, sb.IsPrimaryVersion == nil || *sb.IsPrimaryVersion, "the hand-off promoted the sibling")

	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	fb, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	require.NotNil(t, fb.IsPrimaryVersion)
	require.True(t, *fb.IsPrimaryVersion, "crowned again")
	sb, err = f.s.GetBookByID(sib)
	require.NoError(t, err)
	require.NotNil(t, sb.IsPrimaryVersion)
	require.False(t, *sb.IsPrimaryVersion, "the promoted sibling is demoted again")
}

// TestWriter_OpJournaledMarkerOnlyOnJournaledBooks (N1): in one journaled
// writer, a book with a journaled step gets the marker (undo refused), a book
// that was only Modify'd does not (undo works).
func TestWriter_OpJournaledMarkerOnlyOnJournaledBooks(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	a, b := f.ids["fragF"], f.ids["fragH"]
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-mixed")
	require.NoError(t, w.Step(a, undo.ChangeTypeBookPathUpdate, "file_path", "x", "y", func() error {
		_, err := w.Modify(a, func(bk *database.Book) error { bk.Title = "Journaled"; return nil })
		return err
	}))
	_, err := w.Modify(b, func(bk *database.Book) error { bk.Title = "Plain"; return nil })
	require.NoError(t, err)

	svc := metafetch.NewService(f.s)
	_, err = svc.UndoLastApply(a)
	require.ErrorIs(t, err, metafetch.ErrApplyUndoneFromOperation)
	_, err = svc.UndoLastApply(b)
	require.NoError(t, err)
	got, err := f.s.GetBookByID(b)
	require.NoError(t, err)
	require.Equal(t, "01", got.Title)
}

// TestFragmentFixer_LeftoverSoftDeleteRowCannotUndeleteALaterDelete (N3): a
// retire journaled its soft-delete and never wrote it; the user deleted the
// book later. The revert must not un-delete it.
func TestFragmentFixer_LeftoverSoftDeleteRowCannotUndeleteALaterDelete(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag := f.ids["fragF"]
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-left")
	stamp := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(stamp)))
	later := stamp.Add(time.Hour)
	_, err := f.s.ModifyBook(frag, func(b *database.Book) error {
		yes := true
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &later
		return nil
	})
	require.NoError(t, err)

	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-left")
	require.Error(t, err)
	require.False(t, f.live(t, "fragF"), "the user's own delete stands")
}

// TestFragmentFixer_JournaledStepNeverWrittenIsAlreadyRestored (N5): a
// retire cut off after journaling (nothing written) reverts as already
// restored, not failed.
func TestFragmentFixer_JournaledStepNeverWrittenIsAlreadyRestored(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	b, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-cut")
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookMergedInto, "merged_into_book_id", "", parent))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookPathUpdate, "file_path", b.FilePath, ""))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(time.Now())))

	report, err := undo.PreflightUndoConflicts(f.s, "op-cut")
	require.NoError(t, err)
	require.Empty(t, report.CheckFailed, "%+v", report.CheckFailed)
	require.Equal(t, 4, report.AlreadyRestored, "the preflight counts them (L-b)")

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-cut")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, 4, rr.AlreadyRestored)
	require.Equal(t, 4, rr.Restored)
	require.True(t, f.live(t, "fragF"))
}

// TestFragmentFixer_RetireRefusesBeforeFollowingProgress (N4): an iTunes id
// found at retire time refuses the retire before any user state moves or is
// journaled.
func TestFragmentFixer_RetireRefusesBeforeFollowingProgress(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserPosition(u.ID, frag, f.rowIDs["f03"], 100))
	require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "ABCDEF0123456789", BookID: frag}))

	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-refuse")
	steps, err := newFragmentFixer(f.p).retire(context.Background(), f.s, w, frag, parent, merge.SliceMapping{})
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	require.Zero(t, steps)
	changes, err := f.s.GetOperationChanges("op-refuse")
	require.NoError(t, err)
	require.Empty(t, changes, "nothing journaled")
	pos, err := f.s.ListUserPositionsForBook(u.ID, frag)
	require.NoError(t, err)
	require.Len(t, pos, 1, "the position stayed on the fragment")
}

// TestFragmentFixer_PendingFollowNeverDrainsALiveFragment (N4): a pending
// user-state record left by a follow whose retire failed is deferred while
// the fragment is live; its progress stays.
func TestFragmentFixer_PendingFollowNeverDrainsALiveFragment(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserPosition(u.ID, frag, f.rowIDs["f03"], 100))
	rec, err := json.Marshal(merge.PendingUserStateRepair{LoserBookID: frag, WinnerBookID: parent, RecordedAt: time.Now()})
	require.NoError(t, err)
	require.NoError(t, f.s.SetRaw(merge.PendingUserStateRepairPrefix+frag+":"+parent, rec))

	res, err := merge.CompletePendingUserStateRepairs(f.s, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Deferred)
	pos, err := f.s.ListUserPositionsForBook(u.ID, frag)
	require.NoError(t, err)
	require.Len(t, pos, 1)
}

// TestFragmentFixer_ResumedRetireReusesTheJournaledStamp (L-a): a retire cut
// off after journaling its soft-delete journals no second stamp on resume; it
// writes the stamp it journaled.
func TestFragmentFixer_ResumedRetireReusesTheJournaledStamp(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	stamp := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-resume")
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(stamp)))

	w2 := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-resume")
	_, err := newFragmentFixer(f.p).retire(context.Background(), f.s, w2, frag, parent, merge.SliceMapping{})
	require.NoError(t, err)
	changes, err := f.s.GetOperationChanges("op-resume")
	require.NoError(t, err)
	n := 0
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeBookSoftDelete {
			n++
		}
	}
	require.Equal(t, 1, n, "one soft-delete row")
	b, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	require.NotNil(t, b.MarkedForDeletionAt)
	require.True(t, b.MarkedForDeletionAt.Equal(stamp), "the journaled stamp is written")

	report, err := undo.PreflightUndoConflicts(f.s, "op-resume")
	require.NoError(t, err)
	require.Empty(t, report.CheckFailed, "%+v", report.CheckFailed)
}

// TestFragmentFixer_TwoStampsOfOneRetireAreNotAConflict (L-a): an op that
// already holds two soft-delete stamps for one book (journaled by a resume
// before stamps were reused) reverts cleanly whichever of them was written.
func TestFragmentFixer_TwoStampsOfOneRetireAreNotAConflict(t *testing.T) {
	s1 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	s2 := s1.Add(time.Minute)
	for _, written := range []time.Time{s1, s2} {
		t.Run(written.Format(time.Kitchen), func(t *testing.T) {
			f := newFragFixture(t)
			f.seed(t)
			frag := f.ids["fragF"]
			w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-two")
			require.NoError(t, w.Journal(frag, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(s1)))
			require.NoError(t, w.Journal(frag, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(s2)))
			at := written
			_, err := f.s.ModifyBook(frag, func(b *database.Book) error {
				yes := true
				b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &at
				return nil
			})
			require.NoError(t, err)

			report, err := undo.PreflightUndoConflicts(f.s, "op-two")
			require.NoError(t, err)
			require.Empty(t, report.CheckFailed, "%+v", report.CheckFailed)
			rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-two")
			require.NoError(t, err)
			require.Zero(t, rr.Failed, "%+v", rr)
			require.Equal(t, rr.AlreadyRestored, report.AlreadyRestored, "the preflight predicts the revert's count")
			require.True(t, f.live(t, "fragF"))
		})
	}
}

// TestFragmentFixer_PreflightPredictsDependentRefusals (L-b): when the repoint
// that took a retired fragment's file can no longer be undone, the preflight
// reports every row of the fragment as refused, as the revert does, and its
// restorable count matches what the revert restores.
func TestFragmentFixer_PreflightPredictsDependentRefusals(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply", []string{"moved:" + f.ids["parent"]}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	row := f.fileRow(t, "parent", "p03")
	row.FilePath = f.path("lib/Eldest/elsewhere.mp3")
	require.NoError(t, f.s.UpdateBookFile(row.ID, row))

	report, err := undo.PreflightUndoConflicts(f.s, "op-apply")
	require.NoError(t, err)
	dependent := map[string]bool{}
	for _, it := range report.CheckFailed {
		if it.Reason == undo.ReasonDependentNotReverted {
			dependent[it.ChangeID] = true
		}
	}
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	fragRows := 0
	for _, c := range changes {
		if c.BookID == f.ids["fragF"] && undo.NotRestorableLabel(c) == "" {
			fragRows++
			require.True(t, dependent[c.ID], "%s of the retired fragment is predicted refused", c.ChangeType)
		}
	}
	require.NotZero(t, fragRows)

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.Error(t, err)
	require.Equal(t, rr.Restored, report.Safe+len(report.ContentChanged)+len(report.BookDeleted)+len(report.ReOrganized),
		"preflight %+v vs revert %+v", report, rr)
}

// TestFragmentFixer_AlreadyRestoredDemoteLeavesAnUntouchedGroup (L-d, F1): a
// demote row whose write never happened, with no written soft-delete, is
// already restored and writes nothing: the flags in its group are not the
// operation's (here two primaries a user left) and stay as they are.
func TestFragmentFixer_AlreadyRestoredDemoteLeavesAnUntouchedGroup(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag := f.ids["fragF"]
	group := "vg-frag"
	yes := true
	_, err := f.s.ModifyBook(frag, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &yes
		return nil
	})
	require.NoError(t, err)
	sibPath := f.file(t, "lib/Sibling/Sibling edition.m4b", 5000)
	sib := f.book(t, "sibling", "Eldest, another edition", sibPath, nil)
	f.row(t, "sib", sib, sibPath, "Sibling edition.m4b", 5000, 36000, 0)
	f.organized(t, sib)
	_, err = f.s.ModifyBook(sib, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &yes
		return nil
	})
	require.NoError(t, err)
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-crown")
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-crown")
	require.NoError(t, err)
	require.Equal(t, 1, rr.AlreadyRestored)
	for _, id := range []string{frag, sib} {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b.IsPrimaryVersion)
		require.True(t, *b.IsPrimaryVersion, "%s keeps its flag", id)
	}
}

// TestFragmentFixer_CutOffRetireRevertLeavesTheGroupAlone (F1, review LD1): a
// retire cut off after journaling, before any write, never touched the
// version group. Its revert writes no primary flag: the organized sibling's
// unset flag (read as primary, what ABS shows) stays unset.
func TestFragmentFixer_CutOffRetireRevertLeavesTheGroupAlone(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	group := "vg-cut"
	_, err := f.s.ModifyBook(frag, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = nil
		return nil
	})
	require.NoError(t, err)
	sibPath := f.file(t, "lib/Sibling/Sibling edition.m4b", 5000)
	sib := f.book(t, "sibling", "Eldest, another edition", sibPath, nil)
	f.row(t, "sib", sib, sibPath, "Sibling edition.m4b", 5000, 36000, 0)
	f.organized(t, sib)
	_, err = f.s.ModifyBook(sib, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = nil
		return nil
	})
	require.NoError(t, err)

	fb, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	// What the retire journals for an unset-flag fragment, cut off before it
	// wrote anything.
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-cut-vg")
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookMergedInto, "merged_into_book_id", "", parent))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookPathUpdate, "file_path", fb.FilePath, ""))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(time.Now())))

	report, err := undo.PreflightUndoConflicts(f.s, "op-cut-vg")
	require.NoError(t, err)
	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-cut-vg")
	require.NoError(t, err)
	require.Equal(t, 4, rr.AlreadyRestored, "%+v", rr)
	require.Equal(t, rr.AlreadyRestored, report.AlreadyRestored)

	sb, err := f.s.GetBookByID(sib)
	require.NoError(t, err)
	require.Nil(t, sb.IsPrimaryVersion, "the sibling's flag was written")
	fb, err = f.s.GetBookByID(frag)
	require.NoError(t, err)
	require.Nil(t, fb.IsPrimaryVersion, "the fragment's flag was written")
}

// TestFragmentFixer_AlreadyRestoredDemoteKeepsALaterUserPick (F1, review
// LD2): the fragment is primary again and a user has since picked another
// member too. The demote row is already restored; the revert does not demote
// the user's pick.
func TestFragmentFixer_AlreadyRestoredDemoteKeepsALaterUserPick(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag := f.ids["fragF"]
	group := "vg-user"
	yes := true
	_, err := f.s.ModifyBook(frag, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &yes
		return nil
	})
	require.NoError(t, err)
	otherPath := f.file(t, "lib/Other/Other edition.m4b", 5000)
	other := f.book(t, "other", "Eldest, user pick", otherPath, nil)
	f.organized(t, other)
	_, err = f.s.ModifyBook(other, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &yes
		return nil
	})
	require.NoError(t, err)
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-user")
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))

	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-user")
	require.NoError(t, err)
	ob, err := f.s.GetBookByID(other)
	require.NoError(t, err)
	require.NotNil(t, ob.IsPrimaryVersion)
	require.True(t, *ob.IsPrimaryVersion, "the user's pick was demoted")
}

// crownFailsOnce fails the third version-group read of group, the read
// versionprimary.Crown starts with. Before it: the soft-delete revert's
// incumbent read (versionprimary.IncumbentExcept) and the settle pass's
// own read of the group (RevertService.settleGroup), which then crowns.
type crownFailsOnce struct {
	*database.PebbleStore
	group string
	armed atomic.Bool
	reads atomic.Int32
}

func (s *crownFailsOnce) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	if groupID == s.group && s.reads.Add(1) == 3 && s.armed.CompareAndSwap(true, false) {
		return nil, errors.New("version group read failed")
	}
	return s.PebbleStore.GetBooksByVersionGroup(groupID)
}

// TestFragmentFixer_RetryCrownsAfterACrownFailure (F1, F5): the first revert
// pass restores the retired fragment and its flag, then Crown fails, leaving
// two primaries. The retry finds the demote already restored and, because the
// apply journaled its hand-off of the group (book_primary_handoff), crowns.
func TestFragmentFixer_RetryCrownsAfterACrownFailure(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag := f.ids["fragF"]
	group := "vg-frag"
	yes, no := true, false
	_, err := f.s.ModifyBook(frag, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &yes
		return nil
	})
	require.NoError(t, err)
	sibPath := f.file(t, "lib/Sibling/Sibling edition.m4b", 5000)
	sib := f.book(t, "sibling", "Eldest, another edition", sibPath, nil)
	f.row(t, "sib", sib, sibPath, "Sibling edition.m4b", 5000, 36000, 0)
	f.organized(t, sib)
	_, err = f.s.ModifyBook(sib, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &no
		return nil
	})
	require.NoError(t, err)
	f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply", []string{"moved:" + f.ids["parent"]}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	handOffs := 0
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeBookPrimaryHandoff {
			handOffs++
			require.Equal(t, frag, c.BookID)
			require.Equal(t, group, c.NewValue)
		}
	}
	require.Equal(t, 1, handOffs, "the apply journals its hand-off")

	failing := &crownFailsOnce{PebbleStore: f.s, group: group}
	failing.armed.Store(true)
	_, err = audiobooks.NewRevertService(failing).RevertOperation("op-apply")
	require.Error(t, err, "the first pass's Crown fails")
	require.Contains(t, err.Error(), "demote the rest of group", "the failed read is Crown's")
	require.False(t, failing.armed.Load(), "the Crown ran")
	require.True(t, f.live(t, "fragF"))
	fb, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	require.True(t, fb.IsPrimaryVersion != nil && *fb.IsPrimaryVersion)

	// Every row is restored and marked; the failed group settle is owed
	// and the next revert of the operation retries it.
	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Empty(t, rr.HandOffFailed, "%+v", rr)
	sb, err := f.s.GetBookByID(sib)
	require.NoError(t, err)
	require.NotNil(t, sb.IsPrimaryVersion)
	require.False(t, *sb.IsPrimaryVersion, "the retry crowned the fragment")
}

// TestFragmentFixer_RetryAfterCutOffRetireStillLeavesGroupAlone (F5, review
// LD3): a retire cut off before it wrote anything. A first revert pass
// counted its soft-delete (and every other row but the demote) already
// restored and marked them reverted; the demote hit a transient error. The
// retry finds the demote already restored, and with no hand-off row the
// operation never changed the group's flags, so it crowns nothing.
func TestFragmentFixer_RetryAfterCutOffRetireStillLeavesGroupAlone(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	group := "vg-cut-retry"
	_, err := f.s.ModifyBook(frag, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = nil
		return nil
	})
	require.NoError(t, err)
	sibPath := f.file(t, "lib/Sibling/Sibling edition.m4b", 5000)
	sib := f.book(t, "sibling", "Eldest, another edition", sibPath, nil)
	f.row(t, "sib", sib, sibPath, "Sibling edition.m4b", 5000, 36000, 0)
	f.organized(t, sib)
	_, err = f.s.ModifyBook(sib, func(b *database.Book) error {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = nil
		return nil
	})
	require.NoError(t, err)
	fb, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	w := repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-cut-retry")
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookMergedInto, "merged_into_book_id", "", parent))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookPathUpdate, "file_path", fb.FilePath, ""))
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(time.Now())))

	// The first pass's outcome: every row but the demote marked reverted.
	changes, err := f.s.GetOperationChanges("op-cut-retry")
	require.NoError(t, err)
	var done []string
	for _, c := range changes {
		if c.ChangeType != undo.ChangeTypeBookPrimaryDemote {
			done = append(done, c.ID)
		}
	}
	require.NoError(t, f.s.MarkOperationChangesReverted("op-cut-retry", done))

	report, err := undo.PreflightUndoConflicts(f.s, "op-cut-retry")
	require.NoError(t, err)
	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-cut-retry")
	require.NoError(t, err)
	require.Equal(t, 1, rr.AlreadyRestored, "%+v", rr)
	require.Equal(t, rr.AlreadyRestored, report.AlreadyRestored)
	sb, err := f.s.GetBookByID(sib)
	require.NoError(t, err)
	require.Nil(t, sb.IsPrimaryVersion, "the retry crowned a group the operation never touched")
	fb, err = f.s.GetBookByID(frag)
	require.NoError(t, err)
	require.Nil(t, fb.IsPrimaryVersion, "the fragment's flag was written")
}

// TestFragmentFixer_PathTwinAdoptsMatch: a fragment that matched no parent
// row but shares its exact path with a proven fragment adopts that match,
// so both retire in one row and neither is left as a live co-owner that
// refuses the other's row.
func TestFragmentFixer_PathTwinAdoptsMatch(t *testing.T) {
	f := newFragFixture(t)
	p1 := f.file(t, "lib/P/01.mp3", 801)
	p2 := f.file(t, "lib/P/02.mp3", 802)
	parent := f.book(t, "parent", "P", f.path("lib/P"), nil)
	f.row(t, "p01", parent, p1, "01.mp3", 801, 600, 1)
	f.row(t, "p02", parent, p2, "02.mp3", 802, 600, 2)
	// X: a ghost — imported from the parent's 02 (proven), its own file gone.
	gone := f.path("lib/Q/02/02.mp3")
	ghost := f.book(t, "ghost", "02", p2, nil)
	f.row(t, "x02", ghost, gone, "02.mp3", 802, 600, 0)
	// T: a second book registered for the same gone path, with no evidence of
	// its own: no import history beyond its path, no hash, size unknown.
	twin := f.book(t, "twin", "02", gone, nil)
	f.row(t, "t02", twin, gone, "", 0, 0, 0)

	res := f.plan(t, "op-plan")
	r := findRow(t, res, "ghost:"+parent)
	require.True(t, r.Applicable(), r.SkipReason)
	require.ElementsMatch(t, []string{parent, ghost, twin}, r.BookIDs)
	var adopted bool
	for _, ev := range r.Evidence {
		if strings.Contains(ev, fragEvTwinPrefix+ghost) {
			adopted = true
		}
	}
	require.True(t, adopted, "the twin's evidence names the fragment it adopted from: %v", r.Evidence)
	for _, row := range res.Rows {
		require.NotContains(t, []string{"held:" + twin, "no-parent:" + twin}, row.RowID, "the twin is in the parent's row, not held alone")
	}

	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	for _, id := range []string{ghost, twin} {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		require.True(t, b.IsSoftDeleted(), "%s retired", id)
	}
	pb, err := f.s.GetBookByID(parent)
	require.NoError(t, err)
	require.False(t, pb.IsSoftDeleted())
}

// TestFragmentFixer_PathTwinLimits: what a path twin does NOT do.
func TestFragmentFixer_PathTwinLimits(t *testing.T) {
	// seed: parent P with rows 01, 02 (02 present unless gone), ghost/copy
	// donor D imported from P's 02 at path `at`, then the caller adds a twin.
	seed := func(t *testing.T, f *fragFixture, at string) (parent, donor string) {
		p1 := f.file(t, "lib/P/01.mp3", 801)
		p2 := f.file(t, "lib/P/02.mp3", 802)
		parent = f.book(t, "parent", "P", f.path("lib/P"), nil)
		f.row(t, "p01", parent, p1, "01.mp3", 801, 600, 1)
		f.row(t, "p02", parent, p2, "02.mp3", 802, 600, 2)
		donor = f.book(t, "donor", "02", p2, nil)
		f.row(t, "d02", donor, at, "02.mp3", 802, 600, 0)
		return parent, donor
	}
	inNoRow := func(t *testing.T, res *repairs.PlanResult, id string) {
		t.Helper()
		for _, r := range res.Rows {
			for _, b := range r.BookIDs {
				require.NotEqual(t, id, b, "%s should be in no row, is in %s", id, r.RowID)
			}
		}
	}
	t.Run("twin created first still joins the donor's row", func(t *testing.T) {
		f := newFragFixture(t)
		gone := f.path("lib/Q/02/02.mp3")
		twin := f.book(t, "twin", "02", gone, nil)
		f.row(t, "t02", twin, gone, "", 0, 0, 0)
		parent, donor := seed(t, f, gone)
		r := findRow(t, f.plan(t, "op-plan"), "ghost:"+parent)
		require.ElementsMatch(t, []string{parent, donor, twin}, r.BookIDs)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	})
	t.Run("present twin joins a copy row", func(t *testing.T) {
		f := newFragFixture(t)
		at := f.file(t, "lib/Q/02/02.mp3", 802)
		parent, donor := seed(t, f, at)
		twin := f.book(t, "twin", "02", at, nil)
		f.row(t, "t02", twin, at, "", 0, 0, 0)
		r := findRow(t, f.plan(t, "op-plan"), "copy:"+parent)
		require.True(t, r.Applicable(), r.SkipReason)
		require.ElementsMatch(t, []string{parent, donor, twin}, r.BookIDs)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	})
	t.Run("contradicting import folder is not adopted", func(t *testing.T) {
		f := newFragFixture(t)
		gone := f.path("lib/Q/02/02.mp3")
		parent, donor := seed(t, f, gone)
		twin := f.book(t, "twin", "02", f.path("incoming/OtherBook/02.mp3"), nil)
		f.row(t, "t02", twin, gone, "", 0, 0, 0)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, "ghost:"+parent)
		require.ElementsMatch(t, []string{parent, donor}, r.BookIDs)
		inNoRow(t, res, twin)
	})
	t.Run("contradicting size is not adopted", func(t *testing.T) {
		f := newFragFixture(t)
		gone := f.path("lib/Q/02/02.mp3")
		parent, donor := seed(t, f, gone)
		twin := f.book(t, "twin", "02", gone, nil)
		f.row(t, "t02", twin, gone, "", 999, 0, 0)
		res := f.plan(t, "op-plan")
		require.ElementsMatch(t, []string{parent, donor}, findRow(t, res, "ghost:"+parent).BookIDs)
		inNoRow(t, res, twin)
	})
	// moved: the parent's 02 row is gone (organized away under the donor);
	// donor and twin both name the present file. Row pairs are sorted by
	// book id, so the twin is created first in one variant: the repoint must
	// still carry the donor's size, not the twin's unknown one.
	for _, twinFirst := range []bool{false, true} {
		name := "a moved donor takes the twin and repoints once"
		if twinFirst {
			name += " (twin created first)"
		}
		t.Run(name, func(t *testing.T) {
			f := newFragFixture(t)
			at := f.file(t, "lib/P/02/02/02.mp3", 802)
			var twin string
			if twinFirst {
				twin = f.book(t, "twin", "02", at, nil)
				f.row(t, "t02", twin, at, "", 0, 0, 0)
			}
			p1 := f.file(t, "lib/P/01.mp3", 801)
			parent := f.book(t, "parent", "P", f.path("lib/P"), nil)
			f.row(t, "p01", parent, p1, "01.mp3", 801, 600, 1)
			p2 := f.path("lib/P/02.mp3")
			f.row(t, "p02", parent, p2, "02.mp3", 802, 600, 2)
			donor := f.book(t, "donor", "02", p2, nil)
			f.row(t, "d02", donor, at, "02.mp3", 802, 600, 0)
			if !twinFirst {
				twin = f.book(t, "twin", "02", at, nil)
				f.row(t, "t02", twin, at, "", 0, 0, 0)
			}
			res := f.plan(t, "op-plan")
			r := findRow(t, res, "moved:"+parent)
			require.True(t, r.Applicable(), r.SkipReason)
			require.ElementsMatch(t, []string{parent, donor, twin}, r.BookIDs)
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			p02 := f.fileRow(t, "parent", "p02")
			require.Equal(t, at, p02.FilePath, "the parent's 02 row points at the shared file")
			require.Equal(t, int64(802), p02.FileSize, "the repoint carries the donor's size, not the twin's unknown one")
			for _, id := range []string{donor, twin} {
				b, err := f.s.GetBookByID(id)
				require.NoError(t, err)
				require.True(t, b.IsSoftDeleted(), "%s retired", id)
			}
		})
	}
	t.Run("a twin never lends", func(t *testing.T) {
		f := newFragFixture(t)
		gone := f.path("lib/Q/02/02.mp3")
		parent, donor := seed(t, f, gone)
		t1 := f.book(t, "twin1", "02", gone, nil)
		f.row(t, "t1", t1, gone, "", 0, 0, 0)
		t2 := f.book(t, "twin2", "02", gone, nil)
		f.row(t, "t2", t2, gone, "", 0, 0, 0)
		r := findRow(t, f.plan(t, "op-plan"), "ghost:"+parent)
		require.ElementsMatch(t, []string{parent, donor, t1, t2}, r.BookIDs)
		for _, ev := range r.Evidence {
			if strings.Contains(ev, fragEvTwinPrefix) {
				require.Contains(t, ev, fragEvTwinPrefix+donor, "every twin names the donor, never another twin")
			}
		}
	})
}

// TestFragmentFixer_PathTwinNeedsOneDonor: a path shared by two matched
// fragments, or a donor whose own match is ambiguous, lends nothing.
func TestFragmentFixer_PathTwinNeedsOneDonor(t *testing.T) {
	f := newFragFixture(t)
	gone := f.path("lib/R/02.mp3")
	for _, n := range []string{"A", "B"} {
		p := f.book(t, "parent"+n, "R "+n, f.path("lib/R"+n), nil)
		f.row(t, "p"+n+"01", p, f.file(t, "lib/R"+n+"/01.mp3", 901), "01.mp3", 901, 600, 1)
		f.row(t, "p"+n+"02", p, gone, "02.mp3", 902, 600, 2)
	}
	// X matches both parents' 02 rows (ambiguous); T shares X's path.
	xPath := f.path("lib/R/02/02.mp3")
	x := f.book(t, "fragX", "02", gone, nil)
	f.row(t, "x02", x, xPath, "02.mp3", 902, 600, 0)
	twin := f.book(t, "twin", "02", xPath, nil)
	f.row(t, "t02", twin, xPath, "", 0, 0, 0)
	res := f.plan(t, "op-plan")
	for _, r := range res.Rows {
		for _, id := range r.BookIDs {
			require.NotEqual(t, twin, id, "an ambiguous donor lends nothing: %s", r.RowID)
		}
	}
}
