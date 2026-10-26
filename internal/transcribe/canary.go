// file: internal/transcribe/canary.go
// version: 1.1.0
// guid: 85242dc9-bee7-4bee-9854-d4965e925162
// last-edited: 2026-10-26

package transcribe

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"time"
)

// The canary clip (SF1 of review 4 of #4182). When every endpoint of a dispatch
// has faulted on timeouts, the dispatcher cannot tell a page of genuine
// loopers (the clips are to blame, and each clip's own timeout should count)
// from every machine being sick at once (a Mac swapping under a CI job, llm1
// out of the pool): both answer every clip with a timeout. So before it
// believes a faulted machine again it sends that machine this clip, two
// seconds of synthesised speech ("Welcome to Gotham City.") that any sound
// worker answers in a few seconds. A machine that answers it is decoding: its
// timeouts are about the clips, and are believed. One that does not is sick:
// none of its strikes count in that dispatch.
//
// It is 16 kHz mono 16-bit PCM, the format every worker already receives from
// the upload path, so it exercises the same decode as a real clip. Made with
// eSpeak NG 1.52.0 (its output is not covered by its licence) and ffmpeg; see
// scripts/make_canary_clip.sh.
//
//go:embed canary_clip.wav
var canaryClip []byte

// canaryFileName is the upload name of the canary clip, the key a worker logs.
const canaryFileName = "canary.wav"

// canaryTimeout bounds one canary request. The canary follows a timeout fault,
// and a worker restarts its decode child lazily, so the canary pays a model
// load first: about 7 s for whisper-small.en on the production M1 Max (measured
// from a cold worker, Hugging Face revision check included), then well under a
// second for the clip. The allowance is the worker's own per-clip floor (60 s),
// so the canary never gets more than one clip's time, with room for a slow
// load. Deriving it from child_ready_timeout_s, as requestTimeout does, would
// allow 360 s per canary; a machine that needs that long to load a model and
// decode two seconds of speech is the sick machine the canary is there to find.
var canaryTimeout = 60 * time.Second // a var so tests do not wait it out

// canaryFunc sends the canary clip to one endpoint: nil when the worker
// answered it without an error (any text, or none from a worker whose VAD
// heard nothing: the question is whether it decodes in time, not what it
// heard), else why not.
type canaryFunc func(ctx context.Context, g gatedEndpoint) error

// sendCanary is the canaryFunc of every production dispatch: the clip goes
// through /transcribe, the single-upload route both worker servers serve.
func sendCanary(ctx context.Context, g gatedEndpoint) error {
	ctx, cancel := context.WithTimeout(ctx, canaryTimeout)
	defer cancel()
	r, err := postOneWAV(ctx, &http.Client{Timeout: canaryTimeout}, g.Endpoint.URL, canaryFileName, bytes.NewReader(canaryClip))
	if err != nil {
		return fmt.Errorf("canary clip: %w", err)
	}
	if r.Error != "" {
		return fmt.Errorf("canary clip: %s", r.Error)
	}
	return nil
}
