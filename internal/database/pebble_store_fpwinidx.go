// file: internal/database/pebble_store_fpwinidx.go
// version: 1.0.0
// guid: 6f0d2a8e-41c7-4b95-8e3d-9a7c1b5e2f60
// last-edited: 2026-10-11

package database

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// Window-print inverted index (02-PR8). Key scheme and codec:
// fingerprint_window.go. Band scheme: internal/fingerprint/window_lsh.go.
//
//	fpwinidx:<ver><band><hash>:<file_id>   -> empty value
//	fpwinidx_meta:<file_id>                -> 1B ver + N x (1B band + 8B hash)
//
// The value is empty, not the book ID the head-print fpidx: rows carry: window
// rows are keyed by file ID and a move between books keeps them, so a stored
// book ID would go stale on every move. The collector resolves the book of a
// candidate file when it needs it.
//
// Write-through. Every path that changes a tracked file's stored windows
// rebuilds that file's rows in the SAME batch, under the same window stripe:
//
//   - every delete of a ref's windows goes through stageFingerprintWindowDeletes,
//     which now also stages the delete of the file's index rows via its member
//     list (DeleteFingerprintWindows, the three book_file delete cascades,
//     RecordFingerprintWindowFailure, a CarryOver donor, and the first half of
//     ReplaceFingerprintWindows);
//   - PutFingerprintWindow, ReplaceFingerprintWindows and the CarryOver keeper
//     then stage the rows for the file's FINAL window set (stageWindowIndexWrite).
//
// Deletes are staged before sets, so a key in both the old and the new member
// list survives (a later Set in one batch wins over an earlier Delete).
//
// What is banded: kind "window" rows of f: refs only. A "whole" print is the
// full-file path, not a window; a stored "head" (none are written today) is
// usually the shared publisher intro and would turn its bands into hot
// buckets, the same reason WindowSetSimilarity keeps the head out of its
// score. p: refs (untracked files) are not indexed; a CarryOver onto an f: ref
// indexes them then. A file whose windows give no bands (too short or too
// uniform, see fingerprint.WindowMinDistinctShingles) still gets a member list
// with zero entries, so the build op can tell "indexed, nothing to band" from
// "never indexed".

// windowIndexBuiltFlagKey is set by dedup.window-index-build after a complete
// pass. The v1 suffix tracks fingerprint.WindowBandVersion.
const windowIndexBuiltFlagKey = "system:flag:fpwinidx_v1_done"

// windowIndexMaxPostings bounds one probe key: a (band, hash) shared by more
// files than this is a stop word (near-silence, a test tone that slipped past
// the distinct-shingle floor), not evidence, and is skipped rather than
// walked. It keeps one probe from becoming O(library).
const windowIndexMaxPostings = 512

// IsWindowIndexBuilt reports whether the window-index build op has completed a
// full pass on this database. The window collector gates on it: until it is
// set, a probe would see only the files written since this code shipped.
func (s *PebbleStore) IsWindowIndexBuilt() bool {
	setting, err := s.GetSetting(windowIndexBuiltFlagKey)
	return err == nil && setting != nil && setting.Value == "true"
}

// SetWindowIndexBuilt marks the window index complete. Called by the build op
// only after a pass that indexed every file with windows.
func (s *PebbleStore) SetWindowIndexBuilt() error {
	return s.SetSetting(windowIndexBuiltFlagKey, "true", "bool", false)
}

// WindowIndexMembers returns fileID's indexed bands from its member list.
// indexed is false when the file has no member list under the current band
// version (never indexed, or indexed by another version). A file indexed with
// nothing to band returns (nil, true, nil).
func (s *PebbleStore) WindowIndexMembers(fileID string) (bands []fingerprint.WindowBand, indexed bool, err error) {
	val, closer, err := s.db.Get(fpwinIdxMetaKey(fileID))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("WindowIndexMembers %s: %w", fileID, err)
	}
	defer closer.Close()
	bands, ok := decodeWindowIndexMeta(val)
	if !ok {
		return nil, false, nil
	}
	return bands, true, nil
}

// PutWindowIndex (re)builds fileID's index rows from its stored windows, in
// one batch under the file's window stripe, and returns how many band keys it
// wrote. The windows are re-read under the stripe, so a window write or delete
// that lands between a caller's check and this call cannot leave rows that
// describe windows that are gone. A file with no stored windows has its rows
// (if any) removed and nothing written. Idempotent. Never reads audio.
func (s *PebbleStore) PutWindowIndex(fileID string) (int, error) {
	ref := FileWindowRef(fileID)
	if err := ref.validate(); err != nil {
		return 0, err
	}
	unlock := s.lockWindowRefs(ref)
	defer unlock()
	windows, err := s.readFingerprintWindows(ref)
	if err != nil {
		return 0, fmt.Errorf("PutWindowIndex %s: %w", fileID, err)
	}
	batch := s.db.NewBatch()
	if err := s.stageWindowIndexDelete(batch, fileID); err != nil {
		batch.Close()
		return 0, fmt.Errorf("PutWindowIndex %s: %w", fileID, err)
	}
	n, err := stageWindowIndexWrite(batch, fileID, windows)
	if err != nil {
		batch.Close()
		return 0, fmt.Errorf("PutWindowIndex %s: %w", fileID, err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("PutWindowIndex %s: commit: %w", fileID, err)
	}
	return n, nil
}

// WindowIndexProbe looks up every supplied band and returns the files that
// share at least fingerprint.WindowMinBandHits of them, with their hit counts.
// Duplicate bands in the input count once. A band key with more than
// windowIndexMaxPostings files is skipped as a stop word. max caps the result
// at that many files (highest hit count first, then file ID); max <= 0 means
// no cap. The caller drops its own file.
func (s *PebbleStore) WindowIndexProbe(bands []fingerprint.WindowBand, max int) (map[string]int, error) {
	if len(bands) == 0 {
		return nil, nil
	}
	seen := make(map[fingerprint.WindowBand]struct{}, len(bands))
	hits := make(map[string]int, 64)
	for _, b := range bands {
		if _, dup := seen[b]; dup {
			continue
		}
		seen[b] = struct{}{}
		lower := fpwinIdxBandPrefix(b)
		postings := make([]string, 0, 8)
		stop := false
		err := forEachKeyInRange(s.db, lower, prefixEnd(lower), func(key, _ []byte) error {
			if len(key) <= fpwinIdxFileIDOffset {
				return nil
			}
			if len(postings) == windowIndexMaxPostings {
				stop = true
				return errStopScan
			}
			postings = append(postings, string(key[fpwinIdxFileIDOffset:]))
			return nil
		})
		if err != nil && !errors.Is(err, errStopScan) {
			return nil, fmt.Errorf("WindowIndexProbe: %w", err)
		}
		if stop {
			continue
		}
		for _, id := range postings {
			hits[id]++
		}
	}
	out := make(map[string]int, len(hits))
	for id, n := range hits {
		if n >= fingerprint.WindowMinBandHits {
			out[id] = n
		}
	}
	if max > 0 && len(out) > max {
		ids := make([]string, 0, len(out))
		for id := range out {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if out[ids[i]] != out[ids[j]] {
				return out[ids[i]] > out[ids[j]]
			}
			return ids[i] < ids[j]
		})
		capped := make(map[string]int, max)
		for _, id := range ids[:max] {
			capped[id] = out[id]
		}
		out = capped
	}
	return out, nil
}

// ListWindowIndexFiles returns the IDs of every tracked file that has at least
// one stored window, sorted: the build op's work list. It reads KEYS only
// (fpwin:f:<file_id>:...) and seeks past each file's remaining windows, so no
// window value is loaded; a library's worth of prints is gigabytes.
func (s *PebbleStore) ListWindowIndexFiles() ([]string, error) {
	prefix := []byte(fpwinKeyPrefix + "f:")
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, fmt.Errorf("ListWindowIndexFiles: %w", err)
	}
	var ids []string
	for valid := iter.First(); valid; {
		rest := iter.Key()[len(prefix):]
		end := bytes.IndexByte(rest, ':')
		if end <= 0 {
			valid = iter.Next()
			continue
		}
		id := string(rest[:end])
		ids = append(ids, id)
		// Every key of this file starts "fpwin:f:<id>:"; ';' sorts right
		// after ':', so this seeks to the next file.
		valid = iter.SeekGE([]byte(fpwinKeyPrefix + "f:" + id + ";"))
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, fmt.Errorf("ListWindowIndexFiles: %w", err)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("ListWindowIndexFiles: close: %w", err)
	}
	return ids, nil
}

// fileWindowBands returns the distinct bands of a file's indexable windows:
// kind "window" only (see the file comment for why not head or whole). A
// window too short or uniform to band contributes nothing; an unaligned raw
// print is an error (validate refuses one on write, so it means a corrupt row).
func fileWindowBands(windows []FingerprintWindow) ([]fingerprint.WindowBand, error) {
	var out []fingerprint.WindowBand
	seen := make(map[fingerprint.WindowBand]struct{})
	for i := range windows {
		w := &windows[i]
		if w.Kind != WindowKindWindow {
			continue
		}
		bands, err := fingerprint.WindowBands(w.Raw)
		if err != nil {
			return nil, fmt.Errorf("window %s %s:%d: %w", w.Ref, w.Kind, w.SlotBP, err)
		}
		for _, b := range bands {
			if _, dup := seen[b]; dup {
				continue
			}
			seen[b] = struct{}{}
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Band != out[j].Band {
			return out[i].Band < out[j].Band
		}
		return out[i].Hash < out[j].Hash
	})
	return out, nil
}

// stageWindowIndexDelete stages the delete of fileID's index rows, found
// through its committed member list, and of the member list itself. No member
// list means nothing to delete. A member list written by another band version
// cannot be decoded, so only the list goes; that version's rows sit in their
// own key range (the version byte follows the prefix) and a re-tune removes
// them with one range delete. The caller holds the file's window stripe.
func (s *PebbleStore) stageWindowIndexDelete(batch *GatedBatch, fileID string) error {
	metaKey := fpwinIdxMetaKey(fileID)
	val, closer, err := s.db.Get(metaKey)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read window index member list of %s: %w", fileID, err)
	}
	raw := append([]byte(nil), val...)
	_ = closer.Close()
	bands, _ := decodeWindowIndexMeta(raw)
	for _, b := range bands {
		if err := batch.Delete(fpwinIdxKey(b, fileID), nil); err != nil {
			return err
		}
	}
	return batch.Delete(metaKey, nil)
}

// stageWindowIndexWrite stages fileID's index rows and member list for its
// final window set, and returns the number of band keys. No windows at all
// stages nothing (the file is not indexed); windows with no bands stage an
// empty member list. Callers stage stageWindowIndexDelete first.
func stageWindowIndexWrite(batch *GatedBatch, fileID string, windows []FingerprintWindow) (int, error) {
	if len(windows) == 0 {
		return 0, nil
	}
	bands, err := fileWindowBands(windows)
	if err != nil {
		return 0, err
	}
	for _, b := range bands {
		if err := batch.Set(fpwinIdxKey(b, fileID), nil, nil); err != nil {
			return 0, err
		}
	}
	if err := batch.Set(fpwinIdxMetaKey(fileID), encodeWindowIndexMeta(bands), nil); err != nil {
		return 0, err
	}
	return len(bands), nil
}
