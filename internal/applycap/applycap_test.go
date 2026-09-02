// file: internal/applycap/applycap_test.go
// version: 1.0.0
// guid: 9e2b7c41-3f8a-4d16-b6e5-0a7c4d9f2e18
// last-edited: 2026-09-02

package applycap

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestEffective_ZeroAndNegativeMeanDefaultNeverUnlimited(t *testing.T) {
	for _, in := range []int{0, -1, -5000} {
		if got := Effective(in); got != Default {
			t.Fatalf("Effective(%d) = %d, want Default %d", in, got, Default)
		}
	}
	if got := Effective(12); got != 12 {
		t.Fatalf("Effective(12) = %d, want 12", got)
	}
}

func TestCheck_RefusesCapPlusOneAllowsCap(t *testing.T) {
	// Known-good twin: exactly the cap passes.
	if err := Check("op", Default, 0); err != nil {
		t.Fatalf("Check at cap: unexpected error %v", err)
	}
	// Bogus value: one over is refused with a typed error that names the op.
	err := Check("batch-apply", Default+1, 0)
	var ex *ExceededError
	if !errors.As(err, &ex) {
		t.Fatalf("Check over cap: got %v, want *ExceededError", err)
	}
	if ex.Op != "batch-apply" || ex.Requested != Default+1 || ex.Cap != Default {
		t.Fatalf("ExceededError fields = %+v", *ex)
	}
	for _, want := range []string{"batch-apply", "5001", "5000", "bulk_apply_max_items", "nothing was applied"} {
		if !strings.Contains(ex.Error(), want) {
			t.Errorf("error text %q lacks %q", ex.Error(), want)
		}
	}
	// A configured cap is honoured in both directions.
	if err := Check("op", 3, 3); err != nil {
		t.Fatalf("Check(3, cap 3): %v", err)
	}
	if err := Check("op", 4, 3); err == nil {
		t.Fatal("Check(4, cap 3): want refusal")
	}
}

func TestFits(t *testing.T) {
	if !Fits(Default, 0) || Fits(Default+1, 0) || !Fits(2, 2) || Fits(3, 2) {
		t.Fatal("Fits disagrees with Check")
	}
}

func TestCounter_RefusesTheCapPlusOneth(t *testing.T) {
	c := NewCounter("auto-match", 3)
	for i := range 3 {
		if err := c.Admit(); err != nil {
			t.Fatalf("Admit #%d: %v", i+1, err)
		}
	}
	err := c.Admit()
	var ex *ExceededError
	if !errors.As(err, &ex) || ex.Requested != 4 || ex.Cap != 3 {
		t.Fatalf("4th Admit: got %v, want ExceededError{Requested:4 Cap:3}", err)
	}
	// The refused apply is not counted, and refusing is sticky.
	if c.Admitted() != 3 {
		t.Fatalf("Admitted after refusal = %d, want 3", c.Admitted())
	}
	if err := c.Admit(); err == nil {
		t.Fatal("5th Admit: want refusal")
	}
	if c.Admitted() != 3 {
		t.Fatalf("Admitted after second refusal = %d, want 3", c.Admitted())
	}
}

func TestCounter_ConcurrentAdmitsNeverExceedCap(t *testing.T) {
	const limit, workers, perWorker = 50, 8, 100
	c := NewCounter("op", limit)
	var wg sync.WaitGroup
	var okCount, refused atomicCounter
	for range workers {
		wg.Go(func() {
			for range perWorker {
				if c.Admit() == nil {
					okCount.inc()
				} else {
					refused.inc()
				}
			}
		})
	}
	wg.Wait()
	if okCount.n != limit || refused.n != workers*perWorker-limit || c.Admitted() != limit {
		t.Fatalf("ok=%d refused=%d admitted=%d, want %d/%d/%d",
			okCount.n, refused.n, c.Admitted(), limit, workers*perWorker-limit, limit)
	}
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (a *atomicCounter) inc() { a.mu.Lock(); a.n++; a.mu.Unlock() }
