// file: internal/books/roots/ids.go
// version: 0.1.0
// guid: 785073d3-72ca-4551-a7db-894f6ff6b052
// last-edited: 2026-11-20

package roots

import (
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/library"
)

// BooksLibraryRoot is the ID of the root HEAD's root_dir became.
const BooksLibraryRoot library.RootID = "books-library"

// importPrefix is the root ID prefix of an import root: import-<n>.
const importPrefix = "import-"

// RootIDFor is the one pure function between HEAD's import-path ID n and the
// root ID import-<n> (05-library 5L.10, "IDs are kept"), so no row and no
// client that names n is rewritten.
func RootIDFor(n int) library.RootID { return library.RootID(importPrefix + strconv.Itoa(n)) }

// ImportPathID is RootIDFor's inverse: n and true for an ID RootIDFor
// produced, 0 and false for any other (books-library, a download root, "import-",
// "import-007", "import--1").
func ImportPathID(id library.RootID) (int, bool) {
	rest, ok := strings.CutPrefix(string(id), importPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

// NextImportN is the n of the next import root: one above every n in ids,
// the IDs of the registered roots and of the rootscan rows. A new root takes
// "n above every migrated one" (05-library 5L.10); counting the rows as well as
// the roots keeps a root that was removed with its row left behind from handing
// its scan state to a new root.
func NextImportN(ids ...library.RootID) int {
	next := 1
	for _, id := range ids {
		if n, ok := ImportPathID(id); ok && n >= next {
			next = n + 1
		}
	}
	return next
}
