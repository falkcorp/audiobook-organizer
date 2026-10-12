// file: internal/server/handlers/pcache_stats.go
// version: 1.0.0
// guid: c53d70fc-2f66-45db-9cd2-239e74f3580d
// last-edited: 2026-10-11

package handlers

// Row counts of the shared provider-answer cache (pcache:) for /cache/stats
// and db-health. The total is cheap (the census families, else a key count);
// the expired count decodes every row, so it runs only on ?deep=true, through
// a deepWalker: one walk per handler shared by concurrent callers, bounded,
// and cached briefly.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// providerAnswerCacheName is the shared provider-answer cache's {cache}
// label and its /cache/stats row name.
const providerAnswerCacheName = database.PCacheMetricName

// pcacheCounter is the deep pcache: count: totals by kind and the rows no
// read would serve (database.CountPCache with the read path's TTL rule).
type pcacheCounter struct {
	deepWalker[database.PCacheCounts]
}

// pcacheRuleKey names the TTL settings a count was taken under, so a cached
// count is not served after they change.
func pcacheRuleKey() string {
	c := config.AppConfig
	return fmt.Sprintf("ttl=%d neg=%+v", c.MetadataFetchCacheTTLDays, c.MetadataFetch.NegativeTTLDays)
}

func (p *pcacheCounter) count(ctx context.Context, store database.PrefixPageScanner) (deepWalkResult[database.PCacheCounts], error) {
	return p.run(ctx, "shared cache count", pcacheRuleKey(), func(fctx context.Context) (database.PCacheCounts, int, error) {
		return database.CountPCache(fctx, store, metafetch.PCacheTTLFor, time.Now(), expiredCountPageSize)
	})
}

// pcacheCensusTotal sums the census families of the pcache: family (the
// root and its pcache:q:, pcache:asin:, pcache:book: children, which the
// census counts apart). A census lists every registered family, an empty one
// included, so a census is the answer whenever there is one.
func pcacheCensusTotal(census *database.DBCensus) (keys, errorBound int64, ok bool) {
	if census == nil {
		return 0, 0, false
	}
	for _, f := range census.Families {
		if strings.HasPrefix(f.Prefix, database.PCachePrefix) {
			keys += f.Keys
			errorBound += f.ErrorBoundKeys
		}
	}
	return keys, errorBound, true
}

// pcacheSize is the family's row count: the census estimate when the store
// keeps one, else an exact key count (skipped once the client is gone).
func pcacheSize(ctx context.Context, census *database.DBCensus, counter interface {
	CountPrefix(prefix string) (int64, error)
}) (total int64, estimated bool, errorBound int64, err error) {
	if keys, bound, ok := pcacheCensusTotal(census); ok {
		return keys, true, bound, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, false, 0, err
	}
	n, err := counter.CountPrefix(database.PCachePrefix)
	return n, false, 0, err
}
