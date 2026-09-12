// file: internal/scanner/ai_failure.go
// version: 1.4.0
// guid: 8f2c05d1-47ab-4e93-b60f-1d9a7e3c5482
// last-edited: 2026-09-12

package scanner

import (
	"errors"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
)

// permanentAIFailureMarkers are substrings that identify a provider state which
// the next request cannot clear: billing, authentication, or a revoked key.
//
// This is the fallback path, checked only when err is not an *ai.PermanentError
// (see isPermanentAIFailure below). It was justified by two reasons; reason (1)
// was retired on 2026-08-23 and reason (2) is what keeps this path alive:
//
//  1. NO LONGER TRUE as of 2026-08-23 -- kept as a record of what changed.
//     internal/ai.isPermanentAIError's HTTP-429 branch used to fire only when
//     the provider's "code" field was exactly "insufficient_quota", so the
//     production error this package's test suite is built from ("type":
//     "insufficient_quota", "code": "credit_balance_exhausted") was NOT covered
//     by the typed check and reached main only via the text match below. That
//     branch now checks BOTH "type" and "code" against a marker set
//     (internal/ai/retry.go, isPermanentQuota429), so the prod payload is
//     classified typed-first and arrives here already wrapped as
//     *ai.PermanentError. This fallback no longer carries that case alone.
//  2. aiParser can point at any OpenAI-compatible baseURL (Ollama and others),
//     and the SDK request path only returns a structured *openai.Error when
//     the error body parses as the expected {"error": {...}} JSON shape --
//     an endpoint that fails to conform never reaches PermanentError, typed
//     or not, and both paths are just as blind to it. For the cases that DO
//     parse, "invalid_api_key" and "account_deactivated" carry the provider's
//     own stable code, which is worth keeping in case a non-OpenAI endpoint
//     returns that code family under a status this switch doesn't expect.
//
// "401 Unauthorized" and "403 Forbidden" were dropped from this list: they're
// generic HTTP status text, not a provider code, and any *openai.Error that
// would produce that text already carries StatusCode 401/403 -- which
// isPermanentAIError's switch (internal/ai/retry.go) catches unconditionally,
// so DoWithRetry has already wrapped it in *ai.PermanentError by the time it
// gets here.
//
// The text match is only safe on text the PROVIDER wrote. Errors carrying
// model-written text -- *ai.ReplyParseError, whose message embeds an excerpt
// of the model's reply -- are excluded by type before this list is consulted;
// see isPermanentAIFailure. Any new error that quotes model output must be
// typed the same way, or a book title can match a marker below.
//
// A miss here is not dangerous: the phase still stops after
// maxConsecutiveFailures. This only makes the common case stop on the first
// batch instead of the third.
var permanentAIFailureMarkers = []string{
	// OpenAI
	"insufficient_quota",
	"credit_balance_exhausted",
	"invalid_api_key",
	"account_deactivated",
	// Anthropic
	"authentication_error",
	"permission_error",
}

// isPermanentAIFailure reports whether an AI backend error will still be true
// on the next call.
//
// The distinction matters because the retry policy above it assumes failures
// are transient. On 2026-08-16 an exhausted OpenAI balance made all 77 batches
// of a library scan fail identically; each burned 3 attempts with backoff, the
// phase reported no progress throughout, and the watchdog cancelled the scan at
// five minutes -- throwing away a completed 3,917-file walk over a condition
// that was fully knowable from the first response.
//
// internal/ai's DoWithRetry (internal/ai/retry.go) already classifies OpenAI
// API errors via the real openai-go SDK error type and wraps confirmed
// permanent ones in *ai.PermanentError before they leave ParseBatch. Checking
// for that type first reuses that classification instead of re-deriving it
// from text; the marker-substring loop below is the fallback for errors that
// never went through DoWithRetry, or that didn't come back as a structured
// *openai.Error in the first place (see the marker-list comment above).
//
// An *ai.ReplyParseError is never permanent by itself and its text is never
// read. It means the provider answered and the model's reply could not be
// decoded, which is not an auth, billing, or quota state -- and its Error()
// quotes up to 300 bytes of model-written text, so the marker loop would
// otherwise be matching words the model wrote. A filename echoed into a
// malformed reply ("...permission_error..." in a book title) used to abort the
// whole AI phase and, in parserChain.ParseBatch, stop the fallthrough to the
// next backend. A bad reply now counts toward the phase's ordinary failure
// threshold and lets the chain try the next rung.
//
// The reply error does not mask anything next to it, though. The typed
// *ai.PermanentError check walks %w chains and errors.Join trees, so a join of
// a real PermanentError and a reply error is permanent. The text fallback
// (markerTextIsPermanent) walks the same tree and reads only the branches that
// do not contain a reply error.
func isPermanentAIFailure(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := errors.AsType[*ai.PermanentError](err); ok {
		return true
	}
	return markerTextIsPermanent(err)
}

// markerTextIsPermanent applies the permanentAIFailureMarkers substring match
// to err, without ever reading text that came from the model.
//
// It can only keep that promise for errors wrapped with %w. A caller that
// wraps an *ai.ReplyParseError with %v (or %s, or err.Error()) flattens it into
// a plain string: the type is gone, and the excerpt is matched like any
// provider text. Every wrap of a ParseBatch / ParseFilename error must use %w.
func markerTextIsPermanent(err error) bool {
	if _, ok := errors.AsType[*ai.ReplyParseError](err); !ok {
		// No reply error anywhere in the tree: all of this text came from
		// the provider or from our own wrapping, and is matched whole.
		return containsPermanentMarker(err.Error())
	}
	// A reply error is somewhere below, so err's own Error() embeds the
	// excerpt. Never read it; descend and read only branches without one.
	switch e := err.(type) {
	case *ai.ReplyParseError:
		return false
	case interface{ Unwrap() []error }:
		// errors.Join, or fmt.Errorf with several %w: each branch on its own,
		// so a provider marker in one branch still counts next to a reply
		// error in another.
		for _, branch := range e.Unwrap() {
			if branch != nil && markerTextIsPermanent(branch) {
				return true
			}
		}
		return false
	}
	if inner := errors.Unwrap(err); inner != nil {
		return markerTextIsPermanent(inner)
	}
	return false
}

func containsPermanentMarker(msg string) bool {
	for _, marker := range permanentAIFailureMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
