// file: internal/database/hnsw_clean_stop.go
// version: 1.0.1
// guid: dd04968f-f35c-456d-830f-87bd3ce9b0dd
// last-edited: 2026-11-08

package database

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The clean-stop marker (R1-15 follow-up, MAJOR-1). The truth sidecars let the
// boot see a snapshot whose Pebble row counts moved since it was written, but
// a crash can change the rows and leave every count as it was: an add and a
// delete, a re-embed of a key, a merge or primary change that flips a row's
// hydrate verdict. Such a snapshot used to load as fresh, and the divergence
// was permanent. The marker closes that gap: a process writes it only at the
// end of a clean stop, when the snapshot on disk is exactly the graph it held,
// and every boot takes it. A boot that finds no marker reconciles the snapshot
// against Pebble whatever the counts say.

// hnswCleanStopFile is the marker's name inside the snapshot directory.
const hnswCleanStopFile = "hnsw.clean-stop"

func cleanStopPath(dir string) string { return filepath.Join(dir, hnswCleanStopFile) }

// fsyncDir fsyncs a directory, so a file created in it, renamed into it or
// removed from it stays that way after a crash.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s for fsync: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		return errors.Join(fmt.Errorf("fsync %s: %w", dir, err), d.Close())
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close %s after fsync: %w", dir, err)
	}
	return nil
}

// removeDurably removes path and fsyncs its directory. A path already gone is
// not an error.
func removeDurably(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return fsyncDir(filepath.Dir(path))
}

// bumpGenLocked counts one mutation of graphs/meta; the caller holds s.mu for
// writing. Every mutation goes through it.
//
// The first mutation after MarkCleanStop wrote its marker also removes the
// marker, because the snapshot on disk no longer describes the graph. That is
// file I/O under the store lock, which review m3 keeps out of exports; it is
// allowed here because it happens at most once per process, only in the
// shutdown window, and only when a writer outlived the stop-time snapshot (the
// embed queue's drain, a stop step abandoned at its budget). A marker that
// survives is the dangerous direction, so a failed removal is logged at ERROR.
func (s *HNSWEmbeddingStore) bumpGenLocked() {
	s.gen++
	if s.cleanStop == "" {
		return
	}
	p := s.cleanStop
	s.cleanStop = ""
	if err := removeDurably(p); err != nil {
		hnswLog.Error("hnsw: a write after the clean-stop marker could not remove it, so the next boot may load a snapshot that lacks this write without a reconcile; delete %s by hand: %v", p, err)
	}
}

// MarkCleanStop writes the clean-stop marker in dir when the snapshot there is
// exactly the graph this store holds, so the next boot may load it without a
// reconcile (provided the Pebble row counts still match too). The server calls
// it at the end of its stop, after the stop-time snapshot attempt and once the
// writers it owns have stopped.
//
// The store knows the snapshot describes the graph when the last successful
// export to dir, or the fresh import from it, recorded the current gen, and
// the graph is complete, undamaged and not pending a reconcile. That holds
// after a successful stop-time export, and also when that export was skipped
// for the shutdown budget, or failed, with nothing changed since the last
// successful one (a failed export leaves the previous snapshot in place).
// Otherwise nothing is written and reason says why. A later write to the graph
// removes the marker (bumpGenLocked).
//
// The marker and its directory are fsynced before it is reported written.
func (s *HNSWEmbeddingStore) MarkCleanStop(dir string) (written bool, reason string, err error) {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.damaged:
		return false, "a recovered library panic may have damaged the graph", nil
	case s.pendingReconcile:
		return false, "the snapshot this process loaded is still being reconciled", nil
	case !s.complete:
		return false, "the graph is not a complete projection of the Pebble rows", nil
	case !s.exportedValid || s.exportedDir != dir:
		return false, "this process wrote no snapshot of the graph to " + dir + " and loaded none from it", nil
	case s.gen != s.exportedGen:
		return false, "the graph changed after its last snapshot was written", nil
	}
	p := cleanStopPath(dir)
	body := fmt.Sprintf("clean stop at %s\n", time.Now().UTC().Format(time.RFC3339))
	if err := writeFileSynced(p, []byte(body)); err != nil {
		return false, "", fmt.Errorf("hnsw: write clean-stop marker: %w", errors.Join(err, removeDurably(p)))
	}
	if err := fsyncDir(dir); err != nil {
		return false, "", fmt.Errorf("hnsw: clean-stop marker: %w", errors.Join(err, removeDurably(p)))
	}
	s.cleanStop = p
	return true, "", nil
}

// TakeHNSWCleanStop reports whether dir holds a clean-stop marker and removes
// it durably, so a crash later in this run cannot leave it vouching for a
// snapshot the run lets fall behind. Every boot with an HNSW directory takes
// it once: ImportWithStalenessCheck does, first thing, and the server calls
// this directly when the HNSW store is not in use. An operation-runner child
// takes neither path: the marker belongs to the parent. A marker that cannot be
// removed is reported absent (the snapshot is then reconciled, the safe
// direction) with an error saying it is still there.
func TakeHNSWCleanStop(dir string) (bool, error) {
	p := cleanStopPath(dir)
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("clean-stop marker %s: %w", p, err)
	}
	if err := removeDurably(p); err != nil {
		return false, fmt.Errorf("clean-stop marker %s could not be removed durably, so a later boot may trust a snapshot this run lets fall behind; delete it by hand: %w", p, err)
	}
	return true, nil
}
