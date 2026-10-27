// file: internal/transcribe/canary_identity_test.go
// version: 1.0.0
// guid: 3e7a9c52-81d4-4f6b-b0a3-6d2c15e8f947
// last-edited: 2026-10-26

package transcribe

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

// The re-review of #4193's follow-up: a machine that failed the canary clip is
// remembered under a worker identity that changes (u: <-> m: <-> ?) while its
// URL does not. These tests hold the memory to the URL: the last strong
// identity a probe reported for it (canaryFailed.byURL), the ports of an entry
// that a weaker identity never takes away, and the single question every part of
// the dispatcher asks (endpointRemembered).

const catwomanMachine = "cccc0003"

var timeoutCanaryErr = errors.New("canary clip: " + timeoutText)

// canaryURLMachine is the strong identity a probe last reported for url.
func canaryURLMachine(url string) string {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	return canaryFailed.byURL[url]
}

func canaryURLCount() int {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	return len(canaryFailed.byURL)
}

// sentTo is the endpoints a pool sent any work to.
func sentTo(p *faultPool) []string {
	var out []string
	for _, s := range p.sendLog() {
		if !slices.Contains(out, s.url) {
			out = append(out, s.url)
		}
	}
	return out
}

// askedOnce fails the test unless every url of want was asked exactly once.
func askedOnce(t *testing.T, asked []string, want ...string) {
	t.Helper()
	for _, u := range want {
		n := 0
		for _, a := range asked {
			if a == u {
				n++
			}
		}
		if n != 1 {
			t.Errorf("canaries = %v, want %s asked exactly once (asked %d times)", asked, u, n)
		}
	}
}

// B-1: another dispatch's gate moves a remembered worker's memory from its old
// identity to its new one (u: -> m:) while this dispatch waits for the worker's
// cooldown. The wait must still find the machine remembered: asked the canary
// clip, and sent no work when it fails it. Judged by the key it began with, it
// looked forgotten, was un-benched and was sent a chunk with no question asked.
func TestCanaryIdentity_AConcurrentGateMovingTheMemoryDuringTheWaitDoesNotForgetTheMachine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer error // worker 0's answer to the canary clip
	}{
		{"it fails the canary", timeoutCanaryErr},
		{"it answers the canary", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			oldKey, newKey := "u:"+shortHash(w0), "m:"+catwomanMachine
			markCanaryFailed(oldKey)
			markCanaryFailed(robinKey)
			markEndpointFailure(w0)
			// At 80 ms a concurrent dispatch's gate sees worker 0 report its
			// machine id and moves the memory to the new key, and the cooldown ends.
			after(t, 80*time.Millisecond, func() {
				h.setMachineID(w0, catwomanMachine)
				kept, refused := gateEndpoints(context.Background(), h.eps[:1], nil)
				reconcileCanaryMemory(h.eps[:1], kept, refused)
				noteCanaryProbes(kept, refused)
				clearCooldown(w0)
			})
			rec := &canaryRecorder{err: tc.answer}
			canary := func(ctx context.Context, g gatedEndpoint) error {
				if g.Endpoint.URL == w1 {
					return timeoutCanaryErr // the stand-in is sick
				}
				return rec.fn(ctx, g)
			}
			res, err := dispatchOn(testCtx(t), h.eps, nil, p.send, canary, 8)
			if !slices.Equal(rec.asked, []string{w0}) {
				t.Errorf("canaries = %v, want worker 0 asked once: the machine another dispatch moved is still remembered", rec.asked)
			}
			requireNoStrikes(t, res)
			if tc.answer != nil {
				if !IsPoolUnavailable(err) {
					t.Fatalf("err = %v, want ErrPoolUnavailable: both machines were asked and neither answered", err)
				}
				if got := sentTo(p); len(got) != 0 {
					t.Errorf("work was sent to %v, want none: both machines failed the canary", got)
				}
				if !failedCanaryBefore(newKey) {
					t.Errorf("%s is not remembered: the failure was lost on the way", newKey)
				}
				if failedCanaryBefore(oldKey) {
					t.Errorf("%s was re-created beside the identity the memory moved to", oldKey)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want worker 0 to take the page once it answered", err)
			}
			requireTranscripts(t, res, 8)
			if failedCanaryBefore(newKey) || failedCanaryBefore(oldKey) {
				t.Errorf("remembered: %s=%v %s=%v, want the machine forgotten once it answered under the identity it has now",
					newKey, failedCanaryBefore(newKey), oldKey, failedCanaryBefore(oldKey))
			}
		})
	}
}

// B-1, the lone endpoint's path: the machine is asked after the wait whatever
// identity its worker reports by then.
func TestCanaryIdentity_ALoneEndpointWhoseMemoryMovedDuringTheWaitIsStillAsked(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	h := machineHarness(t, []string{""})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	w0 := h.eps[0].URL
	t.Cleanup(func() { clearCooldown(w0) })
	oldKey, newKey := "u:"+shortHash(w0), "m:"+catwomanMachine
	markCanaryFailed(oldKey)
	markEndpointFailure(w0)
	after(t, 80*time.Millisecond, func() {
		h.setMachineID(w0, catwomanMachine)
		kept, refused := gateEndpoints(context.Background(), h.eps, nil)
		reconcileCanaryMemory(h.eps, kept, refused)
		noteCanaryProbes(kept, refused)
		clearCooldown(w0)
	})
	rec := &canaryRecorder{err: timeoutCanaryErr}
	_, err := dispatchOn(testCtx(t), h.eps, nil, p.send, rec.fn, 8)
	if !IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want ErrPoolUnavailable", err)
	}
	if !slices.Equal(rec.asked, []string{w0}) || len(p.sendLog()) != 0 {
		t.Errorf("canaries = %v, sends = %d: want the endpoint asked once and sent nothing", rec.asked, len(p.sendLog()))
	}
	if !failedCanaryBefore(newKey) || failedCanaryBefore(oldKey) {
		t.Errorf("remembered: %s=%v %s=%v, want the failure under the new identity only", newKey, failedCanaryBefore(newKey), oldKey, failedCanaryBefore(oldKey))
	}
}

// B-2: a worker restarts under memory pressure and answers /health with no
// machine_id until its lookup succeeds (MACHINE_ID_WAIT_S), so its key goes
// m:X -> u:h while the machine is still remembered under m:X. The endpoint is
// still remembered: asked the canary, sent no work when it fails it, and the
// memory under m:X keeps the port.
func TestCanaryIdentity_AMachineWhoseWorkerLostItsIdIsStillRemembered(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	h := machineHarness(t, []string{"", robinMachine})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	t.Cleanup(func() {
		for _, ep := range h.eps {
			clearCooldown(ep.URL)
		}
	})
	w0 := h.eps[0].URL
	key := "m:" + catwomanMachine
	markCanaryFailed(key, w0) // it failed while it reported its id
	rec := &canaryRecorder{err: timeoutCanaryErr}
	res, err := dispatchOn(testCtx(t), h.eps, nil, p.send, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v, want the other machine to take the page", err)
	}
	if !slices.Equal(rec.asked, []string{w0}) {
		t.Errorf("canaries = %v, want worker 0 asked once: it is a known port of a remembered machine", rec.asked)
	}
	for id, r := range res {
		if r.Endpoint == w0 {
			t.Errorf("%s was answered by worker 0, which failed the canary clip", id)
		}
	}
	if !failedCanaryBefore(key) {
		t.Error("the machine failed the canary again and must stay remembered")
	}
	if !hasUnheardPort(key, []gpuRefusal{{URL: w0}}, nil) {
		t.Error("worker 0 is no longer a port of the machine: its weaker identity took it away")
	}
	if failedCanaryBefore("u:" + shortHash(w0)) {
		t.Error("the failure was recorded again under the weaker identity, beside the machine's own entry")
	}
}

// B-2 on the lone endpoint's path: its worker answers as u: while its machine is
// remembered under m:. It is asked the canary clip before it gets work.
func TestCanaryIdentity_ALoneEndpointWhoseWorkerLostItsIdIsStillAsked(t *testing.T) {
	h := machineHarness(t, []string{""})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	w0 := h.eps[0].URL
	key := "m:" + catwomanMachine
	markCanaryFailed(key, w0)
	rec := &canaryRecorder{err: timeoutCanaryErr}
	_, err := dispatchOn(testCtx(t), h.eps, nil, p.send, rec.fn, 8)
	if !IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want ErrPoolUnavailable", err)
	}
	if !slices.Equal(rec.asked, []string{w0}) || len(p.sendLog()) != 0 {
		t.Errorf("canaries = %v, sends = %d: want the endpoint asked once and sent nothing", rec.asked, len(p.sendLog()))
	}
	if !failedCanaryBefore(key) || !hasUnheardPort(key, []gpuRefusal{{URL: w0}}, nil) {
		t.Error("the machine's entry or its port was lost")
	}
	if failedCanaryBefore("u:" + shortHash(w0)) {
		t.Error("the failure was recorded again under the weaker identity")
	}
}

// B-2 on a machine with several ports, one of which answers as u: while the
// others answer as m: and are remembered. The machine is one: all its ports are
// asked, and it passes only when every one of them answers. Forgotten on the
// word of the m: ports alone, the u: port would be sent work next dispatch with
// no question asked.
func TestCanaryIdentity_APortThatAnswersAsUIsAskedWithItsMachinesOtherPorts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failing    int // index of the port that does not answer the canary; -1: none
		wantForget bool
	}{
		{"the u: port fails", 0, false},
		{"an m: port fails", 2, false},
		{"every port answers", -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortenPacing(t, 10*time.Millisecond, time.Minute)
			h := machineHarness(t, []string{"", batMachine, batMachine, robinMachine})
			useEndpoints(t, 16, endpointURLs(h.eps)...)
			p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
			t.Cleanup(func() {
				for _, ep := range h.eps {
					clearCooldown(ep.URL)
				}
			})
			ports := endpointURLs(h.eps[:3])
			markCanaryFailed(batKey, ports...)
			rec := &canaryRecorder{}
			canary := func(ctx context.Context, g gatedEndpoint) error {
				_ = rec.fn(ctx, g)
				if tc.failing >= 0 && g.Endpoint.URL == ports[tc.failing] {
					return timeoutCanaryErr
				}
				return nil
			}
			res, err := dispatchOn(testCtx(t), h.eps, nil, p.send, canary, 8)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			requireTranscripts(t, res, 8)
			askedOnce(t, rec.asked, ports...)
			if slices.Contains(rec.asked, h.eps[3].URL) {
				t.Errorf("canaries = %v: the other machine was never remembered", rec.asked)
			}
			if got := failedCanaryBefore(batKey); got == tc.wantForget {
				t.Errorf("machine remembered = %v, want %v", got, !tc.wantForget)
			}
			if failedCanaryBefore("u:" + shortHash(ports[0])) {
				t.Error("a second entry was left under the u: identity")
			}
			for id, r := range res {
				onMachine := slices.Contains(ports, r.Endpoint)
				if onMachine == !tc.wantForget {
					t.Errorf("%s answered by %s: the machine's ports must take work only when it was forgotten", id, r.Endpoint)
				}
			}
		})
	}
}

// A weaker identity never takes a URL from a machine: only a probe that reports
// another strong identity does. And it leaves the URL's last strong identity
// alone, which is what the dispatcher asks about when the worker answers as u:.
func TestCanaryMemory_AWeakerIdentityNeverTakesAPortFromAMachine(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-a:8000"
	h := shortHash(url)
	markCanaryFailed(batKey, url)
	noteCanaryPort(batKey, url)
	for _, weak := range []string{"u:" + h, "?" + h} {
		noteCanaryPort(weak, url)
		if !hasUnheardPort(batKey, []gpuRefusal{{URL: url}}, nil) {
			t.Fatalf("%s took the port from the machine", weak)
		}
		if got := canaryURLMachine(url); got != batKey {
			t.Fatalf("last strong identity = %q after %s, want %s", got, weak, batKey)
		}
		if !endpointRemembered(weak, url) {
			t.Fatalf("an endpoint answering as %s is not remembered although its URL is a port of a remembered machine", weak)
		}
	}
	noteCanaryPort(robinKey, url) // another strong identity does
	if hasUnheardPort(batKey, []gpuRefusal{{URL: url}}, nil) || canaryURLMachine(url) != robinKey {
		t.Error("a strong identity must move the URL to the new machine")
	}
	if endpointRemembered("u:"+h, url) {
		t.Error("the endpoint is remembered through a machine that no longer has its URL")
	}
}

// endpointRemembered, case by case: its own key, or its URL through the last
// strong identity or an entry's ports, and never through another machine's
// strong key.
func TestEndpointRemembered(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const (
		portA = "http://gotham-a:8000"
		portB = "http://gotham-b:8000"
		portC = "http://gotham-c:8000"
	)
	markCanaryFailed(batKey, portA)
	noteCanaryPort(robinKey, portB) // a probe attributed portB to a machine that is not remembered
	for _, tc := range []struct {
		name string
		key  string
		url  string
		want bool
	}{
		{"its own strong key", batKey, portC, true},
		{"a weak key on a port of the machine", "u:x", portA, true},
		{"no identity on a port of the machine", "?x", portA, true},
		{"a weak key on a port nothing remembers", "u:x", portC, false},
		{"another strong key on a port of the machine", robinKey, portA, false},
		{"a weak key on a port attributed to a machine that is not remembered", "u:x", portB, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := endpointRemembered(tc.key, tc.url); got != tc.want {
				t.Errorf("endpointRemembered(%q, %q) = %v, want %v", tc.key, tc.url, got, tc.want)
			}
		})
	}
	clearCanaryFailed(batKey)
	if endpointRemembered("u:x", portA) {
		t.Error("a forgotten machine still remembers its ports")
	}
}

// S-1: a port that is already down when the machine fails the canary is refused
// with no machine, so the dispatch never sees it as the machine's. What an
// earlier probe attributed to the machine is recorded with it all the same: the
// machine must stay remembered while that port has not answered, or the port
// would be sent work with no question asked once it came back.
func TestCanaryMemory_APortAlreadyDownWhenTheMachineFailedIsStillItsPort(t *testing.T) {
	p := poolOf(t, batMachine, batMachine, batMachine)
	down := p.h.eps[2].URL
	// An earlier dispatch saw all three ports up: that is how the URL was
	// attributed to the machine.
	if _, err := dispatchWith(testCtx(t), p, (&canaryRecorder{}).fn, 4); err != nil {
		t.Fatalf("first dispatch: err = %v", err)
	}
	if got := canaryURLMachine(down); got != batKey {
		t.Fatalf("last strong identity of the third port = %q, want %s", got, batKey)
	}
	p.h.srvs[2].Close() // the port goes down; the machine then fails the canary on the two that are up
	markCanaryFailed(batKey, p.h.eps[0].URL, p.h.eps[1].URL)
	if !hasUnheardPort(batKey, []gpuRefusal{{URL: down}}, nil) {
		t.Fatal("the down port was not recorded as the machine's port")
	}
	rec := &canaryRecorder{}
	res, err := dispatchOn(testCtx(t), p.h.eps, []string{"gpu"}, p.send, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if rec.count() != 2 {
		t.Errorf("canaries = %v, want one to each of the two ports that are up", rec.asked)
	}
	if !failedCanaryBefore(batKey) {
		t.Error("forgotten on its other ports while one of them is down and has not answered")
	}
}

// S-2: a lone job that cannot go back to the only stand-in (it left that worker
// stuck on it alone) is a reason to ask a remembered machine that was set aside
// for the stand-in. Without it the dispatch concluded that no healthy endpoint
// was left for the job, never having asked the machine that could take it.
func TestPending_ALoneStuckJobThatCannotGoBackToTheStandInAsksTheRememberedMachine(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := poolOf(t, batMachine, batMachine, robinMachine)
	w2 := p.h.eps[2].URL
	markCanaryFailed(batKey)
	markEndpointFailure(p.h.eps[1].URL) // a port of the remembered machine is in cooldown: it is set aside
	after(t, 60*time.Millisecond, func() { clearCooldown(p.h.eps[1].URL) })
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pr ProgressFunc) (map[string]BatchResult, error) {
		if g.Endpoint.URL == w2 {
			// The stand-in never answers this one: a request that waited, not a
			// wedged decoder (its /health is idle), so no verdict, but the job
			// does not go back there.
			ids := sortedIDs(sub)
			p.mu.Lock()
			p.sends = append(p.sends, sendRecord{w2, ids})
			p.mu.Unlock()
			return nil, &WorkerStuckError{URL: w2, After: time.Second, Jobs: ids}
		}
		return p.send(ctx, g, sub, pr)
	}
	jobID := loopers(1, "chap")[0]
	rec := &canaryRecorder{}
	res, err := dispatchOn(testCtx(t), p.h.eps, nil, send, rec.fn, 1)
	if err != nil {
		t.Fatalf("err = %v, want the remembered machine asked, and to take the job", err)
	}
	if r := res[jobID]; r.Text != "T:"+jobID || (r.Endpoint != p.h.eps[0].URL && r.Endpoint != p.h.eps[1].URL) {
		t.Errorf("%s = %+v, want the remembered machine's transcript", jobID, r)
	}
	askedOnce(t, rec.asked, p.h.eps[0].URL, p.h.eps[1].URL)
	if len(rec.askedCooly) != 0 {
		t.Errorf("ports asked while in cooldown: %v", rec.askedCooly)
	}
	if failedCanaryBefore(batKey) {
		t.Error("the machine answered on every port: it must be forgotten")
	}
}

// S-2, the shape that needs more than "no stand-in is able": the job stuck alone
// on one port of a machine that is wedged, so it cannot go to that machine's
// other port, which is out of cooldown, probed and has failed nothing: an able
// stand-in that cannot take THIS job. The remembered machine set aside for it is
// asked all the same, instead of the dispatch concluding that no endpoint is left.
func TestPending_ALoneStuckJobThatCannotGoToTheStandInsSiblingPortAsksTheRememberedMachine(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	p := poolOf(t, robinMachine, robinMachine, batMachine, batMachine)
	robinPorts := endpointURLs(p.h.eps[:2])
	markCanaryFailed(batKey)
	markEndpointFailure(p.h.eps[3].URL) // a port of the remembered machine is in cooldown: it is set aside
	after(t, 60*time.Millisecond, func() { clearCooldown(p.h.eps[3].URL) })
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pr ProgressFunc) (map[string]BatchResult, error) {
		if slices.Contains(robinPorts, g.Endpoint.URL) {
			// Its decoder hangs on this job, and it says so when asked.
			ids := sortedIDs(sub)
			p.h.wedge(g.Endpoint.URL)
			p.mu.Lock()
			p.sends = append(p.sends, sendRecord{g.Endpoint.URL, ids})
			p.mu.Unlock()
			return nil, &WorkerStuckError{URL: g.Endpoint.URL, After: time.Second, Jobs: ids}
		}
		return p.send(ctx, g, sub, pr)
	}
	jobID := loopers(1, "chap")[0]
	rec := &canaryRecorder{}
	res, err := dispatchOn(testCtx(t), p.h.eps, nil, send, rec.fn, 1)
	if err != nil {
		t.Fatalf("err = %v, want the remembered machine asked, and to take the job", err)
	}
	if r := res[jobID]; r.Text != "T:"+jobID || (r.Endpoint != p.h.eps[2].URL && r.Endpoint != p.h.eps[3].URL) {
		t.Errorf("%s = %+v, want the remembered machine's transcript", jobID, r)
	}
	askedOnce(t, rec.asked, p.h.eps[2].URL, p.h.eps[3].URL)
	if got := sentTo(p); slices.Contains(got, p.h.eps[1].URL) {
		t.Errorf("work went to %v: the second port of the machine the job was stuck on", got)
	}
}

// Nit 1 (the lone endpoint's path): a cancel that lands while the endpoint is
// probed again after its cooldown wait is the cancellation, not "the pool is
// unavailable" (or whatever the refusals alone would end as). The probe only
// looks unreachable because nobody could ask.
func TestSingle_ACancelDuringTheReProbeAfterTheWaitReturnsTheCancellation(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	h := machineHarness(t, []string{batMachine})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	w0 := h.eps[0].URL
	t.Cleanup(func() { clearCooldown(w0) })
	markCanaryFailed(batKey, w0)
	markEndpointFailure(w0)
	after(t, 40*time.Millisecond, func() { clearCooldown(w0) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sg := singleGate{
		paceUntil: time.Now().Add(time.Minute),
		gate: func(eps []Endpoint) ([]gatedEndpoint, []gpuRefusal) {
			cancel()
			// What a probe with a cancelled context reports, and judged as the
			// gate judges any refusal that is not worth waiting for.
			return nil, []gpuRefusal{{URL: eps[0].URL, Reason: "/health could not be read"}}
		},
		noEndpoint: func([]gpuRefusal) error { return ErrPoolUnavailable },
	}
	g := gatedEndpoint{Endpoint: h.eps[0], Probed: true, Health: remoteHealth{MachineID: batMachine}}
	_, err := dispatchSingle(ctx, g, nil, sg, map[string]string{"j0": "unused"}, nil,
		(&faultPool{h: h, answer: answerWithTranscripts, batch: 16}).send, (&canaryRecorder{}).fn)
	if !errors.Is(err, context.Canceled) || IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want the cancellation and not ErrPoolUnavailable", err)
	}
}

// Nit 2: the canary, and the work after it, get what the probe after the wait
// read, not what the gate read minutes earlier: here the worker's identity.
func TestRegate_TheCanaryGetsTheHealthTheProbeAfterTheWaitRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		machines []string
	}{
		{"lone endpoint", []string{""}},
		{"pool", []string{"", robinMachine}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortenPacing(t, 10*time.Millisecond, time.Minute)
			h := machineHarness(t, tc.machines)
			useEndpoints(t, 16, endpointURLs(h.eps)...)
			p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
			t.Cleanup(func() {
				for _, ep := range h.eps {
					clearCooldown(ep.URL)
				}
			})
			w0 := h.eps[0].URL
			markCanaryFailed("u:" + shortHash(w0))
			markCanaryFailed(robinKey) // (a pool's second machine)
			markEndpointFailure(w0)
			after(t, 80*time.Millisecond, func() {
				h.setMachineID(w0, catwomanMachine)
				clearCooldown(w0)
			})
			var sawCanary, sawWork string
			canary := func(_ context.Context, g gatedEndpoint) error {
				if g.Endpoint.URL != w0 {
					return timeoutCanaryErr // a pool's other machine is sick: worker 0 is what is waited for
				}
				sawCanary = g.Health.MachineID
				return nil
			}
			send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pr ProgressFunc) (map[string]BatchResult, error) {
				sawWork = g.Health.MachineID
				return p.send(ctx, g, sub, pr)
			}
			if _, err := dispatchOn(testCtx(t), h.eps, nil, send, canary, 4); err != nil {
				t.Fatalf("err = %v", err)
			}
			if sawCanary != catwomanMachine || sawWork != catwomanMachine {
				t.Errorf("canary saw machine_id %q, work saw %q, want the fresh %q for both", sawCanary, sawWork, catwomanMachine)
			}
		})
	}
}

// Nit 3: a remembered machine is asked only when its LAST port is out of
// cooldown, so a cooldown on one of its ports that outlasts the pacing allowance
// is deferred at once even when another of its ports comes out of cooldown
// inside it. Waiting for the earliest port instead slept until the first one and
// deferred only then.
func TestPending_ARememberedMachineIsWaitedForUntilItsLastPortIsOut(t *testing.T) {
	const allowance = 45 * time.Second // between the 30 s and the 60 s cooldowns below
	shortenPacing(t, 10*time.Millisecond, allowance)
	var p *faultPool
	p = machineFaultPool(t, []string{batMachine, batMachine, batMachine, robinMachine}, func(url, id string) BatchResult {
		if url == p.h.eps[3].URL {
			return BatchResult{Error: timeoutText} // the stand-in fails its own canary
		}
		return BatchResult{Text: "T:" + id}
	})
	t.Cleanup(func() {
		for _, ep := range p.h.eps {
			clearCooldown(ep.URL)
		}
	})
	markCanaryFailed(batKey)
	hold := func() {
		markEndpointFailure(p.h.eps[1].URL) // 30 s
		markEndpointFailure(p.h.eps[2].URL)
		markEndpointFailure(p.h.eps[2].URL) // 60 s
	}
	hold()
	// faultPool.send clears every other endpoint's cooldown; these must hold.
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pr ProgressFunc) (map[string]BatchResult, error) {
		r, err := p.send(ctx, g, sub, pr)
		hold()
		return r, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	_, err := dispatchOn(ctx, p.h.eps, nil, send, p.canary, 8)
	if !errors.Is(err, errCanarySlotBusy) || IsPoolUnavailable(err) {
		t.Fatalf("err = %v after %s, want the no-verdict deferral at once", err, time.Since(start).Round(time.Millisecond))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %s: the last port's cooldown outlasts the allowance, so it must be deferred at once", d)
	}
	for _, port := range p.h.eps[:3] {
		if slices.Contains(p.canaryLog(), port.URL) {
			t.Errorf("canaries = %v: a machine with a port in cooldown must not be asked", p.canaryLog())
		}
	}
}

// Nit 4: a stand-in that has failed in this dispatch is not coming back to take
// the page, so its cooldown is not what a remembered machine's wait is for. Here
// it ends inside the allowance and the remembered machine's does not: the
// remembered machine is deferred at once (no verdict), not after waiting for the
// stand-in's cooldown to end. (What the round loop does after that is its own
// pacing, which the test does not wait out.)
func TestPending_AStandInThatFailedInThisDispatchIsNotWaitedFor(t *testing.T) {
	const allowance = 45 * time.Second
	shortenPacing(t, 10*time.Millisecond, allowance)
	p := poolOf(t, batMachine, batMachine, robinMachine)
	w1, w2 := p.h.eps[1].URL, p.h.eps[2].URL
	markCanaryFailed(batKey)
	hold := func() {
		markEndpointFailure(w1)
		markEndpointFailure(w1) // 60 s: past the allowance
	}
	hold()
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, pr ProgressFunc) (map[string]BatchResult, error) {
		if g.Endpoint.URL == w2 {
			// The dispatcher benches it for 30 s, inside the allowance.
			return nil, errors.New("connection refused")
		}
		r, err := p.send(ctx, g, sub, pr)
		hold()
		return r, err
	}
	logs := logtest.Capture(t, slog.LevelWarn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = dispatchOn(ctx, p.h.eps, nil, send, (&canaryRecorder{}).fn, 8)
	lines := logs.Lines("outlasts")
	if len(lines) != 1 {
		t.Fatalf("%d deferrals logged within 3 s, want 1 (made at once, not after waiting for the stand-in):\n%s", len(lines), logs.String())
	}
	waited := lines[0][strings.Index(lines[0], "waited ")+len("waited "):]
	waited = waited[:strings.IndexAny(waited, ")")]
	if d, perr := time.ParseDuration(waited); perr != nil || d >= time.Second {
		t.Errorf("the deferral says it waited %q, want it made at once: %s", waited, lines[0])
	}
}

// Nit 5: an endpoint the gate refuses for what it offers still reports an
// identity, and the memory of a machine that fails a pool's requirement is moved
// to the identity its worker reports like any other's.
func TestCanaryIdentity_ARefusedEndpointsMemoryMovesToItsNewIdentity(t *testing.T) {
	h := machineHarness(t, []string{catwomanMachine})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	w0 := h.eps[0].URL
	hash := shortHash(w0)
	markCanaryFailed("u:" + hash)
	markCanaryFailed("?" + hash)
	// The pool asks for a backend this worker does not have: refused at the gate.
	_, err := dispatchOn(testCtx(t), h.eps, []string{"cuda"}, nil, (&canaryRecorder{}).fn, 4)
	if err == nil {
		t.Fatal("want the dispatch refused for its requirement")
	}
	newKey := "m:" + catwomanMachine
	if !failedCanaryBefore(newKey) || failedCanaryBefore("u:"+hash) || failedCanaryBefore("?"+hash) {
		t.Errorf("remembered: %s=%v u:=%v ?=%v, want the failure moved to the identity the refused worker reports",
			newKey, failedCanaryBefore(newKey), failedCanaryBefore("u:"+hash), failedCanaryBefore("?"+hash))
	}
	if got := canaryURLMachine(w0); got != newKey {
		t.Errorf("last strong identity of the refused endpoint = %q, want %s", got, newKey)
	}
}

// Nit 6: what is kept per URL is bounded by the configured endpoints. A URL an
// operator has removed leaves the last-strong-identity map and every entry's
// ports at the next dispatch's gate.
func TestCanaryMemory_AURLNoLongerConfiguredIsDropped(t *testing.T) {
	p := poolOf(t, batMachine, batMachine)
	const gone = "http://gotham-gone:8000"
	markCanaryFailed(batKey, gone)
	noteCanaryPort(batKey, gone)
	if canaryURLMachine(gone) != batKey || !hasUnheardPort(batKey, []gpuRefusal{{URL: gone}}, nil) {
		t.Fatal("setup: the URL was not recorded")
	}
	rec := &canaryRecorder{err: timeoutCanaryErr} // it stays remembered, so the entry can be read after
	if _, err := dispatchWith(testCtx(t), p, rec.fn, 4); !IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want ErrPoolUnavailable", err)
	}
	if canaryURLMachine(gone) != "" || hasUnheardPort(batKey, []gpuRefusal{{URL: gone}}, nil) {
		t.Error("a URL that is not configured any more is still remembered")
	}
	if n := canaryURLCount(); n != len(p.h.eps) {
		t.Errorf("%d URLs are attributed to a machine, want the %d configured", n, len(p.h.eps))
	}
}
