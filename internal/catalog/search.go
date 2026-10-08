// file: internal/catalog/search.go
// version: 1.0.0
// guid: ade6e71f-3758-49c4-8e63-b67b55abb6e8
// last-edited: 2026-10-07
//
// Read side of the author catalog for the metadata search: the Candidates
// view's "Search again" by author (and optionally part of a title) answers
// from the harvested catalog first, because every title Audible lists for a
// library author is already stored here, and a live Audible author listing
// stands in when the catalog has nothing for that author yet.

package catalog

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// SearchLimit caps the entries one catalog search returns. A prolific
// author's listing runs to a few hundred titles; the cap keeps a one-letter
// author fragment from reading the whole catalog.
const SearchLimit = 300

// minAuthorFragment is the shortest folded author text the substring walk
// accepts: "jo" would match half the catalog.
const minAuthorFragment = 3

// TitleMatches reports whether title (plus subtitle) contains want, both
// folded (case, accents, punctuation and spacing ignored). An empty want
// matches everything.
func TitleMatches(title, subtitle, want string) bool {
	w := authorjunk.FoldKey(want)
	if w == "" {
		return true
	}
	return strings.Contains(authorjunk.FoldKey(title+" "+subtitle), w)
}

// Search returns the live (not stale) catalog entries credited to an author
// whose folded name contains author, filtered to titles containing title
// when title is set, as metadata results. An author shorter than three
// letters or digits after folding searches nothing (nil, nil).
func Search(st *database.CatalogStore, author, title string) ([]metadata.BookMetadata, error) {
	if st == nil {
		return nil, nil
	}
	fold := authorjunk.FoldKey(author)
	if len(fold) < minAuthorFragment {
		return nil, nil
	}
	ids, err := st.EntryIDsByAuthorName(fold, SearchLimit)
	if err != nil {
		return nil, err
	}
	out := make([]metadata.BookMetadata, 0, len(ids))
	for _, id := range ids {
		e, err := st.GetEntry(id)
		if errors.Is(err, database.ErrCatalogEntryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if e.StaleSince != nil || !TitleMatches(e.Title, e.Subtitle, title) {
			continue
		}
		out = append(out, EntryMetadata(*e))
	}
	return out, nil
}

// LiveAuthorListing asks the provider for an author's listing (up to pages
// pages of 50) when the catalog has nothing for that author, keeping the
// products in language and, when title is set, those whose title contains
// it. It writes nothing: the scheduled catalog.harvest-authors run is what
// files the author into the catalog.
func LiveAuthorListing(ctx context.Context, l metadata.AuthorLister, author, title, language string, pages int) ([]metadata.BookMetadata, error) {
	if l == nil || strings.TrimSpace(author) == "" {
		return nil, nil
	}
	pages = max(pages, 1)
	var out []metadata.BookMetadata
	fetched := 0
	for page := range pages {
		p, err := l.ListByAuthor(ctx, strings.TrimSpace(author), page, DefaultPageSize)
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		for _, prod := range p.Products {
			if !LanguageMatches(prod.Language, language) || !TitleMatches(prod.Title, prod.Subtitle, title) {
				continue
			}
			out = append(out, EntryMetadata(BuildEntry(prod, l.ProviderID(), "")))
		}
		fetched += len(p.Products) + p.Skipped
		if len(p.Products) == 0 || fetched >= p.TotalResults {
			break
		}
	}
	return out, nil
}

// EntryMetadata converts a catalog entry into a metadata result, the shape
// the search scorer and the candidate list take.
func EntryMetadata(e database.CatalogEntry) metadata.BookMetadata {
	m := metadata.BookMetadata{
		Title:       e.Title,
		Subtitle:    e.Subtitle,
		Publisher:   e.Publisher,
		ASIN:        e.ProviderID,
		CoverURL:    e.CoverURL,
		Language:    e.Language,
		DurationSec: e.RuntimeMin * 60,
	}
	names := make([]string, 0, len(e.Authors))
	for _, a := range e.Authors {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	m.Author = strings.Join(names, ", ")
	m.Narrator = strings.Join(e.Narrators, ", ")
	if len(e.Series) > 0 {
		m.Series = e.Series[0].Name
		m.SeriesPosition = e.Series[0].Sequence
	}
	if len(e.ReleaseDate) >= 4 {
		if y, err := strconv.Atoi(e.ReleaseDate[:4]); err == nil {
			m.PublishYear = y
		}
	}
	switch e.EditionKind {
	case database.EditionAbridged:
		t := true
		m.Abridged = &t
	case database.EditionUnabridged:
		f := false
		m.Abridged = &f
	}
	return m
}
