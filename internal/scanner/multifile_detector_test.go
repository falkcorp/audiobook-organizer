// file: internal/scanner/multifile_detector_test.go
// version: 1.1.0
// guid: 9b4f5d2c-3e6a-4b7c-8d9e-0f1a2b3c4d5e
// last-edited: 2026-06-24

package scanner

import (
	"fmt"
	"testing"
)

// mk builds a MultiFileInfo with the given filename under a fixed directory.
func mk(name, album, albumArtist string) MultiFileInfo {
	return MultiFileInfo{
		Path:        "/library/Tarkin/" + name,
		Album:       album,
		AlbumArtist: albumArtist,
	}
}

func mkTrack(name, album, albumArtist string, track, total int) MultiFileInfo {
	f := mk(name, album, albumArtist)
	f.TrackNum = track
	f.TotalTracks = total
	return f
}

func TestExtractSeqNumber(t *testing.T) {
	cases := []struct {
		stem    string
		wantNum int
		wantTot int
	}{
		{"Chapter 01", 1, 0},
		{"Chapter_12", 12, 0},
		{"Part 1 of 8", 1, 8},
		{"Part 03", 3, 0},
		{"Track 07", 7, 0},
		{"(76 of 85) Tarkin", 76, 85},
		{"(1/85) Tarkin", 1, 85},
		{"Tarkin - 1_85", 1, 85},
		{"01 - Intro", 1, 0},
		{"002. Foo", 2, 0},
		{"01", 1, 0},
		{"Disc 02", 2, 0},
		{"CD 03", 3, 0},
		{"Just a title with no number", 0, 0},
		{"Book Title (final)", 0, 0},
	}
	for _, c := range cases {
		gotNum, gotTot := extractSeqNumber(c.stem)
		if gotNum != c.wantNum || gotTot != c.wantTot {
			t.Errorf("extractSeqNumber(%q) = (%d, %d), want (%d, %d)",
				c.stem, gotNum, gotTot, c.wantNum, c.wantTot)
		}
	}
}

func TestNormalizeTagValue(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"Tarkin":                        "tarkin",
		"  TARKIN  ":                    "tarkin",
		"Star_Wars - Tarkin":            "star wars tarkin",
		"Star  Wars\t\tTarkin":          "star wars tarkin",
	}
	for in, want := range cases {
		got := normalizeTagValue(in)
		if got != want {
			t.Errorf("normalizeTagValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectMultiFileGroup_Tarkin85(t *testing.T) {
	var files []MultiFileInfo
	for i := 1; i <= 85; i++ {
		files = append(files, mk(
			fmt.Sprintf("(%d of 85) Tarkin - Star Wars.mp3", i),
			"Tarkin: Star Wars",
			"James Luceno",
		))
	}
	ok, sorted := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if !ok {
		t.Fatalf("expected positive detection for 85-chapter Tarkin set")
	}
	if len(sorted) != 85 {
		t.Fatalf("expected 85 sorted files, got %d", len(sorted))
	}
	// First sorted file should have number 1.
	if got := sorted[0].Path; got != "/library/Tarkin/(1 of 85) Tarkin - Star Wars.mp3" {
		t.Errorf("sort order wrong: first = %s", got)
	}
	if got := sorted[84].Path; got != "/library/Tarkin/(85 of 85) Tarkin - Star Wars.mp3" {
		t.Errorf("sort order wrong: last = %s", got)
	}
}

func TestDetectMultiFileGroup_ChapterNN(t *testing.T) {
	var files []MultiFileInfo
	for i := 1; i <= 12; i++ {
		files = append(files, mk(
			fmt.Sprintf("Chapter %02d.mp3", i),
			"The Great Hunt",
			"Robert Jordan",
		))
	}
	ok, sorted := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if !ok {
		t.Fatalf("expected positive detection for 12 Chapter NN files")
	}
	if len(sorted) != 12 {
		t.Fatalf("expected 12 files, got %d", len(sorted))
	}
}

func TestDetectMultiFileGroup_PartNofM(t *testing.T) {
	var files []MultiFileInfo
	for i := 1; i <= 8; i++ {
		files = append(files, mk(
			fmt.Sprintf("Part %d of 8.m4b", i),
			"Foundation",
			"Isaac Asimov",
		))
	}
	ok, _ := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if !ok {
		t.Fatalf("expected positive detection for Part N of M files")
	}
}

func TestDetectMultiFileGroup_BareNN(t *testing.T) {
	var files []MultiFileInfo
	for i := 1; i <= 5; i++ {
		files = append(files, mk(
			fmt.Sprintf("%02d.mp3", i),
			"Some Audiobook",
			"Some Author",
		))
	}
	ok, _ := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if !ok {
		t.Fatalf("expected positive detection for bare NN files")
	}
}

func TestDetectMultiFileGroup_TrackTagFallback(t *testing.T) {
	// Filenames have no obvious sequence (just a clean title), but ID3
	// track numbers + matching album tags should still trigger detection.
	files := []MultiFileInfo{
		mkTrack("Intro.mp3", "Foundation", "Isaac Asimov", 1, 5),
		mkTrack("The Plan.mp3", "Foundation", "Isaac Asimov", 2, 5),
		mkTrack("The Encyclopedia.mp3", "Foundation", "Isaac Asimov", 3, 5),
		mkTrack("Trantor.mp3", "Foundation", "Isaac Asimov", 4, 5),
		mkTrack("Outro.mp3", "Foundation", "Isaac Asimov", 5, 5),
	}
	ok, sorted := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if !ok {
		t.Fatalf("expected detection via track-number tag fallback")
	}
	if sorted[0].TrackNum != 1 || sorted[4].TrackNum != 5 {
		t.Errorf("track-tag fallback sort order wrong: %+v", sorted)
	}
}

func TestDetectMultiFileGroup_RejectsDistinctTitles(t *testing.T) {
	files := []MultiFileInfo{
		mk("The Hobbit.mp3", "The Hobbit", "J.R.R. Tolkien"),
		mk("The Silmarillion.mp3", "The Silmarillion", "J.R.R. Tolkien"),
		mk("Unfinished Tales.mp3", "Unfinished Tales", "J.R.R. Tolkien"),
		mk("Children of Hurin.mp3", "Children of Hurin", "J.R.R. Tolkien"),
		mk("Beren and Luthien.mp3", "Beren and Luthien", "J.R.R. Tolkien"),
	}
	ok, _ := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if ok {
		t.Fatalf("expected NEGATIVE detection for 5 distinct titles with no sequence numbers")
	}
}

func TestDetectMultiFileGroup_RejectsTooFew(t *testing.T) {
	files := []MultiFileInfo{
		mk("Chapter 01.mp3", "Book", "Author"),
		mk("Chapter 02.mp3", "Book", "Author"),
	}
	ok, _ := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if ok {
		t.Fatalf("expected NEGATIVE detection for fewer than MinFiles=3 files")
	}
}

func TestDetectMultiFileGroup_RejectsDisagreeingAlbums(t *testing.T) {
	// Sequential numbering present, but album/album_artist disagree across
	// all files — clearly distinct books happening to use the same numeric
	// prefix.
	files := []MultiFileInfo{
		mk("01 - One.mp3", "Album A", "Artist A"),
		mk("02 - Two.mp3", "Album B", "Artist B"),
		mk("03 - Three.mp3", "Album C", "Artist C"),
		mk("04 - Four.mp3", "Album D", "Artist D"),
	}
	ok, _ := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if ok {
		t.Fatalf("expected NEGATIVE detection when album tags disagree across all files")
	}
}

func TestDetectMultiFileGroup_QuorumToleratesOneOddFile(t *testing.T) {
	// 4 of 5 files match the pattern + album tag; one doesn't. Detector
	// should still trigger via quorum.
	files := []MultiFileInfo{
		mk("Chapter 01.mp3", "Book", "Author"),
		mk("Chapter 02.mp3", "Book", "Author"),
		mk("Chapter 03.mp3", "Book", "Author"),
		mk("Chapter 04.mp3", "Book", "Author"),
		mk("Bonus Material.mp3", "Book", "Author"),
	}
	ok, _ := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if !ok {
		t.Fatalf("expected positive detection with 4/5 quorum")
	}
}

func TestDetectMultiFileGroup_RejectsSparseNumbers(t *testing.T) {
	// Three files with numbers 1, 2, and 500 — not a dense sequence.
	files := []MultiFileInfo{
		mk("01.mp3", "Album", "Artist"),
		mk("02.mp3", "Album", "Artist"),
		mk("500.mp3", "Album", "Artist"),
	}
	ok, _ := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if ok {
		t.Fatalf("expected NEGATIVE detection for sparse numbering (1, 2, 500)")
	}
}

func TestDetectMultiFileGroup_NoTagsGroupedBySequentialName(t *testing.T) {
	// AP-5: files with no album / album_artist tags at all but clear sequential
	// naming — e.g. "Assimil Japanese With Ease Audio/06 - X.mp3". The tag
	// quorum cannot be satisfied (no tags), but tag silence is uniform, not a
	// sign of tag conflict.  Sequential names are sufficient evidence.
	files := []MultiFileInfo{
		mk("06 - Lesson Six.mp3", "", ""),
		mk("07 - Lesson Seven.mp3", "", ""),
		mk("08 - Lesson Eight.mp3", "", ""),
		mk("09 - Lesson Nine.mp3", "", ""),
		mk("10 - Lesson Ten.mp3", "", ""),
	}
	ok, sorted := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if !ok {
		t.Fatal("expected positive detection for untagged sequential files (AP-5)")
	}
	if len(sorted) != len(files) {
		t.Fatalf("expected %d sorted files, got %d", len(files), len(sorted))
	}
}

func TestDetectMultiFileGroup_MixedTagsStillGroupedByArtistQuorum(t *testing.T) {
	// Conflicting album tags but consistent album_artist: artist quorum passes.
	// This verifies that the AP-5 allTagsAbsent path doesn't break the existing
	// quorum path when some tags are present.
	files := []MultiFileInfo{
		mk("01.mp3", "Album A", "Same Author"),
		mk("02.mp3", "Album B", "Same Author"),
		mk("03.mp3", "Album C", "Same Author"),
		mk("04.mp3", "Album D", "Same Author"),
	}
	ok, _ := DetectMultiFileGroup(files, DefaultMultiFileConfig())
	if !ok {
		t.Fatal("expected positive detection when album_artist quorum passes despite varying album tags")
	}
}
