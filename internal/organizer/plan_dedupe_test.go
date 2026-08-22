// file: internal/organizer/plan_dedupe_test.go
// version: 1.0.0
// guid: 7a3a046f-53d2-4b05-9d4a-628af64326c9
// last-edited: 2026-08-22

// Tests for the duplicate-path collapse added to planTargetPaths (DUPROW-1).
//
// Production incident, 2026-08-21: a 21-file book had 42 book_file rows (two
// rows per file). planTargetPaths counted totalTracks as the raw row count
// (42), tripped the "file naming pattern does not distinguish files" collision
// branch, and planned two FileRenameEntry values per file — both with the same
// SourcePath. RenameFiles moved the file on the first entry and failed
// "stat rename source ...: no such file or directory" on the second, racing
// the rename phase against itself.
//
// planTargetPaths now collapses rows whose filepath.Clean'd, trimmed FilePath
// is identical, keeping the first such row in the caller's original order
// (before the TrackNumber/FilePath sort — see the ordering comment at the
// dedupe site in pipeline.go). These tests pin: the duplicate collapse itself,
// that no SourcePath is ever planned twice, that non-duplicate input is left
// alone (anti-over-suppression), and that the Missing-row totalTracks rule
// documented at pipeline.go:124-129 survives the change.
package organizer

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// dupeTestVars/dupeTestOpts are shared across the tests below; none of them
// depend on author/series fallback behaviour, only on track numbering.
var (
	dupeTestVars = PathVars{Title: "Book", Author: "Author"}
	dupeTestOpts = BuildOpts{}
)

// buildDuplicateRows returns 2*n rows over n distinct paths (each path
// appears exactly twice, adjacent in the slice), every row's TrackNumber left
// at 0 so planPass falls back to position-derived numbering. Paths are
// zero-padded so their string sort order matches their numeric order, which
// is what lets the expected suffix be computed from the loop index.
func buildDuplicateRows(t *testing.T, n int) []database.BookFile {
	t.Helper()
	srcDir := t.TempDir()
	files := make([]database.BookFile, 0, n*2)
	for i := 1; i <= n; i++ {
		path := filepath.Join(srcDir, fmt.Sprintf("file%02d.mp3", i))
		files = append(files,
			database.BookFile{ID: fmt.Sprintf("row-%d-a", i), FilePath: path, Format: "mp3"},
			database.BookFile{ID: fmt.Sprintf("row-%d-b", i), FilePath: path, Format: "mp3"},
		)
	}
	return files
}

// trackSuffix extracts the "NN" from a target path stem ending "... - NN.ext",
// which is what the collision branch appends when the file naming pattern has
// no {track} placeholder of its own.
func trackSuffix(t *testing.T, targetPath string) string {
	t.Helper()
	base := filepath.Base(targetPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	idx := strings.LastIndex(base, " - ")
	if idx == -1 {
		t.Fatalf("target path %q has no ' - NN' suffix", targetPath)
	}
	return base[idx+len(" - "):]
}

// TestPlanTargetPaths_DuplicateRowsCollapseToDistinctFiles pins the core fix:
// 42 rows over 21 distinct paths plan 21 entries, and totalTracks — which
// drives the position-derived numbering below — is 21, not 42. The file
// pattern deliberately has NO {track} placeholder (the live prod pattern
// shape on 2026-08-15), so the collision branch fires and appends the
// suffix that exposes totalTracks directly.
func TestPlanTargetPaths_DuplicateRowsCollapseToDistinctFiles(t *testing.T) {
	rootDir := t.TempDir()
	files := buildDuplicateRows(t, 21)

	entries, err := planTargetPaths(rootDir, "{author}", "{title} - {author}", files, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("planTargetPaths: %v", err)
	}
	if len(entries) != 21 {
		t.Fatalf("got %d entries, want 21 (42 rows over 21 distinct paths): %+v", len(entries), entries)
	}

	gotSuffixes := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		suffix := trackSuffix(t, e.TargetPath)
		gotSuffixes[suffix] = struct{}{}
		if n, convErr := strconv.Atoi(suffix); convErr != nil || n < 1 || n > 21 {
			t.Errorf("target %q has suffix %q, want a number in 01..21 (a 42 count would produce up to 42)", e.TargetPath, suffix)
		}
	}
	for i := 1; i <= 21; i++ {
		want := fmt.Sprintf("%02d", i)
		if _, ok := gotSuffixes[want]; !ok {
			t.Errorf("missing expected track suffix %q among %v", want, gotSuffixes)
		}
	}
	if _, ok := gotSuffixes["42"]; ok {
		t.Error("found suffix \"42\" — totalTracks was computed from the raw 42-row count, not the 21 distinct paths")
	}
}

// TestPlanTargetPaths_NoDuplicateSourcePathIsPlannedTwice is the direct
// assertion for the prod ENOENT: RenameFiles moved the file on the first
// entry sharing a SourcePath and failed stat on the second. No SourcePath may
// appear in the plan more than once.
func TestPlanTargetPaths_NoDuplicateSourcePathIsPlannedTwice(t *testing.T) {
	rootDir := t.TempDir()
	files := buildDuplicateRows(t, 21)

	entries, err := planTargetPaths(rootDir, "{author}", "{title} - {author}", files, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("planTargetPaths: %v", err)
	}

	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		counts[e.SourcePath]++
	}
	for src, n := range counts {
		if n != 1 {
			t.Errorf("SourcePath %q planned %d times, want exactly 1 (this is the exact shape of the 2026-08-21 rename race)", src, n)
		}
	}
}

// TestPlanTargetPaths_DistinctRowsAreAllPlanned is the anti-over-suppression
// guard: 21 rows with 21 DISTINCT paths and no duplicates must all still be
// planned. Without this, a change that (incorrectly) returned only the first
// row of the whole set would pass the two tests above.
func TestPlanTargetPaths_DistinctRowsAreAllPlanned(t *testing.T) {
	rootDir := t.TempDir()
	srcDir := t.TempDir()
	files := make([]database.BookFile, 0, 21)
	want := make(map[string]struct{}, 21)
	for i := 1; i <= 21; i++ {
		path := filepath.Join(srcDir, fmt.Sprintf("distinct%02d.mp3", i))
		files = append(files, database.BookFile{ID: fmt.Sprintf("row-%d", i), FilePath: path, Format: "mp3"})
		want[path] = struct{}{}
	}

	entries, err := planTargetPaths(rootDir, "{author}", "{title} - {author}", files, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("planTargetPaths: %v", err)
	}
	if len(entries) != 21 {
		t.Fatalf("got %d entries, want 21 (no duplicates in this fixture)", len(entries))
	}
	for _, e := range entries {
		if _, ok := want[e.SourcePath]; !ok {
			t.Errorf("unexpected SourcePath %q not in input fixture", e.SourcePath)
		}
		delete(want, e.SourcePath)
	}
	if len(want) != 0 {
		t.Errorf("%d input paths never appeared as a SourcePath: %v", len(want), want)
	}
}

// TestPlanTargetPaths_MissingRowsStillCountTowardTotalTracks proves the
// documented Missing-counting rule (pipeline.go:124-129) survives the
// duplicate-path collapse: totalTracks still counts every distinct row,
// including ones flagged Missing, so a partially-missing book keeps its
// original track numbers rather than renumbering around the gaps. The file
// pattern here uses an explicit {track:02d} placeholder so the number is
// visible without depending on the collision-suffix branch.
func TestPlanTargetPaths_MissingRowsStillCountTowardTotalTracks(t *testing.T) {
	rootDir := t.TempDir()
	srcDir := t.TempDir()
	files := make([]database.BookFile, 0, 12)
	for i := 1; i <= 12; i++ {
		files = append(files, database.BookFile{
			ID:          fmt.Sprintf("row-%d", i),
			FilePath:    filepath.Join(srcDir, fmt.Sprintf("track%02d.mp3", i)),
			Format:      "mp3",
			TrackNumber: i,
			Missing:     i != 7,
		})
	}

	entries, err := planTargetPaths(rootDir, "{author}", "{title} - {track:02d}", files, dupeTestVars, dupeTestOpts)
	if err != nil {
		t.Fatalf("planTargetPaths: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (11 of 12 rows are Missing): %+v", len(entries), entries)
	}
	if !strings.Contains(filepath.Base(entries[0].TargetPath), " - 07") {
		t.Errorf("surviving entry target %q does not carry track 07 — totalTracks must stay 12 (raw row count) even though only 1 row survives Missing-filtering", entries[0].TargetPath)
	}
}
