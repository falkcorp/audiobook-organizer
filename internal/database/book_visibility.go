// file: internal/database/book_visibility.go
// version: 1.3.0
// guid: 4eee927b-72ce-4b07-aa41-a91afb2368ba
// last-edited: 2026-08-14

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

// IsSoftDeleted is the exported form of bookIsSoftDeleted, for callers outside
// this package.
//
// It exists because there was no exported predicate at all, so every package
// that needed the answer open-coded `b.MarkedForDeletion != nil && *b...` —
// 25 copies across 17 files in dedup, itunes, organizer, undo, maintenance and
// the handlers. #2392 collapsed the 37 copies INSIDE this package onto one rule
// and left those untouched, which is only half the job: an unexported rule
// cannot stop anyone else from restating it, and restating it is precisely how
// the two GetAllBooksCore implementations drifted apart.
//
// All 25 now call this. The standing check, which should return only
// internal/scanner's nil-vs-set merge comparison:
//
//	grep -rn "MarkedForDeletion != nil" --include='*.go' internal/ cmd/ \
//	  | grep -v '^internal/database/' | grep -v _test | grep -v mocks
func (b *Book) IsSoftDeleted() bool {
	return bookIsSoftDeleted(b)
}

// IsSoftDeleted is the BookCore twin of Book.IsSoftDeleted, for the same reason
// bookCoreIsSoftDeleted is the twin of bookIsSoftDeleted: the two row types each
// carry their own copy of the trash bit and callers hold whichever one their
// scan returned.
//
// It exists because the compiler asked for it. Converting the 25 open-coded
// copies outside this package, one of them — internal/dedup's split-book
// detector — turned out to hold a *BookCore rather than a *Book, and the build
// said so. That is the whole argument for a shared predicate over a grep-and-
// replace: the sites that are genuinely different announce themselves instead
// of being quietly rewritten into something that still compiles.
func (b *BookCore) IsSoftDeleted() bool {
	return bookCoreIsSoftDeleted(b)
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

// includeByDeletionState is the tri-state deletion POLICY, stated once, as
// opposed to bookIsSoftDeleted which is the per-row predicate. deleted is the
// row's state; want is the caller's request:
//
//	nil    → no opinion; exclude the trash (the default library view)
//	&false → explicitly require live rows
//	&true  → explicitly require trashed rows
//
// The first two rows of that table are the same state, which is why the whole
// thing is one comparison rather than the three-branch if/else it replaces:
// "no filter" and "require live" both mean "include iff not deleted". Reusing
// markedForDeletionFlag on the FILTER pointer is not a pun — the filter and the
// row's flag are the same *bool shape under the same nil-means-false rule, so
// one function correctly reads both.
//
// This exists because #2392 unified the predicate but left the policy written
// out twice — once in getAllBookSummariesFiltered as an
// excludeDeleted/requireDeleted pair, once in GetAllBooksCore as a
// key-present check plus an inequality. Both were correct at the time. So were
// the two GetAllBooksCore implementations, right up until they weren't, and by
// the same mechanism: one rule, spelled out in two places, drifting because
// nothing forced them to agree.
func includeByDeletionState(deleted bool, want *bool) bool {
	return deleted == markedForDeletionFlag(want)
}

// deletionStateFromFilters decodes GetAllBooksCore's untyped filter map into
// the *bool tri-state includeByDeletionState takes.
//
// A key present with a NON-bool value (a string "true", say) is treated as
// absent — fail closed to the default library view. The code this replaces
// keyed the default exclusion off key PRESENCE and the equality off a
// successful `.(bool)` assertion, two different conditions, so a non-bool
// value satisfied the first and failed the second and the row was returned
// whatever its state: a caller who fumbled the type got the trash mixed into
// live results, silently. That is a narrower instance of the exact leak
// #2392 fixed, and collapsing presence and type into one decode is what
// removes it. Getting live books when you asked wrongly for deleted ones is
// a visible wrong answer; leaking deleted rows into a full-library op is an
// invisible one.
func deletionStateFromFilters(filters map[string]interface{}) *bool {
	v, ok := filters["marked_for_deletion"].(bool)
	if !ok {
		return nil
	}
	return &v
}
