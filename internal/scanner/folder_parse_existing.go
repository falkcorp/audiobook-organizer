// file: internal/scanner/folder_parse_existing.go
// version: 1.0.0
// guid: cc77dad2-5d35-4407-8724-35eb66226c45
// last-edited: 2026-10-05
//
// Keeps the folder parse from rewriting existing books on a rescan.

package scanner

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// folderParsed is what AssembleBookMetadata took from the folder parse for
// one book: the title, primary author and series it returned whose source is
// "folder.*", "" for a field another source (a tag, the file name) supplied.
type folderParsed struct {
	Title, Author, Series string
}

// folderParsedFrom records bm's folder-sourced identity fields.
func folderParsedFrom(bm *metadata.AssembledMetadata) folderParsed {
	var f folderParsed
	if bm == nil {
		return f
	}
	if strings.HasPrefix(bm.TitleSource, "folder.") {
		f.Title = bm.Title
	}
	if strings.HasPrefix(bm.AuthorSource, "folder.") {
		f.Author = bm.PrimaryAuthor()
	}
	if strings.HasPrefix(bm.SeriesSource, "folder.") {
		f.Series = bm.SeriesName
	}
	return f
}

// folderDerivedLocks returns locked plus the lock keys of every identity
// field the book's scanned value came from the folder parse for -- still
// holding that value, so a field a later step re-derived (a tag, the AI
// parse) is not held. The rescan overlay (applyScannerFields) skips locked
// keys, so an existing row keeps its stored title, author and series.
//
// Owner decision 2026-10-05 ("search + new imports only"): the folder parse
// moved to metadata.ParseBookName, and a rescan must not rewrite existing
// rows with it. Holding the folder-derived columns was chosen over keeping
// the old parse alive for existing rows: a second parser is exactly what
// ParseBookName replaced, and holding is the stricter guarantee -- an
// existing row's identity changes on a rescan only through its tags, as
// before, or through the reviewed fixer maintenance.reparse-folder-names.
// It differs from main only where main would have overwritten a stored
// title, author or series with a folder-parse value; new rows are created
// from the new parse unchanged.
func folderDerivedLocks(locked map[string]bool, book *Book) map[string]bool {
	if book == nil {
		return locked
	}
	f := book.folderParse
	hold := map[string]bool{}
	if f.Title != "" && book.Title == f.Title {
		hold[database.FieldKeyTitle] = true
	}
	if f.Author != "" && book.Author == f.Author {
		hold[database.FieldKeyAuthorName] = true
	}
	if f.Series != "" && book.Series == f.Series {
		hold[database.FieldKeySeriesName] = true
		hold[database.FieldKeySeriesPosition] = true
	}
	if len(hold) == 0 {
		return locked
	}
	out := make(map[string]bool, len(locked)+len(hold))
	for k, v := range locked {
		out[k] = v
	}
	for k := range hold {
		out[k] = true
	}
	return out
}
