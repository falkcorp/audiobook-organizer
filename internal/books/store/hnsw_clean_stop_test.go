// file: internal/books/store/hnsw_clean_stop_test.go
// version: 1.0.13
// guid: 81e6acdc-f52e-41ad-80ef-a56203095de9
// last-edited: 2026-11-09

package store

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

// R1-15 follow-up (MAJOR-1): the clean-stop marker. Each test below pins one
// guard, so removing that guard fails it.

// markCleanStop writes s's clean-stop marker in dir, as a clean stop does
// after its snapshot, and fails the test if the store refuses.
func markCleanStop(t *testing.T, s *HNSWEmbeddingStore, dir string) {
	t.Helper()
	written, reason, err := s.MarkCleanStop(dir)
	if err != nil {
		t.Fatalf("MarkCleanStop: %v", err)
	}
	if !written {
		t.Fatalf("MarkCleanStop refused: %s", reason)
	}
}

// newBoundHNSW returns an empty store bound to a fresh in-memory database and
// no embedding model, as the server binds the store it boots with: a store
// with no database bound neither writes the marker nor trusts one (R1-15
// round-2 review, S5).
func newBoundHNSW(t *testing.T, dims int) *HNSWEmbeddingStore {
	t.Helper()
	s := NewHNSWEmbeddingStore(dims)
	s.BindTruthSource(verdictCoreDB(t), "")
	return s
}

func markerExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(cleanStopPath(dir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat marker: %v", err)
	}
	return err == nil
}

// cleanStopRogues are the book keys the clean-stop tests start from.
var cleanStopRogues = map[string][]float32{
	"joker":   {1, 0, 0, 0},
	"bane":    {0, 1, 0, 0},
	"riddler": {0, 0, 1, 0},
}

// exportedRogues returns a complete store holding cleanStopRogues, exported
// to dir with a Pebble truth count of 3, and that truth function.
func exportedRogues(t *testing.T, dir string) (*HNSWEmbeddingStore, func(string) (int, error)) {
	t.Helper()
	s := newBoundHNSW(t, 4)
	for id, v := range cleanStopRogues {
		if err := s.Upsert(context.Background(), "book", id, v, nil); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	s.SetComplete(true)
	truth := func(string) (int, error) { return len(cleanStopRogues), nil }
	if err := s.ExportWithTruth(dir, truth); err != nil {
		t.Fatalf("export: %v", err)
	}
	return s, truth
}

// TestCleanStop_MarkerLetsOneBootSkipTheReconcile: after a clean stop the
// next boot takes the snapshot as fresh and consumes the marker, so a boot
// after that (the run crashed, no stop wrote a new marker) reconciles.
// TestCleanStop_ABootWithNoDatabaseBoundReconciles (R1-15 round-2 review, S5):
// a boot whose store has no database bound cannot see the writes made before
// its import, so a marker does not vouch for it. It still takes the marker.
func TestCleanStop_ABootWithNoDatabaseBoundReconciles(t *testing.T) {
	dir := t.TempDir()
	src, truth := exportedRogues(t, dir)
	markCleanStop(t, src, dir)

	next := NewHNSWEmbeddingStore(4)
	if err := next.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !next.NeedsReconcile() {
		t.Error("a boot with no database bound must reconcile whatever the marker says")
	}
	if markerExists(t, dir) {
		t.Error("the boot must take the marker even when it cannot use it")
	}
}

func TestCleanStop_MarkerLetsOneBootSkipTheReconcile(t *testing.T) {
	dir := t.TempDir()
	src, truth := exportedRogues(t, dir)
	markCleanStop(t, src, dir)
	if !markerExists(t, dir) {
		t.Fatal("MarkCleanStop reported the marker written but it is not on disk")
	}

	first := newBoundHNSW(t, 4)
	if err := first.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatalf("import: %v", err)
	}
	if first.NeedsReconcile() {
		t.Error("a clean stop with unchanged counts must load as fresh, not reconcile")
	}
	if markerExists(t, dir) {
		t.Error("the boot must take the marker, so it vouches for one boot only")
	}

	second := newBoundHNSW(t, 4)
	if err := second.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !second.NeedsReconcile() {
		t.Error("a boot after a run that never stopped cleanly must reconcile")
	}
}

// TestCleanStop_NoMarkerReconcilesWhateverTheCount is MAJOR-1 at the store:
// in the crash window "bane" was deleted and "scarecrow" embedded, so the
// Pebble count is still 3, which is what the sidecar recorded. Without the
// marker the snapshot is stale anyway, and the reconcile fixes both rows.
func TestCleanStop_NoMarkerReconcilesWhateverTheCount(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, truth := exportedRogues(t, dir) // the process crashed: no marker

	s := NewHNSWEmbeddingStore(4)
	if err := s.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !s.NeedsReconcile() {
		t.Fatal("an unchanged count without a clean-stop marker must not load as fresh")
	}

	scarecrow := []float32{0, 0, 0, 1}
	rows := []ReconcileRow{
		{ID: "joker", Vector: cleanStopRogues["joker"], Verdict: ReconcileIndex},
		{ID: "riddler", Vector: cleanStopRogues["riddler"], Verdict: ReconcileIndex},
		{ID: "scarecrow", Vector: scarecrow, Verdict: ReconcileIndex},
	}
	report, err := s.Reconcile(ctx, map[string][]ReconcileRow{"book": rows})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := report.ByType["book"]; got.Added != 1 || got.Tombstoned != 1 {
		t.Errorf("reconcile counts = %+v, want 1 added (scarecrow) and 1 tombstoned (bane)", got)
	}
	if m, _ := s.Get(ctx, "book", "bane"); m != nil {
		t.Error("the row deleted in the crash window is still in the graph")
	}
	res, err := s.FindSimilar(ctx, "book", scarecrow, 1, nil)
	if err != nil || len(res) == 0 || res[0].EntityID != "scarecrow" {
		t.Errorf("the row embedded in the crash window is not in the graph: %v, %v", res, err)
	}
}

// TestMarkCleanStop_RefusesAGraphThatChangedSinceItsSnapshot is the
// budget-skipped stop: the graph changed after its last snapshot and no
// export ran, so the snapshot does not describe it.
func TestMarkCleanStop_RefusesAGraphThatChangedSinceItsSnapshot(t *testing.T) {
	dir := t.TempDir()
	s, _ := exportedRogues(t, dir)
	if err := s.Upsert(context.Background(), "book", "scarecrow", []float32{0, 0, 0, 1}, nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	written, reason, err := s.MarkCleanStop(dir)
	if err != nil || written {
		t.Fatalf("MarkCleanStop = %v, %q, %v; want refused", written, reason, err)
	}
	if !strings.Contains(reason, "changed after its last snapshot") {
		t.Errorf("reason = %q", reason)
	}
	if markerExists(t, dir) {
		t.Error("a refused MarkCleanStop left a marker")
	}
}

// blockRename makes the next export's rename onto name (a snapshot file in
// dir) fail, by putting a non-empty directory where that file goes.
func blockRename(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p, "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestMarkCleanStop_NoMarkerAfterAFailedExport: an export that fails after it
// began changing the directory (its truth sidecars are gone, and a failed
// metadata rename follows a graph rename that landed) leaves a snapshot that
// describes no gen, so no marker may vouch for it, whether or not the graph
// changed first. Each rename has its own baseline drop (MINOR-2, M3b).
func TestMarkCleanStop_NoMarkerAfterAFailedExport(t *testing.T) {
	for _, blocked := range []string{"hnsw-book.bin", "hnsw-book.meta.json"} {
		for _, tc := range []struct {
			name  string
			dirty bool
		}{
			{"graph unchanged since the last snapshot", false},
			{"graph changed since the last snapshot", true},
		} {
			t.Run(blocked+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				s, truth := exportedRogues(t, dir)
				if tc.dirty {
					if err := s.Delete(context.Background(), "book", "bane"); err != nil {
						t.Fatalf("delete: %v", err)
					}
				}
				blockRename(t, dir, blocked)
				if err := s.ExportWithTruth(dir, truth); err == nil {
					t.Fatalf("the export was meant to fail at the rename onto %s", blocked)
				}
				written, reason, err := s.MarkCleanStop(dir)
				if err != nil || written {
					t.Fatalf("MarkCleanStop = %v, %q, %v; want refused", written, reason, err)
				}
				if markerExists(t, dir) {
					t.Error("a failed export left a clean-stop marker")
				}
			})
		}
	}
}

// TestCleanStop_AWriteAfterTheMarkerRemovesIt: a writer that outlives the
// stop-time snapshot (a goroutine the stop never waits for) changes the graph after the
// marker, so the marker must go and the next boot must reconcile.
func TestCleanStop_AWriteAfterTheMarkerRemovesIt(t *testing.T) {
	dir := t.TempDir()
	s, truth := exportedRogues(t, dir)
	markCleanStop(t, s, dir)
	if err := s.Upsert(context.Background(), "book", "scarecrow", []float32{0, 0, 0, 1}, nil); err != nil {
		t.Fatalf("upsert after the marker: %v", err)
	}
	if markerExists(t, dir) {
		t.Fatal("the marker survived a write after it")
	}

	next := newBoundHNSW(t, 4)
	if err := next.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !next.NeedsReconcile() {
		t.Error("a snapshot that lacks a write after the stop must be reconciled")
	}
}

// TestImportWithStalenessCheck_TakesTheMarkerEvenWhenTheLoadFails: a boot
// whose snapshot cannot be read hydrates from Pebble and then lets the old
// snapshot fall behind, so the marker must not survive it either.
func TestImportWithStalenessCheck_TakesTheMarkerEvenWhenTheLoadFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(cleanStopPath(dir), []byte("clean stop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewHNSWEmbeddingStore(4)
	if err := s.ImportWithStalenessCheck(dir, func(string) (int, error) { return 0, nil }); !errors.Is(err, ErrNoHNSWSnapshot) {
		t.Fatalf("import = %v, want ErrNoHNSWSnapshot", err)
	}
	if markerExists(t, dir) {
		t.Error("a boot whose load failed left the marker in place")
	}
}

// TestMarkCleanStop_Refusals: MarkCleanStop writes nothing unless this
// process's graph is a complete, undamaged projection it exported to (or
// loaded fresh from) that directory.
func TestMarkCleanStop_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string) *HNSWEmbeddingStore
		want  string
	}{
		{"incomplete graph", func(t *testing.T, dir string) *HNSWEmbeddingStore {
			s := NewHNSWEmbeddingStore(4)
			if err := s.Upsert(context.Background(), "book", "joker", []float32{1, 0, 0, 0}, nil); err != nil {
				t.Fatal(err)
			}
			return s
		}, "not a complete projection"},
		{"snapshot written to another directory", func(t *testing.T, dir string) *HNSWEmbeddingStore {
			s, _ := exportedRogues(t, t.TempDir())
			return s
		}, "wrote no snapshot"},
		{"stale snapshot pending a reconcile", func(t *testing.T, dir string) *HNSWEmbeddingStore {
			exportedRogues(t, dir)
			s := NewHNSWEmbeddingStore(4)
			if err := s.ImportWithStalenessCheck(dir, func(string) (int, error) { return 3, nil }); err != nil {
				t.Fatal(err)
			}
			return s
		}, "still being reconciled"},
		{"damaged graph", func(t *testing.T, dir string) *HNSWEmbeddingStore {
			s, _ := exportedRogues(t, dir)
			s.mu.Lock()
			s.damaged = true
			s.mu.Unlock()
			return s
		}, "damaged"},
		// R1-15 round-2 review (S5): with no database bound nothing counts
		// the writes the graph could have missed, so the store vouches for
		// nothing.
		{"no database bound", func(t *testing.T, dir string) *HNSWEmbeddingStore {
			s, _ := exportedRogues(t, dir)
			s.BindTruthSource(nil, "")
			return s
		}, "no Pebble database is bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := tc.setup(t, dir)
			written, reason, err := s.MarkCleanStop(dir)
			if err != nil || written {
				t.Fatalf("MarkCleanStop = %v, %q, %v; want refused", written, reason, err)
			}
			if !strings.Contains(reason, tc.want) {
				t.Errorf("reason = %q, want it to mention %q", reason, tc.want)
			}
			if markerExists(t, dir) {
				t.Error("a refused MarkCleanStop left a marker")
			}
		})
	}
}

// TestMarkCleanStop_RefusesAfterAWriteTheGraphMissed (R1-15 follow-up review,
// MAJOR-1(C)): the caller wrote the row to Pebble before it mirrored it, so a
// mirror the graph refused (or the engine reports failed) leaves the graph
// short of a row Pebble holds. A refused Upsert changes nothing in the graph,
// so the generation check alone would let the marker vouch.
func TestMarkCleanStop_RefusesAfterAWriteTheGraphMissed(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		write func(s *HNSWEmbeddingStore) error
	}{
		{"a vector of another dimension", func(s *HNSWEmbeddingStore) error {
			return s.Upsert(ctx, "book", "scarecrow", []float32{1, 0}, nil)
		}},
		{"a reserved metadata key", func(s *HNSWEmbeddingStore) error {
			return s.Upsert(ctx, "book", "scarecrow", []float32{0, 0, 0, 1}, map[string]string{hnswTombstoneKey: "1"})
		}},
		{"a failed mirror the engine reports", func(s *HNSWEmbeddingStore) error {
			s.MarkDiverged("chromem upsert book scarecrow: simulated")
			return errors.New("reported")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, _ := exportedRogues(t, dir)
			if err := tc.write(s); err == nil {
				t.Fatal("the write was accepted; the case needs a refused one")
			}
			written, reason, err := s.MarkCleanStop(dir)
			if err != nil || written {
				t.Fatalf("MarkCleanStop = %v, %q, %v; want refused", written, reason, err)
			}
			if !strings.Contains(reason, "missed a write that Pebble holds") {
				t.Errorf("reason = %q", reason)
			}
			if markerExists(t, dir) {
				t.Error("a refused MarkCleanStop left a marker")
			}
		})
	}
}

// TestCleanStop_AWriteTheGraphMissedAfterTheMarkerRemovesIt: a mirror that
// failed after the stop-time marker (a writer the stop left running) removes it, and
// the next boot reconciles.
func TestCleanStop_AWriteTheGraphMissedAfterTheMarkerRemovesIt(t *testing.T) {
	dir := t.TempDir()
	s, truth := exportedRogues(t, dir)
	markCleanStop(t, s, dir)
	if err := s.Upsert(context.Background(), "book", "scarecrow", []float32{1, 0}, nil); err == nil {
		t.Fatal("a vector of another dimension was accepted")
	}
	if markerExists(t, dir) {
		t.Fatal("the marker survived a write the graph refused")
	}
	next := newBoundHNSW(t, 4)
	if err := next.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !next.NeedsReconcile() {
		t.Error("a snapshot that lacks a refused write must be reconciled")
	}
}

// TestDiscardCleanStop_RetriesARemovalOnce (R1-15 follow-up review, NIT-2): a
// removal that fails once (a transient error) is retried, and the marker is
// gone rather than emptied.
func TestDiscardCleanStop_RetriesARemovalOnce(t *testing.T) {
	dir := t.TempDir()
	s, _ := exportedRogues(t, dir)
	markCleanStop(t, s, dir)
	calls := 0
	markerRemove = func(path string) error {
		calls++
		if calls == 1 {
			return errors.New("simulated transient failure")
		}
		return os.Remove(path)
	}
	t.Cleanup(func() { markerRemove = os.Remove })
	if err := discardCleanStop(cleanStopPath(dir)); err != nil {
		t.Fatalf("discardCleanStop: %v", err)
	}
	if calls != 2 {
		t.Errorf("removal attempts = %d, want 2", calls)
	}
	if markerExists(t, dir) {
		t.Error("the marker is still on disk after the retried removal")
	}
}

// TestCleanStop_AMarkerThatCannotBeRemovedIsEmptied (NIT-2): in a directory
// that refuses the unlink, the marker this process wrote is emptied when a
// later write removes it, and so is the marker a boot takes, so neither can
// vouch for a later boot.
func TestCleanStop_AMarkerThatCannotBeRemovedIsEmptied(t *testing.T) {
	vouches := func(t *testing.T, dir string) bool {
		t.Helper()
		rec, _ := readCleanStop(cleanStopPath(dir))
		return rec != nil
	}

	t.Run("a write after the marker", func(t *testing.T) {
		dir := t.TempDir()
		s, truth := exportedRogues(t, dir)
		markCleanStop(t, s, dir)
		readOnlyDir(t, dir)
		if err := s.Upsert(context.Background(), "book", "scarecrow", []float32{0, 0, 0, 1}, nil); err != nil {
			t.Fatalf("upsert after the marker: %v", err)
		}
		if vouches(t, dir) {
			t.Fatal("the marker still vouches after a write it could not be removed for")
		}
		next := newBoundHNSW(t, 4)
		if err := next.ImportWithStalenessCheck(dir, truth); err != nil {
			t.Fatalf("import: %v", err)
		}
		if !next.NeedsReconcile() {
			t.Error("a boot that found an emptied marker must reconcile")
		}
	})

	t.Run("the boot's take", func(t *testing.T) {
		dir := t.TempDir()
		s, _ := exportedRogues(t, dir)
		markCleanStop(t, s, dir)
		readOnlyDir(t, dir)
		rec, err := takeCleanStop(dir)
		if err != nil {
			t.Fatalf("takeCleanStop: %v", err)
		}
		if rec == nil {
			t.Fatal("the boot's take lost a marker that vouched")
		}
		if vouches(t, dir) {
			t.Error("the marker a boot took still vouches for the boot after it")
		}
	})
}

// writeMarker writes a marker that vouches into dir, creating dir.
func writeMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cleanStopPath(dir), []byte(`{"version":1,"model":"bge-m3"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPebbleStoreOpen_TakesTheCleanStopMarker (R1-15 follow-up review,
// MINOR-1): opening a database on disk takes the marker in the snapshot
// directory beside it, and the import pops what the open took, once. An
// in-memory database, and an open after DisableHNSWCleanStopClaim, leave it.
func TestPebbleStoreOpen_TakesTheCleanStopMarker(t *testing.T) {
	t.Run("a database on disk", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
		dir := HNSWSnapshotDir(dbPath)
		writeMarker(t, dir)
		ps, err := NewPebbleStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ps.Close() })
		if markerExists(t, dir) {
			t.Fatal("the open left the marker on disk")
		}
		s := NewHNSWEmbeddingStore(4)
		s.BindTruthSource(ps.DB(), "bge-m3")
		rec, err := s.takeCleanStopForImport(dir)
		if err != nil || rec == nil || rec.Model != "bge-m3" {
			t.Fatalf("the import's marker = %+v, %v; want the one the open took", rec, err)
		}
		if rec, err := s.takeCleanStopForImport(dir); err != nil || rec != nil {
			t.Errorf("a second import got %+v, %v; the open's marker is popped once", rec, err)
		}
	})

	t.Run("an import from another directory", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
		writeMarker(t, HNSWSnapshotDir(dbPath))
		ps, err := NewPebbleStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ps.Close() })
		s := NewHNSWEmbeddingStore(4)
		s.BindTruthSource(ps.DB(), "bge-m3")
		if rec, err := s.takeCleanStopForImport(t.TempDir()); err != nil || rec != nil {
			t.Errorf("an import from a directory the open did not take from got %+v, %v; want that directory's own (none)", rec, err)
		}
	})

	t.Run("an in-memory database", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
		dir := HNSWSnapshotDir(dbPath)
		writeMarker(t, dir)
		ps, err := NewPebbleStoreInMemory(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ps.Close() })
		if !markerExists(t, dir) {
			t.Error("an in-memory open took a marker on disk")
		}
	})

	t.Run("claims turned off", func(t *testing.T) {
		t.Cleanup(DisableHNSWCleanStopClaim())
		dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
		dir := HNSWSnapshotDir(dbPath)
		writeMarker(t, dir)
		ps, err := NewPebbleStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ps.Close() })
		if !markerExists(t, dir) {
			t.Error("an open with claims turned off took the marker")
		}
	})
}

// TestPopCleanStop_WarnsOnADirectoryMismatch (R1-15 round-2 review, N3): the
// open took the marker from one directory and the import reads another, so the
// claim is discarded. That costs every later boot its fast path, so it is
// logged, naming both directories; a pop that matches, or finds no claim, is
// silent.
func TestPopCleanStop_WarnsOnADirectoryMismatch(t *testing.T) {
	cases := []struct {
		name      string
		claimFrom string // "" leaves no claim
		popFrom   string
		wantOK    bool
		wantWarn  bool
	}{
		{name: "another directory", claimFrom: "gotham/hnsw", popFrom: "arkham/hnsw", wantWarn: true},
		{name: "the same directory", claimFrom: "gotham/hnsw", popFrom: "gotham/hnsw", wantOK: true},
		{name: "no claim", popFrom: "arkham/hnsw"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := logtest.Capture(t, slog.LevelWarn) // not parallel: swaps slog.Default
			d := verdictCoreDB(t)
			if tc.claimFrom != "" {
				d.cleanStop.Store(&cleanStopClaim{dir: cleanStopDir(tc.claimFrom), rec: &cleanStopRecord{Version: 1, Model: "bge-m3"}})
			}
			rec, ok := d.popCleanStop(tc.popFrom)
			if ok != tc.wantOK || (rec != nil) != tc.wantOK {
				t.Errorf("popCleanStop(%q) = %+v, %v; want ok %v", tc.popFrom, rec, ok, tc.wantOK)
			}
			if d.cleanStop.Load() != nil {
				t.Error("the pop left the claim in place")
			}
			lines := buf.Lines("clean-stop marker")
			if !tc.wantWarn {
				if len(lines) != 0 {
					t.Errorf("a pop that is not a mismatch logged %q", lines)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("want one warning, got %q (all output: %q)", lines, buf.String())
			}
			for _, want := range []string{"level=WARN", tc.claimFrom, tc.popFrom} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("the warning %q does not contain %q", lines[0], want)
				}
			}
		})
	}
}

// TestOpenStandalone_TakesTheMarkerBeforePrepare: the standalone opener
// takes the marker once core's Open has succeeded, so an open that fails
// there (another process holds the database) leaves it, and the store keeps
// what it took. A books migration that ran after the take voids it: the step
// wrote through a store of its own, and nothing this store counts saw that.
func TestOpenStandalone_TakesTheMarkerBeforePrepare(t *testing.T) {
	t.Run("an open another process holds leaves it", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
		holder, err := NewPebbleStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = holder.Close() })
		dir := HNSWSnapshotDir(dbPath)
		writeMarker(t, dir)
		if second, err := NewPebbleStore(dbPath); err == nil {
			_ = second.Close()
			t.Fatal("a second open of a held database succeeded: the test proves nothing")
		}
		if !markerExists(t, dir) {
			t.Error("an open that core refused took the marker")
		}
	})

	for name, tc := range map[string]struct {
		rewind  bool
		vouches bool
	}{
		"no migration runs: the claim vouches": {rewind: false, vouches: true},
		"a books migration runs: it is voided": {rewind: true, vouches: false},
	} {
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
			first, err := NewPebbleStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if tc.rewind {
				rewindToR0(t, dbPath, LegacyCap, LegacyCap)
			}
			dir := HNSWSnapshotDir(dbPath)
			writeMarker(t, dir)
			ps, err := NewPebbleStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ps.Close() })
			if markerExists(t, dir) {
				t.Fatal("the open left the marker on disk")
			}
			rec, ok := ps.DB().popCleanStop(dir)
			if !ok {
				t.Fatal("the store holds no claim for the snapshot directory")
			}
			if got := rec != nil; got != tc.vouches {
				t.Errorf("the store's claim vouches = %v, want %v", got, tc.vouches)
			}
		})
	}

	// R1-25 review S-1: a marker that may still vouch fails the open, which
	// closes the database it opened, so the next open, once the marker is
	// gone, takes the lock.
	t.Run("a marker that may still vouch fails the open", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
		first, err := NewPebbleStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		dir := HNSWSnapshotDir(dbPath)
		if err := os.MkdirAll(filepath.Join(cleanStopPath(dir), "riddle"), 0o700); err != nil {
			t.Fatal(err)
		}
		if ps, err := NewPebbleStore(dbPath); !errors.Is(err, ErrCleanStopNotDiscarded) {
			if err == nil {
				_ = ps.Close()
			}
			t.Fatalf("NewPebbleStore = %v; want ErrCleanStopNotDiscarded", err)
		}
		if err := os.RemoveAll(cleanStopPath(dir)); err != nil {
			t.Fatal(err)
		}
		ps, err := NewPebbleStore(dbPath)
		if err != nil {
			t.Fatalf("the open after the marker was cleared: %v; the failed open kept the database", err)
		}
		_ = ps.Close()
	})
}

// TestClaimHNSWCleanStop is the served app's take, in books' Register: the
// marker leaves the disk at the take, the store's import gets it for the
// snapshot directory however the database path was spelled, a void reason
// (a migration, a swap) still takes it off the disk but leaves the import
// nothing that vouches, and with claims off nothing is taken. A marker that
// may still vouch fails the take, voided or not, and the store holds no
// claim (R1-25 review S-1): Register fails the boot on it.
func TestClaimHNSWCleanStop(t *testing.T) {
	store := func(t *testing.T) *PebbleStore {
		t.Helper()
		ps, err := NewPebbleStoreInMemory("served")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ps.Close() })
		return ps
	}

	// The opener resolves the database path; the server derives the import's
	// directory from the configured one, which may be relative. {wd} is the
	// working directory.
	for _, tc := range []struct{ claim, pop string }{
		{"audiobooks.pebble", "hnsw"},
		{"{wd}/audiobooks.pebble", "hnsw"},
		{"audiobooks.pebble", "{wd}/hnsw"},
		{"./data/../audiobooks.pebble", "./hnsw"},
	} {
		t.Run("claimed at "+tc.claim+", imported from "+tc.pop, func(t *testing.T) {
			wd := t.TempDir()
			t.Chdir(wd)
			in := func(p string) string { return strings.ReplaceAll(p, "{wd}", wd) }
			writeMarker(t, filepath.Join(wd, "hnsw"))
			ps := store(t)
			if err := ps.ClaimHNSWCleanStop(in(tc.claim), ""); err != nil {
				t.Fatal(err)
			}
			if markerExists(t, filepath.Join(wd, "hnsw")) {
				t.Fatal("the take left the marker on disk")
			}
			rec, ok := ps.DB().popCleanStop(in(tc.pop))
			if !ok || rec == nil || rec.Model != "bge-m3" {
				t.Errorf("the import got %+v, %v; want the marker the take found", rec, ok)
			}
		})
	}

	for _, void := range []string{HNSWCleanStopVoidMigrated, "the pre-boot swap replaced the store"} {
		t.Run("voided: "+void, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
			dir := HNSWSnapshotDir(dbPath)
			writeMarker(t, dir)
			ps := store(t)
			if err := ps.ClaimHNSWCleanStop(dbPath, void); err != nil {
				t.Fatal(err)
			}
			if markerExists(t, dir) {
				t.Error("a voided take left the marker on disk")
			}
			rec, ok := ps.DB().popCleanStop(dir)
			if !ok || rec != nil {
				t.Errorf("after a void the import got %+v, %v; want a claim that does not vouch", rec, ok)
			}
		})
	}

	t.Run("claims turned off", func(t *testing.T) {
		t.Cleanup(DisableHNSWCleanStopClaim())
		dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
		dir := HNSWSnapshotDir(dbPath)
		writeMarker(t, dir)
		ps := store(t)
		if err := ps.ClaimHNSWCleanStop(dbPath, ""); err != nil {
			t.Fatal(err)
		}
		if !markerExists(t, dir) {
			t.Error("a take with claims off removed the marker")
		}
		if _, ok := ps.DB().popCleanStop(dir); ok {
			t.Error("a take with claims off left the store holding a claim")
		}
	})

	// A non-empty directory in the marker's place can be neither removed nor
	// emptied: it stays on disk, so it may still vouch.
	for name, void := range map[string]string{"not voided": "", "voided": HNSWCleanStopVoidMigrated} {
		t.Run("a marker that may still vouch, "+name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
			dir := HNSWSnapshotDir(dbPath)
			if err := os.MkdirAll(filepath.Join(cleanStopPath(dir), "riddle"), 0o700); err != nil {
				t.Fatal(err)
			}
			ps := store(t)
			if err := ps.ClaimHNSWCleanStop(dbPath, void); !errors.Is(err, ErrCleanStopNotDiscarded) {
				t.Fatalf("ClaimHNSWCleanStop = %v; want ErrCleanStopNotDiscarded", err)
			}
			if _, ok := ps.DB().popCleanStop(dir); ok {
				t.Error("a take that failed left the store holding a claim")
			}
		})
	}
}

// TestDisableHNSWCleanStopClaim_RestoreTurnsClaimsBackOn (R1-15 round-2
// review, N5): the switch lasts until its restore runs, so a caller that
// defers it cannot leave claims off for the rest of the process, and nested
// calls unwind to the setting from before each one.
func TestDisableHNSWCleanStopClaim_RestoreTurnsClaimsBackOn(t *testing.T) {
	if hnswCleanStopClaimOff.Load() {
		t.Fatal("claims are off before the test: an earlier test leaked the switch")
	}
	outer := DisableHNSWCleanStopClaim()
	inner := DisableHNSWCleanStopClaim()
	inner()
	if !hnswCleanStopClaimOff.Load() {
		t.Error("the inner restore turned claims on while the outer call still holds them off")
	}
	outer()
	if hnswCleanStopClaimOff.Load() {
		t.Fatal("the outer restore left claims off")
	}

	// An open after the restore takes the marker again.
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	dir := HNSWSnapshotDir(dbPath)
	writeMarker(t, dir)
	ps, err := NewPebbleStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if markerExists(t, dir) {
		t.Error("an open after the restore left the marker on disk")
	}
}

// TestTakeHNSWCleanStop_SaysWhetherAMarkerStays (R1-15 round-6 verification
// review, NIT): the restore handler fails before its first write when a
// marker may still vouch, so TakeHNSWCleanStop's error says which errors
// those are (ErrCleanStopNotDiscarded). A marker that did not parse vouched
// for nothing and is gone: an error, but not that one.
func TestTakeHNSWCleanStop_SaysWhetherAMarkerStays(t *testing.T) {
	t.Run("a marker that does not parse", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(cleanStopPath(dir), []byte("the joker was here"), 0o644); err != nil {
			t.Fatal(err)
		}
		vouched, err := TakeHNSWCleanStop(dir)
		if vouched || err == nil {
			t.Fatalf("TakeHNSWCleanStop = %v, %v; want false and the parse error", vouched, err)
		}
		if errors.Is(err, ErrCleanStopNotDiscarded) {
			t.Errorf("a marker that was removed is reported as staying: %v", err)
		}
		if markerExists(t, dir) {
			t.Error("the marker is still on disk")
		}
	})
	t.Run("a marker that can be neither removed nor emptied", func(t *testing.T) {
		dir := t.TempDir()
		writeMarker(t, dir)
		if err := os.Chmod(cleanStopPath(dir), 0o444); err != nil {
			t.Fatal(err)
		}
		readOnlyDir(t, dir)
		vouched, err := TakeHNSWCleanStop(dir)
		if vouched {
			t.Error("a marker that stays is reported as vouching for this run")
		}
		if !errors.Is(err, ErrCleanStopNotDiscarded) {
			t.Fatalf("TakeHNSWCleanStop error = %v; want ErrCleanStopNotDiscarded", err)
		}
	})
	t.Run("a directory it may not search", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root searches any directory")
		}
		dir := t.TempDir()
		writeMarker(t, dir)
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		_, err := TakeHNSWCleanStop(dir)
		if !errors.Is(err, ErrCleanStopNotDiscarded) {
			t.Fatalf("TakeHNSWCleanStop error = %v; want ErrCleanStopNotDiscarded", err)
		}
		// The remedy, as the discard variant gives one (R1-25 follow-up N-4).
		for _, want := range []string{"could not be looked up", "the user the service runs as", "delete the marker by hand", "run again"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("TakeHNSWCleanStop error = %v; want it to say %q", err, want)
			}
		}
	})
}
