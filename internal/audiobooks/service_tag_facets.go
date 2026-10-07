// file: internal/audiobooks/service_tag_facets.go
// version: 1.0.0
// guid: 3e9a6c1d-7b2f-4d85-a0c4-8f1e2b6d9a73
// last-edited: 2026-10-06

// Scoped tag facets: the Library's Browse-by-Tag chips counted over the
// CURRENT result set instead of the whole library. The match set comes from
// the list pipeline itself (MatchingBookIDs), so a chip count can never
// disagree with what the list shows for the same request; this file adds no
// filter logic of its own.

package audiobooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

// ScopedTagFacets is the tag cloud for one list request.
type ScopedTagFacets struct {
	// Tags is every tag carried by at least one matching book, with the
	// number of matching books carrying it; count desc, then tag asc.
	Tags []database.TagWithCount `json:"tags"`
	// Total is the number of books in the match set.
	Total int `json:"total"`
}

// tagCountSeekThreshold is the match-set size up to which tags are read per
// book (GetBookTagsByBookIDs: one iterator, one seek per book). Above it, one
// sequential scan of the tag reverse index (CountTagsForBookIDs) is cheaper,
// because its cost is the library's tag-row count, not the set size.
var tagCountSeekThreshold = 2000

// scopedTagFacetsTTL bounds a cached entry. Entries are keyed by the store
// change-log generation, so a book or tag write already puts them out of
// reach; the TTL only backstops writes that bypass the change log.
const scopedTagFacetsTTL = 10 * time.Minute

// tagCountsForIDsStore is the large-set capability (PebbleStore).
type tagCountsForIDsStore interface {
	CountTagsForBookIDs(ids map[string]struct{}) (map[string]int, error)
}

// tagsByBookIDsStore is the small-set capability (on database.Store).
type tagsByBookIDsStore interface {
	GetBookTagsByBookIDs(bookIDs []string) (map[string][]string, error)
}

// MatchingBookIDs returns the IDs of EVERY book the list request
// (search, author_id, series_id, filters) matches — the full match set, not a
// page — through the same pipeline GET /audiobooks runs:
//
//   - a search the result cache serves is looked up under the list's own key
//     and evaluator, so right after the list loaded this is a cache hit on the
//     identical membership;
//   - anything else runs queryAudiobooks uncapped (build mode: no
//     post-filter window, fail-closed hydration).
//
// Order is not meaningful to callers.
func (svc *AudiobookService) MatchingBookIDs(ctx context.Context, search string, authorID, seriesID *int, f ListFilters) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	if key, ok := svc.searchCacheKey(search, authorID, seriesID, f); ok {
		ev := &listSearchEvaluator{svc: svc, search: search, authorID: authorID, seriesID: seriesID, f: f, substring: !svc.bleveSearchable()}
		res, err := svc.resultCache.Lookup(ctx, key, ev, searchcache.LookupOptions{})
		if err == nil {
			return res.IDs, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		// Same fail-open as GetAudiobooksPage: the uncached pipeline below
		// still answers a search the shared cache could not.
		searchCacheLog.Warn("scoped tag facets: search cache lookup failed, running uncached: %v", err)
	}
	books, _, err := svc.queryAudiobooks(ctx, searchFullLimit, 0, search, authorID, seriesID, f, nil, true)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(books))
	for i := range books {
		ids[i] = books[i].ID
	}
	return ids, nil
}

// ScopedTagFacets counts tags over the books the list request matches. See
// MatchingBookIDs for how the match set is produced.
//
// Cached per (normalized request, store change-log generation) when the
// request's result depends only on book rows; concurrent identical misses
// share one computation.
func (svc *AudiobookService) ScopedTagFacets(ctx context.Context, search string, authorID, seriesID *int, f ListFilters) (ScopedTagFacets, error) {
	key, cacheable := svc.scopedTagFacetsKey(search, authorID, seriesID, f)
	if !cacheable || svc.tagFacets == nil {
		return svc.computeScopedTagFacets(ctx, search, authorID, seriesID, f)
	}
	if v, ok := svc.tagFacets.Get(key); ok {
		return v, nil
	}
	v, err, _ := svc.tagFacetsFlight.Do(key, func() (any, error) {
		// Detached from any single caller: a shared computation must not be
		// cancelled because the first requester navigated away.
		res, err := svc.computeScopedTagFacets(context.WithoutCancel(ctx), search, authorID, seriesID, f)
		if err == nil {
			svc.tagFacets.Set(key, res)
		}
		return res, err
	})
	if err != nil {
		return ScopedTagFacets{}, err
	}
	return v.(ScopedTagFacets), nil
}

func (svc *AudiobookService) computeScopedTagFacets(ctx context.Context, search string, authorID, seriesID *int, f ListFilters) (ScopedTagFacets, error) {
	ids, err := svc.MatchingBookIDs(ctx, search, authorID, seriesID, f)
	if err != nil {
		return ScopedTagFacets{}, err
	}
	counts, err := svc.countTagsForIDs(ids)
	if err != nil {
		return ScopedTagFacets{}, err
	}
	out := ScopedTagFacets{Tags: make([]database.TagWithCount, 0, len(counts)), Total: len(ids)}
	for tag, n := range counts {
		out.Tags = append(out.Tags, database.TagWithCount{Tag: tag, Count: n})
	}
	sort.Slice(out.Tags, func(i, j int) bool {
		if out.Tags[i].Count != out.Tags[j].Count {
			return out.Tags[i].Count > out.Tags[j].Count
		}
		return out.Tags[i].Tag < out.Tags[j].Tag
	})
	return out, nil
}

// countTagsForIDs returns tag -> number of distinct books in ids carrying it.
// Both store paths are ONE sequential iteration (no per-book store call and
// no fan-out), which is why there is no worker pool here.
func (svc *AudiobookService) countTagsForIDs(ids []string) (map[string]int, error) {
	counts := map[string]int{}
	if len(ids) == 0 {
		return counts, nil
	}
	if len(ids) > tagCountSeekThreshold {
		if cs, ok := database.AsCapability[tagCountsForIDsStore](svc.store); ok {
			set := make(map[string]struct{}, len(ids))
			for _, id := range ids {
				set[id] = struct{}{}
			}
			return cs.CountTagsForBookIDs(set)
		}
	}
	if bs, ok := database.AsCapability[tagsByBookIDsStore](svc.store); ok {
		byBook, err := bs.GetBookTagsByBookIDs(ids)
		if err != nil {
			return nil, err
		}
		for _, tags := range byBook {
			for _, t := range tags {
				counts[t]++
			}
		}
		return counts, nil
	}
	// Neither capability (test doubles only): per-book reads, deduplicated.
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		tags, err := svc.store.GetBookTags(id)
		if err != nil {
			return nil, err
		}
		for _, t := range tags {
			counts[t]++
		}
	}
	return counts, nil
}

// scopedTagFacetsKey is the cache key for a scoped-facets request, or false
// when it must not be cached. Fails CLOSED exactly where searchCacheKey does:
// per-user filters (ListFilters or search DSL), RestrictToIDs (file-error /
// quick-query state the change log does not track) and fingerprint/coverage
// filters (computed from book files). Also uncached when no change log is
// wired, since the generation is what invalidates an entry.
//
// Sort is dropped: it orders the match set, never changes its membership.
func (svc *AudiobookService) scopedTagFacetsKey(search string, authorID, seriesID *int, f ListFilters) (string, bool) {
	if svc.resultCache == nil || svc.resultCache.Changes() == nil {
		return "", false
	}
	if len(f.PerUserFilters) > 0 || f.RestrictToIDs != nil ||
		f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil {
		return "", false
	}
	q := strings.TrimSpace(search)
	if q != "" && SearchHasPerUserFilters(q) {
		return "", false
	}
	f.SortBy, f.SortOrder, f.UserID = "", "", ""
	fj, err := json.Marshal(f)
	if err != nil {
		return "", false
	}
	engine := "bleve"
	if !svc.bleveSearchable() {
		engine = "substring"
	}
	var b strings.Builder
	b.WriteString(strconv.FormatUint(svc.resultCache.Changes().Generation(), 10))
	b.WriteByte(0)
	b.WriteString(engine)
	b.WriteByte(0)
	b.WriteString(NormalizeSearchKey(search, engine == "substring"))
	b.WriteByte(0)
	b.Write(fj)
	b.WriteByte(0)
	if authorID != nil {
		b.WriteString(strconv.Itoa(*authorID))
	}
	b.WriteByte(0)
	if seriesID != nil {
		b.WriteString(strconv.Itoa(*seriesID))
	}
	return b.String(), true
}
