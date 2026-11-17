// file: internal/metafetch/owed_sources_test.go
// version: 1.0.1
// guid: 7d2e9a40-3b15-4c86-a1f7-0e5c9d84b263
// last-edited: 2026-11-16

package metafetch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// A chain source that could not be asked is OWED an answer on the row that
// holds the other sources' candidates (database.MetadataCandidateCache.
// OwedSources). These pin the row bookkeeping (RecordChainOwed), the verdict
// that re-asks it alone (CachedBatchVerdict) and the merge that keeps the other
// sources' candidates (SearchOptions.MergeWithCached, the failed-source carry).
// A real PebbleStore: the persisted row is what they read.

const owedTitle = "Gotham Year One"

// owedFixture is a book with two title sources: "A" answers, "B" is the owed
// one. The first fetch is made with both answering unless failB is set.
type owedFixture struct {
	*verdictFixture
	bk   *database.Book
	a, b *verdictSource
	hash string
	t0   time.Time
}

func gothamAnswer(title, author string) []metadata.BookMetadata {
	return []metadata.BookMetadata{{Title: title, Author: author, Narrator: "N. Reader"}}
}

func newOwedFixture(t *testing.T, failB bool) *owedFixture {
	t.Helper()
	f := &owedFixture{verdictFixture: newVerdictFixture(t), t0: time.Now().UTC().Add(-time.Hour).Truncate(time.Second)}
	b, err := f.store.CreateBook(&database.Book{Title: owedTitle, FilePath: "/lib/gotham/book.m4b"})
	require.NoError(t, err)
	f.bk = b
	f.a = &verdictSource{name: "A", results: gothamAnswer(owedTitle, "Bruce Wayne")}
	f.b = &verdictSource{name: "B", results: gothamAnswer(owedTitle+" (Special Edition)", "Bruce Wayne")}
	if failB {
		f.b.byTitleErr = &metadata.ProviderStatusError{Provider: "b", Status: 503, Body: "unavailable"}
	}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{f.a, f.b})
	f.hash = BatchSourceHash(b.ID, owedTitle, "")
	withNow(t, f.t0)
	return f
}

// fetch runs one search like the batch op's chain step does (the named sources
// only, merged into the row when merge is set) and returns the row and response.
func (f *owedFixture) fetch(only []string, merge bool) (*MetadataCandidateCache, *SearchMetadataResponse) {
	f.t.Helper()
	entry, resp, err := f.mfs.FetchAndCacheWithResponse(context.Background(), nil, f.bk.ID, owedTitle, "", "", "",
		SearchOptions{OnlySources: only, MergeWithCached: merge})
	require.NoError(f.t, err)
	return entry, resp
}

func (f *owedFixture) row() *MetadataCandidateCache {
	f.t.Helper()
	e, err := f.store.GetMetadataCache(f.bk.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, e)
	return e
}

func (f *owedFixture) sources(e *MetadataCandidateCache) []string {
	var out []string
	for _, raw := range e.Candidates {
		var c struct {
			Source string `json:"source"`
		}
		require.NoError(f.t, json.Unmarshal(raw, &c))
		out = append(out, c.Source)
	}
	return out
}

func (f *owedFixture) record(resp *SearchMetadataResponse, u OwedUpdate) {
	f.t.Helper()
	require.NoError(f.t, f.mfs.RecordChainOwed(f.bk.ID, f.hash, resp.InputFingerprint, u))
}

func TestRecordChainOwed_FailureCountsAsksAndAnswerClears(t *testing.T) {
	f := newOwedFixture(t, true)
	_, resp := f.fetch(nil, false)
	require.Contains(t, resp.SourcesFailed, "B")

	f.record(resp, OwedUpdate{Failed: map[string]string{"B": metadata.CauseUpstream}, Answered: []string{"A"}})
	o := f.row().OwedSources["B"]
	require.Equal(t, 1, o.Attempts)
	require.True(t, o.Since.Equal(f.t0) && o.LastAttempt.Equal(f.t0), "first failure dates Since and LastAttempt: %+v", o)
	require.Equal(t, metadata.CauseUpstream, o.Cause)

	// A later failure of the same source: one more ask, Since does not move.
	withNow(t, f.t0.Add(7*time.Hour))
	f.record(resp, OwedUpdate{Failed: map[string]string{"B": metadata.CauseTimeout}})
	o = f.row().OwedSources["B"]
	require.Equal(t, 2, o.Attempts)
	require.True(t, o.Since.Equal(f.t0), "Since moved: %+v", o)
	require.True(t, o.LastAttempt.Equal(f.t0.Add(7*time.Hour)))
	require.Equal(t, metadata.CauseTimeout, o.Cause)

	// A hold sent nothing: the backoff restarts, the ask is not spent.
	withNow(t, f.t0.Add(14*time.Hour))
	f.record(resp, OwedUpdate{Failed: map[string]string{"B": metadata.CauseHeld}})
	o = f.row().OwedSources["B"]
	require.Equal(t, 2, o.Attempts, "a hold used up an ask")
	require.True(t, o.LastAttempt.Equal(f.t0.Add(14*time.Hour)))

	// It answers: no longer owed. An answer for a source that was never owed
	// changes nothing.
	f.record(resp, OwedUpdate{Answered: []string{"B", "A"}})
	require.Empty(t, f.row().OwedSources)
}

func TestRecordChainOwed_OnlyWritesTheRowItWasAskedFor(t *testing.T) {
	f := newOwedFixture(t, true)
	_, resp := f.fetch(nil, false)
	u := OwedUpdate{Failed: map[string]string{"B": metadata.CauseUpstream}}

	// Another book's inputs, or another question: nothing is written.
	require.NoError(t, f.mfs.RecordChainOwed(f.bk.ID, "some-other-hash", resp.InputFingerprint, u))
	require.NoError(t, f.mfs.RecordChainOwed(f.bk.ID, f.hash, "some-other-fingerprint", u))
	require.NoError(t, f.mfs.RecordChainOwed(f.bk.ID, f.hash, resp.InputFingerprint, OwedUpdate{}))
	require.NoError(t, f.mfs.RecordChainOwed("no-such-book", f.hash, resp.InputFingerprint, u))
	require.Empty(t, f.row().OwedSources)

	var nilSvc *Service
	require.NoError(t, nilSvc.RecordChainOwed(f.bk.ID, f.hash, resp.InputFingerprint, u))
}

// owe puts B on the row as owed, asked n times, last at at.
func (f *owedFixture) owe(n int, at time.Time) {
	f.t.Helper()
	e := f.row()
	e.OwedSources = map[string]database.OwedSource{"B": {Since: f.t0, LastAttempt: at, Attempts: n, Cause: metadata.CauseUpstream}}
	require.NoError(f.t, f.store.PutMetadataCache(e))
}

func (f *owedFixture) verdict() (BatchVerdict, []string) {
	f.t.Helper()
	_, v, ask := f.mfs.CachedBatchVerdict(f.verdictFixture.book(f.bk.ID), owedTitle, "")
	return v, ask
}

// The verdict: a row that holds candidates is fresh until a source it owes is
// due, then it names that source alone. Every non-obvious edge is pinned.
func TestBatchVerdict_OwedSourceIsReaskedAloneWhenDue(t *testing.T) {
	f := newOwedFixture(t, true)
	f.fetch(nil, false)
	require.NotEmpty(t, f.row().Candidates, "A's candidate is what the row is parked on")

	// A row written before the field existed owes nothing: fresh, as before.
	v, ask := f.verdict()
	require.Equal(t, BatchVerdictFreshCandidates, v)
	require.Empty(t, ask)

	// Owed, asked once at t0: 6h backoff.
	f.owe(1, f.t0)
	withNow(t, f.t0.Add(6*time.Hour-time.Second))
	v, _ = f.verdict()
	require.Equal(t, BatchVerdictFreshCandidates, v, "re-asked before its backoff ran out")
	withNow(t, f.t0.Add(6*time.Hour))
	v, ask = f.verdict()
	require.Equal(t, BatchVerdictNone, v)
	require.Equal(t, []string{"B"}, ask, "only the owed source is asked")

	// The schedule: after the 2nd failed ask 24h, then 72h, then 168h.
	for attempts, wait := range map[int]time.Duration{2: 24 * time.Hour, 3: 72 * time.Hour, 4: 168 * time.Hour} {
		f.owe(attempts, f.t0)
		withNow(t, f.t0.Add(wait-time.Second))
		v, _ = f.verdict()
		require.Equal(t, BatchVerdictFreshCandidates, v, "attempts=%d re-asked early", attempts)
		withNow(t, f.t0.Add(wait))
		v, ask = f.verdict()
		require.Equal(t, BatchVerdictNone, v, "attempts=%d not re-asked when due", attempts)
		require.Equal(t, []string{"B"}, ask)
	}

	// The cap: a source that used all its asks is left alone, however old.
	f.owe(database.OwedMaxAsks, f.t0)
	withNow(t, f.t0.Add(100*24*time.Hour))
	v, ask = f.verdict()
	require.Equal(t, BatchVerdictFreshCandidates, v, "an exhausted source was re-asked")
	require.Empty(t, ask)

	// A source the chain no longer has cannot be re-asked.
	f.owe(1, f.t0)
	e := f.row()
	e.OwedSources = map[string]database.OwedSource{"Gone": {Since: f.t0, LastAttempt: f.t0, Attempts: 1}}
	require.NoError(t, f.store.PutMetadataCache(e))
	v, _ = f.verdict()
	require.Equal(t, BatchVerdictFreshCandidates, v)
}

// The re-ask asks only B, keeps A's candidate, adds B's, and clears the debt;
// A is not called again.
func TestOwedReask_MergesIntoTheRowAndAsksOnlyTheOwedSource(t *testing.T) {
	f := newOwedFixture(t, true)
	_, resp := f.fetch(nil, false)
	f.record(resp, OwedUpdate{Failed: map[string]string{"B": metadata.CauseUpstream}, Answered: []string{"A"}})
	require.ElementsMatch(t, []string{"A"}, f.sources(f.row()))
	aCalls := f.a.calls.Load()

	// B recovers; its backoff has run out.
	f.b.byTitleErr = nil
	withNow(t, f.t0.Add(7*time.Hour))
	v, ask := f.verdict()
	require.Equal(t, BatchVerdictNone, v)
	require.Equal(t, []string{"B"}, ask)
	_, resp = f.fetch(ask, true)
	f.record(resp, OwedUpdate{Answered: resp.AnsweredSources()})

	got := f.row()
	require.ElementsMatch(t, []string{"A", "B"}, f.sources(got), "the re-ask lost A's candidate or missed B's")
	require.Empty(t, got.OwedSources, "B answered; it is no longer owed")
	require.Equal(t, aCalls, f.a.calls.Load(), "A was asked again for B's re-ask")
	v, _ = f.verdict()
	require.Equal(t, BatchVerdictFreshCandidates, v)
}

// A source that fails in a later search must not take its earlier candidates
// with it: they are carried onto the new row, and the row still owes it.
func TestCacheSearch_FailedSourceKeepsItsEarlierCandidates(t *testing.T) {
	f := newOwedFixture(t, false)
	f.fetch(nil, false)
	require.ElementsMatch(t, []string{"A", "B"}, f.sources(f.row()))

	f.b.byTitleErr = &metadata.ProviderStatusError{Provider: "b", Status: 503, Body: "unavailable"}
	withNow(t, f.t0.Add(time.Hour))
	_, resp, _ := f.mfs.FetchAndCacheWithResponse(context.Background(), nil, f.bk.ID, owedTitle, "", "", "", SearchOptions{BypassFetchCache: true})
	require.Contains(t, resp.SourcesFailed, "B")
	require.ElementsMatch(t, []string{"A", "B"}, f.sources(f.row()), "B's earlier candidate was dropped because B failed this time")

	// The same search with a DIFFERENT question does not carry them: they
	// answered another one. The book's own title is part of the questions (the
	// search fingerprint) but not of the row's source hash, which hashes the
	// caller's query: the row is the same one and only the fingerprint tells
	// the questions apart.
	retitled := *f.bk
	retitled.Title = owedTitle + " (Retitled)"
	_, err := f.store.UpdateBook(f.bk.ID, &retitled)
	require.NoError(t, err)
	_, resp, _ = f.mfs.FetchAndCacheWithResponse(context.Background(), nil, f.bk.ID, owedTitle, "", "", "",
		SearchOptions{BypassFetchCache: true})
	require.Contains(t, resp.SourcesFailed, "B")
	require.NotEqual(t, f.row().SearchFingerprint, "")
	require.NotContains(t, f.sources(f.row()), "B", "B's candidate from another question was carried")
}
