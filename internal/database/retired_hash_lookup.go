// file: internal/database/retired_hash_lookup.go
// version: 1.3.0
// guid: 0e7b3a6d-52c1-4f89-8d14-b6a39c7e5f20
// last-edited: 2026-10-29

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// RetiredBookFilesWithHash is the Pebble-only answer to "which retired rows
// hold this content, and does a live row hold it too?", for the window in
// which BookFilesWithHash refuses because memdb is not serving (warmup, or a
// warmup that lost book_file rows).
//
// The retired side is complete: every retired row sits under a retired version
// that has an idx:bv:from: key, so the versions are enumerated and each one's
// retired rows are read from Pebble (cost: one scan per retired version, which
// is why callers use the indexed memdb read whenever it is available). Inside a
// scope (BeginRetiredHashScope) that cost is paid once per scan, not once per
// lookup: see retiredHashCache for what is cached and how it is invalidated.
//
// The live side is the two single-owner hash keys BookFilesWithHash also
// unions in: book_file_hash: and book_file_orig_hash: name one row per hash,
// and a live row takes the key from a retired one (see the single-owner index
// rule in iface_retired.go). It can miss a live row that is not the key's
// owner. That errs toward "retired content": a caller that skips an import on
// a retired hit skips a file that imports on a later scan, and never creates a
// duplicate.
func (p *PebbleStore) RetiredBookFilesWithHash(hash string) (retired []RetiredRowRef, liveHolds bool, err error) {
	if hash == "" {
		return nil, false, nil
	}
	retired, cached, err := p.retiredRowsWithHashCached(hash)
	if err != nil {
		return nil, false, err
	}
	if !cached {
		if err := p.forEachRetiredRow(func(r retiredHashRow) error {
			if r.fileHash == hash || r.originalHash == hash {
				retired = append(retired, r.ref)
			}
			return nil
		}); err != nil {
			return nil, false, err
		}
	}
	for _, keyPrefix := range []string{"book_file_hash:", "book_file_orig_hash:"} {
		v, closer, err := p.db.Get([]byte(keyPrefix + hash))
		if errors.Is(err, pebble.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("read %s%s: %w", keyPrefix, hash, err)
		}
		ref := string(v)
		_ = closer.Close()
		parts := strings.SplitN(ref, ":", 2)
		if len(parts) != 2 {
			continue
		}
		f, err := p.getBookFileByID(parts[0], parts[1])
		if err != nil {
			return nil, false, fmt.Errorf("verify book_file %s: %w", parts[1], err)
		}
		if f != nil && !f.Retired && (f.FileHash == hash || f.OriginalFileHash == hash) {
			liveHolds = true
		}
	}
	return retired, liveHolds, nil
}

// RetiredRowRef names one retired row: the row, the live book now holding it
// and the retired version it sits under. It is everything the scanner's
// retired-content check reads of a row (which holder and version own the
// content), and the only shape RetiredBookFilesWithHash and RetiredHoldersOfHash
// return, in a scope or out of one, from Pebble or from memdb, so the answer
// never depends on which read produced it.
type RetiredRowRef struct {
	ID        string
	BookID    string
	VersionID string
}

// retiredHashRow is a retired row as the hash lookups read it: its ref and the
// two hashes it answers to.
type retiredHashRow struct {
	ref          RetiredRowRef
	fileHash     string
	originalHash string
}

// retiredHashMarker is the part of a stored book_file row forEachRetiredRow
// decodes. Everything else, the raw AcoustID fingerprint (about 230 KB a row)
// above all, is skipped rather than allocated.
type retiredHashMarker struct {
	ID               string `json:"id"`
	VersionID        string `json:"version_id"`
	Retired          bool   `json:"retired"`
	FileHash         string `json:"file_hash"`
	OriginalFileHash string `json:"original_file_hash"`
}

// forEachRetiredRow calls fn with every retired row of every retired version.
// The versions come from the idx:bv:from: keys (each retired version has one)
// and are grouped by holder, so a holder's rows are scanned once however many
// retired versions it holds. Rows come holder by holder (holders in the order
// their first version was enumerated), in key order within a holder.
func (p *PebbleStore) forEachRetiredRow(fn func(retiredHashRow) error) error {
	prefix := []byte(retiredVersionFromPrefix)
	seen := map[string]struct{}{}
	var versionIDs []string
	if err := forEachKeyInRange(p.db, prefix, prefixEnd(prefix), func(key, _ []byte) error {
		// idx:bv:from:<shell>:<version>; ids are UUIDs, so the last segment is
		// the version id.
		k := string(key)
		versionID := k[strings.LastIndex(k, ":")+1:]
		if _, dup := seen[versionID]; !dup {
			seen[versionID] = struct{}{}
			versionIDs = append(versionIDs, versionID)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("enumerate retired versions: %w", err)
	}
	var holders []string
	versionsOf := map[string]map[string]struct{}{}
	for _, versionID := range versionIDs {
		v, err := p.GetBookVersion(versionID)
		if err != nil {
			return fmt.Errorf("read retired version %s: %w", versionID, err)
		}
		if v == nil || v.Status != BookVersionStatusRetired {
			continue
		}
		if versionsOf[v.BookID] == nil {
			versionsOf[v.BookID] = map[string]struct{}{}
			holders = append(holders, v.BookID)
		}
		versionsOf[v.BookID][v.ID] = struct{}{}
	}
	for _, holder := range holders {
		rowPrefix := []byte("book_file:" + holder + ":")
		versions := versionsOf[holder]
		if err := forEachKeyInRange(p.db, rowPrefix, prefixEnd(rowPrefix), func(_, value []byte) error {
			var m retiredHashMarker
			if err := json.Unmarshal(value, &m); err != nil {
				return err
			}
			if _, ok := versions[m.VersionID]; !ok || !m.Retired {
				return nil
			}
			return fn(retiredHashRow{
				ref:      RetiredRowRef{ID: m.ID, BookID: holder, VersionID: m.VersionID},
				fileHash: m.FileHash, originalHash: m.OriginalFileHash,
			})
		}); err != nil {
			return fmt.Errorf("read retired rows held by %s: %w", holder, err)
		}
	}
	return nil
}

// retiredHashReader is the PebbleStore method RetiredHoldersOfHash resolves
// through AsCapability for the window in which memdb is not serving.
type retiredHashReader interface {
	RetiredBookFilesWithHash(hash string) ([]RetiredRowRef, bool, error)
}

// RetiredHoldersOfHash answers the scanner's question before it imports a book
// as new: which retired rows hold this content, and does a live row hold it
// too? It never blocks an import on memdb while no retired row can exist, and
// never fails open once one can:
//
//   - A store that can prove no retired version exists (NoRetiredVersionsKnown:
//     the Pebble store's one-seek probe, or a MockStore, which never retires a
//     row) answers "none" at once, whatever memdb is doing. Prod holds no
//     retired rows until the retire apply lands, so warmup cannot stall imports
//     on it.
//   - Otherwise the complete per-hash read (BookFilesHoldingHash, memdb) is
//     used.
//   - When that read refuses because memdb is not serving, the Pebble-only
//     RetiredBookFilesWithHash answers instead (complete for retired rows,
//     see there).
//   - A store with none of these returns the refusal, and the caller fails
//     closed.
func RetiredHoldersOfHash(s any, hash string) (retired []RetiredRowRef, liveHolds bool, err error) {
	if hash == "" {
		return nil, false, nil
	}
	none, err := NoRetiredVersionsKnown(s)
	if err != nil {
		return nil, false, err
	}
	if none {
		return nil, false, nil
	}
	rows, err := BookFilesHoldingHash(s, hash)
	if err == nil {
		for _, r := range rows {
			if r.Retired {
				retired = append(retired, RetiredRowRef{ID: r.ID, BookID: r.BookID, VersionID: r.VersionID})
			} else {
				liveHolds = true
			}
		}
		return retired, liveHolds, nil
	}
	if !errors.Is(err, ErrBookFilesWithHashUnavailable) {
		return nil, false, err
	}
	if fb, ok := AsCapability[retiredHashReader](s); ok {
		return fb.RetiredBookFilesWithHash(hash)
	}
	return nil, false, err
}
