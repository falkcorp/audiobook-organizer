// file: internal/database/pebble_store_bookfile_acoustid_repair.go
// version: 1.0.0
// guid: 0e5b7a3c-91d4-4f28-b6a0-7c2d9e1f4a85
// last-edited: 2026-10-30

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// Repair of orphaned book_file_acoustid: index keys.
//
// WHAT AN ORPHAN IS. book_file_acoustid:<segment> -> "<bookID>:<fileID>" is a
// single-owner secondary index (see lockSingleOwnerIndex) over the legacy
// AcoustIDSeg0..6 columns. Since T020 a book_file: row no longer stores those
// columns (marshalBookFileDropSegs), but until 2026-10-30 every create and
// update still wrote an index key for whatever segments the caller's struct
// carried (the fingerprint backfill sets AcoustIDSeg0 on every row it
// fingerprints). Nothing could remove such a key afterwards:
// deleteBookFileSecondaryIndexes and SweepBookFileSegDrop both find a row's
// segments in the STORED row, which has none. So the keys outlived the
// fingerprint they were written for, and the rows themselves.
//
// A key is an orphan when the row it names does not hold its segment: the row
// is gone, or it decodes without that segment. A key whose row still holds the
// segment is a live legacy key; SweepBookFileSegDrop owns those, and this
// repair leaves them alone.
//
// THE RACE. The index is a last-writer-wins single-owner family, and the
// retire, un-retire and rehome writers repoint its keys from a read taken
// before their commit (indexCAS). A repair that judged a key and deleted it
// later could delete a key a retire had just repointed to a live row. So the
// delete judges each key AGAIN, from committed state, inside the
// commitBookFileBatchVerified verify hook: that runs under
// lockSingleOwnerIndex, which every writer of these keys takes around its own
// apply, so the judgement and the delete are one step and no writer can move a
// key between them. A retire that read a key this repair then deletes fails its
// own indexCAS.verify (the key vanished) and re-stages from fresh reads.
// The first judgement (JudgeBookFileAcoustIDEntry) is therefore only a
// pre-filter that keeps the mutex hold short; it can be wrong without harm.

// bookFileAcoustIDIndexPageMax bounds one ScanBookFileAcoustIDIndex page. Each
// key embeds a whole segment string (hundreds of bytes), so the page, not the
// index, is what is held in memory.
const bookFileAcoustIDIndexPageMax = 4096

// BookFileAcoustIDEntry is one book_file_acoustid: index entry as scanned.
type BookFileAcoustIDEntry struct {
	// Seg is the segment string the key is named for (the key minus its prefix).
	Seg string
	// Ref is the key's value: "<bookID>:<fileID>".
	Ref string
}

// BookFileAcoustIDVerdict is what JudgeBookFileAcoustIDEntry says of an entry.
type BookFileAcoustIDVerdict int

const (
	// AcoustIDEntryLive: the named row holds the key's segment (a legacy row
	// SweepBookFileSegDrop will rewrite). Never deleted by the repair.
	AcoustIDEntryLive BookFileAcoustIDVerdict = iota + 1
	// AcoustIDEntryRowLacksSeg: the named row exists and does not hold the
	// segment. An orphan.
	AcoustIDEntryRowLacksSeg
	// AcoustIDEntryRowGone: no row by the named ID exists. An orphan.
	AcoustIDEntryRowGone
	// AcoustIDEntryMalformed: the value is not "<bookID>:<fileID>", so no row
	// can be judged. Reported, never deleted (a human should look at it).
	AcoustIDEntryMalformed
)

func (v BookFileAcoustIDVerdict) String() string {
	switch v {
	case AcoustIDEntryLive:
		return "live"
	case AcoustIDEntryRowLacksSeg:
		return "row_lacks_segment"
	case AcoustIDEntryRowGone:
		return "row_gone"
	case AcoustIDEntryMalformed:
		return "malformed_ref"
	}
	return fmt.Sprintf("verdict(%d)", int(v))
}

// IsOrphan reports whether the repair may delete an entry with this verdict.
func (v BookFileAcoustIDVerdict) IsOrphan() bool {
	return v == AcoustIDEntryRowLacksSeg || v == AcoustIDEntryRowGone
}

// ScanBookFileAcoustIDIndex returns up to limit (capped at
// bookFileAcoustIDIndexPageMax) book_file_acoustid: entries in key order,
// starting after the key `after` (empty: from the start), and the cursor to
// pass for the next page ("" once the index is exhausted).
func (s *PebbleStore) ScanBookFileAcoustIDIndex(after string, limit int) ([]BookFileAcoustIDEntry, string, error) {
	if limit <= 0 || limit > bookFileAcoustIDIndexPageMax {
		limit = bookFileAcoustIDIndexPageMax
	}
	pairs, next, err := s.ScanPrefixPage("book_file_acoustid:", after, limit)
	if err != nil {
		return nil, "", err
	}
	out := make([]BookFileAcoustIDEntry, len(pairs))
	for i, kv := range pairs {
		out[i] = BookFileAcoustIDEntry{
			Seg: strings.TrimPrefix(kv.Key, "book_file_acoustid:"),
			Ref: string(kv.Value),
		}
	}
	return out, next, nil
}

// JudgeBookFileAcoustIDEntry classifies one scanned entry against the row it
// names, from committed state. It is safe to call concurrently and writes
// nothing.
func (s *PebbleStore) JudgeBookFileAcoustIDEntry(e BookFileAcoustIDEntry) (BookFileAcoustIDVerdict, error) {
	return s.judgeAcoustIDIndexEntry(e.Seg, e.Ref)
}

func (s *PebbleStore) judgeAcoustIDIndexEntry(seg, ref string) (BookFileAcoustIDVerdict, error) {
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return AcoustIDEntryMalformed, nil
	}
	bookID, fileID := parts[0], parts[1]

	raw, closer, err := s.db.Get(bookFileKey(bookID, fileID))
	switch {
	case err == nil:
		has, herr := rowStoresAcoustIDSeg(raw, seg)
		_ = closer.Close()
		if herr != nil {
			return 0, fmt.Errorf("judge book_file_acoustid:%.16s…: row %s: %w", seg, ref, herr)
		}
		if has {
			return AcoustIDEntryLive, nil
		}
		return AcoustIDEntryRowLacksSeg, nil
	case !errors.Is(err, pebble.ErrNotFound):
		return 0, fmt.Errorf("judge book_file_acoustid:%.16s…: read row %s: %w", seg, ref, err)
	}

	// The ref names no row. A row moved to another book under an index that
	// still names its old owner is found by its ID; only if that fails too is
	// the row gone.
	if moved := s.lookupBookFileByIDIndex(fileID); moved != nil {
		if bookFileHoldsSeg(moved, seg) {
			return AcoustIDEntryLive, nil
		}
		return AcoustIDEntryRowLacksSeg, nil
	}
	return AcoustIDEntryRowGone, nil
}

// rowStoresAcoustIDSeg reports whether the stored row bytes hold seg in any of
// acoustid_seg0..6. The byte probe skips the decode of every row without one of
// the (omitempty) segment fields, which is nearly all of them.
func rowStoresAcoustIDSeg(raw []byte, seg string) (bool, error) {
	if seg == "" || !bytes.Contains(raw, []byte(`"acoustid_seg`)) {
		return false, nil
	}
	var f BookFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return false, err
	}
	return bookFileHoldsSeg(&f, seg), nil
}

func bookFileHoldsSeg(f *BookFile, seg string) bool {
	if seg == "" {
		return false
	}
	for _, have := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
		f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
		if have == seg {
			return true
		}
	}
	return false
}

// DeleteOrphanBookFileAcoustIDEntries deletes the entries that are still
// orphans, judged again from committed state under the single-owner index mutex
// (see THE RACE above), and returns how many it deleted. An entry that became
// live, was repointed to a row that holds its segment, is malformed, or was
// already deleted is left alone and not counted; the caller reports
// len(entries) - deleted as kept.
//
// Callers pass modest batches: the mutex is held for one judgement per entry
// plus the apply, and blocks every other writer of a single-owner key.
func (s *PebbleStore) DeleteOrphanBookFileAcoustIDEntries(ctx context.Context, entries []BookFileAcoustIDEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	batch := s.db.NewBatch()
	deleted := 0
	verify := func() error {
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := []byte("book_file_acoustid:" + e.Seg)
			cur, closer, err := s.db.Get(key)
			if errors.Is(err, pebble.ErrNotFound) {
				continue // already gone
			}
			if err != nil {
				return fmt.Errorf("re-read %s: %w", key, err)
			}
			ref := string(cur)
			_ = closer.Close()
			verdict, err := s.judgeAcoustIDIndexEntry(e.Seg, ref)
			if err != nil {
				return err
			}
			if !verdict.IsOrphan() {
				continue
			}
			if err := batch.Delete(key, nil); err != nil {
				return err
			}
			deleted++
		}
		return nil
	}
	// No owner stripes: the repair writes no book_file: row and creates no row
	// under a book. The single-owner index mutex taken inside is what
	// serializes it against every writer of these keys.
	err := s.commitBookFileBatchVerified(batch, nil, nil, nil, verify)
	if !bookFileApplied(err) {
		return 0, err
	}
	return deleted, err
}
