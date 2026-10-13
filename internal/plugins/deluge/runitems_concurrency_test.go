// file: internal/plugins/deluge/runitems_concurrency_test.go
// version: 1.1.0
// guid: 7a3d9c15-8e42-4b60-a1f7-d5e2c8b94f03
// last-edited: 2026-10-13

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// overlapProbe proves that a pool really ran items side by side. Each caller
// of enter parks until `want` callers are inside at once (or the timeout
// passes), so a sequential run never reaches `want` and maxInFlight stays 1.
type overlapProbe struct {
	want  int32
	in    atomic.Int32
	max   atomic.Int32
	ready chan struct{}
	once  sync.Once
}

func newOverlapProbe(want int) *overlapProbe {
	return &overlapProbe{want: int32(want), ready: make(chan struct{})}
}

func (o *overlapProbe) enter() (leave func()) {
	n := o.in.Add(1)
	for {
		m := o.max.Load()
		if n <= m || o.max.CompareAndSwap(m, n) {
			break
		}
	}
	if n >= o.want {
		o.once.Do(func() { close(o.ready) })
	}
	select {
	case <-o.ready:
	case <-time.After(250 * time.Millisecond):
	}
	return func() { o.in.Add(-1) }
}

func (o *overlapProbe) maxInFlight() int { return int(o.max.Load()) }

// recordingReporter is a goroutine-safe reporter that keeps every checkpoint
// the op wrote and the last progress message.
type recordingReporter struct {
	centralizationTestReporter
	mu          sync.Mutex
	checkpoints []centralizationCheckpoint
	last        string
}

func (r *recordingReporter) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = msg
	return nil
}

func (r *recordingReporter) Checkpoint(state any) error {
	if cp, ok := state.(centralizationCheckpoint); ok {
		r.mu.Lock()
		r.checkpoints = append(r.checkpoints, cp)
		r.mu.Unlock()
	}
	return nil
}

func (r *recordingReporter) Logger() *slog.Logger { return slog.Default() }

// centralizeFixture builds synthetic pending rows, perBook files for each of
// `books` books, whose sources sit outside the library root so every row takes
// the copy path to RootDir/<basename>. Like the real pending query, the list
// SHRINKS: a row leaves it once its update marks it imported.
type centralizeFixture struct {
	root  string
	rows  []database.BookFile
	store *database.MockStore

	mu         sync.Mutex
	updated    map[string]string // row id -> new path
	inFlight   map[string]int    // book id -> files between hydrate and update
	violations []string          // two files of one book in flight together
	fail       map[string]bool   // row ids whose hydrate read fails
	probe      *overlapProbe
}

func newCentralizeFixture(t *testing.T, books, perBook int) *centralizeFixture {
	t.Helper()
	fx := &centralizeFixture{
		root:     t.TempDir(),
		updated:  map[string]string{},
		inFlight: map[string]int{},
		fail:     map[string]bool{},
		probe:    newOverlapProbe(2),
	}
	srcDir := t.TempDir()
	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = fx.root
	t.Cleanup(func() { config.AppConfig.RootDir = origRoot })

	for b := range books {
		for f := range perBook {
			name := fmt.Sprintf("b%02d-part%02d.mp3", b, f)
			src := filepath.Join(srcDir, name)
			if err := os.WriteFile(src, []byte("synthetic bytes "+name), 0o644); err != nil {
				t.Fatal(err)
			}
			fx.rows = append(fx.rows, database.BookFile{
				ID: fmt.Sprintf("f%02d-%02d", b, f), BookID: fmt.Sprintf("b%02d", b), FilePath: src,
			})
		}
	}
	rowIdx := func(id string) int {
		for i := range fx.rows {
			if fx.rows[i].ID == id {
				return i
			}
		}
		return -1
	}
	fx.store = &database.MockStore{
		GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			var cores []database.BookFileCore
			for i := range fx.rows {
				if fx.rows[i].ImportedFromDelugeAt == nil {
					cores = append(cores, fx.rows[i].Core())
				}
			}
			return cores, nil
		},
		GetBookFileByIDFunc: func(bookID, fileID string) (*database.BookFile, error) {
			fx.mu.Lock()
			fx.inFlight[bookID]++
			if fx.inFlight[bookID] > 1 {
				fx.violations = append(fx.violations, bookID)
			}
			failing := fx.fail[fileID]
			if failing {
				fx.inFlight[bookID]--
			}
			i := rowIdx(fileID)
			fx.mu.Unlock()
			if failing {
				return nil, errors.New("synthetic read failure")
			}
			if i < 0 {
				return nil, nil
			}
			cp := fx.rows[i]
			return &cp, nil
		},
		UpdateBookFileFunc: func(id string, f *database.BookFile) error {
			// Parks until a second file is in the update at once, so a pool
			// that ran siblings together would be caught between hydrate and
			// here.
			defer fx.probe.enter()()
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.inFlight[f.BookID]--
			fx.updated[id] = f.FilePath
			now := time.Now()
			fx.rows[rowIdx(id)].ImportedFromDelugeAt = &now
			return nil
		},
	}
	return fx
}

func (fx *centralizeFixture) markImported(n int) {
	now := time.Now()
	for i := range n {
		fx.rows[i].ImportedFromDelugeAt = &now
	}
}

// TestRunCentralization_RunsItemsConcurrently: deluge.centralize used to omit
// RunItemsOptions.Concurrency and copied one file at a time. The pool's unit is
// a BOOK: books overlap, a book's own files never do, every file lands at its
// library path, and a mix of failing rows leaves exact error counts and a
// LastError in the checkpoint (the mutex-guarded state). Run with -race.
func TestRunCentralization_RunsItemsConcurrently(t *testing.T) {
	const books, perBook = 24, 4
	fx := newCentralizeFixture(t, books, perBook)
	// Every third file fails its hydrate read, so many workers set LastError
	// while others write the checkpoint that reads it: the mutex-guarded state
	// is exercised, not just present.
	failing := 0
	for i := range fx.rows {
		if i%3 == 1 {
			fx.fail[fx.rows[i].ID] = true
			failing++
		}
	}
	p := &Plugin{store: fx.store}
	rep := &recordingReporter{}

	if err := p.runCentralization(context.Background(), nil, rep); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}

	if got := fx.probe.maxInFlight(); got < 2 {
		t.Fatalf("max files in flight = %d, want >= 2: the op ran on one core", got)
	}
	if len(fx.violations) != 0 {
		t.Errorf("files of one book ran together (books %v): the pool must partition by book", fx.violations)
	}
	wantUpdated := books*perBook - failing
	if len(fx.updated) != wantUpdated {
		t.Fatalf("%d rows updated, want %d", len(fx.updated), wantUpdated)
	}
	for id, path := range fx.updated {
		if filepath.Dir(path) != fx.root {
			t.Errorf("row %s repointed to %s, want a file directly under %s", id, path, fx.root)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("row %s: copy missing: %v", id, err)
		}
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	if want := fmt.Sprintf("%d succeeded", wantUpdated); !strings.Contains(rep.last, want) {
		t.Errorf("summary %q does not report %q", rep.last, want)
	}
	if want := fmt.Sprintf("%d errors", failing); !strings.Contains(rep.last, want) {
		t.Errorf("summary %q does not report %q", rep.last, want)
	}
	if len(rep.checkpoints) != books*perBook {
		t.Fatalf("%d checkpoints, want one per file (%d)", len(rep.checkpoints), books*perBook)
	}
	prev := 0
	for _, cp := range rep.checkpoints {
		if cp.Completed < prev {
			t.Errorf("checkpoint count went backwards: %d after %d", cp.Completed, prev)
		}
		prev = cp.Completed
		if cp.Total != books*perBook {
			t.Errorf("checkpoint Total = %d, want %d", cp.Total, books*perBook)
		}
	}
	last := rep.checkpoints[len(rep.checkpoints)-1]
	if last.Completed != books*perBook {
		t.Errorf("final checkpoint Completed = %d, want %d", last.Completed, books*perBook)
	}
	if !strings.HasPrefix(last.LastError, "hydrate book file ") {
		t.Errorf("final checkpoint LastError = %q, want the hydrate failure", last.LastError)
	}
}

// TestRunCentralization_IgnoresAStaleCheckpoint: the pending query IS the
// resume state. The first four of ten rows were imported before a restart
// (they have left the pending list), and the params still carry an old
// "processed_files": 4. A run that treated that as an index into the freshly
// listed rows would skip four of the six that are still pending. All six run.
func TestRunCentralization_IgnoresAStaleCheckpoint(t *testing.T) {
	fx := newCentralizeFixture(t, 10, 1)
	fx.markImported(4)
	p := &Plugin{store: fx.store}

	params := json.RawMessage(`{"processed_files":4,"total_files":10,"last_book_file_id":"f03-00"}`)
	if err := p.runCentralization(context.Background(), params, &recordingReporter{}); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}
	if len(fx.updated) != 6 {
		var got []string
		for id := range fx.updated {
			got = append(got, id)
		}
		sort.Strings(got)
		t.Fatalf("%d of the 6 pending rows were processed: %v", len(fx.updated), got)
	}
	for i := 4; i < 10; i++ {
		if _, ok := fx.updated[fx.rows[i].ID]; !ok {
			t.Errorf("pending row %s was skipped", fx.rows[i].ID)
		}
	}
}

// TestRunCentralization_StalePositionPastTheEnd: a stored position beyond the
// pending list is simply ignored; all three pending rows run.
func TestRunCentralization_StalePositionPastTheEnd(t *testing.T) {
	fx := newCentralizeFixture(t, 3, 1)
	p := &Plugin{store: fx.store}
	params := json.RawMessage(`{"processed_files":50,"total_files":50}`)

	if err := p.runCentralization(context.Background(), params, &recordingReporter{}); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}
	if len(fx.updated) != 3 {
		t.Errorf("%d rows updated, want all 3", len(fx.updated))
	}
}
