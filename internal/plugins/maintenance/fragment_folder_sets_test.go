// file: internal/plugins/maintenance/fragment_folder_sets_test.go
// version: 1.9.0
// guid: 3b7d2c55-1a4e-4f0b-9c61-8e2f5d7a0b14
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/stretchr/testify/require"
)

// fragFolderSetShapeCases are shared with the review script
// (chapterset2/folder_chapter_sets.py asserts the same table): stem, the
// varying slots, the author names, then the name key and the title.
var fragFolderSetShapeCases = []struct {
	stem    string
	varying []int
	authors []string
	key     string
	title   string
}{
	{"Jim Butcher - Turn Coat - 27 1", []int{0}, []string{"Jim Butcher"}, "jim butcher turn coat # #", "Turn Coat"},
	{"Metro 2034 - 03 Chapter 3", []int{1, 2}, nil, "metro # # chapter #", "Metro 2034"},
	{"Emissaries from the Dead (Unabridged) Part 102-Chapter 2", []int{0, 1}, nil,
		"emissaries from the dead unabridged part # chapter #", "Emissaries from the Dead (Unabridged)"},
	{"The Trapped Mind Project - 02 - Part 1 - Chapter 01", []int{0, 1, 2}, nil,
		"the trapped mind project # part # chapter #", "The Trapped Mind Project"},
	{"Beyond The Shadows 01 Night Angel Trilogy 3 - Brent Weeks", []int{0}, []string{"Brent Weeks"},
		"beyond the shadows # night angel trilogy # brent weeks", "Beyond The Shadows Night Angel Trilogy 3"},
	{"Chapter 07", []int{0}, nil, "chapter #", ""},
	{"Blood Music 2-1", []int{0, 1}, nil, "blood music # #", "Blood Music"},
	{"My_Vampire_System_1001_1200", []int{0, 1}, nil, "my vampire system # #", "My Vampire System"},
	{"Christopher Paolini - Some Work - 04 1", []int{0}, []string{"Christopher Paolini"}, "christopher paolini some work # #", "Some Work"},
	{"Track 12 - read by narrator", []int{0}, nil, "track # read by narrator", ""},
	{"Café Stories #4 (Part 2)", []int{1}, nil, "café stories ## part #", "Café Stories #4"},
	{"Before They Are Hanged 151 of 341", []int{0}, []string{"Joe Abercrombie"}, "before they are hanged # of #", "Before They Are Hanged"},
	{"195-299 Kevin J Anderson", []int{0, 1}, []string{"Kevin J Anderson"}, "# # kevin j anderson", ""},
}

func TestFragFolderSetShape(t *testing.T) {
	t.Parallel()
	for _, tc := range fragFolderSetShapeCases {
		sh, ok := fragFolderSetShape(tc.stem)
		require.True(t, ok, tc.stem)
		require.Equal(t, tc.key, sh.key, tc.stem)
		got, _ := fragSetTitle(tc.stem, sh, tc.varying, tc.authors, tc.authors)
		require.Equal(t, tc.title, got, tc.stem)
	}
	_, ok := fragFolderSetShape("No Numbers Here")
	require.False(t, ok)
}

func TestFragNumberGaps(t *testing.T) {
	t.Parallel()
	single := func(ns ...int) [][2]int {
		var out [][2]int
		for _, n := range ns {
			out = append(out, [2]int{0, n})
		}
		return out
	}
	gaps, desc := fragNumberGaps(single(3, 1, 2, 4))
	require.Empty(t, gaps)
	require.Equal(t, "1–4", desc)
	gaps, _ = fragNumberGaps(single(0, 1, 2))
	require.Empty(t, gaps, "a run from 0")
	gaps, _ = fragNumberGaps(single(2, 3, 4))
	require.Equal(t, []string{"1"}, gaps, "a missing first chapter is a gap")
	gaps, _ = fragNumberGaps(single(1, 2, 2, 5))
	require.Equal(t, []string{"3", "4"}, gaps, "a repeat is no gap; 3 and 4 are")
	gaps, _ = fragNumberGaps([][2]int{{1, 1}, {1, 2}, {2, 1}, {2, 2}})
	require.Empty(t, gaps, "discs restarting")
	gaps, _ = fragNumberGaps([][2]int{{1, 1}, {1, 2}, {2, 3}, {2, 4}})
	require.Empty(t, gaps, "discs running on")
	gaps, _ = fragNumberGaps([][2]int{{1, 1}, {1, 2}, {3, 1}})
	require.Equal(t, []string{"disc 2"}, gaps)
}

// folderSet seeds n organized single-file fragment books under dir named by
// stem(i) for i in nums, each dur seconds, sizes base+101*i. It returns the
// ids in nums order.
func (f *fragFixture) folderSet(t *testing.T, dir string, nums []int, stem func(int) string, dur, base int) []string {
	t.Helper()
	var ids []string
	for _, i := range nums {
		s := stem(i)
		size := base + 101*i
		p := f.file(t, filepath.Join(dir, s+".mp3"), size)
		id := f.book(t, dir+s, s, p, nil)
		f.row(t, dir+s, id, p, s+".mp3", int64(size), dur, 0)
		f.organized(t, id)
		ids = append(ids, id)
	}
	return ids
}

func seq(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

const setDir = "lib/Christopher Paolini/Some Work"

func someWorkStem(i int) string { return fmt.Sprintf("Christopher Paolini - Some Work - %02d 1", i) }

func setRowID(f *fragFixture, dir, name string) string {
	return noParentRowID(f.path(dir), fragFolderSetKeyPrefix+name)
}

const someWorkKey = "christopher paolini some work # #"

// rowsHolding counts the plan's rows that list id among their books.
func rowsHolding(res *repairs.PlanResult, id string) int {
	n := 0
	for _, r := range res.Rows {
		for _, b := range r.BookIDs {
			if b == id {
				n++
				break
			}
		}
	}
	return n
}

// TestFragmentFixer_FolderChapterSet: numbered files of one name in one
// folder, which the chapter-key groups leave as lone chapters, become one
// book, in number order, titled from the file names.
func TestFragmentFixer_FolderChapterSet(t *testing.T) {
	t.Parallel()
	t.Run("a set applies as one book in number order", func(t *testing.T) {
		f := newFragFixture(t)
		// Created out of order: the number, not the id, orders the tracks.
		ids := f.folderSet(t, setDir, []int{4, 1, 6, 2, 5, 3}, someWorkStem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, setRowID(f, setDir, someWorkKey))
		require.Equal(t, fragClassFolderSet, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, repairs.RiskReview, r.Risk)
		require.ElementsMatch(t, ids, r.BookIDs)
		require.Equal(t, "Some Work", r.Proposed["title"])
		ev := strings.Join(r.Evidence, "\n")
		require.Contains(t, ev, "numbered 1–6; no gaps")
		for _, id := range ids {
			require.Equal(t, 1, rowsHolding(res, id), "every fragment ends in exactly one row")
		}
		require.Equal(t, 1, res.ByClass[fragClassFolderSet])

		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		survivor := r.Proposed["survivor"]
		rows, err := f.s.GetBookFiles(survivor)
		require.NoError(t, err)
		require.Len(t, rows, 6, "one book holds every chapter")
		for _, row := range rows {
			var n int
			_, err := fmt.Sscanf(filepath.Base(row.FilePath), "Christopher Paolini - Some Work - %02d 1.mp3", &n)
			require.NoError(t, err)
			require.Equal(t, n, row.TrackNumber, "track follows the chapter number: %s", row.FilePath)
		}
		b, err := f.s.GetBookByID(survivor)
		require.NoError(t, err)
		require.Equal(t, "Some Work", b.Title)
		for _, id := range ids {
			if id == survivor {
				continue
			}
			gb, err := f.s.GetBookByID(id)
			require.NoError(t, err)
			require.True(t, gb.IsSoftDeleted(), "emptied fragment retired")
			require.NotNil(t, gb.MergedIntoBookID)
			require.Equal(t, survivor, *gb.MergedIntoBookID)
		}
	})
	t.Run("a gap is held and named", func(t *testing.T) {
		f := newFragFixture(t)
		f.folderSet(t, setDir, []int{1, 2, 4, 5, 6, 7}, someWorkStem, 900, 9000)
		r := findRow(t, f.plan(t, "op-plan"), setRowID(f, setDir, someWorkKey))
		require.Equal(t, fragClassFolderSet, r.Class)
		require.Equal(t, fragSkipChapterGaps, r.Skipped)
		require.Contains(t, r.SkipReason, "gaps: 3 missing")
	})
	t.Run("disc numbering in the hundreds is an explained gap", func(t *testing.T) {
		f := newFragFixture(t)
		// The Emissaries shape: "Part 102-Chapter 2", the part in the hundreds.
		stem := func(i int) string { return fmt.Sprintf("Some Work (Unabridged) Part %d-Chapter %d", i, i%100) }
		f.folderSet(t, setDir, []int{101, 102, 103, 201, 202, 203}, stem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, setRowID(f, setDir, "some work unabridged part # chapter #"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Contains(t, strings.Join(r.Evidence, "\n"), "disc in the hundreds")
		require.Equal(t, "Some Work (Unabridged)", r.Proposed["title"])
	})
	t.Run("disc numbering with a missing track names the track", func(t *testing.T) {
		f := newFragFixture(t)
		stem := func(i int) string { return fmt.Sprintf("Some Work (Unabridged) Part %d-Chapter %d", i, i%100) }
		f.folderSet(t, setDir, []int{102, 103, 201, 202, 203}, stem, 1200, 90000)
		r := findRow(t, f.plan(t, "op-plan"), setRowID(f, setDir, "some work unabridged part # chapter #"))
		require.Equal(t, fragSkipChapterGaps, r.Skipped)
		require.Contains(t, r.SkipReason, "gaps: 1-01 missing")
	})
	t.Run("under an hour is held", func(t *testing.T) {
		f := newFragFixture(t)
		f.folderSet(t, setDir, seq(1, 6), someWorkStem, 300, 9000)
		r := findRow(t, f.plan(t, "op-plan"), setRowID(f, setDir, someWorkKey))
		require.Equal(t, fragSkipSetShort, r.Skipped)
	})
	t.Run("iTunes sets are their own class, never applicable", func(t *testing.T) {
		f := newFragFixture(t)
		dir := "books/itunes/iTunes Media/Audiobooks/Christopher Paolini"
		f.folderSet(t, dir, seq(1, 6), someWorkStem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, setRowID(f, dir, someWorkKey))
		require.Equal(t, fragClassITunesSet, r.Class)
		require.Equal(t, repairs.SkipITunes, r.Skipped)
		require.False(t, r.Applicable())
		require.Zero(t, res.Applicable)
	})
	t.Run("a book in the folder that is no fragment holds the set", func(t *testing.T) {
		f := newFragFixture(t)
		f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		parent := f.book(t, "parent", "Something Else", f.path(setDir), nil)
		for i := 1; i <= 2; i++ {
			p := f.file(t, filepath.Join(setDir, fmt.Sprintf("other %d.mp3", i)), 3000+i)
			f.row(t, fmt.Sprintf("o%d", i), parent, p, fmt.Sprintf("other %d.mp3", i), int64(3000+i), 1800, i)
		}
		r := findRow(t, f.plan(t, "op-plan"), setRowID(f, setDir, someWorkKey))
		require.Equal(t, fragSkipParentInFolder, r.Skipped)
		require.Contains(t, r.SkipReason, parent)
	})
	t.Run("the same audio elsewhere holds the set", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		// A renamed copy of chapter 3 under another book: same size and
		// duration, different name and folder.
		size := int64(9000 + 101*3)
		other := f.book(t, "other", "Unrelated Name", f.path("lib/Elsewhere/Unrelated"), nil)
		for i, name := range []string{"x1.mp3", "x2.mp3"} {
			p := f.file(t, filepath.Join("lib/Elsewhere/Unrelated", name), int(size)+i*7)
			f.row(t, "x"+name, other, p, name, size+int64(i*7), 900+i*60, i+1)
		}
		r := findRow(t, f.plan(t, "op-plan"), setRowID(f, setDir, someWorkKey))
		require.Equal(t, fragSkipDuplicateAudio, r.Skipped)
		require.Contains(t, r.SkipReason, other)
		require.ElementsMatch(t, ids, r.BookIDs, "the other book is named, never written")
	})
	t.Run("version group with a book outside the set holds it", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		p := f.file(t, "lib/Elsewhere/Some Work.m4b", 4321)
		whole := f.book(t, "whole", "Some Work Whole", p, nil)
		f.row(t, "w", whole, p, "Some Work.m4b", 4321, 5400, 0)
		g := "vg-1"
		for _, id := range []string{ids[0], whole} {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &g; return nil })
			require.NoError(t, err)
		}
		r := findRow(t, f.plan(t, "op-plan"), setRowID(f, setDir, someWorkKey))
		require.Equal(t, fragSkipVersionGroupParent, r.Skipped)
		require.Contains(t, r.SkipReason, whole)
	})
}

// TestFragmentFixer_FolderChapterSetNeverDuplicates is the 2026-10-05
// incident ("Book 2 - Eldest", 347 fragments, beside "Eldest"): a folder
// chapter set whose work is already a live book is never assembled into a
// second one, at plan time or under the apply's re-check.
func TestFragmentFixer_FolderChapterSetNeverDuplicates(t *testing.T) {
	t.Parallel()
	t.Run("totals agree: joined into the existing book", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 6, 900)
		frags := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		res := f.plan(t, "op-plan")
		noRow(t, res, setRowID(f, setDir, someWorkKey), "never assembled beside the existing book")
		r := findRow(t, res, existingRowID(f.path(setDir), fragFolderSetKeyPrefix+someWorkKey))
		require.Equal(t, fragClassExistingBook, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, existing, r.Proposed["join"])
		require.Contains(t, strings.Join(r.Evidence, "\n"), "folder chapter set: 6 files")
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		f.requireRetiredInto(t, frags, existing)
	})
	t.Run("totals disagree: held", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 3, 900)
		f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(setDir), fragFolderSetKeyPrefix+someWorkKey))
		require.Equal(t, fragSkipExistingBook, r.Skipped)
		require.Contains(t, r.SkipReason, existing)
		require.Zero(t, res.Applicable)
	})
	t.Run("a book of the title created after the plan refuses the apply", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, setRowID(f, setDir, someWorkKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		f.existingBook(t, "late", "Some Work", "lib/Other/Some Work", 6, 900)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
		for _, id := range ids {
			rows, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			require.Len(t, rows, 1, "nothing moved")
			require.True(t, f.liveID(t, id), "nothing retired")
		}
	})
}

func (f *fragFixture) liveID(t *testing.T, id string) bool {
	t.Helper()
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	return !b.IsSoftDeleted()
}

const parentDir = "lib/Kevin J Anderson/Horizon Storms"

func horizonStem(i int) string { return fmt.Sprintf("Horizon Storms %03d of 006", i) }

// parentSet seeds one-file-per-folder chapters under parent: each file in a
// folder of its own named like it, as organize left them.
func (f *fragFixture) parentSet(t *testing.T, parent string, nums []int, stem func(int) string, dur, base int) []string {
	t.Helper()
	var ids []string
	for _, i := range nums {
		s := stem(i)
		size := base + 101*i
		p := f.file(t, filepath.Join(parent, s, s+".mp3"), size)
		id := f.book(t, parent+s, s, p, nil)
		f.row(t, parent+s, id, p, s+".mp3", int64(size), dur, 0)
		f.organized(t, id)
		ids = append(ids, id)
	}
	return ids
}

func parentRowID(f *fragFixture, dir, name string) string {
	return noParentRowID(f.path(dir), fragParentSetKeyPrefix+name)
}

const horizonKey = "horizon storms # of #"

// TestFragmentFixer_ParentChapterSet: one-file-per-folder chapters are
// grouped by the folder above (owner decision 2026-10-05 20:45).
func TestFragmentFixer_ParentChapterSet(t *testing.T) {
	t.Parallel()
	t.Run("a parent set with no existing book becomes one new book", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.parentSet(t, parentDir, []int{3, 1, 6, 2, 5, 4}, horizonStem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, parentRowID(f, parentDir, horizonKey))
		require.Equal(t, fragClassParentSet, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.ElementsMatch(t, ids, r.BookIDs)
		require.Equal(t, "Horizon Storms", r.Proposed["title"])
		require.Contains(t, r.Evidence[0], "one per folder")
		for _, id := range ids {
			require.Equal(t, 1, rowsHolding(res, id), "every fragment ends in exactly one row")
		}
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		rows, err := f.s.GetBookFiles(r.Proposed["survivor"])
		require.NoError(t, err)
		require.Len(t, rows, 6)
		for _, row := range rows {
			var n int
			_, err := fmt.Sscanf(filepath.Base(row.FilePath), "Horizon Storms %03d of 006.mp3", &n)
			require.NoError(t, err)
			require.Equal(t, n, row.TrackNumber)
		}
	})
	t.Run("same title and agreeing total: joins the existing book", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "hs", "Horizon Storms", "lib/Other/Horizon Storms", 6, 900)
		f.setAuthor(t, existing, f.authorID(t, "Kevin J Anderson"))
		frags := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		res := f.plan(t, "op-plan")
		noRow(t, res, parentRowID(f, parentDir, horizonKey), "never a second book")
		r := findRow(t, res, existingRowID(f.path(parentDir), fragParentSetKeyPrefix+horizonKey))
		require.Equal(t, fragClassParentJoin, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, existing, r.Proposed["join"])
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		f.requireRetiredInto(t, frags, existing)
	})
	t.Run("audio held by an existing book under another title: joins it", func(t *testing.T) {
		f := newFragFixture(t)
		frags := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		// The same six files (same size and duration) as another book's rows.
		other := f.book(t, "copy", "Some Other Name", f.path("lib/Elsewhere/Copy"), nil)
		for i := 1; i <= 6; i++ {
			name := fmt.Sprintf("track %02d.mp3", i)
			p := f.file(t, filepath.Join("lib/Elsewhere/Copy", name), 9000+101*i)
			f.row(t, name, other, p, name, int64(9000+101*i), 900, i)
		}
		f.organized(t, other)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, existingRowID(f.path(parentDir), fragParentSetKeyPrefix+horizonKey))
		require.Equal(t, fragClassParentJoin, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, other, r.Proposed["join"])
		ev := strings.Join(r.Evidence, "\n")
		require.Contains(t, ev, "6 of the set's 6 files are audio")
		require.Contains(t, ev, "audio join (not a title match): all 6 of the set's 6 files' audio is held by book "+other)
		require.Contains(t, ev, `titles: set "Horizon Storms", book "Some Other Name"`)
		require.NotContains(t, ev, "a live book of this title already exists", "an audio join never claims a title match")
		require.Contains(t, r.Reason, "copies of the audio")
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		f.requireRetiredInto(t, frags, other)
	})
	t.Run("audio held for only part of the set: held, nothing joined", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		other := f.book(t, "copy", "Some Other Name", f.path("lib/Elsewhere/Copy"), nil)
		for i := 1; i <= 3; i++ {
			name := fmt.Sprintf("track %02d.mp3", i)
			p := f.file(t, filepath.Join("lib/Elsewhere/Copy", name), 9000+101*i)
			f.row(t, name, other, p, name, int64(9000+101*i), 900, i)
		}
		f.organized(t, other)
		res := f.plan(t, "op-plan")
		noRow(t, res, existingRowID(f.path(parentDir), fragParentSetKeyPrefix+horizonKey), "never a partial join")
		r := findRow(t, res, parentRowID(f, parentDir, horizonKey))
		require.Equal(t, fragClassParentSet, r.Class)
		require.Equal(t, fragSkipDuplicateAudio, r.Skipped)
		require.Contains(t, r.SkipReason, "3 of the set's 6 files")
		require.Contains(t, r.SkipReason, "the other 3 file(s)' audio is in no other book")
		require.ElementsMatch(t, ids, r.BookIDs, "the target is named, never a member")
		require.Zero(t, res.Applicable)
	})
	t.Run("audio join with a different author: held", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		for _, id := range ids {
			f.setAuthor(t, id, f.authorID(t, "Kevin J Anderson"))
		}
		other := f.book(t, "copy", "Some Other Name", f.path("lib/Elsewhere/Copy"), nil)
		for i := 1; i <= 6; i++ {
			name := fmt.Sprintf("track %02d.mp3", i)
			p := f.file(t, filepath.Join("lib/Elsewhere/Copy", name), 9000+101*i)
			f.row(t, name, other, p, name, int64(9000+101*i), 900, i)
		}
		f.organized(t, other)
		f.setAuthor(t, other, f.authorID(t, "Someone Else Entirely"))
		res := f.plan(t, "op-plan")
		noRow(t, res, existingRowID(f.path(parentDir), fragParentSetKeyPrefix+horizonKey), "no join across authors")
		r := findRow(t, res, parentRowID(f, parentDir, horizonKey))
		require.Equal(t, fragSkipDuplicateAudio, r.Skipped)
		require.Contains(t, r.SkipReason, "differ")
	})
	t.Run("never two books of one title: two parent folders, different lengths, both held", func(t *testing.T) {
		f := newFragFixture(t)
		f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		other := "lib/Unknown Author/Horizon Storms"
		f.parentSet(t, other, seq(1, 6), horizonStem, 960, 50000)
		res := f.plan(t, "op-plan")
		for _, d := range []string{parentDir, other} {
			r := findRow(t, res, parentRowID(f, d, horizonKey))
			require.Equal(t, fragSkipSameTitleSet, r.Skipped, d)
		}
		require.Zero(t, res.Applicable)
	})
	t.Run("never one book of two: different audio at one position is held", func(t *testing.T) {
		f := newFragFixture(t)
		f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		// A second chapter 3 with other audio, in a folder of its own.
		s := horizonStem(3)
		p := f.file(t, filepath.Join(parentDir, s+" (2)", s+".mp3"), 77777)
		id := f.book(t, "dup3", s, p, nil)
		f.row(t, "dup3", id, p, s+".mp3", 77777, 1300, 0)
		f.organized(t, id)
		r := findRow(t, f.plan(t, "op-plan"), parentRowID(f, parentDir, horizonKey))
		require.False(t, r.Applicable())
		require.Equal(t, fragSkipTrackOrder, r.Skipped)
	})
	t.Run("never one book of two: two authors are held", func(t *testing.T) {
		f := newFragFixture(t)
		ids := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		f.setAuthor(t, ids[0], f.authorID(t, "Kevin J Anderson"))
		f.setAuthor(t, ids[1], f.authorID(t, "Someone Different"))
		r := findRow(t, f.plan(t, "op-plan"), parentRowID(f, parentDir, horizonKey))
		require.Equal(t, fragSkipMixedAuthors, r.Skipped)
	})
	t.Run("an author parent folder's name is never matched as the work's title", func(t *testing.T) {
		f := newFragFixture(t)
		const dir = "lib/Joe Writer"
		stem := func(i int) string { return fmt.Sprintf("Some Saga %03d of 006", i) }
		f.parentSet(t, dir, seq(1, 6), stem, 900, 9000)
		// A junk book titled after the author, of the same total.
		junk := f.existingBook(t, "junk", "Joe Writer", "lib/Elsewhere/Joe Writer", 6, 900)
		f.setAuthor(t, junk, f.authorID(t, "Joe Writer"))
		r := findRow(t, f.plan(t, "op-plan"), parentRowID(f, dir, "some saga # of #"))
		require.Equal(t, fragClassParentSet, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, "Some Saga", r.Proposed["title"])
		require.NotContains(t, r.BookIDs, junk)
	})
	t.Run("iTunes parent sets are listed, never applicable", func(t *testing.T) {
		f := newFragFixture(t)
		d := "books/itunes/iTunes Media/Audiobooks/Horizon Storms"
		f.parentSet(t, d, seq(1, 6), horizonStem, 900, 9000)
		r := findRow(t, f.plan(t, "op-plan"), parentRowID(f, d, horizonKey))
		require.Equal(t, fragClassITunesSet, r.Class)
		require.False(t, r.Applicable())
	})
}

// markMissing marks rows of book id missing: all of them, or the first n
// (n > 0) in track order.
func (f *fragFixture) markMissing(t *testing.T, id string, n int) {
	t.Helper()
	rows, err := f.s.GetBookFiles(id)
	require.NoError(t, err)
	sort.Slice(rows, func(i, j int) bool { return rows[i].TrackNumber < rows[j].TrackNumber })
	for i := range rows {
		if n > 0 && i >= n {
			break
		}
		rows[i].Missing = true
		require.NoError(t, f.s.UpdateBookFile(rows[i].ID, &rows[i]))
	}
}

// audioCopy creates a live organized book holding the same six files'
// audio (size and duration) as parentSet(…, seq(1, 6), …, 900, 9000).
func (f *fragFixture) audioCopy(t *testing.T) string {
	t.Helper()
	other := f.book(t, "copy", "Some Other Name", f.path("lib/Elsewhere/Copy"), nil)
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("track %02d.mp3", i)
		p := f.file(t, filepath.Join("lib/Elsewhere/Copy", name), 9000+101*i)
		f.row(t, name, other, p, name, int64(9000+101*i), 900, i)
	}
	f.organized(t, other)
	return other
}

// TestFragmentFixer_ChapterSetNeverJoinsMissingFiles: a join target must
// hold its audio on disk. The set's fragments may be the only copies, so
// retiring them into a book whose files are gone would lose the audio.
func TestFragmentFixer_ChapterSetNeverJoinsMissingFiles(t *testing.T) {
	t.Parallel()
	joinID := func(f *fragFixture) string {
		return existingRowID(f.path(parentDir), fragParentSetKeyPrefix+horizonKey)
	}
	t.Run("title match, target's files all missing: held", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "hs", "Horizon Storms", "lib/Other/Horizon Storms", 6, 900)
		f.setAuthor(t, existing, f.authorID(t, "Kevin J Anderson"))
		f.markMissing(t, existing, 0)
		f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, joinID(f))
		require.Equal(t, fragSkipExistingBook, r.Skipped)
		require.Contains(t, r.SkipReason, "6 file(s) missing on disk")
		require.Zero(t, res.Applicable)
	})
	t.Run("title match, target partly missing so the present total disagrees: held", func(t *testing.T) {
		f := newFragFixture(t)
		existing := f.existingBook(t, "hs", "Horizon Storms", "lib/Other/Horizon Storms", 6, 900)
		f.setAuthor(t, existing, f.authorID(t, "Kevin J Anderson"))
		f.markMissing(t, existing, 2)
		f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, joinID(f))
		require.Equal(t, fragSkipExistingBook, r.Skipped)
		require.Contains(t, r.SkipReason, "2 file(s) missing on disk")
		require.Zero(t, res.Applicable)
	})
	t.Run("audio match, target's files all missing: held, never joined or assembled", func(t *testing.T) {
		f := newFragFixture(t)
		other := f.audioCopy(t)
		f.markMissing(t, other, 0)
		f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		res := f.plan(t, "op-plan")
		noRow(t, res, joinID(f), "no join into a book whose files are gone")
		r := findRow(t, res, parentRowID(f, parentDir, horizonKey))
		require.Equal(t, fragSkipDuplicateAudio, r.Skipped)
		require.Contains(t, r.SkipReason, "whose files are missing on disk")
		require.Zero(t, res.Applicable)
	})
	t.Run("audio join re-checked at apply: a target file gone since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		other := f.audioCopy(t)
		frags := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		res := f.plan(t, "op-plan")
		r := findRow(t, res, joinID(f))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, other, r.Proposed["join"])
		f.markMissing(t, other, 1)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
		for _, id := range frags {
			require.True(t, f.liveID(t, id), "no fragment retired")
		}
	})
}

// requireUntouched asserts every fragment is live and holds its one row.
func (f *fragFixture) requireUntouched(t *testing.T, ids []string) {
	t.Helper()
	for _, id := range ids {
		require.True(t, f.liveID(t, id), "fragment %s not retired", id)
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, rows, 1, "fragment %s keeps its one row", id)
	}
}

// requireRefused applies row id of plan op-plan and asserts the apply
// refused it as changed since the plan and wrote nothing to frags.
func (f *fragFixture) requireRefused(t *testing.T, rowID string, frags []string, why string) {
	t.Helper()
	out := f.apply(t, "op-plan", "op-apply", []string{rowID}, nil)
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	if why != "" {
		require.Contains(t, fmt.Sprintf("%+v", out.Rows), why)
	}
	f.requireUntouched(t, frags)
}

// audioCopyAt is audioCopy under another folder and role: a further live
// book holding the same six files' audio.
func (f *fragFixture) audioCopyAt(t *testing.T, role, dir string) string {
	t.Helper()
	other := f.book(t, role, "Yet Another Name", f.path(dir), nil)
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("part %02d.mp3", i)
		p := f.file(t, filepath.Join(dir, name), 9000+101*i)
		f.row(t, role+name, other, p, name, int64(9000+101*i), 900, i)
	}
	f.organized(t, other)
	return other
}

// journalAssembled writes a plan record of this fixer on book id, as a
// no-parent apply that assembled it does before its first write.
func (f *fragFixture) journalAssembled(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, f.s.CreateOperationChange(&database.OperationChange{
		OperationID: "op-earlier", BookID: id, ChangeType: undo.ChangeTypeRepairPlanRecord,
		FieldName: fragRecordField("no-parent:earlier"), NewValue: "{}", Source: fragFixerID,
	}))
}

// setHashes gives each book's rows, in track order, the hashes of mk(i).
func (f *fragFixture) setHashes(t *testing.T, ids []string, mk func(i int) string) {
	t.Helper()
	i := 0
	for _, id := range ids {
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		sort.Slice(rows, func(a, b int) bool { return rows[a].TrackNumber < rows[b].TrackNumber })
		for _, r := range rows {
			i++
			require.NoError(t, f.s.SetBookFileHash(r.ID, mk(i)))
		}
	}
}

// TestFragmentFixer_ChapterSetApplyRechecksLibrary (#3774 review, item 1): a
// chapter set's plan-time tests read the whole library, so the apply decides
// the row again against the library as it is then. Each case plans an
// applicable row, changes the library, and the apply must refuse it as
// changed since the plan with every fragment untouched.
func TestFragmentFixer_ChapterSetApplyRechecksLibrary(t *testing.T) {
	audioJoin := func(t *testing.T, f *fragFixture) (string, string, []string) {
		other := f.audioCopy(t)
		frags := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(parentDir), fragParentSetKeyPrefix+horizonKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, other, r.Proposed["join"])
		return r.RowID, other, frags
	}
	titleJoin := func(t *testing.T, f *fragFixture) (string, string, []string) {
		existing := f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 6, 900)
		frags := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(setDir), fragFolderSetKeyPrefix+someWorkKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, existing, r.Proposed["join"])
		return r.RowID, existing, frags
	}
	newSet := func(t *testing.T, f *fragFixture) (string, []string) {
		frags := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		r := findRow(t, f.plan(t, "op-plan"), setRowID(f, setDir, someWorkKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		return r.RowID, frags
	}
	t.Run("audio join: a second book of the same audio since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, _, frags := audioJoin(t, f)
		late := f.audioCopyAt(t, "late", "lib/Elsewhere/Late")
		f.requireRefused(t, row, frags, late)
	})
	t.Run("audio join: a target assembled since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, other, frags := audioJoin(t, f)
		f.journalAssembled(t, other)
		f.requireRefused(t, row, frags, "assembled by an earlier no-parent apply")
	})
	t.Run("audio join: a version group added since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, _, frags := audioJoin(t, f)
		p := f.file(t, "lib/Elsewhere/Horizon Storms.m4b", 4321)
		whole := f.book(t, "whole", "Horizon Storms Whole", p, nil)
		f.row(t, "w", whole, p, "Horizon Storms.m4b", 4321, 5400, 0)
		g := "vg-late"
		for _, id := range []string{frags[0], whole} {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &g; return nil })
			require.NoError(t, err)
		}
		f.requireRefused(t, row, frags, whole)
	})
	t.Run("title join: a book holding the same audio since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, _, frags := titleJoin(t, f)
		other := f.book(t, "copy", "Unrelated Name", f.path("lib/Elsewhere/Unrelated"), nil)
		for i := 1; i <= 6; i++ {
			name := fmt.Sprintf("x%02d.mp3", i)
			p := f.file(t, filepath.Join("lib/Elsewhere/Unrelated", name), 9000+101*i)
			f.row(t, name, other, p, name, int64(9000+101*i), 900, i)
		}
		f.requireRefused(t, row, frags, other)
	})
	t.Run("title join: a version group added since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, _, frags := titleJoin(t, f)
		p := f.file(t, "lib/Elsewhere/Some Work.m4b", 4321)
		whole := f.book(t, "whole", "Some Work Whole", p, nil)
		f.row(t, "w", whole, p, "Some Work.m4b", 4321, 5400, 0)
		g := "vg-late"
		for _, id := range []string{frags[0], whole} {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &g; return nil })
			require.NoError(t, err)
		}
		f.requireRefused(t, row, frags, whole)
	})
	t.Run("title join: a better-ranked book of the title since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, _, frags := titleJoin(t, f)
		// Nine files of the same total: the plan's ranking (most files
		// first) would join it now, not the planned target.
		late := f.existingBook(t, "work2", "Some Work", "lib/Shelf/Some Work", 9, 600)
		f.requireRefused(t, row, frags, late)
	})
	t.Run("title join: mixed authors since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, _, frags := titleJoin(t, f)
		f.setAuthor(t, frags[0], f.authorID(t, "Christopher Paolini"))
		f.setAuthor(t, frags[1], f.authorID(t, "Someone Different"))
		// The re-plan reads the members' authors itself: the row comes back
		// held for them (not applicable), and nothing is written.
		out := f.apply(t, "op-plan", "op-apply", []string{row}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Contains(t, fmt.Sprintf("%+v", out.Rows), fragSkipMixedAuthors)
		f.requireUntouched(t, frags)
	})
	t.Run("title join: a target file missing since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, existing, frags := titleJoin(t, f)
		f.markMissing(t, existing, 1)
		f.requireRefused(t, row, frags, "")
	})
	t.Run("new set: the same audio in a book since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, frags := newSet(t, f)
		other := f.book(t, "copy", "Unrelated Name", f.path("lib/Elsewhere/Unrelated"), nil)
		for i := 1; i <= 6; i++ {
			name := fmt.Sprintf("x%02d.mp3", i)
			p := f.file(t, filepath.Join("lib/Elsewhere/Unrelated", name), 9000+101*i)
			f.row(t, name, other, p, name, int64(9000+101*i), 900, i)
		}
		f.requireRefused(t, row, frags, "against the whole library")
	})
	t.Run("new set: a book in its folder since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, frags := newSet(t, f)
		parent := f.book(t, "parent", "Something Else", f.path(setDir), nil)
		// Rows only, no files on disk: the folder's listing is unchanged, so
		// the row itself re-plans the same and only the whole-library
		// re-check under the merge lock (a live book in the set's folder)
		// can refuse it.
		for i := 1; i <= 2; i++ {
			p := f.path(filepath.Join(setDir, fmt.Sprintf("other %d.mp3", i)))
			f.row(t, fmt.Sprintf("o%d", i), parent, p, fmt.Sprintf("other %d.mp3", i), int64(3000+i), 1800, i)
		}
		f.requireRefused(t, row, frags, parent)
	})
	t.Run("new set: a version group added since the plan refuses", func(t *testing.T) {
		f := newFragFixture(t)
		row, frags := newSet(t, f)
		p := f.file(t, "lib/Elsewhere/Some Work.m4b", 4321)
		whole := f.book(t, "whole", "Some Work Whole", p, nil)
		f.row(t, "w", whole, p, "Some Work.m4b", 4321, 5400, 0)
		g := "vg-late"
		for _, id := range []string{frags[0], whole} {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &g; return nil })
			require.NoError(t, err)
		}
		f.requireRefused(t, row, frags, whole)
	})
	t.Run("nothing changed: the re-check passes and the join applies", func(t *testing.T) {
		f := newFragFixture(t)
		row, other, frags := audioJoin(t, f)
		out := f.apply(t, "op-plan", "op-apply", []string{row}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		f.requireRetiredInto(t, frags, other)
	})
}

// TestFragmentFixer_ChapterSetTitleJoinMixedAuthors (#3774 review, item 2):
// a set joined by its title is held for two known authors, as an assembled
// set and an audio join are.
func TestFragmentFixer_ChapterSetTitleJoinMixedAuthors(t *testing.T) {
	f := newFragFixture(t)
	f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 6, 900)
	frags := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
	f.setAuthor(t, frags[0], f.authorID(t, "Christopher Paolini"))
	f.setAuthor(t, frags[1], f.authorID(t, "Someone Different"))
	res := f.plan(t, "op-plan")
	r := findRow(t, res, existingRowID(f.path(setDir), fragFolderSetKeyPrefix+someWorkKey))
	require.Equal(t, fragSkipMixedAuthors, r.Skipped, r.SkipReason)
	require.Zero(t, res.Applicable)
}

// TestFragmentFixer_ChapterSetAudioNeedsMatchingHashes (#3774 review, item 3):
// size and duration are the same audio only where a hash cannot speak.
func TestFragmentFixer_ChapterSetAudioNeedsMatchingHashes(t *testing.T) {
	joinID := func(f *fragFixture) string {
		return existingRowID(f.path(parentDir), fragParentSetKeyPrefix+horizonKey)
	}
	t.Run("both sides hashed, hashes differ: not the same audio, never joined", func(t *testing.T) {
		f := newFragFixture(t)
		other := f.audioCopy(t)
		frags := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		f.setHashes(t, frags, func(i int) string { return fmt.Sprintf("frag-%d", i) })
		f.setHashes(t, []string{other}, func(i int) string { return fmt.Sprintf("book-%d", i) })
		res := f.plan(t, "op-plan")
		noRow(t, res, joinID(f), "six differing hashes are not the book's audio")
		r := findRow(t, res, parentRowID(f, parentDir, horizonKey))
		require.Equal(t, fragClassParentSet, r.Class)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.NotContains(t, strings.Join(r.Evidence, "\n"), other)
	})
	t.Run("one side hashed: size and duration still match", func(t *testing.T) {
		f := newFragFixture(t)
		other := f.audioCopy(t)
		frags := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		f.setHashes(t, frags, func(i int) string { return fmt.Sprintf("frag-%d", i) })
		r := findRow(t, f.plan(t, "op-plan"), joinID(f))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, other, r.Proposed["join"])
	})
	t.Run("same hashes: the book's copies, never a new book", func(t *testing.T) {
		f := newFragFixture(t)
		other := f.audioCopy(t)
		frags := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		f.setHashes(t, frags, func(i int) string { return fmt.Sprintf("h-%d", i) })
		f.setHashes(t, []string{other}, func(i int) string { return fmt.Sprintf("h-%d", i) })
		res := f.plan(t, "op-plan")
		// Same hashes match the fragments to the book's own rows before any
		// chapter grouping (the parent rule): no set forms at all.
		noRow(t, res, parentRowID(f, parentDir, horizonKey), "never assembled beside its own audio")
		for _, id := range frags {
			require.Equal(t, 1, rowsHolding(res, id))
		}
	})
	t.Run("a missing row with a differing hash holds nothing", func(t *testing.T) {
		f := newFragFixture(t)
		other := f.audioCopy(t)
		f.markMissing(t, other, 0)
		frags := f.parentSet(t, parentDir, seq(1, 6), horizonStem, 900, 9000)
		f.setHashes(t, frags, func(i int) string { return fmt.Sprintf("frag-%d", i) })
		f.setHashes(t, []string{other}, func(i int) string { return fmt.Sprintf("book-%d", i) })
		r := findRow(t, f.plan(t, "op-plan"), parentRowID(f, parentDir, horizonKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})
}

// TestFragmentFixer_ChapterSetJoinOffsets (#3774 review, item 4): a set's
// fragments are placed on the target's timeline by the set's own numbering.
// metadata.ChapterPosition reads "Some Work - 04 1" as chapter 1, so every
// fragment landed at the target's first track and carried listening state
// to its start.
func TestFragmentFixer_ChapterSetJoinOffsets(t *testing.T) {
	f := newFragFixture(t)
	existing := f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 6, 900)
	f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
	r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(setDir), fragFolderSetKeyPrefix+someWorkKey))
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, existing, r.Proposed["join"])
	plan, ok := r.Detail.(*fragGroupPlan)
	require.True(t, ok)
	require.Len(t, plan.Members, 6)
	for _, m := range plan.Members {
		var n, dup int
		_, err := fmt.Sscanf(m.Frag.origStem(), "Christopher Paolini - Some Work - %02d %d", &n, &dup)
		require.NoError(t, err)
		require.InDelta(t, float64((n-1)*900), m.Offset, 0.001, "chapter %d starts at track %d of the target", n, n)
	}
}

// TestFragmentFixer_ChapterSetJoinTargetVersionGroup: a member in the join
// target's own version group is no other version of the work, unless that
// group (or any fragment's group) holds an iTunes copy: the retire hand-off
// would write it, so the join is held (PR #3787 review, blocker 1); a member
// versioned with a chapter copy outside the row is held, since retiring it
// would hand its group's primary to that copy.
func TestFragmentFixer_ChapterSetJoinTargetVersionGroup(t *testing.T) {
	setVG := func(t *testing.T, f *fragFixture, g string, ids ...string) {
		for _, id := range ids {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &g; return nil })
			require.NoError(t, err)
		}
	}
	joinID := func(f *fragFixture) string {
		return existingRowID(f.path(setDir), fragFolderSetKeyPrefix+someWorkKey)
	}
	// ownGroup is the target, a sibling edition and the first fragment in
	// one version group; mark edits the sibling before the plan.
	ownGroup := func(t *testing.T, f *fragFixture, mark func(sibling string)) (existing, sibling string, frags []string) {
		existing = f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 6, 900)
		p := f.file(t, "lib/Elsewhere/Some Work.m4b", 4321)
		sibling = f.book(t, "sibling", "Some Work (another edition)", p, nil)
		f.row(t, "s", sibling, p, "Some Work.m4b", 4321, 5400, 0)
		frags = f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		setVG(t, f, "vg-target", existing, sibling, frags[0])
		if mark != nil {
			mark(sibling)
		}
		return existing, sibling, frags
	}
	bookPID := func(t *testing.T, f *fragFixture) func(string) {
		return func(id string) {
			pid := "ITUNESPID01"
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
			require.NoError(t, err)
		}
	}
	t.Run("the target's own group with no iTunes copy: still joins", func(t *testing.T) {
		f := newFragFixture(t)
		existing, _, _ := ownGroup(t, f, nil)
		r := findRow(t, f.plan(t, "op-plan"), joinID(f))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, existing, r.Proposed["join"])
	})
	t.Run("the target's own group holds an iTunes copy: held", func(t *testing.T) {
		// Retiring frags[0] (primary by default) hands vg-target's primary
		// on, and the ranking favours the iTunes book: a write to it.
		f := newFragFixture(t)
		_, sibling, _ := ownGroup(t, f, bookPID(t, f))
		res := f.plan(t, "op-plan")
		r := findRow(t, res, joinID(f))
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, sibling)
		require.Contains(t, r.SkipReason, "vg-target")
		require.Zero(t, res.Applicable)
	})
	t.Run("an iTunes file row on the target group's sibling: held", func(t *testing.T) {
		f := newFragFixture(t)
		_, sibling, _ := ownGroup(t, f, func(id string) {
			rows, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			rows[0].ITunesPersistentID = "ROWPID01"
			require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
		})
		r := findRow(t, f.plan(t, "op-plan"), joinID(f))
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, sibling)
	})
	t.Run("a live itunes external id on the sibling: held", func(t *testing.T) {
		f := newFragFixture(t)
		_, sibling, _ := ownGroup(t, f, func(id string) {
			require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "LIVEPID", BookID: id}))
		})
		r := findRow(t, f.plan(t, "op-plan"), joinID(f))
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, sibling)
	})
	t.Run("a tombstoned itunes external id on the sibling: still joins", func(t *testing.T) {
		f := newFragFixture(t)
		existing, _, _ := ownGroup(t, f, func(id string) {
			require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "DEADPID", BookID: id, Tombstoned: true}))
		})
		r := findRow(t, f.plan(t, "op-plan"), joinID(f))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, existing, r.Proposed["join"])
	})
	t.Run("a fragment's own group holds an iTunes copy: held", func(t *testing.T) {
		f := newFragFixture(t)
		f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 6, 900)
		p := f.file(t, "lib/iTunesish/Other Edition.m4b", 4321)
		it := f.book(t, "it", "Other Edition", p, nil)
		f.row(t, "i", it, p, "Other Edition.m4b", 4321, 5400, 0)
		frags := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		setVG(t, f, "vg-frag", frags[2], it)
		bookPID(t, f)(it)
		r := findRow(t, f.plan(t, "op-plan"), joinID(f))
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, it)
		require.Contains(t, r.SkipReason, "vg-frag")
	})
	t.Run("an iTunes id on the target group's sibling since the plan: refused at apply, nothing written", func(t *testing.T) {
		f := newFragFixture(t)
		_, sibling, frags := ownGroup(t, f, nil)
		r := findRow(t, f.plan(t, "op-plan"), joinID(f))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		bookPID(t, f)(sibling)
		before, err := f.s.GetBookByID(sibling)
		require.NoError(t, err)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Contains(t, fmt.Sprintf("%+v", out.Rows), sibling)
		f.requireUntouched(t, frags)
		after, err := f.s.GetBookByID(sibling)
		require.NoError(t, err)
		require.Equal(t, before.IsPrimaryVersion, after.IsPrimaryVersion, "the iTunes book is never written")
	})
	t.Run("versioned with a chapter copy outside the row: held", func(t *testing.T) {
		f := newFragFixture(t)
		f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 6, 900)
		frags := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		cp := f.folderSet(t, "lib/Copies", []int{1}, func(i int) string { return fmt.Sprintf("Some Work copy %02d", i) }, 900, 70000)
		setVG(t, f, "vg-copy", frags[0], cp[0])
		r := findRow(t, f.plan(t, "op-plan"), joinID(f))
		require.Equal(t, fragSkipVersionGroupParent, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, cp[0])
	})
}

// TestFragJoinGroupITunesFailsClosed: a version group member whose external
// ids cannot be read holds the join; an unreadable group listing does too.
func TestFragJoinGroupITunesFailsClosed(t *testing.T) {
	lib := newFragLibrary()
	lib.books["t"] = fragBook{ID: "t", VersionGroup: "g"}
	lib.books["s"] = fragBook{ID: "s", VersionGroup: "g", FilePath: "/lib/s"}
	lib.extIDs = func(id string) ([]database.ExternalIDMapping, error) {
		if id == "s" {
			return nil, errors.New("disk on fire")
		}
		return nil, nil
	}
	why := lib.retireITunes("t", nil)
	require.Contains(t, why, "book s of version group g cannot be read")
	lib.extIDs = func(string) ([]database.ExternalIDMapping, error) { return nil, nil }
	require.Empty(t, lib.retireITunes("t", nil), "no iTunes member: the join stands")
	lib.groupReads = failingGroupReads{}
	require.Contains(t, lib.retireITunes("t", nil), "version group g of the retire is unreadable")
}

type failingGroupReads struct{}

func (failingGroupReads) GetBooksByVersionGroup(string) ([]database.Book, error) {
	return nil, errors.New("listing failed")
}

func (failingGroupReads) GetBookFiles(string) ([]database.BookFile, error) { return nil, nil }

// countingFragStore counts the whole-library file listings the re-check
// makes (the ~742k-row read on prod).
type countingFragStore struct {
	*database.PebbleStore
	fileLists atomic.Int64
}

func (c *countingFragStore) GetAllBookFilesCoreComplete() ([]database.BookFileCore, error) {
	c.fileLists.Add(1)
	return c.PebbleStore.GetAllBookFilesCoreComplete()
}

// threeSets plans three independent new chapter sets and returns their row
// ids and fragments.
func threeSets(t *testing.T, f *fragFixture) (rows []string, frags [][]string, res *repairs.PlanResult) {
	t.Helper()
	for i, title := range []string{"Alpha Work", "Beta Work", "Gamma Work"} {
		dir := "lib/Christopher Paolini/" + title
		stem := func(n int) string { return fmt.Sprintf("Christopher Paolini - %s - %02d 1", title, n) }
		frags = append(frags, f.folderSet(t, dir, seq(1, 6), stem, 900, 20000*(i+1)))
		rows = append(rows, setRowID(f, dir, "christopher paolini "+strings.ToLower(title)+" # #"))
	}
	res = f.plan(t, "op-plan")
	for _, id := range rows {
		r := findRow(t, res, id)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	}
	return rows, frags, res
}

// TestFragmentFixer_ChapterSetRecheckListsLibraryOncePerApply (PR #3787
// review, blocker 2): the whole-library re-check lists the library once per
// apply run, not twice per row (the engine's unlocked Replan and Apply's
// locked one each listed it, four workers at once).
func TestFragmentFixer_ChapterSetRecheckListsLibraryOncePerApply(t *testing.T) {
	f := newFragFixture(t)
	rows, _, _ := threeSets(t, f)
	cs := &countingFragStore{PebbleStore: f.s}
	f.p = &Plugin{deps: scanDeps{fakeDeps: fakeDeps{store: cs}, scan: &scriptedScan{renewsLeft: -1}, ops: f.ops}, standDownWait: noWait}
	out := f.apply(t, "op-plan", "op-apply", rows, nil)
	require.Equal(t, 3, out.Applied, "%+v", out.Rows)
	require.EqualValues(t, 1, cs.fileLists.Load(), "3 rows, one whole-library listing")
}

// TestFragmentFixer_ChapterSetRecheckSeesWritesBetweenRows: the run's one
// snapshot still sees what another writer did between two rows, by the book
// change log (no relisting) or, when another merge-family writer held the
// merge lock, by listing again.
func TestFragmentFixer_ChapterSetRecheckSeesWritesBetweenRows(t *testing.T) {
	// applyRow applies one planned row as the engine does inside a run:
	// unlocked Replan, then Apply (which re-plans under the merge lock).
	applyRow := func(t *testing.T, f *fragFixture, ctx context.Context, plan *repairs.PlanResult, id string) error {
		t.Helper()
		fx := newFragmentFixer(f.p)
		planned := findRow(t, plan, id)
		fresh, err := fx.Replan(ctx, nil, planned, nil)
		require.NoError(t, err)
		require.Equal(t, planned.Fingerprint, fresh.Fingerprint, fresh.Reason)
		return fx.Apply(ctx, f.fragWriter(t, "op-apply"), fresh)
	}
	setup := func(t *testing.T) (*fragFixture, []string, [][]string, *repairs.PlanResult, context.Context, *fragApplySession) {
		f := newFragFixture(t)
		rows, frags, plan := threeSets(t, f)
		ctx, end := newFragmentFixer(f.p).BeginApply(context.Background(), false)
		t.Cleanup(end)
		return f, rows, frags, plan, ctx, fragSessionOf(ctx)
	}
	// holdAudioOf gives a new live book the audio of set frags (same sizes
	// and durations, no hashes): that set is now another book's audio.
	holdAudioOf := func(t *testing.T, f *fragFixture, frags []string) string {
		other := f.book(t, "holder", "Unrelated Holder", f.path("lib/Elsewhere/Holder"), nil)
		for i, id := range frags {
			rs, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			name := fmt.Sprintf("h%02d.mp3", i)
			p := f.file(t, filepath.Join("lib/Elsewhere/Holder", name), int(rs[0].FileSize))
			f.row(t, name, other, p, name, rs[0].FileSize, rs[0].Duration, i+1)
		}
		return other
	}
	t.Run("rows in a run share one listing", func(t *testing.T) {
		f, rows, _, plan, ctx, sess := setup(t)
		for _, id := range rows {
			require.NoError(t, applyRow(t, f, ctx, plan, id))
		}
		require.Equal(t, 1, sess.loads)
	})
	t.Run("a book written between rows is caught up from the change log", func(t *testing.T) {
		f, rows, frags, plan, ctx, sess := setup(t)
		require.NoError(t, applyRow(t, f, ctx, plan, rows[0]))
		holder := holdAudioOf(t, f, frags[1])
		err := applyRow(t, f, ctx, plan, rows[1])
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), holder)
		f.requireUntouched(t, frags[1])
		require.Equal(t, 1, sess.loads, "caught up point by point, not listed again")
	})
	t.Run("another merge-lock holder between rows: listed again", func(t *testing.T) {
		f, rows, frags, plan, ctx, sess := setup(t)
		require.NoError(t, applyRow(t, f, ctx, plan, rows[0]))
		merge.LockMergeRMW()
		holder := holdAudioOf(t, f, frags[1])
		merge.UnlockMergeRMW()
		err := applyRow(t, f, ctx, plan, rows[1])
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), holder)
		require.Equal(t, 2, sess.loads)
	})
}

// TestFragmentFixer_ChapterSetJoinResumeNeedsOwnRetire (PR #3787 review,
// should-fix 1): a set join with a fragment already retired into the target
// skips the whole-library re-check and resumes only when this fixer's apply
// journaled that retire; a fragment another fixer merged into the target is a
// change, not a cut-off run.
func TestFragmentFixer_ChapterSetJoinResumeNeedsOwnRetire(t *testing.T) {
	plan := func(t *testing.T, f *fragFixture) (string, string, []string) {
		existing := f.existingBook(t, "work", "Some Work", "lib/Other/Some Work", 6, 900)
		frags := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
		r := findRow(t, f.plan(t, "op-plan"), existingRowID(f.path(setDir), fragFolderSetKeyPrefix+someWorkKey))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, existing, r.Proposed["join"])
		return r.RowID, existing, frags
	}
	retire := func(t *testing.T, f *fragFixture, source, opID, id, into string) {
		var w *repairs.Writer
		if source == fragFixerID {
			w = f.fragWriter(t, opID)
		} else {
			w = repairs.NewWriter(f.s, f.s, source, "bulk_update", "repairs-").WithJournal(f.s, f.s, opID)
		}
		_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, source, id, into, &merge.SliceMapping{Mappable: true})
		require.NoError(t, err)
	}
	t.Run("this fixer's cut-off run: resumes", func(t *testing.T) {
		f := newFragFixture(t)
		row, existing, frags := plan(t, f)
		retire(t, f, fragFixerID, "op-cut", frags[0], existing)
		out := f.apply(t, "op-plan", "op-apply", []string{row}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		f.requireRetiredInto(t, frags, existing)
	})
	t.Run("retired into the target by another fixer: refused", func(t *testing.T) {
		f := newFragFixture(t)
		row, existing, frags := plan(t, f)
		retire(t, f, "some-other-fixer", "op-other", frags[0], existing)
		out := f.apply(t, "op-plan", "op-apply", []string{row}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
		require.Contains(t, fmt.Sprintf("%+v", out.Rows), "not by a "+fragFixerID+" apply")
		f.requireUntouched(t, frags[1:])
	})
}

// failingFilesStore fails the complete book-file listing.
type failingFilesStore struct{ *database.PebbleStore }

func (failingFilesStore) GetAllBookFilesCoreComplete() ([]database.BookFileCore, error) {
	return nil, database.ErrMemdbIncomplete
}

// TestFragmentFixer_ChapterSetRecheckFailsClosed (PR #3787 review): when the
// whole library cannot be listed completely the re-check fails the row, and
// nothing is written; it never passes the row on a partial listing.
func TestFragmentFixer_ChapterSetRecheckFailsClosed(t *testing.T) {
	f := newFragFixture(t)
	rows, frags, _ := threeSets(t, f)
	f.p = &Plugin{deps: scanDeps{fakeDeps: fakeDeps{store: failingFilesStore{f.s}}, scan: &scriptedScan{renewsLeft: -1}, ops: f.ops}, standDownWait: noWait}
	out := f.apply(t, "op-plan", "op-apply", rows[:1], nil)
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.Equal(t, 1, out.Failed, "%+v", out.Rows)
	require.Contains(t, fmt.Sprintf("%+v", out.Rows), "library re-check")
	f.requireUntouched(t, frags[0])
}

// TestFragmentFixer_ParentRowITunesVersionGroup (PR #3787 review, round 3):
// a moved or copy row retires its fragments into the parent, which hands
// each fragment's version-group primary on and re-ranks the parent's group,
// as a join does: an iTunes copy in any of those groups holds the row, at
// plan and again under the merge lock.
func TestFragmentFixer_ParentRowITunesVersionGroup(t *testing.T) {
	sibling := func(t *testing.T, f *fragFixture, role string, group string, pid string) string {
		p := f.file(t, "lib/Elsewhere/"+role+".m4b", 4321)
		id := f.book(t, role, "Another Edition "+role, p, nil)
		f.row(t, role, id, p, role+".m4b", 4321, 5400, 0)
		_, err := f.s.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID = &group
			if pid != "" {
				b.ITunesPersistentID = &pid
			}
			return nil
		})
		require.NoError(t, err)
		return id
	}
	inGroup := func(t *testing.T, f *fragFixture, id, group string) {
		_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &group; return nil })
		require.NoError(t, err)
	}
	t.Run("a fragment's group without an iTunes copy: still applicable", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		inGroup(t, f, f.ids["fragF"], "vg-frag")
		sibling(t, f, "plain", "vg-frag", "")
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+f.ids["parent"])
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	})
	t.Run("an iTunes copy in a fragment's group: held", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		inGroup(t, f, f.ids["fragF"], "vg-frag")
		it := sibling(t, f, "it", "vg-frag", "ITPID01")
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+f.ids["parent"])
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, it)
		require.Contains(t, r.SkipReason, "vg-frag")
	})
	t.Run("an iTunes copy in the parent's group: held", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		inGroup(t, f, f.ids["suns"], "vg-parent")
		it := sibling(t, f, "it", "vg-parent", "ITPID02")
		r := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["suns"])
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, it)
		require.Contains(t, r.SkipReason, "vg-parent")
	})
	t.Run("an iTunes id added after the plan: refused at apply, nothing written", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		inGroup(t, f, f.ids["fragF"], "vg-frag")
		it := sibling(t, f, "it", "vg-frag", "")
		r := findRow(t, f.plan(t, "op-plan"), "moved:"+f.ids["parent"])
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		pid := "ITPID03"
		_, err := f.s.ModifyBook(it, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
		require.NoError(t, err)
		before, err := f.s.GetBookByID(it)
		require.NoError(t, err)
		parentRows, err := f.s.GetBookFiles(f.ids["parent"])
		require.NoError(t, err)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		// The re-plan holds the row for the iTunes copy: not applicable.
		require.Len(t, out.Rows, 1)
		require.Equal(t, repairs.OutcomeNotApplicable, out.Rows[0].Outcome, "%+v", out.Rows)
		require.Equal(t, repairs.SkipITunes, out.Rows[0].Skipped, "%+v", out.Rows)
		require.True(t, f.live(t, "fragF"), "the fragment is not retired")
		after, err := f.s.GetBookByID(it)
		require.NoError(t, err)
		require.Equal(t, before.IsPrimaryVersion, after.IsPrimaryVersion, "the iTunes book is never written")
		nowRows, err := f.s.GetBookFiles(f.ids["parent"])
		require.NoError(t, err)
		require.Equal(t, parentRows, nowRows, "the parent's rows are not repointed")
	})
}

// TestFragmentFixer_ChapterSetRecheckCandidatesAreThePlansUnmatched (PR #3787
// review): the re-check treats as "a chapter fragment, not a book" only what
// the plan would, its unmatched set. A fragment matched to a parent is a
// book to the plan (it holds files in the set's folder), so the re-check
// holds for it too instead of ignoring it.
func TestFragmentFixer_ChapterSetRecheckCandidatesAreThePlansUnmatched(t *testing.T) {
	f := newFragFixture(t)
	frags := f.folderSet(t, setDir, seq(1, 6), someWorkStem, 900, 9000)
	r := findRow(t, f.plan(t, "op-plan"), setRowID(f, setDir, someWorkKey))
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	// A parent book elsewhere and, in the set's folder, a chapter fragment
	// of it (same hash as one of its rows). Rows only: the folder's listing
	// on disk is unchanged.
	parent := f.book(t, "otherparent", "Other Book", f.path("lib/Other Book"), nil)
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("Other Book Part %02d.mp3", i)
		f.row(t, "op"+name, parent, f.path(filepath.Join("lib/Other Book", name)), name, int64(5000+i), 600, i)
	}
	stem := "Other Book Part 01"
	frag := f.book(t, "matched", stem, f.path(filepath.Join(setDir, stem+".mp3")), nil)
	f.row(t, "m", frag, f.path(filepath.Join(setDir, stem+".mp3")), stem+".mp3", 5001, 600, 0)
	f.setHashes(t, []string{parent}, func(i int) string { return fmt.Sprintf("ob-%d", i) })
	f.setHashes(t, []string{frag}, func(int) string { return "ob-1" })
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.Contains(t, fmt.Sprintf("%+v", out.Rows), frag)
	f.requireUntouched(t, frags)
}

// applyRowInRun applies one planned row as the engine does inside an apply
// run (ctx carries the run's session): unlocked Replan, then Apply.
func applyRowInRun(t *testing.T, f *fragFixture, fx *fragmentFixer, ctx context.Context, plan *repairs.PlanResult, id string) error {
	t.Helper()
	planned := findRow(t, plan, id)
	fresh, err := fx.Replan(ctx, nil, planned, nil)
	require.NoError(t, err)
	require.Equal(t, planned.Fingerprint, fresh.Fingerprint, fresh.Reason)
	require.True(t, fresh.Applicable(), "%s: %s", fresh.Skipped, fresh.SkipReason)
	return fx.Apply(ctx, f.fragWriter(t, "op-apply"), fresh)
}

func setBookPID(t *testing.T, f *fragFixture, id, pid string) {
	t.Helper()
	_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
	require.NoError(t, err)
}

// TestFragmentFixer_RetireTargetITunes (PR #3787 re-review B1): a moved or
// copy row writes its parent (rows repointed, fragments retired into it,
// state carried, totals recomputed) whether or not the parent is versioned:
// an iTunes parent holds the row, at plan and under the merge lock.
func TestFragmentFixer_RetireTargetITunes(t *testing.T) {
	moved := func(f *fragFixture) string { return "moved:" + f.ids["parent"] }
	t.Run("book iTunes id on an ungrouped parent: held", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		setBookPID(t, f, f.ids["parent"], "PARENTPID")
		r := findRow(t, f.plan(t, "op-plan"), moved(f))
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, f.ids["parent"])
	})
	t.Run("an iTunes id on a parent row: held", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		rows, err := f.s.GetBookFiles(f.ids["parent"])
		require.NoError(t, err)
		rows[0].ITunesPersistentID = "ROWPID"
		require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
		r := findRow(t, f.plan(t, "op-plan"), moved(f))
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, f.ids["parent"])
	})
	t.Run("a live itunes external id on the copy parent: the copy retires writing the fragment only", func(t *testing.T) {
		// Owner decision 2026-10-06: a copy row into an iTunes-linked parent
		// is no longer held; it retires its fragments without writing the
		// parent (TestFragmentFixer_CopyClaimants covers the writes).
		f := newFragFixture(t)
		f.seed(t)
		require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "SUNSPID", BookID: f.ids["suns"]}))
		r := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["suns"])
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Contains(t, r.Current["itunes_parent"], "itunes external id SUNSPID")
	})
	t.Run("added after the plan: refused at apply, nothing written", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		r := findRow(t, f.plan(t, "op-plan"), moved(f))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		setBookPID(t, f, f.ids["parent"], "LATEPID")
		before, err := f.s.GetBookFiles(f.ids["parent"])
		require.NoError(t, err)
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.Equal(t, repairs.SkipITunes, out.Rows[0].Skipped, "%+v", out.Rows)
		require.True(t, f.live(t, "fragF"))
		after, err := f.s.GetBookFiles(f.ids["parent"])
		require.NoError(t, err)
		require.Equal(t, before, after, "the iTunes parent's rows are not repointed")
	})
	t.Run("landing after the locked re-plan: the pre-write check refuses", func(t *testing.T) {
		// N3: only Apply's own check under the lock can see this one.
		f := newFragFixture(t)
		f.seed(t)
		plan := f.plan(t, "op-plan")
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() { setBookPID(t, f, f.ids["parent"], "RACEPID") }
		before, err := f.s.GetBookFiles(f.ids["parent"])
		require.NoError(t, err)
		err = applyRowInRun(t, f, fx, context.Background(), plan, moved(f))
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), "which this row writes")
		require.True(t, f.live(t, "fragF"))
		after, err := f.s.GetBookFiles(f.ids["parent"])
		require.NoError(t, err)
		require.Equal(t, before, after)
	})
}

// TestFragmentFixer_SurvivorRowITunes (PR #3787 re-review S3): a no-parent
// row retires its members into its survivor, handing each member's version
// group primary on: an iTunes book in such a group holds the row, at plan
// and under the merge lock.
func TestFragmentFixer_SurvivorRowITunes(t *testing.T) {
	loose := func(f *fragFixture) string { return noParentRowID(f.path("lib/Loose"), "loose") }
	itunesSibling := func(t *testing.T, f *fragFixture, member, pid string) string {
		p := f.file(t, "lib/Elsewhere/sib.m4b", 4321)
		id := f.book(t, "sib", "Loose Other Edition", p, nil)
		f.row(t, "sib", id, p, "sib.m4b", 4321, 5400, 0)
		g := "vg-loose"
		for _, b := range []string{member, id} {
			_, err := f.s.ModifyBook(b, func(bk *database.Book) error { bk.VersionGroupID = &g; return nil })
			require.NoError(t, err)
		}
		if pid != "" {
			setBookPID(t, f, id, pid)
		}
		return id
	}
	memberOf := func(t *testing.T, f *fragFixture, r repairs.Row) string {
		for _, id := range r.BookIDs {
			if id != r.Proposed["survivor"] {
				return id
			}
		}
		t.Fatal("no member")
		return ""
	}
	t.Run("an iTunes book in a member's group: held", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		r0 := findRow(t, f.plan(t, "op-plan0"), loose(f))
		require.True(t, r0.Applicable(), "%s: %s", r0.Skipped, r0.SkipReason)
		sib := itunesSibling(t, f, memberOf(t, f, r0), "LOOSEPID")
		r := findRow(t, f.plan(t, "op-plan"), loose(f))
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, sib)
	})
	t.Run("landing after the locked re-plan: the pre-write check refuses", func(t *testing.T) {
		f := newFragFixture(t)
		f.seed(t)
		r0 := findRow(t, f.plan(t, "op-plan0"), loose(f))
		member := memberOf(t, f, r0)
		sib := itunesSibling(t, f, member, "")
		plan := f.plan(t, "op-plan")
		r := findRow(t, plan, loose(f))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() { setBookPID(t, f, sib, "RACEPID") }
		err := applyRowInRun(t, f, fx, context.Background(), plan, r.RowID)
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), sib)
		for _, id := range r.BookIDs {
			rows, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			require.Len(t, rows, 1, "book %s keeps its one row: nothing moved", id)
			b, err := f.s.GetBookByID(id)
			require.NoError(t, err)
			require.False(t, b.IsSoftDeleted(), "book %s not retired", id)
		}
	})
}

// TestFragmentFixer_CarryRowNeverOntoITunes (PR #3787 re-review S4): a carry
// row moves file rows onto its terminal book; an iTunes terminal holds it at
// plan, in the re-plan, and under the merge lock. Round 5: the rows leave a
// retired book too, so an iTunes source book, or an iTunes id or path on a
// moved row itself, holds it the same way.
func TestFragmentFixer_CarryRowNeverOntoITunes(t *testing.T) {
	setup := func(t *testing.T) (*fragFixture, string, string, string) {
		f := newFragFixture(t)
		px := f.file(t, "lib/X/x.mp3", 1111)
		x := f.book(t, "x", "Terminal", px, nil)
		f.row(t, "x", x, px, "x.mp3", 1111, 600, 1)
		ps := f.file(t, "lib/S/s.mp3", 2222)
		sb := f.book(t, "s", "Retired Survivor", ps, nil)
		f.row(t, "s", sb, ps, "s.mp3", 2222, 600, 1)
		yes := true
		_, err := f.s.ModifyBook(sb, func(b *database.Book) error {
			b.MarkedForDeletion, b.MergedIntoBookID = &yes, &x
			return nil
		})
		require.NoError(t, err)
		return f, x, sb, f.rowIDs["s"]
	}
	build := func(t *testing.T, f *fragFixture, x, sb, file string) repairs.Row {
		store, _, err := newFragmentFixer(f.p).stores()
		require.NoError(t, err)
		lib := newFragLibrary()
		lib.extIDs, lib.groupReads = store.GetExternalIDsForBook, store
		for _, id := range []string{x, sb} {
			require.NoError(t, replanLoad(store, lib, id))
		}
		rec := fragPlanRecord{RowID: "no-parent:carrytest", Survivor: sb, BookIDs: []string{sb}, PlannedAt: time.Now()}
		r, err := carryRow(store, lib, rec, []string{"op-old"}, x, []fragCarry{{File: file, From: sb, To: x}})
		require.NoError(t, err)
		return r
	}
	onS := func(t *testing.T, f *fragFixture, sb string) {
		rows, err := f.s.GetBookFiles(sb)
		require.NoError(t, err)
		require.Len(t, rows, 1, "the file stays on the retired book")
	}
	t.Run("an iTunes terminal: held at plan", func(t *testing.T) {
		f, x, sb, file := setup(t)
		setBookPID(t, f, x, "TERMPID")
		r := build(t, f, x, sb, file)
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, x)
	})
	t.Run("a plain terminal: applicable, and it moves", func(t *testing.T) {
		f, x, sb, file := setup(t)
		r := build(t, f, x, sb, file)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		fx := newFragmentFixer(f.p)
		fresh, err := fx.Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.True(t, fresh.Applicable(), "%s: %s", fresh.Skipped, fresh.SkipReason)
		require.NoError(t, fx.Apply(context.Background(), f.fragWriter(t, "op-apply"), fresh))
		rows, err := f.s.GetBookFiles(sb)
		require.NoError(t, err)
		require.Empty(t, rows, "control: the carry moves the file off S")
	})
	t.Run("an iTunes id since the plan: the re-plan holds it", func(t *testing.T) {
		f, x, sb, file := setup(t)
		r := build(t, f, x, sb, file)
		setBookPID(t, f, x, "LATEPID")
		fresh, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.Equal(t, repairs.SkipITunes, fresh.Skipped, fresh.SkipReason)
		onS(t, f, sb)
	})
	t.Run("landing after the locked re-plan: the pre-write check refuses", func(t *testing.T) {
		f, x, sb, file := setup(t)
		r := build(t, f, x, sb, file)
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() { setBookPID(t, f, x, "RACEPID") }
		fresh, err := fx.Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		err = fx.Apply(context.Background(), f.fragWriter(t, "op-apply"), fresh)
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), x)
		onS(t, f, sb)
	})

	// The moved row itself, on the retired book: an iTunes id or path.
	setRowITunes := func(t *testing.T, f *fragFixture, sb, pid, path string) {
		t.Helper()
		rows, err := f.s.GetBookFiles(sb)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		rows[0].ITunesPersistentID, rows[0].ITunesPath = pid, path
		require.NoError(t, f.s.UpdateBookFile(rows[0].ID, &rows[0]))
	}
	const itunesPath = "file:///Users/synthetic/Music/iTunes/iTunes%20Media/Audiobooks/s.mp3"
	t.Run("an iTunes source book: held at plan", func(t *testing.T) {
		f, x, sb, file := setup(t)
		setBookPID(t, f, sb, "SRCPID")
		r := build(t, f, x, sb, file)
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, sb)
	})
	t.Run("an iTunes id on the moved row: held at plan", func(t *testing.T) {
		f, x, sb, file := setup(t)
		setRowITunes(t, f, sb, "ROWPID0123456789", "")
		r := build(t, f, x, sb, file)
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, "ROWPID0123456789")
	})
	t.Run("an iTunes path on the moved row: held at plan", func(t *testing.T) {
		f, x, sb, file := setup(t)
		setRowITunes(t, f, sb, "", itunesPath)
		r := build(t, f, x, sb, file)
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, "iTunes path")
	})
	t.Run("a source book the snapshot lacks (a full plan's): read fresh, an iTunes row on it holds", func(t *testing.T) {
		f, x, sb, file := setup(t)
		setRowITunes(t, f, sb, "FRESHROWPID0123", "")
		store, _, err := newFragmentFixer(f.p).stores()
		require.NoError(t, err)
		lib := newFragLibrary()
		lib.extIDs, lib.groupReads = store.GetExternalIDsForBook, store
		require.NoError(t, replanLoad(store, lib, x))
		_, held := lib.books[sb]
		require.False(t, held, "the retired source is not in this snapshot")
		rec := fragPlanRecord{RowID: "no-parent:carrytest", Survivor: sb, BookIDs: []string{sb}, PlannedAt: time.Now()}
		r, err := carryRow(store, lib, rec, []string{"op-old"}, x, []fragCarry{{File: file, From: sb, To: x}})
		require.NoError(t, err)
		require.Equal(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
		require.Contains(t, r.SkipReason, "FRESHROWPID0123")
	})
	t.Run("an iTunes id on the source book since the plan: the re-plan holds it", func(t *testing.T) {
		f, x, sb, file := setup(t)
		r := build(t, f, x, sb, file)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		setBookPID(t, f, sb, "LATESRCPID")
		fresh, err := newFragmentFixer(f.p).Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.Equal(t, repairs.SkipITunes, fresh.Skipped, fresh.SkipReason)
		onS(t, f, sb)
	})
	t.Run("the source book turns iTunes after the locked re-plan: refused", func(t *testing.T) {
		f, x, sb, file := setup(t)
		r := build(t, f, x, sb, file)
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() { setBookPID(t, f, sb, "RACESRCPID") }
		fresh, err := fx.Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.True(t, fresh.Applicable(), "%s: %s", fresh.Skipped, fresh.SkipReason)
		err = fx.Apply(context.Background(), f.fragWriter(t, "op-apply"), fresh)
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), sb)
		onS(t, f, sb)
	})
	t.Run("the moved row turns iTunes after the locked re-plan: refused", func(t *testing.T) {
		f, x, sb, file := setup(t)
		r := build(t, f, x, sb, file)
		fx := newFragmentFixer(f.p)
		fx.afterLockedReplan = func() { setRowITunes(t, f, sb, "RACEROWPID01234", "") }
		fresh, err := fx.Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		require.True(t, fresh.Applicable(), "%s: %s", fresh.Skipped, fresh.SkipReason)
		err = fx.Apply(context.Background(), f.fragWriter(t, "op-apply"), fresh)
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
		require.Contains(t, err.Error(), "RACEROWPID01234")
		onS(t, f, sb)
	})
}

// TestFragmentFixer_ChapterSetRecheckForeignHoldAcrossOtherRows (PR #3787
// re-review S1): a row that never re-checks the library (here a copy row)
// must not hide another writer's merge-lock hold before it: the next set
// row lists the library again.
func TestFragmentFixer_ChapterSetRecheckForeignHoldAcrossOtherRows(t *testing.T) {
	f := newFragFixture(t)
	f.seed(t)
	rows, _, plan := threeSets(t, f)
	fx := newFragmentFixer(f.p)
	ctx, end := fx.BeginApply(context.Background(), false)
	defer end()
	sess := fragSessionOf(ctx)
	require.NoError(t, applyRowInRun(t, f, fx, ctx, plan, rows[0]))
	require.Equal(t, 1, sess.loads)
	merge.LockMergeRMW()
	merge.UnlockMergeRMW()
	require.NoError(t, applyRowInRun(t, f, fx, ctx, plan, "copy:"+f.ids["suns"]))
	require.NoError(t, applyRowInRun(t, f, fx, ctx, plan, rows[1]))
	require.Equal(t, 2, sess.loads, "the foreign hold before the copy row is not hidden by it")
}

// TestFragmentFixer_ChapterSetRecheckSeesLockFreeFileWrites (PR #3787
// re-review S2): a file-only write by a writer that takes no merge lock and
// writes no book (here a hash backfill) between two rows is still seen: the
// re-check reads every book holding the set's own audio fresh.
func TestFragmentFixer_ChapterSetRecheckSeesLockFreeFileWrites(t *testing.T) {
	f := newFragFixture(t)
	rows, frags, plan0 := threeSets(t, f)
	_ = plan0
	f.setHashes(t, frags[1], func(i int) string { return fmt.Sprintf("beta-%d", i) })
	// A book holding six unrelated files, there before the plan.
	holder := f.book(t, "holder", "Unrelated Holder", f.path("lib/Elsewhere/Holder"), nil)
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("h%02d.mp3", i)
		p := f.file(t, filepath.Join("lib/Elsewhere/Holder", name), 777+i)
		f.row(t, name, holder, p, name, int64(777+i), 900, i)
	}
	plan := f.plan(t, "op-plan2")
	fx := newFragmentFixer(f.p)
	ctx, end := fx.BeginApply(context.Background(), false)
	defer end()
	require.NoError(t, applyRowInRun(t, f, fx, ctx, plan, rows[0]))
	// The lock-free file write: the holder's rows now carry set two's hashes.
	f.setHashes(t, []string{holder}, func(i int) string { return fmt.Sprintf("beta-%d", i) })
	err := applyRowInRun(t, f, fx, ctx, plan, rows[1])
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	require.Contains(t, err.Error(), holder)
	f.requireUntouched(t, frags[1])
	require.Equal(t, 1, fragSessionOf(ctx).loads)
}
