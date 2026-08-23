// file: internal/scanner/ai_failure.go
// version: 1.1.0
// guid: 8f2c05d1-47ab-4e93-b60f-1d9a7e3c5482
// last-edited: 2026-08-22

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
// (see isPermanentAIFailure below). It still earns its keep for two reasons:
//
//  1. internal/ai.isPermanentAIError's HTTP-429 branch only fires when the
//     provider's own "code" field is exactly "insufficient_quota". The
//     production error this package's test suite is built from carries
//     "type": "insufficient_quota" but "code": "credit_balance_exhausted" --
//     openai-go's Error.Code decodes the "code" field, so that response is
//     NOT covered by the typed 429 check and still needs a text match.
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
func isPermanentAIFailure(err error) bool {
	if err == nil {
		return false
	}
	var permErr *ai.PermanentError
	if errors.As(err, &permErr) {
		return true
	}
	msg := err.Error()
	for _, marker := range permanentAIFailureMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
