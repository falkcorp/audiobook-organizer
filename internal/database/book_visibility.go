// file: internal/database/book_visibility.go
// version: 1.1.0
// guid: 4eee927b-72ce-4b07-aa41-a91afb2368ba
// last-edited: 2026-08-13

package database

// bookIsSoftDeleted reports whether a book row is in the trash.
//
// This exists so the rule lives in exactly ONE place. Store methods with two
// backing implementations (a Pebble keyspace scan and a memdb index walk) must
// agree on which rows a normal library scan can see, and the way they drifted
// apart was that each open-coded the same three-token nil-check independently:
// the Pebble path applied it unconditionally, the memdb path applied it only
// when a caller opted in, and nothing in the type system or the tests noticed
// the two had stopped meaning the same thing.
//
// Call this instead of writing `b.MarkedForDeletion != nil && *b...` inline.
// A soft-deleted book is restorable (POST /api/v1/audiobooks/:id/restore), so
// "deleted" here means "hidden from library scans", NOT "gone" — code that
// reasons about a book's FILES (see findOrphanBookFiles) must keep treating
// these rows as live owners of their book_files, or restore has nothing to
// restore.
//
// See TestGetAllBooksCore_MemDBAndPebbleAgree for the conformance test that
// holds the two implementations to this shared definition.
func bookIsSoftDeleted(b *Book) bool {
	return b != nil && markedForDeletionFlag(b.MarkedForDeletion)
}

// bookCoreIsSoftDeleted is the BookCore twin of bookIsSoftDeleted.
//
// Book and BookCore are separate structs that each carry their own copy of the
// trash bit, and scans split between them (GetAllBooksCore returns BookCore,
// GetAllBooks returns Book), so the predicate needs both shapes. Both delegate
// to markedForDeletionFlag rather than restating the nil-check, which is the
// whole point: adding a third row type must not add a third copy of the rule.
func bookCoreIsSoftDeleted(b *BookCore) bool {
	return b != nil && markedForDeletionFlag(b.MarkedForDeletion)
}

// markedForDeletionFlag is the rule itself, stated once: the trash bit is a
// *bool where nil means "never deleted", so an unset pointer is live. Every
// visibility check in this package bottoms out here.
func markedForDeletionFlag(flag *bool) bool {
	return flag != nil && *flag
}
