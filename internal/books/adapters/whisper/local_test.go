// file: internal/books/adapters/whisper/local_test.go
// version: 0.1.0
// guid: 3c0d8d2e-5b0e-4a77-9a0a-6a3cbb7d7a4b
// last-edited: 2026-11-21

package whisper

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/library"
)

// fakeRunner is a Runner that answers from fields, so a test controls what the
// "process" does without starting one.
type fakeRunner struct {
	model    string
	checkErr error
	text     string
	runErr   error
	block    chan struct{} // when non-nil, Transcribe waits on it (or ctx)

	inFlight, peak atomic.Int32
	mu             sync.Mutex
	paths          []string
	staged         [][]byte
}

func (f *fakeRunner) Model() string               { return f.model }
func (f *fakeRunner) Check(context.Context) error { return f.checkErr }
func (f *fakeRunner) Transcribe(ctx context.Context, wavPath string) (string, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	data, err := os.ReadFile(wavPath)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	f.paths = append(f.paths, wavPath)
	f.staged = append(f.staged, data)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.text, f.runErr
}

func newLocal(t *testing.T, r Runner) *localInstance {
	t.Helper()
	b, err := buildLocal(r, integrations.Env{Ref: integrations.Ref{Kind: integrations.KindAI, Instance: "batcave-local"}, Log: slog.New(slog.DiscardHandler)},
		integrations.InstanceConfig{Adapter: LocalAdapterID, Enabled: true, Settings: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	return b.(*localInstance)
}

func TestLocal_Caps(t *testing.T) {
	t.Parallel()
	i := newLocal(t, &fakeRunner{model: " base.en "})
	caps, err := i.Caps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(caps.Models) != 1 || caps.Models[0] != "base.en" || caps.Locality != ai.LocalityLocal || len(caps.Features) != 0 || len(caps.Labels) != 0 {
		t.Errorf("caps = %+v: want the model, local, and no features or labels it cannot prove", caps)
	}
	bad := newLocal(t, &fakeRunner{checkErr: errors.New("uv: not found")})
	if _, err := bad.Caps(context.Background()); classOf(err) != integrations.ClassTransient {
		t.Errorf("Caps with a missing launcher: %v", err)
	}
}

func TestLocal_HealthAndTest(t *testing.T) {
	t.Parallel()
	ok := newLocal(t, &fakeRunner{})
	if h := ok.Health(context.Background()); h.Status != integrations.HealthOK {
		t.Errorf("Health = %+v", h)
	}
	if err := ok.Test(context.Background()); err != nil {
		t.Errorf("Test: %v", err)
	}
	bad := newLocal(t, &fakeRunner{checkErr: errors.New("secret launcher path /home/bruce/uv")})
	h := bad.Health(context.Background())
	if h.Status != integrations.HealthDown || strings.Contains(h.Message, "bruce") {
		t.Errorf("Health = %+v: down, and the runner's text stays out of the message", h)
	}
	if err := bad.Test(context.Background()); classOf(err) != integrations.ClassTransient {
		t.Errorf("Test: %v", err)
	}
	if err := bad.Close(); err != nil {
		t.Fatal(err)
	}
	if h := ok.Health(context.Background()); h.Status != integrations.HealthOK {
		t.Errorf("a sibling instance was affected: %+v", h)
	}
}

func TestLocal_TranscribeStagesTheClipAndCleansUp(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{text: "Wayne Manor"}
	i := newLocal(t, r)
	got, err := i.Transcribe(context.Background(), "base.en", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes, MIME: "audio/wav"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Wayne Manor" || got.Usage.Requests != 1 {
		t.Errorf("got %+v", got)
	}
	if len(r.paths) != 1 || !bytes.Equal(r.staged[0], wavBytes) || !strings.HasSuffix(r.paths[0], ".wav") {
		t.Fatalf("the runner saw paths %v", r.paths)
	}
	if _, err := os.Stat(r.paths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staged clip %s outlived the call: %v", r.paths[0], err)
	}
}

func TestLocal_RunnerFailureIsTransientAndCleansUp(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{runErr: errors.New("exit status 137 at /home/bruce/.cache/uv")}
	i := newLocal(t, r)
	_, err := i.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	e, ok := asError(err)
	if !ok || e.Class != integrations.ClassTransient || strings.Contains(e.Error(), "bruce") || !strings.Contains(e.Detail, "137") {
		t.Errorf("err = %+v", e)
	}
	if _, serr := os.Stat(r.paths[0]); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("the staged clip outlived the failure: %v", serr)
	}
}

func TestLocal_RefusesAPathAndEmptyAudio(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	i := newLocal(t, r)
	for name, a := range map[string]ai.AudioSource{
		"a library path": {Path: library.Path{Root: "books-library", Rel: "a.m4b"}},
		"nothing":        {},
	} {
		_, err := i.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: a})
		if classOf(err) != integrations.ClassInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(r.paths) != 0 {
		t.Error("the runner was called for a request the adapter refused")
	}
}

// One clip decodes at a time however many callers there are, and a caller that
// gives up while waiting for the slot leaves with Canceled and starts nothing.
func TestLocal_SerialisesAndCancelsWhileWaiting(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{text: "x", block: make(chan struct{})}
	i := newLocal(t, r)
	const callers = 6
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			if _, err := i.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}}); err != nil {
				t.Errorf("Transcribe: %v", err)
			}
		})
	}
	// Wait until the first caller holds the slot and is inside the runner.
	deadline := time.Now().Add(10 * time.Second)
	for r.inFlight.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no call reached the runner")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := i.Transcribe(ctx, "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	if got := integrations.ClassOf(ctx, err); got != integrations.ClassCanceled {
		t.Errorf("a waiter whose deadline passed: class %v (%v), want Canceled", got, err)
	}
	for range callers {
		r.block <- struct{}{}
	}
	wg.Wait()
	if p := r.peak.Load(); p != 1 {
		t.Errorf("peak concurrency in the runner = %d, want 1", p)
	}
	if n := len(r.paths); n != callers {
		t.Errorf("the runner decoded %d clip(s), want %d (the cancelled waiter must not have)", n, callers)
	}
}

func TestLocal_CancelDuringDecodeIsCanceled(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{block: make(chan struct{})}
	i := newLocal(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := i.Transcribe(ctx, "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
		done <- err
	}()
	for r.inFlight.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	err := <-done
	if got := integrations.ClassOf(ctx, err); got != integrations.ClassCanceled {
		t.Errorf("class %v (%v), want Canceled, not a Transient bench of the endpoint", got, err)
	}
}

func TestLocal_CloseTwiceThenRefuses(t *testing.T) {
	t.Parallel()
	i := newLocal(t, &fakeRunner{})
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := i.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	_, err := i.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes}})
	if classOf(err) != integrations.ClassRefusedLocal {
		t.Errorf("Transcribe after Close: %v", err)
	}
	if h := i.Health(context.Background()); h.Status != integrations.HealthDown {
		t.Errorf("Health after Close = %+v", h)
	}
}

func TestLocal_SettingsMustDecode(t *testing.T) {
	t.Parallel()
	_, err := buildLocal(&fakeRunner{}, integrations.Env{Ref: gotham}, integrations.InstanceConfig{Settings: map[string]any{"concurrency": "many"}})
	if classOf(err) != integrations.ClassInvalid {
		t.Errorf("err = %v", err)
	}
}
