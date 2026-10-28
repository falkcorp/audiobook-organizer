// file: internal/transcribe/canary_held_drops_test.go
// version: 1.0.0
// guid: 82aa7014-9683-4ef7-9977-e5c5f5325624
// last-edited: 2026-10-28

package transcribe

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// heldDropsRun is one dispatch of the scenario below and what the pool looked like
// at the two moments that matter: when the canary probes of the all-sick round
// were over (the round's answers are still to be read) and at the top of each
// pass of the round loop.
type heldDropsRun struct {
	err                        error
	port0, port1               string
	canaryAsked                []string
	afterCanaries              []string
	soloAfterCanaries          int
	roundStarts                [][]string
	soloAtRoundStart           []int
	roundsStartedAfterCanaries int
}

// runHeldDrops dispatches 16 looper jobs to robin's port 0 and bat's port 1 (two
// machines, soloLimit 2). Every endpoint ends up sick, so the canary probes run,
// and what they probe finds the machines changed: port 0 answers as catwoman
// (another dispatch failed it and its cooldown ended inside the round, so only
// its failure generation says it must be probed), and with bothChange port 1
// answers as another machine too.
//
// Port 0 faults on timeouts in round 1 (port 1 is idle then); in round 2 port 1
// takes the requeued jobs and faults worker_misconfigured while the machines
// change inside the round, so every endpoint is sick when its answers are in.
func runHeldDrops(t *testing.T, bothChange bool) *heldDropsRun {
	t.Helper()
	freshCanaryMemory(t)
	shortenPacing(t, 10*time.Millisecond, 5*time.Second)
	run := &heldDropsRun{}
	var (
		mu       sync.Mutex
		round1   = map[string]bool{}
		switched atomic.Bool
	)
	answer := func(url, id string) BatchResult {
		if url == run.port0 && !switched.Load() {
			return BatchResult{Error: timeoutText}
		}
		if url == run.port1 && switched.Load() {
			return BatchResult{Error: PathErrWorkerMisconfigured + pathErrSeparator + "no decoder"}
		}
		return BatchResult{Text: "T:" + id}
	}
	p := machineFaultPool(t, []string{robinMachine, batMachine}, answer)
	clearCooldownsAtEnd(t, p)
	run.port0, run.port1 = p.h.eps[0].URL, p.h.eps[1].URL
	// switchMachines is what another dispatch does inside the round: it fails the
	// ports, a different machine takes them over, and their cooldowns end.
	switchMachines := func() {
		markEndpointFailure(run.port0)
		p.h.setMachineID(run.port0, catwomanMachine)
		clearCooldown(run.port0)
		if bothChange {
			markEndpointFailure(run.port1)
			p.h.setMachineID(run.port1, robinMachine)
			clearCooldown(run.port1)
		}
		switched.Store(true)
	}
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
	var once sync.Once
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		u := g.Endpoint.URL
		switch {
		case u == run.port0 && !switched.Load():
			mu.Lock()
			for id := range sub {
				round1[id] = true
			}
			mu.Unlock()
		case u == run.port1 && !switched.Load():
			mu.Lock()
			later := false
			for id := range sub {
				later = later || round1[id]
			}
			mu.Unlock()
			if later {
				once.Do(switchMachines)
			}
		}
		return reply(u, sub)
	}
	c := &urlCanary{}
	canaried := false
	p.afterCanaries = func(ordered []string, solo int) {
		canaried = true
		run.afterCanaries, run.soloAfterCanaries = slices.Clone(ordered), solo
	}
	p.atRoundStart = func(ordered []string, solo int) {
		run.roundStarts = append(run.roundStarts, slices.Clone(ordered))
		run.soloAtRoundStart = append(run.soloAtRoundStart, solo)
		if canaried {
			run.roundsStartedAfterCanaries++
		}
	}
	jobs := map[string]string{}
	for _, id := range loopers(16, "chap") {
		jobs[id] = "u"
	}
	_, run.err = dispatchPool(testCtx(t), p.h.eps, nil, jobs, p.opts(c.fn), send)
	run.canaryAsked = c.askedPorts()
	if !switched.Load() {
		t.Fatalf("setup did not switch the machines (err = %v)", run.err)
	}
	if run.afterCanaries == nil {
		t.Fatalf("setup did not reach the canary probes (err = %v)", run.err)
	}
	return run
}

// The ports a canary probe finds changed hands are dropped after the round's
// answers are read, not before: while they are read the dispatch is the one the
// answers came from (port 0 is still a port of it, and soloLimit is still the two
// machines it began with, which a lone job's condemnation counts against), and
// the next pass of the round loop is the first without the port.
func TestRunCanaries_APortFoundChangedHandsIsDroppedAfterTheRoundsAnswersAreRead(t *testing.T) {
	run := runHeldDrops(t, false)
	if slices.Contains(run.canaryAsked, run.port0) {
		t.Errorf("the canary clip was sent to port 0, which answers as another machine: asked=%v", run.canaryAsked)
	}
	if !slices.Contains(run.afterCanaries, run.port0) || run.soloAfterCanaries != 2 {
		t.Errorf("while the round's answers are read the pool is %v with soloLimit %d, want port 0 still in it and soloLimit 2",
			run.afterCanaries, run.soloAfterCanaries)
	}
	if run.roundsStartedAfterCanaries == 0 {
		t.Fatalf("no pass of the round loop followed the canaries (err = %v)", run.err)
	}
	n := len(run.roundStarts) - run.roundsStartedAfterCanaries
	for i := n; i < len(run.roundStarts); i++ {
		if slices.Contains(run.roundStarts[i], run.port0) || run.soloAtRoundStart[i] != 1 {
			t.Errorf("pass %d began with the pool %v and soloLimit %d, want port 0 gone and soloLimit 1",
				i, run.roundStarts[i], run.soloAtRoundStart[i])
		}
	}
}

// When the ports the probes drop are every port, the pool ends at the top of the
// next pass as the pool emptied (a deferral for ports that changed hands, not an
// outage), and not in the middle of the round whose answers are still to be read.
func TestRunCanaries_PortsFoundChangedHandsThatEmptyThePoolEndItAfterTheRoundsAnswersAreRead(t *testing.T) {
	run := runHeldDrops(t, true)
	if len(run.canaryAsked) != 0 {
		t.Errorf("the canary clip was sent to a port that answers as another machine: asked=%v", run.canaryAsked)
	}
	if !slices.Contains(run.afterCanaries, run.port0) || !slices.Contains(run.afterCanaries, run.port1) {
		t.Errorf("while the round's answers are read the pool is %v, want both ports still in it", run.afterCanaries)
	}
	if run.roundsStartedAfterCanaries != 0 {
		t.Errorf("%d pass(es) of the round loop began after every port was dropped", run.roundsStartedAfterCanaries)
	}
	if !errors.Is(run.err, errPortChangedHands) || errors.Is(run.err, ErrPoolUnavailable) {
		t.Errorf("err = %v, want the changed-hands deferral (the pool emptied), not an outage", run.err)
	}
}
