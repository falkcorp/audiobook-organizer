// file: internal/transcribe/canary_regate_test.go
// version: 1.3.0
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

// atFirstPace runs fn d after the round loop first finds no endpoint that can take
// work and is about to wait for a cooldown (poolOptions.beforePace). The event, not the
// start of the test, starts the clock: a loaded -race agent that is slow to begin
// the dispatch (its gate probes) cannot run fn before the dispatch has seen the
// state fn changes.
func atFirstPace(t *testing.T, p *faultPool, d time.Duration, fn func()) {
	t.Helper()
	var once sync.Once
	p.beforePace = func() { once.Do(func() { after(t, d, fn) }) }
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
// the wait; the dispatch drops it and port 1 takes every job. The callback is
// run a few milliseconds after the dispatch first finds nothing to send to and
// begins to wait (atFirstPace): the change comes in the wait however slow the
// agent is, which a timer started with the test did not guarantee. It changes the
// port and then ends both cooldowns; the wait steps by poolPaceInterval, so the
// early end is noticed at once rather than after the 30 s cooldown.
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
	atFirstPace(t, p, 20*time.Millisecond, func() {
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
	atFirstPace(t, p, 20*time.Millisecond, func() {
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
	atFirstPace(t, p, 20*time.Millisecond, func() {
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
	atFirstPace(t, p, 20*time.Millisecond, func() {
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

// sendTally records every endpoint a send was made to, whether or not the send
// answered (faultPool.sendLog only has the ones that did).
type sendTally struct {
	mu   sync.Mutex
	urls []string
}

func (s *sendTally) wrap(send func(context.Context, gatedEndpoint, map[string]string, ProgressFunc) (map[string]BatchResult, error)) func(context.Context, gatedEndpoint, map[string]string, ProgressFunc) (map[string]BatchResult, error) {
	return func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		s.mu.Lock()
		s.urls = append(s.urls, g.Endpoint.URL)
		s.mu.Unlock()
		return send(ctx, g, sub, pf)
	}
}

func (s *sendTally) sentTo(url string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.urls, url)
}

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
//
// Port 1 stays unreadable for the whole dispatch here, so when the job is sent to
// it (a port that might be a second machine is tried before a verdict on one
// machine, D66) nobody can say that the request hung the decoder rather than
// waited, and the job has now been tried everywhere without a second machine to
// say so. The right outcome is a deferral, not a condemnation on robin's word:
// no result for the job, so no verdict and no strike, and an error the ops defer
// on. (This test used to expect a condemnation by robin alone, reached because
// the job was never sent to port 1.) A verdict that port 1 is a wedged worker too
// is the test below, where its /health can be read.
func TestUnknownPort_AnUnreadablePortIsNotASecondMachineForACondemnationWithNoWaitAtAll(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, robinMachine}, answerWithTranscripts)
	port1 := p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tried := &sendTally{}
	res, err := dispatchPool(ctx, p.h.eps, nil, map[string]string{"chap1": "unused"}, poolOptions{canary: (&urlCanary{}).fn}, tried.wrap(hangingChap1(p)))
	if r, ok := res["chap1"]; ok {
		t.Errorf("chap1 = %+v: a verdict or a strike for a file that hung one machine and a port nobody could read", r)
	}
	if err == nil {
		t.Error("err = nil, want the job deferred with an error")
	}
	// Pinned: what the ops make of it. The book gets no row either way. This ends
	// as the pool did before an unreadable port stopped counting as a machine:
	// port 1's request ran out and its /health cannot say it is wedged, so it is a
	// recognised transport failure (an outage: the ops count it toward their
	// unavailable streak), not a stuck pool.
	if te, ok := errors.AsType[*TransportError](err); !ok || !te.Recognized || IsWorkerStuck(err) || IsPoolUnavailable(err) {
		t.Errorf("err = %v, want a recognised TransportError that is neither a stuck pool nor ErrPoolUnavailable", err)
	}
	if !tried.sentTo(port1) {
		t.Error("the unreadable port was never sent the job: a verdict on one machine would not have waited for it")
	}
}

// The unknown port, once the job is sent to it, hangs too and can be read by then
// (wedged): the file has left two workers stuck, but only robin is a machine, so
// the verdict names robin alone and says one worker. The unreadable port is still
// no second machine for the count.
func TestUnknownPort_AnUnknownPortThatHangsTooIsNotASecondMachineInTheVerdict(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, robinMachine}, answerWithTranscripts)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	hang := hangingChap1(p)
	tried := &sendTally{}
	send := tried.wrap(func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		p.h.healthDelay.Delete(strings.TrimPrefix(port1, "http://")) // readable from the first request on
		return hang(ctx, g, sub, pf)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatchPool(ctx, p.h.eps, nil, map[string]string{"chap1": "unused"}, poolOptions{canary: (&urlCanary{}).fn}, send)
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
	if !tried.sentTo(port1) {
		t.Error("port 1 was never sent the job before the verdict")
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

// Review 10 of #4201 (D62: every wait re-checks a port's identity). A port the
// dispatch waited out was used under the identity it froze minutes before when
// the wait was not the round loop's own: a stand-in whose cooldown ended inside
// resolvePending's sleep, and a port whose cooldown ended while the dispatch
// worked other ports. The events below are /health requests of the port, not
// timers: the dispatch's gate is the port's first request, so what a callback
// changes is read by the second, whatever the speed of the agent.

// standInPool is a pool of robin on port 0 and bat on port 1, in which robin is
// remembered as having failed the canary clip and both ports are in cooldown:
// the dispatch waits for robin and port 1 is the stand-in whose cooldown ends in
// that wait. On the gate's request of port 1 a callback starts which, a few
// milliseconds later, makes the worker there catwoman's and ends port 1's
// cooldown; port 1's next request (the probe the dispatch owes it) ends robin's
// cooldown, so a dispatch that dropped port 1 can go on with robin.
func standInPool(t *testing.T, answer func(url, id string) BatchResult) *faultPool {
	t.Helper()
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answer)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	markCanaryFailed(robinKey, port0)
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	p.h.onHealthHit(port1, 1, func() {
		after(t, 30*time.Millisecond, func() {
			p.h.setMachineID(port1, catwomanMachine)
			clearCooldown(port1) // only the stand-in comes out
		})
	})
	p.h.onHealthHit(port1, 2, func() { clearCooldown(port0) })
	return p
}

// The stand-in's cooldown ended inside resolvePending (its end is one of the
// targets of the sleep), which returned on the stand-in being able; the round
// loop's recheck only probed ports it had waited for itself, so port 1 took the
// four jobs as bat while catwoman answered there.
func TestStandIn_AStandInWhoseCooldownEndsWhileARememberedMachineIsWaitedForIsProbedBeforeItTakesWork(t *testing.T) {
	p := standInPool(t, answerWithTranscripts)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	canary := &urlCanary{}
	res, err := dispatchWith(testCtx(t), p, canary.fn, 4)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v, want the four jobs answered by robin once the stand-in is dropped", len(res), err)
	}
	for id, r := range res {
		if r.Endpoint != port0 || r.Worker != robinKey {
			t.Errorf("%s = endpoint %s worker %q, want port 0 stored as %s", id, r.Endpoint, r.Worker, robinKey)
		}
	}
	if got := sentOnPort(p, port1); len(got) > 0 {
		t.Errorf("port 1 changed hands during the wait and was sent %v", got)
	}
	if slices.Contains(canary.askedPorts(), port1) {
		t.Errorf("the canary clip was sent to port 1: %v", canary.askedPorts())
	}
}

// The same pool where the worker that took port 1 over times out on every clip:
// the strike would be stored as bat's, which is what D60 is there to prevent.
func TestStandIn_ATimeoutOfAMachineThatTookTheStandInsPortIsNotStoredAsTheStandIn(t *testing.T) {
	var p *faultPool
	p = standInPool(t, func(url, id string) BatchResult {
		if url == p.h.eps[1].URL {
			return BatchResult{Error: timeoutText}
		}
		return BatchResult{Text: "T:" + id}
	})
	port1 := p.h.eps[1].URL
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, poolOptions{canary: (&urlCanary{}).fn}, p.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if r := res["chap1"]; IsStrikeError(r.Error) || r.Endpoint == port1 || r.Worker != robinKey {
		t.Errorf("chap1 = %+v, want it answered by robin on port 0: a strike by the worker on port 1 would be stored as %s", r, "m:"+batMachine)
	}
	if got := sentOnPort(p, port1); len(got) > 0 {
		t.Errorf("port 1 changed hands during the wait and was sent %v", got)
	}
}

// No wait by the dispatch at all: port 1 is in a cooldown an earlier dispatch set
// and port 0 works the first round; during that round port 1's cooldown ends and
// another machine answers there. The second round sent it the work under the
// identity it was frozen with. The send is the event: it runs while the round
// is in progress.
func TestSatOut_APortWhoseCooldownEndedDuringARoundIsProbedBeforeItIsGivenWork(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, 3*time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	markEndpointFailure(port1)
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		if g.Endpoint.URL == port0 {
			p.h.setMachineID(port1, catwomanMachine)
			clearCooldown(port1)
			return nil, errors.New("connection reset by peer")
		}
		return p.send(ctx, g, sub, pf)
	}
	res, _ := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u", "chap2": "u"}, poolOptions{canary: (&urlCanary{}).fn}, send)
	for id, r := range res {
		if r.Endpoint == port1 {
			t.Errorf("%s answered by port 1 (it reports %s now), stored as %q", id, catwomanMachine, r.Worker)
		}
	}
	if got := sentOnPort(p, port1); len(got) > 0 {
		t.Errorf("port 1 changed hands while the round ran and was sent %v", got)
	}
}

// F1 of review 10: the healthy list was made while both ports were in cooldown,
// both cooldowns ended before the loop asked how long to wait, and the pool
// answered ErrPoolUnavailable ("every endpoint ... in cooldown after waiting
// 6m") having waited for nothing. poolOptions.beforePace is the moment between the two.
func TestPaceWait_CooldownsThatEndBetweenTheHealthyListAndTheWaitAreNotAnOutage(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	markEndpointFailure(p.h.eps[0].URL)
	markEndpointFailure(p.h.eps[1].URL)
	p.beforePace = func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	}
	res, err := dispatchWith(testCtx(t), p, (&urlCanary{}).fn, 4)
	if err != nil || len(res) != 4 {
		t.Fatalf("res=%d err=%v, want the four jobs answered: nothing was waited for", len(res), err)
	}
	if IsPoolUnavailable(err) {
		t.Errorf("err = %v: an outage claimed for cooldowns that had ended", err)
	}
}

// S1 of review 10 (D66). A lone job that hangs robin is not condemned on robin
// alone while a port whose machine is unknown (its /health could not be read when
// the dispatch began) has not been sent it: that port is no machine for the
// count, but it may be a second one, and it would have answered. The release of
// its /health happens when the first request is made, so it is read from then on.
func TestUnknownPort_ALoneJobThatHangsTheOnlyKnownMachineIsSentToAnUnknownPortBeforeItIsCondemned(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		p.h.healthDelay.Delete(strings.TrimPrefix(port1, "http://"))
		if g.Endpoint.URL == port0 {
			p.h.wedge(port0)
			return nil, &WorkerStuckError{URL: port0, After: time.Minute, Jobs: sortedIDs(sub)}
		}
		return p.send(ctx, g, sub, pf)
	}
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, poolOptions{canary: (&urlCanary{}).fn}, send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	r := res["chap1"]
	if r.Condemned || IsStrikeError(r.Error) {
		t.Fatalf("chap1 = %+v: condemned on %s alone without being tried on port 1, which would have answered", r, robinKey)
	}
	if r.Endpoint != port1 || r.Text != "T:chap1" {
		t.Errorf("chap1 = %+v, want it answered by port 1", r)
	}
}

// The same pool where the unknown port cannot take the job in this dispatch (it
// is in a cooldown that outlasts the pacing allowance): the job is deferred with
// no strike and no verdict, not condemned on the word of one machine.
func TestUnknownPort_ALoneJobThatHangsTheOnlyKnownMachineIsDeferredNotCondemnedWhenTheUnknownPortCannotTakeIt(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, 300*time.Millisecond)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port1 := p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	markEndpointFailure(port1)
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, poolOptions{canary: (&urlCanary{}).fn}, hangingChap1(p))
	if r, ok := res["chap1"]; ok {
		t.Errorf("chap1 = %+v: a verdict or a strike for a file only one machine hung on, with a port left untried", r)
	}
	if err == nil {
		t.Error("err = nil, want the job deferred with an error")
	}
	// Pinned: robin is wedged and the one port that could take the job is out for
	// the dispatch, which is the stuck pool the split parts end as when no endpoint
	// is left (the ops count it toward their stuck streak, and write nothing for
	// the book).
	if !IsWorkerStuck(err) || IsPoolUnavailable(err) {
		t.Errorf("err = %v, want a stuck pool", err)
	}
	if got := sentOnPort(p, port1); len(got) > 0 {
		t.Errorf("port 1 was in cooldown and was sent %v", got)
	}
}

// N2 of review 10. Port 2 is refused at the gate for what it offers (a pool that
// requires a gpu, a worker on the cpu); ports 0 and 1 change hands during the
// wait and the pool is left with nothing. The error named the gate's refusal of
// port 2 ("refused by capability requirements"), which sends an operator hunting
// a box that is not on a gpu, when what emptied the pool is the ports that
// changed hands.
func TestChangedHands_APoolEmptiedByPortsThatChangedHandsBesideACapabilityRefusalAtTheGateEndsAsChangedHands(t *testing.T) {
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := machineFaultPool(t, []string{robinMachine, batMachine, "dddd0004"}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1, port2 := p.h.eps[0].URL, p.h.eps[1].URL, p.h.eps[2].URL
	p.h.setHealthField(port2, map[string]any{"device": "cpu"})
	markEndpointFailure(port0)
	markEndpointFailure(port1)
	atFirstPace(t, p, 20*time.Millisecond, func() {
		p.h.setMachineID(port0, catwomanMachine)
		p.h.setMachineID(port1, "eeee0005")
		clearCooldown(port0)
		clearCooldown(port1)
	})
	jobs := map[string]string{"chap0": "u", "chap1": "u", "chap2": "u", "chap3": "u"}
	_, err := dispatchPool(testCtx(t), p.h.eps, []string{"gpu"}, jobs, p.opts((&urlCanary{}).fn), p.send)
	if !errors.Is(err, errPortChangedHands) {
		t.Fatalf("err = %v, want one that wraps errPortChangedHands", err)
	}
	if strings.Contains(err.Error(), "refused by capability requirements") {
		t.Errorf("err = %v: names a capability refusal as the reason the pool is empty", err)
	}
	if got := p.sendLog(); len(got) != 0 {
		t.Errorf("sends = %v, want nothing sent (port %s is on the cpu)", got, port2)
	}
}

// Review 11 of #4201. The part-placing path of the round loop (some port can take
// work, but not THESE split parts) asked how long to wait with the same hole the
// healthy list had: a port whose cooldown ended while recheck's probes were in
// flight is still waiting for its probe when the healthy list is made, so it is
// left out, and the wait found nothing in cooldown and ended the dispatch as a
// stuck pool although the port could take the part.

// placedAnyPool is a pool of robin on port 0, bat on port 1 and robin on port 2.
// Ports 1 and 2 are in a cooldown an earlier dispatch set (no failure of this
// dispatch), so the first round has port 0 only. chap1 alone hangs robin on
// port 0, which is wedged afterwards: stuck on one machine of two, so there is no
// verdict and the part must go to bat. Port 2, robin too, can never take it.
// During the round port 2's cooldown ends; port 1 is still in cooldown when the
// second round makes its healthy list (port 2 only).
func placedAnyPool(t *testing.T) (*faultPool, endpointSender[string]) {
	t.Helper()
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, 10*time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine, robinMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1, port2 := p.h.eps[0].URL, p.h.eps[1].URL, p.h.eps[2].URL
	markEndpointFailure(port1)
	markEndpointFailure(port2)
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		if _, ok := sub["chap1"]; ok && g.Endpoint.URL == port0 {
			p.h.wedge(port0)
			clearCooldown(port2)
			return nil, &WorkerStuckError{URL: port0, After: time.Minute, Jobs: sortedIDs(sub)}
		}
		return p.send(ctx, g, sub, pf)
	}
	return p, send
}

func requirePlacedOnBat(t *testing.T, p *faultPool, res map[string]BatchResult, err error) {
	t.Helper()
	port1 := p.h.eps[1].URL
	if err != nil {
		t.Errorf("err = %v (stuck=%v): bat on port 1 was out of cooldown and could take the part", err, IsWorkerStuck(err))
	}
	if r := res["chap1"]; r.Endpoint != port1 || r.Text != "T:chap1" {
		t.Errorf("chap1 = %+v, want it answered by bat on port 1", r)
	}
}

// Control: bat's cooldown ends in the first round too, so the second round probes
// it and the part goes there. Only the window below is the failure.
func TestPlacedAny_ThePartGoesToThePortWhoseCooldownEndedDuringTheRound(t *testing.T) {
	p, send := placedAnyPool(t)
	port1, port2 := p.h.eps[1].URL, p.h.eps[2].URL
	inner := send
	send = func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		clearCooldown(port1)
		clearCooldown(port2)
		return inner(ctx, g, sub, pf)
	}
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), send)
	requirePlacedOnBat(t, p, res, err)
}

// The window, taken at the moment the loop is about to ask how long to wait
// (beforePlacedPace): bat's cooldown ends there, and bat is cooled and unprobed.
func TestPlacedAny_ACooledPortWhoseCooldownEndsBeforeTheWaitIsProbedNotGivenUpOn(t *testing.T) {
	p, send := placedAnyPool(t)
	port1 := p.h.eps[1].URL
	var once sync.Once
	p.beforePlacedPace = func() { once.Do(func() { clearCooldown(port1) }) }
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), send)
	requirePlacedOnBat(t, p, res, err)
}

// The same window opened by the events that open it in production: bat's
// cooldown ends while the probe of port 2 (recheck's, in the second round) is in
// flight, after recheck made its list.
func TestPlacedAny_ACooldownThatEndsWhileRechecksProbesAreInFlightIsNotGivenUpOn(t *testing.T) {
	p, send := placedAnyPool(t)
	port0, port1, port2 := p.h.eps[0].URL, p.h.eps[1].URL, p.h.eps[2].URL
	var armed atomic.Bool
	var once sync.Once
	for n := 1; n <= 6; n++ {
		p.h.onHealthHit(port2, n, func() {
			if armed.Load() {
				once.Do(func() { clearCooldown(port1) })
			}
		})
	}
	inner := send
	send = func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		if _, ok := sub["chap1"]; ok && g.Endpoint.URL == port0 {
			armed.Store(true)
		}
		return inner(ctx, g, sub, pf)
	}
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), send)
	requirePlacedOnBat(t, p, res, err)
}

// S1 had a verdict that depended on order. The same evidence (robin hung chap1
// alone and is wedged; the unknown port only overran it, not wedged) condemned
// when the unknown port overran first and deferred when robin hung first: an
// overran request is no evidence about the file, so it must not make the unknown
// port "tried". Both orders end deferred (D69: ErrWorkerStuck, counted toward the
// ops' stuck streak), with no verdict and no strike.
func unknownPortOverranPool(t *testing.T) (*faultPool, string, string) {
	t.Helper()
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, 3*time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute) // "?" at the gate
	return p, port0, port1
}

func requireDeferredStuck(t *testing.T, res map[string]BatchResult, err error) {
	t.Helper()
	if r, ok := res["chap1"]; ok {
		t.Errorf("chap1 = %+v: a verdict or a strike on one machine's word, with the unknown port's request only overran", r)
	}
	if !IsWorkerStuck(err) || IsPoolUnavailable(err) {
		t.Errorf("err = %v, want a stuck pool (D69: no strike, counted toward the stuck streak)", err)
	}
}

func TestUnknownPort_AnUnknownPortThatOverranBeforeTheKnownMachineHungDefersTheJobLikeTheReverseOrder(t *testing.T) {
	p, port0, port1 := unknownPortOverranPool(t)
	markEndpointFailure(port0) // robin sits out the first round, so the unknown port is tried first
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		if _, ok := sub["chap1"]; ok {
			if g.Endpoint.URL == port1 {
				p.h.healthDelay.Delete(strings.TrimPrefix(port1, "http://")) // readable, not wedged: overran
				clearCooldown(port0)
				return nil, &WorkerStuckError{URL: port1, After: time.Minute, Jobs: sortedIDs(sub)}
			}
			p.h.wedge(port0)
			return nil, &WorkerStuckError{URL: port0, After: time.Minute, Jobs: sortedIDs(sub)}
		}
		return p.send(ctx, g, sub, pf)
	}
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), send)
	requireDeferredStuck(t, res, err)
}

func TestUnknownPort_AKnownMachineThatHungBeforeTheUnknownPortOverranDefersTheJob(t *testing.T) {
	p, port0, port1 := unknownPortOverranPool(t)
	eps := []Endpoint{p.h.eps[0], p.h.eps[1]}
	eps[1].Priority = 5 // robin first
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		if _, ok := sub["chap1"]; ok {
			if g.Endpoint.URL == port1 {
				p.h.healthDelay.Delete(strings.TrimPrefix(port1, "http://"))
				return nil, &WorkerStuckError{URL: port1, After: time.Minute, Jobs: sortedIDs(sub)}
			}
			p.h.wedge(port0)
			return nil, &WorkerStuckError{URL: port0, After: time.Minute, Jobs: sortedIDs(sub)}
		}
		return p.send(ctx, g, sub, pf)
	}
	res, err := dispatchPool(testCtx(t), eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), send)
	requireDeferredStuck(t, res, err)
}

// SF1: an unknown port in cooldown can never take the job in this dispatch (read
// after the wait it answers as a different worker than the nothing it was frozen
// as; still unreadable it is not used), so the dispatch must not wait the whole
// pacing allowance (six minutes in production) for it before ending the same way.
func TestUnknownPort_ADispatchDoesNotWaitThePacingAllowanceForAnUnknownPortThatCanNeverTakeTheJob(t *testing.T) {
	freshCanaryMemory(t)
	shortenHealthProbe(t, 150*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, 2*time.Second)
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	port1 := p.h.eps[1].URL
	p.h.delayHealth(port1, time.Minute)
	markEndpointFailure(port1)
	start := time.Now()
	res, err := dispatchPool(testCtx(t), p.h.eps, nil, map[string]string{"chap1": "u"}, p.opts((&urlCanary{}).fn), hangingChap1(p))
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %s: waited for a port that regate can only drop (pacing allowance %s)", took.Round(10*time.Millisecond), poolPaceMax)
	}
	requireDeferredStuck(t, res, err)
}
