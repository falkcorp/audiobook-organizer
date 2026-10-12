// file: internal/metafetch/pcache.go
// version: 1.1.1
// guid: 0e7a4c92-3b15-4d68-8f2e-a91c5d73b046
// last-edited: 2026-10-11

package metafetch

// The metafetch side of the shared provider-answer cache: the TTL rule per
// provider, the clock, the metrics, the per-search pointer collector, and the
// read-through helpers every provider call site uses. The rows themselves and
// their key builders live in internal/database/metadata_fetch_cache.go; the
// question normalization in question_key.go.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

var pcacheLog = logger.New("metafetch.pcache")

// pcacheName is the {cache} label of the shared provider-answer cache:
// audiobook_organizer_cache_hits_total{cache="provider_answer"} over hits plus
// misses is its hit ratio.
const pcacheName = database.PCacheMetricName

// pcacheMissBypass: the caller forced a refresh, so the read was skipped.
const pcacheMissBypass = "bypass"

// pcacheProductTTL is how long a found product (P2, by ASIN) is replayed.
const pcacheProductTTL = 30 * 24 * time.Hour

// pcacheNow is the cache's clock; tests replace it to age entries.
var pcacheNow = time.Now

// sharedCacheEnabled reports metadata_fetch.shared_cache, the rollback
// switch: false sends every read and write back to the per-book rows.
func sharedCacheEnabled() bool {
	return config.AppConfig.MetadataFetch.SharedCache
}

// negativeTTL is how long an empty answer from provider is replayed. Zero
// (unset, or a provider with no configured value) means empty answers are not
// cached for it.
func negativeTTL(provider string) time.Duration {
	n := config.AppConfig.MetadataFetch.NegativeTTLDays
	days := 0
	switch provider {
	case metadata.SourceIDAudible:
		days = n.Audible
	case metadata.SourceIDAudnexus:
		days = n.Audnexus
	case metadata.SourceIDOpenLibrary:
		days = n.OpenLibrary
	case metadata.SourceIDGoogleBooks:
		days = n.Google
	}
	return time.Duration(days) * 24 * time.Hour
}

// questionTTL is the P1 rule for provider: positive answers live for the
// fetch-cache TTL (metadata_fetch_cache_ttl_days), empty ones for the
// provider's negative TTL.
func questionTTL(provider string) database.PCacheTTL {
	return database.PCacheTTL{Positive: fetchCacheMaxAge(), Negative: negativeTTL(provider)}
}

// productTTL is the P2 rule: a found product for 30 days, a "no such
// product" for the provider's negative TTL.
func productTTL(provider string) database.PCacheTTL {
	return database.PCacheTTL{Positive: pcacheProductTTL, Negative: negativeTTL(provider)}
}

// PCacheTTLFor is the TTL rule the read path applies to a stored row of kind
// (database.PCacheKindQuestion or database.PCacheKindProduct) from provider:
// questionTTL for a question, productTTL for a product. The sweep and the
// counts judge rows with it (database.PCacheTTLRule), so they call a row
// expired exactly when a read would not serve it.
func PCacheTTLFor(kind, provider string) database.PCacheTTL {
	if kind == database.PCacheKindProduct {
		return productTTL(provider)
	}
	return questionTTL(provider)
}

// pcacheRead reads one entry and records the decision on the
// provider_answer counters. A store error is logged and read as a miss: the
// cache is an optimization, never a reason to fail a search.
func pcacheRead(store database.PCacheStore, key string, ttl database.PCacheTTL) (*database.PCacheEntry, bool) {
	e, reason, err := database.GetPCache(store, key, ttl, pcacheNow())
	if err != nil {
		pcacheLog.Warn("shared cache read failed, asking the provider: key=%s err=%v", key, err)
		metrics.RecordCacheMiss(pcacheName, "error")
		return nil, false
	}
	if e == nil {
		metrics.RecordCacheMiss(pcacheName, reason)
		return nil, false
	}
	metrics.RecordCacheHit(pcacheName)
	return e, true
}

// pcacheWrite stores one answer: results is the raw provider answer (any
// JSON-marshalable slice), empty when the provider had nothing. Call it only
// after the call returned err == nil: errors, throttle refusals and 429s are
// never written. askedAt zero means now.
func pcacheWrite(store database.PCacheStore, key, provider, endpoint string, results any, empty bool, ttl database.PCacheTTL, askedAt time.Time) {
	blob, err := json.Marshal(results)
	if err != nil {
		pcacheLog.Warn("shared cache marshal failed: key=%s err=%v", key, err)
		return
	}
	written, err := database.PutPCache(store, key, database.PCacheEntry{
		Provider: provider, Endpoint: endpoint, AskedAt: askedAt, Results: blob, Empty: empty,
	}, ttl, pcacheNow())
	if err != nil {
		pcacheLog.Warn("shared cache write failed: key=%s err=%v", key, err)
		return
	}
	if written {
		metrics.RecordCacheSet(pcacheName)
	}
}

// decodeEntry decodes a hit's raw answer into out. An empty entry decodes to
// nothing; an undecodable one reports false and is treated as a miss.
func decodeEntry[T any](e *database.PCacheEntry) ([]T, bool) {
	if e.Empty || len(e.Results) == 0 {
		return nil, true
	}
	var out []T
	if err := json.Unmarshal(e.Results, &out); err != nil {
		return nil, false
	}
	return out, true
}

// cacheUse says how one call may use the shared cache.
type cacheUse struct {
	// bypass skips the read (a forced refresh); the fresh answer is still
	// written unless readOnly.
	bypass bool
	// readOnly writes nothing: no answer row (a dry run's contract is that it
	// writes nothing). The read still happens unless bypass.
	readOnly bool
}

// cachedCall answers one provider question from P1, else asks live and
// stores the raw answer (empty included) when, and only when, the call
// returned no error. It reports whether the answer came from the cache.
// use.bypass skips the read; use.readOnly skips the write.
func cachedCall[T any](store database.PCacheStore, provider, endpoint, key string, use cacheUse, call func() ([]T, error)) ([]T, bool, error) {
	ttl := questionTTL(provider)
	if use.bypass {
		metrics.RecordCacheMiss(pcacheName, pcacheMissBypass)
	} else if e, ok := pcacheRead(store, key, ttl); ok {
		if rs, ok := decodeEntry[T](e); ok {
			return rs, true, nil
		}
	}
	rs, err := call()
	if err != nil {
		return rs, false, err
	}
	if !use.readOnly {
		pcacheWrite(store, key, provider, endpoint, rs, len(rs) == 0, ttl, time.Time{})
	}
	return rs, false, nil
}

// pcacheKeyLog collects the P1 keys one search or walk read or wrote, by
// provider, for the book's pointer row. Goroutine-safe: a fan-out round
// asks its sources concurrently.
type pcacheKeyLog struct {
	mu   sync.Mutex
	keys map[string][]string
}

func newPCacheKeyLog() *pcacheKeyLog { return &pcacheKeyLog{keys: map[string][]string{}} }

func (l *pcacheKeyLog) add(provider, key string) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys[provider] = append(l.keys[provider], key)
}

// flush records the collected keys on bookID's pointer row. A failure is
// logged: the pointer only makes a later bust reach the shared rows, and a
// missing pointer costs at most one stale answer until its TTL.
func (l *pcacheKeyLog) flush(store database.PCacheStore, bookID string) {
	if l == nil || bookID == "" {
		return
	}
	l.mu.Lock()
	keys := l.keys
	l.keys = map[string][]string{}
	l.mu.Unlock()
	if err := database.RecordPCachePointer(store, bookID, keys, pcacheNow()); err != nil {
		pcacheLog.Warn("pointer row write failed: book=%s err=%v", logger.SanitizeLogValue(bookID), err)
	}
}

// IdentitySearcher is the Audible identity search the ASIN backfill makes.
type IdentitySearcher interface {
	SearchIdentities(ctx context.Context, q metadata.AudibleIdentityQuery) ([]metadata.AudibleIdentity, error)
}

// IdentitySearchCache says how CachedIdentitySearch may use the shared
// cache for one book.
type IdentitySearchCache struct {
	// Refresh skips the read and asks Audible live: the caller wants a fresh
	// answer (the ASIN backfill's retry window for the book has passed, or
	// retry_after_days is 0). The fresh answer is still stored unless
	// ReadOnly, so it serves the next asker.
	Refresh bool
	// ReadOnly writes nothing, neither the answer row nor the book's pointer
	// row: a dry run writes nothing. A stored answer is still read.
	ReadOnly bool
}

// CachedIdentitySearch is the ASIN backfill's Audible identity search behind
// the shared cache (P1, endpoint identity_search): the same question for
// another book, or for this book on a later run, replays the stored answer,
// an empty one included, inside its TTL, unless use.Refresh. Errors are never
// stored, and use.ReadOnly stores nothing. With metadata_fetch.shared_cache
// off it is the bare call, as before. A hit makes no request, so it takes no
// token from Audible's bucket.
//
// The key hashes exactly what the call sends: the title, the one author
// credit (q.Author), the keywords and the result count.
func CachedIdentitySearch(ctx context.Context, store database.PCacheStore, bookID string, s IdentitySearcher, q metadata.AudibleIdentityQuery, use IdentitySearchCache) ([]metadata.AudibleIdentity, error) {
	if !sharedCacheEnabled() {
		return s.SearchIdentities(ctx, q)
	}
	provider := metadata.SourceIDAudible
	endpoint := endpointWithMarketplace(provider, EndpointIdentitySearch)
	key := NormalizeQuestion(provider, endpoint, Question{
		Title: q.Title, Authors: CreditsFromFlat(q.Author), Keywords: q.Keywords, NumResults: q.NumResults,
	})
	rs, _, err := cachedCall(store, provider, endpoint, key, cacheUse{bypass: use.Refresh, readOnly: use.ReadOnly}, func() ([]metadata.AudibleIdentity, error) {
		return s.SearchIdentities(ctx, q)
	})
	if err == nil && !use.ReadOnly {
		if perr := database.RecordPCachePointer(store, bookID, map[string][]string{provider: {key}}, pcacheNow()); perr != nil {
			pcacheLog.Warn("pointer row write failed: book=%s err=%v", logger.SanitizeLogValue(bookID), perr)
		}
	}
	return rs, err
}
