// file: internal/database/book_writer_change_types.go
// version: 1.0.0
// guid: f31bc24e-9547-4911-97ca-f0edb0980fbc
// last-edited: 2026-10-30

package database

// The operation change types the v3 ops.Writer
// (internal/operations/opswriter) records for a book_file field edit and a
// credit-list edit. Declared here, beside ChangeTypeBookFileRepoint, for the
// same reason: the Writer writes them and the revert engine (internal/undo,
// internal/audiobooks) reverses them, and neither may import the other.

// ChangeTypeBookFileModify is one book_file field a Writer changed. FieldName
// is "file.<json field>:<file id>" (the field's JSON name in BookFile);
// OldValue and NewValue are that field's JSON before and after, or "" when
// the field was omitted from the row's JSON on that side (a zero value
// with omitempty or omitzero). Restorable: the field goes back to OldValue
// while it still holds NewValue.
const ChangeTypeBookFileModify = "book_file_modify"

// ChangeTypeBookAuthorsModify is a book's whole credit list before and after
// a Writer's ModifyBookAuthors, FieldName "authors". OldValue is the list as
// the store held it ([]BookAuthor JSON, "null" for none); NewValue is the list
// the callback returned, which the store normalised on write (positions
// 0..n-1, one row per author and role). Restorable: the list goes back to
// OldValue while it still equals the normalised NewValue.
const ChangeTypeBookAuthorsModify = "book_authors_modify"
