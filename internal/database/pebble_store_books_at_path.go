// file: internal/database/pebble_store_books_at_path.go
// version: 1.2.0
// guid: ae16bae9-c3ef-4063-8cbc-7353bb345e55
// last-edited: 2026-09-12

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/cockroachdb/pebble/v2"
)

// bookAtPathRow is the part of a book row LiveBookIDsAtPath reads.
type bookAtPathRow struct {
	FilePath          string `json:"file_path"`
	MarkedForDeletion *bool  `json:"marked_for_deletion,omitempty"`
}

// bookAtPathReadHook, when non-nil, is told which path served each
// LiveBookIDsAtPath call. Test-only; nil in production.
var bookAtPathReadHook func(viaIndex bool)

// LiveBookIDsAtPath returns the id of every live (not soft-deleted) book whose
// FilePath is exactly path, in id (key) order.
//
// It is the multi-valued counterpart of GetBookByFilePath, which reads the
// single book:path:<path> index key. That key holds one id and the last writer
// wins, and UpdateBook deletes it whenever any book moves off the path, so it
// can name a soft-deleted book or nothing while a live book still sits there.
// A caller that must know whether a path is free needs this instead.
//
// Served from the book_atpath: index once its backfill sentinel exists, and
// from a full scan of the book rows until then, so an incomplete index is
// never consulted. Either way every candidate is verified against its row.
//
// Any read or decode error fails the call: a partial answer would report a
// taken path as free.
func (p *PebbleStore) LiveBookIDsAtPath(path string) ([]string, error) {
	built, err := p.bookAtPathIndexBuilt()
	if err != nil {
		return nil, fmt.Errorf("live books at path: %w", err)
	}
	if bookAtPathReadHook != nil {
		bookAtPathReadHook(built)
	}
	if !built {
		return p.liveBookIDsAtPathScan(path)
	}
	return p.liveBookIDsAtPathIndex(path)
}

// liveBookIDsAtPathScan reads every book row. It is the pre-backfill fallback,
// and the oracle the index reader and the verify op are tested against.
func (p *PebbleStore) liveBookIDsAtPathScan(path string) ([]string, error) {
	var ids []string
	err := forEachBookRow(p.db, func(id string, v []byte) error {
		var row bookAtPathRow
		if err := json.Unmarshal(v, &row); err != nil {
			return fmt.Errorf("decode book row %s: %w", id, err)
		}
		if row.FilePath == path && !markedForDeletionFlag(row.MarkedForDeletion) {
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("live books at path: %w", err)
	}
	return ids, nil
}

// liveBookIDsAtPathIndex reads path's index keys and point-verifies each
// candidate row, all from one snapshot. An index key and its row are committed
// in one batch, so a book moving into or out of path mid-lookup is seen
// consistently.
func (p *PebbleStore) liveBookIDsAtPathIndex(path string) ([]string, error) {
	snap := p.db.NewSnapshot()
	defer snap.Close()

	repaired, err := undecodableMarkedAtPath(snap, path)
	if err != nil {
		return nil, fmt.Errorf("live books at path: %w", err)
	}

	lower, upper := bookAtPathBounds(path)
	var candidates []string
	err = forEachKeyInRange(snap, lower, upper, func(key, _ []byte) error {
		rest := key[len(lower):]
		if len(rest) == 0 {
			return fmt.Errorf("corrupt book_atpath key %q: empty book id", key)
		}
		// A NUL in the remainder means this key belongs to a (corrupt)
		// NUL-bearing path that shares P as a prefix, not to P. Book ids
		// never contain NUL.
		if bytes.IndexByte(rest, 0) >= 0 {
			return nil
		}
		candidates = append(candidates, string(rest))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("live books at path: %w", err)
	}

	var ids []string
	for _, id := range candidates {
		v, closer, err := snap.Get([]byte("book:" + id))
		if errors.Is(err, pebble.ErrNotFound) {
			continue // extra: the book was hard-deleted
		}
		if err != nil {
			return nil, fmt.Errorf("live books at path: read book %s: %w", id, err)
		}
		var row bookAtPathRow
		decErr := json.Unmarshal(v, &row)
		closer.Close()
		if decErr != nil {
			return nil, fmt.Errorf("live books at path: decode book row %s: %w", id, decErr)
		}
		if row.FilePath == path && !markedForDeletionFlag(row.MarkedForDeletion) {
			ids = append(ids, id)
		}
	}
	if len(repaired) > 0 {
		// Merge the repaired-out-of-band rows, deduped, back into id order.
		ids = append(ids, repaired...)
		slices.Sort(ids)
		ids = slices.Compact(ids)
	}
	return ids, nil
}

// undecodableMarkedAtPath walks the undecodable-row markers and point-reads
// each marked book:<id> from snap, the same snapshot as the rest of the read:
//
//   - row gone: the marker is stale; ignore it (the next rebuild clears it).
//   - row still undecodable: return the fail-closed error. Store-wide on
//     purpose: such a row has no readable path, so it could be at ANY path,
//     and the pre-sentinel full scan already fails every lookup on it. An
//     empty answer would tell a caller (#3335's regroup) that an occupied
//     folder is free.
//   - row now decodes (repaired out of band): it has NO book_atpath key yet,
//     so ignoring it would report its folder free. It is a candidate: if it is
//     live and at path, its id is returned for merging with the index result.
//
// Normally there are no markers, so this is one empty seek.
func undecodableMarkedAtPath(snap *pebble.Snapshot, path string) ([]string, error) {
	lower := []byte(bookAtPathUndecodablePrefix)
	var blocking, atPath []string
	err := forEachKeyInRange(snap, lower, bareRowUpperBound(bookAtPathUndecodablePrefix), func(key, _ []byte) error {
		id := string(key[len(lower):])
		v, closer, err := snap.Get([]byte("book:" + id))
		if errors.Is(err, pebble.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read undecodable-marked book %s: %w", id, err)
		}
		var row bookAtPathRow
		decErr := json.Unmarshal(v, &row)
		closer.Close()
		if decErr != nil {
			blocking = append(blocking, id)
			return nil
		}
		if row.FilePath == path && !markedForDeletionFlag(row.MarkedForDeletion) {
			atPath = append(atPath, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(blocking) > 0 {
		return nil, fmt.Errorf("%d book row(s) cannot be decoded (e.g. book:%s); LiveBookIDsAtPath "+
			"refuses until each is rewritten or removed out of band (UpdateBook and DeleteBook cannot "+
			"read it either), then run maintenance.book-atpath-index-backfill", len(blocking), blocking[0])
	}
	return atPath, nil
}
