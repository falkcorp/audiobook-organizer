// file: internal/metafetch/candidate_fallback_test.go
// version: 1.0.0
// guid: ea9f0acf-53b5-4342-885a-5843289a5fa7
// last-edited: 2026-10-06

package metafetch

import (
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// TestDailyBudget_SurvivesRestart: the count is persisted on every
// reservation, so a process that restarts mid-day (prod restarted 146 times
// in 30 days) resumes the day's count instead of granting a fresh limit. The
// store is a real on-disk Pebble store, closed and reopened.
func TestDailyBudget_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	clock := func() time.Time { return time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC) }
	limit := func() int { return 3 }

	st, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	b := NewDailyBudget(st, metadata.SourceIDGoogleBooks, limit)
	b.SetClock(clock)
	for i := range 2 {
		if _, _, err := b.Reserve(); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	st2, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = st2.Close() }()
	b2 := NewDailyBudget(st2, metadata.SourceIDGoogleBooks, limit)
	b2.SetClock(clock)
	if got := b2.Remaining(); got != 1 {
		t.Fatalf("after restart Remaining() = %d, want 1 (2 of 3 spent before the restart)", got)
	}
	if used, _, err := b2.Reserve(); err != nil || used != 3 {
		t.Fatalf("third reservation after restart = (%d, %v), want (3, nil)", used, err)
	}
	if _, _, err := b2.Reserve(); !errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("fourth reservation err = %v, want ErrDailyBudgetSpent", err)
	}
}

// TestDailyBudget_RollsOverAtMidnightPacific: Google's per-day quotas reset
// at midnight Pacific, so the count does too -- not at midnight UTC.
func TestDailyBudget_RollsOverAtMidnightPacific(t *testing.T) {
	now := time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC) // 23:00 PDT on the 6th
	b := NewDailyBudget(nil, metadata.SourceIDGoogleBooks, func() int { return 1 })
	b.SetClock(func() time.Time { return now })
	if _, _, err := b.Reserve(); err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	now = now.Add(30 * time.Minute) // 23:30 PDT: same quota day, though a new UTC day
	if _, _, err := b.Reserve(); !errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("same Pacific day err = %v, want ErrDailyBudgetSpent", err)
	}
	now = now.Add(time.Hour) // 00:30 PDT on the 7th
	if _, _, err := b.Reserve(); err != nil {
		t.Fatalf("new Pacific day: %v", err)
	}
}

// TestDailyBudget_ConcurrentReservationsNeverExceedLimit: N workers racing
// for a budget of K get exactly K reservations.
func TestDailyBudget_ConcurrentReservationsNeverExceedLimit(t *testing.T) {
	st, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	const k = 7
	b := NewDailyBudget(st, metadata.SourceIDGoogleBooks, func() int { return k })
	var granted atomic.Int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 4 {
				if _, _, err := b.Reserve(); err == nil {
					granted.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if got := granted.Load(); got != k {
		t.Fatalf("granted %d reservations, want exactly %d", got, k)
	}
}

// failingKV fails every write: a reservation whose count cannot be persisted
// is refused (a quota spend that cannot be counted is not made).
type failingKV struct{}

func (failingKV) GetRaw(string) ([]byte, error) { return nil, nil }
func (failingKV) SetRaw(string, []byte) error   { return errors.New("disk full") }

func TestDailyBudget_UnpersistableReservationIsRefused(t *testing.T) {
	b := NewDailyBudget(failingKV{}, metadata.SourceIDGoogleBooks, func() int { return 10 })
	if _, _, err := b.Reserve(); err == nil || errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("Reserve err = %v, want a persistence error", err)
	}
}

// TestDailyBudget_ZeroLimitRefuses: a limit of 0 (the fallback turned off)
// grants nothing.
func TestDailyBudget_ZeroLimitRefuses(t *testing.T) {
	b := NewDailyBudget(nil, metadata.SourceIDGoogleBooks, func() int { return 0 })
	if _, _, err := b.Reserve(); !errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("Reserve err = %v, want ErrDailyBudgetSpent", err)
	}
	if got := b.Remaining(); got != 0 {
		t.Fatalf("Remaining() = %d, want 0", got)
	}
}

// TestActiveSourceNamesByID_ConfiguredChain: the production chain wraps every
// client in metadata.NewChainSource, and the fallback plan is built from the
// provider ids it reports. If the wrapper hid the id, the plan would be empty
// and Google Books would be asked for every book with no budget -- while every
// test built on unwrapped fakes stayed green. Building clients makes no
// network call.
func TestActiveSourceNamesByID_ConfiguredChain(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "audible", Name: "Audible", Enabled: true, Priority: 1},
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 2},
		{ID: "google-books", Name: "Google Books", Enabled: true, Priority: 3,
			Credentials: map[string]string{"apiKey": "test-placeholder"}},
	}
	mfs := NewService(nil)
	byID := mfs.ActiveSourceNamesByID()
	names := mfs.ActiveSourceNames()
	for _, id := range CandidateFallbackProviderIDs {
		name, ok := byID[id]
		if !ok {
			t.Fatalf("configured chain reports no source with provider id %q (got %v)", id, byID)
		}
		if !slices.Contains(names, name) {
			t.Fatalf("provider %q maps to %q, which is not among the active source names %v", id, name, names)
		}
	}
}
