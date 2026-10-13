// file: internal/catalog/match.go
// version: 1.2.0
// guid: 3b9e6c10-5d2a-4f87-9a41-c8e0d7b25f93
// last-edited: 2026-10-12
//
// The local author-catalog blocking stage (spec 02 section 3.3, B1 and B2):
// a book is looked up in the harvested catalog with ZERO provider calls,
// before the provider fan-out. B1 is a point read by the book's own ASIN. B2
// takes each credit of the book, in order, to its author block by an EXACT
// folded key (or an alias of one) and scores the block's titles against every
// title the book is known by.
//
// Bulk matching never takes the substring fallback of
// CatalogStore.EntryIDsByAuthorName (it scans every author key), never calls
// the network, and never writes.

package catalog

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// SourceName is the Source of a candidate this stage produces. It is
// review-only (metafetch.IsReviewOnlyCandidateSource).
const SourceName = metadata.CatalogSourceName

// BookQuestion and MatchResult are the stage's question and answer. They are
// defined in internal/metadata so the metadata search, a layer below the
// catalog, can ask without importing it.
type (
	BookQuestion = metadata.CatalogQuestion
	MatchResult  = metadata.CatalogMatch
)

const (
	// MaxBlockEntries is the largest author block B2 scores. A bigger one (a
	// junk or franchise row) is skipped and counted (MatchResult.OversizeBlocks).
	MaxBlockEntries = 2000
	// MaxCandidatesPerBook caps the candidates one book gets from the catalog.
	MaxCandidatesPerBook = 5

	// minBlockFold is the shortest folded credit that names a block.
	minBlockFold = 3
	// minTitleJaccard is the trigram-Jaccard floor of a title worth an edit
	// distance; topForEditDistance is how many entries per block get one.
	minTitleJaccard    = 0.5
	topForEditDistance = 10
	// shortTitleRunes and longTitleRunes bound the edit-distance cap: 3 for a
	// folded title under 30 runes, 6 from there up (spec section 3.3).
	longTitleRunes = 30
	maxEditShort   = 3
	maxEditLong    = 6
	// runesPerEdit tightens the cap further for short titles: one edit per 6
	// runes, so a 5-rune title must match exactly and "Book 2" never meets
	// "Book 3" through an edit.
	runesPerEdit = 6
)

// MatchBook looks the book up in the local catalog: B1 by ASIN, otherwise B2
// by author block. It returns at most MaxCandidatesPerBook candidates, each
// from a live (not stale) entry, with their origins. An error is a store read
// failure or ctx ending (the context's error, checked before each block of
// store reads, so a search that gave up stops asking the store); "nothing
// matched" is an empty result.
//
// Credits are read in the order given and are never sorted, joined or
// de-duplicated: the block of each credit is tried in turn, and a candidate
// records the position of the credit that found it.
func MatchBook(ctx context.Context, st *database.CatalogStore, q BookQuestion) (MatchResult, error) {
	var res MatchResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if st == nil {
		return res, nil
	}
	if asin := strings.TrimSpace(q.ASIN); asin != "" && q.Provider != "" && q.Marketplace != "" {
		e, err := st.GetEntryByProviderID(q.Provider, q.Marketplace, asin)
		switch {
		case err == nil && e.StaleSince == nil:
			res.Candidates = append(res.Candidates, EntryMetadata(*e))
			res.Origins = append(res.Origins, metadata.CatalogOrigin{Stage: metadata.CatalogStageIdentifier, ManualOnly: e.ManualOnly})
			res.Stage = metadata.CatalogStageIdentifier
			return res, nil
		case err != nil && !errors.Is(err, database.ErrCatalogEntryNotFound):
			return res, err
		}
	}

	titles := newTitleKeys(q.Titles)
	if len(titles) == 0 {
		return res, nil
	}
	var picked []scoredEntry
	seen := map[string]bool{}
	for pos, credit := range q.Credits {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		fold, ids, oversize, err := blockFor(st, credit)
		if err != nil {
			return res, err
		}
		if oversize {
			res.OversizeBlocks++
			continue
		}
		if len(ids) == 0 {
			continue
		}
		live, err := liveEntries(ctx, st, ids)
		if err != nil {
			return res, err
		}
		asin := ""
		if pos < len(q.AuthorASINs) {
			asin = strings.ToUpper(strings.TrimSpace(q.AuthorASINs[pos]))
		}
		live, skipped, err := settleHomonyms(st, live, fold, authorjunk.FoldKey(credit), asin)
		if err != nil {
			return res, err
		}
		if skipped {
			res.HomonymSkipped++
			continue
		}
		for _, s := range scoreBlock(live, titles, q) {
			if seen[s.e.ID] {
				continue
			}
			seen[s.e.ID] = true
			s.pos = pos
			picked = append(picked, s)
		}
	}
	slices.SortStableFunc(picked, func(a, b scoredEntry) int {
		return cmp.Or(
			cmp.Compare(b.score, a.score),
			cmp.Compare(boolRank(b.seriesMatch), boolRank(a.seriesMatch)),
			cmp.Compare(a.pos, b.pos),
			cmp.Compare(a.e.ProviderID, b.e.ProviderID),
		)
	})
	if len(picked) > MaxCandidatesPerBook {
		picked = picked[:MaxCandidatesPerBook]
	}
	for _, s := range picked {
		res.Candidates = append(res.Candidates, EntryMetadata(*s.e))
		res.Origins = append(res.Origins, metadata.CatalogOrigin{Stage: metadata.CatalogStageAuthor, CreditPosition: s.pos, ManualOnly: s.e.ManualOnly})
	}
	if len(picked) > 0 {
		res.Stage = metadata.CatalogStageAuthor
	}
	return res, nil
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// creditFolds are the block folds a credit may name: its own, and, for a
// single "Last, First", the "First Last" reading (as authorjunk.SamePersonName
// reads it).
func creditFolds(credit string) []string {
	k := authorjunk.FoldKey(credit)
	if len(k) < minBlockFold {
		return nil
	}
	out := []string{k}
	if last, first, ok := strings.Cut(credit, ","); ok && !strings.Contains(first, ",") {
		if f := authorjunk.FoldKey(strings.TrimSpace(first) + " " + strings.TrimSpace(last)); len(f) >= minBlockFold && f != k {
			out = append(out, f)
		}
	}
	return out
}

// blockFor resolves a credit to its author block: the first of its folds that
// has an exact block, else the first that an alias names. It returns the fold
// the block is filed under, its entry ids, or oversize when the block is over
// MaxBlockEntries. Exact keys and one alias point read only: no scan.
func blockFor(st *database.CatalogStore, credit string) (fold string, ids []string, oversize bool, err error) {
	folds := creditFolds(credit)
	// A fold whose block is over the cap is only reported when no other
	// reading of the credit ("Last, First" as "First Last") has a block that
	// can be scored: the junk block must not hide the good one.
	oversizeFold := ""
	for _, f := range folds {
		ids, over, err := st.EntriesForAuthorKey(f, MaxBlockEntries)
		if err != nil {
			return "", nil, false, err
		}
		if over {
			if oversizeFold == "" {
				oversizeFold = f
			}
			continue
		}
		if len(ids) > 0 {
			return f, ids, false, nil
		}
	}
	for _, f := range folds {
		target, ok, err := st.ResolveAuthorAlias(f)
		if err != nil {
			return "", nil, false, err
		}
		if !ok || target == f {
			continue
		}
		ids, over, err := st.EntriesForAuthorKey(target, MaxBlockEntries)
		if err != nil {
			return "", nil, false, err
		}
		if over {
			if oversizeFold == "" {
				oversizeFold = target
			}
			continue
		}
		if len(ids) > 0 {
			return target, ids, false, nil
		}
	}
	if oversizeFold != "" {
		return oversizeFold, nil, true, nil
	}
	return "", nil, false, nil
}

// liveEntries loads the block's entries, leaving out stale ones (no harvest
// lists them any more) and index rows whose entry is gone.
func liveEntries(ctx context.Context, st *database.CatalogStore, ids []string) ([]*database.CatalogEntry, error) {
	out := make([]*database.CatalogEntry, 0, len(ids))
	for i, id := range ids {
		// A block can hold MaxBlockEntries ids; look at ctx every so often
		// rather than per read.
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		e, err := st.GetEntry(id)
		if errors.Is(err, database.ErrCatalogEntryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if e.StaleSince != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// blockAuthorIDs returns the provider author id the entry credits under the
// folded block name ("" when the provider gave none, or no credit folds so).
func blockAuthorID(e *database.CatalogEntry, blockFold string) string {
	for _, a := range e.Authors {
		if authorjunk.FoldKey(a.Name) == blockFold && a.ProviderAuthorID != "" {
			return strings.ToUpper(strings.TrimSpace(a.ProviderAuthorID))
		}
	}
	return ""
}

// settleHomonyms keeps a block from cross-matching two people who share a
// spelling, with no network. When the live entries credit the folded name to
// more than one provider author id, the block holds homonyms: it is narrowed
// to the id the book's author is known to have, or skipped (skipped = true)
// when nothing local chooses. The book's author id is wantASIN (the caller's,
// e.g. from the authority list) or, failing that, the one author ASIN the
// harvest resolved for the book's own name (CatalogAuthorState, written by
// disambig.go) when it resolved exactly one and saw no conflict.
func settleHomonyms(st *database.CatalogStore, live []*database.CatalogEntry, blockFold, creditFold, wantASIN string) ([]*database.CatalogEntry, bool, error) {
	ids := map[string]bool{}
	for _, e := range live {
		if id := blockAuthorID(e, blockFold); id != "" {
			ids[id] = true
		}
	}
	if len(ids) <= 1 {
		return live, false, nil
	}
	if wantASIN == "" {
		for _, f := range []string{creditFold, blockFold} {
			s, err := st.GetAuthorState(f)
			if err != nil {
				return nil, false, err
			}
			if s != nil && !s.Conflict && len(s.AuthorASINs) == 1 {
				wantASIN = strings.ToUpper(strings.TrimSpace(s.AuthorASINs[0]))
				break
			}
		}
	}
	if wantASIN == "" {
		return nil, true, nil
	}
	kept := live[:0:0]
	for _, e := range live {
		if blockAuthorID(e, blockFold) == wantASIN {
			kept = append(kept, e)
		}
	}
	return kept, false, nil
}
