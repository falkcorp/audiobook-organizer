// file: internal/database/pcache_rows.go
// version: 1.0.0
// guid: 104f175e-f508-4329-b64f-008913b24061
// last-edited: 2026-10-11

package database

// Whole-family views of the shared provider-answer cache (pcache:): which
// rows are dead, and how many rows of each kind there are. The sweep op
// (metafetch.pcache-sweep) deletes the dead rows; /cache/stats and db-health
// count them. Both judge a row with the read path's own rule
// (pcacheUnservable), so "expired" means exactly "no read would serve it".

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Kinds of pcache: row, for a PCacheTTLRule and the counts.
const (
	// PCacheKindQuestion is a P1 row (pcache:q:): one provider question's answer.
	PCacheKindQuestion = "question"
	// PCacheKindProduct is a P2 row (pcache:asin:): one product lookup by id.
	PCacheKindProduct = "product"
	// PCacheKindPointer is a book's pointer row (pcache:book:). It has no TTL;
	// it is dead only when its book is gone, which needs the library to tell.
	PCacheKindPointer = "pointer"
)

// PCacheDeadCorrupt is the reason a row that does not decode is dead.
const PCacheDeadCorrupt = PCacheMissCorrupt

// PCacheTTLRule returns the TTL the read path applies to a row of kind
// (PCacheKindQuestion or PCacheKindProduct) from provider. The rule lives in
// internal/metafetch (metafetch.PCacheTTLFor), which this package cannot
// import, so callers pass it in.
type PCacheTTLRule func(kind, provider string) PCacheTTL

// PCacheKindOf returns the kind of the pcache: row at key, or "" for a key
// outside the three row families.
func PCacheKindOf(key string) string {
	switch {
	case strings.HasPrefix(key, PCacheQuestionPrefix):
		return PCacheKindQuestion
	case strings.HasPrefix(key, PCacheASINPrefix):
		return PCacheKindProduct
	case strings.HasPrefix(key, PCacheBookPrefix):
		return PCacheKindPointer
	}
	return ""
}

// pcacheKeyProvider is the provider segment of a P1/P2 key, the fallback
// when an entry does not name its provider.
func pcacheKeyProvider(key string) string {
	rest := ""
	switch {
	case strings.HasPrefix(key, PCacheQuestionPrefix):
		rest = strings.TrimPrefix(key, PCacheQuestionPrefix)
	case strings.HasPrefix(key, PCacheASINPrefix):
		rest = strings.TrimPrefix(key, PCacheASINPrefix)
	default:
		return ""
	}
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// PCacheRowDead reports whether the P1/P2 row (key, value) would never be
// served again at now: past its TTL, empty while its provider's negative
// caching is off, or undecodable. reason is a PCacheMiss* value. A pointer
// row, or a key outside the family, is never dead here.
func PCacheRowDead(key string, value []byte, rule PCacheTTLRule, now time.Time) (dead bool, reason string) {
	kind := PCacheKindOf(key)
	if kind != PCacheKindQuestion && kind != PCacheKindProduct {
		return false, ""
	}
	var e PCacheEntry
	if err := json.Unmarshal(value, &e); err != nil {
		return true, PCacheDeadCorrupt
	}
	provider := e.Provider
	if provider == "" {
		provider = pcacheKeyProvider(key)
	}
	if reason := pcacheUnservable(&e, rule(kind, provider), now); reason != "" {
		return true, reason
	}
	return false, ""
}

// PCacheCounts is a whole-family count of the shared provider-answer cache.
type PCacheCounts struct {
	// Total is every pcache: row: questions, products and pointer rows.
	Total     int64 `json:"total"`
	Questions int64 `json:"questions"`
	Products  int64 `json:"products"`
	Pointers  int64 `json:"pointers"`
	// Expired is the question and product rows no read would serve
	// (PCacheRowDead): what the sweep would delete by TTL.
	Expired int64 `json:"expired"`
}

// CountPCache walks the whole pcache: family pageSize rows at a time and
// counts its rows by kind and the dead ones. Memory is bounded by pageSize;
// ctx is checked between pages. pages is returned on failure too.
func CountPCache(ctx context.Context, store RawKVStore, rule PCacheTTLRule, now time.Time, pageSize int) (c PCacheCounts, pages int, err error) {
	if pageSize <= 0 {
		pageSize = 1000
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return c, pages, err
		}
		pairs, next, err := store.ScanPrefixPage(PCachePrefix, after, pageSize)
		if err != nil {
			return c, pages, fmt.Errorf("pcache count: %w", err)
		}
		pages++
		for _, kv := range pairs {
			c.Total++
			switch PCacheKindOf(kv.Key) {
			case PCacheKindQuestion:
				c.Questions++
			case PCacheKindProduct:
				c.Products++
			case PCacheKindPointer:
				c.Pointers++
			}
			if dead, _ := PCacheRowDead(kv.Key, kv.Value, rule, now); dead {
				c.Expired++
			}
		}
		if next == "" {
			return c, pages, nil
		}
		after = next
	}
}
