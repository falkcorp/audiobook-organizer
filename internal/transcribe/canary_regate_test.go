// file: internal/transcribe/canary_regate_test.go
// version: 1.1.0
// guid: 2f7b1c84-6d39-4e05-8a1b-93c0d5e7f126
// last-edited: 2026-10-27

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
)

// Review 9 of #4201 (D60, D62). A pool uses a port after a cooldown wait only if
// its identity then equals the identity the dispatch froze for it, at every
// wait: the one for a remembered machine's cooldown (resolvePending) and the
// round loop's own (waitOutCooldown). A port whose /health cannot be read after
// the wait is not used either. These tests are the review's repros.

// urlCanary is a canaryFunc that records the ports it was asked and fails the
// ones listed in fail.
type urlCanary struct {
	mu    sync.Mutex
	asked []string
	fail  map[string]bool
}

func (c *urlCanary) fn(_ context.Context, g gatedEndpoint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, g.Endpoint.URL)
	if c.fail[g.Endpoint.URL] {
		return errors.New("canary transcript wrong")
	}
	return nil
}

func (c *urlCanary) askedPorts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.asked)
}

// freshCanaryMemory starts the test with nothing remembered and leaves nothing.
func freshCanaryMemory(t *testing.T) {
	t.Helper()
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
}

// questionFrozenPool is a pool of two ports that both answer as robin once the
// wait is over. Port 0 was robin at the start. Port 1's /health could not be
// read when the dispatch began, so it was frozen as "?" plus the hash of its URL
// and kept (the pool requires nothing); that "?" entry is remembered as having
// failed the canary clip, and both ports are in cooldown. One callback ends the
// delay of port 1's /health and then both cooldowns, so the wait ends only once
// port 1 can be read: no ordering between two timers. The first /health probe of
// port 1 gives up after 150ms, so the callback at 550ms leaves 400ms.
func questionFrozenPool(t *testing.T, answer func(p *faultPool) func(url, id string) BatchResult) (*faultPool, string, string) {
	t.Helper()
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	var p *faultPool
	p = machineFaultPool(t, []string{robinMachine, robinMachine}, func(url, id string) BatchResult { return answer(p)(url, id) })
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	markCanaryFailed("?"+shortHash(port1), port1)
	markCanaryFailed(robinKey, port0)
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	after(t, 550*time.Millisecond, func() {
		p.h.healthDelay.Delete(strings.TrimPrefix(port1, "http://"))
		clearCooldown(port0)
		clearCooldown(port1)
	})
	return p, port0, port1
}

func sentOnPort(p *faultPool, url string) []string {
	var ids []string
	for _, s := range p.sendLog() {
		if s.url == url {
			ids = append(ids, s.ids...)
		}
	}
	return ids
}

// Port 1 was unreadable at the gate (frozen "?"), and answers as robin after the
// wait. Port 0 is robin too and fails the canary clip in this dispatch. Kept,
// port 1 took every job as robin while robin was benched for failing the clip
// on its sibling port, because the "?" check let any answer through. It is
// dropped, never asked the clip and never sent a job.
func TestQuestion_APortFrozenAsUnreadableThatAnswersAsASiblingMachineIsDroppedAfterTheWait(t *testing.T) {
	p, port0, port1 := questionFrozenPool(t, func(*faultPool) func(url, id string) BatchResult { return answerWithTranscripts })
	c := &urlCanary{fail: map[string]bool{port0: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, c.fn, 8)
	if got := sentOnPort(p, port1); len(got) > 0 {
		t.Errorf("port 1 (frozen \"?\", answers as %s now, which failed the clip on port 0) was sent %v", robinKey, got)
	}
	for id, r := range res {
		if r.Endpoint == port1 {
			t.Errorf("%s was answered by port 1, stored as %q", id, r.Worker)
		}
	}
	if slices.Contains(c.askedPorts(), port1) {
		t.Errorf("the canary clip was sent to port 1, which is another worker's identity than the dispatch knew: %v", c.askedPorts())
	}
	if !IsPoolUnavailable(err) {
		t.Errorf("err = %v, want ErrPoolUnavailable: robin failed the clip on its only port left", err)
	}
	if !endpointRemembered(robinKey, port0) {
		t.Errorf("robin failed the clip on port 0 and is no longer remembered")
	}
}

// The same pool, and port 1 times out on every clip it is sent. It took the work
// and wrote timeout strikes naming robin, which failed the clip in this very
// dispatch. Dropped, it writes none.
func TestQuestion_APortFrozenAsUnreadableWritesNoStrikesNamingTheSiblingMachine(t *testing.T) {
	p, port0, _ := questionFrozenPool(t, func(p *faultPool) func(url, id string) BatchResult {
		return func(url, id string) BatchResult {
			if url == p.h.eps[1].URL {
				return BatchResult{Error: timeoutText}
			}
			return BatchResult{Text: "T:" + id}
		}
	})
	c := &urlCanary{fail: map[string]bool{port0: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, _ := dispatchWith(ctx, p, c.fn, 2)
	for id, r := range res {
		if IsStrikeError(r.Error) {
			t.Errorf("%s: a strike (endpoint %s, worker %q) although robin failed the clip and nothing it answers counts", id, r.Endpoint, r.Worker)
		}
	}
}

// Robin passes the clip on port 0. Port 1, frozen "?", answers as robin too, and
// was taken for a second machine ({m:robin, ?hash}) while its answers were stored
// as robin. The pool is one machine with one port left: port 0 takes every job.
func TestQuestion_APortFrozenAsUnreadableThatAnswersAsRobinTakesNoWorkWhenRobinPassesOnPort0(t *testing.T) {
	p, port0, port1 := questionFrozenPool(t, func(*faultPool) func(url, id string) BatchResult { return answerWithTranscripts })
	c := &urlCanary{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, c.fn, 4)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v, want the four jobs answered", len(res), err)
	}
	for id, r := range res {
		if r.Endpoint != port0 || r.Worker != robinKey {
			t.Errorf("%s = endpoint %s worker %q, want port 0 stored as %s", id, r.Endpoint, r.Worker, robinKey)
		}
	}
	if got := sentOnPort(p, port1); len(got) > 0 {
		t.Errorf("port 1 was sent %v", got)
	}
	if slices.Contains(c.askedPorts(), port1) {
		t.Errorf("canary asked port 1: %v", c.askedPorts())
	}
}

// A lone job that hangs the decoder on every port it is sent to is condemned by
// the number of MACHINES it left stuck. Port 1 stood for a second machine
// ("?hash") next to robin on port 0, so one box with two ports condemned a file
// by "two machines". With port 1 dropped there is one machine and one port, and
// the verdict names one worker.
func TestQuestion_APortFrozenAsUnreadableIsNotASecondMachineForACondemnation(t *testing.T) {
	p, port0, port1 := questionFrozenPool(t, func(*faultPool) func(url, id string) BatchResult { return answerWithTranscripts })
	hang := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		if _, ok := sub["chap1"]; ok {
			p.h.wedge(g.Endpoint.URL) // a worker that, asked after the request ran out, is past its own deadline
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Minute, Jobs: sortedIDs(sub)}
		}
		return p.send(ctx, g, sub, pf)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchPool(ctx, p.h.eps, nil, map[string]string{"chap1": "unused"}, poolOptions{canary: (&urlCanary{}).fn}, hang)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	r := res["chap1"]
	if !r.Condemned || r.Endpoint != port0 || r.Worker != robinKey {
		t.Errorf("verdict = %+v, want a condemnation by %s on port 0 alone", r, robinKey)
	}
	if !strings.Contains(r.Error, "left 1 worker(s)") {
		t.Errorf("error = %q, want the file to have left ONE worker stuck: port 1 is robin too, not a second machine", r.Error)
	}
	if strings.Contains(r.Endpoint, port1) {
		t.Errorf("verdict names port 1: %q", r.Endpoint)
	}
}

// A port that was waited for and cannot be read once its cooldown ends is not
// asked the canary clip and not sent work: nothing says it is still the worker
// the dispatch knew. Both ports of robin are remembered and in cooldown; port 0
// cannot be read after the wait until the explicit signal below. The clip is
// asked on neither port before that, and on both after it.
func TestQuestion_APortUnreadableAfterTheWaitIsNotAskedTheCanaryClipUntilItIsRead(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{batMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	markCanaryFailed(batKey, port0, port1)
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	var readable atomic.Bool
	after(t, 100*time.Millisecond, func() {
		p.h.delayHealth(port0, time.Minute)
		clearCooldown(port0)
		clearCooldown(port1)
	})
	after(t, 700*time.Millisecond, func() {
		readable.Store(true)
		p.h.healthDelay.Delete(strings.TrimPrefix(port0, "http://"))
	})
	var early []string
	var mu sync.Mutex
	canary := func(_ context.Context, g gatedEndpoint) error {
		if !readable.Load() {
			mu.Lock()
			early = append(early, g.Endpoint.URL)
			mu.Unlock()
		}
		return nil
	}
	res, err := dispatchWith(testCtx(t), p, canary, 4)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v, want the jobs answered once port 0 can be read", len(res), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(early) > 0 {
		t.Errorf("the canary clip was sent to %v while port 0 could not be read", early)
	}
	if endpointRemembered(batKey, port0) {
		t.Errorf("machine bat answered the clip on both ports and is still remembered")
	}
}

// The round loop's own wait. Before it, the ports kept the identity the gate gave
// them minutes earlier: 4 jobs went to port 0 and were stored as the machine that
// was behind it, while a different one answered. Both ports are in a cooldown an
// earlier dispatch set, and nothing is remembered. Port 0 changes hands during
// the wait; the dispatch drops it and port 1 takes every job. The callback
// changes the port and then ends both cooldowns, so the order is explicit; the
// wait steps by poolPaceInterval, so the early end is noticed at once rather than
// after the 30 s cooldown.
func TestPaceWait_APortThatChangesHandsDuringTheRoundLoopsWaitIsDroppedNotStoredAsTheOldMachine(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	after(t, 100*time.Millisecond, func() {
		p.h.setMachineID(port0, catwomanMachine)
		clearCooldown(port0)
		clearCooldown(port1)
	})
	res, err := dispatchWith(testCtx(t), p, (&urlCanary{}).fn, 4)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v, want the four jobs answered by the port that is still its machine", len(res), err)
	}
	for id, r := range res {
		if r.Endpoint != port1 || r.Worker != "m:"+batMachine {
			t.Errorf("%s = endpoint %s worker %q, want port 1 stored as m:%s", id, r.Endpoint, r.Worker, batMachine)
		}
	}
	if got := sentOnPort(p, port0); len(got) > 0 {
		t.Errorf("port 0, which answers as %s now, was sent %v", catKey, got)
	}
}

// A port that cannot be read after the round loop's wait is kept out of the work
// (not sent, not stored as anything) while the other port takes it all.
func TestPaceWait_APortUnreadableAfterTheRoundLoopsWaitTakesNoWork(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	after(t, 100*time.Millisecond, func() {
		p.h.delayHealth(port0, time.Minute)
		clearCooldown(port0)
		clearCooldown(port1)
	})
	res, err := dispatchWith(testCtx(t), p, (&urlCanary{}).fn, 4)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v, want the four jobs answered by port 1", len(res), err)
	}
	for id, r := range res {
		if r.Endpoint != port1 {
			t.Errorf("%s was answered by %s (worker %q), want port 1", id, r.Endpoint, r.Worker)
		}
	}
	if got := sentOnPort(p, port0); len(got) > 0 {
		t.Errorf("port 0 cannot be read, yet it was sent %v", got)
	}
}

// A port that stays unreadable until the pacing allowance is spent is given up
// on, and a pool left with it and a port that changed hands ends as the lone
// endpoint does: deferred with errPortChangedHands, nothing sent.
func TestPaceWait_APortThatStaysUnreadableBesideOneThatChangedHandsEndsAsChangedHands(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 100*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, 1200*time.Millisecond)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	after(t, 100*time.Millisecond, func() {
		p.h.setMachineID(port0, catwomanMachine)
		p.h.delayHealth(port1, time.Minute)
		clearCooldown(port0)
		clearCooldown(port1)
	})
	res, err := dispatchWith(testCtx(t), p, (&urlCanary{}).fn, 4)
	if !errors.Is(err, errPortChangedHands) {
		t.Fatalf("err = %v, want one that wraps errPortChangedHands", err)
	}
	if IsWorkerStuck(err) || IsPoolUnavailable(err) {
		t.Errorf("err = %v: neither a stuck pool nor an outage", err)
	}
	if len(res) != 0 || len(p.sendLog()) != 0 {
		t.Errorf("results=%d sends=%v, want nothing sent", len(res), p.sendLog())
	}
}

// Stuck beside a port that changed hands fell through to "refused by capability
// requirements []", which sends an operator hunting for a box that is not on a
// GPU. It names what happened, defers the jobs without a stuck verdict (the port
// that changed hands is not wedged, and the next dispatch's gate keys it), and
// names the stuck port.
func TestChangedHands_AStuckPortBesideOneThatChangedHandsEndsDeferredNamingBoth(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	after(t, 100*time.Millisecond, func() {
		p.h.wedge(port0)
		p.h.setMachineID(port1, catwomanMachine)
		clearCooldown(port0)
		clearCooldown(port1)
	})
	_, err := dispatchWith(testCtx(t), p, (&urlCanary{}).fn, 4)
	if err == nil {
		t.Fatal("err = nil, want the jobs deferred")
	}
	if !errors.Is(err, errPortChangedHands) {
		t.Errorf("err = %v, want one that wraps errPortChangedHands", err)
	}
	if IsWorkerStuck(err) || IsPoolUnavailable(err) {
		t.Errorf("err = %v: the ops would stop the run (stuck) or count an outage; neither is true", err)
	}
	if te, ok := errors.AsType[*TransportError](err); !ok || te.Recognized {
		t.Errorf("err = %v, want an unrecognised TransportError, which the ops defer on without counting", err)
	}
	if strings.Contains(err.Error(), "capability requirements") {
		t.Errorf("err = %v: names a capability problem", err)
	}
	if !strings.Contains(err.Error(), port0) || !strings.Contains(err.Error(), "stuck") {
		t.Errorf("err = %v, want the stuck port named", err)
	}
	if got := p.sendLog(); len(got) != 0 {
		t.Errorf("sends = %v, want nothing sent", got)
	}
}

func TestChangedHands_StuckBesideChangedHandsIsNeitherAllStuckNorOnlyChangedHands(t *testing.T) {
	stuck := gpuRefusal{Stuck: true}
	changed := gpuRefusal{ChangedHands: true}
	down := gpuRefusal{Transient: true}
	capability := gpuRefusal{}
	for _, tc := range []struct {
		name string
		in   []gpuRefusal
		want bool
	}{
		{"stuck and changed", []gpuRefusal{stuck, changed}, true},
		{"stuck, changed and down", []gpuRefusal{stuck, changed, down}, true},
		{"only stuck", []gpuRefusal{stuck, stuck}, false},
		{"only changed", []gpuRefusal{changed, changed}, false},
		{"changed and down", []gpuRefusal{changed, down}, false},
		{"stuck and down", []gpuRefusal{stuck, down}, false},
		{"a capability refusal beside them names the capability", []gpuRefusal{stuck, changed, capability}, false},
		{"none", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stuckBesideChangedHands(tc.in); got != tc.want {
				t.Errorf("stuckBesideChangedHands = %v, want %v", got, tc.want)
			}
		})
	}
}

// The port a pool drops for gaining an id during the wait is keyed by the id it
// reports in the NEXT dispatch, which carries the weak entry's memory there and
// asks it the clip before any work (the other half of dropping it).
func TestChangedHands_TheNextDispatchKeysADroppedPortByTheIdItReportsAndAsksItTheClip(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	h := machineHarness(t, []string{"", robinMachine})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	t.Cleanup(func() {
		for _, ep := range h.eps {
			clearCooldown(ep.URL)
		}
	})
	w0, w1 := h.eps[0].URL, h.eps[1].URL
	oldKey := "u:" + shortHash(w0)
	const newID = "cccc0003"
	markCanaryFailed(oldKey)
	markCanaryFailed(robinKey)
	markEndpointFailure(w0)
	after(t, 80*time.Millisecond, func() {
		h.setMachineID(w0, newID)
		clearCooldown(w0)
	})
	c := &urlCanary{fail: map[string]bool{w1: true}}
	if _, err := dispatchOn(testCtx(t), h.eps, nil, p.send, c.fn, 8); err == nil {
		t.Fatalf("dispatch 1: robin failed the clip on its only port, want an error")
	}
	if slices.Contains(c.askedPorts(), w0) {
		t.Errorf("dispatch 1 asked the clip of the machine it knew to the port that changed hands: %v", c.askedPorts())
	}
	clearCooldown(w0)
	clearCooldown(w1)
	c2 := &urlCanary{}
	before := len(p.sendLog())
	res, err := dispatchOn(testCtx(t), h.eps, nil, p.send, c2.fn, 8)
	if err != nil || len(res) != 8 {
		t.Fatalf("dispatch 2: res=%d err=%v", len(res), err)
	}
	var onW0 int
	for _, s := range p.sendLog()[before:] {
		if s.url == w0 {
			onW0 += len(s.ids)
		}
	}
	if onW0 > 0 && !slices.Contains(c2.askedPorts(), w0) {
		t.Errorf("dispatch 2 sent port 0 %d jobs with no clip asked", onW0)
	}
	if !slices.Contains(c2.askedPorts(), w0) {
		t.Errorf("dispatch 2 did not ask port 0 (m:%s, carrying %s's failure): %v", newID, oldKey, c2.askedPorts())
	}
}

// Owner decision on review 9 of #4201: a machine is known only by what its port
// reports. A port whose /health could not be read when the dispatch began (a "?"
// key) is evidence of no machine, so it is never counted as one for a
// condemnation, with or without a cooldown wait; and only a port that reports a
// remembered machine's own id is asked that machine's canary clip on its behalf
// or forgets its failure.

// hangingChap1 is a send that never answers a request holding chap1: the worker
// is past its own deadline when asked (wedged) and the request is stuck.
func hangingChap1(p *faultPool) func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
	return func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		if _, ok := sub["chap1"]; ok {
			p.h.wedge(g.Endpoint.URL)
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Minute, Jobs: sortedIDs(sub)}
		}
		return p.send(ctx, g, sub, pf)
	}
}

// No wait at all: port 1's /health cannot be read when the dispatch begins and
// the pool requires nothing, so it is kept as "?hash" next to robin on port 0.
// Nothing is remembered and nothing is in cooldown. A lone job that hangs the
// decoder is condemned by the machines it left stuck, and port 1 is none: it
// used to make "two machines" and a verdict that two workers were stuck.
func TestUnknownPort_AnUnreadablePortIsNotASecondMachineForACondemnationWithNoWaitAtAll(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, robinMachine}, answerWithTranscripts)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchPool(ctx, p.h.eps, nil, map[string]string{"chap1": "unused"}, poolOptions{canary: (&urlCanary{}).fn}, hangingChap1(p))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	r := res["chap1"]
	if !r.Condemned || r.Endpoint != port0 || r.Worker != robinKey {
		t.Errorf("verdict = %+v, want a condemnation by %s on port 0 alone", r, robinKey)
	}
	if !strings.Contains(r.Error, "left 1 worker(s)") {
		t.Errorf("error = %q, want ONE worker left stuck: the unreadable port is no machine", r.Error)
	}
	if strings.Contains(r.Endpoint, port1) {
		t.Errorf("verdict names port 1: %q", r.Endpoint)
	}
}

// A pool in which no port could be read knows no machine at all (soloLimit 0):
// nothing can be condemned on the word of ports that identify nothing. The job is
// not given a verdict about the file.
func TestUnknownPort_APoolOfUnreadablePortsKnowsNoMachineAndCondemnsNothing(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, robinMachine}, answerWithTranscripts)
	for _, ep := range p.h.eps {
		p.h.delayHealth(ep.URL, time.Minute)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchPool(ctx, p.h.eps, nil, map[string]string{"chap1": "unused"}, poolOptions{canary: (&urlCanary{}).fn}, hangingChap1(p))
	for id, r := range res {
		if r.Condemned {
			t.Errorf("%s = %+v: a condemnation from ports that identify no machine", id, r)
		}
	}
	if err == nil && len(res) == 0 {
		t.Error("the job vanished: want a result or an error")
	}
}

// RR9-D. Port 1 was bat's only port. Its tunnel dropped (a refused probe ends the
// link of the address to its machine) and an id-capable worker answers there as a
// weak "u:", so bat's port set still lists it. The worker never reported bat's
// id, so it is not bat: it is not asked bat's clip on bat's behalf, and its
// answer cannot clear bat's failure. It used to be the only port asked, passed,
// and bat was forgotten without bat's machine ever being asked.
func TestRememberedMachine_AWeakPortListedAmongItsPortsIsNotAskedItsCanaryAndDoesNotClearIt(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, ""}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port1 := p.h.eps[1].URL
	noteCanaryPort(batKey, port1, true)
	markCanaryFailed(batKey, port1)
	refuseProbe(port1)
	c := &urlCanary{}
	res, err := dispatchWith(testCtx(t), p, c.fn, 4)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 4)
	if slices.Contains(c.askedPorts(), port1) {
		t.Errorf("the weak port was asked %s's canary clip on its behalf: %v", batKey, c.askedPorts())
	}
	if !failedCanaryBefore(batKey) {
		t.Errorf("%s was forgotten on the answer of a port that never reported its id", batKey)
	}
}
