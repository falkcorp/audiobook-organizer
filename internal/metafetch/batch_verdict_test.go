// file: internal/metafetch/batch_verdict_test.go
// version: 1.3.0
// guid: fcb57db8-150d-40b2-a501-0934fa3be00b
// last-edited: 2026-10-01

// Tests for the batch candidate fetch's durable verdicts (CachedBatchVerdict):
// when a book may be answered from the candidate cache without a provider
// call, and which providers must be asked when it may not. They use a real
// PebbleStore because the persisted entry is what the verdict reads.
package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// verdictSource counts live calls. byAuthorErr/byTitleErr make that step fail.
type verdictSource struct {
	name        string
	calls       atomic.Int64
	results     []metadata.BookMetadata
	byAuthorErr error
	byTitleErr  error
}

func (v *verdictSource) Name() string { return v.name }
func (v *verdictSource) SearchByTitle(_ context.Context, _ string) ([]metadata.BookMetadata, error) {
	v.calls.Add(1)
	if v.byTitleErr != nil {
		return nil, v.byTitleErr
	}
	return v.results, nil
}
func (v *verdictSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	v.calls.Add(1)
	if v.byAuthorErr != nil {
		return nil, v.byAuthorErr
	}
	return v.results, nil
}

type verdictFixture struct {
	t     *testing.T
	store *database.PebbleStore
	mfs   *Service
}

func newVerdictFixture(t *testing.T) *verdictFixture {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))
	t.Cleanup(func() { _ = store.Close() })
	return &verdictFixture{t: t, store: store, mfs: NewService(store)}
}

func (f *verdictFixture) book(id string) *database.Book {
	f.t.Helper()
	b, err := f.store.GetBookByID(id)
	require.NoError(f.t, err)
	require.NotNil(f.t, b)
	return b
}

// batchFetch mirrors fetchCandidateForBook's use of the verdict: skip on a
// verdict, otherwise search (only the named sources when the verdict says so).
func (f *verdictFixture) batchFetch(id string) BatchVerdict {
	f.t.Helper()
	b := f.book(id)
	_, verdict, ask := f.mfs.CachedBatchVerdict(b, b.Title, "")
	if verdict == BatchVerdictFreshCandidates || verdict == BatchVerdictKnownEmpty {
		return verdict
	}
	_, err := f.mfs.FetchAndCacheLimited(context.Background(), nil, id, b.Title, "", "", "", SearchOptions{OnlySources: ask})
	// A search no source answered writes nothing and says so; that is the
	// outcome, not a fixture failure.
	if !errors.Is(err, ErrNoSourceAnswered) {
		require.NoError(f.t, err)
	}
	return verdict
}

func withNow(t *testing.T, at time.Time) {
	t.Helper()
	prev := nowUTC
	nowUTC = func() time.Time { return at }
	t.Cleanup(func() { nowUTC = prev })
}

// A provider whose ladder did not finish has not answered. Two ways the
// narrator-as-author step used to let one through: a throttle/breaker there
// closed the ladder without recording an error, and a plain error there was
// not recorded at all. With no author, that step is the first query, so the
// provider was marked "answered" after zero successful queries.
func TestBatchVerdict_NarratorStepFailureIsNotAnAnswer(t *testing.T) {
	for name, stepErr := range map[string]error{
		"throttled": metadata.ErrProviderThrottled,
		"circuit":   metadata.ErrCircuitOpen,
		"plain":     errors.New("502 bad gateway"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newVerdictFixture(t)
			narrator := "Some Narrator"
			b, err := f.store.CreateBook(&database.Book{Title: "Narrated Only", FilePath: "/lib/n/" + name + ".m4b", Narrator: &narrator})
			require.NoError(t, err)
			src := &verdictSource{name: "Src", byAuthorErr: stepErr}
			f.mfs.SetOverrideSources([]metadata.MetadataSource{src})

			f.batchFetch(b.ID)
			_, verdict, _ := f.mfs.CachedBatchVerdict(f.book(b.ID), b.Title, "")
			require.NotEqual(t, BatchVerdictKnownEmpty, verdict,
				"a provider whose narrator-as-author step failed (%v) was recorded as having answered", stepErr)
		})
	}
}

// The verdict must be bound to the inputs the search actually used. The
// author comes from AuthorID (GetBookByID leaves book.Author unhydrated), so
// the cache SourceHash never saw it: renaming the author kept the verdict.
func TestBatchVerdict_AuthorChangeReopensVerdict(t *testing.T) {
	for name, results := range map[string][]metadata.BookMetadata{
		"known_empty": nil,
		"candidates":  {{Title: "Author Swap", Author: "First Author"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newVerdictFixture(t)
			first, err := f.store.CreateAuthor("First Author")
			require.NoError(t, err)
			b, err := f.store.CreateBook(&database.Book{Title: "Author Swap", FilePath: "/lib/a/" + name + ".m4b", AuthorID: &first.ID})
			require.NoError(t, err)
			f.mfs.SetOverrideSources([]metadata.MetadataSource{&verdictSource{name: "Src", results: results}})

			f.batchFetch(b.ID)
			_, verdict, _ := f.mfs.CachedBatchVerdict(f.book(b.ID), b.Title, "")
			require.Contains(t, []BatchVerdict{BatchVerdictKnownEmpty, BatchVerdictFreshCandidates}, verdict, "fixture: first fetch must leave a verdict")

			// Renaming the author touches no book row, so nothing invalidates
			// the book's cache entry (UpdateBook would have dropped it).
			require.NoError(t, f.store.UpdateAuthorName(first.ID, "Renamed Author"))
			_, verdict, _ = f.mfs.CachedBatchVerdict(f.book(b.ID), b.Title, "")
			require.Equal(t, BatchVerdictNone, verdict, "a verdict for the old author name was reused after the author was renamed")
		})
	}
}

// Each provider's "nothing" ages on its own, and only providers without a
// valid answer are asked again. A quota-starved provider (Google Books) must
// not make the others be re-asked every run, and an old answer from one
// provider must not be kept alive by a newer answer from another.
func TestBatchVerdict_PerProviderAnswersAgeAndOnlyMissingAreAsked(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: "Nobody Has It", FilePath: "/lib/p/book.m4b"})
	require.NoError(t, err)
	a := &verdictSource{name: "A"}
	g := &verdictSource{name: "G", byTitleErr: metadata.ErrProviderThrottled}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{a, g})

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withNow(t, t0)
	f.batchFetch(b.ID) // A answers empty; G is out of quota.
	require.Positive(t, a.calls.Load())

	withNow(t, t0.Add(80*24*time.Hour))
	g.byTitleErr = nil
	aBefore := a.calls.Load()
	_, verdict, ask := f.mfs.CachedBatchVerdict(f.book(b.ID), b.Title, "")
	require.NotEqual(t, BatchVerdictKnownEmpty, verdict)
	require.Equal(t, []string{"G"}, ask, "only the provider without an answer should be asked")
	f.batchFetch(b.ID)
	require.Equal(t, aBefore, a.calls.Load(), "provider A already answered these inputs and was asked again")
	require.Positive(t, g.calls.Load())

	_, verdict, _ = f.mfs.CachedBatchVerdict(f.book(b.ID), b.Title, "")
	require.Equal(t, BatchVerdictKnownEmpty, verdict, "every provider has now answered")

	// Day 95: A's answer (day 0) is past the backstop; G's (day 80) is not.
	withNow(t, t0.Add(95*24*time.Hour))
	_, verdict, ask = f.mfs.CachedBatchVerdict(f.book(b.ID), b.Title, "")
	require.NotEqual(t, BatchVerdictKnownEmpty, verdict, "A's 95-day-old answer was kept alive by G's newer one")
	sort.Strings(ask)
	require.Equal(t, []string{"A"}, ask)
}

// A zero-candidate entry written before answers were recorded per provider
// cannot say whether any provider answered (the pre-fix path wrote one even
// when every provider failed), so it is not a verdict: ask everyone.
func TestBatchVerdict_LegacyEmptyEntryIsNotKnownEmpty(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: "Legacy Empty", FilePath: "/lib/l/book.m4b"})
	require.NoError(t, err)
	f.mfs.SetOverrideSources([]metadata.MetadataSource{&verdictSource{name: "A"}})
	now := time.Now().UTC()
	require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID:           b.ID,
		FetchedAt:        now,
		SourceHash:       hashSearchInputs(b.ID, b.Title, "", "", ""),
		LastEmptyFetchAt: &now,
	}))
	_, verdict, _ := f.mfs.CachedBatchVerdict(f.book(b.ID), b.Title, "")
	require.Equal(t, BatchVerdictNone, verdict)
}

// A row the batch fetch wrote before 2026-09-28 was hashed with no author
// (GetBookByID leaves Book.Author nil, so the hint was ""), for a book that
// has one. The fetch now hashes the live author. The batch verdict and the
// apply planner must read that row the same way: before, the verdict served
// it as fresh (never refetched) while the apply gate read it as
// identity_stale, so the book was stuck until the row expired. Its search
// fingerprint bound the author resolved from AuthorID, so it is accepted by
// both while that author is unchanged, and stale to both once it changes. A
// no-author row with no fingerprint proves nothing and stays stale.
func TestBatchVerdict_PreDeployNoAuthorRowAgreesWithApply(t *testing.T) {
	f := newVerdictFixture(t)
	pkd, err := f.store.CreateAuthor("Philip K. Dick")
	require.NoError(t, err)
	b, err := f.store.CreateBook(&database.Book{Title: "Valis", FilePath: "/lib/v/valis.m4b", AuthorID: &pkd.ID})
	require.NoError(t, err)
	src := &verdictSource{name: "Src", results: []metadata.BookMetadata{{Title: "Valis", Author: "Philip K. Dick"}}}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{src})
	f.batchFetch(b.ID) // the pre-deploy shape: hint ""

	judge := func() (verdict BatchVerdict, idErr error, queryMatch bool) {
		t.Helper()
		book := f.book(b.ID)
		live, lerr := database.LiveBookAuthorNames(f.store, book)
		require.NoError(t, lerr)
		require.NotEmpty(t, live)
		_, verdict, _ = f.mfs.CachedBatchVerdict(book, book.Title, SearchAuthorHint(live[0]))
		entry, gerr := f.store.GetMetadataCache(b.ID)
		require.NoError(t, gerr)
		return verdict, f.mfs.ValidateCachedIdentityForBook(entry, book, live),
			f.mfs.CachedQueryMatchesIdentity(entry, book, live, book.Title)
	}

	verdict, idErr, queryMatch := judge()
	require.Equal(t, BatchVerdictFreshCandidates, verdict, "the row is the book's: serve it")
	require.NoError(t, idErr, "the apply gate must accept the row the verdict served")
	require.True(t, queryMatch)

	// Without a fingerprint the no-author hash proves nothing about the author.
	entry, err := f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	saved := entry.SearchFingerprint
	entry.SearchFingerprint = ""
	require.NoError(t, f.store.PutMetadataCache(entry))
	verdict, idErr, queryMatch = judge()
	require.Equal(t, BatchVerdictNone, verdict)
	require.ErrorIs(t, idErr, ErrStaleMetadataCache)
	require.False(t, queryMatch)
	entry.SearchFingerprint = saved
	require.NoError(t, f.store.PutMetadataCache(entry))
	verdict, idErr, _ = judge()
	require.Equal(t, BatchVerdictFreshCandidates, verdict, "fingerprint restored")
	require.NoError(t, idErr)

	// The author changes: the row was fetched for someone else, and both
	// readers say so. The store drops the cache row on an identity change,
	// so the old row is put back to see what the identity checks alone say
	// (a row written back by a racing fetch, or one the drop missed).
	leguin, err := f.store.CreateAuthor("Ursula K. Le Guin")
	require.NoError(t, err)
	book := f.book(b.ID)
	book.AuthorID = &leguin.ID
	_, err = f.store.UpdateBook(book.ID, book)
	require.NoError(t, err)
	require.NoError(t, f.store.PutMetadataCache(entry))
	verdict, idErr, queryMatch = judge()
	require.Equal(t, BatchVerdictNone, verdict)
	require.ErrorIs(t, idErr, ErrStaleMetadataCache)
	require.False(t, queryMatch)
}

// The searchInputVersion "2" bump must not re-ask every cached row (~100k
// books against Google Books' 1,000/day quota): a row stamped with the
// version "1" fingerprint for the same inputs keeps its fresh candidates,
// and only a "nothing found" under it is re-asked.
func TestBatchVerdict_LegacyFingerprintKeepsCandidatesOnly(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: "Legacy Rows", FilePath: "/lib/lr/book.m4b"})
	require.NoError(t, err)
	f.mfs.SetOverrideSources([]metadata.MetadataSource{&verdictSource{name: "A"}})
	book := f.book(b.ID)
	in := f.mfs.resolveSearchInputs(book, book.Title, "", "")
	legacy, current := in.legacyFingerprint(book.Title), in.fingerprint(book.Title)
	require.NotEqual(t, legacy, current)
	now := time.Now().UTC()
	put := func(fp string, candidates bool) {
		entry := &database.MetadataCandidateCache{
			BookID:            b.ID,
			FetchedAt:         now,
			SourceHash:        hashSearchInputs(b.ID, book.Title, "", "", ""),
			SearchFingerprint: fp,
		}
		if candidates {
			entry.Candidates = []json.RawMessage{json.RawMessage(`{"title":"Legacy Rows"}`)}
		} else {
			entry.LastEmptyFetchAt = &now
			entry.EmptyAnswers = map[string]time.Time{"A": now}
		}
		require.NoError(t, f.store.PutMetadataCache(entry))
	}

	put(legacy, true)
	_, verdict, _ := f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, "")
	require.Equal(t, BatchVerdictFreshCandidates, verdict, "a version 1 row's candidates stay valid")

	put(legacy, false)
	_, verdict, _ = f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, "")
	require.Equal(t, BatchVerdictNone, verdict, "a version 1 nothing-found is re-asked")

	put(current, false)
	_, verdict, _ = f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, "")
	require.Equal(t, BatchVerdictKnownEmpty, verdict)

	put("not-a-fingerprint", true)
	_, verdict, _ = f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, "")
	require.Equal(t, BatchVerdictNone, verdict)
}
