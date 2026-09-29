// file: internal/metabatch/search_query.go
// version: 1.4.0
// guid: e0ed5705-b771-4cc2-9c8c-bca9f78ead8b
// last-edited: 2026-09-28
//
// Resolves the title a metadata search asks providers for a book.

package metabatch

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"

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
func ResolveCandidateSearchQuery(files BookFilesGetter, book *database.Book) CandidateSearchQuery {
	if book == nil {
		return CandidateSearchQuery{}
	}
	j := &titleJudge{files: files, bookID: book.ID}
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
	files   BookFilesGetter
	bookID  string
	loaded  bool
	present []database.BookFile
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
	bookFiles, err := j.files.GetBookFiles(j.bookID)
	if err != nil {
		return nil
	}
	for i := range bookFiles {
		if !bookFiles[i].Missing {
			j.present = append(j.present, bookFiles[i])
		}
	}
	sortByPosition(j.present)
	return j.present
}

// unsearchable reports whether t is no title to search this book by.
func (j *titleJudge) unsearchable(t string) bool {
	if metadata.IsUnsearchableTitle(t) {
		return true
	}
	return metadata.IsSectionHeadingTitle(t) && headingCorroborated(j.presentFiles(), t)
}

// headingCorroborated reports whether a book's files say a section-heading
// title names a part of something rather than a whole book: the book has
// more than one present file, or the title is a file's own tag title on a
// numbered track (a chapter file's tag promoted to the book).
func headingCorroborated(present []database.BookFile, title string) bool {
	if len(present) > 1 {
		return true
	}
	t := strings.TrimSpace(title)
	for _, f := range present {
		if f.TrackNumber > 0 && strings.EqualFold(strings.TrimSpace(f.Title), t) {
			return true
		}
	}
	return false
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
// own, or "". It asks metadata.ChapterTitleFromDirectory with an empty title
// (so every path qualifies) for the first present file's path, then for the
// book's own path. A directory FilePath (a multi-file book) is given a
// stand-in child so the folder asked about is the book's folder itself, not
// its parent. The organizer files such books under "Unknown Title" folders,
// so the answer is run back through titleJudge.unsearchable.
func (j *titleJudge) folderTitle(bookPath string) string {
	var paths []string
	if present := j.presentFiles(); len(present) > 0 {
		paths = append(paths, present[0].FilePath)
	}
	if p := strings.TrimSpace(bookPath); p != "" {
		if !isFileExt(filepath.Ext(p)) {
			p = filepath.Join(p, "_")
		}
		paths = append(paths, p)
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if t, _, ok := metadata.ChapterTitleFromDirectory(p, ""); ok {
			if t = strings.TrimSpace(t); !j.unsearchable(t) {
				return t
			}
		}
	}
	return ""
}

// isFileExt tells a real extension (".m4b", ".mp3") from a folder name that
// merely contains a dot ("Book 1.5" -> ".5").
func isFileExt(ext string) bool {
	if len(ext) < 2 || len(ext) > 5 {
		return false
	}
	return strings.IndexFunc(ext[1:], unicode.IsLetter) >= 0
}
