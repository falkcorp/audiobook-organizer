// file: internal/database/hnsw_clean_stop_test.go
// version: 1.0.2
// guid: 81e6acdc-f52e-41ad-80ef-a56203095de9
// last-edited: 2026-11-08

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	s := NewHNSWEmbeddingStore(4)
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
func TestCleanStop_MarkerLetsOneBootSkipTheReconcile(t *testing.T) {
	dir := t.TempDir()
	src, truth := exportedRogues(t, dir)
	markCleanStop(t, src, dir)
	if !markerExists(t, dir) {
		t.Fatal("MarkCleanStop reported the marker written but it is not on disk")
	}

	first := NewHNSWEmbeddingStore(4)
	if err := first.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatalf("import: %v", err)
	}
	if first.NeedsReconcile() {
		t.Error("a clean stop with unchanged counts must load as fresh, not reconcile")
	}
	if markerExists(t, dir) {
		t.Error("the boot must take the marker, so it vouches for one boot only")
	}

	second := NewHNSWEmbeddingStore(4)
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

// blockGraphRename makes the next export's rename of the book graph fail, by
// putting a non-empty directory where the graph file goes.
func blockGraphRename(t *testing.T, dir string) {
	t.Helper()
	bin := filepath.Join(dir, "hnsw-book.bin")
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bin, "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestMarkCleanStop_NoMarkerAfterAFailedExport: an export that fails after it
// began changing the directory (its truth sidecars are gone) leaves a snapshot
// that describes no gen, so no marker may vouch for it, whether or not the
// graph changed first.
func TestMarkCleanStop_NoMarkerAfterAFailedExport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty bool
	}{
		{"graph unchanged since the last snapshot", false},
		{"graph changed since the last snapshot", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, truth := exportedRogues(t, dir)
			if tc.dirty {
				if err := s.Delete(context.Background(), "book", "bane"); err != nil {
					t.Fatalf("delete: %v", err)
				}
			}
			blockGraphRename(t, dir)
			if err := s.ExportWithTruth(dir, truth); err == nil {
				t.Fatal("the export was meant to fail at the graph rename")
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

	next := NewHNSWEmbeddingStore(4)
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
	next := NewHNSWEmbeddingStore(4)
	if err := next.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !next.NeedsReconcile() {
		t.Error("a snapshot that lacks a refused write must be reconciled")
	}
}
