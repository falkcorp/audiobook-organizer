// file: internal/plugins/maintenance/unknown_author_audit_test.go
// version: 1.0.0
// guid: 397e4a71-cb8f-4f03-8a55-f548a6c33cf4
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func runUnknownAuthorAudit(t *testing.T, books []database.BookCore, params unknownAuthorAuditParams) unknownAuthorAuditReport {
	t.Helper()
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) { return books, nil },
	}
	rep, err := buildUnknownAuthorAudit(context.Background(), store, params, &fakeReporter{})
	if err != nil {
		t.Fatalf("buildUnknownAuthorAudit: %v", err)
	}
	return rep
}

// TestUnknownAuthorAudit_FlagsPlaceholderPath: a book filed under an
// "Unknown Author" directory is counted and appears in the sample with its ID,
// title and path. Case-insensitive matching goes through authorname.IsPlaceholder.
func TestUnknownAuthorAudit_FlagsPlaceholderPath(t *testing.T) {
	books := []database.BookCore{
		{ID: "ua1", Title: "All Jobs and Classes", FilePath: "library/Unknown Author/All Jobs and Classes/All Jobs and Classes.m4b"},
		{ID: "ua2", Title: "Lowercase", FilePath: "/lib/books/unknown author/Lowercase/Lowercase.m4b"},
		{ID: "ok1", Title: "Clean", FilePath: "library/Jane Doe/Clean/Clean.m4b"},
	}
	got := runUnknownAuthorAudit(t, books, unknownAuthorAuditParams{})

	if got.TotalBooks != 3 {
		t.Errorf("TotalBooks = %d, want 3", got.TotalBooks)
	}
	if got.PlaceholderInPath != 2 {
		t.Fatalf("PlaceholderInPath = %d, want 2", got.PlaceholderInPath)
	}
	if len(got.Sample) != 2 {
		t.Fatalf("Sample len = %d, want 2", len(got.Sample))
	}
	// Sorted by FilePath: "/lib/..." sorts before "library/...".
	want := []unknownAuthorAuditSample{
		{BookID: "ua2", Title: "Lowercase", FilePath: "/lib/books/unknown author/Lowercase/Lowercase.m4b"},
		{BookID: "ua1", Title: "All Jobs and Classes", FilePath: "library/Unknown Author/All Jobs and Classes/All Jobs and Classes.m4b"},
	}
	for i := range want {
		if got.Sample[i] != want[i] {
			t.Errorf("Sample[%d] = %+v, want %+v", i, got.Sample[i], want[i])
		}
	}
}

// TestUnknownAuthorAudit_CleanPathNotFlagged is the anti-over-suppression
// guard in the other direction: real-author paths, a title that merely
// CONTAINS the phrase, and a placeholder that appears only in the FILENAME are
// all left uncounted.
func TestUnknownAuthorAudit_CleanPathNotFlagged(t *testing.T) {
	books := []database.BookCore{
		{ID: "b1", Title: "Clean", FilePath: "library/Jane Doe/Clean/Clean.m4b"},
		{ID: "b2", Title: "The Unknown Author's Diary", FilePath: "library/Jane Doe/The Unknown Author's Diary/x.m4b"},
		{ID: "b3", Title: "Filename Only", FilePath: "library/Jane Doe/Filename Only/Filename Only - Unknown Author.m4b"},
		{ID: "b4", Title: "Bare", FilePath: "Unknown Author"},
	}
	got := runUnknownAuthorAudit(t, books, unknownAuthorAuditParams{})

	if got.PlaceholderInPath != 0 {
		t.Errorf("PlaceholderInPath = %d, want 0 (sample=%+v)", got.PlaceholderInPath, got.Sample)
	}
	if len(got.Sample) != 0 {
		t.Errorf("Sample = %+v, want empty", got.Sample)
	}
}

// TestUnknownAuthorAudit_SampleCap: the count covers every flagged book while
// the sample is capped at SampleLimit and holds the first N in sorted order.
func TestUnknownAuthorAudit_SampleCap(t *testing.T) {
	var books []database.BookCore
	// Appended in reverse so the cap must sort, not take insertion order.
	for i := 4; i >= 0; i-- {
		books = append(books, database.BookCore{
			ID:       fmt.Sprintf("ua%d", i),
			Title:    fmt.Sprintf("Title %d", i),
			FilePath: fmt.Sprintf("library/Unknown Author/Title %d/Title %d.m4b", i, i),
		})
	}
	got := runUnknownAuthorAudit(t, books, unknownAuthorAuditParams{SampleLimit: 2})

	if got.PlaceholderInPath != 5 {
		t.Errorf("PlaceholderInPath = %d, want 5 (the cap must not truncate the count)", got.PlaceholderInPath)
	}
	if len(got.Sample) != 2 {
		t.Fatalf("Sample len = %d, want 2 (SampleLimit)", len(got.Sample))
	}
	if got.Sample[0].BookID != "ua0" || got.Sample[1].BookID != "ua1" {
		t.Errorf("Sample IDs = [%s %s], want [ua0 ua1]", got.Sample[0].BookID, got.Sample[1].BookID)
	}
}

// TestUnknownAuthorAudit_DefaultSampleCap: a zero SampleLimit falls back to the
// default rather than meaning "list nothing".
func TestUnknownAuthorAudit_DefaultSampleCap(t *testing.T) {
	var books []database.BookCore
	for i := 0; i < unknownAuthorAuditSampleLimit+5; i++ {
		books = append(books, database.BookCore{
			ID:       fmt.Sprintf("ua%04d", i),
			FilePath: fmt.Sprintf("library/Unknown Author/T%04d/T.m4b", i),
		})
	}
	got := runUnknownAuthorAudit(t, books, unknownAuthorAuditParams{})
	if got.PlaceholderInPath != unknownAuthorAuditSampleLimit+5 {
		t.Errorf("PlaceholderInPath = %d, want %d", got.PlaceholderInPath, unknownAuthorAuditSampleLimit+5)
	}
	if len(got.Sample) != unknownAuthorAuditSampleLimit {
		t.Errorf("Sample len = %d, want %d", len(got.Sample), unknownAuthorAuditSampleLimit)
	}
}

// TestUnknownAuthorAudit_EmptyLibrary: no books means zero counts, no sample,
// and no error.
func TestUnknownAuthorAudit_EmptyLibrary(t *testing.T) {
	got := runUnknownAuthorAudit(t, nil, unknownAuthorAuditParams{})
	if got.TotalBooks != 0 || got.PlaceholderInPath != 0 || got.EmptyFilePath != 0 || got.SoftDeleted != 0 {
		t.Errorf("report = %+v, want all zero", got)
	}
	if len(got.Sample) != 0 {
		t.Errorf("Sample = %+v, want empty", got.Sample)
	}
}

// TestUnknownAuthorAudit_EmptyFilePathNotFlagged: a never-organized book (blank
// or whitespace FilePath) is counted in its own bucket, never as a baked path.
func TestUnknownAuthorAudit_EmptyFilePathNotFlagged(t *testing.T) {
	books := []database.BookCore{
		{ID: "e1", FilePath: ""},
		{ID: "e2", FilePath: "   "},
		{ID: "ua1", FilePath: "library/Unknown Author/T/T.m4b"},
	}
	got := runUnknownAuthorAudit(t, books, unknownAuthorAuditParams{})
	if got.EmptyFilePath != 2 {
		t.Errorf("EmptyFilePath = %d, want 2", got.EmptyFilePath)
	}
	if got.PlaceholderInPath != 1 {
		t.Errorf("PlaceholderInPath = %d, want 1", got.PlaceholderInPath)
	}
}

// TestUnknownAuthorAudit_SoftDeletedExcluded: a trashed row under the
// placeholder tree is not counted as a live baked path; it lands in the
// SoftDeleted bucket instead.
func TestUnknownAuthorAudit_SoftDeletedExcluded(t *testing.T) {
	trashed := true
	books := []database.BookCore{
		{ID: "gone", FilePath: "library/Unknown Author/T/T.m4b", MarkedForDeletion: &trashed},
		{ID: "live", FilePath: "library/Unknown Author/U/U.m4b"},
	}
	got := runUnknownAuthorAudit(t, books, unknownAuthorAuditParams{})
	if got.SoftDeleted != 1 {
		t.Errorf("SoftDeleted = %d, want 1", got.SoftDeleted)
	}
	if got.PlaceholderInPath != 1 || len(got.Sample) != 1 || got.Sample[0].BookID != "live" {
		t.Errorf("PlaceholderInPath = %d, Sample = %+v, want 1 [live]", got.PlaceholderInPath, got.Sample)
	}
}

// TestUnknownAuthorAudit_StoreErrorPropagates: a failed snapshot is an error,
// not a report of zero.
func TestUnknownAuthorAudit_StoreErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) { return nil, boom },
	}
	_, err := buildUnknownAuthorAudit(context.Background(), store, unknownAuthorAuditParams{}, &fakeReporter{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapping %v", err, boom)
	}
}

// TestUnknownAuthorAudit_DefIsReadOnly pins the report-only contract at the
// op-definition level: only CapLibraryRead is requested.
func TestUnknownAuthorAudit_DefIsReadOnly(t *testing.T) {
	def := (&Plugin{}).unknownAuthorAuditDef()
	if def.ID != "maintenance.unknown-author-audit" {
		t.Errorf("ID = %q", def.ID)
	}
	if len(def.Capabilities) != 1 || def.Capabilities[0] != sdk.CapLibraryRead {
		t.Errorf("Capabilities = %v, want only CapLibraryRead", def.Capabilities)
	}
}
