// file: internal/plugins/maintenance/title_backfill_test.go
// version: 1.4.0
// guid: b2c3d4e5-f6a7-8901-bcde-ef0123456789
// last-edited: 2026-07-18

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// fakeReporter satisfies sdk.Reporter (= registry.Reporter) for tests.
type fakeReporter struct{ logs []string }

func (r *fakeReporter) UpdateProgress(_, _ int, _ string) error { return nil }
func (r *fakeReporter) Log(_ slog.Level, msg string, _ ...slog.Attr) error {
	r.logs = append(r.logs, msg)
	return nil
}
func (r *fakeReporter) Logger() *slog.Logger   { return slog.Default() }
func (r *fakeReporter) Checkpoint(_ any) error { return nil }
func (r *fakeReporter) IsCanceled() bool       { return false }
func (r *fakeReporter) RunPhase(_ context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(context.Background(), r)
}
func (r *fakeReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (r *fakeReporter) SetCurrentItem(_ string)                          {}

var _ sdk.Reporter = (*fakeReporter)(nil)

// fakeDeps satisfies the ServerDeps interface with only Store() wired.
type fakeDeps struct{ store database.Store }

func (d fakeDeps) Store() database.Store { return d.store }

// Delegate stubs — maintenance plugin calls these on ServerDeps from other ops.
func (d fakeDeps) RunIsbnEnrichment(_ context.Context, _ operations.ProgressReporter, _ string) error {
	return nil
}
func (d fakeDeps) RunMetadataRefreshScan(_ context.Context, _ operations.ProgressReporter) error {
	return nil
}
func (d fakeDeps) RunBulkWriteBack(_ context.Context, _ string, _ []string, _ bool, _ int, _ operations.ProgressReporter) error {
	return nil
}
func (d fakeDeps) RunAutoPurgeSoftDeleted(_ string) {}
func (d fakeDeps) ExecuteSeriesPrune(_ context.Context, _ database.Store, _ operations.ProgressReporter, _ string) error {
	return nil
}
func (d fakeDeps) ExecuteSeriesNormalizeCore(_ context.Context, _ database.Store, _ func(string)) ([]string, error) {
	return nil, nil
}
func (d fakeDeps) BackfillExternalIDs(_ func(int, int, string)) error { return nil }
func (d fakeDeps) StripMovementAtoms(_ context.Context)               {}
func (d fakeDeps) RemuxMalformedM4BFiles(_ context.Context, _ func(int, int, string)) error {
	return nil
}
func (d fakeDeps) TranscodeMalformedM4BFiles(_ context.Context, _ func(int, int, string)) error {
	return nil
}
func (d fakeDeps) CleanupOrphanedTempFiles(_ string, _ string) int { return 0 }
func (d fakeDeps) CleanupTrashedVersions() int                     { return 0 }
func (d fakeDeps) SweepArchivedBooks() int                         { return 0 }
func (d fakeDeps) ActivityFlushOp(_ string)                        {}
func (d fakeDeps) EnqueueWriteBack(_ string)                       {}
func (d fakeDeps) PollBatch(_ context.Context) (int, error)        { return 0, nil }
func (d fakeDeps) DedupLLMReview(_ context.Context) error          { return nil }
func (d fakeDeps) DedupTriageExactPending(_ context.Context) (*TriageReport, error) {
	return &TriageReport{}, nil
}
func (d fakeDeps) InvalidateDedupCache() {}
func (d fakeDeps) MetadataUpgradeRun(_ context.Context, _ int) (int, int, int, int, error) {
	return 0, 0, 0, 0, nil
}
func (d fakeDeps) OptimizeAIScanStore() error { return nil }
func (d fakeDeps) OptimizeOLStore() error     { return nil }
func (d fakeDeps) PruneOldLogs(_ int) error   { return nil }
func (d fakeDeps) CompactActivityLog(_ context.Context, _, _, _ int) (int, int, int, error) {
	return 0, 0, 0, nil
}
func (d fakeDeps) HasDedupEngine() bool                { return false }
func (d fakeDeps) HasMetadataFetchService() bool       { return false }
func (d fakeDeps) HasISBNEnrichment() bool             { return false }
func (d fakeDeps) HasAIParsing() bool                  { return false }
func (d fakeDeps) HasBatchPoller() bool                { return false }
func (d fakeDeps) RootDir() string                     { return "/lib" }
func (d fakeDeps) LogRetentionDays() int               { return 30 }
func (d fakeDeps) PurgeSoftDeletedAfterDays() int      { return 30 }
func (d fakeDeps) ActivityLogCompactionDays() int      { return 7 }
func (d fakeDeps) ActivityLogRetentionChangeDays() int { return 30 }
func (d fakeDeps) ActivityLogRetentionDebugDays() int  { return 7 }
func (d fakeDeps) BackupRetentionDays() int            { return 30 }
func (d fakeDeps) EnqueueOp(_ context.Context, _ string, _ any) (string, error) {
	return "", nil
}
func (d fakeDeps) WaitForOp(_ context.Context, _ string) error { return nil }
func (d fakeDeps) SearchTranscriptionCandidate(_ context.Context, _, _, _ string) (string, string, float64, bool, error) {
	return "", "", 0, false, nil
}
func (d fakeDeps) ApplyTranscriptionCandidate(_ context.Context, _, _, _ string) error { return nil }

var _ ServerDeps = fakeDeps{}

// newTestPlugin builds a Plugin backed by a paged fake store.
// Returns the plugin and a pointer to the slice of all written books.
func newTestPlugin(books []database.Book) (*Plugin, *[]database.Book) {
	written := make([]database.Book, 0)
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset >= len(books) {
				return nil, nil
			}
			end := offset + limit
			if end > len(books) {
				end = len(books)
			}
			core := make([]database.BookCore, end-offset)
			for i, b := range books[offset:end] {
				core[i] = b.Core()
			}
			return core, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			for i := range books {
				if books[i].ID == id {
					b := books[i]
					return &b, nil
				}
			}
			return nil, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			written = append(written, *b)
			return b, nil
		},
	}
	return New(fakeDeps{store: store}), &written
}

func mustParams(dryRun bool) json.RawMessage {
	b, _ := json.Marshal(titleBackfillParams{DryRun: dryRun})
	return b
}

// TestTitleBackfill_DryRunWritesNothing verifies no UpdateBook call is made
// when dryRun=true, even when poisoned titles are present.
func TestTitleBackfill_DryRunWritesNothing(t *testing.T) {
	books := []database.Book{
		{ID: "b1", Title: "(76/85) Tarkin: Star Wars"},
		{ID: "b2", Title: "Chapter 03 - The Storm"},
		{ID: "b3", Title: "The Hobbit"},
	}
	p, written := newTestPlugin(books)

	if err := p.runTitleBackfill(context.Background(), mustParams(true), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*written) != 0 {
		t.Errorf("dry run must write nothing, got %d writes", len(*written))
	}
}

// TestTitleBackfill_ApplyCleansPoisonedRows verifies that exactly the poisoned
// rows are updated and clean rows are untouched.
func TestTitleBackfill_ApplyCleansPoisonedRows(t *testing.T) {
	books := []database.Book{
		{ID: "b1", Title: "(76/85) Tarkin: Star Wars"},
		{ID: "b2", Title: "Chapter 03 - The Storm"},
		{ID: "b3", Title: "The Hobbit"}, // clean — must NOT be written
		{ID: "b4", Title: "03 - Dune"},
	}
	p, written := newTestPlugin(books)

	if err := p.runTitleBackfill(context.Background(), mustParams(false), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*written) != 3 {
		t.Errorf("expected 3 writes (b1, b2, b4), got %d", len(*written))
	}

	titles := make(map[string]string, len(*written))
	for _, b := range *written {
		titles[b.ID] = b.Title
	}
	if titles["b1"] != "Tarkin: Star Wars" {
		t.Errorf("b1: got %q, want %q", titles["b1"], "Tarkin: Star Wars")
	}
	if titles["b2"] != "The Storm" {
		t.Errorf("b2: got %q, want %q", titles["b2"], "The Storm")
	}
	if titles["b4"] != "Dune" {
		t.Errorf("b4: got %q, want %q", titles["b4"], "Dune")
	}
	if _, present := titles["b3"]; present {
		t.Error("b3 (clean title) must not be written")
	}
}

// TestTitleBackfill_SkipsEntirePrefixTitle verifies a title that strips to
// empty is skipped, not blanked. "Chapter 1 -" with no body after the
// delimiter matches the chapter pattern entirely, leaving nothing — the
// pattern needs \s* at the end so a bare "Chapter N -" qualifies.
func TestTitleBackfill_SkipsEntirePrefixTitle(t *testing.T) {
	books := []database.Book{
		{ID: "b1", Title: "Chapter 1 -"}, // strips to "" — must be skipped
	}
	p, written := newTestPlugin(books)

	if err := p.runTitleBackfill(context.Background(), mustParams(false), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*written) != 0 {
		t.Errorf("empty-strip title must be skipped, got %d writes", len(*written))
	}
}

// TestTitleBackfill_EmptyLibrary verifies the op exits cleanly with no books.
func TestTitleBackfill_EmptyLibrary(t *testing.T) {
	p, written := newTestPlugin(nil)

	if err := p.runTitleBackfill(context.Background(), mustParams(false), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*written) != 0 {
		t.Errorf("empty library: expected 0 writes, got %d", len(*written))
	}
}

// TestTitleBackfill_NilParamsDefaultsDryRun verifies nil params → dryRun=true.
func TestTitleBackfill_NilParamsDefaultsDryRun(t *testing.T) {
	books := []database.Book{
		{ID: "b1", Title: "(1/2) Some Book"},
	}
	p, written := newTestPlugin(books)

	if err := p.runTitleBackfill(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*written) != 0 {
		t.Errorf("nil params must default to dryRun=true, got %d writes", len(*written))
	}
}
