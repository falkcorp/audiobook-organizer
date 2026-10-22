// file: internal/server/absauth/throttle_cap_test.go
// version: 2.0.0
// guid: 341f8c6a-9fe0-4742-b163-b125af970b92
// last-edited: 2026-10-22

package absauth

import (
	"fmt"
	"testing"
	"time"
)

// Rotating source addresses cannot grow the failure tables past MaxKeys, and the
// entry evicted is the least recently written one.
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
	if th.byIP.Len() > MaxKeys || th.byAcct.Len() > MaxKeys {
		t.Fatalf("tables grew past the cap: ip=%d acct=%d", th.byIP.Len(), th.byAcct.Len())
	}
	if _, ok := th.byIP.Get("192.0.2.0-0"); ok {
		t.Fatal("the oldest source should have been evicted")
	}
	if _, ok := th.byIP.Get(fmt.Sprintf("192.0.2.%d-%d", (MaxKeys+49)/250, (MaxKeys+49)%250)); !ok {
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
	c, _ := th.byIP.Get("k-5")
	if th.byIP.Len() != MaxKeys || c == nil || c.count != 2 {
		t.Fatalf("len=%d counter=%+v", th.byIP.Len(), c)
	}
}

// Expired counters are swept from the cold end on every write: no whole-table
// scan, and nothing idle for a window outlives the next write.
func TestThrottle_ExpiredCountersAreSwept(t *testing.T) {
	th := NewThrottle()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	th.SetClock(func() time.Time { return now })
	for i := 0; i < 100; i++ {
		th.RecordFailure(fmt.Sprintf("a-%d", i), fmt.Sprintf("old-%d", i))
	}
	now = now.Add(Window + time.Second)
	th.RecordFailure("fresh", "fresh-ip")
	th.mu.Lock()
	defer th.mu.Unlock()
	if th.byIP.Len() != 1 || th.byAcct.Len() != 1 {
		t.Fatalf("expired counters survived the sweep: ip=%d acct=%d", th.byIP.Len(), th.byAcct.Len())
	}
}

// Failures against more accounts than maxAccountsPerSource still count toward the
// source's budget; they just cannot be taken back by a success.
func TestThrottle_UntrackedAccountsStillCount(t *testing.T) {
	th := NewThrottle()
	for i := 0; i < maxAccountsPerSource+10; i++ {
		th.RecordFailure(fmt.Sprintf("acct-%d", i), "9.9.9.9")
	}
	th.mu.Lock()
	c, _ := th.byIP.Get("9.9.9.9")
	n, tracked := c.count, len(c.byAcct)
	th.mu.Unlock()
	if n != maxAccountsPerSource+10 || tracked != maxAccountsPerSource {
		t.Fatalf("count=%d tracked=%d", n, tracked)
	}
	th.Clear("acct-"+fmt.Sprint(maxAccountsPerSource+5), "9.9.9.9") // an untracked account
	th.mu.Lock()
	defer th.mu.Unlock()
	if c, _ := th.byIP.Get("9.9.9.9"); c.count != n {
		t.Fatalf("an untracked account's success refunded %d", n-c.count)
	}
}
