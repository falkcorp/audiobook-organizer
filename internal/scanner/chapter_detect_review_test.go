// file: internal/scanner/chapter_detect_review_test.go
// version: 1.0.0
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
