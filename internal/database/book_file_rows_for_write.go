// file: internal/database/book_file_rows_for_write.go
// version: 1.0.0
// guid: 8d21f6a9-0c47-4e3b-b5a2-4f9e7d130c68
// last-edited: 2026-10-24

package database

// BookFileRowsAtPathForWrite answers "which rows claim this path?" for a caller
// that is about to put a row on the path (create one, repoint one, move one)
// and so must not read a retired version's file as free.
//
// The complete lookup (BookFilesAtPath, memdb) is the only one that shows a
// retired row sitting beside a live one, but it refuses for the ~130 s of memdb
// warmup after every restart and for the rest of the process after a warmup that
// lost book_file rows. A write path that required it unconditionally would stop
// working whenever memdb is down, to guard against rows that do not exist:
//
//   - While the store can prove NO retired version exists (NoRetiredVersionsKnown:
//     one Pebble seek, no memdb), no retired row can be hiding, so the rows are
//     BookFileRowsAtPath's: complete when memdb serves, the single-owner path
//     index (one row per path CRC) when it does not. That is exactly the lookup
//     these callers used before retired rows existed.
//   - Once any retired version exists, or when the store cannot say, the lookup
//     is BookFileRowsAtPathStrict: a lookup that cannot be complete is an error
//     wrapping ErrOwnershipLookupIncomplete, never a short list, and the caller
//     must stop (fail closed) rather than treat it as "no row".
//
// Pass the result to RetiredClaimOnPath for the claim rule in iface_retired.go.
func BookFileRowsAtPathForWrite(s BookFilePathLookup, path string) ([]BookFile, error) {
	if path == "" {
		return nil, nil
	}
	none, err := NoRetiredVersionsKnown(s)
	if err != nil {
		return nil, err
	}
	if none {
		return BookFileRowsAtPath(s, path)
	}
	return BookFileRowsAtPathStrict(s, path)
}
