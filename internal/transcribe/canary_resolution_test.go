// file: internal/transcribe/canary_resolution_test.go
// version: 1.0.1
// guid: 6c1d9e47-35a2-4b80-8f6e-0d7a2b93c154
// last-edited: 2026-10-27

package transcribe

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// The review of f241ec20a and the owner's decisions on it (D55): a worker's
// identity is resolved by ADDRESS, once, at the gate. A worker that is able to
// report a machine id and missed it this time (it answers as "u:") is the
// machine its URL last reported; a worker that cannot report one (another
// server that took the URL over) is not. Everything a dispatch does by machine
// (the canary memory, which machines are sick, the condemnation limit, the
// kills held, the identity stored with an answer, the steering away from a
// machine that already struck a file) uses that one identity.

const catKey = "m:" + catwomanMachine

// failedEntry is the entry of key as canaryFailed holds it, or nil.
func failedEntry(key string) *canaryEntry {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	return canaryFailed.m[key]
}

// failedKeys is every key canaryFailed remembers, sorted.
func failedKeys() []string {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	var keys []string
	for k := range canaryFailed.m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// R1 of the review: worker 0 answered as "u:" and failed the canary in an
// earlier dispatch, whose URL last reported the machine that also serves port 1.
// The failure is the machine's, so port 1 is asked before it gets work too; it
// was recorded under the weak key alone, and port 1 was sent a chunk with no
// canary.
func TestCanaryResolution_AWeakFailureOfAKnownMachinesPortIsTheMachinesFailure(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	h := machineHarness(t, []string{"", catwomanMachine, robinMachine})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	w0, w1 := h.eps[0].URL, h.eps[1].URL
	// An earlier gate saw both ports report the machine.
	noteCanaryPort(catKey, w0, true)
	noteCanaryPort(catKey, w1, true)
	weak := "u:" + shortHash(w0)
	markCanaryFailed(weak, w0)
	if failedEntry(weak) != nil {
		t.Errorf("the failure was recorded under %s, beside the machine it belongs to (keys %v)", weak, failedKeys())
	}
	if e := failedEntry(catKey); e == nil || !e.ports[w0] || !e.ports[w1] {
		t.Fatalf("entry of %s = %+v, want the machine remembered with both of its ports", catKey, e)
	}
	if !endpointRemembered(catKey, w1) {
		t.Fatal("the machine's other port is not remembered as sick")
	}
	rec := &canaryRecorder{err: timeoutCanaryErr}
	_, err := dispatchOn(testCtx(t), h.eps, nil, p.send, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v, want the sound machine to take the page", err)
	}
	askedOnce(t, rec.asked, w0, w1)
	if got := sentTo(p); slices.Contains(got, w1) || slices.Contains(got, w0) {
		t.Errorf("work went to %v: a port of the machine that failed the canary", got)
	}
}

// R1 end to end: nothing is remembered. Worker 0 answers as "u:" (its URL last
// reported the machine) and worker 1 as the machine; every clip times out, so
// every machine is asked the canary, and worker 0 fails it. The failure is the
// machine's: the next dispatch asks the machine's other port too.
func TestCanaryResolution_AWeakPortFailingTheCanaryRemembersTheWholeMachine(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	h := machineHarness(t, []string{"", catwomanMachine, robinMachine})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	t.Cleanup(func() {
		for _, ep := range h.eps {
			clearCooldown(ep.URL)
		}
	})
	w0, w1 := h.eps[0].URL, h.eps[1].URL
	noteCanaryPort(catKey, w0, true)
	noteCanaryPort(catKey, w1, true)
	p1 := &faultPool{h: h, answer: func(string, string) BatchResult { return BatchResult{Error: timeoutText} }, batch: 16}
	failW0 := func(_ context.Context, g gatedEndpoint) error {
		if g.Endpoint.URL == w0 {
			return timeoutCanaryErr
		}
		return nil
	}
	_, _ = dispatchOn(testCtx(t), h.eps, nil, p1.send, failW0, 48)
	if e := failedEntry(catKey); e == nil || !e.ports[w0] || !e.ports[w1] {
		t.Fatalf("entry of %s = %+v (keys %v), want the machine remembered with both ports", catKey, e, failedKeys())
	}
	if failedEntry("u:"+shortHash(w0)) != nil {
		t.Errorf("the failure was also recorded under the weak key (keys %v)", failedKeys())
	}
	for _, ep := range h.eps {
		clearCooldown(ep.URL)
	}
	p2 := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	rec := &canaryRecorder{err: timeoutCanaryErr}
	if _, err := dispatchOn(testCtx(t), h.eps, nil, p2.send, rec.fn, 8); err != nil {
		t.Fatalf("err = %v, want the sound machine to take the page", err)
	}
	askedOnce(t, rec.asked, w0, w1)
	if slices.Contains(sentTo(p2), w1) {
		t.Errorf("work went to %v: the machine's other port was sent a chunk with no canary", sentTo(p2))
	}
}

// resolveMachine, case by case: only a worker that can report a machine id
// (backend mlx) and answered with a weak identity is the machine its URL last
// reported.
func TestResolveMachine(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const known, unknown = "http://gotham-known:8000", "http://gotham-unknown:8000"
	noteCanaryPort(catKey, known, true)
	for _, tc := range []struct {
		name      string
		url, raw  string
		idCapable bool
		want      string
	}{
		{"strong", known, robinKey, true, robinKey},
		{"weak from a worker that can report an id", known, "u:" + shortHash(known), true, catKey},
		{"weak from a worker that cannot", known, "u:" + shortHash(known), false, "u:" + shortHash(known)},
		{"weak, nothing known of the URL", unknown, "u:" + shortHash(unknown), true, "u:" + shortHash(unknown)},
		{"no /health read", known, "?" + shortHash(known), false, "?" + shortHash(known)},
		{"a refusal of an unprobed endpoint", known, "", false, ""},
	} {
		if got := resolveMachine(tc.url, tc.raw, tc.idCapable); got != tc.want {
			t.Errorf("%s: resolveMachine(%q, %q, %v) = %q, want %q", tc.name, tc.url, tc.raw, tc.idCapable, got, tc.want)
		}
	}
}

// S-A of the review: a URL whose worker is replaced by one that cannot report a
// machine id (scripts/whisper_server.py, backend faster-whisper) is not the
// machine remembered for it. The probe takes the URL off the machine, and a
// weaker identity from a worker that CAN report an id still leaves it.
func TestCanaryMemory_AWorkerThatCannotReportAnIdTakesItsURLOffTheMachine(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-takeover:8000"
	weak := "u:" + shortHash(url)
	markCanaryFailed(catKey, url)
	noteCanaryPort(catKey, url, true)
	noteCanaryPort(weak, url, true) // the machine's own worker, late with its id
	if canaryURLMachine(url) != catKey || failedEntry(catKey) == nil || !failedEntry(catKey).ports[url] {
		t.Fatal("a weak identity from a worker that can report an id took the URL off the machine")
	}
	noteCanaryPort(weak, url, false) // another server answers there
	if got := canaryURLMachine(url); got != "" {
		t.Errorf("last strong identity = %q, want none: this worker is not the machine", got)
	}
	if failedEntry(catKey).ports[url] || endpointRemembered(weak, url) {
		t.Error("the machine is still remembered through a URL whose worker is another server")
	}
}

// S-A end to end: worker 0's URL last reported the remembered machine, but the
// worker there now is a faster-whisper server. It is not asked the machine's
// canary and is not benched for it; the machine's real port is.
func TestCanaryMemory_AFasterWhisperWorkerAtARememberedMachinesURLIsNotAskedItsCanary(t *testing.T) {
	shortenPacing(t, 10*time.Millisecond, time.Minute)
	h := machineHarness(t, []string{"", catwomanMachine})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	t.Cleanup(func() {
		for _, ep := range h.eps {
			clearCooldown(ep.URL)
		}
	})
	w0, w1 := h.eps[0].URL, h.eps[1].URL
	h.setBackend(w0, "faster-whisper")
	noteCanaryPort(catKey, w0, true) // what the URL's earlier worker reported
	noteCanaryPort(catKey, w1, true)
	markCanaryFailed(catKey, w0, w1)
	rec := &canaryRecorder{}
	res, err := dispatchOn(testCtx(t), h.eps, nil, p.send, rec.fn, 8)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	requireTranscripts(t, res, 8)
	if !slices.Equal(rec.asked, []string{w1}) {
		t.Errorf("canaries = %v, want only the machine's own port %s asked", rec.asked, w1)
	}
	if !slices.Contains(sentTo(p), w0) {
		t.Errorf("work went to %v, want the faster-whisper worker to take work: it is not the machine that failed", sentTo(p))
	}
	if canaryURLMachine(w0) != "" {
		t.Errorf("the URL is still attributed to %s", canaryURLMachine(w0))
	}
}

// gpuRefusal carries whether the refused worker can report a machine id, so a
// refusal is judged as a probe is: a refused faster-whisper worker takes its URL
// off a machine, a refused mlx worker that answered as "u:" does not.
func TestGate_ARefusalCarriesWhetherItsWorkerCanReportAnId(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	h := machineHarness(t, []string{"", "", catwomanMachine})
	h.setBackend(h.eps[1].URL, "faster-whisper")
	w0, w1, w2 := h.eps[0].URL, h.eps[1].URL, h.eps[2].URL
	noteCanaryPort(catKey, w0, true)
	noteCanaryPort(catKey, w1, true)
	markCanaryFailed(catKey, w0, w1, w2)
	kept, refused := gateEndpoints(testCtx(t), h.eps, []string{"cuda"}) // the workers are Metal
	if len(kept) != 0 || len(refused) != 3 {
		t.Fatalf("kept %d, refused %d, want all three refused", len(kept), len(refused))
	}
	for i, want := range []struct {
		capable bool
		machine string
	}{{true, catKey}, {false, "u:" + shortHash(w1)}, {true, catKey}} {
		if refused[i].IDCapable != want.capable || refused[i].Machine != want.machine {
			t.Errorf("refusal %d = capable %v machine %q, want %v %q", i, refused[i].IDCapable, refused[i].Machine, want.capable, want.machine)
		}
	}
	noteCanaryProbes(kept, refused)
	if canaryURLMachine(w0) != catKey || !failedEntry(catKey).ports[w0] {
		t.Error("a refused worker that can report an id and answered as u: was taken off the machine")
	}
	if canaryURLMachine(w1) != "" || failedEntry(catKey).ports[w1] {
		t.Error("a refused faster-whisper worker is still attributed to the machine")
	}
}

// D55: the machine behind a port that answers as "u:" is one machine with its
// other ports for a condemnation: a lone job stuck on both is stuck on one
// machine, which is no second opinion.
func TestCanaryResolution_ACondemnationCountsAWeakPortAndItsMachineOnce(t *testing.T) {
	h := machineHarness(t, []string{"", catwomanMachine}, "j00")
	h.wedgeOnPoison = true
	useEndpoints(t, 1, endpointURLs(h.eps)...) // it forgets the memory, so the URL is noted after it
	noteCanaryPort(catKey, h.eps[0].URL, true)
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j00": "unused"}, poolOptions{}, h.send)
	if err != nil {
		t.Fatalf("err = %v, want a verdict, not a dispatch failure", err)
	}
	r := res["j00"]
	if !r.Condemned || r.Worker != catKey {
		t.Fatalf("verdict = %+v, want a condemnation naming the one machine once", r)
	}
	if n := len(h.sentTo("j00")); n != 1 {
		t.Fatalf("the job was sent %d times; the machine's other port is no second opinion", n)
	}
}

// D55: a machine that timeout-faulted is benched on every port, including the
// port that answers as "u:", whose units can never fault.
func TestCanaryResolution_ASickMachineIsBenchedOnItsWeakPortToo(t *testing.T) {
	var p *faultPool
	p = machineFaultPool(t, []string{catwomanMachine, "", robinMachine}, func(url, id string) BatchResult {
		if url == p.h.eps[0].URL || url == p.h.eps[1].URL {
			return BatchResult{Error: timeoutText}
		}
		return BatchResult{Text: "T:" + id}
	})
	noteCanaryPort(catKey, p.h.eps[1].URL, true)
	ids := loopers(32, "chap")
	res, err := p.run(t, ids...)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	for _, id := range ids {
		r := res[id]
		if IsStrikeError(r.Error) {
			t.Errorf("%s = %+v: a strike from a port of a machine that faulted was believed", id, r)
		} else if r.Text != "T:"+id || r.Worker != "m:"+robinMachine {
			t.Errorf("%s = %+v, want the sound machine's transcript", id, r)
		}
	}
}

// D55: a decoder kill withheld from a machine is never offered to any port of
// it again, a port that answers as "u:" included.
func TestCanaryResolution_AKillWithheldFromAMachineIsNotOfferedToItsWeakPort(t *testing.T) {
	p := machineFaultPool(t, []string{catwomanMachine, ""}, func(_ string, id string) BatchResult {
		if id == canaryJobID {
			return BatchResult{Text: "Welcome to Gotham City."}
		}
		return BatchResult{Error: killedText}
	})
	for i := range p.h.eps {
		p.h.eps[i].Priority = 50
	}
	noteCanaryPort(catKey, p.h.eps[1].URL, true)
	ids := loopers(32, "chap")
	if _, err := p.run(t, ids...); err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	perJob := map[string]int{}
	for _, s := range p.sendLog() {
		for _, id := range s.ids {
			perJob[id]++
		}
	}
	for _, id := range ids {
		if perJob[id] > 2 {
			t.Errorf("%s was sent %d times, want at most 2 (the faulted request, then one unit): a kill was offered to the machine's other port", id, perJob[id])
		}
	}
}

// D55, the identity stored with an answer: the machine, whatever its worker
// answers as. And steering a struck job away: by the machine the file's strike
// names however the strike named it.
func TestCanaryResolution_AnAnswerAndASteerNameTheMachineNotTheWeakIdentity(t *testing.T) {
	t.Run("the stored identity and the steering follow the machine", func(t *testing.T) {
		h := machineHarness(t, []string{"", catwomanMachine, robinMachine})
		h.eps[2].Priority = 50
		useEndpoints(t, 16, endpointURLs(h.eps)...)
		noteCanaryPort(catKey, h.eps[0].URL, true)
		jobs := map[string]string{"batarang": "x", "fresh": "x"}
		res, err := dispatchPool(context.Background(), h.eps, nil, jobs, poolOptions{avoid: map[string]string{"batarang": catKey}}, h.send)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got := res["batarang"]; got.Endpoint != h.eps[2].URL || got.Worker != "m:"+robinMachine {
			t.Errorf("the struck job went to %s (worker %q), want the other machine", got.Endpoint, got.Worker)
		}
		if got := res["fresh"]; got.Endpoint != h.eps[0].URL || got.Worker != catKey {
			t.Errorf("a job with no strike went to %s (worker %q), want priority 1 stored as the machine %s", got.Endpoint, got.Worker, catKey)
		}
	})
	t.Run("a strike stored under the address of a worker that answers with an id now", func(t *testing.T) {
		h := machineHarness(t, []string{catwomanMachine, robinMachine})
		h.eps[1].Priority = 50
		useEndpoints(t, 16, endpointURLs(h.eps)...)
		avoid := map[string]string{"batarang": "u:" + shortHash(h.eps[0].URL)}
		res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"batarang": "x"}, poolOptions{avoid: avoid}, h.send)
		if err != nil || res["batarang"].Endpoint != h.eps[1].URL {
			t.Fatalf("res = %+v err = %v, want the job on the other machine", res["batarang"], err)
		}
	})
	t.Run("a strike stored under the address of a worker that still answers as u:", func(t *testing.T) {
		h := machineHarness(t, []string{"", robinMachine})
		h.eps[1].Priority = 50
		useEndpoints(t, 16, endpointURLs(h.eps)...)
		avoid := map[string]string{"batarang": "u:" + shortHash(h.eps[0].URL)}
		res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"batarang": "x"}, poolOptions{avoid: avoid}, h.send)
		if err != nil || res["batarang"].Endpoint != h.eps[1].URL {
			t.Fatalf("res = %+v err = %v, want the job on the other machine", res["batarang"], err)
		}
	})
}

// The lone endpoint's path keeps the identity the gate resolved for every
// memory call: a worker that answers as "u:" while its machine is remembered is
// asked, fails again, and the machine's entry (not a new weak one) records it.
func TestCanaryResolution_ALoneWeakEndpointRecordsItsFailureOnTheMachine(t *testing.T) {
	h := machineHarness(t, []string{""})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	w0 := h.eps[0].URL
	noteCanaryPort(catKey, w0, true)
	markCanaryFailed(catKey, w0)
	before := failedEntry(catKey).gen
	rec := &canaryRecorder{err: timeoutCanaryErr}
	if _, err := dispatchOn(testCtx(t), h.eps, nil, p.send, rec.fn, 8); !IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want ErrPoolUnavailable", err)
	}
	if e := failedEntry(catKey); e == nil || e.gen == before {
		t.Errorf("entry of %s = %+v: the failure was not recorded on the machine", catKey, e)
	}
	if got := failedKeys(); !slices.Equal(got, []string{catKey}) {
		t.Errorf("remembered keys = %v, want only %s", got, catKey)
	}
}

// N1: every URL the url-to-machine map gives a remembered machine is one of its
// ports, which is why resolving a weak identity through the ports alone finds
// what asking the map first found.
func TestCanaryMemory_ARememberedMachineListsEveryURLTheMapGivesIt(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const a, b, c = "http://gotham-a:8000", "http://gotham-b:8000", "http://gotham-c:8000"
	check := func(step string) {
		t.Helper()
		canaryFailed.mu.Lock()
		defer canaryFailed.mu.Unlock()
		for u, k := range canaryFailed.attributed {
			if e := canaryFailed.m[k]; e != nil && !e.ports[u] {
				t.Errorf("after %s: %s is attributed to %s, which remembers it as no port (ports %v)", step, u, k, e.ports)
			}
		}
	}
	noteCanaryPort(catKey, a, true)
	noteCanaryPort(catKey, b, true)
	check("two probes")
	markCanaryFailed(catKey, c)
	check("a failure")
	noteCanaryPort(robinKey, b, true)
	check("a port that moved machines")
	noteCanaryPort("u:"+shortHash(a), a, false)
	check("a port taken over")
	markCanaryFailed("u:"+shortHash(a), a)
	check("a weak failure")
	noteCanaryPort(catKey, a, true)
	check("a port that came back")
	for _, u := range []string{a, b, c} {
		want := canaryURLMachine(u)
		if failedEntry("u:"+shortHash(u)) != nil {
			continue // its own entry is asked first
		}
		if got := canaryMemoryKey("u:"+shortHash(u), u); want != "" && failedEntry(want) != nil && got != want {
			t.Errorf("a weak identity at %s resolves to %q, want %q", u, got, want)
		}
	}
}

// N3: an entry whose every port has left the configuration is dropped, and one
// that keeps a port, or never had one, is not.
func TestCanaryMemory_AnEntryWhoseEveryURLIsNoLongerConfiguredIsDropped(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const kept, gone1, gone2 = "http://gotham-kept:8000", "http://gotham-gone1:8000", "http://gotham-gone2:8000"
	markCanaryFailed(batKey, gone1, gone2)
	markCanaryFailed(robinKey, gone1, kept)
	markCanaryFailed(catKey) // never had a port
	pruneUnconfiguredCanaryURLs([]Endpoint{{URL: kept}})
	if got, want := failedKeys(), []string{robinKey, catKey}; !slices.Equal(got, want) {
		t.Errorf("remembered after the prune = %v, want %v", got, want)
	}
	if e := failedEntry(robinKey); e == nil || e.ports[gone1] || !e.ports[kept] {
		t.Errorf("ports of %s = %+v, want only the configured one", robinKey, e)
	}
}

// splitMachinePool is a pool of three ports, 0 (the machine, strong), 1 (the same
// machine, answering as "u:" this time) and 2 (another machine). faulty names the
// port of the machine whose every answer is faultText; the machine's other port
// answers its first clip with loneText (one answer of sixteen is no fault of its
// own) and the rest with a transcript; the other machine always answers.
func splitMachinePool(t *testing.T, weakFaults bool, faultText, loneText string) (*faultPool, []string) {
	t.Helper()
	var (
		p    *faultPool
		mu   sync.Mutex
		lone bool
	)
	faulty := 0
	if weakFaults {
		faulty = 1
	}
	p = machineFaultPool(t, []string{catwomanMachine, "", robinMachine}, func(url, id string) BatchResult {
		switch url {
		case p.h.eps[faulty].URL:
			if id != canaryJobID {
				return BatchResult{Error: faultText}
			}
		case p.h.eps[1-faulty].URL:
			mu.Lock()
			first := !lone && id != canaryJobID
			lone = lone || first
			mu.Unlock()
			if first {
				return BatchResult{Error: loneText}
			}
		}
		return BatchResult{Text: "T:" + id}
	})
	for i := range p.h.eps {
		p.h.eps[i].Priority = 50
	}
	noteCanaryPort(catKey, p.h.eps[1].URL, true)
	return p, loopers(48, "chap")
}

// D55: a machine that timeout-faulted is sick on its other ports whatever their
// workers answer as, in both directions: a weak port that faulted takes its
// strong sibling's strike out of play, and a strong port that faulted takes a
// weak sibling's.
func TestCanaryResolution_ASickMachinesSiblingStrikeIsNotBelievedWhicheverPortFaulted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		weakFaults bool
	}{{"the port answering as u: faulted", true}, {"the port answering as the machine faulted", false}} {
		t.Run(tc.name, func(t *testing.T) {
			p, ids := splitMachinePool(t, tc.weakFaults, timeoutText, timeoutText)
			res, err := p.run(t, ids...)
			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			for _, id := range ids {
				if r := res[id]; r.Text != "T:"+id {
					t.Errorf("%s = %+v, want the transcript: a strike from a port of the machine that faulted was believed", id, r)
				}
			}
		})
	}
}

// D55: what the canary clears is the machine's timeouts, not its decoder kills:
// once the machine has passed the canary, a decoder kill that either of its ports
// answers is still withheld, whichever port's fault was the kills and whichever
// identity each answers as.
func TestCanaryResolution_AKillIsStillHeldFromAMachineThatPassedTheCanary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		weakFaults bool
	}{{"the port answering as u: faulted", true}, {"the port answering as the machine faulted", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				p    *faultPool
				mu   sync.Mutex
				lone bool
			)
			faulty := 0
			if tc.weakFaults {
				faulty = 1
			}
			p = machineFaultPool(t, []string{catwomanMachine, ""}, func(url, id string) BatchResult {
				if id == canaryJobID {
					return BatchResult{Text: "Welcome to Gotham City."}
				}
				if url == p.h.eps[faulty].URL {
					return BatchResult{Error: killedText}
				}
				mu.Lock()
				first := !lone
				lone = true
				mu.Unlock()
				if first {
					return BatchResult{Error: killedText}
				}
				return BatchResult{Text: "T:" + id}
			})
			for i := range p.h.eps {
				p.h.eps[i].Priority = 50
			}
			noteCanaryPort(catKey, p.h.eps[1].URL, true)
			ids := loopers(32, "chap")
			res, err := p.run(t, ids...)
			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if len(p.canaryLog()) == 0 {
				t.Fatal("the machine was never asked the canary: the case under test (a kill after the canary passed) did not arise")
			}
			if len(res) != len(ids) {
				t.Fatalf("%d of %d jobs have a result", len(res), len(ids))
			}
			for _, id := range ids {
				r := res[id]
				if PathErrorCode(r.Error) == PathErrDecoderKilled {
					t.Errorf("%s = %+v: a decoder kill from a machine whose fault was kills was believed after its canary", id, r)
				}
				if r.Text != "T:"+id && PathErrorCode(r.Error) != PathErrWorkerFault {
					t.Errorf("%s = %+v, want a transcript or a deferral", id, r)
				}
			}
		})
	}
}

// D55: a sick machine is asked the canary on every port, the one that answers
// as "u:" included (it is asked, and so cleared, with the machine).
func TestCanaryResolution_ASickMachineIsAskedTheCanaryOnItsWeakPortToo(t *testing.T) {
	var p *faultPool
	p = machineFaultPool(t, []string{catwomanMachine, ""}, func(url, id string) BatchResult {
		if id == canaryJobID {
			return BatchResult{Text: "Welcome to Gotham City."}
		}
		return BatchResult{Error: timeoutText}
	})
	for i := range p.h.eps {
		p.h.eps[i].Priority = 50
	}
	noteCanaryPort(catKey, p.h.eps[1].URL, true)
	if _, err := p.run(t, loopers(32, "chap")...); err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	asked := p.canaryLog()
	for _, ep := range p.h.eps {
		if !slices.Contains(asked, ep.URL) {
			t.Errorf("canaries = %v, want %s asked: it is a port of the sick machine", asked, ep.URL)
		}
	}
}

// D55: a lone job stuck on one port of a machine is not offered to the machine's
// other port, the one that answers as "u:" included: that is the same machine,
// and its verdict needs a second one.
func TestCanaryResolution_ALoneStuckJobIsNotOfferedToTheStuckMachinesWeakPort(t *testing.T) {
	h := machineHarness(t, []string{catwomanMachine, "", robinMachine}, "j00")
	h.wedgeOnPoison = true
	useEndpoints(t, 1, endpointURLs(h.eps)...)
	noteCanaryPort(catKey, h.eps[1].URL, true)
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j00": "unused"}, poolOptions{}, h.send)
	if err != nil {
		t.Fatalf("err = %v, want a verdict, not a dispatch failure", err)
	}
	if r := res["j00"]; !r.Condemned {
		t.Fatalf("verdict = %+v, want a condemnation", r)
	}
	if got, want := h.sentTo("j00"), []string{h.eps[0].URL, h.eps[2].URL}; !slices.Equal(got, want) {
		t.Errorf("the job was sent to %v, want %v: the stuck machine's other port is no second opinion", got, want)
	}
}

// D55, the identity stored with an answer on the lone-endpoint path: the machine
// its address last reported, though its worker missed its machine id this time.
func TestCanaryResolution_ALoneWeakEndpointsAnswerNamesTheMachine(t *testing.T) {
	h := machineHarness(t, []string{""})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	noteCanaryPort(catKey, h.eps[0].URL, true)
	res, err := dispatchPool(testCtx(t), h.eps, nil, map[string]string{"j00": "unused"}, poolOptions{}, h.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if r := res["j00"]; r.Text != "T:j00" || r.Worker != catKey {
		t.Errorf("answer = %+v, want a transcript naming %s", r, catKey)
	}
}

// A probe whose weak answer was resolved to a machine ties the URL to it like a
// strong one: the URL leaves any other machine's ports.
func TestCanaryResolution_AResolvedWeakProbeTiesItsURLToTheMachine(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-a:8000"
	noteCanaryPort(catKey, url, true)
	markCanaryFailed(robinKey, url) // a stale record: the URL was once believed to be the other machine's
	g := gatedEndpoint{Endpoint: Endpoint{URL: url}, Probed: true, Health: remoteHealth{Backend: backendMLX}, resolved: catKey}
	noteCanaryProbes([]gatedEndpoint{g}, nil)
	if e := failedEntry(robinKey); e == nil || e.ports[url] {
		t.Errorf("entry of %s = %+v: the URL a probe resolved to %s is still its port", robinKey, e, catKey)
	}
}

// An entry kept under the weak identity of a URL moves to the machine the URL
// resolves to, once a gate resolves it.
func TestCanaryResolution_AnEntryUnderAWeakKeyMovesToTheMachineItsURLResolvesTo(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-a:8000"
	weak := "u:" + shortHash(url)
	markCanaryFailed(weak, url)
	noteCanaryPort(catKey, url, true)
	g := gatedEndpoint{Endpoint: Endpoint{URL: url}, Probed: true, Health: remoteHealth{Backend: backendMLX}, resolved: catKey}
	pruneStaleCanaryIdentities(g)
	if failedEntry(weak) != nil {
		t.Errorf("the entry stayed under %s (keys %v)", weak, failedKeys())
	}
	if e := failedEntry(catKey); e == nil || !e.ports[url] {
		t.Errorf("entry of %s = %+v, want the failure carried to the machine", catKey, e)
	}
}

// Every refusal a dispatch's gate builds carries whether its worker can report
// an id, the ones the pool adds to the capability gate (a wedged worker, one
// whose decoder cannot start, one a caller refuses) as much as the capability
// refusals. The worker answers as "u:" and its URL is nobody's known port: a
// worker that can report an id leaves the other machine's record of the URL
// alone (it missed its id this time), one that cannot (faster-whisper) is not
// that machine's.
func TestGate_EveryRefusalTheDispatchBuildsCarriesWhetherItsWorkerCanReportAnId(t *testing.T) {
	kinds := []struct {
		name   string
		refuse func(h *splitHarness, url string) func(gatedEndpoint) string
	}{
		{"a wedged worker", func(h *splitHarness, url string) func(gatedEndpoint) string {
			h.wedge(url)
			return nil
		}},
		{"a worker whose decoder cannot start", func(h *splitHarness, url string) func(gatedEndpoint) string {
			h.setChildFault(url, true)
			return nil
		}},
		{"a worker the caller refuses", func(_ *splitHarness, url string) func(gatedEndpoint) string {
			return func(g gatedEndpoint) string {
				if g.Endpoint.URL == url {
					return "the mount is unreadable"
				}
				return ""
			}
		}},
	}
	for _, kind := range kinds {
		for _, backend := range []string{backendMLX, "faster-whisper"} {
			t.Run(kind.name+" ("+backend+")", func(t *testing.T) {
				h := machineHarness(t, []string{catwomanMachine, "", catwomanMachine})
				useEndpoints(t, 16, endpointURLs(h.eps)...)
				weak := h.eps[1].URL
				h.setBackend(weak, backend)
				extra := kind.refuse(h, weak)
				markCanaryFailed(robinKey, weak) // another machine's record lists the URL
				p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
				jobs := map[string]string{"j00": "unused", "j01": "unused"}
				if _, err := dispatchPool(testCtx(t), h.eps, nil, jobs, poolOptions{extraRefusal: extra, canary: p.canary}, p.send); err != nil {
					t.Fatalf("err = %v", err)
				}
				if slices.Contains(sentTo(p), weak) {
					t.Fatalf("work went to %v: the refused port took work", sentTo(p))
				}
				e := failedEntry(robinKey)
				if e == nil {
					t.Fatalf("the record of %s was lost (keys %v)", robinKey, failedKeys())
				}
				if want := backend == backendMLX; e.ports[weak] != want {
					t.Errorf("%s is a port of %s = %v, want %v", weak, robinKey, e.ports[weak], want)
				}
			})
		}
	}
}

// N6 of the review, the property D55 asks for: the lone-endpoint path records its
// failure under the identity its gate resolved, not under one it works out again
// when it records. Another dispatch's probe takes the URL over (a faster-whisper
// worker, which cannot be the machine) while this one asks the canary; the
// failure is still the machine's the dispatch asked about, and is not recorded
// under a "u:" key beside it.
func TestCanaryResolution_ALoneEndpointRecordsItsFailureUnderTheIdentityItsGateResolved(t *testing.T) {
	h := machineHarness(t, []string{""})
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	p := &faultPool{h: h, answer: answerWithTranscripts, batch: 16}
	w0 := h.eps[0].URL
	weak := "u:" + shortHash(w0)
	noteCanaryPort(catKey, w0, true)
	markCanaryFailed(catKey, w0)
	before := failedEntry(catKey).gen
	takeOver := func(context.Context, gatedEndpoint) error {
		noteCanaryPort(weak, w0, false)
		return timeoutCanaryErr
	}
	if _, err := dispatchOn(testCtx(t), h.eps, nil, p.send, takeOver, 8); !IsPoolUnavailable(err) {
		t.Fatalf("err = %v, want ErrPoolUnavailable", err)
	}
	if e := failedEntry(catKey); e == nil || e.gen == before {
		t.Errorf("entry of %s = %+v: the failure of the machine the dispatch asked about was not recorded on it", catKey, e)
	}
	if failedEntry(weak) != nil {
		t.Errorf("the failure was recorded under %s (keys %v)", weak, failedKeys())
	}
}
