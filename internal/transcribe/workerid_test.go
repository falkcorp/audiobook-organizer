// file: internal/transcribe/workerid_test.go
// version: 1.0.0
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
	"strings"
	"testing"
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
