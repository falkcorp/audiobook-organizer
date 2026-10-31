// file: internal/database/pebble_store_lsh.go
// version: 1.0.2
// guid: e083305c-0d28-49c9-9f90-40b2c068414f
// last-edited: 2026-10-31

// Package-level exported API for the LSH (locality-sensitive hashing) secondary
// index over whole-file AcoustID fingerprints.
//
// # Key format invariants (normative — do not change without bumping lsh_index_v2)
//
//	fpidx:<band:1B><subprint:8B>:<bookFileID>   → BookID (UTF-8 bytes)
//	fpidx_meta:<bookFileID>                     → 1B version + N×(1B band + 8B subprint)
//
// The fpidx_meta row is the member list that makes O(1) deletes possible: when
// a fingerprint is updated or a file is deleted, we look up its meta row, derive
// the old (band, subprint) pairs, delete each fpidx: row, then delete the meta
// row itself — all in one batch, without needing to recompute the old fingerprint.
//
// The constant names lshKeyPrefix / lshMetaKeyPrefix, helper funcs
// (lshIndexKey, lshMetaKey, encodeLSHMeta, decodeLSHMeta), and the low-level
// write/delete helpers (writeFingerprintLSHIndexes,
// deleteFingerprintLSHIndexesByIDWithStore) live in pebble_store.go alongside
// the CreateBookFile / UpdateBookFile / DeleteBookFile chokepoints that call
// them. This file provides the exported, op-level surface:
//
//   - PutLSHEntries  — idempotent write for a (fileID, bookID, subprints) tuple
//   - DeleteLSHEntries — delete all fpidx: rows for a file via its meta row
//   - LSHProbe       — point-lookup candidate fan-out returning ≥LSHMinBandHits hits
//   - IsLSHIndexBuilt / SetLSHIndexBuilt — versioned completion flag

package database

import (
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// lshIndexBuiltFlagKey is the PebbleDB settings key set when the
// lsh-index-build op completes successfully. The v1 suffix lets us
// force a re-run if the key format ever changes by incrementing to v2.
const lshIndexBuiltFlagKey = "system:flag:lsh_index_v1_done"

// IsLSHIndexBuilt reports whether the lsh-index-build op has completed
// at least once on this database. Callers that need to probe the index
// (T013) should gate on this flag — if unset, the probe would silently
// return empty results rather than the expected candidate set.
func (s *PebbleStore) IsLSHIndexBuilt() bool {
	setting, err := s.GetSetting(lshIndexBuiltFlagKey)
	return err == nil && setting != nil && setting.Value == "true"
}

// SetLSHIndexBuilt marks the lsh-index-build op complete. Called by
// lsh_index_build.go when the full scan finishes without error.
func (s *PebbleStore) SetLSHIndexBuilt() error {
	return s.SetSetting(lshIndexBuiltFlagKey, "true", "bool", false)
}

// PutLSHEntries writes the fpidx: secondary-index rows and the
// fpidx_meta: member-list row for (fileID, bookID, subprints) in a
// single atomic batch. The value stored in each fpidx: row is the
// bookID (UTF-8 bytes), so the probe can return candidate book IDs
// without a second lookup.
//
// Idempotent: re-writing the same (fileID, subprints) pair overwrites
// the previous rows with identical content. Safe to call in batch loops
// without a HasLSHIndex guard — the op itself skips files that already
// have a meta row when doing incremental runs.
//
// OWNER. bookID is what the caller read when it picked the file up, which can
// be a moment before this write. A retire, un-retire or re-home re-keys the row
// under another book and rewrites its fpidx: values in its own batch
// (stageLSHOwnerRewrite), so a value written here from a stale bookID would put
// the old owner back. PutLSHEntries therefore takes the file's write stripe,
// the one those writers hold across their commit, and takes the owner from the
// book_file_id: index when the file has a row; bookID stands only for a file
// with no row (a caller indexing ahead of the row, or a test).
//
// subprints must be non-nil and non-empty; callers should check
// fingerprint.Subprints before calling. If either is empty, PutLSHEntries
// is a no-op (returns nil).
func (s *PebbleStore) PutLSHEntries(fileID, bookID string, subs []fingerprint.Subprint, bands []byte) error {
	if len(subs) == 0 || len(bands) == 0 {
		return nil
	}
	unlock := s.lockBookFile(fileID)
	defer unlock()
	if cur, err := s.GetBookFileByFileID(fileID); err != nil {
		return fmt.Errorf("pebble_lsh: resolve owner of %s: %w", fileID, err)
	} else if cur != nil {
		bookID = cur.BookID
	}
	batch := s.db.NewBatch()
	val := []byte(bookID)
	for i := range subs {
		if err := batch.Set(lshIndexKey(bands[i], subs[i], fileID), val, nil); err != nil {
			batch.Close()
			return fmt.Errorf("pebble_lsh: set fpidx key: %w", err)
		}
	}
	if err := batch.Set(lshMetaKey(fileID), encodeLSHMeta(subs, bands), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble_lsh: set fpidx_meta key: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("pebble_lsh: commit: %w", err)
	}
	return nil
}

// DeleteLSHEntries removes all fpidx: index rows for fileID and then
// removes the fpidx_meta: row. Safe to call when no index exists for
// the file (no-op). Used by the build op when re-indexing is needed,
// and by the write/delete hooks in pebble_store.go.
func (s *PebbleStore) DeleteLSHEntries(fileID string) error {
	batch := s.db.NewBatch()
	if err := deleteFingerprintLSHIndexesByIDWithStore(s, batch, fileID); err != nil {
		batch.Close()
		return fmt.Errorf("pebble_lsh: delete entries: %w", err)
	}
	return batch.Commit(pebble.Sync)
}

// LSHProbe performs point-lookups for each supplied subprint, counts
// band-hit collisions per candidate fileID, and returns only those with
// ≥ fingerprint.LSHMinBandHits hits. The returned map key is the
// candidate fileID; the value is the hit count. An empty map means no
// near-duplicate candidates.
//
// Probe is intentionally symmetric with LookupAcoustIDCandidates but
// returns the raw hit-count map rather than a sorted slice, so the
// collector (T013) can scale confidence by hit count without losing
// information.
//
// maxCandidates caps the returned map at that many entries (highest hit
// count first). ≤0 means no cap.
func (s *PebbleStore) LSHProbe(subs []fingerprint.Subprint, bands []byte, maxCandidates int) (map[string]int, error) {
	if len(subs) == 0 {
		return nil, nil
	}
	hits := make(map[string]int, 256)
	for i := range subs {
		lower := lshIndexKey(bands[i], subs[i], "")
		upper := append([]byte{}, lower...)
		// Bump the trailing ':' to ';' to form the exclusive upper bound.
		upper[len(upper)-1] = ';'
		iter, ierr := s.db.NewIter(&pebble.IterOptions{
			LowerBound: lower,
			UpperBound: upper,
		})
		if ierr != nil {
			return nil, fmt.Errorf("pebble_lsh: probe iter: %w", ierr)
		}
		for iter.First(); iter.Valid(); iter.Next() {
			key := iter.Key()
			// key layout: fpidx:<band:1><subprint:8>:<fileID>
			// fileID starts at offset 16 (prefix 6 + band 1 + subprint 8 + sep 1)
			if len(key) <= 16 {
				continue
			}
			hits[string(key[16:])]++
		}
		_ = iter.Close()
	}

	// Filter to candidates meeting the minimum band-hit threshold.
	filtered := make(map[string]int, len(hits))
	for id, n := range hits {
		if n >= fingerprint.LSHMinBandHits {
			filtered[id] = n
		}
	}

	// Cap by maxCandidates if requested (highest hit count first).
	if maxCandidates > 0 && len(filtered) > maxCandidates {
		type entry struct {
			id  string
			hit int
		}
		ranked := make([]entry, 0, len(filtered))
		for id, n := range filtered {
			ranked = append(ranked, entry{id, n})
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].hit != ranked[j].hit {
				return ranked[i].hit > ranked[j].hit
			}
			return ranked[i].id < ranked[j].id
		})
		filtered = make(map[string]int, maxCandidates)
		for _, e := range ranked[:maxCandidates] {
			filtered[e.id] = e.hit
		}
	}

	return filtered, nil
}
