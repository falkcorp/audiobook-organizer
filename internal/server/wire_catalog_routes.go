// file: internal/server/wire_catalog_routes.go
// version: 1.0.0
// guid: 8f4a2d61-0c7b-4e39-a5d2-6e1b9c3f8a07
// last-edited: 2026-10-01

package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// wireCatalogRoutes registers the P1 author-catalog read API: enough to
// inspect what catalog.harvest-authors wrote. No wanted, requests or links
// (later phases). Both routes answer 404 while catalog.enabled is off, so a
// disabled feature is indistinguishable from an absent one.
func (s *Server) wireCatalogRoutes(protected *gin.RouterGroup) {
	h := &catalogHandler{store: func() *database.CatalogStore { return database.NewCatalogStoreFromStore(s.storeForWiring()) }}
	protected.GET("/catalog/entries", s.perm(auth.PermLibraryView), h.listEntries)
	protected.GET("/catalog/entries/:id", s.perm(auth.PermLibraryView), h.getEntry)
}

type catalogHandler struct {
	store func() *database.CatalogStore
}

// gate answers 404 when the flag is off and 503 when there is no Pebble
// store, returning nil in both cases.
func (h *catalogHandler) gate(c *gin.Context) *database.CatalogStore {
	if !config.AppConfig.Catalog.Enabled {
		httputil.RespondWithError(c, http.StatusNotFound, "author catalog is disabled (catalog.enabled=false)", "catalog_disabled")
		return nil
	}
	st := h.store()
	if st == nil {
		httputil.RespondWithServiceUnavailable(c, "catalog store unavailable")
		return nil
	}
	return st
}

// listEntries: GET /catalog/entries?author=&author_asin=&series=&limit=&offset=
// author is a name (folded, so spelling variants match); author_asin is an
// Audible author ASIN; series is a series name. At most one filter.
func (h *catalogHandler) listEntries(c *gin.Context) {
	st := h.gate(c)
	if st == nil {
		return
	}
	q := database.CatalogListQuery{}
	filters := 0
	if v := strings.TrimSpace(c.Query("author")); v != "" {
		q.AuthorKey = catalog.AuthorNameKey(v)
		filters++
	}
	if v := strings.TrimSpace(c.Query("author_asin")); v != "" {
		q.AuthorKey = catalog.AuthorASINKey(v)
		filters++
	}
	if v := strings.TrimSpace(c.Query("series")); v != "" {
		q.SeriesKey = catalog.SeriesKey(v)
		filters++
	}
	if filters > 1 {
		httputil.RespondWithBadRequest(c, "use at most one of author, author_asin, series")
		return
	}
	if filters == 1 && q.AuthorKey == "" && q.SeriesKey == "" {
		httputil.RespondWithBadRequest(c, "filter value has no letters or digits")
		return
	}
	var err error
	if q.Limit, err = queryInt(c, "limit", 50); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if q.Offset, err = queryInt(c, "offset", 0); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if q.Limit < 1 || q.Limit > 500 {
		httputil.RespondWithBadRequest(c, "limit must be between 1 and 500")
		return
	}
	if q.Offset < 0 {
		httputil.RespondWithBadRequest(c, "offset must not be negative")
		return
	}
	entries, total, err := st.ListEntries(q)
	if err != nil {
		httputil.RespondWithInternalError(c, "list catalog entries: "+err.Error())
		return
	}
	httputil.RespondWithList(c, entries, total, q.Limit, q.Offset)
}

// getEntry: GET /catalog/entries/:id — the entry plus the ids of the other
// entries in its edition group.
func (h *catalogHandler) getEntry(c *gin.Context) {
	st := h.gate(c)
	if st == nil {
		return
	}
	id := c.Param("id")
	e, err := st.GetEntry(id)
	if errors.Is(err, database.ErrCatalogEntryNotFound) {
		httputil.RespondWithNotFound(c, "catalog entry", id)
		return
	}
	if err != nil {
		httputil.RespondWithInternalError(c, "get catalog entry: "+err.Error())
		return
	}
	members, err := st.EditionGroupMembers(e.EditionGroupID)
	if err != nil {
		httputil.RespondWithInternalError(c, "edition group: "+err.Error())
		return
	}
	httputil.RespondWithOK(c, gin.H{"entry": e, "edition_group_entry_ids": members})
}

func queryInt(c *gin.Context, name string, def int) (int, error) {
	v := strings.TrimSpace(c.Query(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.New(name + " must be an integer")
	}
	return n, nil
}
