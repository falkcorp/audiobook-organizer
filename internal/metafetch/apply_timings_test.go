// file: internal/metafetch/apply_timings_test.go
// version: 1.0.0
// guid: 2f6a9d13-4c85-4e7b-9b20-6d1e8a3c5f47
// last-edited: 2026-09-13

package metafetch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// unreadableTags makes every tag read fail, so FilterUnchangedTags keeps the
// full tag map and every file is written: the test measures the writes, not
// the unchanged-tag filter.
type unreadableTags struct{}

func (unreadableTags) ExtractMetadata(string) (metadata.Metadata, error) {
	return metadata.Metadata{}, errors.New("no tags in this fixture")
}

// dirBookFixture is the production shape measured on 2026-09-13: a book whose
// FilePath is a DIRECTORY of audio files with no book_file rows, so the
// write-back goes through its "write-back is a directory" branch.
func dirBookFixture(t *testing.T, nFiles int) (*Service, *database.Book, []string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "Author", "Armageddon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var files []string
	for i := range nFiles {
		p := filepath.Join(dir, fmt.Sprintf("%02d.mp3", i+1))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}

	saved := config.AppConfig
	t.Cleanup(func() { config.AppConfig = saved })
	config.AppConfig.RootDir = root
	config.AppConfig.AutoRenameOnApply = false
	config.AppConfig.AutoWriteTagsOnApply = false

	metadata.SetMetadataExtractor(unreadableTags{})
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	book := &database.Book{ID: "b1", Title: "Armageddon", FilePath: dir}
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id == book.ID {
				cp := *book
				return &cp, nil
			}
			return nil, nil
		},
	}
	return &Service{db: store}, book, files
}

// countingWriter records every per-file write and the most writers seen at
// once. Each write sleeps so overlapping writers really overlap.
type countingWriter struct {
	mu       sync.Mutex
	written  map[string]int
	inFlight atomic.Int64
	maxSeen  atomic.Int64
}

func (c *countingWriter) write(path string, _ map[string]any) error {
	n := c.inFlight.Add(1)
	for {
		m := c.maxSeen.Load()
		if n <= m || c.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(15 * time.Millisecond)
	c.mu.Lock()
	c.written[path]++
	c.mu.Unlock()
	c.inFlight.Add(-1)
	return nil
}

// TestWriteBackForBook_PerFileWritesConcurrentButGateBounded pins the per-file
// pool: the files of one book are written by several writers at once, each
// file exactly once, and never by more writers than 1 (the caller's own slot)
// plus the gate slots that were free. The gate here has 2 free slots, so at
// most 3 writers may ever run.
func TestWriteBackForBook_PerFileWritesConcurrentButGateBounded(t *testing.T) {
	svc, book, files := dirBookFixture(t, 12)
	cw := &countingWriter{written: map[string]int{}}
	svc.fileTagWrite = cw.write

	gate := make(chan struct{}, 2)
	var slotsTaken atomic.Int64
	svc.SetFileWriteGate(func() (func(), bool) {
		select {
		case gate <- struct{}{}:
			slotsTaken.Add(1)
			return func() { <-gate }, true
		default:
			return nil, false
		}
	})

	pt := NewApplyPhaseTimings()
	written, err := svc.writeBackForBook(book.ID, nil, book.ID, pt)
	if err != nil {
		t.Fatalf("writeBackForBook: %v", err)
	}
	if written != len(files) {
		t.Errorf("written = %d, want %d", written, len(files))
	}
	for _, f := range files {
		if cw.written[f] != 1 {
			t.Errorf("%s written %d times, want exactly 1", filepath.Base(f), cw.written[f])
		}
	}
	if len(cw.written) != len(files) {
		t.Errorf("wrote %d distinct paths, want %d", len(cw.written), len(files))
	}
	if got := cw.maxSeen.Load(); got > 3 {
		t.Errorf("max concurrent writers = %d, want <= 3 (1 own + 2 gate slots)", got)
	} else if got < 2 {
		t.Errorf("max concurrent writers = %d, want >= 2: the per-file writes did not overlap", got)
	}
	if slotsTaken.Load() != 2 {
		t.Errorf("gate slots taken = %d, want 2", slotsTaken.Load())
	}
	if len(gate) != 0 {
		t.Errorf("%d gate slot(s) still held after the write returned", len(gate))
	}
	if pt.Get(PhaseTags) <= 0 {
		t.Error("tags phase not recorded")
	}
}

// With no gate wired the book's files are written one at a time, as before.
func TestWriteBackForBook_NoGateIsSequential(t *testing.T) {
	svc, book, files := dirBookFixture(t, 6)
	cw := &countingWriter{written: map[string]int{}}
	svc.fileTagWrite = cw.write

	written, err := svc.writeBackForBook(book.ID, nil, book.ID, nil)
	if err != nil {
		t.Fatalf("writeBackForBook: %v", err)
	}
	if written != len(files) {
		t.Errorf("written = %d, want %d", written, len(files))
	}
	if got := cw.maxSeen.Load(); got != 1 {
		t.Errorf("max concurrent writers = %d, want 1 with no gate", got)
	}
}

// A gate with no free slot never blocks the book: it is written by the one
// writer the caller already accounts for.
func TestRunFileWrites_FullGateNeverBlocks(t *testing.T) {
	svc := &Service{}
	svc.SetFileWriteGate(func() (func(), bool) { return nil, false })
	var calls atomic.Int64
	done := make(chan struct{})
	go func() {
		svc.runFileWrites(5, func(int) { calls.Add(1) })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runFileWrites blocked on a full gate")
	}
	if calls.Load() != 5 {
		t.Errorf("calls = %d, want 5", calls.Load())
	}
}

// TestFinishApplyFileWorkTimed_EmitsPhaseLine pins the per-book
// "apply phase durations" line: one line per FinishApplyFileWork, naming the
// book and every phase, so the next slow apply says where its time went.
func TestFinishApplyFileWorkTimed_EmitsPhaseLine(t *testing.T) {
	svc, book, _ := dirBookFixture(t, 3)
	cw := &countingWriter{written: map[string]int{}}
	svc.fileTagWrite = cw.write

	var mu sync.Mutex
	var lines []string
	savedSink := applyTimingSink
	applyTimingSink = func(id, line string) {
		mu.Lock()
		lines = append(lines, id+" "+line)
		mu.Unlock()
	}
	t.Cleanup(func() { applyTimingSink = savedSink })

	pt := NewApplyPhaseTimings()
	pt.Add(PhaseGateWait, 7*time.Millisecond)
	if err := svc.FinishApplyFileWorkTimed(book.ID, "", false, true, nil, pt); err != nil {
		t.Fatalf("FinishApplyFileWorkTimed: %v", err)
	}

	if len(lines) != 1 {
		t.Fatalf("got %d phase lines, want exactly 1: %q", len(lines), lines)
	}
	line := lines[0]
	if !strings.HasPrefix(line, "b1 ") {
		t.Errorf("line does not name the book: %q", line)
	}
	for _, p := range phaseOrder {
		if !strings.Contains(line, p+"=") {
			t.Errorf("line missing phase %q: %q", p, line)
		}
	}
	for _, want := range []string{"gate_wait=7ms", "files=3", "total="} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %q: %q", want, line)
		}
	}
	if len(cw.written) != 3 {
		t.Errorf("tag write reached %d files, want 3", len(cw.written))
	}
}

// A nil timer records nothing and logs nothing (untimed callers).
func TestApplyPhaseTimings_NilIsNoop(t *testing.T) {
	var pt *ApplyPhaseTimings
	pt.Add(PhaseTags, time.Second)
	pt.AddFiles(3)
	if pt.Get(PhaseTags) != 0 || pt.String() != "" {
		t.Error("nil timer recorded something")
	}
	called := false
	savedSink := applyTimingSink
	applyTimingSink = func(string, string) { called = true }
	t.Cleanup(func() { applyTimingSink = savedSink })
	logApplyPhaseTimings("x", nil)
	if called {
		t.Error("nil timer logged a line")
	}
}
