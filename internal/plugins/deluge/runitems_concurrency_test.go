// file: internal/plugins/deluge/runitems_concurrency_test.go
// version: 1.0.0
// guid: 7a3d9c15-8e42-4b60-a1f7-d5e2c8b94f03
// last-edited: 2026-10-13

package deluge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
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
	case <-time.After(3 * time.Second):
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

// centralizeFixture builds n synthetic pending rows whose sources sit outside
// the library root, so every row takes the copy path to RootDir/<basename>.
type centralizeFixture struct {
	root  string
	rows  []database.BookFile
	store *database.MockStore

	mu      sync.Mutex
	updated map[string]string // row id -> new path
	probe   *overlapProbe
}

func newCentralizeFixture(t *testing.T, n int) *centralizeFixture {
	t.Helper()
	fx := &centralizeFixture{
		root:    t.TempDir(),
		updated: map[string]string{},
		probe:   newOverlapProbe(2),
	}
	srcDir := t.TempDir()
	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = fx.root
	t.Cleanup(func() { config.AppConfig.RootDir = origRoot })

	for i := range n {
		src := filepath.Join(srcDir, fmt.Sprintf("part%02d.mp3", i))
		if err := os.WriteFile(src, []byte(fmt.Sprintf("synthetic bytes %d", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		fx.rows = append(fx.rows, database.BookFile{
			ID: fmt.Sprintf("f%02d", i), BookID: fmt.Sprintf("b%02d", i), FilePath: src,
		})
	}
	fx.store = &database.MockStore{
		GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) {
			cores := make([]database.BookFileCore, len(fx.rows))
			for i := range fx.rows {
				cores[i] = fx.rows[i].Core()
			}
			return cores, nil
		},
		GetBookFileByIDFunc: func(_, fileID string) (*database.BookFile, error) {
			for i := range fx.rows {
				if fx.rows[i].ID == fileID {
					cp := fx.rows[i]
					return &cp, nil
				}
			}
			return nil, nil
		},
		UpdateBookFileFunc: func(id string, f *database.BookFile) error {
			defer fx.probe.enter()()
			fx.mu.Lock()
			fx.updated[id] = f.FilePath
			fx.mu.Unlock()
			return nil
		},
	}
	return fx
}

// TestRunCentralization_RunsItemsConcurrently: deluge.centralize used to omit
// RunItemsOptions.Concurrency and copied one file at a time. With the pool,
// rows are inside the row update together, every file lands at its library
// path, and the checkpoint the op writes is the completion WATERMARK: it only
// ever moves forward and ends at the row count, which a per-item "my index + 1"
// write cannot promise once items finish out of order. Run with -race.
func TestRunCentralization_RunsItemsConcurrently(t *testing.T) {
	const n = 12
	fx := newCentralizeFixture(t, n)
	p := &Plugin{store: fx.store}
	rep := &recordingReporter{}

	if err := p.runCentralization(context.Background(), nil, rep); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}

	if got := fx.probe.maxInFlight(); got < 2 {
		t.Fatalf("max rows in flight = %d, want >= 2: the op ran on one core", got)
	}
	if len(fx.updated) != n {
		t.Fatalf("%d rows updated, want %d", len(fx.updated), n)
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
	if !strings.Contains(rep.last, fmt.Sprintf("%d succeeded", n)) {
		t.Errorf("summary %q does not report %d succeeded", rep.last, n)
	}
	if len(rep.checkpoints) == 0 {
		t.Fatal("no checkpoint written")
	}
	prev := 0
	for _, cp := range rep.checkpoints {
		if cp.ProcessedFiles < prev {
			t.Errorf("checkpoint went backwards: %d after %d", cp.ProcessedFiles, prev)
		}
		prev = cp.ProcessedFiles
		if cp.TotalFiles != n {
			t.Errorf("checkpoint TotalFiles = %d, want %d", cp.TotalFiles, n)
		}
	}
	if prev != n {
		t.Errorf("final checkpoint ProcessedFiles = %d, want %d", prev, n)
	}
}

// TestRunCentralization_ResumeSkipsCompletedPrefix: a restart hands the stored
// ProcessedFiles back through params; the pool must skip exactly that prefix
// (RunItemsOptions.ResumeFrom) and keep reporting the absolute watermark.
func TestRunCentralization_ResumeSkipsCompletedPrefix(t *testing.T) {
	const n, done = 10, 4
	fx := newCentralizeFixture(t, n)
	p := &Plugin{store: fx.store}
	rep := &recordingReporter{}

	params, err := json.Marshal(centralizationCheckpoint{ProcessedFiles: done, TotalFiles: n})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.runCentralization(context.Background(), params, rep); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}

	if len(fx.updated) != n-done {
		t.Fatalf("%d rows updated, want %d (the first %d are already done)", len(fx.updated), n-done, done)
	}
	for i := range done {
		if _, touched := fx.updated[fx.rows[i].ID]; touched {
			t.Errorf("row %s was in the completed prefix but was processed again", fx.rows[i].ID)
		}
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	if len(rep.checkpoints) == 0 {
		t.Fatal("no checkpoint written")
	}
	if got := rep.checkpoints[len(rep.checkpoints)-1].ProcessedFiles; got != n {
		t.Errorf("final checkpoint ProcessedFiles = %d, want the absolute %d", got, n)
	}
}

// TestRunCentralization_ResumePastEndIsANoOp: a stored position beyond the
// freshly listed rows (rows left the pending list since the checkpoint) used
// to slice out of range; the op now finishes having done nothing.
func TestRunCentralization_ResumePastEndIsANoOp(t *testing.T) {
	fx := newCentralizeFixture(t, 3)
	p := &Plugin{store: fx.store}
	params, _ := json.Marshal(centralizationCheckpoint{ProcessedFiles: 50, TotalFiles: 50})

	if err := p.runCentralization(context.Background(), params, &recordingReporter{}); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}
	if len(fx.updated) != 0 {
		t.Errorf("%d rows updated, want none", len(fx.updated))
	}
}

// TestRunPathUpdate_RunsItemsConcurrently: deluge.path-update sent its
// move_storage RPCs one version at a time. With the pool, several are on the
// wire at once, each version's torrent is moved exactly once, and the counter
// the closure shares is exact. Run with -race.
func TestRunPathUpdate_RunsItemsConcurrently(t *testing.T) {
	const n = 6
	probe := newOverlapProbe(2)
	var mu sync.Mutex
	moved := map[string]string{} // torrent hash -> destination

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int64             `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method == "core.move_storage" && len(req.Params) == 2 {
			defer probe.enter()()
			var hashes []string
			var dest string
			_ = json.Unmarshal(req.Params[0], &hashes)
			_ = json.Unmarshal(req.Params[1], &dest)
			mu.Lock()
			for _, h := range hashes {
				moved[h] = dest
			}
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": true, "error": nil})
	}))
	t.Cleanup(srv.Close)
	client, err := delugeclient.New(srv.URL, "test-password")
	if err != nil {
		t.Fatal(err)
	}

	origMove := config.AppConfig.DelugeMoveEnabled
	config.AppConfig.DelugeMoveEnabled = true
	t.Cleanup(func() { config.AppConfig.DelugeMoveEnabled = origMove })

	versions := make([]database.BookVersion, n)
	for i := range versions {
		versions[i] = database.BookVersion{
			ID:          fmt.Sprintf("v%02d", i),
			BookID:      "book-1",
			Status:      database.BookVersionStatusActive,
			TorrentHash: linkTorrentID(i),
		}
	}
	store := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: "/library/example/book.m4b"}, nil
		},
		GetBookVersionsByBookIDFunc: func(string) ([]database.BookVersion, error) { return versions, nil },
	}
	p := &Plugin{store: store, client: client}

	if err := p.runPathUpdate(context.Background(), json.RawMessage(`{"book_id":"book-1"}`), &recordingReporter{}); err != nil {
		t.Fatalf("runPathUpdate: %v", err)
	}

	if got := probe.maxInFlight(); got < 2 {
		t.Fatalf("max move_storage calls in flight = %d, want >= 2: the op ran one at a time", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(moved) != n {
		t.Fatalf("%d torrents moved, want %d: %v", len(moved), n, moved)
	}
	for i, v := range versions {
		want := filepath.Join("/library/example", ".versions", v.ID)
		if got := moved[linkTorrentID(i)]; got != want {
			t.Errorf("torrent %s moved to %q, want %q", linkTorrentID(i), got, want)
		}
	}
}
