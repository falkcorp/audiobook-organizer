// file: internal/database/book_file_location.go
// version: 1.0.0
// guid: 4b9d2e71-6c3a-4f58-9e07-a1d5c8b3f264
// last-edited: 2026-10-13

package database

import (
	"encoding/json"
	"fmt"
)

// The location of a book_file row as a book_file repoint records it. These
// lived in internal/undo until 2026-10-13; they moved here because the v3
// SDK (pkg/ops) and its Writer (internal/operations/opswriter) name them, and
// importing internal/undo from either pulled undo's internal/merge closure
// (backup, config, aidispatch, dedup/unified, ...) into the plugin SDK's
// dependency closure (tools/cmd/sdkguard). Nothing here depends on undo: the
// revert engine reads these, it does not own them.

// ChangeTypeBookFileRepoint: the book_file row named in FieldName
// ("book_file:<id>") was pointed at a file that already sat elsewhere on
// disk, with NOTHING moved. OldValue and NewValue are BookFileLocation
// JSON: the row's path, Missing flag, hash and size before and after.
// Restorable: while the row still holds NewValue's location it is put
// back to OldValue's. Written by the fragment-consolidation repair, which
// repoints a parent book's stale rows at the files an old scan imported
// (and organize moved) as separate chapter books.
const ChangeTypeBookFileRepoint = "book_file_repoint_location"

// SameFileLocation reports whether a row's location matches want where a
// repoint compare-and-set looks: the path and the Missing flag. Hash and size
// are not compared: a hash backfill or rescan fills them in on a row without
// moving it, and that must not block the repoint's revert.
func SameFileLocation(a, b BookFileLocation) bool {
	return a.Path == b.Path && a.Missing == b.Missing
}

// BookFileLocation is the part of a book_file row a ChangeTypeBookFileRepoint
// changes, recorded whole on both sides so the revert restores every field
// the repoint wrote and checks every one of them first.
//
// KeyOwnerBook/KeyOwnerRow are set on the repoint's NewValue only: the row
// that held the single-owner book_file_path key at Path before the repoint
// took it. The revert hands the key back to exactly that row. They are not
// part of the location: BookFileLocationOf never sets them and Apply ignores them.
type BookFileLocation struct {
	Path    string `json:"path"`
	Missing bool   `json:"missing"`
	Hash    string `json:"hash,omitempty"`
	Size    int64  `json:"size,omitempty"`

	KeyOwnerBook string `json:"key_owner_book,omitempty"`
	KeyOwnerRow  string `json:"key_owner_row,omitempty"`
}

// BookFileLocationOf reads f's BookFileLocation.
func BookFileLocationOf(f *BookFile) BookFileLocation {
	return BookFileLocation{Path: f.FilePath, Missing: f.Missing, Hash: f.FileHash, Size: f.FileSize}
}

// Apply writes l onto f.
func (l BookFileLocation) Apply(f *BookFile) {
	f.FilePath, f.Missing, f.FileHash, f.FileSize = l.Path, l.Missing, l.Hash, l.Size
}

// Encode is l as the JSON a change row holds.
func (l BookFileLocation) Encode() string {
	b, err := json.Marshal(l)
	if err != nil { // strings, a bool and an int cannot fail to marshal
		return ""
	}
	return string(b)
}

// DecodeBookFileLocation parses a ChangeTypeBookFileRepoint value. A value
// with no path is refused: restoring it would blank the row.
func DecodeBookFileLocation(v string) (BookFileLocation, error) {
	var l BookFileLocation
	if err := json.Unmarshal([]byte(v), &l); err != nil {
		return l, fmt.Errorf("book_file location %q: %w", v, err)
	}
	if l.Path == "" {
		return l, fmt.Errorf("book_file location %q has no path", v)
	}
	return l, nil
}
