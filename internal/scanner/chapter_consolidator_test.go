// file: internal/scanner/chapter_consolidator_test.go
// version: 1.3.0
// guid: 04249a9d-8f2e-44f4-9652-258511aae768
// last-edited: 2026-09-19

package scanner

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func chIntPtr(v int) *int { return &v }

// chBook is a record whose title is still its file stem (the scanner's
// filename-derived title): a real title with only a numbered FILE is never a
// chapter candidate (see TestReview_RealTitlesWithNumberedFilesNeverGroup).
func chBook(id, path string, dur *int) database.BookCore {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return database.BookCore{ID: id, Title: stem, FilePath: path, Duration: dur}
}

// (a) The primary must be chapter 01, not whichever row the store listed
// first. Store order is a key order (ULID/hash), so "BookIDs[0]" picked an
// arbitrary chapter as the book every other chapter was folded into.
func TestDetectChapterGroups_PrimaryIsLowestChapterNumber(t *testing.T) {
	// Store order scrambled, and "10" must sort after "9" (numeric, not
	// lexical).
	var books []database.BookCore
	for _, n := range []int{3, 10, 1, 7, 2, 9, 4, 8, 6, 5} {
		books = append(books, chBook(fmt.Sprintf("id-%02d", 11-n), fmt.Sprintf("/lib/A/Book/%d - My Book.mp3", n), chIntPtr(300)))
	}
	groups := DetectChapterGroups(books, 2, 600)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	for i, id := range groups[0].BookIDs {
		if want := fmt.Sprintf("id-%02d", 10-i); id != want {
			t.Fatalf("BookIDs = %v, want numeric chapter order", groups[0].BookIDs)
		}
	}
	if groups[0].PrimaryBookID != "id-10" {
		t.Fatalf("primary %s, want chapter 1's id-10", groups[0].PrimaryBookID)
	}
}

// (c) Durations are advisory, never required. A group whose durations are
// unknown (nil, or 0 from a failed probe) is still a group -- the owner's
// worst splits (hundreds of files, none probed) were invisible while an
// unknown duration skipped the group -- and the unknown count is reported.
func TestDetectChapterGroups_UnknownDurationStillGroups(t *testing.T) {
	books := []database.BookCore{
		chBook("1", "/lib/A/Book/01 - Novel.m4b", nil),
		chBook("2", "/lib/A/Book/02 - Novel.m4b", chIntPtr(0)),
		chBook("3", "/lib/A/Book/03 - Novel.m4b", chIntPtr(400)),
	}
	d := DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2})
	if len(d.Groups) != 1 || d.Groups[0].FileCount != 3 {
		t.Fatalf("want one group of 3, got %+v", d)
	}
	g := d.Groups[0]
	if g.DurationsKnown != 1 || g.TotalDuration != 400 {
		t.Fatalf("duration summary wrong: known=%d total=%v", g.DurationsKnown, g.TotalDuration)
	}
}

// (d) Books already absorbed by an earlier merge must not be regrouped, or a
// re-run re-merges them.
func TestDetectChapterGroups_ExcludesMergedAndDeletedAndNonPrimary(t *testing.T) {
	primary := "p"
	yes := true
	no := false
	merged := chBook("m", "/lib/A/Book/03 - My Book.mp3", chIntPtr(300))
	merged.MergedIntoBookID = &primary
	deleted := chBook("d", "/lib/A/Book/04 - My Book.mp3", chIntPtr(300))
	deleted.MarkedForDeletion = &yes
	nonPrimary := chBook("n", "/lib/A/Book/02 - My Book.mp3", chIntPtr(300))
	nonPrimary.IsPrimaryVersion = &no
	books := []database.BookCore{
		chBook("p", "/lib/A/Book/01 - My Book.mp3", chIntPtr(300)),
		merged, deleted, nonPrimary,
	}
	d := DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2})
	if len(d.Groups) != 0 {
		t.Fatalf("a non-primary member must never be offered for merge, got %+v", d.Groups)
	}
	// The primary chapter and the non-primary one are still reported, blocked.
	if len(d.Blocked) != 1 || len(d.Blocked[0].Blockers) == 0 {
		t.Fatalf("want the mixed-version group reported as blocked, got %+v", d.Blocked)
	}
}

func TestDetectChapterGroupsWithOptions_HonoursParams(t *testing.T) {
	books := []database.BookCore{
		chBook("a1", "/lib/A/Tale/01 - Tale.mp3", chIntPtr(900)),
		chBook("a2", "/lib/A/Tale/02 - Tale.mp3", chIntPtr(900)),
		chBook("b1", "/lib/AB/Other/01 - Saga.mp3", chIntPtr(300)),
		chBook("b2", "/lib/AB/Other/02 - Saga.mp3", chIntPtr(300)),
		chBook("b3", "/lib/AB/Other/03 - Saga.mp3", chIntPtr(300)),
	}
	// min_files is the smallest group size.
	d := DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 3})
	if len(d.Groups) != 1 || d.Groups[0].PrimaryBookID != "b1" {
		t.Fatalf("min 3: want only the Saga group, got %+v", d.Groups)
	}
	d = DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2})
	if len(d.Groups) != 2 {
		t.Fatalf("min 2: want 2 groups, got %+v", d.Groups)
	}
	// max_per_file_duration no longer gates; a longer member is only noted.
	d = DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2, MaxPerFileDuration: 600})
	if len(d.Groups) != 2 {
		t.Fatalf("max 600: want 2 groups (duration is advisory), got %+v", d.Groups)
	}
	// path_prefix matches on a directory boundary: /lib/A must not match /lib/AB.
	d = DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2, PathPrefix: "/lib/A"})
	if len(d.Groups) != 1 || d.Groups[0].PrimaryBookID != "a1" || d.Groups[0].Directory != "/lib/A/Tale" {
		t.Fatalf("prefix /lib/A: want only the Tale group, got %+v", d.Groups)
	}
	if d.Groups[0].CommonTitle != "Tale" || d.Groups[0].TotalDuration != 1800 || d.Groups[0].FileCount != 2 {
		t.Fatalf("group summary wrong: %+v", d.Groups[0])
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
		"character prefix It/Ithaca": {"01 - It.mp3", "02 - Ithaca.mp3"},
		// Bare-numbered files with no author and nothing else tying them
		// together are never offered (they may be reported, blocked).
		"empty stripped title":          {"01.mp3", "02.mp3", "03.mp3"},
		"word prefix Dune/Dune Messiah": {"01 - Dune.mp3", "02 - Dune Messiah.mp3"},
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
