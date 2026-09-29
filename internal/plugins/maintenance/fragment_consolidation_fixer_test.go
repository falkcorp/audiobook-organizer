// file: internal/plugins/maintenance/fragment_consolidation_fixer_test.go
// version: 1.3.0
// guid: 8e2d5b19-6a4c-4f37-b1d8-2c9e7a3f5d60
// last-edited: 2026-09-29

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
		{"held: fragment file missing", "held:" + f.ids["heldFrag"], fragClassHeld, fragSkipFilesMissing, []string{"heldFrag", "heldParent"}, fragEvImportPath},
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
	require.Len(t, ids, 3, "moved, proven copy and no-parent")
	before03 := *f.fileRow(t, "parent", "p03")

	out := f.apply(t, "op-plan", "op-apply", ids, nil)
	require.Equal(t, 3, out.Applied, "%+v", out.Rows)

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
	for _, role := range []string{"fragF", "fragG", "loose01", "loose02", "loose03"} {
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

// TestFragmentFixer_AlreadyRestoredDemoteStillCrowns (L-d): a demote row whose
// write never happened is already restored, and the revert still makes its
// book the group's one primary.
func TestFragmentFixer_AlreadyRestoredDemoteStillCrowns(t *testing.T) {
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
	fb, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	require.NotNil(t, fb.IsPrimaryVersion)
	require.True(t, *fb.IsPrimaryVersion)
	sb, err := f.s.GetBookByID(sib)
	require.NoError(t, err)
	require.NotNil(t, sb.IsPrimaryVersion)
	require.False(t, *sb.IsPrimaryVersion, "one primary per group")
}
