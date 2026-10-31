// file: internal/transcribe/canary_cooldown_test.go
// version: 1.1.2
// guid: 768f5a2c-e381-4b86-8e01-15df198a14cb
// last-edited: 2026-10-30

package transcribe

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Review 6 of #4193, nits 1 to 3: what the dispatcher does with a machine that
// failed the canary clip in an earlier dispatch (canaryFailed) when one of its
// ports is in cooldown, when a port of it was refused at the gate, and when its
// worker changes identity.

const batMachine = "aaaa0001"

// rememberedMachine is a pool of `ports` ports of one machine that failed the
// canary clip in an earlier dispatch, every worker answering every clip with a
// transcript. Two ports at least: a lone port goes through dispatchSingle (see
// TestSingle_*), which has no stand-in to consider.
func rememberedMachine(t *testing.T, ports int) *faultPool {
	t.Helper()
	machines := slices.Repeat([]string{batMachine}, ports)
	p := machineFaultPool(t, machines, func(_ string, id string) BatchResult { return BatchResult{Text: "T:" + id} })
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	markCanaryFailed("m:" + batMachine)
	return p
}

// canaryRecorder is a canaryFunc that records every port it was asked and
// whether the port was still in cooldown at that moment, and answers with err.
type canaryRecorder struct {
	mu         sync.Mutex
	asked      []string
	askedCooly []string // the ports that were in cooldown when asked
	err        error
}

func (c *canaryRecorder) fn(_ context.Context, g gatedEndpoint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, g.Endpoint.URL)
	if endpointInCooldown(g.Endpoint.URL) {
		c.askedCooly = append(c.askedCooly, g.Endpoint.URL)
	}
	return c.err
}

func (c *canaryRecorder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.asked)
}

func dispatchWith(ctx context.Context, p *faultPool, canary canaryFunc, n int) (map[string]BatchResult, error) {
	return dispatchWithOpts(ctx, p, p.opts(canary), n)
}

// dispatchWithOpts is dispatchWith with the dispatch's options in hand, for a
// test that hooks the gate (poolOptions.extraRefusal).
func dispatchWithOpts(ctx context.Context, p *faultPool, opts poolOptions, n int) (map[string]BatchResult, error) {
	jobs := make(map[string]string, n)
	for _, id := range loopers(n, "chap") {
		jobs[id] = "unused"
	}
	return dispatchPool(ctx, p.h.eps, nil, jobs, opts, p.send)
}

func requireNoStrikes(t *testing.T, res map[string]BatchResult) {
	t.Helper()
	for id, r := range res {
		if IsStrikeError(r.Error) {
			t.Errorf("%s = %+v: a strike", id, r)
		}
	}
}

// N1: the only machine in the pool failed the canary in an earlier dispatch and
// has recovered, but one of its ports is in cooldown. The dispatch waits the
// cooldown out and asks every port, instead of benching the machine unasked and
// ending as ErrPoolUnavailable at once (which the book op counts toward its
// five-page stop).
func TestPreflight_ARememberedLoneMachineWithAPortInCooldownWaitsThenPassesTheCanary(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	markEndpointFailure(p.h.eps[1].URL) // a real 30 s cooldown
	go func() {
		time.Sleep(80 * time.Millisecond)
		clearCooldown(p.h.eps[1].URL) // the cooldown ends
	}()
	rec := &canaryRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v, want the dispatch to wait out the cooldown and then work", err)
	}
	if rec.count() != len(p.h.eps) {
		t.Errorf("canaries = %v, want one to every port after the cooldown", rec.asked)
	}
	if len(rec.askedCooly) != 0 {
		t.Errorf("ports asked while still in cooldown: %v", rec.askedCooly)
	}
	for _, id := range loopers(8, "chap") {
		if res[id].Text != "T:"+id {
			t.Errorf("%s = %+v, want its transcript", id, res[id])
		}
	}
	if failedCanaryBefore("m:" + batMachine) {
		t.Error("the machine answered on every port and must be forgotten")
	}
}

// The same machine, still sick when the cooldown ends: unavailable, as before
// the wait existed, and remembered.
func TestPreflight_ARememberedLoneMachineStillSickAfterItsCooldownIsUnavailable(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	markEndpointFailure(p.h.eps[1].URL)
	go func() {
		time.Sleep(60 * time.Millisecond)
		clearCooldown(p.h.eps[1].URL)
	}()
	rec := &canaryRecorder{err: errors.New("canary clip: " + timeoutText)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, rec.fn, 8)
	if !IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want ErrPoolUnavailable", err)
	}
	if rec.count() == 0 || len(rec.askedCooly) != 0 {
		t.Errorf("canaries = %v (in cooldown when asked: %v), want it asked after the cooldown", rec.asked, rec.askedCooly)
	}
	if n := len(p.sendLog()); n != 0 {
		t.Errorf("%d requests sent to a machine that failed the canary", n)
	}
	requireNoStrikes(t, res)
	if !failedCanaryBefore("m:" + batMachine) {
		t.Error("a machine that failed the canary again must stay remembered")
	}
}

// A cooldown that outlasts the pacing allowance is no verdict, like a canary
// that cannot get a slot: not ErrPoolUnavailable, which counts toward the ops'
// stop limits; nothing is asked or sent, and the machine stays remembered.
func TestPreflight_ACooldownLongerThanThePacingAllowanceIsNoVerdict(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, 80*time.Millisecond)
	p := rememberedMachine(t, 2)
	markEndpointFailure(p.h.eps[1].URL) // 30 s: the allowance runs out first
	rec := &canaryRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, rec.fn, 8)
	if !errors.Is(err, errCanarySlotBusy) {
		t.Fatalf("err = %v, want the no-verdict deferral (errCanarySlotBusy)", err)
	}
	te, ok := errors.AsType[*TransportError](err)
	if !ok || te.Recognized || IsPoolUnavailable(err) || IsWorkerStuck(err) || IsWorkerFault(err) {
		t.Fatalf("err = %v (TransportError=%v): want an unrecognized error the ops defer on without counting", err, ok)
	}
	if rec.count() != 0 || len(p.sendLog()) != 0 {
		t.Errorf("canaries = %v, sends = %d: nothing may be asked or sent while a port is in cooldown", rec.asked, len(p.sendLog()))
	}
	requireNoStrikes(t, res)
	if !failedCanaryBefore("m:" + batMachine) {
		t.Error("no verdict must leave the machine remembered")
	}
}

// A cancelled op returns the cancellation from the wait: not a verdict about
// the machine, so not ErrPoolUnavailable, and nothing is asked or sent.
func TestPreflight_ACancelDuringTheCooldownWaitReturnsTheCancellation(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	markEndpointFailure(p.h.eps[1].URL)
	rec := &canaryRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := dispatchWith(ctx, p, rec.fn, 8)
	if !errors.Is(err, context.Canceled) || IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want the cancellation and not ErrPoolUnavailable", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s to notice the cancel", d)
	}
	if rec.count() != 0 || len(p.sendLog()) != 0 {
		t.Errorf("canaries = %v, sends = %d: a cancelled wait asks and sends nothing", rec.asked, len(p.sendLog()))
	}
	if !failedCanaryBefore("m:" + batMachine) {
		t.Error("a cancelled op is no evidence: the machine stays remembered")
	}
}

// With another machine in the pool that is not known to be sick, a remembered
// machine with a port in cooldown is benched unasked as before: the page must
// not wait up to five minutes for a machine the other one can stand in for.
func TestPreflight_ARememberedMachineInCooldownDoesNotMakeAPageWaitWhenAnotherMachineCanWork(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	const llm1 = "bbbb0002"
	p := machineFaultPool(t, []string{batMachine, batMachine, llm1}, func(_ string, id string) BatchResult { return BatchResult{Text: "T:" + id} })
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	markCanaryFailed("m:" + batMachine)
	markEndpointFailure(p.h.eps[1].URL)
	rec := &canaryRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := dispatchWith(ctx, p, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v, want the sound machine to take the page without waiting", err)
	}
	if rec.count() != 0 {
		t.Errorf("canaries = %v, want none: the machine with a port in cooldown is benched unasked", rec.asked)
	}
	for _, id := range loopers(8, "chap") {
		if r := res[id]; r.Text != "T:"+id || r.Endpoint != p.h.eps[2].URL {
			t.Errorf("%s = %+v, want the sound machine's transcript", id, r)
		}
	}
	if !failedCanaryBefore("m:" + batMachine) {
		t.Error("benched with nothing learned: it stays remembered")
	}
}

// N2: only some of a remembered machine's ports are eligible (a sibling is
// wedged, so the gate refuses it). The ports that answer must not make the
// dispatcher forget the machine: the sibling has not answered, and the next
// dispatch, when it is eligible again, asks every port before it gets work.
func TestPreflight_AMachineIsNotForgottenWhileAPortTheGateRefusedHasNotAnswered(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports int
	}{
		{"one eligible port (dispatchSingle)", 2},
		{"two eligible ports (pool preflight)", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := rememberedMachine(t, tc.ports)
			last := p.h.eps[tc.ports-1].URL
			p.h.wedge(last)
			rec := &canaryRecorder{}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res, err := dispatchWith(ctx, p, rec.fn, 8)
			if err != nil {
				t.Fatalf("err = %v, want the ports that answered to take work", err)
			}
			for _, id := range loopers(8, "chap") {
				if res[id].Text != "T:"+id {
					t.Errorf("%s = %+v, want its transcript", id, res[id])
				}
			}
			if got := rec.count(); got != tc.ports-1 {
				t.Errorf("canaries = %v, want one to each eligible port", rec.asked)
			}
			if !failedCanaryBefore("m:" + batMachine) {
				t.Fatal("forgotten on the answer of some of its ports: the refused one has not answered")
			}

			// The wedged port recovers: the next dispatch asks every port.
			p.h.wedged.Delete(strings.TrimPrefix(last, "http://"))
			before := rec.count()
			if _, err := dispatchWith(ctx, p, rec.fn, 8); err != nil {
				t.Fatalf("second dispatch: err = %v", err)
			}
			if got := rec.count() - before; got != tc.ports {
				t.Errorf("second dispatch sent %d canaries, want one to every port (%d)", got, tc.ports)
			}
			if failedCanaryBefore("m:" + batMachine) {
				t.Error("every port answered: the machine must be forgotten")
			}
		})
	}
}

// A refused port of ANOTHER machine says nothing about this one.
func TestPreflight_ARefusedPortOfAnotherMachineDoesNotKeepAMachineRemembered(t *testing.T) {
	const llm1 = "bbbb0002"
	p := machineFaultPool(t, []string{batMachine, llm1}, func(_ string, id string) BatchResult { return BatchResult{Text: "T:" + id} })
	markCanaryFailed("m:" + batMachine)
	p.h.wedge(p.h.eps[1].URL)
	rec := &canaryRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := dispatchWith(ctx, p, rec.fn, 8); err != nil {
		t.Fatalf("err = %v", err)
	}
	if failedCanaryBefore("m:" + batMachine) {
		t.Error("the machine answered on every one of its ports: it must be forgotten")
	}
}

func TestHasUnheardPort(t *testing.T) {
	const (
		bat    = "m:aaaa0001"
		robin  = "m:bbbb0002"
		portA  = "http://gotham-a:8000"
		portB  = "http://gotham-b:8000"
		portC  = "http://gotham-c:8000"
		nobody = ""
	)
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	markCanaryFailed(bat, portA)
	refuse := func(url, machine string) gpuRefusal { return gpuRefusal{URL: url, Machine: machine} }
	unprobedKept := func(url string) gatedEndpoint { return gatedEndpoint{Endpoint: Endpoint{URL: url}} }
	for _, tc := range []struct {
		name    string
		refused []gpuRefusal
		kept    []gatedEndpoint
		want    bool
	}{
		{"no refusals", nil, nil, false},
		{"a refusal that names another machine", []gpuRefusal{refuse(portB, robin)}, nil, false},
		{"a refusal that names this machine", []gpuRefusal{refuse(portB, bat)}, nil, true},
		{"an unprobed refusal that is a known port of this machine", []gpuRefusal{refuse(portA, nobody)}, nil, true},
		{"an unprobed refusal nothing has tied to this machine", []gpuRefusal{refuse(portC, nobody)}, nil, false},
		{"an unprobed kept endpoint that is a known port of this machine", nil, []gatedEndpoint{unprobedKept(portA)}, true},
		{"an unprobed kept endpoint nothing has tied to this machine", nil, []gatedEndpoint{unprobedKept(portC)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasUnheardPort(bat, tc.refused, tc.kept); got != tc.want {
				t.Errorf("hasUnheardPort = %v, want %v", got, tc.want)
			}
		})
	}
	if hasUnheardPort(robin, []gpuRefusal{refuse(portA, nobody)}, nil) {
		t.Error("a machine that is not remembered has no ports to wait for")
	}
}

// N3 and B1: an entry under an identity the endpoint no longer has moves to the
// identity it has now once the worker reports a machine_id, so canaryFailed
// cannot grow with dead identities, and the failure goes with it: the worker
// switches from "u:" to "m:" inside one process (it leaves machine_id out of
// /health until its first lookup succeeds), so a machine that failed the canary
// a moment ago may report a new identity with nothing repaired. Another
// endpoint's entries stay, and so does an endpoint nothing was remembered for.
func TestPool_StaleCanaryIdentitiesMoveToTheWorkersNewIdentity(t *testing.T) {
	p := machineFaultPool(t, []string{batMachine, "cccc0003"}, func(_ string, id string) BatchResult { return BatchResult{Text: "T:" + id} })
	h := shortHash(p.h.eps[0].URL)
	markCanaryFailed("u:" + h)      // the worker's identity before it reported a machine_id
	markCanaryFailed("?" + h)       // and before its /health could be read
	markCanaryFailed("u:unrelated") // another endpoint's
	markCanaryFailed("m:dddd0004")  // another machine's
	// Every machine fails the canary, so a remembered one is asked and stays
	// remembered; the other worker takes the page.
	rec := &canaryRecorder{err: errors.New("canary clip: " + timeoutText)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := dispatchWith(ctx, p, rec.fn, 8); err != nil {
		t.Fatalf("err = %v, want the other machine to take the page", err)
	}
	if want := []string{p.h.eps[0].URL}; !slices.Equal(rec.asked, want) {
		t.Fatalf("canaries = %v, want only the worker whose failure was carried (%v)", rec.asked, want)
	}
	for key, want := range map[string]bool{
		"u:" + h: false, "?" + h: false, // dead identities
		"m:" + batMachine: true,  // where the failure went
		"m:cccc0003":      false, // never failed
		"u:unrelated":     true, "m:dddd0004": true,
	} {
		if got := failedCanaryBefore(key); got != want {
			t.Errorf("remembered(%q) = %v, want %v", key, got, want)
		}
	}
	if n := rememberedCanaryFailures(); n != 3 {
		t.Errorf("%d entries remain, want the carried one and the two that are not this endpoint's", n)
	}
}

// pruneStaleCanaryIdentities on its own: the carried entry has a new generation
// and keeps the old entry's ports, and an endpoint with nothing remembered under
// an old identity gets no entry.
func TestPruneStaleCanaryIdentities_CarriesTheFailureAndItsPorts(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-a:8000"
	h := shortHash(url)
	probed := gatedEndpoint{Endpoint: Endpoint{URL: url}, Probed: true, Health: remoteHealth{MachineID: batMachine}}

	pruneStaleCanaryIdentities(probed)
	if n := rememberedCanaryFailures(); n != 0 {
		t.Fatalf("%d entries after pruning with nothing remembered, want none", n)
	}

	markCanaryFailed("u:"+h, "http://gotham-b:8000")
	before := canaryGeneration("u:" + h)
	pruneStaleCanaryIdentities(probed)
	if failedCanaryBefore("u:" + h) {
		t.Error("the old identity was kept")
	}
	if got := canaryGeneration("m:" + batMachine); got <= before {
		t.Errorf("generation = %d, want a new one after %d: a carried failure is a new record", got, before)
	}
	for _, port := range []string{url, "http://gotham-b:8000"} {
		if !hasUnheardPort("m:"+batMachine, []gpuRefusal{{URL: port}}, nil) {
			t.Errorf("%s was not carried as a port of the new identity", port)
		}
	}
}

// A worker that still reports no machine_id keeps its live "u:" identity (the
// canary fails again, so the entry is still wanted); only the "?" key from
// before its /health could be read is dropped.
func TestPool_ALiveCanaryIdentityIsNotPrunedAsStale(t *testing.T) {
	p := machineFaultPool(t, []string{"", "cccc0003"}, func(_ string, id string) BatchResult { return BatchResult{Text: "T:" + id} })
	h := shortHash(p.h.eps[0].URL)
	markCanaryFailed("u:" + h)
	markCanaryFailed("?" + h)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rec := &canaryRecorder{err: errors.New("canary clip: " + timeoutText)}
	if _, err := dispatchWith(ctx, p, rec.fn, 8); err != nil {
		t.Fatalf("err = %v, want the other machine to take the page", err)
	}
	if rec.count() != 1 {
		t.Fatalf("canaries = %v, want the one to the remembered port", rec.asked)
	}
	if !failedCanaryBefore("u:" + h) {
		t.Error("the worker's live identity was pruned")
	}
	if failedCanaryBefore("?" + h) {
		t.Error("the identity from before the worker could be probed was kept")
	}
}
