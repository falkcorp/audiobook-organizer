// file: internal/server/owner_rejected_rerank.go
// version: 1.0.0
// guid: f8c8e21e-cb61-4f38-900e-80bbda768cca
// last-edited: 2026-10-10

package server

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// An owner rejection re-orders the book's cached candidate row (2026-10-10).
// Slot 0 of that row is what the review lane shows and what every bulk apply
// reads; until this date a rejection was only a key the batch fetch's own
// pick consulted, so the rejected candidate stayed in slot 0 and the review
// page's apply buttons applied it. The applygate owner_rejected leg refuses
// it as a backstop; this moves it out of the way so the book's next-best
// candidate is the one shown and applied.

var ownerRejectedLog = logger.New("server.owner-rejected")

// rerankStore is what re-ranking a book's cached row reads: the book (the
// ranker judges candidates against it) and the owner's rejections.
type rerankStore interface {
	GetBookByID(id string) (*database.Book, error)
	metabatch.RejectedCandidateReader
}

// cachedRowReranker is the metafetch.Service method that re-orders a row.
type cachedRowReranker interface {
	RerankCachedCandidates(bookID string, rank func(metafetch.MetadataCandidate) int) (metafetch.RerankOutcome, error)
}

// rerankCounts is what one rerankCachedRows pass did, for the log.
type rerankCounts struct {
	Books       int64
	Reordered   int64
	Unchanged   int64
	NoRow       int64
	BookMissing int64
	Errors      int64
}

func (c *rerankCounts) String() string {
	return fmt.Sprintf("books=%d reordered=%d already_ordered=%d no_row=%d book_missing=%d errors=%d",
		atomic.LoadInt64(&c.Books), atomic.LoadInt64(&c.Reordered), atomic.LoadInt64(&c.Unchanged),
		atomic.LoadInt64(&c.NoRow), atomic.LoadInt64(&c.BookMissing), atomic.LoadInt64(&c.Errors))
}

// rerankConcurrency bounds the re-rank pool: each book is a few store reads
// and at most one write, so the pool is sized to the CPUs like the review
// listing's loader.
func rerankConcurrency() int { return max(2, runtime.NumCPU()) }

// rerankCachedRows re-orders the cached candidate row of every book in ids
// by metabatch.MergeRanker -- owner-rejected candidates last -- over a
// bounded worker pool. Each book is one worker's alone (ids are
// deduplicated), and the row write itself is under metafetch's row lock, so
// no two writers touch one row. It only re-orders: no candidate is removed.
// A cancelled ctx stops it early.
func rerankCachedRows(ctx context.Context, store rerankStore, svc cachedRowReranker, ids []string) *rerankCounts {
	counts := &rerankCounts{}
	if svc == nil || store == nil {
		return counts
	}
	seen := make(map[string]bool, len(ids))
	var g errgroup.Group
	g.SetLimit(rerankConcurrency())
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		atomic.AddInt64(&counts.Books, 1)
		g.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			book, err := store.GetBookByID(id)
			if err != nil {
				atomic.AddInt64(&counts.Errors, 1)
				ownerRejectedLog.Warn("re-rank: book read failed: book_id=%s err=%v", logger.SanitizeLogValue(id), err)
				return nil
			}
			if book == nil {
				atomic.AddInt64(&counts.BookMissing, 1)
				return nil
			}
			// Read here, not through MergeRanker's loader: a failed read must
			// count as an error (so the one-time pass retries), never rank
			// the row as if nothing were rejected.
			rejected, err := metafetch.LoadRejectedCandidates(store, id)
			if err != nil {
				atomic.AddInt64(&counts.Errors, 1)
				ownerRejectedLog.Warn("re-rank: owner rejections unreadable: book_id=%s err=%v", logger.SanitizeLogValue(id), err)
				return nil
			}
			out, err := svc.RerankCachedCandidates(id, metabatch.MergeRankerFor(book, rejected))
			switch {
			case err != nil:
				atomic.AddInt64(&counts.Errors, 1)
				ownerRejectedLog.Warn("re-rank: cache row not re-ordered: book_id=%s err=%v", logger.SanitizeLogValue(id), err)
			case out == metafetch.RerankReordered:
				atomic.AddInt64(&counts.Reordered, 1)
			case out == metafetch.RerankUnchanged:
				atomic.AddInt64(&counts.Unchanged, 1)
			default:
				atomic.AddInt64(&counts.NoRow, 1)
			}
			return nil
		})
	}
	_ = g.Wait()
	return counts
}

// ownerRejectedRerankDoneKey marks the one-time re-order of every cached row
// that held an owner-rejected candidate when the rejection began re-ordering
// the row (2026-10-10).
const ownerRejectedRerankDoneKey = "system:migration:owner_rejected_rerank_v1_done"

// ownerRejectedRerankPageSize is how many rejection keys one page of the
// scan reads (the keyspace grows with the owner's rejections). A var so a
// test can page a small keyspace.
var ownerRejectedRerankPageSize = 1000

// ownerRejectedRerankStore is what the one-time re-order reads and writes.
type ownerRejectedRerankStore interface {
	rerankStore
	ScanPrefixPage(prefix, after string, limit int) (pairs []database.KVPair, next string, err error)
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// ownerRejectedRerankResult reports one rerankOwnerRejectedRows call.
type ownerRejectedRerankResult struct {
	AlreadyDone bool
	Keys        int
	Counts      *rerankCounts
	FlagSet     bool
}

// rerankOwnerRejectedRows is the one-shot data fix for rows written before
// a rejection re-ordered them: production rows hold rejected candidates in
// slot 0. It scans the rejection keyspace once (paged), groups the keys by
// book, and re-ranks each of those books' rows (rerankCachedRows: bounded
// pool, re-order only, never a delete). The done flag is set only when the
// scan completed and no book failed; otherwise the next start retries, and a
// retry is harmless (an ordered row is not written again).
func rerankOwnerRejectedRows(ctx context.Context, store ownerRejectedRerankStore, svc cachedRowReranker, now time.Time) (ownerRejectedRerankResult, error) {
	var res ownerRejectedRerankResult
	if store == nil || svc == nil {
		return res, nil
	}
	if s, err := store.GetSetting(ownerRejectedRerankDoneKey); err == nil && s != nil && s.Value != "" {
		res.AlreadyDone = true
		return res, nil
	}
	var ids []string
	seen := map[string]bool{}
	after := ""
	for {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		pairs, next, err := store.ScanPrefixPage(metafetch.RejectedCandidatePrefix, after, ownerRejectedRerankPageSize)
		if err != nil {
			return res, fmt.Errorf("scan owner rejections: %w", err)
		}
		for _, kv := range pairs {
			res.Keys++
			if bookID, _, ok := metafetch.ParseRejectedCandidateKey(kv.Key); ok && !seen[bookID] {
				seen[bookID] = true
				ids = append(ids, bookID)
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	ownerRejectedLog.Info("one-time re-order of cached rows with owner-rejected candidates: starting: rejection_keys=%d books=%d", res.Keys, len(ids))
	res.Counts = rerankCachedRows(ctx, store, svc, ids)
	ownerRejectedLog.Info("one-time re-order of cached rows with owner-rejected candidates: %s", res.Counts)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if atomic.LoadInt64(&res.Counts.Errors) > 0 {
		ownerRejectedLog.Warn("one-time re-order: %d books failed; not marking done, will retry next start", atomic.LoadInt64(&res.Counts.Errors))
		return res, nil
	}
	if err := store.SetSetting(ownerRejectedRerankDoneKey, now.UTC().Format(time.RFC3339), "string", false); err != nil {
		return res, fmt.Errorf("record owner-rejected re-order done flag: %w", err)
	}
	res.FlagSet = true
	return res, nil
}

// startOwnerRejectedRerank runs rerankOwnerRejectedRows once per start in a
// bgWG-tracked goroutine (it reads every book with a rejection, so it never
// holds up the start), cancelled with the server.
func (s *Server) startOwnerRejectedRerank() {
	if s.metadataFetchService == nil {
		return
	}
	store, svc, ctx := s.storeForWiring(), s.metadataFetchService, s.bgCtx
	if ctx == nil {
		ctx = context.Background()
	}
	s.bgWG.Go("owner-rejected-rerank", func() {
		if _, err := rerankOwnerRejectedRows(ctx, store, svc, time.Now()); err != nil && ctx.Err() == nil {
			ownerRejectedLog.Error("one-time re-order of cached rows with owner-rejected candidates failed; will retry next start: %v", err)
		}
	})
}
