// file: internal/scanner/folder_parse_existing.go
// version: 1.1.0
// guid: cc77dad2-5d35-4407-8724-35eb66226c45
// last-edited: 2026-10-06
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
	Title, Author, Series, Narrator string
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
	if strings.HasPrefix(bm.NarratorSource, "folder.") {
		f.Narrator = bm.Narrator
	}
	return f
}

// folderHold is what holdFolderFieldsForExisting kept for an existing row:
// the row's own author, series and work, used in place of resolving (and
// creating) rows from the folder parse's values.
type folderHold struct {
	author, series, work bool
	authorID, seriesID   *int
	workID               *string
}

// holdFolderFieldsForExisting replaces, on book, every identity value that
// came from the folder parse with the stored row's value when the book's path
// already has a row: title (and with it the work and the series position,
// which DetectVolumeNumber/IdentifySeries read off the title), author,
// series (and position) and narrator. saveBookToDatabase calls it before it
// resolves or creates any author, series or work row, so a rescan of an
// existing book creates none from the new parse and writes nothing it would
// change. A field that came from a tag is left alone, as is a book with no
// row yet (a new import takes the folder parse). A lookup error holds
// nothing: the merge-time hold (folderDerivedLocks) still applies.
func holdFolderFieldsForExisting(book *Book) folderHold {
	var h folderHold
	f := book.folderParse
	store := getStore()
	if f == (folderParsed{}) || store == nil {
		return h
	}
	existing, err := store.GetBookByFilePath(book.FilePath)
	if err != nil || existing == nil {
		return h
	}
	holdPosition := false
	if f.Title != "" && book.Title == f.Title {
		book.Title = existing.Title
		h.work, h.workID = true, existing.WorkID
		holdPosition = true
	}
	if f.Author != "" && book.Author == f.Author {
		h.author, h.authorID = true, existing.AuthorID
		book.Author = ""
		if existing.AuthorID != nil {
			if a, aerr := store.GetAuthorByID(*existing.AuthorID); aerr == nil && a != nil {
				book.Author = a.Name
			}
		}
	}
	if f.Series != "" && book.Series == f.Series {
		h.series, h.seriesID = true, existing.SeriesID
		book.Series = ""
		holdPosition = true
	}
	if f.Narrator != "" && book.Narrator == f.Narrator {
		book.Narrator = ""
		if existing.Narrator != nil {
			book.Narrator = *existing.Narrator
		}
	}
	if holdPosition {
		book.Position = 0
		if existing.SeriesSequence != nil {
			book.Position = *existing.SeriesSequence
		}
	}
	return h
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
	if f.Narrator != "" && book.Narrator == f.Narrator {
		hold[database.FieldKeyNarrator] = true
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
