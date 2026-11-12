// file: core/integrations/ai/metrics_test.go
// version: 1.0.0
// guid: d4a81c53-7e29-4f06-9b3d-0c5e2a8f7b61
// last-edited: 2026-11-12

package ai_test

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// TestAISeries_Golden: the "ai" scope exports exactly the families of 05-ai
// section 8, with the closed label sets the golden lists, and no instrument
// reaches the cardinality limit. A family with no sample exports nothing, so
// the test makes one of every kind of call first: a text call that fails over
// (requests, tokens, request, slot wait, failovers), a call no endpoint is
// capable of (no_capable), a structured call that needs a repair turn
// (structured_repairs), a transcription (audio_seconds) and an open batch job
// (batch_jobs); the in-flight and benched gauges are observed per instance.
func TestAISeries_Golden(t *testing.T) {
	t.Parallel()
	e := newBatchEnv(t, withMetrics())
	ctx := context.Background()

	// The batch job first: once arkham is benched by the failover below it no
	// longer takes a submit.
	e.submit(1)
	if _, err := e.poll(); err != nil {
		t.Fatalf("poll: %v", err)
	}

	transcribe := e.capability("transcribe", func(s *ai.CapabilitySpec) { s.Kind = ai.KindTranscribe })
	nobody := e.capability("nobody")
	good := fake()
	set := textEndpoint("joker-7b", "gotham.riddle_batch", "gotham.transcribe")
	set.TranscribeModel = "whisper-bat"
	e.addEndpoint("blackgate", 2, set, good)
	e.f.ScriptText(coretest.FakeAIStep{Err: classErr(integrations.ClassTransient)})
	good.ScriptText(
		coretest.FakeAIStep{Text: "the Penguin", Usage: ai.Usage{Requests: 1, InputTokens: 12, CachedInputTokens: 2, OutputTokens: 3}},
		coretest.FakeAIStep{Text: "not json at all"},
		coretest.FakeAIStep{Text: goodVerdict},
	)
	good.ScriptTranscribe(coretest.FakeAIStep{Transcript: "holy riddles", Duration: 2 * time.Second, Usage: ai.Usage{Requests: 1, AudioSeconds: 2}})

	if _, err := e.r.Generate(ctx, e.c, text("who?")); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := e.r.Generate(ctx, nobody, text("who?")); err == nil {
		t.Fatal("a capability no endpoint ticks was served")
	}
	if _, _, err := ai.GenerateJSON[verdict](ctx, e.r, e.c, text("who?")); err != nil {
		t.Fatalf("GenerateJSON: %v", err)
	}
	if _, err := e.r.Transcribe(ctx, transcribe, ai.TranscribeRequest{Audio: ai.AudioSource{Data: []byte("RIFF"), MIME: "audio/wav"}}); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	metricstest.AssertSeries(t, e.prov, e.scope, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, e.prov)
}
