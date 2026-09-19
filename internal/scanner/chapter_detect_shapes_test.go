// file: internal/scanner/chapter_detect_shapes_test.go
// version: 1.2.0
// guid: 8d2b6c1e-47a9-4b35-a0f2-6e1c9d3b7a52
// last-edited: 2026-09-19

package scanner

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// shBook is a single-file record with an explicit title (the chapter shapes
// the owner's library shows live in the TITLE; the filename is secondary).
func shBook(id, title, path string) database.BookCore {
	return database.BookCore{ID: id, Title: title, FilePath: path}
}

// shRun builds n records in one folder with title(i) and file(i), created in
// reverse order so store order is not chapter order.
func shRun(prefix, dir string, from, to int, title, file func(i int) string) []database.BookCore {
	var out []database.BookCore
	for i := to; i >= from; i-- {
		out = append(out, shBook(fmt.Sprintf("%s%04d", prefix, i), title(i), dir+"/"+file(i)))
	}
	return out
}

// shEv gives every record a known duration and author: the corroboration a
// bare-numbered run needs before it is offered.
func shEv(books []database.BookCore, dur int) []database.BookCore {
	author := 42
	for i := range books {
		d := dur
		books[i].Duration = &d
		books[i].AuthorID = &author
	}
	return books
}

func shDetect(books []database.BookCore) ChapterDetection {
	return DetectChapterGroupsWithOptions(books, ChapterDetectOptions{MinFiles: 2})
}

// The real title shapes, each in its own folder, must group into exactly one
// group in index order with a proposed title that is not the number.
func TestDetectChapterGroups_RealTitleShapes(t *testing.T) {
	cases := []struct {
		name      string
		books     []database.BookCore
		wantN     int
		wantTitle string
	}{
		{
			name: "bare number title, 'Title - NNN' file",
			books: shRun("e", "/lib/Author/Cycle/Eldritch", 1, 40,
				func(i int) string { return fmt.Sprint(i) },
				func(i int) string { return fmt.Sprintf("Eldritch - %d.mp3", i) }),
			wantN: 40, wantTitle: "Eldritch",
		},
		{
			name: "N of M title",
			books: shRun("w", "/lib/Author/Prism/Prism - 5 - The Burning Glass", 1, 30,
				func(i int) string { return fmt.Sprintf("%d of 30", i) },
				func(i int) string { return fmt.Sprintf("%d Some Other Book - %d of 30.mp3", i, i) }),
			wantN: 30, wantTitle: "The Burning Glass",
		},
		{
			name: "NNN_Title",
			books: shRun("r", "/lib/imported/Author/06_Head of the Wyrm", 1, 25,
				func(i int) string { return fmt.Sprintf("%03d_Head of the Wyrm", i) },
				func(i int) string { return fmt.Sprintf("%03d_Head of the Wyrm.mp3", i) }),
			wantN: 25, wantTitle: "Head of the Wyrm",
		},
		{
			name: "bare title mixed with NNN_Title, folder names the book",
			books: append(
				shRun("x", "/lib/imported/Author/07_The Wide Sea", 1, 10,
					func(i int) string { return fmt.Sprintf("%03d_The Wide Sea", i) },
					func(i int) string { return fmt.Sprintf("%03d_The Wide Sea.mp3", i) }),
				shRun("y", "/lib/imported/Author/07_The Wide Sea", 11, 20,
					func(i int) string { return fmt.Sprint(i) },
					func(i int) string { return fmt.Sprintf("%03d_The Wide Sea.mp3", i) })...),
			wantN: 20, wantTitle: "The Wide Sea",
		},
		{
			name: "zero-padded bare title, 'Title - Author - NNN' file",
			books: shRun("f", "/lib/Author/Moon and Stone/Moon and Stone", 1, 12,
				func(i int) string { return fmt.Sprintf("%03d", i) },
				func(i int) string { return fmt.Sprintf("Moon and Stone - Jane Author - %03d.mp3", i) }),
			wantN: 12, wantTitle: "Moon and Stone",
		},
		{
			name: "disc-track title",
			books: append(
				shRun("g", "/lib/Media/Audiobooks/Some Author", 1, 8,
					func(i int) string { return fmt.Sprintf("1-%02d The Hollow Yard", i) },
					func(i int) string { return fmt.Sprintf("1-%02d The Hollow Yard.m4b", i) }),
				shRun("h", "/lib/Media/Audiobooks/Some Author", 1, 6,
					func(i int) string { return fmt.Sprintf("2-%02d The Hollow Yard", i) },
					func(i int) string { return fmt.Sprintf("2-%02d The Hollow Yard.m4b", i) })...),
			wantN: 14, wantTitle: "The Hollow Yard",
		},
		{
			name: "NN Title NN-MM",
			books: shRun("s", "/lib/Media/Audiobooks/Other Author", 1, 9,
				func(i int) string { return fmt.Sprintf("%02d Heir of Ash %02d-09", i, i) },
				func(i int) string { return fmt.Sprintf("%02d Heir of Ash %02d-09.m4b", i, i) }),
			wantN: 9, wantTitle: "Heir of Ash",
		},
		{
			name: "Part N titles",
			books: shEv(shRun("p", "/lib/Author/The Long Road", 1, 4,
				func(i int) string { return fmt.Sprintf("Part %d", i) },
				func(i int) string { return fmt.Sprintf("Part %d.mp3", i) }), 1500),
			wantN: 4, wantTitle: "The Long Road",
		},
		{
			name: "chapter-named tracks share only the book",
			books: shEv(shRun("c", "/lib/Author/Tunnels", 1, 5,
				func(i int) string { return fmt.Sprintf("%02d Chapter %d - Name %c", i, i, 'A'+rune(i)) },
				func(i int) string { return fmt.Sprintf("%02d Chapter %d - Name %c.m4b", i, i, 'A'+rune(i)) }), 1500),
			wantN: 5, wantTitle: "Tunnels",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := shDetect(c.books)
			if len(d.Groups) != 1 {
				t.Fatalf("want 1 group, got %d groups (blocked %+v)", len(d.Groups), d.Blocked)
			}
			g := d.Groups[0]
			if g.FileCount != c.wantN || len(g.BookIDs) != c.wantN {
				t.Fatalf("want %d members, got %d", c.wantN, g.FileCount)
			}
			if g.CommonTitle != c.wantTitle {
				t.Errorf("proposed title %q, want %q", g.CommonTitle, c.wantTitle)
			}
			if g.PrimaryBookID != g.BookIDs[0] {
				t.Errorf("primary %s is not the lowest index %s", g.PrimaryBookID, g.BookIDs[0])
			}
			// Members come back in index order: labels strictly ascending.
			for i := 1; i < len(g.IndexLabels); i++ {
				if seqLabelLess(g.IndexLabels[i], g.IndexLabels[i-1]) {
					t.Fatalf("members out of order: %v", g.IndexLabels)
				}
			}
			if g.Confidence == "" || len(g.Reasons) == 0 {
				t.Errorf("group has no confidence/reasons: %+v", g)
			}
		})
	}
}

func seqLabelLess(a, b string) bool {
	var ad, ai, bd, bi int
	if strings.Contains(a, "-") {
		fmt.Sscanf(a, "%d-%d", &ad, &ai)
		fmt.Sscanf(b, "%d-%d", &bd, &bi)
	} else {
		fmt.Sscanf(a, "%d", &ai)
		fmt.Sscanf(b, "%d", &bi)
	}
	if ad != bd {
		return ad < bd
	}
	return ai < bi
}

// Legit single books whose titles start with a number never group: each
// lives alone in its folder, or shares a folder with differently-titled
// books (a series folder of organized singles).
func TestDetectChapterGroups_LegitNumericTitlesDoNotGroup(t *testing.T) {
	books := []database.BookCore{
		shBook("a", "1984", "/lib/Orwell Q/1984/1984.m4b"),
		shBook("b", "11/22/63", "/lib/King Q/11-22-63/11-22-63.m4b"),
		shBook("c", "2001: A Space Voyage", "/lib/Clark Q/2001/2001 A Space Voyage.m4b"),
		shBook("d", "84K", "/lib/North Q/84K/84K.m4b"),
		// Organized series singles sharing one folder: residuals differ.
		shBook("e", "09 - Ruins of the Deep", "/lib/Series Q/Deep Saga/09 - Ruins of the Deep.m4b"),
		shBook("f", "10 - Rise of the Deep", "/lib/Series Q/Deep Saga/10 - Rise of the Deep.m4b"),
		shBook("g", "11 - Fall of the Deep", "/lib/Series Q/Deep Saga/11 - Fall of the Deep.m4b"),
		// Year-prefixed distinct books: contiguous numbers, different residuals.
		shBook("h", "1980 - The Shadow of the Tower", "/lib/newbooks/Wolfe Q/1980 - The Shadow of the Tower.m4b"),
		shBook("i", "1981 - The Claw of the Keeper", "/lib/newbooks/Wolfe Q/1981 - The Claw of the Keeper.m4b"),
		shBook("j", "1982 - The Sword of the Warden", "/lib/newbooks/Wolfe Q/1982 - The Sword of the Warden.m4b"),
		// Voice memo timestamps: six digits are not a chapter index.
		shBook("k", "185917", "/lib/Media/Voice Memos/20190223 185917.m4a"),
		shBook("l", "165847", "/lib/Media/Voice Memos/20190212 165847.m4a"),
		shBook("m", "165940", "/lib/Media/Voice Memos/20190212 165940.m4a"),
		// Bare years in one folder are not chapters either.
		shBook("n", "1987", "/lib/Years/1987.mp3"),
		shBook("o", "1988", "/lib/Years/1988.mp3"),
		shBook("p", "1989", "/lib/Years/1989.mp3"),
		// A dumping-ground folder of unrelated numbered books.
		shBook("q", "02 Ten Thousand Threads", "/lib/Unknown Author/Tales 02 - Ten Thousand Threads.m4b"),
		shBook("r", "03 The Other Tale", "/lib/Unknown Author/Tales 03 - The Other Tale.m4b"),
		shBook("s", "14", "/lib/Unknown Author/14 - Meeting With Rama - A Writer.mp3"),
		shBook("t", "17", "/lib/Unknown Author/17 - The Time Engine - B Writer.mp3"),
	}
	d := shDetect(books)
	if len(d.Groups) != 0 || len(d.Blocked) != 0 {
		t.Fatalf("legit numeric titles grouped: groups=%+v blocked=%+v", d.Groups, d.Blocked)
	}
}

// Two copies of one book interleaved in a folder repeat indices: reported as
// blocked, never merged into one double-length book.
func TestDetectChapterGroups_DuplicateIndicesBlock(t *testing.T) {
	// Durations corroborate the bare copy, so it joins the named run -- and
	// the repeated positions then block the whole folder.
	books := shEv(append(
		shRun("a", "/lib/A/Moon and Stone", 1, 5,
			func(i int) string { return fmt.Sprintf("%03d - Moon and Stone", i) },
			func(i int) string { return fmt.Sprintf("%03d - Moon and Stone.mp3", i) }),
		shRun("b", "/lib/A/Moon and Stone", 1, 5,
			func(i int) string { return fmt.Sprintf("%03d", i) },
			func(i int) string { return fmt.Sprintf("%03d.mp3", i) })...), 1500)
	d := shDetect(books)
	if len(d.Groups) != 0 || len(d.Blocked) != 1 {
		t.Fatalf("want one blocked group, got groups=%+v blocked=%+v", d.Groups, d.Blocked)
	}
	if !anyContains(d.Blocked[0].Blockers, "duplicate index") {
		t.Fatalf("blockers %v do not name the duplicate index", d.Blocked[0].Blockers)
	}
}

// Gaps are reported; a mostly-contiguous run still groups, a sparse one is
// blocked with the reason.
func TestDetectChapterGroups_GapsReportedAndSparseBlocked(t *testing.T) {
	var books []database.BookCore
	for i := 1; i <= 20; i++ {
		if i == 7 {
			continue
		}
		books = append(books, shBook(fmt.Sprintf("a%02d", i), fmt.Sprint(i), fmt.Sprintf("/lib/A/Tale/Tale - %02d.mp3", i)))
	}
	d := shDetect(books)
	if len(d.Groups) != 1 || !slices.Equal(d.Groups[0].Gaps, []string{"7"}) {
		t.Fatalf("want one group with gap [7], got %+v", d)
	}
	var sparse []database.BookCore
	for _, i := range []int{1, 2, 9, 15, 20} {
		sparse = append(sparse, shBook(fmt.Sprintf("s%02d", i), fmt.Sprint(i), fmt.Sprintf("/lib/B/Saga/Saga - %02d.mp3", i)))
	}
	d = shDetect(sparse)
	if len(d.Groups) != 0 || len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "sparse") {
		t.Fatalf("want sparse run blocked, got %+v", d)
	}
}

// "N of M" totals that disagree are two different sets.
func TestDetectChapterGroups_NofMTotalsMustAgree(t *testing.T) {
	books := []database.BookCore{
		shBook("a", "1 of 3", "/lib/A/Tale/a.mp3"),
		shBook("b", "2 of 3", "/lib/A/Tale/b.mp3"),
		shBook("c", "3 of 4", "/lib/A/Tale/c.mp3"),
	}
	d := shDetect(books)
	if len(d.Groups) != 0 || len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "totals") {
		t.Fatalf("want disagreeing totals blocked, got %+v", d)
	}
}

// Different known authors block the group; an unknown author does not.
func TestDetectChapterGroups_Authors(t *testing.T) {
	one, two := 1, 2
	mk := func(id, title string, a *int) database.BookCore {
		b := shBook(id, title, "/lib/A/Tale/Tale - "+title+".mp3")
		b.AuthorID = a
		return b
	}
	d := shDetect([]database.BookCore{mk("a", "01", &one), mk("b", "02", nil), mk("c", "03", &one)})
	if len(d.Groups) != 1 {
		t.Fatalf("unknown author must not block: %+v", d)
	}
	d = shDetect([]database.BookCore{mk("a", "01", &one), mk("b", "02", &two), mk("c", "03", &one)})
	if len(d.Groups) != 0 || len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "author") {
		t.Fatalf("different authors must block: %+v", d)
	}
}

// Non-primary versions are grouped as records but blocked, naming where
// their primaries live; a protected (iTunes) group is reported, blocked.
func TestDetectChapterGroups_VersionsAndProtectedAreBlocked(t *testing.T) {
	no := false
	var books []database.BookCore
	for i := 1; i <= 3; i++ {
		src := shBook(fmt.Sprintf("n%d", i), fmt.Sprintf("%03d_Tale", i), fmt.Sprintf("/lib/imported/A/01_Tale/%03d_Tale.mp3", i))
		src.IsPrimaryVersion = &no
		vg := fmt.Sprintf("vg%d", i)
		src.VersionGroupID = &vg
		prim := shBook(fmt.Sprintf("p%d", i), fmt.Sprintf("Tale %d", i), fmt.Sprintf("/lib/organized/A/Tale/Tale %d.mp3", i))
		prim.VersionGroupID = &vg
		books = append(books, src, prim)
	}
	d := shDetect(books)
	if len(d.Groups) != 0 || len(d.Blocked) != 1 {
		t.Fatalf("want the non-primary run blocked, got %+v", d)
	}
	if !anyContains(d.Blocked[0].Blockers, "non-primary") || !anyContains(d.Blocked[0].Blockers, "/lib/organized/A/Tale") {
		t.Fatalf("blockers %v must say non-primary and where the primaries live", d.Blocked[0].Blockers)
	}

	it := shRun("i", "/lib/itunes/Media/Author", 1, 3,
		func(i int) string { return fmt.Sprintf("1-%02d The Tale", i) },
		func(i int) string { return fmt.Sprintf("1-%02d The Tale.m4b", i) })
	d = DetectChapterGroupsWithOptions(it, ChapterDetectOptions{MinFiles: 2, Protected: func(b *database.BookCore) string {
		if strings.HasPrefix(b.FilePath, "/lib/itunes/") {
			return "iTunes library"
		}
		return ""
	}})
	if len(d.Groups) != 0 || len(d.Blocked) != 1 || !anyContains(d.Blocked[0].Blockers, "iTunes") {
		t.Fatalf("want the iTunes group reported blocked, got %+v", d)
	}
}

// A multi-file record (FilePath is a folder) is not a single-file chapter.
func TestDetectChapterGroups_MultiFileRecordsAreNotCandidates(t *testing.T) {
	books := []database.BookCore{
		shBook("a", "01", "/lib/A/Tale/Disc 01"),
		shBook("b", "02", "/lib/A/Tale/Disc 02"),
	}
	d := shDetect(books)
	if len(d.Groups) != 0 || len(d.Blocked) != 0 || d.SkippedNotSingleFile != 2 {
		t.Fatalf("folder-path records grouped: %+v", d)
	}
}

// Re-running detection over ONLY a group's members reproduces the group
// exactly (the merge job re-verifies a reviewed group that way), for every
// way a group forms: a file-keyed run, bare members joining the residual that
// names the folder, and chapter-named tracks regrouped by their book part.
func TestDetectChapterGroups_SubsetReproducesGroup(t *testing.T) {
	cases := map[string][]database.BookCore{
		"file-keyed bare titles": append(shRun("e", "/lib/A/Eldritch", 1, 30,
			func(i int) string { return fmt.Sprint(i) },
			func(i int) string { return fmt.Sprintf("Eldritch - %d.mp3", i) }),
			shBook("z", "Other Book", "/lib/A/Eldritch/Other Book.mp3")),
		"bare members join the folder-named residual": shEv(append(
			shRun("a", "/lib/A/Moon and Stone", 1, 5,
				func(i int) string { return fmt.Sprintf("%03d - Moon and Stone", i) },
				func(i int) string { return fmt.Sprintf("%03d - Moon and Stone.mp3", i) }),
			shRun("b", "/lib/A/Moon and Stone", 6, 8,
				func(i int) string { return fmt.Sprintf("%03d", i) },
				func(i int) string { return fmt.Sprintf("%03d.mp3", i) })...), 1500),
		"chapter-named tracks": shEv(shRun("c", "/lib/Author/Tunnels", 1, 5,
			func(i int) string { return fmt.Sprintf("%02d Chapter %d - Name %c", i, i, 'A'+rune(i)) },
			func(i int) string { return fmt.Sprintf("%02d Chapter %d - Name %c.m4b", i, i, 'A'+rune(i)) }), 1500),
	}
	for name, books := range cases {
		t.Run(name, func(t *testing.T) {
			full := shDetect(books)
			if len(full.Groups) != 1 {
				t.Fatalf("want 1 group, got %+v", full)
			}
			byID := map[string]database.BookCore{}
			for _, b := range books {
				byID[b.ID] = b
			}
			var sub []database.BookCore
			for _, id := range full.Groups[0].BookIDs {
				sub = append(sub, byID[id])
			}
			slices.Reverse(sub)
			again := shDetect(sub)
			if len(again.Groups) != 1 || !slices.Equal(again.Groups[0].BookIDs, full.Groups[0].BookIDs) ||
				again.Groups[0].CommonTitle != full.Groups[0].CommonTitle {
				t.Fatalf("subset re-detection differs: %+v vs %+v", again.Groups, full.Groups)
			}
		})
	}
}

// Detection over many folders is deterministic whatever order rows arrive in.
func TestDetectChapterGroups_DeterministicAcrossFolders(t *testing.T) {
	var books []database.BookCore
	for f := 0; f < 50; f++ {
		dir := fmt.Sprintf("/lib/A/Book%02d", f)
		books = append(books, shRun(fmt.Sprintf("f%02d-", f), dir, 1, 5,
			func(i int) string { return fmt.Sprint(i) },
			func(i int) string { return fmt.Sprintf("Book%02d - %d.mp3", f, i) })...)
	}
	a := shDetect(books)
	slices.Reverse(books)
	b := shDetect(books)
	if len(a.Groups) != 50 || len(b.Groups) != 50 {
		t.Fatalf("want 50 groups, got %d / %d", len(a.Groups), len(b.Groups))
	}
	for i := range a.Groups {
		if !slices.Equal(a.Groups[i].BookIDs, b.Groups[i].BookIDs) {
			t.Fatalf("group %d differs by input order", i)
		}
	}
}

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
