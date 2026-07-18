// file: internal/scanner/chapter_consolidation_test.go
// version: 1.0.0
// guid: bd65bfea-ee3b-4062-a677-dc057ec04749
// last-edited: 2026-07-18

package scanner

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestConsolidateChapterGroups_AllUnreadable_SkipsConsolidation is R-8's
// regression test. A chapter-named group (>= 3 files, numeric prefix) whose
// files are all unreadable by mediainfo (nonexistent paths here, so
// mediainfo.Extract's os.Open fails deterministically for every one) has
// duration UNKNOWN for the whole group, not "short". Before the fix, the
// group's total/average duration were computed from all-zero readings,
// which always looked like "average 0 < threshold" and got silently
// consolidated into one merged Book. The fix must instead skip
// consolidation entirely for this group: every file stays its own Book with
// zero Duration and no SegmentFiles.
func TestConsolidateChapterGroups_AllUnreadable_SkipsConsolidation(t *testing.T) {
	prevThreshold := config.AppConfig.ChapterConsolidationThresholdMin
	config.AppConfig.ChapterConsolidationThresholdMin = 10
	t.Cleanup(func() { config.AppConfig.ChapterConsolidationThresholdMin = prevThreshold })

	files := []string{
		"/nonexistent/01 - My Book.mp3",
		"/nonexistent/02 - My Book.mp3",
		"/nonexistent/03 - My Book.mp3",
	}

	books := consolidateChapterGroups(context.Background(), files)

	if len(books) != len(files) {
		t.Fatalf("expected %d individual books (consolidation must be skipped), got %d: %+v",
			len(files), len(books), books)
	}
	for _, b := range books {
		if len(b.SegmentFiles) != 0 {
			t.Errorf("book %q was consolidated (SegmentFiles=%v) despite all-unreadable durations in its group",
				b.FilePath, b.SegmentFiles)
		}
		if b.Duration != 0 {
			t.Errorf("book %q has non-zero Duration %d despite every file in its group being unreadable",
				b.FilePath, b.Duration)
		}
	}
}

// TestConsolidateChapterGroups_StandaloneFile_Unaffected is a narrow guard
// against an overcorrection: a single file with no numeric prefix (never
// grouped as a chapter candidate at all) must still pass through unchanged,
// confirming the R-8 guard only fires for actual chapter-sequence groups.
func TestConsolidateChapterGroups_StandaloneFile_Unaffected(t *testing.T) {
	prevThreshold := config.AppConfig.ChapterConsolidationThresholdMin
	config.AppConfig.ChapterConsolidationThresholdMin = 10
	t.Cleanup(func() { config.AppConfig.ChapterConsolidationThresholdMin = prevThreshold })

	files := []string{
		"/nonexistent/solo-file.mp3", // no numeric prefix → standalone, never grouped
	}

	books := consolidateChapterGroups(context.Background(), files)
	if len(books) != 1 {
		t.Fatalf("expected 1 standalone book, got %d: %+v", len(books), books)
	}
	if books[0].FilePath != files[0] {
		t.Errorf("expected standalone file path %q, got %q", files[0], books[0].FilePath)
	}
}
