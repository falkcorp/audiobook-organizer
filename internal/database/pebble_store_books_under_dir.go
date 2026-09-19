// file: internal/database/pebble_store_books_under_dir.go
// version: 1.0.0
// guid: 7c41e2a9-5b3d-4f86-9a0e-2d8b6f1c4e57
// last-edited: 2026-09-19

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// LiveBookPathsUnderDir returns id -> FilePath for every live (not
// soft-deleted) book whose FilePath lies beneath dir, at any depth. dir itself
// and a sibling that merely shares the prefix ("/a/Tale 2" for "/a/Tale") are
// not beneath it.
//
// Served from the book_atpath: index once its backfill sentinel exists: one
// range scan over [book_atpath:<dir>/, book_atpath:<dir>0) -- '0' is the byte
// after '/' -- plus a point read of each candidate row, all from one
// snapshot. Before the sentinel exists it falls back to a full scan of the
// book rows, like LiveBookIDsAtPath. Any read or decode error fails the call:
// a partial answer would hide a book in the folder.
func (p *PebbleStore) LiveBookPathsUnderDir(dir string) (map[string]string, error) {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	built, err := p.bookAtPathIndexBuilt()
	if err != nil {
		return nil, fmt.Errorf("live books under dir: %w", err)
	}
	if !built {
		return p.liveBookPathsUnderDirScan(prefix)
	}
	return p.liveBookPathsUnderDirIndex(prefix)
}

func (p *PebbleStore) liveBookPathsUnderDirScan(prefix string) (map[string]string, error) {
	out := map[string]string{}
	err := forEachBookRow(p.db, func(id string, v []byte) error {
		var row bookAtPathRow
		if err := json.Unmarshal(v, &row); err != nil {
			return fmt.Errorf("decode book row %s: %w", id, err)
		}
		if strings.HasPrefix(row.FilePath, prefix) && !markedForDeletionFlag(row.MarkedForDeletion) {
			out[id] = row.FilePath
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("live books under dir: %w", err)
	}
	return out, nil
}

func (p *PebbleStore) liveBookPathsUnderDirIndex(prefix string) (map[string]string, error) {
	snap := p.db.NewSnapshot()
	defer snap.Close()

	under := func(path string) bool { return strings.HasPrefix(path, prefix) }
	out, err := undecodableMarkedMatching(snap, under)
	if err != nil {
		return nil, fmt.Errorf("live books under dir: %w", err)
	}

	lower := append([]byte(bookAtPathPrefix), prefix...)
	upper := append([]byte(bookAtPathPrefix), prefix[:len(prefix)-1]...)
	upper = append(upper, '/'+1)
	type cand struct{ id, path string }
	var cands []cand
	err = forEachKeyInRange(snap, lower, upper, func(key, _ []byte) error {
		rest := key[len(bookAtPathPrefix):]
		nul := bytes.IndexByte(rest, 0)
		if nul < 0 || nul == len(rest)-1 {
			return fmt.Errorf("corrupt book_atpath key %q", key)
		}
		cands = append(cands, cand{id: string(rest[nul+1:]), path: string(rest[:nul])})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("live books under dir: %w", err)
	}
	for _, c := range cands {
		v, closer, err := snap.Get([]byte("book:" + c.id))
		if errors.Is(err, pebble.ErrNotFound) {
			continue // extra: the book was hard-deleted
		}
		if err != nil {
			return nil, fmt.Errorf("live books under dir: read book %s: %w", c.id, err)
		}
		var row bookAtPathRow
		decErr := json.Unmarshal(v, &row)
		closer.Close()
		if decErr != nil {
			return nil, fmt.Errorf("live books under dir: decode book row %s: %w", c.id, decErr)
		}
		// Verify against the row: an extra key left by a move is skipped.
		if under(row.FilePath) && row.FilePath == c.path && !markedForDeletionFlag(row.MarkedForDeletion) {
			out[c.id] = row.FilePath
		}
	}
	return out, nil
}
