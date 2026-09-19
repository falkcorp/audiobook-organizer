// file: internal/scanner/chapter_consolidator_test.go
// version: 1.1.0
// guid: 04249a9d-8f2e-44f4-9652-258511aae768
// last-edited: 2026-09-19

package scanner

import (
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func chIntPtr(v int) *int { return &v }

func chBook(id, path string, dur *int) database.BookCore {
	return database.BookCore{ID: id, Title: "raw", FilePath: path, Duration: dur}
}

// (a) The primary must be chapter 01, not whichever row the store listed
// first. Store order is a key order (ULID/hash), so "BookIDs[0]" picked an
// arbitrary chapter as the book every other chapter was folded into.
func TestDetectChapterGroups_PrimaryIsLowestChapterNumber(t *testing.T) {
	books := []database.BookCore{
		chBook("z-ch3", "/lib/A/Book/03 - My Book.mp3", chIntPtr(300)),
		chBook("a-ch10", "/lib/A/Book/10 - My Book.mp3", chIntPtr(300)),
		chBook("m-ch1", "/lib/A/Book/01 - My Book.mp3", chIntPtr(300)),
		chBook("b-ch2", "/lib/A/Book/02 - My Book.mp3", chIntPtr(300)),
	}
	groups := DetectChapterGroups(books, 2, 600)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	want := []string{"m-ch1", "b-ch2", "z-ch3", "a-ch10"}
	for i, id := range want {
		if groups[0].BookIDs[i] != id {
			t.Fatalf("BookIDs = %v, want %v (numeric chapter order)", groups[0].BookIDs, want)
		}
	}
}

// (c) A book whose duration is unknown must not count as "short". nil read as
// 0 seconds, so a group of full-length books with no probed duration always
// passed the all-short test and became a merge candidate.
func TestDetectChapterGroups_UnknownDurationIsNotShort(t *testing.T) {
	books := []database.BookCore{
		chBook("1", "/lib/A/Book/01 - Novel.m4b", nil),
		chBook("2", "/lib/A/Book/02 - Novel.m4b", nil),
		chBook("3", "/lib/A/Book/03 - Novel.m4b", nil),
	}
	if groups := DetectChapterGroups(books, 2, 600); len(groups) != 0 {
		t.Fatalf("all-unknown-duration group must not be detected, got %+v", groups)
	}
}

// (c) The OR branch (enough files AND average < 30 min) summed nil as 0 too,
// so one known long file plus unknown siblings averaged "short".
func TestDetectChapterGroups_UnknownDurationDoesNotPassAverageBranch(t *testing.T) {
	books := []database.BookCore{
		chBook("1", "/lib/A/Book/01 - Novel.m4b", chIntPtr(4000)),
		chBook("2", "/lib/A/Book/02 - Novel.m4b", nil),
		chBook("3", "/lib/A/Book/03 - Novel.m4b", nil),
		chBook("4", "/lib/A/Book/04 - Novel.m4b", chIntPtr(0)),
	}
	if groups := DetectChapterGroups(books, 2, 600); len(groups) != 0 {
		t.Fatalf("group with unknown durations must not pass the average branch, got %+v", groups)
	}
}

// (d) Books already absorbed by an earlier merge must not be regrouped, or a
// re-run re-merges them.
func TestDetectChapterGroups_ExcludesMergedAndDeletedAndNonPrimary(t *testing.T) {
	primary := "p"
	yes := true
	no := false
	merged := chBook("m", "/lib/A/Book/02 - My Book.mp3", chIntPtr(300))
	merged.MergedIntoBookID = &primary
	deleted := chBook("d", "/lib/A/Book/03 - My Book.mp3", chIntPtr(300))
	deleted.MarkedForDeletion = &yes
	nonPrimary := chBook("n", "/lib/A/Book/04 - My Book.mp3", chIntPtr(300))
	nonPrimary.IsPrimaryVersion = &no
	books := []database.BookCore{
		chBook("p", "/lib/A/Book/01 - My Book.mp3", chIntPtr(300)),
		merged, deleted, nonPrimary,
	}
	if groups := DetectChapterGroups(books, 2, 600); len(groups) != 0 {
		t.Fatalf("only one live chapter remains; want no groups, got %+v", groups)
	}
}

func TestDetectChapterGroupsWithOptions_HonoursParams(t *testing.T) {
	books := []database.BookCore{
		chBook("a1", "/lib/A/Book/01 - Tale.mp3", chIntPtr(900)),
		chBook("a2", "/lib/A/Book/02 - Tale.mp3", chIntPtr(900)),
		chBook("b1", "/lib/AB/Other/01 - Saga.mp3", chIntPtr(300)),
		chBook("b2", "/lib/AB/Other/02 - Saga.mp3", chIntPtr(300)),
	}

	// max_per_file_duration: 900 s files are not "short" at 600, and with
	// min_files=3 the average branch does not apply to a 2-file group.
	d := DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 3, MaxPerFileDuration: 600})
	if len(d.Groups) != 1 || d.Groups[0].PrimaryBookID != "b1" {
		t.Fatalf("max 600/min 3: want only the Saga group, got %+v", d.Groups)
	}
	// Raising the ceiling admits the Tale group.
	d = DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 3, MaxPerFileDuration: 1000})
	if len(d.Groups) != 2 {
		t.Fatalf("max 1000: want 2 groups, got %+v", d.Groups)
	}
	// min_files=2 lets the average branch (900 s < 30 min) admit Tale at 600.
	d = DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2, MaxPerFileDuration: 600})
	if len(d.Groups) != 2 {
		t.Fatalf("min 2: want 2 groups, got %+v", d.Groups)
	}
	// path_prefix matches on a directory boundary: /lib/A must not match /lib/AB.
	d = DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2, MaxPerFileDuration: 1000, PathPrefix: "/lib/A"})
	if len(d.Groups) != 1 || d.Groups[0].PrimaryBookID != "a1" || d.Groups[0].Directory != "/lib/A/Book" {
		t.Fatalf("prefix /lib/A: want only the Tale group, got %+v", d.Groups)
	}
	if d.Groups[0].CommonTitle != "Tale" || d.Groups[0].TotalDuration != 1800 || d.Groups[0].FileCount != 2 {
		t.Fatalf("group summary wrong: %+v", d.Groups[0])
	}
}

func TestDetectChapterGroupsWithOptions_CountsUnknownDurationSkips(t *testing.T) {
	books := []database.BookCore{
		chBook("1", "/lib/A/Book/01 - Novel.m4b", nil),
		chBook("2", "/lib/A/Book/02 - Novel.m4b", chIntPtr(100)),
	}
	d := DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2})
	if len(d.Groups) != 0 || d.SkippedUnknownDuration != 1 {
		t.Fatalf("want 0 groups and 1 unknown-duration skip, got %+v", d)
	}
}

func TestChapterTitleIsFilenameDerived(t *testing.T) {
	const p = "/lib/A/Book/01 - My Book.mp3"
	cases := map[string]bool{
		"":                      true,
		"01 - My Book":          true,
		"My Book":               true,
		"my book":               true,
		"My Book: The Original": false,
		"A Curated Title":       false,
	}
	for title, want := range cases {
		if got := ChapterTitleIsFilenameDerived(title, p); got != want {
			t.Errorf("ChapterTitleIsFilenameDerived(%q) = %v, want %v", title, got, want)
		}
	}
}

// chGroupOf detects over one directory of chapter-shaped files, all with the
// given per-file duration, and returns the groups.
func chGroupOf(t *testing.T, dur int, names ...string) []ChapterGroup {
	t.Helper()
	books := make([]database.BookCore, len(names))
	for i, n := range names {
		books[i] = chBook(fmt.Sprintf("b%02d", i), "/lib/A/Dir/"+n, chIntPtr(dur))
	}
	return DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2, MaxPerFileDuration: 600}).Groups
}

// Golden shapes that must NOT form a chapter group. Each one merged distinct
// books under the old prefix / 80%-word-overlap similarity.
func TestDetectChapterGroups_GoldenMustNotGroup(t *testing.T) {
	cases := map[string][]string{
		"character prefix It/Ithaca":    {"01 - It.mp3", "02 - Ithaca.mp3"},
		"word prefix Dune/Dune Messiah": {"01 - Dune.mp3", "02 - Dune Messiah.mp3"},
		"empty stripped title":          {"01.mp3", "02.mp3", "03.mp3"},
		"too-short stripped title":      {"01 - It.mp3", "02 - It.mp3"},
		"numbered series books":         {"01 - Wheel of Time Book 01.mp3", "02 - Wheel of Time Book 02.mp3"},
		"numbered episodes":             {"01 - The Show Episode 1.mp3", "02 - The Show Episode 2.mp3"},
		"volume numbers":                {"01 - Saga Volume 1.mp3", "02 - Saga Volume 2.mp3"},
		"bare trailing number":          {"01 - Wheel of Time 01.mp3", "02 - Wheel of Time 02.mp3"},
		"differing non-numeric word":    {"01 - Short Trips Alpha.mp3", "02 - Short Trips Beta.mp3"},
		"80% overlap, one word differs": {"01 - The Long Dark Road Home.mp3", "02 - The Long Dark Road Back.mp3"},
		"mixed containers of one book":  {"01 - The Novel.mp3", "02 - The Novel.m4b"},
	}
	for name, files := range cases {
		if g := chGroupOf(t, 300, files...); len(g) != 0 {
			t.Errorf("%s: must not group, got %+v", name, g)
		}
	}
}

// Real chapter shapes that MUST group.
func TestDetectChapterGroups_GoldenMustGroup(t *testing.T) {
	cases := map[string][]string{
		"plain numbered chapters": {"01 - My Book.mp3", "02 - My Book.mp3", "03 - My Book.mp3"},
		"part tokens differ":      {"01 - My Book Part 1.mp3", "02 - My Book Part 2.mp3"},
		"chapter tokens differ":   {"01 - My Book - Chapter 1.mp3", "02 - My Book - Chapter 2.mp3"},
		"track tokens differ":     {"001 My Book Track 01.mp3", "002 My Book Track 02.mp3"},
		"joined cd tokens":        {"01 - My Book CD1.mp3", "02 - My Book CD2.mp3"},
	}
	for name, files := range cases {
		g := chGroupOf(t, 300, files...)
		if len(g) != 1 || g[0].FileCount != len(files) {
			t.Errorf("%s: want one group of %d, got %+v", name, len(files), g)
		}
	}
}

// Two full-length copies of one book whose names happen to be chapter-shaped
// are dedup's job, not chapter consolidation's.
func TestDetectChapterGroups_FullLengthCopiesAreNotChapters(t *testing.T) {
	books := []database.BookCore{
		chBook("a", "/lib/A/Dir/01 - The Novel.mp3", chIntPtr(36000)),
		chBook("b", "/lib/A/Dir/02 - The Novel.mp3", chIntPtr(36300)),
	}
	d := DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2, MaxPerFileDuration: 100000})
	if len(d.Groups) != 0 || d.SkippedDuplicateCopies != 1 {
		t.Fatalf("full-length copies grouped: %+v", d)
	}
}

func TestDetectChapterGroups_ExcludeHook(t *testing.T) {
	books := []database.BookCore{
		chBook("a", "/lib/Doctor Who/Story/01 - The Story.mp3", chIntPtr(300)),
		chBook("b", "/lib/Doctor Who/Story/02 - The Story.mp3", chIntPtr(300)),
	}
	d := DetectChapterGroupsWithOptions(books, ChapterDetectOptions{
		MinFiles: 2,
		Exclude:  func(b *database.BookCore) bool { return strings.Contains(b.FilePath, "Doctor Who") },
	})
	if len(d.Groups) != 0 || d.SkippedExcluded != 2 {
		t.Fatalf("excluded books grouped: %+v", d)
	}
}
