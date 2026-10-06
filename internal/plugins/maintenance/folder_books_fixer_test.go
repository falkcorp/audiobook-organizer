// file: internal/plugins/maintenance/folder_books_fixer_test.go
// version: 1.7.1
// guid: 9d4c7a2e-1b6f-4e83-a5d0-8f2b3c6e9a17
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	f := newGlobalRootFragFixture(t)
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
	require.Contains(t, row.BookIDs, f.ids["shadow"], "the version group's other members are row books")
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
	byPath, err := f.s.GetBookFileByPath(sw1)
	require.NoError(t, err)
	require.NotNil(t, byPath)
	require.Contains(t, []string{fb, fb2}, byPath.BookID, "the path index names a live book's row again")
}

// TestFolderBooksFixer_RevertRefusesAMovedCreatedBook: once a created book's
// row names another path than the one journaled, or the book joins a version
// group, the preflight and the revert refuse to hide it.
func TestFolderBooksFixer_RevertRefusesAMovedCreatedBook(t *testing.T) {
	for _, how := range []string{"moved", "linked", "retitled"} {
		t.Run(how, func(t *testing.T) {
			f := newGlobalRootFragFixture(t)
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
			} else if how == "linked" {
				f.setVG(t, created.ID, "vg-other", true)
			} else {
				_, err := f.s.ModifyBook(created.ID, func(b *database.Book) error { b.Title = "Sword (edited)"; return nil })
				require.NoError(t, err)
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
		f := newGlobalRootFragFixture(t)
		f.seedWolfe(t, fbITunes, "sword")
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipSplitWork, row.Skipped, row.SkipReason)
	})
	t.Run("duplicate title", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
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
		f := newGlobalRootFragFixture(t)
		f.seedWolfe(t, "", "citadel")
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipNeedsNewBook, row.Skipped, row.SkipReason)
	})
	t.Run("no heir", func(t *testing.T) {
		// Shadow cannot be crowned (not organized): FB is held, not retired
		// into a headless group.
		f := newGlobalRootFragFixture(t)
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
	f := newGlobalRootFragFixture(t)
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
	f := newGlobalRootFragFixture(t)
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
	f := newGlobalRootFragFixture(t)
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
	for _, disc := range []string{"Disc 1 of 3", "1", "Book CD2", "Book (Disc 2)"} {
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
	f := newGlobalRootFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	ids := []string{f.ids["fb"], f.ids["fb2"]}
	kind, _, err := repairs.GuardBooks(f.s, nil, nil, repairs.NewPathResolver(), ids)
	require.NoError(t, err)
	require.Equal(t, repairs.SkipITunes, kind, "without the opt-out the iTunes guard holds the row")
	kind, _, err = repairs.GuardBooksFor(newFolderBooksFixer(f.p), f.s, nil, nil, repairs.NewPathResolver(), ids)
	require.NoError(t, err)
	require.Empty(t, kind, "the folder-books fixer is cleared for iTunes database rows")

	dw, err := f.s.CreateSeries("Doctor Who", nil)
	require.NoError(t, err)
	_, err = f.s.ModifyBook(f.ids["fb"], func(b *database.Book) error { b.SeriesID = &dw.ID; return nil })
	require.NoError(t, err)
	row := f.fbSingleRow(t, "op-plan")
	require.Equal(t, repairs.SkipOwnerManual, row.Skipped, row.SkipReason)
}

// TestFolderBooksFixer_ChapterBooksAreNotProperBooks: a real book in its
// author's folder whose chapters an old scan imported one book each (prod's
// "Metro 2033": eleven one-hour chapter books) is not a shelf of works.
func TestFolderBooksFixer_ChapterBooksAreNotProperBooks(t *testing.T) {
	dir := "/lib/Dmitry Glukhovsky/"
	var paths []string
	for _, ch := range []string{"Intro", "Chapter One - The End", "Chapter Two - The Hunter", "Chapter Three - If",
		"Chapter Four - The V", "Chapter Five - Ka"} {
		paths = append(paths, dir+ch+".mp3")
	}
	lib := fbUnitLib([]string{"/lib"}, []string{"Dmitry Glukhovsky"}, "Metro 2033", paths, 3700)
	for i, p := range paths[1:4] {
		id := fmt.Sprintf("ch%d", i)
		d := 3700
		lib.add(database.BookCore{ID: id, Title: filepath.Base(p), Duration: &d},
			[]database.BookFileCore{{ID: id + "-0", BookID: id, FilePath: p, Duration: d}})
	}
	lib.finish()
	ev := lib.evaluate("b")
	require.Equal(t, 0, ev.Proper, "a one-hour single-file chapter book is not a proper book")
	require.False(t, fbApplicableTier(ev.Tier), "tier %q evidence %v", ev.Tier, ev.Evidence)
}

// TestFolderBooksFixer_DiscFoldersUnderTheRoot: disc folders directly in an
// author folder are one work, not a shelf of "01"/"02" stems; "Vol N"
// folders there stay separate works; a number-titled group is unclear.
func TestFolderBooksFixer_DiscFoldersUnderTheRoot(t *testing.T) {
	root := "/lib/Frank Herbert"
	for _, disc := range []string{"Dune CD1", "Dune (Disc 1)", "Dune [CD 1]", "Dune, Disc 1"} {
		k, label, dir := fbGroupKey(root+"/"+disc+"/01.mp3", root)
		require.Equal(t, "Dune", label, disc)
		require.Empty(t, dir, disc)
		k2, _, _ := fbGroupKey(root+"/"+strings.ReplaceAll(disc, "1", "2")+"/01.mp3", root)
		require.Equal(t, k, k2, "sibling discs are one work: %s", disc)
	}
	kv2, _, _ := fbGroupKey(root+"/Wheel Vol 2/01.mp3", root)
	kv3, _, _ := fbGroupKey(root+"/Wheel Vol 3/01.mp3", root)
	require.NotEqual(t, kv2, kv3, "volumes are separate works")
	k1984, l1984, _ := fbGroupKey(root+"/1984/01.mp3", root)
	require.Equal(t, "1984", l1984)
	require.True(t, strings.HasPrefix(k1984, "dir:"), "a year-titled folder is a work")

	var paths []string
	for _, d := range []string{"Dune CD1", "Dune CD2", "Dune CD3"} {
		paths = append(paths, root+"/"+d+"/01.mp3", root+"/"+d+"/02.mp3")
	}
	ev := fbUnitLib([]string{"/lib"}, []string{"Frank Herbert"}, "Frank Herbert", paths, 3600).evaluate("b")
	require.False(t, fbApplicableTier(ev.Tier), "one multi-disc book: tier %q evidence %v", ev.Tier, ev.Evidence)
	for _, title := range []string{"01", "", "CD2", "Prologue"} {
		require.True(t, fbUnclearTitle(title), title)
	}
	require.False(t, fbUnclearTitle("Dune"))
}

// TestFolderBooksFixer_ResumeAfterCrash: an apply cut off after CreateBook,
// or after k of a group's rows, resumes in the same operation into the same
// book: no second book, no stuck row.
func TestFolderBooksFixer_ResumeAfterCrash(t *testing.T) {
	for _, stop := range []struct {
		stage string
		n     int
	}{{"book", 0}, {"row", 1}} {
		t.Run(fmt.Sprintf("%s-%d", stop.stage, stop.n), func(t *testing.T) {
			f := newGlobalRootFragFixture(t)
			f.seedWolfe(t, fbITunes, "citadel")
			sw1 := f.path(filepath.Join(fbITunes, "Gene Wolfe", "Sword", "01.mp3"))
			row := f.fbSingleRow(t, "op-plan")
			fbCrashHook = func(stage string, n int) error {
				if stage == stop.stage && n == stop.n {
					return fmt.Errorf("injected stop at %s %d", stage, n)
				}
				return nil
			}
			t.Cleanup(func() { fbCrashHook = nil })
			out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
			require.Equal(t, 0, out.Applied, "%+v", out.Rows)
			require.True(t, f.live(t, "fb"), "the cut-off run never reached the retire")
			byPath, err := f.s.GetBookFileByPath(sw1)
			require.NoError(t, err)
			require.NotNil(t, byPath)
			require.Contains(t, []string{f.ids["fb"], f.ids["fb2"]}, byPath.BookID,
				"while the created book is hidden the path index names a live row")
			fbCrashHook = nil

			out = f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)
			require.False(t, f.live(t, "fb"))
			owners, err := f.s.BookFilesAtPath(sw1)
			require.NoError(t, err)
			others := map[string]bool{}
			for _, o := range owners {
				if o.BookID != f.ids["fb"] && o.BookID != f.ids["fb2"] {
					others[o.BookID] = true
				}
			}
			require.Len(t, others, 1, "one created book holds Sword/01")
			for id := range others {
				b, err := f.s.GetBookByID(id)
				require.NoError(t, err)
				require.False(t, b.IsSoftDeleted(), "the resumed apply un-hid it")
				rows, err := f.s.GetBookFiles(id)
				require.NoError(t, err)
				require.Len(t, rows, 2, "no row created twice")
			}
		})
	}
}

// TestFolderBooksFixer_PreviouslyResolvedFileIsNotRecreated: an orphan a
// soft-deleted book holds (a merge loser, a deleted book) was resolved once;
// the row skips rather than bring it back.
func TestFolderBooksFixer_PreviouslyResolvedFileIsNotRecreated(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	sw1 := f.path(filepath.Join(fbITunes, "Gene Wolfe", "Sword", "01.mp3"))
	id := f.fbBook(t, "loser", "Sword (old)", sw1, 1200, sw1)
	yes := true
	now := time.Now()
	_, err := f.s.ModifyBook(id, func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
		return nil
	})
	require.NoError(t, err)
	row := f.fbSingleRow(t, "op-plan")
	require.Equal(t, fbSkipResolved, row.Skipped, row.SkipReason)
}

// TestFolderBooksFixer_RootFolderOnlyRetires: a folder-book over the iTunes
// media root itself never creates books; it is skipped while it has orphans.
func TestFolderBooksFixer_RootFolderOnlyRetires(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	var paths []string
	for _, w := range []string{"Ann/Work One", "Bob/Work Two"} {
		for i := 1; i <= 3; i++ {
			paths = append(paths, f.file(t, filepath.Join(fbITunes, w, fmt.Sprintf("0%d.mp3", i)), 10))
		}
	}
	f.fbBook(t, "root", "iTunes Media", f.path(fbITunes), 3600, paths...)
	row := f.fbSingleRow(t, "op-plan")
	require.Equal(t, fbTierShelf, row.Class)
	require.Equal(t, fbSkipRootFolder, row.Skipped, row.SkipReason)
}

// TestFolderBooksFixer_DuplicateCreatedAfterPlan: a book with a group's
// title and author that appears between plan and apply stops the row under
// the merge lock.
func TestFolderBooksFixer_DuplicateCreatedAfterPlan(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "%s %s", row.Skipped, row.SkipReason)
	a, err := f.s.GetAuthorByName("Gene Wolfe")
	require.NoError(t, err)
	id := f.fbBook(t, "late", "Sword", f.path("Elsewhere/Sword.m4b"), 3600)
	_, err = f.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &a.ID; return nil })
	require.NoError(t, err)
	require.NoError(t, f.s.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: a.ID, Role: "author"}}))
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 0, out.Applied, "%+v", out.Rows)
	require.True(t, f.live(t, "fb"), "nothing was written")
}

// TestFolderBooksFixer_SubsetRowNeverStrandsAPresentFile: folder-books A
// (fb, fb2 over Gene Wolfe) and B (over the whole iTunes media folder, a
// superset) share Sword; A's rows say Missing and B's are present. Missing is
// per row, so A must not read Sword as missing from its own copy: it builds
// Sword from B's present row, and B, whose only keeper of Sword (A) holds no
// present copy, waits instead of retiring first and stranding the file on
// soft-deleted books.
func TestFolderBooksFixer_SubsetRowNeverStrandsAPresentFile(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	all := f.seedWolfe(t, fbITunes, "citadel")
	sw1 := f.path(filepath.Join(fbITunes, "Gene Wolfe", "Sword", "01.mp3"))
	for _, role := range []string{"fb", "fb2"} {
		for _, i := range []int{7, 8} {
			f.fbSetRow(t, role, i, func(r *database.BookFile) { r.Missing = true })
		}
	}
	var ann []string
	for i := 1; i <= 3; i++ {
		ann = append(ann, f.file(t, filepath.Join(fbITunes, "Ann", "Work One", fmt.Sprintf("0%d.mp3", i)), 10))
	}
	f.fbBook(t, "ann", "Work One", f.path(filepath.Join(fbITunes, "Ann", "Work One")), 3600, ann...)
	f.fbBook(t, "root", "iTunes Media", f.path(fbITunes), 1200, append(append([]string(nil), all...), ann...)...)

	res := f.fbPlan(t, fbFixerID, "op-plan")
	a, b := fbFindRow(res, f.ids["fb"]), fbFindRow(res, f.ids["root"])
	require.NotNil(t, a)
	require.NotNil(t, b)
	require.True(t, a.Applicable(), "%s %s", a.Skipped, a.SkipReason)
	require.Equal(t, "1", a.Proposed["new_books"], "Sword has a present copy on B: not missing")
	require.Equal(t, "0", a.Proposed["missing_orphans"])
	require.Equal(t, fbSkipOnlyPresentCopy, b.Skipped, b.SkipReason)

	out := f.fbApply(t, "op-plan", "op-apply", []string{a.RowID, b.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fb"))
	require.True(t, f.live(t, "root"), "B waited")

	// Now Sword's keeper is the created book with a present row: B retires.
	again := f.fbPlan(t, fbFixerID, "op-plan-2")
	b = fbFindRow(again, f.ids["root"])
	require.NotNil(t, b)
	require.True(t, b.Applicable(), "%s %s", b.Skipped, b.SkipReason)
	out = f.fbApply(t, "op-plan-2", "op-apply-2", []string{b.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "root"))

	owners, err := f.s.BookFilesAtPath(sw1)
	require.NoError(t, err)
	livePresent := 0
	for _, o := range owners {
		bk, err := f.s.GetBookByID(o.BookID)
		require.NoError(t, err)
		if bk != nil && !bk.IsSoftDeleted() && !o.Missing {
			livePresent++
		}
	}
	require.Equal(t, 1, livePresent, "Sword/01 keeps exactly one live, present holder")
	byPath, err := f.s.GetBookFileByPath(sw1)
	require.NoError(t, err)
	require.NotNil(t, byPath)
	bk, err := f.s.GetBookByID(byPath.BookID)
	require.NoError(t, err)
	require.False(t, bk.IsSoftDeleted(), "the path index names the live holder")
}

// TestFBCreatedPath: a disc group directly under the root and a stem group
// never get the root as file_path (every such group of a row would share
// it); a folder group gets its folder, one disc folder its folder.
func TestFBCreatedPath(t *testing.T) {
	root := "/lib/Frank Herbert"
	group := func(rels ...string) fbGroup {
		var g fbGroup
		for i, r := range rels {
			k, lbl, dir := fbGroupKey(root+"/"+r, root)
			if i == 0 {
				g.Key, g.Title, g.Dir = k, lbl, dir
			}
			require.Equal(t, g.Key, k, "one work: %v", rels)
			g.Files = append(g.Files, fbNewFile{Path: root + "/" + r})
		}
		return g
	}
	cases := []struct {
		name string
		g    fbGroup
		want string
	}{
		{"discs under root", group("Dune CD1/01.mp3", "Dune CD2/01.mp3"), root + "/Dune CD1/01.mp3"},
		{"one disc under root", group("Dune CD1/01.mp3", "Dune CD1/02.mp3"), root + "/Dune CD1"},
		{"stem", group("Children of Dune - Part 1.mp3", "Children of Dune - Part 2.mp3"),
			root + "/Children of Dune - Part 1.mp3"},
		{"single-file stem", group("God Emperor of Dune.m4b"), root + "/God Emperor of Dune.m4b"},
		{"folder", group("Heretics/01.mp3", "Heretics/Disc 2/01.mp3"), root + "/Heretics"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		got := fbCreatedPath(c.g, root)
		require.Equal(t, c.want, got, c.name)
		require.NotEqual(t, root, got, c.name)
		require.Empty(t, seen[got], "%s shares %s with %s", c.name, got, seen[got])
		seen[got] = c.name
	}
}

// TestFolderBooksFixer_DuplicateIsSymmetric: an authored group matches a
// live AUTHORLESS same-title book, and a live book with no rows sitting at
// the group's folder is that work's book, both at plan and under the merge
// lock at apply.
func TestFolderBooksFixer_DuplicateIsSymmetric(t *testing.T) {
	for _, kind := range []string{"authorless", "at-folder"} {
		for _, when := range []string{"before-plan", "after-plan"} {
			t.Run(kind+"/"+when, func(t *testing.T) {
				f := newGlobalRootFragFixture(t)
				f.seedWolfe(t, fbITunes, "citadel")
				swordDir := f.path(filepath.Join(fbITunes, "Gene Wolfe", "Sword"))
				add := func() {
					if kind == "authorless" {
						f.fbBook(t, "late", "Sword", f.path("Elsewhere/Sword.m4b"), 3600)
					} else {
						f.fbBook(t, "late", "Some Other Title", swordDir, 3600)
					}
				}
				if when == "before-plan" {
					add()
				}
				row := f.fbSingleRow(t, "op-plan")
				if when == "before-plan" {
					require.Equal(t, fbSkipDupTitle, row.Skipped, row.SkipReason)
					return
				}
				require.True(t, row.Applicable(), "%s %s", row.Skipped, row.SkipReason)
				add()
				out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
				require.Equal(t, 0, out.Applied, "%+v", out.Rows)
				require.True(t, f.live(t, "fb"), "nothing was written")
			})
		}
	}
}

// TestFolderBooksFixer_RevertReindexesAnAlreadyHiddenBook: a created book
// already hidden when the revert reaches it (a cut-off revert, an apply cut
// off before its un-hide) still has the path index handed back to a live
// row.
func TestFolderBooksFixer_RevertReindexesAnAlreadyHiddenBook(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	swordDir := f.path(filepath.Join(fbITunes, "Gene Wolfe", "Sword"))
	sw1 := filepath.Join(swordDir, "01.mp3")
	row := f.fbSingleRow(t, "op-plan")
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	created, err := f.s.GetBookByFilePath(swordDir)
	require.NoError(t, err)
	require.NotNil(t, created)
	byPath, err := f.s.GetBookFileByPath(sw1)
	require.NoError(t, err)
	require.Equal(t, created.ID, byPath.BookID, "the live created book owns the path key after apply")
	yes, now := true, time.Now()
	_, err = f.s.ModifyBook(created.ID, func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
		return nil
	})
	require.NoError(t, err)
	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	byPath, err = f.s.GetBookFileByPath(sw1)
	require.NoError(t, err)
	require.NotNil(t, byPath)
	require.Contains(t, []string{f.ids["fb"], f.ids["fb2"]}, byPath.BookID,
		"the already-hidden created book gave the path key back")
}

// TestFolderBooksFixer_StrandCheckRunsUnderTheLock: B is applicable at plan
// (A's copy of Sword is present, so A keeps it); A's rows turn Missing
// before the apply, and B's apply refuses under the merge lock instead of
// retiring the only present copy.
func TestFolderBooksFixer_StrandCheckRunsUnderTheLock(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	all := f.seedWolfe(t, fbITunes, "citadel")
	var ann []string
	for i := 1; i <= 3; i++ {
		ann = append(ann, f.file(t, filepath.Join(fbITunes, "Ann", "Work One", fmt.Sprintf("0%d.mp3", i)), 10))
	}
	f.fbBook(t, "ann", "Work One", f.path(filepath.Join(fbITunes, "Ann", "Work One")), 3600, ann...)
	f.fbBook(t, "root", "iTunes Media", f.path(fbITunes), 1200, append(append([]string(nil), all...), ann...)...)
	res := f.fbPlan(t, fbFixerID, "op-plan")
	b := fbFindRow(res, f.ids["root"])
	require.NotNil(t, b)
	require.True(t, b.Applicable(), "%s %s", b.Skipped, b.SkipReason)
	for _, role := range []string{"fb", "fb2"} {
		for _, i := range []int{7, 8} {
			f.fbSetRow(t, role, i, func(r *database.BookFile) { r.Missing = true })
		}
	}
	out := f.fbApply(t, "op-plan", "op-apply", []string{b.RowID})
	require.Equal(t, 0, out.Applied, "%+v", out.Rows)
	require.True(t, f.live(t, "root"), "B was not retired")
}

// TestFolderBooksFixer_DupNowFindsTheDuplicateAmongManySameTitleBooks: every
// same-title candidate is checked, however many there are (250 by another
// author come first), and titles match by fbNorm equality ("It!" is "It").
func TestFolderBooksFixer_DupNowFindsTheDuplicateAmongManySameTitleBooks(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	other, err := f.s.CreateAuthor("Someone Else")
	require.NoError(t, err)
	mine, err := f.s.CreateAuthor("Stephen King")
	require.NoError(t, err)
	for i := 0; i < 250; i++ {
		id := f.fbBook(t, fmt.Sprintf("it-%d", i), "It", f.path(fmt.Sprintf("Other/It %d.m4b", i)), 3600)
		_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &other.ID; return nil })
		require.NoError(t, err)
	}
	fx := &folderBooksFixer{}
	g := fbGroup{Title: "It", AuthorID: &mine.ID}
	idx, err := fx.titleIndex(f.s, "it")
	require.NoError(t, err)
	require.Len(t, idx, 250)
	why, err := fx.dupNow(f.s, nil, "", g)
	require.NoError(t, err)
	require.Empty(t, why, "another author's same-title books are not duplicates")

	late := f.fbBook(t, "late", "It!", f.path("Elsewhere/It.m4b"), 3600) // authorless, sorts after the 250
	idx, err = fx.titleIndex(f.s, "it")
	require.NoError(t, err)
	require.Len(t, idx, 251, "the create moved the generation: the index caught up")
	why, err = fx.dupNow(f.s, nil, "", g)
	require.NoError(t, err)
	require.Contains(t, why, late, "an authorless fbNorm-equal title after 250 other candidates")
}

// fbSeedAnn adds a second author shelf "Ann Author" holding two proper books
// and an orphan "Sword" folder, under an authorless folder-book: its Sword
// group is created with no author, so it duplicates Gene Wolfe's Sword.
func (f *fragFixture) fbSeedAnn(t *testing.T) {
	t.Helper()
	rel := func(s string) string { return filepath.Join(fbITunes, "Ann Author", s) }
	var one, two, sword []string
	for i := 1; i <= 3; i++ {
		one = append(one, f.file(t, rel(fmt.Sprintf("One/0%d.mp3", i)), 10))
		two = append(two, f.file(t, rel(fmt.Sprintf("Two/0%d.mp3", i)), 10))
	}
	for i := 1; i <= 2; i++ {
		sword = append(sword, f.file(t, rel(fmt.Sprintf("Sword/0%d.mp3", i)), 10))
	}
	f.fbBook(t, "ann-one", "Book One", f.path(rel("One")), 3600, one...)
	f.fbBook(t, "ann-two", "Book Two", f.path(rel("Two")), 3600, two...)
	_, err := f.s.CreateAuthor("Ann Author")
	require.NoError(t, err)
	f.fbBook(t, "ann", "Ann Author", f.path(filepath.Join(fbITunes, "Ann Author")), 1200,
		append(append(append([]string(nil), one...), two...), sword...)...)
}

// liveTitled counts live books whose fbNorm title is title.
func (f *fragFixture) liveTitled(t *testing.T, title string) int {
	t.Helper()
	all, err := f.s.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	n := 0
	for _, b := range all {
		if !b.IsSoftDeleted() && fbNorm(b.Title) == fbNorm(title) {
			n++
		}
	}
	return n
}

// TestFolderBooksFixer_TwoRowsOfOneApplyCreateOneTitle: two rows of ONE
// apply both plan a new "Sword"; the second, under the merge lock, sees the
// first's book (its create and un-hide moved the library generation, so the
// shared title index is rebuilt) and refuses.
func TestFolderBooksFixer_TwoRowsOfOneApplyCreateOneTitle(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	f.fbSeedAnn(t)
	res := f.fbPlan(t, fbFixerID, "op-plan")
	a, b := fbFindRow(res, f.ids["fb"]), fbFindRow(res, f.ids["ann"])
	require.NotNil(t, a)
	require.NotNil(t, b)
	require.True(t, a.Applicable(), "%s %s", a.Skipped, a.SkipReason)
	require.True(t, b.Applicable(), "%s %s", b.Skipped, b.SkipReason)
	require.Zero(t, f.liveTitled(t, "Sword"))
	out := f.fbApply(t, "op-plan", "op-apply", []string{a.RowID, b.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.Equal(t, 1, f.liveTitled(t, "Sword"), "one Sword, not two")
}

// TestFolderBooksFixer_RetitleAfterTheIndexIsCaught: the shared title index
// was built (an earlier row) before another merge-lock holder retitled a
// book to the group's title; the generation moved, so the apply rebuilds the
// index under the lock and refuses.
func TestFolderBooksFixer_RetitleAfterTheIndexIsCaught(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	id := f.fbBook(t, "renamed", "Something Else", f.path("Elsewhere/x.m4b"), 3600)
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "%s %s", row.Skipped, row.SkipReason)
	fixer, ok := f.p.repairsReg.Get(fbFixerID)
	require.True(t, ok)
	fx := fixer.(*folderBooksFixer)
	idx, err := fx.titleIndex(f.s, "sword")
	require.NoError(t, err)
	require.Empty(t, idx)
	_, err = f.s.ModifyBook(id, func(b *database.Book) error { b.Title = "Sword"; return nil })
	require.NoError(t, err)
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 0, out.Applied, "%+v", out.Rows)
	require.True(t, f.live(t, "fb"), "nothing was written")
}

// TestFolderBooksFixer_CreatedRowChangedBeforeClaim: a created row moved off
// its path, or gone, after the un-hide is not re-indexed; the row stops.
func TestFolderBooksFixer_CreatedRowChangedBeforeClaim(t *testing.T) {
	for _, how := range []string{"moved", "vanished"} {
		t.Run(how, func(t *testing.T) {
			f := newGlobalRootFragFixture(t)
			f.seedWolfe(t, fbITunes, "citadel")
			swordDir := f.path(filepath.Join(fbITunes, "Gene Wolfe", "Sword"))
			row := f.fbSingleRow(t, "op-plan")
			hit := false
			fbCrashHook = func(stage string, n int) error {
				if stage != "claim" || n != 1 || hit {
					return nil
				}
				hit = true
				created, err := f.s.GetBookByFilePath(swordDir)
				if err != nil || created == nil {
					return fmt.Errorf("created book: %v", err)
				}
				rows, err := f.s.GetBookFiles(created.ID)
				if err != nil || len(rows) == 0 {
					return fmt.Errorf("created rows: %v", err)
				}
				r := rows[0]
				if how == "vanished" {
					return f.s.DeleteBookFile(r.ID)
				}
				r.FilePath = f.path("Elsewhere/moved.mp3")
				return f.s.UpdateBookFile(r.ID, &r)
			}
			t.Cleanup(func() { fbCrashHook = nil })
			out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
			require.True(t, hit)
			require.Equal(t, 0, out.Applied, "%+v", out.Rows)
			require.Equal(t, 1, out.Partial, "%+v", out.Rows)
			require.True(t, f.live(t, "fb"), "the row stopped before the retire")
		})
	}
}

// TestFolderBooksFixer_SourceRowChangedBeforeReindex: the source row a new
// row was copied from vanishes, or moves off the path, before the apply
// hands its path key back: the row stops (changed since plan) rather than
// re-index a row that no longer names the file.
func TestFolderBooksFixer_SourceRowChangedBeforeReindex(t *testing.T) {
	for _, how := range []string{"vanished", "moved"} {
		t.Run(how, func(t *testing.T) {
			f := newGlobalRootFragFixture(t)
			f.seedWolfe(t, fbITunes, "citadel")
			row := f.fbSingleRow(t, "op-plan")
			hit := false
			fbCrashHook = func(stage string, n int) error {
				if stage != "reindex" || n != 1 || hit {
					return nil
				}
				hit = true
				for _, role := range []string{"fb", "fb2"} {
					rid := f.rowIDs[role+"-7"]
					if how == "vanished" {
						if err := f.s.DeleteBookFile(rid); err != nil {
							return err
						}
						continue
					}
					r, err := f.s.GetBookFileByID(f.ids[role], rid)
					if err != nil || r == nil {
						return fmt.Errorf("read %s: %v", rid, err)
					}
					r.FilePath = f.path("Elsewhere/moved.mp3")
					if err := f.s.UpdateBookFile(r.ID, r); err != nil {
						return err
					}
				}
				return nil
			}
			t.Cleanup(func() { fbCrashHook = nil })
			out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
			require.True(t, hit)
			require.Equal(t, 0, out.Applied, "%+v", out.Rows)
			require.Equal(t, 1, out.Partial, "%+v", out.Rows)
			require.True(t, f.live(t, "fb"), "the row stopped before the retire")
		})
	}
}
