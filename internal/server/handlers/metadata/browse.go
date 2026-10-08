// file: internal/server/handlers/metadata/browse.go
// version: 1.0.0
// guid: 76ce2b0a-7810-4237-a777-8ae28ace2042
// last-edited: 2026-10-07

package metadatahandler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	metadatapkg "github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// browseListingPages is how many 50-product pages the live author listing
// fetches when the catalog has nothing for an author (200 titles).
const browseListingPages = 4

// defaultBrowseSources reads the harvested author catalog from the Pebble
// store under h.store, and falls back to a live Audible author listing
// (through the shared Audible client and its rate limit) when the catalog
// has nothing for the author. The catalog is read whenever the store holds
// one, independent of catalog.enabled: that flag gates the harvest op and the
// /catalog routes, and an unharvested catalog simply answers nothing.
func (h *Handler) defaultBrowseSources() metafetch.BrowseSources {
	var src metafetch.BrowseSources
	if st := database.NewCatalogStoreFromStore(h.store); st != nil {
		src.Catalog = func(author, title string) ([]metadatapkg.BookMetadata, error) {
			return catalog.Search(st, author, title)
		}
	}
	lister := metadatapkg.NewAudibleClient()
	lang := config.AppConfig.Catalog.Language
	src.AuthorListing = func(ctx context.Context, author, title string) ([]metadatapkg.BookMetadata, error) {
		return catalog.LiveAuthorListing(ctx, lister, author, title, lang, browseListingPages)
	}
	return src
}

// browseSearch answers POST /audiobooks/:id/search-metadata with browse=true
// (metafetch.BrowseSearch). Its results go through the 60-second listCache
// under their own key (browse and catalog_only are part of it) and never
// into the persistent candidate cache.
func (h *Handler) browseSearch(c *gin.Context, id, title, author string, catalogOnly, refresh bool) {
	if h.metadataFetchService == nil {
		httputil.RespondWithInternalError(c, "metadata fetch service not initialized")
		return
	}
	cacheKey := fmt.Sprintf("meta_browse:%s:%s:%s:%t", id, title, author, catalogOnly)
	if !refresh {
		if cached, ok := h.listCache.Get(cacheKey); ok {
			h.respondCandidates(c, id, cached, nil)
			return
		}
	}
	var src metafetch.BrowseSources
	if h.browseSources != nil {
		src = h.browseSources()
	}
	resp, err := h.metadataFetchService.BrowseSearch(c.Request.Context(), id,
		metafetch.BrowseQuery{Title: title, Author: author, CatalogOnly: catalogOnly}, src)
	if errors.Is(err, metafetch.ErrBrowseEmpty) {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if err != nil {
		httputil.RespondWithError(c, http.StatusNotFound, err.Error(), "NOT_FOUND")
		return
	}
	respH := gin.H{"results": resp.Results, "query": resp.Query, "sources_tried": resp.SourcesTried,
		"sources_failed": resp.SourcesFailed, "browse": true, "catalog_only": catalogOnly}
	h.listCache.Set(cacheKey, respH)
	h.respondCandidates(c, id, respH, nil)
}
