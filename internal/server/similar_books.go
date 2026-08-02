// file: internal/server/similar_books.go
// version: 1.2.0
// guid: 3a1b2c0d-4e5f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-05-01
//
// "Similar books" lookup (backlog 5.2). Given a book, searches Bleve
// for other books by the same author or in the same series — quick
// discovery feature for the BookDetail page.

package server

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/gin-gonic/gin"
)

// handleSimilarBooks returns books similar to the given book.
// GET /api/v1/audiobooks/:id/similar
func (s *Server) handleSimilarBooks(c *gin.Context) {
	bookID := c.Param("id")
	book, err := s.Store().GetBookByID(bookID)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "book", "")
		return
	}

	idx := s.SearchIndex()
	if idx == nil {
		httputil.RespondWithServiceUnavailable(c, "search index not available")
		return
	}

	// Build a query from the book's author and series.
	var queryParts []string
	if book.AuthorID != nil {
		author, _ := s.Store().GetAuthorByID(*book.AuthorID)
		if author != nil && author.Name != "" {
			queryParts = append(queryParts, "author:"+search.QuoteIfNeeded(author.Name))
		}
	}
	if book.SeriesID != nil {
		series, _ := s.Store().GetSeriesByID(*book.SeriesID)
		if series != nil && series.Name != "" {
			queryParts = append(queryParts, "series:"+search.QuoteIfNeeded(series.Name))
		}
	}

	if len(queryParts) == 0 {
		httputil.RespondWithOK(c, struct {
			Books []database.Book `json:"books"`
			Count int             `json:"count"`
		}{Books: []database.Book{}, Count: 0})
		return
	}

	query := strings.Join(queryParts, " || ")
	ast, err := search.ParseQuery(query)
	if err != nil {
		httputil.RespondWithOK(c, struct {
			Books []database.Book `json:"books"`
			Count int             `json:"count"`
		}{Books: []database.Book{}, Count: 0})
		return
	}
	bleveQ, _, err := search.Translate(ast)
	if err != nil {
		httputil.RespondWithOK(c, struct {
			Books []database.Book `json:"books"`
			Count int             `json:"count"`
		}{Books: []database.Book{}, Count: 0})
		return
	}

	hits, _, err := idx.SearchNative(bleveQ, 0, 20)
	if err != nil {
		httputil.RespondWithOK(c, struct {
			Books []database.Book `json:"books"`
			Count int             `json:"count"`
		}{Books: []database.Book{}, Count: 0})
		return
	}

	var similar []database.Book
	for _, h := range hits {
		if h.BookID == bookID {
			continue
		}
		b, _ := s.Store().GetBookByID(h.BookID)
		if b != nil {
			similar = append(similar, *b)
		}
		if len(similar) >= 10 {
			break
		}
	}
	if similar == nil {
		similar = []database.Book{}
	}

	httputil.RespondWithOK(c, struct {
		Books []database.Book `json:"books"`
		Count int             `json:"count"`
	}{Books: similar, Count: len(similar)})
}
