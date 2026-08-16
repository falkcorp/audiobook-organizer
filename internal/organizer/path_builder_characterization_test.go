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
//
// These were written against FormatPath. FormatPath is deleted, so they now run
// against BuildPath — which is the point: the behaviours had to survive the
// builder that carried them.

// TestChar_S2_ScrubsSeparatorsBeforeSubstitution pins the security property.
// A title containing a path separator must not create directory levels.
//
// This is the behaviour internal/metafetch's hand-copied twin NEVER had. Real
// production data — a title of "Tarkin - Star Wars - 3/85" — split into a
// directory plus a file, and the next scan read 85 single-file directories as
// 85 separate Book records.
func TestChar_S2_ScrubsSeparatorsBeforeSubstitution(t *testing.T) {
	got, err := BuildPath("{author}/{title}", PathVars{
		Author: "A",
		Title:  "../../etc/passwd",
		Ext:    "m4b",
	}, BuildOpts{})
	if err != nil {
		t.Fatalf("BuildPath: %v", err)
	}
	if strings.Contains(got, "..") {
		t.Errorf("path traversal survived scrubbing: %q", got)
	}
	if strings.Count(got, "/") != 1 {
		t.Errorf("title injected extra directory levels: %q", got)
	}
}

// TestChar_S2_MultiFileVocabulary pins the variables scheme #1 has never had.
func TestChar_S2_MultiFileVocabulary(t *testing.T) {
	got, err := BuildPath("{author}/{title}/{track_title}", PathVars{
		Author: "A", Title: "T", Ext: "mp3",
		Track: 3, TotalTracks: 12,
	}, BuildOpts{})
	if err != nil {
		t.Fatalf("BuildPath: %v", err)
	}
	if !strings.Contains(got, "3") {
		t.Errorf("track number lost: %q", got)
	}
	t.Logf("multi-file result: %q", got)
}

// TestChar_S2_CollapsesEmptySegments pins that the collapsing fallback is still
// REACHABLE. It is no longer a property of a rival builder — it is what
// BuildOpts.AuthorFallback == "" selects, and the organize path opts out of it
// by setting placeholderAuthor. Keeping both reachable from one builder is the
// whole design: the two former schemes differed on this and each was right for
// its caller.
func TestChar_S2_CollapsesEmptySegments(t *testing.T) {
	got, err := BuildPath("{author}/{title}", PathVars{Title: "T", Ext: "m4b"}, BuildOpts{})
	if err != nil {
		t.Fatalf("BuildPath: %v", err)
	}
	if strings.Contains(got, placeholderAuthor) {
		t.Errorf("empty AuthorFallback unexpectedly produced the placeholder: %q", got)
	}
	if strings.HasPrefix(got, "/") {
		t.Errorf("empty leading segment not collapsed: %q", got)
	}
}

// --- The divergence, now pinned as AGREEMENT ---

// TestChar_SchemesAgree is the conformance test. It was written as
// TestChar_SchemesDisagree, recording that the organize path and the
// metadata-apply path produced different targets for the same book under the
// live production config — four directory levels against two. Since
// ReOrganizeInPlace is a true os.Rename, that made each engine drag the file
// back toward its own answer forever.
//
// It now asserts they are EQUAL, and it is the test that must never be deleted:
// two implementations of one question drift the moment nothing compares them.
func TestChar_SchemesAgree(t *testing.T) {
	// Live production values, read from the running server on 2026-08-15.
	const (
		prodFolder = "{author}/{series}/{title} ({print_year})"
		prodFile   = "{title} - {author} - read by {narrator}"
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

	organizePath, err := org.generateTargetPath(book)
	if err != nil {
		t.Fatalf("organize path: %v", err)
	}

	// The apply path, for the same book as a single-file entry.
	entries, err := org.ComputeTargetPaths(book, []database.BookFile{{
		ID:       "virtual-1",
		FilePath: "/src/foundation.m4b",
		Format:   "m4b",
	}})
	if err != nil {
		t.Fatalf("apply path: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("apply path planned %d renames, want exactly 1", len(entries))
	}
	applyPath := entries[0].TargetPath

	t.Logf("organize      : %s", organizePath)
	t.Logf("metadata apply: %s", applyPath)

	if organizePath != applyPath {
		t.Errorf("the two path builders disagree — this is the ping-pong:\n"+
			"  organize      : %s\n"+
			"  metadata apply: %s", organizePath, applyPath)
	}
}

// TestChar_AlreadyOrganizedBookPlansNoRename is the ping-pong stated as its
// observable symptom. A book already sitting at its organize-computed target
// must produce ZERO rename entries — if the apply path plans a move for a file
// organize just put there, the two are still fighting.
func TestChar_AlreadyOrganizedBookPlansNoRename(t *testing.T) {
	year := 1951
	book := &database.Book{
		Title:     "Foundation",
		FilePath:  "/src/foundation.m4b",
		Author:    &database.Author{Name: "Isaac Asimov"},
		Series:    &database.Series{Name: "Foundation"},
		PrintYear: &year,
	}

	org := charOrganizer("{author}/{series}/{title} ({print_year})", "{title} - {author}")

	target, err := org.generateTargetPath(book)
	if err != nil {
		t.Fatalf("organize path: %v", err)
	}

	// Now the file IS at that target — exactly the state organize leaves behind.
	entries, err := org.ComputeTargetPaths(book, []database.BookFile{{
		ID:       "f1",
		FilePath: target,
		Format:   "m4b",
	}})
	if err != nil {
		t.Fatalf("apply path: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("apply planned %d rename(s) for a book already at its organized path %q:\n  → %s",
			len(entries), target, entries[0].TargetPath)
	}
}

// TestChar_MultiFileBookNamesEveryFileDistinctly covers the requirement the
// unification exists to serve: "make sure it's file and folder aware so it
// updates all the rows correctly". Every file of a directory book must get its
// OWN target — if two collide, one file overwrites the other and a book_file
// row is stranded.
func TestChar_MultiFileBookNamesEveryFileDistinctly(t *testing.T) {
	book := &database.Book{
		Title:    "Foundation",
		FilePath: "/src/foundation",
		Author:   &database.Author{Name: "Isaac Asimov"},
	}

	org := charOrganizer("{author}/{title}", "{title} - {track:02d}")

	files := make([]database.BookFile, 0, 12)
	for i := 1; i <= 12; i++ {
		files = append(files, database.BookFile{
			ID:          "f" + string(rune('a'+i-1)),
			FilePath:    "/src/foundation/ch" + string(rune('0'+i%10)) + ".mp3",
			Format:      "mp3",
			TrackNumber: i,
		})
	}

	entries, err := org.ComputeTargetPaths(book, files)
	if err != nil {
		t.Fatalf("apply path: %v", err)
	}
	if len(entries) != 12 {
		t.Fatalf("planned %d renames for a 12-file book, want 12", len(entries))
	}

	seen := make(map[string]string, len(entries))
	for _, e := range entries {
		if prev, dup := seen[e.TargetPath]; dup {
			t.Errorf("two files share one target — one would overwrite the other:\n"+
				"  %s\n  %s\n  → %s", prev, e.SourcePath, e.TargetPath)
		}
		seen[e.TargetPath] = e.SourcePath
	}

	// Zero-padded so a file manager sorts them correctly.
	if !strings.HasSuffix(entries[0].TargetPath, "Foundation - 01.mp3") {
		t.Errorf("first track = %q, want a zero-padded \"Foundation - 01.mp3\"", entries[0].TargetPath)
	}
}

// TestChar_SingleFileBookDropsTheTrackSegment is the other half of the same
// requirement, and the reason the default file pattern can carry {track:02d} at
// all: a book with ONE file must not be named "Foundation - 01.m4b".
func TestChar_SingleFileBookDropsTheTrackSegment(t *testing.T) {
	book := &database.Book{
		Title:    "Foundation",
		FilePath: "/src/foundation.m4b",
		Author:   &database.Author{Name: "Isaac Asimov"},
	}

	org := charOrganizer("{author}", "{title} - {track:02d}")

	entries, err := org.ComputeTargetPaths(book, []database.BookFile{{
		ID: "f1", FilePath: "/src/foundation.m4b", Format: "m4b", TrackNumber: 1,
	}})
	if err != nil {
		t.Fatalf("apply path: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("planned %d renames, want 1", len(entries))
	}
	if want := "/lib/Isaac Asimov/Foundation.m4b"; entries[0].TargetPath != want {
		t.Errorf("single-file target = %q, want %q", entries[0].TargetPath, want)
	}
}

func strPtr(s string) *string { return &s }
