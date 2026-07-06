// file: internal/server/handlers/entities/handler.go
// version: 1.2.0
// guid: b02a07d8-1806-4c86-bb72-f0688d6caff3
// last-edited: 2026-07-05

// Package entities hosts the entity-domain HTTP handlers extracted from the
// server package: works, authors, series, and narrators — CRUD plus merges,
// splits, reclassification, and listing. Business logic that depended on the
// *Server receiver is reached through narrow interfaces (EntitiesStore,
// WorkService, AuthorSeriesService, OperationsRegistry) and an injected
// enrichBooks function, so package entities never imports package server.

package entities

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	ulid "github.com/oklog/ulid/v2"
)

// authorMergeOpParams holds the parameters for the entities.author-merge op. It
// mirrors the server-package type of the same shape; EnqueueOp json.Marshals
// params immediately, so the server-side Run func unmarshals by these json
// tags. The tags MUST stay identical to the server-package definition.
type authorMergeOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
	KeepID     int    `json:"keep_id"`
	MergeIDs   []int  `json:"merge_ids"`
	KeepName   string `json:"keep_name"`
}

// resolveProductionAuthorOpParams holds the parameters for the
// entities.resolve-production-author op. Mirrors the server-package type.
type resolveProductionAuthorOpParams struct {
	LegacyOpID     string `json:"legacy_op_id"`
	AuthorID       int    `json:"author_id"`
	ProdAuthorName string `json:"prod_author_name"`
}

// Handler hosts the entities-domain HTTP endpoints.
type Handler struct {
	store               EntitiesStore
	workService         WorkService
	authorSeriesService AuthorSeriesService
	registry            OperationsRegistry

	// Concrete caches (spec cache exception): the exact T matches the
	// server-package field types.
	authorsCache *cache.Cache[*audiobooks.AuthorWithCountListResponse]
	seriesCache  *cache.Cache[*audiobooks.SeriesWithCountsResponse]
	dedupCache   *cache.Cache[gin.H]

	// enrichBooks resolves author/series/narrator names for a list of books and
	// returns JSON-ready values (one per input book, in order). It wraps the
	// server-private batchFetchBookAuthorsAndNarrators + enrichBookForResponse
	// pair, whose return type (enrichedBookResponse) is server-private. The
	// controller (wire_handlers.go, package server) implements it.
	enrichBooks func(books []database.Book) []any
}

// New constructs an entities Handler from its dependencies.
func New(
	store EntitiesStore,
	workService WorkService,
	authorSeriesService AuthorSeriesService,
	registry OperationsRegistry,
	authorsCache *cache.Cache[*audiobooks.AuthorWithCountListResponse],
	seriesCache *cache.Cache[*audiobooks.SeriesWithCountsResponse],
	dedupCache *cache.Cache[gin.H],
	enrichBooks func(books []database.Book) []any,
) *Handler {
	return &Handler{
		store:               store,
		workService:         workService,
		authorSeriesService: authorSeriesService,
		registry:            registry,
		authorsCache:        authorsCache,
		seriesCache:         seriesCache,
		dedupCache:          dedupCache,
		enrichBooks:         enrichBooks,
	}
}

// --- Works ---

// ListWorks implements GET /works.
func (h *Handler) ListWorks(c *gin.Context) {
	resp, err := h.workService.ListWorks()
	if err != nil {
		httputil.InternalError(c, "failed to list works", err)
		return
	}
	httputil.RespondWithOK(c, resp)
}

// CreateWork implements POST /works.
func (h *Handler) CreateWork(c *gin.Context) {
	var work database.Work
	if err := c.ShouldBindJSON(&work); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	created, err := h.workService.CreateWork(&work)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	httputil.RespondWithCreated(c, created)
}

// GetWork implements GET /works/:id.
func (h *Handler) GetWork(c *gin.Context) {
	id := c.Param("id")
	work, err := h.workService.GetWork(id)
	if err != nil {
		httputil.RespondWithNotFound(c, "work", id)
		return
	}
	httputil.RespondWithOK(c, work)
}

// UpdateWork implements PUT /works/:id.
func (h *Handler) UpdateWork(c *gin.Context) {
	id := c.Param("id")
	var work database.Work
	if err := c.ShouldBindJSON(&work); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if strings.TrimSpace(work.Title) == "" {
		httputil.RespondWithBadRequest(c, "title is required")
		return
	}
	updated, err := h.workService.UpdateWork(id, &work)
	if err != nil {
		if err.Error() == "work not found" {
			httputil.RespondWithNotFound(c, "work", id)
			return
		}
		httputil.InternalError(c, "failed to update work", err)
		return
	}
	httputil.RespondWithOK(c, updated)
}

// DeleteWork implements DELETE /works/:id.
func (h *Handler) DeleteWork(c *gin.Context) {
	id := c.Param("id")
	if err := h.workService.DeleteWork(id); err != nil {
		if err.Error() == "work not found" {
			httputil.RespondWithNotFound(c, "work", id)
			return
		}
		httputil.InternalError(c, "failed to delete work", err)
		return
	}
	httputil.RespondWithNoContent(c)
}

// ListWorkBooks implements GET /works/:id/books.
func (h *Handler) ListWorkBooks(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	id := c.Param("id")
	books, err := h.store.GetBooksByWorkID(id)
	if err != nil {
		httputil.InternalError(c, "failed to list work books", err)
		return
	}
	if books == nil {
		books = []database.Book{}
	}
	httputil.RespondWithOK(c, gin.H{"items": books, "count": len(books)})
}

// ListWork returns work items (audiobooks grouped by work entity), paginated.
// Uses GetAllWorkBookCounts to compute book counts in a single pass instead of
// per-work GetBooksByWorkID calls (N+1 on a 50K-work corpus). Books for each
// work in the page are still fetched individually so callers that need the
// books slice continue to work; pagination bounds the per-request cost.
// Implements GET /work.
func (h *Handler) ListWork(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	params := httputil.ParsePaginationParams(c)

	works, err := h.store.GetAllWorks()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to retrieve works")
		return
	}

	counts, err := h.store.GetAllWorkBookCounts()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to retrieve work book counts")
		return
	}

	total := len(works)
	start := params.Offset
	if start > total {
		start = total
	}
	end := start + params.Limit
	if end > total {
		end = total
	}
	page := works[start:end]

	items := make([]map[string]any, 0, len(page))
	for _, work := range page {
		books, err := h.store.GetBooksByWorkID(work.ID)
		if err != nil {
			books = []database.Book{}
		}

		items = append(items, map[string]any{
			"id":         work.ID,
			"title":      work.Title,
			"author_id":  work.AuthorID,
			"book_count": counts[work.ID],
			"books":      books,
		})
	}

	httputil.RespondWithOK(c, gin.H{
		"items":  items,
		"total":  total,
		"limit":  params.Limit,
		"offset": params.Offset,
	})
}

// GetWorkStats returns statistics about work items. Uses GetAllWorkBookCounts
// to compute per-work counts in a single store call instead of N+1
// GetBooksByWorkID lookups. Implements GET /work/stats.
func (h *Handler) GetWorkStats(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	works, err := h.store.GetAllWorks()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to retrieve works")
		return
	}

	counts, err := h.store.GetAllWorkBookCounts()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to retrieve work book counts")
		return
	}

	totalWorks := len(works)
	totalBooks := 0
	worksWithMultipleEditions := 0

	for _, work := range works {
		bookCount := counts[work.ID]
		totalBooks += bookCount
		if bookCount > 1 {
			worksWithMultipleEditions++
		}
	}

	httputil.RespondWithOK(c, gin.H{
		"total_works":                  totalWorks,
		"total_books":                  totalBooks,
		"works_with_multiple_editions": worksWithMultipleEditions,
		"average_editions_per_work":    float64(totalBooks) / float64(max(totalWorks, 1)),
	})
}

// --- Authors ---

// ListAuthors implements GET /authors.
func (h *Handler) ListAuthors(c *gin.Context) {
	if cached, ok := h.authorsCache.Get("all"); ok {
		httputil.RespondWithOK(c, cached)
		return
	}
	resp, err := h.authorSeriesService.ListAuthorsWithCounts()
	if err != nil {
		httputil.InternalError(c, "failed to list authors", err)
		return
	}
	h.authorsCache.Set("all", resp)
	httputil.RespondWithOK(c, resp)
}

// CountAuthors implements GET /authors/count.
func (h *Handler) CountAuthors(c *gin.Context) {
	count, err := h.store.CountAuthors()
	if err != nil {
		httputil.InternalError(c, "failed to count authors", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"count": count})
}

// RenameAuthor implements PUT /authors/:id/name.
func (h *Handler) RenameAuthor(c *gin.Context) {
	authorID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid author ID")
		return
	}

	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		httputil.RespondWithBadRequest(c, "name must not be empty")
		return
	}

	if err := h.store.UpdateAuthorName(authorID, name); err != nil {
		httputil.InternalError(c, "failed to rename author", err)
		return
	}

	h.dedupCache.Invalidate("author-duplicates")
	h.authorsCache.InvalidateAll()
	httputil.RespondWithOK(c, gin.H{"id": authorID, "name": name})
}

// SplitCompositeAuthor splits an author like "Author1 / Author2" or "Author1, Author2"
// into individual author records, relinking all books to each new author.
// Implements POST /authors/:id/split.
func (h *Handler) SplitCompositeAuthor(c *gin.Context) {
	authorID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid author ID")
		return
	}

	author, err := h.store.GetAuthorByID(authorID)
	if err != nil || author == nil {
		httputil.RespondWithNotFound(c, "author", "")
		return
	}

	// Optional: caller can provide explicit names to split into
	var req struct {
		Names []string `json:"names"`
	}
	_ = c.ShouldBindJSON(&req)

	// If no explicit names, auto-detect split
	names := req.Names
	if len(names) == 0 {
		names = dedup.SplitCompositeAuthorName(author.Name)
	}
	if len(names) <= 1 {
		httputil.RespondWithBadRequest(c, "author name does not appear to be composite")
		return
	}

	// Create or find each individual author
	var newAuthors []database.Author
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		existing, err := h.store.GetAuthorByName(name)
		if err == nil && existing != nil {
			newAuthors = append(newAuthors, *existing)
			continue
		}
		created, err := h.store.CreateAuthor(name)
		if err != nil {
			httputil.InternalError(c, "failed to create author", err)
			return
		}
		newAuthors = append(newAuthors, *created)
	}

	// Get all books linked to the composite author
	books, err := h.store.GetBooksByAuthorIDWithRoleCore(authorID)
	if err != nil {
		httputil.InternalError(c, "failed to get author books", err)
		return
	}

	booksUpdated := 0
	for _, book := range books {
		bookAuthors, err := h.store.GetBookAuthors(book.ID)
		if err != nil {
			continue
		}

		// Find the role/position of the composite author entry
		role := "author"
		for _, ba := range bookAuthors {
			if ba.AuthorID == authorID {
				role = ba.Role
				break
			}
		}

		// Remove composite author, add individual authors
		var updated []database.BookAuthor
		for _, ba := range bookAuthors {
			if ba.AuthorID != authorID {
				updated = append(updated, ba)
			}
		}
		for i, na := range newAuthors {
			// Check not already linked
			alreadyLinked := false
			for _, ba := range updated {
				if ba.AuthorID == na.ID {
					alreadyLinked = true
					break
				}
			}
			if !alreadyLinked {
				updated = append(updated, database.BookAuthor{
					BookID:   book.ID,
					AuthorID: na.ID,
					Role:     role,
					Position: len(updated) + i,
				})
			}
		}
		if err := h.store.SetBookAuthors(book.ID, updated); err != nil {
			continue
		}
		booksUpdated++
	}

	// Delete the composite author
	if err := h.store.DeleteAuthor(authorID); err != nil {
		httputil.InternalError(c, "failed to delete author", err)
		return
	}

	result := make([]gin.H, len(newAuthors))
	for i, a := range newAuthors {
		result[i] = gin.H{"id": a.ID, "name": a.Name}
	}

	h.dedupCache.Invalidate("author-duplicates")
	h.authorsCache.InvalidateAll()
	httputil.RespondWithOK(c, gin.H{"authors": result, "books_updated": booksUpdated})
}

// MergeAuthors implements POST /authors/merge.
func (h *Handler) MergeAuthors(c *gin.Context) {
	var req struct {
		KeepID   int   `json:"keep_id" binding:"required"`
		MergeIDs []int `json:"merge_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.MergeIDs) == 0 {
		httputil.RespondWithBadRequest(c, "merge_ids must not be empty")
		return
	}

	keepAuthor, err := h.store.GetAuthorByID(req.KeepID)
	if err != nil || keepAuthor == nil {
		httputil.RespondWithNotFound(c, "author", "")
		return
	}

	opID := ulid.Make().String()
	detail := fmt.Sprintf("merge-authors:keep=%d,merge=%v", req.KeepID, req.MergeIDs)
	op, err := h.store.CreateOperation(opID, "author-merge", &detail)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	params := authorMergeOpParams{
		LegacyOpID: op.ID,
		KeepID:     req.KeepID,
		MergeIDs:   req.MergeIDs,
		KeepName:   keepAuthor.Name,
	}
	if _, enqErr := h.registry.EnqueueOp(c.Request.Context(), "entities.author-merge", params); enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}
	httputil.RespondWithSuccess(c, 202, op)
}

// DeleteAuthor implements DELETE /authors/:id.
func (h *Handler) DeleteAuthor(c *gin.Context) {
	authorID, err := strconv.Atoi(c.Param("id"))
	if err != nil || authorID <= 0 {
		httputil.RespondWithBadRequest(c, "invalid author ID")
		return
	}
	books, err := h.store.GetBooksByAuthorIDCore(authorID)
	if err != nil {
		httputil.InternalError(c, "failed to get author books", err)
		return
	}
	if len(books) > 0 {
		httputil.RespondWithConflict(c, "cannot delete author with books")
		return
	}
	if err := h.store.DeleteAuthor(authorID); err != nil {
		httputil.InternalError(c, "failed to delete author", err)
		return
	}
	h.authorsCache.InvalidateAll()
	httputil.RespondWithOK(c, gin.H{"message": "author deleted"})
}

// BulkDeleteAuthors deletes multiple zero-book authors at once.
// Implements POST /authors/bulk-delete.
func (h *Handler) BulkDeleteAuthors(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	deleted := 0
	skipped := 0
	var errors []string
	for _, id := range req.IDs {
		books, err := h.store.GetBooksByAuthorIDCore(id)
		if err != nil {
			errors = append(errors, fmt.Sprintf("author %d: %v", id, err))
			continue
		}
		if len(books) > 0 {
			skipped++
			continue
		}
		if err := h.store.DeleteAuthor(id); err != nil {
			errors = append(errors, fmt.Sprintf("author %d: %v", id, err))
			continue
		}
		deleted++
	}
	if deleted > 0 {
		h.authorsCache.InvalidateAll()
	}
	httputil.RespondWithOK(c, gin.H{
		"deleted": deleted,
		"skipped": skipped,
		"errors":  errors,
		"total":   len(req.IDs),
	})
}

// GetAuthorBooks implements GET /authors/:id/books.
func (h *Handler) GetAuthorBooks(c *gin.Context) {
	authorID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid author ID")
		return
	}
	booksCore, err := h.store.GetBooksByAuthorIDCore(authorID)
	if err != nil {
		httputil.InternalError(c, "failed to get author books", err)
		return
	}
	// h.enrichBooks (injected from package server, see wire_handlers.go) is
	// shared with full-fidelity Book sources elsewhere, so its signature
	// stays []database.Book — convert back via BookCore.ToBook() rather than
	// widening that shared response-building code (out of scope for
	// STOREFID P3-W2). The heavy fields it would enrich with were already
	// absent from this SLIM getter before this change; no behavior change.
	books := make([]database.Book, len(booksCore))
	for i := range booksCore {
		books[i] = booksCore[i].ToBook()
	}

	enriched := h.enrichBooks(books)
	httputil.RespondWithOK(c, gin.H{"items": enriched, "count": len(enriched)})
}

// GetAuthorAliases implements GET /authors/:id/aliases.
func (h *Handler) GetAuthorAliases(c *gin.Context) {
	authorID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid author ID")
		return
	}
	aliases, err := h.store.GetAuthorAliases(authorID)
	if err != nil {
		httputil.InternalError(c, "failed to get author aliases", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"aliases": aliases})
}

// CreateAuthorAlias implements POST /authors/:id/aliases.
func (h *Handler) CreateAuthorAlias(c *gin.Context) {
	authorID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid author ID")
		return
	}
	var req struct {
		AliasName string `json:"alias_name"`
		AliasType string `json:"alias_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if req.AliasName == "" {
		httputil.RespondWithBadRequest(c, "alias_name is required")
		return
	}
	if req.AliasType == "" {
		req.AliasType = "alias"
	}
	alias, err := h.store.CreateAuthorAlias(authorID, req.AliasName, req.AliasType)
	if err != nil {
		httputil.InternalError(c, "failed to create author alias", err)
		return
	}
	h.authorsCache.InvalidateAll()
	httputil.RespondWithCreated(c, alias)
}

// DeleteAuthorAlias implements DELETE /authors/:id/aliases/:aliasId.
func (h *Handler) DeleteAuthorAlias(c *gin.Context) {
	aliasID, err := strconv.Atoi(c.Param("aliasId"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid alias ID")
		return
	}
	if err := h.store.DeleteAuthorAlias(aliasID); err != nil {
		httputil.InternalError(c, "failed to delete author alias", err)
		return
	}
	h.authorsCache.InvalidateAll()
	httputil.RespondWithOK(c, gin.H{"status": "deleted"})
}

// ReclassifyAuthorAsNarrator implements POST /authors/:id/reclassify-as-narrator.
func (h *Handler) ReclassifyAuthorAsNarrator(c *gin.Context) {
	authorID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid author ID")
		return
	}

	author, err := h.store.GetAuthorByID(authorID)
	if err != nil || author == nil {
		httputil.RespondWithNotFound(c, "author", "")
		return
	}

	// Create or find narrator with same name
	narrator, err := h.store.GetNarratorByName(author.Name)
	if err != nil || narrator == nil {
		narrator, err = h.store.CreateNarrator(author.Name)
		if err != nil {
			httputil.InternalError(c, "failed to create narrator", err)
			return
		}
	}

	// Get all books linked to this author
	books, err := h.store.GetBooksByAuthorIDWithRoleCore(authorID)
	if err != nil {
		httputil.InternalError(c, "failed to get author books", err)
		return
	}

	booksUpdated := 0
	for _, book := range books {
		// Remove author link
		bookAuthors, err := h.store.GetBookAuthors(book.ID)
		if err != nil {
			continue
		}
		var newAuthors []database.BookAuthor
		for _, ba := range bookAuthors {
			if ba.AuthorID != authorID {
				newAuthors = append(newAuthors, ba)
			}
		}
		if err := h.store.SetBookAuthors(book.ID, newAuthors); err != nil {
			continue
		}

		// Add narrator link if not already present
		bookNarrators, err := h.store.GetBookNarrators(book.ID)
		if err != nil {
			continue
		}
		hasNarrator := false
		for _, bn := range bookNarrators {
			if bn.NarratorID == narrator.ID {
				hasNarrator = true
				break
			}
		}
		if !hasNarrator {
			bookNarrators = append(bookNarrators, database.BookNarrator{
				BookID:     book.ID,
				NarratorID: narrator.ID,
				Role:       "narrator",
				Position:   len(bookNarrators),
			})
			if err := h.store.SetBookNarrators(book.ID, bookNarrators); err != nil {
				continue
			}
		}
		booksUpdated++
	}

	// Delete the author record
	if err := h.store.DeleteAuthor(authorID); err != nil {
		httputil.InternalError(c, "failed to delete author", err)
		return
	}

	h.dedupCache.Invalidate("author-duplicates")
	h.authorsCache.InvalidateAll()
	httputil.RespondWithOK(c, gin.H{"narrator_id": narrator.ID, "books_updated": booksUpdated})
}

// ResolveProductionAuthor attempts to find real authors for books attributed to
// a production company by searching metadata sources by title only and optionally
// using AI cover art analysis. Implements POST /authors/:id/resolve-production.
func (h *Handler) ResolveProductionAuthor(c *gin.Context) {
	authorID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid author ID")
		return
	}

	author, err := h.store.GetAuthorByID(authorID)
	if err != nil || author == nil {
		httputil.RespondWithNotFound(c, "author", "")
		return
	}

	if !dedup.IsProductionCompany(author.Name) {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("%q is not a recognized production company", author.Name))
		return
	}

	opID := ulid.Make().String()
	op, err := h.store.CreateOperation(opID, "resolve-production-author", nil)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	params := resolveProductionAuthorOpParams{
		LegacyOpID:     op.ID,
		AuthorID:       authorID,
		ProdAuthorName: author.Name,
	}
	if _, enqErr := h.registry.EnqueueOp(c.Request.Context(), "entities.resolve-production-author", params); enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}
	httputil.RespondWithSuccess(c, 202, gin.H{"operation": op})
}

// --- Series ---

// CountSeries implements GET /series/count.
func (h *Handler) CountSeries(c *gin.Context) {
	count, err := h.store.CountSeries()
	if err != nil {
		httputil.InternalError(c, "failed to count series", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"count": count})
}

// ListSeries implements GET /series.
func (h *Handler) ListSeries(c *gin.Context) {
	if cached, ok := h.seriesCache.Get("all"); ok {
		httputil.RespondWithOK(c, cached)
		return
	}
	resp, err := h.authorSeriesService.ListSeriesWithCounts()
	if err != nil {
		httputil.InternalError(c, "failed to list series", err)
		return
	}
	h.seriesCache.Set("all", resp)
	httputil.RespondWithOK(c, resp)
}

// GetSeriesBooks implements GET /series/:id/books.
func (h *Handler) GetSeriesBooks(c *gin.Context) {
	seriesID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid series ID")
		return
	}
	books, err := h.store.GetBooksBySeriesID(seriesID)
	if err != nil {
		httputil.InternalError(c, "failed to get series books", err)
		return
	}

	enriched := h.enrichBooks(books)
	httputil.RespondWithOK(c, gin.H{"items": enriched, "count": len(enriched)})
}

// RenameSeries implements PUT /series/:id/name.
func (h *Handler) RenameSeries(c *gin.Context) {
	seriesID, err := strconv.Atoi(c.Param("id"))
	if err != nil || seriesID <= 0 {
		httputil.RespondWithBadRequest(c, "invalid series ID")
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httputil.RespondWithBadRequest(c, "name must not be empty")
		return
	}
	if err := h.store.UpdateSeriesName(seriesID, name); err != nil {
		httputil.InternalError(c, "failed to rename series", err)
		return
	}
	if h.dedupCache != nil {
		h.dedupCache.Invalidate("series-duplicates")
	}
	h.seriesCache.InvalidateAll()
	series, _ := h.store.GetSeriesByID(seriesID)
	httputil.RespondWithOK(c, series)
}

// SplitSeries implements POST /series/:id/split.
func (h *Handler) SplitSeries(c *gin.Context) {
	seriesID, err := strconv.Atoi(c.Param("id"))
	if err != nil || seriesID <= 0 {
		httputil.RespondWithBadRequest(c, "invalid series ID")
		return
	}
	var req struct {
		BookIDs []string `json:"book_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.BookIDs) == 0 {
		httputil.RespondWithBadRequest(c, "book_ids must not be empty")
		return
	}
	oldSeries, err := h.store.GetSeriesByID(seriesID)
	if err != nil || oldSeries == nil {
		httputil.RespondWithNotFound(c, "series", "")
		return
	}
	newSeries, err := h.store.CreateSeries(oldSeries.Name+" (Split)", oldSeries.AuthorID)
	if err != nil {
		httputil.InternalError(c, "failed to create new series", err)
		return
	}
	moved := 0
	for _, bookID := range req.BookIDs {
		book, err := h.store.GetBookByID(bookID)
		if err != nil || book == nil {
			continue
		}
		if book.SeriesID == nil || *book.SeriesID != seriesID {
			continue
		}
		book.SeriesID = &newSeries.ID
		if _, err := h.store.UpdateBook(book.ID, book); err != nil {
			continue
		}
		moved++
	}
	if h.dedupCache != nil {
		h.dedupCache.Invalidate("series-duplicates")
	}
	h.seriesCache.InvalidateAll()
	httputil.RespondWithOK(c, gin.H{"new_series": newSeries, "books_moved": moved})
}

// DeleteEmptySeries implements DELETE /series/:id.
func (h *Handler) DeleteEmptySeries(c *gin.Context) {
	seriesID, err := strconv.Atoi(c.Param("id"))
	if err != nil || seriesID <= 0 {
		httputil.RespondWithBadRequest(c, "invalid series ID")
		return
	}
	books, err := h.store.GetBooksBySeriesID(seriesID)
	if err != nil {
		httputil.InternalError(c, "failed to get series books", err)
		return
	}
	if len(books) > 0 {
		httputil.RespondWithConflict(c, "cannot delete series with books")
		return
	}
	if err := h.store.DeleteSeries(seriesID); err != nil {
		httputil.InternalError(c, "failed to delete series", err)
		return
	}
	h.seriesCache.InvalidateAll()
	httputil.RespondWithOK(c, gin.H{"message": "series deleted"})
}

// BulkDeleteSeries deletes multiple empty series at once.
// Implements POST /series/bulk-delete.
func (h *Handler) BulkDeleteSeries(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	deleted := 0
	skipped := 0
	var errors []string
	for _, id := range req.IDs {
		books, err := h.store.GetBooksBySeriesID(id)
		if err != nil {
			errors = append(errors, fmt.Sprintf("series %d: %v", id, err))
			continue
		}
		if len(books) > 0 {
			skipped++
			continue
		}
		if err := h.store.DeleteSeries(id); err != nil {
			errors = append(errors, fmt.Sprintf("series %d: %v", id, err))
			continue
		}
		deleted++
	}
	if deleted > 0 {
		h.seriesCache.InvalidateAll()
	}
	httputil.RespondWithOK(c, gin.H{
		"deleted": deleted,
		"skipped": skipped,
		"errors":  errors,
		"total":   len(req.IDs),
	})
}

// UpdateSeriesName implements PATCH /series/:id.
func (h *Handler) UpdateSeriesName(c *gin.Context) {
	idStr := c.Param("id")
	id := 0
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id <= 0 {
		httputil.RespondWithBadRequest(c, "invalid series id")
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httputil.RespondWithBadRequest(c, "name cannot be empty")
		return
	}
	if err := h.store.UpdateSeriesName(id, name); err != nil {
		httputil.InternalError(c, "failed to update series", err)
		return
	}
	h.dedupCache.Invalidate("series-duplicates")
	h.seriesCache.InvalidateAll()
	series, _ := h.store.GetSeriesByID(id)
	httputil.RespondWithOK(c, series)
}

// --- Narrators ---

// ListNarrators implements GET /narrators.
func (h *Handler) ListNarrators(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	narrators, err := h.store.ListNarrators()
	if err != nil {
		httputil.InternalError(c, "failed to list narrators", err)
		return
	}
	httputil.RespondWithOK(c, narrators)
}

// CountNarrators implements GET /narrators/count.
func (h *Handler) CountNarrators(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	narrators, err := h.store.ListNarrators()
	if err != nil {
		httputil.InternalError(c, "failed to count narrators", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"count": len(narrators)})
}

// ListAudiobookNarrators implements GET /audiobooks/:id/narrators.
func (h *Handler) ListAudiobookNarrators(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	narrators, err := h.store.GetBookNarrators(id)
	if err != nil {
		httputil.InternalError(c, "failed to list audiobook narrators", err)
		return
	}
	if narrators == nil {
		narrators = []database.BookNarrator{}
	}
	httputil.RespondWithOK(c, narrators)
}

// SetAudiobookNarrators implements PUT /audiobooks/:id/narrators.
func (h *Handler) SetAudiobookNarrators(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	var narrators []database.BookNarrator
	if err := c.ShouldBindJSON(&narrators); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if err := h.store.SetBookNarrators(id, narrators); err != nil {
		httputil.InternalError(c, "failed to set audiobook narrators", err)
		return
	}

	// Invalidate caches since narrators may have changed
	h.authorsCache.InvalidateAll()

	httputil.RespondWithOK(c, gin.H{"status": "ok"})
}
