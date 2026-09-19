// file: internal/database/pebble_store_fpwin.go
// version: 1.0.0
// guid: d40d1916-5ea7-4ce8-9fb6-fab9d3d56026
// last-edited: 2026-09-19

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble/v2"
)

// Fingerprint-window storage (the fpwin: sidecar). Types and key scheme are in
// fingerprint_window.go.
//
// Lifecycle, and where each rule is enforced:
//
//   - Delete cascade: the three book_file delete paths (DeleteBookFile,
//     DeleteBookFilesForBook, DeleteBookFilesByIDs) stage the row's window keys
//     and failure tombstone into the SAME batch as the row delete, via
//     stageFingerprintWindowDeletes. It is deliberately NOT in
//     deleteBookFileSecondaryIndexes: that helper also runs on rows that SURVIVE
//     (UpdateBookFile, the batch upsert, the PID transfer and the bulk move all
//     drop-and-rewrite indexes through it), and a cascade there would wipe a
//     live file's windows on every update.
//   - Move between books: nothing to do. Keys carry the file ID, not the book
//     ID, and MoveBookFilesToBookBulk keeps the file ID. Pinned by
//     TestFpwin_CarryOver_MoveBetweenBooksKeepsWindows.
//   - Row merge (two rows for one file collapsed into one, as
//     maintenance.dedupe-book-file-rows does): CarryOverFingerprintWindows moves
//     the donor's windows onto the keeper before the donor is deleted.
//   - Repoint of an untracked candidate: CarryOverFingerprintWindows from its
//     p: ref to the new row's f: ref.
//
// fpwinMu serializes every window write against the delete cascade. Without it
// a Put that passed its "row exists" check could commit after a concurrent
// delete staged its cascade, leaving a window with no row.

// PutFingerprintWindow stores one window, replacing any stored window with the
// same ref, kind and slot. For an f: ref the book_file row must exist; a window
// for a row that is gone is refused rather than written as an orphan.
func (s *PebbleStore) PutFingerprintWindow(w *FingerprintWindow) error {
	if err := w.validate(); err != nil {
		return err
	}
	row := *w
	if row.SchemaVersion == 0 {
		row.SchemaVersion = FingerprintWindowSchemaVersion
	}
	data, err := json.Marshal(&row)
	if err != nil {
		return fmt.Errorf("PutFingerprintWindow %s: marshal: %w", row.Ref, err)
	}

	s.fpwinMu.Lock()
	defer s.fpwinMu.Unlock()
	if fileID, ok := fileIDOfWindowRef(row.Ref); ok {
		f, ferr := s.resolveBookFileByID(fileID)
		if ferr != nil {
			return fmt.Errorf("PutFingerprintWindow %s: %w", row.Ref, ferr)
		}
		if f == nil {
			return fmt.Errorf("PutFingerprintWindow %s: book_file %s does not exist", row.Ref, fileID)
		}
	}
	if err := s.db.Set(fpwinKey(&row), data, pebble.Sync); err != nil {
		return fmt.Errorf("PutFingerprintWindow %s: %w", row.Ref, err)
	}
	return nil
}

// GetFingerprintWindows returns the STORED windows of ref, ordered head, window
// (by slot), whole. It never includes the virtual legacy head; use
// WindowsForFile for that. An undecodable row is an error, not a skipped row:
// a caller planning work from this list would otherwise recompute (or worse,
// match on) a set it believes complete.
func (s *PebbleStore) GetFingerprintWindows(ref FingerprintWindowRef) ([]FingerprintWindow, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	return s.readFingerprintWindows(ref)
}

// WindowsForFile returns the file's stored windows plus, when the book_file row
// carries a legacy print, a virtual kind=head row synthesized from
// BookFile.AcoustIDFingerprint (Virtual=true, Pipeline=LegacyHeadPipeline).
// This is the "migration: none" shim: legacy prints stay where they are.
//
// The row is read from Pebble by primary key, NEVER from memdb: memdb strips
// AcoustIDFingerprint (memdb_strip.go), so a memdb-served row would make every
// file look like it had no legacy print.
//
// A file whose row no longer exists returns its stored windows (normally none,
// because the delete cascade removed them) and no head.
func (s *PebbleStore) WindowsForFile(fileID string) ([]FingerprintWindow, error) {
	ref := FileWindowRef(fileID)
	if err := ref.validate(); err != nil {
		return nil, err
	}
	stored, err := s.readFingerprintWindows(ref)
	if err != nil {
		return nil, err
	}
	f, err := s.resolveBookFileByID(fileID)
	if err != nil {
		return nil, fmt.Errorf("WindowsForFile %s: %w", fileID, err)
	}
	head, ok := legacyHeadWindow(f)
	if !ok {
		return stored, nil
	}
	return append([]FingerprintWindow{head}, stored...), nil
}

// DeleteFingerprintWindows removes every stored window of ref and its failure
// tombstone. It returns the number of window rows removed.
func (s *PebbleStore) DeleteFingerprintWindows(ref FingerprintWindowRef) (int, error) {
	if err := ref.validate(); err != nil {
		return 0, err
	}
	s.fpwinMu.Lock()
	defer s.fpwinMu.Unlock()
	batch := s.db.NewBatch()
	n, err := s.stageFingerprintWindowDeletes(batch, ref)
	if err != nil {
		batch.Close()
		return 0, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("DeleteFingerprintWindows %s: %w", ref, err)
	}
	return n, nil
}

// CarryOverFingerprintWindows moves every stored window of from onto to, in one
// batch: each is rewritten under to (Ref updated) and deleted under from, and
// from's failure tombstone is dropped (a failure recorded against the donor says
// nothing about the keeper). A window to already has for the same kind and slot
// wins and the donor's copy is discarded: the keeper's data is never overwritten.
// It returns the number of windows written under to.
//
// For an f: target the book_file row must exist, as for PutFingerprintWindow.
// from == to is a no-op.
func (s *PebbleStore) CarryOverFingerprintWindows(from, to FingerprintWindowRef) (int, error) {
	if err := from.validate(); err != nil {
		return 0, err
	}
	if err := to.validate(); err != nil {
		return 0, err
	}
	if from == to {
		return 0, nil
	}

	s.fpwinMu.Lock()
	defer s.fpwinMu.Unlock()

	donor, err := s.readFingerprintWindows(from)
	if err != nil {
		return 0, err
	}
	if len(donor) == 0 {
		// Still drop a lone tombstone so the donor leaves nothing behind.
		if _, closer, gerr := s.db.Get(fpwinFailKey(from)); gerr == nil {
			closer.Close()
			if derr := s.db.Delete(fpwinFailKey(from), pebble.Sync); derr != nil {
				return 0, fmt.Errorf("CarryOverFingerprintWindows %s: drop tombstone: %w", from, derr)
			}
		} else if !errors.Is(gerr, pebble.ErrNotFound) {
			return 0, fmt.Errorf("CarryOverFingerprintWindows %s: read tombstone: %w", from, gerr)
		}
		return 0, nil
	}
	if fileID, ok := fileIDOfWindowRef(to); ok {
		f, ferr := s.resolveBookFileByID(fileID)
		if ferr != nil {
			return 0, fmt.Errorf("CarryOverFingerprintWindows %s -> %s: %w", from, to, ferr)
		}
		if f == nil {
			return 0, fmt.Errorf("CarryOverFingerprintWindows %s -> %s: book_file %s does not exist", from, to, fileID)
		}
	}
	existing, err := s.readFingerprintWindows(to)
	if err != nil {
		return 0, err
	}
	have := make(map[string]struct{}, len(existing))
	for i := range existing {
		have[string(fpwinKey(&existing[i]))] = struct{}{}
	}

	batch := s.db.NewBatch()
	moved := 0
	for i := range donor {
		w := donor[i]
		w.Ref = to
		key := fpwinKey(&w)
		if _, taken := have[string(key)]; taken {
			continue
		}
		data, merr := json.Marshal(&w)
		if merr != nil {
			batch.Close()
			return 0, fmt.Errorf("CarryOverFingerprintWindows %s -> %s: marshal: %w", from, to, merr)
		}
		if err := batch.Set(key, data, nil); err != nil {
			batch.Close()
			return 0, err
		}
		moved++
	}
	if _, err := s.stageFingerprintWindowDeletes(batch, from); err != nil {
		batch.Close()
		return 0, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("CarryOverFingerprintWindows %s -> %s: %w", from, to, err)
	}
	return moved, nil
}

// stageFingerprintWindowDeletes stages a point delete for every stored window of
// ref, plus its failure tombstone, into batch. It returns how many window rows
// were staged. The caller holds fpwinMu and commits.
//
// Point deletes, not DeleteRange: an indexed batch's Get/NewIter do not observe
// a range delete staged in the same batch, and the book_file delete batches this
// is staged into are indexed batches that read through themselves. A file has at
// most a handful of windows, so the scan is a single short seek.
func (s *PebbleStore) stageFingerprintWindowDeletes(batch *pebble.Batch, ref FingerprintWindowRef) (int, error) {
	prefix := fpwinRefPrefix(ref)
	var keys [][]byte
	if err := forEachKeyInRange(s.db, prefix, prefixEnd(prefix), func(key, _ []byte) error {
		keys = append(keys, append([]byte(nil), key...))
		return nil
	}); err != nil {
		return 0, fmt.Errorf("stage window deletes for %s: %w", ref, err)
	}
	for _, k := range keys {
		if err := batch.Delete(k, nil); err != nil {
			return 0, err
		}
	}
	if err := batch.Delete(fpwinFailKey(ref), nil); err != nil {
		return 0, err
	}
	return len(keys), nil
}

// stageFileWindowCascade is the delete cascade for book_file rows: it stages
// the windows of every file in files into batch. The caller holds fpwinMu.
func (s *PebbleStore) stageFileWindowCascade(batch *pebble.Batch, files ...*BookFile) error {
	for _, f := range files {
		if f == nil || f.ID == "" {
			continue
		}
		ref := FileWindowRef(f.ID)
		if ref.validate() != nil {
			// An ID containing ':' cannot have windows: PutFingerprintWindow
			// refuses to build a key for it. Nothing to cascade.
			continue
		}
		if _, err := s.stageFingerprintWindowDeletes(batch, ref); err != nil {
			return fmt.Errorf("cascade windows of book_file %s: %w", f.ID, err)
		}
	}
	return nil
}

// readFingerprintWindows reads and decodes every stored window of ref, sorted.
func (s *PebbleStore) readFingerprintWindows(ref FingerprintWindowRef) ([]FingerprintWindow, error) {
	prefix := fpwinRefPrefix(ref)
	var out []FingerprintWindow
	if err := forEachKeyInRange(s.db, prefix, prefixEnd(prefix), func(key, value []byte) error {
		var w FingerprintWindow
		if err := json.Unmarshal(value, &w); err != nil {
			return fmt.Errorf("undecodable fingerprint window %q: %w", key, err)
		}
		out = append(out, w)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read fingerprint windows of %s: %w", ref, err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ki, kj := windowKindOrder(out[i].Kind), windowKindOrder(out[j].Kind); ki != kj {
			return ki < kj
		}
		return out[i].SlotBP < out[j].SlotBP
	})
	return out, nil
}

// resolveBookFileByID reads a book_file row from Pebble by ID: the
// book_file_id index first, then the pre-index scan, exactly as the delete
// paths resolve rows. nil, nil means no such row.
func (s *PebbleStore) resolveBookFileByID(fileID string) (*BookFile, error) {
	if f := s.lookupBookFileByIDIndex(fileID); f != nil {
		return f, nil
	}
	return s.scanForBookFileByID(fileID)
}

func windowKindOrder(k FingerprintWindowKind) int {
	switch k {
	case WindowKindHead:
		return 0
	case WindowKindWindow:
		return 1
	case WindowKindWhole:
		return 2
	}
	return 3
}

// fileIDOfWindowRef returns the file ID of an f: ref.
func fileIDOfWindowRef(ref FingerprintWindowRef) (string, bool) {
	s := string(ref)
	if len(s) > 2 && s[:2] == "f:" {
		return s[2:], true
	}
	return "", false
}
