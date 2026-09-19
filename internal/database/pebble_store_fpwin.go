// file: internal/database/pebble_store_fpwin.go
// version: 1.4.0
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
// Locking: fpwinLocks is a stripe set keyed by window ref (lockWindowRefs). A
// window write and the delete cascade of the SAME file take the same stripe
// across their check/stage and their commit, so a Put that passed its "row
// exists" check can never commit after the delete staged its cascade (which
// would leave a window with no row). Different files take different stripes
// and do not wait on each other. Multi-ref holders take their stripes in
// ascending stripe order, so two of them cannot deadlock.

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

	fileID, isFile := fileIDOfWindowRef(row.Ref)
	var hint *BookFile
	if isFile {
		// Resolved BEFORE the window stripe: the pre-index fallback is a scan of every
		// book_file row, and holding a window stripe across it would stall every
		// writer that hashes to the same stripe. Existence is then
		// re-confirmed under the lock with point reads only.
		if hint, err = s.resolveBookFileByID(fileID); err != nil {
			return fmt.Errorf("PutFingerprintWindow %s: %w", row.Ref, err)
		}
		if hint == nil {
			return fmt.Errorf("PutFingerprintWindow %s: %w: %s", row.Ref, ErrFingerprintWindowRowGone, fileID)
		}
	}

	unlock := s.lockWindowRefs(row.Ref)
	defer unlock()
	if isFile {
		ok, cerr := s.fileRowStillExists(fileID, hint)
		if cerr != nil {
			return fmt.Errorf("PutFingerprintWindow %s: %w", row.Ref, cerr)
		}
		if !ok {
			return fmt.Errorf("PutFingerprintWindow %s: %w: %s was deleted", row.Ref, ErrFingerprintWindowRowGone, fileID)
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
// BookFile.AcoustIDFingerprint (Virtual=true, Pipeline and tool fields empty).
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
	unlock := s.lockWindowRefs(ref)
	defer unlock()
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

// CarryOverFingerprintWindows moves the stored windows of every donor in from
// onto to, in one batch: each is rewritten under to (Ref updated) and deleted
// under its donor, and each donor's failure tombstone is dropped (a failure
// recorded against a donor says nothing about the keeper). A window to already
// has for the same kind and slot wins, and among donors the first listed wins:
// the keeper's data is never overwritten. It returns the number of windows
// written under to.
//
// A group with NOTHING to carry is a strict no-op: (0, nil), with no keeper
// lookup and no write. That is every dedupe group in production until windows
// exist, so it must not fail or cost anything:
//   - a donor ref the key scheme cannot represent (an ID containing ':' or ';',
//     the same rule the delete cascade skips on) can hold no windows, so it is
//     treated as empty rather than as an error;
//   - to is only validated, and an f: keeper only resolved, once some donor has
//     a window or tombstone. The keeper is resolved ONCE per call, before any
//     stripe is taken: a row written before the book_file_id index resolves by
//     a scan of every book_file row.
func (s *PebbleStore) CarryOverFingerprintWindows(from []FingerprintWindowRef, to FingerprintWindowRef) (int, error) {
	// Donors worth looking at: representable, distinct, not the keeper.
	donors := make([]FingerprintWindowRef, 0, len(from))
	seen := make(map[FingerprintWindowRef]struct{}, len(from))
	for _, ref := range from {
		if ref == to || ref.validate() != nil {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		donors = append(donors, ref)
	}

	// Unlocked pre-check: nothing stored under any donor means nothing to do.
	// The authoritative read happens again under the stripes below.
	anything := false
	for _, ref := range donors {
		has, err := s.refHasWindowState(ref)
		if err != nil {
			return 0, fmt.Errorf("CarryOverFingerprintWindows %s: %w", ref, err)
		}
		if has {
			anything = true
			break
		}
	}
	if !anything {
		return 0, nil
	}

	if err := to.validate(); err != nil {
		return 0, fmt.Errorf("CarryOverFingerprintWindows: donors hold windows but the keeper ref is unusable: %w", err)
	}
	toFileID, toIsFile := fileIDOfWindowRef(to)
	var hint *BookFile
	if toIsFile {
		f, ferr := s.resolveBookFileByID(toFileID)
		if ferr != nil {
			return 0, fmt.Errorf("CarryOverFingerprintWindows -> %s: %w", to, ferr)
		}
		if f == nil {
			return 0, fmt.Errorf("CarryOverFingerprintWindows -> %s: book_file %s does not exist", to, toFileID)
		}
		hint = f
	}

	unlock := s.lockWindowRefs(append([]FingerprintWindowRef{to}, donors...)...)
	defer unlock()

	if toIsFile {
		ok, cerr := s.fileRowStillExists(toFileID, hint)
		if cerr != nil {
			return 0, fmt.Errorf("CarryOverFingerprintWindows -> %s: %w", to, cerr)
		}
		if !ok {
			return 0, fmt.Errorf("CarryOverFingerprintWindows -> %s: book_file %s was deleted", to, toFileID)
		}
	}
	existing, err := s.readFingerprintWindows(to)
	if err != nil {
		return 0, err
	}
	taken := make(map[string]struct{}, len(existing))
	for i := range existing {
		taken[string(fpwinKey(&existing[i]))] = struct{}{}
	}

	batch := s.db.NewBatch()
	moved := 0
	for _, ref := range donors {
		windows, rerr := s.readFingerprintWindows(ref)
		if rerr != nil {
			batch.Close()
			return 0, rerr
		}
		for i := range windows {
			w := windows[i]
			w.Ref = to
			key := fpwinKey(&w)
			if _, dup := taken[string(key)]; dup {
				continue
			}
			data, merr := json.Marshal(&w)
			if merr != nil {
				batch.Close()
				return 0, fmt.Errorf("CarryOverFingerprintWindows %s -> %s: marshal: %w", ref, to, merr)
			}
			if err := batch.Set(key, data, nil); err != nil {
				batch.Close()
				return 0, err
			}
			taken[string(key)] = struct{}{}
			moved++
		}
		if _, err := s.stageFingerprintWindowDeletes(batch, ref); err != nil {
			batch.Close()
			return 0, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("CarryOverFingerprintWindows -> %s: %w", to, err)
	}
	return moved, nil
}

// refHasWindowState reports whether ref has any stored window or a failure
// tombstone, with at most one short seek and one point read.
func (s *PebbleStore) refHasWindowState(ref FingerprintWindowRef) (bool, error) {
	prefix := fpwinRefPrefix(ref)
	found := false
	if err := forEachKeyInRange(s.db, prefix, prefixEnd(prefix), func(_, _ []byte) error {
		found = true
		return errStopScan
	}); err != nil && !errors.Is(err, errStopScan) {
		return false, err
	}
	if found {
		return true, nil
	}
	_, closer, err := s.db.Get(fpwinFailKey(ref))
	if err == nil {
		closer.Close()
		return true, nil
	}
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	return false, err
}

// lockWindowRefs takes the window stripes for refs, in ascending stripe order
// with duplicates collapsed, and returns the func that releases them. Every
// caller that takes more than one stripe goes through here, so the order is
// global and two multi-ref holders cannot deadlock. Nothing slow runs under a
// stripe: Pebble reads and one batch commit.
func (s *PebbleStore) lockWindowRefs(refs ...FingerprintWindowRef) func() {
	idx := make([]int, 0, len(refs))
	have := make(map[int]struct{}, len(refs))
	for _, r := range refs {
		i := stripeFor(string(r))
		if _, dup := have[i]; dup {
			continue
		}
		have[i] = struct{}{}
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		s.fpwinLocks[i].Lock()
	}
	return func() {
		for j := len(idx) - 1; j >= 0; j-- {
			s.fpwinLocks[idx[j]].Unlock()
		}
	}
}

// commitWithWindowCascade stages the window cascade of files into batch and
// commits it, holding those files' window stripes across stage AND commit and
// releasing them before returning. The three book_file delete paths use it.
// On error the batch is closed.
func (s *PebbleStore) commitWithWindowCascade(batch *pebble.Batch, files ...*BookFile) error {
	refs := make([]FingerprintWindowRef, 0, len(files))
	for _, f := range files {
		if f != nil && f.ID != "" {
			refs = append(refs, FileWindowRef(f.ID))
		}
	}
	unlock := s.lockWindowRefs(refs...)
	defer unlock()
	if err := s.stageFileWindowCascade(batch, files...); err != nil {
		batch.Close()
		return err
	}
	return batch.Commit(pebble.Sync)
}

// stageFingerprintWindowDeletes stages a point delete for every stored window of
// ref, plus its failure tombstone, into batch. It returns how many window rows
// were staged. The caller holds ref's window stripe and commits.
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
// the windows of every file in files into batch. The caller holds their
// window stripes (commitWithWindowCascade).
func (s *PebbleStore) stageFileWindowCascade(batch *pebble.Batch, files ...*BookFile) error {
	for _, f := range files {
		if f == nil || f.ID == "" {
			continue
		}
		ref := FileWindowRef(f.ID)
		if ref.validate() != nil {
			// An ID containing ':' or ';' cannot have windows: PutFingerprintWindow
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

// fileRowStillExists re-confirms, under the window stripe, that a row resolved before the
// lock still exists, using point reads only: the primary key the pre-lock read
// found, then the book_file_id index (which a move between books rewrites).
// Every delete path removes the primary key in the same batch as its window
// cascade, and holds the same stripe across that commit, so a miss on both means the
// row is gone and a window written now would be an orphan.
func (s *PebbleStore) fileRowStillExists(fileID string, hint *BookFile) (bool, error) {
	if hint != nil && hint.BookID != "" {
		_, closer, err := s.db.Get([]byte(fmt.Sprintf("book_file:%s:%s", hint.BookID, fileID)))
		if err == nil {
			closer.Close()
			return true, nil
		}
		if !errors.Is(err, pebble.ErrNotFound) {
			return false, fmt.Errorf("re-check book_file %s: %w", fileID, err)
		}
	}
	return s.lookupBookFileByIDIndex(fileID) != nil, nil
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

// ReplaceFingerprintWindows atomically replaces the stored windows of ref, and
// its failure tombstone, with ws. It is the write a backfill makes when it has
// recomputed a file: a plain Put per window would leave a slot the new plan no
// longer has (a file that crossed the 600 s threshold) sitting next to the new
// rows, and a crash between a delete and the puts would leave the file with
// nothing. One batch, under the ref's stripe, avoids both.
//
// ws must be non-empty and every row must name ref. For an f: ref the
// book_file row must exist, re-checked under the stripe exactly as
// PutFingerprintWindow does.
func (s *PebbleStore) ReplaceFingerprintWindows(ref FingerprintWindowRef, ws []FingerprintWindow) error {
	if err := ref.validate(); err != nil {
		return err
	}
	if len(ws) == 0 {
		return fmt.Errorf("ReplaceFingerprintWindows %s: no windows (use DeleteFingerprintWindows to clear)", ref)
	}
	encoded := make([][2][]byte, 0, len(ws))
	for i := range ws {
		row := ws[i]
		if row.Ref != ref {
			return fmt.Errorf("ReplaceFingerprintWindows %s: row %d names ref %s", ref, i, row.Ref)
		}
		if err := row.validate(); err != nil {
			return err
		}
		if row.SchemaVersion == 0 {
			row.SchemaVersion = FingerprintWindowSchemaVersion
		}
		data, err := json.Marshal(&row)
		if err != nil {
			return fmt.Errorf("ReplaceFingerprintWindows %s: marshal: %w", ref, err)
		}
		encoded = append(encoded, [2][]byte{fpwinKey(&row), data})
	}
	return s.commitRefWrite(ref, "ReplaceFingerprintWindows", func(batch *pebble.Batch) error {
		for _, kv := range encoded {
			if err := batch.Set(kv[0], kv[1], nil); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordFingerprintWindowFailure drops ref's stored windows and writes its
// fpwin_fail: tombstone, in one batch. The windows go because a durable
// failure is only ever recorded after the caller decided the stored windows
// were not current for the file as it is now; keeping them would leave a
// stale print beside a tombstone that says the file cannot be printed.
func (s *PebbleStore) RecordFingerprintWindowFailure(f *FingerprintWindowFailure) error {
	if err := f.validate(); err != nil {
		return err
	}
	row := *f
	if row.SchemaVersion == 0 {
		row.SchemaVersion = FingerprintWindowSchemaVersion
	}
	data, err := json.Marshal(&row)
	if err != nil {
		return fmt.Errorf("RecordFingerprintWindowFailure %s: marshal: %w", row.Ref, err)
	}
	return s.commitRefWrite(row.Ref, "RecordFingerprintWindowFailure", func(batch *pebble.Batch) error {
		return batch.Set(fpwinFailKey(row.Ref), data, nil)
	})
}

// GetFingerprintWindowFailure returns ref's tombstone, or nil, nil when there
// is none. An undecodable tombstone is an error, not "no tombstone": a
// backfill reading nil would retry a file that is known to fail, every run.
func (s *PebbleStore) GetFingerprintWindowFailure(ref FingerprintWindowRef) (*FingerprintWindowFailure, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	val, closer, err := s.db.Get(fpwinFailKey(ref))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetFingerprintWindowFailure %s: %w", ref, err)
	}
	defer closer.Close()
	var f FingerprintWindowFailure
	if err := json.Unmarshal(val, &f); err != nil {
		return nil, fmt.Errorf("undecodable fingerprint window failure %s: %w", ref, err)
	}
	return &f, nil
}

// commitRefWrite is the shared body of the replace-style writes: resolve an
// f: ref's row before the stripe (the pre-index fallback is a full scan),
// take the stripe, re-confirm the row with point reads, stage the deletes of
// every stored window and the tombstone, let stage add the new keys, commit.
func (s *PebbleStore) commitRefWrite(ref FingerprintWindowRef, op string, stage func(*pebble.Batch) error) error {
	fileID, isFile := fileIDOfWindowRef(ref)
	var hint *BookFile
	if isFile {
		var err error
		if hint, err = s.resolveBookFileByID(fileID); err != nil {
			return fmt.Errorf("%s %s: %w", op, ref, err)
		}
		if hint == nil {
			return fmt.Errorf("%s %s: %w: %s", op, ref, ErrFingerprintWindowRowGone, fileID)
		}
	}
	unlock := s.lockWindowRefs(ref)
	defer unlock()
	if isFile {
		ok, err := s.fileRowStillExists(fileID, hint)
		if err != nil {
			return fmt.Errorf("%s %s: %w", op, ref, err)
		}
		if !ok {
			return fmt.Errorf("%s %s: %w: %s was deleted", op, ref, ErrFingerprintWindowRowGone, fileID)
		}
	}
	batch := s.db.NewBatch()
	if _, err := s.stageFingerprintWindowDeletes(batch, ref); err != nil {
		batch.Close()
		return fmt.Errorf("%s %s: %w", op, ref, err)
	}
	if err := stage(batch); err != nil {
		batch.Close()
		return fmt.Errorf("%s %s: %w", op, ref, err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("%s %s: %w", op, ref, err)
	}
	return nil
}
