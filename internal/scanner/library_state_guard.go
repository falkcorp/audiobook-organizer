// file: internal/scanner/library_state_guard.go
// version: 1.0.0
// guid: 146e0696-abbc-4b4c-a3fd-889a292ad957
// last-edited: 2026-10-30

package scanner

import "github.com/falkcorp/audiobook-organizer/internal/database"

// scannerMayChangeLibraryState reports whether a scan may write dst's
// LibraryState at all. It is the one test every scan-derived state write
// consults, so a new write site has an obvious function to call.
//
// A row in the trash (database.IsInTrash: the deletion flag is set, OR a trash
// path labelled it "deleted") is not the scan's to relabel. The label alone is
// the trash for a row the batch library_state:"deleted" path made, with no
// flag to fall back on, so a scan-derived "suspicious" written over it takes
// the book out of the trash with no restore, no library.organize permission and
// no audit of a restore. For a flag-trashed row the flag keeps it hidden, but
// the overlay still destroys the label a restore reads its state back from.
//
// The scan still refreshes everything else about such a row (path, hash, size,
// tags): only the state is held, because the state is the one column a scan
// does not own for a row someone deliberately removed.
func scannerMayChangeLibraryState(dst *database.Book) bool {
	return !database.IsInTrash(dst)
}

// scanDerivedLibraryState is the state the scan wants dst to carry, or nil when
// it has none to impose. A state the scan genuinely derived (today only
// "suspicious") wins; the bare "imported" creation default never overwrites a
// state already on the row (see applyScannerFields), but a row with no state
// yet still gets it.
func scanDerivedLibraryState(dst, scanned *database.Book) *string {
	if scanned.LibraryState == nil {
		return nil
	}
	if dst.LibraryState != nil && *scanned.LibraryState == libraryStateImported {
		return nil
	}
	return scanned.LibraryState
}

// applyScannerLibraryState is applyScannerFields' LibraryState overlay: the
// scan-derived state is written unless scannerMayChangeLibraryState refuses.
func applyScannerLibraryState(dst, scanned *database.Book) {
	want := scanDerivedLibraryState(dst, scanned)
	if want == nil || !scannerMayChangeLibraryState(dst) {
		return
	}
	dst.LibraryState = want
}

// libraryStateHeldForTrash reports whether the overlay of scanned onto dst
// would have changed dst's LibraryState had dst not been in the trash: the
// books the end-of-run summary counts as "left alone because they are trashed".
// A scan with nothing to impose, or one that would have written the value the
// row already carries, withheld nothing.
func libraryStateHeldForTrash(dst, scanned *database.Book) bool {
	if scannerMayChangeLibraryState(dst) {
		return false
	}
	want := scanDerivedLibraryState(dst, scanned)
	if want == nil {
		return false
	}
	return dst.LibraryState == nil || *dst.LibraryState != *want
}
