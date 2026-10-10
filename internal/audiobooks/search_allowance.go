// file: internal/audiobooks/search_allowance.go
// version: 1.1.0
// guid: 1feebd0f-6ce3-434b-b259-94010968fefa
// last-edited: 2026-10-10

package audiobooks

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

// A pattern allowance is the pattern slot and the Budget one scan with a
// regex or glob filter runs under (querygrammar.AcquirePatternSlot,
// querygrammar.Budget).
//
// By default each scan takes its own. One Library list request runs TWO
// scans, the page (GetAudiobooksPage) and the total (CountAudiobooksFiltered),
// and with an allowance each it would hold two slots and spend up to twice
// the budget. A caller that runs both for one request wraps its ctx with
// WithSharedSearchAllowance: every scan under that ctx then takes the slot
// once (on the first scan that needs it) and spends ONE budget, so the
// request either finishes inside the budget or is refused once.

type searchAllowanceKey struct{}

// libraryPatternClock times every Library scan's pattern matches. It is
// time.Now in production; a test swaps in a clock that advances a fixed step
// per read, so each match costs exactly that step and a budget assertion is
// arithmetic rather than a wall-clock measurement under -race and load.
var libraryPatternClock = time.Now

// newLibraryPatternBudget is the Budget one Library scan (or one request's
// shared allowance) runs under: libraryPatternBudget on libraryPatternClock.
// Every budget patternAllowance hands out comes from here.
func newLibraryPatternBudget() *querygrammar.Budget {
	return querygrammar.NewBudgetClock(libraryPatternBudget, libraryPatternClock)
}

type sharedSearchAllowance struct {
	once    sync.Once
	budget  *querygrammar.Budget
	release func()
	err     error
	done    sync.Once
	ended   atomic.Bool
}

// WithSharedSearchAllowance returns ctx carrying one pattern allowance shared
// by every scan run under it, and a func that returns its slot. Call the func
// once every scan under ctx has returned.
func WithSharedSearchAllowance(ctx context.Context) (context.Context, func()) {
	a := &sharedSearchAllowance{}
	return context.WithValue(ctx, searchAllowanceKey{}, a), func() {
		a.done.Do(func() {
			a.ended.Store(true)
			a.once.Do(func() {}) // waits out an acquire in flight; else marks it never taken
			if a.release != nil {
				a.release()
			}
		})
	}
}

// patternAllowance returns the budget a scan with a pattern filter runs
// under and the func that ends this scan's use of it: the request's shared
// allowance when ctx carries one (the release is then the request's, so
// end is a no-op), else a fresh slot and budget for this scan alone.
func patternAllowance(ctx context.Context) (budget *querygrammar.Budget, end func(), err error) {
	// A scan that outlives its request's allowance (misuse) gets its own
	// rather than an untimed run.
	if a, ok := ctx.Value(searchAllowanceKey{}).(*sharedSearchAllowance); ok && !a.ended.Load() {
		a.once.Do(func() {
			a.release, a.err = querygrammar.AcquirePatternSlot(ctx)
			if a.err == nil {
				a.budget = newLibraryPatternBudget()
			}
		})
		// once.Do returns after the winning call finished, so these reads are
		// ordered after its writes. A nil budget with a nil error means the
		// request's done() won the race for the Once (it marks "never taken")
		// between the ended check above and here: the allowance is over, so
		// this misused scan takes its own below rather than run untimed.
		if a.err != nil || a.budget != nil {
			return a.budget, func() {}, a.err
		}
	}
	release, err := querygrammar.AcquirePatternSlot(ctx)
	if err != nil {
		return nil, nil, err
	}
	return newLibraryPatternBudget(), release, nil
}
