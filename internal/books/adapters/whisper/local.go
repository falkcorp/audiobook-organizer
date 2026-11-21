// file: internal/books/adapters/whisper/local.go
// version: 0.1.0
// guid: 8338c68a-3b08-441e-a579-e4fdea41dfba
// last-edited: 2026-11-21

package whisper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// LocalAdapterID is the adapter of the local_process protocol.
const LocalAdapterID integrations.AdapterID = "books.whisper_local"

// Runner transcribes audio on this machine. It is what the caller supplies to
// LocalDescriptor: books builds one over HEAD's uv launcher, a test builds one
// that answers from a table. The adapter never starts a process itself, so it
// carries no decoder, no path to one and no assumption about the host.
type Runner interface {
	// Model names the model the runner decodes with. Caps reports it, and a
	// transcribe_model of the instance has to match what the router asks for.
	Model() string
	// Check says, cheaply and without decoding anything, whether the runner can
	// run now (its launcher is present). A non-nil error means it cannot.
	Check(ctx context.Context) error
	// Transcribe returns the text of the audio file at wavPath. The file exists
	// for the duration of the call only; the runner must not keep it.
	Transcribe(ctx context.Context, wavPath string) (string, error)
}

// LocalSettings are a whisper_local instance's settings: the routing attributes
// every ai instance carries and nothing else. A local process has no network
// address, so it has no url setting, and so nothing for the egress guard to
// judge.
type LocalSettings struct {
	ai.EndpointSettings
}

// LocalDescriptor is the whisper_local adapter for Registrar.Adapter, over r.
// It returns an error when r is nil or when LocalSettings cannot be reflected,
// which a test of this package rules out.
func LocalDescriptor(r Runner) (integrations.Descriptor, error) {
	if r == nil {
		return integrations.Descriptor{}, errors.New("whisper: LocalDescriptor needs a Runner")
	}
	schema, err := config.SchemaFor[LocalSettings]()
	if err != nil {
		return integrations.Descriptor{}, fmt.Errorf("whisper: settings schema: %w", err)
	}
	return ai.Adapter(integrations.Meta{
		ID:          LocalAdapterID,
		DisplayName: "Whisper (local process)",
		Version:     "1.0.0",
		Settings:    schema,
		// The adapter dials nothing, so the egress policy has no URL to judge;
		// the value is the kind's default.
		Egress: integrations.EgressConfigured,
		Limits: integrations.Limits{MaxRetries: -1, Timeout: callTimeout},
	}, func(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (ai.Backend, error) {
		return buildLocal(r, env, cfg)
	}), nil
}

// localInstance is the local process endpoint.
type localInstance struct {
	ref    integrations.Ref
	run    Runner
	log    *slog.Logger
	slot   chan struct{} // one token: the runner decodes one clip at a time
	mu     sync.Mutex
	closed bool
}

var (
	_ ai.Backend          = (*localInstance)(nil)
	_ ai.Transcriber      = (*localInstance)(nil)
	_ integrations.Tester = (*localInstance)(nil)
)

// buildLocal makes an instance. It does no I/O. LocalSettings carry nothing the
// instance reads (the routing attributes are the router's), so they are decoded
// only to refuse a settings record that does not fit the schema.
func buildLocal(r Runner, env integrations.Env, cfg integrations.InstanceConfig) (ai.Backend, error) {
	if _, err := decodeSettings[LocalSettings](env.Ref, cfg.Settings); err != nil {
		return nil, err
	}
	i := &localInstance{ref: env.Ref, run: r, log: logOf(env), slot: make(chan struct{}, 1)}
	i.slot <- struct{}{}
	return i, nil
}

// Close stops new transcriptions; one already decoding runs to its end. It can
// be called twice.
func (i *localInstance) Close() error {
	i.mu.Lock()
	i.closed = true
	i.mu.Unlock()
	return nil
}

func (i *localInstance) isClosed() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.closed
}

// Caps reports the runner's model and that the endpoint is local: a process on
// this machine has no network address, so it is always local (HEAD's
// aidispatch locality rule 2). GPU is not claimed: the runner proves none.
func (i *localInstance) Caps(ctx context.Context) (ai.BackendCaps, error) {
	if err := canceled(ctx, i.ref, "caps"); err != nil {
		return ai.BackendCaps{}, err
	}
	if err := i.check(ctx, "caps"); err != nil {
		return ai.BackendCaps{}, err
	}
	var caps ai.BackendCaps
	if m := strings.TrimSpace(i.run.Model()); m != "" {
		caps.Models = []string{m}
	}
	caps.Locality = ai.LocalityLocal
	return caps, nil
}

// check is the runner's cheap availability probe as a classified error.
func (i *localInstance) check(ctx context.Context, op string) error {
	if err := i.run.Check(ctx); err != nil {
		if cerr := canceled(ctx, i.ref, op); cerr != nil {
			return cerr
		}
		return failf(i.ref, op, integrations.ClassTransient, 0, err.Error(), "the local runner is unavailable")
	}
	return nil
}

// Health is the runner's availability. The detail of why it is unavailable goes
// to the instance's log, never into the health message.
func (i *localInstance) Health(ctx context.Context) integrations.Health {
	if i.isClosed() {
		return integrations.Health{Status: integrations.HealthDown, Message: "the instance is closed"}
	}
	if err := i.check(ctx, "health"); err != nil {
		if e, ok := errors.AsType[*integrations.Error](err); ok && e.Detail != "" {
			i.log.Warn("whisper_local: the runner is unavailable", "instance", i.ref.String(), "detail", e.Detail)
		}
		return integrations.Health{Status: integrations.HealthDown, Message: "the local runner is unavailable"}
	}
	return integrations.Health{Status: integrations.HealthOK}
}

// Test is the extra test of Registry.Test: the same availability check.
func (i *localInstance) Test(ctx context.Context) error { return i.check(ctx, "test") }

// Transcribe decodes audio bytes with the runner. A library path is refused:
// the endpoint never advertises shared_fs, so the router sends bytes, and a
// caller that sent a path anyway gets a plain ClassInvalid rather than a guess
// at where the file is. One clip decodes at a time; a call waits for the slot
// and gives up with the caller's context. The call is attempted once.
func (i *localInstance) Transcribe(ctx context.Context, _ string, req ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	if err := canceled(ctx, i.ref, "transcribe"); err != nil {
		return ai.TranscribeResponse{}, err
	}
	if len(req.Audio.Data) == 0 {
		return ai.TranscribeResponse{}, failf(i.ref, "transcribe", integrations.ClassInvalid, 0, "",
			"the local runner takes audio bytes, not a library path")
	}
	if i.isClosed() {
		return ai.TranscribeResponse{}, failf(i.ref, "transcribe", integrations.ClassRefusedLocal, 0, "", "the instance is closed")
	}
	select {
	case <-i.slot:
		defer func() { i.slot <- struct{}{} }()
	case <-ctx.Done():
		return ai.TranscribeResponse{}, canceled(ctx, i.ref, "transcribe")
	}
	f, err := os.CreateTemp("", "books-whisper-*"+clipExt(req.Audio.MIME))
	if err != nil {
		return ai.TranscribeResponse{}, failf(i.ref, "transcribe", integrations.ClassTransient, 0, err.Error(), "the clip could not be staged for the runner")
	}
	name := f.Name()
	defer func() {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			i.log.Warn("whisper_local: the staged clip was not removed", "instance", i.ref.String(), "err", rerr)
		}
	}()
	_, werr := f.Write(req.Audio.Data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return ai.TranscribeResponse{}, failf(i.ref, "transcribe", integrations.ClassTransient, 0, werr.Error(), "the clip could not be staged for the runner")
	}
	text, err := i.run.Transcribe(ctx, name)
	if err != nil {
		if cerr := canceled(ctx, i.ref, "transcribe"); cerr != nil {
			return ai.TranscribeResponse{}, cerr
		}
		// A process that failed (killed for memory, a missing interpreter) is the
		// endpoint's problem, not the clip's: another endpoint may serve it.
		return ai.TranscribeResponse{}, failf(i.ref, "transcribe", integrations.ClassTransient, 0, err.Error(), "the local runner failed")
	}
	return ai.TranscribeResponse{Text: text, Usage: ai.Usage{Requests: 1}}, nil
}
