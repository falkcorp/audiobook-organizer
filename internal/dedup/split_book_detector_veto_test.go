// file: internal/dedup/split_book_detector_veto_test.go
// version: 1.0.2
// guid: 5d3a8e21-7c94-4b60-a1f2-9e0c4b7d6a35
// last-edited: 2026-10-21

package dedup

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Every name below is synthetic: the repo is public, so no real library titles.

// testChapterMaxSec is repair_chapter_max_min's default (120 min) in seconds.
const testChapterMaxSec = 2 * 3600

func secs(n int) *int { return &n }

// detectKept is what the fail-closed DetectSplitBookCandidates returns for
// slim books: the clusters with no veto.
func detectKept(books []splitBookSlim, resolve func(int) string, chapterMaxSec int) []SplitBookCandidate {
	return withoutVetoed(detectFromSlimWithVetoes(books, resolve, chapterMaxSec))
}

// vetoCodes is the rule codes ("V2", "V9", ...) a cluster carries, sorted,
// each once.
func vetoCodes(c SplitBookCandidate) []string {
	seen := map[string]bool{}
	for _, v := range c.Vetoes {
		seen[strings.Fields(v)[0]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// vbook is a slim book under author/series (shared by the whole cluster).
func vbook(id, title, p string, author, series, dur *int) splitBookSlim {
	return splitBookSlim{ID: id, Title: title, FilePath: p, AuthorID: author, SeriesID: series, Duration: dur}
}

// one runs the detector over a single cluster and returns the lone cluster
// (vetoed or not) it found.
func one(t *testing.T, books []splitBookSlim) SplitBookCandidate {
	t.Helper()
	got := detectFromSlimWithVetoes(books, nil, testChapterMaxSec)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 cluster (vetoed or not), got %d: %+v", len(got), got)
	}
	return got[0]
}

func wantVetoed(t *testing.T, books []splitBookSlim, codes ...string) SplitBookCandidate {
	t.Helper()
	c := one(t, books)
	if got := vetoCodes(c); !slices.Equal(got, codes) {
		t.Fatalf("veto codes: want %v, got %v (%q)", codes, got, c.Vetoes)
	}
	if kept := detectKept(books, nil, testChapterMaxSec); len(kept) != 0 {
		t.Fatalf("a vetoed cluster must not come back from the fail-closed detector: %+v", kept)
	}
	return c
}

func wantQualifies(t *testing.T, books []splitBookSlim) SplitBookCandidate {
	t.Helper()
	c := one(t, books)
	if len(c.Vetoes) != 0 {
		t.Fatalf("want a clean candidate, got vetoes %q", c.Vetoes)
	}
	if kept := detectKept(books, nil, testChapterMaxSec); len(kept) != 1 {
		t.Fatalf("a clean cluster must come back from the fail-closed detector, got %d", len(kept))
	}
	return c
}

// A series folder of separate books: one subfolder per volume, same author and
// series, distinct subtitles, 6-9 hour books. Passed every old check.
func TestVeto_SeriesFolderOfSeparateBooks(t *testing.T) {
	author, series := new(11), new(21)
	var books []splitBookSlim
	for i, name := range []string{"Bold", "Brave", "Quiet", "Open"} {
		n := i + 1
		books = append(books, vbook(
			fmt.Sprintf("01HM%08d", n),
			fmt.Sprintf("%s Quill: A Tale (The Quill Saga, Book %d)", name, n),
			fmt.Sprintf("/lib/Author Q/Quill Saga Books 1-4/Quill Saga %02d - %s Quill/%s Quill.m4b", n, name, name),
			author, series, secs((5+i)*3600)))
		books[len(books)-1].SeriesSequence = secs(n)
	}
	c := wantVetoed(t, books, "V2", "V3", "V4", "V9")
	if c.Shape != "grandparent" {
		t.Errorf("shape: want grandparent, got %q", c.Shape)
	}
	if len(c.Evidence) == 0 || !strings.Contains(c.Evidence[0], "series_sequence") {
		t.Errorf("distinct series_sequence values are shown as evidence: %q", c.Evidence)
	}
}

func TestVeto_StarfallVolSubfolders(t *testing.T) {
	var books []splitBookSlim
	for i := 1; i <= 4; i++ {
		books = append(books, vbook(fmt.Sprintf("01SF%08d", i), "Starfall",
			fmt.Sprintf("/lib/Author R/Starfall/Starfall, Vol. %02d/Starfall.m4b", i), new(5), nil, secs(1800)))
	}
	wantVetoed(t, books, "V2")
}

func TestVeto_KestrelTalesNumberedSubtitles(t *testing.T) {
	subs := []string{"Dawn Patrol", "Night Watch", "The Long Road", "Ember Court"}
	var books []splitBookSlim
	for i, st := range subs {
		books = append(books, vbook(fmt.Sprintf("01KT%08d", i+1), fmt.Sprintf("Kestrel Tales %d: %s", i+1, st),
			fmt.Sprintf("/lib/Author S/Kestrel Tales/%02d.m4b", i+1), new(6), nil, secs(1800)))
	}
	wantVetoed(t, books, "V2b")
}

func TestVeto_YearsRun(t *testing.T) {
	var books []splitBookSlim
	for y := 2010; y <= 2012; y++ {
		books = append(books, vbook(fmt.Sprintf("01BS%08d", y), "Yearly Digest",
			fmt.Sprintf("/lib/Author T/Yearly Digest/Yearly Digest %d.m4b", y), new(7), nil, secs(1800)))
	}
	wantVetoed(t, books, "V5")
	if _, _, years := sequentialRun([]int{2010, 2011, 2012}); !years {
		t.Error("sequentialRun must flag a run of years")
	}
}

func TestVeto_CopySuffixes(t *testing.T) {
	var books []splitBookSlim
	for i := 1; i <= 5; i++ {
		books = append(books, vbook(fmt.Sprintf("01CP%08d", i), "Some Title",
			fmt.Sprintf("/lib/Author U/Some Title/Some Title_copy%d.m4b", i), new(8), nil, secs(120)))
	}
	wantVetoed(t, books, "V8")
}

// Each rule alone vetoes.
func TestVeto_EachRuleAlone(t *testing.T) {
	t.Run("V3 a single long member", func(t *testing.T) {
		var books []splitBookSlim
		for i := 1; i <= 4; i++ {
			d := 300
			if i == 3 {
				d = testChapterMaxSec // at the limit: >= vetoes
			}
			books = append(books, vbook(fmt.Sprintf("v3-%d", i), "Ashfield",
				fmt.Sprintf("/lib/A/Ashfield/%02d.m4b", i), new(1), nil, secs(d)))
		}
		wantVetoed(t, books, "V3")
	})
	t.Run("V3 nil and zero durations are unknown, not a veto", func(t *testing.T) {
		var books []splitBookSlim
		for i := 1; i <= 4; i++ {
			var d *int
			if i%2 == 0 {
				d = secs(0)
			}
			books = append(books, vbook(fmt.Sprintf("v3n-%d", i), "Ashfield", fmt.Sprintf("/lib/A/Ashfield/%02d.m4b", i), new(1), nil, d))
		}
		wantQualifies(t, books)
	})
	t.Run("V4 folder names several works", func(t *testing.T) {
		for _, folder := range []string{"Embergate Books 1-4", "Embergate Books 1 thru 6", "Embergate Books 2 & 3", "Embergate Series", "Embergate Collection", "Embergate Omnibus", "Embergate Box Set", "Embergate Boxed Set"} {
			var books []splitBookSlim
			for i := 1; i <= 3; i++ {
				books = append(books, vbook(fmt.Sprintf("v4-%d", i), "Embergate", fmt.Sprintf("/lib/A/%s/%02d.m4b", folder, i), new(1), nil, secs(300)))
			}
			wantVetoed(t, books, "V4")
		}
	})
	t.Run("V2 Book/Vol numbers in file names", func(t *testing.T) {
		var books []splitBookSlim
		for i := 1; i <= 3; i++ {
			books = append(books, vbook(fmt.Sprintf("v2-%d", i), "Driftwood", fmt.Sprintf("/lib/A/Driftwood/Driftwood Bk %d.m4b", i), new(1), nil, secs(300)))
		}
		wantVetoed(t, books, "V2")
	})
	t.Run("V2 hash markers in titles", func(t *testing.T) {
		var books []splitBookSlim
		for i := 1; i <= 3; i++ {
			books = append(books, vbook(fmt.Sprintf("v2h-%d", i), fmt.Sprintf("Driftwood #%d", i), fmt.Sprintf("/lib/A/Driftwood/%02d.m4b", i), new(1), nil, secs(300)))
		}
		wantVetoed(t, books, "V2")
	})
	t.Run("V9 grandparent subfolders name different things", func(t *testing.T) {
		var books []splitBookSlim
		for i, n := range []string{"Salt", "Tide", "Ledger"} {
			books = append(books, vbook(fmt.Sprintf("v9-%d", i), "Harbor Songs", fmt.Sprintf("/lib/A/Harbor Songs/%02d %s/x.m4b", i+1, n), new(1), nil, secs(300)))
		}
		wantVetoed(t, books, "V9")
	})
}

// V7: two or more distinct ASINs among the members are separate catalogued
// products.
func TestVeto_DistinctASINs(t *testing.T) {
	mkBooks := func(asins ...string) []splitBookSlim {
		var books []splitBookSlim
		for i, a := range asins {
			b := vbook(fmt.Sprintf("asin-%d", i), "Reedmarsh", fmt.Sprintf("/lib/A/Reedmarsh/%02d.m4b", i+1), new(1), nil, secs(300))
			b.ASIN = a
			books = append(books, b)
		}
		return books
	}
	t.Run("two distinct ASINs veto", func(t *testing.T) {
		wantVetoed(t, mkBooks("B000AAAAA1", "B000AAAAA2", "B000AAAAA1"), "V7")
	})
	t.Run("case and padding do not make a distinct ASIN", func(t *testing.T) {
		// slimFromBook normalises; the detector input is already normalised.
		a := "  b000aaaaa1 "
		core := &database.BookCore{ID: "x", Title: "T", FilePath: "/lib/A/T/01.m4b", ASIN: &a}
		if got := slimFromBook(core).ASIN; got != "B000AAAAA1" {
			t.Errorf("slimFromBook ASIN = %q", got)
		}
	})
	t.Run("one ASIN, or ASINs on only some members, qualifies", func(t *testing.T) {
		wantQualifies(t, mkBooks("B000AAAAA1", "B000AAAAA1", "B000AAAAA1"))
		wantQualifies(t, mkBooks("B000AAAAA1", "", ""))
		wantQualifies(t, mkBooks("", "", ""))
	})
}

// "Episode" and "Section" are not chapter words: a run of "Stem Episode N:
// Subtitle" titles is separate installments (V2b), and a Book number written
// as a Roman numeral or a word is a volume (V2).
func TestVeto_EpisodesAndNumberedBooks(t *testing.T) {
	titled := func(titles ...string) []splitBookSlim {
		var books []splitBookSlim
		for i, ti := range titles {
			books = append(books, vbook(fmt.Sprintf("ep-%d", i), ti, fmt.Sprintf("/lib/A/Saga/%02d.m4b", i+1), new(1), nil, secs(300)))
		}
		return books
	}
	t.Run("Episode N: Subtitle", func(t *testing.T) {
		wantVetoed(t, titled("Saga Episode 1: The Pilot", "Saga Episode 2: The Chase", "Saga Episode 3: The End"), "V2b")
	})
	t.Run("Section N: Subtitle", func(t *testing.T) {
		wantVetoed(t, titled("Saga Section 1: Dawn", "Saga Section 2: Dusk", "Saga Section 3: Night"), "V2b")
	})
	t.Run("Book I/II/III", func(t *testing.T) {
		wantVetoed(t, titled("Saga Book I", "Saga Book II", "Saga Book III"), "V2")
	})
	t.Run("Book One/Two/Three", func(t *testing.T) {
		wantVetoed(t, titled("Saga Book One", "Saga Book Two", "Saga Book Three"), "V2")
	})
	t.Run("Volume IV and Vol. twelve in file names", func(t *testing.T) {
		var books []splitBookSlim
		for i, n := range []string{"Saga Volume IV", "Saga Vol. twelve", "Saga Vol. XX"} {
			books = append(books, vbook(fmt.Sprintf("vf-%d", i), "Saga", fmt.Sprintf("/lib/A/Saga/%02d %s.m4b", i+1, n), new(1), nil, secs(300)))
		}
		wantVetoed(t, books, "V2")
	})
	t.Run("a genuine Part 1/2/3 split of one book still qualifies", func(t *testing.T) {
		wantQualifies(t, titled("Saga Part 1: Arrival", "Saga Part 2: Voyage", "Saga Part 3: Landfall"))
	})
	t.Run("one Roman or word Book marker across the cluster is one work", func(t *testing.T) {
		wantQualifies(t, titled("Saga Book Two - 01", "Saga Book Two - 02", "Saga Book Two - 03"))
		wantQualifies(t, titled("Saga Book II - 01", "Saga Book II - 02", "Saga Book II - 03"))
	})
	t.Run("volNumbers", func(t *testing.T) {
		got := volNumbers([]string{"Book XIV", "bk Nine", "Vol. 3", "Book i", "Book Ten Thousand", "Books Two"})
		if !slices.Equal(got, []int{3, 9, 10, 14}) {
			t.Errorf("volNumbers = %v", got)
		}
	})
}

// A chapter heading is not a series volume: "Chapter N: Name" and "Track #N"
// titles are not V2b / V2.
func TestVeto_ChapterHeadingsAreNotVolumes(t *testing.T) {
	var books []splitBookSlim
	for i, st := range []string{"The Letter", "The Door", "The Stair"} {
		books = append(books, vbook(fmt.Sprintf("ch-%d", i), fmt.Sprintf("Chapter %d: %s", i+1, st),
			fmt.Sprintf("/lib/A/Quietus/%02d.m4b", i+1), new(1), nil, secs(300)))
	}
	wantQualifies(t, books)
	books = nil
	for i := 1; i <= 3; i++ {
		books = append(books, vbook(fmt.Sprintf("tr-%d", i), fmt.Sprintf("Track #%d", i),
			fmt.Sprintf("/lib/A/Quietus/%02d.m4b", i), new(1), nil, secs(300)))
	}
	wantQualifies(t, books)
}

// V9 order: Part and (N of M) markers come off BEFORE the digits.
func TestSubdirStem(t *testing.T) {
	for in, want := range map[string]string{
		"Moonwatch - Part 01":     "moonwatch",
		"Moonwatch - Part 2 of 5": "moonwatch",
		"Moonwatch (3 of 5)":      "moonwatch",
		"Quill Saga 02 - Brave":   "quill saga - brave",
		"4":                       "",
		"Disc 2":                  "",
	} {
		if got := subdirStem(in); got != want {
			t.Errorf("subdirStem(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- clusters that must still qualify -----------------------------------

func TestQualifies_PartSubfolders(t *testing.T) {
	var books []splitBookSlim
	for i := 1; i <= 5; i++ {
		books = append(books, vbook(fmt.Sprintf("pt-%d", i), fmt.Sprintf("Moonwatch - Part %02d", i),
			fmt.Sprintf("/lib/Author R/Moonwatch/Moonwatch - Part %02d/Moonwatch.m4b", i), new(9), nil, secs(3000)))
	}
	c := wantQualifies(t, books)
	if c.Shape != "grandparent" || c.SuggestedTitle != "Moonwatch" {
		t.Errorf("shape %q title %q", c.Shape, c.SuggestedTitle)
	}
	if !strings.HasPrefix(c.SequentialPattern, "subdir:1..5") {
		t.Errorf("the numbers come from the subfolders: %q", c.SequentialPattern)
	}
}

func TestQualifies_NofM(t *testing.T) {
	var books []splitBookSlim
	for i := 1; i <= 3; i++ {
		books = append(books, vbook(fmt.Sprintf("nm-%d", i), fmt.Sprintf("Lanternfall (%d of 3)", i),
			fmt.Sprintf("/lib/Author R/Lanternfall/Lanternfall (%d of 3).m4b", i), new(9), nil, secs(3000)))
	}
	c := wantQualifies(t, books)
	if c.SuggestedTitle != "Lanternfall" {
		t.Errorf("title %q", c.SuggestedTitle)
	}
}

// Flat chapter files, each titled with its own chapter name: no title repeats,
// so the folder names the work.
func TestQualifies_DistinctChapterNamesUseFolderTitle(t *testing.T) {
	var books []splitBookSlim
	for i, name := range []string{"03_Intro", "04_Preface", "05_Chapter 01"} {
		books = append(books, vbook(fmt.Sprintf("dn-%d", i), name, fmt.Sprintf("/lib/Author V/Ironbound/%s.mp3", name), new(3), nil, secs(600)))
	}
	c := wantQualifies(t, books)
	if c.SuggestedTitle != "Ironbound" {
		t.Errorf("suggested title: want the folder name, got %q", c.SuggestedTitle)
	}
	if !strings.HasPrefix(c.SequentialPattern, "filename:3..5") {
		t.Errorf("pattern %q", c.SequentialPattern)
	}
}

func TestQualifies_BookNumberInTitleIsKept(t *testing.T) {
	for _, tf := range []func(i int) string{
		func(i int) string { return "Galaxy Run Book 3" },
		func(i int) string { return fmt.Sprintf("Galaxy Run Book 3 - %02d", i) },
	} {
		var books []splitBookSlim
		for i := 1; i <= 4; i++ {
			books = append(books, vbook(fmt.Sprintf("gr-%d", i), tf(i), fmt.Sprintf("/lib/Author W/Galaxy Run Book 3/%02d.m4b", i), new(4), nil, secs(900)))
		}
		if c := wantQualifies(t, books); c.SuggestedTitle != "Galaxy Run Book 3" {
			t.Errorf("suggested title %q, want it to keep its Book 3", c.SuggestedTitle)
		}
	}
}

// The number source is the real one, not the shape's guess.
func TestSequentialPatternNamesTheRealNumberSource(t *testing.T) {
	var flat, grand, mixed []splitBookSlim
	for i := 1; i <= 3; i++ {
		flat = append(flat, vbook(fmt.Sprintf("f%d", i), "Tidewater", fmt.Sprintf("/lib/A/Tidewater/%02d.mp3", i), new(1), nil, nil))
		grand = append(grand, vbook(fmt.Sprintf("g%d", i), "Tidewater", fmt.Sprintf("/lib/A/Tidewater/%d/x.mp3", i), new(1), nil, nil))
	}
	// Files named by chapter number, except one that has none: its subfolder
	// supplies the number.
	mixed = append(mixed,
		vbook("m1", "Tidewater", "/lib/A/Tidewater/1/01.mp3", new(1), nil, nil),
		vbook("m2", "Tidewater", "/lib/A/Tidewater/2/02.mp3", new(1), nil, nil),
		vbook("m3", "Tidewater", "/lib/A/Tidewater/3/final.mp3", new(1), nil, nil))
	for name, tc := range map[string]struct {
		books  []splitBookSlim
		prefix string
	}{"flat": {flat, "filename:"}, "grandparent": {grand, "subdir:"}, "mixed": {mixed, "filename+subdir:"}} {
		if c := wantQualifies(t, tc.books); !strings.HasPrefix(c.SequentialPattern, tc.prefix) {
			t.Errorf("%s: pattern %q, want prefix %q", name, c.SequentialPattern, tc.prefix)
		}
	}
	// A grandparent-shaped cluster whose files carry the numbers is "filename".
	named := []splitBookSlim{
		vbook("n1", "Tidewater", "/lib/A/Tidewater/a/01.mp3", new(1), nil, nil),
		vbook("n2", "Tidewater", "/lib/A/Tidewater/b/02.mp3", new(1), nil, nil),
		vbook("n3", "Tidewater", "/lib/A/Tidewater/c/03.mp3", new(1), nil, nil),
	}
	if c := one(t, named); c.Shape != "grandparent" || !strings.HasPrefix(c.SequentialPattern, "filename:") {
		t.Errorf("shape %q pattern %q: the numbers came from the filenames", c.Shape, c.SequentialPattern)
	}
}

// A vetoed cluster claims its books: no later pass may re-emit them under
// another shape.
func TestVetoedClusterClaimsItsBooks(t *testing.T) {
	var books []splitBookSlim
	for i := 1; i <= 4; i++ {
		books = append(books, vbook(fmt.Sprintf("01CL%08d", i), "Pale Fire", fmt.Sprintf("/lib/A/Pale Fire Series/%02d - Pale Fire.m4b", i), new(2), nil, secs(300)))
		books[len(books)-1].OriginalFilename = fmt.Sprintf("%02d - Pale Fire.m4b", i)
	}
	got := detectFromSlimWithVetoes(books, nil, testChapterMaxSec)
	if len(got) != 1 || len(got[0].Vetoes) == 0 {
		t.Fatalf("want one vetoed cluster and nothing else, got %+v", got)
	}
	if kept := detectKept(books, nil, testChapterMaxSec); len(kept) != 0 {
		t.Fatalf("fail closed: %+v", kept)
	}
}

// ---- parity with the investigation's classifier -------------------------

// TestVetoParityWithClassifier mirrors, with synthetic names, each category
// the investigation's classify.py found on prod's 64 applicable rows (veto
// codes per category, and the categories that stay applicable). The detector
// owns the rules V2, V2b, V3, V4, V5, V8, V9; V6 (zero-file members) is the
// fixer's skipped_no_files and is covered in the maintenance package. V3 is
// deliberately stricter than the script: ANY member at or over the limit
// vetoes (Option A, the fragment fixer's gate), with no shared-Part exemption.
func TestVetoParityWithClassifier(t *testing.T) {
	type cluster struct {
		name   string
		folder string
		titles []string // one per member
		paths  []string // member file paths, parallel to titles
		durs   []int    // seconds; <0 = unknown
		want   []string // veto codes; nil = qualifies
	}
	mk := func(name, folder string, n int, title func(i int) string, file func(i int) string, dur func(i int) int, want ...string) cluster {
		c := cluster{name: name, folder: folder, want: want}
		for i := 1; i <= n; i++ {
			c.titles = append(c.titles, title(i))
			c.paths = append(c.paths, path.Join("/lib", name, folder, file(i)))
			c.durs = append(c.durs, dur(i))
		}
		return c
	}
	same := func(s string) func(int) string { return func(int) string { return s } }
	hours := func(h int) func(int) int { return func(int) int { return h * 3600 } }
	short := func(m int) func(int) int { return func(int) int { return m * 60 } }
	unknown := func(int) int { return -1 }
	num := func(f string) func(int) string { return func(i int) string { return fmt.Sprintf(f, i) } }
	grandNum := func(f string) func(int) string {
		return func(i int) string { return fmt.Sprintf(f, i) + "/book.m4b" }
	}
	// named is a subfolder whose NAME differs per member (a different work),
	// numbered "01 Ash", "02 Birch" ...; digits aside the names differ.
	words := []string{"", "Ash", "Birch", "Cedar", "Dune", "Elm", "Fir", "Gum"}
	named := func(prefix string) func(int) string {
		return func(i int) string { return fmt.Sprintf("%s %02d %s/book.m4b", prefix, i, words[i]) }
	}

	cases := []cluster{
		// VETOED categories.
		mk("c01", "Quill Saga Books 1-4", 4, func(i int) string {
			return fmt.Sprintf("%s Quill: A Tale (Quill Saga, Book %d)", words[i], i)
		},
			named("Quill Saga"), hours(7), "V2", "V3", "V4", "V9"),
		mk("c02", "Zephyr Example", 5, num("Zephyr Example %d: Sub%[1]d"), grandNum("Volume %d"), hours(11), "V2", "V2b", "V3"),
		mk("c03", "Quill Weaver - SERIES", 6, same("Quill Weaver"), num("Quill Weaver Book %02d.m4b"), hours(13), "V2", "V3", "V4"),
		mk("c04", "Plover and Wren [02]", 4, same("Plover and Wren"), num("%02d.m4b"), hours(4), "V3"),
		mk("c05", "Nimbus Cove series Books 1 thru 5", 5, same("Nimbus Cove"), named("Nimbus Cove"), hours(3), "V3", "V4", "V9"),
		mk("c06", "Marble Orchard", 4, num("Marble Orchard %d: Part Name %[1]d"), num("%02d.m4b"), hours(7), "V2b", "V3"),
		mk("c07", "Quartz Harvester", 4, same("Quartz Harvester"), num("%02d.m4b"), hours(14), "V3"),
		mk("c08", "Top Picks", 4, same("Top Picks"), func(i int) string { return fmt.Sprintf("Top Picks %d.m4b", 2009+i) }, hours(5), "V3", "V5"),
		mk("c09", "Otter Annals", 4, same("Otter Annals"), named("Otter"), short(2), "V9"),
		mk("c10", "Thistle Copies", 6, same("Thistle Copies"), num("Thistle Copies_copy%d.m4b"), short(4), "V8"),
		mk("c11", "Heron Records", 5, same("Heron Records"), num("Heron Records - Book %d.m4b"), hours(3), "V2", "V3"),
		mk("c12", "Cobalt Drift Lite", 4, same("Cobalt Drift Lite"), grandNum("Cobalt Drift Lite - Volume %d"), hours(10), "V2", "V3"),
		// STILL APPLICABLE categories.
		mk("a01", "Paper Lantern", 7, same("Paper Lantern"), num("%02d.m4b"), short(58)),
		mk("a02", "Amber Reach [01]", 4, same("Amber Reach"), num("%02d.m4b"), short(80)),
		mk("a03", "Willow Gate", 137, same("Willow Gate"), num("%03d.mp3"), unknown),
		mk("a04", "Birchwood Lane", 13, same("Birchwood Lane"), num("Birchwood Lane %02d.mp3"), short(71)),
		mk("a05", "Part Subfolders", 4, num("Part Subfolders - Part %d"), grandNum("Part Subfolders - Part %02d"), short(50)),
		mk("a06", "Galaxy Run Book 3", 5, same("Galaxy Run Book 3"), num("%02d.mp3"), short(40)),
	}

	var all []splitBookSlim
	for ci, c := range cases {
		for i := range c.titles {
			var d *int
			if c.durs[i] >= 0 {
				d = secs(c.durs[i])
			}
			all = append(all, vbook(fmt.Sprintf("01P%02d%05d", ci, i), c.titles[i], c.paths[i], new(100+ci), nil, d))
		}
	}
	got := detectFromSlimWithVetoes(all, nil, testChapterMaxSec)
	byFolder := map[string]SplitBookCandidate{}
	for _, cand := range got {
		byFolder[path.Base(cand.ParentFolder)] = cand
	}
	for _, c := range cases {
		cand, ok := byFolder[c.folder]
		if !ok {
			t.Errorf("%s: no cluster found for %q", c.name, c.folder)
			continue
		}
		if codes := vetoCodes(cand); !slices.Equal(codes, c.want) && !(len(codes) == 0 && len(c.want) == 0) {
			t.Errorf("%s: veto codes want %v got %v (%q)", c.name, c.want, vetoCodes(cand), cand.Vetoes)
		}
	}
	if len(byFolder) != len(cases) {
		t.Errorf("want %d clusters, got %d", len(cases), len(byFolder))
	}
	wantKept := 0
	for _, c := range cases {
		if len(c.want) == 0 {
			wantKept++
		}
	}
	if kept := detectKept(all, nil, testChapterMaxSec); len(kept) != wantKept {
		t.Errorf("fail-closed detector keeps %d clusters, want %d", len(kept), wantKept)
	}
}
