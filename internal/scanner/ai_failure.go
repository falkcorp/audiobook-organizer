// file: internal/scanner/ai_failure.go
// version: 1.0.0
// guid: 8f2c05d1-47ab-4e93-b60f-1d9a7e3c5482
// last-edited: 2026-08-16

package scanner

import "strings"

// permanentAIFailureMarkers are substrings that identify a provider state which
// the next request cannot clear: billing, authentication, or a revoked key.
//
// Matching on strings is not how anyone would choose to do this, and the reason
// it is done here is worth stating: ParseBatch returns an error built by
// fmt.Errorf several layers down, so the HTTP status and the provider's own
// error code are already flattened into text by the time this sees them.
// Threading a typed error up through the AI parser is the right fix and is
// tracked in todo.d/20260816-typed-ai-provider-errors.md; this is deliberately
// the narrow version that reads only codes providers treat as stable API.
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
	// Generic
	"401 Unauthorized",
	"403 Forbidden",
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
func isPermanentAIFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range permanentAIFailureMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
