// file: internal/metadata/dailyquota/dailyquota.go
// version: 1.0.0
// guid: 00535d2d-567d-4418-abc2-bf8b732856d9
// last-edited: 2026-10-06

// Package dailyquota counts a metadata provider's lookups per quota day and
// refuses one past the day's limit, with two tiers on ONE counter:
// background work (the scheduled candidate fetch's fallback, the bulk fetch,
// any op) stops at the background limit, and interactive work (a person
// waiting on the search dialog or a test-connection click) may go on to the
// full limit. Owner decision 2026-10-06: one 1,000/day Google Books counter,
// background stops at 800 so 200 stay reserved for interactive.
//
// The budget is enforced in the provider's HTTP transport
// (internal/metadata/providerhttp), where every request -- every retry
// included -- passes, so no caller can reach the provider around it. The
// tier travels on the request context: a context nobody marked is
// BACKGROUND, so a new caller that forgets to say what it is gets the
// smaller allowance, never the reserved one.
//
// A stdlib-only leaf package: providerhttp, metadata and metafetch all
// import it.
package dailyquota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	// The zone database, embedded: a container image without one would
	// otherwise roll the quota day at 01:00 Pacific during daylight saving
	// (quotaZone's fixed-offset fallback).
	_ "time/tzdata"
)

// Priority is a lookup's tier.
type Priority int

const (
	// Background is every lookup no person is waiting on. The default.
	Background Priority = iota
	// Interactive is a lookup a person started and is waiting on.
	Interactive
)

func (p Priority) String() string {
	if p == Interactive {
		return "interactive"
	}
	return "background"
}

type interactiveKey struct{}

// WithInteractive marks ctx's lookups interactive.
func WithInteractive(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveKey{}, true)
}

// PriorityOf returns ctx's tier: Interactive only when WithInteractive marked
// it, Background otherwise (nil included).
func PriorityOf(ctx context.Context) Priority {
	if ctx != nil {
		if v, _ := ctx.Value(interactiveKey{}).(bool); v {
			return Interactive
		}
	}
	return Background
}

// RawKV is the store slice a budget persists through: database.Store's
// GetRaw/SetRaw.
type RawKV interface {
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
}

// KeyPrefix is the Pebble keyspace of the daily budgets:
// provider_daily_budget:<provider id> -> state JSON. Registered in
// internal/database/keyfamilies.go.
const KeyPrefix = "provider_daily_budget:"

// state is one provider's persisted count for one quota day.
type state struct {
	// Day is the quota day (YYYY-MM-DD in the budget's zone) Used counts.
	Day  string `json:"day"`
	Used int    `json:"used"`
	// UpdatedAt is when the count last moved, for an operator reading the row.
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrDailyBudgetSpent is returned when the day's lookups for the caller's
// tier are used up. It is a refusal made before any request is sent: not a
// provider failure, so it must never trip a circuit breaker or a throttle
// hold (metadata.ProtectedSource, metadata.ClassifyProviderError).
var ErrDailyBudgetSpent = errors.New("daily lookup budget spent")

// SpentError is ErrDailyBudgetSpent with the count that refused it.
type SpentError struct {
	Provider string
	Priority Priority
	Used     int
	Limit    int
}

func (e *SpentError) Error() string {
	return fmt.Sprintf("%s daily lookup budget spent: %d/%d %s lookups used today; left for the next quota day",
		e.Provider, e.Used, e.Limit, e.Priority)
}

// Unwrap makes errors.Is(err, ErrDailyBudgetSpent) hold.
func (e *SpentError) Unwrap() error { return ErrDailyBudgetSpent }

// Limits is a budget's two caps for one quota day: Total is what an
// interactive lookup may reach, Background what a background one may. A
// Background above Total is read as Total.
type Limits struct {
	Total      int
	Background int
}

// For returns p's cap.
func (l Limits) For(p Priority) int {
	if p == Interactive {
		return max(l.Total, 0)
	}
	return max(min(l.Background, l.Total), 0)
}

// DailyBudget counts one provider's lookups per quota day. The count is
// persisted on every reservation, so a restart resumes the day where it stood
// instead of granting a fresh limit (prod restarted 146 times in 30 days).
//
// It is a COUNTER, not a rate limiter: the request rate is paced by
// providerhttp's token bucket, and a provider that answers 429 is held by the
// throttle registry; neither counts calls per day, which is the one thing a
// daily key quota needs.
//
// A reservation is taken BEFORE the request and never refunded: a request
// that errored still counts. Over-counting a quota is the safe direction.
// Safe for concurrent use.
type DailyBudget struct {
	mu       sync.Mutex
	store    RawKV
	provider string
	limits   func() Limits
	now      func() time.Time
	loc      *time.Location
	// mem is the count when no store is attached: in-memory only.
	mem state
}

// quotaZone is where Google's per-day quotas roll over: midnight Pacific. The
// zone database is embedded (the time/tzdata import above), so LoadLocation
// cannot miss it in a container image without one; the fixed UTC-8 fallback
// (01:00 Pacific during daylight saving) is reachable only if that import is
// removed. TestQuotaZone_IsPacific pins it.
func quotaZone() *time.Location {
	if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return loc
	}
	return time.FixedZone("PST", -8*60*60)
}

// New returns provider's daily budget, persisted in store (nil: in-memory
// only). limits is read on every reservation, so a settings change takes
// effect without a restart; a cap of 0 or less refuses every lookup of that
// tier. provider is normalized once here (trimmed), so the key it persists
// under and the id Provider reports are the same string.
func New(store RawKV, provider string, limits func() Limits) *DailyBudget {
	return &DailyBudget{store: store, provider: strings.TrimSpace(provider), limits: limits, now: time.Now, loc: quotaZone()}
}

// SetClock replaces the budget's clock (tests).
func (b *DailyBudget) SetClock(now func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
}

// Provider returns the provider id the budget counts.
func (b *DailyBudget) Provider() string { return b.provider }

// Key returns the store key the budget persists under.
func (b *DailyBudget) Key() string { return KeyPrefix + b.provider }

// load returns today's state; a row for an earlier day reads as 0 used.
func (b *DailyBudget) load(day string) (state, error) {
	st := b.mem
	if b.store != nil {
		raw, err := b.store.GetRaw(b.Key())
		if err != nil {
			return state{}, fmt.Errorf("read %s daily budget: %w", b.provider, err)
		}
		st = state{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &st); err != nil {
				return state{}, fmt.Errorf("decode %s daily budget: %w", b.provider, err)
			}
		}
	}
	if st.Day != day {
		st = state{Day: day}
	}
	return st, nil
}

func (b *DailyBudget) save(st state) error {
	if b.store == nil {
		b.mem = st
		return nil
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode %s daily budget: %w", b.provider, err)
	}
	if err := b.store.SetRaw(b.Key(), raw); err != nil {
		return fmt.Errorf("save %s daily budget: %w", b.provider, err)
	}
	return nil
}

func (b *DailyBudget) today() string { return b.now().In(b.loc).Format("2006-01-02") }

// Reserve takes one lookup of tier p from today's count. It returns a
// *SpentError (errors.Is ErrDailyBudgetSpent) when p's cap is reached, and any
// other error when the count cannot be read or persisted -- the lookup is then
// refused too: a quota spend that cannot be counted is not made.
func (b *DailyBudget) Reserve(p Priority) (used, limit int, err error) {
	if b == nil {
		return 0, 0, ErrDailyBudgetSpent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	limit = b.limits().For(p)
	st, err := b.load(now.In(b.loc).Format("2006-01-02"))
	if err != nil {
		return 0, limit, err
	}
	if st.Used >= limit {
		return st.Used, limit, &SpentError{Provider: b.provider, Priority: p, Used: st.Used, Limit: limit}
	}
	st.Used++
	st.UpdatedAt = now.UTC()
	if err := b.save(st); err != nil {
		return st.Used - 1, limit, err
	}
	return st.Used, limit, nil
}

// Remaining returns how many lookups of tier p today's count has left (0 when
// spent, or when the count cannot be read).
func (b *DailyBudget) Remaining(p Priority) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st, err := b.load(b.today())
	if err != nil {
		return 0
	}
	return max(b.limits().For(p)-st.Used, 0)
}

// Limit returns tier p's cap as configured now.
func (b *DailyBudget) Limit(p Priority) int {
	if b == nil {
		return 0
	}
	return b.limits().For(p)
}

// Used returns today's count (0 when it cannot be read).
func (b *DailyBudget) Used() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st, err := b.load(b.today())
	if err != nil {
		return 0
	}
	return st.Used
}

// The process-wide budgets, by provider id. A provider with none installed
// is not counted.
var (
	regMu    sync.RWMutex
	registry = map[string]*DailyBudget{}
)

// Install makes b provider's process-wide budget (b.Provider()), replacing
// any earlier one, and returns a func restoring the previous one (tests).
func Install(b *DailyBudget) (restore func()) {
	regMu.Lock()
	defer regMu.Unlock()
	id := b.Provider()
	prev, had := registry[id]
	registry[id] = b
	return func() {
		regMu.Lock()
		defer regMu.Unlock()
		if had {
			registry[id] = prev
		} else {
			delete(registry, id)
		}
	}
}

// For returns provider's process-wide budget, nil when none is installed.
func For(provider string) *DailyBudget {
	regMu.RLock()
	defer regMu.RUnlock()
	return registry[strings.TrimSpace(provider)]
}

// ReserveFor takes one lookup from provider's process-wide budget at ctx's
// tier. nil when the provider has no budget.
func ReserveFor(ctx context.Context, provider string) error {
	b := For(provider)
	if b == nil {
		return nil
	}
	_, _, err := b.Reserve(PriorityOf(ctx))
	return err
}
