// file: internal/server/absauth/password_concurrency_test.go
// version: 1.0.1
// guid: 6c2f94d1-7e03-4a58-b9d6-1a4e8f307c25
// last-edited: 2026-10-20

package absauth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestArgon2idConcurrencyIsCapped runs many hashes and verifies at once and checks
// that no more than the cap overlap inside the derivation. Each real derivation
// allocates 64 MiB, so an uncapped burst of logins is a memory spike.
func TestArgon2idConcurrencyIsCapped(t *testing.T) {
	// A real hash to verify, made before the instrumented derivation is swapped in.
	algo, stored, err := HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}

	const limit = 3
	origSem, origCompute := argonSem, argonCompute
	t.Cleanup(func() { argonSem, argonCompute = origSem, origCompute })
	argonSem = make(chan struct{}, limit)

	var running, peak, calls atomic.Int32
	argonCompute = func(password, salt []byte, time_, memory uint32, threads uint8, keyLen uint32) []byte {
		n := running.Add(1)
		defer running.Add(-1)
		calls.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		return make([]byte, keyLen)
	}

	const workers = 8 * limit
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if _, _, err := HashPassword("pw"); err != nil {
					t.Errorf("HashPassword: %v", err)
				}
			} else {
				VerifyPassword(algo, stored, "pw")
			}
		}(i)
	}
	wg.Wait()

	if got := calls.Load(); got != workers {
		t.Fatalf("derivations = %d, want %d (a path bypassed the instrumented derivation)", got, workers)
	}
	if got := peak.Load(); got > limit {
		t.Fatalf("peak concurrent argon2id derivations = %d, want <= %d", got, limit)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("peak concurrent derivations = %d; the test never overlapped work, so it proves nothing", got)
	}
}
