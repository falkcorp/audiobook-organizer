// file: internal/transcribe/workerid_test.go
// version: 1.2.0
// guid: f008da3b-b827-4ee4-b116-679d66568c3c
// last-edited: 2026-10-26

package transcribe

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Worker identity (N1, N7) and routing a struck job to another machine (R4),
// from the review of #4182.

// machineHarness is a splitHarness whose workers report machine_id in /health:
// machines[i] is worker i's id ("" for a worker too old to report one). Worker
// i has Priority i+1, so worker 0 is the priority-1 endpoint.
func machineHarness(t *testing.T, machines []string, poison ...string) *splitHarness {
	t.Helper()
	h := &splitHarness{poison: map[string]bool{}}
	for _, p := range poison {
		h.poison[p] = true
	}
	for i, m := range machines {
		health := maps.Clone(aliveHealth)
		if m != "" {
			health["machine_id"] = m
		}
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			body := health
			if _, wedged := h.wedged.Load(r.Host); wedged {
				body = maps.Clone(health)
				body["busy_seconds"], body["busy_limit_seconds"] = 900.0, 60.0
			}
			_ = json.NewEncoder(rw).Encode(body)
		}))
		t.Cleanup(srv.Close)
		h.srvs = append(h.srvs, srv)
		h.eps = append(h.eps, Endpoint{URL: srv.URL, Concurrency: 1, Priority: i + 1, Label: fmt.Sprintf("w%d", i)})
	}
	return h
}

// sentTo is every endpoint URL that received id.
func (h *splitHarness) sentTo(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, s := range h.sends {
		for _, sid := range s.ids {
			if sid == id {
				out = append(out, s.url)
			}
		}
	}
	return out
}

func TestWorkerID_IsTheMachineNotTheURLAndNeverTheAddress(t *testing.T) {
	a := gatedEndpoint{Endpoint: Endpoint{URL: "http://192.0.2.10:19852"}, Health: remoteHealth{MachineID: "a1b2c3d4e5f60718"}, Probed: true}
	b := gatedEndpoint{Endpoint: Endpoint{URL: "http://192.0.2.10:19853"}, Health: remoteHealth{MachineID: "a1b2c3d4e5f60718"}, Probed: true}
	if a.workerID() != b.workerID() || a.workerID() != "m:a1b2c3d4e5f60718" {
		t.Fatalf("two ports on one machine must be one worker: %q vs %q", a.workerID(), b.workerID())
	}
	old := gatedEndpoint{Endpoint: Endpoint{URL: "http://192.0.2.11:8000"}, Probed: true}
	if id := old.workerID(); !strings.HasPrefix(id, "u:") || strings.Contains(id, "192.0.2") {
		t.Fatalf("an old worker's id must be an opaque hash of its URL, got %q", id)
	}
	if id := (gatedEndpoint{Endpoint: Endpoint{URL: "http://192.0.2.12:8000"}}).workerID(); id != "" {
		t.Fatalf("an unprobed endpoint's machine is unknown and must not be named, got %q", id)
	}
	odd := gatedEndpoint{Endpoint: Endpoint{URL: "http://192.0.2.13:8000"}, Health: remoteHealth{MachineID: "Wayne Manor + Batcave"}, Probed: true}
	if id := odd.workerID(); strings.ContainsAny(id, " +") || strings.Contains(id, "Wayne") {
		t.Fatalf("a machine_id that is not a short hex hash must be hashed, got %q", id)
	}
	if got := joinWorkers([]string{"m:b", "?x", "m:a", "m:b"}); got != "m:a + m:b" {
		t.Fatalf("joinWorkers = %q, want the sorted distinct storable ids", got)
	}
}

// R4: a priority-1 endpoint takes every lone job. A job whose file carries a
// strike from that machine must go to the other machine instead, whatever the
// round-robin cursor, while a job with no strike still goes to priority 1.
func TestPool_AStruckJobIsRoutedToAnotherMachineEvenAtLowerPriority(t *testing.T) {
	h := machineHarness(t, []string{"aaaa0001", "bbbb0002"})
	h.eps[1].Priority = 50
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	avoid := map[string]string{"looper": "m:aaaa0001"}
	for round := range 6 {
		res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"looper": "x", "fresh": "x"}, poolOptions{avoid: avoid}, h.send)
		if err != nil || len(res) != 2 {
			t.Fatalf("round %d: res=%d err=%v", round, len(res), err)
		}
		if res["looper"].Endpoint != h.eps[1].URL || res["looper"].Worker != "m:bbbb0002" {
			t.Fatalf("round %d: the struck job went to %s (worker %q), want the other machine", round, res["looper"].Endpoint, res["looper"].Worker)
		}
		if res["fresh"].Endpoint != h.eps[0].URL || res["fresh"].Worker != "m:aaaa0001" {
			t.Fatalf("round %d: a job with no strike went to %s, want priority 1", round, res["fresh"].Endpoint)
		}
	}
	// A stored condemnation names two machines; the job avoids both.
	h3 := machineHarness(t, []string{"aaaa0001", "bbbb0002", "cccc0003"})
	h3.eps[2].Priority = 90
	useEndpoints(t, 16, endpointURLs(h3.eps)...)
	res, err := dispatchPool(context.Background(), h3.eps, nil, map[string]string{"looper": "x"},
		poolOptions{avoid: map[string]string{"looper": "m:aaaa0001 + m:bbbb0002"}}, h3.send)
	if err != nil || res["looper"].Worker != "m:cccc0003" {
		t.Fatalf("res=%+v err=%v, want the one machine the condemnation does not name", res["looper"], err)
	}
}

// The preference never strands a job: with no other machine available (the
// other endpoint is benched, is the same machine, or is of unknown identity)
// the job is still sent.
func TestPool_AStruckJobWithNoOtherMachineIsStillSent(t *testing.T) {
	t.Run("the other machine is benched", func(t *testing.T) {
		h := machineHarness(t, []string{"aaaa0001", "bbbb0002"})
		useEndpoints(t, 16, endpointURLs(h.eps)...)
		markEndpointFailure(h.eps[1].URL)
		t.Cleanup(func() { clearCooldown(h.eps[1].URL) })
		res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"looper": "x"},
			poolOptions{avoid: map[string]string{"looper": "m:aaaa0001"}}, h.send)
		if err != nil || res["looper"].Endpoint != h.eps[0].URL {
			t.Fatalf("res=%+v err=%v, want the job sent to the struck machine rather than held", res["looper"], err)
		}
	})
	t.Run("both endpoints are one machine", func(t *testing.T) {
		h := machineHarness(t, []string{"aaaa0001", "aaaa0001"})
		useEndpoints(t, 16, endpointURLs(h.eps)...)
		res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"looper": "x"},
			poolOptions{avoid: map[string]string{"looper": "m:aaaa0001"}}, h.send)
		if err != nil || res["looper"].Text == "" || res["looper"].Worker != "m:aaaa0001" {
			t.Fatalf("res=%+v err=%v", res["looper"], err)
		}
	})
}

// N1: a condemnation needs a lone job stuck on two different MACHINES. Two
// ports on one box are one machine, so it is condemned after one (the pool has
// one machine) and the verdict names that machine once; on two machines it
// names both, sorted, and never an endpoint address.
func TestPool_ACondemnationCountsMachinesNotPorts(t *testing.T) {
	t.Run("two ports, one machine", func(t *testing.T) {
		h := machineHarness(t, []string{"aaaa0001", "aaaa0001", ""}, "j00")
		h.wedgeOnPoison = true
		h.eps = h.eps[:2]
		res, err := h.run(t, 1)
		if err != nil {
			t.Fatalf("err = %v, want a verdict, not a dispatch failure", err)
		}
		r := res["j00"]
		if !r.Condemned || r.Worker != "m:aaaa0001" {
			t.Fatalf("verdict = %+v, want a condemnation naming the one machine once", r)
		}
		if n := len(h.sentTo("j00")); n != 1 {
			t.Fatalf("the job was sent %d times; another port on the same machine is no second opinion", n)
		}
	})
	t.Run("two machines", func(t *testing.T) {
		h := machineHarness(t, []string{"bbbb0002", "aaaa0001", "cccc0003"}, "j00")
		h.wedgeOnPoison = true
		res, err := h.run(t, 1)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		r := res["j00"]
		if !r.Condemned || r.Worker != "m:aaaa0001 + m:bbbb0002" {
			t.Fatalf("verdict = %+v, want both machines, sorted", r)
		}
		if strings.Contains(r.Worker, "127.0.0.1") || strings.Contains(r.Worker, "http") {
			t.Fatalf("the stored identity carries an address: %q", r.Worker)
		}
	})
}

// S2 (review 3 of #4182): a split part (a unit) is placed by its own loop, not
// by normal allocation, so steerAway never saw it and the unit loop took the
// first endpoint by priority. A lone stuck job whose file B already struck went
// to B next, and the condemnation named B again: a verdict the store cannot
// count as a second machine. The unit loop now tries every endpoint off the
// avoided machines first, and falls back to them rather than strand the job.
func TestPool_AStuckUnitIsPlacedOffTheMachinesThatAlreadyStruckIt(t *testing.T) {
	dispatch := func(t *testing.T, h *splitHarness, avoid string) BatchResult {
		t.Helper()
		h.wedgeOnPoison = true
		useEndpoints(t, 16, endpointURLs(h.eps)...)
		t.Cleanup(func() {
			for _, ep := range h.eps {
				clearCooldown(ep.URL)
			}
		})
		res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j00": "x"},
			poolOptions{avoid: map[string]string{"j00": avoid}}, h.send)
		if err != nil {
			t.Fatalf("err = %v, want a verdict", err)
		}
		return res["j00"]
	}
	t.Run("another machine is free", func(t *testing.T) {
		h := machineHarness(t, []string{"aaaa0001", "bbbb0002", "cccc0003"}, "j00")
		r := dispatch(t, h, "m:bbbb0002")
		if got, want := h.sentTo("j00"), []string{h.eps[0].URL, h.eps[2].URL}; !slices.Equal(got, want) {
			t.Fatalf("the job went to %v, want priority 1 and then the machine that has not struck it (skipping %s)", got, h.eps[1].URL)
		}
		if !r.Condemned || r.Worker != "m:aaaa0001 + m:cccc0003" {
			t.Fatalf("verdict = %+v, want a condemnation by the two machines that had not struck the file", r)
		}
	})
	t.Run("only the avoided machine is left", func(t *testing.T) {
		h := machineHarness(t, []string{"aaaa0001", "bbbb0002"}, "j00")
		r := dispatch(t, h, "m:bbbb0002")
		if got, want := h.sentTo("j00"), []string{h.eps[0].URL, h.eps[1].URL}; !slices.Equal(got, want) {
			t.Fatalf("the job went to %v, want it still sent to the avoided machine rather than stranded", got)
		}
		if !r.Condemned {
			t.Fatalf("verdict = %+v, want a condemnation", r)
		}
	})
}

// machineFaultPool is a faultPool over machineHarness workers, with requests of
// 16 decoded in chunks of 16.
func machineFaultPool(t *testing.T, machines []string, answer func(url, id string) BatchResult) *faultPool {
	t.Helper()
	p := &faultPool{h: machineHarness(t, machines), answer: answer, batch: 16}
	useEndpoints(t, 16, endpointURLs(p.h.eps)...)
	return p
}

// B1 (review 3 of #4182), per machine: prod's Mac serves four ports. When one
// port timeout-faulted only that port was benched, and the re-queued units of
// one or two clips went to its three siblings, which are just as ill (a Mac
// that swaps, or whose decoders jetsam kills, is ill on every port) and whose
// units can never fault, so their strikes were believed: 16 of 32 ordinary
// files struck. A timeout-majority fault now benches every port of the machine.
func TestPool_ASickMachineIsBenchedOnEveryPort(t *testing.T) {
	const killed = PathErrDecoderKilled + pathErrSeparator + "the decode process was killed from outside (exit code -9)"
	for _, kind := range []string{"timeout", "kill"} {
		for _, jobs := range []int{32, 200} {
			t.Run(fmt.Sprintf("%s %d jobs", kind, jobs), func(t *testing.T) {
				sickErr := timeoutText
				if kind == "kill" {
					sickErr = killed
				}
				var p *faultPool
				p = machineFaultPool(t, []string{"aaaa0001", "aaaa0001", "aaaa0001", "aaaa0001", "bbbb0002", "bbbb0002"},
					func(url, id string) BatchResult {
						if slices.ContainsFunc(p.h.eps[:4], func(ep Endpoint) bool { return ep.URL == url }) {
							return BatchResult{Error: sickErr}
						}
						return BatchResult{Text: "T:" + id}
					})
				ids := loopers(jobs, "chap")
				res, err := p.run(t, ids...)
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}
				strikes := 0
				for _, id := range ids {
					r := res[id]
					if IsStrikeError(r.Error) {
						strikes++
						continue
					}
					if r.Text != "T:"+id || r.Worker != "m:bbbb0002" {
						t.Errorf("%s = %+v, want the sound machine's transcript", id, r)
					}
				}
				if strikes != 0 {
					t.Errorf("strikes = %d of %d ordinary files, want 0 while a sound machine remains", strikes, jobs)
				}
			})
		}
	}
}

// The ports of one machine answer in the same round: one faults, its sibling
// answers a single timeout among sound clips (a minority, so a per-clip strike)
// and its response drains first. Every fault of the round is marked before any
// result is read, so the sibling's strike is withheld whatever the order.
func TestPool_ASiblingPortsStrikeInTheSameRoundAsItsMachinesFaultIsWithheld(t *testing.T) {
	var (
		p       *faultPool
		sibling atomic.Int32
		faulted atomic.Int32
	)
	p = machineFaultPool(t, []string{"aaaa0001", "aaaa0001", "bbbb0002"}, func(url, id string) BatchResult {
		switch url {
		case p.h.eps[0].URL:
			if faulted.Add(1) == 1 {
				time.Sleep(50 * time.Millisecond) // let the sibling's response drain first
			}
			return BatchResult{Error: timeoutText}
		case p.h.eps[1].URL:
			if sibling.Add(1) == 1 {
				return BatchResult{Error: timeoutText}
			}
		}
		return BatchResult{Text: "T:" + id}
	})
	ids := loopers(48, "chap")
	res, err := p.run(t, ids...)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if faulted.Load() == 0 || sibling.Load() == 0 {
		t.Fatalf("both ports of the sick machine must be sent work in the first round (fault=%d sibling=%d)", faulted.Load(), sibling.Load())
	}
	for _, id := range ids {
		if r := res[id]; r.Text != "T:"+id {
			t.Errorf("%s = %+v, want its transcript (a strike from a port of a machine that faulted is no verdict)", id, r)
		}
	}
}
