// file: internal/plugins/maintenance/filepath_collision_report_test.go
// version: 1.0.0
// guid: 989ac723-652b-498c-bd0f-55ce506ba859
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func runFilePathCollisionReport(t *testing.T, books []database.BookCore, params filePathCollisionReportParams) filePathCollisionReport {
	t.Helper()
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) { return books, nil },
	}
	rep, err := buildFilePathCollisionReport(context.Background(), store, params, &fakeReporter{})
	if err != nil {
		t.Fatalf("buildFilePathCollisionReport: %v", err)
	}
	return rep
}

// TestFilePathCollisionReport_DetectsSharedPath is the brief's required case:
// 3 books, 2 sharing an identical FilePath, 1 unique -- collisionGroups=1,
// affectedRows=2.
func TestFilePathCollisionReport_DetectsSharedPath(t *testing.T) {
	books := []database.BookCore{
		{ID: "b1", FilePath: "/books/shared.m4b"},
		{ID: "b2", FilePath: "/books/shared.m4b"},
		{ID: "b3", FilePath: "/books/unique.m4b"},
	}
	got := runFilePathCollisionReport(t, books, filePathCollisionReportParams{})

	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"TotalBooks", got.TotalBooks, 3},
		{"DistinctPaths", got.DistinctPaths, 2},
		{"CollisionGroups", got.CollisionGroups, 1},
		{"AffectedRows", got.AffectedRows, 2},
		{"EmptyFilePath", got.EmptyFilePath, 0},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	if len(got.Sample) != 1 {
		t.Fatalf("Sample len = %d, want 1", len(got.Sample))
	}
	if got.Sample[0].FilePath != "/books/shared.m4b" {
		t.Errorf("Sample[0].FilePath = %q, want /books/shared.m4b", got.Sample[0].FilePath)
	}
	wantIDs := []string{"b1", "b2"}
	if len(got.Sample[0].BookIDs) != len(wantIDs) {
		t.Fatalf("Sample[0].BookIDs = %v, want %v", got.Sample[0].BookIDs, wantIDs)
	}
	for i, id := range wantIDs {
		if got.Sample[0].BookIDs[i] != id {
			t.Errorf("Sample[0].BookIDs[%d] = %q, want %q", i, got.Sample[0].BookIDs[i], id)
		}
	}
}

// TestFilePathCollisionReport_NoCollisions_ReportsZero is the anti-over-
// suppression case: a known-good, all-unique-path input must still report
// zero collisions with the guard active, proving the detector does not fire
// on clean data.
func TestFilePathCollisionReport_NoCollisions_ReportsZero(t *testing.T) {
	books := []database.BookCore{
		{ID: "b1", FilePath: "/books/one.m4b"},
		{ID: "b2", FilePath: "/books/two.m4b"},
		{ID: "b3", FilePath: "/books/three.m4b"},
	}
	got := runFilePathCollisionReport(t, books, filePathCollisionReportParams{})

	if got.CollisionGroups != 0 {
		t.Errorf("CollisionGroups = %d, want 0", got.CollisionGroups)
	}
	if got.AffectedRows != 0 {
		t.Errorf("AffectedRows = %d, want 0", got.AffectedRows)
	}
	if got.DistinctPaths != 3 {
		t.Errorf("DistinctPaths = %d, want 3", got.DistinctPaths)
	}
	if len(got.Sample) != 0 {
		t.Errorf("Sample = %v, want empty", got.Sample)
	}
}

// TestFilePathCollisionReport_DistinctPathsNotCounted proves that books with
// distinct paths are NOT counted as collisions -- the negative case
// explicitly required alongside the positive detection test.
func TestFilePathCollisionReport_DistinctPathsNotCounted(t *testing.T) {
	books := []database.BookCore{
		{ID: "b1", FilePath: "/books/alpha.m4b"},
		{ID: "b2", FilePath: "/books/bravo.m4b"},
	}
	got := runFilePathCollisionReport(t, books, filePathCollisionReportParams{})

	if got.CollisionGroups != 0 {
		t.Errorf("CollisionGroups = %d, want 0 (distinct paths must not collide)", got.CollisionGroups)
	}
	if got.AffectedRows != 0 {
		t.Errorf("AffectedRows = %d, want 0", got.AffectedRows)
	}
}

// TestFilePathCollisionReport_EmptyPathExcludedNotCounted asserts the brief's
// edge-case decision: a blank FilePath is excluded from the collision map
// entirely (never treated as a shared "" path) and is reported in its own
// bucket instead.
func TestFilePathCollisionReport_EmptyPathExcludedNotCounted(t *testing.T) {
	books := []database.BookCore{
		{ID: "b1", FilePath: ""},
		{ID: "b2", FilePath: ""},
		{ID: "b3", FilePath: "   "}, // whitespace-only is trimmed to blank too
		{ID: "b4", FilePath: "/books/real.m4b"},
	}
	got := runFilePathCollisionReport(t, books, filePathCollisionReportParams{})

	if got.EmptyFilePath != 3 {
		t.Errorf("EmptyFilePath = %d, want 3", got.EmptyFilePath)
	}
	if got.CollisionGroups != 0 {
		t.Errorf("CollisionGroups = %d, want 0 (blank paths must not form a collision group)", got.CollisionGroups)
	}
	if got.DistinctPaths != 1 {
		t.Errorf("DistinctPaths = %d, want 1", got.DistinctPaths)
	}
}

// TestFilePathCollisionReport_MultipleGroupsAndSampleCap exercises more than
// one collision group and the sample-size cap.
func TestFilePathCollisionReport_MultipleGroupsAndSampleCap(t *testing.T) {
	var books []database.BookCore
	// 5 collision groups of 2 books each, plus 1 unique book.
	for g := 0; g < 5; g++ {
		path := fmt.Sprintf("/books/group%d.m4b", g)
		books = append(books,
			database.BookCore{ID: fmt.Sprintf("g%d-1", g), FilePath: path},
			database.BookCore{ID: fmt.Sprintf("g%d-2", g), FilePath: path},
		)
	}
	books = append(books, database.BookCore{ID: "solo", FilePath: "/books/solo.m4b"})

	got := runFilePathCollisionReport(t, books, filePathCollisionReportParams{SampleLimit: 2})

	if got.CollisionGroups != 5 {
		t.Errorf("CollisionGroups = %d, want 5", got.CollisionGroups)
	}
	if got.AffectedRows != 10 {
		t.Errorf("AffectedRows = %d, want 10", got.AffectedRows)
	}
	if len(got.Sample) != 2 {
		t.Errorf("Sample len = %d, want 2 (SampleLimit)", len(got.Sample))
	}
}

// TestFilePathCollisionReport_ShardsAcrossManyBooks pins the sharded-worker
// reduce path (not just the single-shard case a small fixture would exercise)
// by running enough books that shardBookCores splits into multiple shards on
// any multi-core test runner, and confirms the merged result is still exact.
func TestFilePathCollisionReport_ShardsAcrossManyBooks(t *testing.T) {
	const n = 500
	var books []database.BookCore
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("book%d", i)
		// Every 10th book shares a path with the one before it.
		path := fmt.Sprintf("/books/unique%d.m4b", i)
		if i%10 == 1 {
			path = books[len(books)-1].FilePath
		}
		books = append(books, database.BookCore{ID: id, FilePath: path})
	}

	got := runFilePathCollisionReport(t, books, filePathCollisionReportParams{})

	if got.TotalBooks != n {
		t.Fatalf("TotalBooks = %d, want %d", got.TotalBooks, n)
	}
	wantGroups := n / 10
	if got.CollisionGroups != wantGroups {
		t.Errorf("CollisionGroups = %d, want %d", got.CollisionGroups, wantGroups)
	}
	if got.AffectedRows != wantGroups*2 {
		t.Errorf("AffectedRows = %d, want %d", got.AffectedRows, wantGroups*2)
	}
}

func TestShardBookCores(t *testing.T) {
	books := make([]database.BookCore, 7)
	for i := range books {
		books[i] = database.BookCore{ID: fmt.Sprintf("b%d", i)}
	}

	shards := shardBookCores(books, 3)
	if len(shards) != 3 {
		t.Fatalf("len(shards) = %d, want 3", len(shards))
	}
	total := 0
	for _, s := range shards {
		if len(s) == 0 {
			t.Errorf("shard is empty, want no empty shards")
		}
		total += len(s)
	}
	if total != len(books) {
		t.Errorf("total sharded books = %d, want %d", total, len(books))
	}

	// More workers than books: never return an empty shard.
	shards = shardBookCores(books[:2], 8)
	if len(shards) != 2 {
		t.Fatalf("len(shards) = %d, want 2 (capped to book count)", len(shards))
	}

	if shards := shardBookCores(nil, 4); shards != nil {
		t.Errorf("shardBookCores(nil) = %v, want nil", shards)
	}
}
