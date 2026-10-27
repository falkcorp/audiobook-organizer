// file: internal/transcribe/canary_test.go
// version: 1.4.1
// guid: d4812f1f-05db-470a-939d-c8a0a264f017
// last-edited: 2026-10-27

package transcribe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The canary clip is real speech in the format every worker already decodes
// from the upload path: a RIFF/WAVE PCM file, 16 kHz, mono, 16-bit, about two
// seconds, small enough to embed.
func TestCanaryClip_IsAShortSpeechClipInTheUploadFormat(t *testing.T) {
	c := canaryClip
	if len(c) < 44 || string(c[0:4]) != "RIFF" || string(c[8:12]) != "WAVE" || string(c[12:16]) != "fmt " {
		t.Fatalf("not a RIFF/WAVE file (%d bytes)", len(c))
	}
	format, channels := binary.LittleEndian.Uint16(c[20:22]), binary.LittleEndian.Uint16(c[22:24])
	rate, bits := binary.LittleEndian.Uint32(c[24:28]), binary.LittleEndian.Uint16(c[34:36])
	if format != 1 || channels != 1 || rate != 16000 || bits != 16 {
		t.Fatalf("format=%d channels=%d rate=%d bits=%d, want PCM mono 16 kHz 16-bit", format, channels, rate, bits)
	}
	seconds := float64(len(c)-44) / 32000
	if seconds < 1 || seconds > 4 || len(c) > 128<<10 {
		t.Fatalf("%.2f s, %d bytes: want a clip of a few seconds that embeds cheaply", seconds, len(c))
	}
}

// canaryWorker is a fake worker's /transcribe: it records each upload and
// answers with answer(), after delay.
func canaryWorker(t *testing.T, delay time.Duration, answer func() (int, map[string]any)) (url string, uploads func() [][]byte, names func() []string) {
	t.Helper()
	var (
		mu    sync.Mutex
		got   [][]byte
		files []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /transcribe", func(rw http.ResponseWriter, r *http.Request) {
		f, fh, err := r.FormFile("file")
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		mu.Lock()
		got, files = append(got, b), append(files, fh.Filename)
		mu.Unlock()
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		code, body := answer()
		rw.WriteHeader(code)
		_ = json.NewEncoder(rw).Encode(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, func() [][]byte {
			mu.Lock()
			defer mu.Unlock()
			return got
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return files
		}
}

func TestSendCanary_UploadsTheClipAndPassesOnlyAnAnswerWithoutAnError(t *testing.T) {
	ok := func() (int, map[string]any) {
		return 200, map[string]any{"text": "Welcome to Gotham City.", "error": nil}
	}
	url, uploads, names := canaryWorker(t, 0, ok)
	if err := sendCanary(context.Background(), gatedEndpoint{Endpoint: Endpoint{URL: url}}); err != nil {
		t.Fatalf("a worker that transcribed the clip failed the canary: %v", err)
	}
	if u := uploads(); len(u) != 1 || !bytes.Equal(u[0], canaryClip) || names()[0] != canaryFileName {
		t.Fatalf("uploads = %d (names %v), want the embedded clip once as %q", len(u), names(), canaryFileName)
	}

	// Any answer without an error passes: a worker whose VAD heard nothing
	// still decoded in time.
	silent, _, _ := canaryWorker(t, 0, func() (int, map[string]any) { return 200, map[string]any{"text": "", "error": nil} })
	if err := sendCanary(context.Background(), gatedEndpoint{Endpoint: Endpoint{URL: silent}}); err != nil {
		t.Fatalf("an empty transcript failed the canary: %v", err)
	}

	for name, answer := range map[string]func() (int, map[string]any){
		"a timeout": func() (int, map[string]any) { return 200, map[string]any{"text": "", "error": timeoutText} },
		"a kill": func() (int, map[string]any) {
			return 200, map[string]any{"text": "", "error": PathErrDecoderKilled + pathErrSeparator + "killed"}
		},
		"an HTTP 500": func() (int, map[string]any) { return 500, map[string]any{} },
	} {
		url, _, _ := canaryWorker(t, 0, answer)
		if err := sendCanary(context.Background(), gatedEndpoint{Endpoint: Endpoint{URL: url}}); err == nil {
			t.Errorf("%s passed the canary", name)
		}
	}
}

func TestSendCanary_AWorkerSlowerThanTheCanaryTimeoutFails(t *testing.T) {
	old := canaryTimeout
	canaryTimeout = 100 * time.Millisecond
	t.Cleanup(func() { canaryTimeout = old })
	url, _, _ := canaryWorker(t, 5*time.Second, func() (int, map[string]any) { return 200, map[string]any{"text": "late", "error": nil} })
	start := time.Now()
	if err := sendCanary(context.Background(), gatedEndpoint{Endpoint: Endpoint{URL: url}}); err == nil {
		t.Fatal("a worker that did not answer within the canary timeout passed")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the canary waited %s, want it bounded by canaryTimeout", d)
	}
}

// SF1 (review 5 of #4182), the reviewer's probe made a test. A worker decodes
// one request at a time (whisper_mlx_server.py's inference lock). Another
// dispatch holds this endpoint's request slot and is decoding a request that
// runs longer than the canary's whole timeout; the canary must wait for the
// slot, not queue on the worker against its own clock, and pass once the slot
// is free. Before the fix it went straight to the worker and timed out in the
// queue, failing a healthy machine.
func TestSendCanary_WaitsForABusyHealthyWorkersSlotAndThenPasses(t *testing.T) {
	old := canaryTimeout
	canaryTimeout = 300 * time.Millisecond // stands for 60 s against a 16 x 360 s chunk
	t.Cleanup(func() { canaryTimeout = old })
	var (
		lock    sync.Mutex
		canary  sync.WaitGroup
		started = make(chan struct{})
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /transcribe", func(rw http.ResponseWriter, r *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		_ = json.NewEncoder(rw).Encode(map[string]any{"text": "Welcome to Gotham City.", "error": nil})
	})
	mux.HandleFunc("POST /other-dispatch", func(rw http.ResponseWriter, r *http.Request) {
		lock.Lock()
		close(started)
		time.Sleep(900 * time.Millisecond) // three canary timeouts
		lock.Unlock()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	release, err := acquireInFlight(context.Background(), srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer release()
		if resp, err := http.Post(srv.URL+"/other-dispatch", "text/plain", nil); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started

	var cerr error
	canary.Go(func() {
		cerr = sendCanary(context.Background(), gatedEndpoint{Endpoint: Endpoint{URL: srv.URL, Concurrency: 1}, Probed: true})
	})
	canary.Wait()
	if cerr != nil {
		t.Fatalf("a healthy worker busy with another dispatch failed the canary: %v", cerr)
	}
}

// A slot that never frees within canarySlotWait is no evidence about the
// worker: the canary is not sent, and the error is errCanarySlotBusy, which no
// part of the classification reads as a network failure. A cancelled op
// returns the cancellation, not busy.
func TestSendCanary_ASlotThatNeverFreesIsBusyNotAFailure(t *testing.T) {
	oldWait := canarySlotWait
	canarySlotWait = func(gatedEndpoint) time.Duration { return 100 * time.Millisecond }
	t.Cleanup(func() { canarySlotWait = oldWait })
	url, uploads, _ := canaryWorker(t, 0, func() (int, map[string]any) { return 200, map[string]any{"text": "x", "error": nil} })
	release, err := acquireInFlight(context.Background(), url, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	g := gatedEndpoint{Endpoint: Endpoint{URL: url, Concurrency: 1}, Probed: true}

	err = sendCanary(context.Background(), g)
	if !errors.Is(err, errCanarySlotBusy) {
		t.Fatalf("err = %v, want errCanarySlotBusy", err)
	}
	if isTransportishError(err) || errors.Is(err, ErrSlotWait) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v: a busy slot must not read as a network failure or carry the slot wait's deadline", err)
	}
	if n := len(uploads()); n != 0 {
		t.Fatalf("uploads = %d, want the canary never sent while the slot was taken", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sendCanary(ctx, g); !errors.Is(err, context.Canceled) || errors.Is(err, errCanarySlotBusy) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
}

// The slot wait outlasts one full request by the worker's own formula: a
// batch of 90 s first-pass clips at the reported floor and factor, plus the
// model load and ffmpeg allowances. Shorter, and a canary queued behind a
// chunk of genuine loopers would always come back busy.
func TestCanarySlotWait_OutlastsAFullFirstPassRequest(t *testing.T) {
	useEndpoints(t, 16)
	h := remoteHealth{ClipTimeoutFloorS: 60, ClipTimeoutFactor: 4, ChildReadyTimeoutS: 300, ExtractTimeoutS: 120}
	if got, want := canarySlotWait(gatedEndpoint{Health: h}), (16*360+60+300+120)*time.Second; got != want {
		t.Fatalf("canarySlotWait = %s, want %s (16 x 360 s + slack + model load + ffmpeg)", got, want)
	}
}

// resetCanaryFailed forgets every machine remembered as failing the canary.
func resetCanaryFailed() {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	clear(canaryFailed.m)
	clear(canaryFailed.byURL)
	clear(canaryFailed.attributed)
	clear(canaryFailed.firstRefused)
}
