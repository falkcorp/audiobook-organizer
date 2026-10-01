// file: internal/plugins/maintenance/folder_books_fixer_test.go
// version: 1.0.0
// guid: 9d4c7a2e-1b6f-4e83-a5d0-8f2b3c6e9a17
// last-edited: 2026-10-01

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
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

// seedWolfe builds an author shelf "Gene Wolfe" holding three works: two
// proper books (Shadow, Claw), one work nobody else holds (Sword: 01 an
// orphan, 02 also a single-file book G), and two folder-books over all of it
// (FB primary, FB2 an exact duplicate) in one version group with Shadow.
func (f *fragFixture) seedWolfe(t *testing.T) (all []string) {
	t.Helper()
	var shadow, claw, sword []string
	for i := 1; i <= 3; i++ {
		shadow = append(shadow, f.file(t, fmt.Sprintf("Gene Wolfe/Shadow/0%d.mp3", i), 10))
		claw = append(claw, f.file(t, fmt.Sprintf("Gene Wolfe/Claw/0%d.mp3", i), 10))
	}
	for i := 1; i <= 2; i++ {
		sword = append(sword, f.file(t, fmt.Sprintf("Gene Wolfe/Sword/0%d.mp3", i), 10))
	}
	f.fbBook(t, "shadow", "The Shadow of the Torturer", f.path("Gene Wolfe/Shadow"), 3600, shadow...)
	f.fbBook(t, "claw", "The Claw of the Conciliator", f.path("Gene Wolfe/Claw"), 3600, claw...)
	f.fbBook(t, "g", "Sword 02", sword[1], 1200, sword[1])
	all = append(append(append(all, shadow...), claw...), sword...)
	author, err := f.s.CreateAuthor("Gene Wolfe")
	require.NoError(t, err)
	for _, role := range []string{"fb", "fb2"} {
		id := f.fbBook(t, role, "Gene Wolfe", f.path("Gene Wolfe"), 1200, all...)
		_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &author.ID; return nil })
		require.NoError(t, err)
	}
	f.setVG(t, f.ids["fb"], "vg-wolfe", true)
	f.setVG(t, f.ids["fb2"], "vg-wolfe", false)
	f.setVG(t, f.ids["shadow"], "vg-wolfe", false)
	f.organized(t, f.ids["shadow"])
	return all
}

func TestFolderBooksFixer_ApplyAndRevert(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t)
	fb, fb2 := f.ids["fb"], f.ids["fb2"]

	// The fragment fixer sees the live folder-books as parents of G's file.
	before := f.fbPlan(t, fragFixerID, "op-frag-before")
	require.NotNil(t, fbFindRow(before, fb), "a live folder-book is a fragment-consolidation parent")

	res := f.fbPlan(t, fbFixerID, "op-plan")
	require.Len(t, res.Rows, 1)
	row := res.Rows[0]
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
		require.Len(t, rows, 8, "a retired folder-book keeps its rows")
	}
	sh, err := f.s.GetBookByID(f.ids["shadow"])
	require.NoError(t, err)
	require.True(t, sh.IsPrimaryVersion != nil && *sh.IsPrimaryVersion, "primacy handed to the real book")
	// One new book over Sword/01, credited to the shelf's author.
	owners, err := f.s.BookFilesAtPath(f.path("Gene Wolfe/Sword/01.mp3"))
	require.NoError(t, err)
	var created *database.Book
	for _, o := range owners {
		if o.BookID != fb && o.BookID != fb2 {
			created, err = f.s.GetBookByID(o.BookID)
			require.NoError(t, err)
		}
	}
	require.NotNil(t, created)
	require.Equal(t, "Sword", created.Title)
	require.False(t, created.IsSoftDeleted())
	require.NotNil(t, created.AuthorID)
	require.Equal(t, rowsBefore+1, fbRowCount(t, f.s), "only the new book's row was added")

	// Re-plan: nothing left to flag.
	again := f.fbPlan(t, fbFixerID, "op-plan-2")
	require.Empty(t, again.Rows)
	// The fragment fixer no longer treats the retired folder-books as parents.
	after := f.fbPlan(t, fragFixerID, "op-frag-after")
	require.Nil(t, fbFindRow(after, fb))
	require.Nil(t, fbFindRow(after, fb2))

	// Revert: folder-books live again, FB re-crowned, created book hidden,
	// no row deleted.
	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.True(t, f.live(t, "fb"))
	require.True(t, f.live(t, "fb2"))
	b, err := f.s.GetBookByID(fb)
	require.NoError(t, err)
	require.Equal(t, f.path("Gene Wolfe"), b.FilePath)
	require.True(t, b.IsPrimaryVersion == nil || *b.IsPrimaryVersion, "FB is primary again")
	sh, err = f.s.GetBookByID(f.ids["shadow"])
	require.NoError(t, err)
	require.False(t, sh.IsPrimaryVersion == nil || *sh.IsPrimaryVersion)
	created, err = f.s.GetBookByID(created.ID)
	require.NoError(t, err)
	require.True(t, created.IsSoftDeleted(), "the created book is hidden by the revert")
	require.Equal(t, rowsBefore+1, fbRowCount(t, f.s), "the revert deletes no row")
}

// TestFolderBooksFixer_Resume: a book an earlier cut-off apply created for a
// group is extended, not created twice.
func TestFolderBooksFixer_Resume(t *testing.T) {
	f := newFragFixture(t)
	f.seedWolfe(t)
	// "mine" stands for the book a cut-off apply created for Sword: it holds
	// Sword/01 and carries an unreverted repair_book_create row.
	mine := f.fbBook(t, "mine", "Sword", f.path("Gene Wolfe/Sword/01.mp3"), 1200, f.path("Gene Wolfe/Sword/01.mp3"))
	require.NoError(t, f.s.CreateOperationChange(&database.OperationChange{ID: "chg-create", OperationID: "op-old",
		BookID: mine, ChangeType: undo.ChangeTypeRepairBookCreate, FieldName: "book", NewValue: mine}))
	res := f.fbPlan(t, fbFixerID, "op-plan")
	require.Len(t, res.Rows, 1)
	// Sword/01 is held by "mine"; Sword/02 by G: nothing orphaned.
	require.Equal(t, "0", res.Rows[0].Proposed["new_books"])
	require.Equal(t, "8", res.Rows[0].Proposed["held"])
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

	// Fragmentary: a flat folder of separately titled one-minute tracks.
	var tracks []string
	for _, n := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"} {
		tracks = append(tracks, f.file(t, "Narrator X/"+n+".mp3", 10))
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
	k1, _ := fbGroupKey("/r/A/Book/CD1/01.mp3", "/r/A")
	k2, _ := fbGroupKey("/r/A/Book/CD2/01.mp3", "/r/A")
	require.Equal(t, k1, k2, "disc folders group with their parent")
	k3, l3 := fbGroupKey("/r/A/Title - 01.mp3", "/r/A")
	k4, _ := fbGroupKey("/r/A/Title - 02.mp3", "/r/A")
	require.Equal(t, k3, k4)
	require.Equal(t, "Title", l3)
}
