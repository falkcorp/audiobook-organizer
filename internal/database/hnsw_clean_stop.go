// file: internal/database/hnsw_clean_stop.go
// version: 1.0.7
// guid: dd04968f-f35c-456d-830f-87bd3ce9b0dd
// last-edited: 2026-11-09

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// The clean-stop marker (R1-15 follow-up, MAJOR-1). The truth sidecars let the
// boot see a snapshot whose Pebble row counts moved since it was written, but
// a crash can change the rows and leave every count as it was: an add and a
// delete, a re-embed of a key, a merge or primary change that flips a row's
// hydrate verdict. Such a snapshot used to load as fresh, and the divergence
// was permanent. The marker closes that gap for the runs it can vouch for: the
// server writes it as the last step of a clean stop, and every process that
// opens the database on disk takes it (takeCleanStopClaim). A boot that holds no
// marker reconciles the snapshot against Pebble whatever the counts say.
//
// The marker vouches that the snapshot is the graph AND that the graph is the
// Pebble rows, so MarkCleanStop refuses unless both hold:
//
//   - the graph side: the last snapshot written to (or loaded from) the
//     directory recorded the current gen, and the graph is complete,
//     undamaged, not diverged (MarkDiverged: a write it refused, or a mirror
//     the dedup engine reports failed) and not pending a reconcile;
//   - the Pebble side: a database is bound (BindTruthSource) and no write
//     since it opened touched a key the reconcile's verdict reads
//     (hnsw_verdict_watch.go), which covers writes that never reach the
//     graph, such as a merge that flips a book's primary flag or a cleanup op
//     that deletes embedding rows.
//
// The marker records the embedding model the reconcile classified rows
// against, and a boot that classifies against another model reconciles. A
// later write to the graph, or a later verdict write to Pebble, removes it,
// and a marker that cannot be removed is emptied (discardCleanStop).
//
// Not covered: (1) a write to those keys from a process that opens Pebble
// without a GatedDB (no production path does; the raw-pebble tools under cmd/
// write settings or only read); (2) a verdict write by an operation-runner
// child, which counts in the child's process, not the parent's (no production
// op runs in a child); (3) a graph whose rows were indexed by a rule the
// reconcile does not share (the open question in todo.d about EmbedBook
// mirroring non-primary books). The marker also refuses more than it must:
// any verdict write in a run, an embedding upsert included, means no marker,
// so it saves the reconcile only after a quiet run.

// hnswCleanStopFile is the marker's name inside the snapshot directory.
const hnswCleanStopFile = "hnsw.clean-stop"

// HNSWSnapshotDir is the HNSW snapshot directory of the Pebble database at
// dbPath: "hnsw" beside it. The server persists the graph there, and every
// process that opens that database on disk takes the clean-stop marker from
// it (takeCleanStopClaim).
func HNSWSnapshotDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "hnsw")
}

// hnswCleanStopClaimOff stops store opens from taking the marker
// (DisableHNSWCleanStopClaim).
var hnswCleanStopClaimOff atomic.Bool

// DisableHNSWCleanStopClaim stops every later store open in this process, and
// ClaimHNSWCleanStop, from taking the HNSW clean-stop marker. An
// operation-runner child calls it before it opens the store: the marker
// belongs to its parent.
func DisableHNSWCleanStopClaim() { hnswCleanStopClaimOff.Store(true) }

// cleanStopClaim is the marker an open took from dir (an absolute, clean
// path; cleanStopDir): rec is nil when there was none, one that did not
// vouch, or one a books migration voided (adoptCleanStop).
type cleanStopClaim struct {
	dir string
	rec *cleanStopRecord
}

// cleanStopDir is dir as a claim records and compares it: absolute and
// clean, so the open (which resolves the database path) and the server's
// import (which derives the directory from the configured path, relative or
// not) name one directory one way.
func cleanStopDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return abs
}

// takeCleanStopClaim takes the clean-stop marker from dir before anything
// writes the database beside it (R1-15 follow-up review, MINOR-1). Any
// process that opens the database can change the rows the snapshot
// projects, and only the server counts those writes against the marker
// (BindTruthSource), so the marker must not outlive an open by a CLI command
// or a tool: a run between a clean stop and the next boot costs that boot a
// reconcile. Two openers call it, each before the store's first write:
//
//   - OpenStandalone, for a database on disk, after core's Open (which
//     writes nothing, and fails on a database another process holds) and
//     before Prepare;
//   - the served app, before app.New opens, prepares and migrates the store
//     (ClaimHNSWCleanStop, from books' Options).
//
// The store keeps what the open took (adoptCleanStop) for the server's
// import (popCleanStop).
func takeCleanStopClaim(dir string) *cleanStopClaim {
	dir = cleanStopDir(dir)
	rec, err := takeCleanStop(dir)
	if err != nil {
		hnswLog.Error("hnsw: opening the database: %v; the next snapshot import reconciles", err)
	}
	return &cleanStopClaim{dir: dir, rec: rec}
}

// adoptCleanStop keeps the claim the database's open took on d, until the
// snapshot import pops it. migrated says a books migration step ran after
// the take: the step wrote the store through a step store of its own, whose
// verdict writes no watch of d counted, so the claim's marker no longer
// vouches and the import reconciles. A nil claim (the open took none) leaves
// d without one.
func (d *GatedDB) adoptCleanStop(c *cleanStopClaim, migrated bool) {
	if c == nil {
		return
	}
	adopted := *c
	if migrated && adopted.rec != nil {
		hnswLog.Info("hnsw: a books migration ran after the clean-stop marker in %s was taken, so it no longer vouches; the snapshot import reconciles", adopted.dir)
		adopted.rec = nil
	}
	d.cleanStop.Store(&adopted)
}

// HNSWCleanStopClaim is the clean-stop marker the served app takes before
// app.New opens the store (ClaimHNSWCleanStop), held until books' Register
// hands it to the store it builds (PebbleStore.AdoptHNSWCleanStopClaim).
type HNSWCleanStopClaim struct{ c *cleanStopClaim }

// ClaimHNSWCleanStop takes the clean-stop marker beside the database at
// dbPath, for the served app to call before app.New: the kernel prepares and
// migrates the store inside New, and the marker must be gone before the
// first write, so a crash in a migration cannot leave it vouching
// (takeCleanStopClaim). It returns nil when an operation-runner child turned
// claims off (DisableHNSWCleanStopClaim).
func ClaimHNSWCleanStop(dbPath string) *HNSWCleanStopClaim {
	if hnswCleanStopClaimOff.Load() {
		return nil
	}
	return &HNSWCleanStopClaim{c: takeCleanStopClaim(HNSWSnapshotDir(dbPath))}
}

// AdoptHNSWCleanStopClaim hands the store the marker ClaimHNSWCleanStop took,
// for the server's snapshot import. migrated says one of books' migration
// steps ran in this boot (BooksSpecObserved), which voids it
// (adoptCleanStop). Books' Register calls it right after NewBooksStore,
// before the settings boot writes anything through the store. A nil claim
// does nothing.
func (p *PebbleStore) AdoptHNSWCleanStopClaim(c *HNSWCleanStopClaim, migrated bool) {
	if c == nil {
		return
	}
	p.db.adoptCleanStop(c.c, migrated)
}

// popCleanStop returns the marker the open took from dir, once: ok is false
// when this database's open took none (an in-memory database, a raw engine
// wrapped by NewGatedDB, a child, or a second pop), or took it from another
// directory, whose claim it then discards with a warning (R1-15 round-2
// review, N3): the server derives both directories from one database path,
// so a mismatch means a refactor split them, and every boot after it loses
// the fast path without one.
func (d *GatedDB) popCleanStop(dir string) (rec *cleanStopRecord, ok bool) {
	c := d.cleanStop.Swap(nil)
	if c == nil {
		return nil, false
	}
	if want := cleanStopDir(dir); c.dir != want {
		hnswLog.Warn("hnsw: the database's open took the clean-stop marker from %s, but the snapshot import reads %s; the open's claim is discarded and the import uses the marker in %s, so a marker the open removed cannot vouch for this boot", c.dir, want, dir)
		return nil, false
	}
	return c.rec, true
}

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

// markerRemove removes a clean-stop marker file. A test replaces it to make a
// removal fail.
var markerRemove = os.Remove

// discardCleanStop makes the marker at path vouch for nothing, durably. It
// removes the file, retrying once, and fsyncs the directory. When neither
// removal works (a directory that refuses the unlink, say) it empties the
// file instead and fsyncs it: an empty marker does not parse, so it vouches
// for nothing (readCleanStop). A marker already gone is not an error. An error
// means a marker that may still vouch is on disk, and says so.
func discardCleanStop(path string) error {
	first := removeMarker(path)
	if first == nil {
		return fsyncDir(filepath.Dir(path))
	}
	second := removeMarker(path)
	if second == nil {
		return fsyncDir(filepath.Dir(path))
	}
	if err := writeFileSynced(path, nil); err != nil {
		return fmt.Errorf("clean-stop marker %s could be neither removed (%w) nor emptied (%w)", path, errors.Join(first, second), err)
	}
	hnswLog.Warn("hnsw: clean-stop marker %s could not be removed (%v), so it was emptied; an empty marker vouches for nothing", path, second)
	return nil
}

// removeMarker removes path with markerRemove; a path already gone is not an
// error.
func removeMarker(path string) error {
	if err := markerRemove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
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
	if err := discardCleanStop(p); err != nil {
		hnswLog.Error("hnsw: %s, and the clean-stop marker could not be discarded, so the next boot may load a snapshot that lacks it without a reconcile; delete %s by hand: %v", why, p, err)
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
// A store with no database bound (db nil) cannot count those writes, so it
// vouches for nothing: MarkCleanStop refuses, and the import treats any marker
// as one that does not vouch (R1-15 round-2 review, S5). db may be nil to bind
// only the model.
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
// it opened. The caller holds s.mu and has checked that a database is bound.
func (s *HNSWEmbeddingStore) pebbleVerdictWritesLocked() int64 {
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
// no verdict write since it opened; with no database bound it cannot know.
// Otherwise nothing is written and reason says why. A later write to the graph, or a later verdict write to Pebble,
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
	case s.truthDB == nil:
		return false, "no Pebble database is bound to the store, so nothing counted the writes the graph could have missed", nil
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
		return false, "", fmt.Errorf("hnsw: write clean-stop marker: %w", errors.Join(err, discardCleanStop(p)))
	}
	if err := fsyncDir(dir); err != nil {
		return false, "", fmt.Errorf("hnsw: clean-stop marker: %w", errors.Join(err, discardCleanStop(p)))
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
// a snapshot the run lets fall behind. Every open of a database on disk takes
// the marker beside it already (takeCleanStopClaim); this covers a directory the
// open did not, and the server calls it when the HNSW store is not in use.
// An operation-runner child calls neither: the marker belongs to the parent. A marker that cannot be removed is emptied instead, so it vouches
// for nothing (discardCleanStop); one that can be neither is reported absent
// (the snapshot is then reconciled, the safe direction) with an error saying
// it is still there.
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
	if err := discardCleanStop(p); err != nil {
		return nil, errors.Join(readErr, fmt.Errorf("clean-stop marker %s could not be discarded durably, so a later boot may trust a snapshot this run lets fall behind; delete it by hand: %w", p, err))
	}
	return rec, readErr
}

// takeCleanStopForImport returns the marker the snapshot import in dir holds:
// the one the bound database's open took (popCleanStop), or, when that open
// took none for dir, the one on disk now (takeCleanStop).
func (s *HNSWEmbeddingStore) takeCleanStopForImport(dir string) (*cleanStopRecord, error) {
	s.mu.RLock()
	db := s.truthDB
	s.mu.RUnlock()
	if db != nil {
		if rec, ok := db.popCleanStop(dir); ok {
			return rec, nil
		}
	}
	return takeCleanStop(dir)
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
	case s.truthDB == nil:
		return "no Pebble database is bound to the store, so nothing counted the writes made before the snapshot import"
	}
	if n := s.pebbleVerdictWritesLocked(); n > 0 {
		return pebbleVerdictWritesReason(n) + " before the snapshot import"
	}
	return ""
}
