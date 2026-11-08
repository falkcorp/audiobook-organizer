// file: internal/database/hnsw_clean_stop.go
// version: 1.0.3
// guid: dd04968f-f35c-456d-830f-87bd3ce9b0dd
// last-edited: 2026-11-08

package database

import (
	"encoding/json"
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
// was permanent. The marker closes that gap for the runs it can vouch for: a
// process writes it at the end of a clean stop, and every boot takes it. A
// boot that finds no marker reconciles the snapshot against Pebble whatever
// the counts say.
//
// The marker vouches that the snapshot is the graph AND that the graph is the
// Pebble rows, so MarkCleanStop refuses unless both hold:
//
//   - the graph side: the last snapshot written to (or loaded from) the
//     directory recorded the current gen, and the graph is complete,
//     undamaged and not pending a reconcile;
//   - the Pebble side: no write since the store opened touched a key the
//     reconcile's verdict reads (hnsw_verdict_watch.go), which covers writes
//     that never reach the graph, such as a merge that flips a book's primary
//     flag or a cleanup op that deletes embedding rows.
//
// The marker records the embedding model the reconcile classified rows
// against, and a boot that classifies against another model reconciles. A
// write the graph refused, or a mirror the dedup engine reports failed
// (MarkDiverged), also stops the marker for the rest of the process.
//
// Not covered: a write to those keys from a process that opens Pebble without
// a GatedDB (no production path does; the raw-pebble tools under cmd/ write
// settings or only read), and a graph whose rows were indexed by a rule the
// reconcile does not share (the open question in todo.d about EmbedBook
// mirroring non-primary books).

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

// cleanStopRecord is the marker's content. A marker that does not parse, or
// has another version, vouches for nothing: the boot reconciles.
type cleanStopRecord struct {
	Version int    `json:"version"`
	Model   string `json:"model"`
	At      string `json:"at"`
}

const cleanStopVersion = 1

// readCleanStop reads and validates the marker at path. It returns nil and no
// error when there is none, and nil with an error when one is there but
// cannot be read or does not vouch.
func readCleanStop(path string) (*cleanStopRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read clean-stop marker %s: %w", path, err)
	}
	var rec cleanStopRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("clean-stop marker %s does not parse, so it vouches for nothing: %w", path, err)
	}
	if rec.Version != cleanStopVersion {
		return nil, fmt.Errorf("clean-stop marker %s has version %d, not %d, so it vouches for nothing", path, rec.Version, cleanStopVersion)
	}
	return &rec, nil
}

// dropCleanStopLocked removes the marker MarkCleanStop wrote in this process,
// if any, because what follows makes the snapshot or Pebble differ from what
// it vouched for; why says what. The caller holds s.mu for writing.
//
// That is file I/O under the store lock, which review m3 keeps out of
// exports; it is allowed here because it happens at most once per process,
// only in the shutdown window, and only when a writer outlived the stop. A
// marker that survives is the dangerous direction, so a failed removal is
// logged at ERROR.
func (s *HNSWEmbeddingStore) dropCleanStopLocked(why string) {
	if s.cleanStop == "" {
		return
	}
	p := s.cleanStop
	s.cleanStop = ""
	s.cleanStopLive.Store(false)
	if err := removeDurably(p); err != nil {
		hnswLog.Error("hnsw: %s, and the clean-stop marker could not be removed, so the next boot may load a snapshot that lacks it without a reconcile; delete %s by hand: %v", why, p, err)
	}
}

// bumpGenLocked counts one mutation of graphs/meta; the caller holds s.mu for
// writing. Every mutation goes through it. The first mutation after
// MarkCleanStop wrote its marker also removes the marker, because the
// snapshot on disk no longer describes the graph.
func (s *HNSWEmbeddingStore) bumpGenLocked() {
	s.gen++
	s.dropCleanStopLocked("a write to the graph after the clean-stop marker")
}

// BindTruthSource binds the store to the Pebble database whose rows its graph
// projects and to the embedding model the reconcile classifies them against.
// The server binds it at boot, before the snapshot import, and from then on:
//
//   - MarkCleanStop refuses once any write since db opened touched a key the
//     reconcile's verdict reads (hnsw_verdict_watch.go), and records model in
//     the marker;
//   - ImportWithStalenessCheck treats the snapshot as stale when such a write
//     happened before it, or when the marker records another model;
//   - such a write made while this process's marker is on disk removes it.
//
// An unbound store (nil, "") vouches for the graph alone, as before the
// binding existed. db may be nil to bind only the model.
func (s *HNSWEmbeddingStore) BindTruthSource(db *GatedDB, model string) {
	s.mu.Lock()
	s.truthDB, s.model = db, model
	s.mu.Unlock()
	if db != nil {
		db.verdict.setHook(s.onPebbleVerdictWrite)
	}
}

// onPebbleVerdictWrite is the bound database's hook, called on the writer's
// goroutine before each verdict write reaches the engine. It costs one atomic
// load unless this process wrote a marker. No caller of a Pebble write holds
// s.mu (the store never writes Pebble), so taking it here cannot deadlock.
//
// MarkCleanStop sets cleanStopLive after writing the marker and then reads the
// write count again, and the writer counts before it reads cleanStopLive
// (verdictWatch.note): with sequentially consistent atomics at least one of
// them sees the other, so a write racing the marker always removes it.
func (s *HNSWEmbeddingStore) onPebbleVerdictWrite() {
	if !s.cleanStopLive.Load() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropCleanStopLocked("a Pebble write changed what the reconcile reads after the clean-stop marker")
}

// pebbleVerdictWritesLocked returns the bound database's verdict writes since
// it opened, 0 when unbound. The caller holds s.mu.
func (s *HNSWEmbeddingStore) pebbleVerdictWritesLocked() int64 {
	if s.truthDB == nil {
		return 0
	}
	return s.truthDB.verdict.count()
}

// MarkCleanStop writes the clean-stop marker in dir when the snapshot there is
// exactly the graph this store holds and nothing since the store opened
// changed what the reconcile would read from Pebble, so the next boot may load
// it without a reconcile (provided the Pebble row counts and the embedding
// model still match too). The server calls it as the last step of a stop that
// abandoned no step, after the stop-time snapshot attempt.
//
// The store knows the snapshot describes the graph when the last successful
// export to dir, or the fresh import from it, recorded the current gen, and
// the graph is complete, undamaged and not pending a reconcile. That holds
// after a successful stop-time export, and also when that export was skipped
// for the shutdown budget, or failed, with nothing changed since the last
// successful one (a failed export leaves the previous snapshot in place). It
// knows Pebble still matches when the bound database (BindTruthSource) took
// no verdict write since it opened. Otherwise nothing is written and reason
// says why. A later write to the graph, or a later verdict write to Pebble,
// removes the marker.
//
// The marker records the bound embedding model, and it and its directory are
// fsynced before it is reported written.
func (s *HNSWEmbeddingStore) MarkCleanStop(dir string) (written bool, reason string, err error) {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.damaged:
		return false, "a recovered library panic may have damaged the graph", nil
	case s.diverged != "":
		return false, "the graph missed a write that Pebble holds (" + s.diverged + ")", nil
	case s.pendingReconcile:
		return false, "the snapshot this process loaded is still being reconciled", nil
	case !s.complete:
		return false, "the graph is not a complete projection of the Pebble rows", nil
	case !s.exportedValid || s.exportedDir != dir:
		return false, "this process wrote no snapshot of the graph to " + dir + " and loaded none from it", nil
	case s.gen != s.exportedGen:
		return false, "the graph changed after its last snapshot was written", nil
	}
	if n := s.pebbleVerdictWritesLocked(); n > 0 {
		return false, pebbleVerdictWritesReason(n), nil
	}
	if markCleanStopAfterCheckHook != nil {
		markCleanStopAfterCheckHook()
	}
	p := cleanStopPath(dir)
	body, err := json.Marshal(cleanStopRecord{Version: cleanStopVersion, Model: s.model, At: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return false, "", fmt.Errorf("hnsw: encode clean-stop marker: %w", err)
	}
	if err := writeFileSynced(p, append(body, '\n')); err != nil {
		return false, "", fmt.Errorf("hnsw: write clean-stop marker: %w", errors.Join(err, removeDurably(p)))
	}
	if err := fsyncDir(dir); err != nil {
		return false, "", fmt.Errorf("hnsw: clean-stop marker: %w", errors.Join(err, removeDurably(p)))
	}
	s.cleanStop = p
	s.cleanStopLive.Store(true)
	// A verdict write that counted after the check above but read
	// cleanStopLive before the store above did not remove the marker; this
	// second read sees it (onPebbleVerdictWrite).
	if n := s.pebbleVerdictWritesLocked(); n > 0 {
		s.dropCleanStopLocked("a Pebble write changed what the reconcile reads while the clean-stop marker was being written")
		return false, pebbleVerdictWritesReason(n), nil
	}
	return true, "", nil
}

// markCleanStopAfterCheckHook, when non-nil, runs inside MarkCleanStop after
// the verdict-write check and before the marker is written: the window a
// racing Pebble write lands in. Test-only; nil in production.
var markCleanStopAfterCheckHook func()

// pebbleVerdictWritesReason is the refusal for n verdict writes.
func pebbleVerdictWritesReason(n int64) string {
	return fmt.Sprintf("%d Pebble write(s) since the store opened touched what the reconcile reads (embedding rows, or a book's existence, primary flag or series fields), and the graph may not reflect them", n)
}

// TakeHNSWCleanStop reports whether dir holds a clean-stop marker that
// vouches (it parses, with this build's version), and removes whatever marker
// is there durably, so a crash later in this run cannot leave it vouching for
// a snapshot the run lets fall behind. ImportWithStalenessCheck takes it first
// thing, and the server calls this directly when the HNSW store is not in use.
// An operation-runner child takes neither path: the marker belongs to the
// parent. A marker that cannot be removed is reported absent (the snapshot is
// then reconciled, the safe direction) with an error saying it is still there.
func TakeHNSWCleanStop(dir string) (bool, error) {
	rec, err := takeCleanStop(dir)
	return rec != nil, err
}

// takeCleanStop is TakeHNSWCleanStop returning the marker's content: nil when
// there was none, when it did not vouch, or when it could not be removed.
func takeCleanStop(dir string) (*cleanStopRecord, error) {
	p := cleanStopPath(dir)
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("clean-stop marker %s: %w", p, err)
	}
	rec, readErr := readCleanStop(p)
	if err := removeDurably(p); err != nil {
		return nil, errors.Join(readErr, fmt.Errorf("clean-stop marker %s could not be removed durably, so a later boot may trust a snapshot this run lets fall behind; delete it by hand: %w", p, err))
	}
	return rec, readErr
}

// cleanStopStaleReason returns why a boot holding marker rec (nil: none, or
// one that does not vouch) must reconcile the snapshot in dir, or "" when the
// marker lets it skip the reconcile. The caller holds s.mu.
func (s *HNSWEmbeddingStore) cleanStopStaleReasonLocked(rec *cleanStopRecord, dir string) string {
	switch {
	case rec == nil:
		return "no clean-stop marker in " + dir + ": the previous process did not stop cleanly, or its stop could not vouch that the snapshot matched Pebble"
	case rec.Model != s.model:
		return fmt.Sprintf("the clean-stop marker in %s records embedding model %q, and this process classifies rows against %q", dir, rec.Model, s.model)
	}
	if n := s.pebbleVerdictWritesLocked(); n > 0 {
		return pebbleVerdictWritesReason(n) + " before the snapshot import"
	}
	return ""
}
