// file: internal/database/pebble_store_bookfiles.go
// version: 1.2.0
// guid: bee03868-fbc4-48b0-9c9a-11180e19779e
// last-edited: 2026-07-05

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// getBookFileByID fetches a BookFile by its primary key (book_file:<bookID>:<fileID>).
func (s *PebbleStore) getBookFileByID(bookID, fileID string) (*BookFile, error) {
	key := []byte(fmt.Sprintf("book_file:%s:%s", bookID, fileID))
	value, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var f BookFile
	if err := json.Unmarshal(value, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// deleteBookFileSecondaryIndexes removes PID, path, and acoustid secondary
// index entries from the batch for the given BookFile. The store pointer
// is used to read the LSH meta-row from the committed DB state — Pebble's
// non-indexed batch can't satisfy that lookup itself.
func (s *PebbleStore) deleteBookFileSecondaryIndexes(batch *pebble.Batch, f *BookFile) error {
	// Delete book_file_id secondary index.
	if f.ID != "" {
		if err := batch.Delete([]byte(fmt.Sprintf("book_file_id:%s", f.ID)), nil); err != nil {
			return err
		}
	}

	if f.ITunesPersistentID != "" {
		pidKey := []byte(fmt.Sprintf("book_file_pid:%s", f.ITunesPersistentID))
		if err := batch.Delete(pidKey, nil); err != nil {
			return err
		}
	}

	if f.FilePath != "" {
		pathKey := []byte(fmt.Sprintf("book_file_path:%s", bookFilePathCRC(f.FilePath)))
		if err := batch.Delete(pathKey, nil); err != nil {
			return err
		}
	}

	if f.FileHash != "" {
		hashKey := []byte(fmt.Sprintf("book_file_hash:%s", f.FileHash))
		if err := batch.Delete(hashKey, nil); err != nil {
			return err
		}
	}

	if f.OriginalFileHash != "" && f.OriginalFileHash != f.FileHash {
		origKey := []byte(fmt.Sprintf("book_file_orig_hash:%s", f.OriginalFileHash))
		if err := batch.Delete(origKey, nil); err != nil {
			return err
		}
	}

	// Delete secondary index for each non-empty fingerprint segment.
	for _, seg := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
		f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
		if seg != "" {
			acoustKey := []byte(fmt.Sprintf("book_file_acoustid:%s", seg))
			if err := batch.Delete(acoustKey, nil); err != nil {
				return err
			}
		}
	}

	// LSH index — best-effort delete via the fpidx_meta side-table.
	if err := deleteFingerprintLSHIndexesByIDWithStore(s, batch, f.ID); err != nil {
		return err
	}
	return nil
}

// LookupAcoustIDCandidates returns BookFile IDs whose LSH subprints collide
// with fp on ≥ fingerprint.LSHMinBandHits bands. Sorted by hit-count desc,
// capped at maxCandidates. Empty fp ⇒ ([], nil).
func (s *PebbleStore) LookupAcoustIDCandidates(fp []byte, maxCandidates int) ([]string, error) {
	if len(fp) == 0 {
		return nil, nil
	}
	subs, bands, err := fingerprint.Subprints(fp)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, nil
	}
	if maxCandidates <= 0 {
		maxCandidates = 200
	}

	hits := make(map[string]int, 256)
	for i := range subs {
		// Iterate fpidx:<band><subprint>:* — the trailing ':' anchors us at
		// the start of the per-(band,subprint) range; upper bound is the
		// next subprint by lexicographic order.
		lower := lshIndexKey(bands[i], subs[i], "")
		upper := append([]byte{}, lower...)
		// Bump the last byte (':') to ';' to form the exclusive upper bound.
		upper[len(upper)-1] = ';'
		iter, ierr := s.db.NewIter(&pebble.IterOptions{
			LowerBound: lower,
			UpperBound: upper,
		})
		if ierr != nil {
			return nil, ierr
		}
		for iter.First(); iter.Valid(); iter.Next() {
			key := iter.Key()
			// key = fpidx:<band:1><subprint:8>:<bookFileID>
			// id starts after prefix(6) + 1 + 8 + 1 = 16
			if len(key) <= 16 {
				continue
			}
			id := string(key[16:])
			hits[id]++
		}
		_ = iter.Close()
	}

	type scoredID struct {
		id  string
		hit int
	}
	scored := make([]scoredID, 0, len(hits))
	for id, n := range hits {
		if n < fingerprint.LSHMinBandHits {
			continue
		}
		scored = append(scored, scoredID{id, n})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].hit != scored[j].hit {
			return scored[i].hit > scored[j].hit
		}
		return scored[i].id < scored[j].id
	})
	if len(scored) > maxCandidates {
		scored = scored[:maxCandidates]
	}
	out := make([]string, len(scored))
	for i, s := range scored {
		out[i] = s.id
	}
	return out, nil
}

// HasLSHIndex reports whether a BookFile has an LSH meta entry that matches
// the current LSHIndexVersion. Returns false for missing entries AND for
// entries written by an older index version, so the build op rewrites stale
// rows whenever LSHIndexVersion or LSHBandCount changes.
func (s *PebbleStore) HasLSHIndex(bookFileID string) bool {
	val, closer, err := s.db.Get(lshMetaKey(bookFileID))
	if err != nil {
		return false
	}
	defer closer.Close()
	return len(val) > 0 && val[0] == fingerprint.LSHIndexVersion
}

// CreateBookFile stores a new BookFile, generating a ULID if the ID is empty.
// It writes the primary key book_file:<bookID>:<fileID> and secondary indexes
// for iTunes PID and file path (when non-empty) atomically in a single batch.
func (s *PebbleStore) CreateBookFile(file *BookFile) error {
	// CONS-18: repair millisecond-valued durations at the write chokepoint so no
	// ingest path can re-create the ms/seconds corruption (CONS-16).
	normalizeBookFileDuration(file)

	if file.ID == "" {
		id, err := newULID()
		if err != nil {
			return err
		}
		file.ID = id
	}

	now := time.Now()
	if file.CreatedAt.IsZero() {
		file.CreatedAt = now
	}
	file.UpdatedAt = now

	// T020: drop AcoustIDSeg0..6 from the stored value via a copy; the
	// original struct is preserved for writeBookFileSecondaryIndexes and
	// UpsertBookFileToMemDB below.
	data, err := marshalBookFileDropSegs(file)
	if err != nil {
		return err
	}

	batch := s.db.NewBatch()

	key := []byte(fmt.Sprintf("book_file:%s:%s", file.BookID, file.ID))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return err
	}

	if err := writeBookFileSecondaryIndexes(batch, file); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "create_book_file")
	s.UpsertBookFileToMemDB(file)
	// Recompute book-level Duration/FileSize aggregates now that a file was added.
	// Best-effort: the file write already committed; don't fail on aggregate errors.
	s.notifyBookFileChange(file.BookID)
	return nil
}

// UpdateBookFile replaces an existing BookFile, cleaning up stale secondary
// indexes when the PID or path changes.
func (s *PebbleStore) UpdateBookFile(id string, file *BookFile) error {
	// We need the bookID to build the primary key; it must be set on file.
	old, err := s.getBookFileByID(file.BookID, id)
	if err != nil {
		return err
	}
	if old == nil {
		return fmt.Errorf("book file not found: %s", id)
	}

	file.ID = id
	file.CreatedAt = old.CreatedAt
	file.UpdatedAt = time.Now()

	// T020: drop AcoustIDSeg0..6 from the stored value via a copy.
	data, err := marshalBookFileDropSegs(file)
	if err != nil {
		return err
	}

	batch := s.db.NewBatch()

	// Remove stale secondary indexes before writing new ones.
	if err := s.deleteBookFileSecondaryIndexes(batch, old); err != nil {
		batch.Close()
		return err
	}

	key := []byte(fmt.Sprintf("book_file:%s:%s", file.BookID, file.ID))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return err
	}

	if err := writeBookFileSecondaryIndexes(batch, file); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "update_book_file")
	s.UpsertBookFileToMemDB(file)
	// Recompute book-level Duration/FileSize aggregates now that a file was updated.
	// Best-effort: the file write already committed; don't fail on aggregate errors.
	s.notifyBookFileChange(file.BookID)
	return nil
}

// GetBookFiles returns all BookFile records for the given bookID by iterating
// the prefix book_file:<bookID>:.
func (s *PebbleStore) GetBookFiles(bookID string) ([]BookFile, error) {
	prefix := []byte(fmt.Sprintf("book_file:%s:", bookID))
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte(nil), prefix...), 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var files []BookFile
	for iter.First(); iter.Valid(); iter.Next() {
		var f BookFile
		if err := json.Unmarshal(iter.Value(), &f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	// Sort: disc ASC, track ASC, file_path ASC — matches SQLite ORDER BY.
	sort.Slice(files, func(i, j int) bool {
		if files[i].DiscNumber != files[j].DiscNumber {
			return files[i].DiscNumber < files[j].DiscNumber
		}
		if files[i].TrackNumber != files[j].TrackNumber {
			return files[i].TrackNumber < files[j].TrackNumber
		}
		return files[i].FilePath < files[j].FilePath
	})
	return files, nil
}

// GetBookFilesForIDsCore returns book files grouped by bookID, as the
// BookFileCore projection. When memdb is published, uses the memdb book_id
// index — O(sum of files per ID), not O(all 308K book_files) like the
// Pebble full-scan fallback. For a 500-book page query, this drops the call
// from ~15s to <5ms; for a 20-book query, from ~15s to <1ms. The Pebble
// full-scan was the actual killer behind 500-per-page taking 3m51s pre-fix.
//
// Pebble full-scan retained as fallback for cold-start (before memdb
// publishes) and tests with no memdb.
//
// Core-typed (STOREFID): the return type is BookFileCore, not BookFile, so
// the missing heavy fingerprint fields (FingerprintFailureReason/Detail/
// DiagnosticJSON, AcoustIDFingerprint, AcoustIDSeg0..6) are compiler-enforced
// rather than silently nil'd. A caller that needs any of those MUST fetch via
// GetBookFiles(bookID) (full Pebble). See
// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (s *PebbleStore) GetBookFilesForIDsCore(bookIDs []string) (map[string][]BookFileCore, error) {
	if s.UseMemDB && s.mem() != nil {
		return s.mem().GetBookFilesForIDsCore(bookIDs)
	}
	return s.getBookFilesForIDsPebbleScan(bookIDs)
}

func (s *PebbleStore) getBookFilesForIDsPebbleScan(bookIDs []string) (map[string][]BookFileCore, error) {
	result := make(map[string][]BookFileCore)
	if len(bookIDs) == 0 {
		return result, nil
	}
	idSet := make(map[string]bool)
	for _, id := range bookIDs {
		idSet[id] = true
	}
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		var f BookFile
		if err := json.Unmarshal(iter.Value(), &f); err != nil {
			return nil, err
		}
		if idSet[f.BookID] {
			result[f.BookID] = append(result[f.BookID], f.Core())
		}
	}
	return result, nil
}

// GetAllBookFiles returns every BookFile in the database. When memdb is
// published, iterates the in-memory book_files table — a pointer walk over
// ~308K rows — instead of a Pebble prefix scan with per-row JSON unmarshal.
// Mirrors the GetBookFilesForIDsCore fastpath from PR #1153.
//
// Pebble full-scan retained as fallback for cold-start (before memdb
// publishes) and tests with no memdb.
//
// SLIM (memdb projection): returns rows with heavy fields nil'd —
// FingerprintFailureReason/Detail/DiagnosticJSON, AcoustIDFingerprint,
// AcoustIDSeg0..6 (FingerprintFailedAt and AcoustIDFingerprintDurationSec are
// kept). A caller that needs any of those MUST fetch via GetBookFiles(bookID)
// (full Pebble). See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (s *PebbleStore) GetAllBookFiles() ([]BookFile, error) {
	if s.UseMemDB && s.mem() != nil {
		return s.mem().GetAllBookFiles()
	}
	return s.getAllBookFilesPebbleScan()
}

func (s *PebbleStore) getAllBookFilesPebbleScan() ([]BookFile, error) {
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"), // ';' is one past ':' in ASCII
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var files []BookFile
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Skip secondary index entries (book_file_pid:, book_file_path:, book_file_acoustid:).
		if !strings.HasPrefix(key, "book_file:") {
			continue
		}
		// Primary keys look like book_file:<bookID>:<fileID> — must have exactly 2 colons
		// after the prefix, meaning the full key has 3 colon-separated segments.
		parts := strings.SplitN(key, ":", 4)
		if len(parts) != 3 {
			continue
		}
		var f BookFile
		if err := json.Unmarshal(iter.Value(), &f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// getAllBooksPebbleScan returns every non-deleted Book by scanning Pebble directly,
// bypassing memdb. Mirrors the Pebble branch of GetAllBooks (skips ":path:" index
// keys and MarkedForDeletion rows). Used by callers — like GetAcoustIDStats — that
// must not depend on the async memdb warmup having published.
func (s *PebbleStore) getAllBooksPebbleScan() ([]Book, error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var books []Book
	for iter.First(); iter.Valid(); iter.Next() {
		// Skip path index keys (book:path:...).
		if strings.Contains(string(iter.Key()), ":path:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			return nil, err
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		books = append(books, book)
	}
	return books, nil
}

// GetBookFilesNeedingDelugeImport returns book_files that have a non-empty
// deluge_hash but have not yet been imported (imported_from_deluge_at IS NULL).
//
// When memdb is published, walks the sparse deluge_hash index — only rows
// where DelugeHash is non-empty are indexed — and post-filters on the
// ImportedFromDelugeAt nil check. Drops the deluge discovery handler +
// centralization plugin from a 308K full BookFile scan to walking just the
// (much smaller) deluge-touched subset (H2 + H8). Pebble full-scan retained
// as the cold-start fallback.
//
// SLIM (memdb projection): returns rows with heavy fields nil'd —
// FingerprintFailureReason/Detail/DiagnosticJSON, AcoustIDFingerprint,
// AcoustIDSeg0..6 (FingerprintFailedAt and AcoustIDFingerprintDurationSec are
// kept). A caller that needs any of those MUST fetch via GetBookFiles(bookID)
// (full Pebble). See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (s *PebbleStore) GetBookFilesNeedingDelugeImport() ([]BookFile, error) {
	if s.UseMemDB && s.mem() != nil {
		return s.mem().GetBookFilesNeedingDelugeImport()
	}
	all, err := s.GetAllBookFiles()
	if err != nil {
		return nil, err
	}
	var out []BookFile
	for _, f := range all {
		if f.DelugeHash != "" && f.ImportedFromDelugeAt == nil {
			out = append(out, f)
		}
	}
	return out, nil
}

// GetBookFileByPID looks up a BookFile by iTunes persistent ID using the
// book_file_pid:<pid> secondary index.
func (s *PebbleStore) GetBookFileByPID(itunesPID string) (*BookFile, error) {
	if itunesPID == "" {
		return nil, nil
	}
	pidKey := []byte(fmt.Sprintf("book_file_pid:%s", itunesPID))
	value, closer, err := s.db.Get(pidKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref := string(value)
	closer.Close()

	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("corrupt book_file_pid index value: %q", ref)
	}
	return s.getBookFileByID(parts[0], parts[1])
}

// ClearITunesPID clears itunes_persistent_id and itunes_path on the
// book_file with the given PID. Returns (true, nil) if a row was
// updated, (false, nil) if no row with that PID exists.
func (s *PebbleStore) ClearITunesPID(itunesPID string) (bool, error) {
	if itunesPID == "" {
		return false, nil
	}
	f, err := s.GetBookFileByPID(itunesPID)
	if err != nil {
		return false, err
	}
	if f == nil {
		return false, nil
	}
	f.ITunesPersistentID = ""
	f.ITunesPath = ""
	if err := s.UpdateBookFile(f.ID, f); err != nil {
		return false, fmt.Errorf("ClearITunesPID: %w", err)
	}
	return true, nil
}

// GetBookFileByPath looks up a BookFile by file path using the
// book_file_path:<crc32hex> secondary index.
func (s *PebbleStore) GetBookFileByPath(filePath string) (*BookFile, error) {
	if filePath == "" {
		return nil, nil
	}
	pathKey := []byte(fmt.Sprintf("book_file_path:%s", bookFilePathCRC(filePath)))
	value, closer, err := s.db.Get(pathKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref := string(value)
	closer.Close()

	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("corrupt book_file_path index value: %q", ref)
	}
	f, err := s.getBookFileByID(parts[0], parts[1])
	if err != nil {
		return nil, err
	}
	// Verify the stored path matches (CRC collision guard).
	if f != nil && f.FilePath != filePath {
		return nil, nil
	}
	return f, nil
}

// GetBookFileByAcoustID looks up a BookFile by exact AcoustID fingerprint
// using the book_file_acoustid: secondary index.
func (s *PebbleStore) GetBookFileByAcoustID(fp string) (*BookFile, error) {
	if fp == "" {
		return nil, nil
	}
	key := []byte(fmt.Sprintf("book_file_acoustid:%s", fp))
	value, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref := string(value)
	closer.Close()

	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("corrupt book_file_acoustid index: %q", ref)
	}
	return s.getBookFileByID(parts[0], parts[1])
}

// GetBookFileByAcoustIDFuzzy scans all book_file records and returns the first
// whose AcoustID fingerprint similarity to fp is >= minSimilarity.
// O(n) over fingerprinted files — only called when exact match misses.
//
// Memdb fastpath: when memdb is warm, walk in-RAM book_files (seg0..6 are
// preserved post-MAYDEPLOY-J). Falls back to Pebble prefix scan below.
func (s *PebbleStore) GetBookFileByAcoustIDFuzzy(fp string, minSimilarity float64) (*BookFile, error) {
	if s.UseMemDB {
		if m := s.mem(); m != nil {
			return m.GetBookFileByAcoustIDFuzzy(fp, minSimilarity)
		}
	}

	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var f BookFile
		if err := json.Unmarshal(iter.Value(), &f); err != nil {
			continue
		}
		// Check all 7 fingerprint segments for a fuzzy match.
		segs := [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
			f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6}
		matched := false
		for _, seg := range segs {
			if seg == "" {
				continue
			}
			sim, err := fingerprint.HammingSimilarity(fp, seg)
			if err != nil {
				continue
			}
			if sim >= minSimilarity {
				matched = true
				break
			}
		}
		if matched {
			return &f, nil
		}
	}
	return nil, iter.Error()
}

// DeleteBookFile removes the BookFile with the given ID (and its secondary
// indexes) from the store. It requires the bookID to be available on the
// struct; the caller must have obtained the record first, so we scan the
// secondary path index or retrieve by ID. Since we only have fileID here we
// perform a prefix scan to locate the record.
func (s *PebbleStore) DeleteBookFile(id string) error {
	// Scan all book_file: keys to find the one with this file ID.
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return err
	}

	var found *BookFile
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: book_file:<bookID>:<fileID>
		key := string(iter.Key())
		parts := strings.SplitN(key, ":", 3)
		if len(parts) == 3 && parts[2] == id {
			var f BookFile
			if jsonErr := json.Unmarshal(iter.Value(), &f); jsonErr == nil {
				found = &f
			}
			break
		}
	}
	iter.Close()

	if found == nil {
		return nil // already gone
	}

	batch := s.db.NewBatch()

	// Delete primary key.
	primaryKey := []byte(fmt.Sprintf("book_file:%s:%s", found.BookID, found.ID))
	if err := batch.Delete(primaryKey, nil); err != nil {
		batch.Close()
		return err
	}

	// Delete secondary indexes.
	if err := s.deleteBookFileSecondaryIndexes(batch, found); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "delete_book_file")
	s.DeleteBookFileFromMemDB(id)
	// Recompute book-level Duration/FileSize aggregates now that a file was removed.
	// Best-effort: the file delete already committed; don't fail on aggregate errors.
	s.notifyBookFileChange(found.BookID)
	return nil
}

// DeleteBookFilesForBook removes all BookFile records for a given bookID,
// including their secondary indexes.
func (s *PebbleStore) DeleteBookFilesForBook(bookID string) error {
	files, err := s.GetBookFiles(bookID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	batch := s.db.NewBatch()

	for i := range files {
		f := &files[i]
		primaryKey := []byte(fmt.Sprintf("book_file:%s:%s", f.BookID, f.ID))
		if err := batch.Delete(primaryKey, nil); err != nil {
			batch.Close()
			return err
		}
		if err := s.deleteBookFileSecondaryIndexes(batch, f); err != nil {
			batch.Close()
			return err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	// Recompute book-level aggregates after bulk-deleting all files for this book.
	// The book likely has Duration=0 after deletion, which is correct — nothing to sum.
	s.notifyBookFileChange(bookID)
	return nil
}

// UpsertBookFile creates or updates a BookFile. Lookup order:
//  1. If ITunesPersistentID is set, look up by PID.
//  2. Otherwise look up by FilePath.
//  3. If still not found, create a new record.
func (s *PebbleStore) UpsertBookFile(file *BookFile) error {
	normalizeBookFileDuration(file) // CONS-18: repair ms durations at the chokepoint

	var existing *BookFile
	var err error

	if file.ITunesPersistentID != "" {
		existing, err = s.GetBookFileByPID(file.ITunesPersistentID)
		if err != nil {
			return err
		}
	}

	if existing == nil && file.FilePath != "" {
		existing, err = s.GetBookFileByPath(file.FilePath)
		if err != nil {
			return err
		}
	}

	if existing == nil {
		return s.CreateBookFile(file)
	}

	// Preserve the existing ID and bookID; update in place.
	file.ID = existing.ID
	file.BookID = existing.BookID

	// Preserve heavy fingerprint fields stripped by stripBookFileForMemdb (PERF-7).
	// Callers that read from the memdb projection (GetAllBookFiles etc.) get nil
	// AcoustIDFingerprint and nil diagnostic strings. Without this guard, a memdb
	// round-trip via UpsertBookFile would silently erase the stored fingerprint.
	// The same guard exists in BatchUpsertBookFiles — keep both in sync.
	if len(file.AcoustIDFingerprint) == 0 {
		file.AcoustIDFingerprint = existing.AcoustIDFingerprint
	}
	if file.FingerprintFailureReason == nil {
		file.FingerprintFailureReason = existing.FingerprintFailureReason
	}
	if file.FingerprintFailureDetail == nil {
		file.FingerprintFailureDetail = existing.FingerprintFailureDetail
	}
	if file.FingerprintDiagnosticJSON == nil {
		file.FingerprintDiagnosticJSON = existing.FingerprintDiagnosticJSON
	}

	return s.UpdateBookFile(existing.ID, file)
}

// BatchUpsertBookFiles upserts a slice of BookFile records using a single
// PebbleDB batch for all writes. Each file is matched by iTunes persistent ID
// (if set) or by file path. This amortises the per-Commit overhead across
// all records in the slice.
func (s *PebbleStore) BatchUpsertBookFiles(files []*BookFile) error {
	if len(files) == 0 {
		return nil
	}

	batch := s.db.NewBatch()

	now := time.Now()
	for _, file := range files {
		if file == nil {
			continue
		}
		normalizeBookFileDuration(file) // CONS-18: repair ms durations at the chokepoint

		var existing *BookFile
		var lookupErr error

		if file.ITunesPersistentID != "" {
			existing, lookupErr = s.GetBookFileByPID(file.ITunesPersistentID)
		}
		if lookupErr != nil {
			batch.Close()
			return lookupErr
		}
		if existing == nil && file.FilePath != "" {
			existing, lookupErr = s.GetBookFileByPath(file.FilePath)
			if lookupErr != nil {
				batch.Close()
				return lookupErr
			}
		}

		if existing != nil {
			// Preserve identity fields; remove stale secondary indexes.
			file.ID = existing.ID
			file.BookID = existing.BookID
			file.CreatedAt = existing.CreatedAt
			file.UpdatedAt = now

			// Preserve memdb-stripped heavy fields. A caller that sourced `file`
			// from the memdb view (GetAllBookFiles → stripBookFileForMemdb) carries
			// a nil AcoustIDFingerprint + nil fingerprint diagnostics. Writing that
			// back verbatim would WIPE the raw chromaprint (~230 KB/file, expensive
			// to recompute) on every row — a mass data-loss on any whole-library
			// round-trip (e.g. maintenance.tag-backfill). Restore from the stored
			// row whenever the incoming value is empty. The fingerprint WRITE path
			// (internal/plugins/acoustid/backfill.go) always supplies a fresh
			// non-empty value via UpdateBookFile, so this never blocks a real update.
			if len(file.AcoustIDFingerprint) == 0 {
				file.AcoustIDFingerprint = existing.AcoustIDFingerprint
			}
			if file.FingerprintFailureReason == nil {
				file.FingerprintFailureReason = existing.FingerprintFailureReason
			}
			if file.FingerprintFailureDetail == nil {
				file.FingerprintFailureDetail = existing.FingerprintFailureDetail
			}
			if file.FingerprintDiagnosticJSON == nil {
				file.FingerprintDiagnosticJSON = existing.FingerprintDiagnosticJSON
			}

			if err := s.deleteBookFileSecondaryIndexes(batch, existing); err != nil {
				batch.Close()
				return err
			}
		} else {
			if file.ID == "" {
				id, err := newULID()
				if err != nil {
					batch.Close()
					return err
				}
				file.ID = id
			}
			if file.CreatedAt.IsZero() {
				file.CreatedAt = now
			}
			file.UpdatedAt = now
		}

		// T020: drop AcoustIDSeg0..6 from the stored value via a copy.
		data, err := marshalBookFileDropSegs(file)
		if err != nil {
			batch.Close()
			return err
		}

		key := []byte(fmt.Sprintf("book_file:%s:%s", file.BookID, file.ID))
		if err := batch.Set(key, data, nil); err != nil {
			batch.Close()
			return err
		}

		if err := writeBookFileSecondaryIndexes(batch, file); err != nil {
			batch.Close()
			return err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	// Refresh memdb for every upserted file. UpdateBookFile does this per row;
	// BatchUpsertBookFiles historically did not, so after a batch write the memdb
	// view (what GetAllBookFiles / the UI read) stayed stale until the next warmup.
	// That made whole-library batch backfills (e.g. maintenance.tag-backfill) look
	// like they did nothing on re-read and forced redundant re-runs. Keep memdb in
	// sync here so batch writes are immediately visible.
	for _, file := range files {
		if file != nil {
			s.UpsertBookFileToMemDB(file)
		}
	}
	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "batch_upsert_book_files")
	return nil
}

// GetBookFileByID returns a single BookFile by bookID and fileID.
func (s *PebbleStore) GetBookFileByID(bookID, fileID string) (*BookFile, error) {
	return s.getBookFileByID(bookID, fileID)
}

// MoveBookFilesToBook reassigns BookFile records from sourceBookID to targetBookID.
func (s *PebbleStore) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	batch := s.db.NewBatch()

	for _, fid := range fileIDs {
		f, err := s.getBookFileByID(sourceBookID, fid)
		if err != nil {
			batch.Close()
			return fmt.Errorf("file not found: %s: %w", fid, err)
		}
		if f == nil {
			batch.Close()
			return fmt.Errorf("file not found: %s", fid)
		}

		// Delete old primary key
		oldKey := []byte(fmt.Sprintf("book_file:%s:%s", sourceBookID, fid))
		if err := batch.Delete(oldKey, nil); err != nil {
			batch.Close()
			return err
		}

		// Delete old secondary indexes
		if err := s.deleteBookFileSecondaryIndexes(batch, f); err != nil {
			batch.Close()
			return err
		}

		// Update book ID and write under new primary key
		f.BookID = targetBookID
		f.UpdatedAt = time.Now()

		data, err := json.Marshal(f)
		if err != nil {
			batch.Close()
			return err
		}
		newKey := []byte(fmt.Sprintf("book_file:%s:%s", targetBookID, fid))
		if err := batch.Set(newKey, data, nil); err != nil {
			batch.Close()
			return err
		}

		// Re-create secondary indexes with updated bookID
		if err := writeBookFileSecondaryIndexes(batch, f); err != nil {
			batch.Close()
			return err
		}
	}

	return batch.Commit(pebble.Sync)
}

// UpdateBookFileHashes updates the original_file_hash and post_metadata_hash
// fields on a BookFile record stored in PebbleDB.
func (s *PebbleStore) UpdateBookFileHashes(id, originalHash, postMetadataHash string) error {
	// Find the file across all books via the secondary id index.
	val, closer, err := s.db.Get([]byte("book_file_id:" + id))
	if err != nil {
		return fmt.Errorf("UpdateBookFileHashes: lookup id index: %w", err)
	}
	bookFileKey := string(val)
	closer.Close()

	val2, closer2, err := s.db.Get([]byte(bookFileKey))
	if err != nil {
		return fmt.Errorf("UpdateBookFileHashes: get file: %w", err)
	}
	var bf BookFile
	if err := json.Unmarshal(val2, &bf); err != nil {
		closer2.Close()
		return fmt.Errorf("UpdateBookFileHashes: unmarshal: %w", err)
	}
	closer2.Close()

	if bf.OriginalFileHash == "" && originalHash != "" {
		bf.OriginalFileHash = originalHash
	}
	if postMetadataHash != "" {
		bf.PostMetadataHash = postMetadataHash
	}
	bf.UpdatedAt = time.Now()

	data, err := json.Marshal(&bf)
	if err != nil {
		return fmt.Errorf("UpdateBookFileHashes: marshal: %w", err)
	}
	return s.db.Set([]byte(bookFileKey), data, pebble.Sync)
}

// SetBookFileHash sets file_hash on a BookFile record in PebbleDB, and also
// sets original_file_hash if it is currently empty, matching scanner behaviour.
func (s *PebbleStore) SetBookFileHash(id, hash string) error {
	val, closer, err := s.db.Get([]byte("book_file_id:" + id))
	if err != nil {
		return fmt.Errorf("SetBookFileHash: lookup id index: %w", err)
	}
	bookFileKey := string(val)
	closer.Close()

	val2, closer2, err := s.db.Get([]byte(bookFileKey))
	if err != nil {
		return fmt.Errorf("SetBookFileHash: get file: %w", err)
	}
	var bf BookFile
	if err := json.Unmarshal(val2, &bf); err != nil {
		closer2.Close()
		return fmt.Errorf("SetBookFileHash: unmarshal: %w", err)
	}
	closer2.Close()

	bf.FileHash = hash
	if bf.OriginalFileHash == "" {
		bf.OriginalFileHash = hash
	}
	bf.UpdatedAt = time.Now()

	data, err := json.Marshal(&bf)
	if err != nil {
		return fmt.Errorf("SetBookFileHash: marshal: %w", err)
	}
	return s.db.Set([]byte(bookFileKey), data, pebble.Sync)
}

// ClearAllAcoustIDFingerprints wipes AcoustIDSeg0..6 on every BookFile and
// drops the matching book_file_acoustid:<seg> secondary index entries in
// batched Pebble commits — one fsync per batchSize records instead of one
// per UpdateBookFile call. The progress callback is invoked roughly every
// 5000 records with (processed, cleared, total).
//
// Returns (cleared, total, err). cleared counts only files that actually had
// at least one non-empty segment.
func (s *PebbleStore) ClearAllAcoustIDFingerprints(ctx context.Context, batchSize int, progress func(processed, cleared, total int)) (int, int, error) {
	if batchSize <= 0 {
		batchSize = 2000
	}

	// First pass: collect primary keys so we don't hold an iterator open while
	// we mutate Pebble underneath it. 308K keys at ~50 bytes each ≈ 15MB — fine.
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return 0, 0, err
	}
	type ref struct {
		key  []byte
		data []byte
	}
	var refs []ref
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		if !strings.HasPrefix(string(key), "book_file:") {
			continue
		}
		// Skip secondary indexes — only primary book_file:<bookID>:<fileID>.
		if c := strings.Count(string(key), ":"); c != 2 {
			continue
		}
		kc := make([]byte, len(key))
		copy(kc, key)
		vc := make([]byte, len(iter.Value()))
		copy(vc, iter.Value())
		refs = append(refs, ref{key: kc, data: vc})
	}
	if err := iter.Close(); err != nil {
		return 0, 0, err
	}

	total := len(refs)
	cleared := 0
	processed := 0
	batch := s.db.NewBatch()
	flush := func() error {
		if err := batch.Commit(pebble.Sync); err != nil {
			return err
		}
		batch = s.db.NewBatch()
		return nil
	}

	// Pre-check needles: a row whose JSON contains none of these is
	// guaranteed to have no fingerprint data and can be skipped without
	// the (now-expensive) json.Unmarshal/Marshal round-trip. The
	// AcoustIDFingerprint []byte field can be 230 KB–1 MB of base64 per
	// row, so unmarshaling all 308 K rows just to check emptiness costs
	// ~10 minutes at ~33 rows/sec. With these needles the skip path is
	// ~5000 rows/sec and the slow path runs only for rows that actually
	// need clearing.
	//
	// Why two needles per seg: omitempty-tagged fields can either be
	// absent (no key) or present-but-empty (`"":""`). Either way the
	// fingerprint is empty. Same logic for the raw fp.
	var (
		fpNeedle   = []byte(`"acoustid_fingerprint":"`)
		seg0Needle = []byte(`"acoustid_seg0":"`)
		seg1Needle = []byte(`"acoustid_seg1":"`)
		seg2Needle = []byte(`"acoustid_seg2":"`)
		seg3Needle = []byte(`"acoustid_seg3":"`)
		seg4Needle = []byte(`"acoustid_seg4":"`)
		seg5Needle = []byte(`"acoustid_seg5":"`)
		seg6Needle = []byte(`"acoustid_seg6":"`)
		emptyStr   = []byte(`""`)
	)
	// hasNonEmpty returns true if the needle appears and is not
	// immediately followed by `""` (i.e. the field exists with a value).
	hasNonEmpty := func(data, needle []byte) bool {
		idx := bytes.Index(data, needle)
		if idx < 0 {
			return false
		}
		after := idx + len(needle)
		// needle ends with `:"` — the byte after that is the first char
		// of the value. If it's a closing quote, the value is empty.
		if after-1 >= 0 && after-1 < len(data) && data[after-1] == '"' {
			// Check immediately past the needle: if it's the closing
			// quote of an empty string we have `"":""` — empty value.
			if after < len(data) && data[after] == '"' {
				return false
			}
		}
		_ = emptyStr // referenced for documentation; bytes.Equal not needed
		return true
	}

	for _, r := range refs {
		select {
		case <-ctx.Done():
			_ = batch.Close()
			return cleared, total, ctx.Err()
		default:
		}

		processed++

		// Fast skip: raw bytes don't carry any of the fp/seg fields with
		// a non-empty value. Saves the json.Unmarshal cost — which has
		// to decode AcoustIDFingerprint's multi-KB base64 even when we
		// only care about it being empty.
		if !hasNonEmpty(r.data, fpNeedle) &&
			!hasNonEmpty(r.data, seg0Needle) &&
			!hasNonEmpty(r.data, seg1Needle) &&
			!hasNonEmpty(r.data, seg2Needle) &&
			!hasNonEmpty(r.data, seg3Needle) &&
			!hasNonEmpty(r.data, seg4Needle) &&
			!hasNonEmpty(r.data, seg5Needle) &&
			!hasNonEmpty(r.data, seg6Needle) {
			if progress != nil && processed%5000 == 0 {
				progress(processed, cleared, total)
			}
			continue
		}

		var f BookFile
		if err := json.Unmarshal(r.data, &f); err != nil {
			continue
		}

		if f.AcoustIDSeg0 == "" && f.AcoustIDSeg1 == "" && f.AcoustIDSeg2 == "" &&
			f.AcoustIDSeg3 == "" && f.AcoustIDSeg4 == "" && f.AcoustIDSeg5 == "" &&
			f.AcoustIDSeg6 == "" && len(f.AcoustIDFingerprint) == 0 {
			if progress != nil && processed%5000 == 0 {
				progress(processed, cleared, total)
			}
			continue
		}

		// Delete each acoustid secondary index entry.
		for _, seg := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2,
			f.AcoustIDSeg3, f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
			if seg == "" {
				continue
			}
			if err := batch.Delete([]byte("book_file_acoustid:"+seg), nil); err != nil {
				_ = batch.Close()
				return cleared, total, err
			}
		}

		f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2 = "", "", ""
		f.AcoustIDSeg3, f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6 = "", "", "", ""
		// Whole-file fp + duration are part of "fingerprint state" — clearing
		// segs without clearing these would leave reset-all in a broken
		// half-cleared state and the LSH index orphaned.
		f.AcoustIDFingerprint = nil
		f.AcoustIDFingerprintDurationSec = 0
		f.UpdatedAt = time.Now()

		data, err := json.Marshal(&f)
		if err != nil {
			continue
		}
		if err := batch.Set(r.key, data, nil); err != nil {
			_ = batch.Close()
			return cleared, total, err
		}
		cleared++

		// Keep memdb in lock-step with Pebble so subsequent in-RAM lookups
		// don't return stale seg values.
		if s.UseMemDB {
			s.UpsertBookFileToMemDB(&f)
		}

		if batch.Len() >= batchSize {
			if err := flush(); err != nil {
				return cleared, total, err
			}
			if progress != nil {
				progress(processed, cleared, total)
			}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return cleared, total, err
	}

	// Wipe the LSH index in one prefix-delete pass — every per-row meta
	// entry is gone anyway since we just cleared every fingerprint, so we
	// don't need to look up individual subprints. RangeDelete is much
	// cheaper than 308K × 64 individual Deletes.
	wipeBatch := s.db.NewBatch()
	if err := wipeBatch.DeleteRange([]byte(lshKeyPrefix), []byte("fpidx;"), nil); err != nil {
		_ = wipeBatch.Close()
		return cleared, total, err
	}
	if err := wipeBatch.DeleteRange([]byte(lshMetaKeyPrefix), []byte("fpidx_meta;"), nil); err != nil {
		_ = wipeBatch.Close()
		return cleared, total, err
	}
	if err := wipeBatch.Commit(pebble.Sync); err != nil {
		return cleared, total, err
	}

	if progress != nil {
		progress(processed, cleared, total)
	}

	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "clear_all_acoustid_fingerprints")
	return cleared, total, nil
}

// SweepBookFileSegDrop iterates all primary book_file: rows in Pebble and
// rewrites any row that still carries AcoustIDSeg0..6 values, removing those
// fields from the stored JSON and deleting the corresponding
// book_file_acoustid: secondary index entries.
//
// T020 background sweep. Design notes:
//   - Reads raw Pebble bytes to avoid the memdb route (which has already
//     stripped segs in memory, T019).  This is the same pattern used by
//     ClearAllAcoustIDFingerprints (line ~10616 in this file).
//   - Uses byte-needle fast-skip to avoid json.Unmarshal on already-clean rows.
//   - If dryRun is true the method scans and counts but writes nothing.
//   - Resumable: re-running after a partial apply produces the same result —
//     rows already rewritten have no seg needles and are fast-skipped.
//   - batchSize controls how many rewrites are committed per Pebble sync
//     (0 → default 1000).
//   - progress is called roughly every batchSize rewrites with (rewrite, total).
func (s *PebbleStore) SweepBookFileSegDrop(
	ctx context.Context,
	dryRun bool,
	batchSize int,
	progress func(rewrite, total int),
) (SweepBookFileSegDropResult, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	var result SweepBookFileSegDropResult

	// Pass 1: collect primary keys + raw values so we don't hold an iterator
	// open while mutating Pebble.
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return result, err
	}
	type ref struct {
		key  []byte
		data []byte
	}
	var refs []ref
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		if !strings.HasPrefix(string(key), "book_file:") {
			continue
		}
		// Primary keys are book_file:<bookID>:<fileID> — exactly 2 colons.
		if strings.Count(string(key), ":") != 2 {
			continue
		}
		kc := make([]byte, len(key))
		copy(kc, key)
		vc := make([]byte, len(iter.Value()))
		copy(vc, iter.Value())
		refs = append(refs, ref{key: kc, data: vc})
	}
	if err := iter.Close(); err != nil {
		return result, err
	}
	result.Total = len(refs)

	// seg needles (omitempty — field is absent when empty, so any occurrence
	// means the value is non-empty).
	seg0N := []byte(`"acoustid_seg0":"`)
	seg1N := []byte(`"acoustid_seg1":"`)
	seg2N := []byte(`"acoustid_seg2":"`)
	seg3N := []byte(`"acoustid_seg3":"`)
	seg4N := []byte(`"acoustid_seg4":"`)
	seg5N := []byte(`"acoustid_seg5":"`)
	seg6N := []byte(`"acoustid_seg6":"`)

	hasSegNeedle := func(data []byte) bool {
		return bytes.Contains(data, seg0N) ||
			bytes.Contains(data, seg1N) ||
			bytes.Contains(data, seg2N) ||
			bytes.Contains(data, seg3N) ||
			bytes.Contains(data, seg4N) ||
			bytes.Contains(data, seg5N) ||
			bytes.Contains(data, seg6N)
	}

	if dryRun {
		for _, r := range refs {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			default:
			}
			if !hasSegNeedle(r.data) {
				result.Skipped++
				continue
			}
			result.Rewrite++
		}
		return result, nil
	}

	// Pass 2: rewrite rows that carry seg needles.
	batch := s.db.NewBatch()
	rewrittenSinceFlush := 0
	flush := func() error {
		if err := batch.Commit(pebble.Sync); err != nil {
			return err
		}
		batch = s.db.NewBatch()
		rewrittenSinceFlush = 0
		return nil
	}

	for _, r := range refs {
		select {
		case <-ctx.Done():
			_ = batch.Close()
			return result, ctx.Err()
		default:
		}

		if !hasSegNeedle(r.data) {
			result.Skipped++
			continue
		}

		var f BookFile
		if err := json.Unmarshal(r.data, &f); err != nil {
			slog.Warn("SweepBookFileSegDrop: unmarshal error; skipping row",
				"key", string(r.key), "error", err)
			result.Errors++
			continue
		}

		// Delete book_file_acoustid: secondary index entries for the segs
		// that are present on this row (they'll be absent after the rewrite).
		for _, seg := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2,
			f.AcoustIDSeg3, f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
			if seg == "" {
				continue
			}
			if delErr := batch.Delete([]byte("book_file_acoustid:"+seg), nil); delErr != nil {
				_ = batch.Close()
				return result, delErr
			}
		}

		// Rewrite the row without segs.
		newData, marshalErr := marshalBookFileDropSegs(&f)
		if marshalErr != nil {
			slog.Warn("SweepBookFileSegDrop: marshal error; skipping row",
				"key", string(r.key), "error", marshalErr)
			result.Errors++
			continue
		}
		if setErr := batch.Set(r.key, newData, nil); setErr != nil {
			_ = batch.Close()
			return result, setErr
		}
		result.Rewrite++
		rewrittenSinceFlush++

		if rewrittenSinceFlush >= batchSize {
			if err := flush(); err != nil {
				return result, err
			}
			if progress != nil {
				progress(result.Rewrite, result.Total)
			}
		}
	}

	// Commit any remaining work.
	if err := batch.Commit(pebble.Sync); err != nil {
		return result, err
	}

	if progress != nil {
		progress(result.Rewrite, result.Total)
	}
	return result, nil
}
