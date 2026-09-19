// file: internal/scanner/chapter_detect_review_test.go
// version: 1.1.0
// guid: 5a7c3e19-2b84-4d6f-9e01-c8b4f2a6d735
// last-edited: 2026-09-19

package scanner

import (
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Fixtures from the adversarial review of the first cut: each grouped
// DIFFERENT books (or two copies of one) as a mergeable group.

func rvBook(id, title, path string, dur int, author *int) database.BookCore {
	b := database.BookCore{ID: id, Title: title, FilePath: path, AuthorID: author}
	if dur > 0 {
		b.Duration = &dur
	}
	return b
}

// noMergeable fails when any mergeable group contains two of the given ids.
func noMergeable(t *testing.T, d ChapterDetection) {
	t.Helper()
	if len(d.Groups) != 0 {
		t.Fatalf("different books offered for merge: %+v", d.Groups)
	}
}

// (1) A real title with a numbered FILE is not a chapter: the filename alone
// never makes a candidate.
func TestReview_RealTitlesWithNumberedFilesNeverGroup(t *testing.T) {
	a := 7
	cases := map[string][]database.BookCore{
		"series volumes 'Saga - 01.m4b'": {
			rvBook("a1", "Leviathan Rising", "/lib/Jim Q/The Saga/The Saga - 01.m4b", 60000, &a),
			rvBook("a2", "Calibans Gate", "/lib/Jim Q/The Saga/The Saga - 02.m4b", 65000, &a),
			rvBook("a3", "Abaddons Road", "/lib/Jim Q/The Saga/The Saga - 03.m4b", 70000, &a),
			rvBook("a4", "Cibola Fire", "/lib/Jim Q/The Saga/The Saga - 04.m4b", 72000, &a),
			rvBook("a5", "Nemesis Play", "/lib/Jim Q/The Saga/The Saga - 05.m4b", 75000, &a),
		},
		"series volumes '01 - Saga.m4b'": {
			rvBook("b1", "Leviathan Rising", "/lib/Jim Q/The Saga/01 - The Saga.m4b", 60000, &a),
			rvBook("b2", "Calibans Gate", "/lib/Jim Q/The Saga/02 - The Saga.m4b", 65000, &a),
			rvBook("b3", "Abaddons Road", "/lib/Jim Q/The Saga/03 - The Saga.m4b", 70000, &a),
		},
		"'Book N' titles, Dune_N files": {
			rvBook("c1", "Book 1", "/lib/A/Dunes/Dunes_1.m4b", 40000, nil),
			rvBook("c2", "Book 2", "/lib/A/Dunes/Dunes_2.m4b", 45000, nil),
			rvBook("c3", "Book 3", "/lib/A/Dunes/Dunes_3.m4b", 50000, nil),
			rvBook("c4", "Book 4", "/lib/A/Dunes/Dunes_4.m4b", 55000, nil),
		},
		"trilogy Book_N files with real titles": {
			rvBook("d1", "The First Road", "/lib/Author/Trilogy/Book_1.m4b", 40000, &a),
			rvBook("d2", "The Second Road", "/lib/Author/Trilogy/Book_2.m4b", 41000, &a),
			rvBook("d3", "The Third Road", "/lib/Author/Trilogy/Book_3.m4b", 42000, &a),
		},
	}
	for name, books := range cases {
		t.Run(name, func(t *testing.T) {
			d := shDetect(books)
			noMergeable(t, d)
			if len(d.Blocked) != 0 {
				t.Fatalf("real titles must not even be candidates: %+v", d.Blocked)
			}
		})
	}
}

// (1b) Non-marker titles that all agree with the file residual are allowed
// in, but book-length members with non-marker titles are blocked.
func TestReview_FullLengthNonMarkerTitlesBlock(t *testing.T) {
	a := 7
	books := []database.BookCore{
		rvBook("a1", "The Saga", "/lib/Jim Q/The Saga/The Saga - 01.m4b", 60000, &a),
		rvBook("a2", "The Saga", "/lib/Jim Q/The Saga/The Saga - 02.m4b", 65000, &a),
		rvBook("a3", "The Saga", "/lib/Jim Q/The Saga/The Saga - 03.m4b", 90000, &a),
	}
	d := shDetect(books)
	noMergeable(t, d)
	if len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "book-length") {
		t.Fatalf("want book-length non-marker run blocked, got %+v", d.Blocked)
	}
}

// (2) Plain-numbered and disc-track copies of one book in one folder are two
// copies, not one 20-part book.
func TestReview_DiscAndPlainNumberingMixBlocks(t *testing.T) {
	a := 3
	var books []database.BookCore
	for i := 1; i <= 10; i++ {
		books = append(books,
			rvBook(fmt.Sprintf("p%02d", i), fmt.Sprintf("%02d Anansi Lads", i), fmt.Sprintf("/lib/Gai Q/Anansi Lads/%02d Anansi Lads.mp3", i), 1500, &a),
			rvBook(fmt.Sprintf("d%02d", i), fmt.Sprintf("1-%02d Anansi Lads", i), fmt.Sprintf("/lib/Gai Q/Anansi Lads/1-%02d Anansi Lads.mp3", i), 1500, &a))
	}
	d := shDetect(books)
	noMergeable(t, d)
	if len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "two copies") {
		t.Fatalf("want the mixed numbering blocked as two copies, got %+v", d.Blocked)
	}
}

// (2b) A bare title takes the disc from a disc-track file name.
func TestReview_BareTitleTakesDiscFromFile(t *testing.T) {
	a := 3
	var books []database.BookCore
	for d := 1; d <= 2; d++ {
		for i := 1; i <= 4; i++ {
			books = append(books, rvBook(fmt.Sprintf("x%d%02d", d, i), fmt.Sprint(i),
				fmt.Sprintf("/lib/Gai Q/Yard/%d-%02d The Yard.mp3", d, i), 1500, &a))
		}
	}
	d := shDetect(books)
	if len(d.Groups) != 1 || d.Groups[0].IndexLabels[4] != "2-01" {
		t.Fatalf("want one disc-aware group (label 2-01 at position 5), got groups=%+v blocked=%+v", d.Groups, d.Blocked)
	}
}

// (3) Unrelated bare-numbered files in a catch-all folder, book-length, no
// author: never mergeable, and never titled after the folder.
func TestReview_UnrelatedBareNumberedFilesDoNotMerge(t *testing.T) {
	for _, stem := range []string{"Track %02d", "%02d"} {
		t.Run(stem, func(t *testing.T) {
			var books []database.BookCore
			for i := 1; i <= 4; i++ {
				books = append(books, rvBook(fmt.Sprintf("u%d", i), fmt.Sprint(i),
					"/lib/Unknown Author/"+fmt.Sprintf(stem, i)+".mp3", 30000+i*1000, nil))
			}
			d := shDetect(books)
			noMergeable(t, d)
			for _, g := range d.Blocked {
				if strings.EqualFold(g.CommonTitle, "Unknown Author") {
					t.Fatalf("proposed a catch-all folder name as a title: %+v", g)
				}
			}
		})
	}
}

// (3b) A bare run with no known durations is not mergeable: it is reported
// as needing durations.
func TestReview_BareRunWithoutDurationsNeedsDurations(t *testing.T) {
	a := 3
	var books []database.BookCore
	for i := 1; i <= 6; i++ {
		books = append(books, rvBook(fmt.Sprintf("n%d", i), fmt.Sprint(i), fmt.Sprintf("/lib/A/The Long Road/%02d.mp3", i), 0, &a))
	}
	d := shDetect(books)
	noMergeable(t, d)
	if len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "needs durations") {
		t.Fatalf("want needs-durations block, got %+v", d.Blocked)
	}
}

// (4) Bare records beside a folder-named run only join it with
// corroboration; otherwise they are reported separately for review.
func TestReview_BareJoinNeedsCorroboration(t *testing.T) {
	a := 3
	var books []database.BookCore
	for i := 1; i <= 5; i++ {
		books = append(books, rvBook(fmt.Sprintf("r%d", i), fmt.Sprintf("%02d Anansi Lads", i), fmt.Sprintf("/lib/Gai Q/Anansi Lads/%02d Anansi Lads.mp3", i), 1500, &a))
	}
	bare := func(dur int) []database.BookCore {
		var out []database.BookCore
		for i := 6; i <= 8; i++ {
			out = append(out, rvBook(fmt.Sprintf("t%d", i), fmt.Sprint(i), fmt.Sprintf("/lib/Gai Q/Anansi Lads/Track %02d.m4a", i), dur, &a))
		}
		return out
	}
	d := shDetect(append(append([]database.BookCore{}, books...), bare(0)...))
	if len(d.Groups) != 1 || len(d.Groups[0].BookIDs) != 5 {
		t.Fatalf("uncorroborated bare records joined the run: %+v", d.Groups)
	}
	if len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "needs review") {
		t.Fatalf("want the bare records reported for review, got %+v", d.Blocked)
	}
}

// (6) Confidence reflects evidence: high needs marker titles, a contiguous
// run and known chapter-length durations; anything missing is listed.
func TestReview_ConfidenceReflectsEvidence(t *testing.T) {
	a := 3
	mk := func(dur int, skip int) []database.BookCore {
		var out []database.BookCore
		for i := 1; i <= 6; i++ {
			if i == skip {
				continue
			}
			out = append(out, rvBook(fmt.Sprintf("c%d", i), fmt.Sprintf("%03d_Head of the Wyrm", i), fmt.Sprintf("/lib/A/Head of the Wyrm/%03d_Head of the Wyrm.mp3", i), dur, &a))
		}
		return out
	}
	if d := shDetect(mk(1200, 0)); len(d.Groups) != 1 || d.Groups[0].Confidence != ChapterConfidenceHigh {
		t.Fatalf("full evidence: want high, got %+v", d.Groups)
	}
	d := shDetect(mk(0, 0))
	if len(d.Groups) != 1 || d.Groups[0].Confidence == ChapterConfidenceHigh || !anyContains(d.Groups[0].Reasons, "durations unknown") {
		t.Fatalf("unknown durations: want not-high listing the missing evidence, got %+v", d.Groups)
	}
	d = shDetect(mk(1200, 3))
	if len(d.Groups) != 1 || d.Groups[0].Confidence == ChapterConfidenceHigh || !anyContains(d.Groups[0].Reasons, "gaps") {
		t.Fatalf("gap: want not-high listing gaps, got %+v", d.Groups)
	}
}

// --- Second review round (probe cases) ---

// (R2-1) Untagged series volumes whose titles are their stems ("01 - The
// Tower") are positions in shape, but every member is book-length: blocked.
// (A declared "N of M" total does not exempt them: see
// TestReview3_DeclaredTotalDoesNotExemptBookLength.)
func TestReview2_BookLengthVolumesBlockUnlessTotalDeclared(t *testing.T) {
	a := 7
	var books []database.BookCore
	for i, d := range []int{25000, 40000, 55000, 60000, 80000, 90000, 100000} {
		n := fmt.Sprintf("%02d - The Tower", i+1)
		books = append(books, rvBook(fmt.Sprintf("v%d", i), n, "/lib/Kin Q/The Tower/"+n+".m4b", d, &a))
	}
	d := shDetect(books)
	noMergeable(t, d)
	if len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "book-length") {
		t.Fatalf("want book-length volumes blocked, got %+v", d.Blocked)
	}
	// "Part N" bare-token volumes are the same case.
	var parts []database.BookCore
	for i := 1; i <= 3; i++ {
		parts = append(parts, rvBook(fmt.Sprintf("s%d", i), fmt.Sprintf("Part %d", i), fmt.Sprintf("/lib/A/Stand/The Stand Part %d.m4b", i), 40000+i*5000, &a))
	}
	noMergeable(t, shDetect(parts))
	// One unknown duration does not hide the rest being book-length.
	var saga []database.BookCore
	for i, dur := range []int{60000, 65000, 0, 72000} {
		n := fmt.Sprintf("The Saga - %02d", i+1)
		saga = append(saga, rvBook(fmt.Sprintf("g%d", i), n, "/lib/Jim Q/The Saga/"+n+".m4b", dur, &a))
	}
	noMergeable(t, shDetect(saga))
}

// (R2-2) Book names containing "Part N - Subtitle" never regroup by the
// part before the token; a singleton needs chapter-length duration to move.
func TestReview2_PartNamedVolumesDoNotRegroup(t *testing.T) {
	a := 7
	var books []database.BookCore
	for i, s := range []string{"The Gunslinger", "The Drawing of the Three", "The Waste Lands", "Wizard and Glass"} {
		n := fmt.Sprintf("%02d - The Tower Part %d - %s", i+1, i+1, s)
		books = append(books, rvBook(fmt.Sprintf("t%d", i), n, "/lib/Kin Q/The Tower/"+n+".m4b", 30000+i*9000, &a))
	}
	d := shDetect(books)
	noMergeable(t, d)
	if len(d.Blocked) != 0 {
		t.Fatalf("book-length singletons must not regroup at all: %+v", d.Blocked)
	}
	// Same shape at chapter length with unknown durations: no corroboration, no regroup.
	var unk []database.BookCore
	for i := 1; i <= 4; i++ {
		n := fmt.Sprintf("%02d - Tunnels Chapter %d - Name %d", i, i, i)
		unk = append(unk, rvBook(fmt.Sprintf("u%d", i), n, "/lib/A/Tunnels/"+n+".m4b", 0, &a))
	}
	if d := shDetect(unk); len(d.Groups) != 0 {
		t.Fatalf("uncorroborated singletons regrouped: %+v", d.Groups)
	}
}

// (R2-4) Episodes numbered only by a trailing file number with non-position
// titles are at most low confidence (never bulk-selectable, and the merge
// job refuses low without an explicit acknowledgement).
func TestReview2_TrailingEpisodesAreLow(t *testing.T) {
	a := 7
	var books []database.BookCore
	for i := 1; i <= 6; i++ {
		n := fmt.Sprintf("Lore - %03d", i)
		books = append(books, rvBook(fmt.Sprintf("e%d", i), n, "/lib/Aar Q/Lore/"+n+".mp3", 2700, &a))
	}
	d := shDetect(books)
	for _, g := range d.Groups {
		if g.Confidence != ChapterConfidenceLow {
			t.Fatalf("episodic trailing run offered at %s: %+v", g.Confidence, g)
		}
	}
}

// (R2-5) Bare titles need corroboration whatever the key: no author and no
// durations block even when the file supplies a residual.
func TestReview2_BareTitlesNeedCorroborationWhateverTheKey(t *testing.T) {
	var books []database.BookCore
	for i := 1; i <= 5; i++ {
		books = append(books, rvBook(fmt.Sprintf("b%d", i), fmt.Sprint(i), fmt.Sprintf("/lib/Poe/Poe/%02d - Poe.mp3", i), 1800, nil))
	}
	d := shDetect(books)
	noMergeable(t, d)
	if len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "no author") {
		t.Fatalf("want bare/no-author run blocked, got %+v", d.Blocked)
	}
	a := 7
	var nodur []database.BookCore
	for i := 1; i <= 5; i++ {
		nodur = append(nodur, rvBook(fmt.Sprintf("n%d", i), fmt.Sprint(i), fmt.Sprintf("/lib/Poe/Poe/%02d - Poe.mp3", i), 0, &a))
	}
	d = shDetect(nodur)
	noMergeable(t, d)
	if len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "needs durations") {
		t.Fatalf("want bare/no-duration run blocked, got %+v", d.Blocked)
	}
}

// --- Third review round ---

// (R3-2) A declared total does not turn an all-book-length run into a
// mergeable one: "Part 1 of 3".."Part 3 of 3" at 60-70k s are three novels.
func TestReview3_DeclaredTotalDoesNotExemptBookLength(t *testing.T) {
	a := 7
	mk := func(prefix, dir string, titles []string, durs []int) []database.BookCore {
		var out []database.BookCore
		for i, ti := range titles {
			out = append(out, rvBook(fmt.Sprintf("%s%d", prefix, i), ti, dir+ti+".m4b", durs[i], &a))
		}
		return out
	}
	cases := map[string][]database.BookCore{
		"Part N of 3":        mk("d1", "/lib/T/Ring Q/", []string{"Part 1 of 3", "Part 2 of 3", "Part 3 of 3"}, []int{70000, 60000, 65000}),
		"Series N of 3":      mk("d2", "/lib/T/Ring Q2/", []string{"The Ring Q 1 of 3", "The Ring Q 2 of 3", "The Ring Q 3 of 3"}, []int{70000, 60000, 65000}),
		"bare N of 3":        mk("d3", "/lib/T/Ring Q3/", []string{"1 of 3", "2 of 3", "3 of 3"}, []int{70000, 60000, 65000}),
		"Title, Part N of 2": mk("d6", "/lib/S/Kings Q/", []string{"The Kings Q, Part 1 of 2", "The Kings Q, Part 2 of 2"}, []int{90000, 85000}),
	}
	for name, books := range cases {
		t.Run(name, func(t *testing.T) { noMergeable(t, shDetect(books)) })
	}
}

// (R3-3) A run mixing chapter-length and book-length members is low, and
// names the book-length members.
func TestReview3_BookLengthOutliersAreLowAndNamed(t *testing.T) {
	a := 7
	for name, durs := range map[string][]int{
		"one book-length": {1500, 1600, 40000, 1400, 1500},
		"two book-length": {1500, 50000, 40000, 1400, 1500},
	} {
		t.Run(name, func(t *testing.T) {
			var books []database.BookCore
			for i, d := range durs {
				n := fmt.Sprintf("Chapter %d", i+1)
				books = append(books, rvBook(fmt.Sprintf("m%d", i), n, "/lib/A/Mix/"+n+".mp3", d, &a))
			}
			d := shDetect(books)
			all := append(append([]ChapterGroup{}, d.Groups...), d.Blocked...)
			if len(all) != 1 {
				t.Fatalf("want one group, got %+v", d)
			}
			g := all[0]
			if len(d.Groups) == 1 && g.Confidence != ChapterConfidenceLow {
				t.Fatalf("mixed-length run offered at %s", g.Confidence)
			}
			named := false
			for _, r := range append(g.Reasons, g.Blockers...) {
				if i := strings.Index(r, "book-length members: "); i >= 0 && strings.Contains(r[i:], "3") {
					named = true
				}
			}
			if !named {
				t.Fatalf("book-length outliers not named: reasons=%v blockers=%v", g.Reasons, g.Blockers)
			}
		})
	}
}
