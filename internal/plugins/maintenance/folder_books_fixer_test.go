// file: internal/plugins/maintenance/folder_books_fixer_test.go
// version: 1.1.0
// guid: 9d4c7a2e-1b6f-4e83-a5d0-8f2b3c6e9a17
// last-edited: 2026-10-01

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

func (f *fragFixture) fbPlan(t *testing.T, fixerID, opID string) *repairs.PlanResult {
	t.Helper()
	params, err := json.Marshal(repairs.PlanParams{FixerID: fixerID})
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

func (f *fragFixture) fbApply(t *testing.T, planOpID, opID string, rowIDs []string) *repairs.ApplyResult {
	t.Helper()
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fbFixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &no})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsApply(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	return res
}

// fbBook creates a book holding one row per path (duration secs each).
func (f *fragFixture) fbBook(t *testing.T, role, title, path string, secs int, paths ...string) string {
	t.Helper()
	b, err := f.s.CreateBook(&database.Book{Title: title, FilePath: path})
	require.NoError(t, err)
	f.ids[role] = b.ID
	for i, p := range paths {
		f.row(t, fmt.Sprintf("%s-%d", role, i), b.ID, p, "", int64(100+i), secs, i+1)
	}
	return b.ID
}

func (f *fragFixture) setVG(t *testing.T, id, group string, primary bool) {
	t.Helper()
	_, err := f.s.ModifyBook(id, func(b *database.Book) error {
		b.VersionGroupID, b.IsPrimaryVersion = &group, &primary
		return nil
	})
	require.NoError(t, err)
}

func fbRowCount(t *testing.T, s *database.PebbleStore) int {
	t.Helper()
	all, err := s.GetAllBookFilesCore()
	require.NoError(t, err)
	return len(all)
}

func fbFindRow(res *repairs.PlanResult, bookID string) *repairs.Row {
	for i := range res.Rows {
		for _, id := range res.Rows[i].BookIDs {
			if id == bookID {
				return &res.Rows[i]
			}
		}
	}
	return nil
}

// fbITunes is the iTunes Media/Audiobooks shelf under the fixture root: the
// only place the fixer creates books (organize never moves a file there).
const fbITunes = "books/itunes/iTunes Media/Audiobooks"

// seedWolfe builds an author shelf "Gene Wolfe" under base holding four works:
// two proper books (Shadow, Claw), Citadel (one file, also held by gOn's
// single-file book G when gOn is "citadel"), and Sword (01 and 02, held by no
// proper book; with gOn "sword" G holds Sword/02 instead). Two folder-books
// hold all of it: FB (primary) and FB2 (an exact duplicate) in one version
// group with Shadow (organized).
func (f *fragFixture) seedWolfe(t *testing.T, base, gOn string) (all []string) {
	t.Helper()
	rel := func(s string) string { return filepath.Join(base, "Gene Wolfe", s) }
	var shadow, claw, citadel, sword []string
	for i := 1; i <= 3; i++ {
		shadow = append(shadow, f.file(t, rel(fmt.Sprintf("Shadow/0%d.mp3", i)), 10))
		claw = append(claw, f.file(t, rel(fmt.Sprintf("Claw/0%d.mp3", i)), 10))
	}
	citadel = append(citadel, f.file(t, rel("Citadel/01.mp3"), 10))
	for i := 1; i <= 2; i++ {
		sword = append(sword, f.file(t, rel(fmt.Sprintf("Sword/0%d.mp3", i)), 10))
	}
	f.fbBook(t, "shadow", "The Shadow of the Torturer", f.path(rel("Shadow")), 3600, shadow...)
	f.fbBook(t, "claw", "The Claw of the Conciliator", f.path(rel("Claw")), 3600, claw...)
	switch gOn {
	case "citadel":
		f.fbBook(t, "g", "Citadel 01", citadel[0], 1200, citadel[0])
	case "sword":
		f.fbBook(t, "g", "Sword 02", sword[1], 1200, sword[1])
	}
	all = append(append(append(append(all, shadow...), claw...), citadel...), sword...)
	author, err := f.s.CreateAuthor("Gene Wolfe")
	require.NoError(t, err)
	for _, role := range []string{"fb", "fb2"} {
		id := f.fbBook(t, role, "Gene Wolfe", f.path(filepath.Join(base, "Gene Wolfe")), 1200, all...)
		_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &author.ID; return nil })
		require.NoError(t, err)
	}
	f.setVG(t, f.ids["fb"], "vg-wolfe", true)
	f.setVG(t, f.ids["fb2"], "vg-wolfe", false)
	f.setVG(t, f.ids["shadow"], "vg-wolfe", false)
	f.organized(t, f.ids["shadow"])
	return all
}

// fbSetRow rewrites one seeded book_file row (role as fbBook names them:
// "<book role>-<index>").
func (f *fragFixture) fbSetRow(t *testing.T, bookRole string, idx int, fn func(*database.BookFile)) {
	t.Helper()
	r, err := f.s.GetBookFileByID(f.ids[bookRole], f.rowIDs[fmt.Sprintf("%s-%d", bookRole, idx)])
	require.NoError(t, err)
	require.NotNil(t, r)
	fn(r)
	require.NoError(t, f.s.UpdateBookFile(r.ID, r))
}

// fbSingleRow plans and returns the one row, which must exist.
func (f *fragFixture) fbSingleRow(t *testing.T, opID string) repairs.Row {
	t.Helper()
	res := f.fbPlan(t, fbFixerID, opID)
	require.Len(t, res.Rows, 1)
	return res.Rows[0]
}

func TestFolderBooksFixer_ApplyAndRevert(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	fb, fb2 := f.ids["fb"], f.ids["fb2"]
	swordDir := f.path(filepath.Join(fbITunes, "Gene Wolfe", "Sword"))
	sw1 := filepath.Join(swordDir, "01.mp3")
	// A row-level iTunes id on the folder-book's copy of Sword/01: allowed,
	// and it must stay where it is.
	f.fbSetRow(t, "fb", 7, func(r *database.BookFile) { r.ITunesPersistentID = "00000000000000AB" })

	// The fragment fixer sees the live folder-books as parents of G's file.
	before := f.fbPlan(t, fragFixerID, "op-frag-before")
	require.NotNil(t, fbFindRow(before, fb), "a live folder-book is a fragment-consolidation parent")

	row := f.fbSingleRow(t, "op-plan")
	require.Equal(t, fbRowID([]string{min(fb, fb2), max(fb, fb2)}), row.RowID)
	require.Equal(t, fbTierShelf, row.Class)
	require.True(t, row.Applicable(), "skipped: %s %s", row.Skipped, row.SkipReason)
	require.Equal(t, "1", row.Proposed["new_books"])
	require.Equal(t, "7", row.Proposed["held"])
	rowsBefore := fbRowCount(t, f.s)

	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	require.False(t, f.live(t, "fb"))
	require.False(t, f.live(t, "fb2"))
	for _, role := range []string{"fb", "fb2"} {
		rows, err := f.s.GetBookFiles(f.ids[role])
		require.NoError(t, err)
		require.Len(t, rows, 9, "a retired folder-book keeps its rows")
	}
	pid, err := f.s.GetBookFileByID(fb, f.rowIDs["fb-7"])
	require.NoError(t, err)
	require.Equal(t, "00000000000000AB", pid.ITunesPersistentID, "the iTunes id stays on its row")
	sh, err := f.s.GetBookByID(f.ids["shadow"])
	require.NoError(t, err)
	require.True(t, sh.IsPrimaryVersion != nil && *sh.IsPrimaryVersion, "primacy handed to the real book")

	// One new book over Sword, credited to the shelf's author, at the Sword
	// folder: a rescan of that folder finds this book at its path.
	created, err := f.s.GetBookByFilePath(swordDir)
	require.NoError(t, err)
	require.NotNil(t, created, "the created book owns its folder path")
	require.Equal(t, "Sword", created.Title)
	require.False(t, created.IsSoftDeleted())
	require.NotNil(t, created.AuthorID)
	crows, err := f.s.GetBookFiles(created.ID)
	require.NoError(t, err)
	require.Len(t, crows, 2)
	require.Equal(t, sw1, crows[0].FilePath)
	for _, r := range crows {
		require.Empty(t, r.ITunesPersistentID, "a created row never carries an iTunes id")
		_, err := os.Stat(r.FilePath)
		require.NoError(t, err, "nothing moved on disk")
	}
	require.Equal(t, rowsBefore+2, fbRowCount(t, f.s), "only the new book's rows were added")

	// The hand-off happened before the soft-delete.
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	at := map[string]int{}
	for i, c := range changes {
		if c.BookID == fb {
			if _, seen := at[c.ChangeType]; !seen {
				at[c.ChangeType] = i
			}
		}
	}
	require.Contains(t, at, undo.ChangeTypeBookPrimaryHandoff)
	require.Less(t, at[undo.ChangeTypeBookPrimaryDemote], at[undo.ChangeTypeBookPrimaryHandoff])
	require.Less(t, at[undo.ChangeTypeBookPrimaryHandoff], at[undo.ChangeTypeBookSoftDelete])

	// Re-plan: nothing left to flag.
	again := f.fbPlan(t, fbFixerID, "op-plan-2")
	require.Empty(t, again.Rows)
	// The fragment fixer no longer treats the retired folder-books as parents.
	after := f.fbPlan(t, fragFixerID, "op-frag-after")
	require.Nil(t, fbFindRow(after, fb))
	require.Nil(t, fbFindRow(after, fb2))

	// The preflight agrees with the revert: every journaled row is restorable
	// except the record-only book_file_create rows.
	pre, err := undo.PreflightUndoConflicts(f.s, "op-apply")
	require.NoError(t, err)
	require.Equal(t, map[string]int{undo.ChangeTypeBookFileCreate: 2}, pre.NotRestorableTypes)
	require.Equal(t, pre.TotalChanges-pre.NotRestorable, pre.Safe, "%+v", pre)

	// Revert: folder-books live again, FB re-crowned, created book hidden,
	// no row deleted.
	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.True(t, f.live(t, "fb"))
	require.True(t, f.live(t, "fb2"))
	b, err := f.s.GetBookByID(fb)
	require.NoError(t, err)
	require.Equal(t, f.path(filepath.Join(fbITunes, "Gene Wolfe")), b.FilePath)
	require.True(t, b.IsPrimaryVersion == nil || *b.IsPrimaryVersion, "FB is primary again")
	sh, err = f.s.GetBookByID(f.ids["shadow"])
	require.NoError(t, err)
	require.False(t, sh.IsPrimaryVersion == nil || *sh.IsPrimaryVersion)
	created, err = f.s.GetBookByID(created.ID)
	require.NoError(t, err)
	require.True(t, created.IsSoftDeleted(), "the created book is hidden by the revert")
	require.Equal(t, rowsBefore+2, fbRowCount(t, f.s), "the revert deletes no row")
}

// TestFolderBooksFixer_RevertRefusesAMovedCreatedBook: once a created book's
// row names another path than the one journaled, or the book joins a version
// group, the preflight and the revert refuse to hide it.
func TestFolderBooksFixer_RevertRefusesAMovedCreatedBook(t *testing.T) {
	for _, how := range []string{"moved", "linked"} {
		t.Run(how, func(t *testing.T) {
			f := newFragFixture(t)
			f.seedWolfe(t, fbITunes, "citadel")
			row := f.fbSingleRow(t, "op-plan")
			out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			created, err := f.s.GetBookByFilePath(f.path(filepath.Join(fbITunes, "Gene Wolfe", "Sword")))
			require.NoError(t, err)
			require.NotNil(t, created)
			if how == "moved" {
				rows, err := f.s.GetBookFiles(created.ID)
				require.NoError(t, err)
				r := rows[0]
				r.FilePath = f.path("Gene Wolfe/Sword/01.mp3")
				require.NoError(t, f.s.UpdateBookFile(r.ID, &r))
			} else {
				f.setVG(t, created.ID, "vg-other", true)
			}
			var c *database.OperationChange
			changes, err := f.s.GetOperationChanges("op-apply")
			require.NoError(t, err)
			for _, oc := range changes {
				if oc.ChangeType == undo.ChangeTypeRepairBookCreate {
					c = oc
				}
			}
			require.NotNil(t, c)
			require.Equal(t, undo.ReasonChangedSince, undo.RefusalReason(undo.CheckRepairBookCreate(f.s, c)))
			_, _ = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
			got, err := f.s.GetBookByID(created.ID)
			require.NoError(t, err)
			require.False(t, got.IsSoftDeleted(), "a changed created book is not hidden")
		})
	}
}

// TestFolderBooksFixer_SkipKinds: the shapes that list a row without applying it.
func TestFolderBooksFixer_SkipKinds(t *testing.T) {
	t.Run("split work", func(t *testing.T) {
		// G holds Sword/02, Sword/01 is orphaned: a new book would be a
		// second, partial Sword.
		f := newFragFixture(t)
		f.seedWolfe(t, fbITunes, "sword")
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipSplitWork, row.Skipped, row.SkipReason)
	})
	t.Run("duplicate title", func(t *testing.T) {
		f := newFragFixture(t)
		f.seedWolfe(t, fbITunes, "citadel")
		a, err := f.s.GetAuthorByName("Gene Wolfe")
		require.NoError(t, err)
		id := f.fbBook(t, "other", "Sword", f.path("Elsewhere/Sword.m4b"), 3600)
		_, err = f.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &a.ID; return nil })
		require.NoError(t, err)
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipDupTitle, row.Skipped, row.SkipReason)
	})
	t.Run("new book outside iTunes", func(t *testing.T) {
		f := newFragFixture(t)
		f.seedWolfe(t, "", "citadel")
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipNeedsNewBook, row.Skipped, row.SkipReason)
	})
	t.Run("no heir", func(t *testing.T) {
		// Shadow cannot be crowned (not organized): FB is held, not retired
		// into a headless group.
		f := newFragFixture(t)
		f.seedWolfe(t, fbITunes, "citadel")
		imported := "imported"
		_, err := f.s.ModifyBook(f.ids["shadow"], func(b *database.Book) error { b.LibraryState = &imported; return nil })
		require.NoError(t, err)
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipNoHeir, row.Skipped, row.SkipReason)
	})
}

// TestFolderBooksFixer_GroupOfOnlyFolderBooksNeedsNoHeir: a version group
// whose only members are the row's own folder-books has nobody to hand to,
// and nobody who needs it: the row applies.
func TestFolderBooksFixer_GroupOfOnlyFolderBooksNeedsNoHeir(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	f.setVG(t, f.ids["shadow"], "vg-shadow", true)
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "%s %s", row.Skipped, row.SkipReason)
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fb"))
}

// TestFolderBooksFixer_MissingOrphansBuildNothing: a file only the
// folder-books hold, and only as Missing rows, gets no book; the row still
// retires them.
func TestFolderBooksFixer_MissingOrphansBuildNothing(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	for _, role := range []string{"fb", "fb2"} {
		for _, i := range []int{7, 8} {
			f.fbSetRow(t, role, i, func(r *database.BookFile) { r.Missing = true })
		}
	}
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "%s %s", row.Skipped, row.SkipReason)
	require.Equal(t, "0", row.Proposed["new_books"])
	require.Equal(t, "2", row.Proposed["missing_orphans"])
	rowsBefore := fbRowCount(t, f.s)
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fb"))
	require.Equal(t, rowsBefore, fbRowCount(t, f.s))
}

// TestFolderBooksFixer_Negatives: books that must not be flagged, and the
// tiers that are listed but never applied.
func TestFolderBooksFixer_Negatives(t *testing.T) {
	f := newFragFixture(t)
	// One work titled with its author's name, directly in the author folder.
	var one []string
	for i := 1; i <= 6; i++ {
		one = append(one, f.file(t, fmt.Sprintf("Brandon Smith/Brandon Smith - 0%d.mp3", i), 10))
	}
	f.fbBook(t, "single", "Brandon Smith", f.path("Brandon Smith"), 6000, one...)

	// One real book whose files organize scattered, with fragment books
	// holding subsets (P>=2 alone would flag it).
	var wind []string
	for d := 1; d <= 3; d++ {
		var part []string
		for i := 1; i <= 2; i++ {
			part = append(part, f.file(t, fmt.Sprintf("BS/Wind and Truth - %d/0%d.mp3", d, i), 10))
		}
		f.fbBook(t, fmt.Sprintf("frag%d", d), fmt.Sprintf("Wind and Truth - %d", d), filepath0(part), 1800, part...)
		wind = append(wind, part...)
	}
	f.fbBook(t, "wind", "Wind and Truth", f.path("BS/Wind and Truth - 1"), 1800, wind...)

	// Deep: 100h in a series folder, listed only.
	var deep []string
	for _, d := range []string{"a", "b"} {
		for i := 1; i <= 3; i++ {
			deep = append(deep, f.file(t, fmt.Sprintf("Auth/Series/%s/0%d.mp3", d, i), 10))
		}
	}
	f.fbBook(t, "deep", "Omnibus", f.path("Auth/Series"), 60000, deep...)

	// Fragmentary: an author folder of one-minute works.
	_, err := f.s.CreateAuthor("Narrator X")
	require.NoError(t, err)
	var tracks []string
	for _, n := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"} {
		tracks = append(tracks, f.file(t, "Narrator X/"+n+"/01.mp3", 10))
	}
	f.fbBook(t, "flat", "Narrator X", f.path("Narrator X"), 60, tracks...)

	res := f.fbPlan(t, fbFixerID, "op-plan")
	require.Nil(t, fbFindRow(res, f.ids["single"]), "a single work titled with its author is not a folder-book")
	require.Nil(t, fbFindRow(res, f.ids["wind"]), "a scattered single book is not a folder-book")
	d := fbFindRow(res, f.ids["deep"])
	require.NotNil(t, d)
	require.Equal(t, fbTierDeep, d.Class)
	require.Equal(t, fbSkipDeep, d.Skipped)
	fl := fbFindRow(res, f.ids["flat"])
	require.NotNil(t, fl)
	require.Equal(t, fbTierShelf, fl.Class)
	require.Equal(t, fbSkipFragmentary, fl.Skipped)
}

// fbUnitLib is a one-book library for detection unit tests.
func fbUnitLib(shelves, authors []string, title string, paths []string, dur int) *fbLib {
	lib := newFBLib()
	lib.shelves = shelves
	for i, a := range authors {
		lib.authors[i+1] = a
		lib.authorNorm[fbNorm(a)] = true
	}
	var rows []database.BookFileCore
	for i, p := range paths {
		rows = append(rows, database.BookFileCore{ID: fmt.Sprint(i), BookID: "b", FilePath: p, Duration: dur})
	}
	lib.add(database.BookCore{ID: "b", Title: title}, rows)
	lib.finish()
	return lib
}

// TestFolderBooksFixer_DetectionFalsePositives are the 2026-10-01 review's
// cases: real books (one folder, discs, chapter-named files, a box set, a
// long book) must never reach an applicable tier.
func TestFolderBooksFixer_DetectionFalsePositives(t *testing.T) {
	authors := []string{"Robert Jordan", "Victor Hugo", "Frank Herbert"}
	cases := []struct {
		name, title string
		shelves     []string
		paths       []string
		dur         int
	}{
		{"import flat chapter-titled", "The Hobbit", []string{"/imp"}, []string{
			"/imp/The Hobbit/01 - An Unexpected Party.mp3", "/imp/The Hobbit/02 - Roast Mutton.mp3",
			"/imp/The Hobbit/03 - A Short Rest.mp3", "/imp/The Hobbit/04 - Over Hill and Under Hill.mp3",
			"/imp/The Hobbit/05 - Riddles in the Dark.mp3"}, 2400},
		{"import multi-disc Disc N of M", "Dune", []string{"/imp"}, []string{
			"/imp/Dune/Disc 1 of 3/01.mp3", "/imp/Dune/Disc 1 of 3/02.mp3", "/imp/Dune/Disc 2 of 3/01.mp3",
			"/imp/Dune/Disc 2 of 3/02.mp3", "/imp/Dune/Disc 3 of 3/01.mp3"}, 3600},
		{"import multi-disc numeric", "Dune", []string{"/imp"}, []string{
			"/imp/Dune/1/01.mp3", "/imp/Dune/1/02.mp3", "/imp/Dune/2/01.mp3", "/imp/Dune/2/02.mp3", "/imp/Dune/3/01.mp3"}, 3600},
		{"import multi-disc Title CD1", "Dune", []string{"/imp"}, []string{
			"/imp/Dune/Dune CD1/01.mp3", "/imp/Dune/Dune CD1/02.mp3", "/imp/Dune/Dune CD2/01.mp3",
			"/imp/Dune/Dune CD2/02.mp3", "/imp/Dune/Dune CD3/01.mp3"}, 3600},
		{"multi-disc in an author folder", "Dune", []string{"/lib"}, []string{
			"/lib/Frank Herbert/Dune/CD1/01.mp3", "/lib/Frank Herbert/Dune/CD1/02.mp3", "/lib/Frank Herbert/Dune/CD2/01.mp3",
			"/lib/Frank Herbert/Dune/CD2/02.mp3", "/lib/Frank Herbert/Dune/CD3/01.mp3"}, 3600},
		{"root flat no-author layout", "Project Hail Mary", []string{"/lib"}, []string{
			"/lib/Project Hail Mary/Chapter 1 - Petrova.mp3", "/lib/Project Hail Mary/Chapter 2 - Astrophage.mp3",
			"/lib/Project Hail Mary/Ch3 Hail Mary.mp3", "/lib/Project Hail Mary/Ch4 Rocky.mp3",
			"/lib/Project Hail Mary/Ch5 Erid.mp3"}, 2400},
		{"box set import", "The Expanse Box Set", []string{"/imp"}, []string{
			"/imp/The Expanse Box Set/Leviathan Wakes/01.mp3", "/imp/The Expanse Box Set/Leviathan Wakes/02.mp3",
			"/imp/The Expanse Box Set/Calibans War/01.mp3", "/imp/The Expanse Box Set/Calibans War/02.mp3",
			"/imp/The Expanse Box Set/Abaddons Gate/01.mp3"}, 7200},
		{"long single book >80h in its own folder", "Wheel of Time Companion", []string{"/lib"}, []string{
			"/lib/Robert Jordan/Wheel of Time Companion/01.mp3", "/lib/Robert Jordan/Wheel of Time Companion/02.mp3",
			"/lib/Robert Jordan/Wheel of Time Companion/03.mp3", "/lib/Robert Jordan/Wheel of Time Companion/04.mp3",
			"/lib/Robert Jordan/Wheel of Time Companion/05.mp3"}, 60000},
		{"long single book flat in an author folder", "Les Miserables", []string{"/lib"}, []string{
			"/lib/Victor Hugo/Les Mis 01.mp3", "/lib/Victor Hugo/Les Mis 02.mp3", "/lib/Victor Hugo/Les Mis 03.mp3",
			"/lib/Victor Hugo/Les Mis 04.mp3", "/lib/Victor Hugo/Les Mis 05.mp3"}, 60000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := fbUnitLib(c.shelves, authors, c.title, c.paths, c.dur).evaluate("b")
			require.False(t, fbApplicableTier(ev.Tier), "tier %q root %s evidence %v", ev.Tier, ev.Root, ev.Evidence)
		})
	}
}

// TestFolderBooksFixer_DetectionPositives: an author folder holding several
// works is a shelf; the same layout under a folder that names no author is
// not (it could be a box set).
func TestFolderBooksFixer_DetectionPositives(t *testing.T) {
	paths := func(dir string) []string {
		return []string{dir + "/Leviathan Wakes/01.mp3", dir + "/Leviathan Wakes/02.mp3",
			dir + "/Calibans War/01.mp3", dir + "/Calibans War/02.mp3", dir + "/Abaddons Gate/01.mp3"}
	}
	ev := fbUnitLib([]string{"/lib"}, []string{"James S. A. Corey"}, "James S. A. Corey", paths("/lib/James S. A. Corey"), 7200).evaluate("b")
	require.Equal(t, fbTierShelf, ev.Tier, "%v", ev.Evidence)
	ev = fbUnitLib([]string{"/lib"}, nil, "James S. A. Corey", paths("/lib/James S. A. Corey"), 7200).evaluate("b")
	require.False(t, fbApplicableTier(ev.Tier), "a folder naming no known author is not an author shelf")
	ev = fbUnitLib([]string{"/lib"}, nil, "Audiobooks", paths("/lib"), 7200).evaluate("b")
	require.Equal(t, fbTierShelf, ev.Tier, "the library root itself")
}

func filepath0(paths []string) string { return paths[0] }

func TestFBStemAndGroupKey(t *testing.T) {
	cases := map[string]string{
		"/r/A/01 Awakened Essence - 01.mp3":  "Awakened Essence",
		"/r/A/Awakened Essence - 00.mp3":     "Awakened Essence",
		"/r/A/Book Title (3 of 12).mp3":      "Book Title",
		"/r/A/Book Title - Part 2.m4b":       "Book Title",
		"/r/A/The Claw - Chapter 07 - x.mp3": "The Claw",
		"/r/A/Falling Fast - A Reynolds.m4b": "Falling Fast - A Reynolds",
	}
	for p, want := range cases {
		require.Equal(t, want, fbStem(p), p)
	}
	k1, _, _ := fbGroupKey("/r/A/Book/CD1/01.mp3", "/r/A")
	k2, _, _ := fbGroupKey("/r/A/Book/CD2/01.mp3", "/r/A")
	require.Equal(t, k1, k2, "disc folders group with their parent")
	for _, disc := range []string{"Disc 1 of 3", "1", "Book CD2", "Part 2"} {
		k, _, d := fbGroupKey("/r/A/Book/"+disc+"/01.mp3", "/r/A")
		require.Equal(t, k1, k, disc)
		require.Equal(t, "/r/A/Book", d, disc)
	}
	k3, l3, _ := fbGroupKey("/r/A/Title - 01.mp3", "/r/A")
	k4, _, _ := fbGroupKey("/r/A/Title - 02.mp3", "/r/A")
	require.Equal(t, k3, k4)
	require.Equal(t, "Title", l3)
}

// TestFolderBooksFixer_ITunesOptOutIsThisFixerOnly: the framework guard skips
// these iTunes books for any other fixer, and still skips Doctor Who / Big
// Finish / Torchwood for this one.
func TestFolderBooksFixer_ITunesOptOutIsThisFixerOnly(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	ids := []string{f.ids["fb"], f.ids["fb2"]}
	kind, _, err := repairs.GuardBooks(f.s, nil, repairs.NewPathResolver(), ids)
	require.NoError(t, err)
	require.Equal(t, repairs.SkipITunes, kind, "without the opt-out the iTunes guard holds the row")
	kind, _, err = repairs.GuardBooksFor(newFolderBooksFixer(f.p), f.s, nil, repairs.NewPathResolver(), ids)
	require.NoError(t, err)
	require.Empty(t, kind, "the folder-books fixer is cleared for iTunes database rows")

	dw, err := f.s.CreateSeries("Doctor Who", nil)
	require.NoError(t, err)
	_, err = f.s.ModifyBook(f.ids["fb"], func(b *database.Book) error { b.SeriesID = &dw.ID; return nil })
	require.NoError(t, err)
	row := f.fbSingleRow(t, "op-plan")
	require.Equal(t, repairs.SkipOwnerManual, row.Skipped, row.SkipReason)
}
