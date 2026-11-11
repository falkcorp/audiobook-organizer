// file: core/auth/throttle_test.go
// version: 1.1.0
// guid: 78e17557-457f-4ae3-be51-7a8d454fd0a0
// last-edited: 2026-11-10

package auth

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Ported from ABO's internal/server/absauth/absauth_test.go (the throttle
// section), saturation_test.go
// (TestThrottle_AccountFailuresReadsWithoutCharging) and password_dummy_test.go (TestRefreshKey_IsAKeyOfItsOwn), F4-02.

func TestThrottle_PerIPHardBlockAfterBudget(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	for i := range DefaultMaxFailuresPerSource {
		if tr.SourceBlocked("192.0.2.1") {
			t.Fatalf("blocked too early at failure %d", i)
		}
		tr.RecordFailure("nightwing", "192.0.2.1")
	}
	if !tr.SourceBlocked("192.0.2.1") {
		t.Fatal("the source IP must be blocked once it exhausts its failure budget")
	}
	if tr.SourceBlocked("192.0.2.2") {
		t.Fatal("a different source IP must not be blocked")
	}
}

// TestThrottle_NeverHardLocksAnAccount pins §1.9.4 item 3 and the HIGH-3 finding: a
// client's legitimate retry loop (or a third party hammering a known username) must
// never deny the real user access from a clean source IP.
func TestThrottle_NeverHardLocksAnAccount(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	for range DefaultMaxFailuresPerSource * 5 {
		tr.RecordFailure("robin", "192.0.2.9")
	}
	if tr.SourceBlocked("robin") {
		t.Fatal("the account id must not be treated as an IP key")
	}
	if tr.SourceBlocked("192.0.2.10") {
		t.Fatal("the real user's own source IP must be unaffected — accounts are never hard-locked")
	}
}

func TestThrottle_SoftDelayIsProgressiveAndCapped(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	var last time.Duration
	for range 40 {
		d := tr.RecordFailure("nightwing", "192.0.2.22")
		if d > DefaultSoftMax {
			t.Fatalf("soft delay %v exceeded the cap %v", d, DefaultSoftMax)
		}
		if d < last {
			t.Fatalf("soft delay went backwards: %v then %v", last, d)
		}
		last = d
	}
	if last == 0 {
		t.Fatal("expected a non-zero soft delay after many failures")
	}
}

// A success clears the account's own counter, and takes back from the source only
// that source's failures against THAT account, at most DefaultSoftThreshold of them.
func TestThrottle_ClearRefundsOnlyTheSameAccountsFailuresCapped(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	for range DefaultMaxFailuresPerSource {
		tr.RecordFailure("nightwing", "192.0.2.33")
	}
	if !tr.SourceBlocked("192.0.2.33") {
		t.Fatal("expected block")
	}
	tr.Clear("nightwing", "192.0.2.33")
	// 15 failures, at most DefaultSoftThreshold (5) taken back: 10 remain, so the source is
	// no longer at its budget but has NOT been reset.
	if tr.SourceBlocked("192.0.2.33") {
		t.Fatal("5 refunded failures should take the source below its budget of 15")
	}
	for range DefaultMaxFailuresPerSource - (DefaultMaxFailuresPerSource - DefaultSoftThreshold) - 1 {
		tr.RecordFailure("oracle", "192.0.2.33")
	}
	if tr.SourceBlocked("192.0.2.33") {
		t.Fatal("10 remaining + 4 more is still under 15")
	}
	tr.RecordFailure("oracle", "192.0.2.33")
	if !tr.SourceBlocked("192.0.2.33") {
		t.Fatal("the source was reset by a success: it should be at 15 again (10 + 5)")
	}
	if tr.AccountFailures("nightwing") != 0 {
		t.Fatal("the account's own counter must be cleared")
	}
}

// The reported abuse: one valid account must not buy unlimited guesses at others
// from one address. Failures against other accounts survive a success, however
// many times the success is repeated.
func TestThrottle_SuccessOnOneAccountDoesNotResetGuessesAtOthers(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	const src = "192.0.2.33"
	for round := 0; ; round++ {
		if round > 100 {
			t.Fatal("the source was never blocked: a success keeps resetting it")
		}
		tr.RecordFailure("robin", src)
		tr.RecordFailure("batgirl", src)
		tr.Clear("batgirl", src) // log in to the attacker's own account
		if tr.SourceBlocked(src) {
			break
		}
	}
}

// A mistyped password on the account you then log in to costs nothing in the end.
func TestThrottle_ClearTakesBackOwnMistypes(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	for range 3 {
		tr.RecordFailure("batgirl", "192.0.2.55")
	}
	tr.Clear("batgirl", "192.0.2.55")
	tr.mu.Lock()
	_, ok := tr.bySrc.get("192.0.2.55")
	tr.mu.Unlock()
	if ok {
		t.Fatal("nothing but the account's own failures were counted, so the source entry should be gone")
	}
}

// Failures in a source window that has since rolled over are not refunded into
// the new window.
func TestThrottle_ClearDoesNotRefundAnExpiredWindow(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	tr.SetClock(func() time.Time { return now })
	for range 4 {
		tr.RecordFailure("batgirl", "192.0.2.66")
	}
	now = now.Add(DefaultThrottleWindow + time.Second)
	tr.RecordFailure("oracle", "192.0.2.66") // a new window with one failure
	tr.Clear("batgirl", "192.0.2.66")
	tr.mu.Lock()
	c, _ := tr.bySrc.get("192.0.2.66")
	tr.mu.Unlock()
	if c == nil || c.count != 1 {
		t.Fatalf("the new window's one failure must survive, got %+v", c)
	}
}

func TestAccountKey_NormalisesAndBounds(t *testing.T) {
	if got := AccountKey("  Alfred "); got != "alfred" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("é", 200) // 2 bytes each
	k := AccountKey(long)
	if len(k) > 128 || !utf8.ValidString(k) {
		t.Fatalf("len=%d valid=%v", len(k), utf8.ValidString(k))
	}
	if AccountKey("   ") != "" {
		t.Fatal("blank stays blank")
	}
}

func TestThrottle_ConcurrentUseIsRaceFree(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 200 {
				tr.RecordFailure("nightwing", "192.0.2.44")
				_ = tr.SourceBlocked("192.0.2.44")
				tr.Clear("nightwing", "192.0.2.44")
			}
		}()
	}
	for range 8 {
		<-done
	}
}

func TestRefreshKey_IsAKeyOfItsOwn(t *testing.T) {
	if RefreshKey("") != "" {
		t.Fatal("an empty source stays empty")
	}
	if k := RefreshKey("192.0.2.1"); k == "192.0.2.1" || k == "" {
		t.Fatalf("refresh key %q must differ from the source key", k)
	}
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	for range DefaultMaxFailuresPerSource {
		th.RecordFailure("", RefreshKey("192.0.2.1"))
	}
	if !th.SourceBlocked(RefreshKey("192.0.2.1")) || th.SourceBlocked("192.0.2.1") {
		t.Fatal("refresh failures spend the refresh budget only")
	}
}

func TestThrottle_AccountFailuresReadsWithoutCharging(t *testing.T) {
	th := NewThrottle(ThrottleLimits{}, nil, nil)
	if th.AccountFailures("alfred") != 0 || th.AccountFailures("") != 0 {
		t.Fatal("unknown account should have 0 failures")
	}
	th.RecordFailure("alfred", "192.0.2.1")
	th.RecordFailure("alfred", "192.0.2.2")
	if got := th.AccountFailures("alfred"); got != 2 {
		t.Fatalf("AccountFailures = %d, want 2", got)
	}
	_ = th.AccountFailures("alfred")
	if got := th.AccountFailures("alfred"); got != 2 {
		t.Fatalf("reading changed the count: %d", got)
	}
}

// TestThrottle_SaturationPolicy: the delay grows with the failures already
// counted on the submitted name, is capped, and turns into a refusal at the
// soft threshold in force (SetLimits), whether or not the name exists.
func TestThrottle_SaturationPolicy(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	for failures := range DefaultSoftThreshold + 3 {
		d, refuse := tr.SaturationPolicy("robin")
		want := min(SaturationDelayBase+time.Duration(failures)*SaturationDelayStep, SaturationDelayMax)
		if failures >= DefaultSoftThreshold {
			want = 0
		}
		if d != want || refuse != (failures >= DefaultSoftThreshold) {
			t.Fatalf("failures %d: delay %v refuse %v, want %v %v", failures, d, refuse, want, failures >= DefaultSoftThreshold)
		}
		if d > SaturationDelayMax {
			t.Fatalf("failures %d: delay %v above the cap", failures, d)
		}
		tr.RecordFailure("robin", "192.0.2.7")
	}
	if d, _ := NewThrottle(ThrottleLimits{}, nil, nil).SaturationPolicy("never-seen"); d != SaturationDelayBase {
		t.Fatalf("an unknown name waits %v, want the base %v", d, SaturationDelayBase)
	}
	// 1s + 4 x 500ms = 3s: the cap applies below the threshold.
	tr = NewThrottle(ThrottleLimits{}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	for range 4 {
		tr.RecordFailure("batgirl", "192.0.2.8")
	}
	if d, _ := tr.SaturationPolicy("batgirl"); d != SaturationDelayMax {
		t.Fatalf("delay %v, want the cap %v", d, SaturationDelayMax)
	}
	// A lower threshold set later refuses at once.
	tr.SetLimits(ThrottleLimits{SoftThreshold: 3})
	if _, refuse := tr.SaturationPolicy("batgirl"); !refuse {
		t.Fatal("4 failures under a threshold of 3 were not refused")
	}
}

// TestThrottle_SetLimitsAppliesAndKeepsTheTableSize: a reloaded policy takes
// effect on the next call, zero fields fall back to their defaults, and the
// table bound stays the one the throttle was built with.
func TestThrottle_SetLimitsAppliesAndKeepsTheTableSize(t *testing.T) {
	tr := NewThrottle(ThrottleLimits{MaxKeys: 7}, nil, nil)
	tr.SetSleep(func(time.Duration) {})
	tr.SetLimits(ThrottleLimits{MaxFailuresPerSource: 2, MaxKeys: 999})
	got := tr.Limits()
	want := ThrottleLimits{MaxFailuresPerSource: 2, Window: DefaultThrottleWindow, SoftThreshold: DefaultSoftThreshold,
		SoftStep: DefaultSoftStep, SoftMax: DefaultSoftMax, MaxKeys: 7}
	if got != want {
		t.Fatalf("Limits() = %+v, want %+v", got, want)
	}
	tr.RecordFailure("alfred", "192.0.2.9")
	if tr.SourceBlocked("192.0.2.9") {
		t.Fatal("blocked after 1 failure under a budget of 2")
	}
	tr.RecordFailure("alfred", "192.0.2.9")
	if !tr.SourceBlocked("192.0.2.9") {
		t.Fatal("the reloaded budget of 2 did not block")
	}
	tr.SetLimits(ThrottleLimits{})
	if tr.SourceBlocked("192.0.2.9") {
		t.Fatal("back at the default budget, 2 failures still block")
	}
}
