// file: internal/querygrammar/budget.go
// version: 1.0.0
// guid: 1771412b-a65b-42bd-815e-dcf0c2cf7c1d
// last-edited: 2026-10-10

package querygrammar

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Bounding the TIME a search spends in patterns.
//
// MaxTextValueBytes and MaxPatternInst refuse absurd values at compile time,
// but no size limit bounds the time a scan takes: the cost of one regex or
// glob match is roughly program size times input length, and some one-
// instruction classes (\pL, \w, \s) cost far more than others. Measured over
// 40,000 rows of 85-character titles, (?:\pL?){45}zzz (95 instructions) took
// 3.2 s, and three such tokens on one request three times that. So a caller
// that matches user patterns across a library-sized set gives the evaluation
// a Budget and stops when it is spent.
//
// A Budget times only the regex and glob matches themselves (Budget.Match),
// not the wall-clock time of the scan: a Library scan also reads rows from
// Pebble and per-user state, and a slow disk or a cold cache must not turn a
// plain search into "simplify your pattern". Timing every match also scales
// with the number of pattern tokens and with field length without any
// tuning: forty slow tokens spend the budget forty times as fast as one.

// DefaultPatternBudget is how much time one evaluation may spend matching
// regex and glob patterns before it is stopped.
const DefaultPatternBudget = time.Second

// TooSlowError is returned when an evaluation spent its Budget. Callers
// answer it with a 400: the search, as written, cannot be answered within
// the server's limit, and nothing partial is ever returned in its place.
type TooSlowError struct {
	Budget time.Duration
}

func (e *TooSlowError) Error() string {
	return fmt.Sprintf("the search was too slow: it spent more than %s matching patterns and was stopped. "+
		"Try a simpler search, such as fewer regex or * terms, shorter patterns, or another filter that narrows it first", e.Budget)
}

// BusyError is returned when every pattern-evaluation slot stayed taken for
// the whole wait. Callers answer it with a 503 and Retry-After.
type BusyError struct {
	Slots  int
	Waited time.Duration
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("the server is already running %d pattern searches and none finished within %s; try again in a moment", e.Slots, e.Waited)
}

// Budget is the pattern-matching time one evaluation may spend. Safe for
// concurrent use; a nil *Budget times nothing and never expires.
type Budget struct {
	limit   time.Duration
	now     func() time.Time
	spent   atomic.Int64 // nanoseconds
	expired atomic.Bool
}

// NewBudget returns a Budget of limit, timed by the wall clock on EVERY
// regex or glob match. Timing costs two clock reads, about 70 ns per match
// on an M1 Max (BenchmarkCompiledPredicate_100k/title-regex: 7.6 ms without
// a budget, 15 ms with one, per 100,000 rows). Timing a random sample and
// scaling it up was tried and dropped: a sampled match the scheduler
// preempted for 10 ms was charged eight times over, and under load that
// refused searches well inside their real budget.
func NewBudget(limit time.Duration) *Budget {
	return NewBudgetClock(limit, time.Now)
}

// NewBudgetClock is NewBudget with an injected clock, so tests can make a
// match cost an exact amount.
func NewBudgetClock(limit time.Duration, now func() time.Time) *Budget {
	b := &Budget{limit: limit, now: now}
	if limit <= 0 {
		b.expired.Store(true)
	}
	return b
}

// Costly reports whether matching m runs a compiled program (a regex or a
// glob). Substring and non-empty matches are a scan of the input with no
// program, and are not timed.
func (m *TextMatcher) Costly() bool {
	return m.Kind == KindRegex || m.Kind == KindGlob
}

// Match is m.Match(s), timed against the budget when m is Costly. Once the
// budget is spent it returns false without matching; the caller must then
// discard the whole evaluation (Err), since a row that "did not match"
// after expiry is unknown, not false.
func (b *Budget) Match(m *TextMatcher, s string) bool {
	if b == nil || !m.Costly() {
		return m.Match(s)
	}
	if b.expired.Load() {
		return false
	}
	start := b.now()
	ok := m.Match(s)
	if b.spent.Add(int64(b.now().Sub(start))) >= int64(b.limit) {
		b.expired.Store(true)
	}
	return ok
}

// Expired reports whether the budget is spent.
func (b *Budget) Expired() bool {
	return b != nil && b.expired.Load()
}

// Spent is the pattern-matching time charged so far.
func (b *Budget) Spent() time.Duration {
	if b == nil {
		return 0
	}
	return time.Duration(b.spent.Load())
}

// Err is a *TooSlowError once the budget is spent, else nil.
func (b *Budget) Err() error {
	if !b.Expired() {
		return nil
	}
	return &TooSlowError{Budget: b.limit}
}

// Concurrency: a process-wide bound on evaluations that run patterns.
//
// Each such evaluation may spend up to DefaultPatternBudget of one core. With
// no bound, N concurrent slow searches take N cores for that long. Callers
// acquire one slot per evaluation that has at least one Costly matcher (an
// evaluation without one is a plain scan and takes none), wait at most
// patternSlotWait for it, and answer a BusyError with a 503.

var (
	patternSlotsMu  sync.Mutex
	patternSlots    = make(chan struct{}, defaultPatternSlots())
	patternSlotWait = 2 * time.Second
)

func defaultPatternSlots() int {
	return max(2, runtime.NumCPU()/2)
}

// AcquirePatternSlot takes one pattern-evaluation slot, waiting at most the
// slot wait (or until ctx is done). release returns the slot and must be
// called exactly once. Acquire at ONE level of a call chain: a nested
// acquire would hold a slot while waiting for another.
func AcquirePatternSlot(ctx context.Context) (release func(), err error) {
	patternSlotsMu.Lock()
	slots, wait := patternSlots, patternSlotWait
	patternSlotsMu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	default:
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-timer.C:
		return nil, &BusyError{Slots: cap(slots), Waited: wait}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SetPatternSlotsForTesting replaces the slot pool with n slots and the wait
// with wait, returning a func that restores both. Slots held from the old
// pool are released into the old pool. Tests only.
func SetPatternSlotsForTesting(n int, wait time.Duration) (restore func()) {
	patternSlotsMu.Lock()
	oldSlots, oldWait := patternSlots, patternSlotWait
	patternSlots, patternSlotWait = make(chan struct{}, n), wait
	patternSlotsMu.Unlock()
	return func() {
		patternSlotsMu.Lock()
		patternSlots, patternSlotWait = oldSlots, oldWait
		patternSlotsMu.Unlock()
	}
}

// ShortToken is a value as an error message names it: whole when short, else
// its first 64 bytes (cut on a rune boundary) and an ellipsis, so a refused
// long value is not echoed back in full.
func ShortToken(v string) string {
	const keep = 64
	if len(v) <= keep {
		return v
	}
	cut := keep
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut] + "…"
}
