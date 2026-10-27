// file: internal/transcribe/canary_changed_hands_test.go
// version: 1.0.0
// guid: 5d0c7e2a-83b1-4f6e-9a47-2c1e6b0d8f35
// last-edited: 2026-10-27

package transcribe

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// B-1 and S-1 of review 6 of #4201. A dispatch that waits out a cooldown
// re-probes the port, and what answers there now may not be the machine the
// dispatch knew it as: the port changed hands while it waited. Its answers and
// its timeout strikes must never be stored as the old machine (one machine could
// then make a file permanent by itself), so the port is dropped from the
// dispatch like a refusal. And a restarted port of a machine that struck a file
// still has to be steered away from.

// requireAllOnPort0AsBat is what a pool dispatch whose second port changed
// hands must end as: no error, every job answered by port 0 (the machine that
// failed the canary clip, which answered it) and stored as that machine, and
// nothing sent to, or asked of, the port that is somebody else's now.
func requireAllOnPort0AsBat(t *testing.T, p *faultPool, rec *canaryRecorder, res map[string]BatchResult, err error) {
	t.Helper()
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	if err != nil {
		t.Fatalf("err = %v, want the dispatch to go on with the port that is still the machine", err)
	}
	if len(res) != 8 {
		t.Fatalf("%d results, want 8", len(res))
	}
	for id, r := range res {
		if r.Endpoint != port0 || r.Worker != batKey || r.Text != "T:"+id {
			t.Errorf("%s = endpoint %s worker %q text %q, want an answer of port 0 stored as %s", id, r.Endpoint, r.Worker, r.Text, batKey)
		}
	}
	for _, s := range p.sendLog() {
		if s.url == port1 {
			t.Errorf("port 1, which answers as another worker now, was sent %v", s.ids)
		}
	}
	if slices.Contains(rec.asked, port1) {
		t.Errorf("the canary clip of the machine that failed it was sent to port 1, which is another worker's now: %v", rec.asked)
	}
}

func TestChangedHands_APortTakenOverByAnotherMachineDuringTheWaitIsDroppedNotStoredAsTheOldMachine(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	port1 := p.h.eps[1].URL
	p.h.eps[1].Priority = 0 // port 1 would take the work
	markEndpointFailure(port1)
	after(t, 30*time.Millisecond, func() { p.h.setMachineID(port1, catwomanMachine) })
	after(t, 80*time.Millisecond, func() { clearCooldown(port1) })
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	requireAllOnPort0AsBat(t, p, rec, res, err)
	// The taken-over port's answer is not what forgets the old machine; port 0's
	// own answer is, and port 1 is on another machine's memory now.
	if endpointRemembered(batKey, p.h.eps[0].URL) {
		t.Errorf("machine bat answered the canary clip on its only remaining port and is still remembered as having failed it")
	}
}

func TestChangedHands_APortRefusedDuringTheWaitThenAnsweringWeakIsDroppedNotStoredAsTheOldMachine(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	port1 := p.h.eps[1].URL
	p.h.eps[1].Priority = 0
	markEndpointFailure(port1)
	after(t, 30*time.Millisecond, func() {
		noteProbeResult(port1, false) // the tunnel was down for one probe
		p.h.setMachineID(port1, "")   // another Mac's mlx worker, missed MACHINE_ID_WAIT_S
	})
	after(t, 80*time.Millisecond, func() { clearCooldown(port1) })
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	requireAllOnPort0AsBat(t, p, rec, res, err)
	// Nothing says port 1 is not still a port of the machine that failed the
	// clip, and it has not been heard from as that machine: bat stays remembered.
	if !endpointRemembered(batKey, p.h.eps[0].URL) {
		t.Errorf("machine bat was forgotten although its port 1 was not heard from as bat")
	}
}

func TestChangedHands_APortWhoseOwnReprobeWasRefusedThenAnsweredWeakIsDroppedNotStoredAsTheOldMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a probe timeout")
	}
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	port1 := p.h.eps[1].URL
	p.h.eps[1].Priority = 0
	host := strings.TrimPrefix(port1, "http://")
	markEndpointFailure(port1)
	after(t, 20*time.Millisecond, func() {
		p.h.delayHealth(port1, 10*time.Second) // unreachable: every probe times out
		p.h.setMachineID(port1, "")
		clearCooldown(port1)
	})
	after(t, 4500*time.Millisecond, func() { p.h.healthDelay.Delete(host) })
	rec := &canaryRecorder{}
	res, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, rec.fn, 8)
	requireAllOnPort0AsBat(t, p, rec, res, err)
}

// A timeout the new worker gave is not evidence about the machine that used to
// be behind the port: the file's row would carry m:bat twice over one cat.
func TestChangedHands_ATimeoutStrikeOfAMachineThatTookOverAPortIsNotStoredAsTheOldMachine(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	var p *faultPool
	p = machineFaultPool(t, []string{batMachine, batMachine}, func(url, id string) BatchResult {
		if url == p.h.eps[1].URL && id == "chap3" {
			return BatchResult{Error: timeoutText}
		}
		return BatchResult{Text: "T:" + id}
	})
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	markCanaryFailed(batKey)
	port1 := p.h.eps[1].URL
	p.h.eps[1].Priority = 0
	markEndpointFailure(port1)
	after(t, 30*time.Millisecond, func() { p.h.setMachineID(port1, catwomanMachine) })
	after(t, 80*time.Millisecond, func() { clearCooldown(port1) })
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	requireAllOnPort0AsBat(t, p, rec, res, err)
	requireNoStrikes(t, res)
}

// The lone endpoint: its one port changed hands during the cooldown wait, so
// there is nobody left to send to. The jobs are deferred with an error that is
// neither a verdict nor a network failure, and nothing was sent or asked.
func TestChangedHands_ALoneEndpointTakenOverDuringTheWaitDefersTheJobsAndSendsNothing(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 1)
	port0 := p.h.eps[0].URL
	markEndpointFailure(port0)
	after(t, 30*time.Millisecond, func() { p.h.setMachineID(port0, catwomanMachine) })
	after(t, 80*time.Millisecond, func() { clearCooldown(port0) })
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if !errors.Is(err, errPortChangedHands) {
		t.Fatalf("err = %v, want one that wraps errPortChangedHands", err)
	}
	if te, ok := errors.AsType[*TransportError](err); !ok || te.Recognized {
		t.Errorf("err = %#v, want an unrecognised transport error, which the ops defer on without counting toward stopping the run", err)
	}
	if IsWorkerStuck(err) || errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("err = %v, want neither a stuck worker nor an unavailable pool", err)
	}
	if len(res) != 0 {
		t.Errorf("results = %v, want none: the worker behind the port is not the machine that was waited for", res)
	}
	if n := len(p.sendLog()); n != 0 {
		t.Errorf("%d requests sent to the port that changed hands", n)
	}
	if rec.count() != 0 {
		t.Errorf("the old machine's canary clip was sent to the port that changed hands: %v", rec.asked)
	}
	if !endpointRemembered(batKey, port0) {
		t.Errorf("a canary answer of another worker forgot machine bat")
	}
}

// The reverse direction is safe and keeps working: a worker that missed its
// machine id when the dispatch began and reports one after the wait is stored
// under the identity the dispatch knew it by, with the machine it reports
// beside it, so the store can still learn the machine (learnsMachine).
func TestChangedHands_AWorkerThatGainedItsMachineIdDuringTheWaitIsStoredWithBothIdentities(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	// Both workers missed their machine id and both are remembered as having
	// failed the clip, so nothing can stand in and the dispatch waits for the
	// first cooldown to end. That worker (port 1) reports a machine id by then.
	p := machineFaultPool(t, []string{"", ""}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	weak := "u:" + shortHash(port1)
	markCanaryFailed("u:"+shortHash(port0), port0)
	markCanaryFailed(weak, port1)
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	after(t, 30*time.Millisecond, func() {
		p.h.setMachineID(port1, catwomanMachine)
		clearCooldown(port1)
	})
	after(t, 80*time.Millisecond, func() { clearCooldown(port0) })
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := joinWorkers([]string{weak, catKey})
	var onPort1 int
	for id, r := range res {
		if r.Endpoint == port1 {
			onPort1++
			if r.Worker != want {
				t.Errorf("%s answered by the worker that gained its id is stored as %q, want %q", id, r.Worker, want)
			}
		} else if r.Endpoint == port0 && r.Worker != "u:"+shortHash(port0) {
			t.Errorf("%s answered by port 0 is stored as %q, want its own weak identity", id, r.Worker)
		}
	}
	if onPort1 == 0 {
		t.Errorf("no job ran on port 1, which stays in the dispatch when its worker only gained an id")
	}
}

// reprobed decides, for one endpoint, whether a probe after a wait is the worker
// the dispatch knew. It replaces the old rule that always carried the frozen
// identity over whatever answered (N2 of the D58 review), which stored another
// machine's answers as the old one's.
func TestReprobed_JudgesAFreshProbeAgainstTheIdentityTheDispatchFroze(t *testing.T) {
	const url = "http://gotham-tunnel:8000"
	weak := "u:" + shortHash(url)
	probe := func(backend, machineID string) gatedEndpoint {
		g := gatedEndpoint{Endpoint: Endpoint{URL: url}, Probed: true, Health: remoteHealth{Backend: backend, MachineID: machineID}}
		g.resolved = g.machineKey() // the gate's resolution, with no URL link known
		return g
	}
	unprobed := gatedEndpoint{Endpoint: Endpoint{URL: url}}
	unprobed.resolved = unprobed.machineKey()
	for _, tc := range []struct {
		name        string
		fresh       gatedEndpoint
		key         string
		capable     bool
		wantKeep    bool
		wantStored  string
		wantLearned string
	}{
		{"the same machine", probe(backendMLX, batMachine), batKey, true, true, batKey, ""},
		{"another machine took the port", probe(backendMLX, catwomanMachine), batKey, true, false, "", ""},
		{"a weak answer where a machine was known", probe(backendMLX, ""), batKey, true, false, "", ""},
		{"a worker that cannot report an id took the port", probe("faster-whisper", ""), batKey, true, false, "", ""},
		{"the same weak worker", probe(backendMLX, ""), weak, true, true, weak, ""},
		{"a worker that cannot report an id behind a weak id-capable one", probe("faster-whisper", ""), weak, true, false, "", ""},
		{"an id-capable worker behind a weak one that could not report an id", probe(backendMLX, ""), weak, false, false, "", ""},
		{"a weak worker that gained its machine id", probe(backendMLX, catwomanMachine), weak, true, true, weak, catKey},
		{"a worker that cannot report an id gained an id (another program)", probe(backendMLX, catwomanMachine), weak, false, false, "", ""},
		{"a frozen identity of no machine takes what answers", probe(backendMLX, catwomanMachine), "?" + shortHash(url), false, true, catKey, ""},
		{"no frozen identity takes what answers", probe(backendMLX, catwomanMachine), "", false, true, catKey, ""},
		{"a probe that read nothing carries the frozen identity", unprobed, batKey, true, true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, keep := tc.fresh.reprobed(tc.key, tc.capable)
			if keep != tc.wantKeep {
				t.Fatalf("keep = %v, want %v", keep, tc.wantKeep)
			}
			if !keep {
				return
			}
			if s := got.storedWorker(); s != tc.wantStored {
				t.Errorf("storedWorker = %q, want %q", s, tc.wantStored)
			}
			if got.learned != tc.wantLearned {
				t.Errorf("learned = %q, want %q", got.learned, tc.wantLearned)
			}
		})
	}
}

// S-1 of review 6 of #4201: machine bat struck the file. Its port 0 was refused
// by a probe while the worker restarted, and answers as a weak "u:" now. Its key
// is the address, which names no machine, so the avoid set {m:bat} no longer
// matched it and the job went back to the machine that already struck it.
func TestSteer_ARestartedPortOfAStruckMachineIsStillSteeredAwayFrom(t *testing.T) {
	h := machineHarness(t, []string{"", robinMachine})
	h.eps[1].Priority = 50
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	noteCanaryPort(batKey, h.eps[0].URL, true) // port 0 is bat's
	refuseProbe(h.eps[0].URL)                  // bat's worker restarted; a probe caught it
	res, err := dispatchPool(testCtx(t), h.eps, nil, map[string]string{"j": "x"}, poolOptions{avoid: map[string]string{"j": batKey}}, h.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := res["j"]; got.Endpoint != h.eps[1].URL {
		t.Errorf("the job struck by %s went to %s (worker %q, port 0 is attributed to %q), want robin's port",
			batKey, got.Endpoint, got.Worker, attributedMachine(h.eps[0].URL))
	}
}

// The over-steering boundary: when no other machine can take the job it stays on
// the restarted port instead of being stranded.
func TestSteer_ARestartedPortOfAStruckMachineKeepsTheJobWhenNothingElseCanTakeIt(t *testing.T) {
	h := machineHarness(t, []string{""})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	noteCanaryPort(batKey, h.eps[0].URL, true)
	refuseProbe(h.eps[0].URL)
	res, err := dispatchPool(testCtx(t), h.eps, nil, map[string]string{"j": "x"}, poolOptions{avoid: map[string]string{"j": batKey}}, h.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := res["j"]; got.Endpoint != h.eps[0].URL {
		t.Errorf("the job went to %s, want the only port there is", got.Endpoint)
	}
}

// Control: with no refusal in between, the link of the URL to its machine
// names the weak answer, and the job is steered off the port as before.
func TestSteer_AWeakAnswerOfAStruckMachinesPortWithoutARefusalIsSteeredAwayFrom(t *testing.T) {
	h := machineHarness(t, []string{"", robinMachine})
	h.eps[1].Priority = 50
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	noteCanaryPort(batKey, h.eps[0].URL, true)
	res, err := dispatchPool(testCtx(t), h.eps, nil, map[string]string{"j": "x"}, poolOptions{avoid: map[string]string{"j": batKey}}, h.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := res["j"]; got.Endpoint != h.eps[1].URL {
		t.Errorf("control: job went to %s worker=%q, want robin's port", got.Endpoint, got.Worker)
	}
}
