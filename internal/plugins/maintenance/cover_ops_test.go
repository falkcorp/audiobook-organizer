// file: internal/plugins/maintenance/cover_ops_test.go
// version: 1.0.0
// guid: 1c8f4a27-9e3b-4d60-b5a2-7d0e6f3c9b18
// last-edited: 2026-09-26

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/covertext"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func coverTestPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{G: 180, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// coverOpFixture: a Pebble store with two books, each alone in a folder with
// a cover.png, and a root dir for .covers.
func coverOpFixture(t *testing.T) (*database.PebbleStore, rootDirDeps, []string) {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	var ids []string
	for i, name := range []string{"One", "Two"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		audio := filepath.Join(dir, name+".m4b")
		if err := os.WriteFile(audio, []byte("not really audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		coverTestPNG(t, filepath.Join(dir, "cover.png"), 300+i, 300)
		b, err := st.CreateBook(&database.Book{Title: name, FilePath: audio})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	return st, rootDirDeps{fakeDeps: fakeDeps{store: st}, root: root}, ids
}

func TestFolderCoverBackfillPreviewWritesNothing(t *testing.T) {
	st, deps, ids := coverOpFixture(t)
	p := New(deps)
	if err := p.runFolderCoverBackfill(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		b, err := st.GetBookByID(id)
		if err != nil || b == nil {
			t.Fatal(err)
		}
		if b.CoverURL != nil {
			t.Fatalf("preview wrote cover_url %q on %s", *b.CoverURL, id)
		}
	}
	if _, err := os.Stat(filepath.Join(deps.root, ".covers")); !os.IsNotExist(err) {
		t.Fatalf("preview created the covers directory: %v", err)
	}

	// Apply writes both.
	if err := p.runFolderCoverBackfill(context.Background(), json.RawMessage(`{"dry_run":false}`), &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		b, _ := st.GetBookByID(id)
		if b.CoverURL == nil || filepath.Ext(*b.CoverURL) != ".png" {
			t.Fatalf("apply did not set a local png cover on %s: %v", id, b.CoverURL)
		}
	}
}

type fakeCoverReader struct {
	calls    atomic.Int64
	capacity int
	mu       sync.Mutex
	seen     map[string]int
}

func (f *fakeCoverReader) Capacity() int { return f.capacity }
func (f *fakeCoverReader) ReadCoverText(_ context.Context, img []byte, mime string) (*ai.CoverTextResult, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.seen[mime]++
	f.mu.Unlock()
	return &ai.CoverTextResult{Text: &covertext.Text{Title: "Read"}, Raw: `{"title":"Read"}`, Model: "vision-model", EndpointID: "pool-a"}, nil
}

func TestCoverTextReadPreviewThenResumable(t *testing.T) {
	st, deps, ids := coverOpFixture(t)
	fr := &fakeCoverReader{capacity: 2, seen: map[string]int{}}
	orig := newCoverTextReader
	newCoverTextReader = func() coverTextReader { return fr }
	t.Cleanup(func() { newCoverTextReader = orig })
	p := New(deps)

	// Preview: no model call, nothing stored.
	if err := p.runCoverTextRead(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	if fr.calls.Load() != 0 {
		t.Fatalf("preview called the model %d times", fr.calls.Load())
	}
	if n, err := st.CountPrefix("cover_text"); err != nil || n != 0 {
		t.Fatalf("preview stored %d cover_text keys (%v)", n, err)
	}

	// Apply: one read per distinct image, records and indexes stored.
	if err := p.runCoverTextRead(context.Background(), json.RawMessage(`{"dry_run":false}`), &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	if fr.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (two distinct folder images)", fr.calls.Load())
	}
	for _, id := range ids {
		got, err := covertext.ForBook(st, id)
		if err != nil || len(got) != 1 || got[0].Source != covertext.SourceFolder || !got[0].Record.Current() ||
			got[0].Record.Model != "vision-model" || got[0].Record.Text.Title != "Read" {
			t.Fatalf("book %s cover text = %+v %v", id, got, err)
		}
		b, _ := st.GetBookByID(id)
		if b.CoverURL != nil {
			t.Fatal("cover-text-read wrote book metadata")
		}
	}

	// Rerun: everything current, nothing re-read.
	if err := p.runCoverTextRead(context.Background(), json.RawMessage(`{"dry_run":false}`), &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	if fr.calls.Load() != 2 {
		t.Fatalf("rerun re-read current images: calls = %d", fr.calls.Load())
	}
}

func TestCoverTextReadRefusesWithoutCapacity(t *testing.T) {
	_, deps, _ := coverOpFixture(t)
	fr := &fakeCoverReader{capacity: 0, seen: map[string]int{}}
	orig := newCoverTextReader
	newCoverTextReader = func() coverTextReader { return fr }
	t.Cleanup(func() { newCoverTextReader = orig })
	err := New(deps).runCoverTextRead(context.Background(), json.RawMessage(`{"dry_run":false}`), &fakeReporter{})
	if err == nil || fr.calls.Load() != 0 {
		t.Fatalf("err = %v calls = %d; want a refusal before any read", err, fr.calls.Load())
	}
}
