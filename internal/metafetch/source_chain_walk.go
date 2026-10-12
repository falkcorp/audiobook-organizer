// file: internal/metafetch/source_chain_walk.go
// version: 1.12.0
// guid: b71e4d20-8f36-4c95-a1d7-52e0c6b93f84
// last-edited: 2026-10-11

package metafetch

// The single implementation of "search the provider chain for one book".
//
// This existed as THREE near-identical private copies -- two in internal/server
// (the all-books and by-IDs bulk-fetch paths) and one in the
// internal/maintenance/jobs bulk-fetch job -- and they had already drifted:
// only one retried with the untrimmed title, and all three independently
// discarded the provider error so a throttled response was recorded as a
// missing book. A fix applied to any one of them was inert in the other two.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// DefaultPerProviderFetchCap is the fallback bound on concurrent live search
// calls to a single provider, used when that provider has no configured
// max_concurrent. Per-provider values come from
// config.MetadataSource.RateLimit.MaxConcurrent.
const DefaultPerProviderFetchCap = 2

// ProviderSemaphore bounds in-flight live search calls per source name. The map
// is built ONCE before dispatch and is read-only thereafter, so concurrent
// workers only ever touch a per-name buffered channel — never the map itself.
type ProviderSemaphore struct{ byName map[string]chan struct{} }

// NewProviderSemaphore builds a per-provider semaphore from a source chain.
// Duplicate names share one channel.
//
// Capacity is per provider: a provider with a configured max_concurrent gets
// that, everything else gets defaultCap.
//
// Both the map and the config lookup key on the canonical provider id, so the
// two can never disagree -- which they would if one used the display name.
func NewProviderSemaphore(chain []metadata.MetadataSource, defaultCap int) *ProviderSemaphore {
	configured := make(map[string]int)
	for _, cs := range config.AppConfig.MetadataSources {
		if cs.ID != "" && cs.RateLimit.MaxConcurrent > 0 {
			configured[cs.ID] = cs.RateLimit.MaxConcurrent
		}
	}

	m := make(map[string]chan struct{}, len(chain))
	for _, src := range chain {
		n := metadata.ProviderKey(src)
		if _, ok := m[n]; ok {
			continue
		}
		c := defaultCap
		if v, ok := configured[n]; ok {
			c = v
		}
		if c < 1 {
			// A zero-capacity channel would block every worker forever, turning
			// a misconfiguration into a hang rather than a slow fetch.
			c = 1
		}
		m[n] = make(chan struct{}, c)
	}
	return &ProviderSemaphore{byName: m}
}

// Acquire blocks until a slot for name is free or ctx is done. An unknown name
// (no channel) is treated as unbounded and acquire is a no-op — release then
// matches. Returns ctx.Err() when the context is canceled while waiting.
func (p *ProviderSemaphore) Acquire(ctx context.Context, name string) error {
	if p == nil {
		return nil
	}
	ch := p.byName[name]
	if ch == nil {
		return nil
	}
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns a slot previously taken by Acquire. Paired with Acquire via
// defer, so the channel always holds a token to drain; a no-op for unknown names.
func (p *ProviderSemaphore) Release(name string) {
	if p == nil {
		return
	}
	if ch := p.byName[name]; ch != nil {
		<-ch
	}
}

// Ledger statuses recorded on OperationResult rows by the bulk-fetch paths.
// They are the resume ledger's vocabulary, so a later pass can select exactly
// the books worth retrying rather than re-walking the whole library.
const (
	FetchStatusCached          = "cached"
	FetchStatusNotFound        = "not_found"
	FetchStatusFetchError      = "fetch_error"
	FetchStatusSkippedFragment = "skipped_fragment"
	// FetchStatusSkippedNoTitle: the book's title is empty or a placeholder
	// and no fallback (transcribed title, folder name) offered a usable one,
	// so nothing was searched. A chapter-number or fragment title with no
	// fallback keeps FetchStatusSkippedFragment.
	FetchStatusSkippedNoTitle = "skipped_no_title"
)

// ChainOutcome is the result of walking the metadata source chain for one book.
type ChainOutcome struct {
	Results    []metadata.BookMetadata
	SourceName string
	// providerKey is the canonical id the winning source's cache row is written
	// under. sourceName stays the DISPLAY name: it is what the operation ledger
	// and the review UI show a human.
	ProviderKey string
	// CacheHit: the book's OWN per-book row (metadata_fetch_cache:<book>:
	// <provider>) answered, so the caller has nothing to write back.
	CacheHit bool
	// SharedCacheHit: the winning answer came from the shared provider-answer
	// cache (P1), with no provider call. CacheHit stays false for it, so the
	// callers still write the book's per-book row (WriteBackOutcome), which
	// the review UI and the bulk "already cached" skip read.
	SharedCacheHit bool
	// SharedAskedAt is when the provider gave the winning answer when that
	// answer came from the shared cache: on a SharedCacheHit, or when a later
	// rung of a ladder that also asked live was answered from P1. Zero for a
	// live answer. The per-book row copied from it is stamped with this age,
	// not with the copy time: a copy stamped "now" would let an answer outlive
	// its TTL by up to the TTL again.
	SharedAskedAt time.Time
	// SearchIdentity is the book's database.MetadataSearchIdentity the walk
	// read the cache with. Callers stamp their write-back with it, so the read
	// and the write can never derive the identity two different ways.
	SearchIdentity string

	// err is the last live-call error seen while walking the chain, and
	// errSource the provider that produced it.
	//
	// Capturing this is what makes a THROTTLED (or broken, or circuit-broken)
	// provider distinguishable from a book that genuinely is not in any
	// catalog. Both end the walk with zero results, so collapsing them into
	// "not_found" -- which is what this code did until 2026-09-02 -- records a
	// false miss for every book that was merely rate-limited. The practical
	// cost is that "fetch the ones we are missing" becomes untrustworthy and
	// the only safe recovery is a full re-scan of the library.
	Err       error
	ErrSource string

	// Variant is the query that produced Results when it was one of the
	// decoration-free retries (extraTitleVariants) rather than the book's
	// literal title; "" for a literal hit or a cache hit. Ledgered so a
	// bulk run's rescues can be told apart from its literal hits.
	Variant string

	// AllThrottled is set when EVERY source in the chain refused before making
	// a call because it is under a global throttle.
	//
	// This is the mid-run counterpart to the pre-flight check in
	// prepareFetchChain. The chain is filtered once, before the worker pool
	// starts; a provider whose quota runs out at book 350 is still in the
	// captured chain for books 351..N. Without this flag those books each get a
	// ledger row, a warn line and a 200ms pause for work that provably cannot
	// happen -- which is the incident the throttle exists to prevent, minus only
	// the outbound HTTP. The caller aborts the run on this.
	AllThrottled bool
}

// FromCache reports whether the outcome was answered with no provider call:
// from the book's own row or from the shared cache. The bulk callers skip
// the inter-book pause and label the op-log line "(from cache)" on it.
func (o ChainOutcome) FromCache() bool {
	return o.CacheHit || o.SharedCacheHit
}

// WriteBackOutcome writes a found outcome's results to the book's own
// per-provider row (metadata_fetch_cache:<book>:<provider>), which the review
// UI and the bulk "already cached" skip read. Nothing is written when the
// book's own row answered (CacheHit) or there are no results. A shared-cache
// answer is copied with that answer's age (SharedAskedAt), so the copy
// expires when the answer does. The three bulk-fetch paths share this one
// write so the rule cannot drift between them.
func WriteBackOutcome(store database.RawKVStore, bookID string, out ChainOutcome) error {
	if out.CacheHit || len(out.Results) == 0 {
		return nil
	}
	blob, err := json.Marshal(out.Results)
	if err != nil {
		return fmt.Errorf("encode results: %w", err)
	}
	// SharedAskedAt is zero for a live answer, which PutCachedMetadataFetchAt
	// stamps now.
	return database.PutCachedMetadataFetchAt(store, bookID, out.ProviderKey, out.SearchIdentity, blob, 0, out.SharedAskedAt)
}

// status maps a completed walk onto the ledger status. Order matters: results
// win over an error (an earlier provider erroring does not invalidate a later
// provider's hit), and an error beats "not found".
func (o ChainOutcome) Status() string {
	if len(o.Results) > 0 && o.SourceName != "" {
		return FetchStatusCached
	}
	if o.Err != nil {
		return FetchStatusFetchError
	}
	return FetchStatusNotFound
}

// WalkSourceChain runs the priority-ordered source chain for one book and stops
// at the first source that yields results. Cache is consulted per source before
// any live call. It returns a non-nil error ONLY for context cancellation; a
// provider failure is reported inside the outcome so a single bad book (or a
// single throttled provider) never fails the whole operation.
//
// Both bulk-fetch entry points call this. They previously carried near-identical
// private copies of the walk, and the copies had ALREADY DRIFTED: only the
// all-books copy retried with the untrimmed title when stripChapterFromTitle had
// changed it. Unifying them fixes that divergence in passing -- the by-IDs path
// gains the retry it was silently missing.
//
// Shared cache (metadata_fetch.shared_cache): each rung of a source's ladder
// is one provider question with its own P1 row. Per source the walk first
// replays the ladder from P1 without the semaphore -- the first rung whose
// stored answer survives the rung's filter wins with no call -- then reads the
// book's own per-book row (which may hold the answer to a question outside the
// ladder), and only then, unless every rung is stored EMPTY (the source has
// nothing for this book until its negative TTL: no call), asks live from the
// first rung with no stored answer, writing each answer (empty included) when
// the call returned no error. The P1 keys read or written are recorded on the
// book's pointer row.
func WalkSourceChain(
	ctx context.Context,
	store database.RawKVStore,
	sourceChain []metadata.MetadataSource,
	sem *ProviderSemaphore,
	bookID, bookTitle, author, identity string,
	maxAge time.Duration,
) (ChainOutcome, error) {
	out := ChainOutcome{SearchIdentity: identity}
	searchTitle := stripChapterFromTitle(bookTitle)
	shared := sharedCacheEnabled()
	var pkeys *pcacheKeyLog
	if shared {
		pkeys = newPCacheKeyLog()
		defer pkeys.flush(store, bookID)
	}

	// live counts sources that were actually callable this book; throttledAll
	// stays true only while every one of them refused on the throttle.
	live, throttledAll := 0, true

	for _, src := range sourceChain {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		// Two different keys on purpose. slotKey is the canonical provider id
		// and governs concurrency. name is the DISPLAY name and is what the
		// metadata-fetch cache rows are keyed under (normalized) -- that column
		// is persisted, so re-keying it to the id would orphan every cached row
		// and silently trigger a full-library refetch. Changing it needs a
		// migration, not a rename.
		slotKey := metadata.ProviderKey(src)
		name := src.Name()

		attempts := chainAttempts(ctx, src, slotKey, bookTitle, searchTitle, author, shared)

		// Shared cache first: replay the ladder from P1 while every rung has
		// a stored answer.
		start := 0
		if shared {
			for ; start < len(attempts); start++ {
				a := attempts[start]
				e, hit := pcacheRead(store, a.key, questionTTL(slotKey))
				if !hit {
					break
				}
				res, ok := decodeEntry[metadata.BookMetadata](e)
				if !ok {
					break
				}
				pkeys.add(slotKey, a.key)
				if a.variant != nil {
					res = keepVariant(res, *a.variant, author)
				}
				if len(res) > 0 {
					markReleaseYear(res, name)
					metrics.IncMetadataFetch(slotKey, metrics.FetchSourceCacheHit)
					out.Results, out.SourceName, out.ProviderKey, out.SharedCacheHit = res, name, slotKey, true
					out.SharedAskedAt = e.AskedAt
					if a.variant != nil {
						out.Variant = a.query
					}
					return out, nil
				}
			}
		}

		// The book's own per-provider row is read BEFORE the all-empty
		// short-circuit below: FetchMetadataForBook's context search and its
		// transcribed-title fallback, and the interactive search's pooled
		// write, store answers to questions outside this ladder there, so an
		// empty ladder (another copy of the book walked it) must not hide them.
		if cached, _, cerr := database.CachedMetadataForProvider(store, bookID, slotKey, name, identity, maxAge); cerr == nil && cached != nil {
			var cr []metadata.BookMetadata
			if jerr := json.Unmarshal(cached.Results, &cr); jerr == nil && len(cr) > 0 {
				metrics.IncMetadataFetch(slotKey, metrics.FetchSourceCacheHit)
				out.Results, out.SourceName, out.ProviderKey, out.CacheHit = cr, name, slotKey, true
				return out, nil
			}
		}
		if shared && start == len(attempts) {
			// Every question this source would be asked is stored as
			// answered-with-nothing, and the book has no row of its own: the
			// source has nothing for the book until a negative TTL runs out.
			// Not a throttle, and no call, so throttledAll is left as it is.
			metrics.IncMetadataFetch(slotKey, metrics.FetchSourceCacheHit)
			continue
		}
		metrics.IncMetadataFetch(slotKey, metrics.FetchSourceCacheMiss)

		// Live calls only: bound concurrency per provider. A ctx cancel while
		// waiting on the semaphore aborts the book with ctx.Err().
		if err := sem.Acquire(ctx, slotKey); err != nil {
			return out, err
		}
		live++
		throttled := 0
		hit := func() bool {
			defer sem.Release(slotKey)

			for i := start; i < len(attempts); i++ {
				a := attempts[i]
				// A cancelled walk makes no further calls: each one would fail
				// fast and count against the shared circuit breaker.
				if ctx.Err() != nil {
					return false
				}
				var res []metadata.BookMetadata
				var err error
				var cachedAt time.Time // non-zero: this rung was answered from P1
				switch {
				case !shared:
					res, err = a.run()
				case i == start:
					// Known miss from the replay above: ask, and store.
					res, err = a.run()
					if err == nil {
						pcacheWrite(store, a.key, slotKey, a.endpoint, res, len(res) == 0, questionTTL(slotKey), time.Time{})
					}
				default:
					res, cachedAt, err = cachedCall(store, slotKey, a.endpoint, a.key, cacheUse{}, a.run)
				}
				if shared && err == nil {
					pkeys.add(slotKey, a.key)
				}
				if err != nil {
					if errors.Is(err, metadata.ErrProviderThrottled) {
						throttled++
					}
					if next := keepDiagnosis(out.Err, err); next != out.Err {
						out.Err, out.ErrSource = next, name
					}
					if providerSentinel(err) {
						// Every later attempt in this ladder returns the same
						// refusal; stop instead of paying for it up to 8 times.
						return false
					}
					continue
				}
				if a.variant != nil {
					res = keepVariant(res, *a.variant, author)
				}
				if len(res) > 0 {
					out.Results, out.SourceName, out.ProviderKey = res, name, slotKey
					// A later rung can win from P1 after an earlier rung was
					// asked live: the walk made a call (not FromCache), but the
					// winning answer keeps its age for the write-back.
					out.SharedAskedAt = cachedAt
					if !cachedAt.IsZero() {
						markReleaseYear(res, name)
					}
					if a.variant != nil {
						out.Variant = a.query
						slog.Info("metadata-fetch: hit on title variant",
							"book_id", bookID, "provider", name, "variant", a.query, "count", len(res))
					}
					return true
				}
			}
			return false
		}()
		if hit {
			return out, nil
		}
		// A source counts as throttled when its first refusal was a throttle
		// hold: the ladder closes on that refusal, so no later attempt is made.
		// One real call means the provider was reachable. An open circuit
		// breaker is a different, 30-second mechanism and does not count.
		if throttled == 0 {
			throttledAll = false
		}
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	// A cache-only walk (live == 0) is not "all throttled" -- nothing refused.
	out.AllThrottled = live > 0 && throttledAll
	return out, nil
}

// chainAttempt is one rung of a source's query ladder: one provider call.
type chainAttempt struct {
	query    string
	variant  *titleVariant // non-nil: a variant, results gated on it
	endpoint string        // P1 endpoint (marketplace applied); "" when the shared cache is off
	key      string        // P1 key; "" when the shared cache is off
	run      func() ([]metadata.BookMetadata, error)
}

// chainAttempts is a source's query ladder for one book, most specific
// first. The untrimmed retries are appended only when trimming actually
// changed the title, so an unchanged title is not searched twice.
func chainAttempts(ctx context.Context, src metadata.MetadataSource, provider, bookTitle, searchTitle, author string, shared bool) []chainAttempt {
	attempts := make([]chainAttempt, 0, 8)
	add := func(title string, variant *titleVariant, titleOnly bool) {
		if author != "" {
			a := chainAttempt{query: title, variant: variant, run: func() ([]metadata.BookMetadata, error) {
				return src.SearchByTitleAndAuthor(ctx, title, author)
			}}
			if shared {
				a.endpoint = endpointWithMarketplace(provider, EndpointSearchTitleAuthor)
				a.key = NormalizeQuestion(provider, a.endpoint, Question{Title: title, Authors: CreditsFromFlat(author)})
			}
			attempts = append(attempts, a)
		}
		if titleOnly {
			a := chainAttempt{query: title, variant: variant, run: func() ([]metadata.BookMetadata, error) {
				return src.SearchByTitle(ctx, title)
			}}
			if shared {
				a.endpoint = endpointWithMarketplace(provider, EndpointSearchTitle)
				a.key = NormalizeQuestion(provider, a.endpoint, Question{Title: title})
			}
			attempts = append(attempts, a)
		}
	}
	add(searchTitle, nil, true)
	if searchTitle != bookTitle {
		add(bookTitle, nil, true)
	}
	// Series-decorated titles ("Eternal Dominion, Book 04 - Assertions")
	// miss on every provider verbatim; the walk returns on the first hit, so
	// these cost calls only for a book the two literal queries did not find,
	// and their results are anchored on the book's own name so a series-name
	// answer cannot be cached as it.
	for _, v := range extraTitleVariants(bookTitle, searchTitle) {
		add(v.Query, &v, v.titleOnlyAllowed())
	}
	return attempts
}

// markReleaseYear re-derives the year-kind flag of answers replayed from a
// cache, as every replay path does: Audible and Audnexus report the
// audiobook RELEASE year.
func markReleaseYear(rs []metadata.BookMetadata, sourceName string) {
	isRelease := metadata.SourceProducesAudiobookReleaseYear(sourceName)
	for i := range rs {
		rs[i].PublishYearIsAudiobookRelease = isRelease
	}
}
