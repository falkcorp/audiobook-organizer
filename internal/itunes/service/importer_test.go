// file: internal/itunes/service/importer_test.go
// version: 1.0.0
// guid: 3e7f1a2b-8c4d-4e9a-b6f0-2d5e8c1a7f3b

package itunesservice

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// newTestImporter creates a minimal *Importer for white-box unit tests.
func newTestImporter() *Importer {
	return &Importer{cfg: Config{}}
}

// TestBuildBookFromAlbumGroup verifies field mapping from iTunes tracks.
func TestBuildBookFromAlbumGroup(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "itunes-track-*.m4b")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	content := bytes.Repeat([]byte("a"), 2048)
	if _, err := tmpFile.Write(content); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("failed to close temp file: %v", err)
	}

	filePath := tmpFile.Name()
	location := itunes.EncodeLocation(filePath)
	now := time.Now().UTC()
	playDate := now.Add(-time.Hour).Unix()
	libraryPath := "/tmp/iTunes Library.xml"

	track := &itunes.Track{
		Location:     location,
		Name:         "Chapter 1",
		Album:        "My Audiobook",
		PersistentID: "ABC123",
		TotalTime:    123000,
		Year:         2000,
		PlayCount:    2,
		Rating:       80,
		Bookmark:     5000,
		DateAdded:    now,
		PlayDate:     playDate,
		AlbumArtist:  "Narrator",
		Artist:       "Author",
		Comments:     "First edition",
		Size:         4096,
	}

	imp := newTestImporter()
	group := albumGroup{key: "Author|My Audiobook", tracks: []*itunes.Track{track}}
	book, err := imp.buildBookFromAlbumGroup(group, libraryPath, itunes.ImportOptions{})
	if err != nil {
		t.Fatalf("buildBookFromAlbumGroup error: %v", err)
	}

	if book.Title != "My Audiobook" {
		t.Fatalf("title = %q, want %q", book.Title, "My Audiobook")
	}
	if book.ITunesPersistentID == nil || *book.ITunesPersistentID != "ABC123" {
		t.Fatalf("persistent ID not set correctly")
	}
	if book.ITunesDateAdded == nil || !book.ITunesDateAdded.Equal(now) {
		t.Fatalf("date added not set correctly")
	}
	if book.ITunesLastPlayed == nil || book.ITunesLastPlayed.Unix() != playDate {
		t.Fatalf("last played not set correctly")
	}
	if book.ITunesPlayCount == nil || *book.ITunesPlayCount != 2 {
		t.Fatalf("play count not set correctly")
	}
	if book.ITunesRating == nil || *book.ITunesRating != 80 {
		t.Fatalf("rating not set correctly")
	}
	if book.ITunesBookmark == nil || *book.ITunesBookmark != 5000 {
		t.Fatalf("bookmark not set correctly")
	}
	if book.ITunesImportSource == nil || *book.ITunesImportSource != libraryPath {
		t.Fatalf("import source not set correctly")
	}
	if book.Narrator == nil || *book.Narrator != "Narrator" {
		t.Fatalf("narrator not set correctly")
	}
	if book.Edition != nil {
		t.Fatalf("edition should be nil, got %q", *book.Edition)
	}
	if book.Description == nil || *book.Description != "First edition" {
		t.Fatalf("description should be %q from Comments field", "First edition")
	}
	if book.FileSize == nil || *book.FileSize != 4096 {
		t.Fatalf("file size = %d, want 4096", ptrOrZeroInt64(book.FileSize))
	}
}

func ptrOrZeroInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// TestBuildBookFromAlbumGroup_AllFields verifies complete field mapping.
func TestBuildBookFromAlbumGroup_AllFields(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "complete-track-*.m4b")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	filePath := tmpFile.Name()
	location := itunes.EncodeLocation(filePath)
	now := time.Now().UTC()

	track := &itunes.Track{
		Location:     location,
		Name:         "Test Track Title",
		PersistentID: "DEF456",
		TotalTime:    456000,
		Year:         2021,
		PlayCount:    5,
		Rating:       100,
		Bookmark:     10000,
		DateAdded:    now,
		PlayDate:     now.Unix(),
		AlbumArtist:  "Test Narrator",
		Artist:       "Test Author",
		Album:        "Test Series",
		Comments:     "Test Edition",
		Size:         8192,
	}

	imp := newTestImporter()
	libraryPath := "/path/to/iTunes Library.xml"
	group := albumGroup{key: "Test Author|Test Series", tracks: []*itunes.Track{track}}
	book, err := imp.buildBookFromAlbumGroup(group, libraryPath, itunes.ImportOptions{})
	if err != nil {
		t.Fatalf("buildBookFromAlbumGroup error: %v", err)
	}

	if book.ITunesPersistentID == nil || *book.ITunesPersistentID != "DEF456" {
		t.Error("persistent ID not set")
	}
	if book.ITunesDateAdded == nil {
		t.Error("date added not set")
	}
	if book.ITunesPlayCount == nil || *book.ITunesPlayCount != 5 {
		t.Error("play count not set correctly")
	}
	if book.ITunesRating == nil || *book.ITunesRating != 100 {
		t.Error("rating not set correctly")
	}
	if book.ITunesBookmark == nil || *book.ITunesBookmark != 10000 {
		t.Error("bookmark not set correctly")
	}
	if book.ITunesImportSource == nil || *book.ITunesImportSource != libraryPath {
		t.Error("import source not set")
	}
}

// TestBuildBookFromAlbumGroup_MinimalTrack verifies handling of minimal track data.
func TestBuildBookFromAlbumGroup_MinimalTrack(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "minimal-track-*.m4b")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	filePath := tmpFile.Name()
	location := itunes.EncodeLocation(filePath)

	track := &itunes.Track{
		Location:     location,
		PersistentID: "MIN123",
	}

	imp := newTestImporter()
	group := albumGroup{key: "|", tracks: []*itunes.Track{track}}
	book, err := imp.buildBookFromAlbumGroup(group, "/library.xml", itunes.ImportOptions{})
	if err != nil {
		t.Fatalf("buildBookFromAlbumGroup error: %v", err)
	}

	if book.ITunesPersistentID == nil {
		t.Error("persistent ID should be set")
	}
	if book.FilePath == "" {
		t.Error("file path should be decoded")
	}
}

// TestGroupTracksByAlbum verifies multi-track grouping and music filtering.
func TestGroupTracksByAlbum(t *testing.T) {
	library := &itunes.Library{
		Tracks: map[string]*itunes.Track{
			"1": {TrackID: 1, Name: "Chapter 1", Artist: "Author A", Album: "Book One", Kind: "Audiobook", TrackNumber: 1, DiscNumber: 1},
			"2": {TrackID: 2, Name: "Chapter 2", Artist: "Author A", Album: "Book One", Kind: "Audiobook", TrackNumber: 2, DiscNumber: 1},
			"3": {TrackID: 3, Name: "Chapter 1", Artist: "Author B", Album: "Book Two", Kind: "Audiobook", TrackNumber: 1, DiscNumber: 1},
			"4": {TrackID: 4, Name: "Music Track", Artist: "Singer", Album: "Pop Album", Kind: "MPEG audio file"},
		},
	}

	imp := newTestImporter()
	groups := imp.groupTracksByAlbum(library)

	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	var bookOneGroup *albumGroup
	for i := range groups {
		if groups[i].key == "album:book one" {
			bookOneGroup = &groups[i]
			break
		}
	}
	if bookOneGroup == nil {
		t.Fatal("expected to find 'album:book one' group")
	}
	if len(bookOneGroup.tracks) != 2 {
		t.Errorf("expected 2 tracks in Book One group, got %d", len(bookOneGroup.tracks))
	}
	if bookOneGroup.tracks[0].TrackNumber != 1 || bookOneGroup.tracks[1].TrackNumber != 2 {
		t.Error("tracks not sorted by track number")
	}
}

// TestGroupTracksByAlbum_EmptyAlbumChapterParts verifies that iTunes tracks with
// NO album tag whose names carry a trailing part marker ("At All Costs – 11/23")
// collapse into a single book group, rather than one book per chapter part.
// This is the root cause of the part-vs-full-book dedup candidate explosion.
func TestGroupTracksByAlbum_EmptyAlbumChapterParts(t *testing.T) {
	library := &itunes.Library{
		Tracks: map[string]*itunes.Track{
			"1": {TrackID: 1, Name: "At All Costs – 11/23", Artist: "David Weber", Album: "", Kind: "Audiobook", TrackNumber: 11, DiscNumber: 1},
			"2": {TrackID: 2, Name: "At All Costs – 13/23", Artist: "David Weber", Album: "", Kind: "Audiobook", TrackNumber: 13, DiscNumber: 1},
			"3": {TrackID: 3, Name: "At All Costs – 23/23", Artist: "David Weber", Album: "", Kind: "Audiobook", TrackNumber: 23, DiscNumber: 1},
		},
	}

	imp := newTestImporter()
	groups := imp.groupTracksByAlbum(library)

	if len(groups) != 1 {
		t.Fatalf("expected 1 group (all chapter parts collapsed), got %d: %+v", len(groups), groups)
	}
	if groups[0].key != "name:David Weber|at all costs" {
		t.Errorf("group key = %q, want %q", groups[0].key, "name:David Weber|at all costs")
	}
	if len(groups[0].tracks) != 3 {
		t.Errorf("expected 3 tracks in the collapsed group, got %d", len(groups[0].tracks))
	}
}

// TestGroupTracksByAlbum_MultiTrackBooks tests grouping with multi-track test data.
func TestGroupTracksByAlbum_MultiTrackBooks(t *testing.T) {
	library := &itunes.Library{
		Tracks: map[string]*itunes.Track{
			"500": {TrackID: 500, Name: "Chapter 1 - Loomings", Artist: "Herman Melville", Album: "Moby Dick", Kind: "Audiobook", TrackNumber: 1, DiscNumber: 1, TotalTime: 3600000, Size: 50000000},
			"501": {TrackID: 501, Name: "Chapter 2 - The Carpet-Bag", Artist: "Herman Melville", Album: "Moby Dick", Kind: "Audiobook", TrackNumber: 2, DiscNumber: 1, TotalTime: 3200000, Size: 45000000},
			"502": {TrackID: 502, Name: "Chapter 3 - The Spouter-Inn", Artist: "Herman Melville", Album: "Moby Dick", Kind: "Audiobook", TrackNumber: 3, DiscNumber: 1, TotalTime: 3400000, Size: 48000000},
			"600": {TrackID: 600, Name: "Part 1", Artist: "Jane Austen", Album: "Pride and Prejudice", Kind: "Audiobook", TrackNumber: 1, DiscNumber: 1, TotalTime: 5400000, Size: 60000000},
			"601": {TrackID: 601, Name: "Part 2", Artist: "Jane Austen", Album: "Pride and Prejudice", Kind: "Audiobook", TrackNumber: 2, DiscNumber: 1, TotalTime: 4800000, Size: 55000000},
		},
	}

	imp := newTestImporter()
	groups := imp.groupTracksByAlbum(library)

	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	var mobyGroup, prideGroup *albumGroup
	for i := range groups {
		switch groups[i].key {
		case "album:moby dick":
			mobyGroup = &groups[i]
		case "album:pride and prejudice":
			prideGroup = &groups[i]
		}
	}

	if mobyGroup == nil {
		t.Fatal("expected to find 'album:moby dick' group")
	}
	if len(mobyGroup.tracks) != 3 {
		t.Errorf("expected 3 tracks in Moby Dick group, got %d", len(mobyGroup.tracks))
	}
	for i, track := range mobyGroup.tracks {
		if track.TrackNumber != i+1 {
			t.Errorf("Moby Dick track %d has TrackNumber %d, want %d", i, track.TrackNumber, i+1)
		}
	}

	if prideGroup == nil {
		t.Fatal("expected to find 'Jane Austen|Pride and Prejudice' group")
	}
	if len(prideGroup.tracks) != 2 {
		t.Errorf("expected 2 tracks in Pride and Prejudice group, got %d", len(prideGroup.tracks))
	}
}

// TestGroupTracksByAlbum_DiscSorting tests that tracks are sorted by disc then track number.
func TestGroupTracksByAlbum_DiscSorting(t *testing.T) {
	library := &itunes.Library{
		Tracks: map[string]*itunes.Track{
			"1": {TrackID: 1, Name: "D2T1", Artist: "Author", Album: "Book", Kind: "Audiobook", TrackNumber: 1, DiscNumber: 2},
			"2": {TrackID: 2, Name: "D1T2", Artist: "Author", Album: "Book", Kind: "Audiobook", TrackNumber: 2, DiscNumber: 1},
			"3": {TrackID: 3, Name: "D1T1", Artist: "Author", Album: "Book", Kind: "Audiobook", TrackNumber: 1, DiscNumber: 1},
		},
	}

	imp := newTestImporter()
	groups := imp.groupTracksByAlbum(library)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}

	tracks := groups[0].tracks
	expected := []struct{ disc, track int }{{1, 1}, {1, 2}, {2, 1}}
	for i, e := range expected {
		if tracks[i].DiscNumber != e.disc || tracks[i].TrackNumber != e.track {
			t.Errorf("track %d: got disc=%d track=%d, want disc=%d track=%d",
				i, tracks[i].DiscNumber, tracks[i].TrackNumber, e.disc, e.track)
		}
	}
}

// TestBuildBookFromAlbumGroup_MultiTrack tests that multi-track albums sum duration and size.
func TestBuildBookFromAlbumGroup_MultiTrack(t *testing.T) {
	tmpDir := t.TempDir()
	var tracks []*itunes.Track
	for i := 1; i <= 3; i++ {
		f, err := os.Create(filepath.Join(tmpDir, fmt.Sprintf("chapter%d.m4b", i)))
		if err != nil {
			t.Fatal(err)
		}
		f.Write(bytes.Repeat([]byte("x"), 100))
		f.Close()

		tracks = append(tracks, &itunes.Track{
			TrackID:     500 + i - 1,
			Name:        fmt.Sprintf("Chapter %d", i),
			Artist:      "Herman Melville",
			Album:       "Moby Dick",
			Kind:        "Audiobook",
			TrackNumber: i,
			DiscNumber:  1,
			TotalTime:   int64(3000000 + i*100000),
			Size:        int64(40000000 + i*5000000),
			Location:    itunes.EncodeLocation(filepath.Join(tmpDir, fmt.Sprintf("chapter%d.m4b", i))),
		})
	}

	imp := newTestImporter()
	group := albumGroup{key: "Herman Melville|Moby Dick", tracks: tracks}
	book, err := imp.buildBookFromAlbumGroup(group, "/library.xml", itunes.ImportOptions{})
	if err != nil {
		t.Fatalf("buildBookFromAlbumGroup error: %v", err)
	}

	if book.Title != "Moby Dick" {
		t.Errorf("title = %q, want %q", book.Title, "Moby Dick")
	}
	if book.FilePath != tmpDir {
		t.Errorf("filePath = %q, want common parent dir %q", book.FilePath, tmpDir)
	}

	expectedDuration := int((3100000 + 3200000 + 3300000) / 1000)
	if book.Duration == nil || *book.Duration != expectedDuration {
		t.Errorf("duration = %v, want %d", book.Duration, expectedDuration)
	}

	expectedSize := int64(45000000 + 50000000 + 55000000)
	if book.FileSize == nil || *book.FileSize != expectedSize {
		t.Errorf("fileSize = %v, want %d", book.FileSize, expectedSize)
	}
}

// TestExtractSeriesName tests series name extraction from album strings.
func TestExtractSeriesName(t *testing.T) {
	tests := []struct {
		album string
		want  string
	}{
		{"", ""},
		{"Simple Album", "Simple Album"},
		{"Series, Book 1", "Series"},
		{"Series - Part 2", "Series"},
		{"Series: Volume 3", "Series"},
		{"  Padded , Book  ", "Padded"},
		{"No Separator Here", "No Separator Here"},
		{"A,B,C", "A"}, // SplitN(..., 2) splits on first comma regardless of part count
	}
	for _, tt := range tests {
		t.Run(tt.album, func(t *testing.T) {
			got := extractSeriesName(tt.album)
			if got != tt.want {
				t.Errorf("extractSeriesName(%q) = %q, want %q", tt.album, got, tt.want)
			}
		})
	}
}

// TestImportLibraryState tests import mode to library state mapping.
func TestImportLibraryState(t *testing.T) {
	imp := newTestImporter()
	tests := []struct {
		mode itunes.ImportMode
		want string
	}{
		{itunes.ImportModeOrganized, "organized"},
		{itunes.ImportModeImport, "imported"},
		{itunes.ImportModeOrganize, "imported"},
	}
	for _, tt := range tests {
		got := imp.importLibraryState(tt.mode)
		if got != tt.want {
			t.Errorf("importLibraryState(%q) = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

// TestResolveImportMode tests import mode string resolution.
func TestResolveImportMode(t *testing.T) {
	imp := newTestImporter()
	tests := []struct {
		input string
		want  itunes.ImportMode
	}{
		{"organized", itunes.ImportModeOrganized},
		{"organize", itunes.ImportModeOrganize},
		{"import", itunes.ImportModeImport},
		{"unknown", itunes.ImportModeImport},
		{"", itunes.ImportModeImport},
	}
	for _, tt := range tests {
		got := imp.resolveImportMode(tt.input)
		if got != tt.want {
			t.Errorf("resolveImportMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestPtrHelpers tests int/int64 pointer helpers.
func TestPtrHelpers(t *testing.T) {
	p := intPtrLocal(42)
	if p == nil || *p != 42 {
		t.Errorf("intPtrLocal(42) = %v, want *42", p)
	}
	p64 := int64PtrLocal(99)
	if p64 == nil || *p64 != 99 {
		t.Errorf("int64PtrLocal(99) = %v, want *99", p64)
	}
}

// TestStatusHelpers tests the status tracking functions.
func TestStatusHelpers(t *testing.T) {
	t.Run("load_and_reuse", func(t *testing.T) {
		var sm importStatusMap
		s1 := sm.load("op1")
		if s1 == nil {
			t.Fatal("expected non-nil status")
		}
		s2 := sm.load("op1")
		if s2 != s1 {
			t.Error("expected same status instance on second load")
		}
	})

	t.Run("setImportTotal", func(t *testing.T) {
		s := &itunesImportStatus{}
		setImportTotal(s, 42)
		if s.Total != 42 {
			t.Errorf("expected Total=42, got %d", s.Total)
		}
	})

	t.Run("incImportProcessed", func(t *testing.T) {
		s := &itunesImportStatus{}
		incImportProcessed(s, 10)
		if s.Processed != 10 {
			t.Errorf("expected Processed=10, got %d", s.Processed)
		}
	})

	t.Run("incImportImported", func(t *testing.T) {
		s := &itunesImportStatus{}
		incImportImported(s)
		incImportImported(s)
		if s.Imported != 2 {
			t.Errorf("expected Imported=2, got %d", s.Imported)
		}
	})

	t.Run("incImportSkipped", func(t *testing.T) {
		s := &itunesImportStatus{}
		incImportSkipped(s)
		if s.Skipped != 1 {
			t.Errorf("expected Skipped=1, got %d", s.Skipped)
		}
	})

	t.Run("recordImportFailure", func(t *testing.T) {
		s := &itunesImportStatus{}
		recordImportFailure(s, "error1")
		recordImportFailure(s, "error2")
		if s.Failed != 2 {
			t.Errorf("expected Failed=2, got %d", s.Failed)
		}
		if len(s.Errors) != 2 {
			t.Errorf("expected 2 errors, got %d", len(s.Errors))
		}
	})

	t.Run("recordImportFailure_respects_limit", func(t *testing.T) {
		s := &itunesImportStatus{}
		for i := 0; i < importErrorLimit+10; i++ {
			recordImportFailure(s, "err")
		}
		if len(s.Errors) != importErrorLimit {
			t.Errorf("expected %d errors, got %d", importErrorLimit, len(s.Errors))
		}
	})

	t.Run("recordImportError", func(t *testing.T) {
		s := &itunesImportStatus{}
		recordImportError(s, "import error")
		if len(s.Errors) != 1 || s.Errors[0] != "import error" {
			t.Errorf("unexpected errors: %v", s.Errors)
		}
	})

	t.Run("recordImportError_respects_limit", func(t *testing.T) {
		s := &itunesImportStatus{}
		for i := 0; i < importErrorLimit+5; i++ {
			recordImportError(s, "err")
		}
		if len(s.Errors) != importErrorLimit {
			t.Errorf("expected %d errors, got %d", importErrorLimit, len(s.Errors))
		}
	})
}

// TestSnapshotStatus tests status snapshotting via importStatusMap.
func TestSnapshotStatus(t *testing.T) {
	var sm importStatusMap
	opID := "test-snapshot"

	s := sm.load(opID)
	setImportTotal(s, 100)
	incImportProcessed(s, 50)
	incImportImported(s)
	incImportSkipped(s)
	recordImportFailure(s, "test error")

	snap := sm.snapshot(opID)

	if snap.Total != 100 || snap.Processed != 50 || snap.Imported != 1 || snap.Skipped != 1 || snap.Failed != 1 {
		t.Errorf("snapshot mismatch: %+v", snap)
	}
	if len(snap.Errors) != 1 || snap.Errors[0] != "test error" {
		t.Errorf("snapshot errors mismatch: %v", snap.Errors)
	}

	snap.Total = 999
	if s.Total != 100 {
		t.Error("snapshot modification should not affect original")
	}
}

// TestBuildImportSummary tests summary string generation.
func TestBuildImportSummary(t *testing.T) {
	s := &itunesImportStatus{
		Imported: 10,
		Skipped:  3,
		Failed:   2,
	}
	summary := buildImportSummary(s)
	for _, want := range []string{"10 new", "0 linked", "3 skipped", "2 failed"} {
		if !containsStr(summary, want) {
			t.Errorf("summary %q missing %q", summary, want)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && stringContains(s, sub))
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
