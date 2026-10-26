// file: internal/transcribe/canary.go
// version: 1.2.0
// guid: 85242dc9-bee7-4bee-9854-d4965e925162
// last-edited: 2026-10-26

package transcribe

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

// errCanarySlotBusy is a canary that was never sent: every request slot on its
// endpoint stayed taken for canarySlotWait (SF1 of review 5 of #4182). That is
// no evidence about the machine, which may be decoding other dispatches' clips
// perfectly well, so the dispatcher neither believes it nor judges it sick: its
// strikes stay withheld, it is not put in cooldown, and a dispatch left with no
// machine to believe ends in an error the ops defer on without counting it
// toward stopping the run (not ErrPoolUnavailable). Its text must not contain
// any of transportWordings, or classifyTransport would read it as a network
// failure; in particular it never wraps the slot error, which carries "context
// deadline exceeded".
var errCanarySlotBusy = errors.New("canary clip not sent: every request slot on the endpoint stayed taken")

// canaryHolderClipSeconds is the clip length canarySlotWait assumes for the
// request ahead of the canary: the 90 s first-pass clip both intro ops send,
// which is what nearly every request on the pool carries.
const canaryHolderClipSeconds = 90

// canarySlotWait bounds how long the canary waits for its endpoint's request
// slot, which it takes like every real request (a worker decodes one request
// at a time behind its inference lock, so a canary that skipped the slot
// queued on the worker behind another dispatch's chunk and ran out its own
// timeout there, failing a healthy machine). The wait must outlast one full
// request already holding the slot: anything shorter loses to a chunk of
// genuine loopers (16 clips at the worker's 360 s per-clip limit is over an
// hour and a half), and a shorter wait would also turn a sick machine's
// verdict into "busy" whenever other pages hold its slots, so the run would
// never stop for it. It is that request's own ceiling, requestTimeout for a
// full batch of first-pass clips by this worker's reported formula: with the
// production defaults (60 s floor, 4x factor, 300 s child start, 120 s
// ffmpeg) 16 x 360 + 60 + 300 + 120 s = 104 min. A request that holds the slot
// longer than its ceiling is stuck, which the dispatch that sent it judges.
//
// The slot channel is not first-come first-served, so with several pages
// queued on one port the canary can still lose the slot to them for the
// whole wait; that ends as errCanarySlotBusy, a deferral, never a verdict.
// A var so tests can shorten it.
var canarySlotWait = func(g gatedEndpoint) time.Duration {
	return requestTimeout(g.Health, slices.Repeat([]float64{canaryHolderClipSeconds}, whisperBatchSize()), true)
}

// sendCanary is the canaryFunc of every production dispatch: the clip goes
// through /transcribe, the single-upload route both worker servers serve. It
// first takes the endpoint's request slot (bounded by canarySlotWait); the
// canaryTimeout clock starts only once the slot is held, so the canary times
// the worker's own decode and never another request's queue. A cancelled ctx
// returns ctx.Err() as it is.
func sendCanary(ctx context.Context, g gatedEndpoint) error {
	url := g.Endpoint.URL
	wait := canarySlotWait(g)
	slotCtx, cancelSlot := context.WithTimeout(ctx, wait)
	release, err := acquireInFlight(slotCtx, url, g.Endpoint.Concurrency)
	cancelSlot()
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("%w for %s", errCanarySlotBusy, wait.Round(time.Second))
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, canaryTimeout)
	defer cancel()
	r, err := postOneWAV(ctx, &http.Client{Timeout: canaryTimeout}, url, canaryFileName, bytes.NewReader(canaryClip))
	if err != nil {
		return fmt.Errorf("canary clip: %w", err)
	}
	if r.Error != "" {
		return fmt.Errorf("canary clip: %s", r.Error)
	}
	return nil
}
