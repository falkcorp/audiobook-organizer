// file: internal/plugins/maintenance/fragment_folder_sets_test.go
// version: 1.3.1
// guid: 3b7d2c55-1a4e-4f0b-9c61-8e2f5d7a0b14
// last-edited: 2026-10-06

package maintenance

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
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
