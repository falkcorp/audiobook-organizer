// file: internal/plugins/deluge/runitems_concurrency_test.go
// version: 1.2.0
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
	totals      map[int]bool // every progress denominator the op reported
}

func (r *recordingReporter) UpdateProgress(_, total int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = msg
	if r.totals == nil {
		r.totals = map[int]bool{}
	}
	r.totals[total] = true
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
	inFlight   map[string]bool   // row ids between hydrate and update
	violations []string          // two connected files in flight together
	fail       map[string]bool   // row ids whose hydrate read fails
	probe      *overlapProbe
}

func newCentralizeFixture(t *testing.T, books, perBook int) *centralizeFixture {
	t.Helper()
	fx := &centralizeFixture{
		root:     t.TempDir(),
		updated:  map[string]string{},
		inFlight: map[string]bool{},
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
			i := rowIdx(fileID)
			failing := fx.fail[fileID]
			if !failing && i >= 0 {
				// Two files of one book, or two files of one torrent, must
				// never be between hydrate and update together.
				for other := range fx.inFlight {
					o := fx.rows[rowIdx(other)]
					if o.BookID == fx.rows[i].BookID ||
						(o.DelugeHash != "" && o.DelugeHash == fx.rows[i].DelugeHash) {
						fx.violations = append(fx.violations, other+" with "+fileID)
					}
				}
				fx.inFlight[fileID] = true
			}
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
			delete(fx.inFlight, id)
			fx.updated[id] = f.FilePath
			now := time.Now()
			fx.rows[rowIdx(id)].ImportedFromDelugeAt = &now
			return nil
		},
	}
	return fx
}

// shareTorrent gives the named rows one Deluge torrent hash.
func (fx *centralizeFixture) shareTorrent(hash string, ids ...string) {
	for _, id := range ids {
		for i := range fx.rows {
			if fx.rows[i].ID == id {
				fx.rows[i].DelugeHash = hash
			}
		}
	}
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
		t.Errorf("connected files ran together (%v): the pool must partition by book and torrent", fx.violations)
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
	if len(rep.checkpoints) == 0 || len(rep.checkpoints) > books*perBook {
		t.Fatalf("%d checkpoints, want between 1 and one per file (%d)", len(rep.checkpoints), books*perBook)
	}
	prev := 0
	for _, cp := range rep.checkpoints {
		if cp.Completed <= prev {
			t.Errorf("checkpoint count did not move forward: %d after %d", cp.Completed, prev)
		}
		prev = cp.Completed
		if cp.Total != books*perBook {
			t.Errorf("checkpoint Total = %d, want %d", cp.Total, books*perBook)
		}
	}
	// One progress unit from Start to Done: the pool's groups (here a book
	// each) plus sdk.Progress's start and done steps, never files for some
	// frames and groups for others. The only other denominator is the
	// "loading" frame sent before the count is known.
	for total := range rep.totals {
		if total != books+2 && total != 2 {
			t.Errorf("progress denominator %d, want %d (groups + 2) or the initial loading frame", total, books+2)
		}
	}
	if !rep.totals[books+2] {
		t.Errorf("no progress frame used the groups denominator %d: %v", books+2, rep.totals)
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

// livenessReporter counts every call that stamps the registry's stuck-op
// clock: UpdateProgress (what RunItems calls once per finished group) and
// TouchLiveness. The watchdog reads nothing else; Checkpoint does not move it.
type livenessReporter struct {
	recordingReporter
	progress atomic.Int64
	touches  atomic.Int64
}

func (r *livenessReporter) UpdateProgress(c, t int, msg string) error {
	r.progress.Add(1)
	return r.recordingReporter.UpdateProgress(c, t, msg)
}
func (r *livenessReporter) TouchLiveness() { r.touches.Add(1) }

// TestRunCentralization_StampsLivenessPerFile: one book whose files are slow
// to copy must not go quiet for the length of the whole book. RunItems stamps
// progress once per finished group, so the op stamps TouchLiveness after every
// file; otherwise a group that takes longer than the watchdog's ProgressTimeout
// times out a healthy op (and a timed-out op does not resume).
func TestRunCentralization_StampsLivenessPerFile(t *testing.T) {
	const files = 30
	fx := newCentralizeFixture(t, 1, files)
	fx.probe = newOverlapProbe(1) // one group: nothing to overlap with, do not park
	p := &Plugin{store: fx.store}
	rep := &livenessReporter{}

	if err := p.runCentralization(context.Background(), nil, rep); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}
	if len(fx.updated) != files {
		t.Fatalf("%d of %d files centralized", len(fx.updated), files)
	}
	if got := rep.touches.Load(); got != files {
		t.Errorf("TouchLiveness called %d times for %d files in one group, want one per file", got, files)
	}
}

// TestRunCentralization_FilesOfOneTorrentNeverRunTogether: two books can hold
// files of the same Deluge torrent, and a move of that torrent's storage races
// a sibling's copy. Books are connected through a shared hash, and a book
// holding two hashes joins both. Books 0+1 share one torrent; book 2 holds
// hashes A and B while book 3 holds B, so 2+3 are one component; the rest are
// independent so the pool still overlaps.
func TestRunCentralization_FilesOfOneTorrentNeverRunTogether(t *testing.T) {
	const books, perBook = 10, 3
	fx := newCentralizeFixture(t, books, perBook)
	id := func(b, f int) string { return fmt.Sprintf("f%02d-%02d", b, f) }
	fx.shareTorrent("aaaa000000000000000000000000000000000001", id(0, 0), id(0, 1), id(0, 2), id(1, 0), id(1, 1), id(1, 2))
	fx.shareTorrent("bbbb000000000000000000000000000000000002", id(2, 0), id(2, 1))
	fx.shareTorrent("cccc000000000000000000000000000000000003", id(2, 2), id(3, 0), id(3, 1), id(3, 2))
	p := &Plugin{store: fx.store}

	if err := p.runCentralization(context.Background(), nil, &recordingReporter{}); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}
	if len(fx.updated) != books*perBook {
		t.Fatalf("%d of %d files centralized", len(fx.updated), books*perBook)
	}
	if len(fx.violations) != 0 {
		t.Errorf("connected files ran together: %v", fx.violations)
	}
	if got := fx.probe.maxInFlight(); got < 2 {
		t.Errorf("max files in flight = %d, want >= 2: independent groups should still overlap", got)
	}
}

// TestConnectedFileGroups pins the partition itself: connected components of
// "same book or same torrent hash", first-appearance order, hash compared
// without regard to case, and a file with no hash joined only by its book.
func TestConnectedFileGroups(t *testing.T) {
	f := func(id, book, hash string) *database.BookFileCore {
		return &database.BookFileCore{ID: id, BookID: book, DelugeHash: hash}
	}
	files := []*database.BookFileCore{
		f("1", "a", "H1"),
		f("2", "b", ""),
		f("3", "c", "h1"), // same torrent as file 1, different case
		f("4", "b", ""),
		f("5", "d", "H2"),
		f("6", "e", "H2"), // joins d
		f("7", "d", "H3"),
		f("8", "f", "H3"), // joins d and e through d
		f("9", "g", ""),
	}
	var got [][]string
	for _, g := range connectedFileGroups(files) {
		var ids []string
		for _, bf := range g {
			ids = append(ids, bf.ID)
		}
		got = append(got, ids)
	}
	want := [][]string{{"1", "3"}, {"2", "4"}, {"5", "6", "7", "8"}, {"9"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}
