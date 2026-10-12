// file: internal/metafetch/search_fanout_pcache_test.go
// version: 1.1.0
// guid: 4f9b2d61-8e07-4c5a-a3f1-7d26c0b95e18
// last-edited: 2026-10-11

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// Tests of the shared provider-answer cache (P1/P2). Every fixture is
// synthetic ("Title 000123", "Author 07").

// pcacheKV is an in-memory raw key space behind a MockStore.
type pcacheKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (kv *pcacheKV) keys(prefix string) []string {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	var out []string
	for k := range kv.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func newPCacheService(t *testing.T) (*Service, *pcacheKV) {
	t.Helper()
	kv := &pcacheKV{m: map[string][]byte{}}
	mock := &database.MockStore{
		GetRawFunc: func(k string) ([]byte, error) {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			return kv.m[k], nil
		},
		SetRawFunc: func(k string, v []byte) error {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			kv.m[k] = v
			return nil
		},
		DeleteRawFunc: func(k string) error {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			delete(kv.m, k)
			return nil
		},
		ScanPrefixFunc: func(prefix string) ([]database.KVPair, error) {
			var out []database.KVPair
			for _, k := range kv.keys(prefix) {
				kv.mu.Lock()
				out = append(out, database.KVPair{Key: k, Value: kv.m[k]})
				kv.mu.Unlock()
			}
			return out, nil
		},
	}
	return NewService(mock), kv
}

// withSharedCache turns the shared cache on with the shipped TTLs (and a
// 180-day positive TTL) for one test; on is false for the rollback flag.
func withSharedCache(t *testing.T, on bool) {
	t.Helper()
	prevMF, prevTTL := config.AppConfig.MetadataFetch, config.AppConfig.MetadataFetchCacheTTLDays
	t.Cleanup(func() {
		config.AppConfig.MetadataFetch, config.AppConfig.MetadataFetchCacheTTLDays = prevMF, prevTTL
	})
	config.AppConfig.MetadataFetch = config.DefaultMetadataFetchConfig()
	config.AppConfig.MetadataFetch.SharedCache = on
	config.AppConfig.MetadataFetchCacheTTLDays = 180
}

// withClock pins the cache clock for one test.
func withClock(t *testing.T, now *time.Time) {
	t.Helper()
	prev := pcacheNow
	t.Cleanup(func() { pcacheNow = prev })
	pcacheNow = func() time.Time { return *now }
}

// scriptedProvider answers every question with answer(title, author) or
// fails with err, and counts the calls that reached it.
type scriptedProvider struct {
	id     string
	answer func(title, author string) []metadata.BookMetadata
	err    error
	calls  atomic.Int64
}

func (s *scriptedProvider) Name() string       { return "Scripted " + s.id }
func (s *scriptedProvider) ProviderID() string { return s.id }
func (s *scriptedProvider) ask(title, author string) ([]metadata.BookMetadata, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	if s.answer == nil {
		return nil, nil
	}
	return s.answer(title, author), nil
}
func (s *scriptedProvider) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	return s.ask(title, "")
}
func (s *scriptedProvider) SearchByTitleAndAuthor(_ context.Context, title, author string) ([]metadata.BookMetadata, error) {
	return s.ask(title, author)
}

func echoAnswer(title, author string) []metadata.BookMetadata {
	return []metadata.BookMetadata{{Title: title, Author: author, ASIN: "B0SYNTH001"}}
}

// ask runs askVariant for one book and returns what the variant pooled.
func askFor(svc *Service, src metadata.MetadataSource, bookID string, v queryVariant, opts SearchOptions) *fanoutSource {
	st := &fanoutSource{src: src, name: src.Name(), open: true}
	p := fanoutParams{ctx: context.Background(), bookID: bookID, identity: "v1:" + bookID, opts: opts}
	svc.askVariant(p, st, v)
	return st
}

func titleAuthor(title, author string) queryVariant {
	return queryVariant{Kind: variantTitleAuthor, Title: title, Author: author}
}

func TestAskVariant_SameQuestionTwoBooksOneLiveCall(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: echoAnswer}
	v := titleAuthor("Title 000123", "Author 07")

	a := askFor(svc, src, "book-a", v, SearchOptions{})
	// The second book sends the same question in another spelling.
	b := askFor(svc, src, "book-b", titleAuthor("title 000123", "AUTHOR 07"), SearchOptions{})

	assert.EqualValues(t, 1, src.calls.Load(), "one live call for one question")
	assert.Equal(t, 1, a.live)
	assert.Equal(t, 0, b.live, "the second book is answered from the shared row")
	require.Len(t, b.results, 1)
	assert.Equal(t, "B0SYNTH001", b.results[0].ASIN)
	assert.Len(t, kv.keys(database.PCacheQuestionPrefix), 1)
	assert.Empty(t, kv.keys("metadata_fetch_cache:book-a:audible#q"), "no legacy per-variant row is written")
}

func TestAskVariant_EmptyAnswerReplayedInsideNegativeTTL(t *testing.T) {
	withSharedCache(t, true)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	withClock(t, &now)
	svc, kv := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible} // answers nothing
	v := titleAuthor("Title 000404", "Author 07")

	askFor(svc, src, "book-a", v, SearchOptions{})
	require.EqualValues(t, 1, src.calls.Load())
	keys := kv.keys(database.PCacheQuestionPrefix)
	require.Len(t, keys, 1, "the empty answer is stored")
	var e database.PCacheEntry
	require.NoError(t, json.Unmarshal(kv.m[keys[0]], &e))
	assert.True(t, e.Empty)
	assert.Equal(t, now, e.AskedAt)

	now = now.Add(13 * 24 * time.Hour) // inside Audible's 14 days
	st := askFor(svc, src, "book-b", v, SearchOptions{})
	assert.EqualValues(t, 1, src.calls.Load(), "an empty answer is replayed, not re-asked")
	assert.Empty(t, st.results)
	assert.Equal(t, 0, st.live)
}

func TestAskVariant_EmptyAnswerExpiresAfterNegativeTTL(t *testing.T) {
	withSharedCache(t, true)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	withClock(t, &now)
	svc, _ := newPCacheService(t)
	google := &scriptedProvider{id: metadata.SourceIDGoogleBooks} // 7-day negative TTL
	v := titleAuthor("Title 000405", "Author 07")

	askFor(svc, google, "book-a", v, SearchOptions{})
	now = now.Add(6*24*time.Hour + 23*time.Hour)
	askFor(svc, google, "book-a", v, SearchOptions{})
	require.EqualValues(t, 1, google.calls.Load(), "inside 7 days: replayed")

	now = now.Add(2 * time.Hour) // 7 days and 1 hour after the ask
	askFor(svc, google, "book-a", v, SearchOptions{})
	assert.EqualValues(t, 2, google.calls.Load(), "past the negative TTL the question is asked again")
}

// neverCachedCase asks one question of a failing provider twice and proves
// no pcache: row was written and the second ask reached the provider.
func neverCachedCase(t *testing.T, failure error) {
	t.Helper()
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, err: failure}
	v := titleAuthor("Title 000500", "Author 07")

	st := askFor(svc, src, "book-a", v, SearchOptions{})
	require.True(t, st.stepFailed, "the failure is recorded on the source")
	assert.Empty(t, kv.keys(database.PCachePrefix), "a failed call writes 0 pcache: keys")

	askFor(svc, src, "book-b", v, SearchOptions{})
	assert.EqualValues(t, 2, src.calls.Load(), "the next asker asks again")
	assert.Empty(t, kv.keys(database.PCachePrefix))
}

func TestAskVariant_ErrorNeverCached(t *testing.T) {
	neverCachedCase(t, errors.New("503 from the provider"))
}

func TestAskVariant_ThrottleNeverCached(t *testing.T) {
	neverCachedCase(t, metadata.ErrProviderThrottled)
}

func TestAskVariant_429NeverCached(t *testing.T) {
	neverCachedCase(t, &metadata.ProviderStatusError{Provider: metadata.SourceIDAudible, Status: 429, Body: "rate limited"})
}

// P1 stores the RAW answer: a series-slot variant whose filter keeps nothing
// for book A must not make book B's looser variant of the same question read
// "no answer".
func TestAskVariant_StrictVariantDoesNotPoisonLooseVariant(t *testing.T) {
	withSharedCache(t, true)
	svc, _ := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: func(title, author string) []metadata.BookMetadata {
		return []metadata.BookMetadata{{Title: "Title 000777", Author: author, Series: "Series 09", SeriesPosition: "1"}}
	}}
	strict := queryVariant{Kind: variantSeriesAuthor, Title: "Series 09", Author: "Author 07",
		slotSeries: "Series 09", slotPosition: "4"}
	a := askFor(svc, src, "book-a", strict, SearchOptions{})
	require.Empty(t, a.results, "the strict variant keeps nothing for book A")

	loose := titleAuthor("Series 09", "Author 07")
	b := askFor(svc, src, "book-b", loose, SearchOptions{})
	assert.EqualValues(t, 1, src.calls.Load(), "book B is answered from the shared row")
	require.Len(t, b.results, 1, "the stored raw answer, filtered by book B's own variant")
	assert.Equal(t, "Title 000777", b.results[0].Title)
}

// A legacy per-book per-variant row is still read when the shared row is
// missing (read old), and its answer is copied to the shared row with its
// original age (write new).
func TestAskVariant_LegacyPerBookRowStillRead(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: echoAnswer}
	v := titleAuthor("Title 000600", "Author 07")
	blob, err := json.Marshal([]metadata.BookMetadata{{Title: "Title 000600", Author: "Author 07", ASIN: "B0LEGACY01"}})
	require.NoError(t, err)
	require.NoError(t, database.PutCachedMetadataFetch(svc.db, "book-a", variantCacheSource(src, v), "v1:book-a", blob, 0))

	st := askFor(svc, src, "book-a", v, SearchOptions{})
	assert.Zero(t, src.calls.Load(), "the legacy row answers")
	require.Len(t, st.results, 1)
	assert.Equal(t, "B0LEGACY01", st.results[0].ASIN)

	require.Len(t, kv.keys(database.PCacheQuestionPrefix), 1, "the legacy answer is copied to the shared row")
	other := askFor(svc, src, "book-b", v, SearchOptions{})
	assert.Zero(t, src.calls.Load(), "and now serves another book")
	require.Len(t, other.results, 1)
	assert.Equal(t, "B0LEGACY01", other.results[0].ASIN)
}

// contextProvider is a ContextualSearch source that records each context.
type contextProvider struct {
	scriptedProvider
	mu   sync.Mutex
	seen []metadata.SearchContext
}

func (c *contextProvider) SearchByContext(_ context.Context, sc *metadata.SearchContext) ([]metadata.BookMetadata, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.seen = append(c.seen, *sc)
	c.mu.Unlock()
	return []metadata.BookMetadata{{Title: sc.Title, Series: sc.Series}}, nil
}

func TestSearchByContext_KeyIncludesSeriesAndISBN(t *testing.T) {
	base := metadata.SearchContext{Title: "Title 000123", Author: "Author 07", Narrator: "Narrator 03", Series: "Series 09", ISBN13: "9780000000001"}
	key := func(sc metadata.SearchContext) string {
		return NormalizeQuestion(metadata.SourceIDAudnexus, EndpointSearchContext, SearchContextQuestion(&sc))
	}
	otherSeries, otherISBN, otherNarrator, otherASIN := base, base, base, base
	otherSeries.Series = "Series 10"
	otherISBN.ISBN13 = "9780000000002"
	otherNarrator.Narrator = "Narrator 04"
	otherASIN.ASIN = "B0SYNTH002"
	assert.NotEqual(t, key(base), key(otherSeries), "books differing only in series get different keys")
	assert.NotEqual(t, key(base), key(otherISBN), "books differing only in ISBN get different keys")
	assert.NotEqual(t, key(base), key(otherNarrator))
	assert.NotEqual(t, key(base), key(otherASIN))
	assert.Equal(t, key(base), key(base))

	// End to end through the call FetchMetadataForBook makes.
	withSharedCache(t, true)
	svc, _ := newPCacheService(t)
	src := &contextProvider{scriptedProvider: scriptedProvider{id: metadata.SourceIDAudnexus}}
	ctx := context.Background()
	got, err := svc.cachedSearchByContext(ctx, "book-a", src, src, &base)
	require.NoError(t, err)
	require.Len(t, got, 1)
	_, err = svc.cachedSearchByContext(ctx, "book-b", src, src, &otherSeries)
	require.NoError(t, err)
	assert.EqualValues(t, 2, src.calls.Load(), "a different series is a different question")
	again, err := svc.cachedSearchByContext(ctx, "book-c", src, src, &otherSeries)
	require.NoError(t, err)
	assert.EqualValues(t, 2, src.calls.Load(), "the same full context is answered from the shared row")
	require.Len(t, again, 1)
	assert.Equal(t, "Series 10", again[0].Series)
}

// A ProtectedSource has SearchByContext even when the source it wraps does
// not, and then answers (nil, nil): that is not an answer and is not stored.
func TestSearchByContext_WrappedNonContextualSourceIsNotCached(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	inner := &scriptedProvider{id: metadata.SourceIDAudible}
	ps := metadata.NewProtectedSource(inner, 5, time.Minute)
	sc := metadata.SearchContext{Title: "Title 000123"}
	_, err := svc.cachedSearchByContext(context.Background(), "book-a", ps, ps, &sc)
	require.NoError(t, err)
	assert.Empty(t, kv.keys(database.PCachePrefix))
}

// With metadata_fetch.shared_cache=false every read and write goes back to
// the book's own rows: two books asking one question make two calls, an
// empty answer is never cached, and no pcache: key or pointer row appears.
func TestSharedCacheFlagOffRestoresPerBookKeys(t *testing.T) {
	withSharedCache(t, false)
	svc, kv := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: echoAnswer}
	v := titleAuthor("Title 000123", "Author 07")

	askFor(svc, src, "book-a", v, SearchOptions{})
	askFor(svc, src, "book-b", v, SearchOptions{})
	assert.EqualValues(t, 2, src.calls.Load(), "per-book keys: one call per book")
	askFor(svc, src, "book-a", v, SearchOptions{})
	assert.EqualValues(t, 2, src.calls.Load(), "the book's own row replays")
	assert.Len(t, kv.keys("metadata_fetch_cache:book-a:audible#q"), 1)
	assert.Len(t, kv.keys("metadata_fetch_cache:book-b:audible#q"), 1)

	empty := &scriptedProvider{id: metadata.SourceIDOpenLibrary}
	askFor(svc, empty, "book-a", v, SearchOptions{})
	askFor(svc, empty, "book-a", v, SearchOptions{})
	assert.EqualValues(t, 2, empty.calls.Load(), "an empty answer is never cached with the flag off")

	run := svc.runSearchFanout(fanoutParams{ctx: context.Background(), bookID: "book-c", identity: "v1:book-c"},
		[]metadata.MetadataSource{src}, []queryVariant{v})
	require.Len(t, run, 1)
	assert.Empty(t, kv.keys(database.PCachePrefix), "the shared rows stay inert")
	assert.Empty(t, kv.keys(database.PCacheBookPrefix), "no pointer row")
}

// "Search again" busts what the book's questions map to: the reject path
// (InvalidateFetchCacheForBook) and a forced search (BypassFetchCache) both
// delete the shared rows on the book's pointer row.
func TestSearchAgainBustDeletesPointedKeys(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: echoAnswer}
	variants := []queryVariant{titleAuthor("Title 000123", "Author 07"), {Kind: variantTitleOnly, Title: "Title 000123"}}
	search := func(bookID string, opts SearchOptions) {
		svc.runSearchFanout(fanoutParams{ctx: context.Background(), bookID: bookID, identity: "v1:" + bookID, opts: opts},
			[]metadata.MetadataSource{src}, variants)
	}

	search("book-a", SearchOptions{})
	asked := src.calls.Load()
	require.Positive(t, asked)
	shared := kv.keys(database.PCacheQuestionPrefix)
	require.NotEmpty(t, shared)
	pointed, err := pcachePointedKeys(svc.db, "book-a")
	require.NoError(t, err)
	assert.ElementsMatch(t, shared, pointed[metadata.SourceIDAudible], "the pointer row lists the keys the search touched")

	// Reject path.
	require.NoError(t, svc.InvalidateFetchCacheForBook("book-a"))
	assert.Empty(t, kv.keys(database.PCacheQuestionPrefix), "the pointed shared rows are deleted")
	assert.Empty(t, kv.keys(database.PCacheBookPrefix+"book-a"), "and the book's pointer row")

	// Forced search: busts, then re-asks and stores the fresh answers.
	search("book-a", SearchOptions{})
	afterRefill := src.calls.Load()
	require.Greater(t, afterRefill, asked, "after the bust the question is asked again")
	search("book-a", SearchOptions{})
	require.Equal(t, afterRefill, src.calls.Load(), "a plain repeat is answered from the shared rows")
	search("book-a", SearchOptions{BypassFetchCache: true})
	assert.Greater(t, src.calls.Load(), afterRefill, "a forced search asks again")
	assert.NotEmpty(t, kv.keys(database.PCacheQuestionPrefix), "and stores the fresh answers")
}

// An empty forced refresh never replaces a fresh found product (P2): the
// empty-refetch damage shape, on the shared row.
func TestCachedLookupASIN_EmptyRefreshKeepsFoundProduct(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	var answer atomic.Pointer[metadata.BookMetadata]
	answer.Store(&metadata.BookMetadata{Title: "Title 000123", ASIN: "B0SYNTH003", DurationSec: 3600})
	var calls atomic.Int64
	svc.asinLookupOverride = func(context.Context, string, string) (*metadata.BookMetadata, error) {
		calls.Add(1)
		if r := answer.Load(); r != nil {
			cp := *r
			return &cp, nil
		}
		return nil, nil
	}
	p := fanoutParams{ctx: context.Background(), bookID: "book-a", identity: "v1:book-a"}
	got, err := svc.cachedLookupASIN(p, metadata.SourceIDAudible, "B0SYNTH003")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, kv.keys(database.PCacheASINPrefix), 1)

	p.bookID = "book-b"
	got, err = svc.cachedLookupASIN(p, metadata.SourceIDAudible, "B0SYNTH003")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.EqualValues(t, 1, calls.Load(), "the product is shared across books")

	answer.Store(nil)
	p.opts.BypassFetchCache = true
	got, err = svc.cachedLookupASIN(p, metadata.SourceIDAudible, "B0SYNTH003")
	require.NoError(t, err)
	assert.Nil(t, got, "the forced refresh reports what the provider said")
	p.opts.BypassFetchCache = false
	got, err = svc.cachedLookupASIN(p, metadata.SourceIDAudible, "B0SYNTH003")
	require.NoError(t, err)
	require.NotNil(t, got, "an empty refresh did not erase the found product")

	// A "no such ASIN" for an unknown product is a negative entry.
	_, err = svc.cachedLookupASIN(p, metadata.SourceIDAudible, "B0SYNTH404")
	require.NoError(t, err)
	before := calls.Load()
	_, err = svc.cachedLookupASIN(p, metadata.SourceIDAudible, "B0SYNTH404")
	require.NoError(t, err)
	assert.Equal(t, before, calls.Load(), "a not-found product is replayed inside the negative TTL")
}

// ── The synthetic library scenario (brief step 11) ──

// scenarioQuestion is book i's single question. Composition (1,000 books,
// 600 distinct questions):
//   - books 0..299: 100 answered questions, each shared by 3 books;
//   - books 300..699: 200 unanswerable questions, each asked by 2 books;
//   - books 700..999: 300 answered questions, one per book.
func scenarioQuestion(i int) (title string, answered bool) {
	switch {
	case i < 300:
		return fmt.Sprintf("Title %06d", i/3), true
	case i < 700:
		return fmt.Sprintf("Title %06d", 100000+(i-300)/2), false
	default:
		return fmt.Sprintf("Title %06d", 200000+i), true
	}
}

// runScenarioPass asks every book's question once, sequentially, through
// askVariant, and returns the live calls the pass made.
func runScenarioPass(svc *Service, src *scriptedProvider) int64 {
	before := src.calls.Load()
	for i := 0; i < 1000; i++ {
		title, _ := scenarioQuestion(i)
		askFor(svc, src, fmt.Sprintf("book-%04d", i), titleAuthor(title, "Author 07"), SearchOptions{})
	}
	return src.calls.Load() - before
}

func scenarioProvider() *scriptedProvider {
	unanswerable := map[string]bool{}
	for i := 300; i < 700; i++ {
		title, _ := scenarioQuestion(i)
		unanswerable[title] = true
	}
	return &scriptedProvider{id: metadata.SourceIDAudible, answer: func(title, author string) []metadata.BookMetadata {
		if unanswerable[title] {
			return nil
		}
		return echoAnswer(title, author)
	}}
}

// providerAnswerCounters reads cache_{hits,misses}_total{cache=provider_answer}
// from the default registry.
func providerAnswerCounters(t *testing.T) (hits, misses float64) {
	t.Helper()
	metrics.Register()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, f := range families {
		name := f.GetName()
		if name != "audiobook_organizer_cache_hits_total" && name != "audiobook_organizer_cache_misses_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			isPCache := false
			for _, l := range m.GetLabel() {
				if l.GetName() == "cache" && l.GetValue() == pcacheName {
					isPCache = true
				}
			}
			if !isPCache {
				continue
			}
			if name == "audiobook_organizer_cache_hits_total" {
				hits += m.GetCounter().GetValue()
			} else {
				misses += m.GetCounter().GetValue()
			}
		}
	}
	return hits, misses
}

func TestPCacheScenario_SyntheticLibrary(t *testing.T) {
	distinct := map[string]bool{}
	for i := 0; i < 1000; i++ {
		title, _ := scenarioQuestion(i)
		distinct[title] = true
	}
	require.Len(t, distinct, 600)

	// Before: the rollback flag off, which is the per-book cache.
	withSharedCache(t, false)
	svcOff, _ := newPCacheService(t)
	off := scenarioProvider()
	beforePass1 := runScenarioPass(svcOff, off)
	beforePass2 := runScenarioPass(svcOff, off)

	// After: the shared cache on.
	withSharedCache(t, true)
	svcOn, _ := newPCacheService(t)
	on := scenarioProvider()
	afterPass1 := runScenarioPass(svcOn, on)
	h0, m0 := providerAnswerCounters(t)
	afterPass2 := runScenarioPass(svcOn, on)
	h1, m1 := providerAnswerCounters(t)
	ratio := (h1 - h0) / ((h1 - h0) + (m1 - m0))

	t.Logf("scenario: 1000 books, %d distinct questions", len(distinct))
	t.Logf("N_before (shared_cache=false): pass1=%d pass2=%d", beforePass1, beforePass2)
	t.Logf("N_after  (shared_cache=true):  pass1=%d pass2=%d", afterPass1, afterPass2)
	t.Logf("provider_answer hit ratio on pass 2: %.4f (hits %.0f, misses %.0f)", ratio, h1-h0, m1-m0)

	assert.EqualValues(t, 1000, beforePass1, "per-book keys: every book asks")
	assert.EqualValues(t, 400, beforePass2, "per-book keys: empty answers are re-asked")
	assert.EqualValues(t, len(distinct), afterPass1, "one live call per distinct question")
	assert.Zero(t, afterPass2, "an immediate second pass makes no live call")
	assert.GreaterOrEqual(t, ratio, 0.99)
}

// The bulk chain walk reads and writes the same shared rows: a second book
// with the same title and author makes no call, and its outcome says the
// answer came from the shared cache (so the caller still writes the book's
// own per-book row). A ladder whose every rung is stored empty asks nothing.
func TestWalkSourceChain_SharedCache(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	found := &scriptedProvider{id: metadata.SourceIDAudible, answer: echoAnswer}
	sem := NewProviderSemaphore([]metadata.MetadataSource{found}, 2)
	ctx := context.Background()

	first, err := WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{found}, sem, "book-a", "Title 000123", "Author 07", "v1:book-a", 0)
	require.NoError(t, err)
	require.Len(t, first.Results, 1)
	assert.False(t, first.CacheHit)
	assert.False(t, first.SharedCacheHit)
	assert.EqualValues(t, 1, found.calls.Load())

	second, err := WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{found}, sem, "book-b", "Title 000123", "Author 07", "v1:book-b", 0)
	require.NoError(t, err)
	require.Len(t, second.Results, 1)
	assert.EqualValues(t, 1, found.calls.Load(), "the second book is answered from the shared row")
	assert.True(t, second.SharedCacheHit)
	assert.False(t, second.CacheHit, "the caller still writes book-b's own row")
	assert.Equal(t, FetchStatusCached, second.Status())
	pointed, err := pcachePointedKeys(svc.db, "book-b")
	require.NoError(t, err)
	assert.Len(t, pointed[metadata.SourceIDAudible], 1)

	none := &scriptedProvider{id: metadata.SourceIDOpenLibrary}
	sem2 := NewProviderSemaphore([]metadata.MetadataSource{none}, 2)
	out, err := WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{none}, sem2, "book-c", "Title 000404", "Author 07", "v1:book-c", 0)
	require.NoError(t, err)
	require.Empty(t, out.Results)
	ladder := none.calls.Load()
	require.Positive(t, ladder)
	out, err = WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{none}, sem2, "book-d", "Title 000404", "Author 07", "v1:book-d", 0)
	require.NoError(t, err)
	assert.Equal(t, FetchStatusNotFound, out.Status())
	assert.Equal(t, ladder, none.calls.Load(), "every rung is stored empty: no call until the negative TTL")
	assert.False(t, out.AllThrottled)

	failing := &scriptedProvider{id: metadata.SourceIDGoogleBooks, err: metadata.ErrProviderThrottled}
	sem3 := NewProviderSemaphore([]metadata.MetadataSource{failing}, 2)
	before := len(kv.keys(database.PCachePrefix))
	_, err = WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{failing}, sem3, "book-e", "Title 000500", "Author 07", "v1:book-e", 0)
	require.NoError(t, err)
	assert.Len(t, kv.keys(database.PCachePrefix), before, "a throttled walk writes no pcache: key")
}
