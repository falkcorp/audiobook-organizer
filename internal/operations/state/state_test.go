// file: internal/operations/state/state_test.go
// version: 1.0.0
// guid: e21fad58-28c0-4247-986a-a964c0817c81
// last-edited: 2026-10-10

package state

import (
	"strings"
	"testing"
)

// TestClassifyPinsEveryStatus pins all eight Props fields for every status in
// the table. A change here changes what a poller, the resume sweep, the
// completed_at stamp or the Activity page buttons do, so it must be deliberate.
func TestClassifyPinsEveryStatus(t *testing.T) {
	finished := Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}
	want := map[State]Props{
		Queued:              {Resumable: true},
		WaitingDeps:         {},
		Running:             {HoldsSlot: true, Resumable: true},
		Completed:           {Terminal: true, Discardable: true, Settled: true},
		Failed:              finished,
		Canceled:            finished,
		InterruptedDropped:  finished,
		InterruptedQuiesced: {Resumable: true, Retryable: true, Discardable: true, Settled: true},
		InterruptedAsk:      {Retryable: true, Discardable: true, AwaitingDecision: true, Settled: true},
		InterruptedRestart:  {Retryable: true, Discardable: true, Settled: true, Legacy: true},
		Interrupted:         {Retryable: true, Discardable: true, Settled: true, Legacy: true},
	}

	all := All()
	if len(all) != len(want) {
		t.Fatalf("All() has %d statuses, test pins %d: add the new status to this table", len(all), len(want))
	}
	for _, s := range all {
		w, ok := want[s]
		if !ok {
			t.Errorf("status %q is not pinned by this test", s)
			continue
		}
		if got := Classify(string(s)); got != w {
			t.Errorf("Classify(%q) = %+v, want %+v", s, got, w)
		}
	}
}

// TestAllOrderIsStable pins the order the generator emits, so a reordering
// shows up here rather than only as a regenerated ops.ts diff.
func TestAllOrderIsStable(t *testing.T) {
	want := []State{
		Queued, WaitingDeps, Running, Completed, Failed, Canceled,
		InterruptedDropped, InterruptedQuiesced, InterruptedAsk,
		InterruptedRestart, Interrupted,
	}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("All() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("All()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUnknownStatusIsLive(t *testing.T) {
	for _, s := range []string{"banana", "", "interrupting", "cancelled", "Interrupted_quiesced", "interruptedx"} {
		p := Classify(s)
		if p != (Props{}) {
			t.Errorf("Classify(%q) = %+v, want zero Props (live)", s, p)
		}
		if IsTerminal(s) || IsSettled(s) || IsInterrupted(s) {
			t.Errorf("%q must be live: IsTerminal=%v IsSettled=%v IsInterrupted=%v",
				s, IsTerminal(s), IsSettled(s), IsInterrupted(s))
		}
	}
}

// TestFutureInterruptedPolicyIsInterrupted: an interrupted_* status nobody has
// added to the table yet must still stop pollers and offer Retry/Discard.
//
// Deviation from the 05-PR1 brief, which asked for Resumable here too:
// Resumable is "the boot sweep picks it up", and the sweep reads only
// interrupted_quiesced. Marking an unknown policy resumable would make
// database.isResumableV2Status hand it to resumeAfterStartup, a behaviour
// change no caller asked for.
func TestFutureInterruptedPolicyIsInterrupted(t *testing.T) {
	const s = "interrupted_future"
	p := Classify(s)
	if !p.Settled || !p.Retryable || !p.Discardable {
		t.Errorf("Classify(%q) = %+v, want Settled, Retryable and Discardable", s, p)
	}
	if p.Terminal || p.Resumable || p.HoldsSlot || p.Legacy || p.AwaitingDecision {
		t.Errorf("Classify(%q) = %+v, want only Settled, Retryable, Discardable", s, p)
	}
	if !IsInterrupted(s) {
		t.Errorf("IsInterrupted(%q) = false", s)
	}
	if p != UnknownInterrupted() {
		t.Errorf("UnknownInterrupted() = %+v, Classify(%q) = %+v; the generator reads the former", UnknownInterrupted(), s, p)
	}
}

func TestIsInterruptedFamily(t *testing.T) {
	for _, s := range All() {
		want := s == Interrupted || strings.HasPrefix(string(s), "interrupted_")
		if got := IsInterrupted(string(s)); got != want {
			t.Errorf("IsInterrupted(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestSettledIsTerminalOrInterrupted is the invariant the pollers rely on.
func TestSettledIsTerminalOrInterrupted(t *testing.T) {
	for _, s := range append(All(), "interrupted_future", "banana") {
		p := Classify(string(s))
		if want := p.Terminal || IsInterrupted(string(s)); p.Settled != want {
			t.Errorf("%q: Settled=%v, want Terminal||IsInterrupted=%v", s, p.Settled, want)
		}
	}
}

func TestWithProp(t *testing.T) {
	got := WithProp(func(p Props) bool { return p.Terminal })
	want := []string{"completed", "failed", "canceled", "interrupted_dropped"}
	if len(got) != len(want) {
		t.Fatalf("WithProp(Terminal) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WithProp(Terminal) = %v, want %v", got, want)
		}
	}
}
