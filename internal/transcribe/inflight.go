// file: internal/transcribe/inflight.go
// version: 2.0.0
// guid: 55d73cef-7ffe-4cc4-bc48-434789153386
// last-edited: 2026-09-13

package transcribe

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// ErrSlotWait marks a failure to ACQUIRE a slot, as distinct from a failure of
// the endpoint itself. It is aidispatch.ErrSlotWait (the same value, so
// errors.Is matches either name); see the dispatcher's cooldown handling for
// why the distinction matters.
var ErrSlotWait = aidispatch.ErrSlotWait

// The in-flight registry moved to internal/aidispatch (PLAN PR 1), keyed by
// endpoint ID. These wrappers keep transcribe's URL-keyed call sites and their
// behaviour identical until PR 3 moves Whisper onto the dispatcher proper.
//
// Endpoint.Concurrency is still enforced HERE, at the request, and not in
// allocateJobs, which uses Concurrency only as an allocation weight. The
// asymmetry is preserved exactly: a per-endpoint limit < 1 means 1, while
// whisper_max_in_flight < 1 means no pool-wide cap.

// whisperTotalGroup names the pool-wide cap whisper_max_in_flight governs.
const whisperTotalGroup = "whisper"

// endpointIDForURL is the URL→ID shim. Every slot and cooldown wrapper goes
// through it, so a URL maps to exactly one aidispatch ID.
func endpointIDForURL(url string) string { return aidispatch.LegacyURLID(url) }

// acquireInFlight blocks until this endpoint has a free request slot, or ctx is
// done. It returns a release function that is safe to call more than once.
func acquireInFlight(ctx context.Context, url string, limit int) (func(), error) {
	return aidispatch.DefaultSlots().Acquire(ctx, endpointIDForURL(url), limit, aidispatch.TotalCap{
		Group: whisperTotalGroup,
		Limit: config.AppConfig.WhisperMaxInFlight,
	})
}

// inFlightDepth reports how many slots are currently held for url. Test and
// telemetry helper; returns 0 for an endpoint that has never been dispatched to.
func inFlightDepth(url string) int {
	return aidispatch.DefaultSlots().Depth(endpointIDForURL(url))
}

// inFlightPoolExists reports whether url has ever been given a slot pool.
func inFlightPoolExists(url string) bool {
	return aidispatch.DefaultSlots().HasPool(endpointIDForURL(url))
}

// resetInFlightState clears the process-wide slot registry. Tests only.
func resetInFlightState() { aidispatch.DefaultSlots().Reset() }
