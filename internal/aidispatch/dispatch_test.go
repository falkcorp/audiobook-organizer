// file: internal/aidispatch/dispatch_test.go
// version: 1.1.0
// guid: 29aa8b75-6c17-4559-9e41-f39e445566bd
// last-edited: 2026-09-19

package aidispatch

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func chatEP(id string, prio int, caps ...string) Endpoint {
	return Endpoint{ID: id, Protocol: ProtocolOpenAICompat, ChatModel: "chat-m", EmbedModel: "embed-m",
		Priority: prio, Enabled: true, Capabilities: caps}
}

func whisperEP(id string, prio int, labels ...string) Endpoint {
	return Endpoint{ID: id, Protocol: ProtocolWhisperServer, Priority: prio, Enabled: true,
		Capabilities: []string{TranscribeBatch.ID()}, Labels: labels}
}

func isolated(eps []Endpoint, opts ...Option) *Dispatcher {
	return New(eps, append([]Option{WithSlots(NewSlots()), WithHealth(NewHealth())}, opts...)...)
}

var dialRefused = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

func ids(eps []Endpoint) []string {
	out := make([]string, 0, len(eps))
	for _, e := range eps {
		out = append(out, e.ID)
	}
	return out
}

func TestSelectionTable(t *testing.T) {
	fp := LLMFilenameParse.ID()
	tests := []struct {
		name      string
		cap       Capability
		eps       []Endpoint
		opts      []Option
		setup     func(d *Dispatcher)
		want      []string
		refusedAs map[string]string // endpoint ID -> reason substring
	}{
		{
			name: "priority ascending, stable",
			cap:  LLMFilenameParse,
			eps:  []Endpoint{chatEP("c", 20, fp), chatEP("a", 5, fp), chatEP("b", 10, fp)},
			want: []string{"a", "b", "c"},
		},
		{
			name: "equal priority keeps config order",
			cap:  LLMFilenameParse,
			eps:  []Endpoint{chatEP("x", 10, fp), chatEP("y", 10, fp), chatEP("z", 10, fp)},
			want: []string{"x", "y", "z"},
		},
		{
			name: "equal priority breaks ties by in-flight count",
			cap:  LLMFilenameParse,
			eps:  []Endpoint{func() Endpoint { e := chatEP("busy", 10, fp); e.Concurrency = 4; return e }(), chatEP("idle", 10, fp)},
			setup: func(d *Dispatcher) {
				if _, err := d.slots.Acquire(context.Background(), "busy", 4, TotalCap{}); err != nil {
					panic(err)
				}
			},
			want: []string{"idle", "busy"},
		},
		{
			name:      "disabled endpoint refused",
			cap:       LLMFilenameParse,
			eps:       []Endpoint{func() Endpoint { e := chatEP("off", 1, fp); e.Enabled = false; return e }(), chatEP("on", 9, fp)},
			want:      []string{"on"},
			refusedAs: map[string]string{"off": "disabled"},
		},
		{
			name:      "empty capability list means deny",
			cap:       LLMFilenameParse,
			eps:       []Endpoint{chatEP("empty", 1)},
			want:      nil,
			refusedAs: map[string]string{"empty": "default-deny"},
		},
		{
			name:      "wildcards are not honoured",
			cap:       LLMFilenameParse,
			eps:       []Endpoint{chatEP("star", 1, "*"), chatEP("llmstar", 2, "llm.*")},
			want:      nil,
			refusedAs: map[string]string{"star": "wildcard", "llmstar": "wildcard"},
		},
		{
			name:      "ticked for another capability only",
			cap:       LLMFilenameParse,
			eps:       []Endpoint{chatEP("other", 1, LLMAuthorReview.ID())},
			want:      nil,
			refusedAs: map[string]string{"other": "not ticked"},
		},
		{
			name: "protocol must match kind",
			cap:  LLMFilenameParse,
			eps: []Endpoint{func() Endpoint {
				e := whisperEP("w", 1)
				e.Capabilities = []string{fp}
				return e
			}()},
			want:      nil,
			refusedAs: map[string]string{"w": "protocol"},
		},
		{
			name: "required feature missing",
			cap:  LLMCoverArtVision,
			eps: []Endpoint{
				chatEP("novision", 1, LLMCoverArtVision.ID()),
				func() Endpoint {
					e := chatEP("vision", 2, LLMCoverArtVision.ID())
					e.Features = []string{FeatureVision}
					return e
				}(),
			},
			want:      []string{"vision"},
			refusedAs: map[string]string{"novision": "missing required feature"},
		},
		{
			name: "required labels are contains-ALL, not contains-any",
			cap:  TranscribeBatch,
			eps: []Endpoint{
				whisperEP("gpu-only", 1, "gpu"),
				whisperEP("local-only", 2, "local"),
				whisperEP("both", 3, "gpu", "local", "fast"),
			},
			opts:      []Option{WithRequiredLabels(TranscribeBatch, "gpu", "local")},
			want:      []string{"both"},
			refusedAs: map[string]string{"gpu-only": "missing required label", "local-only": "missing required label"},
		},
		{
			name: "require_gpu needs the gpu label",
			cap:  TranscribeBatch,
			eps: []Endpoint{
				func() Endpoint { e := whisperEP("cpu", 1, "local"); e.RequireGPU = true; return e }(),
				func() Endpoint { e := whisperEP("gpu", 2, "gpu"); e.RequireGPU = true; return e }(),
			},
			want:      []string{"gpu"},
			refusedAs: map[string]string{"cpu": "gpu"},
		},
		{
			name: "local_process serves whisper work",
			cap:  TranscribeIntroClip,
			eps: []Endpoint{{ID: "local-uv", Protocol: ProtocolLocalProcess, Enabled: true,
				Capabilities: []string{TranscribeIntroClip.ID()}}},
			want: []string{"local-uv"},
		},
		{
			name: "chat endpoint without a model refused; per-capability model accepted",
			cap:  LLMFilenameParse,
			eps: []Endpoint{
				func() Endpoint { e := chatEP("nomodel", 1, fp); e.ChatModel = ""; return e }(),
				func() Endpoint {
					e := chatEP("override", 2, fp)
					e.ChatModel = ""
					e.CapabilityModels = map[string]string{fp: "special"}
					return e
				}(),
			},
			want:      []string{"override"},
			refusedAs: map[string]string{"nomodel": "no chat model"},
		},
		{
			name:      "endpoint in cooldown skipped",
			cap:       LLMFilenameParse,
			eps:       []Endpoint{chatEP("sick", 1, fp), chatEP("well", 2, fp)},
			setup:     func(d *Dispatcher) { d.health.MarkFailure("sick") },
			want:      []string{"well"},
			refusedAs: map[string]string{"sick": "cooldown"},
		},
		{
			name: "shared_fs feature is data only and never gates selection",
			cap:  TranscribeBatch,
			eps: []Endpoint{func() Endpoint {
				e := whisperEP("w", 1)
				e.Features = []string{SharedFSFeature("library")}
				return e
			}(), whisperEP("plain", 2)},
			want: []string{"w", "plain"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := isolated(tc.eps, tc.opts...)
			if tc.setup != nil {
				tc.setup(d)
			}
			got, refusals, err := d.Candidates(tc.cap)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(ids(got), tc.want) {
				t.Fatalf("candidates %v, want %v (refusals %+v)", ids(got), tc.want, refusals)
			}
			if len(got)+len(refusals) != len(tc.eps) {
				t.Fatalf("every endpoint must be either a candidate or refused with a reason: %d + %d != %d",
					len(got), len(refusals), len(tc.eps))
			}
			for id, sub := range tc.refusedAs {
				i := slices.IndexFunc(refusals, func(r Refusal) bool { return r.EndpointID == id })
				if i < 0 || !strings.Contains(refusals[i].Reason, sub) {
					t.Errorf("endpoint %s: want refusal containing %q, got %+v", id, sub, refusals)
				}
			}
		})
	}
}

func TestCandidates_UnknownCapability(t *testing.T) {
	d := isolated([]Endpoint{chatEP("a", 1, LLMFilenameParse.ID())})
	if _, _, err := d.Candidates(Capability{}); !errors.Is(err, ErrUnknownCapability) {
		t.Fatalf("zero capability: want ErrUnknownCapability, got %v", err)
	}
	if _, err := Call(context.Background(), d, Capability{}, func(context.Context, Target) (int, error) { return 0, nil }); !errors.Is(err, ErrUnknownCapability) {
		t.Fatalf("Call with zero capability: want ErrUnknownCapability, got %v", err)
	}
}

func TestCapacity(t *testing.T) {
	fp := LLMFilenameParse.ID()
	a := chatEP("a", 1, fp) // Concurrency 0 counts as 1
	b := chatEP("b", 2, fp)
	b.Concurrency = 3
	d := isolated([]Endpoint{a, b, chatEP("unticked", 3)})
	if got := d.Capacity(LLMFilenameParse); got != 4 {
		t.Fatalf("Capacity = %d, want 1+3", got)
	}
	d2 := isolated([]Endpoint{a, b}, WithTotalCap(KindChat, TotalCap{Group: "chat", Limit: 2}))
	if got := d2.Capacity(LLMFilenameParse); got != 2 {
		t.Fatalf("Capacity under total cap 2 = %d", got)
	}
}

func TestCall_FailoverOnTransportAndBench(t *testing.T) {
	fp := LLMFilenameParse.ID()
	d := isolated([]Endpoint{chatEP("primary", 1, fp), chatEP("secondary", 2, fp)})
	var seen []string
	got, err := Call(context.Background(), d, LLMFilenameParse, func(_ context.Context, tg Target) (string, error) {
		seen = append(seen, tg.Endpoint.ID)
		if tg.Endpoint.ID == "primary" {
			return "", dialRefused
		}
		return "ok:" + tg.Model, nil
	})
	if err != nil || got != "ok:chat-m" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !slices.Equal(seen, []string{"primary", "secondary"}) {
		t.Fatalf("order %v", seen)
	}
	if !d.health.InCooldown("primary") || d.health.InCooldown("secondary") {
		t.Fatal("the failed endpoint must be benched and the good one not")
	}
}

func TestCall_QualityErrorDoesNotFailOver(t *testing.T) {
	fp := LLMFilenameParse.ID()
	d := isolated([]Endpoint{chatEP("primary", 1, fp), chatEP("secondary", 2, fp)})
	calls := 0
	_, err := Call(context.Background(), d, LLMFilenameParse, func(context.Context, Target) (int, error) {
		calls++
		return 0, Quality(errors.New("reply was not JSON"))
	})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("quality failure must return after one attempt; calls %d, err %v", calls, err)
	}
	if d.health.InCooldown("primary") {
		t.Fatal("a quality failure is not endpoint evidence")
	}
}

func TestCall_QuotaGetsLongCooldown(t *testing.T) {
	fp := LLMFilenameParse.ID()
	d := isolated([]Endpoint{chatEP("paid", 1, fp), chatEP("local", 2, fp)})
	_, err := Call(context.Background(), d, LLMFilenameParse, func(_ context.Context, tg Target) (int, error) {
		if tg.Endpoint.ID == "paid" {
			return 0, &StatusError{Status: 429, Type: "insufficient_quota"}
		}
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if until := d.health.CooldownUntil("paid"); time.Until(until) < QuotaCooldown-time.Minute {
		t.Fatalf("quota exhaustion benched only until %v", until)
	}
}

func TestCall_DeadlineFailsOverOnce(t *testing.T) {
	fp := LLMFilenameParse.ID()
	d := isolated([]Endpoint{chatEP("slow1", 1, fp), chatEP("slow2", 2, fp), chatEP("fast", 3, fp)},
		WithAttemptTimeout(LLMFilenameParse, 20*time.Millisecond))
	var seen []string
	_, err := Call(context.Background(), d, LLMFilenameParse, func(ctx context.Context, tg Target) (int, error) {
		seen = append(seen, tg.Endpoint.ID)
		if tg.Endpoint.ID == "fast" {
			return 1, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	})
	var ex *ExhaustedError
	if !errors.As(err, &ex) || len(ex.Attempts) != 2 {
		t.Fatalf("want ExhaustedError after 2 deadline attempts, got %v", err)
	}
	if !slices.Equal(seen, []string{"slow1", "slow2"}) {
		t.Fatalf("deadline must fail over exactly once; tried %v", seen)
	}
}

// The per-attempt clock starts after the slot is acquired.
func TestCall_AttemptTimeoutStartsAfterSlot(t *testing.T) {
	fp := LLMFilenameParse.ID()
	d := isolated([]Endpoint{chatEP("only", 1, fp)}, WithAttemptTimeout(LLMFilenameParse, 200*time.Millisecond))
	rel, err := d.slots.Acquire(context.Background(), "only", 1, TotalCap{})
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(300*time.Millisecond, rel)
	_, err = Call(context.Background(), d, LLMFilenameParse, func(ctx context.Context, _ Target) (int, error) {
		dl, _ := ctx.Deadline()
		if time.Until(dl) < 100*time.Millisecond {
			return 0, Quality(errors.New("deadline was consumed by the slot wait"))
		}
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCall_ParentCancelStops(t *testing.T) {
	fp := LLMFilenameParse.ID()
	d := isolated([]Endpoint{chatEP("a", 1, fp), chatEP("b", 2, fp)})
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := Call(ctx, d, LLMFilenameParse, func(context.Context, Target) (int, error) {
		calls++
		cancel()
		return 0, dialRefused
	})
	if calls != 1 || err == nil {
		t.Fatalf("cancelled parent must stop after the current attempt; calls %d err %v", calls, err)
	}
	if d.health.InCooldown("a") {
		t.Fatal("our own cancellation is not endpoint evidence")
	}
}

func TestCall_EmbedFailoverOnlyToSameModel(t *testing.T) {
	e := EmbedText.ID()
	mk := func(id string, prio int, model string) Endpoint {
		ep := chatEP(id, prio, e)
		ep.EmbedModel = model
		return ep
	}
	d := isolated([]Endpoint{mk("bge-a", 1, "bge-m3"), mk("nomic", 2, "nomic-embed"), mk("bge-b", 3, "bge-m3")},
		WithPinnedModel(EmbedText, "bge-m3"))
	var seen []string
	_, err := Call(context.Background(), d, EmbedText, func(_ context.Context, tg Target) (int, error) {
		seen = append(seen, tg.Endpoint.ID+"/"+tg.Model)
		if tg.Endpoint.ID == "bge-a" {
			return 0, dialRefused
		}
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{"bge-a/bge-m3", "bge-b/bge-m3"}) {
		t.Fatalf("embedding failover crossed models: %v", seen)
	}
}

func TestCall_AllFailIsExhausted(t *testing.T) {
	fp := LLMFilenameParse.ID()
	d := isolated([]Endpoint{chatEP("a", 1, fp), chatEP("b", 2, fp)})
	_, err := Call(context.Background(), d, LLMFilenameParse, func(context.Context, Target) (int, error) {
		return 0, &StatusError{Status: 503}
	})
	var ex *ExhaustedError
	if !errors.As(err, &ex) || len(ex.Attempts) != 2 || errors.Is(err, ErrNoCapableEndpoint) {
		t.Fatalf("want ExhaustedError with 2 attempts, got %v", err)
	}
}

// N goroutines against 3 endpoints that fail at random. The observed maximum
// in-flight per endpoint, tracked atomically inside the call, must never
// exceed that endpoint's effective Concurrency. Endpoint "zero" has
// Concurrency 0, which must mean 1.
func TestCall_RaceNeverExceedsConcurrency(t *testing.T) {
	fp := LLMFilenameParse.ID()
	eps := []Endpoint{chatEP("zero", 10, fp), chatEP("two", 10, fp), chatEP("three", 10, fp)}
	eps[1].Concurrency = 2
	eps[2].Concurrency = 3
	limit := map[string]int64{"zero": 1, "two": 2, "three": 3}

	// A clock that jumps an hour per read, so every cooldown has expired by
	// the next check: random failures keep all three endpoints in rotation.
	var tick atomic.Int64
	h := newHealthWithClock(func() time.Time { return time.Unix(0, 0).Add(time.Duration(tick.Add(1)) * time.Hour) })
	d := New(eps, WithSlots(NewSlots()), WithHealth(h))

	live := map[string]*atomic.Int64{}
	peak := map[string]*atomic.Int64{}
	for _, ep := range eps {
		live[ep.ID], peak[ep.ID] = &atomic.Int64{}, &atomic.Int64{}
	}
	var ok, failed atomic.Int64

	const goroutines, perG = 40, 25
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perG {
				_, err := Call(context.Background(), d, LLMFilenameParse, func(_ context.Context, tg Target) (int, error) {
					id := tg.Endpoint.ID
					cur := live[id].Add(1)
					for {
						old := peak[id].Load()
						if cur <= old || peak[id].CompareAndSwap(old, cur) {
							break
						}
					}
					time.Sleep(time.Duration(rand.IntN(400)) * time.Microsecond)
					live[id].Add(-1)
					if rand.IntN(10) < 3 {
						return 0, dialRefused
					}
					return 1, nil
				})
				if err == nil {
					ok.Add(1)
				} else {
					failed.Add(1)
				}
			}
		})
	}
	wg.Wait()

	for id, lim := range limit {
		if p := peak[id].Load(); p > lim {
			t.Errorf("endpoint %s: observed %d in flight, cap %d", id, p, lim)
		}
		if peak[id].Load() == 0 {
			t.Errorf("endpoint %s never received work; the test did not exercise it", id)
		}
	}
	if peak["three"].Load() < 2 {
		t.Errorf("endpoint three peaked at %d; the test never overlapped requests, so it proves nothing", peak["three"].Load())
	}
	if ok.Load() == 0 || failed.Load()+ok.Load() != goroutines*perG {
		t.Errorf("ok %d failed %d", ok.Load(), failed.Load())
	}
}
