// file: internal/transcribe/canary_cooldown_probe_test.go
// version: 1.1.1
// guid: 2c6c9f65-2b2c-4271-8a11-7648ef7949d8
// last-edited: 2026-10-28

package transcribe

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
)

func clearCooldownsAtEnd(t *testing.T, p *faultPool) {
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
}

func healthHitsFor(p *faultPool, url string) int {
	p.h.healthMu.Lock()
	defer p.h.healthMu.Unlock()
	return p.h.healthHits[strings.TrimPrefix(url, "http://")]
}

// runCanaries asks a port that faulted in an EARLIER round of the dispatch and
// whose cooldown ended during a later round: it was seen in the cooldown, so it
// is probed again before the clip is sent (D60, D62). Robin port 0 faults (timeout
// majority) in round 1 and is in cooldown. In round 2 bat port 1 faults
// worker_misconfigured, so every endpoint is sick and runCanaries runs; while
// round 2 ran, port 0's cooldown ended and its tunnel came back from catwoman.
// Without the probe the clip is sent to catwoman and the verdict is stored
// under robin.
func TestRunCanaries_AProbeComesBeforeTheClipOfAPortWhoseCooldownEndedInALaterRound(t *testing.T) {
	for _, canaryFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "canary passes", true: "canary fails"}[canaryFails], func(t *testing.T) {
			freshCanaryMemory(t)
			shortenPacing(t, 10*time.Millisecond, 5*time.Second)
			var port0, port1 string
			var mu sync.Mutex
			round1 := map[string]bool{}
			var switched atomic.Bool
			var hitsAtSwitch atomic.Int64
			answer := func(url, id string) BatchResult {
				if url == port0 && !switched.Load() {
					return BatchResult{Error: timeoutText}
				}
				if url == port1 && switched.Load() {
					return BatchResult{Error: PathErrWorkerMisconfigured + pathErrSeparator + "no decoder"}
				}
				return BatchResult{Text: "T:" + id}
			}
			p := machineFaultPool(t, []string{robinMachine, batMachine}, answer)
			clearCooldownsAtEnd(t, p)
			port0, port1 = p.h.eps[0].URL, p.h.eps[1].URL
			send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
				if g.Endpoint.URL == port0 && !switched.Load() {
					mu.Lock()
					for id := range sub {
						round1[id] = true
					}
					mu.Unlock()
					// port0's own send: do not clear others' cooldowns
					res := map[string]BatchResult{}
					for id := range sub {
						res[id] = answer(port0, id)
					}
					kept, fault := splitWorkerFault(port0, res)
					// A timeout-majority fault does not put the port in cooldown;
					// another dispatch sharing the endpoint (the book op) does.
					markEndpointFailure(port0)
					if fault != nil {
						return kept, fault
					}
					return kept, nil
				}
				if g.Endpoint.URL == port1 {
					mu.Lock()
					later := false
					for id := range sub {
						if round1[id] {
							later = true
						}
					}
					mu.Unlock()
					if later && !switched.Load() {
						if !endpointInCooldown(port0) {
							t.Errorf("setup: port 0 should be in cooldown when round 2 starts")
						}
						p.h.setMachineID(port0, catwomanMachine)
						clearCooldown(port0)
						hitsAtSwitch.Store(int64(healthHitsFor(p, port0)))
						switched.Store(true)
						res := map[string]BatchResult{}
						for id := range sub {
							res[id] = answer(port1, id)
						}
						kept, fault := splitWorkerFault(port1, res)
						return kept, fault
					}
				}
				res := map[string]BatchResult{}
				for id := range sub {
					res[id] = answer(g.Endpoint.URL, id)
				}
				kept, fault := splitWorkerFault(g.Endpoint.URL, res)
				if fault != nil {
					return kept, fault
				}
				return kept, nil
			}
			c := &urlCanary{fail: map[string]bool{}}
			if canaryFails {
				c.fail[port0] = true
			}
			jobs := map[string]string{}
			for _, id := range loopers(16, "chap") {
				jobs[id] = "u"
			}
			var hitsAtCanary atomic.Int64
			hitsAtCanary.Store(-1)
			cfn := func(ctx context.Context, g gatedEndpoint) error {
				if g.Endpoint.URL == port0 {
					hitsAtCanary.Store(int64(healthHitsFor(p, port0)))
				}
				return c.fn(ctx, g)
			}
			res, err := dispatchPool(testCtx(t), p.h.eps, nil, jobs, poolOptions{canary: cfn}, send)
			if !switched.Load() {
				t.Fatalf("setup did not reach round 2 on port 1")
			}
			if hitsAtCanary.Load() != -1 {
				t.Errorf("the canary clip was sent to port 0 (robin at the gate, catwoman since its cooldown ended in round 2) after %d /health request(s), %d of them before the switch: it is probed again first, and then dropped", hitsAtCanary.Load(), hitsAtSwitch.Load())
			}
			okR, portsR := rememberedPorts(robinKey)
			t.Logf("switched=%v err=%v asked=%v healthHits(port0) at switch=%d end=%d robinRemembered=%v ports=%v res=%d",
				switched.Load(), err, c.askedPorts(), hitsAtSwitch.Load(), healthHitsFor(p, port0), okR, portsR, len(res))
			if got := healthHitsFor(p, port0); got <= int(hitsAtSwitch.Load()) {
				t.Errorf("port 0 was not probed again after its cooldown ended and it changed hands: %d request(s), %d at the switch", got, hitsAtSwitch.Load())
			}
			if canaryFails && okR {
				t.Errorf("robin is remembered as having failed the canary on catwoman's answer: ports=%v", portsR)
			}
		})
	}
}

// The !placedAny path looks for a cooled port that is out of its cooldown, then
// asks pace how long to wait. A cooldown that ends between the two makes pace
// find nothing to wait for; the look is made again after it, or the dispatch ends
// ErrWorkerStuck while bat is usable (afterPlacedCheck is the moment between).
func TestPlacedAny_ACooldownThatEndsBetweenTheLookAndPaceIsNotGivenUpOn(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, 10*time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine, robinMachine}, answerWithTranscripts)
	clearCooldownsAtEnd(t, p)
	port0, port1, port2 := p.h.eps[0].URL, p.h.eps[1].URL, p.h.eps[2].URL
	markEndpointFailure(port1)
	var once sync.Once
	p.afterPlacedCheck = func() { once.Do(func() { clearCooldown(port1) }) }
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		if _, ok := sub["chap1"]; ok && (g.Endpoint.URL == port0 || g.Endpoint.URL == port2) {
			p.h.wedge(g.Endpoint.URL)
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Minute, Jobs: sortedIDs(sub)}
		}
		return p.send(ctx, g, sub, pf)
	}
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), send)
	t.Logf("err=%v stuck=%v chap1=%+v", err, IsWorkerStuck(err), res["chap1"])
	if err != nil || res["chap1"].Endpoint != port1 {
		t.Errorf("err=%v chap1=%+v: bat on port 1 was out of cooldown and could take the part", err, res["chap1"])
	}
}

// The look for a cooled port that is out of its cooldown must not livelock: a
// cooled port that comes out of cooldown during recheck's probe but cannot take
// the part (robin's sibling), plus an unknown port in cooldown. Every continue is
// followed by a probe that clears cooled. The dispatch ends ErrWorkerStuck (an
// untried unknown port), with no verdict, in bounded time, and does not wait for
// the "?" port.
func TestPlacedAny_TheLookForACooledPortEndsWhenThePartCannotGoToIt(t *testing.T) {
	for _, variant := range []string{"q stays in cooldown", "q out of cooldown unreadable", "q out of cooldown readable"} {
		t.Run(variant, func(t *testing.T) {
			freshCanaryMemory(t)
			shortenHealthProbe(t, 150*time.Millisecond)
			shortenPacing(t, 10*time.Millisecond, 8*time.Second)
			p := machineFaultPool(t, []string{robinMachine, robinMachine, robinMachine, batMachine}, answerWithTranscripts)
			clearCooldownsAtEnd(t, p)
			port0, port1, port2, port3 := p.h.eps[0].URL, p.h.eps[1].URL, p.h.eps[2].URL, p.h.eps[3].URL
			p.h.delayHealth(port3, time.Minute) // "?" at the gate
			markEndpointFailure(port1)
			markEndpointFailure(port2)
			markEndpointFailure(port3)
			var once sync.Once
			p.h.onHealthHit(port2, 2, func() { once.Do(func() { clearCooldown(port1) }) })
			send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
				if _, ok := sub["chap1"]; ok && g.Endpoint.URL != port3 {
					p.h.wedge(g.Endpoint.URL)
					clearCooldown(port2)
					switch variant {
					case "q out of cooldown unreadable":
						clearCooldown(port3)
					case "q out of cooldown readable":
						p.h.healthDelay.Delete(strings.TrimPrefix(port3, "http://"))
						clearCooldown(port3)
					}
					return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Minute, Jobs: sortedIDs(sub)}
				}
				return nil, errors.New("unexpected send")
			}
			start := time.Now()
			res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), send)
			el := time.Since(start)
			t.Logf("elapsed=%s err=%v stuck=%v chap1=%+v hits=%d/%d/%d/%d sends=%v", el.Round(time.Millisecond), err, IsWorkerStuck(err), res["chap1"],
				healthHitsFor(p, port0), healthHitsFor(p, port1), healthHitsFor(p, port2), healthHitsFor(p, port3), p.sendLog())
			if res["chap1"].Condemned {
				t.Errorf("condemned on robin alone while an untried unknown port remained")
			}
			if !IsWorkerStuck(err) {
				t.Errorf("err = %v, want ErrWorkerStuck (D69)", err)
			}
			if el > 4*time.Second {
				t.Errorf("took %s", el)
			}
		})
	}
}

// A pool of only unknown ports, all in cooldown, with no endpoint that failed
// in the dispatch: the error does not claim a wait of the pacing allowance that
// the dispatch did not make.
func TestUnknownPort_AnOutageMessageSaysWhatTheDispatchDid(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, 3*time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	clearCooldownsAtEnd(t, p)
	for _, ep := range p.h.eps {
		p.h.delayHealth(ep.URL, time.Minute)
		markEndpointFailure(ep.URL)
	}
	start := time.Now()
	_, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), p.send)
	t.Logf("elapsed=%s err=%v unavailable=%v stuck=%v transport=%v", time.Since(start).Round(time.Millisecond), err,
		errors.Is(err, ErrPoolUnavailable), IsWorkerStuck(err), IsTransport(err))
	if err != nil && strings.Contains(err.Error(), "after waiting") && time.Since(start) < time.Second {
		t.Errorf("message claims a wait of the pacing allowance that did not happen: %v", err)
	}
}

// A known port changes hands during the wait and is dropped while the "?" port
// beside it is in cooldown (no endpoint failed in the dispatch): the dispatch
// ends as the pool emptied (errPortChangedHands, a deferral the ops do not count
// as an outage), not as ErrPoolUnavailable.
func TestUnknownPort_AKnownPortDroppedBesideAnUnknownOneInCooldownEndsAsChangedHands(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, 3*time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	clearCooldownsAtEnd(t, p)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	atFirstPace(t, p, 50*time.Millisecond, func() {
		p.h.setMachineID(port0, catwomanMachine)
		clearCooldown(port0)
		after(t, 500*time.Millisecond, func() { clearCooldown(port1) })
	})
	start := time.Now()
	_, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), p.send)
	t.Logf("elapsed=%s err=%v unavailable=%v changedHands=%v stuck=%v", time.Since(start).Round(time.Millisecond), err,
		errors.Is(err, ErrPoolUnavailable), errors.Is(err, errPortChangedHands), IsWorkerStuck(err))
	if !errors.Is(err, errPortChangedHands) || IsPoolUnavailable(err) {
		t.Errorf("err = %v: want the changed-hands deferral (not counted), as noEndpointError gives for changed hands beside an unreadable port", err)
	}
}

// preflight asks a remembered machine's ports the canary clip, and a port whose
// cooldown ended while the gate probed it is probed again first (D60, D62):
// bat's port 0 is in cooldown when the dispatch starts, its cooldown ends during
// the gate's own request, and a different machine answers there afterwards.
// The clip is sent to the port that is still bat's and never to port 0, and the
// work goes where the machine answered.
func TestPreflight_APortWhoseCooldownEndedDuringTheGateIsProbedBeforeTheClip(t *testing.T) {
	p := rememberedMachine(t, 2)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	markEndpointFailure(port0)
	p.h.onHealthHit(port0, 1, func() {
		clearCooldown(port0)
		p.h.setMachineID(port0, catwomanMachine)
	})
	c := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, c.fn, 4)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(c.asked, port0) {
		t.Errorf("the canary clip was sent to port 0 with no probe since its cooldown ended and another machine took it over: asked=%v", c.asked)
	}
	if !slices.Contains(c.asked, port1) {
		t.Errorf("port 1, still bat, was not asked: asked=%v", c.asked)
	}
	for id, r := range res {
		if r.Endpoint != port1 {
			t.Errorf("%s was answered by %s, want port 1", id, r.Endpoint)
		}
	}
	if n := healthHitsFor(p, port0); n < 2 {
		t.Errorf("port 0 was probed %d time(s): want the gate's and the one after its cooldown ended", n)
	}
}

// Port 0 (robin) is found wedged when its cooldown ends (a Stuck refusal, dropped);
// port 1 (bat, a KNOWN machine) stays in a cooldown that outlasts the allowance.
// The pool is not stuck, bat is only out for a while: ErrPoolUnavailable, not the
// emptied pool noEndpointError would call "all 2 whisper endpoint(s) are stuck".
func TestPoolEmptied_AWedgedPortBesideAKnownPortInALongCooldownIsAnOutageNotAStuckPool(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	clearCooldownsAtEnd(t, p)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	for range 5 {
		markEndpointFailure(port1) // a long streak: far past the allowance
	}
	markEndpointFailure(port0)
	atFirstPace(t, p, 20*time.Millisecond, func() {
		p.h.wedge(port0)
		clearCooldown(port0)
	})
	_, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), p.send)
	if IsWorkerStuck(err) || !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("err = %v: a pool with one wedged port and one known port in cooldown is an outage (ErrPoolUnavailable), not a stuck pool", err)
	}
}

// The same with port 1 an unknown ("?") port in cooldown, which the dispatch does
// not wait for. Nothing changed hands, so it is no deferral either.
func TestPoolEmptied_AWedgedPortBesideAnUnknownPortInCooldownIsAnOutageNotAStuckPool(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	clearCooldownsAtEnd(t, p)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	atFirstPace(t, p, 20*time.Millisecond, func() {
		p.h.wedge(port0)
		clearCooldown(port0)
	})
	_, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), p.send)
	if IsWorkerStuck(err) || !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("err = %v: one wedged port beside an unknown port in cooldown is an outage (ErrPoolUnavailable), not a stuck pool", err)
	}
}

// The count in "all N whisper endpoint(s) are stuck" is of the ports judged: port 2
// was wedged at the gate and refused there, and ports 0 and 1 are found wedged
// when their cooldowns end, so the pool that ends is two ports, not three.
func TestPoolEmptied_TheStuckCountIsOfThePortsTheRefusalsJudged(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, 5*time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine, catwomanMachine}, answerWithTranscripts)
	clearCooldownsAtEnd(t, p)
	port0, port1, port2 := p.h.eps[0].URL, p.h.eps[1].URL, p.h.eps[2].URL
	p.h.wedge(port2)
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	atFirstPace(t, p, 20*time.Millisecond, func() {
		p.h.wedge(port0)
		p.h.wedge(port1)
		clearCooldown(port0)
		clearCooldown(port1)
	})
	_, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), p.send)
	if !IsWorkerStuck(err) || !strings.Contains(err.Error(), "all 2 whisper endpoint(s) are stuck") {
		t.Errorf("err = %v: want a stuck pool of the 2 ports found wedged after the wait", err)
	}
}

// A cooldown that starts and ends inside one round, as another dispatch's 30 s
// failure inside a request of several minutes does, is never seen by a look at
// InCooldown; the failure generation moved all the same, so the port is probed
// again before runCanaries sends it the clip. Robin's port 0 faults in round 1;
// in round 2 another dispatch fails it and clears it, and a different machine
// (catwoman) answers there; bat's port 1 faults worker_misconfigured, so every
// endpoint is sick and the canary runs. The cooldown ends by MarkSuccess, or by
// the passage of time (the health clock moved past it).
func TestRunCanaries_ACooldownThatStartsAndEndsInsideOneRoundStillGetsAProbeBeforeTheClip(t *testing.T) {
	for _, ending := range []string{"cleared by a success", "ended by time"} {
		t.Run(ending, func(t *testing.T) {
			freshCanaryMemory(t)
			shortenPacing(t, 10*time.Millisecond, 5*time.Second)
			var offset atomic.Int64
			var port0, port1 string
			var mu sync.Mutex
			round1 := map[string]bool{}
			var switched atomic.Bool
			var hitsAtSwitch, hitsAtCanary atomic.Int64
			hitsAtCanary.Store(-1)
			answer := func(url, id string) BatchResult {
				if url == port0 && !switched.Load() {
					return BatchResult{Error: timeoutText}
				}
				if url == port1 && switched.Load() {
					return BatchResult{Error: PathErrWorkerMisconfigured + pathErrSeparator + "no decoder"}
				}
				return BatchResult{Text: "T:" + id}
			}
			p := machineFaultPool(t, []string{robinMachine, batMachine}, answer)
			port0, port1 = p.h.eps[0].URL, p.h.eps[1].URL
			// The cooldowns the dispatch and the test start while the clock is moved are
			// measured by it: the ports are cleared before the wall clock is put back.
			restore := aidispatchClock(&offset)
			t.Cleanup(func() {
				for _, ep := range p.h.eps {
					clearCooldown(ep.URL)
				}
				restore()
			})
			reply := func(url string, sub map[string]string) (map[string]BatchResult, error) {
				res := map[string]BatchResult{}
				for id := range sub {
					res[id] = answer(url, id)
				}
				kept, fault := splitWorkerFault(url, res)
				if fault != nil {
					return kept, fault
				}
				return kept, nil
			}
			send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
				u := g.Endpoint.URL
				if u == port0 && !switched.Load() {
					mu.Lock()
					for id := range sub {
						round1[id] = true
					}
					mu.Unlock()
					return reply(u, sub)
				}
				if u == port1 && !switched.Load() {
					mu.Lock()
					later := false
					for id := range sub {
						later = later || round1[id]
					}
					mu.Unlock()
					if later {
						markEndpointFailure(port0) // another dispatch: a cooldown starts mid-round...
						p.h.setMachineID(port0, catwomanMachine)
						if ending == "ended by time" {
							offset.Store(int64(time.Hour)) // ...and runs out before the round does
						} else {
							clearCooldown(port0)
						}
						hitsAtSwitch.Store(int64(healthHitsFor(p, port0)))
						switched.Store(true)
					}
				}
				return reply(u, sub)
			}
			c := &urlCanary{fail: map[string]bool{port0: true}}
			cfn := func(ctx context.Context, g gatedEndpoint) error {
				if g.Endpoint.URL == port0 {
					hitsAtCanary.Store(int64(healthHitsFor(p, port0)))
				}
				return c.fn(ctx, g)
			}
			jobs := map[string]string{}
			for _, id := range loopers(16, "chap") {
				jobs[id] = "u"
			}
			_, err := dispatchPool(testCtx(t), p.h.eps, nil, jobs, poolOptions{canary: cfn}, send)
			if !switched.Load() {
				t.Fatalf("setup did not reach round 2 (err = %v)", err)
			}
			if endpointInCooldown(port0) {
				t.Fatalf("setup: port 0 should be out of its cooldown")
			}
			if hitsAtCanary.Load() != -1 {
				t.Errorf("the canary clip was sent to port 0 (robin at the gate, catwoman since a cooldown began and ended in round 2): err = %v", err)
			}
			if got := healthHitsFor(p, port0); got <= int(hitsAtSwitch.Load()) {
				t.Errorf("port 0 was not probed after the switch: %d request(s), %d at the switch", got, hitsAtSwitch.Load())
			}
			if ok, ports := rememberedPorts(robinKey); ok {
				t.Errorf("robin is remembered as failing the canary clip on catwoman's answer: ports=%v", ports)
			}
		})
	}
}

// aidispatchClock moves the clock the pool's cooldowns are measured by forward by
// *offset (nanoseconds), and returns the function that puts the wall clock back.
func aidispatchClock(offset *atomic.Int64) func() {
	return aidispatch.DefaultHealth().SetClockForTest(func() time.Time {
		return time.Now().Add(time.Duration(offset.Load()))
	})
}
