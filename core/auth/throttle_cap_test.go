// file: core/auth/throttle_cap_test.go
// version: 2.0.0
// guid: 341f8c6a-9fe0-4742-b163-b125af970b92
// last-edited: 2026-11-10

package auth

import (
	"fmt"
	"testing"
	"time"
)

// Rotating source addresses cannot grow the failure tables past DefaultThrottleKeys, and the
// entry evicted is the least recently written one.
func TestThrottle_TablesAreCappedOldestFirst(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	th.SetClock(func() time.Time { return now })
	for i := 0; i < DefaultThrottleKeys+50; i++ {
		now = now.Add(time.Millisecond)
		th.RecordFailure(fmt.Sprintf("acct-%d", i), fmt.Sprintf("192.0.2.%d-%d", i/250, i%250))
	}
	th.mu.Lock()
	defer th.mu.Unlock()
	if th.bySrc.len() > DefaultThrottleKeys || th.byAcct.len() > DefaultThrottleKeys {
		t.Fatalf("tables grew past the cap: ip=%d acct=%d", th.bySrc.len(), th.byAcct.len())
	}
	if _, ok := th.bySrc.get("192.0.2.0-0"); ok {
		t.Fatal("the oldest source should have been evicted")
	}
	if _, ok := th.bySrc.get(fmt.Sprintf("192.0.2.%d-%d", (DefaultThrottleKeys+49)/250, (DefaultThrottleKeys+49)%250)); !ok {
		t.Fatal("the newest source must be tracked")
	}
}

// An existing key at the cap is bumped, never evicted for itself.
func TestThrottle_BumpingAnExistingKeyAtTheCapEvictsNothing(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	for i := 0; i < DefaultThrottleKeys; i++ {
		th.RecordFailure("", fmt.Sprintf("k-%d", i))
	}
	th.RecordFailure("", "k-5")
	th.mu.Lock()
	defer th.mu.Unlock()
	c, _ := th.bySrc.get("k-5")
	if th.bySrc.len() != DefaultThrottleKeys || c == nil || c.count != 2 {
		t.Fatalf("len=%d counter=%+v", th.bySrc.len(), c)
	}
}

// Expired counters are swept from the cold end on every write: no whole-table
// scan, and nothing idle for a window outlives the next write.
func TestThrottle_ExpiredCountersAreSwept(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	th.SetClock(func() time.Time { return now })
	for i := 0; i < 100; i++ {
		th.RecordFailure(fmt.Sprintf("a-%d", i), fmt.Sprintf("old-%d", i))
	}
	now = now.Add(DefaultThrottleWindow + time.Second)
	th.RecordFailure("fresh", "fresh-ip")
	th.mu.Lock()
	defer th.mu.Unlock()
	if th.bySrc.len() != 1 || th.byAcct.len() != 1 {
		t.Fatalf("expired counters survived the sweep: ip=%d acct=%d", th.bySrc.len(), th.byAcct.len())
	}
}

// Failures against more accounts than maxAccountsPerSource still count toward the
// source's budget; they just cannot be taken back by a success.
func TestThrottle_UntrackedAccountsStillCount(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	for i := 0; i < maxAccountsPerSource+10; i++ {
		th.RecordFailure(fmt.Sprintf("acct-%d", i), "192.0.2.9")
	}
	th.mu.Lock()
	c, _ := th.bySrc.get("192.0.2.9")
	n, tracked := c.count, len(c.byAcct)
	th.mu.Unlock()
	if n != maxAccountsPerSource+10 || tracked != maxAccountsPerSource {
		t.Fatalf("count=%d tracked=%d", n, tracked)
	}
	th.Clear("acct-"+fmt.Sprint(maxAccountsPerSource+5), "192.0.2.9") // an untracked account
	th.mu.Lock()
	defer th.mu.Unlock()
	if c, _ := th.bySrc.get("192.0.2.9"); c.count != n {
		t.Fatalf("an untracked account's success refunded %d", n-c.count)
	}
}

// N3: a username that has been refused (DefaultSoftThreshold failures) is not washed out
// by a stream of distinct names. Before pinning, the refused account's counter was
// the coldest entry (nothing writes it while it is refused) and the first to go.
func TestThrottle_RefusedAccountCounterSurvivesNameRotation(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	th.SetSleep(func(time.Duration) {})
	for i := 0; i < DefaultSoftThreshold; i++ {
		th.RecordFailure("batman", fmt.Sprintf("192.0.2.%d", 10+i))
	}
	for i := 0; i < 2*DefaultThrottleKeys; i++ {
		th.RecordFailure(fmt.Sprintf("n-%d", i), fmt.Sprintf("rot-%d", i))
	}
	if got := th.AccountFailures("batman"); got != DefaultSoftThreshold {
		t.Fatalf("the refused account's counter is %d after a stream of names, want %d", got, DefaultSoftThreshold)
	}
}

// ...but when every counter is refused the coldest goes, so the table stays bounded.
func TestThrottle_WhenEveryCounterIsRefusedTheTableStaysBounded(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	th.SetSleep(func(time.Duration) {})
	for i := 0; i < DefaultThrottleKeys+50; i++ {
		for j := 0; j < DefaultSoftThreshold; j++ {
			th.RecordFailure(fmt.Sprintf("hot-%d", i), "")
		}
	}
	if n := th.byAcct.len(); n != DefaultThrottleKeys {
		t.Fatalf("table holds %d, want the bound %d", n, DefaultThrottleKeys)
	}
	if th.AccountFailures("hot-0") != 0 {
		t.Fatal("the coldest refused counter should have been evicted when all are refused")
	}
	if th.AccountFailures(fmt.Sprintf("hot-%d", DefaultThrottleKeys+49)) != DefaultSoftThreshold {
		t.Fatal("the newest refused counter must be kept")
	}
}

// The same holds for a source that has spent its budget.
func TestThrottle_SpentSourceSurvivesAddressRotation(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	for i := 0; i < DefaultMaxFailuresPerSource; i++ {
		th.RecordFailure("", "192.0.2.1")
	}
	for i := 0; i < 2*DefaultThrottleKeys; i++ {
		th.RecordFailure("", fmt.Sprintf("rot-%d", i))
	}
	if !th.SourceBlocked("192.0.2.1") {
		t.Fatal("a spent source was washed out by address rotation")
	}
}

// M9: a success takes back some of a source's failures; when that drops the
// source below its budget it is no longer refused, so it must stop being pinned
// and be evictable like any other counter (Clear re-files the entry).
func TestThrottle_ClearRefilesASourceThatFellBelowThePinThreshold(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	th.SetClock(func() time.Time { return now })
	const src = "192.0.2.77"
	for i := 0; i < DefaultMaxFailuresPerSource-DefaultSoftThreshold; i++ {
		th.RecordFailure("other", src)
	}
	for i := 0; i < DefaultSoftThreshold; i++ {
		th.RecordFailure("mine", src)
	}
	if !th.SourceBlocked(src) {
		t.Fatal("setup: the source is at its budget")
	}
	th.Clear("mine", src) // takes back the DefaultSoftThreshold failures against "mine"
	if th.SourceBlocked(src) {
		t.Fatal("the source is back under its budget")
	}
	for i := 0; i < DefaultThrottleKeys+5; i++ {
		now = now.Add(time.Millisecond)
		th.RecordFailure("", fmt.Sprintf("rot-%d", i))
	}
	th.mu.Lock()
	defer th.mu.Unlock()
	if _, ok := th.bySrc.get(src); ok {
		t.Fatal("a source under its budget is the oldest idle counter: the bound evicts it, it is not still pinned")
	}
}

// Clear also marks the counter written now: the table is in write order and
// expiry reads lastSeen, so a counter moved to the front keeps a lastSeen to match.
func TestThrottle_ClearUpdatesLastSeen(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	th.SetClock(func() time.Time { return now })
	const src = "192.0.2.78"
	th.RecordFailure("a", src)
	th.RecordFailure("b", src)
	now = now.Add(5 * time.Minute)
	th.Clear("a", src)
	th.mu.Lock()
	defer th.mu.Unlock()
	c, ok := th.bySrc.get(src)
	if !ok || !c.lastSeen.Equal(now) {
		t.Fatalf("lastSeen after Clear = %v, want %v (counter %+v)", c, now, c)
	}
}
