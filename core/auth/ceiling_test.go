// file: core/auth/ceiling_test.go
// version: 1.1.0
// guid: c74f922a-5136-4b51-93da-0108b945b37f
// last-edited: 2026-11-10

package auth

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Ported from ABO's internal/server/login_ceiling_test.go (the cases that
// read the ceiling's state rather than the gin middleware's answers) and
// absauth/saturation_test.go (TestSaturationPolicy, now TestThrottle_SaturationPolicy
// in throttle_test.go), F4-02. The middleware
// cases stay in ABO, driving this type through Admit.

var ceilingT0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// clocked is a ceiling on a settable fake clock.
func clocked() (*LoginCeiling, *time.Time) {
	now := ceilingT0
	l := NewLoginCeiling(func() time.Time { return now })
	return l, &now
}

// login drives one login through l as a caller does: Admit, then Done with
// outcome. It reports whether the login was admitted saturated and the
// decision (a refusal admits nothing).
func login(l *LoginCeiling, visitor, connKey string, outcome LoginOutcome, account string) (saturated bool, d CeilingDecision) {
	a, d := l.Admit(visitor, func() string { return visitor }, connKey)
	if a == nil {
		return false, d
	}
	saturated = a.Saturated()
	a.Done(outcome, account)
	return saturated, d
}

// fill spends n global slots with failures that no success refunds.
func fill(t *testing.T, l *LoginCeiling, n int) {
	t.Helper()
	for range n {
		if _, ok, _ := l.reserve(""); !ok {
			t.Fatal("the global ceiling was spent before the fill ended")
		}
	}
}

func globalCount(l *LoginCeiling) int {
	g, _ := l.Failures("")
	return g
}

// A spent ceiling does not refuse: the login is admitted saturated. The
// connector ceiling marks only its connector; the global one every visitor.
func TestLoginCeiling_ConnectorAndGlobalCeilingsMarkSaturated(t *testing.T) {
	l := NewLoginCeiling(nil)
	for i := range LoginConnectorCeiling {
		sat, d := login(l, fmt.Sprintf("192.0.2.%d", i%200+1), "192.0.2.45", LoginFailed, "")
		require.Equal(t, CeilingAdmitted, d.Refusal, "failure %d", i)
		require.False(t, sat, "failure %d", i)
	}
	sat, d := login(l, "192.0.2.250", "192.0.2.45", LoginFailed, "")
	require.Equal(t, CeilingAdmitted, d.Refusal, "past the connector ceiling the login still goes on")
	require.True(t, sat, "...saturated")
	require.Equal(t, "connector 192.0.2.45", d.Warn, "the first saturated login names the spent ceiling")
	_, d = login(l, "192.0.2.251", "192.0.2.45", LoginOther, "")
	require.Empty(t, d.Warn, "once per window")
	sat, _ = login(l, "192.0.2.252", "192.0.2.46", LoginFailed, "")
	require.False(t, sat, "another connector is untouched")

	g := NewLoginCeiling(nil)
	for i := range LoginGlobalCeiling {
		sat, _ := login(g, fmt.Sprintf("192.0.2.%d", i%200+1), "", LoginFailed, "")
		require.False(t, sat, "failure %d", i)
	}
	sat, d = login(g, "198.51.100.9", "", LoginFailed, "")
	require.True(t, sat, "a fresh address cannot get past the global ceiling unmarked")
	require.Equal(t, "global", d.Warn)
}

// Only failures count: a success hands its slot back, and so does any other
// answer.
func TestLoginCeiling_OnlyFailuresCount(t *testing.T) {
	l := NewLoginCeiling(nil)
	for _, o := range []LoginOutcome{LoginSucceeded, LoginOther} {
		for i := range 2 * LoginGlobalCeiling {
			sat, _ := login(l, "192.0.2.5", "", o, "nightwing")
			require.False(t, sat, "outcome %d login %d", o, i)
		}
	}
	require.Zero(t, globalCount(l))
	login(l, "192.0.2.5", "", LoginFailed, "")
	require.Equal(t, 1, globalCount(l))
}

// A success hands back only what the same visitor failed against the SAME
// account, at the global and connector levels alike.
func TestLoginCeiling_SuccessRefundsOnlyTheSameAccountsFailures(t *testing.T) {
	l := NewLoginCeiling(nil)
	const conn = "192.0.2.45"
	for range 10 {
		login(l, "192.0.2.5", conn, LoginFailed, "robin")
	}
	for range 3 {
		login(l, "192.0.2.5", conn, LoginFailed, "nightwing")
	}
	for range 4 { // another visitor's failures on the same account are not this visitor's to refund
		login(l, "192.0.2.6", conn, LoginFailed, "nightwing")
	}
	g, c := l.Failures(conn)
	require.Equal(t, 17, g)
	require.Equal(t, 17, c)
	login(l, "192.0.2.5", conn, LoginSucceeded, "nightwing")
	g, c = l.Failures(conn)
	require.Equal(t, 14, g, "only the 3 failures on nightwing are handed back")
	require.Equal(t, 14, c, "the connector level agrees")
	login(l, "192.0.2.5", conn, LoginSucceeded, "nightwing")
	require.Equal(t, 14, globalCount(l), "a second success has nothing left to refund")
}

// A success refunds at most DefaultSoftThreshold failures and only those from
// the last window.
func TestLoginCeiling_RefundIsCappedAndAgeLimited(t *testing.T) {
	l, _ := clocked()
	for range 20 {
		login(l, "192.0.2.5", "", LoginFailed, "nightwing")
	}
	require.Equal(t, 20, globalCount(l))
	login(l, "192.0.2.5", "", LoginSucceeded, "nightwing")
	require.Equal(t, 20-DefaultSoftThreshold, globalCount(l), "capped at the per-account limit")

	// Age: failures older than the window are not refunded even if their
	// bucket window is somehow still open.
	l2, now := clocked()
	for range 5 {
		tk, ok, _ := l2.reserve("")
		require.True(t, ok)
		l2.failed("v", "nightwing", tk)
	}
	l2.mu.Lock()
	vis, _ := l2.visitors.get("v")
	fs := vis.byAccount["nightwing"]
	for i := range 3 {
		fs[i].at = now.Add(-LoginCeilingWindow - time.Minute)
	}
	l2.mu.Unlock()
	tk, ok, _ := l2.reserve("")
	require.True(t, ok)
	require.Equal(t, 6, globalCount(l2))
	l2.succeeded("v", "nightwing", tk)
	require.Equal(t, 3, globalCount(l2), "the success's own slot and the 2 recent failures go back; the 3 old ones stay")
}

// Failures against no account (an unknown username) are never refunded, and
// a visitor's failures are remembered for at most maxLoginVisitorAccounts
// accounts (the rest stay counted, unrefundable).
func TestLoginCeiling_UnrecordedFailuresAreNeverRefunded(t *testing.T) {
	l := NewLoginCeiling(nil)
	for range 6 {
		login(l, "192.0.2.5", "", LoginFailed, "")
	}
	login(l, "192.0.2.5", "", LoginSucceeded, "nightwing")
	require.Equal(t, 6, globalCount(l))

	m := NewLoginCeiling(nil)
	for i := range maxLoginVisitorAccounts + 1 {
		login(m, "192.0.2.5", "", LoginFailed, fmt.Sprintf("henchman-%d", i))
	}
	login(m, "192.0.2.5", "", LoginSucceeded, fmt.Sprintf("henchman-%d", maxLoginVisitorAccounts))
	require.Equal(t, maxLoginVisitorAccounts+1, globalCount(m), "the account past the per-visitor bound was never recorded")
	login(m, "192.0.2.5", "", LoginSucceeded, "henchman-0")
	require.Equal(t, maxLoginVisitorAccounts, globalCount(m), "a recorded account is refunded")
	login(m, "", "", LoginFailed, "nightwing")
	login(m, "", "", LoginSucceeded, "nightwing")
	require.Equal(t, maxLoginVisitorAccounts+1, globalCount(m), "an unattributed visitor's failure is never recorded")
}

func TestLoginCeiling_WindowRollsOver(t *testing.T) {
	l, now := clocked()
	for range LoginGlobalCeiling {
		login(l, "192.0.2.5", "", LoginFailed, "")
	}
	sat, _ := login(l, "192.0.2.5", "", LoginOther, "")
	require.True(t, sat)
	*now = now.Add(LoginCeilingWindow + time.Second)
	require.Zero(t, globalCount(l), "a window that ran out reads as empty")
	sat, _ = login(l, "192.0.2.6", "", LoginFailed, "")
	require.False(t, sat, "a new window starts empty")
	// A ticket from the old window must not decrement the new one.
	tk, ok, _ := l.reserve("")
	require.True(t, ok)
	*now = now.Add(LoginCeilingWindow + time.Second)
	_, ok, _ = l.reserve("")
	require.True(t, ok)
	l.release(tk)
	require.Equal(t, 1, globalCount(l))
	// The same for a connector's window.
	ct, ok, _ := l.reserve("192.0.2.45")
	require.True(t, ok)
	*now = now.Add(LoginCeilingWindow + time.Second)
	_, ok, _ = l.reserve("192.0.2.45")
	require.True(t, ok)
	l.release(ct)
	_, c := l.Failures("192.0.2.45")
	require.Equal(t, 1, c)
}

// Slots are taken on entry, so parallel guesses cannot all pass a check made
// before any has failed.
func TestLoginCeiling_InFlightAttemptsCountAgainstTheCeiling(t *testing.T) {
	l := NewLoginCeiling(nil)
	admitted := 0
	for range LoginGlobalCeiling + 50 {
		if _, ok, _ := l.reserve(""); ok {
			admitted++
		}
	}
	require.Equal(t, LoginGlobalCeiling, admitted)
}

// One visitor may have MaxLoginInFlightPerVisitor logins in flight; the next
// is refused with the short wait and takes no slot, others are unaffected,
// and the slot comes back when a login ends.
func TestLoginCeiling_PerVisitorInFlightCap(t *testing.T) {
	l := NewLoginCeiling(nil)
	var held []*LoginAdmission
	for range MaxLoginInFlightPerVisitor {
		a, d := l.Admit("192.0.2.5", nil, "")
		require.Equal(t, CeilingAdmitted, d.Refusal)
		held = append(held, a)
	}
	a, d := l.Admit("192.0.2.5", nil, "")
	require.Nil(t, a)
	require.Equal(t, CeilingBusy, d.Refusal)
	require.Equal(t, 5, d.RetryAfter)
	require.Equal(t, MaxLoginInFlightPerVisitor, globalCount(l), "the refused login took no slot")
	logins, _ := l.InFlight("192.0.2.5")
	require.Equal(t, MaxLoginInFlightPerVisitor, logins)
	other, d := l.Admit("192.0.2.6", nil, "")
	require.Equal(t, CeilingAdmitted, d.Refusal, "another visitor is unaffected")
	other.Done(LoginOther, "")
	for _, h := range held {
		h.Done(LoginSucceeded, "")
	}
	logins, _ = l.InFlight("192.0.2.5")
	require.Zero(t, logins)
	again, d := l.Admit("192.0.2.5", nil, "")
	require.Equal(t, CeilingAdmitted, d.Refusal, "the slots came back")
	again.Done(LoginOther, "")
	// An unattributed visitor shares one key.
	for range MaxLoginInFlightPerVisitor {
		_, d := l.Admit("", nil, "")
		require.Equal(t, CeilingAdmitted, d.Refusal)
	}
	_, d = l.Admit("", nil, "")
	require.Equal(t, CeilingBusy, d.Refusal, "every unattributed login shares one in-flight budget")
}

// Saturated requests are bounded per visitor: a visitor holds at most
// MaxSaturatedPerVisitor, and above MaxSaturatedSoft one. The service-wide
// bucket is off so the per-visitor rules are what is measured.
func TestLoginCeiling_SaturatedInFlightIsBoundedPerVisitor(t *testing.T) {
	l := NewLoginCeiling(nil)
	l.SetSaturatedBound(math.Inf(1), 1)
	fill(t, l, LoginGlobalCeiling)
	admit := func(visitor string) (*LoginAdmission, CeilingDecision) {
		return l.Admit(visitor, func() string { return visitor }, "")
	}
	var held []*LoginAdmission
	for range MaxSaturatedPerVisitor {
		a, d := admit("192.0.2.5")
		require.Equal(t, CeilingAdmitted, d.Refusal)
		require.True(t, a.Saturated())
		held = append(held, a)
	}
	_, d := admit("192.0.2.5")
	require.Equal(t, CeilingSaturatedBusy, d.Refusal, "past the per-visitor bound")
	require.Equal(t, 5, d.RetryAfter)
	const flood = MaxSaturatedSoft + 44
	for i := range flood {
		a, d := admit(fmt.Sprintf("198.51.%d.%d", 100+i/250, i%250+1))
		require.Equal(t, CeilingAdmitted, d.Refusal, "fresh visitor %d", i)
		held = append(held, a)
	}
	_, d = admit("198.51.100.1")
	require.Equal(t, CeilingSaturatedBusy, d.Refusal, "above the soft bound a visitor with one in flight is refused")
	a, d := admit("203.0.113.77")
	require.Equal(t, CeilingAdmitted, d.Refusal, "a brand-new visitor is still admitted")
	held = append(held, a)
	_, sat := l.InFlight("192.0.2.5")
	require.Equal(t, MaxSaturatedPerVisitor, sat)
	for _, h := range held {
		h.Done(LoginSucceeded, "")
	}
	require.Zero(t, l.saturated)
	_, sat = l.InFlight("192.0.2.5")
	require.Zero(t, sat)
}

// A visitor that failed while saturated, by a failed credential or by the
// saturation policy's refusal, is refused outright until the window ends;
// other visitors are unaffected.
func TestLoginCeiling_VisitorThatFailedWhileSaturatedIsRefused(t *testing.T) {
	for _, outcome := range []LoginOutcome{LoginFailed, LoginSaturationRefused} {
		l, now := clocked()
		fill(t, l, LoginGlobalCeiling)
		sat, d := login(l, "192.0.2.5", "", outcome, "nightwing")
		require.Equal(t, CeilingAdmitted, d.Refusal, "outcome %d: the first saturated attempt goes on", outcome)
		require.True(t, sat)
		_, d = login(l, "192.0.2.5", "", LoginOther, "")
		require.Equal(t, CeilingSaturatedFailed, d.Refusal, "outcome %d: the same visitor's next one is refused", outcome)
		_, d = login(l, "192.0.2.6", "", LoginOther, "")
		require.Equal(t, CeilingAdmitted, d.Refusal, "another visitor is admitted")
		*now = now.Add(LoginCeilingWindow + time.Second)
		_, d = login(l, "192.0.2.5", "", LoginOther, "")
		require.Equal(t, CeilingAdmitted, d.Refusal, "after the window the visitor goes on again")
	}
	// A saturated success or another answer marks nothing.
	l, _ := clocked()
	fill(t, l, LoginGlobalCeiling)
	for _, o := range []LoginOutcome{LoginSucceeded, LoginOther, LoginOther} {
		_, d := login(l, "192.0.2.5", "", o, "nightwing")
		require.Equal(t, CeilingAdmitted, d.Refusal)
	}
	require.Equal(t, LoginGlobalCeiling, globalCount(l), "a saturated login holds no slot and refunds none")
}

// Saturated admission has a service-wide rate bound, however many fresh
// visitors ask: admissions per second bound derivations per second.
func TestLoginCeiling_SaturatedAdmissionIsBoundedServiceWide(t *testing.T) {
	l, now := clocked()
	const burst, perSecond = 8, 2
	l.SetSaturatedBound(perSecond, burst)
	fill(t, l, LoginGlobalCeiling)
	admitted, refused := 0, 0
	try := func(i int) {
		_, d := login(l, fmt.Sprintf("198.51.%d.%d", 100+i/250, i%250+1), "", LoginSucceeded, "")
		switch d.Refusal {
		case CeilingAdmitted:
			admitted++
		case CeilingSaturatedCapacity:
			refused++
			require.Equal(t, 1, d.RetryAfter, "an empty bucket at 2/s holds a token again within a second")
		default:
			t.Fatalf("visitor %d: refusal %d", i, d.Refusal)
		}
	}
	for i := range 100 {
		try(i)
	}
	require.Equal(t, burst, admitted, "a flood of fresh visitors derives at most the burst")
	require.Equal(t, 100-burst, refused)
	total := admitted
	for step := 1; step <= 100; step++ {
		*now = now.Add(100 * time.Millisecond)
		try(1000 + step)
	}
	require.LessOrEqual(t, admitted, burst+perSecond*10, "admissions never exceed burst + rate x elapsed")
	require.GreaterOrEqual(t, admitted-total, perSecond*10-1, "and the bucket does refill at the rate")
}

// A visitor refused by its OWN limits spends no service-wide token, and a
// visitor refused for want of a token is not marked as having failed.
func TestLoginCeiling_RefusedVisitorsSpendNoServiceTokens(t *testing.T) {
	l, now := clocked()
	l.SetSaturatedBound(1, 3)
	fill(t, l, LoginGlobalCeiling)
	tokens := func() float64 { l.mu.Lock(); defer l.mu.Unlock(); return l.satBound.tokensAt(*now) }
	_, d := login(l, "192.0.2.5", "", LoginFailed, "")
	require.Equal(t, CeilingAdmitted, d.Refusal)
	require.InDelta(t, 2, tokens(), 0.001)
	for range 50 {
		_, d := login(l, "192.0.2.5", "", LoginOther, "")
		require.Equal(t, CeilingSaturatedFailed, d.Refusal)
	}
	require.InDelta(t, 2, tokens(), 0.001, "a visitor refused by its own failure spends no token")
	for _, v := range []string{"192.0.2.6", "192.0.2.7"} {
		_, d := login(l, v, "", LoginSucceeded, "")
		require.Equal(t, CeilingAdmitted, d.Refusal)
	}
	_, d = login(l, "192.0.2.8", "", LoginSucceeded, "")
	require.Equal(t, CeilingSaturatedCapacity, d.Refusal, "bucket empty")
	*now = now.Add(1100 * time.Millisecond)
	_, d = login(l, "192.0.2.8", "", LoginSucceeded, "")
	require.Equal(t, CeilingAdmitted, d.Refusal, "refused for want of a token only: admitted once one refills")
}

// The saturated key is asked only when a ceiling is spent, and a saturated
// login is keyed on it, not on the visitor.
func TestLoginCeiling_SaturatedKeyIsAskedOnlyWhenSaturated(t *testing.T) {
	l := NewLoginCeiling(nil)
	l.SetSaturatedBound(math.Inf(1), 1)
	asked := 0
	key := func() string { asked++; return "2001:db8:aaaa::/48" }
	a, _ := l.Admit("2001:db8:aaaa:1::/64", key, "")
	a.Done(LoginFailed, "")
	require.Zero(t, asked, "an unsaturated login never derives the saturated key")
	fill(t, l, LoginGlobalCeiling-1)
	a, _ = l.Admit("2001:db8:aaaa:1::/64", key, "")
	require.True(t, a.Saturated())
	require.Equal(t, 1, asked)
	a.Done(LoginFailed, "")
	_, d := l.Admit("2001:db8:aaaa:2::/64", key, "")
	require.Equal(t, CeilingSaturatedFailed, d.Refusal, "another visitor of the same saturated key shares the refusal")
	_, d = l.Admit("192.0.2.5", nil, "")
	require.Equal(t, CeilingAdmitted, d.Refusal, "no saturated key: the shared unattributed key, not yet refused")
}

// Done is idempotent and nil-safe: a second Done neither releases twice nor
// counts twice.
func TestLoginAdmission_DoneOnce(t *testing.T) {
	l := NewLoginCeiling(nil)
	a, _ := l.Admit("192.0.2.5", nil, "")
	a.Done(LoginFailed, "")
	a.Done(LoginSucceeded, "nightwing")
	require.Equal(t, 1, globalCount(l))
	logins, _ := l.InFlight("192.0.2.5")
	require.Zero(t, logins, "left once, not below zero")
	var none *LoginAdmission
	none.Done(LoginFailed, "") // no panic
}

// S-3: idle visitors are swept from the cold end, ones with a login in
// flight are kept, and the table never passes its bound.
func TestLoginCeiling_VisitorTableIsSweptAndBounded(t *testing.T) {
	l, now := clocked()
	for i := range 50 {
		leave, ok := l.enter(fmt.Sprintf("idle-%d", i))
		require.True(t, ok)
		leave()
	}
	_, ok := l.enter("busy") // never left: a login still in flight
	require.True(t, ok)
	require.Equal(t, 51, l.visitors.len())
	*now = now.Add(LoginCeilingWindow + time.Second)
	leave, ok := l.enter("fresh")
	require.True(t, ok)
	leave()
	require.Equal(t, 2, l.visitors.len(), "the 50 idle visitors are gone; the busy one and the fresh one remain")
	_, kept := l.visitors.get("busy")
	require.True(t, kept, "a visitor with a login in flight is not swept")

	l2 := NewLoginCeiling(nil)
	for i := range MaxLoginVisitors + 25 {
		leave, ok := l2.enter(fmt.Sprintf("v-%d", i))
		require.True(t, ok)
		leave()
	}
	require.Equal(t, MaxLoginVisitors, l2.visitors.len(), "bounded; the oldest entries are evicted one at a time")
	_, first := l2.visitors.get("v-0")
	require.False(t, first)
	_, last := l2.visitors.get(fmt.Sprintf("v-%d", MaxLoginVisitors+24))
	require.True(t, last)
}

// N2: a visitor with a login in flight at the cold end of the table does not
// stop the sweep reaching the idle entries written after it.
func TestLoginCeiling_SweepIsNotBlockedByAColdInFlightVisitor(t *testing.T) {
	l, now := clocked()
	_, ok := l.enter("busy") // first in: the cold end, and never leaves
	require.True(t, ok)
	for i := range 50 {
		leave, ok := l.enter(fmt.Sprintf("idle-%d", i))
		require.True(t, ok)
		leave()
	}
	require.Equal(t, 51, l.visitors.len())
	*now = now.Add(LoginCeilingWindow + time.Second)
	leave, ok := l.enter("fresh")
	require.True(t, ok)
	leave()
	require.Equal(t, 2, l.visitors.len(), "the 50 idle visitors are swept; the busy one and the fresh one remain")
	_, kept := l.visitors.get("busy")
	require.True(t, kept)
}

// N4: at the bound a stream of new visitors evicts idle entries, never one
// with a login in flight or one refused for the rest of the window.
func TestLoginCeiling_BoundEvictsIdleVisitorsBeforeBusyOrRefusedOnes(t *testing.T) {
	l := NewLoginCeiling(nil)
	_, ok := l.enter("busy") // oldest entry, login in flight
	require.True(t, ok)
	l.saturatedFailed("refused") // refused for the window
	for i := range MaxLoginVisitors + 25 {
		leave, ok := l.enter(fmt.Sprintf("v-%d", i))
		require.True(t, ok)
		leave()
	}
	require.Equal(t, MaxLoginVisitors, l.visitors.len())
	busy, kept := l.visitors.get("busy")
	require.True(t, kept, "an in-flight visitor survives the bound")
	require.Equal(t, 1, busy.inFlight)
	_, kept = l.visitors.get("refused")
	require.True(t, kept, "a refused visitor does not get a fresh start from address rotation")
	_, first := l.visitors.get("v-0")
	require.False(t, first, "idle entries went instead")
}

// The pin on a visitor refused for the rest of the window ends with the
// window: a refused visitor touched after it ended is an ordinary, evictable
// entry.
func TestLoginCeiling_SaturatedFailurePinExpiresWithTheWindow(t *testing.T) {
	l, now := clocked()
	l.saturatedFailed("refused")
	*now = now.Add(LoginCeilingWindow - time.Minute)
	leave, ok := l.enter("refused") // written inside the window: the refusal still pins it
	require.True(t, ok)
	leave()
	for i := range MaxLoginVisitors + 25 {
		leave, ok := l.enter(fmt.Sprintf("a-%d", i))
		require.True(t, ok)
		leave()
	}
	_, kept := l.visitors.get("refused")
	require.True(t, kept, "inside its window the refusal still pins the entry against eviction")
	*now = now.Add(2 * time.Minute)
	leave, ok = l.enter("refused")
	require.True(t, ok)
	leave()
	for i := range MaxLoginVisitors + 25 {
		leave, ok := l.enter(fmt.Sprintf("b-%d", i))
		require.True(t, ok)
		leave()
	}
	require.Equal(t, MaxLoginVisitors, l.visitors.len())
	_, kept = l.visitors.get("refused")
	require.False(t, kept, "an expired refusal no longer pins the entry against eviction")
}

// A visitor with saturated requests in flight is never swept, however old its
// last write: dropping it would let the release find no entry and a fresh
// admission start from zero.
func TestLoginCeiling_SweepKeepsVisitorsWithSaturatedRequestsInFlight(t *testing.T) {
	l, now := clocked()
	l.SetSaturatedBound(math.Inf(1), 1)
	release, refusal, _ := l.admitSaturated("sat")
	require.Equal(t, CeilingAdmitted, refusal)
	*now = now.Add(LoginCeilingWindow + time.Second)
	leave, ok := l.enter("fresh") // triggers the sweep
	require.True(t, ok)
	leave()
	v, kept := l.visitors.get("sat")
	require.True(t, kept, "a visitor with a saturated request in flight survives the sweep")
	require.Equal(t, 1, v.satInFlight)
	release()
	require.Equal(t, 0, v.satInFlight)
	require.Equal(t, 0, l.saturated)
}

// SF-C: each saturated refusal tells the caller how long to wait for ITS
// reason. Direct, on a fake clock, so the arithmetic is exact.
func TestLoginCeiling_SaturatedRefusalRetryAfterMatchesTheReason(t *testing.T) {
	l, now := clocked()
	l.SetSaturatedBound(math.Inf(1), 1)

	// Failed: ceil(satFailedAt + window - now), at least 1.
	l.saturatedFailed("failed")
	*now = now.Add(10 * time.Second)
	_, refusal, retry := l.admitSaturated("failed")
	require.Equal(t, CeilingSaturatedFailed, refusal)
	require.Equal(t, int((LoginCeilingWindow-10*time.Second)/time.Second), retry, "the rest of the visitor's window")
	*now = now.Add(LoginCeilingWindow - 10*time.Second - 400*time.Millisecond)
	_, refusal, retry = l.admitSaturated("failed")
	require.Equal(t, CeilingSaturatedFailed, refusal)
	require.Equal(t, 1, retry, "a fraction of a second left rounds up to 1, never 0")

	// Busy: its own saturated requests are in flight; the fixed short wait.
	var releases []func()
	for range MaxSaturatedPerVisitor {
		release, refusal, _ := l.admitSaturated("busy")
		require.Equal(t, CeilingAdmitted, refusal)
		releases = append(releases, release)
	}
	_, refusal, retry = l.admitSaturated("busy")
	require.Equal(t, CeilingSaturatedBusy, refusal)
	require.Equal(t, 5, retry)
	for _, release := range releases {
		release()
	}

	// Capacity: max(1, ceil((1 - tokens) / rate)).
	c := NewLoginCeiling(func() time.Time { return *now })
	c.SetSaturatedBound(0.5, 1)
	release, refusal, _ := c.admitSaturated("a")
	require.Equal(t, CeilingAdmitted, refusal)
	release()
	_, refusal, retry = c.admitSaturated("b")
	require.Equal(t, CeilingSaturatedCapacity, refusal)
	require.Equal(t, 2, retry, "an empty bucket at 0.5/s needs 2s for one token")
	*now = now.Add(1200 * time.Millisecond) // 0.6 tokens: 0.4 more at 0.5/s is 0.8s
	_, refusal, retry = c.admitSaturated("b")
	require.Equal(t, CeilingSaturatedCapacity, refusal)
	require.Equal(t, 1, retry, "a partly refilled bucket rounds up to 1")
	*now = now.Add(time.Second)
	release, refusal, _ = c.admitSaturated("b")
	require.Equal(t, CeilingAdmitted, refusal, "and the advertised wait was enough")
	release()

	// A slower bucket advertises a longer wait, capped at the window.
	slow := NewLoginCeiling(func() time.Time { return *now })
	slow.SetSaturatedBound(0.1, 1)
	release, _, _ = slow.admitSaturated("a")
	release()
	_, refusal, retry = slow.admitSaturated("b")
	require.Equal(t, CeilingSaturatedCapacity, refusal)
	require.Equal(t, 10, retry)
	// Deliberately below MinSaturatedRate, to reach the defensive window cap
	// that no valid configuration can.
	slow.SetSaturatedBound(0.0001, 1)
	release, _, _ = slow.admitSaturated("c")
	release()
	_, _, retry = slow.admitSaturated("d")
	require.Equal(t, int(LoginCeilingWindow/time.Second), retry, "never advertised past the window")
	zero := NewLoginCeiling(func() time.Time { return *now })
	zero.SetSaturatedBound(0, 1)
	release, _, _ = zero.admitSaturated("a")
	release()
	_, _, retry = zero.admitSaturated("b")
	require.Equal(t, int(LoginCeilingWindow/time.Second), retry, "a bucket that never refills advertises the window")

	// The hard bound (goroutine safety) waits out the longest saturation delay.
	h := NewLoginCeiling(func() time.Time { return *now })
	h.SetSaturatedBound(math.Inf(1), 1)
	h.mu.Lock()
	h.saturated = MaxSaturatedHard
	h.mu.Unlock()
	_, refusal, retry = h.admitSaturated("e")
	require.Equal(t, CeilingSaturatedCapacity, refusal)
	require.Equal(t, int(SaturationDelayMax/time.Second), retry)
}

// Concurrent logins keep the counts exact: every failure is counted once,
// every other answer hands its slot back, and nothing stays in flight.
func TestLoginCeiling_ConcurrentLoginsKeepExactCounts(t *testing.T) {
	l := NewLoginCeiling(nil)
	var wg sync.WaitGroup
	const workers, each = 8, 20
	for w := range workers {
		wg.Go(func() {
			visitor := fmt.Sprintf("192.0.2.%d", w+1)
			for i := range each {
				outcome := LoginOther
				if i%2 == 0 {
					outcome = LoginFailed
				}
				_, d := login(l, visitor, "192.0.2.45", outcome, "")
				if d.Refusal != CeilingAdmitted {
					t.Errorf("visitor %s login %d refused %d", visitor, i, d.Refusal)
				}
			}
		})
	}
	wg.Wait()
	g, c := l.Failures("192.0.2.45")
	require.Equal(t, workers*each/2, g)
	require.Equal(t, workers*each/2, c)
	for w := range workers {
		logins, sat := l.InFlight(fmt.Sprintf("192.0.2.%d", w+1))
		require.Zero(t, logins)
		require.Zero(t, sat)
	}
}

func TestSaturatedLimitsValid(t *testing.T) {
	for _, tt := range []struct {
		rate  float64
		burst int
		ok    bool
	}{
		{DefaultSaturatedRate, DefaultSaturatedBurst, true},
		{MinSaturatedRate, 1, true},
		{MaxSaturatedRate, MaxSaturatedBurst, true},
		{MinSaturatedRate / 2, 1, false},
		{MaxSaturatedRate + 0.1, 1, false},
		{0, 1, false},
		{math.NaN(), 1, false},
		{math.Inf(1), 1, false},
		{2, 0, false},
		{2, MaxSaturatedBurst + 1, false},
	} {
		require.Equal(t, tt.ok, SaturatedLimitsValid(tt.rate, tt.burst), "rate %v burst %d", tt.rate, tt.burst)
	}
}

// The bucket keeps golang.org/x/time/rate's semantics for the two calls the
// ceiling makes: full at the start, a refused call spends nothing, refill at
// the rate up to the burst, and an infinite rate admits everything.
func TestTokenBucket_Semantics(t *testing.T) {
	b := newTokenBucket(2, 3)
	require.InDelta(t, 3, b.tokensAt(ceilingT0), 1e-9, "full at the start")
	for range 3 {
		require.True(t, b.allow(ceilingT0))
	}
	require.False(t, b.allow(ceilingT0), "empty")
	require.InDelta(t, 0, b.tokensAt(ceilingT0), 1e-9, "a refused call spends nothing")
	require.InDelta(t, 1, b.tokensAt(ceilingT0.Add(500*time.Millisecond)), 1e-9, "refills at the rate")
	require.False(t, b.allow(ceilingT0.Add(400*time.Millisecond)), "0.8 of a token is not one")
	require.True(t, b.allow(ceilingT0.Add(500*time.Millisecond)))
	require.InDelta(t, 3, b.tokensAt(ceilingT0.Add(time.Hour)), 1e-9, "never past the burst")
	require.InDelta(t, 0, b.tokensAt(ceilingT0), 1e-9, "a time before the last call adds nothing")
	for _, r := range []float64{math.Inf(1), math.MaxFloat64} {
		u := newTokenBucket(r, 1)
		for range 100 {
			require.True(t, u.allow(ceilingT0))
		}
		require.InDelta(t, 1, u.tokensAt(ceilingT0), 1e-9)
	}
}

func TestLoginCeiling_SaturatedBoundAndClockSeams(t *testing.T) {
	l := NewLoginCeiling(nil)
	r, b := l.SaturatedBound()
	require.InDelta(t, DefaultSaturatedRate, r, 1e-9)
	require.Equal(t, DefaultSaturatedBurst, b)
	l.SetSaturatedBound(0.5, 3)
	r, b = l.SaturatedBound()
	require.InDelta(t, 0.5, r, 1e-9)
	require.Equal(t, 3, b)
	at := ceilingT0
	l.SetClock(func() time.Time { return at })
	l.SetClock(nil) // ignored
	fill(t, l, 1)
	at = at.Add(LoginCeilingWindow + time.Second)
	require.Zero(t, globalCount(l), "the replaced clock rolls the window")
}
