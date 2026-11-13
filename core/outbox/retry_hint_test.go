// file: core/outbox/retry_hint_test.go
// version: 1.0.0
// guid: 8b2f6d14-0a57-4c93-b1e8-3d7a5f09c2e6
// last-edited: 2026-11-13

package outbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/outbox"
)

// seen records one delivery the hint tests care about.
type seen struct {
	attempt int
	at      time.Time
}

// hintRig runs a core consumer whose handler is f, with a 20ms backoff floor
// and cap so the curve and a handler's hint cannot be confused.
func hintRig(t *testing.T, maxAttempts int, f func(n int) error) (*rig, func() []seen) {
	t.Helper()
	var mu sync.Mutex
	var got []seen
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) {
			c.MaxAttempts = maxAttempts
			c.BackoffMin, c.BackoffMax = 20*time.Millisecond, 20*time.Millisecond
		},
		rand: func() float64 { return 0 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(_ context.Context, m outbox.Message[Dispatch]) error {
					mu.Lock()
					got = append(got, seen{attempt: m.Attempt, at: time.Now()})
					n := len(got)
					mu.Unlock()
					return f(n)
				})}}}
		},
	})
	return r, func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), got...)
	}
}

// RetryAfter replaces the backoff curve's delay with the handler's own and
// still counts the attempt.
func TestOutbox_RetryAfterHonoursHandlerDelay(t *testing.T) {
	r, deliveries := hintRig(t, 5, func(n int) error {
		if n == 1 {
			return outbox.RetryAfter(300*time.Millisecond, errors.New("the Riddler asks for patience"))
		}
		return nil
	})
	r.stage("wayne-manor", Dispatch{Villain: "Riddler"})
	eventually(t, "the second delivery", func() bool { return len(deliveries()) == 2 })
	d := deliveries()
	if gap := d[1].at.Sub(d[0].at); gap < 300*time.Millisecond {
		t.Fatalf("retried after %v, want at least the handler's 300ms (the curve is 20ms)", gap)
	}
	if d[0].attempt != 1 || d[1].attempt != 2 {
		t.Fatalf("attempts %d then %d, want 1 then 2: RetryAfter counts the attempt", d[0].attempt, d[1].attempt)
	}
}

// A RetryAfter failure on the last allowed attempt dead-letters: the hint
// moves a retry, it never grants one.
func TestOutbox_RetryAfterDoesNotExtendMaxAttempts(t *testing.T) {
	r, deliveries := hintRig(t, 2, func(int) error {
		return outbox.RetryAfter(10*time.Millisecond, errors.New("still busy"))
	})
	r.stage("wayne-manor", Dispatch{Villain: "Penguin"})
	eventually(t, "the dead letter", func() bool {
		l, _, err := r.ob.ListDead(context.Background(), "", 10)
		must(t, err)
		return len(l) == 1
	})
	if n := len(deliveries()); n != 2 {
		t.Fatalf("%d deliveries, want MaxAttempts=2", n)
	}
}

// A permanent error wins over a hint wrapped around it.
func TestOutbox_PermanentBeatsRetryAfter(t *testing.T) {
	r, deliveries := hintRig(t, 5, func(int) error {
		return outbox.RetryAfter(10*time.Millisecond, errors.Join(outbox.ErrPermanent, errors.New("refused")))
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the dead letter", func() bool {
		l, _, err := r.ob.ListDead(context.Background(), "", 10)
		must(t, err)
		return len(l) == 1 && l[0].Attempts == 1
	})
	if n := len(deliveries()); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
}

// RetryFree waits without using an attempt: the same message is delivered
// well past MaxAttempts times, always as attempt 1, and is never dead-lettered.
func TestOutbox_RetryFreeDoesNotUseAttempts(t *testing.T) {
	r, deliveries := hintRig(t, 2, func(n int) error {
		if n <= 6 {
			return outbox.RetryFree(10*time.Millisecond, errors.New("the Batcave is dark"))
		}
		return nil
	})
	r.stage("wayne-manor", Dispatch{Villain: "Bane"})
	eventually(t, "the seventh delivery", func() bool { return len(deliveries()) == 7 })
	for i, d := range deliveries() {
		if d.attempt != 1 {
			t.Fatalf("delivery %d saw Attempt=%d, want 1: RetryFree must not count", i+1, d.attempt)
		}
	}
	if l, _, err := r.ob.ListDead(context.Background(), "", 10); err != nil || len(l) != 0 {
		t.Fatalf("dead letters %+v err=%v, want none", l, err)
	}
}

// A RetryFree failure after a counted one keeps the count it had.
func TestOutbox_RetryFreeKeepsEarlierAttempts(t *testing.T) {
	r, deliveries := hintRig(t, 5, func(n int) error {
		switch n {
		case 1:
			return errors.New("counted")
		case 2, 3:
			return outbox.RetryFree(10*time.Millisecond, errors.New("free"))
		}
		return nil
	})
	r.stage("wayne-manor", Dispatch{Villain: "Scarecrow"})
	eventually(t, "the fourth delivery", func() bool { return len(deliveries()) == 4 })
	var got []int
	for _, d := range deliveries() {
		got = append(got, d.attempt)
	}
	if want := []int{1, 2, 2, 2}; len(got) != 4 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("attempts %v, want %v", got, want)
	}
}
