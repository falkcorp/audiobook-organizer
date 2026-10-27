// file: internal/transcribe/steer_weak_id_test.go
// version: 1.0.0
// guid: 5c1e7b94-2d0a-4f38-9a6e-b1f3d8c40e72
// last-edited: 2026-10-27

package transcribe

import (
	"context"
	"testing"
)

// S1 of the D58 review: a strike stored by an earlier build as "u:" + the hash
// of ONE port's URL, while the machine behind that port answered as a weak
// identity, names the machine, not the port. When that machine answers as "m:..."
// on every port now, the job must be steered away from all of its ports, not
// only the one the hash was made from.
//
// The job is struck as u:hash(port 1) while machine cat is on ports 0 and 1;
// allocation places it on port 0 (priority 1), and steering must move it to
// robin's port 2.
func TestRR5_LegacyWeakIdOfASiblingPortIsNotSteeredAway(t *testing.T) {
	h := machineHarness(t, []string{catwomanMachine, catwomanMachine, robinMachine})
	h.eps[1].Priority = 50
	h.eps[2].Priority = 60
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	avoid := map[string]string{"batarang": "u:" + shortHash(h.eps[1].URL)}
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"batarang": "x"}, poolOptions{avoid: avoid}, h.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	got := res["batarang"]
	if got.Endpoint != h.eps[2].URL {
		t.Errorf("the struck job went to %s (worker %q), want robin's port %s: a legacy u: id of cat's other port did not resolve to cat",
			got.Endpoint, got.Worker, h.eps[2].URL)
	}
}

// The id names the machine of its port and no other: a stored u: id of robin's
// port 2 steers a job placed on robin's port 0 to cat's port, and would leave a
// job on cat's port alone.
func TestRR5_LegacyWeakIdSteersOnlyOffTheMachineItsPortBelongsTo(t *testing.T) {
	h := machineHarness(t, []string{robinMachine, catwomanMachine, robinMachine})
	h.eps[1].Priority = 50
	h.eps[2].Priority = 60
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	avoid := map[string]string{"batarang": "u:" + shortHash(h.eps[2].URL)} // robin's port 2
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"batarang": "x"}, poolOptions{avoid: avoid}, h.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := res["batarang"]; got.Endpoint != h.eps[1].URL {
		t.Errorf("the job went to %s (worker %q), want cat's port %s: robin's ports 0 and 2 are the machine the strike names", got.Endpoint, got.Worker, h.eps[1].URL)
	}
}
