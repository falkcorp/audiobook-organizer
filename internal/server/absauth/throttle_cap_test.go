// file: internal/server/absauth/throttle_cap_test.go
// version: 1.0.0
// guid: 341f8c6a-9fe0-4742-b163-b125af970b92
// last-edited: 2026-10-21

package absauth

import (
	"fmt"
	"testing"
	"time"
)

// Rotating source addresses cannot grow the failure tables past MaxKeys, and
// the entry evicted is the one whose window started longest ago.
func TestThrottle_TablesAreCappedOldestFirst(t *testing.T) {
	th := NewThrottle()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	th.SetClock(func() time.Time { return now })
	for i := 0; i < MaxKeys+50; i++ {
		now = now.Add(time.Millisecond)
		th.RecordFailure(fmt.Sprintf("acct-%d", i), fmt.Sprintf("192.0.2.%d-%d", i/250, i%250))
	}
	th.mu.Lock()
	defer th.mu.Unlock()
	if len(th.byIP) > MaxKeys || len(th.byAcct) > MaxKeys {
		t.Fatalf("tables grew past the cap: ip=%d acct=%d", len(th.byIP), len(th.byAcct))
	}
	if _, ok := th.byIP["192.0.2.0-0"]; ok {
		t.Fatal("the oldest source should have been evicted")
	}
	if _, ok := th.byIP[fmt.Sprintf("192.0.2.%d-%d", (MaxKeys+49)/250, (MaxKeys+49)%250)]; !ok {
		t.Fatal("the newest source must be tracked")
	}
}

// An existing key at the cap is bumped, never evicted for itself.
func TestThrottle_BumpingAnExistingKeyAtTheCapEvictsNothing(t *testing.T) {
	th := NewThrottle()
	for i := 0; i < MaxKeys; i++ {
		th.RecordFailure("", fmt.Sprintf("k-%d", i))
	}
	th.RecordFailure("", "k-5")
	th.mu.Lock()
	defer th.mu.Unlock()
	if len(th.byIP) != MaxKeys || th.byIP["k-5"].count != 2 {
		t.Fatalf("len=%d count=%d", len(th.byIP), th.byIP["k-5"].count)
	}
}
