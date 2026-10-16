// file: internal/identification/advance.go
// version: 1.0.0
// guid: d6a0d049-6ba0-4351-921f-20f70d88eebf
// last-edited: 2026-10-16

// Package identification is the body of the identification.advance driver
// (spec 02 section 3.2 "The driver", section 4 PR 14): it drains the durable
// dirty set (ident:dirty:<bookID>) and runs each marked book once through the
// existing question, candidate-search, scoring and decision pipeline.
//
// The package is written in the ops-v3 Batch shape (05-operations-v3
// sdk-api.md 1.2 and 1.5) so the port is an adapter swap: it holds ONLY the
// Source (Pages over the dirty set), the Item function (item.go) and the
// Finish counters. It has no loop, no worker pool, no chunking, no progress
// and no checkpoint code, and it must never import the operations registry
// (TestAdvance_NoRegistryImport). All of that belongs to the runner: today
// the v2 adapter in internal/server/identification_advance_op.go, which calls
// registry.RunItems one page at a time; after the port, the v3 chunk-leasing
// runner (owner decision D28a) with Source: ops.DirtySet("ident:dirty:").
package identification

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// PageSize is one page of the dirty set. It is D28a's chunk size and the same
// constant the v3 DirtySet uses, so after the port a leased chunk is exactly
// one page (spec 02 section 6, r2 check item 3).
const PageSize = 256

// Mark is one dirty-set entry: the book and the epoch its mark carried when
// the page was read. Item clears the mark by compare-and-delete on this epoch,
// so a writer that re-marks the book while Item runs (a newer epoch) keeps
// it dirty.
type Mark struct {
	BookID string
	Epoch  uint64
}

// Key is the source's stable item key: the book id (v3 Source.Key).
func Key(m Mark) string { return m.BookID }

// PartitionBy routes every item with the same key to the same worker (v3
// BatchSpec.PartitionBy). The key is the book id. Within one page the dirty
// set's keys are unique (one key per book), so no two workers ever hold the
// same book; across pages the runner processes one page after another. That
// is the exclusive-access argument CLAUDE.md asks for: Item writes only its
// own book's cache row, reason and mark, and no other worker can hold that
// book at the same time. A concurrent metadata.candidate-fetch of the same
// book is excluded separately, by the server's per-book claim around FetchOne.
func PartitionBy(m Mark) string { return m.BookID }

// Order says what a cursor over the source may assume (v3 SourceOrder).
type Order int

// Unordered: a re-mark can land below the cursor (the key is the book id, not
// a sequence), so a resumed run starts from the beginning of the dirty set.
// That is cheap: a processed entry is gone.
const Unordered Order = 1

// Source yields the dirty set a page at a time (v3 Source.Pages). next is the
// cursor for the following page; an empty page ends the stream. No slice of
// every mark is ever materialized.
type Source interface {
	Pages(ctx context.Context, after string, limit int) (page []Mark, next string, err error)
}

// DirtyPager is the store capability DirtySet reads.
type DirtyPager interface {
	IdentDirtyPage(after string, limit int) ([]database.IdentDirtyEntry, error)
}

// DirtySet is the Source over ident:dirty:, in book-id key order.
type DirtySet struct {
	Store DirtyPager
}

// Pages returns up to limit marks after the book id after.
func (d DirtySet) Pages(ctx context.Context, after string, limit int) ([]Mark, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, after, err
	}
	entries, err := d.Store.IdentDirtyPage(after, limit)
	if err != nil {
		return nil, after, fmt.Errorf("identification: read dirty set after %q: %w", after, err)
	}
	if len(entries) == 0 {
		return nil, after, nil
	}
	page := make([]Mark, len(entries))
	for i, e := range entries {
		page[i] = Mark{BookID: e.BookID, Epoch: e.Epoch}
	}
	return page, page[len(page)-1].BookID, nil
}

// Outcome is what Item did with one mark; Finish counts them.
type Outcome string

const (
	// OutcomeSkippedDone: nothing to ask. The book is gone, not in the
	// primary scope, quarantined, applied or held, or has no usable title;
	// the mark is cleared without a provider call.
	OutcomeSkippedDone Outcome = "skipped_done"
	// OutcomeFetched: the per-book pipeline ran (from the cache or live).
	OutcomeFetched Outcome = "fetched"
	// OutcomeDeferred: a provider step was put off (quota, throttle); the
	// book carries reason quota_deferred and the safety net re-marks it.
	OutcomeDeferred Outcome = "deferred"
	// OutcomeError: the pipeline or a store write failed for this book.
	OutcomeError Outcome = "error"
)

// ItemFunc processes one mark (v3 BatchSpec.Item). It must be idempotent: the
// runner may run it again for the same mark after a restart.
type ItemFunc func(ctx context.Context, m Mark) (Outcome, error)

// Summary is what Finish receives: the counters of one run (v3 Summary).
type Summary struct {
	Done     int64
	Counters map[string]int64
	Canceled bool
}

// String renders the summary for the op log.
func (s Summary) String() string {
	return fmt.Sprintf("done=%d fetched=%d skipped_done=%d deferred=%d errors=%d canceled=%v",
		s.Done, s.Counters[string(OutcomeFetched)], s.Counters[string(OutcomeSkippedDone)],
		s.Counters[string(OutcomeDeferred)], s.Counters[string(OutcomeError)], s.Canceled)
}

// Tally counts outcomes. Every method is safe for concurrent use: the runner
// records from its worker goroutines, and a v2 Label may read Done from them
// too.
type Tally struct {
	skippedDone, fetched, deferred, errored atomic.Int64
}

// Record counts one outcome. An empty outcome (an item stopped by a cancel)
// counts nothing.
func (t *Tally) Record(o Outcome) {
	switch o {
	case OutcomeSkippedDone:
		t.skippedDone.Add(1)
	case OutcomeFetched:
		t.fetched.Add(1)
	case OutcomeDeferred:
		t.deferred.Add(1)
	case OutcomeError:
		t.errored.Add(1)
	}
}

// Done is the number of items that settled.
func (t *Tally) Done() int64 {
	return t.skippedDone.Load() + t.fetched.Load() + t.deferred.Load() + t.errored.Load()
}

// Summary snapshots the counters.
func (t *Tally) Summary(canceled bool) Summary {
	return Summary{
		Done: t.Done(),
		Counters: map[string]int64{
			string(OutcomeSkippedDone): t.skippedDone.Load(),
			string(OutcomeFetched):     t.fetched.Load(),
			string(OutcomeDeferred):    t.deferred.Load(),
			string(OutcomeError):       t.errored.Load(),
		},
		Canceled: canceled,
	}
}

// Advance is the whole op body in the v3 Batch shape. A runner reads it; it
// runs nothing itself.
type Advance struct {
	Source Source
	// Order is Unordered: see the constant.
	Order Order
	// Key is the item key (Key: the book id).
	Key func(Mark) string
	// Item is the per-book pipeline (NewItem).
	Item ItemFunc
	// Finish receives the run's counters once, also after a cancel.
	Finish func(Summary)
	// Label names an item for the "currently working on" line. It reads the
	// item only, never a shared counter, so it cannot race.
	Label func(Mark) string
	// PartitionBy is PartitionBy: the book id.
	PartitionBy func(Mark) string
	// Concurrency is the worker count, and ConcurrencyBasis how it was
	// derived (the v3 Network(n, why) form). Item is network-bound.
	Concurrency      int
	ConcurrencyBasis string
}

// New builds the Advance over source, with the fixed Key, Order, Label and
// PartitionBy of the identification driver.
func New(source Source, item ItemFunc, finish func(Summary), concurrency int, basis string) Advance {
	return Advance{
		Source:           source,
		Order:            Unordered,
		Key:              Key,
		Item:             item,
		Finish:           finish,
		Label:            func(m Mark) string { return "book " + m.BookID },
		PartitionBy:      PartitionBy,
		Concurrency:      concurrency,
		ConcurrencyBasis: basis,
	}
}
