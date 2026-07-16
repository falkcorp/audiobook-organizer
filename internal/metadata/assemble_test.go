// file: internal/metadata/assemble_test.go
// version: 1.3.0
// guid: 2c3d4e5f-6a7b-8c9d-0e1f-2a3b4c5d6e7f

package metadata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/mock"
)

// newAssembleExtractorStub returns an in-package mockery-generated
// MetadataExtractor configured to return fixed metadata. Used by
// tests that exercise AssembleBookMetadata and want to skip real
// tag extraction.
func newAssembleExtractorStub(t *testing.T, meta Metadata, err error) *mockMetadataExtractorInternal {
	t.Helper()
	m := newMockMetadataExtractorInternal(t)
	m.EXPECT().
		ExtractMetadata(mock.Anything).
		Return(meta, err).Maybe()
	return m
}

func TestResolveTitlePrefersTag(t *testing.T) {
	tag := &Metadata{Title: "The Great Book"}
	fm := &FolderMetadata{Title: "Folder Title"}
	title, source := resolveTitle(tag, fm, "/some/path", "", nil)
	if title != "The Great Book" || source != "tag.Title" {
		t.Errorf("got title=%q source=%q, want 'The Great Book' / 'tag.Title'", title, source)
	}
}

func TestResolveTitleSkipsGenericTag(t *testing.T) {
	tag := &Metadata{Title: "Part 1"}
	fm := &FolderMetadata{Title: "Real Book Title"}
	title, source := resolveTitle(tag, fm, "/some/path", "", nil)
	if title != "Real Book Title" || source != "folder.Title" {
		t.Errorf("got title=%q source=%q, want 'Real Book Title' / 'folder.Title'", title, source)
	}
}

// TestAssembleBookMetadata_GenericChapterUsesFolder is the CONS-17 (Path B)
// regression guard at the resolution seam. Multi-file groups detected by the
// scanner's sequential-naming detector have generic-likely per-chapter tag
// titles ("Chapter 1", "Part 1", bare numbers). Routing such a group through
// AssembleBookMetadata must yield the FOLDER title, not the first chapter's tag —
// otherwise the chapter name leaks into (and collides across) Book.Title.
func TestAssembleBookMetadata_GenericChapterUsesFolder(t *testing.T) {
	base := t.TempDir()
	bookDir := filepath.Join(base, "Douglas Adams", "The Hitchhikers Guide")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	firstChapter := filepath.Join(bookDir, "chapter01.mp3")
	if err := os.WriteFile(firstChapter, []byte("not a real mp3"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First chapter carries a generic per-chapter title, as sequential multi-file
	// audiobooks typically do.
	SetMetadataExtractor(newAssembleExtractorStub(t, Metadata{
		Title:  "Chapter 1",
		Artist: "Douglas Adams",
	}, nil))
	defer func() { SetMetadataExtractor(nil) }()

	bm, err := AssembleBookMetadata(bookDir, firstChapter, 5, 0)
	if err != nil {
		t.Fatalf("AssembleBookMetadata error: %v", err)
	}
	if bm.Title != "The Hitchhikers Guide" || bm.TitleSource != "folder.Title" {
		t.Errorf("Title = %q (source %q), want 'The Hitchhikers Guide' / 'folder.Title'",
			bm.Title, bm.TitleSource)
	}
}

// newPerFileExtractorStub returns a MetadataExtractor whose result depends on
// the file's base name, so a test can give each chapter its own tag title.
func newPerFileExtractorStub(t *testing.T, byBase map[string]Metadata) *mockMetadataExtractorInternal {
	t.Helper()
	m := newMockMetadataExtractorInternal(t)
	m.EXPECT().ExtractMetadata(mock.Anything).RunAndReturn(func(path string) (Metadata, error) {
		return byBase[filepath.Base(path)], nil
	}).Maybe()
	return m
}

// withChapterFiles builds <base>/<author>/<book>/ containing the named files and
// points config at .mp3 so the CONS-17b agree-check can enumerate them.
func withChapterFiles(t *testing.T, names ...string) (bookDir, firstChapter string) {
	t.Helper()
	prevExts := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prevExts })

	bookDir = filepath.Join(t.TempDir(), "Douglas Adams", "The Hitchhikers Guide")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(bookDir, n), []byte("not a real mp3"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return bookDir, filepath.Join(bookDir, names[0])
}

// TestAssembleBookMetadata_DisagreeingChapterTagsUseFolder is the CONS-17b
// regression guard. The first chapter's tag title ("Big Finish Ident") is NOT
// generic, so isGenericTitle doesn't catch it and the pre-fix resolveTitle
// adopted it as the whole book's title — leaking a per-chapter ident into
// Book.Title and colliding across every book with that ident. Because the
// chapters disagree on their stripped tag titles, the title must come from the
// folder instead.
func TestAssembleBookMetadata_DisagreeingChapterTagsUseFolder(t *testing.T) {
	bookDir, firstChapter := withChapterFiles(t, "chapter01.mp3", "chapter02.mp3")

	SetMetadataExtractor(newPerFileExtractorStub(t, map[string]Metadata{
		"chapter01.mp3": {Title: "Big Finish Ident", Artist: "Douglas Adams"},
		"chapter02.mp3": {Title: "The Hitchhikers Guide - Part 2", Artist: "Douglas Adams"},
	}))
	defer func() { SetMetadataExtractor(nil) }()

	bm, err := AssembleBookMetadata(bookDir, firstChapter, 2, 0)
	if err != nil {
		t.Fatalf("AssembleBookMetadata error: %v", err)
	}
	if bm.Title != "The Hitchhikers Guide" || bm.TitleSource != "folder.Title" {
		t.Errorf("Title = %q (source %q), want 'The Hitchhikers Guide' / 'folder.Title' — "+
			"a per-chapter ident must not become the book title", bm.Title, bm.TitleSource)
	}
}

// TestAssembleBookMetadata_AgreeingChapterTagsWin is the positive half of
// CONS-17b: when every chapter strips to the SAME title, that IS the book title
// and must beat the folder name (mirrors the iTunes agreedStrippedTitle case,
// e.g. "Aces Abroad - Part 1…14" → "Aces Abroad").
func TestAssembleBookMetadata_AgreeingChapterTagsWin(t *testing.T) {
	bookDir, firstChapter := withChapterFiles(t, "chapter01.mp3", "chapter02.mp3", "chapter03.mp3")

	SetMetadataExtractor(newPerFileExtractorStub(t, map[string]Metadata{
		"chapter01.mp3": {Title: "Aces Abroad - Part 1", Artist: "George R. R. Martin"},
		"chapter02.mp3": {Title: "Aces Abroad - Part 2", Artist: "George R. R. Martin"},
		"chapter03.mp3": {Title: "Aces Abroad - Part 3", Artist: "George R. R. Martin"},
	}))
	defer func() { SetMetadataExtractor(nil) }()

	bm, err := AssembleBookMetadata(bookDir, firstChapter, 3, 0)
	if err != nil {
		t.Fatalf("AssembleBookMetadata error: %v", err)
	}
	if bm.Title != "Aces Abroad" || bm.TitleSource != "tag.Title(agreed)" {
		t.Errorf("Title = %q (source %q), want 'Aces Abroad' / 'tag.Title(agreed)'",
			bm.Title, bm.TitleSource)
	}
}

func TestResolveTitleFallsBackToFilename(t *testing.T) {
	tag := &Metadata{Title: "Chapter 3"}
	fm := &FolderMetadata{}
	title, source := resolveTitle(tag, fm, "/audiobooks/My Novel.mp3", "", nil)
	if title != "My Novel" || source != "filename" {
		t.Errorf("got title=%q source=%q, want 'My Novel' / 'filename'", title, source)
	}
}

func TestResolveTitleNoSources(t *testing.T) {
	fm := &FolderMetadata{}
	title, source := resolveTitle(nil, fm, "", "", nil)
	if title != "" || source != "unknown" {
		t.Errorf("got title=%q source=%q, want '' / 'unknown'", title, source)
	}
}

func TestResolveAuthorsPrefersTag(t *testing.T) {
	tag := &Metadata{Artist: "Author One & Author Two"}
	fm := &FolderMetadata{Authors: []string{"Folder Author"}}
	authors, source := resolveAuthors(tag, fm)
	if source != "tag.Artist" {
		t.Errorf("got source=%q, want 'tag.Artist'", source)
	}
	if len(authors) != 2 || authors[0] != "Author One" || authors[1] != "Author Two" {
		t.Errorf("got authors=%v, want [Author One, Author Two]", authors)
	}
}

func TestResolveAuthorsFallsBackToFolder(t *testing.T) {
	fm := &FolderMetadata{Authors: []string{"Folder Author"}}
	authors, source := resolveAuthors(nil, fm)
	if source != "folder.Authors" || len(authors) != 1 || authors[0] != "Folder Author" {
		t.Errorf("got authors=%v source=%q", authors, source)
	}
}

func TestResolveAuthorsNone(t *testing.T) {
	authors, source := resolveAuthors(nil, &FolderMetadata{})
	if authors != nil || source != "unknown" {
		t.Errorf("got authors=%v source=%q", authors, source)
	}
}

func TestResolveSeriesFromTag(t *testing.T) {
	tag := &Metadata{Series: "Discworld", SeriesIndex: 5}
	fm := &FolderMetadata{}
	name, pos, source := resolveSeries(tag, fm)
	if name != "Discworld" || pos != 5 || source != "tag.Series" {
		t.Errorf("got name=%q pos=%d source=%q", name, pos, source)
	}
}

func TestResolveSeriesFromFolder(t *testing.T) {
	fm := &FolderMetadata{SeriesName: "Wheel of Time", SeriesPosition: 14}
	name, pos, source := resolveSeries(nil, fm)
	if name != "Wheel of Time" || pos != 14 || source != "folder.Series" {
		t.Errorf("got name=%q pos=%d source=%q", name, pos, source)
	}
}

func TestResolveSeriesAlbumConfirmed(t *testing.T) {
	tag := &Metadata{Album: "Discworld"}
	fm := &FolderMetadata{SeriesName: "Discworld", SeriesPosition: 3}
	name, pos, source := resolveSeries(tag, fm)
	if name != "Discworld" || pos != 3 || source != "folder.Series(album-confirmed)" {
		t.Errorf("got name=%q pos=%d source=%q", name, pos, source)
	}
}

func TestResolveSeriesNone(t *testing.T) {
	name, pos, source := resolveSeries(nil, &FolderMetadata{})
	if name != "" || pos != 0 || source != "unknown" {
		t.Errorf("got name=%q pos=%d source=%q", name, pos, source)
	}
}

func TestResolveNarratorFromTag(t *testing.T) {
	tag := &Metadata{Narrator: "Stephen Fry"}
	fm := &FolderMetadata{Narrator: "Folder Narrator"}
	narrator, source := resolveNarrator(tag, fm)
	if narrator != "Stephen Fry" || source != "tag.Narrator" {
		t.Errorf("got narrator=%q source=%q", narrator, source)
	}
}

func TestResolveNarratorFromFolder(t *testing.T) {
	fm := &FolderMetadata{Narrator: "Folder Narrator"}
	narrator, source := resolveNarrator(nil, fm)
	if narrator != "Folder Narrator" || source != "folder.Narrator" {
		t.Errorf("got narrator=%q source=%q", narrator, source)
	}
}

func TestResolveNarratorFromComment(t *testing.T) {
	tag := &Metadata{Comments: "Some info. Narrated by: John Smith, more stuff"}
	narrator, source := resolveNarrator(tag, &FolderMetadata{})
	if narrator != "John Smith" || source != "tag.Comment" {
		t.Errorf("got narrator=%q source=%q", narrator, source)
	}
}

func TestResolveNarratorNone(t *testing.T) {
	narrator, source := resolveNarrator(nil, &FolderMetadata{})
	if narrator != "" || source != "unknown" {
		t.Errorf("got narrator=%q source=%q", narrator, source)
	}
}

func TestExtractNarratorFromComment(t *testing.T) {
	tests := []struct {
		comment string
		want    string
	}{
		{"Narrator: Jane Doe", "Jane Doe"},
		{"Read by: Bob Smith, chapter 1", "Bob Smith"},
		{"Narrated by: Alice\nMore info", "Alice"},
		{"Reader: Tom Jones; extra", "Tom Jones"},
		{"No narrator info here", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := extractNarratorFromComment(tc.comment)
		if got != tc.want {
			t.Errorf("extractNarratorFromComment(%q) = %q, want %q", tc.comment, got, tc.want)
		}
	}
}

func TestIsGenericTitle(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"Part 1", true},
		{"Chapter 2", true},
		{"Track 03", true},
		{"Disc 1", true},
		{"Disk 2", true},
		{"The Great Gatsby", false},
		{"My Audiobook", false},
	}
	for _, tc := range tests {
		got := isGenericTitle(tc.title)
		if got != tc.want {
			t.Errorf("isGenericTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestFindFirstAudioFile(t *testing.T) {
	dir := t.TempDir()
	// Create files in non-alphabetical order
	for _, name := range []string{"chapter02.mp3", "chapter01.mp3", "cover.jpg", "chapter03.m4b"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fake"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got := FindFirstAudioFile(dir, []string{".mp3", ".m4b", ".m4a"})
	want := filepath.Join(dir, "chapter01.mp3")
	if got != want {
		t.Errorf("FindFirstAudioFile = %q, want %q", got, want)
	}
}

func TestFindFirstAudioFileNoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0644)
	got := FindFirstAudioFile(dir, []string{".mp3", ".m4b"})
	if got != "" {
		t.Errorf("FindFirstAudioFile = %q, want empty", got)
	}
}

func TestFindFirstAudioFileEmptyDir(t *testing.T) {
	dir := t.TempDir()
	got := FindFirstAudioFile(dir, []string{".mp3"})
	if got != "" {
		t.Errorf("FindFirstAudioFile = %q, want empty", got)
	}
}

func TestFindFirstAudioFileBadDir(t *testing.T) {
	got := FindFirstAudioFile("/nonexistent/path/12345", []string{".mp3"})
	if got != "" {
		t.Errorf("FindFirstAudioFile = %q, want empty", got)
	}
}

func TestAssembleBookMetadataIntegration(t *testing.T) {
	// Create a temp directory structure: Author/Book/file.mp3
	base := t.TempDir()
	bookDir := filepath.Join(base, "Terry Pratchett", "The Colour of Magic")
	if err := os.MkdirAll(bookDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create a fake mp3 file (tag extraction will fail, falling back to folder)
	fakeFile := filepath.Join(bookDir, "chapter01.mp3")
	if err := os.WriteFile(fakeFile, []byte("not a real mp3"), 0644); err != nil {
		t.Fatal(err)
	}

	// Use a mock extractor to simulate real tag data
	SetMetadataExtractor(newAssembleExtractorStub(t, Metadata{
		Title:  "The Colour of Magic",
		Artist: "Terry Pratchett",
		Year:   1983,
		Genre:  "Fantasy",
	}, nil))
	defer func() { SetMetadataExtractor(nil) }()

	bm, err := AssembleBookMetadata(bookDir, fakeFile, 3, 12345.0)
	if err != nil {
		t.Fatalf("AssembleBookMetadata error: %v", err)
	}

	if bm.FileCount != 3 {
		t.Errorf("FileCount = %d, want 3", bm.FileCount)
	}
	if bm.TotalDuration != 12345.0 {
		t.Errorf("TotalDuration = %f, want 12345.0", bm.TotalDuration)
	}
	if bm.Title != "The Colour of Magic" {
		t.Errorf("Title = %q, want 'The Colour of Magic'", bm.Title)
	}
	if bm.TitleSource != "tag.Title" {
		t.Errorf("TitleSource = %q, want 'tag.Title'", bm.TitleSource)
	}
	if len(bm.Authors) == 0 || bm.Authors[0] != "Terry Pratchett" {
		t.Errorf("Authors = %v, want [Terry Pratchett]", bm.Authors)
	}
	if bm.Year != 1983 {
		t.Errorf("Year = %d, want 1983", bm.Year)
	}
	if bm.Genre != "Fantasy" {
		t.Errorf("Genre = %q, want 'Fantasy'", bm.Genre)
	}
}

func TestAssembleBookMetadataNoFile(t *testing.T) {
	base := t.TempDir()
	bookDir := filepath.Join(base, "Some Author", "Some Book")
	os.MkdirAll(bookDir, 0755)

	bm, err := AssembleBookMetadata(bookDir, "", 0, 0)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Should still get folder-based metadata
	if bm.Title != "Some Book" {
		t.Errorf("Title = %q, want 'Some Book'", bm.Title)
	}
}
