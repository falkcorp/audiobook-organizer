// file: internal/metafetch/browse_search.go
// version: 1.0.1
// guid: 5bc1837f-3fbc-42c0-ad1c-7faf383b5983
// last-edited: 2026-10-10
//
// BrowseSearch is the Candidates view's "Search again": a free search by what
// the reviewer typed, not a search for the book's own identity.
//
// The per-book search (searchMetadataForBook) is built to find ONE book: an
// empty title is replaced by the book's own title (resolveSearchInputs), the
// scorer's words come from that title, and every answer scoring <= 0 is
// dropped. So "Search again" with the title cleared and an author typed
// re-asked the book's own title and showed the same single pick (owner report
// 2026-10-07: "joseph phelps" alone -> only the cached pick). BrowseSearch
// asks what was typed and keeps every answer:
//
//   - author given: the harvested author catalog (internal/catalog, local, no
//     request), filtered by the partial title when one is typed. When the
//     catalog has nothing for that author, a live Audible author listing.
//   - title given: every enabled provider by that title (and author).
//
// Every answer is scored by the existing scorer against what was typed (no
// title typed: a flat base, so author, narrator and runtime agreement with
// the book decide the order), deduplicated by ASIN/ISBN, and nothing is
// dropped for a low score. It writes no cache row: its answers are not the
// book's identity answers, so the per-book and bulk replays must never read
// them back.

package metafetch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// BrowseLimit caps the candidates one browse search returns.
const BrowseLimit = 300

// browseLiveTimeout bounds the live provider part of a browse search, so a
// slow provider cannot hold the catalog's answer back indefinitely.
const browseLiveTimeout = 20 * time.Second

// browseNoTitleBase is the base score of an answer to a search with no
// title: there are no words to match, so every answer starts level and the
// author, narrator and runtime steps order them.
const browseNoTitleBase = 0.5

// ErrBrowseEmpty is returned for a browse search with neither a title nor an
// author.
var ErrBrowseEmpty = errors.New("enter a title, an author, or both")

// BrowseQuery is what the reviewer typed.
type BrowseQuery struct {
	Title  string
	Author string
	// CatalogOnly answers from the local author catalog alone (no provider
	// request), so the view can show it at once while the full search runs.
	CatalogOnly bool
}

// BrowseSources are the author-catalog reads, injected because the catalog
// package depends on this one.
type BrowseSources struct {
	// Catalog returns the catalog entries for an author (partial name) and
	// optional partial title. Nil: no catalog.
	Catalog func(author, title string) ([]metadata.BookMetadata, error)
	// AuthorListing asks the provider live for an author's listing, used
	// when the catalog has nothing for the author. Nil: no live listing.
	AuthorListing func(ctx context.Context, author, title string) ([]metadata.BookMetadata, error)
}

// Source labels a browse answer reports in SourcesTried / SourcesFailed.
const (
	browseCatalogLabel = "Audible catalog"
	browseListingLabel = "Audible author listing"
)

type browseAnswer struct {
	r           metadata.BookMetadata
	source      string
	fromCatalog bool
}

// BrowseSearch runs one browse search for bookID. See the file comment.
func (mfs *Service) BrowseSearch(ctx context.Context, bookID string, q BrowseQuery, src BrowseSources) (*SearchMetadataResponse, error) {
	title, author := strings.TrimSpace(q.Title), strings.TrimSpace(q.Author)
	if title == "" && author == "" {
		return nil, ErrBrowseEmpty
	}
	book, err := mfs.db.GetBookByID(bookID)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}
	audible := (&metadata.AudibleClient{}).Name()

	var answers []browseAnswer
	var tried []string
	failed := map[string]string{}

	catalogHits := 0
	if author != "" && src.Catalog != nil {
		tried = append(tried, browseCatalogLabel)
		rs, cerr := src.Catalog(author, title)
		if cerr != nil {
			failed[browseCatalogLabel] = cerr.Error()
		}
		for _, r := range rs {
			answers = append(answers, browseAnswer{r: r, source: audible, fromCatalog: true})
		}
		catalogHits = len(rs)
	}

	if !q.CatalogOnly {
		// A person is waiting on this search (as in the per-book dialog).
		lctx := metadata.WithInteractiveQuota(metadata.WithThrottleBypass(ctx))
		lctx, cancel := context.WithTimeout(lctx, browseLiveTimeout)
		defer cancel()
		if title == "" {
			if catalogHits == 0 && src.AuthorListing != nil {
				tried = append(tried, browseListingLabel)
				rs, lerr := src.AuthorListing(lctx, author, "")
				if lerr != nil {
					failed[browseListingLabel] = lerr.Error()
				}
				for _, r := range rs {
					answers = append(answers, browseAnswer{r: r, source: audible})
				}
			}
		} else {
			live, ltried, lfailed := mfs.browseProviders(lctx, title, author)
			answers = append(answers, live...)
			tried = append(tried, ltried...)
			for k, v := range lfailed {
				failed[k] = v
			}
		}
	}

	bookSec := mfs.bookRuntimeSec(book)
	bookNarrator := ""
	if book.Narrator != nil {
		bookNarrator = SearchAuthorHint(*book.Narrator)
	}
	words := SignificantWords(dropGenreTagline(title))
	seen := candidateSeen{}
	out := make([]MetadataCandidate, 0, len(answers))
	for _, a := range answers {
		if !seen.add(a.r) {
			continue
		}
		c := scoreBrowseAnswer(a.r, a.source, title, words, author, bookNarrator, bookSec)
		c.FromCatalog = a.fromCatalog
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > BrowseLimit {
		out = out[:BrowseLimit]
	}
	if len(failed) == 0 {
		failed = nil
	}
	return &SearchMetadataResponse{
		Results:       out,
		Query:         title,
		SourcesTried:  tried,
		SourcesFailed: failed,
		BookASIN:      trimmedASIN(book),
	}, nil
}

// browseProviders asks every enabled provider by title (and author) once,
// concurrently, and pools what they answer.
func (mfs *Service) browseProviders(ctx context.Context, title, author string) ([]browseAnswer, []string, map[string]string) {
	sources := mfs.overrideSources
	if len(sources) == 0 {
		sources = mfs.BuildSourceChain()
	}
	results := make([][]metadata.BookMetadata, len(sources))
	errs := make([]error, len(sources))
	var g errgroup.Group
	g.SetLimit(sourceFanoutLimit())
	for i, s := range sources {
		g.Go(func() error {
			if author != "" {
				results[i], errs[i] = s.SearchByTitleAndAuthor(ctx, title, author)
			} else {
				results[i], errs[i] = s.SearchByTitle(ctx, title)
			}
			return nil
		})
	}
	_ = g.Wait() // the callbacks never return an error
	failed := map[string]string{}
	tried := make([]string, 0, len(sources))
	var out []browseAnswer
	for i, s := range sources {
		tried = append(tried, s.Name())
		if errs[i] != nil && len(results[i]) == 0 {
			failed[s.Name()] = errs[i].Error()
		}
		for _, r := range results[i] {
			out = append(out, browseAnswer{r: r, source: s.Name()})
		}
	}
	return out, tried, failed
}

// scoreBrowseAnswer scores one answer against what was typed with the
// existing scorer steps: the title F1 (ScoreOneResultWithBreakdown) when a
// title was typed, else a flat base; then author, narrator and runtime
// agreement. Nothing is dropped for a low score.
func scoreBrowseAnswer(r metadata.BookMetadata, source, title string, words map[string]bool, author, bookNarrator string, bookSec int) MetadataCandidate {
	var rec *scoreRecorder
	if title == "" {
		rec = newScoreRecorder(browseNoTitleBase, "Author search",
			"No title was typed, so every result starts level; author, narrator and runtime agreement with the book order them.")
	} else {
		s, bd := ScoreOneResultWithBreakdown(r, words)
		rec = recorderFrom(s, &bd)
	}
	if author != "" {
		want := authorjunk.FoldKey(author)
		got := authorjunk.FoldKey(r.Author)
		switch {
		case got == "":
			rec.mulAbsence("author", "Author missing", 0.75, "The search named an author but the result does not name one.")
		case strings.Contains(got, want) || strings.Contains(want, got):
			rec.mul("author", "Author match", 1.5, "The result's author matches the author searched for.")
		default:
			rec.mul("author", "Author mismatch", 0.7, "The result names a different author than the one searched for.")
		}
	}
	if bookNarrator != "" && r.Narrator != "" {
		rn, bn := strings.ToLower(r.Narrator), strings.ToLower(bookNarrator)
		if strings.Contains(rn, bn) || strings.Contains(bn, rn) {
			rec.mul("narrator_match", "Narrator match", 1.3, "The result's narrator matches the book's known narrator.")
		}
	}
	rec.mul("duration", "Runtime comparison", durationScoreMultiplier(bookSec, r.DurationSec), durationStepDetail(bookSec, r.DurationSec))
	return newSearchCandidate(r, source, rec.score, rec.breakdown(), bookSec, false)
}
