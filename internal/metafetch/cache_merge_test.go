// file: internal/metafetch/cache_merge_test.go
// version: 1.0.0
// guid: 6f2d8a41-93c7-4e0b-b5a2-1d7c4e9f3a58
// last-edited: 2026-10-06

// The candidate fetch's provider fallback MERGES its answer into the book's
// cached candidates (SearchOptions.MergeWithCached). These pin the rows a
// merge must treat as the book's own: an unstamped row (B1), a row hashed from
// other inputs the caller vouched for (MergeFromSourceHash), and a legacy or
// prior-rule row, whose filtered candidates and dates a merge must not revive
// or re-date (B2). All fixtures are synthetic.
package metafetch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const (
	mergeBookID = "book-merge-1"
	mergeQuery  = "The Synthetic Example Book"
	mergeAuthor = "Jane Example"
)

func mergeCand(t *testing.T, source, title string, score float64) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(MetadataCandidate{Source: source, Title: title, Score: score})
	require.NoError(t, err)
	return raw
}

func candSources(t *testing.T, rows []json.RawMessage) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var c MetadataCandidate
		require.NoError(t, json.Unmarshal(r, &c))
		out = append(out, c.Source+":"+c.Title)
	}
	return out
}

// B1: a row written before 2026-10-05 has no FetchedForASIN. A fallback merge
// for a book that has an ASIN must merge into it, not replace it: the
// unstamped row was last vouched for the ASIN the book holds now.
func TestCacheSearchResponse_MergeIntoUnstampedRowKeepsCandidates(t *testing.T) {
	mfs := preserveFixture(t)
	seeded := mfs.cacheSearchResponse(mergeBookID, mergeQuery, mergeAuthor, "", "",
		&SearchMetadataResponse{Results: []MetadataCandidate{{Source: "Audible", Title: mergeQuery, Score: 0.6}}})
	require.Empty(t, seeded.FetchedForASIN, "fixture: the row is unstamped")

	got := mfs.cacheSearchResponse(mergeBookID, mergeQuery, mergeAuthor, "", "", &SearchMetadataResponse{
		Results: []MetadataCandidate{{Source: "Open Library", Title: mergeQuery, Score: 0.4}}, BookASIN: "B00CURRENT", mergeCached: true,
	})
	require.ElementsMatch(t, []string{"Audible:" + mergeQuery, "Open Library:" + mergeQuery}, candSources(t, got.Candidates))
	stored, err := mfs.db.GetMetadataCache(mergeBookID)
	require.NoError(t, err)
	require.Len(t, stored.Candidates, 2, "the stored row keeps the chain's candidate")
}

// A row stamped for ANOTHER ASIN is still replaced: its candidates were found
// for a record since taken off the book.
func TestCacheSearchResponse_MergeIntoOtherASINRowReplaces(t *testing.T) {
	mfs := preserveFixture(t)
	mfs.cacheSearchResponse(mergeBookID, mergeQuery, mergeAuthor, "", "", &SearchMetadataResponse{
		Results: []MetadataCandidate{{Source: "Audible", Title: mergeQuery, Score: 0.6}}, BookASIN: "B00OLDASIN",
	})
	got := mfs.cacheSearchResponse(mergeBookID, mergeQuery, mergeAuthor, "", "", &SearchMetadataResponse{
		Results: []MetadataCandidate{{Source: "Open Library", Title: mergeQuery, Score: 0.4}}, BookASIN: "B00CURRENT", mergeCached: true,
	})
	require.Equal(t, []string{"Open Library:" + mergeQuery}, candSources(t, got.Candidates))
}

// The batch fetch's verdict vouches for a row hashed from other inputs (the
// raw author credit before 2026-10-06's cleaning, or a pre-2026-09-28
// no-author row). A merge told that row's hash (MergeFromSourceHash) merges
// into it -- and an empty fallback answer keeps it -- instead of replacing
// the chain's candidates.
func TestCacheSearchResponse_MergeIntoVouchedRowUnderOtherHash(t *testing.T) {
	const rawCredit = "zz" + mergeAuthor
	put := func(mfs *Service) string {
		hash := hashSearchInputs(mergeBookID, mergeQuery, rawCredit, "", "")
		require.NoError(t, mfs.db.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: mergeBookID, FetchedAt: time.Now().UTC().Add(-48 * time.Hour), SourceHash: hash,
			Candidates: []json.RawMessage{mergeCand(t, "Audible", mergeQuery, 0.6)},
		}))
		return hash
	}

	mfs := preserveFixture(t)
	hash := put(mfs)
	got := mfs.cacheSearchResponse(mergeBookID, mergeQuery, mergeAuthor, "", "", &SearchMetadataResponse{
		Results: []MetadataCandidate{{Source: "Google Books", Title: mergeQuery, Score: 0.4}}, mergeCached: true, mergeFromHash: hash,
	})
	require.ElementsMatch(t, []string{"Audible:" + mergeQuery, "Google Books:" + mergeQuery}, candSources(t, got.Candidates))
	require.Equal(t, hashSearchInputs(mergeBookID, mergeQuery, mergeAuthor, "", ""), got.SourceHash,
		"the merged row is written under the search's own inputs")

	mfs = preserveFixture(t)
	hash = put(mfs)
	got = mfs.cacheSearchResponse(mergeBookID, mergeQuery, mergeAuthor, "", "", &SearchMetadataResponse{
		SourcesAnswered: []string{"Google Books"}, mergeCached: true, mergeFromHash: hash,
	})
	require.Equal(t, []string{"Audible:" + mergeQuery}, candSources(t, got.Candidates),
		"an empty fallback answer keeps the vouched row's candidates")
}

// B2: merging into a legacy (version "1") row filters the carried candidates
// by this version's position rules, keeps the row's FetchedAt and its legacy
// fingerprint: the sibling the old ladder pooled stays out of what the apply
// paths read (GetCachedCandidates), and nothing is re-dated as fresh.
func TestCacheSearchResponse_MergeIntoLegacyRowKeepsItFiltered(t *testing.T) {
	f := newVerdictFixture(t)
	const title = "Starfall Gambit 8: A Synthetic Saga"
	b, err := f.store.CreateBook(&database.Book{Title: title, FilePath: "/lib/sg/book.m4b"})
	require.NoError(t, err)
	book := f.book(b.ID)
	in := f.mfs.resolveSearchInputs(book, book.Title, "", "")
	legacy, current := in.legacyFingerprint(book.Title), in.fingerprint(book.Title)
	cand := func(title, pos string, score float64) json.RawMessage {
		raw, merr := json.Marshal(MetadataCandidate{Source: "Audible", Title: title, Author: "Ada Placeholder",
			Series: "Starfall Gambit", SeriesPosition: pos, Score: score})
		require.NoError(t, merr)
		return raw
	}
	sib := cand("Starfall Gambit 7: A Synthetic Saga", "7", 0.9)
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	put := func(fp string, cands ...json.RawMessage) {
		require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: b.ID, FetchedAt: old, SourceHash: hashSearchInputs(b.ID, book.Title, "", "", ""),
			SearchFingerprint: fp, Candidates: cands,
		}))
	}
	p := parseSearchTitle(title, "", "")
	c := newStrongCriteria(p, p.Title, title, "", "", 0)
	merge := func() *MetadataCandidateCache {
		return f.mfs.cacheSearchResponse(b.ID, book.Title, "", "", "", &SearchMetadataResponse{
			Results:          []MetadataCandidate{{Source: "Open Library", Title: title, Score: 0.4}},
			InputFingerprint: current, LegacyFingerprint: legacy, carryFilter: c.filterCarried, mergeCached: true,
		})
	}

	// The legacy row holds the sibling and the right book.
	right := cand(title, "8", 0.7)
	put(legacy, sib, right)
	merge()
	stored, err := f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Audible:" + title, "Open Library:" + title}, candSources(t, stored.Candidates),
		"the sibling the legacy filter drops is not carried")
	assert.Equal(t, legacy, stored.SearchFingerprint, "a legacy row keeps its fingerprint: its readers go on filtering it")
	assert.False(t, isCurrentFingerprint(stored.SearchFingerprint))
	assert.True(t, stored.FetchedAt.Equal(old), "carried candidates keep their date: FetchedAt = %v, want %v", stored.FetchedAt, old)
	read, _, err := f.mfs.GetCachedCandidates(b.ID)
	require.NoError(t, err)
	require.NotEmpty(t, read.Candidates)
	assert.NotEqual(t, sib, read.Candidates[0], "the apply paths' slot 0 is never the sibling")

	// The legacy row holds ONLY the sibling: nothing is carried, and the
	// row is this search's own answer.
	put(legacy, sib)
	got := merge()
	assert.Equal(t, []string{"Open Library:" + title}, candSources(t, got.Candidates))
	assert.Equal(t, current, got.SearchFingerprint)
}

// A prior-rule row (this version's stamp, other questions) is carried whole --
// its candidates stay valid -- but keeps its FetchedAt: they are not this
// search's fresh answer.
func TestCacheSearchResponse_MergeIntoPriorRowKeepsItsDate(t *testing.T) {
	mfs := preserveFixture(t)
	old := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Second)
	require.NoError(t, mfs.db.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: mergeBookID, FetchedAt: old, SourceHash: hashSearchInputs(mergeBookID, mergeQuery, mergeAuthor, "", ""),
		SearchFingerprint: FingerprintPrefix + "prior-questions",
		Candidates:        []json.RawMessage{mergeCand(t, "Audible", mergeQuery, 0.6)},
	}))
	got := mfs.cacheSearchResponse(mergeBookID, mergeQuery, mergeAuthor, "", "", &SearchMetadataResponse{
		Results:          []MetadataCandidate{{Source: "Open Library", Title: mergeQuery, Score: 0.4}},
		InputFingerprint: FingerprintPrefix + "current-questions", mergeCached: true,
	})
	require.Len(t, got.Candidates, 2)
	require.True(t, got.FetchedAt.Equal(old), "FetchedAt = %v, want %v", got.FetchedAt, old)
}

// S1: a merge ranks usable candidates first, whatever their score, so the
// row's slot 0 -- what the review lane and bulk apply read -- is a candidate
// the owner can use when there is one.
func TestMergeCandidateRows_UsableFirst(t *testing.T) {
	blocked := mergeCand(t, "Audible", "Rejected Example", 0.95)
	usableLow := mergeCand(t, "Open Library", "Usable Example", 0.5)
	usableHigh := mergeCand(t, "Audible", "Usable Example Two", 0.7)
	usable := func(c MetadataCandidate) bool { return c.Title != "Rejected Example" }

	got := mergeCandidateRows([]json.RawMessage{usableLow}, []json.RawMessage{blocked, usableHigh}, usable)
	assert.Equal(t, []string{"Audible:Usable Example Two", "Open Library:Usable Example", "Audible:Rejected Example"}, candSources(t, got))

	got = mergeCandidateRows([]json.RawMessage{usableLow}, []json.RawMessage{blocked, usableHigh}, nil)
	assert.Equal(t, "Audible:Rejected Example", candSources(t, got)[0], "nil ranks by score alone")
}
