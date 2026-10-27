// file: internal/transcribe/canary_memory_test.go
// version: 1.0.0
// guid: 5b0f7c1e-2d64-4f0b-9a57-3c8e1d6a9b42
// last-edited: 2026-10-26

package transcribe

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

// The follow-up to review 6 of #4193 (reviews of d19492374): what the dispatcher
// remembers about a machine that failed the canary clip (generation, ports),
// what it does with a remembered machine it has set aside for a stand-in, and
// how it waits for a remembered machine's cooldown.

const (
	robinMachine = "bbbb0002"
	batKey       = "m:" + batMachine
	robinKey     = "m:" + robinMachine
)

func answerWithTranscripts(_ string, id string) BatchResult { return BatchResult{Text: "T:" + id} }

// after runs fn in d, unless the test is over first.
func after(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		<-done
	})
	go func() {
		defer close(done)
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			fn()
		case <-stop:
		}
	}()
}

// poolOf is a machineFaultPool over machines whose cooldowns are cleared when
// the test ends.
func poolOf(t *testing.T, machines ...string) *faultPool {
	t.Helper()
	p := machineFaultPool(t, machines, answerWithTranscripts)
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	return p
}

func dispatchOn(ctx context.Context, eps []Endpoint, requires []string, send endpointSender[string], canary canaryFunc, n int) (map[string]BatchResult, error) {
	jobs := make(map[string]string, n)
	for _, id := range loopers(n, "chap") {
		jobs[id] = "unused"
	}
	return dispatchPool(ctx, eps, requires, jobs, poolOptions{canary: canary}, send)
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func requireTranscripts(t *testing.T, res map[string]BatchResult, n int) {
	t.Helper()
	for _, id := range loopers(n, "chap") {
		if res[id].Text != "T:"+id {
			t.Errorf("%s = %+v, want its transcript", id, res[id])
		}
	}
}

// S1: on a pool that requires a GPU an unreachable endpoint is refused with no
// machine. One that nothing ties to the remembered machine says nothing about
// it, so the machine is forgotten once every port that was asked answered.
func TestCanaryMemory_AnUnrelatedUnreachableEndpointDoesNotKeepAMachineRemembered(t *testing.T) {
	p := poolOf(t, batMachine, batMachine)
	markCanaryFailed(batKey)
	eps := append(slices.Clone(p.h.eps), Endpoint{URL: deadURL(t), Concurrency: 1, Priority: 9, Label: "arkham"})
	rec := &canaryRecorder{}
	res, err := dispatchOn(testCtx(t), eps, []string{"gpu"}, p.send, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if rec.count() != 2 {
		t.Errorf("canaries = %v, want one to each of the machine's ports", rec.asked)
	}
	if failedCanaryBefore(batKey) {
		t.Error("kept remembered for an unreachable endpoint that no probe ever tied to the machine")
	}
}

// S1: the machine's own known port, once its /health cannot be read, is still
// unheard: the machine stays remembered while it is down.
func TestCanaryMemory_AKnownPortThatIsNowUnreachableKeepsTheMachineRemembered(t *testing.T) {
	for _, tc := range []struct {
		name     string
		requires []string
	}{
		{"refused at the gate (a pool that requires a gpu)", []string{"gpu"}},
		{"kept unprobed (a pool with no requirement)", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := poolOf(t, batMachine, batMachine, batMachine)
			down := p.h.eps[2].URL
			markCanaryFailed(batKey, down) // a probe once reported it as the machine's
			p.h.srvs[2].Close()
			rec := &canaryRecorder{}
			res, err := dispatchOn(testCtx(t), p.h.eps, tc.requires, p.send, rec.fn, 8)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			requireTranscripts(t, res, 8)
			if rec.count() != 2 || slices.Contains(rec.asked, down) {
				t.Errorf("canaries = %v, want one to each of the two ports that are up", rec.asked)
			}
			if !failedCanaryBefore(batKey) {
				t.Error("forgotten while one of its ports is down and has not answered")
			}
		})
	}
}

// N-e, the other side: with no requirement an unprobed endpoint is kept, and one
// that was never tied to the machine does not keep it remembered either.
func TestCanaryMemory_AnUnrelatedUnprobedKeptEndpointDoesNotKeepAMachineRemembered(t *testing.T) {
	p := poolOf(t, batMachine, batMachine)
	markCanaryFailed(batKey)
	eps := append(slices.Clone(p.h.eps), Endpoint{URL: deadURL(t), Concurrency: 1, Priority: 9, Label: "arkham"})
	rec := &canaryRecorder{}
	if _, err := dispatchOn(testCtx(t), eps, nil, p.send, rec.fn, 8); err != nil {
		t.Fatalf("err = %v", err)
	}
	if failedCanaryBefore(batKey) {
		t.Error("kept remembered for an unprobed endpoint that no probe ever tied to the machine")
	}
}

// A URL belongs to one machine at a time: once a probe reports it under another
// machine it is no longer the first machine's port.
func TestCanaryMemory_APortThatChangesMachinesLeavesTheOldOnesSet(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-a:8000"
	markCanaryFailed(batKey, url)
	markCanaryFailed(robinKey)
	noteCanaryPort(robinKey, url)
	if hasUnheardPort(batKey, []gpuRefusal{{URL: url}}, nil) {
		t.Error("the port was still counted for the machine it left")
	}
	if !hasUnheardPort(robinKey, []gpuRefusal{{URL: url}}, nil) {
		t.Error("the port was not counted for the machine it joined")
	}
}

// S3: the generation. A pass forgets a machine only if no failure of it was
// recorded since the dispatch began to ask.
func TestCanaryMemory_APassDoesNotEraseANewerFailure(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)

	markCanaryFailed(batKey)
	gen := canaryGeneration(batKey)
	markCanaryFailed(batKey) // another dispatch's failure of a machine already remembered
	if forgetCanaryIf(batKey, gen, nil, nil) || !failedCanaryBefore(batKey) {
		t.Fatal("a newer failure of an already remembered machine was erased by an older answer")
	}
	if !forgetCanaryIf(batKey, canaryGeneration(batKey), nil, nil) || failedCanaryBefore(batKey) {
		t.Fatal("an answer to the latest failure must forget the machine")
	}

	// ABA: the entry is cleared and written again while the dispatch waits. The
	// generation never repeats, so the new failure is not mistaken for the old.
	markCanaryFailed(batKey)
	gen = canaryGeneration(batKey)
	clearCanaryFailed(batKey)
	markCanaryFailed(batKey)
	if forgetCanaryIf(batKey, gen, nil, nil) || !failedCanaryBefore(batKey) {
		t.Fatal("a failure recorded after the entry was cleared was erased by an answer from before")
	}

	// A machine that was not remembered when the dispatch asked (generation 0)
	// and was marked failed by another dispatch since.
	clearCanaryFailed(batKey)
	gen = canaryGeneration(batKey)
	if gen != 0 {
		t.Fatalf("generation of a machine that is not remembered = %d, want 0", gen)
	}
	markCanaryFailed(batKey)
	if forgetCanaryIf(batKey, gen, nil, nil) || !failedCanaryBefore(batKey) {
		t.Fatal("a failure recorded after the dispatch asked was erased by it")
	}
}

// S3 through the dispatchers: another dispatch records a failure of the machine
// while this one's canary is in flight; this one's pass must not erase it.
func TestCanaryMemory_ADispatchDoesNotForgetAFailureRecordedWhileItAsked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports int
	}{
		{"pool preflight", 2},
		{"lone endpoint", 1},
	} {
		for _, aba := range []bool{false, true} {
			name := tc.name + " newer failure"
			if aba {
				name = tc.name + " cleared and failed again"
			}
			t.Run(name, func(t *testing.T) {
				p := rememberedMachine(t, tc.ports)
				var rec canaryRecorder
				canary := func(ctx context.Context, g gatedEndpoint) error {
					err := rec.fn(ctx, g)
					if aba {
						clearCanaryFailed(batKey)
					}
					markCanaryFailed(batKey)
					return err
				}
				res, err := dispatchWith(testCtx(t), p, canary, 8)
				if err != nil {
					t.Fatalf("err = %v, want the machine that answered to take work", err)
				}
				requireTranscripts(t, res, 8)
				if rec.count() != tc.ports {
					t.Errorf("canaries = %v, want one per port", rec.asked)
				}
				if !failedCanaryBefore(batKey) {
					t.Error("a failure another dispatch recorded while this one asked was erased by this one's answer")
				}
			})
		}
	}
}

// S3, the other direction: this dispatch's canary failed, and another dispatch
// answered the machine's canary and cleared it while this one asked. The failure
// this dispatch saw is recorded again, not lost.
func TestCanaryMemory_AFailureIsRecordedAgainWhenAnotherDispatchClearedTheMachineMeanwhile(t *testing.T) {
	for _, ports := range []int{3, 1} {
		name := "pool preflight"
		if ports == 1 {
			name = "lone endpoint"
		}
		t.Run(name, func(t *testing.T) {
			p := rememberedMachine(t, ports)
			canary := func(_ context.Context, _ gatedEndpoint) error {
				clearCanaryFailed(batKey)
				return errors.New("canary clip: " + timeoutText)
			}
			_, err := dispatchWith(testCtx(t), p, canary, 8)
			if !IsPoolUnavailable(err) {
				t.Fatalf("err = %v, want ErrPoolUnavailable: the only machine failed its canary", err)
			}
			if !failedCanaryBefore(batKey) {
				t.Fatal("the failure this dispatch saw was lost to another dispatch's clear")
			}
			if ports == 1 {
				return
			}
			// The entry was created afresh, so the ports this dispatch saw are the
			// only record of them (S1 at mark time): one that is unreachable in the
			// next dispatch has not answered, and the machine stays remembered.
			for _, ep := range p.h.eps {
				clearCooldown(ep.URL)
			}
			p.h.srvs[2].Close()
			before := len(p.canaryLog())
			res, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, p.canary, 8)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			requireTranscripts(t, res, 8)
			if asked := p.canaryLog()[before:]; len(asked) != 2 || slices.Contains(asked, p.h.eps[2].URL) {
				t.Errorf("canaries = %v, want one to each of the two ports that are up", asked)
			}
			if !failedCanaryBefore(batKey) {
				t.Error("forgotten while a port this dispatch saw as the machine's is down and has not answered")
			}
		})
	}
}

// S1 at mark time on the lone endpoint's paths: a machine whose sibling port
// the gate refused (wedged) and whose lone eligible port failed the canary is
// recorded with both ports. When the sibling is unreachable in the next
// dispatch the lone port's answer does not vouch for the machine.
func TestCanaryMemory_ALoneEndpointRecordsItsRefusedSiblingWhenItFailsTheCanary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) *faultPool
	}{
		{"a remembered machine's canary fails again, another dispatch having cleared it", func(t *testing.T) *faultPool {
			p := rememberedMachine(t, 2)
			p.h.wedge(p.h.eps[1].URL)
			canary := func(_ context.Context, _ gatedEndpoint) error {
				clearCanaryFailed(batKey)
				return errors.New("canary clip: " + timeoutText)
			}
			if _, err := dispatchWith(testCtx(t), p, canary, 8); !IsPoolUnavailable(err) {
				t.Fatalf("err = %v, want ErrPoolUnavailable", err)
			}
			return p
		}},
		{"a machine faults on timeouts and fails its first canary", func(t *testing.T) *faultPool {
			s := newSickThenSound(t, batMachine, batMachine)
			s.h.wedge(s.h.eps[1].URL)
			_, err := dispatchOn(testCtx(t), s.h.eps, []string{"gpu"}, s.send, s.canary, 8)
			if !IsPoolUnavailable(err) || len(s.canaryLog()) != 1 {
				t.Fatalf("err = %v, canaries = %v, want ErrPoolUnavailable after one canary", err, s.canaryLog())
			}
			s.sick.Store(false)
			return s.faultPool
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.setup(t)
			if !failedCanaryBefore(batKey) {
				t.Fatal("the machine that failed its canary is not remembered")
			}
			for _, ep := range p.h.eps {
				clearCooldown(ep.URL)
			}
			p.h.srvs[1].Close() // the refused sibling, now unreachable
			before := len(p.canaryLog())
			res, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, p.canary, 8)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			requireTranscripts(t, res, 8)
			if asked := p.canaryLog()[before:]; !slices.Equal(asked, []string{p.h.eps[0].URL}) {
				t.Errorf("canaries = %v, want the lone port asked once", asked)
			}
			if !failedCanaryBefore(batKey) {
				t.Error("forgotten while the machine's other port, refused before, is down and has not answered")
			}
		})
	}
}

// S3 in the canary a faulted dispatch sends (runCanaries): the machine answers,
// but another dispatch recorded a failure of it while this one asked.
func TestCanaryMemory_AFaultedDispatchDoesNotForgetAFailureRecordedWhileItAsked(t *testing.T) {
	p := poolOf(t, batMachine, batMachine)
	jobs := make(map[string]string)
	for _, id := range loopers(8, "loop") {
		jobs[id] = "unused"
	}
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pr ProgressFunc) (map[string]BatchResult, error) {
		// every clip times out on every port, so the whole pool is sick
		res := make(map[string]BatchResult, len(sub))
		for id := range sub {
			res[id] = BatchResult{Error: timeoutText}
		}
		kept, fault := splitWorkerFault(g.Endpoint.URL, res)
		if fault != nil {
			return kept, fault
		}
		return kept, nil
	}
	canary := func(ctx context.Context, g gatedEndpoint) error {
		markCanaryFailed(batKey) // another dispatch's failure of the same machine
		return p.canary(ctx, g)
	}
	if _, err := dispatchPool(testCtx(t), p.h.eps, nil, jobs, poolOptions{canary: canary}, send); err != nil {
		t.Fatalf("err = %v, want the machine that answered the canary to be believed", err)
	}
	if len(p.canaryLog()) == 0 {
		t.Fatal("no canary was sent: the pool was not sick, the test shows nothing")
	}
	if !failedCanaryBefore(batKey) {
		t.Error("a failure another dispatch recorded while this one asked was erased by this one's answer")
	}
}

// S2: a remembered machine with a port in cooldown, set aside because another
// machine could take the work, is asked once that machine has failed its own
// canary: the dispatch must not report that no machine answered one when it
// never asked this one.
func TestPending_ARememberedMachineIsAskedWhenTheStandInFailsItsCanary(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	var p *faultPool
	p = machineFaultPool(t, []string{batMachine, batMachine, robinMachine}, func(url, id string) BatchResult {
		if url == p.h.eps[2].URL {
			return BatchResult{Error: timeoutText} // llm1: answers nothing, the canary included
		}
		return BatchResult{Text: "T:" + id}
	})
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	markCanaryFailed(batKey)
	markEndpointFailure(p.h.eps[1].URL)
	res, err := dispatchWith(testCtx(t), p, p.canary, 8)
	if err != nil {
		t.Fatalf("err = %v, want the remembered machine to be asked after the stand-in failed, and to take the page", err)
	}
	for _, id := range loopers(8, "chap") {
		if r := res[id]; r.Text != "T:"+id || r.Worker != batKey {
			t.Errorf("%s = %+v, want the remembered machine's transcript", id, r)
		}
	}
	asked := p.canaryLog()
	for _, port := range p.h.eps[:2] {
		if n := strings.Count(strings.Join(asked, " "), port.URL); n != 1 {
			t.Errorf("canaries = %v, want %s asked exactly once", asked, port.URL)
		}
	}
	if !slices.Contains(asked, p.h.eps[2].URL) {
		t.Errorf("canaries = %v, want the stand-in asked once it faulted", asked)
	}
	if failedCanaryBefore(batKey) {
		t.Error("the machine answered on every port: it must be forgotten")
	}
	if !failedCanaryBefore(robinKey) {
		t.Error("the stand-in failed its canary: it must be remembered")
	}
	requireNoStrikes(t, res)
}

// S2 and N-b: when the stand-in has failed its canary and the remembered
// machine's cooldown outlasts the pacing allowance, the dispatch defers at once,
// without a verdict: neither ErrPoolUnavailable (nobody answered) nor a wait for
// the whole allowance (the cooldown cannot end inside it).
func TestPending_ACooldownThatOutlastsTheAllowanceIsDeferredAtOnceWithNoVerdict(t *testing.T) {
	const allowance = 4 * time.Second
	shortenPacing(t, 10*time.Millisecond, allowance)
	var p *faultPool
	p = machineFaultPool(t, []string{batMachine, batMachine, robinMachine}, func(url, id string) BatchResult {
		if url == p.h.eps[2].URL {
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
	markEndpointFailure(p.h.eps[1].URL)
	// faultPool.send clears every other endpoint's cooldown; the Mac's must hold.
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pr ProgressFunc) (map[string]BatchResult, error) {
		r, err := p.send(ctx, g, sub, pr)
		markEndpointFailure(p.h.eps[1].URL)
		return r, err
	}
	logs := logtest.Capture(t, slog.LevelWarn)
	start := time.Now()
	res, err := dispatchOn(testCtx(t), p.h.eps, nil, send, p.canary, 8)
	elapsed := time.Since(start)
	if !errors.Is(err, errCanarySlotBusy) || IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want the no-verdict deferral and not ErrPoolUnavailable", err)
	}
	if te, ok := errors.AsType[*TransportError](err); !ok || te.Recognized {
		t.Fatalf("err = %v: want an unrecognised error the ops defer on without counting", err)
	}
	if elapsed > allowance/2 {
		t.Errorf("took %s: a cooldown that ends after the %s allowance must be deferred at once, not slept through", elapsed, allowance)
	}
	if slices.Contains(p.canaryLog(), p.h.eps[0].URL) || slices.Contains(p.canaryLog(), p.h.eps[1].URL) {
		t.Errorf("canaries = %v: a machine in cooldown must not be asked", p.canaryLog())
	}
	if !failedCanaryBefore(batKey) {
		t.Error("no verdict must leave the machine remembered")
	}
	requireNoStrikes(t, res)
	// N-a: the warning names how long the dispatch really waited.
	lines := logs.Lines("outlasts")
	if len(lines) != 1 {
		t.Fatalf("%d warnings about the cooldown, want 1:\n%s", len(lines), logs.String())
	}
	waited := lines[0][strings.Index(lines[0], "waited ")+len("waited "):]
	waited = waited[:strings.IndexAny(waited, ")")]
	if d, perr := time.ParseDuration(waited); perr != nil || d >= allowance/2 {
		t.Errorf("warning says it waited %q, want the real time (well under %s): %s", waited, allowance, lines[0])
	}
}

// S2: two remembered machines, each with a port in cooldown, and no one else.
// Each is asked once its cooldown ends, and only if the one asked before it did
// not answer.
func TestPending_TwoRememberedMachinesBothInCooldown(t *testing.T) {
	type want struct {
		batAnswers, robinAnswers bool
		endpoint                 string // "", "bat" or "robin": who took the page
		unavailable              bool
		robinAsked               bool
	}
	for _, tc := range []struct {
		name string
		want want
	}{
		{"the first to come out answers", want{batAnswers: true, robinAnswers: true, endpoint: "bat"}},
		{"the first fails, the second answers", want{robinAnswers: true, endpoint: "robin", robinAsked: true}},
		{"both fail", want{unavailable: true, robinAsked: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortenPacing(t, 10*time.Millisecond, time.Minute)
			p := poolOf(t, batMachine, batMachine, robinMachine, robinMachine)
			markCanaryFailed(batKey)
			markCanaryFailed(robinKey)
			markEndpointFailure(p.h.eps[1].URL)
			markEndpointFailure(p.h.eps[3].URL)
			after(t, 60*time.Millisecond, func() { clearCooldown(p.h.eps[1].URL) })
			after(t, 200*time.Millisecond, func() { clearCooldown(p.h.eps[3].URL) })
			var rec canaryRecorder
			canary := func(ctx context.Context, g gatedEndpoint) error {
				_ = rec.fn(ctx, g)
				switch g.Endpoint.URL {
				case p.h.eps[0].URL, p.h.eps[1].URL:
					if !tc.want.batAnswers {
						return errors.New("canary clip: " + timeoutText)
					}
				default:
					if !tc.want.robinAnswers {
						return errors.New("canary clip: " + timeoutText)
					}
				}
				return nil
			}
			res, err := dispatchOn(testCtx(t), p.h.eps, nil, p.send, canary, 8)
			if tc.want.unavailable {
				if !IsPoolUnavailable(err) {
					t.Fatalf("err = %v, want ErrPoolUnavailable: both were asked and neither answered", err)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			} else {
				requireTranscripts(t, res, 8)
				wantURLs := p.h.eps[:2]
				if tc.want.endpoint == "robin" {
					wantURLs = p.h.eps[2:]
				}
				for _, id := range loopers(8, "chap") {
					if r := res[id]; r.Endpoint != wantURLs[0].URL && r.Endpoint != wantURLs[1].URL {
						t.Errorf("%s answered by %s, want one of %v", id, r.Endpoint, endpointURLs(wantURLs))
					}
				}
			}
			if len(rec.askedCooly) != 0 {
				t.Errorf("ports asked while in cooldown: %v", rec.askedCooly)
			}
			robinAsked := slices.Contains(rec.asked, p.h.eps[2].URL) || slices.Contains(rec.asked, p.h.eps[3].URL)
			if robinAsked != tc.want.robinAsked {
				t.Errorf("canaries = %v: second machine asked = %v, want %v", rec.asked, robinAsked, tc.want.robinAsked)
			}
			if got := failedCanaryBefore(batKey); got != !tc.want.batAnswers {
				t.Errorf("bat remembered = %v, want %v", got, !tc.want.batAnswers)
			}
			if got := failedCanaryBefore(robinKey); got != !(tc.want.robinAnswers && tc.want.robinAsked) {
				t.Errorf("robin remembered = %v, want %v", got, !(tc.want.robinAnswers && tc.want.robinAsked))
			}
		})
	}
}

// S2: a machine that is itself in cooldown cannot stand in for a remembered
// one, so the page waits for the remembered machine and asks it.
func TestPending_AStandInInCooldownIsNotAStandIn(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := poolOf(t, batMachine, batMachine, robinMachine)
	markCanaryFailed(batKey)
	markEndpointFailure(p.h.eps[1].URL)
	markEndpointFailure(p.h.eps[2].URL) // llm1 is out for 30 s
	after(t, 60*time.Millisecond, func() { clearCooldown(p.h.eps[1].URL) })
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v, want the remembered machine asked after its cooldown, and to take the page", err)
	}
	requireTranscripts(t, res, 8)
	if rec.count() != 2 || len(rec.askedCooly) != 0 {
		t.Errorf("canaries = %v (in cooldown when asked: %v), want one to each port after the cooldown", rec.asked, rec.askedCooly)
	}
}

// S2: an endpoint whose /health could not be read (kept because the pool
// requires nothing) is a guess, not a machine to set a remembered one aside for.
func TestPending_AnUnprobedEndpointIsNotAStandIn(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := poolOf(t, batMachine, batMachine)
	markCanaryFailed(batKey)
	markEndpointFailure(p.h.eps[1].URL)
	after(t, 60*time.Millisecond, func() { clearCooldown(p.h.eps[1].URL) })
	eps := append(slices.Clone(p.h.eps), Endpoint{URL: deadURL(t), Concurrency: 1, Priority: 9, Label: "arkham"})
	rec := &canaryRecorder{}
	if _, err := dispatchOn(testCtx(t), eps, nil, p.send, rec.fn, 8); err != nil {
		t.Fatalf("err = %v", err)
	}
	if rec.count() != 2 || len(rec.askedCooly) != 0 {
		t.Errorf("canaries = %v (in cooldown when asked: %v), want the remembered machine asked on both ports after its cooldown", rec.asked, rec.askedCooly)
	}
}

// N-d: another dispatch asked the machine while this one waited and it answered:
// this one does not ask it again, and does not keep it benched.
func TestPending_AMachineAnotherDispatchForgotWhileWaitingIsNotAsked(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	markEndpointFailure(p.h.eps[1].URL)
	after(t, 40*time.Millisecond, func() { clearCanaryFailed(batKey) })
	// The cooldown ends at 3 s: a dispatch that did not notice the forgetting
	// waits for it, and the bound below sits far from both ends so a loaded
	// agent running under -race cannot tip it either way.
	after(t, 3*time.Second, func() { clearCooldown(p.h.eps[1].URL) })
	rec := &canaryRecorder{}
	start := time.Now()
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if rec.count() != 0 {
		t.Errorf("canaries = %v, want none: the machine had already answered another dispatch", rec.asked)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("took %s: the machine's other port was free and the machine no longer remembered", d)
	}
}

// S4: after the wait the ports are probed again. A port that wedged in the
// meantime is neither asked nor used, and the machine is not forgotten on the
// word of the port that answered.
func TestRegate_APortThatWedgedDuringTheWaitIsNotAsked(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	markEndpointFailure(p.h.eps[1].URL)
	after(t, 60*time.Millisecond, func() {
		p.h.wedge(p.h.eps[1].URL)
		clearCooldown(p.h.eps[1].URL)
	})
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if want := []string{p.h.eps[0].URL}; !slices.Equal(rec.asked, want) {
		t.Errorf("canaries = %v, want only the port that is still sound (%v)", rec.asked, want)
	}
	for _, s := range p.sendLog() {
		if s.url == p.h.eps[1].URL {
			t.Errorf("work was sent to the wedged port: %v", s)
		}
	}
	if !failedCanaryBefore(batKey) {
		t.Error("forgotten on the answer of one port while another is wedged and has not answered")
	}
}

// S4: when every port of the only machine is wedged by the time the wait is
// over, the dispatch ends as a stuck pool (which the ops stop on), not as a
// machine that failed the canary clip.
func TestRegate_EveryPortWedgedAfterTheWaitIsAStuckPoolNotASickMachine(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 2)
	markEndpointFailure(p.h.eps[1].URL)
	after(t, 60*time.Millisecond, func() {
		for _, ep := range p.h.eps {
			p.h.wedge(ep.URL)
		}
		clearCooldown(p.h.eps[1].URL)
	})
	rec := &canaryRecorder{err: errors.New("canary clip: " + timeoutText)}
	_, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if !IsWorkerStuck(err) || IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want ErrWorkerStuck", err)
	}
	if rec.count() != 0 || len(p.sendLog()) != 0 {
		t.Errorf("canaries = %v, sends = %d: a wedged port is neither asked nor sent work", rec.asked, len(p.sendLog()))
	}
	if !failedCanaryBefore(batKey) {
		t.Error("no verdict: the machine stays remembered")
	}
}

// S4: a port in its decoder's fault window when the wait is over is waited for
// like a cooldown, then asked with the others, not asked in the window.
func TestRegate_APortInItsFaultWindowAfterTheWaitIsWaitedFor(t *testing.T) {
	// A cooldown is 30 s: the allowance must outlast it or the dispatch defers.
	shortenPacing(t, 10*time.Millisecond, 40*time.Second)
	p := rememberedMachine(t, 2)
	port := p.h.eps[1].URL
	markEndpointFailure(port)
	after(t, 60*time.Millisecond, func() {
		p.h.setChildFault(port, true) // its decoder stops starting while the machine is waited for
		clearCooldown(port)
	})
	after(t, 250*time.Millisecond, func() { p.h.setChildFault(port, false) })
	var (
		rec canaryRecorder
		mu  sync.Mutex
		bad []string
	)
	canary := func(ctx context.Context, g gatedEndpoint) error {
		if _, inWindow := p.h.childFault.Load(strings.TrimPrefix(g.Endpoint.URL, "http://")); inWindow {
			mu.Lock()
			bad = append(bad, g.Endpoint.URL)
			mu.Unlock()
		}
		return rec.fn(ctx, g)
	}
	res, err := dispatchWith(testCtx(t), p, canary, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if len(bad) != 0 {
		t.Errorf("asked in the decoder's fault window: %v", bad)
	}
	if rec.count() != 2 {
		t.Errorf("canaries = %v, want one to each port once the window was over", rec.asked)
	}
	if failedCanaryBefore(batKey) {
		t.Error("every port answered: the machine must be forgotten")
	}
}

// S4: a refused port is waited for only while it may clear by itself and the
// pacing allowance lasts; otherwise it is given up on, as the gate gives up on a
// port it refused. (The dispatch-level case needs a cooldown that ends inside a
// short allowance, and a cooldown is 30 s at least, which decideCooldownWait
// defers at once.)
func TestKeepWaitingFor(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	until := now.Add(time.Minute)
	for _, tc := range []struct {
		name string
		r    gpuRefusal
		now  time.Time
		want bool
	}{
		{"unreachable or restarting its decoder, allowance left", gpuRefusal{Transient: true}, now, true},
		{"transient, allowance spent", gpuRefusal{Transient: true}, until, false},
		{"stuck", gpuRefusal{Stuck: true}, now, false},
		{"refused for a capability or a mount", gpuRefusal{}, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := keepWaitingFor(tc.r, tc.now, until); got != tc.want {
				t.Errorf("keepWaitingFor = %v, want %v", got, tc.want)
			}
		})
	}
}

// N-f: a lone remembered endpoint is asked the canary only once its cooldown is
// over, like the pool path asks a machine.
func TestSingle_ARememberedLoneEndpointInCooldownIsWaitedFor(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 1)
	markEndpointFailure(p.h.eps[0].URL)
	after(t, 60*time.Millisecond, func() { clearCooldown(p.h.eps[0].URL) })
	rec := &canaryRecorder{}
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if rec.count() != 1 || len(rec.askedCooly) != 0 {
		t.Errorf("canaries = %v (in cooldown when asked: %v), want one, after the cooldown", rec.asked, rec.askedCooly)
	}
	if failedCanaryBefore(batKey) {
		t.Error("the machine answered: it must be forgotten")
	}
}

// N-f: a cooldown that outlasts the allowance is no verdict, deferred at once.
func TestSingle_ACooldownThatOutlastsTheAllowanceIsNoVerdict(t *testing.T) {
	const allowance = 4 * time.Second
	shortenPacing(t, 10*time.Millisecond, allowance)
	p := rememberedMachine(t, 1)
	markEndpointFailure(p.h.eps[0].URL) // 30 s
	rec := &canaryRecorder{}
	logs := logtest.Capture(t, slog.LevelWarn)
	start := time.Now()
	res, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if !errors.Is(err, errCanarySlotBusy) || IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want the no-verdict deferral", err)
	}
	if te, ok := errors.AsType[*TransportError](err); !ok || te.Recognized {
		t.Fatalf("err = %v: want an unrecognised error the ops defer on without counting", err)
	}
	if d := time.Since(start); d > allowance/2 {
		t.Errorf("took %s, want an immediate deferral for a cooldown that ends after the %s allowance", d, allowance)
	}
	if rec.count() != 0 || len(p.sendLog()) != 0 {
		t.Errorf("canaries = %v, sends = %d: nothing may be asked or sent in cooldown", rec.asked, len(p.sendLog()))
	}
	requireNoStrikes(t, res)
	if !failedCanaryBefore(batKey) {
		t.Error("no verdict must leave the machine remembered")
	}
	lines := logs.Lines("outlasts")
	if len(lines) != 1 {
		t.Fatalf("%d warnings about the cooldown, want 1:\n%s", len(lines), logs.String())
	}
	waited := lines[0][strings.Index(lines[0], "waited ")+len("waited "):]
	waited = waited[:strings.IndexAny(waited, ")")]
	if d, perr := time.ParseDuration(waited); perr != nil || d >= allowance/2 {
		t.Errorf("warning says it waited %q, want the real time (well under %s): %s", waited, allowance, lines[0])
	}
}

// N-f with S4: the lone endpoint wedged while it was waited for is a stuck pool.
func TestSingle_AWedgedLoneEndpointAfterTheWaitIsAStuckPool(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 1)
	markEndpointFailure(p.h.eps[0].URL)
	after(t, 60*time.Millisecond, func() {
		p.h.wedge(p.h.eps[0].URL)
		clearCooldown(p.h.eps[0].URL)
	})
	rec := &canaryRecorder{err: errors.New("canary clip: " + timeoutText)}
	_, err := dispatchWith(testCtx(t), p, rec.fn, 8)
	if !IsWorkerStuck(err) || IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want ErrWorkerStuck", err)
	}
	if rec.count() != 0 || len(p.sendLog()) != 0 {
		t.Errorf("canaries = %v, sends = %d, want none", rec.asked, len(p.sendLog()))
	}
}

// N-f: a cancelled op returns the cancellation from the wait.
func TestSingle_ACancelDuringTheCooldownWaitReturnsTheCancellation(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := rememberedMachine(t, 1)
	markEndpointFailure(p.h.eps[0].URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	after(t, 50*time.Millisecond, cancel)
	rec := &canaryRecorder{}
	_, err := dispatchWith(ctx, p, rec.fn, 8)
	if !errors.Is(err, context.Canceled) || IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if rec.count() != 0 {
		t.Errorf("canaries = %v, want none", rec.asked)
	}
}

// decideCooldownWait is the one rule every wait for a remembered machine's
// cooldown follows.
func TestDecideCooldownWait(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	until := now.Add(time.Minute)
	for _, tc := range []struct {
		name     string
		earliest time.Time
		paceEnd  time.Time
		action   cooldownAction
		wait     time.Duration
	}{
		{"nothing in cooldown", time.Time{}, until, cooldownClear, 0},
		{"nothing in cooldown and the allowance is spent", time.Time{}, now, cooldownClear, 0},
		{"a cooldown that has just ended, allowance spent too", now.Add(-time.Second), now.Add(-time.Second), cooldownClear, 0},
		{"a cooldown that ends inside the allowance", now.Add(10 * time.Second), until, cooldownSleep, 10 * time.Second},
		{"a cooldown that ends at the end of the allowance", until, until, cooldownSleep, time.Minute},
		{"a cooldown that ends after the allowance is deferred at once", until.Add(time.Second), until, cooldownDefer, 0},
		{"a cooldown still running when the allowance is spent", now.Add(time.Second), now, cooldownDefer, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait, act := decideCooldownWait(now, tc.paceEnd, tc.earliest)
			if act != tc.action || wait != tc.wait {
				t.Errorf("decideCooldownWait = (%s, %d), want (%s, %d)", wait, act, tc.wait, tc.action)
			}
		})
	}
}

// sickThenSound is a pool of machines whose workers time out on every clip
// until sound is called, the canary clip included.
type sickThenSound struct {
	*faultPool
	sick atomic.Bool
}

func newSickThenSound(t *testing.T, machines ...string) *sickThenSound {
	t.Helper()
	s := &sickThenSound{}
	s.sick.Store(true)
	s.faultPool = machineFaultPool(t, machines, func(_ string, id string) BatchResult {
		if s.sick.Load() {
			return BatchResult{Error: timeoutText}
		}
		return BatchResult{Text: "T:" + id}
	})
	t.Cleanup(func() {
		for _, ep := range s.h.eps {
			clearCooldown(ep.URL)
		}
	})
	return s
}

// S1, written at mark time: a machine that fails the canary clip is recorded
// with the ports this dispatch has seen as its own (markCanaryFailed's ports).
// The gate noted nothing for it, it was not remembered then. A later dispatch
// that finds one of those ports unreachable must keep the machine remembered:
// that port has not answered.
func TestCanaryMemory_PortsSeenWhenAMachineFailsItsCanaryAreRecordedWithIt(t *testing.T) {
	p := newSickThenSound(t, batMachine, batMachine, batMachine)
	_, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, p.canary, 8)
	if err == nil || !failedCanaryBefore(batKey) || len(p.canaryLog()) == 0 {
		t.Fatalf("err = %v, canaries = %v, remembered = %v: the first dispatch must end with a machine that failed the canary clip",
			err, p.canaryLog(), failedCanaryBefore(batKey))
	}
	for _, ep := range p.h.eps {
		clearCooldown(ep.URL)
	}
	p.sick.Store(false)
	down := p.h.eps[2].URL
	p.h.srvs[2].Close()
	before := len(p.canaryLog())
	res, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, p.canary, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if asked := p.canaryLog()[before:]; len(asked) != 2 || slices.Contains(asked, down) {
		t.Errorf("canaries = %v, want one to each of the two ports that are up", asked)
	}
	if !failedCanaryBefore(batKey) {
		t.Error("forgotten while a port the first dispatch saw as its own is down and has not answered")
	}
}

// S1, written at the gate: a machine remembered with no ports, never asked in
// this dispatch (a port in cooldown and a stand-in that can take the work), has
// its ports noted by the gate of every dispatch that sees it probed. That is
// the only record of them. A later dispatch that finds one unreachable must
// keep the machine remembered.
func TestCanaryMemory_PortsTheGateSeesOfARememberedMachineAreRecordedWithIt(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := poolOf(t, batMachine, batMachine, batMachine, robinMachine)
	markCanaryFailed(batKey) // no ports: the first dispatch's gate has to learn them
	markEndpointFailure(p.h.eps[1].URL)
	res, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, p.canary, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if len(p.canaryLog()) != 0 || !failedCanaryBefore(batKey) {
		t.Fatalf("canaries = %v, remembered = %v: the stand-in takes the work and the machine is not asked, so it stays remembered",
			p.canaryLog(), failedCanaryBefore(batKey))
	}
	for _, ep := range p.h.eps {
		clearCooldown(ep.URL)
	}
	down := p.h.eps[2].URL
	p.h.srvs[2].Close()
	res, err = dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, p.canary, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	asked := p.canaryLog()
	if len(asked) != 2 || slices.Contains(asked, down) || slices.Contains(asked, p.h.eps[3].URL) {
		t.Errorf("canaries = %v, want one to each of the machine's two ports that are up", asked)
	}
	if !failedCanaryBefore(batKey) {
		t.Error("forgotten while a port its earlier dispatch's gate saw as its own is down and has not answered")
	}
}

// S1 at the gate, for a refused sibling: a remembered machine whose sibling is
// wedged stays remembered when its lone port answers (the refusal names the
// machine), and the gate records that sibling's port. It is the only record of
// it: when the sibling is unreachable in the next dispatch, the machine stays
// remembered.
func TestCanaryMemory_APortTheGateRefusedWithItsMachineIsRecordedWithIt(t *testing.T) {
	p := rememberedMachine(t, 2)
	markCanaryFailed(batKey) // a new generation, still no ports: the gate has to learn them
	p.h.wedge(p.h.eps[1].URL)
	res, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, p.canary, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if !failedCanaryBefore(batKey) {
		t.Fatal("forgotten on the answer of one port while its sibling is wedged and has not answered")
	}
	p.h.srvs[1].Close()
	before := len(p.canaryLog())
	res, err = dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, p.canary, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if asked := p.canaryLog()[before:]; !slices.Equal(asked, []string{p.h.eps[0].URL}) {
		t.Errorf("canaries = %v, want the lone port asked once", asked)
	}
	if !failedCanaryBefore(batKey) {
		t.Error("forgotten while a port the gate once refused with the machine's name is down and has not answered")
	}
}

// B1 x S4 x N-d: the identity a dispatch asks a remembered machine under is the
// one its gate gave it. The re-probe that follows a wait for a cooldown must not
// reconcile identities: a worker that began to report a machine id meanwhile
// would have its memory moved to the new key under the dispatch's feet, and the
// machine it is still waiting for would look as if another dispatch had
// forgotten it, so it would be sent work without being asked.
func TestRegate_AWorkerThatBeginsToReportAMachineIdWhileWaitedForIsStillAsked(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	h := machineHarness(t, []string{"", robinMachine}) // worker 0 reports no machine id yet
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
	// At 80 ms worker 0 reports its machine id and enters its decoder's fault
	// window (a transient refusal), and its cooldown ends: the dispatch probes
	// it again, finds it refused for now and goes on waiting. The window ends at
	// 250 ms.
	after(t, 80*time.Millisecond, func() {
		h.setMachineID(w0, newID)
		h.setChildFault(w0, true)
		clearCooldown(w0)
	})
	after(t, 250*time.Millisecond, func() { h.setChildFault(w0, false) })
	rec := &canaryRecorder{}
	canary := func(ctx context.Context, g gatedEndpoint) error {
		if g.Endpoint.URL == w1 {
			return errors.New("canary: " + timeoutText) // the stand-in is sick
		}
		return rec.fn(ctx, g)
	}
	res, err := dispatchOn(testCtx(t), h.eps, nil, p.send, canary, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if want := []string{w0}; !slices.Equal(rec.asked, want) {
		t.Errorf("canaries = %v, want worker 0 asked once, before it was sent anything", rec.asked)
	}
	if failedCanaryBefore(oldKey) || failedCanaryBefore("m:"+newID) {
		t.Errorf("remembered: %s=%v m:%s=%v, want it forgotten under the identity it was asked as",
			oldKey, failedCanaryBefore(oldKey), newID, failedCanaryBefore("m:"+newID))
	}
}

// The same on the lone endpoint's path: a failed canary after the wait must not
// re-create the dead identity beside the one the worker now reports.
func TestSingle_AWorkerThatBeginsToReportAMachineIdWhileWaitedForKeepsOneIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		canary   error
		wantKept bool // the failure is remembered, under the key it was remembered under
	}{
		{"canary passes", nil, false},
		{"canary fails", errors.New("canary: " + timeoutText), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortenPacing(t, 10*time.Millisecond, time.Minute)
			h := machineHarness(t, []string{""})
			useEndpoints(t, 16, endpointURLs(h.eps)...)
			p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
			t.Cleanup(func() { clearCooldown(h.eps[0].URL) })
			w0 := h.eps[0].URL
			oldKey := "u:" + shortHash(w0)
			const newID = "cccc0003"
			markCanaryFailed(oldKey)
			markEndpointFailure(w0)
			after(t, 80*time.Millisecond, func() {
				h.setMachineID(w0, newID)
				clearCooldown(w0)
			})
			rec := &canaryRecorder{err: tc.canary}
			res, err := dispatchOn(testCtx(t), h.eps, nil, p.send, rec.fn, 8)
			if tc.canary == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				requireTranscripts(t, res, 8)
			} else if !IsPoolUnavailable(err) {
				t.Fatalf("err = %v, want ErrPoolUnavailable", err)
			}
			if !slices.Equal(rec.asked, []string{w0}) {
				t.Errorf("canaries = %v, want the endpoint asked once", rec.asked)
			}
			if got := failedCanaryBefore(oldKey); got != tc.wantKept {
				t.Errorf("remembered under %s = %v, want %v", oldKey, got, tc.wantKept)
			}
			if failedCanaryBefore("m:" + newID) {
				t.Errorf("remembered under m:%s: the re-probe after the wait moved the memory to an identity the dispatch does not use", newID)
			}
		})
	}
}
