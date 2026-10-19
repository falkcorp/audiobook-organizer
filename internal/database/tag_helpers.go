// file: internal/database/tag_helpers.go
// version: 1.3.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-10-19

package database

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Tag helpers for the "exactly one tag in this namespace" idiom.
//
// Several system tag namespaces are semantically singletons — a
// book (or author / series) should have exactly one value at a
// time. Examples:
//
//	metadata:source:<name>    — last metadata apply provenance
//	metadata:language:<code>  — language of the applied metadata
//	dedup:merge-survivor:<method> — how the last merge happened
//
// The naive approach "delete prefix + add tag" does wasteful
// writes on every apply even when the value hasn't changed. These
// helpers read current state first and short-circuit when the
// desired tag is already in place, so a re-apply of identical
// metadata is a true no-op at the tag layer.

// BookTagSingletonStore is what EnsureSingletonBookTag needs. It was
// previously Store -- the 398-method union -- to call three methods on one
// entity; the narrow interface means a caller only has to satisfy the tag
// methods it uses.
type BookTagSingletonStore interface {
	GetBookTagsDetailed(bookID string) ([]BookTag, error)
	RemoveBookTagsByPrefix(bookID, prefix, source string) error
	AddBookTagWithSource(bookID, tag, source string) error
}

// EnsureSingletonBookTag makes sure exactly one book_tag row
// matches `prefix` at the given `source`, and it equals `fullTag`.
// If `fullTag` is already present at `source`, does nothing.
// Otherwise removes any conflicting tags with that prefix and
// adds the new one.
//
// `fullTag` must start with `prefix` — the helper enforces this
// because mismatched prefix/tag combinations are almost always
// programmer bugs ("metadata:source:audible" with prefix
// "metadata:language:" would delete every language tag).
func EnsureSingletonBookTag(store BookTagSingletonStore, bookID, prefix, fullTag, source string) error {
	prefix = util.NormalizeString(prefix)
	fullTag = util.NormalizeString(fullTag)
	if prefix == "" || fullTag == "" {
		return nil
	}
	if !strings.HasPrefix(fullTag, prefix) {
		return errInvalidSingletonTag
	}

	detailed, err := store.GetBookTagsDetailed(bookID)
	if err != nil {
		return err
	}

	// Scan for conflicts + short-circuit hit.
	alreadyCorrect := false
	hasConflict := false
	for _, bt := range detailed {
		if !strings.HasPrefix(bt.Tag, prefix) {
			continue
		}
		if bt.Source != source {
			continue
		}
		if bt.Tag == fullTag {
			alreadyCorrect = true
			continue
		}
		hasConflict = true
	}

	// Fast path: exactly the right tag is already present and
	// nothing else in the namespace needs to go.
	if alreadyCorrect && !hasConflict {
		return nil
	}

	// Clear everything in the namespace (at this source), then
	// write the target value. Done in two steps rather than one
	// so an interrupted apply never leaves zero tags in a
	// namespace that should always have exactly one.
	if hasConflict {
		if err := store.RemoveBookTagsByPrefix(bookID, prefix, source); err != nil {
			return err
		}
	}
	return store.AddBookTagWithSource(bookID, fullTag, source)
}

// errInvalidSingletonTag signals that a caller passed a fullTag
// that doesn't start with the given prefix. Kept as a package-
// level sentinel so tests can assert on it without string
// matching.
var errInvalidSingletonTag = &tagError{"singleton tag does not match prefix"}

type tagError struct{ msg string }

func (e *tagError) Error() string { return e.msg }
