// file: internal/metastate/metastate.go
// version: 1.0.0
// guid: 3a7e5c81-9d24-4f60-b8e3-1c6a2b9d4f07
// last-edited: 2026-09-01

// Package metastate owns the on-disk representation of per-book metadata
// state: the user-preference KEY a book's state is stored under, and the
// JSON encoding of an individual field value.
//
// # Why this is its own package
//
// These three functions were duplicated BYTE-FOR-BYTE across
// internal/audiobooks, internal/server and internal/metafetch. They were still
// identical when they were folded together, which is the reason to do it now
// rather than the reason not to: they describe a PERSISTED format. Key() names
// a namespace that already has rows written under it, and Encode/Decode are a
// matched pair. Three private copies meant any one of them could be "fixed" in
// isolation, and the result would not be a compile error or a failing test --
// it would be a reader that no longer finds what a writer stored.
//
// That is the same shape as the four-way file_hash split: several
// implementations of one identity, agreeing by luck rather than by
// construction. See internal/filehash.
package metastate

import (
	"encoding/json"
	"fmt"
)

// keyPrefix is a PERSISTED namespace. Existing user-preference rows are stored
// under it, so changing this string orphans every one of them -- it is not a
// formatting choice. TestKeyFormatIsPinned guards it.
const keyPrefix = "metadata_state_"

// Key returns the user-preference key holding a book's metadata state.
func Key(bookID string) string {
	return fmt.Sprintf("%s%s", keyPrefix, bookID)
}

// Decode turns a stored field value back into the Go value it was written
// from. A nil or empty pointer decodes to nil (the field was never set).
//
// A value that does not parse as JSON is returned AS THE RAW STRING rather
// than as an error. That is deliberate and load-bearing: rows written before
// values were JSON-encoded are still plain strings, so failing them would
// discard real user data. Encode/Decode therefore round-trip new values and
// tolerate old ones.
func Decode(raw *string) any {
	if raw == nil || *raw == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return *raw
	}
	return value
}

// Encode renders a field value for storage. A nil value encodes to a nil
// pointer -- "unset" is represented by absence, not by the JSON literal null,
// which is what Decode's nil check expects to read back.
func Encode(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	encoded := string(data)
	return &encoded, nil
}
