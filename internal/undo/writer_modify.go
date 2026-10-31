// file: internal/undo/writer_modify.go
// version: 1.0.0
// guid: 21f48e3d-4986-407f-9fd4-0f6a666384bd
// last-edited: 2026-10-30

package undo

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The two record-only change types the v3 ops.Writer
// (internal/operations/opswriter) writes, and the compare-and-set checks the
// revert (audiobooks.RevertService) and the preflight share for them:
//
//   - database.ChangeTypeBookFileModify: one book_file field, FieldName
//     "file.<json field>:<file id>", OldValue and NewValue that field's JSON
//     before and after ("" when the side omitted the field: a zero value
//     with omitempty or omitzero).
//   - database.ChangeTypeBookAuthorsModify: a book's whole credit list,
//     FieldName "authors".
//
// Each check answers as the checks in current.go do: nil (the target still
// holds what the Writer wrote: revert it), ErrAlreadyRestored (it already
// holds the old value), or a ReasonChangedSince refusal.

// bookFileFieldPrefix starts the FieldName of a ChangeTypeBookFileModify row.
const bookFileFieldPrefix = "file."

// BookFileModify is the decoded form of a ChangeTypeBookFileModify row: one
// book_file field, by its JSON name, from Old to New. An empty side means the
// field was omitted from the row's JSON (a zero value).
type BookFileModify struct {
	Key, FileID string
	Old, New    string
}

// BookFileModifyFromField splits "file.<json field>:<file id>". JSON field
// names never contain a colon, so the first one ends the key.
func BookFileModifyFromField(field string) (key, fileID string, ok bool) {
	rest, ok := strings.CutPrefix(field, bookFileFieldPrefix)
	if !ok {
		return "", "", false
	}
	key, fileID, ok = strings.Cut(rest, ":")
	return key, fileID, ok && key != "" && fileID != ""
}

// bookFileFieldIndex is the index of the BookFile struct field whose JSON
// name is key, or -1. The identity fields (id, book_id) are never a valid
// target: the store refuses a write that changes them, and a row cannot have
// recorded such a change.
func bookFileFieldIndex(key string) int {
	if key == "id" || key == "book_id" {
		return -1
	}
	t := reflect.TypeFor[database.BookFile]()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		if name, _, _ := strings.Cut(sf.Tag.Get("json"), ","); name == key {
			return i
		}
	}
	return -1
}

// DecodeBookFileModify parses a ChangeTypeBookFileModify row: the field name
// must name a BookFile field, and each non-empty side must be JSON that the
// field decodes.
func DecodeBookFileModify(c *database.OperationChange) (BookFileModify, error) {
	key, fileID, ok := BookFileModifyFromField(c.FieldName)
	if !ok {
		return BookFileModify{}, fmt.Errorf("no book_file field in %q", c.FieldName)
	}
	idx := bookFileFieldIndex(key)
	if idx < 0 {
		return BookFileModify{}, fmt.Errorf("%q is not a revertable book_file field", key)
	}
	m := BookFileModify{Key: key, FileID: fileID, Old: c.OldValue, New: c.NewValue}
	ft := reflect.TypeFor[database.BookFile]().Field(idx).Type
	for _, side := range []string{m.Old, m.New} {
		if side == "" {
			continue
		}
		if err := json.Unmarshal([]byte(side), reflect.New(ft).Interface()); err != nil {
			return BookFileModify{}, fmt.Errorf("book_file field %q value %q: %w", key, side, err)
		}
	}
	return m, nil
}

// bookFileFieldJSON is the JSON of f's field key as the Writer recorded it
// (the field's entry in json.Marshal(f)), or "" when the field is omitted.
func bookFileFieldJSON(f *database.BookFile, key string) (string, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	return string(m[key]), nil
}

// CheckBookFileModifyCurrent compares f's field with the row: still the New
// the Writer wrote (nil: revert it), already the Old (ErrAlreadyRestored: a
// journaled write that never landed, or one something put back), or anything
// else (ReasonChangedSince: a later write of that field is left in place). New
// is checked first, so a row whose Old equals its New is reverted as a no-op.
func CheckBookFileModifyCurrent(f *database.BookFile, m BookFileModify) error {
	cur, err := bookFileFieldJSON(f, m.Key)
	if err != nil {
		return refuse(ReasonFieldUnreadable, "read field %q of book_file %s: %v", m.Key, f.ID, err)
	}
	switch cur {
	case m.New:
		return nil
	case m.Old:
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book_file %s field %q is now %s, not what the operation wrote (%s)",
		f.ID, m.Key, valueOrAbsent(cur), valueOrAbsent(m.New))
}

// ApplyBookFileModifyOld sets f's field back to m.Old; an empty Old zeroes it.
// Only that one struct field is written, so nothing else on the row (hidden
// fields included) is touched.
func ApplyBookFileModifyOld(f *database.BookFile, m BookFileModify) error {
	idx := bookFileFieldIndex(m.Key)
	if idx < 0 {
		return fmt.Errorf("%q is not a revertable book_file field", m.Key)
	}
	dst := reflect.ValueOf(f).Elem().Field(idx)
	v := reflect.New(dst.Type())
	if m.Old != "" {
		if err := json.Unmarshal([]byte(m.Old), v.Interface()); err != nil {
			return fmt.Errorf("book_file field %q value %q: %w", m.Key, m.Old, err)
		}
	}
	dst.Set(v.Elem())
	return nil
}

func valueOrAbsent(s string) string {
	if s == "" {
		return "(absent)"
	}
	return s
}

// BookAuthorsModify is the decoded form of a ChangeTypeBookAuthorsModify row.
type BookAuthorsModify struct {
	Before, After []database.BookAuthor
}

// DecodeBookAuthorsModify parses a ChangeTypeBookAuthorsModify row. "null" is
// an empty list (the Writer marshals a nil slice); an empty string is not JSON.
func DecodeBookAuthorsModify(c *database.OperationChange) (BookAuthorsModify, error) {
	var m BookAuthorsModify
	if err := json.Unmarshal([]byte(c.OldValue), &m.Before); err != nil {
		return BookAuthorsModify{}, fmt.Errorf("credits before: %w", err)
	}
	if err := json.Unmarshal([]byte(c.NewValue), &m.After); err != nil {
		return BookAuthorsModify{}, fmt.Errorf("credits after: %w", err)
	}
	return m, nil
}

// WrittenAfter is the list the store holds after the Writer's write: the
// callback's result normalised as the store normalises it on write
// (database.NormalizeBookAuthors, with the book's primary author breaking
// position ties). The journaled After is the callback's result, not what the
// store kept, so it is compared in this form.
func (m BookAuthorsModify) WrittenAfter(primary *int) []database.BookAuthor {
	return database.NormalizeBookAuthors(slices.Clone(m.After), primary)
}

// CheckBookAuthorsModifyCurrent compares a book's current credits with the row:
// still what the Writer wrote (nil: revert it), already the list before it
// (ErrAlreadyRestored), or anything else (ReasonChangedSince). primary is the
// book's AuthorID, which orders credits that share a position.
func CheckBookAuthorsModifyCurrent(bookID string, cur []database.BookAuthor, primary *int, m BookAuthorsModify) error {
	switch {
	case SameJunction(cur, m.WrittenAfter(primary)):
		return nil
	case SameJunction(cur, database.NormalizeBookAuthors(slices.Clone(m.Before), primary)):
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book %s credits changed since the operation wrote them", bookID)
}

// BookAuthorsModifyReturned lists the authors the revert hands a credit back
// to: those of m.Before that the written list no longer carries. Each must
// still be a live author row (CheckAuthorLive), as an author-merge revert
// requires, or the restored credit would dangle.
func BookAuthorsModifyReturned(m BookAuthorsModify) []int {
	var ids []int
	for _, b := range m.Before {
		if b.AuthorID <= 0 || slices.Contains(ids, b.AuthorID) {
			continue
		}
		if !slices.ContainsFunc(m.After, func(a database.BookAuthor) bool { return a.AuthorID == b.AuthorID }) {
			ids = append(ids, b.AuthorID)
		}
	}
	return ids
}
