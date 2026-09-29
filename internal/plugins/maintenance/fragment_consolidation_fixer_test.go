// file: internal/plugins/maintenance/fragment_consolidation_fixer_test.go
// version: 1.0.0
// guid: 8e2d5b19-6a4c-4f37-b1d8-2c9e7a3f5d60
// last-edited: 2026-09-28

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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

func newFragFixture(t *testing.T) *fragFixture {
	t.Helper()
	f := &fragFixture{s: newSeriesPhantomStore(t), root: t.TempDir(), ids: map[string]string{}, rowIDs: map[string]string{}}
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
	f.row(t, "h01", h, hPath, "01.mp3", 201, 600, 0)

	// no-parent
	for i, n := range []string{"03", "01", "02"} {
		p := f.file(t, "lib/Loose/Loose "+n+".mp3", 300+i)
		f.book(t, "loose"+n, "Loose "+n, p, nil)
		f.row(t, "l"+n, f.ids["loose"+n], p, "Loose "+n+".mp3", int64(300+i), 300, 0)
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
	// Doctor Who: the no-parent shape.
	for i, n := range []string{"01", "02", "03"} {
		p := f.file(t, "lib/Doctor Who - Loose/Part "+n+".mp3", 600+i)
		f.book(t, "dw"+n, "Part "+n, p, nil)
		f.row(t, "dw"+n, f.ids["dw"+n], p, "Part "+n+".mp3", int64(600+i), 300, 0)
	}
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

func rowByClass(res *repairs.PlanResult) map[string][]repairs.Row {
	out := map[string][]repairs.Row{}
	for _, r := range res.Rows {
		out[r.Class] = append(out[r.Class], r)
	}
	return out
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
