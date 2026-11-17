// file: internal/metafetch/owed_sources_test.go
// version: 1.2.0
// guid: 7d2e9a40-3b15-4c86-a1f7-0e5c9d84b263
// last-edited: 2026-11-17

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

func (f *owedFixture) record(resp *SearchMetadataResponse, u OwedUpdate) (gaveUp []string) {
	f.t.Helper()
	gaveUp, err := f.mfs.RecordChainOwed(f.bk.ID, f.hash, resp.InputFingerprint, u)
	require.NoError(f.t, err)
	return gaveUp
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
	for _, c := range []struct {
		id, hash, fp string
		u            OwedUpdate
	}{
		{f.bk.ID, "some-other-hash", resp.InputFingerprint, u},
		{f.bk.ID, f.hash, "some-other-fingerprint", u},
		{f.bk.ID, f.hash, resp.InputFingerprint, OwedUpdate{}},
		{"no-such-book", f.hash, resp.InputFingerprint, u},
	} {
		gaveUp, err := f.mfs.RecordChainOwed(c.id, c.hash, c.fp, c.u)
		require.NoError(t, err)
		require.Empty(t, gaveUp)
	}
	require.Empty(t, f.row().OwedSources)

	var nilSvc *Service
	_, err := nilSvc.RecordChainOwed(f.bk.ID, f.hash, resp.InputFingerprint, u)
	require.NoError(t, err)
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

// Every writer of the row -- the batch op, the search dialog -- hashes different
// hints into it, yet asks the same questions: the debt must survive a row
// rewritten under another input hash, and be cleared by one that hears the source.
func TestCacheSearch_OwedDebtSurvivesAnotherInputHash(t *testing.T) {
	f := newOwedFixture(t, true)
	_, resp := f.fetch(nil, false)
	f.record(resp, OwedUpdate{Failed: map[string]string{"B": metadata.CauseUpstream}, Answered: []string{"A"}})
	before := f.row().OwedSources["B"]
	require.Equal(t, 1, before.Attempts)

	// The dialog's plain fetch: no query, so another SourceHash for the same book.
	withNow(t, f.t0.Add(time.Hour))
	_, resp2, err := f.mfs.FetchAndCacheWithResponse(context.Background(), nil, f.bk.ID, "", "", "", "", SearchOptions{})
	require.NoError(t, err)
	require.Contains(t, resp2.SourcesFailed, "B")
	require.NotEqual(t, f.hash, f.row().SourceHash, "the fixture must exercise a different hash")
	require.Equal(t, map[string]database.OwedSource{"B": before}, f.row().OwedSources,
		"a row rewritten under another input hash forgot the debt")

	// B is back and the same dialog search hears it: no longer owed.
	f.b.byTitleErr = nil
	withNow(t, f.t0.Add(2*time.Hour))
	_, _, err = f.mfs.FetchAndCacheWithResponse(context.Background(), nil, f.bk.ID, "", "", "", "", SearchOptions{})
	require.NoError(t, err)
	require.Empty(t, f.row().OwedSources, "a search that heard B left it owed")
}

// The debt belongs to the questions: asked about another title it is dropped,
// and so is the debt of a row past its TTL, whose failing source starts a fresh
// schedule (the rule OwedSource.Exhausted documents).
func TestCacheSearch_OwedDebtIsDroppedForOtherQuestionsAndPastTheTTL(t *testing.T) {
	f := newOwedFixture(t, true)
	_, resp := f.fetch(nil, false)
	f.record(resp, OwedUpdate{Failed: map[string]string{"B": metadata.CauseUpstream}, Answered: []string{"A"}})

	// Past the TTL: not carried.
	withNow(t, f.t0.Add(database.MetadataCacheTTL+time.Hour))
	f.fetch(nil, false)
	require.Empty(t, f.row().OwedSources, "a row past its TTL carried its debt")
	// The failed source is then recorded afresh, as a first failure.
	gaveUp := f.record(resp, OwedUpdate{Failed: map[string]string{"B": metadata.CauseUpstream}, Answered: []string{"A"}})
	require.Empty(t, gaveUp)
	o := f.row().OwedSources["B"]
	require.Equal(t, 1, o.Attempts)
	require.False(t, o.Exhausted())

	// Another title: the questions differ, the debt goes.
	withNow(t, f.t0.Add(database.MetadataCacheTTL+2*time.Hour))
	retitled := *f.bk
	retitled.Title = owedTitle + " (Retitled)"
	_, err := f.store.UpdateBook(f.bk.ID, &retitled)
	require.NoError(t, err)
	f.fetch(nil, false)
	require.Empty(t, f.row().OwedSources, "the debt of other questions was carried")
}

// The fifth failed ask is the last, and says so once; a spent source is not
// charged on, and an explicit retry (Reset) starts its schedule over.
func TestRecordChainOwed_GivingUpIsReportedOnceAndAResetStartsOver(t *testing.T) {
	f := newOwedFixture(t, true)
	_, resp := f.fetch(nil, false)
	fail := OwedUpdate{Failed: map[string]string{"B": metadata.CauseTimeout}}

	f.owe(database.OwedMaxAsks-1, f.t0)
	withNow(t, f.t0.Add(8*24*time.Hour))
	require.Equal(t, []string{"B"}, f.record(resp, fail), "the last ask did not report giving up")
	require.Equal(t, database.OwedMaxAsks, f.row().OwedSources["B"].Attempts)
	require.True(t, f.row().OwedSources["B"].Exhausted())
	require.Equal(t, []string{"B"}, f.row().OwedExhaustedSources())
	require.Empty(t, f.row().OwedPendingSources())

	// Past the cap: reported no more, not charged on.
	withNow(t, f.t0.Add(9*24*time.Hour))
	require.Empty(t, f.record(resp, fail))
	require.Equal(t, database.OwedMaxAsks, f.row().OwedSources["B"].Attempts, "a spent source was charged past the cap")

	// An owner retry that fails again restarts the schedule: Attempts 1, Since now.
	reset := fail
	reset.Reset = true
	withNow(t, f.t0.Add(10*24*time.Hour))
	require.Empty(t, f.record(resp, reset))
	o := f.row().OwedSources["B"]
	require.Equal(t, 1, o.Attempts, "a forced retry stepped on a spent schedule")
	require.True(t, o.Since.Equal(f.t0.Add(10*24*time.Hour)), "Since did not restart: %+v", o)
	require.False(t, o.Exhausted())
	require.Equal(t, []string{"B"}, f.row().OwedPendingSources())
	// A Reset on a source that is not owed is just its first failure.
	f.owe(1, f.t0)
	withNow(t, f.t0.Add(11*24*time.Hour))
	require.Empty(t, f.record(resp, reset))
	require.Equal(t, 1, f.row().OwedSources["B"].Attempts)
}

// One window is one ask: a writer that read the row before another charged it
// (OwedUpdate.Seen) charges nothing when it records; one that read it as it is
// still charges; a caller that read nothing (nil Seen) always does.
func TestRecordChainOwed_SeenGuardChargesOneAskPerWindow(t *testing.T) {
	f := newOwedFixture(t, true)
	_, resp := f.fetch(nil, false)
	f.owe(1, f.t0)
	fail := func(seen map[string]time.Time) OwedUpdate {
		return OwedUpdate{Failed: map[string]string{"B": metadata.CauseUpstream}, Seen: seen}
	}
	seen := map[string]time.Time{"B": f.t0} // what two askers both read

	withNow(t, f.t0.Add(7*time.Hour))
	f.record(resp, fail(seen))
	require.Equal(t, 2, f.row().OwedSources["B"].Attempts)
	first := f.row().OwedSources["B"].LastAttempt

	// The second asker of the same window: charged nothing, row untouched.
	withNow(t, f.t0.Add(7*time.Hour+time.Minute))
	f.record(resp, fail(seen))
	o := f.row().OwedSources["B"]
	require.Equal(t, 2, o.Attempts, "two askers of one window charged two asks")
	require.True(t, o.LastAttempt.Equal(first), "the skipped charge moved the row")

	// A fresh read of the row charges the next window.
	withNow(t, f.t0.Add(32*time.Hour))
	f.record(resp, fail(map[string]time.Time{"B": first}))
	require.Equal(t, 3, f.row().OwedSources["B"].Attempts)
	// nil Seen: unconditional.
	withNow(t, f.t0.Add(33*time.Hour))
	f.record(resp, fail(nil))
	require.Equal(t, 4, f.row().OwedSources["B"].Attempts)
	// A source the row no longer owes is charged as a first failure whatever the
	// asker saw (a TTL refetch dropped the debt it read).
	e := f.row()
	e.OwedSources = nil
	require.NoError(t, f.store.PutMetadataCache(e))
	f.record(resp, fail(map[string]time.Time{"B": f.t0}))
	require.Equal(t, 1, f.row().OwedSources["B"].Attempts)
}

// A full search asks Audnexus only through the direct ASIN lookup, which is no
// title question (not in SourcesAsked): hearing from it must still clear the
// debt, and a lookup that failed must not.
func TestAnsweredSources_CountsTheDirectASINLookup(t *testing.T) {
	resp := &SearchMetadataResponse{
		SourcesAsked:        []string{"Audible"},
		SourcesAnswered:     []string{"Audible", "Audnexus (Audible)"},
		SourcesASINAnswered: []string{"Audnexus (Audible)"},
	}
	require.ElementsMatch(t, []string{"Audible", "Audnexus (Audible)"}, resp.AnsweredSources())
	// Without the lookup's answer, an ASIN-only source that sent nothing is not heard from.
	resp.SourcesASINAnswered = nil
	require.Equal(t, []string{"Audible"}, resp.AnsweredSources())
	// A lookup that failed is not an answer.
	resp.SourcesASINAnswered = []string{"Audnexus (Audible)"}
	resp.SourcesFailed = map[string]string{"Audnexus (Audible)": "502"}
	require.Equal(t, []string{"Audible"}, resp.AnsweredSources())
}

// OwedUpdateOf names only the chain's own sources as owed, each with its cause.
func TestOwedUpdateOf_OnlyActiveChainSourcesAreOwed(t *testing.T) {
	resp := &SearchMetadataResponse{SourcesFailed: map[string]string{"A": "502", "Some Other Label": "boom"}}
	u := OwedUpdateOf(resp, []string{"A", "B"})
	require.Len(t, u.Failed, 1)
	require.NotEmpty(t, u.Failed["A"])
	require.Equal(t, OwedUpdate{}, OwedUpdateOf(nil, nil))
}

// Service.Now is the overridable clock itself, the one callers outside this
// package (the candidate fetch's selection) use to agree with the verdict.
func TestServiceNow_IsTheOverridableClock(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	restore := SetClockForTesting(func() time.Time { return at })
	require.True(t, (&Service{}).Now().Equal(at))
	restore()
	require.WithinDuration(t, time.Now(), (&Service{}).Now(), time.Minute)
}

// A failed ask is charged to the row it was selected from, whoever wrote it. A
// row under another input hash (the dialog's) is the same row when it already
// owes, was fetched for the same ASIN and is inside the TTL; a row that owes
// nothing, another ASIN, a past-TTL row and another question set are not.
func TestRecordChainOwed_ChargesTheSelectedRowWhateverItsHash(t *testing.T) {
	fail := OwedUpdate{Failed: map[string]string{"B": metadata.CauseUpstream}}
	for _, tc := range []struct {
		name    string
		mutate  func(e *MetadataCandidateCache)
		asin    string
		fp      func(string) string
		charged bool
	}{
		{"other hash, owes, same ASIN", func(e *MetadataCandidateCache) {}, "", nil, true},
		{"other hash, row fetched for the same ASIN", func(e *MetadataCandidateCache) { e.FetchedForASIN = "B0TESTASIN" }, "b0testasin", nil, true},
		{"other hash, other ASIN", func(e *MetadataCandidateCache) { e.FetchedForASIN = "B0OTHERASN" }, "B0TESTASIN", nil, false},
		{"other hash, past the TTL", func(e *MetadataCandidateCache) {
			e.FetchedAt = nowUTC().Add(-database.MetadataCacheTTL - time.Hour)
		}, "", nil, false},
		{"other hash, owes nothing", func(e *MetadataCandidateCache) { e.OwedSources = nil }, "", nil, false},
		{"other hash, other questions", func(e *MetadataCandidateCache) {}, "", func(fp string) string { return fp + "x" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwedFixture(t, true)
			_, resp := f.fetch(nil, false)
			f.owe(1, f.t0)
			e := f.row()
			e.SourceHash = "another-writers-hash"
			e.FetchedAt = nowUTC()
			tc.mutate(e)
			require.NoError(t, f.store.PutMetadataCache(e))
			fp := resp.InputFingerprint
			if tc.fp != nil {
				fp = tc.fp(fp)
			}
			u := fail
			u.ASIN = tc.asin
			withNow(t, f.t0.Add(7*time.Hour))
			_, err := f.mfs.RecordChainOwed(f.bk.ID, f.hash, fp, u)
			require.NoError(t, err)
			got := f.row().OwedSources["B"].Attempts
			if tc.charged {
				require.Equal(t, 2, got, "a failed ask was not charged to the row it was selected from")
				require.True(t, f.row().OwedSources["B"].LastAttempt.After(f.t0), "the last attempt did not move")
			} else if tc.name != "other hash, owes nothing" {
				require.Equal(t, 1, got, "a row that is not the selected one was charged")
			} else {
				require.Empty(t, f.row().OwedSources, "a row that owed nothing was given a debt")
			}
		})
	}
}
