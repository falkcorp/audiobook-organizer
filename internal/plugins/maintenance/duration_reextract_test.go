// file: internal/plugins/maintenance/duration_reextract_test.go
// version: 1.0.0
// guid: 4a7d1e92-8c63-4f50-a1b8-3e6c9d2f5a04
// last-edited: 2026-06-21

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func mustReextractParams(t *testing.T, dryRun bool, limit int) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(durationReextractParams{DryRun: dryRun, Limit: limit})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return b
}

// newReextractPlugin wires a MockStore over a fixed book slice. updates records
// every UpdateBook call so apply-path tests can assert writes.
func newReextractPlugin(books []database.Book) (*Plugin, *[]database.Book) {
	updates := make([]database.Book, 0)
	byID := make(map[string]database.Book, len(books))
	for _, b := range books {
		byID[b.ID] = b
	}
	store := &database.MockStore{
		CountBooksFunc: func() (int, error) { return len(books), nil },
		GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) {
			if offset >= len(books) {
				return nil, nil
			}
			end := offset + limit
			if end > len(books) {
				end = len(books)
			}
			return books[offset:end], nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := byID[id]
			if !ok {
				return nil, nil
			}
			cp := b
			return &cp, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			updates = append(updates, *b)
			return b, nil
		},
	}
	return New(fakeDeps{store: store}), &updates
}

func intPtr(v int) *int { return &v }

// TestDurationReextract_Registered verifies the op def carries the expected ID,
// capabilities, and dry-run-friendly defaults.
func TestDurationReextract_Registered(t *testing.T) {
	p := New(fakeDeps{store: &database.MockStore{}})
	def := p.durationReextractDef()
	if def.ID != "maintenance.duration-reextract" {
		t.Errorf("def ID = %q, want maintenance.duration-reextract", def.ID)
	}
	if def.ConcurrencyKey != "maintenance.duration-reextract" {
		t.Errorf("ConcurrencyKey = %q", def.ConcurrencyKey)
	}
	if !def.Cancellable {
		t.Error("op must be cancellable")
	}
	if def.Run == nil {
		t.Error("op Run must be set")
	}
}

// TestDurationDiffMeaningful exercises the tolerance gate (>2% AND >5s).
func TestDurationDiffMeaningful(t *testing.T) {
	cases := []struct {
		old, new int
		want     bool
	}{
		{0, 100, true},     // no stored value, any real value is an improvement
		{0, 0, false},      // nothing usable
		{3600, 3600, false}, // identical
		{3600, 3603, false}, // 3s — under both floors
		{3600, 3700, true},  // 100s and ~2.8%
		{3600, 7200, true},  // exactly double (the m4b bug signature)
		{1000, 1004, false}, // 4s, under abs floor even though >0.2%
		{100, 110, true},    // 10s and 10%
	}
	for _, c := range cases {
		if got := durationDiffMeaningful(c.old, c.new); got != c.want {
			t.Errorf("durationDiffMeaningful(%d,%d) = %v, want %v", c.old, c.new, got, c.want)
		}
	}
}

// TestDurationReextract_DryRunWritesNothing runs the full dry-run path over a
// real on-disk file and confirms no UpdateBook calls occur. The temp file is not
// valid audio, so mediainfo.Extract yields a read path that exercises the op's
// error/skip accounting — the contract under test is "dry run never writes".
func TestDurationReextract_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "book.m4b")
	if err := os.WriteFile(fp, []byte("not real audio but present on disk"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	books := []database.Book{
		{ID: "b1", Title: "Present file", FilePath: fp, Duration: intPtr(100)},
		{ID: "b2", Title: "Missing file", FilePath: filepath.Join(dir, "gone.m4b"), Duration: intPtr(100)},
		{ID: "b3", Title: "No path", FilePath: ""},
	}
	p, updates := newReextractPlugin(books)

	if err := p.runDurationReextract(context.Background(), mustReextractParams(t, true, 0), &fakeReporter{}); err != nil {
		t.Fatalf("dry-run returned error: %v", err)
	}
	if len(*updates) != 0 {
		t.Errorf("dry run must write nothing, got %d UpdateBook calls", len(*updates))
	}
}

// TestDurationReextract_NilParamsDefaultsDryRun verifies nil params -> dryRun=true.
func TestDurationReextract_NilParamsDefaultsDryRun(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "book.m4b")
	if err := os.WriteFile(fp, []byte("present"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	books := []database.Book{{ID: "b1", FilePath: fp, Duration: intPtr(100)}}
	p, updates := newReextractPlugin(books)

	if err := p.runDurationReextract(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatalf("nil-params run returned error: %v", err)
	}
	if len(*updates) != 0 {
		t.Errorf("nil params must default to dry run, got %d writes", len(*updates))
	}
}

// TestDurationReextract_EmptyLibrary verifies a clean exit with no books.
func TestDurationReextract_EmptyLibrary(t *testing.T) {
	p, updates := newReextractPlugin(nil)
	if err := p.runDurationReextract(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("empty-library run returned error: %v", err)
	}
	if len(*updates) != 0 {
		t.Errorf("empty library must write nothing, got %d writes", len(*updates))
	}
}
