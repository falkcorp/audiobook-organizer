// file: internal/transcribe/canary_deadport_test.go
// version: 1.0.0
// guid: d36a51ab-655f-4fc0-b079-c840df29370a
// last-edited: 2026-10-27

package transcribe

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// D56 of the review of f241ec20a: a configured port that has refused every
// probe for 24 hours stops counting as a port of its machine. Until then a
// machine stays remembered while one of its ports is down; after it, a dead
// tunnel left in the configuration no longer keeps a sound machine asked a
// canary clip for as long as it stays there. The clock is canaryNow, moved by
// these tests.

// testClock is canaryNow for one test: it starts at a fixed instant and moves
// only when told to.
type testClock struct{ ns atomic.Int64 }

func (c *testClock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *testClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

func fakeCanaryClock(t *testing.T) *testClock {
	t.Helper()
	c := &testClock{}
	c.ns.Store(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC).UnixNano())
	old := canaryNow
	canaryNow = c.now
	t.Cleanup(func() { canaryNow = old })
	return c
}

// portIsDead is portDeadLocked under the lock.
func portIsDead(url string) bool {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	return portDeadLocked(url)
}

// refusedSince is when the run of refusals of url began, and whether one is running.
func refusedSince(url string) (time.Time, bool) {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	t, ok := canaryFailed.firstRefused[url]
	return t, ok
}

func TestDeadPort_TheThresholdIsTwentyFourHours(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	clock := fakeCanaryClock(t)
	const url = "http://gotham-a:8000"
	if portIsDead(url) {
		t.Fatal("a port that was never refused is dead")
	}
	noteProbeResult(url, false)
	clock.advance(24*time.Hour - time.Nanosecond)
	if portIsDead(url) {
		t.Error("dead 1ns before 24h")
	}
	clock.advance(time.Nanosecond)
	if !portIsDead(url) {
		t.Error("not dead at exactly 24h")
	}
}

func TestDeadPort_AnAnswerEndsTheRunOfRefusals(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	clock := fakeCanaryClock(t)
	const url = "http://gotham-a:8000"
	noteProbeResult(url, false)
	clock.advance(23 * time.Hour)
	noteProbeResult(url, true)
	if _, running := refusedSince(url); running {
		t.Fatal("a run of refusals outlived an answered probe")
	}
	clock.advance(2 * time.Hour)
	if portIsDead(url) {
		t.Fatal("dead 25h after the first refusal although a probe answered in between")
	}
	noteProbeResult(url, false)
	clock.advance(23 * time.Hour)
	if portIsDead(url) {
		t.Error("the new run of refusals was counted from the old one")
	}
	clock.advance(time.Hour)
	if !portIsDead(url) {
		t.Error("not dead 24h after the new run began")
	}
}

func TestDeadPort_ARepeatedRefusalDoesNotRestartTheClock(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	clock := fakeCanaryClock(t)
	const url = "http://gotham-a:8000"
	noteProbeResult(url, false)
	first, _ := refusedSince(url)
	clock.advance(12 * time.Hour)
	noteProbeResult(url, false)
	if got, _ := refusedSince(url); !got.Equal(first) {
		t.Errorf("the run began at %s after a second refusal, want %s", got, first)
	}
	clock.advance(12 * time.Hour)
	if !portIsDead(url) {
		t.Error("not dead 24h after the first refusal")
	}
}

// noteCanaryProbes is what feeds the rule: a probe that was answered ends the
// run whoever answered it (a kept endpoint that was probed, a refusal that names
// a machine), a probe that was not (a kept endpoint that could not be probed, a
// refusal that names none) begins or continues one.
func TestDeadPort_NoteCanaryProbesTracksEveryKindOfProbe(t *testing.T) {
	const url = "http://gotham-a:8000"
	unanswered := []struct {
		name    string
		kept    []gatedEndpoint
		refused []gpuRefusal
	}{
		{"kept unprobed", []gatedEndpoint{{Endpoint: Endpoint{URL: url}}}, nil},
		{"refused with no machine", nil, []gpuRefusal{{URL: url}}},
	}
	answered := []struct {
		name    string
		kept    []gatedEndpoint
		refused []gpuRefusal
	}{
		{"kept probed", []gatedEndpoint{{Endpoint: Endpoint{URL: url}, Probed: true, Health: remoteHealth{MachineID: batMachine, Backend: backendMLX}}}, nil},
		{"refused by a worker that named its machine", nil, []gpuRefusal{{URL: url, Machine: batKey, IDCapable: true}}},
		{"refused by a worker that named a weak identity", nil, []gpuRefusal{{URL: url, Machine: "u:" + shortHash(url)}}},
	}
	for _, tc := range unanswered {
		t.Run("begins a run: "+tc.name, func(t *testing.T) {
			resetCanaryFailed()
			t.Cleanup(resetCanaryFailed)
			clock := fakeCanaryClock(t)
			noteCanaryProbes(tc.kept, tc.refused)
			if _, running := refusedSince(url); !running {
				t.Fatal("no run of refusals began")
			}
			clock.advance(24 * time.Hour)
			noteCanaryProbes(tc.kept, tc.refused)
			if !portIsDead(url) {
				t.Error("not dead after 24h of refusals")
			}
		})
	}
	for _, tc := range answered {
		t.Run("ends a run: "+tc.name, func(t *testing.T) {
			resetCanaryFailed()
			t.Cleanup(resetCanaryFailed)
			clock := fakeCanaryClock(t)
			noteProbeResult(url, false)
			clock.advance(24 * time.Hour)
			noteCanaryProbes(tc.kept, tc.refused)
			if _, running := refusedSince(url); running || portIsDead(url) {
				t.Error("the run of refusals survived an answered probe")
			}
		})
	}
}

// markCanaryFailed adds every URL a probe attributed to a machine to its ports,
// up or down; a port that is dead is not added.
func TestDeadPort_ADeadPortIsNotAddedToAMachineThatFailsLater(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	clock := fakeCanaryClock(t)
	const live, down = "http://gotham-live:8000", "http://gotham-down:8000"
	noteCanaryPort(batKey, live, true)
	noteCanaryPort(batKey, down, true)
	noteProbeResult(down, false)

	markCanaryFailed(batKey)
	if e := failedEntry(batKey); e == nil || !e.ports[live] || !e.ports[down] {
		t.Fatalf("entry = %+v, want both ports while the down one has been refused for less than 24h", e)
	}
	clearCanaryFailed(batKey)
	clock.advance(deadPortAfter)
	markCanaryFailed(batKey)
	e := failedEntry(batKey)
	if e == nil || !e.ports[live] {
		t.Fatalf("entry = %+v, want the live port", e)
	}
	if e.ports[down] {
		t.Error("a port that has been refused for 24h was added to the machine")
	}
}

// A weak identity is resolved to a machine through the ports it lists; a dead
// port no longer leads there.
func TestDeadPort_AWeakKeyAtADeadPortNoLongerResolvesToTheMachine(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	clock := fakeCanaryClock(t)
	const url = "http://gotham-down:8000"
	weak := "u:" + shortHash(url)
	markCanaryFailed(batKey, url)
	noteProbeResult(url, false)
	if got := canaryMemoryKey(weak, url); got != batKey {
		t.Fatalf("resolved to %q before the port was dead, want %q", got, batKey)
	}
	clock.advance(deadPortAfter)
	if got := canaryMemoryKey(weak, url); got != "" {
		t.Errorf("resolved to %q through a port that has been refused for 24h", got)
	}
}

// Both reads of hasUnheardPort: a refusal that names no machine and a kept
// endpoint that could not be probed count for the machine only while the port
// is a live one of it.
func TestDeadPort_ADeadPortIsNotAnUnheardPort(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	clock := fakeCanaryClock(t)
	const url = "http://gotham-down:8000"
	markCanaryFailed(batKey, url)
	noteProbeResult(url, false)
	refused := []gpuRefusal{{URL: url}}
	kept := []gatedEndpoint{{Endpoint: Endpoint{URL: url}}}
	if !hasUnheardPort(batKey, refused, nil) {
		t.Error("a refused port of the machine is not unheard before it has been dead for 24h")
	}
	if !hasUnheardPort(batKey, nil, kept) {
		t.Error("a kept unprobed port of the machine is not unheard before it has been dead for 24h")
	}
	clock.advance(deadPortAfter)
	if hasUnheardPort(batKey, refused, nil) {
		t.Error("a refused port that has been dead for 24h is still unheard")
	}
	if hasUnheardPort(batKey, nil, kept) {
		t.Error("a kept unprobed port that has been dead for 24h is still unheard")
	}
	// What the refusal names is not affected: a worker that answered as the
	// machine is heard from whatever its URL's history.
	if !hasUnheardPort(batKey, []gpuRefusal{{URL: url, Machine: batKey}}, nil) {
		t.Error("a refusal that names the machine was ignored")
	}
}

// The configured URLs the operator removed take their record of refusals with
// them.
func TestDeadPort_PruningTheConfigurationForgetsTheRefusals(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	fakeCanaryClock(t)
	const kept, gone = "http://gotham-kept:8000", "http://gotham-gone:8000"
	noteProbeResult(kept, false)
	noteProbeResult(gone, false)
	pruneUnconfiguredCanaryURLs([]Endpoint{{URL: kept}})
	if _, ok := refusedSince(kept); !ok {
		t.Error("the refusals of a configured URL were forgotten")
	}
	if _, ok := refusedSince(gone); ok {
		t.Error("the refusals of a URL no longer configured were kept")
	}
}

// S-B end to end: a machine remembered as sick with one of its ports down. The
// first dispatch keeps it remembered; once that port has been down for 24h the
// next one forgets the machine on the answers of its other ports.
func TestDeadPort_ADownPortStopsKeepingTheMachineRememberedAfterADay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		requires []string
	}{
		{"refused at the gate (a pool that requires a gpu)", []string{"gpu"}},
		{"kept unprobed (a pool with no requirement)", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := fakeCanaryClock(t)
			p := poolOf(t, batMachine, batMachine, batMachine)
			down := p.h.eps[2].URL
			markCanaryFailed(batKey, down) // a probe once reported it as the machine's
			p.h.srvs[2].Close()

			rec := &canaryRecorder{}
			if _, err := dispatchOn(testCtx(t), p.h.eps, tc.requires, p.send, rec.fn, 8); err != nil {
				t.Fatalf("first dispatch: err = %v", err)
			}
			if !failedCanaryBefore(batKey) {
				t.Fatal("forgotten on the first dispatch, while its port had been down for no time at all")
			}
			if rec.count() != 2 {
				t.Fatalf("first dispatch asked %v, want the two ports that are up", rec.asked)
			}

			clock.advance(deadPortAfter)
			rec2 := &canaryRecorder{}
			res, err := dispatchOn(testCtx(t), p.h.eps, tc.requires, p.send, rec2.fn, 8)
			if err != nil {
				t.Fatalf("second dispatch: err = %v", err)
			}
			requireTranscripts(t, res, 8)
			if rec2.count() != 2 || slices.Contains(rec2.asked, down) {
				t.Errorf("second dispatch asked %v, want the two ports that are up", rec2.asked)
			}
			if failedCanaryBefore(batKey) {
				t.Error("still remembered after its down port had refused every probe for 24h")
			}
		})
	}
}

// A dispatch cut short by cancellation read nothing from its probes: they all
// look unreachable, and must not begin a run of refusals.
func TestDeadPort_ACancelledGateStartsNoRunOfRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		requires []string
	}{
		{"refused at the gate", []string{"gpu"}},
		{"kept unprobed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeCanaryClock(t)
			p := poolOf(t, batMachine, batMachine)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			rec := &canaryRecorder{}
			_, _ = dispatchOn(ctx, p.h.eps, tc.requires, p.send, rec.fn, 8)
			for _, ep := range p.h.eps {
				if since, ok := refusedSince(ep.URL); ok {
					t.Errorf("%s has a run of refusals since %s after a gate that was cancelled", ep.URL, since)
				}
			}
		})
	}
}
