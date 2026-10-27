// file: internal/transcribe/canary_changed_hands_test.go
// version: 1.0.1
// guid: 5d0c7e2a-83b1-4f6e-9a47-2c1e6b0d8f35
// last-edited: 2026-10-27

package transcribe

import (
	"errors"
	"maps"
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
	// The probe timeout is injected (it is 3 s in production), so this runs in
	// well under a second and under -short like the rest.
	shortenHealthProbe(t, 60*time.Millisecond)
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
	after(t, 400*time.Millisecond, func() { p.h.healthDelay.Delete(host) })
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

// shortenHealthProbe makes a /health probe that gets no answer give up after d
// instead of healthProbeTimeout.
func shortenHealthProbe(t *testing.T, d time.Duration) {
	t.Helper()
	old := healthProbeTimeout
	healthProbeTimeout = d
	t.Cleanup(func() { healthProbeTimeout = old })
}

// rememberedPorts is the ports canaryFailed lists for key, sorted, and whether
// key is remembered at all.
func rememberedPorts(key string) (bool, []string) {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	e := canaryFailed.m[key]
	if e == nil {
		return false, nil
	}
	return true, slices.Sorted(maps.Keys(e.ports))
}

// poolWhoseBatPortGainedAnotherMachinesId is review 7 of #4201. Port 1 was bat's
// only port. Its tunnel dropped (a refused probe ends the link of the address to
// its machine), then a worker answered there as a weak "u:" (an id-capable mlx
// worker that missed MACHINE_ID_WAIT_S), so the dispatch freezes it as "u:"
// while bat's port set still groups it under bat. While the dispatch waits out
// port 1's cooldown the worker reports a machine id, and it is catwoman's.
// reprobed keeps such a worker (a weak worker that gained its id), so only the
// group tells that this is not bat.
//
// Port 0 is robin's, and its /health does not answer when the dispatch begins,
// so it cannot stand in for bat (a stand-in is an endpoint whose health was read)
// and the dispatch waits for bat; once port 1 is dropped it takes the work.
func poolWhoseBatPortGainedAnotherMachinesId(t *testing.T) *faultPool {
	t.Helper()
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	shortenHealthProbe(t, 60*time.Millisecond)
	p := machineFaultPool(t, []string{robinMachine, ""}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.eps[1].Priority = 0
	noteCanaryPort(batKey, port1, true)
	markCanaryFailed(batKey, port1)
	refuseProbe(port1)
	p.h.delayHealth(port0, time.Minute)
	clearCooldown(port1) // no earlier failure count
	markEndpointFailure(port1)
	after(t, 150*time.Millisecond, func() { p.h.setMachineID(port1, catwomanMachine) })
	after(t, 220*time.Millisecond, func() { clearCooldown(port1) })
	return p
}

// requireGainedIdPortDropped is what the dispatch of
// poolWhoseBatPortGainedAnotherMachinesId must end as, whatever the canary says:
// robin (port 0) does all the work, the worker that is catwoman's now is asked
// nothing and sent nothing, bat, whose only port it was, is still remembered
// (nobody has answered for it) and catwoman, who was never asked, is not.
func requireGainedIdPortDropped(t *testing.T, p *faultPool, rec *canaryRecorder, res map[string]BatchResult, err error) {
	t.Helper()
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	if err != nil {
		t.Fatalf("err = %v, want the dispatch to go on with robin", err)
	}
	if len(res) != 8 {
		t.Fatalf("%d results, want 8", len(res))
	}
	for id, r := range res {
		if r.Endpoint != port0 {
			t.Errorf("%s ran on %s (worker %q), want robin's port 0", id, r.Endpoint, r.Worker)
		}
	}
	if slices.Contains(rec.asked, port1) {
		t.Errorf("bat's canary clip was sent to port 1, which reports %s after the wait: %v", catKey, rec.asked)
	}
	for _, s := range p.sendLog() {
		if s.url == port1 {
			t.Errorf("port 1, which is catwoman's now, was sent %v in a dispatch that waited for bat", s.ids)
		}
	}
	if ok, ports := rememberedPorts(batKey); !ok {
		t.Errorf("bat was forgotten on an answer of catwoman's worker")
	} else if slices.Contains(ports, port1) {
		t.Errorf("bat's entry lists port 1, which is %s's now: %v", catKey, ports)
	}
	if ok, ports := rememberedPorts(catKey); ok {
		t.Errorf("catwoman is remembered as having failed the canary clip (ports %v), but she was never asked it", ports)
	}
}

func TestChangedHands_APortGroupedUnderOneMachineThatGainsAnotherMachinesIdIsDroppedNotAskedTheOldMachinesCanary(t *testing.T) {
	p := poolWhoseBatPortGainedAnotherMachinesId(t)
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	requireGainedIdPortDropped(t, p, rec, res, err)
}

// A canary the old machine would have failed is not recorded against the machine
// that took its port: it used to be recorded as bat failing, with cat's port in
// bat's list, and cat was never remembered.
func TestChangedHands_ACanaryTheOldMachineWouldHaveFailedIsNotRecordedAgainstTheMachineThatTookItsPort(t *testing.T) {
	p := poolWhoseBatPortGainedAnotherMachinesId(t)
	rec := &canaryRecorder{err: errors.New("canary transcript wrong")}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	requireGainedIdPortDropped(t, p, rec, res, err)
}

// One dispatch later the port is simply catwoman's: she never failed a canary
// clip (she was not asked one), so she is sent work as herself, and bat's clip is
// still not sent to her. Before the fix she had been credited with a failure she
// was never asked about, recorded under bat, and was sent work with nothing asked.
func TestChangedHands_TheNextDispatchAfterAGainedIdPortWasDroppedKnowsItAsTheNewMachineAndAsksItNothingOfTheOldOne(t *testing.T) {
	p := poolWhoseBatPortGainedAnotherMachinesId(t)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	rec := &canaryRecorder{err: errors.New("canary transcript wrong")}
	res1, err1 := dispatchWith(testCtx(t), p, rec.fn, 8)
	requireGainedIdPortDropped(t, p, rec, res1, err1)
	clearCooldown(port0)
	clearCooldown(port1)
	rec2 := &canaryRecorder{err: errors.New("canary transcript wrong")}
	res, err := dispatchWith(testCtx(t), p, rec2.fn, 4)
	if err != nil {
		t.Fatalf("dispatch 2: err = %v", err)
	}
	if slices.Contains(rec2.asked, port1) {
		t.Errorf("dispatch 2 sent a canary clip to port 1, which is %s's: %v", catKey, rec2.asked)
	}
	var onPort1 int
	for id, r := range res {
		if r.Endpoint == port1 {
			onPort1++
			if r.Worker != catKey {
				t.Errorf("%s answered by port 1 is stored as %q, want %q", id, r.Worker, catKey)
			}
		}
	}
	if onPort1 == 0 {
		t.Errorf("no job ran on port 1: a worker that never failed anything is not benched on the old machine's record")
	}
	if ok, _ := rememberedPorts(catKey); ok {
		t.Errorf("catwoman is remembered as having failed the canary clip")
	}
}

// The lone endpoint has no group that can disagree with what it reports, so a
// worker that gained its id is kept and asked, as before. A guard: the pool path
// above drops such a port, and the lone path must not.
func TestChangedHands_ALoneEndpointWhoseWorkerGainedAnIdIsKeptAndAskedItsOwnCanary(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "canary passes", true: "canary fails"}[fail], func(t *testing.T) {
			shortenPacing(t, 10*time.Millisecond, time.Minute)
			p := machineFaultPool(t, []string{""}, answerWithTranscripts)
			port := p.h.eps[0].URL
			t.Cleanup(func() { clearCooldown(port) })
			noteCanaryPort(batKey, port, true)
			markCanaryFailed(batKey, port)
			refuseProbe(port)
			markEndpointFailure(port)
			after(t, 30*time.Millisecond, func() { p.h.setMachineID(port, catwomanMachine) })
			after(t, 80*time.Millisecond, func() { clearCooldown(port) })
			rec := &canaryRecorder{}
			if fail {
				rec.err = errors.New("canary transcript wrong")
			}
			res, err := dispatchWith(testCtx(t), p, rec.fn, 4)
			if rec.count() != 1 {
				t.Errorf("%d canary clips sent, want 1 to the only endpoint", rec.count())
			}
			if fail {
				if err == nil || len(res) != 0 {
					t.Errorf("err = %v, %d results, want the failed canary to send it no work", err, len(res))
				}
				if ok, _ := rememberedPorts(catKey); !ok {
					t.Errorf("the worker that failed the canary clip as %s is not remembered", catKey)
				}
				return
			}
			if err != nil || len(res) != 4 {
				t.Fatalf("err = %v, %d results, want the 4 jobs answered", err, len(res))
			}
			if ok, _ := rememberedPorts(batKey); !ok {
				t.Errorf("a canary answer of the worker that reports %s forgot machine bat", catKey)
			}
			want := joinWorkers([]string{"u:" + shortHash(port), catKey})
			for id, r := range res {
				if r.Worker != want {
					t.Errorf("%s is stored as %q, want the identity the dispatch knew it by and %s: %q", id, r.Worker, catKey, want)
				}
			}
		})
	}
}

// Every port of the pool changed hands during the wait: nothing is left to send
// to. The dispatch defers its jobs as the lone endpoint does (errPortChangedHands,
// an unrecognised transport error the ops defer on without counting), and does
// not call it a capability problem, which sends an operator hunting for a box
// that is not on a GPU.
func TestChangedHands_APoolWhoseEveryPortChangedHandsDefersTheJobsAsChangedHandsNotAsACapabilityRefusal(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	for _, ep := range p.h.eps {
		markEndpointFailure(ep.URL)
	}
	after(t, 30*time.Millisecond, func() {
		for _, ep := range p.h.eps {
			p.h.setMachineID(ep.URL, catwomanMachine)
		}
	})
	after(t, 80*time.Millisecond, func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 4)
	if !errors.Is(err, errPortChangedHands) {
		t.Fatalf("err = %v, want one that wraps errPortChangedHands", err)
	}
	if strings.Contains(err.Error(), "capability requirements") {
		t.Errorf("err = %v, want no mention of capability requirements", err)
	}
	if te, ok := errors.AsType[*TransportError](err); !ok || te.Recognized {
		t.Errorf("err = %#v, want an unrecognised transport error, which the ops defer on without counting toward stopping the run", err)
	}
	if IsWorkerStuck(err) || IsPoolUnavailable(err) {
		t.Errorf("err = %v, want neither a stuck worker nor an unavailable pool", err)
	}
	if len(res) != 0 || len(p.sendLog()) != 0 || rec.count() != 0 {
		t.Errorf("%d results, %d requests, %d canary clips, want none of them", len(res), len(p.sendLog()), rec.count())
	}
	if !endpointRemembered(batKey, p.h.eps[0].URL) {
		t.Errorf("a canary answer of another worker forgot machine bat")
	}
}

// Only a pool whose every refusal is a port that changed hands ends as one: with
// any other refusal beside them the capability error stands.
func TestChangedHands_OnlyARefusalOfEveryPortAsChangedHandsEndsTheDispatchAsChangedHands(t *testing.T) {
	hands := gpuRefusal{URL: "http://192.0.2.1:8000", ChangedHands: true}
	stuck := gpuRefusal{URL: "http://192.0.2.2:8000", Stuck: true}
	for _, tc := range []struct {
		name    string
		refused []gpuRefusal
		want    bool
	}{
		{"none", nil, false},
		{"one", []gpuRefusal{hands}, true},
		{"two", []gpuRefusal{hands, hands}, true},
		{"beside a stuck one", []gpuRefusal{hands, stuck}, false},
		{"beside a capability refusal", []gpuRefusal{hands, {URL: "http://192.0.2.3:8000"}}, false},
	} {
		if got := allChangedHands(tc.refused); got != tc.want {
			t.Errorf("%s: allChangedHands = %v, want %v", tc.name, got, tc.want)
		}
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
