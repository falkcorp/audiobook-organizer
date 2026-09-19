// file: internal/aidispatch/routing_test.go
// version: 1.1.0
// guid: bcb13a89-88a9-4712-96a9-77be75c40ec3
// last-edited: 2026-09-19

package aidispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A saturated preferred endpoint must spill to a lower-priority peer with a
// free slot instead of parking the request in the preferred endpoint's queue
// (design "Routing and failure behavior" steps 2 and 2a: free-slot assignment).
func TestCall_SpillsToPeerWhenPreferredIsSaturated(t *testing.T) {
	fp := LLMFilenameParse.ID()
	fast := chatEP("fast", 1, fp)
	fast.Concurrency = 1
	slow := chatEP("slow", 2, fp)
	d := isolated([]Endpoint{fast, slow})

	release, err := d.slots.Acquire(context.Background(), "fast", 1, TotalCap{})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := Call(ctx, d, LLMFilenameParse, func(_ context.Context, tg Target) (string, error) {
		return tg.Endpoint.ID, nil
	})
	if err != nil {
		t.Fatalf("Call: %v (a saturated preferred endpoint must not block while a peer is free)", err)
	}
	if got != "slow" {
		t.Fatalf("served by %q, want spill to %q", got, "slow")
	}
}

// With free slots everywhere, priority still decides: the preferred endpoint
// gets the work.
func TestCall_PreferredGetsWorkWhenFree(t *testing.T) {
	fp := LLMFilenameParse.ID()
	d := isolated([]Endpoint{chatEP("slow", 2, fp), chatEP("fast", 1, fp)})
	got, err := Call(context.Background(), d, LLMFilenameParse, func(_ context.Context, tg Target) (string, error) {
		return tg.Endpoint.ID, nil
	})
	if err != nil || got != "fast" {
		t.Fatalf("got %q, %v; want fast", got, err)
	}
}

// When every candidate is saturated the request waits for the preferred one;
// it neither fails nor exceeds any endpoint's cap.
func TestCall_AllSaturatedWaitsForPreferred(t *testing.T) {
	fp := LLMFilenameParse.ID()
	a := chatEP("a", 1, fp)
	b := chatEP("b", 2, fp)
	d := isolated([]Endpoint{a, b})
	relA, _ := d.slots.Acquire(context.Background(), "a", 1, TotalCap{})
	relB, _ := d.slots.Acquire(context.Background(), "b", 1, TotalCap{})
	defer relB()
	go func() { time.Sleep(50 * time.Millisecond); relA() }()

	got, err := Call(context.Background(), d, LLMFilenameParse, func(_ context.Context, tg Target) (string, error) {
		return tg.Endpoint.ID, nil
	})
	if err != nil || got != "a" {
		t.Fatalf("got %q, %v; want a after its slot frees", got, err)
	}
}

// WithPinnedModel refuses every endpoint whose effective model for the
// capability differs, INCLUDING the first candidate, so a caller whose stored
// vectors are keyed by one model can never be served by another.
func TestCall_PinnedEmbedModelRefusesOtherModels(t *testing.T) {
	et := EmbedText.ID()
	other := chatEP("other-model", 1, et)
	other.EmbedModel = "nomic-embed-text"
	pinned := chatEP("pinned-model", 5, et)
	pinned.EmbedModel = "bge-m3"
	d := isolated([]Endpoint{other, pinned}, WithPinnedModel(EmbedText, "bge-m3"))

	cands, refusals, err := d.Candidates(EmbedText)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].ID != "pinned-model" {
		t.Fatalf("candidates = %v, want only pinned-model", ids(cands))
	}
	if len(refusals) != 1 || !strings.Contains(refusals[0].Reason, "pinned") {
		t.Fatalf("refusals = %+v, want a pinned-model refusal for other-model", refusals)
	}

	var models []string
	_, err = Call(context.Background(), d, EmbedText, func(_ context.Context, tg Target) (int, error) {
		models = append(models, tg.Model)
		return 0, nil
	})
	if err != nil || len(models) != 1 || models[0] != "bge-m3" {
		t.Fatalf("models used = %v, err %v; want [bge-m3]", models, err)
	}
}

// No endpoint serves the pinned model: fail closed, never fall back to a
// different vector space.
func TestCall_PinnedEmbedModelFailsClosed(t *testing.T) {
	et := EmbedText.ID()
	other := chatEP("other-model", 1, et)
	other.EmbedModel = "nomic-embed-text"
	d := isolated([]Endpoint{other}, WithPinnedModel(EmbedText, "bge-m3"))
	called := false
	_, err := Call(context.Background(), d, EmbedText, func(_ context.Context, _ Target) (int, error) {
		called = true
		return 0, nil
	})
	if !errors.Is(err, ErrNoCapableEndpoint) || called {
		t.Fatalf("err = %v, called = %v; want ErrNoCapableEndpoint and no call", err, called)
	}
}

// Every attempt is attributed to the endpoint that served it: requests,
// failures and last-used time per endpoint, by capability.
func TestCall_AttributionCounters(t *testing.T) {
	fp := LLMFilenameParse.ID()
	attr := NewAttribution()
	d := isolated([]Endpoint{chatEP("dead", 1, fp), chatEP("live", 2, fp)}, WithAttribution(attr))

	before := time.Now()
	_, err := Call(context.Background(), d, LLMFilenameParse, func(_ context.Context, tg Target) (int, error) {
		if tg.Endpoint.ID == "dead" {
			return 0, dialRefused
		}
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	dead := attr.Snapshot("dead")
	live := attr.Snapshot("live")
	if dead.Requests != 1 || dead.Failures != 1 {
		t.Fatalf("dead = %+v, want 1 request 1 failure", dead)
	}
	if live.Requests != 1 || live.Failures != 0 {
		t.Fatalf("live = %+v, want 1 request 0 failures", live)
	}
	if live.LastUsed.Before(before) || live.LastCapability != fp {
		t.Fatalf("live = %+v, want last_used >= start and capability %s", live, fp)
	}
	if live.ByCapability[fp] != 1 {
		t.Fatalf("live.ByCapability = %v", live.ByCapability)
	}
	if got := attr.Snapshot("never"); got.Requests != 0 || !got.LastUsed.IsZero() {
		t.Fatalf("unused endpoint = %+v, want zero", got)
	}
}

func TestEndpointLocality(t *testing.T) {
	cases := []struct {
		url, authRef string
		proto        Protocol
		want         Locality
	}{
		{"http://127.0.0.1:11434/v1", "", ProtocolOpenAICompat, LocalityLocal},
		{"http://[::1]:11434/v1", "", ProtocolOpenAICompat, LocalityLocal},
		{"http://localhost:11434/v1", "", ProtocolOpenAICompat, LocalityLocal},
		{"http://192.168.1.20:11434/v1", "", ProtocolOpenAICompat, LocalityLocal},
		{"http://10.0.0.5/v1", "", ProtocolOpenAICompat, LocalityLocal},
		{"http://100.101.1.2/v1", "", ProtocolOpenAICompat, LocalityLocal},
		{"http://mac-studio:11434/v1", "", ProtocolOpenAICompat, LocalityLocal},
		{"http://mac-studio.local:11434/v1", "", ProtocolOpenAICompat, LocalityLocal},
		{"", "", ProtocolLocalProcess, LocalityLocal},
		{"https://api.openai.com/v1", "", ProtocolOpenAICompat, LocalityCloud},
		{"https://8.8.8.8/v1", "", ProtocolOpenAICompat, LocalityCloud},
		{"http://127.0.0.1:9999/v1", "openai_api_key", ProtocolOpenAICompat, LocalityCloud},
		{"::not a url", "", ProtocolOpenAICompat, LocalityCloud},
		{"", "", ProtocolOpenAICompat, LocalityCloud},
	}
	for _, tc := range cases {
		got := EndpointLocality(Endpoint{URL: tc.url, AuthRef: tc.authRef, Protocol: tc.proto})
		if got != tc.want {
			t.Errorf("EndpointLocality(%q, auth_ref=%q, %s) = %s, want %s", tc.url, tc.authRef, tc.proto, got, tc.want)
		}
	}
}

func TestCall_LocalOnlyRefusesCloud(t *testing.T) {
	fp := LLMFilenameParse.ID()
	cloud := chatEP("cloud", 1, fp)
	cloud.URL, cloud.AuthRef = "https://api.openai.com/v1", "openai_api_key"
	local := chatEP("local", 5, fp)
	local.URL = "http://127.0.0.1:11434/v1"
	d := isolated([]Endpoint{cloud, local}, WithLocalOnly())
	cands, refusals, _ := d.Candidates(LLMFilenameParse)
	if len(cands) != 1 || cands[0].ID != "local" || len(refusals) != 1 || !strings.Contains(refusals[0].Reason, "local-only") {
		t.Fatalf("cands %v refusals %+v", ids(cands), refusals)
	}
}

// FIX 2: a panicking fn must not leak its endpoint slot or in-flight gauge.
func TestCall_PanicInFnReleasesSlot(t *testing.T) {
	fp := LLMFilenameParse.ID()
	one := chatEP("one", 1, fp)
	one.Concurrency = 1
	d := isolated([]Endpoint{one})
	func() {
		defer func() { _ = recover() }()
		_, _ = Call(context.Background(), d, LLMFilenameParse, func(context.Context, Target) (int, error) {
			panic("boom")
		})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Call(ctx, d, LLMFilenameParse, func(context.Context, Target) (int, error) { return 1, nil }); err != nil {
		t.Fatalf("after a panic the concurrency-1 endpoint could not be acquired: %v", err)
	}
}

// FIX 4: embedding work must be pinned to a model, or spillover could mix
// vector spaces. Call refuses an unpinned KindEmbed call.
func TestCall_EmbedRequiresPinnedModel(t *testing.T) {
	d := isolated([]Endpoint{chatEP("e", 1, EmbedText.ID())})
	called := false
	_, err := Call(context.Background(), d, EmbedText, func(context.Context, Target) (int, error) {
		called = true
		return 0, nil
	})
	if !errors.Is(err, ErrEmbedModelNotPinned) || called {
		t.Fatalf("err = %v, called = %v; want ErrEmbedModelNotPinned and no call", err, called)
	}
}
