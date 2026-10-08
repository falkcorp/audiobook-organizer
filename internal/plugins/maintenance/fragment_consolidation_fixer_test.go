// file: internal/plugins/maintenance/fragment_consolidation_fixer_test.go
// version: 1.31.0
// guid: 8e2d5b19-6a4c-4f37-b1d8-2c9e7a3f5d60
// last-edited: 2026-10-08

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
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
//
// It is in memory (database.NewPebbleStoreInMemory), as newCutFixture's has
// been: every Pebble write passes pebble.Sync, and on a real disk ~170 tests
// each paid that fsync on every seed and apply write (on macOS, F_FULLFSYNC,
// the package's wall time was mostly fsync waits). The fixer's reads and
// writes are the same PebbleStore code either way.
func newFragStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
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
	f.p = &Plugin{deps: scanDeps{fakeDeps: fakeDeps{store: f.s, root: f.root}, scan: &scriptedScan{renewsLeft: -1}, ops: f.ops}, standDownWait: noWait}
	return f
}

// newGlobalRootFragFixture is newFragFixture for a test whose code under
// test still reads config.AppConfig.RootDir directly (folder-books and
// duplicate-copies do; the fragment, retire and leftovers code reads
// deps.RootDir, which the fixture sets per test). It also
// swaps the global, so a test using it must not call t.Parallel.
func newGlobalRootFragFixture(t *testing.T) *fragFixture {
	t.Helper()
	f := newFragFixture(t)
	withRoot(t, f.root)
	return f
}

// setDepsRoot points the fixture's deps.RootDir at root (the library root
// the fragment, retire and leftovers code reads).
func (f *fragFixture) setDepsRoot(t *testing.T, root string) {
	t.Helper()
	sd, ok := f.p.deps.(scanDeps)
	require.True(t, ok, "fixture deps are %T, not scanDeps", f.p.deps)
	sd.fakeDeps.root = root
	f.p.deps = sd
}

func (f *fragFixture) path(rel string) string { return filepath.Join(f.root, rel) }

func (f *fragFixture) file(t *testing.T, rel string, size int) string {
	t.Helper()
	p := f.path(rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, fragFixtureBytes(rel, size), 0o644))
	return p
}

// fragFixtureBytes is size bytes of content seeded by seed. Files of one
// size but different seeds differ in content, so a fixture's same-size files
// are not byte-identical copies unless a test writes them so: the fixer
// compares the content of a copy that only its name and size match
// (proveCopiesByContent).
func fragFixtureBytes(seed string, size int) []byte {
	sum := sha256.Sum256([]byte(seed))
	b := make([]byte, size)
	for i := range b {
		b[i] = sum[i%len(sum)] ^ byte(i/len(sum))
	}
	return b
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
//	itunes      books/itunes Eldest twin: moved shape under the iTunes tree,
//	            applicable like any other (owner 2026-10-08)
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
		// 3 h each: whole books, over the repairs' 120-min chapter limit.
		f.row(t, "lg"+n, f.ids["long"+n], p, "Long "+n+".mp3", int64(400+i), 3*3600, 0)
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
	f.applyOp(opID, fragFixerID)
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fragFixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &no, Resume: resume})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsApply(context.Background(), params, rep), "apply op %s of plan %s", opID, planOpID)
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	return res
}

// applyOp records opID as a repairs apply run of fixerID, as the op queue
// does for a real apply: the fragment fixer attributes a journaled retire to
// itself by the op row of the journal row's operation.
func (f *fragFixture) applyOp(opID, fixerID string) {
	params, _ := json.Marshal(repairs.ApplyParams{FixerID: fixerID})
	f.ops.mu.Lock()
	defer f.ops.mu.Unlock()
	if _, ok := f.ops.rows[opID]; !ok {
		f.ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.ApplyOpID, Status: "running", Params: string(params)}
	}
}

// fragWriter is a journaled Writer for the fragment fixer under opID, with
// opID recorded as one of its apply runs (a test's stand-in for a cut-off
// apply).
func (f *fragFixture) fragWriter(t *testing.T, opID string) *repairs.Writer {
	t.Helper()
	f.applyOp(opID, fragFixerID)
	return repairs.NewWriter(f.s, f.s, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, opID)
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
	t.Parallel()
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
		// Name and size only, and the plan read both files: other bytes.
		{"copy content differs", fragClassHeld + ":" + f.ids["fragH"], fragClassHeld, fragSkipCopyUnproven, []string{"suns", "fragH"}, fragEvContentDiffers},
		{"no-parent", noParentRowID(f.path("lib/Loose"), "loose"), fragClassNoParent, "", []string{"loose01", "loose02", "loose03"}, ""},
		{"duration gate", noParentRowID(f.path("lib/Long"), "long"), fragClassNoParent, fragSkipDurationGate, []string{"long01", "long02", "long03"}, ""},
		{"itunes", "moved:" + f.ids["itunesParent"], fragClassMoved, "", []string{"itunesParent", "itunesFrag"}, fragEvImportPath},
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
	require.Equal(t, 1, res.ByClass[fragClassManual], "Doctor Who only")
	require.Equal(t, 2, res.ByClass[fragClassMoved], "Eldest and its iTunes twin")
	require.Equal(t, 1, res.ByClass[fragClassCopy], "the copy whose content differs is held on its own row")
}

// TestFragmentFixer_ApplyThenUndoRoundTrip applies every applicable row and
// reverts the apply operation, checking every step comes back.
func TestFragmentFixer_ApplyThenUndoRoundTrip(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	var ids []string
	for _, r := range res.Rows {
		if r.Applicable() {
			ids = append(ids, r.RowID)
		}
	}
	require.Len(t, ids, 5, "moved (twice: one under the iTunes tree), proven copy, no-parent and ghost")
	before03 := *f.fileRow(t, "parent", "p03")
	beforeHp02 := *f.fileRow(t, "heldParent", "hp02")

	out := f.apply(t, "op-plan", "op-apply", ids, nil)
	require.Equal(t, 5, out.Applied, "%+v", out.Rows)

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
	// iTunes: the fragment is retired into the iTunes parent like any other;
	// only database rows changed (the parent's row is repointed, no file
	// moved), and the row keeps its own iTunes facts.
	require.False(t, f.live(t, "itunesFrag"))
	i02 := f.fileRow(t, "itunesParent", "i02")
	require.Equal(t, f.path("lib/Eldest iTunes/02/02.mp3"), i02.FilePath)
	require.FileExists(t, f.path("lib/Eldest iTunes/02/02.mp3"))
	// hands-off rows untouched.
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
	for _, role := range []string{"fragF", "fragG", "loose01", "loose02", "loose03", "heldFrag", "itunesFrag"} {
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
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	f.plan(t, "op-plan")
	movedID := "moved:" + f.ids["parent"]

	// The first half of the row, as an interrupted run left it.
	w := f.fragWriter(t, "op-cut")
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
	t.Parallel()
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
	w := f.fragWriter(t, "op-cut")
	f.journalPlanRecord(t, w, findRow(t, res, id))
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
		// The plan read both files: K's bytes are not the parent's.
		unproven := findRow(t, res, fragClassHeld+":"+k)
		require.Equal(t, fragSkipCopyUnproven, unproven.Skipped)
		require.Contains(t, unproven.SkipReason, "content differs")
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
			// Two copies of one chapter: the same bytes.
			tmp := f.file(t, "lib/tmp"+n+"/02.mp3", 1102)
			require.NoError(t, os.WriteFile(tmp, fragFixtureBytes("G 02", 1102), 0o644))
			f.organize(t, fr, tmp, f.path("lib/G"+n+"/02/02.mp3"))
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
		// path, so only its name and size match, and the plan reads both
		// files: the same bytes, a proven copy.
		res2 := f.plan(t, "op-plan-2")
		copyRow := findRow(t, res2, "copy:"+parent)
		require.Contains(t, copyRow.BookIDs, other)
		require.Contains(t, strings.Join(copyRow.Evidence, "\n"), fragEvContentHashPrefix)
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
	t.Run("two unproven copies of a present row are each a copy (owner, 2026-10-06)", func(t *testing.T) {
		f := newFragFixture(t)
		parent := seed(t, f)
		var ks []string
		for _, n := range []string{"K", "M"} {
			p := f.file(t, "lib/Elsewhere "+n+"/02.mp3", 1002)
			k := f.book(t, "frag"+n, "02", p, nil)
			f.row(t, n+"02", k, p, "02.mp3", 1002, 590, 0)
			ks = append(ks, k)
		}
		// Their content is not compared (unreadable here): unproven copies.
		f.noContentReads(t)
		res := f.plan(t, "op-plan")
		unproven := findRow(t, res, fragRowCopyUnproven+":"+parent)
		require.Equal(t, fragSkipCopyUnproven, unproven.Skipped, unproven.SkipReason)
		require.ElementsMatch(t, append([]string{parent}, ks...), unproven.BookIDs)
		for _, r := range res.Rows {
			require.NotEqual(t, fragClassAmbiguous, r.Class, "%s: %s", r.RowID, r.SkipReason)
		}
		proven := findRow(t, res, "copy:"+parent)
		require.ElementsMatch(t, []string{parent, f.ids["fragJ"]}, proven.BookIDs, "the proven claimant still pairs")
	})
}

// TestFragmentFixer_SurvivorMustBeOrganizedAndPrimary (H3).
func TestFragmentFixer_SurvivorMustBeOrganizedAndPrimary(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	none := f.looseGroup(t, "lib/NoneOrg", "Chap", 3, func(int) bool { return false })
	last := f.looseGroup(t, "lib/LastOrg", "Chap", 3, func(i int) bool { return i == 3 })
	res := f.plan(t, "op-plan")

	// No member organized: the lowest-id primary member takes the files
	// (2026-10-03; it used to be held as "no survivor").
	r := rowWithBooks(t, res, none)
	require.True(t, r.Applicable(), r.SkipReason)
	sorted := append([]string(nil), none...)
	sort.Strings(sorted)
	require.Equal(t, sorted[0], r.Proposed["survivor"])

	r = rowWithBooks(t, res, last)
	require.True(t, r.Applicable(), r.SkipReason)
	require.Equal(t, last[2], r.Proposed["survivor"], "the only organized member, not the lowest id")

	// An organized member that is not primary blocks the unorganized
	// fallback: the version group has an organized book elsewhere.
	g := newFragFixture(t)
	demoted := g.looseGroup(t, "lib/Demoted", "Chap", 3, func(i int) bool { return i == 1 })
	no := false
	_, err := g.s.ModifyBook(demoted[0], func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
	require.NoError(t, err)
	r = rowWithBooks(t, g.plan(t, "op-plan"), demoted)
	require.Equal(t, fragSkipNoSurvivor, r.Skipped, r.SkipReason)
}

// TestFragmentFixer_ChapterOrderMustBeKnown (H1): two files at one position
// skip the group rather than guess an order.
func TestFragmentFixer_ChapterOrderMustBeKnown(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	f.file(t, "lib/Loose/Bonus interview.mp3", 10)
	r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Loose"), "loose"))
	require.True(t, r.Applicable(), r.SkipReason)
	require.NotContains(t, r.Proposed, "book_path")
}

// TestFragmentFixer_PathProvenCopyComparesSizeOnDisk (L1).
func TestFragmentFixer_PathProvenCopyComparesSizeOnDisk(t *testing.T) {
	t.Parallel()
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

// TestFragmentFixer_SymlinkIntoITunesIsOrdinary (L2; owner 2026-10-08): a
// group behind a symlink into books/itunes is planned like any other.
func TestFragmentFixer_SymlinkIntoITunesIsOrdinary(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	require.NoError(t, os.MkdirAll(f.path("books/itunes/Real"), 0o755))
	require.NoError(t, os.MkdirAll(f.path("lib"), 0o755))
	require.NoError(t, os.Symlink(f.path("books/itunes/Real"), f.path("lib/Link")))
	ids := f.looseGroup(t, "lib/Link", "Chap", 3, func(int) bool { return true })
	r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
	require.Equal(t, fragClassNoParent, r.Class)
	require.NotEqual(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
}

// TestFragmentFixer_ITunesIDFragmentIsOrdinary (owner 2026-10-08, "combine
// them too"; was H5): a fragment carrying an iTunes persistent id, on its
// book or its row, is planned and applied like any other. iTunes is
// import-only, so only database rows change, and a moved row keeps its
// iTunes id and path so the next import finds it on the survivor.
func TestFragmentFixer_ITunesIDFragmentIsOrdinary(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	pid := "ABCDEF0123456789"
	_, err := f.s.ModifyBook(f.ids["fragF"], func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
	require.NoError(t, err)
	row, err := f.s.GetBookFileByID(f.ids["loose02"], f.rowIDs["l02"])
	require.NoError(t, err)
	row.ITunesPersistentID = "0123456789ABCDEF"
	row.ITunesPath = "file://localhost/W:/itunes/iTunes%20Media/Audiobooks/Loose/Loose%2002.mp3"
	require.NoError(t, f.s.UpdateBookFile(row.ID, row))

	res := f.plan(t, "op-plan")
	ids := []string{"moved:" + f.ids["parent"], noParentRowID(f.path("lib/Loose"), "loose")}
	for _, id := range ids {
		r := findRow(t, res, id)
		require.NotEqual(t, fragClassManual, r.Class, id)
		require.True(t, r.Applicable(), "%s: %s: %s", id, r.Skipped, r.SkipReason)
	}
	out := f.apply(t, "op-plan", "op-apply", ids, nil)
	require.Equal(t, 2, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fragF"))
	// The row with the iTunes id now belongs to the survivor, its iTunes
	// facts intact.
	var moved *database.BookFile
	for _, n := range []string{"01", "02", "03"} {
		rows, err := f.s.GetBookFiles(f.ids["loose"+n])
		require.NoError(t, err)
		for i := range rows {
			if rows[i].ID == f.rowIDs["l02"] {
				moved = &rows[i]
			}
		}
	}
	require.NotNil(t, moved)
	require.Equal(t, "0123456789ABCDEF", moved.ITunesPersistentID)
	require.Equal(t, row.ITunesPath, moved.ITunesPath)
	got, err := f.s.GetBookFileByPID("0123456789ABCDEF")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, row.ITunesPath, got.ITunesPath, "the importer's match (PID and iTunes path) still holds")
}

// TestFragmentFixer_RetireIsAMerge (H4, H5, M7): the retired fragment names
// its parent, loses its path, hands over its external ids and every user's
// position; the revert puts every one of them back and crowns it again.
func TestFragmentFixer_RetireIsAMerge(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
// moving one member's row is continued by a NEW plan from the run's plan
// record: the same row, survivor and members, the emptied member included,
// instead of stranding it.
func TestFragmentFixer_ReplanReformsAnAbandonedGroup(t *testing.T) {
	t.Parallel()
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
	w := f.fragWriter(t, "op-cut")
	f.journalPlanRecord(t, w, findRow(t, res, id))
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
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	for i := 0; i < 2; i++ {
		w := f.fragWriter(t, "op-x")
		require.NoError(t, w.Journal(f.ids["fragF"], undo.ChangeTypeBookMergedInto, "merged_into_book_id", "", f.ids["parent"]))
	}
	changes, err := f.s.GetOperationChanges("op-x")
	require.NoError(t, err)
	require.Len(t, changes, 1)
}

// TestFragmentFixer_ReplanReformsAGroupCutMidRetire (M6): a run cut off
// after every row moved and one member was retired is continued whole by a
// new plan (the retired member included), and the apply finishes it.
func TestFragmentFixer_ReplanReformsAGroupCutMidRetire(t *testing.T) {
	t.Parallel()
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
	w := f.fragWriter(t, "op-cut")
	f.journalPlanRecord(t, w, findRow(t, res, id))
	for i := range others {
		require.NoError(t, w.MoveBookFiles([]string{otherRows[i]}, others[i], survivor))
	}
	// The first member was retired before the cut, by this fixer's own
	// retire (a resume accepts only a retire this fixer journaled into the
	// survivor; a bare merged_into write is somebody else's change).
	_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, others[0], survivor,
		&merge.SliceMapping{Mappable: true})
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

// TestFragmentFixer_UnrecordedCutIsHeld: a run that moved and retired
// members and left no plan record (older code, or a record that is gone)
// cannot be continued. A new plan never forms an applicable row over the
// folder's remaining fragments with another survivor: the row is held, and
// the emptied member is listed as stranded.
func TestFragmentFixer_UnrecordedCutIsHeld(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := f.looseGroup(t, "lib/Walk", "Chap", 8, func(int) bool { return true })
	r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
	require.True(t, r.Applicable(), r.SkipReason)
	plan := r.Detail.(*fragGroupPlan)
	w := f.fragWriter(t, "op-cut")
	moved := 0
	var emptied string
	for _, m := range plan.Members {
		if m.Frag.Book.ID == plan.SurvivorID || moved >= 3 {
			continue
		}
		moved++
		require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
		if moved < 3 {
			_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, plan.SurvivorID,
				&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
			require.NoError(t, err)
		} else {
			emptied = m.Frag.Book.ID
		}
	}
	res := f.plan(t, "op-plan2")
	require.Empty(t, applicableRowsWith(res, ids), "no applicable row over the cut set")
	held := 0
	for _, row := range res.Rows {
		if row.Skipped == fragSkipInterrupted {
			held++
			require.Contains(t, row.SkipReason, plan.SurvivorID, "the hold names the book holding the moved files")
			require.NotContains(t, row.BookIDs, plan.SurvivorID)
		}
	}
	require.Equal(t, 1, held, "the folder's four remaining fragments form one row, held")
	st := findRow(t, res, "stranded:"+emptied)
	require.Equal(t, fragSkipStranded, st.Skipped)
}

// TestFragmentFixer_StrandedMemberIsListed (M6): an emptied member whose
// group no longer re-forms is listed as a held row, not dropped.
func TestFragmentFixer_StrandedMemberIsListed(t *testing.T) {
	t.Parallel()
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
	w := f.fragWriter(t, "op-cut")
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
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	h, err := f.s.GetBookFileByID(f.ids["fragH"], f.rowIDs["h01"])
	require.NoError(t, err)
	h.Duration = 600 // now equal to the parent row's
	require.NoError(t, f.s.UpdateBookFile(h.ID, h))
	// The plan compares the two files' content: other bytes, so held.
	r := findRow(t, f.plan(t, "op-plan"), fragClassHeld+":"+f.ids["fragH"])
	require.Contains(t, r.BookIDs, f.ids["suns"])
	require.Equal(t, fragSkipCopyUnproven, r.Skipped)
	require.Contains(t, r.SkipReason, "content differs")
	// With the content not compared, it is an unproven copy as before.
	f2 := newFragFixture(t)
	f2.seed(t)
	h2, err := f2.s.GetBookFileByID(f2.ids["fragH"], f2.rowIDs["h01"])
	require.NoError(t, err)
	h2.Duration = 600
	require.NoError(t, f2.s.UpdateBookFile(h2.ID, h2))
	f2.noContentReads(t)
	r2 := findRow(t, f2.plan(t, "op-plan"), fragRowCopyUnproven+":"+f2.ids["suns"])
	require.Contains(t, r2.BookIDs, f2.ids["fragH"])
	require.Equal(t, fragSkipCopyUnproven, r2.Skipped)
	require.Contains(t, r2.SkipReason, "content not compared")
}

// TestFragmentFixer_RevertCrownsARetiredPrimary (M7): the hand-off promoted a
// sibling when the primary fragment retired; the revert crowns the fragment
// again and demotes the sibling.
func TestFragmentFixer_RevertCrownsARetiredPrimary(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	a, b := f.ids["fragF"], f.ids["fragH"]
	w := f.fragWriter(t, "op-mixed")
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
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	frag := f.ids["fragF"]
	w := f.fragWriter(t, "op-left")
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
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	b, err := f.s.GetBookByID(frag)
	require.NoError(t, err)
	w := f.fragWriter(t, "op-cut")
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

// TestFragmentFixer_RetireCarriesAnITunesFragment (was N4's refusal; owner
// 2026-10-08): an iTunes id on the fragment no longer refuses the retire.
// Its user state and its itunes external id follow it onto the parent like
// any other fragment's.
func TestFragmentFixer_RetireCarriesAnITunesFragment(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	u, err := f.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserPosition(u.ID, frag, f.rowIDs["f03"], 100))
	require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "ABCDEF0123456789", BookID: frag}))

	w := f.fragWriter(t, "op-retire")
	steps, err := newFragmentFixer(f.p).retire(context.Background(), f.s, w, frag, parent, merge.SliceMapping{})
	require.NoError(t, err)
	require.NotZero(t, steps)
	require.False(t, f.live(t, "fragF"))
	exts, err := f.s.GetExternalIDsForBook(parent)
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(exts, func(e database.ExternalIDMapping) bool {
		return e.Source == "itunes" && e.ExternalID == "ABCDEF0123456789"
	}), "the itunes id follows the fragment: %+v", exts)
	pos, err := f.s.ListUserPositionsForBook(u.ID, frag)
	require.NoError(t, err)
	require.Empty(t, pos, "the position followed the fragment")
}

// TestFragmentFixer_PendingFollowNeverDrainsALiveFragment (N4): a pending
// user-state record left by a follow whose retire failed is deferred while
// the fragment is live; its progress stays.
func TestFragmentFixer_PendingFollowNeverDrainsALiveFragment(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	frag, parent := f.ids["fragF"], f.ids["parent"]
	stamp := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	w := f.fragWriter(t, "op-resume")
	require.NoError(t, w.Journal(frag, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(stamp)))

	w2 := f.fragWriter(t, "op-resume")
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
	t.Parallel()
	s1 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	s2 := s1.Add(time.Minute)
	for _, written := range []time.Time{s1, s2} {
		t.Run(written.Format(time.Kitchen), func(t *testing.T) {
			f := newFragFixture(t)
			f.seed(t)
			frag := f.ids["fragF"]
			w := f.fragWriter(t, "op-two")
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
	t.Parallel()
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
	t.Parallel()
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
	w := f.fragWriter(t, "op-crown")
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
	t.Parallel()
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
	w := f.fragWriter(t, "op-cut-vg")
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
	t.Parallel()
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
	w := f.fragWriter(t, "op-user")
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
	t.Parallel()
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
	t.Parallel()
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
	w := f.fragWriter(t, "op-cut-retry")
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
	t.Parallel()
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
	t.Parallel()
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
	// inNoRow: the twin is adopted by no row; it is listed only in its own
	// never-applied unplaced row (no fragment is dropped silently).
	inNoRow := func(t *testing.T, res *repairs.PlanResult, id string) {
		t.Helper()
		for _, r := range res.Rows {
			for _, b := range r.BookIDs {
				if b != id {
					continue
				}
				require.Equal(t, fragClassUnplaced+":"+id, r.RowID, "%s should be in no row but its own, is in %s", id, r.RowID)
				require.False(t, r.Applicable())
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
	t.Parallel()
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
			if id == twin {
				require.Equal(t, fragClassUnplaced+":"+twin, r.RowID, "an ambiguous donor lends nothing: %s", r.RowID)
				require.Equal(t, fragSkipLoneChapter, r.Skipped, "listed as unplaced, never applied")
			}
		}
	}
}

// numberedSeed imports differently-titled numbered chapters from one folder,
// each as its own organized book of durSec seconds, by authorID (nil: none).
// The recorded size is plausible for the duration, so the duration is kept.
func (f *fragFixture) numberedSeed(t *testing.T, dir string, stems []string, durSec int, authorID *int) []string {
	t.Helper()
	var ids []string
	for i, stem := range stems {
		p := f.file(t, dir+"/"+stem+".mp3", 700+i)
		id := f.book(t, "n:"+dir+":"+stem, stem, p, nil)
		f.row(t, "nr:"+dir+":"+stem, id, p, stem+".mp3", int64(durSec)*8000+int64(i), durSec, 0)
		f.organized(t, id)
		if authorID != nil {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = authorID; return nil })
			require.NoError(t, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func noRow(t *testing.T, res *repairs.PlanResult, id, why string) {
	t.Helper()
	for _, row := range res.Rows {
		require.NotEqual(t, id, row.RowID, why)
	}
}

// serialStems is a serial as it sits on disk: one folder, numbered from 1,
// each chapter with its own name, three of them named alike, two with a
// trailing part marker.
var serialStems = []string{"005 - Core", "001 - Skating", "002 - Interlude", "003 - Gear", "004 - Interlude",
	"006 - Interlude", "007 - Arc Part 1", "008 - Arc Part 2"}

// TestFragmentFixer_NumberedSet: a folder's numbered chapters with titles of
// their own form ONE no-parent row, in number order, titled from the folder.
func TestFragmentFixer_NumberedSet(t *testing.T) {
	t.Run("differently titled chapters group and apply", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.numberedSeed(t, "lib/Serial", serialStems, 300, nil)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, noParentRowID(f.path("lib/Serial"), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, fragClassNoParent, r.Class)
		require.Equal(t, repairs.RiskReview, r.Risk)
		require.ElementsMatch(t, ids, r.BookIDs, "Interludes and Arc parts belong to the set, not to books of their own")
		require.Equal(t, "Serial", r.Proposed["title"])
		require.Contains(t, r.Evidence[0], "numbered chapters with titles of their own")
		require.Contains(t, r.Evidence[len(r.Evidence)-1], "in order: 001 - Skating | 002 - Interlude")
		noRow(t, res, noParentRowID(f.path("lib/Serial"), "interlude"), "no separate key group inside a numbered set")
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		rows, err := f.s.GetBookFiles(r.Proposed["survivor"])
		require.NoError(t, err)
		track := map[string]int{}
		for _, row := range rows {
			track[filepath.Base(row.FilePath)] = row.TrackNumber
		}
		require.Equal(t, map[string]int{"001 - Skating.mp3": 1, "002 - Interlude.mp3": 2, "003 - Gear.mp3": 3,
			"004 - Interlude.mp3": 4, "005 - Core.mp3": 5, "006 - Interlude.mp3": 6,
			"007 - Arc Part 1.mp3": 7, "008 - Arc Part 2.mp3": 8}, track)
		sb, err := f.s.GetBookByID(r.Proposed["survivor"])
		require.NoError(t, err)
		require.Equal(t, "Serial", sb.Title, "titled from the folder, never from a chapter")
	})
	t.Run("fewer than three numbered files form nothing", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.numberedSeed(t, "lib/Pair", []string{"01 - One", "02 - Two"}, 300, nil)
		res := f.plan(t, "op-plan")
		require.Zero(t, res.Applicable)
		require.Len(t, res.Rows, 2, "each file is listed as unplaced, never dropped")
		for i, id := range ids {
			r := findRow(t, res, fragClassUnplaced+":"+id)
			require.Equal(t, fragClassUnplaced, r.Class)
			require.Equal(t, fragSkipLoneChapter, r.Skipped, "file %d: %s", i, r.SkipReason)
		}
	})

	// held: the folder is listed as one row, never applicable, with the
	// reason; and no key group is carved out of it.
	held := func(t *testing.T, f *fragFixture, dir, want string) repairs.Row {
		t.Helper()
		res := f.plan(t, "op-plan")
		r := findRow(t, res, noParentRowID(f.path(dir), fragNumberedKey))
		require.False(t, r.Applicable())
		require.Contains(t, r.SkipReason, want)
		require.Zero(t, res.Applicable, "nothing in this folder may apply")
		return r
	}
	author := func(t *testing.T, f *fragFixture, name string) *int {
		t.Helper()
		a, err := f.s.CreateAuthor(name)
		require.NoError(t, err)
		return &a.ID
	}
	for _, tc := range []struct {
		name, dir string
		stems     []string
		dur       int
		author    string
		want      string
	}{
		{"a folder with no title of its own", "audiobooks/Serial", serialStems, 300, "", "gives no title for the work"},
		{"files dropped in the library root", ".", []string{"01 - Green Eggs and Ham", "02 - Some Podcast Episode", "03 - A Poem"}, 300, "", "is a library root or import path"},
		{"a folder directly under the library root", "Kids", serialStems, 300, "", "directly under the library root"},
		{"years and title numbers are not chapter numbers", "lib/Years", []string{"1632 - sample x", "1984 - sample", "2001 - A Space Odyssey sample"}, 300, "", "not a chapter run from 0 or 1"},
		{"an author folder", "lib/Jane Author", serialStems, 300, "Jane Author", "named for the files' author"},
		{"an author folder by initial", "lib/J. Author", serialStems, 300, "Jane Author", "named for the files' author"},
		{"an author folder, surname first", "lib/Author, Jane", serialStems, 300, "Jane Author", "named for the files' author"},
		{"an author's collection folder", "lib/Jane Author Collection", serialStems, 300, "Jane Author", "named for the files' author"},
		{"a duplicate download is not an extra chapter", "lib/Serial", append([]string{"005 - Core (1)"}, serialStems...), 300, "", "carry the same chapter number"},
		{"scattered chapters named alike are not carved out by a duplicate number", "lib/Dup",
			[]string{"01 - A", "01 - B", "02 - C", "03 - D", "04 - Intro", "05 - F", "06 - Intro", "07 - H", "08 - Intro", "09 - Z"}, 300, "", "carry the same chapter number"},
		{"a shelf of short works", "lib/Kids", []string{"1 - Green Eggs and Ham", "2 - The Cat in the Hat", "3 - Fox in Socks"}, 300, "", "only 3 files"},
		{"two two-part works", "lib/Shelf", []string{"01 - Book A", "02 - Book A", "03 - Book B", "04 - Book B"}, 300, "", "several multi-part works"},
		{"one chapter too long for the gate holds the whole run", "lib/Serial", append([]string{"009 - Long One"}, serialStems...), 0, "", "min or longer"},
		{"a short run with three chapters named alike is not carved up", "lib/Short", []string{"01 - A", "02 - Intro", "03 - B", "04 - Intro", "05 - C", "06 - Intro"}, 300, "", "only 6 files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFragFixture(t)
			var a *int
			if tc.author != "" {
				a = author(t, f, tc.author)
			}
			if tc.dur == 0 {
				f.numberedSeed(t, tc.dir, tc.stems[:1], 3*3600, a)
				f.numberedSeed(t, tc.dir, tc.stems[1:], 300, a)
			} else {
				f.numberedSeed(t, tc.dir, tc.stems, tc.dur, a)
			}
			held(t, f, tc.dir, tc.want)
		})
	}
	// The repairs' chapter limit is repair_chapter_max_min (default 120), not
	// the import scanner's chapter_consolidation_threshold_min (owner
	// 2026-10-03: "one for import, one for jobs").
	t.Run("hour-long chapters merge under the repairs' own limit", func(t *testing.T) {
		prevImport, prevRepair := config.AppConfig.ChapterConsolidationThresholdMin, config.AppConfig.RepairChapterMaxMin
		t.Cleanup(func() {
			config.AppConfig.ChapterConsolidationThresholdMin, config.AppConfig.RepairChapterMaxMin = prevImport, prevRepair
		})
		config.AppConfig.ChapterConsolidationThresholdMin, config.AppConfig.RepairChapterMaxMin = 10, 120
		f := newFragFixture(t)
		f.numberedSeed(t, "lib/Serial", serialStems, 60*60, nil)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Serial"), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)

		config.AppConfig.RepairChapterMaxMin = 30
		held(t, f, "lib/Serial", "30 min or longer")
	})
	// Leading pairs (2026-10-03): "1-03 Title" is book/disc 1, chapter 3.
	t.Run("a pair-numbered set sharing its first number is one run", func(t *testing.T) {
		f := newFragFixture(t)
		f.numberedSeed(t, "lib/Paired", []string{"1-01 Arrival", "1-02 The Road", "1-03 Gear", "1-04 Ash",
			"1-05 Night", "1-06 Ember", "1-07 Coda", "1-08 Home"}, 300, nil)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Paired"), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Len(t, r.BookIDs, 8)
	})
	t.Run("a leading number plus a trailing (n of m) is no pair: the serial stays one run", func(t *testing.T) {
		f := newFragFixture(t)
		var stems []string
		for i, name := range []string{"Arrival", "The Road", "Gear", "Ash", "Night", "Ember", "Coda", "Home"} {
			stems = append(stems, fmt.Sprintf("%03d - %s (%d of 8)", i+1, name, i+1))
		}
		f.numberedSeed(t, "lib/OfM", stems, 300, nil)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/OfM"), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})
	t.Run("two titles on two discs split into their own groups", func(t *testing.T) {
		f := newFragFixture(t)
		f.numberedSeed(t, "lib/TwoDiscs", []string{"1-01 Book A", "1-02 Book A", "1-03 Book A", "1-04 Book A",
			"2-01 Book B", "2-02 Book B", "2-03 Book B", "2-04 Book B"}, 300, nil)
		res := f.plan(t, "op-plan")
		var groups int
		for _, r := range res.Rows {
			if r.Class == fragClassNoParent && len(r.BookIDs) == 4 {
				groups++
			}
		}
		require.Equal(t, 2, groups, "one row per title, never one row for both")
	})
	t.Run("a pair-numbered set across several discs is held", func(t *testing.T) {
		f := newFragFixture(t)
		f.numberedSeed(t, "lib/Discs", []string{"1-01 Arrival", "1-02 The Road", "1-03 Gear", "1-04 Ash",
			"2-01 Night", "2-02 Ember", "2-03 Coda", "2-04 Home"}, 300, nil)
		held(t, f, "lib/Discs", "disc-track numbers across several discs")
	})
	// Junk author/series fields copied from paths count as missing (owner
	// 2026-10-03).
	t.Run("an author field that is the folder's own non-person name is ignored", func(t *testing.T) {
		f := newFragFixture(t)
		dir := "lib/Pyper Down/Jennsen/Jennsen, GS_ 08 Rubicon (Amaranthe 08)"
		f.numberedSeed(t, dir, serialStems, 300, author(t, f, "Jennsen, GS_ 08 Rubicon (Amaranthe 08)"))
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})
	t.Run("series fields that are file names are ignored, a real second series is not", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.numberedSeed(t, "lib/Gone/08) Villain", serialStems, 300, nil)
		for i, id := range ids {
			name := []string{"read by narrator", "01.Intro", "02.Prologue"}[i%3]
			sr, err := f.s.CreateSeries(name, nil)
			require.NoError(t, err)
			_, err = f.s.ModifyBook(id, func(b *database.Book) error { b.SeriesID = &sr.ID; return nil })
			require.NoError(t, err)
		}
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Gone/08) Villain"), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)

		g := newFragFixture(t)
		ids = g.numberedSeed(t, "lib/Children", serialStems, 300, nil)
		for i, id := range ids {
			name := []string{"Children of Time", "Children of Ruin"}[i%2]
			sr, err := g.s.CreateSeries(name, nil)
			require.NoError(t, err)
			_, err = g.s.ModifyBook(id, func(b *database.Book) error { b.SeriesID = &sr.ID; return nil })
			require.NoError(t, err)
		}
		held(t, g, "lib/Children", "different series")
	})
	t.Run("an author folder whose files have no author linked", func(t *testing.T) {
		f := newFragFixture(t)
		author(t, f, "Jane Author") // known to the library, linked to none of the files
		f.numberedSeed(t, "lib/Jane Author", serialStems, 300, nil)
		r := held(t, f, "lib/Jane Author", `named like the author "Jane Author"`)
		noRow(t, &repairs.PlanResult{Rows: []repairs.Row{r}}, noParentRowID(f.path("lib/Jane Author"), "interlude"), "")
	})
	t.Run("files by different authors", func(t *testing.T) {
		f := newFragFixture(t)
		f.numberedSeed(t, "lib/Mixed", serialStems[:5], 300, author(t, f, "First Writer"))
		f.numberedSeed(t, "lib/Mixed", serialStems[5:], 300, author(t, f, "Second Writer"))
		held(t, f, "lib/Mixed", "different authors")
	})
	t.Run("a member with its own ASIN is a published work", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.numberedSeed(t, "lib/Serial", serialStems, 300, nil)
		asin := "B00TESTASIN"
		_, err := f.s.ModifyBook(ids[3], func(b *database.Book) error { b.ASIN = &asin; return nil })
		require.NoError(t, err)
		held(t, f, "lib/Serial", "carries its own ASIN")
	})
	t.Run("a member titled otherwise than its file is a work of its own", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.numberedSeed(t, "lib/Serial", serialStems, 300, nil)
		_, err := f.s.ModifyBook(ids[2], func(b *database.Book) error { b.Title = "A Real Novel"; return nil })
		require.NoError(t, err)
		held(t, f, "lib/Serial", `is titled "A Real Novel"`)
	})
	t.Run("one file per disc folder", func(t *testing.T) {
		f := newFragFixture(t)
		for i, stem := range []string{"CD1/01 - Alpha", "CD2/01 - Beta", "CD3/01 - Gamma"} {
			p := f.file(t, "lib/Shelf/"+stem+".mp3", 700+i)
			id := f.book(t, "d"+stem, filepath.Base(stem), p, nil)
			f.row(t, "dr"+stem, id, p, filepath.Base(stem)+".mp3", 2400000, 300, 0)
			f.organized(t, id)
		}
		held(t, f, "lib/Shelf", "not formed across discs")
	})
	t.Run("a book's disc folders still form their key group", func(t *testing.T) {
		f := newFragFixture(t)
		for i, stem := range []string{"CD1/00 - Intro", "CD1/01 - Book", "CD1/02 - Book", "CD2/01 - Book", "CD2/02 - Book"} {
			p := f.file(t, "lib/Discs/"+stem+".mp3", 700+i)
			id := f.book(t, "d"+stem, filepath.Base(stem), p, nil)
			f.row(t, "dr"+stem, id, p, filepath.Base(stem)+".mp3", 2400000, 300, 0)
			f.organized(t, id)
		}
		res := f.plan(t, "op-plan")
		noRow(t, res, noParentRowID(f.path("lib/Discs"), fragNumberedKey), "discs are the key groups' to take")
		r := findRow(t, res, noParentRowID(f.path("lib/Discs"), "book"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Len(t, r.BookIDs, 4)
		require.Contains(t, r.Evidence[len(r.Evidence)-1], "1 other numbered file(s)")
	})

	// A folder that is NOT one numbered run: the key groups decide, as they
	// did before the rule, and the key row says what else the folder holds.
	fallback := func(t *testing.T, stems []string, wantKey string, wantN int, wantOthers string) {
		t.Helper()
		f := newFragFixture(t)
		f.numberedSeed(t, "lib/Two", stems, 300, nil)
		res := f.plan(t, "op-plan")
		noRow(t, res, noParentRowID(f.path("lib/Two"), fragNumberedKey), "the key groups decide")
		r := findRow(t, res, noParentRowID(f.path("lib/Two"), wantKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Len(t, r.BookIDs, wantN)
		require.Contains(t, r.Evidence[len(r.Evidence)-1], wantOthers)
	}
	t.Run("two works that both start at 01 stay two key groups", func(t *testing.T) {
		fallback(t, []string{"01 - Book A", "02 - Book A", "03 - Book A", "01 - Book B", "02 - Book B", "03 - Book B"}, "book b", 3, "3 other numbered file(s)")
	})
	t.Run("a work numbered after another is not its chapter", func(t *testing.T) {
		fallback(t, []string{"01 - Book A", "02 - Book A", "03 - Book A", "04 - Book B", "05 - Book B"}, "book a", 3, "2 other numbered file(s)")
	})
	t.Run("a stray file after a key group does not join it", func(t *testing.T) {
		fallback(t, []string{"01 - Intro", "02 - Intro", "03 - Intro", "04 - Something Else"}, "intro", 3, "1 other numbered file(s)")
	})
}

func TestFolderNamesAuthor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		folder, author string
		want           bool
	}{
		{"Jane Author", "Jane Author", true},
		{"J. Author", "Jane Author", true},
		{"Author, Jane", "Jane Author", true},
		{"Jane Author Collection", "Jane Author", true},
		{"Jane Author - Short Stories", "Jane Author", true},
		{"SenescentSoul", "Jane Author", false},
		{"The Author's Tale", "Jane Author", false},
		{"Delve", "SenescentSoul", false},
		{"Jane", "Jane Author", false},
		{"Serial", "", false},
	} {
		require.Equal(t, tc.want, folderNamesAuthor(tc.folder, tc.author), "%q vs %q", tc.folder, tc.author)
	}
}

// TestFragmentFixer_CoOwnerIsHeldAtPlan: a row whose file a live book outside
// the row also owns is listed held, naming that book, instead of being
// planned applicable and refused at apply.
func TestFragmentFixer_CoOwnerIsHeldAtPlan(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T, f *fragFixture) (parent, frag, other string) {
		p1 := f.file(t, "lib/P/01.mp3", 801)
		parent = f.book(t, "parent", "P", f.path("lib/P"), nil)
		f.row(t, "p01", parent, p1, "01.mp3", 801, 600, 1)
		p2 := f.path("lib/P/02.mp3") // gone: organized away under the fragment
		f.row(t, "p02", parent, p2, "02.mp3", 802, 600, 2)
		at := f.file(t, "lib/P/02/02/02.mp3", 802)
		frag = f.book(t, "frag", "02", p2, nil)
		f.row(t, "f02", frag, at, "02.mp3", 802, 600, 0)
		// A real-titled book that also holds a row at the fragment's file: not
		// a chapter-shaped twin, so it is in no row of its own.
		other = f.book(t, "other", "Prelude to P", at, nil)
		f.row(t, "o02", other, at, "Prelude to P.mp3", 802, 600, 0)
		return parent, frag, other
	}
	t.Run("a moved row is held and keeps its class", func(t *testing.T) {
		f := newFragFixture(t)
		parent, frag, other := seed(t, f)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, "moved:"+parent)
		require.False(t, r.Applicable())
		require.Equal(t, fragClassMoved, r.Class, "the class chip still counts it under Moved")
		require.Equal(t, fragSkipCoOwner, r.Skipped)
		require.Equal(t, repairs.RiskReview, r.Risk)
		require.Contains(t, r.SkipReason, "1 file(s) of this row are also owned by 1 live book(s)")
		require.Contains(t, r.SkipReason, other)
		require.Contains(t, r.SkipReason, "Prelude to P")
		require.ElementsMatch(t, []string{parent, frag}, r.BookIDs, "the co-owner is listed, never written")
		var role string
		for _, m := range r.Members {
			if m.BookID == other {
				role = m.Role
			}
		}
		require.Equal(t, "co-owner", role)
		require.Zero(t, res.Applicable)
	})
	t.Run("a retired co-owner holds nothing", func(t *testing.T) {
		f := newFragFixture(t)
		parent, _, other := seed(t, f)
		_, err := f.s.ModifyBook(other, func(b *database.Book) error { yes := true; b.MarkedForDeletion = &yes; return nil })
		require.NoError(t, err)
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+parent)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})
	t.Run("a no-parent row is held too", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.numberedSeed(t, "lib/Serial", serialStems, 300, nil)
		at := f.path("lib/Serial/003 - Gear.mp3")
		other := f.book(t, "other", "A Real Book", at, nil)
		f.row(t, "o", other, at, "A Real Book.mp3", 701, 300, 0)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Serial"), fragNumberedKey))
		require.Equal(t, fragClassNoParent, r.Class)
		require.Equal(t, fragSkipCoOwner, r.Skipped, r.SkipReason)
		require.ElementsMatch(t, ids, r.BookIDs)
	})
}

// TestFragmentFixer_RenamedChapterCopies: a folder holding every chapter
// twice, under its original name and renamed, with the same size (owner
// decision 2026-10-03: one chapter). The copies are not members: their
// books are retired into the survivor, each keeping its own file row.
func TestFragmentFixer_RenamedChapterCopies(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T, f *fragFixture, dir string, sizeOf func(n int, copy bool) int) (orig, copies []string) {
		for n := 1; n <= 8; n++ {
			for _, cp := range []bool{false, true} {
				stem := fmt.Sprintf("02_%03d", n)
				if cp {
					stem += " - 02_light_of_other_days - read by narrator"
				}
				size := sizeOf(n, cp)
				p := f.file(t, dir+"/"+stem+".mp3", size)
				id := f.book(t, "c:"+stem, stem, p, nil)
				f.row(t, "cr:"+stem, id, p, stem+".mp3", int64(size), 300, 0)
				if cp {
					copies = append(copies, id)
				} else {
					orig = append(orig, id)
				}
			}
		}
		return orig, copies
	}
	t.Run("same size: one chapter, the copy retired with its row", func(t *testing.T) {
		f := newFragFixture(t)
		dir := "lib/Clarke/02_light_of_other_days"
		orig, copies := seed(t, f, dir, func(n int, _ bool) int { return 1000 + n })
		res := f.plan(t, "op-plan")
		for _, x := range res.Rows {
			t.Logf("ROW %s class=%s books=%d skipped=%s %s", x.RowID, x.Class, len(x.BookIDs), x.Skipped, x.SkipReason)
		}
		r := findRow(t, res, noParentRowID(f.path(dir), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Len(t, r.BookIDs, 16)
		require.Contains(t, r.Proposed["action"], "retire 8 renamed copy book(s)")
		plan := r.Detail.(*fragGroupPlan)
		require.Len(t, plan.Members, 8)
		require.Len(t, plan.Copies, 8)

		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
		survivor := r.Proposed["survivor"]
		rows, err := f.s.GetBookFiles(survivor)
		require.NoError(t, err)
		require.Len(t, rows, 8, "the survivor holds one file per chapter, never the copies")
		for _, id := range copies {
			b, err := f.s.GetBookByID(id)
			require.NoError(t, err)
			require.True(t, b.IsSoftDeleted(), "copy %s retired", id)
			own, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			require.Len(t, own, 1, "a retired copy keeps its own file row")
		}
		for _, id := range orig {
			if id == survivor {
				continue
			}
			b, err := f.s.GetBookByID(id)
			require.NoError(t, err)
			require.True(t, b.IsSoftDeleted())
		}
	})
	t.Run("different sizes at one position stay a conflict", func(t *testing.T) {
		f := newFragFixture(t)
		dir := "lib/Clarke/03_light_of_other_days"
		seed(t, f, dir, func(n int, cp bool) int {
			if cp && n == 4 {
				return 5000
			}
			return 1000 + n
		})
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.False(t, r.Applicable())
		require.Contains(t, r.SkipReason, "same chapter number")
	})
}

// seedChapterCopies builds the "02_light_of_other_days" folder: chapters
// 1-8 as "02_00N" plus a renamed copy "02_00N - 02_light_of_other_days -
// read by narrator" of each, same size. copyFirst(n) creates chapter n's
// copy BEFORE its original, so the copy holds the lower book id.
func (f *fragFixture) seedChapterCopies(t *testing.T, dir string, copyFirst func(n int) bool) (orig, copies []string) {
	t.Helper()
	for n := 1; n <= 8; n++ {
		order := []bool{false, true}
		if copyFirst != nil && copyFirst(n) {
			order = []bool{true, false}
		}
		for _, cp := range order {
			stem := fmt.Sprintf("02_%03d", n)
			if cp {
				stem += " - 02_light_of_other_days - read by narrator"
			}
			size := 1000 + n
			p := f.file(t, dir+"/"+stem+".mp3", size)
			id := f.book(t, "c:"+stem, stem, p, nil)
			f.row(t, "cr:"+stem, id, p, stem+".mp3", int64(size), 300, 0)
			if cp {
				copies = append(copies, id)
			} else {
				orig = append(orig, id)
			}
		}
	}
	return orig, copies
}

// applicableRowsWith lists the applicable rows that hold any of ids.
func applicableRowsWith(res *repairs.PlanResult, ids []string) []repairs.Row {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []repairs.Row
	for _, r := range res.Rows {
		if !r.Applicable() {
			continue
		}
		for _, id := range r.BookIDs {
			if want[id] {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// TestFragmentFixer_NumberedCopiesReview covers the review of PR #3700: renamed
// chapter copies whatever their book ids, iTunes ids on copies, resuming a
// run cut off after the retitle, and the copy-aware set checks.
func TestFragmentFixer_NumberedCopiesReview(t *testing.T) {
	t.Parallel()
	const dir = "lib/Clarke/02_light_of_other_days"
	copyStem := " - 02_light_of_other_days - read by narrator"

	for _, tc := range []struct {
		name      string
		copyFirst func(n int) bool
	}{
		{"copies hold the lower ids of chapters 1-4", func(n int) bool { return n <= 4 }},
		{"copies hold every lower id", func(int) bool { return true }},
		{"copies hold the lower ids of the even chapters", func(n int) bool { return n%2 == 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFragFixture(t)
			orig, copies := f.seedChapterCopies(t, dir, tc.copyFirst)
			res := f.plan(t, "op-plan")
			got := applicableRowsWith(res, append(append([]string(nil), orig...), copies...))
			require.Len(t, got, 1, "one applicable row for one work; never two books of the same audio")
			r := got[0]
			require.Equal(t, noParentRowID(f.path(dir), fragNumberedKey), r.RowID)
			plan := r.Detail.(*fragGroupPlan)
			require.Len(t, plan.Members, 8)
			require.Len(t, plan.Copies, 8)
			for _, m := range plan.Members {
				require.NotContains(t, m.Frag.origStem(), copyStem, "the folder's commonest key is kept in every chapter")
			}
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
			rows, err := f.s.GetBookFiles(plan.SurvivorID)
			require.NoError(t, err)
			require.Len(t, rows, 8)
			for _, id := range copies {
				own, err := f.s.GetBookFiles(id)
				require.NoError(t, err)
				require.Len(t, own, 1, "a retired copy keeps its own file row")
			}
		})
	}

	t.Run("organized copies that split the keys hold the folder, never two rows", func(t *testing.T) {
		f := newFragFixture(t)
		orig, copies := f.seedChapterCopies(t, dir, nil)
		for _, id := range copies[:4] {
			f.organized(t, id) // organized and primary: keptOfCopies keeps them first
		}
		res := f.plan(t, "op-plan")
		require.Empty(t, applicableRowsWith(res, append(append([]string(nil), orig...), copies...)))
		r := findRow(t, res, noParentRowID(f.path(dir), fragNumberedKey))
		require.False(t, r.Applicable())
		require.Len(t, r.BookIDs, 16, "the whole folder stays one held row")
	})

	t.Run("same size but different hashes stay a conflict", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		for i, id := range append([]string{f.ids["c:02_004"]}, copies[3]) {
			rows, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			rows[0].FileHash = fmt.Sprintf("hash-%d", i)
			require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
		}
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.False(t, r.Applicable())
		require.Contains(t, r.SkipReason, "same chapter number")
	})

	t.Run("equal hashes, or one unknown, are still one chapter", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		set := func(id, h string) {
			rows, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			rows[0].FileHash = h
			require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
		}
		set(f.ids["c:02_004"], "same")
		set(copies[3], "same")
		set(f.ids["c:02_005"], "only-one-known")
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Len(t, r.Detail.(*fragGroupPlan).Copies, 8)
	})

	t.Run("an iTunes id on a copy is ordinary (owner 2026-10-08)", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		rows, err := f.s.GetBookFiles(copies[3])
		require.NoError(t, err)
		rows[0].ITunesPersistentID = "0123456789ABCDEF"
		require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.NotEqual(t, fragClassManual, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})

	t.Run("a copy carrying an ASIN holds the set", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		_, err := f.s.ModifyBook(copies[2], func(b *database.Book) error { asin := "B000TEST01"; b.ASIN = &asin; return nil })
		require.NoError(t, err)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.False(t, r.Applicable())
		require.Contains(t, r.SkipReason, "ASIN")
	})

	t.Run("a copy by another author holds the set", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		a, err := f.s.CreateAuthor("Someone Else")
		require.NoError(t, err)
		_, err = f.s.ModifyBook(copies[6], func(b *database.Book) error { b.AuthorID = &a.ID; return nil })
		require.NoError(t, err)
		other, err := f.s.CreateAuthor("Arthur Clarke")
		require.NoError(t, err)
		f.setAuthor(t, f.ids["c:02_001"], other.ID)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.False(t, r.Applicable())
		require.Contains(t, r.SkipReason, "different authors")
	})

	t.Run("rowPaths lists the copies' files", func(t *testing.T) {
		f := newFragFixture(t)
		f.seedChapterCopies(t, dir, nil)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.Len(t, rowPaths(r), 16)
	})
}

func (f *fragFixture) setAuthor(t *testing.T, bookID string, authorID int) {
	t.Helper()
	_, err := f.s.ModifyBook(bookID, func(b *database.Book) error { b.AuthorID = &authorID; return nil })
	require.NoError(t, err)
}

// TestFragmentFixer_NumberedResumeAfterRetitle: a numbered set cut off after
// its own retitle re-plans to the same fingerprint, stays applicable, and
// finishes; with and without renamed copies.
func TestFragmentFixer_NumberedResumeAfterRetitle(t *testing.T) {
	t.Parallel()
	t.Run("no copies, retitled before anything else", func(t *testing.T) {
		f := newFragFixture(t)
		f.numberedSeed(t, "lib/Serial Work", []string{"001 - Arrival", "002 - The Road", "003 - Gear", "004 - Ash",
			"005 - Night", "006 - Ember", "007 - Coda", "008 - Home"}, 300, nil)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Serial Work"), fragNumberedKey))
		require.True(t, r.Applicable(), r.SkipReason)
		plan := r.Detail.(*fragGroupPlan)
		require.NotEmpty(t, plan.Title)
		_, err := f.s.ModifyBook(plan.SurvivorID, func(b *database.Book) error { b.Title = plan.Title; return nil })
		require.NoError(t, err)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.Equal(t, r.Fingerprint, got.Fingerprint)
		require.True(t, got.Applicable(), "%s: %s", got.Skipped, got.SkipReason)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
	})

	for _, org := range fragOrgs(2) {
		t.Run("copies, cut off after every retire and the retitle, organized="+org, func(t *testing.T) {
			resumeAfterEveryRetire(t, org)
		})
		t.Run("copies, retitled before anything else, organized="+org, func(t *testing.T) {
			resumeRetitledFirst(t, org)
		})
	}
}

// organizeCopiesFixture marks books organized (and so electable) per mode:
// "none", "all" (originals and copies), or "copies" (copies only, so
// keptOfCopies keeps every copy).
func (f *fragFixture) organizeCopiesFixture(t *testing.T, mode string, orig, copies []string) {
	t.Helper()
	switch mode {
	case "all":
		for _, id := range append(append([]string(nil), orig...), copies...) {
			f.organized(t, id)
		}
	case "copies":
		for _, id := range copies {
			f.organized(t, id)
		}
	}
}

func resumeAfterEveryRetire(t *testing.T, org string) {
	{
		f := newFragFixture(t)
		const dir = "lib/Clarke/02_light_of_other_days"
		orig, copies := f.seedChapterCopies(t, dir, nil)
		f.organizeCopiesFixture(t, org, orig, copies)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.True(t, r.Applicable(), r.SkipReason)
		plan := r.Detail.(*fragGroupPlan)
		surv := plan.SurvivorID
		w := f.fragWriter(t, "op-cut")
		for _, m := range plan.Members {
			if m.Frag.Book.ID == surv {
				continue
			}
			require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, surv))
			_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, surv,
				&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
			require.NoError(t, err)
		}
		for _, cp := range plan.Copies {
			_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, cp.Frag.Book.ID, surv, &merge.SliceMapping{Mappable: true})
			require.NoError(t, err)
		}
		_, err := f.s.ModifyBook(surv, func(b *database.Book) error { b.Title = plan.Title; return nil })
		require.NoError(t, err)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.Equal(t, r.Fingerprint, got.Fingerprint)
		require.True(t, got.Applicable(), "%s: %s", got.Skipped, got.SkipReason)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
		b, err := f.s.GetBookByID(surv)
		require.NoError(t, err)
		require.Equal(t, plan.Title, b.Title)
	}
}

func resumeRetitledFirst(t *testing.T, org string) {
	{
		f := newFragFixture(t)
		const dir = "lib/Clarke/02_light_of_other_days"
		orig, copies := f.seedChapterCopies(t, dir, nil)
		f.organizeCopiesFixture(t, org, orig, copies)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.True(t, r.Applicable(), r.SkipReason)
		plan := r.Detail.(*fragGroupPlan)
		_, err := f.s.ModifyBook(plan.SurvivorID, func(b *database.Book) error { b.Title = plan.Title; return nil })
		require.NoError(t, err)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
		f.requireAllRetiredBut(t, r.BookIDs, plan.SurvivorID)
		_ = copies
	}
}

// requireAllRetiredBut checks every book of ids but survivor is retired into
// survivor, and that every book still holds a file row (copies keep theirs).
func (f *fragFixture) requireAllRetiredBut(t *testing.T, ids []string, survivor string) {
	t.Helper()
	for _, id := range ids {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		if id == survivor {
			require.False(t, b.IsSoftDeleted())
			continue
		}
		require.True(t, b.IsSoftDeleted(), "book %s retired", id)
		require.NotNil(t, b.MergedIntoBookID)
		require.Equal(t, survivor, *b.MergedIntoBookID)
	}
	rows, err := f.s.GetBookFiles(survivor)
	require.NoError(t, err)
	require.Len(t, rows, 8, "the survivor holds one file per chapter")
}

// TestFragmentFixer_BracketedAuthorFolder: an author with a bracketed role,
// in a folder named exactly for it, is a real author folder.
func TestFragmentFixer_BracketedAuthorFolder(t *testing.T) {
	t.Parallel()
	stems := []string{"001 - Arrival", "002 - The Road", "003 - Gear", "004 - Ash", "005 - Night", "006 - Ember", "007 - Coda", "008 - Home"}
	f := newFragFixture(t)
	a, err := f.s.CreateAuthor("Jane Author (Narrator)")
	require.NoError(t, err)
	f.numberedSeed(t, "lib/Jane Author (Narrator)", stems, 300, &a.ID)
	r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Jane Author (Narrator)"), fragNumberedKey))
	require.False(t, r.Applicable())
	require.Contains(t, r.SkipReason, "author folder")
}

func TestFragmentPersonShapedName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"Jane Author":                            true,
		"Jane Author (Narrator)":                 true,
		"Jane Author [Editor]":                   true,
		"G.S. Jennsen":                           true,
		"Jennsen, GS_ 08 Rubicon (Amaranthe 08)": false,
		"Jennsen, GS_ 08 Rubicon":                false,
		"(Narrator)":                             false,
		"Book 2 (Narrator)":                      false,
		"A (B) C":                                false,
		"Rubicon (Amaranthe Book Eight)":         false,
		"Light of Other Days (Unabridged)":       false,
		"Rubicon (Amaranthe 08)":                 false,
		"The Expanse [Book 3]":                   false,
		"Jane Author (ed.)":                      true,
		"Jane Author (Translator)":               true,
	} {
		require.Equal(t, want, personShapedName(name), name)
	}
}

// TestFragmentFixer_NumberedReviewProbes pins the review probes of PR #3700
// that need no code change, so their behaviour cannot drift silently.
func TestFragmentFixer_NumberedReviewProbes(t *testing.T) {
	t.Parallel()
	serial := []string{"001 - Arrival", "002 - The Road", "003 - Gear", "004 - Ash", "005 - Night", "006 - Ember", "007 - Coda", "008 - Home"}

	for _, org := range fragOrgs(1) {
		t.Run("cut off after members and two copies retired, organized="+org, func(t *testing.T) {
			partialCopiesResume(t, org)
		})
	}
	reviewProbesRest(t, serial)
}

func partialCopiesResume(t *testing.T, org string) {
	{
		f := newFragFixture(t)
		const dir = "lib/Clarke/02_light_of_other_days"
		orig, copies := f.seedChapterCopies(t, dir, nil)
		f.organizeCopiesFixture(t, org, orig, copies)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.True(t, r.Applicable(), r.SkipReason)
		plan := r.Detail.(*fragGroupPlan)
		surv := plan.SurvivorID
		w := f.fragWriter(t, "op-cut")
		for _, m := range plan.Members {
			if m.Frag.Book.ID == surv {
				continue
			}
			require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, surv))
			_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, surv,
				&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
			require.NoError(t, err)
		}
		for _, cp := range plan.Copies[:2] {
			_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, cp.Frag.Book.ID, surv, &merge.SliceMapping{Mappable: true})
			require.NoError(t, err)
		}
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.Equal(t, r.Fingerprint, got.Fingerprint, got.Reason)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
		f.requireAllRetiredBut(t, r.BookIDs, surv)
		_ = copies
	}
}

func reviewProbesRest(t *testing.T, serial []string) {
	t.Run("half the files by a real author, half by none: applicable (owner decision)", func(t *testing.T) {
		f := newFragFixture(t)
		a, err := f.s.CreateAuthor("Jane Author")
		require.NoError(t, err)
		ids := f.numberedSeed(t, "lib/Mixed", serial, 300, nil)
		for _, id := range ids[:4] {
			f.setAuthor(t, id, a.ID)
		}
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Mixed"), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})

	t.Run("a person's author folder is held", func(t *testing.T) {
		f := newFragFixture(t)
		a, err := f.s.CreateAuthor("Jane Author")
		require.NoError(t, err)
		f.numberedSeed(t, "lib/Jane Author", serial, 300, &a.ID)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Jane Author"), fragNumberedKey))
		require.False(t, r.Applicable())
		require.Contains(t, r.SkipReason, "author folder")
	})

	t.Run("junk folder-name author on 7 files, the real author on 1: applicable", func(t *testing.T) {
		f := newFragFixture(t)
		junk, err := f.s.CreateAuthor("Jennsen, GS_ 08 Rubicon (Amaranthe 08)")
		require.NoError(t, err)
		real, err := f.s.CreateAuthor("G.S. Jennsen")
		require.NoError(t, err)
		dir := "lib/Jennsen, GS_ 08 Rubicon (Amaranthe 08)"
		ids := f.numberedSeed(t, dir, serial, 300, &junk.ID)
		f.setAuthor(t, ids[7], real.ID)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})

	t.Run("book number, chapter title, n_of_m: held as the same chapter number", func(t *testing.T) {
		f := newFragFixture(t)
		var stems []string
		for i, name := range []string{"Arrival", "Road", "Gear", "Ash", "Night", "Ember", "Coda", "Home"} {
			stems = append(stems, fmt.Sprintf("02_%s_%03d_of_008", name, i+1))
		}
		ids := f.numberedSeed(t, "lib/OfT", stems, 300, nil)
		res := f.plan(t, "op-plan")
		require.Empty(t, applicableRowsWith(res, ids))
		r := findRow(t, res, noParentRowID(f.path("lib/OfT"), fragNumberedKey))
		require.Contains(t, r.SkipReason, "same chapter number")
	})
}

// TestFragmentFixer_NumberedResumeAfterFolder: a plain numbered set gets a
// book_path; a run cut off after the folder write (and the retitle) resumes.
func TestFragmentFixer_NumberedResumeAfterFolder(t *testing.T) {
	t.Parallel()
	serial := []string{"001 - Arrival", "002 - The Road", "003 - Gear", "004 - Ash", "005 - Night", "006 - Ember", "007 - Coda", "008 - Home"}
	for _, retitled := range []bool{false, true} {
		t.Run(fmt.Sprintf("retitled=%t", retitled), func(t *testing.T) {
			f := newFragFixture(t)
			f.numberedSeed(t, "lib/Serial Work", serial, 300, nil)
			r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Serial Work"), fragNumberedKey))
			require.True(t, r.Applicable(), r.SkipReason)
			plan := r.Detail.(*fragGroupPlan)
			require.NotEmpty(t, plan.Folder, "a plain numbered set gets a book_path")
			surv := plan.SurvivorID
			w := f.fragWriter(t, "op-cut")
			for _, m := range plan.Members {
				if m.Frag.Book.ID == surv {
					continue
				}
				require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, surv))
				_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, surv,
					&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
				require.NoError(t, err)
			}
			_, err := f.s.ModifyBook(surv, func(b *database.Book) error {
				b.FilePath = plan.Folder
				if retitled {
					b.Title = plan.Title
				}
				return nil
			})
			require.NoError(t, err)
			got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
			require.NoError(t, err)
			require.Equal(t, r.Fingerprint, got.Fingerprint, got.Reason)
			require.True(t, got.Applicable(), "%s: %s", got.Skipped, got.SkipReason)
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
			b, err := f.s.GetBookByID(surv)
			require.NoError(t, err)
			require.Equal(t, plan.Folder, b.FilePath)
			require.Equal(t, plan.Title, b.Title)
		})
	}
}

// TestFragmentFixer_DiscFolderCopies: a disc folder holding every track twice
// (once renamed, same size) never becomes two books of the same audio, even
// when organized copies make the kept files carry two keys.
func TestFragmentFixer_DiscFolderCopies(t *testing.T) {
	t.Parallel()
	for _, organizedCopies := range []int{0, 4} {
		t.Run(fmt.Sprintf("organized copies=%d", organizedCopies), func(t *testing.T) {
			discFolderCopies(t, organizedCopies)
		})
	}
}

func discFolderCopies(t *testing.T, organizedCopies int) {
	f := newFragFixture(t)
	const dir = "lib/Clarke/Light of Other Days/Disc 1"
	var ids []string
	for n := 1; n <= 8; n++ {
		for _, cp := range []bool{true, false} {
			stem := fmt.Sprintf("02_%03d", n)
			if cp {
				stem += " - 02_light_of_other_days - read by narrator"
			}
			size := 1000 + n
			p := f.file(t, dir+"/"+stem+".mp3", size)
			id := f.book(t, "d:"+stem, stem, p, nil)
			f.row(t, "dr:"+stem, id, p, stem+".mp3", int64(size), 300, 0)
			if cp && n <= organizedCopies {
				f.organized(t, id)
			}
			ids = append(ids, id)
		}
	}
	res := f.plan(t, "op-plan")
	for _, x := range res.Rows {
		t.Logf("ROW %s class=%s books=%d applicable=%v %s %s", x.RowID, x.Class, len(x.BookIDs), x.Applicable(), x.Skipped, x.SkipReason)
	}
	require.Empty(t, applicableRowsWith(res, ids), "never two books of one folder's originals and copies")
	held := 0
	for _, x := range res.Rows {
		if len(x.BookIDs) == 16 {
			held++
			require.Contains(t, x.SkipReason, "renamed copies")
		}
	}
	require.Equal(t, 1, held, "the folder is one held row")
}

// TestFragmentFixer_NumberedCopiesResumeMidMembers: a run cut off after only
// some member retires resumes when the books are organized and primary.
// retireInto demotes what it retires; without the planned flags Replan kept
// a different file per chapter and the row was stranded.
func TestFragmentFixer_NumberedCopiesResumeMidMembers(t *testing.T) {
	t.Parallel()
	for oi, org := range []string{"none", "all", "copies"} {
		for ci, cut := range []int{1, 4, 7} {
			if oi != ci && !fragSweepFull() {
				continue // sampled: the diagonal covers every org and every cut
			}
			t.Run(fmt.Sprintf("organized=%s cut=%d", org, cut), func(t *testing.T) {
				f := newFragFixture(t)
				const dir = "lib/Clarke/02_light_of_other_days"
				orig, copies := f.seedChapterCopies(t, dir, nil)
				f.organizeCopiesFixture(t, org, orig, copies)
				r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
				require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
				plan := r.Detail.(*fragGroupPlan)
				surv := plan.SurvivorID
				w := f.fragWriter(t, "op-cut")
				var done []fragGroupMember
				for _, m := range plan.Members {
					if m.Frag.Book.ID != surv && len(done) < cut {
						done = append(done, m)
					}
				}
				for _, m := range done {
					require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, surv))
				}
				for _, m := range done {
					_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, surv,
						&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
					require.NoError(t, err)
				}
				got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
				require.NoError(t, err)
				require.Equal(t, r.Fingerprint, got.Fingerprint, "cut after %d member retires must resume: %s", cut, got.Reason)
				require.True(t, got.Applicable(), "%s: %s", got.Skipped, got.SkipReason)
				out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
				require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
				f.requireAllRetiredBut(t, r.BookIDs, surv)
			})
		}
	}
}

// TestFragmentFixer_NumberedRetiredElsewhere: a member or copy that another
// fixer retired into an unrelated book after the plan makes the row changed;
// nothing is written and the book stays merged where it went.
func TestFragmentFixer_NumberedRetiredElsewhere(t *testing.T) {
	t.Parallel()
	for _, which := range []string{"copy", "member"} {
		t.Run(which, func(t *testing.T) {
			f := newFragFixture(t)
			const dir = "lib/Clarke/02_light_of_other_days"
			orig, copies := f.seedChapterCopies(t, dir, nil)
			r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
			require.True(t, r.Applicable())
			plan := r.Detail.(*fragGroupPlan)
			x := f.book(t, "X", "Other Book", f.file(t, "lib/Other/x.mp3", 5), nil)
			victim := copies[3]
			if which == "member" {
				for _, m := range plan.Members {
					if m.Frag.Book.ID != plan.SurvivorID {
						victim = m.Frag.Book.ID
						break
					}
				}
			}
			_, err := f.s.ModifyBook(victim, func(b *database.Book) error {
				tr, now := true, time.Now()
				b.MarkedForDeletion, b.MarkedForDeletionAt, b.MergedIntoBookID = &tr, &now, &x
				return nil
			})
			require.NoError(t, err)
			if which == "copy" {
				var cp fragGroupCopy
				for _, c := range plan.Copies {
					if c.Frag.Book.ID == victim {
						cp = c
					}
				}
				require.ErrorIs(t, copyRetireRefusal(f.s, cp, plan.SurvivorID, "copy"), repairs.ErrChangedSincePlan)
			}
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 0, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
			require.Zero(t, out.ByOutcome[repairs.OutcomePartial])
			b, err := f.s.GetBookByID(victim)
			require.NoError(t, err)
			require.Equal(t, x, *b.MergedIntoBookID)
			for _, id := range orig {
				if id == victim {
					continue
				}
				ob, err := f.s.GetBookByID(id)
				require.NoError(t, err)
				require.False(t, ob.IsSoftDeleted(), "member %s untouched", id)
			}
			rows, err := f.s.GetBookFiles(victim)
			require.NoError(t, err)
			require.Len(t, rows, 1, "the victim keeps its row")
		})
	}
}

// TestFragmentFixer_CopyHashesPairwise: three files at one position, the
// first with no hash and the other two with different hashes, are not one
// chapter: each pair must agree, not each file with the first.
func TestFragmentFixer_CopyHashesPairwise(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	const dir = "lib/Clarke/02_light_of_other_days"
	_, copies := f.seedChapterCopies(t, dir, nil)
	stem := "02_004 (1)"
	p := f.file(t, dir+"/"+stem+".mp3", 1004)
	third := f.book(t, "c:"+stem, stem, p, nil)
	f.row(t, "cr:"+stem, third, p, stem+".mp3", 1004, 300, 0)
	for id, h := range map[string]string{third: "hash-x", copies[3]: "hash-y"} {
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		rows[0].FileHash = h
		require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
	}
	r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
	require.False(t, r.Applicable(), "two different known hashes at one position must not both retire as copies")
}

// TestFragmentFixer_CopiesInAnotherGroup: renamed copies whose names put
// them in a different group than their originals (a key group beside a
// numbered set, or beside a "Chapter NNN" key group) hold both rows.
func TestFragmentFixer_CopiesInAnotherGroup(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"numbered originals", "no leading numbers"} {
		for _, organized := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s organized=%t", variant, organized), func(t *testing.T) {
				f := newFragFixture(t)
				const dir = "lib/Clarke/Light of Other Days"
				names := []string{"Arrival", "The Road", "Gear", "Ash", "Night", "Ember", "Coda", "Home"}
				var ids []string
				for i, nm := range names {
					n := i + 1
					orig := fmt.Sprintf("%03d - %s", n, nm)
					if variant == "no leading numbers" {
						orig = fmt.Sprintf("Chapter %03d", n)
					}
					for _, stem := range []string{orig, fmt.Sprintf("Light of Other Days - Part %03d", n)} {
						size := 5000 + n
						p := f.file(t, dir+"/"+stem+".mp3", size)
						id := f.book(t, "g:"+stem, stem, p, nil)
						f.row(t, "gr:"+stem, id, p, stem+".mp3", int64(size), 300, 0)
						if organized {
							f.organized(t, id)
						}
						ids = append(ids, id)
					}
				}
				res := f.plan(t, "op-plan")
				require.Empty(t, applicableRowsWith(res, ids), "never two books of the same audio")
				held := 0
				for _, x := range res.Rows {
					if x.Skipped == fragSkipSameAudioRows {
						held++
						require.Contains(t, x.SkipReason, "no-parent:")
					}
				}
				require.Equal(t, 2, held, "both rows are held, each naming the other")
			})
		}
	}
}

// p7Plan plans the 02_light_of_other_days copies folder and returns its row.
func (f *fragFixture) p7Plan(t *testing.T, dir string) (repairs.Row, *fragGroupPlan) {
	t.Helper()
	r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey))
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	return r, r.Detail.(*fragGroupPlan)
}

// requireResumes re-plans r (same fingerprint, applicable) and applies it.
func (f *fragFixture) requireResumes(t *testing.T, r repairs.Row, opID string) {
	t.Helper()
	got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
	require.NoError(t, err)
	require.Equal(t, r.Fingerprint, got.Fingerprint, "reason=%q", got.Reason)
	require.True(t, got.Applicable(), "%s: %s", got.Skipped, got.SkipReason)
	out := f.apply(t, "op-plan", opID, []string{r.RowID}, nil)
	require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
}

// TestFragmentFixer_NumberedPinnedResume: a re-plan keeps the plan's own
// survivor and kept files, whatever the apply did to the election flags.
func TestFragmentFixer_NumberedPinnedResume(t *testing.T) {
	t.Parallel()
	const dir = "lib/Clarke/02_light_of_other_days"

	t.Run("cut between a member's demote and its soft-delete", func(t *testing.T) {
		f := newFragFixture(t)
		orig, copies := f.seedChapterCopies(t, dir, nil)
		f.organizeCopiesFixture(t, "all", orig, copies)
		r, plan := f.p7Plan(t, dir)
		w := f.fragWriter(t, "op-cut")
		f.journalPlanRecord(t, w, r)
		var m fragGroupMember
		for _, x := range plan.Members {
			if x.Frag.Book.ID != plan.SurvivorID {
				m = x
				break
			}
		}
		require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
		// retireInto's step 3, journaled first as it journals it; the run is
		// cut off before the soft-delete.
		no := false
		require.NoError(t, w.Step(m.Frag.Book.ID, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false", func() error {
			_, err := w.Modify(m.Frag.Book.ID, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
			return err
		}))
		f.requireResumes(t, r, "op-apply")
		f.requireAllRetiredBut(t, r.BookIDs, plan.SurvivorID)
	})

	t.Run("a version-group hand-off crowns a live member with a lower id", func(t *testing.T) {
		f := newFragFixture(t)
		orig, _ := f.seedChapterCopies(t, dir, nil)
		for _, id := range orig {
			f.organized(t, id)
		}
		group := "vg-ch"
		yes, no := true, false
		_, err := f.s.ModifyBook(orig[0], func(b *database.Book) error { b.VersionGroupID = &group; b.IsPrimaryVersion = &no; return nil })
		require.NoError(t, err)
		_, err = f.s.ModifyBook(orig[2], func(b *database.Book) error { b.VersionGroupID = &group; b.IsPrimaryVersion = &yes; return nil })
		require.NoError(t, err)
		r, plan := f.p7Plan(t, dir)
		require.NotEqual(t, orig[0], plan.SurvivorID)
		w := f.fragWriter(t, "op-cut")
		f.journalPlanRecord(t, w, r)
		var m fragGroupMember
		for _, x := range plan.Members {
			if x.Frag.Book.ID == orig[2] {
				m = x
			}
		}
		require.NotNil(t, m.Frag)
		require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
		_, err = retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, plan.SurvivorID,
			&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
		require.NoError(t, err)
		b0, err := f.s.GetBookByID(orig[0])
		require.NoError(t, err)
		require.True(t, b0.IsPrimaryVersion == nil || *b0.IsPrimaryVersion, "the hand-off crowned orig[0]")
		f.requireResumes(t, r, "op-apply")
		f.requireAllRetiredBut(t, r.BookIDs, plan.SurvivorID)
	})

	t.Run("a row planned before roles were stored derives them from its members", func(t *testing.T) {
		f := newFragFixture(t)
		orig, copies := f.seedChapterCopies(t, dir, nil)
		f.organizeCopiesFixture(t, "all", orig, copies)
		r, plan := f.p7Plan(t, dir)
		var st fragGroupState
		require.NoError(t, json.Unmarshal(r.State, &st))
		st.Survivor, st.Roles = "", nil
		raw, err := json.Marshal(st)
		require.NoError(t, err)
		r.State = raw
		w := f.fragWriter(t, "op-cut")
		n := 0
		for _, m := range plan.Members {
			if m.Frag.Book.ID == plan.SurvivorID || n >= 3 {
				continue
			}
			n++
			require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
			_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, plan.SurvivorID,
				&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
			require.NoError(t, err)
		}
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.Equal(t, r.Fingerprint, got.Fingerprint, "reason=%q", got.Reason)
		w2 := f.fragWriter(t, "op-2")
		require.NoError(t, newFragmentFixer(f.p).Apply(context.Background(), w2, r))
		f.requireAllRetiredBut(t, r.BookIDs, plan.SurvivorID)
	})

	t.Run("a book merged into the survivor by someone else is a change", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		r, plan := f.p7Plan(t, dir)
		surv := plan.SurvivorID
		_, err := f.s.ModifyBook(copies[2], func(b *database.Book) error {
			tr, now := true, time.Now()
			b.MarkedForDeletion, b.MarkedForDeletionAt, b.MergedIntoBookID = &tr, &now, &surv
			return nil
		})
		require.NoError(t, err)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.NotEqual(t, r.Fingerprint, got.Fingerprint, "a changed row")
		require.Contains(t, got.Reason, "no journaled retire")
		// copyRetireRefusal trusts the locked Replan's attribution (it runs
		// right after it under the merge lock), so the refusal comes from
		// the engine's Replan: nothing is written.
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 0, out.Applied)
		require.Zero(t, out.ByOutcome[repairs.OutcomePartial])
	})

	t.Run("a book another fixer retired into the survivor is a change", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		r, plan := f.p7Plan(t, dir)
		f.applyOp("op-other", "some-other-fixer")
		w := repairs.NewWriter(f.s, f.s, "some-other-fixer", "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-other")
		_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, "some-other-fixer", copies[2], plan.SurvivorID, nil)
		require.NoError(t, err)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.NotEqual(t, r.Fingerprint, got.Fingerprint, "a changed row")
		require.Contains(t, got.Reason, "not by a "+fragFixerID+" apply")
	})
}

// errInjectedCut is the failure cutStore injects.
var errInjectedCut = errors.New("injected cut")

// cutStore wraps the fixture store for a Writer and fails the at-th event,
// counting every journal row and every book or book_file write in order:
// cutting at the journal row of a step stops before the step, cutting at
// its write leaves the journal row standing with nothing written (the two
// ways a run is cut off between steps). at <= 0 never cuts.
type cutStore struct {
	*database.PebbleStore
	at, n int
	hit   bool
	// history makes each history row an event too (a process that dies
	// after a write and before its history rows).
	history bool
}

func (c *cutStore) event() error {
	c.n++
	if c.at > 0 && c.n == c.at {
		c.hit = true
		return errInjectedCut
	}
	return nil
}

func (c *cutStore) CreateOperationChange(ch *database.OperationChange) error {
	if err := c.event(); err != nil {
		return err
	}
	return c.PebbleStore.CreateOperationChange(ch)
}

func (c *cutStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if err := c.event(); err != nil {
		return nil, err
	}
	return c.PebbleStore.ModifyBook(id, fn)
}

func (c *cutStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if err := c.event(); err != nil {
		return nil, err
	}
	return c.PebbleStore.ModifyBookFile(bookID, fileID, fn)
}

func (c *cutStore) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	if c.history {
		if err := c.event(); err != nil {
			return err
		}
	}
	return c.PebbleStore.RecordMetadataChange(r)
}

func (c *cutStore) MoveBookFilesToBook(fileIDs []string, source, target string) error {
	if err := c.event(); err != nil {
		return err
	}
	return c.PebbleStore.MoveBookFilesToBook(fileIDs, source, target)
}

// copiesFixtureState is the outcome of a numbered-copies apply, keyed by
// stem so two fixtures (different ids) compare.
func (f *fragFixture) copiesFixtureState(t *testing.T) map[string]string {
	t.Helper()
	stemOf := map[string]string{}
	for role, id := range f.ids {
		if stem, ok := strings.CutPrefix(role, "c:"); ok {
			stemOf[id] = stem
		}
	}
	out := map[string]string{}
	for id, stem := range stemOf {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		merged := ""
		if b.MergedIntoBookID != nil {
			merged = stemOf[*b.MergedIntoBookID]
		}
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		var rs []string
		for _, r := range rows {
			rs = append(rs, fmt.Sprintf("%s#%d", r.OriginalFilename, r.TrackNumber))
		}
		sort.Strings(rs)
		rel, _ := filepath.Rel(f.root, b.FilePath)
		if b.FilePath == "" {
			rel = ""
		}
		// The stored flag, not the effective one: a hand-off that crowns a
		// nil-flag member writes explicit true, and the end states must
		// agree on that too.
		out[stem] = fmt.Sprintf("deleted=%t merged=%q primary=%s title=%q path=%q rows=%v",
			b.IsSoftDeleted(), merged, storedPrimaryFlag(b.IsPrimaryVersion), b.Title, rel, rs)
	}
	return out
}

// newCutFixture seeds and plans the copies folder on an in-memory store
// (database.NewPebbleStoreInMemory: every Pebble write passes pebble.Sync,
// and on a real disk the ~130 events of each cut point cost seconds of
// fsync). The fixer's reads and writes are the same code either way.
//
// The returned close frees the store at once (a cut loop opens one per cut
// point, ~120 per shape; left to the subtest's cleanup they all stay open).
//
// vg links books in one version group: cutVGNone links none; cutVGOrig the
// eight originals with the fourth primary (the survivor, a renamed copy, is
// NOT in the group: its members' retires demote and hand off); cutVGSurvivor
// the originals AND the survivor, the survivor the group's one primary (the
// retired group members are not primary, so no demote and no hand-off: the
// hand-off is resumeHandOff's "the group has its one primary" check). With
// every book organized that linking leaves each copy primary beside a
// non-primary original, the plan keeps the copies as a block and is held
// (nothing to cut), so organized=all links the copies into the group too;
// linking them in the other two shapes holds the plan the same way.
//
// Two more shapes (organized=all only) put the survivor in the group with
// a nil flag and make a RETIRED member the group's explicit primary, so the
// retire's demote, hand-off and note run with the survivor's own flag in
// play: cutVGHandOffToSurvivor lets the hand-off crown the survivor (nil to
// explicit true); cutVGHandOffPast gives a renamed copy better metadata, so
// the hand-off crowns it and demotes the survivor, and that copy's own
// retire later hands the group back to the survivor.
func newCutFixture(t *testing.T, org, vg string) (*fragFixture, repairs.Row, func()) {
	t.Helper()
	const folder = "lib/Clarke/02_light_of_other_days"
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err, "open the in-memory store")
	var once sync.Once
	closeStore := func() { once.Do(func() { _ = s.Close() }) }
	t.Cleanup(closeStore)
	s.WaitForWarmup()
	f := &fragFixture{s: s, root: root, ids: map[string]string{}, rowIDs: map[string]string{}}
	f.ops = &planOps{rows: map[string]*database.OperationV2Row{}}
	f.p = &Plugin{deps: scanDeps{fakeDeps: fakeDeps{store: s, root: root}, scan: &scriptedScan{renewsLeft: -1}, ops: f.ops}, standDownWait: noWait}
	orig, copies := f.seedChapterCopies(t, folder, nil)
	f.organizeCopiesFixture(t, org, orig, copies)
	group := "vg-ch"
	setVG := func(id string, primary bool) {
		_, err := s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &group; b.IsPrimaryVersion = &primary; return nil })
		require.NoError(t, err)
	}
	switch vg {
	case cutVGNone:
	case cutVGOrig:
		for i, id := range orig {
			setVG(id, i == 3)
		}
	case cutVGSurvivor:
		_, plan := f.p7Plan(t, folder)
		survivor := plan.SurvivorID
		linked := append([]string(nil), orig...)
		if org == "all" {
			linked = append(linked, copies...)
		}
		for _, id := range linked {
			setVG(id, id == survivor)
		}
		setVG(survivor, true)
		r, plan := f.p7Plan(t, folder)
		require.Equal(t, survivor, plan.SurvivorID, "the group keeps the survivor")
		return f, r, closeStore
	case cutVGHandOffToSurvivor, cutVGHandOffPast:
		require.Equal(t, "all", org, "the hand-off shapes need eligible (organized) members")
		_, plan := f.p7Plan(t, folder)
		survivor := plan.SurvivorID
		// The retired primary R: a kept member after the survivor in chapter
		// order, so the survivor stays the lowest-id electable member. The
		// better book X (cutVGHandOffPast) is a renamed copy: every member's
		// row is on the survivor by the time R retires, so only the copies
		// (retired last, each still holding its own file) can outrank it.
		var later []string
		for _, m := range plan.Members {
			if m.Frag.Book.ID > survivor {
				later = append(later, m.Frag.Book.ID)
			}
		}
		require.NotEmpty(t, later)
		require.Len(t, plan.Copies, 8)
		retired, better := later[0], plan.Copies[3].Frag.Book.ID
		for _, id := range append(append([]string(nil), orig...), copies...) {
			setVG(id, id == retired)
		}
		_, err := s.ModifyBook(survivor, func(b *database.Book) error { b.VersionGroupID = &group; b.IsPrimaryVersion = nil; return nil })
		require.NoError(t, err)
		if vg == cutVGHandOffPast {
			pub, desc, narr := "Better Publisher", "A description", "A Narrator"
			_, err := s.ModifyBook(better, func(b *database.Book) error {
				b.Publisher, b.Description, b.Narrator = &pub, &desc, &narr
				return nil
			})
			require.NoError(t, err)
		}
		r, plan := f.p7Plan(t, folder)
		require.Equal(t, survivor, plan.SurvivorID, "the survivor is still elected")
		var st fragGroupState
		require.NoError(t, json.Unmarshal(r.State, &st))
		require.True(t, st.Flags[retired].Primary)
		return f, r, closeStore
	default:
		t.Fatalf("unknown version-group shape %q", vg)
	}
	r, _ := f.p7Plan(t, folder)
	return f, r, closeStore
}

// The version-group shapes of newCutFixture.
const (
	cutVGNone     = "none"
	cutVGOrig     = "originals"
	cutVGSurvivor = "with-survivor"
	// cutVGHandOffToSurvivor / cutVGHandOffPast: see newCutFixture.
	cutVGHandOffToSurvivor = "hand-off-to-survivor"
	cutVGHandOffPast       = "hand-off-past-survivor"
)

// TestFragmentFixer_NumberedCopiesCutAtEveryStep cuts a numbered set with
// renamed copies at EVERY write event of Apply (each journal row and each
// write: the plan record, moves, track numbers, each retire's demote,
// merged-into, path and soft-delete steps and its hand-off note, the copy
// retires, the folder and the title), then finishes it one of two ways:
//
//   - resume: the ORIGINAL plan is applied again. Its re-plan must keep the
//     fingerprint, and the end state must equal an uninterrupted run's.
//   - fresh: a NEW plan is made and every applicable row touching the set is
//     applied. The only such row must be the cut row itself (same row id,
//     same survivor: the fresh plan continues the run from its plan record),
//     and the end state must equal an uninterrupted run's: one live book
//     holds every file, no second live book. The original plan's resume
//     afterwards must find nothing to write (no journal row) and leave the
//     state as it is. Before 2026-10-03 the fresh plan re-formed the leftover
//     fragments around another survivor at 36 of 122 cut points (no version
//     group) and 10 of 122 (the prod shape), splitting the work in two.
//
// Shapes: organized none / all / copies, crossed with the version-group
// shapes of newCutFixture, plus the two hand-off shapes (organized=all).
//
// One event does not stop the run: the version-group hand-off's journal row
// (retireHandOff) is written AFTER versionprimary.EnsureSinglePrimary has
// written the crown straight to the store, and a failure to journal it is
// logged, not returned, by design. A cut there must still end in the same
// state. The crown write itself goes past the Writer and cannot be cut here.
//
// Cost: every cut point re-seeds and re-applies. The default run covers the
// prod shape (organized=all, the originals in a version group) in both
// modes and the survivor-in-group hand-off shape (cutVGHandOffPast) in fresh
// mode; without -short it adds no version group in fresh mode (probe F's
// other shape). Measured 2026-10-03 under -race: all/originals 145 s per
// mode, hand-off-past 97 s, none/none 111 s, so CI's -short -race run pays
// about 390 s here. AORG_FRAG_CUT_MATRIX=full runs every shape in both modes
// and also cuts at every history row (the crash-before-history window).
func TestFragmentFixer_NumberedCopiesCutAtEveryStep(t *testing.T) {
	t.Parallel()
	full := os.Getenv("AORG_FRAG_CUT_MATRIX") == "full"
	type shape struct{ org, vg, mode string }
	var shapes []shape
	if full {
		for _, mode := range []string{"resume", "fresh"} {
			for _, org := range []string{"none", "all", "copies"} {
				for _, vg := range []string{cutVGNone, cutVGOrig, cutVGSurvivor} {
					shapes = append(shapes, shape{org, vg, mode})
				}
			}
			shapes = append(shapes, shape{"all", cutVGHandOffToSurvivor, mode}, shape{"all", cutVGHandOffPast, mode})
		}
	} else {
		shapes = []shape{
			{"all", cutVGOrig, "resume"}, {"all", cutVGOrig, "fresh"},
			{"all", cutVGHandOffPast, "fresh"},
		}
		if !testing.Short() {
			shapes = append(shapes, shape{"none", cutVGNone, "fresh"})
		}
	}
	// Every cut point under AORG_FRAG_CUT_MATRIX=full; otherwise every 15th,
	// staggered per shape (GitHub's -race Go job has 10 minutes for the
	// whole package, and every point of these shapes took 377 s there).
	stride := fragSweepStride(1, 15)
	if full {
		stride = 1
	}
	for si, sh := range shapes {
		t.Run(fmt.Sprintf("organized=%s vg=%s %s", sh.org, sh.vg, sh.mode), func(t *testing.T) {
			t.Parallel()
			ref, rr, closeRef := newCutFixture(t, sh.org, sh.vg)
			out := ref.apply(t, "op-plan", "op-apply", []string{rr.RowID}, nil)
			require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
			want := ref.copiesFixtureState(t)
			closeRef()
			cuts, through := 0, 0
			for at := fragSweepStart(si, stride); ; at += stride {
				f, r, closeCut := newCutFixture(t, sh.org, sh.vg)
				cs := &cutStore{PebbleStore: f.s, at: at, history: full}
				f.applyOp("op-cut", fragFixerID)
				w := repairs.NewWriter(cs, cs, fragFixerID, "bulk_update", "repairs-").WithJournal(cs, cs, "op-cut")
				err := newFragmentFixer(f.p).Apply(context.Background(), w, r)
				if !cs.hit {
					require.NoError(t, err, "event %d never reached", at)
					closeCut()
					break
				}
				if err == nil {
					// A best-effort write (the hand-off note, a history
					// row): the run went on.
					through++
					require.Equal(t, want, f.copiesFixtureState(t), "cut at event %d (not stopping): end state", at)
					closeCut()
					continue
				}
				cuts++
				if sh.mode == "fresh" {
					f.finishByFreshPlan(t, r, at, want)
				} else {
					got, rerr := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
					require.NoError(t, rerr, "cut at event %d: re-plan", at)
					require.Equal(t, r.Fingerprint, got.Fingerprint, "cut at event %d: re-plan reason %q", at, got.Reason)
					res := f.apply(t, "op-plan", "op-resume", []string{r.RowID}, nil)
					require.Equal(t, 1, res.Applied, "cut at event %d: outcomes %v %+v", at, res.ByOutcome, res.Rows)
					require.Equal(t, want, f.copiesFixtureState(t), "cut at event %d: end state", at)
				}
				closeCut()
			}
			t.Logf("organized=%s vg=%s %s: %d cut points finished to the same end state; %d best-effort events ran through", sh.org, sh.vg, sh.mode, cuts, through)
			require.Greater(t, cuts, 40/stride)
		})
	}
}

// TestFragmentFixer_HandOffShapesReachTheSurvivor pins what the two hand-off
// shapes of newCutFixture exercise: the first hand-off crowns the survivor
// (nil to explicit true), or crowns another member and so demotes the
// survivor, whose flag a later hand-off raises again. Either way the
// survivor's own stored flag changes during the run.
func TestFragmentFixer_HandOffShapesReachTheSurvivor(t *testing.T) {
	t.Parallel()
	for _, vg := range []string{cutVGHandOffToSurvivor, cutVGHandOffPast} {
		t.Run(vg, func(t *testing.T) {
			f, r, closeF := newCutFixture(t, "all", vg)
			defer closeF()
			survivor := r.Proposed["survivor"]
			out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
			require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
			changes, err := f.s.GetOperationChanges("op-apply")
			require.NoError(t, err)
			sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
			var crowned []string
			for _, c := range changes {
				if id, ok := undo.HandOffCrowned(c); ok && c.ChangeType == undo.ChangeTypeBookPrimaryHandoff {
					crowned = append(crowned, id)
				}
			}
			require.NotEmpty(t, crowned, "the run hands the group off")
			if vg == cutVGHandOffToSurvivor {
				require.Equal(t, survivor, crowned[0], "the first hand-off crowns the survivor")
			} else {
				require.NotEqual(t, survivor, crowned[0], "the first hand-off crowns another member, demoting the survivor")
				require.Contains(t, crowned, survivor, "a later hand-off crowns the survivor again")
			}
			b, err := f.s.GetBookByID(survivor)
			require.NoError(t, err)
			require.Equal(t, "true", storedPrimaryFlag(b.IsPrimaryVersion), "the survivor ends the explicit primary")
		})
	}
}

// journalRows counts every row of the operation journal.
func (f *fragFixture) journalRows(t *testing.T) int {
	t.Helper()
	n := 0
	require.NoError(t, f.s.ScanOperationChanges(func(*database.OperationChange) error { n++; return nil }))
	return n
}

// finishByFreshPlan is the cut test's fresh mode (see
// TestFragmentFixer_NumberedCopiesCutAtEveryStep).
func (f *fragFixture) finishByFreshPlan(t *testing.T, r repairs.Row, at int, want map[string]string) {
	t.Helper()
	res := f.plan(t, "op-plan2")
	rows := applicableRowsWith(res, r.BookIDs)
	require.Len(t, rows, 1, "cut at event %d: one applicable row over the set", at)
	require.Equal(t, r.RowID, rows[0].RowID, "cut at event %d: the fresh plan continues the cut row", at)
	require.Equal(t, r.Proposed["survivor"], rows[0].Proposed["survivor"], "cut at event %d: same survivor", at)
	out := f.apply(t, "op-plan2", "op-fresh", []string{rows[0].RowID}, nil)
	require.Equal(t, 1, out.Applied, "cut at event %d: fresh apply outcomes %v %+v", at, out.ByOutcome, out.Rows)
	require.Equal(t, want, f.copiesFixtureState(t), "cut at event %d: end state after the fresh plan", at)
	before := f.journalRows(t)
	orig := f.apply(t, "op-plan", "op-resume", []string{r.RowID}, nil)
	require.Equal(t, 1, orig.Applied+orig.ChangedSincePlan, "cut at event %d: original resume outcomes %v %+v", at, orig.ByOutcome, orig.Rows)
	require.Equal(t, before, f.journalRows(t), "cut at event %d: the original plan's resume writes nothing", at)
	require.Equal(t, want, f.copiesFixtureState(t), "cut at event %d: end state after the original resume", at)
}

// TestFragmentFixer_NumberedLiveFlagChecks: the pin does not let a re-plan
// ignore a live book's election flags. One changed since the plan by
// anything but this row's own apply is a change.
func TestFragmentFixer_NumberedLiveFlagChecks(t *testing.T) {
	t.Parallel()
	const dir = "lib/Clarke/02_light_of_other_days"

	t.Run("a member organized in place after the plan (fallback survivor)", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.looseGroup(t, "lib/NoneOrg", "Chap", 3, func(int) bool { return false })
		r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
		require.True(t, r.Applicable(), r.SkipReason)
		sorted := append([]string(nil), ids...)
		sort.Strings(sorted)
		require.Equal(t, sorted[0], r.Proposed["survivor"])
		f.organized(t, sorted[2])
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 0, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
		b, err := f.s.GetBookByID(sorted[2])
		require.NoError(t, err)
		require.False(t, b.IsSoftDeleted(), "the organized member is never retired into an unorganized survivor")
	})

	t.Run("a copy demoted by someone else", func(t *testing.T) {
		f := newFragFixture(t)
		_, copies := f.seedChapterCopies(t, dir, nil)
		r, _ := f.p7Plan(t, dir)
		no := false
		_, err := f.s.ModifyBook(copies[1], func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
		require.NoError(t, err)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.NotEqual(t, r.Fingerprint, got.Fingerprint)
		require.Contains(t, got.Reason, "did not change it")
	})

	t.Run("a held no-survivor row re-evaluates to its own skip reason", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.looseGroup(t, "lib/NoSurv", "Chap", 3, func(int) bool { return false })
		for _, id := range ids {
			no := false
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
			require.NoError(t, err)
		}
		r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
		require.Equal(t, fragSkipNoSurvivor, r.Skipped)
		got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.Equal(t, r.Fingerprint, got.Fingerprint, got.Reason)
		require.Equal(t, fragSkipNoSurvivor, got.Skipped)
	})
}

// TestFragmentFixer_CrashBeforeHistoryResumes: the process dies after the
// soft-delete commits and before the Writer's history rows land (no
// apply_incomplete marker either). The journal row, written first, still
// attributes the retire to this fixer and the row resumes.
func TestFragmentFixer_CrashBeforeHistoryResumes(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	f.seed(t)
	res := f.plan(t, "op-plan")
	id := noParentRowID(f.path("lib/Loose"), "loose")
	planned := findRow(t, res, id)
	survivor := planned.Proposed["survivor"]
	var others, otherRows []string
	for _, n := range []string{"01", "02", "03"} {
		if f.ids["loose"+n] != survivor {
			others = append(others, f.ids["loose"+n])
			otherRows = append(otherRows, f.rowIDs["l"+n])
		}
	}
	f.applyOp("op-apply", fragFixerID)
	w := repairs.NewWriter(f.s, dropHistory{}, fragFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-apply")
	for i := range others {
		require.NoError(t, w.MoveBookFiles([]string{otherRows[i]}, others[i], survivor))
	}
	_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, others[0], survivor, &merge.SliceMapping{Mappable: true})
	require.NoError(t, err)
	got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	require.Equal(t, planned.Fingerprint, got.Fingerprint, got.Reason)
	out := f.apply(t, "op-plan", "op-resume", []string{id}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
}

// dropHistory loses every history row, as a process that died after the
// write and before its history would.
type dropHistory struct{}

func (dropHistory) RecordMetadataChange(*database.MetadataChangeRecord) error { return nil }

// TestFragmentFixer_ReplanJournalCost measures a resumed re-plan's journal
// read: a 346-fragment row cut off after 300 retires, on a store holding
// 300,000 other journal rows. Skipped unless AORG_FRAG_REPLAN_BENCH=1 (it
// seeds for about a minute); the numbers go in the log.
func TestFragmentFixer_ReplanJournalCost(t *testing.T) {
	t.Parallel()
	if os.Getenv("AORG_FRAG_REPLAN_BENCH") != "1" {
		t.Skip("set AORG_FRAG_REPLAN_BENCH=1 to measure")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	f := &fragFixture{s: s, root: root, ids: map[string]string{}, rowIDs: map[string]string{}}
	f.ops = &planOps{rows: map[string]*database.OperationV2Row{}}
	f.p = &Plugin{deps: scanDeps{fakeDeps: fakeDeps{store: s, root: root}, scan: &scriptedScan{renewsLeft: -1}, ops: f.ops}, standDownWait: noWait}
	const n, cut, noise = 346, 300, 300000
	var ids []string
	for i := 1; i <= n; i++ {
		stem := fmt.Sprintf("Chap %03d", i)
		p := f.file(t, filepath.Join("lib/Big", stem+".mp3"), 800+i)
		id := f.book(t, stem, stem, p, nil)
		f.row(t, stem, id, p, stem+".mp3", int64(800+i), 300, 0)
		ids = append(ids, id)
	}
	r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
	require.True(t, r.Applicable(), r.SkipReason)
	plan := r.Detail.(*fragGroupPlan)

	start := time.Now()
	got, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
	require.NoError(t, err)
	require.Equal(t, r.Fingerprint, got.Fingerprint, got.Reason)
	fresh := time.Since(start)

	w := f.fragWriter(t, "op-cut")
	done := 0
	for _, m := range plan.Members {
		if m.Frag.Book.ID == plan.SurvivorID || done >= cut {
			continue
		}
		done++
		require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
		_, err := retireInto(context.Background(), f.p, s, w, time.Now, fragFixerID, m.Frag.Book.ID, plan.SurvivorID,
			&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
		require.NoError(t, err)
	}
	for i := 0; i < noise; i++ {
		require.NoError(t, s.CreateOperationChange(&database.OperationChange{
			ID: fmt.Sprintf("n%09d", i), OperationID: fmt.Sprintf("op-noise-%d", i%50), BookID: fmt.Sprintf("noise-%d", i%5000),
			ChangeType: "metadata_update", FieldName: "title", OldValue: "a", NewValue: "b",
		}))
	}
	start = time.Now()
	got, err = newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
	require.NoError(t, err)
	resumed := time.Since(start)
	require.Equal(t, r.Fingerprint, got.Fingerprint, got.Reason)
	start = time.Now()
	_, err = s.GetBookChanges(plan.SurvivorID)
	require.NoError(t, err)
	one := time.Since(start)
	start = time.Now()
	rows := 0
	require.NoError(t, s.ScanOperationChanges(func(*database.OperationChange) error { rows++; return nil }))
	scan := time.Since(start)
	// Now trust the opchange_by_book index (startup does this on prod) and
	// measure the indexed paths: the same re-plan (one GetBookChanges per
	// judged book), one GetBookChanges, and the per-book cost Plan's
	// whole-journal read would pay for a 5000-book library instead of its
	// one pass.
	_, err = s.RebuildOpChangeByBookIndex(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, s.OpChangeByBookIndexUsable())
	start = time.Now()
	got, err = newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
	require.NoError(t, err)
	indexedReplan := time.Since(start)
	require.Equal(t, r.Fingerprint, got.Fingerprint, got.Reason)
	start = time.Now()
	_, err = s.GetBookChanges(plan.SurvivorID)
	require.NoError(t, err)
	oneIndexed := time.Since(start)
	start = time.Now()
	for i := 0; i < 5000; i++ {
		_, err := s.GetBookChanges(fmt.Sprintf("noise-%d", i))
		require.NoError(t, err)
	}
	perBook5000 := time.Since(start)
	t.Logf("346-fragment row, %d retires, %d other journal rows. Unindexed: re-plan %v (one pass), one GetBookChanges %v, one full scan of %d rows %v. Indexed: re-plan %v, one GetBookChanges %v, GetBookChanges x5000 books %v",
		cut, noise, resumed, one, rows, scan, indexedReplan, oneIndexed, perBook5000)
	_ = fresh
}

// TestFragTitleKey: the titles that name one work, and the ones that must
// stay apart (another name, or the same name at another position or volume).
func TestFragTitleKey(t *testing.T) {
	t.Parallel()
	id := fragTitleIdentity
	for _, tc := range [][2]string{
		{"Book 2 - Eldest", "Eldest"},
		{"03 - Horizon Storms", "Horizon Storms"},
		{"Inheritance Cycle 02 - Eldest", "Book 2 - Eldest"},
		{"Eldest (Unabridged)", "Eldest"},
		{"Saga, Vol 3", "Saga Volume 3"},
		{"Harry Potter, Book 1", "Harry Potter Bk. 01"},
	} {
		require.NotEmpty(t, id(tc[0]).key, tc[0])
		require.True(t, id(tc[0]).sameWork(id(tc[1])), "%q and %q are one work", tc[0], tc[1])
	}
	for _, tc := range [][2]string{
		{"Dragon Born 3", "Dragon Born"},
		{"Dragon Born, Book 3", "Dragon Born"},
		{"Harry Potter, Book 1", "Harry Potter Book 7"},
		{"Saga, Vol 3", "Saga, Vol 1"},
		{"The Expanse Volume 1", "The Expanse Volume 2"},
		{"Book 2 - Eldest", "Book 3 - Eldest"},
		{"Foundation 6 - Foundation's Edge", "01 - Foundation's Edge"},
		{"5 - Genius Camp: The Smartest Kid in the Universe, Book 2", "2 - Genius Camp: The Smartest Kid in the Universe, Book 2"},
		{"Prelude to Foundation", "Foundation"},
		{"Doctor Who - Loose", "Loose"},
	} {
		require.False(t, id(tc[0]).sameWork(id(tc[1])), "%q and %q are different works", tc[0], tc[1])
	}
	for _, title := range []string{"", "c5", "ab", "Part 3", "Chapter 12", "01", "New Folder"} {
		require.Empty(t, id(title).key, "%q names no work", title)
	}
	rel := func(a, b string) int { return id(a).relate(id(b)) }
	for _, tc := range [][2]string{
		{"Eldest, Book 2", "Book 2 - Eldest"},
		{"Eldest (Book 2)", "Eldest, Book 2"},
		{"Eldest: Inheritance, Book 2", "Eldest (Book 2)"},
		{"Book 2 - Eldest", "Eldest"},
	} {
		require.Equal(t, fragTitleSame, rel(tc[0], tc[1]), "%q / %q", tc[0], tc[1])
		require.Equal(t, fragTitleSame, rel(tc[1], tc[0]), "%q / %q", tc[1], tc[0])
	}
	for _, tc := range [][2]string{
		{"Eldest, Book 2", "Eldest"},
		{"Eldest (Book 2)", "Eldest"},
		{"Eldest: Inheritance, Book 2", "Eldest"},
		{"Dragon Born, Book 3", "Dragon Born"},
	} {
		require.Equal(t, fragTitleUncertain, rel(tc[0], tc[1]), "%q / %q: a trailing position on one side only", tc[0], tc[1])
		require.Equal(t, fragTitleUncertain, rel(tc[1], tc[0]), "%q / %q", tc[1], tc[0])
	}
	for _, tc := range [][2]string{
		{"Saga, Vol 3", "Saga, Vol 1"},
		{"Harry Potter, Book 1", "Harry Potter Book 7"},
		{"The Expanse Volume 1", "The Expanse Volume 2"},
		{"Book 2 - Eldest", "Book 3 - Eldest"},
		{"Eldest, Book 2", "Book 3 - Eldest"},
	} {
		require.Equal(t, fragTitleOther, rel(tc[0], tc[1]), "%q / %q", tc[0], tc[1])
		require.Equal(t, fragTitleOther, rel(tc[1], tc[0]), "%q / %q", tc[1], tc[0])
	}
}

// existingBook creates a live, organized multi-file book of n files of dur
// seconds each under dir.
func (f *fragFixture) existingBook(t *testing.T, role, title, dir string, n, dur int) string {
	t.Helper()
	id := f.book(t, role, title, f.path(dir), nil)
	f.setAuthor(t, id, f.authorID(t, "Christopher Paolini"))
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("track %03d.mp3", i)
		// Sizes step by 37 bytes: no constant difference from any fragment,
		// so the re-tag rule never reads these as copies.
		p := f.file(t, filepath.Join(dir, name), 5000+37*i)
		f.row(t, fmt.Sprintf("%s%d", role, i), id, p, name, int64(5000+37*i), dur, i)
	}
	f.organized(t, id)
	return id
}

// chapterFragsAt is chapterFrags with the files' sizes at base+37*i, so they
// differ from chapterFrags' (7000+i) by no constant: never re-tagged copies.
func (f *fragFixture) chapterFragsAt(t *testing.T, dir, name string, n, dur, base int) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= n; i++ {
		stem := fmt.Sprintf("%s %02d", name, i)
		p := f.file(t, filepath.Join(dir, stem+".mp3"), base+37*i)
		id := f.book(t, dir+stem, stem, p, nil)
		f.row(t, dir+stem, id, p, stem+".mp3", int64(base+37*i), dur, 0)
		f.organized(t, id)
		ids = append(ids, id)
	}
	return ids
}

// chapterFrags imports n chapter files "<name> 0i" from dir, each its own
// organized book of dur seconds.
func (f *fragFixture) chapterFrags(t *testing.T, dir, name string, n, dur int) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= n; i++ {
		stem := fmt.Sprintf("%s %02d", name, i)
		p := f.file(t, filepath.Join(dir, stem+".mp3"), 7000+i)
		id := f.book(t, dir+stem, stem, p, nil)
		f.row(t, dir+stem, id, p, stem+".mp3", int64(7000+i), dur, 0)
		f.organized(t, id)
		ids = append(ids, id)
	}
	return ids
}

func (f *fragFixture) requireRetiredInto(t *testing.T, ids []string, target string) {
	t.Helper()
	for _, id := range ids {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		require.True(t, b.IsSoftDeleted(), "fragment %s retired", id)
		require.NotNil(t, b.MergedIntoBookID)
		require.Equal(t, target, *b.MergedIntoBookID)
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, rows, 1, "a joined fragment keeps its own file row")
	}
}

// TestFragmentFixer_ExistingBook: a no-parent group whose work is already a
// live book (the "Book 2 - Eldest" beside "Eldest" shape, 13 of 19 rows
// applied on prod 2026-10-05) is never assembled into a second book.
func TestFragmentFixer_ExistingBook(t *testing.T) {
	t.Parallel()
	const fragDir = "lib/Christopher Paolini/Book 2 - Eldest"
	t.Run("totals agree: the fragments join the existing book", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "eldest", "Eldest", "lib/Other Author/Eldest", 4, 900)
		frags := f.chapterFrags(t, fragDir, "Eldest", 6, 600)
		res := f.plan(t, "op-plan")
		noRow(t, res, noParentRowID(f.path(fragDir), "eldest"), "never assembled")
		r := findRow(t, res, existingRowID(f.path(fragDir), "eldest"))
		require.Equal(t, fragClassExistingBook, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, repairs.RiskReview, r.Risk)
		require.ElementsMatch(t, append(append([]string(nil), frags...), existing), r.BookIDs)
		require.Equal(t, existing, r.Proposed["join"])
		require.Empty(t, r.Proposed["title"], "nothing is retitled")
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		f.requireRetiredInto(t, frags, existing)
		rows, err := f.s.GetBookFiles(existing)
		require.NoError(t, err)
		require.Len(t, rows, 4, "the existing book's files are untouched")
		b, err := f.s.GetBookByID(existing)
		require.NoError(t, err)
		require.Equal(t, "Eldest", b.Title)
	})
	t.Run("totals disagree: held", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "eldest", "Eldest", "lib/Other Author/Eldest", 4, 1200)
		frags := f.chapterFrags(t, fragDir, "Eldest", 6, 600)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(fragDir), "eldest"))
		require.Equal(t, fragClassExistingBook, r.Class)
		require.Equal(t, fragSkipExistingBook, r.Skipped)
		require.Contains(t, r.SkipReason, existing)
		require.ElementsMatch(t, frags, r.BookIDs, "the existing book is listed, never written")
		require.Zero(t, res.Applicable)
	})
	t.Run("a short single-file book of the title is no existing book", func(t *testing.T) {
		f := newFragFixture(t)
		p := f.file(t, "lib/Other/Eldest.mp3", 4999)
		single := f.book(t, "single", "Eldest", p, nil)
		f.row(t, "s", single, p, "Eldest.mp3", 4999, 600, 0)
		f.chapterFrags(t, fragDir, "Eldest", 6, 600)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(fragDir), "eldest"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})
	t.Run("a whole-book single file of the title is", func(t *testing.T) {
		f := newFragFixture(t)
		p := f.file(t, "lib/Other/Eldest.m4b", 4999)
		single := f.book(t, "single", "Eldest", p, nil)
		f.setAuthor(t, single, f.authorID(t, "Christopher Paolini"))
		f.row(t, "s", single, p, "Eldest.m4b", 3_500_000, 3500, 0)
		f.chapterFrags(t, fragDir, "Eldest", 6, 600)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(fragDir), "eldest"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, single, r.Proposed["join"])
	})
	t.Run("a cut-off join resumes, from the plan and from a fresh plan", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "eldest", "Eldest", "lib/Other Author/Eldest", 4, 900)
		frags := f.chapterFrags(t, fragDir, "Eldest", 6, 600)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(fragDir), "eldest"))
		cut := func(id string) {
			w := f.fragWriter(t, "op-cut-"+id)
			_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, id, existing,
				&merge.SliceMapping{Mappable: true})
			require.NoError(t, err)
		}
		cut(frags[0])
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "the planned row resumes past the retired fragment: %+v", out.Rows)
		f.requireRetiredInto(t, frags, existing)

		g := newFragFixture(t)
		existing = g.existingBook(t, "eldest", "Eldest", "lib/Other Author/Eldest", 4, 900)
		frags = g.chapterFrags(t, fragDir, "Eldest", 6, 600)
		w := g.fragWriter(t, "op-cut")
		for _, id := range frags[:2] {
			_, err := retireInto(context.Background(), g.p, g.s, w, time.Now, fragFixerID, id, existing,
				&merge.SliceMapping{Mappable: true})
			require.NoError(t, err)
		}
		res := g.plan(t, "op-plan")
		r = findRow(t, res, existingRowID(g.path(fragDir), "eldest"))
		require.True(t, r.Applicable(), "the rest still joins, credited with the retired ones: %s: %s", r.Skipped, r.SkipReason)
		require.Contains(t, strings.Join(r.Evidence, "\n"), "2 fragment(s) of these folders")
		noRow(t, res, noParentRowID(g.path(fragDir), "eldest"), "never assembled after a cut")
		out = g.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		g.requireRetiredInto(t, frags, existing)
	})
}

// TestFragmentFixer_ExistingBookIdentity: the review probes of #3747. A
// group joins or is held only by a book of the same work: never another
// volume or position, never another author's book of the same name; and a
// group whose work has no derivable title is held, never assembled blind.
func TestFragmentFixer_ExistingBookIdentity(t *testing.T) {
	t.Parallel()
	t.Run("another volume is another work", func(t *testing.T) {
		f := newFragFixture(t)
		f.existingBook(t, "v1", "Saga, Vol 1", "lib/Writer/Saga, Vol 1", 4, 900)
		dir := "lib/Writer/Saga, Vol 3"
		f.chapterFrags(t, dir, "Saga Vol 3 Part", 6, 600)
		res := f.plan(t, "op-plan")
		for _, r := range res.Rows {
			require.NotEqual(t, fragClassExistingBook, r.Class, "%s: %s", r.RowID, r.SkipReason)
		}
	})
	t.Run("another position is another work", func(t *testing.T) {
		f := newFragFixture(t)
		f.existingBook(t, "b3", "Book 3 - Eldest", "lib/Other/Book 3 - Eldest", 4, 900)
		f.chapterFrags(t, "lib/Christopher Paolini/Book 2 - Eldest", "Eldest", 6, 600)
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path("lib/Christopher Paolini/Book 2 - Eldest"), "eldest"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})
	t.Run("same title by another author: held, both authors shown", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "dan", "Origin", "lib/Dan Brown/Origin", 4, 900)
		f.setAuthor(t, existing, f.authorID(t, "Dan Brown"))
		dir := "lib/Jessica Khoury/Origin"
		frags := f.chapterFrags(t, dir, "Origin", 6, 600)
		khoury := f.authorID(t, "Jessica Khoury")
		for _, id := range frags {
			f.setAuthor(t, id, khoury)
		}
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(dir), "origin"))
		require.Equal(t, fragSkipExistingBook, r.Skipped)
		require.Contains(t, r.SkipReason, "Dan Brown")
		require.Contains(t, r.SkipReason, "Jessica Khoury")
		require.Zero(t, res.Applicable)
	})
	t.Run("a folder the walk refuses still names the work", func(t *testing.T) {
		// "import/Eldest": the folder walk offers no title for a folder under
		// an import root; its own name, the chapter key and the members'
		// titles still find the live "Eldest".
		f := newFragFixture(t)
		existing := f.existingBook(t, "eldest", "Eldest", "lib/Other Author/Eldest", 4, 900)
		dir := "import/Eldest"
		f.chapterFrags(t, dir, "Eldest", 6, 600)
		res := f.plan(t, "op-plan")
		noRow(t, res, noParentRowID(f.path(dir), "eldest"), "never a plain assemble beside a live Eldest")
		r := findRow(t, res, existingRowID(f.path(dir), "eldest"))
		require.Equal(t, existing, r.Proposed["join"], "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, "Eldest", r.Current["source_folder"])
		require.NotEmpty(t, r.Current["source_title"])
	})
	t.Run("no derivable title: held, never assembled", func(t *testing.T) {
		f := newFragFixture(t)
		dir := "lib/Downloads"
		var ids []string
		for i := 1; i <= 3; i++ {
			stem := fmt.Sprintf("%02d", i)
			p := f.file(t, filepath.Join(dir, stem+".mp3"), 7000+i)
			id := f.book(t, dir+stem, stem, p, nil)
			f.row(t, dir+stem, id, p, stem+".mp3", int64(7000+i), 600, 0)
			f.organized(t, id)
			ids = append(ids, id)
		}
		res := f.plan(t, "op-plan")
		r := rowWithBooks(t, res, ids)
		require.Equal(t, fragSkipNoTitleKey, r.Skipped, "%s %s: %s", r.RowID, r.Class, r.SkipReason)
		require.Zero(t, res.Applicable)
	})
}

// TestFragmentFixer_TrailingPositionProbes: the second review's probes. A
// live title carrying a trailing position the group's lacks, or the reverse,
// is found and held (never a plain assemble, never a join), even with
// agreeing totals and the same author.
func TestFragmentFixer_TrailingPositionProbes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, live, dir string }{
		{"live (Book 2), bare folder", "Eldest (Book 2)", "lib/Christopher Paolini/Eldest"},
		{"live bare, folder (Book 2)", "Eldest", "lib/Christopher Paolini/Eldest (Book 2)"},
		{"live , Book 2, import folder", "Eldest, Book 2", "import/Eldest"},
		{"live bare, folder , Book 2", "Eldest", "lib/Christopher Paolini/Eldest, Book 2"},
		{"live series subtitle, bare folder", "Eldest: Inheritance, Book 2", "lib/Christopher Paolini/Eldest"},
		{"live bare, series subtitle folder", "Eldest", "lib/Christopher Paolini/Eldest - Inheritance, Book 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFragFixture(t)
			existing := f.existingBook(t, "live", tc.live, "lib/Shelf/Live", 3, 600)
			f.chapterFrags(t, tc.dir, "Eldest", 3, 600)
			res := f.plan(t, "op-plan")
			require.Zero(t, res.Applicable, "nothing assembles or joins beside the live book")
			found := false
			for _, r := range res.Rows {
				if r.Class == fragClassExistingBook {
					found = true
					require.Equal(t, fragSkipExistingBook, r.Skipped, r.SkipReason)
					require.Contains(t, r.SkipReason, existing)
					require.Contains(t, r.SkipReason, "series position on one side only")
				}
			}
			require.True(t, found, "the live book is found: %+v", res.Rows)
		})
	}
	t.Run("same position on both sides joins", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "live", "Eldest, Book 2", "lib/Shelf/Live", 3, 600)
		dir := "lib/Christopher Paolini/Book 2 - Eldest"
		f.chapterFrags(t, dir, "Eldest", 3, 600)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(dir), "eldest"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, existing, r.Proposed["join"])
	})
}

// TestFragmentFixer_FolderAuthor: untagged fragments take their author from
// the author folder above the work folder, so they never join another
// author's book of the same name, nor a book with no author.
func TestFragmentFixer_FolderAuthor(t *testing.T) {
	t.Parallel()
	const dir = "lib/Jessica Khoury/Origin"
	t.Run("another author's book of the name: held", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "dan", "Origin", "lib/Dan Brown/Origin", 3, 600)
		f.setAuthor(t, existing, f.authorID(t, "Dan Brown"))
		f.chapterFrags(t, dir, "Origin", 3, 600)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(dir), "origin"))
		require.Equal(t, fragSkipExistingBook, r.Skipped)
		require.Contains(t, r.SkipReason, "Jessica Khoury")
		require.Contains(t, r.SkipReason, "Dan Brown")
		require.Zero(t, res.Applicable)
	})
	t.Run("a book with no author: held", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.book(t, "anon", "Origin", f.path("lib/Shelf/Origin"), nil)
		for i := 1; i <= 3; i++ {
			name := fmt.Sprintf("track %03d.mp3", i)
			p := f.file(t, filepath.Join("lib/Shelf/Origin", name), 5000+37*i)
			f.row(t, fmt.Sprintf("anon%d", i), existing, p, name, int64(5000+37*i), 600, i)
		}
		f.chapterFrags(t, dir, "Origin", 3, 600)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(dir), "origin"))
		require.Equal(t, fragSkipExistingBook, r.Skipped)
		require.Contains(t, r.SkipReason, "with no author")
		require.Zero(t, res.Applicable)
	})
	t.Run("the same author's book joins", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "jk", "Origin", "lib/Shelf/Origin", 3, 600)
		f.setAuthor(t, existing, f.authorID(t, "Jessica Khoury"))
		f.chapterFrags(t, dir, "Origin", 3, 600)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(dir), "origin"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})
}

// TestFragmentFixer_RetaggedCopies: fragments that sit at the chapter
// positions of an existing book's "_copyN" files with the same durations and
// a constant size difference (Horizon Storms) are never assembled, and not
// joined either, whatever the titles say.
func TestFragmentFixer_RetaggedCopies(t *testing.T) {
	t.Parallel()
	const dir = "lib/Kevin J Anderson/Horizon Storms"
	seed := func(t *testing.T, f *fragFixture, title string, delta func(i int) int64) (string, []string) {
		frags := f.chapterFrags(t, dir, "Storm", 6, 600)
		existing := f.book(t, "hs", title, f.path("lib/Unknown Author/Horizon Storms"), nil)
		for i := 1; i <= 6; i++ {
			name := fmt.Sprintf("Storm %02d_copy1.mp3", i)
			size := int64(7000+i) - delta(i)
			p := f.file(t, filepath.Join(dir, name), int(size))
			f.row(t, fmt.Sprintf("hs%d", i), existing, p, name, size, 600, i)
		}
		f.organized(t, existing)
		return existing, frags
	}
	retag := func(i int) int64 {
		if i%2 == 0 {
			return 3045
		}
		return 0
	}
	t.Run("same title, totals agree: still held as copies", func(t *testing.T) {
		f := newFragFixture(t)
		existing, frags := seed(t, f, "Horizon Storms", retag)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(dir), "storm"))
		require.Equal(t, fragClassExistingBook, r.Class)
		require.Equal(t, fragSkipRetagged, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, existing)
		require.ElementsMatch(t, frags, r.BookIDs)
		require.Zero(t, res.Applicable)
	})
	t.Run("no title match: found beside them", func(t *testing.T) {
		f := newFragFixture(t)
		_, _ = seed(t, f, "Something Else Entirely", retag)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(dir), "storm"))
		require.Equal(t, fragSkipRetagged, r.Skipped, r.SkipReason)
	})
	t.Run("size differences that are not constant are no re-tag", func(t *testing.T) {
		f := newFragFixture(t)
		_, _ = seed(t, f, "Something Else Entirely", func(i int) int64 { return int64(i * 997) })
		r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), "storm"))
		require.NotEqual(t, fragSkipRetagged, r.Skipped, r.SkipReason)
	})
}

// TestFragmentFixer_CoOwnerRule: owner rule 2026-10-05 for a moved row whose
// fragment's file is also a row of a live single-file book outside it. The
// co-owner is compared with the PARENT's whole total, never the few chapters
// the row repairs.
func TestFragmentFixer_CoOwnerRule(t *testing.T) {
	t.Parallel()
	// seed: parent (title) with rows 01 present and 02..n+1 gone, 600 s each;
	// n fragments imported from the gone paths, now organized away; a
	// co-owner (coTitle, coDur seconds) holding a row at fragment 1's file.
	seed := func(t *testing.T, f *fragFixture, title, coTitle string, n, coDur int) (parent string, frags []string, co string) {
		p1 := f.file(t, "lib/P/01.mp3", 801)
		parent = f.book(t, "parent", title, f.path("lib/P"), nil)
		f.row(t, "p01", parent, p1, "01.mp3", 801, 600, 1)
		var first string
		for i := 2; i <= n+1; i++ {
			name := fmt.Sprintf("%02d.mp3", i)
			gone := f.path("lib/P/" + name)
			f.row(t, "p"+name, parent, gone, name, int64(800+i), 600, i)
			at := f.file(t, fmt.Sprintf("lib/P/%02d/%s", i, name), 800+i)
			fr := f.book(t, "frag"+name, fmt.Sprintf("%02d", i), gone, nil)
			f.row(t, "f"+name, fr, at, name, int64(800+i), 600, 0)
			frags = append(frags, fr)
			if first == "" {
				first = at
			}
		}
		co = f.book(t, "co", coTitle, first, nil)
		f.row(t, "co", co, first, "co.mp3", int64(coDur)*1000+1, coDur, 0)
		return parent, frags, co
	}
	t.Run("same title, agreeing with the parent: a second copy, held", func(t *testing.T) {
		f := newFragFixture(t)
		parent, frags, co := seed(t, f, "Smartest Camp", "Smartest Camp", 3, 2400)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, "moved:"+parent)
		require.Equal(t, fragSkipCoOwnerDuplicate, r.Skipped, r.SkipReason)
		require.Equal(t, repairs.RiskReview, r.Risk)
		require.Contains(t, r.SkipReason, co)
		require.Contains(t, r.SkipReason, "parent "+parent)
		require.Contains(t, r.SkipReason, "deduplicate the two books first")
		require.ElementsMatch(t, append(append([]string(nil), frags...), parent), r.BookIDs, "the co-owner is listed, never written")
		require.Zero(t, res.Applicable)
		for _, row := range res.Rows {
			require.NotEqual(t, fragClassExistingBook, row.Class, "never joined into the copy")
		}
	})
	t.Run("same title, a whole book that disagrees with the parent: held", func(t *testing.T) {
		// The review probe: parent "Eldest" with 3 moved fragments beside a
		// 10 h co-owner "Eldest". Comparing the 3 chapters with the 10 h
		// book always "disagreed" and let the row through, leaving two live
		// books holding the same file.
		f := newFragFixture(t)
		parent, _, co := seed(t, f, "Eldest", "Eldest", 3, 36000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, "moved:"+parent)
		require.Equal(t, fragSkipCoOwner, r.Skipped)
		require.Contains(t, r.SkipReason, "same-titled co-owner(s) "+co)
		require.Contains(t, r.SkipReason, "parent "+parent)
		require.Zero(t, res.Applicable)
	})
	t.Run("same name at another position: a different work", func(t *testing.T) {
		f := newFragFixture(t)
		parent, _, _ := seed(t, f, "5 - Smartest Camp, Book 2", "2 - Smartest Camp, Book 2", 1, 1200)
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+parent)
		require.Equal(t, fragSkipCoOwner, r.Skipped)
		require.Contains(t, r.SkipReason, "a different work")
	})
	t.Run("same title by another author: held", func(t *testing.T) {
		f := newFragFixture(t)
		parent, _, co := seed(t, f, "Origin", "Origin", 3, 2400)
		f.setAuthor(t, parent, f.authorID(t, "Dan Brown"))
		f.setAuthor(t, co, f.authorID(t, "Jessica Khoury"))
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+parent)
		require.Equal(t, fragSkipCoOwner, r.Skipped)
		require.Contains(t, r.SkipReason, "different authors")
		require.Contains(t, r.SkipReason, "Jessica Khoury")
		require.Contains(t, r.SkipReason, "Dan Brown")
	})
	t.Run("junk-titled co-owner, totals disagree: the row proceeds into its parent", func(t *testing.T) {
		f := newFragFixture(t)
		parent, frags, co := seed(t, f, "Eldest", "", 3, 600)
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+parent)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, repairs.RiskReview, r.Risk)
		require.Equal(t, []string{co}, rowCoOwners(r))
		require.Contains(t, strings.Join(r.Evidence, "\n"), "the row proceeds into its parent "+parent)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "checkOwners lets the accepted co-owner through: %+v", out.Rows)
		f.requireRetiredInto(t, frags, parent)
		rows, err := f.s.GetBookFiles(co)
		require.NoError(t, err)
		require.Len(t, rows, 1, "the co-owner keeps its row")
		require.True(t, f.live(t, "co"))
	})
	t.Run("junk-titled co-owner agreeing with the parent: held", func(t *testing.T) {
		f := newFragFixture(t)
		parent, _, _ := seed(t, f, "Eldest", "c5", 1, 1200)
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+parent)
		require.Equal(t, fragSkipCoOwner, r.Skipped)
		require.Contains(t, r.SkipReason, "nothing proves the same work")
	})
	t.Run("co-owner of unknown duration: held", func(t *testing.T) {
		f := newFragFixture(t)
		parent, _, _ := seed(t, f, "Smartest Camp", "2 - Smartest Camp", 3, 0)
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+parent)
		require.Equal(t, fragSkipCoOwner, r.Skipped)
		require.Contains(t, r.SkipReason, "no duration")
	})
	t.Run("a co-owner with another work's title is refused", func(t *testing.T) {
		f := newFragFixture(t)
		parent, _, _ := seed(t, f, "Foundation", "Prelude to Foundation", 1, 600)
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+parent)
		require.Equal(t, fragSkipCoOwner, r.Skipped)
		require.Contains(t, r.SkipReason, "a different work, not an edition")
	})
}

// authorID creates (or finds) an author named name.
func (f *fragFixture) authorID(t *testing.T, name string) int {
	t.Helper()
	if a, err := f.s.GetAuthorByName(name); err == nil && a != nil {
		return a.ID
	}
	a, err := f.s.CreateAuthor(name)
	require.NoError(t, err)
	return a.ID
}

// TestFragmentFixer_NoSilentDrops: every fragment candidate the no-parent
// path sees ends in exactly one row; the ones no group takes are unplaced
// rows whose skip kind names why.
func TestFragmentFixer_NoSilentDrops(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	lone := f.chapterFrags(t, "lib/Solo", "Solo", 2, 300)
	scattered := f.numberedSeed(t, "lib/Two", []string{"01 - Intro", "02 - Intro", "03 - Intro", "04 - Something Else"}, 300, nil)
	p := f.file(t, "lib/Odd/zzqx.mp3", 990)
	nokey := f.book(t, "nokey", "Chapter 3", p, nil)
	f.row(t, "nk", nokey, p, "zzqx.mp3", 990, 300, 0)
	res := f.plan(t, "op-plan")
	seen := map[string]int{}
	for _, r := range res.Rows {
		for _, id := range r.BookIDs {
			seen[id]++
		}
	}
	all := append(append(append([]string(nil), lone...), scattered...), nokey)
	for _, id := range all {
		require.Equal(t, 1, seen[id], "book %s is in exactly one row", id)
	}
	for _, id := range lone {
		require.Equal(t, fragSkipLoneChapter, findRow(t, res, fragClassUnplaced+":"+id).Skipped)
	}
	require.Equal(t, fragSkipScattered, findRow(t, res, fragClassUnplaced+":"+scattered[3]).Skipped)
	r := findRow(t, res, fragClassUnplaced+":"+nokey)
	require.Equal(t, fragSkipNoChapterKey, r.Skipped)
	require.Equal(t, fragClassUnplaced, r.Class)
	require.Equal(t, 4, res.ByClass[fragClassUnplaced])
}

// TestFragTitleKeyGenericSubtitle (S1): a genre or edition subtitle never
// becomes the key; the work is the part before it.
func TestFragTitleKeyGenericSubtitle(t *testing.T) {
	t.Parallel()
	id := fragTitleIdentity
	for _, tc := range [][2]string{
		{"Origin: A Novel", "Origin"},
		{"Awaken Online: A LitRPG Adventure", "Awaken Online"},
		{"The Martian - A Novel", "The Martian"},
		{"Gone Girl: A Thriller", "Gone Girl (Unabridged)"},
	} {
		require.NotEmpty(t, id(tc[0]).key, tc[0])
		require.True(t, id(tc[0]).sameWork(id(tc[1])), "%q and %q are one work", tc[0], tc[1])
	}
	for _, tc := range [][2]string{
		{"Fahrenheit 451: A Novel", "1984: A Novel"},
		{"Ready Player One: A Novel", "Ready Player Two: A Novel"},
		{"Gone Girl: A Thriller", "Sharp Objects: A Thriller"},
	} {
		require.False(t, id(tc[0]).sameWork(id(tc[1])), "%q and %q are different works", tc[0], tc[1])
	}
	require.NotEqual(t, "anovel", id("Fahrenheit 451: A Novel").key)
	require.NotEqual(t, "anovel", id("1984: A Novel").key)
}

// TestFragmentFixer_JoinTargetRanking (S3): a book this fixer assembled is
// never the join target while one it did not assemble agrees, and when it is
// the only one that agrees the row is held ("revert first, then plan").
func TestFragmentFixer_JoinTargetRanking(t *testing.T) {
	t.Parallel()
	// assemble runs a no-parent apply of three "Eldest" chapters in a folder
	// named "Eldest": its survivor is a 3-file book titled "Eldest" that the
	// journal's plan record marks as this fixer's product.
	assemble := func(t *testing.T, f *fragFixture) string {
		dir := "lib/Christopher Paolini/Eldest"
		frags := f.chapterFrags(t, dir, "Eldest", 3, 600)
		cp := f.authorID(t, "Christopher Paolini")
		for _, id := range frags {
			f.setAuthor(t, id, cp)
		}
		r := findRow(t, f.plan(t, "op-plan-a"), noParentRowID(f.path(dir), "eldest"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		out := f.apply(t, "op-plan-a", "op-apply-a", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		return r.Proposed["survivor"]
	}
	const dir = "lib/Christopher Paolini/Book 2 - Eldest"
	t.Run("a book it did not assemble wins", func(t *testing.T) {
		f := newFragFixture(t)
		assembled := assemble(t, f)
		real := f.existingBook(t, "real", "Eldest", "lib/Shelf/Eldest", 2, 900)
		// Other sizes than the assembled book's chapters: not re-tagged copies.
		f.chapterFragsAt(t, dir, "Eldest", 3, 600, 8000)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(dir), "eldest"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, real, r.Proposed["join"], "not the assembled %s, though it has more files", assembled)
	})
	t.Run("only the assembled book agrees: held", func(t *testing.T) {
		f := newFragFixture(t)
		assembled := assemble(t, f)
		f.chapterFragsAt(t, dir, "Eldest", 3, 600, 8000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(dir), "eldest"))
		require.Equal(t, fragSkipExistingBook, r.Skipped)
		require.Contains(t, r.SkipReason, assembled)
		require.Contains(t, r.SkipReason, "revert that apply first")
		require.Zero(t, res.Applicable)
	})
}

// TestFragmentFixer_ExistingBookAtApply (S4): a book of the group's title
// created between the plan and the apply refuses the plain no-parent row.
func TestFragmentFixer_ExistingBookAtApply(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	dir := "lib/Christopher Paolini/Book 2 - Eldest"
	frags := f.chapterFrags(t, dir, "Eldest", 3, 600)
	r := findRow(t, f.plan(t, "op-plan"), noParentRowID(f.path(dir), "eldest"))
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	f.existingBook(t, "late", "Eldest", "lib/Shelf/Eldest", 3, 600)
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	require.Contains(t, fmt.Sprintf("%+v", out.Rows), "exists now")
	for _, id := range frags {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		require.False(t, b.IsSoftDeleted(), "nothing written")
	}
}

// TestFragmentFixer_JoinOffsets (N1): a joined fragment's listening position
// maps to the start of the target's file at its chapter position, not to its
// running sum among the fragments.
func TestFragmentFixer_JoinOffsets(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	target := f.book(t, "t", "Eldest", f.path("lib/Shelf/Eldest"), nil)
	f.setAuthor(t, target, f.authorID(t, "Christopher Paolini"))
	// The target's chapters 01..03 run 500, 700, 600 s (1800 s); the
	// fragments are chapters 02 and 03 and 01, all 600 s.
	for i, d := range []int{500, 700, 600} {
		name := fmt.Sprintf("Eldest %02d_copy1.mp3", i+1)
		p := f.file(t, filepath.Join("lib/Shelf/Eldest", name), 9000+37*i)
		f.row(t, fmt.Sprintf("t%d", i), target, p, name, int64(9000+37*i), d, i+1)
	}
	dir := "lib/Christopher Paolini/Book 2 - Eldest"
	ff := newFragmentFixer(f.p)
	store, _, err := ff.stores()
	require.NoError(t, err)
	lib, err := ff.loadLibrary(store)
	require.NoError(t, err)
	plan := &fragGroupPlan{}
	for i, n := range []int{1, 2, 3} {
		stem := fmt.Sprintf("Eldest %02d", n)
		plan.Members = append(plan.Members, fragGroupMember{Frag: &fragCandidate{Book: fragBook{ID: stem},
			File: fragFile{Path: filepath.Join(f.path(dir), stem+".mp3"), Duration: 600}, OrigName: stem + ".mp3"}, Offset: float64(600 * i)})
	}
	joinOffsets(lib, plan, target)
	require.Equal(t, []float64{0, 500, 1200}, []float64{plan.Members[0].Offset, plan.Members[1].Offset, plan.Members[2].Offset})
}

// TestFragmentFixer_JoinTargetITunes (was N2; owner 2026-10-08): an existing
// book that iTunes knows about (an iTunes id on its row, or an itunes
// external id) is joined like any other.
func TestFragmentFixer_JoinTargetITunes(t *testing.T) {
	t.Parallel()
	const dir = "lib/Christopher Paolini/Book 2 - Eldest"
	check := func(t *testing.T, f *fragFixture) {
		t.Helper()
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(dir), "eldest"))
		require.Equal(t, fragClassExistingBook, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	}
	t.Run("row-level", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "eldest", "Eldest", "lib/Shelf/Eldest", 3, 600)
		rows, err := f.s.GetBookFiles(existing)
		require.NoError(t, err)
		rows[0].ITunesPersistentID = "ABCDEF0123456789"
		require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
		f.chapterFrags(t, dir, "Eldest", 3, 600)
		check(t, f)
	})
	t.Run("external id", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "eldest", "Eldest", "lib/Shelf/Eldest", 3, 600)
		require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "FEDCBA9876543210", BookID: existing}))
		f.chapterFrags(t, dir, "Eldest", 3, 600)
		check(t, f)
	})
}

// TestFragmentFixer_UnplacedITunesCounted (N4): an unplaced fragment under
// the iTunes library is counted under unplaced, not manual-only.
func TestFragmentFixer_UnplacedITunesCounted(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	p := f.file(t, "books/itunes/Solo/Solo 01.mp3", 990)
	id := f.book(t, "solo", "Solo 01", p, nil)
	f.row(t, "s", id, p, "Solo 01.mp3", 990, 300, 0)
	res := f.plan(t, "op-plan")
	r := findRow(t, res, fragClassUnplaced+":"+id)
	require.Equal(t, fragClassUnplaced, r.Class)
	require.Equal(t, fragSkipLoneChapter, r.Skipped)
	require.Equal(t, 1, res.ByClass[fragClassUnplaced])
	require.Zero(t, res.ByClass[fragClassManual])
}
