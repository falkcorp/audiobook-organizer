// file: internal/transcribe/canary_refusal_test.go
// version: 1.0.1
// guid: a3d90261-5960-4067-ab7e-f4d536051b69
// last-edited: 2026-10-27

package transcribe

import (
	"context"
	"testing"
)

// D58: the address-based identity of D55 (a worker that missed its machine id is
// the machine its URL last reported) is guarded by refusals. A probe of a URL
// that was refused ends the link, because a refused URL is the one moment it can
// have changed hands: a port that later answers from a different Mac must not
// inherit the old machine's id. The canary memory is not affected: it resolves
// through the ports of the entry.

// refuseProbe is what a gate that could not reach url records.
func refuseProbe(url string) {
	noteCanaryProbes(nil, []gpuRefusal{{URL: url}})
}

// A URL answered as m:cat, was refused, then answered as a weak "u:" from a
// worker that can report an id: it is NOT the machine remembered for the URL
// before the refusal, so the weak identity stands for itself.
func TestCanaryRefusal_AWeakAnswerAfterARefusalDoesNotInheritTheOldMachine(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-tunnel:8000"
	weak := "u:" + shortHash(url)

	noteCanaryPort(catKey, url, true)
	if got := resolveMachine(url, weak, true); got != catKey {
		t.Fatalf("before any refusal a weak answer resolves to %q, want %q", got, catKey)
	}
	refuseProbe(url)
	if got := canaryURLMachine(url); got != "" {
		t.Errorf("byURL[url] = %q after a refusal, want it dropped", got)
	}
	if got := resolveMachine(url, weak, true); got != weak {
		t.Errorf("a weak answer after a refusal resolves to %q, want the raw %q", got, weak)
	}
	// A strong answer ties the URL to the machine it names again.
	noteCanaryPort(robinKey, url, true)
	if got := resolveMachine(url, weak, true); got != robinKey {
		t.Errorf("after robin answered, a weak answer resolves to %q, want %q", got, robinKey)
	}
}

// An unprobed endpoint the gate kept (a pool with no capability requirement) is
// a refused probe as well.
func TestCanaryRefusal_AnUnprobedKeptEndpointEndsTheLinkToo(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-tunnel:8000"
	noteCanaryPort(catKey, url, true)
	noteCanaryProbes([]gatedEndpoint{{Endpoint: Endpoint{URL: url}}}, nil)
	if got := resolveMachine(url, "u:"+shortHash(url), true); got != "u:"+shortHash(url) {
		t.Errorf("weak answer resolves to %q after an unprobed pass, want the raw identity", got)
	}
}

// The memory does not rest on the link. A machine that failed the clip keeps the
// port that was then refused (it is a port of the machine until it has been
// refused for deadPortAfter), so a weak answer from that port is still found
// remembered through the entry's ports even though it is no longer resolved to
// the machine by name.
func TestCanaryRefusal_TheCanaryMemoryStillResolvesThroughThePorts(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	fakeCanaryClock(t)
	const live, down = "http://gotham-live:8000", "http://gotham-down:8000"
	noteCanaryPort(catKey, live, true)
	noteCanaryPort(catKey, down, true)
	refuseProbe(down)
	markCanaryFailed(catKey)

	weak := "u:" + shortHash(down)
	if got := canaryMemoryKey(weak, down); got != catKey {
		t.Errorf("canaryMemoryKey(weak, down) = %q, want %q: the refused port is still a port of the remembered machine", got, catKey)
	}
	if got := canaryMemoryKey("u:"+shortHash(live), live); got != catKey {
		t.Errorf("canaryMemoryKey(weak, live) = %q, want %q", got, catKey)
	}
	if !endpointRemembered(weak, down) {
		t.Error("the refused port's weak answer is not remembered as the sick machine")
	}
}

// Residual, accepted by the owner (D58): with no refusal in between the link
// stands, because the URL was answered continuously and nothing says it changed
// hands. Production ports are one per Mac, so two machines do not trade a URL
// without the tunnel dropping first (see noteProbeResult).
func TestCanaryRefusal_WithoutARefusalTheLinkStandsByDesign(t *testing.T) {
	resetCanaryFailed()
	t.Cleanup(resetCanaryFailed)
	const url = "http://gotham-tunnel:8000"
	weak := "u:" + shortHash(url)
	noteCanaryPort(catKey, url, true)
	// answered by a worker that can report an id and did not this time
	noteCanaryProbes([]gatedEndpoint{{Endpoint: Endpoint{URL: url}, Probed: true, Health: remoteHealth{Backend: backendMLX}, resolved: catKey}}, nil)
	if got := resolveMachine(url, weak, true); got != catKey {
		t.Errorf("resolveMachine = %q, want %q: an answered URL keeps its machine", got, catKey)
	}
}

// RR5-1 with a refusal between: the URL reported machine cat (catwoman), was
// refused (the tunnel dropped), and now reaches machine robin, whose worker
// there answers as "u:" (it missed its MACHINE_ID_WAIT_S) while its other port
// answers as m:robin. A lone job that wedges both of robin's ports must not be
// condemned as the two KNOWN machines "m:cat + m:robin": it is one machine.
func TestCanaryRefusal_AStaleLinkDoesNotCondemnOneMachineAsTwo(t *testing.T) {
	h := machineHarness(t, []string{"", robinMachine}, "j00")
	h.wedgeOnPoison = true
	useEndpoints(t, 1, endpointURLs(h.eps)...)
	noteCanaryPort(catKey, h.eps[0].URL, true) // what the URL reported before the tunnel dropped
	refuseProbe(h.eps[0].URL)
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j00": "unused"}, poolOptions{}, h.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if r := res["j00"]; r.Condemned && r.Worker == joinWorkers([]string{catKey, robinKey}) {
		t.Errorf("one machine (robin, on both ports) condemned the file as two known machines: %q", r.Worker)
	}
}

// RR5-3 with a refusal between: one port that last reported machine cat and now
// reaches machine bat, whose worker answers as "u:". The file was struck last
// night by robin. The answer must not be stored as m:cat, and the job is routed
// to the other machine as steering is meant to.
func TestCanaryRefusal_AStaleLinkDoesNotStoreTheWrongMachineOrDefeatSteering(t *testing.T) {
	h := machineHarness(t, []string{"", batMachine})
	h.eps[1].Priority = 50
	useEndpoints(t, 16, endpointURLs(h.eps)...)
	noteCanaryPort(catKey, h.eps[0].URL, true)
	refuseProbe(h.eps[0].URL)
	res, err := dispatchPool(context.Background(), h.eps, nil, map[string]string{"j": "x"}, poolOptions{avoid: map[string]string{"j": robinKey}}, h.send)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := res["j"]; got.Worker == catKey {
		t.Errorf("the answer names machine cat (%q), which was never behind either port", got.Worker)
	}
}
