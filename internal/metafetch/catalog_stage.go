// file: internal/metafetch/catalog_stage.go
// version: 1.2.0
// guid: 6c0f3b95-2e8a-4d71-b4a9-9f1d5a7e0c28
// last-edited: 2026-10-12
//
// The local author-catalog blocking stage of the metadata search (spec 02
// section 3.3, B1 and B2; owner decision D31). Before the provider fan-out a
// book is looked up in the harvested catalog with no provider call; what that
// finds joins the pool the scorer ranks as REVIEW-ONLY candidates tagged with
// their stage of origin. In this version it adds candidates and removes no
// provider call: a strong catalog candidate does not stop the fan-out.
//
// The matcher itself is internal/catalog.MatchBook; the catalog is a layer
// above this package, so the search holds it through CatalogMatcher and the
// server installs it (SetCatalogMatcher).

package metafetch

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// CatalogMatcher is the local catalog lookup (catalog.Matcher).
type CatalogMatcher interface {
	MatchBook(q metadata.CatalogQuestion) (metadata.CatalogMatch, error)
}

// SetCatalogMatcher installs the local catalog stage's matcher; nil removes
// it. The stage also needs metadata_fetch.catalog_stage on.
func (mfs *Service) SetCatalogMatcher(m CatalogMatcher) {
	if m == nil {
		mfs.catalogMatcher.Store(nil)
		return
	}
	mfs.catalogMatcher.Store(&m)
}

// CatalogStageStats are one run's counters of where its candidates came from.
// A run attaches one to its context (WithCatalogStageStats) and every search
// under that context adds to it. They count THIS attempt's searches: a book
// answered from the candidate cache runs no search and adds nothing.
type CatalogStageStats struct {
	// CatalogMatched counts the candidates the stage found, before the pool
	// dropped any a provider also returned (same ASIN) or the scorer refused:
	// with CatalogCandidates it shows how many the providers made redundant.
	CatalogMatched atomic.Int64
	// CatalogCandidates and ProviderCandidates count the candidates the
	// searches returned, by origin: the first cost no provider call.
	CatalogCandidates  atomic.Int64
	ProviderCandidates atomic.Int64
	// BooksWithCatalog counts searches that returned at least one catalog
	// candidate.
	BooksWithCatalog atomic.Int64
	// OversizeBlocks and HomonymSkipped count credit blocks the stage skipped
	// (catalog.MaxBlockEntries; a name shared by two provider authors).
	OversizeBlocks atomic.Int64
	HomonymSkipped atomic.Int64
}

type catalogStageStatsKey struct{}

// WithCatalogStageStats returns a context whose searches count into st.
func WithCatalogStageStats(ctx context.Context, st *CatalogStageStats) context.Context {
	return context.WithValue(ctx, catalogStageStatsKey{}, st)
}

func catalogStageStatsFrom(ctx context.Context) *CatalogStageStats {
	st, _ := ctx.Value(catalogStageStatsKey{}).(*CatalogStageStats)
	return st
}

// recordCatalogStageStats adds a finished search's candidates to the run's
// counters, when it has any.
func recordCatalogStageStats(ctx context.Context, candidates []MetadataCandidate) {
	st := catalogStageStatsFrom(ctx)
	if st == nil {
		return
	}
	catalog := int64(0)
	for i := range candidates {
		if candidates[i].Source == metadata.CatalogSourceName {
			catalog++
		}
	}
	st.CatalogCandidates.Add(catalog)
	st.ProviderCandidates.Add(int64(len(candidates)) - catalog)
	if catalog > 0 {
		st.BooksWithCatalog.Add(1)
	}
}

// catalogStageApplies reports whether this search runs the catalog stage: the
// switch is on, a matcher is installed, and the search is not a fallback
// provider's own (OnlySources naming only review-only providers, the Open
// Library / Google Books lookup that follows a chain answer). A partial
// re-ask of the chain's providers still runs it: the stage is local and its
// candidates are deduplicated by the pool.
func (mfs *Service) catalogStageApplies(opts SearchOptions) (CatalogMatcher, bool) {
	if !config.Snapshot().MetadataFetch.CatalogStage {
		return nil, false
	}
	mp := mfs.catalogMatcher.Load()
	if mp == nil {
		return nil, false
	}
	if len(opts.OnlySources) > 0 {
		onlyFallback := true
		for _, n := range opts.OnlySources {
			if !IsReviewOnlyCandidateSource(n) {
				onlyFallback = false
				break
			}
		}
		if onlyFallback {
			return nil, false
		}
	}
	return *mp, true
}

// catalogTitles are the titles the catalog may know the book by: the one
// searched, the cleaned and name-only readings of it, the stored title and
// the query. B2 compares a block entry with each.
func catalogTitles(book *database.Book, in searchInputs, searchTitle, rawTitle string) []string {
	cands := []string{searchTitle, in.literal, in.rawQuery, rawTitle, book.Title}
	if !in.parsed.Junk {
		if !in.parsed.TitleIsSeries {
			cands = append(cands, in.parsed.Title, in.parsed.YearFree, in.parsed.Short)
		}
		cands = append(cands, in.parsed.Name)
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range cands {
		t = strings.TrimSpace(t)
		if t == "" || metadata.IsUnsearchableTitle(t) {
			continue
		}
		if k := authorjunk.FoldKey(t); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	return out
}

// catalogCredits are the book's author credits in order, one per live credit
// (database.LiveBookAuthorNames, never the flat Book.Author). A placeholder
// credit ("Unknown Author") stays in its place as "", which names no block, so
// a credit's index here is its index in the live list and the position a
// candidate reports. The author this search was asked by stands in only when
// the book has no usable live credit at all (a book whose author row is
// missing); it never goes in front of, or between, real credits.
func (mfs *Service) catalogCredits(book *database.Book, searchAuthor string) []string {
	live, err := database.LiveBookAuthorNames(mfs.db, book)
	if err != nil {
		searchFanoutLog.Warn("catalog stage: author credits of book %s unreadable, matching by ASIN only: %v",
			logger.SanitizeLogValue(book.ID), err)
		return nil
	}
	credits := make([]string, len(live))
	usable := false
	for i, name := range live {
		credits[i] = SearchAuthorHint(name)
		usable = usable || credits[i] != ""
	}
	if usable {
		return credits
	}
	if h := SearchAuthorHint(searchAuthor); h != "" {
		return []string{h}
	}
	return nil
}

// runCatalogStage runs the stage for one book. It returns nil when the stage
// does not apply, has no question to ask, or failed (logged: the stage only
// adds candidates, so the search carries on without it).
func (mfs *Service) runCatalogStage(ctx context.Context, book *database.Book, in searchInputs,
	searchTitle, rawTitle, series, asin string, opts SearchOptions) *metadata.CatalogMatch {
	matcher, ok := mfs.catalogStageApplies(opts)
	if !ok {
		return nil
	}
	q := metadata.CatalogQuestion{
		ASIN:     asin,
		Titles:   catalogTitles(book, in, searchTitle, rawTitle),
		Credits:  mfs.catalogCredits(book, in.author),
		Series:   series,
		Position: in.parsed.Position,
	}
	if q.ASIN == "" && (len(q.Titles) == 0 || len(q.Credits) == 0) {
		return nil
	}
	match, err := matcher.MatchBook(q)
	if err != nil {
		searchFanoutLog.Warn("catalog stage: book %s: %v", logger.SanitizeLogValue(book.ID), err)
		return nil
	}
	if st := catalogStageStatsFrom(ctx); st != nil {
		st.CatalogMatched.Add(int64(len(match.Candidates)))
		st.OversizeBlocks.Add(int64(match.OversizeBlocks))
		st.HomonymSkipped.Add(int64(match.HomonymSkipped))
	}
	return &match
}

// tagCatalogCandidates stamps the stage of origin on the candidates the
// catalog produced, matched to the stage's answers by ASIN.
func tagCatalogCandidates(cands []MetadataCandidate, m *metadata.CatalogMatch) {
	byASIN := make(map[string]metadata.CatalogOrigin, len(m.Candidates))
	for i, c := range m.Candidates {
		if i < len(m.Origins) {
			byASIN[strings.ToUpper(strings.TrimSpace(c.ASIN))] = m.Origins[i]
		}
	}
	for i := range cands {
		o, ok := byASIN[strings.ToUpper(strings.TrimSpace(cands[i].ASIN))]
		if !ok {
			continue
		}
		cands[i].Stage, cands[i].StageCreditPosition, cands[i].ManualOnly = o.Stage, o.CreditPosition, o.ManualOnly
		cands[i].FromCatalog = true
	}
}
