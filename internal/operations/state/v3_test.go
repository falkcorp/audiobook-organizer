// file: internal/operations/state/v3_test.go
// version: 1.0.2
// guid: e23135cd-0f05-456d-b2f0-dbe4bdefb9c1
// last-edited: 2026-10-13

package state

import (
	"errors"
	"strings"
	"testing"
)

type transitionKey struct {
	from V3
	ev   Event
	// reason is the stop reason passed to Next ("" for every other event).
	reason string
}

// allowedTransitions is the transition list of state-and-persistence.md
// section 1, written out independently of Next so the test is not a copy of
// the code it checks.
func allowedTransitions() map[transitionKey]V3 {
	m := map[transitionKey]V3{
		{"", EvEnqueue, ""}:                             V3Queued,
		{V3Queued, EvWaitCleared, ""}:                   V3Queued,
		{V3Queued, EvDispatch, ""}:                      V3Running,
		{V3Running, EvReturnNil, ""}:                    V3Succeeded,
		{V3Running, EvReturnErr, ""}:                    V3Failed,
		{V3Running, EvBootSweep, ""}:                    V3Interrupted,
		{V3Stopping, EvBootSweep, ""}:                   V3Interrupted,
		{V3Interrupted, EvResume, ""}:                   V3Queued,
		{V3Interrupted, EvAsk, ""}:                      V3AwaitingDecision,
		{V3AwaitingDecision, EvDecideRetry, ""}:         V3Queued,
		{V3Failed, EvDecideRetry, ""}:                   V3Queued,
		{V3Canceled, EvDecideRetry, ""}:                 V3Queued,
		{V3TimedOut, EvDecideRetry, ""}:                 V3Queued,
		{V3Dropped, EvDecideRetry, ""}:                  V3Queued,
		{V3AwaitingDecision, EvDecideDrop, ""}:          V3Dropped,
		{V3Interrupted, EvDecideDrop, ""}:               V3Dropped,
		{V3Stopping, EvGoroutineReturned, StopUser}:     V3Canceled,
		{V3Stopping, EvGoroutineReturned, StopQuiesce}:  V3Interrupted,
		{V3Stopping, EvGoroutineReturned, StopShutdown}: V3Interrupted,
		{V3Stopping, EvGoroutineReturned, StopTimeout}:  V3TimedOut,
		{V3Stopping, EvGoroutineReturned, StopWatchdog}: V3TimedOut,
	}
	for _, r := range StopReasons {
		m[transitionKey{V3Running, EvStopRequested, r}] = V3Stopping
	}
	return m
}

// TestNextIsClosedSet walks every (from, event, reason) combination, including
// the not-yet-existing run "", an unknown state and the two record-only
// events, and requires Next to accept exactly the listed transitions.
func TestNextIsClosedSet(t *testing.T) {
	allowed := allowedTransitions()
	froms := append([]V3{"", "bogus"}, AllV3()...)
	events := append([]Event{EvMigrate, EvV2Write, "bogus"}, AllEvents...)
	reasons := append([]string{"", "bogus"}, StopReasons...)
	seen := 0
	for _, from := range froms {
		for _, ev := range events {
			for _, r := range reasons {
				got, err := Next(from, ev, r)
				// Events other than the two stop events ignore the reason.
				key := transitionKey{from, ev, r}
				if ev != EvStopRequested && ev != EvGoroutineReturned {
					key.reason = ""
				}
				want, ok := allowed[key]
				switch {
				case ok && err != nil:
					t.Errorf("Next(%q, %s, %q) refused a listed transition: %v", from, ev, r, err)
				case ok && got != want:
					t.Errorf("Next(%q, %s, %q) = %q, want %q", from, ev, r, got, want)
				case !ok && err == nil:
					t.Errorf("Next(%q, %s, %q) = %q, want an error (not in the transition list)", from, ev, r, got)
				case !ok && !errors.Is(err, ErrTransition):
					t.Errorf("Next(%q, %s, %q) error %v does not wrap ErrTransition", from, ev, r, err)
				}
				if ok {
					seen++
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no listed transition was exercised")
	}
}

func TestClassifyV3PinsEveryState(t *testing.T) {
	finished := Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}
	want := map[V3]Props{
		V3Queued:           {Resumable: true},
		V3Running:          {HoldsSlot: true, Resumable: true},
		V3Stopping:         {HoldsSlot: true, Resumable: true},
		V3Interrupted:      {Resumable: true, Retryable: true, Discardable: true, Settled: true},
		V3AwaitingDecision: {Retryable: true, Discardable: true, AwaitingDecision: true, Settled: true},
		V3Succeeded:        {Terminal: true, Discardable: true, Settled: true},
		V3Failed:           finished,
		V3Canceled:         finished,
		V3TimedOut:         finished,
		V3Dropped:          finished,
	}
	if len(AllV3()) != 10 || len(want) != 10 {
		t.Fatalf("the v3 table has %d states, want ten", len(AllV3()))
	}
	for _, s := range AllV3() {
		if got := ClassifyV3(s); got != want[s] {
			t.Errorf("ClassifyV3(%q) = %+v, want %+v", s, got, want[s])
		}
	}
	if (ClassifyV3("nope") != Props{}) {
		t.Error("an unknown v3 state must classify as live (zero Props)")
	}
}

// TestToV2EveryState is the 10-row mirror table (plus the reason variants
// that change the answer).
func TestToV2EveryState(t *testing.T) {
	cases := []struct {
		name                     string
		s                        V3
		wait, why                string
		successor, ledger        bool
		msg                      string
		wantStatus, wantMsgStart string
	}{
		{"queued", V3Queued, "", "", false, false, "", "queued", ""},
		{"queued ready", V3Queued, WaitReady, "", false, false, "", "queued", ""},
		{"queued pause", V3Queued, WaitPause, "", false, false, "", "queued", ""},
		{"queued deps", V3Queued, WaitDeps, "", false, false, "", "waiting_deps", ""},
		{"running", V3Running, "", "", false, false, "", "running", ""},
		{"stopping", V3Stopping, "", "", false, false, "", "running", ""},
		{"interrupted", V3Interrupted, "", WhyQuiesced, false, false, "", "interrupted_quiesced", ""},
		{"interrupted crash", V3Interrupted, "", WhyCrash, false, false, "", "interrupted_quiesced", ""},
		{"awaiting_decision", V3AwaitingDecision, "", "", false, false, "x", "interrupted_ask", "x"},
		{"succeeded", V3Succeeded, "", "", false, false, "", "completed", ""},
		{"failed", V3Failed, "", "", false, false, "boom", "failed", "boom"},
		{"canceled", V3Canceled, "", "", false, false, "", "canceled", ""},
		{"timed_out", V3TimedOut, "", "", false, false, "run exceeded 1h", "canceled", "timeout: run exceeded 1h"},
		{"timed_out keeps prefix", V3TimedOut, "", "", false, false, "timeout: run exceeded 1h", "canceled", "timeout: run exceeded 1h"},
		{"dropped", V3Dropped, "", "", false, false, "", "interrupted_dropped", ""},
		{"dropped with successor", V3Dropped, "", "", true, false, "", "interrupted_dropped", RequeuedMessage},
	}
	covered := map[V3]bool{}
	for _, c := range cases {
		gotS, gotM := ToV2(c.s, c.wait, c.why, c.successor, c.ledger, c.msg)
		if gotS != c.wantStatus || !strings.HasPrefix(gotM, c.wantMsgStart) {
			t.Errorf("%s: ToV2 = (%q, %q), want (%q, %q...)", c.name, gotS, gotM, c.wantStatus, c.wantMsgStart)
		}
		covered[c.s] = true
	}
	for _, s := range AllV3() {
		if !covered[s] {
			t.Errorf("state %q has no ToV2 row", s)
		}
	}
	if got, _ := ToV2(V3TimedOut, "", "", false, false, ""); got != "canceled" {
		t.Errorf("timed_out with no message mirrors as %q", got)
	}
	if _, m := ToV2(V3TimedOut, "", "", false, false, ""); !strings.HasPrefix(m, TimeoutPrefix) {
		t.Errorf("timed_out with no message must still carry the timeout prefix, got %q", m)
	}
}

// TestLedgerRunMirrorsAsDropped: a v2 binary cannot resume a chunk ledger.
func TestLedgerRunMirrorsAsDropped(t *testing.T) {
	if got, _ := ToV2(V3Interrupted, "", WhyQuiesced, false, true, ""); got != "interrupted_dropped" {
		t.Fatalf("interrupted run with a ledger mirrors as %q, want interrupted_dropped", got)
	}
}

// TestFromV2EveryRowOfTheMappingTable is section 3's table: 13 rows plus
// "anything else".
func TestFromV2EveryRowOfTheMappingTable(t *testing.T) {
	cases := []struct {
		status, msg string
		want        V3
		why         string
		ok          bool
	}{
		{"queued", "", V3Queued, "", true},
		{"waiting_deps", "", V3Queued, WaitDeps, true},
		{"running", "", V3Interrupted, WhyCrash, true},
		{"completed", "", V3Succeeded, "", true},
		{"failed", "x", V3Failed, "", true},
		{"canceled", "timeout: run exceeded 5m", V3TimedOut, "", true},
		{"canceled", "canceled by user", V3Canceled, "", true},
		{"interrupted_quiesced", "", V3Interrupted, WhyQuiesced, true},
		{"interrupted_ask", "", V3AwaitingDecision, "", true},
		{"interrupted_dropped", RequeuedMessage, V3Dropped, WhyRequeued, true},
		{"interrupted_dropped", "force-dropped", V3Dropped, "", true},
		{"interrupted_restart", "", V3Dropped, WhyRequeued, true},
		{"interrupted", "", V3Interrupted, WhyLegacy, true},
		{"pending", "", V3Failed, "", false},
		{"", "", V3Failed, "", false},
	}
	for _, c := range cases {
		got, why, ok := FromV2(c.status, c.msg)
		if got != c.want || why != c.why || ok != c.ok {
			t.Errorf("FromV2(%q, %q) = (%q, %q, %v), want (%q, %q, %v)", c.status, c.msg, got, why, ok, c.want, c.why, c.ok)
		}
	}
	if got, _, _ := FromV2Live("running", ""); got != V3Running {
		t.Errorf("FromV2Live(running) = %q, want running: a live v2 write is a run this process executes", got)
	}
}

// TestRoundTripV2 holds ToV2(FromV2(s)) == s for every status the v2
// registry mints, so a v2 binary reading the mirror sees what it wrote.
// "running" round-trips through FromV2Live (a live write); FromV2 maps it to
// interrupted{crash} on purpose (nothing owns it at migration time).
func TestRoundTripV2(t *testing.T) {
	minted := []struct{ status, msg string }{
		{"queued", ""},
		{"waiting_deps", ""},
		{"running", ""},
		{"completed", ""},
		{"failed", "boom"},
		{"canceled", ""},
		{"canceled", "timeout: run exceeded 5m"},
		{"interrupted_quiesced", "interrupted: server shutdown during run"},
		{"interrupted_ask", "awaiting user decision"},
		{"interrupted_dropped", "force-dropped: infinite restart without progress"},
		{"interrupted_dropped", RequeuedMessage},
	}
	for _, m := range minted {
		s, why, ok := FromV2Live(m.status, m.msg)
		if !ok {
			t.Fatalf("FromV2Live(%q) not ok", m.status)
		}
		wait, iwhy := "", ""
		if s == V3Queued {
			wait = why
		}
		if s == V3Interrupted {
			iwhy = why
		}
		gotS, gotM := ToV2(s, wait, iwhy, why == WhyRequeued, false, m.msg)
		if gotS != m.status || gotM != m.msg {
			t.Errorf("round trip of (%q, %q) = (%q, %q)", m.status, m.msg, gotS, gotM)
		}
	}
}

// TestInferEvent: the façade names the event for every listed move and
// reports false for the v2 moves the v3 table does not have.
func TestInferEvent(t *testing.T) {
	for k, to := range allowedTransitions() {
		if k.from == to {
			continue // queued --wait_cleared--> queued is not a state change
		}
		ev, reason, ok := InferEvent(k.from, to)
		if !ok {
			t.Errorf("InferEvent(%q, %q) found no event; %s connects them", k.from, to, k.ev)
			continue
		}
		if got, err := Next(k.from, ev, reason); err != nil || got != to {
			t.Errorf("InferEvent(%q, %q) = (%s, %q), but Next gives (%q, %v)", k.from, to, ev, reason, got, err)
		}
	}
	for _, pair := range [][2]V3{
		{V3Queued, V3Canceled},
		{V3Running, V3Interrupted + "x"},
		{V3Queued, V3Failed},
		{V3Canceled, V3Succeeded},
		{V3Running, V3Canceled},
	} {
		if ev, _, ok := InferEvent(pair[0], pair[1]); ok {
			t.Errorf("InferEvent(%q, %q) = %s; no single listed event connects them", pair[0], pair[1], ev)
		}
	}
}
