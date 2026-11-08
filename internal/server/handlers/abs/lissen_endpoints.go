// file: internal/server/handlers/abs/lissen_endpoints.go
// version: 1.0.1
// guid: 3b7d52e0-9a41-4c68-b1f5-6e2a8c04d917
// last-edited: 2026-11-07

package abs

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// ── Endpoints the Lissen Android client calls ───────────────────────────────
//
// Each of these fell through to the /api/* → /api/v1/* redirect before
// 2026-11-07. Lissen's Genre grouping, its search-by-series, and its author
// portraits therefore failed with no hint why.

// absBatchGetMaxItems bounds POST /api/items/batch/get. Upstream ABS has no
// bound; this one exists because every id costs a store point-get and the whole
// batch is hydrated into full expanded items in one response. Lissen sends the
// ids of one search result's series books, so 200 is far above real use.
// lissenLog routes this file through internal/logger (the log-injection barrier).
var lissenLog = logger.New("abs")

const absBatchGetMaxItems = 200

// absBatchGetMaxBodyBytes caps the request body read. 200 ULID-sized ids are
// under 10 KiB; this leaves ample room while refusing a multi-megabyte POST.
const absBatchGetMaxBodyBytes = 1 << 20

// absStatsTopAuthors matches upstream ABS, which reports only the ten authors
// with the most books in authorsWithCount.
const absStatsTopAuthors = 10

type libraryStatsGenreDTO struct {
	Genre string `json:"genre"`
	Count int    `json:"count"`
}

type libraryStatsAuthorDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// libraryStatsDTO is the part of upstream ABS's LibraryController.stats document
// this server can compute from its cached indexes.
//
// 🔴 totalItems, totalDuration, totalSize, numAudioTrack, longestItems and
// largestItems are OMITTED, not zeroed. Computing them needs a full pass over
// every book and its files, which this endpoint exists to avoid, and a
// "totalDuration": 0 would be a false claim about the library rather than a gap.
// Lissen decodes only genresWithCount; a client that requires the others gets a
// visible decode failure instead of a plausible wrong number.
type libraryStatsDTO struct {
	TotalAuthors     int                     `json:"totalAuthors"`
	TotalGenres      int                     `json:"totalGenres"`
	AuthorsWithCount []libraryStatsAuthorDTO `json:"authorsWithCount"`
	GenresWithCount  []libraryStatsGenreDTO  `json:"genresWithCount"`
}

// LibraryStats handles GET /api/libraries/:libraryId/stats.
//
// The genre list and counts are the very ones /filterdata and /search serve
// (filterDataCached), so the Genre view and the filter dropdown cannot disagree;
// the author figures come from the same contributor index as /authors.
// genresWithCount is always a non-nil array: Lissen's field is non-nullable.
func (h *Handler) LibraryStats(c *gin.Context) {
	if !h.knownLibrary(c) {
		return
	}
	ctx := c.Request.Context()
	fd := h.filterDataCached(ctx)

	out := libraryStatsDTO{
		TotalGenres:      len(fd.Genres),
		AuthorsWithCount: []libraryStatsAuthorDTO{},
		GenresWithCount:  make([]libraryStatsGenreDTO, 0, len(fd.Genres)),
	}
	for _, g := range fd.Genres {
		out.GenresWithCount = append(out.GenresWithCount, libraryStatsGenreDTO{Genre: g, Count: fd.genreCounts[g]})
	}

	idx, err := h.contributorsCached(ctx)
	if err != nil {
		// The genre half is complete and is all Lissen reads; degrade the author
		// half to its zero value rather than failing the Genre view.
		lissenLog.Warn("abs: library stats: contributor index unavailable; omitting author figures: %v", err)
		respondJSON(c, http.StatusOK, out)
		return
	}
	out.TotalAuthors = len(idx.authors)
	top := make([]authorDTO, len(idx.authors))
	copy(top, idx.authors) // the index is shared by every reader: never sort it in place
	sort.SliceStable(top, func(i, j int) bool {
		if top[i].NumBooks != top[j].NumBooks {
			return top[i].NumBooks > top[j].NumBooks
		}
		return top[i].Name < top[j].Name
	})
	if len(top) > absStatsTopAuthors {
		top = top[:absStatsTopAuthors]
	}
	for _, a := range top {
		out.AuthorsWithCount = append(out.AuthorsWithCount, libraryStatsAuthorDTO{ID: a.ID, Name: a.Name, Count: a.NumBooks})
	}
	respondJSON(c, http.StatusOK, out)
}

type itemsBatchRequest struct {
	LibraryItemIDs []string `json:"libraryItemIds"`
}

type itemsBatchResponse struct {
	LibraryItems []itemWithProgressDTO `json:"libraryItems"`
}

// ItemsBatchGet handles POST /api/items/batch/get.
//
// Each id is resolved exactly as GET /api/items/:id resolves it (lookupItemRef,
// so a merge loser's alias still resolves and is rendered under the id the
// client sent), and each item is the same expanded shape with userMediaProgress.
// Ids that name no live item are SKIPPED, as upstream does, so one stale id in a
// series does not blank the whole result. Order follows the request; duplicate
// ids are answered once.
//
// The books are hydrated in ONE GetBooksByIDs + loadItemViews call, which batches
// the author, narrator and series lookups and runs per-book file I/O in a bounded
// worker pool. Only the id resolution is per id: one identity point-get each,
// bounded by absBatchGetMaxItems.
func (h *Handler) ItemsBatchGet(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, absBatchGetMaxBodyBytes)
	var req itemsBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.LibraryItemIDs == nil {
		respondError(c, http.StatusBadRequest, "request body must be {\"libraryItemIds\": [...]}")
		return
	}
	if len(req.LibraryItemIDs) > absBatchGetMaxItems {
		respondError(c, http.StatusBadRequest, "too many libraryItemIds in one request")
		return
	}

	userID := currentUserID(c)
	refs := make([]itemRef, 0, len(req.LibraryItemIDs))
	seen := make(map[string]struct{}, len(req.LibraryItemIDs))
	bookIDs := make([]string, 0, len(req.LibraryItemIDs))
	seenBook := make(map[string]struct{}, len(req.LibraryItemIDs))
	for _, raw := range req.LibraryItemIDs {
		id := strings.TrimSpace(raw)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ref, err := h.lookupItemRef(userID, id, false)
		if errors.Is(err, errItemNotFound) {
			continue
		}
		if err != nil {
			lissenLog.Warn("abs: items batch get: could not resolve item id %s: %v", logger.SanitizeLogValue(id), err)
			respondError(c, http.StatusInternalServerError, "could not resolve library items")
			return
		}
		refs = append(refs, ref)
		if _, ok := seenBook[ref.BookID]; !ok {
			seenBook[ref.BookID] = struct{}{}
			bookIDs = append(bookIDs, ref.BookID)
		}
	}

	out := itemsBatchResponse{LibraryItems: make([]itemWithProgressDTO, 0, len(refs))}
	if len(bookIDs) == 0 {
		respondJSON(c, http.StatusOK, out)
		return
	}
	books, err := h.library.GetBooksByIDs(bookIDs)
	if err != nil {
		lissenLog.Warn("abs: items batch get: could not load %d books: %v", len(bookIDs), err)
		respondError(c, http.StatusInternalServerError, "could not load library items")
		return
	}
	// A book dropped here is a merge loser: loadItemViews omits it, and GET
	// /api/items/:id answers it 404 (errRedirectedSyncID), so it is skipped too.
	views, err := h.loadItemViews(c.Request.Context(), books)
	if err != nil {
		lissenLog.Warn("abs: items batch get: could not hydrate %d items: %v", len(books), err)
		respondError(c, http.StatusInternalServerError, "could not load library items")
		return
	}
	byBook := make(map[string]*itemView, len(views))
	for i := range views {
		byBook[views[i].Book.ID] = &views[i]
	}

	user, hasUser := servermiddleware.CurrentUser(c)
	for _, ref := range refs {
		src, ok := byBook[ref.BookID]
		if !ok {
			continue
		}
		view := *src // this request's own copy, as in Item
		view.SyncID = ref.RequestedID
		item := itemWithProgressDTO{libraryItemExpandedDTO: h.expandedItem(&view)}
		if hasUser && user != nil {
			item.UserMediaProgress = h.mediaProgress(user.ID, &view)
		}
		h.noteAliasUse(userID, ref)
		out.LibraryItems = append(out.LibraryItems, item)
	}
	respondJSON(c, http.StatusOK, out)
}

// AuthorImage handles GET/HEAD /api/authors/:id/image.
//
// There is no author image to serve: authorDTO.ImagePath is declared but nothing
// ever sets it, database.Author carries no image reference, and no import or
// enrichment path stores a portrait. So this is a plain 404, which Lissen (and
// upstream ABS's own app) render as a placeholder — the same answer
// NarratorImage gives. If author images are ever stored, serve them here the
// way ItemCover serves covers, and set authorDTO.ImagePath to match.
func (h *Handler) AuthorImage(c *gin.Context) {
	respondError(c, http.StatusNotFound, "author image not found")
}
