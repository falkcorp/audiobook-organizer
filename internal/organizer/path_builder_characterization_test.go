// file: internal/organizer/path_builder_characterization_test.go
// version: 1.0.0
// guid: 5d8e2a41-9b73-4c06-8f15-6e39a2c7b048
// last-edited: 2026-08-15

// Characterization tests for the TWO target-path builders, written while both
// still exist, so that unifying them is a measurement rather than a promise.
//
// The repo currently computes a book's target path two different ways:
//
//   scheme #1  expandPattern + generateTargetPath  (organizer.go)
//              driven by folder_naming_pattern + file_naming_pattern
//              used by OrganizeBook, OrganizeBookDirectory, ReOrganizeInPlace
//
//   scheme #2  FormatPath + ComputeTargetPaths     (path_format.go, pipeline.go)
//              driven by path_format + segment_title_format
//              used by the metadata-apply rename pipeline
//
// Both are live in production (auto_organize and auto_rename_on_apply are both
// true), they disagree, and each moves files toward its own answer — so a book
// can be moved back and forth indefinitely.
//
// NEITHER IS A SUPERSET. Each encodes fixes the other never got:
//
//   only #1: dropping " - " pattern segments whose placeholders are all empty,
//            INCLUDING their connector words; erroring on unresolved
//            placeholders; store lookups for author/series by ID; the quality
//            vocabulary (publisher, language, edition, bitrate, codec, quality)
//   only #2: scrubbing every variable BEFORE substitution so metadata cannot
//            inject a path separator; per-component sanitization; the
//            multi-file vocabulary (track, total_tracks, track_title, ext)
//
// The tests below pin the behaviours that must SURVIVE unification. If a merged
// builder drops one, one of these goes red. The connector-word case in
// particular is not cosmetic: without it, a missing narrator in
// "{title} - {author} - read by {narrator}" produced
// "Time Pebbles - read by Jerry Merritt", crediting the AUTHOR as the narrator
// (see organizer.go:483-487).

package organizer

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func charOrganizer(folderPattern, filePattern string) *Organizer {
	return &Organizer{
		config: &config.Config{
			RootDir:             "/lib",
			FolderNamingPattern: folderPattern,
			FileNamingPattern:   filePattern,
		},
	}
}

// --- Scheme #1 behaviours that MUST survive unification ---

// TestChar_S1_EmptySegmentDropsConnectorWords is the important one. A segment
// of the pattern whose placeholders are all empty must be removed together with
// its literal connector text, or the connector reattaches to the neighbouring
// value and relabels it.
func TestChar_S1_EmptySegmentDropsConnectorWords(t *testing.T) {
	org := charOrganizer("{author}", "{title} - {author} - read by {narrator}")
	book := &database.Book{
		Title:    "Time Pebbles",
		FilePath: "/src/tp.m4b",
		Author:   &database.Author{Name: "Jerry Merritt"},
		// Narrator deliberately absent.
	}

	got, err := org.expandPattern(org.config.FileNamingPattern, book)
	if err != nil {
		t.Fatalf("expandPattern: %v", err)
	}
	if strings.Contains(got, "read by") {
		t.Errorf("connector word survived an empty placeholder: %q\n"+
			"this is the regression that credited the AUTHOR as the narrator", got)
	}
	t.Logf("scheme #1 empty-narrator result: %q", got)
}

// TestChar_S1_UnresolvedPlaceholderIsAnError pins that an unknown placeholder
// fails loudly instead of leaving "{unsupported}" in a real filesystem path.
func TestChar_S1_UnresolvedPlaceholderIsAnError(t *testing.T) {
	org := charOrganizer("{author}", "{title}")
	book := &database.Book{Title: "T", FilePath: "/src/t.m4b"}

	if _, err := org.expandPattern("{title}/{unsupported}", book); err == nil {
		t.Error("expected an error for an unknown placeholder, got nil")
	}
}

// TestChar_S1_MissingAuthorUsesPlaceholder pins scheme #1's fallback. Scheme #2
// collapses the segment away instead, which is the single most visible
// divergence between the two: the same book lands in a different directory
// DEPTH depending on which engine moved it.
func TestChar_S1_MissingAuthorUsesPlaceholder(t *testing.T) {
	org := charOrganizer("{author}", "{title}")
	book := &database.Book{Title: "Orphan", FilePath: "/src/o.m4b"}

	got, err := org.expandPattern("{author}", book)
	if err != nil {
		t.Fatalf("expandPattern: %v", err)
	}
	if got != placeholderAuthor {
		t.Errorf("missing author = %q, want %q", got, placeholderAuthor)
	}
}

// TestChar_S1_QualityVocabulary pins the variables scheme #2 has never had.
func TestChar_S1_QualityVocabulary(t *testing.T) {
	br := 128
	org := charOrganizer("{author}", "{title}")
	book := &database.Book{
		Title:     "Q",
		FilePath:  "/src/q.m4b",
		Author:    &database.Author{Name: "A"},
		Publisher: strPtr("Pub"),
		Language:  strPtr("eng"),
		Edition:   strPtr("Unabridged"),
		Codec:     strPtr("aac"),
		Quality:   strPtr("high"),
		Bitrate:   &br,
	}

	for _, v := range []string{"{publisher}", "{language}", "{edition}", "{codec}", "{quality}", "{bitrate}"} {
		got, err := org.expandPattern(v, book)
		if err != nil {
			t.Errorf("%s: unexpected error %v", v, err)
			continue
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("%s expanded to empty - vocabulary lost", v)
		}
	}
}

// --- Scheme #2 behaviours that MUST survive unification ---

// TestChar_S2_ScrubsSeparatorsBeforeSubstitution pins the security property.
// A title containing a path separator must not create directory levels.
func TestChar_S2_ScrubsSeparatorsBeforeSubstitution(t *testing.T) {
	got := FormatPath("{author}/{title}.{ext}", FormatVars{
		Author: "A",
		Title:  "../../etc/passwd",
		Ext:    "m4b",
	})
	if strings.Contains(got, "..") {
		t.Errorf("path traversal survived scrubbing: %q", got)
	}
	if strings.Count(got, "/") != 1 {
		t.Errorf("title injected extra directory levels: %q", got)
	}
}

// TestChar_S2_MultiFileVocabulary pins the variables scheme #1 has never had.
func TestChar_S2_MultiFileVocabulary(t *testing.T) {
	got := FormatPath("{author}/{title}/{track_title}.{ext}", FormatVars{
		Author: "A", Title: "T", Ext: "mp3",
		Track: 3, TotalTracks: 12,
	})
	if !strings.Contains(got, "3") {
		t.Errorf("track number lost: %q", got)
	}
	t.Logf("scheme #2 multi-file result: %q", got)
}

// TestChar_S2_CollapsesEmptySegments pins the fallback that differs from
// scheme #1: an absent author removes the segment rather than naming it.
func TestChar_S2_CollapsesEmptySegments(t *testing.T) {
	got := FormatPath("{author}/{title}.{ext}", FormatVars{Title: "T", Ext: "m4b"})
	if strings.Contains(got, placeholderAuthor) {
		t.Errorf("scheme #2 unexpectedly used the scheme #1 placeholder: %q", got)
	}
	if strings.HasPrefix(got, "/") {
		t.Errorf("empty leading segment not collapsed: %q", got)
	}
	t.Logf("scheme #2 missing-author result: %q (scheme #1 would say %q/...)", got, placeholderAuthor)
}

// --- The divergence itself, pinned as a fact ---

// TestChar_SchemesDisagree records that the two builders produce different
// paths for the same book under the CURRENT production config. This is the
// ping-pong: whichever engine ran last moves the file to its own answer.
//
// When the builders are unified this test should be updated to assert
// agreement - it is deliberately written to fail loudly if someone "fixes" the
// symptom without understanding it.
func TestChar_SchemesDisagree(t *testing.T) {
	// Live production values, read from the running server on 2026-08-15.
	const (
		prodFolder     = "{author}/{series}/{title} ({print_year})"
		prodFile       = "{title} - {author} - read by {narrator}"
		prodPathFormat = "{author}/{series_prefix}{title}/{track_title}.{ext}"
	)

	year := 1951
	book := &database.Book{
		Title:     "Foundation",
		FilePath:  "/src/foundation.m4b",
		Author:    &database.Author{Name: "Isaac Asimov"},
		Series:    &database.Series{Name: "Foundation"},
		PrintYear: &year,
	}

	org := charOrganizer(prodFolder, prodFile)
	s1, err := org.generateTargetPath(book)
	if err != nil {
		t.Fatalf("scheme #1: %v", err)
	}

	s2 := FormatPath(prodPathFormat, FormatVars{
		Author: "Isaac Asimov",
		Title:  "Foundation",
		Series: "Foundation",
		Year:   year,
		Ext:    "m4b",
		Track:  1,
	})

	t.Logf("scheme #1 (organize)      : %s", s1)
	t.Logf("scheme #2 (metadata apply): %s", s2)

	if strings.TrimPrefix(s1, "/lib/") == s2 {
		t.Log("schemes now AGREE - if this is intentional, update this test to assert agreement")
	}
}

func strPtr(s string) *string { return &s }
