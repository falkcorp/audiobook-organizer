// file: internal/books/store/junction_bookid.go
// version: 1.1.2
// guid: 073892d4-fddf-47bf-a5aa-be1665175519
// last-edited: 2026-11-09

package store

// The book_authors and book_narrators junctions are stored one Pebble key per
// book: book_authors:<bookID> / book_narrators:<bookID>, value = a JSON array of
// rows. Each row ALSO carries a book_id field, and the two are not allowed to
// disagree: memdb's primary index on both tables is a non-AllowMissing compound
// {BookID, AuthorID|NarratorID}, so a row whose BookID is empty is rejected
// ("object missing primary index") and one whose BookID names another book is
// indexed under the wrong book.
//
// Until 2026-09-12 nothing enforced it. SetBookAuthors / SetBookNarrators wrote
// caller rows to Pebble verbatim, and two callers omitted book_id: the
// narrator-split in POST /operations/optimize-database, and PUT
// /audiobooks/:id/narrators, which binds client JSON straight through. The
// Pebble write succeeded, the setter returned nil -- and from then on every
// UpsertBookToMemDB for that book reloaded the row from Pebble, failed the
// insert, and aborted the WHOLE transaction (book row, authors, narrators,
// files), so memdb's copy of the book went stale on every later update. Warmup
// dropped the row too, flagging the table incomplete for the process lifetime.
//
// The key is the source of truth: it is what GetBookAuthors(bookID) looks up.
// So the store stamps the key's book ID onto every row -- on write (the
// setters), on read (the getters, so a row damaged before this fix cannot
// poison a memdb reload) and at warmup. (Migration 63 once repaired the rows
// durably, once; it ran on every store at the upgrade floor, and R3 deleted it.)

// stampBookAuthorsInPlace forces every row's BookID to bookID. It returns how
// many rows carried a NON-EMPTY BookID naming a different book -- the case a
// setter logs, because an empty one is just an omitted field while a different
// one means the caller built rows for another book and asked to store them
// here.
func stampBookAuthorsInPlace(bookID string, rows []BookAuthor) (mismatched int) {
	for i := range rows {
		if rows[i].BookID == bookID {
			continue
		}
		if rows[i].BookID != "" {
			mismatched++
		}
		rows[i].BookID = bookID
	}
	return mismatched
}

// stampBookNarratorsInPlace is the narrator twin of stampBookAuthorsInPlace.
func stampBookNarratorsInPlace(bookID string, rows []BookNarrator) (mismatched int) {
	for i := range rows {
		if rows[i].BookID == bookID {
			continue
		}
		if rows[i].BookID != "" {
			mismatched++
		}
		rows[i].BookID = bookID
	}
	return mismatched
}
