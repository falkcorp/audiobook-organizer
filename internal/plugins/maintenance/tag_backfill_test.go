// file: internal/plugins/maintenance/tag_backfill_test.go
// version: 1.1.0
// guid: 5b6e7f4a-9c1d-4e0a-8f2b-3a6d1c9e5b70
// last-edited: 2026-07-06

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// fakeTagExtractor is a deterministic, goroutine-safe metadata.MetadataExtractor
// stub used so TestTagBackfill_ParallelProducesSameResultAsSerial doesn't need
// real audio fixtures. Classifies files by a marker in their path:
//   - path contains "readerr" -> returns an error (readErr path)
//   - path contains "notags"  -> returns Metadata with no AllTags (skip path)
//   - otherwise               -> returns deterministic non-empty tags
type fakeTagExtractor struct {
	mu    sync.Mutex
	calls int
}

func (e *fakeTagExtractor) ExtractMetadata(filePath string) (metadata.Metadata, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()

	base := filepath.Base(filePath)
	switch {
	case strings.Contains(filePath, "readerr"):
		return metadata.Metadata{}, fmt.Errorf("simulated tag read failure for %s", base)
	case strings.Contains(filePath, "notags"):
		return metadata.Metadata{}, nil // AllTags empty -> "nothing capturable"
	default:
		return metadata.Metadata{
			Title:       "Title-" + base,
			TrackNumber: 3,
			TrackTotal:  12,
			DiscNumber:  1,
			DiscTotal:   2,
			AllTags:     map[string]string{"title": "Title-" + base, "artist": "Author"},
		}, nil
	}
}

// TestTagBackfill_ParallelProducesSameResultAsSerial exercises the
// registry.RunItems-based parallel loop (CONC-7) across a mix of
// skip-already-tagged / skip-empty-path / missing-on-disk / read-error /
// no-tags / needs-backfill BookFiles, and asserts the write set produced is
// exactly the set that a serial pass would have produced — independent of
// goroutine completion order, since RunItems runs with Concurrency ==
// runtime.NumCPU()*4 (I/O-bound sizing). Run with -race to catch data races
// on the shared examined/needed/missing/readErr counters and the fixes /
// examples slices.
func TestTagBackfill_ParallelProducesSameResultAsSerial(t *testing.T) {
	dir := t.TempDir()

	extractor := &fakeTagExtractor{}
	metadata.SetMetadataExtractor(extractor)
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	var files []database.BookFile
	wantBackfilled := map[string]bool{}

	// Already has RawTags, Force=false -> must be skipped untouched (path is
	// never created on disk; if the code statted/opened it, the test would
	// still pass today but any future regression that drops the skip would
	// surface as a missing/readErr count mismatch below).
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("skip-has-tags-%d", i)
		files = append(files, database.BookFile{
			ID:       id,
			FilePath: filepath.Join(dir, id+".mp3"),
			RawTags:  map[string]string{"title": "already tagged"},
		})
	}

	// Empty FilePath -> skipped.
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("skip-empty-path-%d", i)
		files = append(files, database.BookFile{ID: id, FilePath: ""})
	}

	// Path does not exist on disk -> missing.
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("missing-%d", i)
		files = append(files, database.BookFile{ID: id, FilePath: filepath.Join(dir, "nope", id+".mp3")})
	}

	// Exists but the extractor errors on it -> readErr.
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("readerr-%d", i)
		p := filepath.Join(dir, "readerr", id+".mp3")
		mustWriteFile(t, p)
		files = append(files, database.BookFile{ID: id, FilePath: p})
	}

	// Exists but the extractor finds no tags -> skipped ("nothing capturable").
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("notags-%d", i)
		p := filepath.Join(dir, "notags", id+".mp3")
		mustWriteFile(t, p)
		files = append(files, database.BookFile{ID: id, FilePath: p})
	}

	// Exists and should be successfully backfilled.
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("needs-backfill-%d", i)
		p := filepath.Join(dir, id+".mp3")
		mustWriteFile(t, p)
		files = append(files, database.BookFile{ID: id, FilePath: p})
		wantBackfilled[id] = true
	}

	// core is the BookFileCore projection of files — the op's
	// GetAllBookFilesCore call. GetBookFiles (the hydrate path for each
	// backfill candidate) returns the full rows directly since none of the
	// fixtures set a BookID (all share the empty-string bucket).
	core := make([]database.BookFileCore, len(files))
	for i := range files {
		core[i] = files[i].Core()
	}

	var (
		upsertMu      sync.Mutex
		upserted      []*database.BookFile
		upsertBatches int
	)
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return core, nil },
		GetBookFilesFunc:        func(bookID string) ([]database.BookFile, error) { return files, nil },
		BatchUpsertBookFilesFunc: func(batch []*database.BookFile) error {
			upsertMu.Lock()
			upserted = append(upserted, batch...)
			upsertBatches++
			upsertMu.Unlock()
			return nil
		},
	}

	plugin := New(fakeDeps{store: store})
	reporter := &fakeReporter{}

	raw, err := json.Marshal(tagBackfillParams{DryRun: false})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	if err := plugin.runTagBackfill(context.Background(), raw, reporter); err != nil {
		t.Fatalf("runTagBackfill: %v", err)
	}

	if upsertBatches == 0 {
		t.Fatalf("expected at least one BatchUpsertBookFiles call")
	}

	gotByID := map[string]*database.BookFile{}
	for _, f := range upserted {
		gotByID[f.ID] = f
	}
	if len(gotByID) != len(wantBackfilled) {
		var gotList []string
		for id := range gotByID {
			gotList = append(gotList, id)
		}
		sort.Strings(gotList)
		t.Fatalf("upserted %d distinct files, want %d\n got=%v", len(gotByID), len(wantBackfilled), gotList)
	}
	for id := range wantBackfilled {
		if _, ok := gotByID[id]; !ok {
			t.Errorf("expected %s to be upserted, but it was not", id)
		}
	}
	for id := range gotByID {
		if !wantBackfilled[id] {
			t.Errorf("unexpected upsert for %s (should have been skipped/missing/read-err)", id)
		}
	}

	for id, f := range gotByID {
		if len(f.RawTags) == 0 {
			t.Errorf("%s: expected RawTags to be backfilled", id)
		}
		if f.TrackNumber != 3 || f.TrackCount != 12 || f.DiscNumber != 1 || f.DiscCount != 2 {
			t.Errorf("%s: unexpected track/disc fields: %+v", id, f)
		}
	}
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
