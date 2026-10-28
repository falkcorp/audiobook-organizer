// file: internal/transcribe/canary_changed_hands_test.go
// version: 1.0.4
// guid: 5d0c7e2a-83b1-4f6e-9a47-2c1e6b0d8f35
// last-edited: 2026-10-28

package transcribe

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
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
// only port and reports bat's id when the dispatch begins, so bat is one group
// and the dispatch waits out port 1's cooldown to ask it. While it waits, the
// worker there reports another machine's id, and it is catwoman's: the port
// changed hands, so it is dropped from the group and asked nothing of bat's.
//
// (This pool used to freeze port 1 as a weak "u:" that bat's port set still
// grouped under bat. A weak port is no longer a member of a remembered machine's
// group, however its URL is listed: only a port that reports the machine's own id
// is asked its clip or clears its failure, TestRememberedMachine_AWeakPortListedAmongItsPortsIsNotAskedItsCanaryAndDoesNotClearIt)
//
// Port 0 is robin's, and its /health does not answer when the dispatch begins,
// so it cannot stand in for bat (a stand-in is an endpoint whose health was read)
// and the dispatch waits for bat; once port 1 is dropped it takes the work.
func poolWhoseBatPortGainedAnotherMachinesId(t *testing.T) *faultPool {
	t.Helper()
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	// The start gate waits port 0's probe out (healthProbeTimeout), and the id flip
	// must land after that gate has finished, or the dispatch would freeze port 1
	// under the id it flips to. The flip and the release are placed a few probe
	// lengths after it, so a loaded -race agent that is slow to start the gate
	// cannot reorder them.
	shortenHealthProbe(t, 250*time.Millisecond)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.eps[1].Priority = 0
	noteCanaryPort(batKey, port1, true)
	markCanaryFailed(batKey, port1)
	p.h.delayHealth(port0, time.Minute)
	clearCooldown(port1) // no earlier failure count
	markEndpointFailure(port1)
	after(t, 700*time.Millisecond, func() { p.h.setMachineID(port1, catwomanMachine) })
	after(t, 800*time.Millisecond, func() { clearCooldown(port1) })
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
// still not sent to her. What this dispatch discriminates is the memory the first
// one left behind: had it credited her with bat's failure, she would be remembered
// now, asked a clip (which fails here) and benched, and sent nothing.
//
// Robin's port answers /health again, so this is a pool of two. While it did not
// (the first dispatch), the second would have been a lone endpoint, which
// resolves its memory at call time and passes whatever the first dispatch left.
func TestChangedHands_TheNextDispatchAfterAGainedIdPortWasDroppedKnowsItAsTheNewMachineAndAsksItNothingOfTheOldOne(t *testing.T) {
	p := poolWhoseBatPortGainedAnotherMachinesId(t)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	rec := &canaryRecorder{err: errors.New("canary transcript wrong")}
	res1, err1 := dispatchWith(testCtx(t), p, rec.fn, 8)
	requireGainedIdPortDropped(t, p, rec, res1, err1)
	p.h.healthDelay.Delete(strings.TrimPrefix(port0, "http://"))
	clearCooldown(port0)
	clearCooldown(port1)
	rec2 := &canaryRecorder{err: errors.New("canary transcript wrong")}
	res, err := dispatchWith(testCtx(t), p, rec2.fn, 4)
	if err != nil {
		t.Fatalf("dispatch 2: err = %v", err)
	}
	requireTranscripts(t, res, 4)
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

// A pool ends as changed hands when at least one of its refusals is a port that
// changed hands and every other refusal is only unreachable (Transient): nothing
// is wrong with the capabilities, and nothing is left to send to. Any capability
// refusal or stuck port beside them keeps the error that names it, and a pool of
// nothing but unreachable ports is the outage it always was (allTransient, which
// noEndpointError asks first).
func TestChangedHands_ARefusalOfEveryPortAsChangedHandsOrUnreachableWithOneChangedHandsEndsTheDispatchAsChangedHands(t *testing.T) {
	hands := gpuRefusal{URL: "http://192.0.2.1:8000", ChangedHands: true}
	stuck := gpuRefusal{URL: "http://192.0.2.2:8000", Stuck: true}
	dead := gpuRefusal{URL: "http://192.0.2.4:8000", Transient: true}
	capability := gpuRefusal{URL: "http://192.0.2.3:8000"}
	for _, tc := range []struct {
		name    string
		refused []gpuRefusal
		want    bool
	}{
		{"none", nil, false},
		{"one", []gpuRefusal{hands}, true},
		{"two", []gpuRefusal{hands, hands}, true},
		{"beside an unreachable one", []gpuRefusal{hands, dead}, true},
		{"unreachable one first", []gpuRefusal{dead, hands, hands}, true},
		{"only unreachable ones", []gpuRefusal{dead, dead}, false},
		{"beside a stuck one", []gpuRefusal{hands, stuck}, false},
		{"beside a capability refusal", []gpuRefusal{hands, capability}, false},
		{"beside an unreachable and a capability refusal", []gpuRefusal{hands, dead, capability}, false},
	} {
		if got := changedHandsOrTransient(tc.refused); got != tc.want {
			t.Errorf("%s: changedHandsOrTransient = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Review 8 of #4201. A pool keeps a re-probed port only if its identity is the
// one the dispatch froze; a worker that gained a machine id during the wait
// (frozen "u:", reports "m:" now) is dropped like any other change, whatever the
// canary memory groups it under, because the dispatch cannot tell it from
// another machine that took the port and a kept port would be counted, benched
// and struck under two identities. The next dispatch's gate keys it by the id it
// reports.
func TestChangedHands_APoolPortWhoseWorkerGainedAMachineIdDuringTheWaitIsDroppedAndUsedNextDispatchUnderThatId(t *testing.T) {
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
	weak0, weak1 := "u:"+shortHash(port0), "u:"+shortHash(port1)
	markCanaryFailed(weak0, port0)
	markCanaryFailed(weak1, port1)
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
		t.Fatalf("dispatch 1: err = %v, want it to go on with the port that is still the worker it was", err)
	}
	requireTranscripts(t, res, 8)
	for id, r := range res {
		if r.Endpoint != port0 || r.Worker != weak0 {
			t.Errorf("dispatch 1: %s = endpoint %s worker %q, want an answer of port 0 stored as %q", id, r.Endpoint, r.Worker, weak0)
		}
	}
	for _, s := range p.sendLog() {
		if s.url == port1 {
			t.Errorf("dispatch 1: port 1, whose worker gained an id, was sent %v", s.ids)
		}
	}
	if slices.Contains(rec.asked, port1) {
		t.Errorf("dispatch 1: the canary clip was sent to port 1, whose worker is not the one that was waited for: %v", rec.asked)
	}

	// Dispatch 2: the gate keys port 1 by the id it reports, so it is catwoman, and
	// the memory of the weak identity its URL failed as moves to her (the machine
	// behind a URL does not change because its worker reports an id). She is asked
	// her own canary clip, which she passes, and is sent work stored as herself
	// alone: neither identity is joined to the other in a pool.
	p.h.eps[1].Priority = 0 // port 1 would take the work
	clearCooldown(port0)
	clearCooldown(port1)
	rec2 := &canaryRecorder{}
	res2, err := dispatchWith(testCtx(t), p, rec2.fn, 4)
	if err != nil {
		t.Fatalf("dispatch 2: err = %v", err)
	}
	requireTranscripts(t, res2, 4)
	var onPort1 int
	for id, r := range res2 {
		if r.Endpoint == port1 {
			onPort1++
			if r.Worker != catKey {
				t.Errorf("dispatch 2: %s answered by port 1 is stored as %q, want %q alone", id, r.Worker, catKey)
			}
		}
	}
	if onPort1 == 0 {
		t.Errorf("dispatch 2: no job ran on port 1, which reports %s and passed its canary clip", catKey)
	}
	if !slices.Equal(rec2.asked, []string{port1}) {
		t.Errorf("dispatch 2: canary clips asked of %v, want port 1 only, once", rec2.asked)
	}
	if ok, _ := rememberedPorts(catKey); ok {
		t.Errorf("dispatch 2: %s passed her canary clip and is still remembered", catKey)
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

// perPortCanary is a canaryFunc that fails the clip on the listed ports and
// passes elsewhere, recording every port it was asked.
type perPortCanary struct {
	mu    sync.Mutex
	asked []string
	fail  map[string]bool
}

func (c *perPortCanary) fn(_ context.Context, g gatedEndpoint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, g.Endpoint.URL)
	if c.fail[g.Endpoint.URL] {
		return errors.New("canary transcript wrong")
	}
	return nil
}

// poolWhoseWeakPortLearnsASiblingMachine is review 8 of #4201. Port 0 is robin's
// and failed the canary clip earlier; port 1 is grouped under its own weak entry
// ("u:" + its address: its worker failed a clip as itself, and an earlier refusal
// ended its link to any machine). While the dispatch waits out the cooldowns
// port 1's worker reports robin's machine id, a machine that is ALSO in this
// dispatch, on port 0. Robin fails the canary clip on port 0 in this very
// dispatch (failPort0), so port 1 must not take work as robin or as anyone else.
func poolWhoseWeakPortLearnsASiblingMachine(t *testing.T, answer func(url, id string) BatchResult) *faultPool {
	t.Helper()
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, ""}, answer)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	refuseProbe(port1)                             // no URL-to-machine link for port 1
	markCanaryFailed("u:"+shortHash(port1), port1) // a weak worker at port 1 failed a clip as itself
	markCanaryFailed(robinKey, port0)
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	after(t, 30*time.Millisecond, func() { p.h.setMachineID(port1, robinMachine) })
	after(t, 80*time.Millisecond, func() { clearCooldown(port0); clearCooldown(port1) })
	return p
}

// Port 1 reports robin after the wait, robin is benched on port 0 for failing the
// clip, and port 1 used to escape the bench (it was grouped under its own weak
// entry, so robin's bench did not cover it) and take every job as robin.
func TestChangedHands_AWeakPortThatLearnsAMachineWhoFailsTheCanaryOnItsSiblingPortIsSentNoWork(t *testing.T) {
	p := poolWhoseWeakPortLearnsASiblingMachine(t, answerWithTranscripts)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	c := &perPortCanary{fail: map[string]bool{port0: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, c.fn, 8)
	t.Logf("err = %v, canary asked %v, %d results", err, c.asked, len(res))
	for id, r := range res {
		if r.Endpoint == port1 {
			t.Errorf("%s ran on port 1 (stored as %q), which reports %s, the machine that failed the canary clip on port 0 in this dispatch", id, r.Worker, robinKey)
		}
	}
	for _, s := range p.sendLog() {
		if s.url == port1 {
			t.Errorf("port 1 was sent %v in a dispatch where %s failed the canary clip", s.ids, robinKey)
		}
	}
	if slices.Contains(c.asked, port1) {
		t.Errorf("port 1, dropped as another worker, was asked a canary clip: %v", c.asked)
	}
	if !endpointRemembered(robinKey, port0) {
		t.Errorf("%s failed the canary clip on port 0 and is not remembered", robinKey)
	}
}

// The same pool, with port 1 answering every request with a decoder timeout: its
// strikes must not be written under robin, who is benched on port 0 for failing
// the clip (a strike names "m:" robin + the weak identity), because a file struck
// that way has a machine's strike it never earned. Port 1 is sent no work, so it
// writes none.
func TestChangedHands_AWeakPortThatLearnsAMachineWhoFailsTheCanaryOnItsSiblingPortWritesNoTimeoutStrikes(t *testing.T) {
	var p *faultPool
	p = poolWhoseWeakPortLearnsASiblingMachine(t, func(url, id string) BatchResult {
		if url == p.h.eps[1].URL {
			return BatchResult{Error: timeoutText}
		}
		return BatchResult{Text: "T:" + id}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	c := &perPortCanary{fail: map[string]bool{port0: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, c.fn, 2)
	t.Logf("err = %v, canary asked %v, %d results", err, c.asked, len(res))
	for id, r := range res {
		if r.Endpoint == port1 || IsStrikeError(r.Error) || strings.Contains(r.Worker, robinKey) {
			t.Errorf("%s = endpoint %s worker %q strike=%v condemned=%v err=%q, want no strike, and nothing from port 1", id, r.Endpoint, r.Worker, IsStrikeError(r.Error), r.Condemned, r.Error)
		}
	}
	for _, s := range p.sendLog() {
		if s.url == port1 {
			t.Errorf("port 1 was sent %v in a dispatch where %s failed the canary clip", s.ids, robinKey)
		}
	}
}

// What the dropped port used to cost beyond the bench: countLimits counts
// machines by keyOf, so a port frozen as "u:" that reports robin made robin two
// machines, and a lone job that stuck on port 0 and on port 1 (both robin) was
// condemned by "two machines", the second of them the same box. With the port
// dropped at the re-probe the dispatch has one machine, so the condemnation names
// it once and never the weak identity of port 1, and port 1 is never tried.
func TestChangedHands_AMachineThatAWeakPortLearnedIsNeverCountedAsASecondMachineForACondemnation(t *testing.T) {
	p := poolWhoseWeakPortLearnsASiblingMachine(t, func(url, id string) BatchResult {
		if id == "chap1" {
			return BatchResult{Error: timeoutText}
		}
		return BatchResult{Text: "T:" + id}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	weak1 := "u:" + shortHash(port1)
	p.h.eps[1].Priority = 0 // port 1 would take the work first, if it were kept
	c := &perPortCanary{}   // robin passes the clip on port 0 here
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, c.fn, 4)
	t.Logf("err = %v, canary asked %v, %d results", err, c.asked, len(res))
	for _, s := range p.sendLog() {
		if s.url == port1 {
			t.Errorf("port 1 was sent %v: its worker is robin's machine, already on port 0, and is dropped from the dispatch", s.ids)
		}
	}
	for id, r := range res {
		if r.Endpoint == port1 || strings.Contains(r.Worker, weak1) || strings.Contains(r.Endpoint, port1) {
			t.Errorf("%s = endpoint %s worker %q: port 1 took part in the verdict of a dispatch that dropped it", id, r.Endpoint, r.Worker)
		}
		if r.Endpoint == port0 && r.Worker != robinKey {
			t.Errorf("%s answered by port 0 is stored as %q, want %s alone", id, r.Worker, robinKey)
		}
	}
}

// A pool with one unreachable port (a dead tunnel: Transient) whose every other
// port changed hands during the wait has nothing left to send to, and no
// capability problem to report: it ends as changed hands, deferred without
// counting, not as "refused by capability requirements" (which sends an operator
// hunting for a box that is not on a GPU).
func TestChangedHands_APoolWithOneUnreachablePortWhoseOtherPortsAllChangedHandsDefersAsChangedHandsNotAsACapabilityRefusal(t *testing.T) {
	// The start gate waits the dead port's probe out (healthProbeTimeout); the ids
	// flip a few probe lengths after it, on the same footing as
	// poolWhoseBatPortGainedAnotherMachinesId.
	shortenHealthProbe(t, 250*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 3)
	dead := p.h.eps[2].URL
	p.h.delayHealth(dead, time.Minute)
	live := p.h.eps[:2]
	for _, ep := range live {
		markEndpointFailure(ep.URL)
	}
	after(t, 700*time.Millisecond, func() {
		for _, ep := range live {
			p.h.setMachineID(ep.URL, catwomanMachine)
		}
	})
	after(t, 800*time.Millisecond, func() {
		for _, ep := range live {
			clearCooldown(ep.URL)
		}
	})
	rec := &canaryRecorder{}
	res, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, rec.fn, 4)
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
}
