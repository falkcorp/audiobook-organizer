// file: core/integrations/ai/aitest/conformance.go
// version: 1.0.0
// guid: a7c5e913-2d48-4b60-8f1a-3e9d6b0c4a52
// last-edited: 2026-11-12

package aitest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// Outcome is how a scripted remote answers its next request.
type Outcome uint8

// The outcomes a recorded run scripts. Each is a failure class an adapter must
// map to the integrations.Class the router's failover table acts on (05-ai
// section 5), whatever wire the server uses to say it.
const (
	OutcomeOK           Outcome = iota // the server's normal answer
	OutcomeRateLimited                 // 429 without a quota marker
	OutcomeQuota                       // 429 whose error type or code is a quota marker
	OutcomeAuthRejected                // 401
	OutcomeServerError                 // 500
	OutcomeGarbage                     // 200 with a body that is not the API's JSON
)

// Replay scripts the far side of a recorded run. A live run has none: its
// subject sets Replay to nil and the scenarios that need a scripted answer are
// skipped.
type Replay interface {
	// Answer makes the remote answer the next request as o; the one after that
	// is OutcomeOK again.
	Answer(t testing.TB, o Outcome)
	// Requests is how many requests have reached the remote since the subject
	// was built.
	Requests() int
}

// Models are the models the suite asks for. A model left empty skips the
// scenario of its kind (a text-only server has no embedding model).
type Models struct{ Text, Embed, Transcribe string }

// Subject is one adapter instance under test.
type Subject struct {
	Backend ai.Backend
	Models  Models
	// Batch says the remote implements the batch API (OpenAI does; Ollama and
	// llama.cpp do not). The batch scenario runs only when it is set.
	Batch bool
	// Replay is set for a recorded run and nil for a live one.
	Replay Replay
}

// Conformance runs the adapter conformance of 05-ai section 9 against the
// subject factory builds (one fresh subject per scenario). It asserts, for
// every interface the Backend implements: the happy path of each call kind
// returns a usable answer; every failure class maps to the integrations.Class
// the router fails over on, carries the instance's Ref, and is attempted once
// (nothing below the router retries); a canceled caller is ClassCanceled and
// sends nothing; and Close can be called twice. A live run is the same suite
// without the scripted failures.
func Conformance(t *testing.T, factory func(t *testing.T) Subject) {
	t.Helper()
	ctx := context.Background()

	t.Run("caps_and_health", func(t *testing.T) {
		s := factory(t)
		caps, err := s.Backend.Caps(ctx)
		if err != nil {
			t.Fatalf("Caps: %v", err)
		}
		if len(caps.Models) == 0 {
			t.Error("Caps lists no model")
		}
		if h := s.Backend.Health(ctx); h.Status != integrations.HealthOK {
			t.Errorf("Health = %+v, want OK", h)
		}
	})

	t.Run("generate", func(t *testing.T) {
		s := factory(t)
		g, ok := s.Backend.(ai.TextGenerator)
		if !ok || s.Models.Text == "" {
			t.Skip("no text generation")
		}
		got, err := g.Generate(ctx, s.Models.Text, ai.TextRequest{
			System:          "Answer in one short sentence.",
			Messages:        []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "Name one Gotham villain."}}}},
			MaxOutputTokens: 64,
		})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got.Text == "" || got.Usage.Requests != 1 {
			t.Errorf("Generate = %+v, want text and one counted request", got)
		}
	})

	t.Run("embed", func(t *testing.T) {
		s := factory(t)
		e, ok := s.Backend.(ai.Embedder)
		if !ok || s.Models.Embed == "" {
			t.Skip("no embeddings")
		}
		got, err := e.Embed(ctx, s.Models.Embed, ai.EmbedRequest{Inputs: []string{"the batcave", "the bat signal"}})
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if len(got.Vectors) != 2 || len(got.Vectors[0]) == 0 || len(got.Vectors[0]) != len(got.Vectors[1]) {
			t.Fatalf("Embed returned %d vector(s) of lengths %d and %d, want two of one dimension", len(got.Vectors), len(got.Vectors[0]), len(got.Vectors[len(got.Vectors)-1]))
		}
		for _, v := range got.Vectors {
			for _, f := range v {
				if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
					t.Fatalf("a vector holds %v", f)
				}
			}
		}
	})

	t.Run("transcribe", func(t *testing.T) {
		s := factory(t)
		tr, ok := s.Backend.(ai.Transcriber)
		if !ok || s.Models.Transcribe == "" {
			t.Skip("no transcription")
		}
		// Half a second of 16 kHz mono silence: any server accepts it, and a
		// transcript of silence may be empty, so only the call is asserted.
		got, err := tr.Transcribe(ctx, s.Models.Transcribe, ai.TranscribeRequest{Audio: ai.AudioSource{Data: silence(), MIME: "audio/wav"}, Timestamps: true})
		if err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if got.Usage.Requests != 1 {
			t.Errorf("Transcribe = %+v, want one counted request", got)
		}
	})

	t.Run("canceled_caller", func(t *testing.T) {
		s := factory(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		before := 0
		if s.Replay != nil {
			before = s.Replay.Requests()
		}
		_, err := s.Backend.Caps(cctx)
		if c := integrations.ClassOf(cctx, err); c != integrations.ClassCanceled {
			t.Errorf("Caps with a canceled context: class %v (%v), want Canceled", c, err)
		}
		if s.Replay != nil && s.Replay.Requests() != before {
			t.Error("a canceled caller still reached the remote")
		}
	})

	t.Run("close_twice", func(t *testing.T) {
		s := factory(t)
		if err := s.Backend.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		if err := s.Backend.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})

	t.Run("failure_classes", func(t *testing.T) {
		for _, f := range []struct {
			name string
			o    Outcome
			want integrations.Class
		}{
			{"rate limited", OutcomeRateLimited, integrations.ClassRateLimited},
			{"quota spent", OutcomeQuota, integrations.ClassQuota},
			{"key rejected", OutcomeAuthRejected, integrations.ClassAuth},
			{"server error", OutcomeServerError, integrations.ClassTransient},
			{"answer is not the API's JSON", OutcomeGarbage, integrations.ClassBadOutput},
		} {
			t.Run(f.name, func(t *testing.T) {
				for _, k := range callKinds() {
					s := factory(t)
					if s.Replay == nil {
						t.Skip("a live server cannot be scripted to fail")
					}
					fn, ok := k.call(ctx, s)
					if !ok {
						continue
					}
					before := s.Replay.Requests()
					s.Replay.Answer(t, f.o)
					err := fn()
					if err == nil {
						t.Errorf("%s: a %s answer was accepted", k.name, f.name)
						continue
					}
					e, isErr := errors.AsType[*integrations.Error](err)
					switch {
					case !isErr:
						t.Errorf("%s: error %T is not an *integrations.Error: %v", k.name, err, err)
					case e.Class != f.want:
						t.Errorf("%s: class = %v, want %v (%v)", k.name, e.Class, f.want, err)
					case e.Ref == (integrations.Ref{}):
						t.Errorf("%s: the error names no instance", k.name)
					}
					if n := s.Replay.Requests() - before; n != 1 {
						t.Errorf("%s: %d request(s) for one call; nothing below the router may retry", k.name, n)
					}
				}
			})
		}
	})

	t.Run("batch", func(t *testing.T) {
		s := factory(t)
		b, ok := s.Backend.(ai.BatchBackend)
		if !ok || !s.Batch || s.Models.Text == "" {
			t.Skip("no batch API")
		}
		items := []ai.BatchItem{
			{CustomID: "q1", Request: ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "one"}}}}}},
			{CustomID: "q2", Request: ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "two"}}}}}},
		}
		h, err := b.SubmitBatch(ctx, s.Models.Text, items, map[string]string{"job": "riddler-001"})
		if err != nil || h == "" {
			t.Fatalf("SubmitBatch = %q, %v", h, err)
		}
		deadline := time.Now().Add(2 * time.Minute)
		var st ai.BatchState
		for {
			if st, err = b.BatchStatus(ctx, h); err != nil {
				t.Fatalf("BatchStatus: %v", err)
			}
			if st.Phase != ai.BatchInProgress || s.Replay != nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Second)
		}
		if st.Phase != ai.BatchCompleted {
			t.Fatalf("BatchStatus = %+v, want a completed batch", st)
		}
		seq, err := b.BatchResults(ctx, h)
		if err != nil {
			t.Fatalf("BatchResults: %v", err)
		}
		got := map[string]bool{}
		for r, err := range seq {
			if err != nil {
				t.Fatalf("a result row: %v", err)
			}
			got[r.CustomID] = r.Err == "" && r.Text != nil
		}
		if len(got) != 2 || !got["q1"] || !got["q2"] {
			t.Errorf("results = %v, want a usable row for q1 and q2", got)
		}
		if found, err := b.FindBatches(ctx, map[string]string{"job": "riddler-001"}); err != nil || len(found) != 1 || found[0] != h {
			t.Errorf("FindBatches = %v, %v, want the batch just submitted", found, err)
		}
	})
}

// callKind is one call kind a Backend may implement: call returns a closure
// that makes the call, or false when the subject's Backend lacks the kind.
type callKind struct {
	name string
	call func(ctx context.Context, s Subject) (func() error, bool)
}

// callKinds lists every call kind in a fixed order, so the failure scenarios
// cover each interface the Backend implements and read the same on every run.
func callKinds() []callKind {
	return []callKind{
		{"caps", func(ctx context.Context, s Subject) (func() error, bool) {
			return func() error { _, err := s.Backend.Caps(ctx); return err }, true
		}},
		{"generate", func(ctx context.Context, s Subject) (func() error, bool) {
			g, ok := s.Backend.(ai.TextGenerator)
			if !ok || s.Models.Text == "" {
				return nil, false
			}
			return func() error {
				_, err := g.Generate(ctx, s.Models.Text, ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "hi"}}}}})
				return err
			}, true
		}},
		{"embed", func(ctx context.Context, s Subject) (func() error, bool) {
			e, ok := s.Backend.(ai.Embedder)
			if !ok || s.Models.Embed == "" {
				return nil, false
			}
			return func() error {
				_, err := e.Embed(ctx, s.Models.Embed, ai.EmbedRequest{Inputs: []string{"hi"}})
				return err
			}, true
		}},
		{"transcribe", func(ctx context.Context, s Subject) (func() error, bool) {
			tr, ok := s.Backend.(ai.Transcriber)
			if !ok || s.Models.Transcribe == "" {
				return nil, false
			}
			return func() error {
				_, err := tr.Transcribe(ctx, s.Models.Transcribe, ai.TranscribeRequest{Audio: ai.AudioSource{Data: silence(), MIME: "audio/wav"}})
				return err
			}, true
		}},
	}
}

// wavHeader is the 44-byte header of a PCM WAV file.
type wavHeader struct {
	RIFF       [4]byte
	Size       uint32
	WAVE       [4]byte
	Fmt        [4]byte
	FmtSize    uint32
	Format     uint16
	Channels   uint16
	Rate       uint32
	ByteRate   uint32
	BlockAlign uint16
	Bits       uint16
	Data       [4]byte
	DataSize   uint32
}

// silence is half a second of 16 kHz mono 16-bit PCM silence as a WAV file.
func silence() []byte {
	const rate, seconds = 16000, 0.5
	pcm := make([]byte, int(rate*seconds)*2)
	h := wavHeader{
		RIFF: [4]byte{'R', 'I', 'F', 'F'}, Size: uint32(36 + len(pcm)), WAVE: [4]byte{'W', 'A', 'V', 'E'},
		Fmt: [4]byte{'f', 'm', 't', ' '}, FmtSize: 16, Format: 1, Channels: 1, Rate: rate, ByteRate: rate * 2, BlockAlign: 2, Bits: 16,
		Data: [4]byte{'d', 'a', 't', 'a'}, DataSize: uint32(len(pcm)),
	}
	var b bytes.Buffer
	// A fixed-size struct into a bytes.Buffer cannot fail; a failure here is a
	// bug in wavHeader, not a runtime condition, so it stops the run.
	if err := binary.Write(&b, binary.LittleEndian, h); err != nil {
		panic("aitest: encode the WAV header: " + err.Error())
	}
	b.Write(pcm)
	return b.Bytes()
}
