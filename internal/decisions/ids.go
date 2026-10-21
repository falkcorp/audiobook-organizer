// file: internal/decisions/ids.go
// version: 1.0.0
// guid: 2d94b7e0-6c1a-4f38-95a3-e80b17d4c652
// last-edited: 2026-10-21

package decisions

import (
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"
)

// ID CANONICALISATION. An id is part of a fingerprint (SubjectKey) and of a
// pair-edge key, so two spellings of one id are two different proposals: a
// lowercase book ULID would miss the veto recorded under its uppercase twin,
// and "b1 " would miss "b1". A miss is a missing veto, the unsafe direction,
// so every entry point that takes an id either returns the canonical form or
// refuses:
//
//   - an entity id may not be empty, hold a control character, or begin or end
//     with whitespace (refused, never trimmed: trimming would hide a caller bug
//     and make two ids one);
//   - an entity id that is a ULID (26 Crockford characters; every book id is
//     one) is upper-cased, the form ulid.String() prints;
//   - a record id is a ULID and is upper-cased the same way;
//   - a subject key is 64 hex characters and is lower-cased.
//
// Entry points: Record (Input.validate), SubjectKey, Lookup, Get, History,
// ByEntity, Store.PairVetoed and Vetoes.PairVetoed.

// CanonicalID returns the canonical form of an entity id, or an error wrapping
// ErrInvalid.
func CanonicalID(id string) (string, error) {
	if err := checkComponent("entity_id", id); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if strings.TrimSpace(id) != id {
		return "", invalid("entity_id %q has leading or trailing whitespace", id)
	}
	if len(id) == ulid.EncodedSize {
		if u, err := ulid.ParseStrict(id); err == nil {
			return u.String(), nil
		}
	}
	return id, nil
}

// canonicalIDs canonicalises, sorts and de-duplicates ids. Two spellings of
// one id collapse into one.
func canonicalIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		c, err := CanonicalID(id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return sortedUnique(out), nil
}

// CanonicalRecordID returns the canonical (upper-case) form of a record id.
func CanonicalRecordID(id string) (string, error) {
	u, err := ulid.ParseStrict(id)
	if err != nil {
		return "", invalid("record id %q is not a ULID", id)
	}
	return u.String(), nil
}

// CanonicalSubjectKey returns the canonical (lower-case) form of a subject key.
func CanonicalSubjectKey(key string) (string, error) {
	if len(key) != 64 {
		return "", invalid("subject key must be 64 hex characters, got %d", len(key))
	}
	k := strings.ToLower(key)
	for i := 0; i < len(k); i++ {
		if c := k[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", invalid("subject key %q is not hex", key)
		}
	}
	return k, nil
}
