// file: internal/transcribe/workerswap_test.go
// version: 1.1.0
// guid: 2c80f6dd-e49e-4cf4-acef-60b49489f7aa
// last-edited: 2026-10-30

package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A port handed to another machine while a request to it is in flight (a tunnel
// that comes back from another Mac): see workerswap.go.

const swappedInMachine = "cccc0003"

// swapHealth is a /health body of an id-capable worker that reports busy_seconds
// (so the liveness poller runs) and the given machine id.
func swapHealth(id string) map[string]any {
	b := map[string]any{"busy_seconds": 1.0, "busy_limit_seconds": 60.0, "backend": backendMLX}
	if id != "" {
		b["machine_id"] = id
	}
	return b
}

func requireNoStrikeKind(t *testing.T, err error) {
	t.Helper()
	var te *TransportError
	if IsWorkerStuck(err) || IsPoolUnavailable(err) || IsWorkerFault(err) || (errors.As(err, &te) && te.Recognized) {
		t.Fatalf("err = %T %v: a swap is neither stuck, faulted, nor an outage", err, err)
	}
}

// waitOrAnswer is a request that ends only when it is cancelled; if nothing
// cancels it, it is answered after a while (so a missing check fails an
// assertion instead of hanging the test).
func waitOrAnswer(answer time.Duration) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(answer):
			return nil
		}
	}
}

func TestLiveness_AWorkerThatAnswersAsAnotherMachineMidRequestEndsTheRequestWithoutBlame(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	var n atomic.Int32
	srv, _ := probeServer(t, func() (int, map[string]any) {
		if n.Add(1) < 3 {
			return http.StatusOK, swapHealth(batMachine)
		}
		return http.StatusOK, swapHealth(swappedInMachine)
	})
	noteCanaryPort(batKey, srv.URL, true)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})

	start := time.Now()
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a", "b"}, waitOrAnswer(10*time.Second))
	sw, ok := errors.AsType[*WorkerSwappedError](err)
	if !ok {
		t.Fatalf("err = %T %v, want a *WorkerSwappedError", err, err)
	}
	if sw.URL != srv.URL || sw.Was != batKey || sw.Now != "m:"+swappedInMachine || len(sw.Jobs) != 2 {
		t.Errorf("swap error = %+v", sw)
	}
	if !IsWorkerSwapped(err) || !errors.Is(err, errPortChangedHands) {
		t.Errorf("err = %v: must read as a swap and as a port that changed hands", err)
	}
	requireNoStrikeKind(t, err)
	// Wrapped by classifyTransport, as the dispatchers return it, it is still not
	// a recognised network failure (the ops would count that as an outage).
	if wrapped := classifyTransport([]string{srv.URL}, err); !IsTransport(wrapped) {
		t.Fatalf("classifyTransport lost the type: %v", wrapped)
	} else {
		requireNoStrikeKind(t, wrapped)
	}
	if strings.Contains(strings.ToLower(err.Error()), "context") {
		t.Errorf("message %q must not read as a transport failure", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s: the request ran to its end instead of being ended by the poll", d)
	}
}

// The swap check runs before the stuck check: a decode past its deadline on the
// NEW machine says nothing about the old one.
func TestLiveness_ASwappedWorkerThatIsAlsoWedgedIsASwapNotStuck(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	body := swapHealth(swappedInMachine)
	body["busy_seconds"] = 500.0
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusOK, body })
	noteCanaryPort(batKey, srv.URL, true)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"}, waitOrAnswer(10*time.Second))
	if !IsWorkerSwapped(err) || IsWorkerStuck(err) {
		t.Fatalf("err = %T %v, want a swap and not a stuck worker", err, err)
	}
}

// A worker that missed its machine id answers as "u:": it is still the machine
// last seen behind the URL (resolveMachine), not a swap.
func TestLiveness_AWorkerThatMissedItsMachineIDMidRequestIsNotASwap(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	srv, hits := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth("") })
	noteCanaryPort(batKey, srv.URL, true) // the link the gate made: the URL is the bat machine
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"}, waitOrAnswer(300*time.Millisecond))
	if err != nil {
		t.Fatalf("a worker that only missed its machine id was treated as swapped: %v", err)
	}
	if hits.Load() < 2 {
		t.Errorf("probed %d time(s): the check never ran", hits.Load())
	}
}

// Control for the test above: the "u:" is excused only through the link to the
// machine last seen at the URL. Without it the worker stands for itself, as it
// does at the gate and after a cooldown wait (D58).
func TestLiveness_AWeakIdentityWithNoLinkToTheFrozenMachineIsASwap(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth("") })
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"}, waitOrAnswer(10*time.Second))
	if !IsWorkerSwapped(err) {
		t.Fatalf("err = %v, want a swap", err)
	}
}

func TestLiveness_ARequestOnTheSameMachineIsUnchanged(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	srv, hits := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(batMachine) })
	noteCanaryPort(batKey, srv.URL, true)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	if err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"}, waitOrAnswer(300*time.Millisecond)); err != nil {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() < 2 {
		t.Errorf("probed %d time(s)", hits.Load())
	}
}

// A request sent with no frozen identity (any caller outside the dispatchers) and
// one frozen for a port whose machine was unknown are not checked.
func TestLiveness_ARequestWithNoFrozenIdentityIsNotCheckedForASwap(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(swappedInMachine) })
	for name, ctx := range map[string]context.Context{
		"no identity": context.Background(),
		"unknown":     withFrozenWorker(context.Background(), frozenWorker{key: "?abc", capable: true}),
		"empty":       withFrozenWorker(context.Background(), frozenWorker{key: "", capable: true}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, nil, waitOrAnswer(200*time.Millisecond)); err != nil {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// The lone endpoint keeps a worker that gained a machine id (reprobed): its
// frozen "u:" does not make that worker a swap, while a pool's rule does.
func TestLiveness_ALoneEndpointThatGainedItsMachineIDIsNotASwapButAPoolPortIs(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(batMachine) })
	weak := "u:" + shortHash(srv.URL)
	lone := withFrozenWorker(context.Background(), frozenWorker{key: weak, capable: true, lone: true})
	if err := superviseRequest(lone, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, nil, waitOrAnswer(200*time.Millisecond)); err != nil {
		t.Fatalf("lone endpoint: err = %v", err)
	}
	pool := withFrozenWorker(context.Background(), frozenWorker{key: weak, capable: true})
	if err := superviseRequest(pool, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, nil, waitOrAnswer(10*time.Second)); !IsWorkerSwapped(err) {
		t.Fatalf("pool port: err = %v, want a swap", err)
	}
}

// A response that raced the poll's cancel is withheld too.
func TestLiveness_AResponseThatRacesTheSwapCancelIsWithheld(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(swappedInMachine) })
	noteCanaryPort(batKey, srv.URL, true)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	// A request that ignores its context and answers once the poll has fired.
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"}, func(context.Context) error {
		time.Sleep(300 * time.Millisecond)
		return nil
	})
	if !IsWorkerSwapped(err) {
		t.Fatalf("err = %v, want the answer withheld as a swap", err)
	}
}

// swapSend wraps a faultPool's sender: the first request to each url in swaps is
// a long one during which the port is handed to another machine (its /health
// reports id from then on, "" for a worker that missed its id). The request goes
// through the real superviseRequest, as the transports' do, with the context the
// dispatcher gave it, and lasts hold unless the poll ends it. It carries an answer for some jobs, as a request does when
// earlier chunks completed, which the dispatcher must withhold with the rest.
func swapSend(t *testing.T, p *faultPool, swaps map[string]string, hold time.Duration, mu *sync.Mutex, swapped map[string]bool) endpointSender[string] {
	t.Helper()
	return func(ctx context.Context, g gatedEndpoint, sub map[string]string, pf ProgressFunc) (map[string]BatchResult, error) {
		url := g.Endpoint.URL
		id, ok := swaps[url]
		mu.Lock()
		first := ok && !swapped[url]
		swapped[url] = true
		mu.Unlock()
		if !first {
			return p.send(ctx, g, sub, pf)
		}
		ids := sortedIDs(sub)
		p.mu.Lock()
		p.sends = append(p.sends, sendRecord{url, ids})
		p.mu.Unlock()
		p.h.setMachineID(url, id)
		earlier := map[string]BatchResult{ids[0]: {Text: "T:" + ids[0]}}
		if len(ids) > 1 {
			earlier[ids[1]] = BatchResult{Error: timeoutText}
		}
		err := superviseRequest(ctx, url, g.Health, time.Hour, ids, waitOrAnswer(hold))
		if err == nil {
			// Not ended by the poll: the transport would return the answers.
			out := make(map[string]BatchResult, len(ids))
			for _, jid := range ids {
				out[jid] = BatchResult{Text: "T:" + jid}
			}
			return out, nil
		}
		return earlier, err
	}
}

func swapPool(t *testing.T, machines ...string) *faultPool {
	t.Helper()
	freshCanaryMemory(t)
	shortLiveness(t, 20*time.Millisecond)
	shortenPacing(t, 10*time.Millisecond, 2*time.Second)
	return poolOf(t, machines...)
}

func TestIdentitySwap_APortHandedToAnotherMachineMidRequestStoresNothingUnderTheOldIdentityAndTheDispatchContinues(t *testing.T) {
	p := swapPool(t, robinMachine, batMachine)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	var mu sync.Mutex
	send := swapSend(t, p, map[string]string{port0: swappedInMachine}, 10*time.Second, &mu, map[string]bool{})

	res, err := dispatchOn(testCtx(t), p.h.eps, nil, send, p.canary, 4)
	if err != nil {
		t.Fatalf("err = %v: the other port could take every job", err)
	}
	for _, id := range loopers(4, "chap") {
		r := res[id]
		if r.Text != "T:"+id || r.Error != "" {
			t.Errorf("%s = %+v, want the other port's transcript", id, r)
		}
		if r.Endpoint != port1 || r.Worker != batKey {
			t.Errorf("%s answered by endpoint=%q worker=%q, want port 1 and %s: nothing may be stored under the frozen identity of the port that was swapped", id, r.Endpoint, r.Worker, batKey)
		}
		if r.Condemned || IsStrikeError(r.Error) {
			t.Errorf("%s = %+v: a strike was stored", id, r)
		}
	}
	if endpointInCooldown(port0) {
		t.Error("the swapped port was benched process-wide; it is not down")
	}
	if failedCanaryBefore(robinKey) || failedCanaryBefore(batKey) || len(p.canaryLog()) != 0 {
		t.Errorf("the swap touched the canary memory (robin=%v bat=%v asked=%v)", failedCanaryBefore(robinKey), failedCanaryBefore(batKey), p.canaryLog())
	}
	// The port is dropped from the dispatch: it is sent exactly the request that was ended.
	n := 0
	for _, s := range p.sendLog() {
		if s.url == port0 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the swapped port was sent %d request(s), want only the one that was ended", n)
	}
}

// Every port is swapped in turn: the pool is emptied, which ends as a port that
// changed hands (a deferral the ops do not count), with no result at all.
func TestIdentitySwap_APoolWhoseEveryPortIsSwappedEndsAsPortsThatChangedHands(t *testing.T) {
	p := swapPool(t, robinMachine, batMachine)
	port0, port1 := p.h.eps[0].URL, p.h.eps[1].URL
	var mu sync.Mutex
	send := swapSend(t, p, map[string]string{port0: swappedInMachine, port1: "dddd0004"}, 10*time.Second, &mu, map[string]bool{})

	res, err := dispatchOn(testCtx(t), p.h.eps, nil, send, p.canary, 4)
	if !errors.Is(err, errPortChangedHands) {
		t.Fatalf("err = %v, want one that wraps errPortChangedHands", err)
	}
	requireNoStrikeKind(t, err)
	if len(res) != 0 {
		t.Errorf("results = %+v, want none: both requests were withheld", res)
	}
	if endpointInCooldown(port0) || endpointInCooldown(port1) {
		t.Error("a swapped port was benched process-wide")
	}
}

// A worker that only missed its machine id mid-request is not a swap: the
// request completes and its answers are stored under the machine it is.
func TestIdentitySwap_AWorkerThatMissedItsMachineIDMidRequestKeepsItsAnswers(t *testing.T) {
	p := swapPool(t, robinMachine, batMachine)
	port0 := p.h.eps[0].URL
	var mu sync.Mutex
	send := swapSend(t, p, map[string]string{port0: ""}, 300*time.Millisecond, &mu, map[string]bool{})
	res, err := dispatchOn(testCtx(t), p.h.eps, nil, send, p.canary, 4)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, id := range loopers(4, "chap") {
		if r := res[id]; r.Text != "T:"+id || r.Endpoint != port0 || r.Worker != robinKey {
			t.Errorf("%s = %+v, want port 0's answer stored as %s", id, r, robinKey)
		}
	}
}

// The lone endpoint: nowhere else to send the jobs, so nothing is stored and the
// jobs are deferred with an error the ops do not count.
func TestIdentitySwap_TheOnlyEndpointSwappedMidRequestStoresNothingAndDefers(t *testing.T) {
	p := swapPool(t, robinMachine)
	port0 := p.h.eps[0].URL
	var mu sync.Mutex
	send := swapSend(t, p, map[string]string{port0: swappedInMachine}, 10*time.Second, &mu, map[string]bool{})

	res, err := dispatchOn(testCtx(t), p.h.eps, nil, send, p.canary, 4)
	if !IsWorkerSwapped(err) || !errors.Is(err, errPortChangedHands) {
		t.Fatalf("err = %T %v, want a swap that reads as a port that changed hands", err, err)
	}
	requireNoStrikeKind(t, err)
	if len(res) != 0 {
		t.Errorf("results = %+v, want none", res)
	}
	if endpointInCooldown(port0) {
		t.Error("the swapped port was benched process-wide")
	}
	if got := len(p.canaryLog()); got != 0 {
		t.Errorf("canary asked %d time(s)", got)
	}
}

// ── the result journal ──────────────────────────────────────────────────────

// flipOnSecondPost makes srv's second POST to post hang until the request is
// cancelled and, from then on, its /health report machine swappedInMachine
// instead of batMachine: a port handed to another machine while the second chunk
// of a send is in flight, after the first chunk was answered and journalled.
func flipOnSecondPost(t *testing.T, srv *httptest.Server, post string, base map[string]any) {
	t.Helper()
	var posts atomic.Int32
	var flipped atomic.Bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			body := map[string]any{}
			for k, v := range base {
				body[k] = v
			}
			if flipped.Load() {
				body["machine_id"] = swappedInMachine
			}
			_ = json.NewEncoder(rw).Encode(body)
		case r.Method == http.MethodPost && r.URL.Path == post && posts.Add(1) == 2:
			flipped.Store(true)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		default:
			inner.ServeHTTP(rw, r)
		}
	})
}

func swapBaseHealth(extra map[string]any) map[string]any {
	h := map[string]any{"status": "ok", "model": "M", "backend": backendMLX, "machine_id": batMachine, "busy_seconds": 0.0, "busy_limit_seconds": nil}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// requireNothingReplayable fails unless the journal holds no entry a later run
// could be served from, and reports that the entries the send wrote were taken
// back (so the test is not blind to a send that never journalled anything).
func requireNothingReplayable(t *testing.T, j *mapJournal, wrote int) {
	t.Helper()
	if got := len(j.invalidated); got != wrote {
		t.Errorf("journal was asked to invalidate %d entries, want the %d the send wrote before the swap was seen", got, wrote)
	}
	if got := j.len(); got != 0 {
		t.Errorf("journal still holds %d entry(ies) of a swapped send, replayable by a later run", got)
	}
}

// An upload send: chunk 1 is answered and journalled, chunk 2 is in flight when
// the port is handed to another machine. The dispatcher withholds every answer
// of the send; the journal must not keep and replay the chunk that was answered.
func TestJournalSwap_ASwappedUploadSendLeavesNothingReplayable(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	srv, _ := batchServer(t, swapBaseHealth(nil))
	useEndpoints(t, 2, srv.URL)
	flipOnSecondPost(t, srv, "/transcribe-batch", swapBaseHealth(map[string]any{"batch_pipeline": true}))
	noteCanaryPort(batKey, srv.URL, true)
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	if !ok {
		t.Fatal("setup: health")
	}
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	j := newMapJournal()

	jobs := writeClips(t, 4)
	res, err := transcribeRemoteWithHealth(ctx, srv.URL, h, true, 1, jobs, nil, j)
	if !IsWorkerSwapped(err) {
		t.Fatalf("err = %T %v, want a swap", err, err)
	}
	if len(res) != 2 {
		t.Fatalf("the sender returned %d result(s), want the 2 of chunk 1 (the dispatcher withholds them)", len(res))
	}
	requireNothingReplayable(t, j, 2)

	// A later run plans the same clips again: nothing is served from the journal.
	served, _, _ := newEndpointJournal(j, srv.URL, h, true).plan(jobs)
	if len(served) != 0 {
		t.Errorf("a later run is served %d result(s) of the swapped send", len(served))
	}
}

// The same for a path send.
func TestJournalSwap_ASwappedPathSendLeavesNothingReplayable(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	w := newPathWorker(t, pathHealth(map[string]any{"machine_id": batMachine, "busy_seconds": 0.0, "busy_limit_seconds": nil}))
	useEndpoints(t, 2, w.srv.URL)
	flipOnSecondPost(t, w.srv, "/transcribe-path-batch", pathHealth(map[string]any{"machine_id": batMachine, "busy_seconds": 0.0, "busy_limit_seconds": nil}))
	noteCanaryPort(batKey, w.srv.URL, true)
	h, ok := probeRemoteHealth(context.Background(), w.srv.URL)
	if !ok {
		t.Fatal("setup: health")
	}
	g := gatedEndpoint{Endpoint: Endpoint{URL: w.srv.URL, Concurrency: 1}, Health: h, Probed: true, resolved: batKey}
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	j := newMapJournal()
	jobs := pathJobs(t, 4)

	res, err := transcribePathRemote(ctx, g, jobs, nil, j)
	if !IsWorkerSwapped(err) {
		t.Fatalf("err = %T %v, want a swap", err, err)
	}
	if len(res) != 2 {
		t.Fatalf("the sender returned %d result(s), want the 2 of chunk 1", len(res))
	}
	requireNothingReplayable(t, j, 2)

	served, _, _ := newEndpointJournal(j, w.srv.URL, h, true).planPath(jobs)
	if len(served) != 0 {
		t.Errorf("a later run is served %d result(s) of the swapped send", len(served))
	}
}

// Control: the same send with no swap keeps its entries, so the two tests above
// are not passing because nothing is ever journalled.
func TestJournalSwap_ASendWithNoSwapKeepsItsEntries(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	srv, _ := batchServer(t, swapBaseHealth(nil))
	useEndpoints(t, 2, srv.URL)
	noteCanaryPort(batKey, srv.URL, true)
	h, _ := probeRemoteHealth(context.Background(), srv.URL)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(ctx, srv.URL, h, true, 1, writeClips(t, 4), nil, j); err != nil {
		t.Fatalf("err = %v", err)
	}
	if j.len() != 4 || len(j.invalidated) != 0 {
		t.Errorf("journal holds %d entries, %d invalidated; want 4 and 0", j.len(), len(j.invalidated))
	}
}

// ── the response itself ─────────────────────────────────────────────────────

// A swap in the window after the poll's last probe is not seen by the poll; the
// answer is checked once more before it is accepted.
func TestLiveness_ASwapAfterTheLastPollIsCaughtWhenTheResponseIsAccepted(t *testing.T) {
	shortLiveness(t, time.Hour) // the poll never probes during the request
	freshCanaryMemory(t)
	srv, hits := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(swappedInMachine) })
	noteCanaryPort(batKey, srv.URL, true)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"}, func(context.Context) error { return nil })
	sw, ok := errors.AsType[*WorkerSwappedError](err)
	if !ok || sw.Unverified || sw.Now != "m:"+swappedInMachine {
		t.Fatalf("err = %T %v, want a swap to the new machine", err, err)
	}
	requireNoStrikeKind(t, err)
	if hits.Load() != 1 {
		t.Errorf("probed %d time(s), want the one post-response probe", hits.Load())
	}
}

// A worker the poll skips (no busy_seconds: its event loop is busy during a
// request) is checked once the request is over, when its /health answers.
func TestLiveness_AWorkerThePollSkipsIsStillCheckedAfterItsResponse(t *testing.T) {
	shortLiveness(t, 10*time.Millisecond)
	freshCanaryMemory(t)
	srv, hits := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(swappedInMachine) })
	noteCanaryPort(batKey, srv.URL, true)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	err := superviseRequest(ctx, srv.URL, remoteHealth{}, time.Hour, []string{"a"}, func(context.Context) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	if !IsWorkerSwapped(err) {
		t.Fatalf("err = %T %v, want a swap", err, err)
	}
	if hits.Load() != 1 {
		t.Errorf("probed %d time(s): the poll must not run for this worker, only the post-response probe", hits.Load())
	}
}

// An unreadable probe withholds the response: nothing says it came from the
// frozen machine.
func TestLiveness_AnUnreadableProbeAfterTheResponseWithholdsIt(t *testing.T) {
	shortLiveness(t, time.Hour)
	freshCanaryMemory(t)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusInternalServerError, nil })
	noteCanaryPort(batKey, srv.URL, true)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"}, func(context.Context) error { return nil })
	sw, ok := errors.AsType[*WorkerSwappedError](err)
	if !ok || !sw.Unverified || sw.Now != "" {
		t.Fatalf("err = %T %v, want a withheld, unverified answer", err, err)
	}
	requireNoStrikeKind(t, err)
	if !errors.Is(classifyTransport([]string{srv.URL}, err), errPortChangedHands) {
		t.Error("an unverified answer must defer like a port that changed hands")
	}
}

func TestLiveness_AResponseFromTheSameMachineIsAcceptedAfterTheProbe(t *testing.T) {
	shortLiveness(t, time.Hour)
	freshCanaryMemory(t)
	srv, hits := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(batMachine) })
	noteCanaryPort(batKey, srv.URL, true)
	ctx := withFrozenWorker(context.Background(), frozenWorker{key: batKey, capable: true})
	if err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, nil, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("probed %d time(s)", hits.Load())
	}
	// A frozen identity that names no machine costs no probe.
	hits.Store(0)
	unk := withFrozenWorker(context.Background(), frozenWorker{key: "?abc", capable: true})
	if err := superviseRequest(unk, srv.URL, remoteHealth{}, time.Hour, nil, func(context.Context) error { return nil }); err != nil || hits.Load() != 0 {
		t.Errorf("unknown identity: err=%v probes=%d, want none", err, hits.Load())
	}
}

// A cancelled op still does not store an unverified answer.
func TestLiveness_ACancelledOpStillVerifiesTheAnswerItAccepts(t *testing.T) {
	shortLiveness(t, time.Hour)
	freshCanaryMemory(t)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(swappedInMachine) })
	noteCanaryPort(batKey, srv.URL, true)
	base, cancel := context.WithCancel(context.Background())
	ctx := withFrozenWorker(base, frozenWorker{key: batKey, capable: true})
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"}, func(context.Context) error {
		cancel() // the op ends just as the answer arrives
		return nil
	})
	if !IsWorkerSwapped(err) {
		t.Fatalf("err = %v, want the answer withheld although the op was cancelled", err)
	}
}

// ── the lone endpoint's learned machine ─────────────────────────────────────

// A lone endpoint frozen as "u:" whose worker gained its machine id keeps it
// (reprobed), but only as the machine it already showed.
func TestIdentitySwap_ALoneEndpointIsPinnedToTheMachineItLearned(t *testing.T) {
	freshCanaryMemory(t)
	const url = "http://192.0.2.7:8000"
	weak := "u:" + shortHash(url)
	mlx := func(id string) remoteHealth {
		return remoteHealth{Backend: backendMLX, MachineID: id}
	}
	fw := frozenWorker{key: weak, capable: true, lone: true, pin: newWorkerPin(batKey)}
	if now, swapped := fw.swappedTo(url, mlx(batMachine)); swapped {
		t.Errorf("the learned machine itself was read as a swap (now=%s)", now)
	}
	if now, swapped := fw.swappedTo(url, mlx(swappedInMachine)); !swapped || now != "m:"+swappedInMachine {
		t.Errorf("a strong id that contradicts the learned machine: swapped=%v now=%s, want a swap", swapped, now)
	}
	// A worker that missed its id again is not a contradiction.
	if _, swapped := fw.swappedTo(url, mlx("")); swapped {
		t.Error("a worker that only missed its id again was read as a swap")
	}
}

// With nothing learned, the first strong identity any poll sees is pinned and a
// later, different one is a swap.
func TestIdentitySwap_ALoneEndpointPinsTheFirstStrongIdentityItSees(t *testing.T) {
	freshCanaryMemory(t)
	const url = "http://192.0.2.7:8000"
	fw := frozenWorker{key: "u:" + shortHash(url), capable: true, lone: true, pin: newWorkerPin("")}
	mlx := func(id string) remoteHealth { return remoteHealth{Backend: backendMLX, MachineID: id} }
	if _, swapped := fw.swappedTo(url, mlx(batMachine)); swapped {
		t.Fatal("the first strong identity is the worker gaining its id, not a swap")
	}
	if _, swapped := fw.swappedTo(url, mlx(batMachine)); swapped {
		t.Error("the pinned machine read as a swap")
	}
	if _, swapped := fw.swappedTo(url, mlx(swappedInMachine)); !swapped {
		t.Error("a different strong identity after the pin is a swap")
	}
	// A pool port has no pin and takes no such allowance.
	pool := frozenWorker{key: "u:" + shortHash(url), capable: true}
	if _, swapped := pool.swappedTo(url, mlx(batMachine)); !swapped {
		t.Error("a pool port that gained an id must be a swap")
	}
}

// dispatchSingle seeds the pin from the machine it learned and shares it across
// the requests of the dispatch, and a request that then reads another machine
// stores nothing.
func TestIdentitySwap_TheLoneEndpointStoresNothingUnderALearnedMachineThePollContradicted(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	freshCanaryMemory(t)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusOK, swapHealth(swappedInMachine) })
	h := remoteHealth{Backend: backendMLX, BusySeconds: f64(0)}
	weak := "u:" + shortHash(srv.URL)
	g := gatedEndpoint{Endpoint: Endpoint{URL: srv.URL, Concurrency: 1}, Health: h, Probed: true, resolved: weak, learned: batKey}

	var seen frozenWorker
	send := func(ctx context.Context, g gatedEndpoint, sub map[string]string, _ ProgressFunc) (map[string]BatchResult, error) {
		seen, _ = frozenWorkerFrom(ctx)
		ids := sortedIDs(sub)
		err := superviseRequest(ctx, g.Endpoint.URL, g.Health, time.Hour, ids, waitOrAnswer(10*time.Second))
		return map[string]BatchResult{ids[0]: {Text: "T:" + ids[0]}}, err
	}
	res, err := dispatchSingle(testCtx(t), g, nil, singleGate{}, map[string]string{"chap1": "u", "chap2": "u"}, nil, send, nil)
	if !seen.lone || seen.pin == nil || seen.pin.id != batKey {
		t.Fatalf("frozen worker = %+v, want a lone endpoint pinned to the learned machine", seen)
	}
	if !IsWorkerSwapped(err) {
		t.Fatalf("err = %T %v, want a swap", err, err)
	}
	if len(res) != 0 {
		t.Errorf("results = %+v: an answer was stored under a machine the poll contradicted", res)
	}
}
