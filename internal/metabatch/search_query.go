// file: internal/metabatch/search_query.go
// version: 1.6.0
// guid: e0ed5705-b771-4cc2-9c8c-bca9f78ead8b
// last-edited: 2026-09-29
//
// Resolves the title a metadata search asks providers for a book.

package metabatch

import (
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Sources of a candidate search title, recorded on CandidateResult.SearchQuerySource
// and in the op log so a match can be traced to the text that produced it.
const (
	SearchQuerySourceTitle               = "title"
	SearchQuerySourceTranscribedTitle    = "transcribed_title"
	SearchQuerySourceFileTranscribedText = "file_transcribed_title"
	// SearchQuerySourceFolderTitle is the name of the folder holding the
	// book's files (metadata.ChapterTitleFromDirectory), used when neither
	// the title nor any transcription names the book.
	SearchQuerySourceFolderTitle = "folder_title"
)

// IsTranscribedSource reports whether source names a title heard in the
// book's audio (book- or file-level transcription), as opposed to one read
// from the book row or its path.
func IsTranscribedSource(source string) bool {
	return source == SearchQuerySourceTranscribedTitle || source == SearchQuerySourceFileTranscribedText
}

// SkipReasonNoUsableTitle is the skip reason for a book that has no title
// worth searching: its own title is unsearchable (metadata.IsUnsearchableTitle)
// and neither a transcription nor its folder offers one.
const SkipReasonNoUsableTitle = "no usable title"

// SkipDetailNoUsableTitle spells SkipReasonNoUsableTitle out for a per-book
// log line: what was tried, so the reader knows the skip is not a bug.
const SkipDetailNoUsableTitle = SkipReasonNoUsableTitle +
	" (title is empty, a placeholder or a chapter number, and no transcribed title or folder name offers one)"

// CandidateSearchQuery is the title a metadata search asks providers for a
// book and where it came from. Usable is false when the book has no title
// worth searching; the caller must then skip the book rather than search.
//
// Title is ONLY a search query. No caller writes it onto the book: a
// fallback title that finds the right record is confirmed by that record,
// and one that finds the wrong record must not have already renamed the book.
type CandidateSearchQuery struct {
	Title  string
	Source string
	// Author is the author heard in the same transcription as Title (the
	// book's or the first file's TranscribedAuthor), "" for any other
	// source. The certainty gate requires a transcribed-title candidate to
	// match it too (util.MainTranscriptionConfirms), so "X, by A" never
	// vouches for a candidate "X" by B.
	Author string
	Usable bool
}

// ResolveCandidateSearchQuery picks the title a metadata search asks
// providers for book.
//
// The book's own title wins unless it is unsearchable
// (metadata.IsUnsearchableTitle): empty, a system placeholder ("Unknown
// Title", "read by narrator"), a bare chapter number ("Chapter 3", "03") or a
// chapter fragment ("06 Chapter 6"). Searching a catalog for any of those
// returns whatever the provider ranks first: on 2026-09-27 two books titled
// "" "matched" Audible's "Bad in Bed" while their intro transcription said
// "Marvel's Planet Hulk". A section heading in words or a front-matter name
// ("Book Two", "Prologue") is unsearchable only when the book's files
// corroborate it (titleJudge): alone it is some real book's title.
//
// For such a book the fallbacks are tried in order, each itself refused when
// unsearchable:
//
//  1. the book-level transcribed title (the audio intro);
//  2. the transcribed title of the book's FIRST present file (lowest disc,
//     then track; sortByPosition) -- that file only. A later file's
//     transcription is mid-book narration or another work's intro on a
//     mis-merged book, not the book's title, so there is no fall-through;
//  3. the folder holding the book's files (metadata.ChapterTitleFromDirectory:
//     "Eldest/98.mp3" -> "Eldest", one disc/part folder skipped), from the
//     first present file, then from the book's own path.
//
// If none is usable the result is not Usable and the book must be skipped
// with SkipReasonNoUsableTitle -- never searched.
//
// A GetBookFiles error is treated as "no file rows": the file-level steps are
// skipped and only the book's own path is tried, which is the safe direction.
func ResolveCandidateSearchQuery(files SearchQueryReader, book *database.Book) CandidateSearchQuery {
	if book == nil {
		return CandidateSearchQuery{}
	}
	j := &titleJudge{files: files, book: book, bookID: book.ID, bookPath: book.FilePath}
	if !j.unsearchable(book.Title) {
		return CandidateSearchQuery{Title: book.Title, Source: SearchQuerySourceTitle, Usable: true}
	}
	if t := j.usableTitle(book.TranscribedTitle); t != "" {
		return CandidateSearchQuery{Title: t, Source: SearchQuerySourceTranscribedTitle,
			Author: trimmed(book.TranscribedAuthor), Usable: true}
	}
	present := j.presentFiles()
	if len(present) > 0 {
		if t := j.usableTitle(present[0].TranscribedTitle); t != "" {
			return CandidateSearchQuery{Title: t, Source: SearchQuerySourceFileTranscribedText,
				Author: trimmed(present[0].TranscribedAuthor), Usable: true}
		}
	}
	if t := j.folderTitle(book.FilePath); t != "" {
		return CandidateSearchQuery{Title: t, Source: SearchQuerySourceFolderTitle, Usable: true}
	}
	return CandidateSearchQuery{}
}

// titleJudge decides whether a title is worth searching for one book. Most
// of that is metadata.IsUnsearchableTitle, which needs nothing but the text.
// A section heading in words or a roman numeral, or a front-matter name
// ("Book Two", "Prologue"; metadata.IsSectionHeadingTitle), is also some real
// book's title, so it is refused only when the book's files corroborate that
// it names a part (headingCorroborated). The files are read at most once,
// and only when a title needs them or a file-level fallback is reached.
type titleJudge struct {
	files    SearchQueryReader
	book     *database.Book
	bookID   string
	bookPath string
	loaded   bool
	present  []database.BookFile
	// authorsLoaded/authors: the book's live author names plus its snapshot
	// Book.Author, read once and only when a heading reaches the folder test.
	authorsLoaded bool
	authorsErr    error
	authors       []string
}

// SearchQueryReader is what ResolveCandidateSearchQuery reads: the book's
// files (fallback titles, heading corroboration) and its authors (a folder
// named for the author is not a work folder).
type SearchQueryReader interface {
	BookFilesGetter
	database.BookAuthorReader
}

// presentFiles returns the book's present files in play order
// (sortByPosition). A GetBookFiles error is treated as "no file rows": the
// file-level steps are skipped and a heading is not corroborated, so it is
// searched as-is -- a real title is never refused on a read fault.
func (j *titleJudge) presentFiles() []database.BookFile {
	if j.loaded {
		return j.present
	}
	j.loaded = true
	if j.files == nil {
		return nil
	}
	present, err := presentFilesInPlayOrder(j.files, j.bookID)
	if err != nil {
		return nil
	}
	j.present = present
	return j.present
}

// presentFilesInPlayOrder reads bookID's book_file rows and returns the
// present ones in play order (sortByPosition).
func presentFilesInPlayOrder(files BookFilesGetter, bookID string) ([]database.BookFile, error) {
	bookFiles, err := files.GetBookFiles(bookID)
	if err != nil {
		return nil, err
	}
	var present []database.BookFile
	for i := range bookFiles {
		if !bookFiles[i].Missing {
			present = append(present, bookFiles[i])
		}
	}
	sortByPosition(present)
	return present, nil
}

// FirstPresentFilePath returns the path of bookID's first present file in
// play order (lowest disc, then track; the file ResolveCandidateSearchQuery
// takes a file-level transcription and a folder title from), or "" when the
// book has none. The certainty gate reads the book's work folder from it
// (applygate.TranscribedSearch.FirstFilePath): a multi-file book's own
// FilePath is a directory.
func FirstPresentFilePath(files BookFilesGetter, bookID string) (string, error) {
	if files == nil {
		return "", nil
	}
	present, err := presentFilesInPlayOrder(files, bookID)
	if err != nil || len(present) == 0 {
		return "", err
	}
	return present[0].FilePath, nil
}

// unsearchable reports whether t is no title to search this book by.
func (j *titleJudge) unsearchable(t string) bool {
	if metadata.IsUnsearchableTitle(t) {
		return true
	}
	if !metadata.IsSectionHeadingTitle(t) {
		return false
	}
	authors, err := j.bookAuthors()
	if err != nil {
		// Unknown authors: the folder might be the author's, so it is no
		// evidence -- a real title is never refused on a read fault.
		return false
	}
	return headingCorroborated(j.presentFiles(), j.bookPath, t, authors)
}

// bookAuthors returns the book's live author names (database.LiveBookAuthorNames)
// plus its snapshot Book.Author name, read at most once.
func (j *titleJudge) bookAuthors() ([]string, error) {
	if j.authorsLoaded {
		return j.authors, j.authorsErr
	}
	j.authorsLoaded = true
	if j.files == nil || j.book == nil {
		return nil, nil
	}
	live, err := database.LiveBookAuthorNames(j.files, j.book)
	if err != nil {
		j.authorsErr = err
		return nil, err
	}
	j.authors = live
	if j.book.Author != nil && strings.TrimSpace(j.book.Author.Name) != "" {
		j.authors = append(j.authors, j.book.Author.Name)
	}
	return j.authors, nil
}

// headingCorroborated reports whether a book's files say a section-heading
// title names a part of something rather than a whole book. Two facts count:
//
//   - a present file's own Title tag is the title: a chapter file's tag
//     promoted to the book ("Chapter One" on a book whose files are tagged
//     "Chapter One", "Book Two");
//   - the book's work folder (metadata.WorkFolderTitle, from the first present
//     file, else the book's own path) names something else: "Prologue" in
//     ".../Paolini/Eldest/" is Eldest's prologue.
//
// The number of files is NOT evidence: a multi-file "Act One" or "Forward"
// in a folder of that name is a real book in chapters. A work folder that
// names nothing (a placeholder or chapter folder, metadata.IsUnsearchableTitle)
// is no evidence either way, and neither is a folder named for one of the
// book's authors: ".../Anne Roiphe/Epilogue.m4b" is a book called Epilogue
// filed under its author, whenever the author folder's own parent is not one
// of the generic roots WorkFolderTitle already skips.
func headingCorroborated(present []database.BookFile, bookPath, title string, authors []string) bool {
	t := normTitle(title)
	for _, f := range present {
		if normTitle(f.Title) == t {
			return true
		}
	}
	path := bookPath
	if len(present) > 0 {
		path = present[0].FilePath
	}
	folder, ok := metadata.WorkFolderTitle(path)
	if !ok || metadata.IsUnsearchableTitle(folder) {
		return false
	}
	nf := normTitle(folder)
	for _, a := range authors {
		if normTitle(a) == nf {
			return false
		}
	}
	return nf != t
}

// normTitle lowercases a title and collapses its whitespace.
func normTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// sortByPosition orders a book's files as they play: disc, then track, then
// path. A file with no disc or track number sorts as 0 (first), where a
// single-file book's only file belongs. Stable, so equal positions keep the
// store's order.
func sortByPosition(files []database.BookFile) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if a.DiscNumber != b.DiscNumber {
			return a.DiscNumber < b.DiscNumber
		}
		if a.TrackNumber != b.TrackNumber {
			return a.TrackNumber < b.TrackNumber
		}
		return a.FilePath < b.FilePath
	})
}

// trimmed dereferences p and trims it, "" for nil.
func trimmed(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// usableTitle returns the trimmed title, or "" when it is absent or itself
// unsearchable for this book (titleJudge.unsearchable: a lone "Prologue"
// transcription on a single-file book is still a usable stand-in).
func (j *titleJudge) usableTitle(p *string) string {
	if p == nil {
		return ""
	}
	t := strings.TrimSpace(*p)
	if j.unsearchable(t) {
		return ""
	}
	return t
}

// folderTitle returns the work folder's name for a book with no title of its
// own, or "": metadata.WorkFolderTitle of the first present file's path, then
// of the book's own path (a directory for a multi-file book). The organizer
// files such books under "Unknown Title" folders, so the answer is run back
// through titleJudge.unsearchable.
func (j *titleJudge) folderTitle(bookPath string) string {
	var paths []string
	if present := j.presentFiles(); len(present) > 0 {
		paths = append(paths, present[0].FilePath)
	}
	paths = append(paths, bookPath)
	for _, p := range paths {
		if t, ok := metadata.WorkFolderTitle(p); ok && !j.unsearchable(t) {
			return t
		}
	}
	return ""
}
