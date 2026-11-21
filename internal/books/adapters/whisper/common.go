// file: internal/books/adapters/whisper/common.go
// version: 0.1.0
// guid: 82d29612-5cfa-417d-b09e-5c9f08236c94
// last-edited: 2026-11-21

package whisper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// callTimeout is the per-attempt transport timeout of the adapters' instances.
// The router bounds each call with its own capability timeout; this only has to
// exceed every one of them. A worker's own worst case for one clip is about
// twelve minutes (child-ready wait plus the clip deadline), so twenty is a
// ceiling that never cuts a healthy worker off.
const callTimeout = 20 * time.Minute

// decodeSettings reads an instance's stored settings into T. The settings are
// validated against the adapter's schema before they reach build, so a failure
// here means the schema and T drifted apart, which the descriptor tests rule
// out; it is still returned as a ClassInvalid build error, never a panic.
func decodeSettings[T any](ref integrations.Ref, settings map[string]any) (T, error) {
	var out T
	raw, err := json.Marshal(settings)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	if err != nil {
		return out, &integrations.Error{Ref: ref, Op: "build", Class: integrations.ClassInvalid,
			Err: errors.New("the instance's settings do not decode")}
	}
	return out, nil
}

// logOf is env's logger, or one that discards when the host supplied none.
func logOf(env integrations.Env) *slog.Logger {
	if env.Log != nil {
		return env.Log
	}
	return slog.New(slog.DiscardHandler)
}

// holdPolicy is the providerhttp stand-off a whisper_server instance asks for.
// The router, not the gate, owns an endpoint's cooldown (05-ai section 4 step
// 4): a gate default of half an hour for a 5xx would sit under the router's
// bench and refuse the call the router had just decided to try, so every class
// a transient condition produces is held for no longer than the router's first
// rung. Auth is the exception, as it is for core.openai_compat: a rejection a
// reverse proxy in front of the worker makes does not heal in seconds.
//
// It is a function, not a package variable, so no two descriptors share the map.
func holdPolicy() integrations.HoldPolicy {
	return integrations.HoldPolicy{
		Durations: map[integrations.Class]time.Duration{
			integrations.ClassTransient:   ai.DefaultCooldownBase,
			integrations.ClassRateLimited: ai.DefaultCooldownBase,
			integrations.ClassQuota:       ai.DefaultCooldownBase,
			integrations.ClassAuth:        ai.DefaultCooldownMax,
		},
		Max:     ai.DefaultCooldownMax,
		Persist: false,
	}
}

// detail is a bounded, one-line excerpt of a remote's text for the instance's
// own log (integrations.Error.Detail): control characters are blanked so a
// worker cannot forge a log line, and it is cut at integrations.MaxDetail on a
// rune boundary.
func detail(raw string) string {
	raw = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, raw)
	if len(raw) > integrations.MaxDetail {
		raw = strings.ToValidUTF8(raw[:integrations.MaxDetail], "")
	}
	return raw
}

// failf builds the *integrations.Error every adapter failure is returned as.
func failf(ref integrations.Ref, op string, class integrations.Class, status int, text string, format string, args ...any) error {
	return &integrations.Error{Ref: ref, Op: op, Class: class, Status: status, Detail: detail(text), Err: fmt.Errorf(format, args...)}
}

// workerErrorClass is the class of a per-clip error the worker answered inside a
// 200 body. The worker prefixes each error "<code>: <detail>"
// (scripts/whisper_mlx_server.py ERR_*); the table below is the router-facing
// reading of HEAD's internal/transcribe PathErr* codes:
//
//   - worker_misconfigured, request_abandoned and worker_fault say nothing about
//     the clip: the worker could not do the job, so another endpoint may.
//     ClassTransient fails over and benches this one.
//   - not_found is the one definitive "no such file" verdict.
//   - path_unmapped is a settings problem (the root is not mapped on the
//     worker, or its mount is lost): the request is wrong for this endpoint, and
//     ClassInvalid is returned as it is.
//   - transcribe_timeout, decoder_crashed and decoder_killed are verdicts about
//     the clip's audio (the decoder looped, died or was killed holding it), and
//     ffmpeg and whisper errors, a bad request and a container rejection are
//     answers about the file too: ClassBadOutput, which the router does not
//     fail over (another worker would meet the same clip) and which the caller
//     may count as a strike.
//   - Any other text, or none (an old or foreign worker), is ClassBadOutput: the
//     worker answered and the answer is unusable.
//
// The code's text is kept in Detail for the instance's log; the error string
// carries the code only.
func workerErrorClass(code string) integrations.Class {
	switch code {
	case "worker_misconfigured", "request_abandoned", "worker_fault":
		return integrations.ClassTransient
	case "not_found":
		return integrations.ClassNotFound
	case "path_unmapped":
		return integrations.ClassInvalid
	}
	return integrations.ClassBadOutput
}

// workerErrorCodes are the codes the worker puts before ": " in a per-clip
// error (HEAD's internal/transcribe pathErrCodes, less worker_fault, which only
// HEAD's dispatcher makes and which is recognised here too for a proxy that
// forwards it). A prefix that is not one of these is not a code.
var workerErrorCodes = map[string]bool{
	"path_unmapped": true, "not_found": true, "ffmpeg_error": true, "ffmpeg_timeout": true,
	"bad_request": true, "whisper_error": true, "worker_misconfigured": true,
	"container_rejected": true, "transcribe_timeout": true, "decoder_crashed": true,
	"decoder_killed": true, "request_abandoned": true, "worker_fault": true,
}

// workerError is the failure of one clip the worker reported in a 200 body.
func workerError(ref integrations.Ref, op, text string) error {
	code, _, _ := strings.Cut(text, ": ")
	if !workerErrorCodes[code] {
		code = "unrecognised"
	}
	return failf(ref, op, workerErrorClass(code), 0, text, "the worker could not transcribe the clip (%s)", code)
}

// canceled reports whether the caller's context is done, as the error to return
// when it is: nothing was sent and nothing is known about the endpoint.
func canceled(ctx context.Context, ref integrations.Ref, op string) error {
	if err := ctx.Err(); err != nil {
		return &integrations.Error{Ref: ref, Op: op, Class: integrations.ClassCanceled, Err: err}
	}
	return nil
}
