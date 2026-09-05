// file: internal/metafetch/source_chain_walk.go
// version: 1.2.0
// guid: b71e4d20-8f36-4c95-a1d7-52e0c6b93f84
// last-edited: 2026-09-05

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
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
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
)

// ChainOutcome is the result of walking the metadata source chain for one book.
type ChainOutcome struct {
	Results    []metadata.BookMetadata
	SourceName string
	// providerKey is the canonical id the winning source's cache row is written
	// under. sourceName stays the DISPLAY name: it is what the operation ledger
	// and the review UI show a human.
	ProviderKey string
	CacheHit    bool

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
func WalkSourceChain(
	ctx context.Context,
	store database.RawKVStore,
	sourceChain []metadata.MetadataSource,
	sem *ProviderSemaphore,
	bookID, bookTitle, author string,
	maxAge time.Duration,
) (ChainOutcome, error) {
	var out ChainOutcome
	searchTitle := stripChapterFromTitle(bookTitle)

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

		if cached, _, cerr := database.CachedMetadataForProvider(store, bookID, slotKey, name, maxAge); cerr == nil && cached != nil {
			var cr []metadata.BookMetadata
			if jerr := json.Unmarshal(cached.Results, &cr); jerr == nil && len(cr) > 0 {
				out.Results, out.SourceName, out.ProviderKey, out.CacheHit = cr, name, slotKey, true
				return out, nil
			}
		}

		// Live calls only: bound concurrency per provider. A ctx cancel while
		// waiting on the semaphore aborts the book with ctx.Err().
		if err := sem.Acquire(ctx, slotKey); err != nil {
			return out, err
		}
		live++
		throttled := 0
		hit := func() bool {
			defer sem.Release(slotKey)

			// Query ladder for this source, most specific first. The untrimmed
			// retries are appended only when trimming actually changed the
			// title, so an unchanged title is not searched twice.
			attempts := make([]func() ([]metadata.BookMetadata, error), 0, 4)
			add := func(title string) {
				if author != "" {
					attempts = append(attempts, func() ([]metadata.BookMetadata, error) {
						return src.SearchByTitleAndAuthor(ctx, title, author)
					})
				}
				attempts = append(attempts, func() ([]metadata.BookMetadata, error) {
					return src.SearchByTitle(ctx, title)
				})
			}
			add(searchTitle)
			if searchTitle != bookTitle {
				add(bookTitle)
			}
			// Series-decorated titles ("Eternal Dominion, Book 04 - Assertions",
			// "Path Of The Voidwalker - BK07") miss on every provider verbatim;
			// the attempts loop returns on the first hit, so these cost calls
			// only for a book the two literal queries did not find.
			for _, variant := range extraTitleVariants(bookTitle, searchTitle) {
				add(variant)
			}

			for _, attempt := range attempts {
				res, err := attempt()
				if err != nil {
					// Remember it and keep trying: a later query or a later
					// source may still succeed, but if nothing does, this is
					// the difference between "not in the catalog" and "we
					// never got a usable answer".
					//
					// A CONTROL-PLANE sentinel must not displace a diagnosis.
					// The first book to exhaust a quota gets the real message
					// ("Quota exceeded ... 'Queries per day'") on attempt 1 and
					// ErrProviderThrottled on attempts 2-4; overwriting blindly
					// meant the one ledger row that could name the cause said
					// only "provider is throttled" and the quota text appeared
					// nowhere in the operation's output.
					if errors.Is(err, metadata.ErrProviderThrottled) {
						throttled++
						if out.Err == nil {
							out.Err, out.ErrSource = err, name
						}
					} else {
						out.Err, out.ErrSource = err, name
					}
					continue
				}
				if len(res) > 0 {
					out.Results, out.SourceName, out.ProviderKey = res, name, slotKey
					return true
				}
			}
			return false
		}()
		if hit {
			return out, nil
		}
		// A source counts as throttled only when EVERY attempt for it was
		// refused by the gate. One real call means the provider was reachable.
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
