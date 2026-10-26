// file: internal/transcribe/liveness_test.go
// version: 1.1.0
// guid: 91b4c7d2-5e08-4a3f-b6d1-0f8a2c9e7413
// last-edited: 2026-10-26

package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func shortLiveness(t *testing.T, d time.Duration) {
	t.Helper()
	old := livenessInterval
	livenessInterval = d
	t.Cleanup(func() { livenessInterval = old })
}

// healthServer answers /health from a function and counts the probes.
func probeServer(t *testing.T, body func() (int, map[string]any)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		code, b := body()
		rw.WriteHeader(code)
		if b != nil {
			_ = json.NewEncoder(rw).Encode(b)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// untilCancelled is a request that never answers by itself.
func untilCancelled(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestSuperviseRequest_AWorkerThatStopsAnsweringHealthIsAnOutageNotStuck(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusInternalServerError, nil })
	h := remoteHealth{BusySeconds: f64(0)}

	start := time.Now()
	err := superviseRequest(context.Background(), srv.URL, h, time.Hour, []string{"a", "b"}, untilCancelled)
	if !IsTransport(err) {
		t.Fatalf("err = %v, want a transport error", err)
	}
	if IsWorkerStuck(err) {
		t.Fatalf("an unreachable worker must be an outage, not a stuck decode: %v", err)
	}
	if !strings.Contains(err.Error(), "stopped answering /health") {
		t.Errorf("message should say why: %v", err)
	}
	// Minutes to hours before; now livenessMisses probe intervals.
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s to give up on a worker that stopped answering", d)
	}
}

func TestSuperviseRequest_ASingleFailedProbeDoesNotCancel(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	var n atomic.Int32
	srv, _ := probeServer(t, func() (int, map[string]any) {
		if n.Add(1) == 2 {
			return http.StatusInternalServerError, nil // one blip
		}
		return http.StatusOK, map[string]any{"busy_seconds": 1.0, "busy_limit_seconds": 60.0}
	})
	h := remoteHealth{BusySeconds: f64(0)}
	err := superviseRequest(context.Background(), srv.URL, h, time.Hour, nil, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(400 * time.Millisecond):
			return nil
		}
	})
	if err != nil {
		t.Fatalf("one dropped probe cancelled a healthy request: %v", err)
	}
}

func TestSuperviseRequest_ADecodePastItsDeadlineIsStuck(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	// stuckGrace is 45 s: 500 s into a 60 s deadline is far past it.
	srv, _ := probeServer(t, func() (int, map[string]any) {
		return http.StatusOK, map[string]any{"busy_seconds": 500.0, "busy_limit_seconds": 60.0}
	})
	h := remoteHealth{BusySeconds: f64(0)}
	err := superviseRequest(context.Background(), srv.URL, h, time.Hour, []string{"a", "b"}, untilCancelled)
	se, ok := errors.AsType[*WorkerStuckError](err)
	if !ok {
		t.Fatalf("err = %v, want a stuck error", err)
	}
	if se.URL != srv.URL || len(se.Jobs) != 2 {
		t.Errorf("stuck error = %+v", se)
	}
}

func TestSuperviseRequest_AWorkerInsideItsDeadlineIsLeftAlone(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	srv, hits := probeServer(t, func() (int, map[string]any) {
		return http.StatusOK, map[string]any{"busy_seconds": 70.0, "busy_limit_seconds": 60.0} // inside the grace
	})
	h := remoteHealth{BusySeconds: f64(0)}
	err := superviseRequest(context.Background(), srv.URL, h, time.Hour, nil, func(ctx context.Context) error {
		time.Sleep(300 * time.Millisecond)
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() < 2 {
		t.Errorf("the worker was probed %d time(s) during a 300 ms request at a 20 ms interval", hits.Load())
	}
}

func TestSuperviseRequest_AnOldWorkerIsNeverProbed(t *testing.T) {
	// A worker older than 1.8.0 blocks its event loop for the length of a
	// request, so a probe would fail on a healthy one.
	shortLiveness(t, 10*time.Millisecond)
	srv, hits := probeServer(t, func() (int, map[string]any) { return http.StatusInternalServerError, nil })
	err := superviseRequest(context.Background(), srv.URL, remoteHealth{}, time.Hour, nil, func(ctx context.Context) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("an old worker was probed %d time(s)", hits.Load())
	}
}

func TestSuperviseRequest_TheCallersCancellationWins(t *testing.T) {
	shortLiveness(t, 10*time.Millisecond)
	srv, _ := probeServer(t, func() (int, map[string]any) { return http.StatusInternalServerError, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	err := superviseRequest(ctx, srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, nil, untilCancelled)
	if err == nil || IsWorkerStuck(err) || IsTransport(err) {
		t.Fatalf("an operator cancel must come back as the plain context error, got %v", err)
	}
}

func TestSuperviseRequest_StopsItsPollerWhenTheRequestEnds(t *testing.T) {
	shortLiveness(t, 10*time.Millisecond)
	srv, hits := probeServer(t, func() (int, map[string]any) {
		return http.StatusOK, map[string]any{"busy_seconds": 1.0, "busy_limit_seconds": 60.0}
	})
	if err := superviseRequest(context.Background(), srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, nil,
		func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	after := hits.Load()
	time.Sleep(100 * time.Millisecond)
	if hits.Load() != after {
		t.Errorf("the poller kept probing after the request finished (%d -> %d)", after, hits.Load())
	}
}

// End to end through the path chunk sender: a worker whose host goes away after
// it accepted the request is detected by /health, not after the (here: ten
// minute) request ceiling, and the chunk that finished earlier is kept.
func TestPathChunk_AWorkerThatGoesSilentMidRequestIsAnOutageAndKeepsEarlierChunks(t *testing.T) {
	shortLiveness(t, 20*time.Millisecond)
	var down atomic.Bool
	w := hangingPathWorker(t, pathHealth(map[string]any{"busy_seconds": 0.0, "busy_limit_seconds": nil}), "book-c")
	inner := w.srv.Config.Handler
	w.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && down.Load() {
			http.Error(rw, "gone", http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(rw, r)
	})
	useEndpoints(t, 2, w.srv.URL)

	h := remoteHealth{BusySeconds: f64(0)}
	go func() {
		// The host "goes away" shortly after the run starts.
		time.Sleep(150 * time.Millisecond)
		down.Store(true)
	}()
	start := time.Now()
	res, err := transcribePathChunks(context.Background(), w.srv.URL, h, 1, pathJobs(t, 4), nil, nil, nil)
	if !IsTransport(err) || IsWorkerStuck(err) {
		t.Fatalf("err = %v, want an outage (transport, not stuck)", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s: the ten-minute ceiling decided, not the liveness probe", d)
	}
	if len(res) != 2 {
		t.Errorf("the chunk answered before the host went silent must be kept, got %d result(s)", len(res))
	}
}

// N5: a probe that cancels the request in the window after it already failed
// for its own reason (here an HTTP 500 from the worker) must not relabel that
// failure as a stuck or unreachable worker. The window is nanoseconds wide in
// production; the hook lands the cancel in it every time. The positive side (a
// request the supervisor did end keeps its cause) is the outage and wedged
// tests above, whose request returns the context's Canceled.
func TestSuperviseRequest_ALivenessCancelAfterTheRequestFailedDoesNotRelabelIt(t *testing.T) {
	for _, cause := range []error{errLivenessWedged, errLivenessUnreachable} {
		t.Run(cause.Error(), func(t *testing.T) {
			old := superviseAfterRequestHook
			superviseAfterRequestHook = func(cancel context.CancelCauseFunc) { cancel(cause) }
			t.Cleanup(func() { superviseAfterRequestHook = old })
			srv, _ := probeServer(t, func() (int, map[string]any) {
				return http.StatusOK, map[string]any{"busy_seconds": 1.0, "busy_limit_seconds": 60.0}
			})
			http500 := errors.New("worker answered HTTP 500: internal error")
			err := superviseRequest(context.Background(), srv.URL, remoteHealth{BusySeconds: f64(0)}, time.Hour, []string{"a"},
				func(context.Context) error { return http500 })
			if !errors.Is(err, http500) || IsWorkerStuck(err) {
				t.Fatalf("the request's own failure must come back as itself, got %T %v", err, err)
			}
			var te *TransportError
			if errors.As(err, &te) {
				t.Fatalf("an HTTP 500 was relabelled as an unreachable worker: %v", err)
			}
		})
	}
}
