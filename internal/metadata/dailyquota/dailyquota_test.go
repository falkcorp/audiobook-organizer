// file: internal/metadata/dailyquota/dailyquota_test.go
// version: 1.1.0
// guid: 446c17dc-de7c-45d8-955e-b98f0e40859d
// last-edited: 2026-10-06

package dailyquota

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func fixed(total, background int) func() Limits {
	return func() Limits { return Limits{Total: total, Background: background} }
}

// TestDailyBudget_SurvivesRestart: the count is persisted on every
// reservation, so a process that restarts mid-day resumes the day's count
// instead of granting a fresh limit. A real on-disk Pebble store, closed and
// reopened.
func TestDailyBudget_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	clock := func() time.Time { return time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC) }
	st, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	b := New(st, "google-books", fixed(3, 3))
	b.SetClock(clock)
	for i := range 2 {
		if _, _, err := b.Reserve(Background); err != nil {
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
	b2 := New(st2, "google-books", fixed(3, 3))
	b2.SetClock(clock)
	if got := b2.Remaining(Background); got != 1 {
		t.Fatalf("after restart Remaining = %d, want 1", got)
	}
	if used, _, err := b2.Reserve(Background); err != nil || used != 3 {
		t.Fatalf("third reservation = (%d, %v), want (3, nil)", used, err)
	}
	if _, _, err := b2.Reserve(Background); !errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("fourth reservation err = %v, want ErrDailyBudgetSpent", err)
	}
}

// TestDailyBudget_RollsOverAtMidnightPacific: the count resets at midnight
// Pacific (Google's quota day), not at midnight UTC.
func TestDailyBudget_RollsOverAtMidnightPacific(t *testing.T) {
	now := time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC) // 23:00 PDT on the 6th
	b := New(nil, "google-books", fixed(1, 1))
	b.SetClock(func() time.Time { return now })
	if _, _, err := b.Reserve(Background); err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	now = now.Add(30 * time.Minute) // same Pacific day, a new UTC day
	if _, _, err := b.Reserve(Background); !errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("same Pacific day err = %v, want ErrDailyBudgetSpent", err)
	}
	now = now.Add(time.Hour) // 00:30 PDT on the 7th
	if _, _, err := b.Reserve(Background); err != nil {
		t.Fatalf("new Pacific day: %v", err)
	}
}

// TestDailyBudget_ConcurrentReservationsNeverExceedLimit: N workers racing for
// a budget of K get exactly K reservations.
func TestDailyBudget_ConcurrentReservationsNeverExceedLimit(t *testing.T) {
	st, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	const k = 7
	b := New(st, "google-books", fixed(k, k))
	var granted atomic.Int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 4 {
				if _, _, err := b.Reserve(Background); err == nil {
					granted.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if got := granted.Load(); got != k {
		t.Fatalf("granted %d, want exactly %d", got, k)
	}
}

type failingKV struct{}

func (failingKV) GetRaw(string) ([]byte, error) { return nil, nil }
func (failingKV) SetRaw(string, []byte) error   { return errors.New("disk full") }

// A reservation whose count cannot be persisted is refused: a quota spend
// that cannot be counted is not made.
func TestDailyBudget_UnpersistableReservationIsRefused(t *testing.T) {
	b := New(failingKV{}, "google-books", fixed(10, 10))
	if _, _, err := b.Reserve(Background); err == nil || errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("Reserve err = %v, want a persistence error", err)
	}
}

// The two tiers share ONE counter: background stops at its cap, interactive
// goes on to the total, and background work never eats the reserved share.
func TestDailyBudget_TiersShareOneCounter(t *testing.T) {
	b := New(nil, "google-books", fixed(10, 8))
	for i := range 8 {
		if _, _, err := b.Reserve(Background); err != nil {
			t.Fatalf("background %d: %v", i, err)
		}
	}
	_, _, err := b.Reserve(Background)
	var se *SpentError
	if !errors.As(err, &se) || se.Used != 8 || se.Limit != 8 || se.Priority != Background {
		t.Fatalf("9th background err = %v, want SpentError 8/8 background", err)
	}
	if got := b.Remaining(Interactive); got != 2 {
		t.Fatalf("interactive remaining = %d, want 2", got)
	}
	for i := range 2 {
		if _, _, err := b.Reserve(Interactive); err != nil {
			t.Fatalf("interactive %d: %v", i, err)
		}
	}
	if _, _, err := b.Reserve(Interactive); !errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("11th lookup err = %v, want spent", err)
	}
	if got := b.Used(); got != 10 {
		t.Fatalf("used = %d, want 10", got)
	}
}

// Interactive lookups made first count against the background share too: it
// is one counter, so background stops once the total reaches its cap.
func TestDailyBudget_InteractiveSpendCountsForBackground(t *testing.T) {
	b := New(nil, "google-books", fixed(10, 8))
	for range 8 {
		if _, _, err := b.Reserve(Interactive); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := b.Reserve(Background); !errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("background after 8 interactive: err = %v, want spent", err)
	}
}

// A background cap above the total is read as the total; a zero cap refuses.
func TestLimits_For(t *testing.T) {
	if got := (Limits{Total: 5, Background: 9}).For(Background); got != 5 {
		t.Fatalf("background cap above total = %d, want 5", got)
	}
	b := New(nil, "google-books", fixed(0, 0))
	if _, _, err := b.Reserve(Interactive); !errors.Is(err, ErrDailyBudgetSpent) {
		t.Fatalf("zero total err = %v, want spent", err)
	}
}

// An unmarked context is BACKGROUND: a caller that says nothing gets the
// smaller share.
func TestPriorityOf(t *testing.T) {
	if PriorityOf(context.Background()) != Background {
		t.Fatal("unmarked context is not background")
	}
	if PriorityOf(WithInteractive(context.Background())) != Interactive {
		t.Fatal("marked context is not interactive")
	}
}

// The provider id is normalized once: the key it persists under and the id
// it reports (and is registered by) agree.
func TestDailyBudget_ProviderAndKeyAgree(t *testing.T) {
	b := New(nil, " google-books ", fixed(1, 1))
	if b.Provider() != "google-books" || b.Key() != KeyPrefix+"google-books" {
		t.Fatalf("provider %q key %q", b.Provider(), b.Key())
	}
	restore := Install(b)
	defer restore()
	if For("google-books") != b {
		t.Fatal("installed budget not found by its trimmed id")
	}
}

// The quota day is the real Pacific zone (embedded tzdata), not the fixed
// UTC-8 fallback that is an hour off during daylight saving.
func TestQuotaZone_IsPacific(t *testing.T) {
	if got := quotaZone().String(); got != "America/Los_Angeles" {
		t.Fatalf("quota zone = %q, want America/Los_Angeles", got)
	}
}

// N2: a count that cannot be persisted refuses as ErrBudgetUnavailable, a
// refusal (IsRefusal) -- never mistaken for a provider failure.
func TestDailyBudget_StoreFailureIsARefusal(t *testing.T) {
	b := New(failingKV{}, "google-books", fixed(10, 10))
	_, _, err := b.Reserve(Background)
	if !errors.Is(err, ErrBudgetUnavailable) || !IsRefusal(err) {
		t.Fatalf("Reserve err = %v, want ErrBudgetUnavailable (a refusal)", err)
	}
}
