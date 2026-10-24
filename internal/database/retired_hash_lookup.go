// file: internal/database/retired_hash_lookup.go
// version: 1.0.0
// guid: 0e7b3a6d-52c1-4f89-8d14-b6a39c7e5f20
// last-edited: 2026-10-24

package database

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// retiredVersionFromPrefix is the prefix of every idx:bv:from:<shell>:<version>
// key. A retired book_file row exists only while its retired version does (a
// row is never born retired, and a retired version that still holds rows can
// be neither deleted nor moved nor leave the status), and CreateBookVersion
// refuses a retired version without RetiredFromBookID, so "no key under this
// prefix" proves "no retired row exists".
const retiredVersionFromPrefix = "idx:bv:from:"

// HasRetiredVersions reports whether any retired book version exists. It is one
// Pebble seek and never needs memdb. A false answer proves no retired book_file
// row exists, which lets a caller whose only question is "does this belong to a
// retired version?" answer no without the complete (memdb-only) per-hash read.
// A true answer is conservative: a retired version whose rows were all
// un-retired still counts.
func (p *PebbleStore) HasRetiredVersions() (bool, error) {
	prefix := []byte(retiredVersionFromPrefix)
	found := false
	err := forEachKeyInRange(p.db, prefix, prefixEnd(prefix), func(_, _ []byte) error {
		found = true
		return errStopScan
	})
	if err != nil {
		return false, fmt.Errorf("probe for retired versions: %w", err)
	}
	return found, nil
}

// RetiredBookFilesWithHash is the Pebble-only answer to "which retired rows
// hold this content, and does a live row hold it too?", for the window in
// which BookFilesWithHash refuses because memdb is not serving (warmup, or a
// warmup that lost book_file rows).
//
// The retired side is complete: every retired row sits under a retired version
// that has an idx:bv:from: key, so the versions are enumerated and each one's
// retired rows are read from Pebble (cost: one scan per retired version, which
// is why callers use the indexed memdb read whenever it is available).
//
// The live side is the two single-owner hash keys BookFilesWithHash also
// unions in: book_file_hash: and book_file_orig_hash: name one row per hash,
// and a live row takes the key from a retired one (see the single-owner index
// rule in iface_retired.go). It can miss a live row that is not the key's
// owner. That errs toward "retired content": a caller that skips an import on
// a retired hit skips a file that imports on a later scan, and never creates a
// duplicate.
func (p *PebbleStore) RetiredBookFilesWithHash(hash string) (retired []BookFile, liveHolds bool, err error) {
	if hash == "" {
		return nil, false, nil
	}
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
		return nil, false, fmt.Errorf("enumerate retired versions: %w", err)
	}
	for _, versionID := range versionIDs {
		v, err := p.GetBookVersion(versionID)
		if err != nil {
			return nil, false, fmt.Errorf("read retired version %s: %w", versionID, err)
		}
		if v == nil || v.Status != BookVersionStatusRetired {
			continue
		}
		rows, err := p.retiredRowsOfVersion(v.BookID, v.ID)
		if err != nil {
			return nil, false, fmt.Errorf("read retired rows of version %s: %w", versionID, err)
		}
		for _, r := range rows {
			if r.FileHash == hash || r.OriginalFileHash == hash {
				retired = append(retired, r)
			}
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

// retiredVersionProbe and retiredHashReader are the two PebbleStore methods
// RetiredHoldersOfHash resolves through AsCapability.
type retiredVersionProbe interface {
	HasRetiredVersions() (bool, error)
}

type retiredHashReader interface {
	RetiredBookFilesWithHash(hash string) ([]BookFile, bool, error)
}

// RetiredHoldersOfHash answers the scanner's question before it imports a book
// as new: which retired rows hold this content, and does a live row hold it
// too? It never blocks an import on memdb while no retired row can exist, and
// never fails open once one can:
//
//   - A store that can say no retired version exists (HasRetiredVersions)
//     answers "none" at once, whatever memdb is doing. Prod holds no retired
//     rows until the retire apply lands, so warmup cannot stall imports on it.
//   - Otherwise the complete per-hash read (BookFilesHoldingHash, memdb) is
//     used.
//   - When that read refuses because memdb is not serving, the Pebble-only
//     RetiredBookFilesWithHash answers instead (complete for retired rows,
//     see there).
//   - A store with none of these returns the refusal, and the caller fails
//     closed. A MockStore or a fake declaring NoRetiredBookFiles has no
//     retired rows and answers none.
func RetiredHoldersOfHash(s any, hash string) (retired []BookFile, liveHolds bool, err error) {
	if hash == "" {
		return nil, false, nil
	}
	if probe, ok := AsCapability[retiredVersionProbe](s); ok {
		has, err := probe.HasRetiredVersions()
		if err != nil {
			return nil, false, err
		}
		if !has {
			return nil, false, nil
		}
	}
	rows, err := BookFilesHoldingHash(s, hash)
	if err == nil {
		for _, r := range rows {
			if r.Retired {
				retired = append(retired, r)
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
