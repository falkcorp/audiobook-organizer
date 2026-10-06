// file: internal/metafetch/candidate_fallback.go
// version: 1.0.0
// guid: 2b452994-605f-4efc-9523-eccefaafce31
// last-edited: 2026-10-06

package metafetch

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// CandidateFallbackProviderIDs are the providers the batch candidate fetch
// (metadata.candidate-fetch) asks only AFTER the rest of the chain found
// nothing for a book, in this order: Open Library, then Google Books.
//
// Owner decision 2026-10-06 ("Both, spread over days"): a book Audible cannot
// match is tried on Open Library first, then on Google Books under a daily
// budget (DailyBudget) well below Google's 1,000 queries/day key quota. Every
// other search path (the interactive search dialog, the bulk fetch, the
// maintenance.isbn-enrichment op) still asks every enabled source at once;
// only the batch candidate fetch tiers them. (The ASIN backfill asks Audible
// only.)
var CandidateFallbackProviderIDs = []string{metadata.SourceIDOpenLibrary, metadata.SourceIDGoogleBooks}

// IsCandidateFallbackProvider reports whether id is one of
// CandidateFallbackProviderIDs.
func IsCandidateFallbackProvider(id string) bool {
	for _, f := range CandidateFallbackProviderIDs {
		if f == id {
			return true
		}
	}
	return false
}

// ActiveSourceNamesByID maps the provider id of every source a search would
// ask right now (the test override, or the configured chain) to its display
// name. The fetch cache's EmptyAnswers and SearchOptions.OnlySources are
// keyed by display name ("Google Books"), and the fallback order by provider
// id ("google-books"); this is the one translation between them. A source
// that declares no id (a test stub) is left out: it is never a fallback.
func (mfs *Service) ActiveSourceNamesByID() map[string]string {
	if mfs == nil {
		return nil
	}
	sources := mfs.overrideSources
	if len(sources) == 0 {
		sources = mfs.BuildSourceChain()
	}
	out := make(map[string]string, len(sources))
	for _, src := range sources {
		if id := metadata.ProviderIDOf(src); id != "" {
			out[id] = src.Name()
		}
	}
	return out
}

// DefaultGoogleBooksFallbackDailyLimit is the Google Books fallback's daily
// budget when none is configured: 800 of the key's 1,000 queries/day, leaving
// headroom for the paths that still ask Google directly (the interactive
// search dialog, the bulk fetch, maintenance.isbn-enrichment). Those paths
// are not counted against this budget.
const DefaultGoogleBooksFallbackDailyLimit = 800

// RawKV is the store slice DailyBudget persists through: database.Store's
// GetRaw/SetRaw. Nothing is added to database.Store.
type RawKV interface {
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
}

// dailyBudgetKeyPrefix is the Pebble keyspace of the daily budgets:
// provider_daily_budget:<provider id> -> dailyBudgetState JSON.
const dailyBudgetKeyPrefix = "provider_daily_budget:"

// dailyBudgetState is one provider's persisted count for one quota day.
type dailyBudgetState struct {
	// Day is the quota day (YYYY-MM-DD in the budget's zone) Used counts.
	Day  string `json:"day"`
	Used int    `json:"used"`
	// UpdatedAt is when the count last moved, for an operator reading the row.
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrDailyBudgetSpent is returned by DailyBudget.Reserve when today's
// lookups are used up. The book is left for the next quota day.
var ErrDailyBudgetSpent = errors.New("daily lookup budget spent")

// DailyBudget counts one provider's lookups per quota day and refuses one
// past the day's limit. The count is persisted on every reservation (RawKV),
// so a restart resumes the day where it stood instead of granting a fresh
// limit: prod restarted 146 times in 30 days.
//
// It is a COUNTER, not a rate limiter. The provider's request rate is paced
// by its providerhttp token bucket and the candidate op's own gate, and a
// provider that answers 429 is held by the persisted throttle registry
// (metadata.DefaultThrottleRegistry); neither counts calls per day, which is
// the one thing a 1,000/day quota needs.
//
// A reservation is taken BEFORE the lookup and never refunded: a lookup the
// fetch cache answered, or one that errored, still counts. Over-counting a
// quota is the safe direction. Safe for concurrent use.
type DailyBudget struct {
	mu       sync.Mutex
	store    RawKV
	provider string
	limit    func() int
	now      func() time.Time
	loc      *time.Location
	// mem is the count when no store is attached (tests, a store without
	// GetRaw/SetRaw): in-memory only.
	mem dailyBudgetState
}

// googleQuotaZone is where Google's per-day quotas roll over: midnight
// Pacific. A zone database missing from the image falls back to a fixed
// UTC-8, which is off by an hour during daylight saving -- still one count
// per day, just rolling at 01:00 instead of 00:00.
func googleQuotaZone() *time.Location {
	if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return loc
	}
	return time.FixedZone("PST", -8*60*60)
}

// NewDailyBudget returns provider's daily budget, persisted in store (nil:
// in-memory only). limit is read on every reservation, so a settings change
// takes effect without a restart; a limit of 0 or less refuses every lookup.
func NewDailyBudget(store RawKV, provider string, limit func() int) *DailyBudget {
	return &DailyBudget{store: store, provider: provider, limit: limit, now: time.Now, loc: googleQuotaZone()}
}

// SetClock replaces the budget's clock (tests).
func (b *DailyBudget) SetClock(now func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
}

func (b *DailyBudget) key() string { return dailyBudgetKeyPrefix + b.provider }

// load returns today's state; a row for an earlier day reads as 0 used.
func (b *DailyBudget) load(day string) (dailyBudgetState, error) {
	st := b.mem
	if b.store != nil {
		raw, err := b.store.GetRaw(b.key())
		if err != nil {
			return dailyBudgetState{}, fmt.Errorf("read %s daily budget: %w", b.provider, err)
		}
		st = dailyBudgetState{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &st); err != nil {
				return dailyBudgetState{}, fmt.Errorf("decode %s daily budget: %w", b.provider, err)
			}
		}
	}
	if st.Day != day {
		st = dailyBudgetState{Day: day}
	}
	return st, nil
}

func (b *DailyBudget) save(st dailyBudgetState) error {
	if b.store == nil {
		b.mem = st
		return nil
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode %s daily budget: %w", b.provider, err)
	}
	if err := b.store.SetRaw(b.key(), raw); err != nil {
		return fmt.Errorf("save %s daily budget: %w", b.provider, err)
	}
	return nil
}

// Reserve takes one lookup from today's budget. It returns
// ErrDailyBudgetSpent when the day's limit is reached, and any other error
// when the count cannot be read or persisted -- the lookup is then refused
// too: a quota spend that cannot be counted is not made.
func (b *DailyBudget) Reserve() (used, limit int, err error) {
	if b == nil {
		return 0, 0, ErrDailyBudgetSpent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	day := now.In(b.loc).Format("2006-01-02")
	limit = b.limit()
	st, err := b.load(day)
	if err != nil {
		return 0, limit, err
	}
	if st.Used >= limit {
		return st.Used, limit, ErrDailyBudgetSpent
	}
	st.Used++
	st.UpdatedAt = now.UTC()
	if err := b.save(st); err != nil {
		return st.Used - 1, limit, err
	}
	return st.Used, limit, nil
}

// Remaining returns how many lookups today's budget has left (0 when spent,
// or when the count cannot be read).
func (b *DailyBudget) Remaining() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	day := b.now().In(b.loc).Format("2006-01-02")
	st, err := b.load(day)
	if err != nil {
		return 0
	}
	return max(b.limit()-st.Used, 0)
}

// Provider returns the provider id the budget counts.
func (b *DailyBudget) Provider() string { return strings.TrimSpace(b.provider) }
