// file: internal/itunes/service/fs_regroup_shape_test.go
// version: 1.1.0
// guid: 4d2a7f81-6c93-4e50-b8a1-0f5e2c9d3b76
// last-edited: 2026-07-13

package itunesservice

import (
	"fmt"
	"testing"
)

const shatterRoot = "/mnt/bigdata/books/audiobook-organizer"

// sb builds a primary single-file ShatterBook at a path.
func sb(id, path string) ShatterBook {
	return ShatterBook{BookID: id, FilePath: path, FileCount: 1, IsPrimary: true}
}

// sbT is like sb but also sets the album Title tag — used to prove a parent-series
// name carried on the tag ("The Traitor Spy Trilogy") does NOT leak into the anthology
// classifier (Bug 1); the marker is matched only against the book-folder name.
func sbT(id, path, title string) ShatterBook {
	b := sb(id, path)
	b.Title = title
	return b
}

// sbIT is like sb but also sets the original iTunes album-folder path (Bug 2).
func sbIT(id, path, itunesPath string) ShatterBook {
	b := sb(id, path)
	b.ITunesPath = itunesPath
	return b
}

// findGroup returns the group whose FolderRef ends with the given suffix.
func findGroup(t *testing.T, groups []RegroupGroup, folderSuffix string) RegroupGroup {
	t.Helper()
	for _, g := range groups {
		if len(g.FolderRef) >= len(folderSuffix) && g.FolderRef[len(g.FolderRef)-len(folderSuffix):] == folderSuffix {
			return g
		}
	}
	t.Fatalf("no group with folder ending %q in %+v", folderSuffix, groups)
	return RegroupGroup{}
}

// Classic chapter shatter: `<Book>/<Book> - N/file` → confident multidisc collapse.
func TestClassify_ChapterShatter_Multidisc(t *testing.T) {
	base := shatterRoot + "/Adrian Tchaikovsky/Cage of Souls"
	var books []ShatterBook
	for i := 1; i <= 12; i++ {
		books = append(books, sb(fmt.Sprintf("c%02d", i),
			fmt.Sprintf("%s/Cage of Souls - %d/01.mp3", base, i)))
	}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d (stats %+v)", len(groups), st)
	}
	g := groups[0]
	if g.Kind != KindMultidisc || !g.Confident {
		t.Errorf("kind=%q confident=%v, want multidisc/true", g.Kind, g.Confident)
	}
	if g.FolderRef != base {
		t.Errorf("FolderRef=%q, want %q", g.FolderRef, base)
	}
	if len(g.Members) != 12 {
		t.Errorf("members=%d, want 12", len(g.Members))
	}
	if g.SurvivorTitle != "Cage of Souls" {
		t.Errorf("survivorTitle=%q, want 'Cage of Souls'", g.SurvivorTitle)
	}
	// Members ordered by chapter number.
	for i, m := range g.Members {
		want := fmt.Sprintf("c%02d", i+1)
		if m.BookID != want {
			t.Errorf("member %d = %q, want %q (ordering)", i, m.BookID, want)
		}
	}
}

// Flat multi-track: many numbered tracks directly in one book folder → confident multidisc.
func TestClassify_FlatMultitrack_Multidisc(t *testing.T) {
	base := shatterRoot + "/Neil Gaiman/Coraline"
	var books []ShatterBook
	for i := 1; i <= 8; i++ {
		books = append(books, sb(fmt.Sprintf("f%02d", i),
			fmt.Sprintf("%s/%02d - Chapter.mp3", base, i)))
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Coraline")
	if g.Kind != KindMultidisc || !g.Confident {
		t.Errorf("kind=%q confident=%v, want multidisc/true", g.Kind, g.Confident)
	}
	if len(g.Members) != 8 {
		t.Errorf("members=%d, want 8", len(g.Members))
	}
}

// Disc subfolders: `<Book>/Disc N/file` → confident multidisc.
func TestClassify_DiscSubfolders_Multidisc(t *testing.T) {
	base := shatterRoot + "/Patrick Rothfuss/The Name of the Wind"
	books := []ShatterBook{
		sb("d1", base+"/Disc 1/track.mp3"),
		sb("d2", base+"/Disc 2/track.mp3"),
		sb("d3", base+"/Disc 3/track.mp3"),
		sb("d4", base+"/CD 4/track.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "The Name of the Wind")
	if g.Kind != KindMultidisc || !g.Confident {
		t.Errorf("kind=%q confident=%v, want multidisc/true", g.Kind, g.Confident)
	}
}

// Abridged + Unabridged editions in one folder → version-group hold (not confident).
func TestClassify_VersionGroup(t *testing.T) {
	base := shatterRoot + "/Frank Herbert/Dune"
	books := []ShatterBook{
		sb("v1", base+"/Dune (Unabridged)/01.mp3"),
		sb("v2", base+"/Dune (Abridged)/01.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Dune")
	if g.Kind != KindVersionGroup {
		t.Fatalf("kind=%q, want version-group", g.Kind)
	}
	if g.Confident {
		t.Errorf("version-group must not be confident (needs human primary pick)")
	}
}

// GENUINE anthology: an anthology marker ON THE BOOK FOLDER + several genuinely
// distinct, non-sequential story titles → anthology hold, count = DISTINCT WORKS
// (not the raw file count). One story repeats a track to prove the count is distinct
// titles, not files.
func TestClassify_GenuineAnthology(t *testing.T) {
	base := shatterRoot + "/George R R Martin/Dangerous Women Anthology"
	books := []ShatterBook{
		sb("g1", base+"/The Princess and the Queen.mp3"),
		sb("g2", base+"/Some Desperate Glory.mp3"),
		sb("g3", base+"/Bombshells.mp3"),
		sb("g4", base+"/Raisa Stepanova.mp3"),
		sb("g5", base+"/Nora's Song.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Dangerous Women Anthology")
	if g.Kind != KindAnthology {
		t.Fatalf("kind=%q, want anthology", g.Kind)
	}
	if g.Confident {
		t.Errorf("anthology must not be confident")
	}
	if g.DistinctWorks != 5 {
		t.Errorf("DistinctWorks=%d, want 5 distinct titles (not file count)", g.DistinctWorks)
	}
}

// PROD FALSE POSITIVE (Bug 1): one novel (book 1 of a trilogy) split into 133
// sequential chapter files, with the album Title tag carrying the PARENT series name
// "The Traitor Spy Trilogy". The old code mis-counted the 133 chapter FILES as 133
// distinct WORKS because the anthology marker leaked in through the album tag. The
// book-folder name ("01-The Ambassadors Mission") has no marker → must be multidisc.
func TestClassify_133SequentialChapters_NotAnthology(t *testing.T) {
	base := shatterRoot + "/Trudi Canavan/The Traitor Spy Trilogy/01-The Ambassadors Mission"
	var books []ShatterBook
	for i := 1; i <= 133; i++ {
		books = append(books, sbT(
			fmt.Sprintf("t%03d", i),
			fmt.Sprintf("%s/Chapter %03d.mp3", base, i),
			"The Traitor Spy Trilogy")) // parent-series name on the album tag — must not leak
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "01-The Ambassadors Mission")
	if g.Kind != KindMultidisc {
		t.Fatalf("kind=%q, want multidisc (133 sequential chapters of one novel)", g.Kind)
	}
	if g.Kind == KindAnthology {
		t.Errorf("must NOT be anthology — 133 chapter files are 1 work")
	}
	if !g.Confident {
		t.Errorf("a clean flat sequential chapter run should be a confident collapse")
	}
}

// Parent "…Trilogy/" folder marker + a child single-book folder → the child is
// classified on ITS OWN merits, never anthology just because a parent dir says Trilogy
// (Bug 1). Here the child is a small, non-sequential, ambiguous folder → ambiguous.
func TestClassify_ParentTrilogyMarker_ChildOnMerits(t *testing.T) {
	base := shatterRoot + "/Author/The Broken Empire Trilogy/Prince of Thorns"
	books := []ShatterBook{
		sb("p1", base+"/Prince of Thorns.mp3"),
		sb("p2", base+"/Prince of Thorns (bonus).mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Prince of Thorns")
	if g.Kind == KindAnthology {
		t.Fatalf("parent 'Trilogy' marker must not make the child an anthology; got %+v", g)
	}
	if g.Kind != KindAmbiguous {
		t.Errorf("kind=%q, want ambiguous (child judged on its own merits)", g.Kind)
	}
}

// A trilogy-marked folder with SEQUENTIAL, single-stem sub-parts is ambiguous — it
// could be 3 chapters OR 3 volumes; we refuse a confident-but-wrong anthology split
// AND refuse a confident multidisc collapse (maintainer rule: prefer ambiguous).
func TestClassify_TrilogyMarkerSequential_Ambiguous(t *testing.T) {
	base := shatterRoot + "/Isaac Asimov/Foundation Trilogy"
	books := []ShatterBook{
		sb("a1", base+"/Foundation Trilogy - 1/01.mp3"),
		sb("a2", base+"/Foundation Trilogy - 2/01.mp3"),
		sb("a3", base+"/Foundation Trilogy - 3/01.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Foundation Trilogy")
	if g.Kind != KindAmbiguous {
		t.Fatalf("kind=%q, want ambiguous (marker + sequential single stem)", g.Kind)
	}
	if g.Confident {
		t.Errorf("ambiguous must not be confident")
	}
}

// ITunesPath grouping (Bug 2): two single-file books with DIFFERENT FilePaths but the
// SAME original iTunes album folder must be GROUPED into one hold. Because the two
// identity signals (FilePath folder vs iTunes album) disagree, the group leans
// ambiguous rather than a confident collapse.
func TestClassify_ITunesPathGrouping_Grouped(t *testing.T) {
	album := `W:\itunes\Music\Neil Gaiman\Stardust`
	books := []ShatterBook{
		sbIT("i1", shatterRoot+"/newbooks/loose/track-a.mp3", album+`\01 Track.mp3`),
		sbIT("i2", shatterRoot+"/newbooks/other/track-b.mp3", album+`\02 Track.mp3`),
	}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 1 {
		t.Fatalf("want 1 group (joined by shared iTunes album), got %d (stats %+v)", len(groups), st)
	}
	g := groups[0]
	if len(g.Members) != 2 {
		t.Fatalf("want 2 members grouped, got %d", len(g.Members))
	}
	if g.Kind != KindAmbiguous {
		t.Errorf("kind=%q, want ambiguous (FilePath folders disagree)", g.Kind)
	}
	if g.Confident {
		t.Errorf("ambiguous must not be confident")
	}
}

// ITunesPath must NOT over-merge: two books with the same iTunes album path grouping is
// only triggered when the album folders MATCH. Books with distinct iTunes albums and
// distinct FilePaths stay separate (no spurious grouping / regression guard).
func TestClassify_ITunesPathDistinct_NotMerged(t *testing.T) {
	books := []ShatterBook{
		sbIT("d1", shatterRoot+"/newbooks/a/x.mp3", `W:\itunes\A\Book One\01.mp3`),
		sbIT("d2", shatterRoot+"/newbooks/b/y.mp3", `W:\itunes\A\Book Two\01.mp3`),
	}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 0 {
		t.Fatalf("distinct iTunes albums must not be grouped, got %d: %+v", len(groups), groups)
	}
	if st.Singletons != 2 {
		t.Errorf("Singletons=%d, want 2 (stats %+v)", st.Singletons, st)
	}
}

// A genuine single-file book (group of 1) gets NO hold.
func TestClassify_GenuineSingle_NoHold(t *testing.T) {
	books := []ShatterBook{sb("s1", shatterRoot+"/Andy Weir/The Martian/The Martian.m4b")}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 0 {
		t.Fatalf("want 0 groups for a lone single file, got %d", len(groups))
	}
	if st.Singletons != 1 {
		t.Errorf("Singletons=%d, want 1 (stats %+v)", st.Singletons, st)
	}
}

// Correctly-stored series volumes under the author dir must NOT be held: grandparent
// is the author, not named after the series (mirrors fs_regroup's prefix ⊆ parent).
func TestClassify_SeriesVolumes_Skipped(t *testing.T) {
	author := shatterRoot + "/Brandon Sanderson"
	books := []ShatterBook{
		sb("m1", author+"/Mistborn - 1/book.m4b"),
		sb("m2", author+"/Mistborn - 2/book.m4b"),
		sb("m3", author+"/Mistborn - 3/book.m4b"),
	}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 0 {
		t.Fatalf("series volumes must not be held, got %d groups: %+v", len(groups), groups)
	}
	if st.DistinctSkip != 1 {
		t.Errorf("DistinctSkip=%d, want 1 (stats %+v)", st.DistinctSkip, st)
	}
}

// A flat dump of DISTINCT books under one non-book folder must NOT be held.
func TestClassify_FlatDumpDistinctBooks_Skipped(t *testing.T) {
	dump := shatterRoot + "/abooks"
	books := []ShatterBook{
		sb("x1", dump+"/The Hobbit.m4b"),
		sb("x2", dump+"/Neuromancer.m4b"),
		sb("x3", dump+"/Snow Crash.m4b"),
	}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 0 {
		t.Fatalf("flat dump of distinct books must not be held, got %d: %+v", len(groups), groups)
	}
	if st.DistinctSkip != 1 {
		t.Errorf("DistinctSkip=%d, want 1 (stats %+v)", st.DistinctSkip, st)
	}
}

// A book folder whose chapter dirs carry two distinct titles → ambiguous hold.
func TestClassify_MixedIdentityChapters_Ambiguous(t *testing.T) {
	base := shatterRoot + "/Various/Split Decision"
	books := []ShatterBook{
		sb("i1", base+"/Split Decision - 1/01.mp3"),
		sb("i2", base+"/Split Decision - 2/01.mp3"),
		sb("i3", base+"/Split Decision - 3/01.mp3"),
		sb("i4", base+"/Other Work - 1/01.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Split Decision")
	if g.Kind != KindAmbiguous {
		t.Errorf("kind=%q, want ambiguous", g.Kind)
	}
	if g.Confident {
		t.Errorf("ambiguous must not be confident")
	}
}

// deriveSurvivorTitle strips a leading "NN - " track prefix, trailing parentheticals,
// and a trailing " - N" number.
func TestDeriveSurvivorTitle(t *testing.T) {
	cases := map[string]string{
		"Cage of Souls":            "Cage of Souls",
		"01 - The Fellowship":      "The Fellowship",
		"Dune (Unabridged)":        "Dune",
		"Dune (Unabridged) (2019)": "Dune",
		"The Name of the Wind - 2": "The Name of the Wind",
	}
	for in, want := range cases {
		if got := deriveSurvivorTitle(in); got != want {
			t.Errorf("deriveSurvivorTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// versionMarkers must not false-positive "abridged" inside "unabridged".
func TestVersionMarkers_UnabridgedOnly(t *testing.T) {
	if _, hasAb := versionMarkers("The Book (Unabridged)"); hasAb {
		t.Errorf("unabridged-only text wrongly flagged abridged")
	}
	hasUn, hasAb := versionMarkers("Dune Unabridged / Dune Abridged")
	if !hasUn || !hasAb {
		t.Errorf("want both markers, got unabridged=%v abridged=%v", hasUn, hasAb)
	}
}
