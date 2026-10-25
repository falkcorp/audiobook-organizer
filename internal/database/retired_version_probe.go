// file: internal/database/retired_version_probe.go
// version: 1.0.1
// guid: 3b8e51c4-7a0d-4f62-9c15-e2d74a6b0f83
// last-edited: 2026-10-25

package database

import (
	"fmt"
)

// retiredVersionFromPrefix is the prefix of every idx:bv:from:<shell>:<version>
// key, and the one place that literal is spelled (retiredVersionFromKey and
// GetRetiredVersionsFromBook build theirs from it). A retired book_file row exists only while its retired version does (a
// row is never born retired, and a retired version that still holds rows can
// be neither deleted nor moved nor leave the status), and CreateBookVersion
// refuses a retired version without RetiredFromBookID, so "no key under this
// prefix" proves "no retired row exists".
const retiredVersionFromPrefix = "idx:bv:from:"

// HasRetiredVersions reports whether any retired book version exists. It is one
// Pebble seek and never needs memdb. A false answer proves no retired book_file
// row exists, which lets a caller whose only question is "does a retired
// version hold this?" answer no without the complete (memdb-only) per-path or
// per-hash read. A true answer is conservative: a retired version whose rows
// were all un-retired still counts.
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

// retiredVersionProbe is the PebbleStore method NoRetiredVersionsKnown resolves
// through AsCapability.
type retiredVersionProbe interface {
	HasRetiredVersions() (bool, error)
}

// NoRetiredVersionsKnown reports whether s can PROVE that no retired book
// version (so no retired book_file row) exists. It answers false, never an
// assumption of "none", for a store that cannot say (a decorator that has not
// opted into Unwrap, a test double): a caller that skips a retired-row check on
// true must keep making it on false. A probe read failure is returned.
func NoRetiredVersionsKnown(s any) (bool, error) {
	probe, ok := AsCapability[retiredVersionProbe](s)
	if !ok {
		return false, nil
	}
	has, err := probe.HasRetiredVersions()
	if err != nil {
		return false, err
	}
	return !has, nil
}
