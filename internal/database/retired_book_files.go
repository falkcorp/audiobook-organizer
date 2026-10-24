// file: internal/database/retired_book_files.go
// version: 1.3.0
// guid: b2a8d3f4-5c61-4e0a-9f7d-1c3e48a6b925
// last-edited: 2026-10-24

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// ErrBookFileRetired is returned by the generic book_file move and delete
// primitives when a row they were asked to touch is retired. A retired row is
// held by a retired book version; the only operations that may move or
// un-retire it are UnretireBookFiles and RehomeRetiredVersion, and nothing may
// delete it (never delete book_file rows: repoint them).
var ErrBookFileRetired = errors.New("book_file row is retired")

// refuseRetiredInsert rejects a caller-built row that would be INSERTED with
// Retired=true. Only RetireBookFiles re-keys an existing row into that state;
// a row born retired would have no shell it came from and no version to answer
// to. (An existing row is protected by the bfStoredOwned merge rule instead.)
func refuseRetiredInsert(f *BookFile) error {
	if f != nil && f.Retired {
		return fmt.Errorf("create book_file %q: a new row cannot be born retired: %w", f.FilePath, ErrBookFileRetired)
	}
	return nil
}

// ErrBookFileNotRetired is returned by UnretireBookFiles for a row that is not
// retired.
var ErrBookFileNotRetired = errors.New("book_file row is not retired")

// ErrRetiredVersionMismatch is returned when a retire, un-retire or re-home
// names a version that is not a retired version of the book it must belong to,
// or a row that does not carry the version it was asked to move with.
var ErrRetiredVersionMismatch = errors.New("retired version does not match")

// ErrRetiredVersionHasFiles is returned when a retired version that still has
// retired rows would be deleted, moved to another book, or leave the retired
// status. Un-retire its rows first.
var ErrRetiredVersionHasFiles = errors.New("retired version still holds retired book_file rows")

// ErrRetireIndexIncomplete is returned by RetireBookFiles when the retiring
// row owns a single-owner index entry (its path or content hash) and the store
// cannot prove that no other live row holds the same key, because the
// complete lookup (BookFilesAtPath, BookFilesWithHash) needs memdb and memdb
// is not serving. Nothing was written; retry once memdb is published.
var ErrRetireIndexIncomplete = errors.New("cannot prove the book_file index key has no other live owner; retry later")

// retiredVersionFromKey is idx:bv:from:<shell book id>:<version id>. The value
// is the version id.
func retiredVersionFromKey(shellID, versionID string) []byte {
	return []byte("idx:bv:from:" + shellID + ":" + versionID)
}

// refuseRetiredVersionChange guards UpdateBookVersion. A version only becomes
// retired through CreateBookVersion; one that is retired and still holds rows
// may not change status or book. (A retired version with no rows left, after
// UnretireBookFiles, may be changed or deleted freely.)
func (p *PebbleStore) refuseRetiredVersionChange(prev, v *BookVersion) error {
	if v.Status == BookVersionStatusRetired && prev.Status != BookVersionStatusRetired {
		return fmt.Errorf("book version %s: status %q is set only when the version is created: %w",
			v.ID, BookVersionStatusRetired, ErrRetiredVersionMismatch)
	}
	if prev.Status != BookVersionStatusRetired {
		return nil
	}
	if v.Status == BookVersionStatusRetired && v.BookID == prev.BookID {
		return nil
	}
	return p.refuseIfRetiredVersionHoldsFiles(prev, "change")
}

// refuseRetiredVersionDelete guards DeleteBookVersion.
func (p *PebbleStore) refuseRetiredVersionDelete(v *BookVersion) error {
	if v.Status != BookVersionStatusRetired {
		return nil
	}
	return p.refuseIfRetiredVersionHoldsFiles(v, "delete")
}

func (p *PebbleStore) refuseIfRetiredVersionHoldsFiles(v *BookVersion, verb string) error {
	rows, err := p.retiredRowsOfVersion(v.BookID, v.ID)
	if err != nil {
		return fmt.Errorf("%s book version %s: cannot verify it holds no retired rows: %w", verb, v.ID, err)
	}
	if len(rows) > 0 {
		return fmt.Errorf("%s book version %s: %w (%d row(s))", verb, v.ID, ErrRetiredVersionHasFiles, len(rows))
	}
	return nil
}

// retiredRowsOfVersion returns the retired rows under holderID that carry
// versionID, read from Pebble.
func (p *PebbleStore) retiredRowsOfVersion(holderID, versionID string) ([]BookFile, error) {
	rows, err := p.bookFilesForBook(holderID, bookFilesRetiredOnly)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, r := range rows {
		if r.VersionID == versionID {
			out = append(out, r)
		}
	}
	return out, nil
}

// GetBookFilesIncludingRetired returns every row keyed under bookID, live and
// retired. See RetiredBookFileReader.
func (p *PebbleStore) GetBookFilesIncludingRetired(bookID string) ([]BookFile, error) {
	return p.bookFilesForBook(bookID, bookFilesAll)
}

// GetRetiredBookFiles returns the retired rows keyed under holderID.
func (p *PebbleStore) GetRetiredBookFiles(holderID string) ([]BookFile, error) {
	return p.bookFilesForBook(holderID, bookFilesRetiredOnly)
}

// GetAllBookFilesCoreIncludingRetired is GetAllBookFilesCore with the retired
// rows kept. Like GetAllBookFilesCore it answers from memdb when published and
// from a Pebble scan otherwise.
func (p *PebbleStore) GetAllBookFilesCoreIncludingRetired() ([]BookFileCore, error) {
	if mem := p.memOrFallback("GetAllBookFilesCoreIncludingRetired"); mem != nil {
		return mem.GetAllBookFilesCoreIncludingRetired()
	}
	full, err := p.getAllBookFilesPebbleScanFiltered(bookFilesAll)
	if err != nil {
		return nil, err
	}
	out := make([]BookFileCore, len(full))
	for i := range full {
		out[i] = full[i].Core()
	}
	return out, nil
}

// GetBookFileByPIDIncludingRetired is GetBookFileByPID without the retired
// filter. See RetiredBookFileReader.
func (p *PebbleStore) GetBookFileByPIDIncludingRetired(itunesPID string) (*BookFile, error) {
	return p.getBookFileByPIDAny(itunesPID)
}

// GetBookFileByPathIncludingRetired is GetBookFileByPath without the retired
// filter: the single-row path-index hit, whoever holds it. It is the twin the
// "is this path claimed?" checks need, because GetBookFileByPath answers nil
// for a path only a retired row holds. Like the index it reads, it names one
// row per path CRC; BookFilesAtPath is the complete multi-row lookup.
func (p *PebbleStore) GetBookFileByPathIncludingRetired(filePath string) (*BookFile, error) {
	return p.getBookFileByPathAny(filePath)
}

// GetRetiredVersionsFromBook returns the retired versions retired out of
// shellID, oldest first.
func (p *PebbleStore) GetRetiredVersionsFromBook(shellID string) ([]BookVersion, error) {
	if shellID == "" {
		return nil, nil
	}
	prefix := []byte("idx:bv:from:" + shellID + ":")
	var out []BookVersion
	if err := forEachKeyInRange(p.db, prefix, prefixEnd(prefix), func(key, _ []byte) error {
		versionID := strings.TrimPrefix(string(key), string(prefix))
		v, err := p.GetBookVersion(versionID)
		if err != nil {
			return fmt.Errorf("GetRetiredVersionsFromBook %s: reading version %s: %w", shellID, versionID, err)
		}
		if v != nil {
			out = append(out, *v)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ── The single-owner index rule ─────────────────────────────────────────────
//
// book_file_path:, book_file_pid:, book_file_hash:, book_file_orig_hash: and
// book_file_acoustid: each hold ONE row reference per key ("<bookID>:<fileID>"),
// whichever row wrote last. A retired row keeps its rows' secondary indexes (so
// the complete lookups, BookFilesAtPath and BookFilesWithHash, and the undo
// that restores the row still find it), but it must never HIDE a live row:
// GetBookFileByPath on a path the live target also holds has to answer the live
// row, not the retired copy. The single-identity getters (ByPath, ByPID,
// ByAcoustID, GetBookBySegmentFileHash) answer "which LIVE file has this
// identity" and so answer nil when the key is held only by a retired row.
//
// So retiring row X, for each single-owner key K:
//   - K is not X's (another row owns it, or nothing does): leave it.
//   - K is X's: repoint it to another LIVE row carrying the same path, PID or
//     hash if one exists (the holder's own rows first, then the complete
//     lookups BookFilesAtPath / BookFilesWithHash), and only when none does,
//     X's retired row keeps the key.
//
// Un-retiring is the inverse: the live row claims a key that is free, its own,
// or held by a retired row; a key held by another live row stays with it.

type singleOwnerKind uint8

const (
	ownerPath singleOwnerKind = iota + 1
	ownerPID
	ownerFileHash
	ownerOrigHash
	ownerSeg
)

type singleOwnerKey struct {
	kind  singleOwnerKind
	key   []byte
	value string // the path, PID, hash or segment the key is named for
}

// singleOwnerKeysOf lists the single-owner index keys writeBookFileSecondaryIndexes
// writes for f, in a fixed order.
func singleOwnerKeysOf(f *BookFile) []singleOwnerKey {
	var out []singleOwnerKey
	if f.FilePath != "" {
		out = append(out, singleOwnerKey{ownerPath, []byte("book_file_path:" + bookFilePathCRC(f.FilePath)), f.FilePath})
	}
	if f.ITunesPersistentID != "" {
		out = append(out, singleOwnerKey{ownerPID, []byte("book_file_pid:" + f.ITunesPersistentID), f.ITunesPersistentID})
	}
	if f.FileHash != "" {
		out = append(out, singleOwnerKey{ownerFileHash, []byte("book_file_hash:" + f.FileHash), f.FileHash})
	}
	if f.OriginalFileHash != "" && f.OriginalFileHash != f.FileHash {
		out = append(out, singleOwnerKey{ownerOrigHash, []byte("book_file_orig_hash:" + f.OriginalFileHash), f.OriginalFileHash})
	}
	for _, seg := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
		f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
		if seg != "" {
			out = append(out, singleOwnerKey{ownerSeg, []byte("book_file_acoustid:" + seg), seg})
		}
	}
	return out
}

// carries reports whether row would write k's key.
func (k singleOwnerKey) carries(row *BookFile) bool {
	switch k.kind {
	case ownerPath:
		return row.FilePath == k.value
	case ownerPID:
		return row.ITunesPersistentID == k.value
	case ownerFileHash:
		return row.FileHash == k.value
	case ownerOrigHash:
		return row.OriginalFileHash == k.value && row.OriginalFileHash != row.FileHash
	}
	return false
}

func fileRef(bookID, fileID string) string { return bookID + ":" + fileID }

// liveAlternativeOwner finds a live row other than the retiring ones that
// carries k, or nil when there is none. holderLive is the holder's own live
// rows (Pebble-direct, always complete). The complete lookups are consulted
// only when the holder has no candidate; they need memdb, and when it is not
// serving the answer is ErrRetireIndexIncomplete rather than a guess.
func (p *PebbleStore) liveAlternativeOwner(k singleOwnerKey, holderLive []BookFile, retiring map[string]struct{}) (*BookFile, error) {
	for i := range holderLive {
		r := &holderLive[i]
		if _, skip := retiring[r.ID]; !skip && !r.Retired && k.carries(r) {
			return r, nil
		}
	}
	var rows []BookFile
	var err error
	switch k.kind {
	case ownerPath:
		rows, err = p.BookFilesAtPath(k.value)
		if errors.Is(err, ErrBookFilesAtPathUnavailable) {
			return nil, fmt.Errorf("%w: path %q: %w", ErrRetireIndexIncomplete, k.value, err)
		}
	case ownerFileHash, ownerOrigHash:
		rows, err = p.BookFilesWithHash(k.value)
		if errors.Is(err, ErrBookFilesWithHashUnavailable) {
			return nil, fmt.Errorf("%w: hash %q: %w", ErrRetireIndexIncomplete, k.value, err)
		}
	default:
		// No complete lookup exists for an iTunes PID or a legacy segment, and
		// neither is shared between live rows by construction (the PID transfer
		// strips the prior owner). Only the holder's rows were candidates.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var best *BookFile
	for i := range rows {
		r := &rows[i]
		if _, skip := retiring[r.ID]; skip || r.Retired || !k.carries(r) {
			continue
		}
		if best == nil || r.BookID < best.BookID || (r.BookID == best.BookID && r.ID < best.ID) {
			best = r
		}
	}
	return best, nil
}

// requireRetiredVersionOf checks that versionID is a retired version of
// holderID.
func (p *PebbleStore) requireRetiredVersionOf(versionID, holderID string) (*BookVersion, error) {
	v, err := p.GetBookVersion(versionID)
	if err != nil {
		return nil, fmt.Errorf("read book version %s: %w", versionID, err)
	}
	if v == nil {
		return nil, fmt.Errorf("book version %s does not exist: %w", versionID, ErrRetiredVersionMismatch)
	}
	if v.Status != BookVersionStatusRetired {
		return nil, fmt.Errorf("book version %s has status %q, want %q: %w", versionID, v.Status, BookVersionStatusRetired, ErrRetiredVersionMismatch)
	}
	if v.BookID != holderID {
		return nil, fmt.Errorf("book version %s belongs to book %s, not %s: %w", versionID, v.BookID, holderID, ErrRetiredVersionMismatch)
	}
	return v, nil
}

// requireHolderNotSoftDeleted refuses a holder that is missing or in the
// trash. The check is a pre-read, not under the owner stripe: a holder trashed
// between this read and the commit ends up holding retired rows while
// soft-deleted, which DeleteBook still refuses (ErrBookOwnsFiles) and the
// version lifecycle guards treat as a holder with retired files.
func (p *PebbleStore) requireHolderNotSoftDeleted(holderID string) error {
	b, err := p.GetBookByID(holderID)
	if err != nil {
		return fmt.Errorf("read holder book %s: %w", holderID, err)
	}
	if b == nil {
		return fmt.Errorf("%w: holder %s", ErrBookFileOwnerMissing, holderID)
	}
	if bookIsSoftDeleted(b) {
		return fmt.Errorf("holder book %s is soft-deleted: %w", holderID, ErrBookFileOwnerMissing)
	}
	return nil
}

// setKeys returns the keys of a string set.
func setKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// sortedFileIDs returns the sorted file IDs of rows.
func sortedFileIDs(rows []BookFile) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].ID
	}
	slices.Sort(out)
	return out
}

// moveFileIDs flattens moves to a de-duplicated id set and rejects an empty
// move list or a duplicate id (a row retired twice in one call would be
// staged twice).
func moveFileIDs(moves []BookFileMove) (map[string]struct{}, error) {
	ids := make(map[string]struct{})
	for _, mv := range moves {
		if mv.SourceBookID == "" {
			return nil, errors.New("book_file move names no source book")
		}
		for _, id := range mv.FileIDs {
			if id == "" {
				return nil, errors.New("book_file move names an empty file id")
			}
			if _, dup := ids[id]; dup {
				return nil, fmt.Errorf("book_file %s named twice in one move", id)
			}
			ids[id] = struct{}{}
		}
	}
	return ids, nil
}

// RetireBookFiles re-keys the rows named by moves from their source book (the
// shell) to holderID as retired rows of the retired version versionID.
//
// One atomic batch, under the owner stripes of the shell(s) and the holder: it
// either retires every row or none. No row is deleted: every row keeps its ID,
// its content and its fingerprint, and the book_file row count is unchanged.
// Refused, with nothing written: a row that is missing under its stated
// source, or already retired (ErrBookFileRetired); a version that is not a
// retired version of the holder (ErrRetiredVersionMismatch); a holder that is
// missing or soft-deleted (ErrBookFileOwnerMissing); a shell equal to the
// holder; and an index key that cannot be proven free of another live owner
// (ErrRetireIndexIncomplete, retry later). The index rule is described above.
func (p *PebbleStore) RetireBookFiles(moves []BookFileMove, holderID, versionID string) error {
	if len(moves) == 0 {
		return nil
	}
	if holderID == "" || versionID == "" {
		return errors.New("RetireBookFiles: holder and version are required")
	}
	ids, err := moveFileIDs(moves)
	if err != nil {
		return fmt.Errorf("RetireBookFiles: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	for _, mv := range moves {
		if mv.SourceBookID == holderID {
			return fmt.Errorf("RetireBookFiles: book %s cannot be retired into itself", holderID)
		}
	}
	// The version stripe makes "versionID is a retired version of holderID"
	// stay true until the rows are re-keyed: RehomeRetiredVersion and
	// UpdateBookVersion take it too.
	unlockVersion := p.lockBookVersion(versionID)
	defer unlockVersion()
	if _, err := p.requireRetiredVersionOf(versionID, holderID); err != nil {
		return fmt.Errorf("RetireBookFiles: %w", err)
	}
	if err := p.requireHolderNotSoftDeleted(holderID); err != nil {
		return fmt.Errorf("RetireBookFiles: %w", err)
	}
	for range maxStageRetries {
		err = p.retireBookFilesAttempt(moves, ids, holderID, versionID)
		if !errors.Is(err, errBookFileRowVanished) || errors.Is(err, ErrBookFileOwnerMissing) {
			return err
		}
	}
	return fmt.Errorf("RetireBookFiles: %w", err)
}

func (p *PebbleStore) retireBookFilesAttempt(moves []BookFileMove, retiring map[string]struct{}, holderID, versionID string) error {
	// Every row's stripe is held from the read below to the commit, so a
	// concurrent UpdateBookFile (a fingerprint or transcript write) lands before
	// the read, and is carried into the retired row, or after the commit, and is
	// applied to the retired row; it is never overwritten by the batch.
	unlockFiles := p.lockBookFiles(setKeys(retiring))
	filesLocked := true
	releaseFiles := func() {
		if filesLocked {
			filesLocked = false
			unlockFiles()
		}
	}
	defer releaseFiles()
	holderLive, err := p.bookFilesForBook(holderID, bookFilesLive)
	if err != nil {
		return fmt.Errorf("RetireBookFiles: read holder %s rows: %w", holderID, err)
	}
	batch := p.db.NewIndexedBatch()

	var moved []*BookFile
	var oldKeys [][]byte
	affected := []string{holderID}
	seen := map[string]struct{}{holderID: {}}
	now := time.Now()

	for _, mv := range moves {
		if _, ok := seen[mv.SourceBookID]; !ok {
			seen[mv.SourceBookID] = struct{}{}
			affected = append(affected, mv.SourceBookID)
		}
		for _, fid := range mv.FileIDs {
			f, err := p.getBookFileByID(mv.SourceBookID, fid)
			if err != nil {
				batch.Close()
				return fmt.Errorf("RetireBookFiles: read %s: %w", fid, err)
			}
			if f == nil {
				batch.Close()
				return fmt.Errorf("RetireBookFiles: file not found: %s under %s", fid, mv.SourceBookID)
			}
			if f.Retired {
				batch.Close()
				return fmt.Errorf("RetireBookFiles: %s: %w", fid, ErrBookFileRetired)
			}

			oldKey := []byte("book_file:" + mv.SourceBookID + ":" + fid)
			oldKeys = append(oldKeys, oldKey)
			if err := batch.Delete(oldKey, nil); err != nil {
				batch.Close()
				return err
			}

			oldRef := fileRef(f.BookID, f.ID)
			newRef := fileRef(holderID, f.ID)
			for _, k := range singleOwnerKeysOf(f) {
				cur, found, err := p.hashIndexValue(batch, k.key)
				if err != nil {
					batch.Close()
					return err
				}
				if !found || cur != oldRef {
					continue // not this row's key: leave it
				}
				owner := newRef // no other live row: the retired row keeps the key
				if k.kind != ownerSeg {
					alt, err := p.liveAlternativeOwner(k, holderLive, retiring)
					if err != nil {
						batch.Close()
						return fmt.Errorf("RetireBookFiles: %s: %w", fid, err)
					}
					if alt != nil {
						owner = fileRef(alt.BookID, alt.ID)
						// The alternative was read before the owner stripes are
						// taken: re-check its key under them.
						oldKeys = append(oldKeys, []byte("book_file:"+alt.BookID+":"+alt.ID))
					}
				}
				if err := batch.Set(k.key, []byte(owner), nil); err != nil {
					batch.Close()
					return err
				}
			}
			if err := batch.Set([]byte("book_file_id:"+f.ID), []byte("book_file:"+holderID+":"+f.ID), nil); err != nil {
				batch.Close()
				return err
			}

			f.BookID = holderID
			f.Retired = true
			f.VersionID = versionID
			f.UpdatedAt = now
			data, err := json.Marshal(f)
			if err != nil {
				batch.Close()
				return err
			}
			if err := batch.Set([]byte("book_file:"+holderID+":"+f.ID), data, nil); err != nil {
				batch.Close()
				return err
			}
			moved = append(moved, f)
		}
	}

	durErr := p.commitBookFileBatch(batch, affected, []string{holderID}, oldKeys)
	// The aggregate recompute below takes book stripes, and no book_file stripe
	// may be held while one is taken.
	releaseFiles()
	if !bookFileApplied(durErr) {
		return durErr
	}
	p.afterRetireWrite(moved, affected, "retire_book_files")
	return durErr
}

// afterRetireWrite is the post-commit derived-state refresh every book_file
// mutator runs: library stats, the quick-query dirty mark, memdb (keyed by file
// ID, so the re-keyed row replaces the old one in place) and the aggregates of
// every book touched (the holder's cover live rows only, so it is unchanged by
// a retire; the shell's is recomputed under RecomputeBookAggregates'
// partial-data rule).
func (p *PebbleStore) afterRetireWrite(rows []*BookFile, affected []string, reason string) {
	p.InvalidateLibraryStats()
	p.MarkQuickQueryDirty("no_fingerprints", reason)
	for _, f := range rows {
		p.UpsertBookFileToMemDB(f)
	}
	p.notifyBookFileChanges(affected)
}

// UnretireBookFiles is the inverse of RetireBookFiles. moves name the retired
// rows under the holder (SourceBookID is the holder); they go back under
// backToBookID with Retired cleared and VersionID restored from
// versionIDBefore. Every row must be retired and carry retireVersionID, or
// nothing is written.
func (p *PebbleStore) UnretireBookFiles(moves []BookFileMove, backToBookID, retireVersionID string, versionIDBefore map[string]string) error {
	if len(moves) == 0 {
		return nil
	}
	if backToBookID == "" || retireVersionID == "" {
		return errors.New("UnretireBookFiles: destination book and retire version are required")
	}
	ids, err := moveFileIDs(moves)
	if err != nil {
		return fmt.Errorf("UnretireBookFiles: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	for _, mv := range moves {
		if mv.SourceBookID == backToBookID {
			return fmt.Errorf("UnretireBookFiles: rows are already under book %s", backToBookID)
		}
	}
	unlockVersion := p.lockBookVersion(retireVersionID)
	defer unlockVersion()
	for range maxStageRetries {
		err = p.unretireBookFilesAttempt(moves, backToBookID, retireVersionID, versionIDBefore)
		if !errors.Is(err, errBookFileRowVanished) || errors.Is(err, ErrBookFileOwnerMissing) {
			return err
		}
	}
	return fmt.Errorf("UnretireBookFiles: %w", err)
}

func (p *PebbleStore) unretireBookFilesAttempt(moves []BookFileMove, backToBookID, retireVersionID string, versionIDBefore map[string]string) error {
	// Row stripes held from the first read to the commit; see
	// retireBookFilesAttempt.
	var fileIDs []string
	for _, mv := range moves {
		fileIDs = append(fileIDs, mv.FileIDs...)
	}
	unlockFiles := p.lockBookFiles(fileIDs)
	filesLocked := true
	releaseFiles := func() {
		if filesLocked {
			filesLocked = false
			unlockFiles()
		}
	}
	defer releaseFiles()
	batch := p.db.NewIndexedBatch()
	var moved []*BookFile
	var oldKeys [][]byte
	affected := []string{backToBookID}
	seen := map[string]struct{}{backToBookID: {}}
	now := time.Now()

	for _, mv := range moves {
		if _, ok := seen[mv.SourceBookID]; !ok {
			seen[mv.SourceBookID] = struct{}{}
			affected = append(affected, mv.SourceBookID)
		}
		for _, fid := range mv.FileIDs {
			f, err := p.getBookFileByID(mv.SourceBookID, fid)
			if err != nil {
				batch.Close()
				return fmt.Errorf("UnretireBookFiles: read %s: %w", fid, err)
			}
			if f == nil {
				batch.Close()
				return fmt.Errorf("UnretireBookFiles: file not found: %s under %s", fid, mv.SourceBookID)
			}
			if !f.Retired {
				batch.Close()
				return fmt.Errorf("UnretireBookFiles: %s: %w", fid, ErrBookFileNotRetired)
			}
			if f.VersionID != retireVersionID {
				batch.Close()
				return fmt.Errorf("UnretireBookFiles: %s carries version %q, not %q: %w", fid, f.VersionID, retireVersionID, ErrRetiredVersionMismatch)
			}

			oldKey := []byte("book_file:" + mv.SourceBookID + ":" + fid)
			oldKeys = append(oldKeys, oldKey)
			if err := batch.Delete(oldKey, nil); err != nil {
				batch.Close()
				return err
			}

			oldRef := fileRef(f.BookID, f.ID)
			newRef := fileRef(backToBookID, f.ID)
			for _, k := range singleOwnerKeysOf(f) {
				cur, found, err := p.hashIndexValue(batch, k.key)
				if err != nil {
					batch.Close()
					return err
				}
				claim := !found || cur == oldRef
				if !claim {
					held, err := p.indexOwnerIsRetiredOrGone(cur)
					if err != nil {
						batch.Close()
						return err
					}
					claim = held
				}
				if claim {
					if err := batch.Set(k.key, []byte(newRef), nil); err != nil {
						batch.Close()
						return err
					}
				}
			}
			if err := batch.Set([]byte("book_file_id:"+f.ID), []byte("book_file:"+backToBookID+":"+f.ID), nil); err != nil {
				batch.Close()
				return err
			}

			f.BookID = backToBookID
			f.Retired = false
			f.VersionID = versionIDBefore[f.ID]
			f.UpdatedAt = now
			data, err := json.Marshal(f)
			if err != nil {
				batch.Close()
				return err
			}
			if err := batch.Set([]byte("book_file:"+backToBookID+":"+f.ID), data, nil); err != nil {
				batch.Close()
				return err
			}
			moved = append(moved, f)
		}
	}

	durErr := p.commitBookFileBatch(batch, affected, []string{backToBookID}, oldKeys)
	releaseFiles()
	if !bookFileApplied(durErr) {
		return durErr
	}
	p.afterRetireWrite(moved, affected, "unretire_book_files")
	return durErr
}

// indexOwnerIsRetiredOrGone reports whether the "<bookID>:<fileID>" reference
// ref names a retired row or a row that no longer exists. A live row never
// yields its index key to a retired one's return, but a retired or vanished
// owner does.
func (p *PebbleStore) indexOwnerIsRetiredOrGone(ref string) (bool, error) {
	bookID, fileID, ok := strings.Cut(ref, ":")
	if !ok {
		return true, nil // a malformed reference names nothing
	}
	row, err := p.getBookFileByID(bookID, fileID)
	if err != nil {
		return false, err
	}
	return row == nil || row.Retired, nil
}

// RehomeRetiredVersion moves a retired version, and every retired row it
// holds, from its current holder to newHolderID. It is for the chain case: the
// holder is itself being retired into another book, and the versions it holds
// must follow the files they describe rather than be stranded under a shell.
// One atomic batch (the version row and its rows commit together). The new
// holder must not be soft-deleted; the inverse, which has to accept the old
// holder the chain case trashed, is RestoreRetiredVersionHolder.
func (p *PebbleStore) RehomeRetiredVersion(versionID, newHolderID string) error {
	if versionID == "" || newHolderID == "" {
		return errors.New("RehomeRetiredVersion: version and new holder are required")
	}
	if err := p.requireHolderNotSoftDeleted(newHolderID); err != nil {
		return fmt.Errorf("RehomeRetiredVersion: %w", err)
	}
	return p.moveRetiredVersion(versionID, newHolderID)
}

// RestoreRetiredVersionHolder is the revert of a RehomeRetiredVersion: it moves
// the retired version and its rows back to previousHolderID, the holder the
// rehome journaled as the one the version left. Unlike RehomeRetiredVersion it
// accepts a soft-deleted destination, because in the chain case the previous
// holder is trashed by definition (it was being retired into another book when
// the version left it) and a revert does not un-trash it. A destination that
// does not exist is still refused (ErrBookFileOwnerMissing), so the caller
// recreates a purged one first. Resolve it with AsCapability
// (RetiredVersionHolderRestorer); it is not part of RetiredBookFileWriter.
func (p *PebbleStore) RestoreRetiredVersionHolder(versionID, previousHolderID string) error {
	if versionID == "" || previousHolderID == "" {
		return errors.New("RestoreRetiredVersionHolder: version and previous holder are required")
	}
	b, err := p.GetBookByID(previousHolderID)
	if err != nil {
		return fmt.Errorf("RestoreRetiredVersionHolder: read previous holder %s: %w", previousHolderID, err)
	}
	if b == nil {
		return fmt.Errorf("RestoreRetiredVersionHolder: %w: previous holder %s", ErrBookFileOwnerMissing, previousHolderID)
	}
	return p.moveRetiredVersion(versionID, previousHolderID)
}

// moveRetiredVersion is the locked, retried body both rehome entry points
// share; the caller has already decided whether the destination is acceptable.
func (p *PebbleStore) moveRetiredVersion(versionID, newHolderID string) error {
	// The version stripe excludes RetireBookFiles, UnretireBookFiles and
	// UpdateBookVersion for this version, so neither its holder nor the set of
	// rows that carry it changes between the reads below and the commit.
	unlockVersion := p.lockBookVersion(versionID)
	defer unlockVersion()
	var err error
	for range maxStageRetries {
		err = p.rehomeRetiredVersionAttempt(versionID, newHolderID)
		if !errors.Is(err, errBookFileRowVanished) || errors.Is(err, ErrBookFileOwnerMissing) {
			return err
		}
	}
	return fmt.Errorf("RehomeRetiredVersion: %w", err)
}

func (p *PebbleStore) rehomeRetiredVersionAttempt(versionID, newHolderID string) error {
	v, err := p.GetBookVersion(versionID)
	if err != nil {
		return fmt.Errorf("RehomeRetiredVersion: read version %s: %w", versionID, err)
	}
	if v == nil || v.Status != BookVersionStatusRetired {
		return fmt.Errorf("RehomeRetiredVersion: book version %s is not a retired version: %w", versionID, ErrRetiredVersionMismatch)
	}
	oldHolder := v.BookID
	if oldHolder == newHolderID {
		return fmt.Errorf("RehomeRetiredVersion: version %s is already held by %s", versionID, newHolderID)
	}
	if newHolderID == v.RetiredFromBookID {
		return fmt.Errorf("RehomeRetiredVersion: version %s was retired out of %s and cannot be held by it: %w",
			versionID, newHolderID, ErrRetiredVersionMismatch)
	}
	rows, err := p.retiredRowsOfVersion(oldHolder, versionID)
	if err != nil {
		return fmt.Errorf("RehomeRetiredVersion: read rows of %s: %w", versionID, err)
	}
	// Lock the rows' stripes, then read them again: a single-row writer that
	// committed after the first read is carried into the batch, and one that
	// arrives later waits for the commit. The row set itself cannot change
	// (the version stripe is held); a mismatch means a row left the version some
	// other way, and the attempt starts over.
	unlockFiles := p.lockBookFiles(sortedFileIDs(rows))
	filesLocked := true
	releaseFiles := func() {
		if filesLocked {
			filesLocked = false
			unlockFiles()
		}
	}
	defer releaseFiles()
	fresh, err := p.retiredRowsOfVersion(oldHolder, versionID)
	if err != nil {
		return fmt.Errorf("RehomeRetiredVersion: re-read rows of %s: %w", versionID, err)
	}
	if !slices.Equal(sortedFileIDs(fresh), sortedFileIDs(rows)) {
		return fmt.Errorf("RehomeRetiredVersion: the rows of %s changed while locking them: %w", versionID, errBookFileRowVanished)
	}
	rows = fresh

	batch := p.db.NewIndexedBatch()
	var moved []*BookFile
	var oldKeys [][]byte
	now := time.Now()
	for i := range rows {
		f := &rows[i]
		oldKey := []byte("book_file:" + oldHolder + ":" + f.ID)
		oldKeys = append(oldKeys, oldKey)
		if err := batch.Delete(oldKey, nil); err != nil {
			batch.Close()
			return err
		}
		oldRef := fileRef(oldHolder, f.ID)
		newRef := fileRef(newHolderID, f.ID)
		for _, k := range singleOwnerKeysOf(f) {
			cur, found, err := p.hashIndexValue(batch, k.key)
			if err != nil {
				batch.Close()
				return err
			}
			if found && cur == oldRef {
				if err := batch.Set(k.key, []byte(newRef), nil); err != nil {
					batch.Close()
					return err
				}
			}
		}
		if err := batch.Set([]byte("book_file_id:"+f.ID), []byte("book_file:"+newHolderID+":"+f.ID), nil); err != nil {
			batch.Close()
			return err
		}
		f.BookID = newHolderID
		f.UpdatedAt = now
		data, err := json.Marshal(f)
		if err != nil {
			batch.Close()
			return err
		}
		if err := batch.Set([]byte("book_file:"+newHolderID+":"+f.ID), data, nil); err != nil {
			batch.Close()
			return err
		}
		moved = append(moved, f)
	}

	// The version row moves in the same batch, so a reader never sees rows
	// under one holder and their version under another.
	nv := *v
	nv.BookID = newHolderID
	nv.UpdatedAt = now
	nv.Version = v.Version + 1
	vdata, err := json.Marshal(&nv)
	if err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set([]byte("bv:"+versionID), vdata, nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Delete([]byte("idx:bv:book:"+oldHolder+":"+versionID), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set([]byte("idx:bv:book:"+newHolderID+":"+versionID), []byte("1"), nil); err != nil {
		batch.Close()
		return err
	}

	affected := []string{newHolderID, oldHolder}
	durErr := p.commitBookFileBatch(batch, affected, []string{newHolderID}, oldKeys)
	releaseFiles()
	if !bookFileApplied(durErr) {
		return durErr
	}
	p.afterRetireWrite(moved, affected, "rehome_retired_version")
	return durErr
}

// writeBookFileSecondaryIndexesFor is writeBookFileSecondaryIndexes for a row
// that already exists and is being rewritten in place (UpdateBookFile, the
// batch upserts). A live row writes its indexes exactly as before. A RETIRED
// row writes a single-owner key (path, PID, hash, segment) only when the key is
// free, its own, or held by another retired row or a row that is gone: the
// rewrite of a retired row must never take a key away from the live row that
// RetireBookFiles handed it to, or GetBookFileByPath and the hash lookups would
// answer the retired copy instead of the file in the library.
func (p *PebbleStore) writeBookFileSecondaryIndexesFor(batch *GatedBatch, f *BookFile) error {
	if !f.Retired {
		return writeBookFileSecondaryIndexes(batch, f)
	}
	ref := []byte(fileRef(f.BookID, f.ID))
	if err := batch.Set([]byte("book_file_id:"+f.ID), []byte("book_file:"+f.BookID+":"+f.ID), nil); err != nil {
		return err
	}
	for _, k := range singleOwnerKeysOf(f) {
		cur, found, err := p.hashIndexValue(batch, k.key)
		if err != nil {
			return err
		}
		claim := !found || cur == string(ref)
		if !claim {
			if claim, err = p.indexOwnerIsRetiredOrGone(cur); err != nil {
				return err
			}
		}
		if claim {
			if err := batch.Set(k.key, ref, nil); err != nil {
				return err
			}
		}
	}
	return writeFingerprintLSHIndexes(batch, f)
}
