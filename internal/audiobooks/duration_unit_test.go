// file: internal/audiobooks/duration_unit_test.go
// version: 1.0.0
// guid: 4d9e2a71-6b58-4c03-9f14-8e7a05b3d62c
// last-edited: 2026-08-03

package audiobooks

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// 🔴 TestAggregateFileMetadata_DoesNotDivideCorrectSeconds is the library-list
// duration regression.
//
// aggregateFileMetadataWithFiles did `agg.totalDuration += f.Duration / 1000`,
// assuming BookFile.Duration is milliseconds. It is SECONDS by convention
// (database/duration_sanity.go); only ~2% of rows are milliseconds, from the
// iTunes importer.
//
// Two compounding effects, both reproduced here:
//   - correct values were divided by 1000;
//   - the division is INTEGER and applied PER ROW BEFORE summing, so every file
//     shorter than 1000 s contributed exactly 0.
//
// On production that showed Hyperion as 20 s against a stored 174,658 s, and
// left 25,938 of 44,886 books with an implausibly small listed duration.
func TestAggregateFileMetadata_DoesNotDivideCorrectSeconds(t *testing.T) {
	svc := &AudiobookService{}

	// A realistic multi-file book: 5 tracks, ~10 min each, all well under the
	// 1000 s that used to truncate to zero. ~64 kbps implied, plainly plausible.
	const track = 600      // 10 minutes
	const size = 4_800_000 // 4.8 MB -> 64 kbps at 600 s
	files := make([]database.BookFileCore, 0, 5)
	for i := 0; i < 5; i++ {
		files = append(files, database.BookFileCore{Duration: track, FileSize: size})
	}

	books := []database.Book{{ID: "b1"}}
	svc.aggregateFileMetadataWithFiles(books, map[string][]database.BookFileCore{"b1": files})

	if books[0].Duration == nil {
		t.Fatal("duration was not aggregated at all")
	}
	want := track * 5 // 3000 s
	if got := *books[0].Duration; got != want {
		t.Fatalf("aggregated duration = %d, want %d — correct seconds must not be divided (old code gave %d)",
			got, want, 0)
	}
}

// A genuine millisecond row must STILL be repaired, so the fix narrows the
// division rather than removing it.
func TestAggregateFileMetadata_StillNormalizesMillisecondRows(t *testing.T) {
	svc := &AudiobookService{}

	// 4.8 MB with a 600,000 "second" duration implies 64 bits/sec — far below
	// the 4 kbps floor — while /1000 lands back at a plausible 64 kbps. That is
	// exactly the millisecond signature DurationLooksLikeMillis detects.
	files := []database.BookFileCore{{Duration: 600_000, FileSize: 4_800_000}}

	books := []database.Book{{ID: "b1"}}
	svc.aggregateFileMetadataWithFiles(books, map[string][]database.BookFileCore{"b1": files})

	if books[0].Duration == nil {
		t.Fatal("duration was not aggregated at all")
	}
	if got := *books[0].Duration; got != 600 {
		t.Fatalf("aggregated duration = %d, want 600 — a real millisecond row must still be normalized", got)
	}
}

// Mixed units inside one book: each row is judged on its own evidence.
func TestAggregateFileMetadata_MixedUnitsInOneBook(t *testing.T) {
	svc := &AudiobookService{}

	files := []database.BookFileCore{
		{Duration: 600, FileSize: 4_800_000},     // seconds — keep
		{Duration: 600_000, FileSize: 4_800_000}, // milliseconds — normalize to 600
	}

	books := []database.Book{{ID: "b1"}}
	svc.aggregateFileMetadataWithFiles(books, map[string][]database.BookFileCore{"b1": files})

	if books[0].Duration == nil {
		t.Fatal("duration was not aggregated at all")
	}
	if got := *books[0].Duration; got != 1200 {
		t.Fatalf("aggregated duration = %d, want 1200 — each row must be judged on its own implied bitrate", got)
	}
}
