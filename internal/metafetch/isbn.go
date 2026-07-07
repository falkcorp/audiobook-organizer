// file: internal/metafetch/isbn.go
// version: 1.5.0
// guid: 34290bd0-745e-4509-ad2d-e237785bb7ef
// last-edited: 2026-07-07

package metafetch

import (
	"context"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// ISBNService searches external metadata sources for ISBN (and ASIN)
// when a book is missing those identifiers after a metadata fetch/apply.
type ISBNService struct {
	db      database.Store
	sources []metadata.MetadataSource
}

// NewISBNService creates an enrichment service that will search the
// given metadata sources for ISBN/ASIN data.
func NewISBNService(db database.Store, sources []metadata.MetadataSource) *ISBNService {
	return &ISBNService{db: db, sources: sources}
}

// EnrichBookISBN searches external sources for ISBN if the book doesn't have one.
// It also back-fills ASIN from Audible when missing. Returns true if any
// identifier was found and saved.
func (s *ISBNService) EnrichBookISBN(ctx context.Context, bookID string) (bool, error) {
	book, err := s.db.GetBookByID(bookID)
	if err != nil || book == nil {
		return false, err
	}

	hasISBN := (book.ISBN10 != nil && *book.ISBN10 != "") || (book.ISBN13 != nil && *book.ISBN13 != "")
	hasASIN := book.ASIN != nil && *book.ASIN != ""

	// Nothing to do if both are already present.
	if hasISBN && hasASIN {
		return false, nil
	}

	// Build search query from book title + author.
	title := book.Title
	author := s.resolveAuthor(book)

	updated := false

	// --- ISBN enrichment ---
	if !hasISBN {
		for _, src := range s.sources {
			isbn, isbnLen := s.searchSourceForISBN(src, title, author)
			if isbn == "" {
				continue
			}
			if isbnLen == 13 {
				book.ISBN13 = &isbn
			} else {
				book.ISBN10 = &isbn
			}
			if _, err := s.db.UpdateBook(bookID, book); err != nil {
				return false, err
			}
						logging.Info(ctx, "ISBN enrichment found", "isbn", isbn, "title", title, "source", src.Name())
			updated = true
			break
		}
	}

	// --- ASIN enrichment ---
	if !hasASIN {
		for _, src := range s.sources {
			if src.Name() != "Audible" {
				continue
			}
			asin := s.searchSourceForASIN(src, title, author)
			if asin == "" {
				break
			}
			book.ASIN = &asin
			if _, err := s.db.UpdateBook(bookID, book); err != nil {
				return updated, err
			}
						logging.Info(ctx, "ASIN enrichment found", "asin", asin, "title", title)
			updated = true
			break
		}
	}

	return updated, nil
}

// EnrichMissingISBNs scans books missing ISBN data and enriches up to limit of them.
// It returns the number of candidate books checked and the number updated.
// w and opID are optional — if provided, each enriched book is submitted to the
// activity batcher instead of emitting a per-book log line.
func (s *ISBNService) EnrichMissingISBNs(ctx context.Context, limit int, w *activity.Writer, opID string) (int, int, error) {
	if limit <= 0 {
		limit = 100
	}

	const pageSize = 250
	checked := 0
	updated := 0

	for offset := 0; checked < limit; offset += pageSize {
		if ctx != nil && ctx.Err() != nil {
			return checked, updated, ctx.Err()
		}

		books, err := s.db.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return checked, updated, err
		}
		if len(books) == 0 {
			break
		}

		for i := range books {
			if ctx != nil && ctx.Err() != nil {
				return checked, updated, ctx.Err()
			}
			if !needsIdentifierEnrichment(&books[i]) {
				continue
			}

			checked++
			found, err := s.EnrichBookISBN(ctx, books[i].ID)
			if err != nil {
								logging.Warn(ctx, "ISBN enrichment failed for during batch scan", "id", books[i].ID, "error", err)
			} else if found {
				updated++
				activity.LogBatch(w, opID, "isbn-enrich", "isbn-enrichment",
					activity.BatchItem{Name: books[i].Title, Detail: books[i].ID})
			}
			if checked >= limit {
				break
			}
		}

		if len(books) < pageSize {
			break
		}
	}

	return checked, updated, nil
}

// resolveAuthor returns the author name for the book, or "" if unknown.
func (s *ISBNService) resolveAuthor(book *database.Book) string {
	if book.AuthorID == nil {
		return ""
	}
	a, err := s.db.GetAuthorByID(*book.AuthorID)
	if err != nil || a == nil {
		return ""
	}
	return a.Name
}

func needsIdentifierEnrichment(book *database.BookCore) bool {
	if book == nil {
		return false
	}
	return (book.ISBN10 == nil || strings.TrimSpace(*book.ISBN10) == "") &&
		(book.ISBN13 == nil || strings.TrimSpace(*book.ISBN13) == "")
}

// searchSourceForISBN queries a single metadata source and returns the first
// ISBN that matches the title strictly, along with its length (10 or 13).
func (s *ISBNService) searchSourceForISBN(src metadata.MetadataSource, title, author string) (string, int) {
	var results []metadata.BookMetadata
	ctx := context.Background()

	if author != "" {
		results, _ = src.SearchByTitleAndAuthor(ctx, title, author)
	}
	if len(results) == 0 {
		results, _ = src.SearchByTitle(ctx, title)
	}

	for _, r := range results {
		if !IsStrictTitleMatch(title, r.Title) {
			continue
		}
		if r.ISBN != "" {
			return r.ISBN, len(r.ISBN)
		}
	}
	return "", 0
}

// searchSourceForASIN queries a single metadata source and returns the first
// ASIN that matches the title strictly.
func (s *ISBNService) searchSourceForASIN(src metadata.MetadataSource, title, author string) string {
	var results []metadata.BookMetadata
	ctx := context.Background()

	if author != "" {
		results, _ = src.SearchByTitleAndAuthor(ctx, title, author)
	}
	if len(results) == 0 {
		results, _ = src.SearchByTitle(ctx, title)
	}

	for _, r := range results {
		if IsStrictTitleMatch(title, r.Title) && r.ASIN != "" {
			return r.ASIN
		}
	}
	return ""
}

// isStrictTitleMatch returns true if titles are close enough to be the same book.
func IsStrictTitleMatch(dbTitle, searchTitle string) bool {
	a := strings.ToLower(strings.TrimSpace(dbTitle))
	b := strings.ToLower(strings.TrimSpace(searchTitle))
	if a == "" || b == "" {
		return false
	}
	// Exact match
	if a == b {
		return true
	}
	// One is a prefix of the other (e.g., "Shadows of Self" matches "Shadows of Self: A Mistborn Novel")
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		shorterText := a
		longerText := b
		if len(shorterText) > len(longerText) {
			shorterText, longerText = longerText, shorterText
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(longerText, shorterText))
		if remainder == "" {
			return true
		}
		if strings.HasPrefix(remainder, ":") ||
			strings.HasPrefix(remainder, "-") ||
			strings.HasPrefix(remainder, "—") {
			return true
		}

		// But only if the shorter one is at least 60% of the longer one's length
		shorter := len(a)
		longer := len(b)
		if shorter > longer {
			shorter, longer = longer, shorter
		}
		return float64(shorter)/float64(longer) >= 0.6
	}
	return false
}
