// file: internal/organizer/path_builder_characterization_test.go
// version: 1.1.2
// guid: 5d8e2a41-9b73-4c06-8f15-6e39a2c7b048
// last-edited: 2026-09-02

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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
		Publisher: new("Pub"),
		Language:  new("eng"),
		Edition:   new("Unabridged"),
		Codec:     new("aac"),
		Quality:   new("high"),
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

// TestChar_DirectoryOrganizeAgreesWithApply is the THIRD leg of the conformance
// test, and the one that would have caught F8.
//
// TestChar_SchemesAgree compares generateTargetPath to ComputeTargetPaths --
// both single-file. OrganizeBookDirectory was a third computation of the same
// question for MULTI-file books, and nothing compared it to either: it expanded
// the folder pattern and then kept filepath.Base(src) forever. So a directory
// book organized to one set of names and the apply path planned another, which
// is the ping-pong with an extra door.
//
// This runs a real multi-file book through OrganizeBookDirectory on disk and
// asserts every file landed exactly where ComputeTargetPaths would have put it.
//
// SCOPE, so nobody over-trusts it: both legs are handed the SAME rows here, so
// this proves planTargetPaths is deterministic and that organize actually writes
// where the plan says. It does NOT prove the three production callers pass the
// same row set -- that divergence is prevented inside planTargetPaths (it
// normalizes the rows itself) rather than asserted here, which is why the
// zero-track case below deliberately includes a pathless row.
func TestChar_DirectoryOrganizeAgreesWithApply(t *testing.T) {
	// The middle case is the pattern that was actually configured in production
	// on 2026-08-15. It has no {track} placeholder at all, so every file of the
	// book computes the same name and the collision guard has to re-plan with a
	// numbered suffix. That guard is the highest-stakes code in this change --
	// without it a 40-part book collapses into one file -- so it gets asserted
	// through BOTH legs, not just unit-tested on the planner.
	cases := []struct {
		name     string
		filePat  string
		files    func(srcDir string) []database.BookFile
		wantPlan int
	}{
		{
			name:    "track placeholder, tracks numbered",
			filePat: "{title} - {track:02d}",
			files: func(d string) []database.BookFile {
				// Deliberately junk source names in non-alphabetical track
				// order: if the track number came from sort position rather
				// than the row, the two paths would silently disagree here.
				return []database.BookFile{
					{ID: "s3", FilePath: filepath.Join(d, "aaa.m4b"), Format: "m4b", TrackNumber: 3},
					{ID: "s1", FilePath: filepath.Join(d, "zzz.m4b"), Format: "m4b", TrackNumber: 1},
					{ID: "s2", FilePath: filepath.Join(d, "mmm.m4b"), Format: "m4b", TrackNumber: 2},
				}
			},
			wantPlan: 3,
		},
		{
			name:    "production track-less pattern, through the collision guard",
			filePat: "{title} - {author} - read by {narrator}",
			files: func(d string) []database.BookFile {
				return []database.BookFile{
					{ID: "s1", FilePath: filepath.Join(d, "part1.m4b"), Format: "m4b", TrackNumber: 1},
					{ID: "s2", FilePath: filepath.Join(d, "part2.m4b"), Format: "m4b", TrackNumber: 2},
					{ID: "s3", FilePath: filepath.Join(d, "part3.m4b"), Format: "m4b", TrackNumber: 3},
				}
			},
			wantPlan: 3,
		},
		{
			name:    "no track numbers at all, plus a pathless row",
			filePat: "{title} - {track:02d}",
			files: func(d string) []database.BookFile {
				// TrackNumber 0 everywhere means numbering falls out of sort
				// position over the row slice -- the shape where a row-set
				// mismatch between callers bites hardest. The pathless row is
				// the mismatch: "" sorts first, so if planTargetPaths kept it,
				// every track number would shift by one.
				return []database.BookFile{
					{ID: "s2", FilePath: filepath.Join(d, "b-second.m4b"), Format: "m4b"},
					{ID: "ghost", FilePath: "", Format: "m4b"},
					{ID: "s1", FilePath: filepath.Join(d, "a-first.m4b"), Format: "m4b"},
				}
			},
			wantPlan: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rootDir := t.TempDir()
			srcDir := t.TempDir()

			src := tc.files(srcDir)
			for _, f := range src {
				if f.FilePath == "" {
					continue
				}
				if err := os.WriteFile(f.FilePath, []byte("audio "+f.ID), 0644); err != nil {
					t.Fatalf("fixture: %v", err)
				}
			}

			year := 1951
			narrator := "Scott Brick"
			book := &database.Book{
				Title:     "Foundation",
				FilePath:  srcDir,
				Author:    &database.Author{Name: "Isaac Asimov"},
				Series:    &database.Series{Name: "Foundation"},
				Narrator:  &narrator,
				PrintYear: &year,
			}

			org := charOrganizer("{author}/{series}/{title} ({print_year})", tc.filePat)
			org.config.RootDir = rootDir
			org.config.OrganizationStrategy = "copy"

			// What the metadata-apply path plans.
			entries, err := org.ComputeTargetPaths(book, src)
			if err != nil {
				t.Fatalf("apply path: %v", err)
			}
			planned := make(map[string]string, len(entries))
			for _, e := range entries {
				planned[e.SourcePath] = e.TargetPath
			}
			if len(planned) != tc.wantPlan {
				t.Fatalf("apply path planned %d renames, want %d: %v", len(planned), tc.wantPlan, planned)
			}

			// Distinct targets, or organize would overwrite its own output.
			targets := make(map[string]string, len(planned))
			for srcPath, dst := range planned {
				if prev, dup := targets[dst]; dup {
					t.Fatalf("plan gives %q and %q the same target %q", prev, srcPath, dst)
				}
				targets[dst] = srcPath
			}

			// What organize actually does on disk.
			_, pathMap, err := organizeDirTriple(org, book, src)
			if err != nil {
				t.Fatalf("OrganizeBookDirectory: %v", err)
			}

			if !reflect.DeepEqual(planned, pathMap) {
				t.Errorf("directory organize and metadata apply disagree — the ping-pong, via OrganizeBookDirectory:\n"+
					"  organize : %v\n"+
					"  apply    : %v", pathMap, planned)
			}
			for _, dst := range planned {
				if _, statErr := os.Stat(dst); statErr != nil {
					t.Errorf("organize reported %q but nothing is there: %v", dst, statErr)
				}
			}
		})
	}
}

// TestChar_PlanIgnoresPathlessRows is the assertion the conformance test above
// structurally cannot make. That test hands both legs the same rows, so it
// proves the planner is deterministic, not that the three production callers
// agree on WHICH rows to hand it -- and they did not: OrganizeDirectoryBook
// pre-filtered rows with an empty FilePath while CreateOrganizedVersion and the
// metafetch apply paths passed GetBookFiles straight through.
//
// One pathless row is enough to break it. It changes totalTracks, and since ""
// sorts first it shifts every position-derived track number by one, so organize
// writes "... - 07.mp3" while the row writer plans "... - 08.mp3", finds nothing
// there, and falls back to the un-organized source. planTargetPaths therefore
// drops those rows itself; this pins that, by asserting the plan is unchanged by
// their presence.
func TestChar_PlanIgnoresPathlessRows(t *testing.T) {
	srcDir := t.TempDir()
	real1 := database.BookFile{ID: "s1", FilePath: filepath.Join(srcDir, "a.m4b"), Format: "m4b"}
	real2 := database.BookFile{ID: "s2", FilePath: filepath.Join(srcDir, "b.m4b"), Format: "m4b"}
	ghost := database.BookFile{ID: "ghost", Format: "m4b"}

	book := &database.Book{
		Title:    "Foundation",
		FilePath: srcDir,
		Author:   &database.Author{Name: "Isaac Asimov"},
	}
	org := charOrganizer("{author}/{title}", "{title} - {track:02d}")
	org.config.RootDir = t.TempDir()

	plan := func(files []database.BookFile) map[string]string {
		t.Helper()
		entries, err := org.ComputeTargetPaths(book, files)
		if err != nil {
			t.Fatalf("ComputeTargetPaths: %v", err)
		}
		out := make(map[string]string, len(entries))
		for _, e := range entries {
			out[e.SourcePath] = e.TargetPath
		}
		return out
	}

	want := plan([]database.BookFile{real1, real2})
	if len(want) != 2 {
		t.Fatalf("fixture planned %d renames, want 2: %v", len(want), want)
	}
	// The ghost goes first, which is also where "" sorts.
	got := plan([]database.BookFile{ghost, real1, real2})

	if !reflect.DeepEqual(want, got) {
		t.Errorf("a pathless row changed the plan — callers that filter and callers that don't will disagree:\n"+
			"  without ghost: %v\n"+
			"  with ghost   : %v", want, got)
	}
}

// TestChar_SanitizePathComponentIsIdempotent underwrites a load-bearing
// assumption: GenerateTargetDirPath and OrganizeBookDirectory run sanitizePath
// over output BuildPath has ALREADY sanitized, while BuildRelPath does not. The
// two agree on the folder only if the second pass is a no-op. If it ever stops
// being one, a directory book's folder drifts one character from the same
// book's single-file folder and the ping-pong is back.
func TestChar_SanitizePathComponentIsIdempotent(t *testing.T) {
	inputs := []string{
		"", "normal", "Dr. Who - Season 1", "../../../etc/passwd", "..evil",
		"with/slash", "back\\slash", "colon:name", "star*name", "q?mark",
		"quote\"name", "<angle>", "pipe|name", "trail...", "  padded  ",
		"[brackets]", "emoji 🎧 name", "ctrl\x01char", "多字节标题",
	}
	for _, in := range inputs {
		once := SanitizePathComponent(in)
		twice := SanitizePathComponent(once)
		if once != twice {
			t.Errorf("SanitizePathComponent is not idempotent for %q:\n  once : %q\n  twice: %q", in, once, twice)
		}
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
			ID: "f" + string(rune('a'+i-1)),
			// Zero-padded to 2 digits so all 12 paths are distinct. The prior
			// "ch"+string(rune('0'+i%10))+".mp3" form wrapped mod 10, so i=1
			// and i=11 both produced "ch1.mp3" and i=2/i=12 both produced
			// "ch2.mp3" -- an accidental duplicate-FilePath fixture that
			// planTargetPaths's new duplicate-row collapse (DUPROW-1) now
			// collapses to 10 entries, which is a correct response to a
			// fixture bug, not a regression. See TASK-223.
			FilePath:    fmt.Sprintf("/src/foundation/ch%02d.mp3", i),
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
