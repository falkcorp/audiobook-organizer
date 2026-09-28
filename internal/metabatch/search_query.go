// file: internal/metabatch/search_query.go
// version: 1.0.0
// guid: e0ed5705-b771-4cc2-9c8c-bca9f78ead8b
// last-edited: 2026-09-27
//
// Resolves the title the batch candidate fetch searches a book by.

package metabatch

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// Sources of a candidate search title, recorded on CandidateResult.SearchQuerySource
// and in the op log so a match can be traced to the text that produced it.
const (
	SearchQuerySourceTitle               = "title"
	SearchQuerySourceTranscribedTitle    = "transcribed_title"
	SearchQuerySourceFileTranscribedText = "file_transcribed_title"
)

// SkipReasonNoUsableTitle is the skip reason for a book that has neither a
// real title nor a transcribed one.
const SkipReasonNoUsableTitle = "no usable title"

// CandidateSearchQuery is the title a batch candidate fetch searches a book by
// and where it came from. Usable is false when the book has no title worth
// searching; the fetch must then skip the book rather than search.
type CandidateSearchQuery struct {
	Title  string
	Source string
	Usable bool
}

// ResolveCandidateSearchQuery picks the title the candidate fetch searches
// book by.
//
// The book's own title wins unless it is empty or one of the system's
// placeholders (organizer.IsPlaceholderTitle: "", "Unknown Title",
// "Unknown Author", "Narrator", ...). Searching a catalog for an empty or
// placeholder title returns whatever the provider ranks first for nothing:
// on 2026-09-27 two books titled "" "matched" Audible's "Bad in Bed" while
// their intro transcription said "Marvel's Planet Hulk".
//
// For such a book the intro transcription's parsed title is used instead:
// the book-level TranscribedTitle first, then the first book_file carrying
// one (per-file transcription fills the file rows, not always the book). If
// neither exists the result is not Usable and the book must be skipped with
// SkipReasonNoUsableTitle — never searched.
//
// A GetBookFiles error is treated as "no file transcription": the result is
// then a skip, which is the safe direction (no search, no junk match).
func ResolveCandidateSearchQuery(files BookFilesGetter, book *database.Book) CandidateSearchQuery {
	if book == nil {
		return CandidateSearchQuery{}
	}
	if !organizer.IsPlaceholderTitle(book.Title) {
		return CandidateSearchQuery{Title: book.Title, Source: SearchQuerySourceTitle, Usable: true}
	}
	if t := usableTranscribedTitle(book.TranscribedTitle); t != "" {
		return CandidateSearchQuery{Title: t, Source: SearchQuerySourceTranscribedTitle, Usable: true}
	}
	if files != nil {
		if bookFiles, err := files.GetBookFiles(book.ID); err == nil {
			for i := range bookFiles {
				if bookFiles[i].Missing {
					continue
				}
				if t := usableTranscribedTitle(bookFiles[i].TranscribedTitle); t != "" {
					return CandidateSearchQuery{Title: t, Source: SearchQuerySourceFileTranscribedText, Usable: true}
				}
			}
		}
	}
	return CandidateSearchQuery{}
}

// usableTranscribedTitle returns the trimmed transcribed title, or "" when it
// is absent or itself a placeholder.
func usableTranscribedTitle(p *string) string {
	if p == nil {
		return ""
	}
	t := strings.TrimSpace(*p)
	if organizer.IsPlaceholderTitle(t) {
		return ""
	}
	return t
}
