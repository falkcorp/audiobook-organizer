// file: internal/syncapi/progress/policy.go
// version: 1.0.0
// guid: f93d8147-df19-4993-a09b-39f727b522ad
// last-edited: 2026-07-30

// Package progress implements the ABS-sync progress conflict-resolution policy
// from docs/specs/2026-07-29-abs-sync-api-design.md §5, §5b and §1.8.7 as pure,
// I/O-free decision functions.
//
// "Pure" is load-bearing: nothing here imports internal/database, touches HTTP,
// or reads the clock. Callers pass every timestamp in explicitly (see
// NextServerTimestampMs's nowMs parameter), which makes each rule in §5
// deterministically unit-testable ahead of the Phase-6 UserBookState /
// UserPosition store adapter and the handlers that will eventually call it.
// This package is modeled on the sibling pure package internal/audioutil
// (CumulativeOffsets, SynthesizeChapters).
//
// Every rule below exists because a real client silently loses data without it;
// the per-function doc comments cite the spec section and, where the spec cites
// one, the client source line that proves it.
package progress

import (
	"errors"
	"fmt"
	"math"
)

// Progress is the pure (user, item) progress record this package operates on.
// "item" is deliberately opaque here (a raw Book ULID today, an ABS sync_item
// syncID once TASK-01 ships) — this package does not know or care which
// identity scheme the caller uses.
type Progress struct {
	// CurrentTime is the whole-book position in seconds. It is never a
	// percentage, and per spec §1.8.7 it must be the user's true latest
	// position — AudioBooth takes max() on position at session start while
	// ignoring timestamps (SessionManager.swift:175-180), so a session-start
	// snapshot or 0 here silently rewinds the client.
	CurrentTime float64

	// Duration is the book's duration in seconds. Spec §5b: pick ONE
	// authoritative duration per book (sum-of-tracks recommended) and pass it
	// consistently — this package does not compute duration, it only compares
	// against it. It is a required non-pointer float64 specifically so
	// ValidateFinishedDuration can enforce §1.8.7's null-duration invariant
	// before the DTO layer ever serializes a response.
	Duration float64

	// IsFinished is derived by the server on the /sync path and supplied by the
	// client on the PATCH /api/me/progress/:id path — see MergeIncoming versus
	// MergeExplicit.
	IsFinished bool

	// UpdatedAtMs is a millisecond epoch, server-authoritative once stored.
	// Spec §1.7.3 #1: this is the single highest-value field in the whole
	// protocol — omit it and the server permanently loses every conflict,
	// because clients compare it against their own wall clock and ties go to
	// the client.
	UpdatedAtMs int64
}

// FinishedToleranceSec is the §5b tolerance: currentTime within this many
// seconds of duration counts as finished. Measured worst-case
// container/chapter/track-sum skew on a real m4b is ~52ms (9975.480544 vs
// 9975.428000 vs 9975.431111 for the same book — m4b chapter marks are
// millisecond-quantized via time_base 1/1000 while per-track durations are
// frame-accurate); 2s is comfortably above that and far below a meaningful
// amount of unlistened audio. A tighter epsilon means a fully-listened book
// never auto-marks finished and sits at 99% forever.
const FinishedToleranceSec = 2.0

// ErrFinishedWithoutDuration is returned by ValidateFinishedDuration for a
// Progress that claims IsFinished with no positive Duration. Callers should
// match it with errors.Is.
var ErrFinishedWithoutDuration = errors.New("progress marked finished without a positive duration")

// IsWithinFinishedTolerance reports whether currentTime has reached duration
// within FinishedToleranceSec (spec §5b). A non-positive duration is never
// within tolerance: duration is unknown at that point, so claiming "finished"
// would both be a guess and trip §1.8.7's null-duration trap.
func IsWithinFinishedTolerance(currentTime, duration float64) bool {
	if duration <= 0 || math.IsNaN(duration) || math.IsNaN(currentTime) {
		return false
	}
	return currentTime >= duration-FinishedToleranceSec
}

// applyBaseRules implements the two §5 base rules that MergeIncoming and
// MergeExplicit share, and nothing else — it deliberately does NOT decide
// IsFinished, because that is exactly where those two endpoints differ (server-
// derived on /sync, client-supplied on PATCH /api/me/progress/:id). The
// returned Progress carries whatever IsFinished the winning record had; every
// caller must overwrite it.
//
//   - Rule 2, newer wins: incoming.UpdatedAtMs strictly greater -> take
//     incoming wholesale, with one carve-out: Duration is not a position, so a
//     payload that omits it (Duration <= 0) keeps the server's known duration
//     rather than erasing it. Erasing it would disable finished-detection
//     outright (§5b's "sits at 99% forever") and make any later isFinished
//     response trip §1.8.7's null-duration trap, which zeroes the client's
//     position. A positive incoming Duration always wins.
//   - Rule 3, stale device forward-only: otherwise accept only if
//     incoming.CurrentTime > server.CurrentTime. A stale device that listened
//     further while offline still advances; a stale device that is behind can
//     never rewind newer server progress. An equal timestamp counts as "not
//     newer" and therefore lands here.
//
// viaNewerWins reports which branch accepted, so MergeExplicit can restrict
// client-supplied isFinished handling to the strictly-newer branch.
//
// MergeOfflineReplay deliberately does NOT call this helper: its whole point is
// that timestamps are unusable, so it must not share a code path with rules
// that consult them.
func applyBaseRules(server, incoming Progress) (result Progress, accepted, viaNewerWins bool) {
	if incoming.UpdatedAtMs > server.UpdatedAtMs {
		result = incoming
		if result.Duration <= 0 {
			result.Duration = server.Duration
		}
		return result, true, true
	}
	if incoming.CurrentTime > server.CurrentTime {
		result = server
		result.CurrentTime = incoming.CurrentTime
		// "Advance" UpdatedAtMs monotonically. On this branch incoming's
		// timestamp is <= the server's by construction, so taking the max
		// leaves the server's value in place: a stale-but-ahead accept must
		// never rewind the stored timestamp, or the next update from any device
		// would falsely look newer and win a conflict it should lose (spec
		// §1.7.3 #1). The Phase-6 store adapter is expected to re-stamp the
		// write with NextServerTimestampMs anyway.
		if incoming.UpdatedAtMs > result.UpdatedAtMs {
			result.UpdatedAtMs = incoming.UpdatedAtMs
		}
		// Duration is not a position and is never "stale": adopt a positive
		// incoming duration so finished-detection has something to compare
		// against even when the stored record predates duration probing. A
		// zero/absent incoming duration never clobbers a known one.
		if incoming.Duration > 0 {
			result.Duration = incoming.Duration
		}
		return result, true, false
	}
	return server, false, false
}

// MergeIncoming implements spec §5 rules 2-4 for endpoints where the server
// DERIVES isFinished (POST /api/session/:id/sync — clients do not send
// isFinished/progress there, spec §1.8.7's amendment to §5). In order:
//
//  1. Newer wins: if incoming.UpdatedAtMs > server.UpdatedAtMs, accept incoming
//     wholesale, EXCEPT isFinished, which is sticky-OR'd (rule 3 below) rather
//     than overwritten, and EXCEPT an omitted Duration, which never wipes a
//     known one (see applyBaseRules). Note this means a genuinely newer update
//     is honoured even when it seeks backwards — only a STALE update is
//     position-gated.
//  2. Stale device, forward-only: otherwise (incoming not newer, including an
//     equal timestamp) accept ONLY IF incoming.CurrentTime > server.CurrentTime
//     — advance CurrentTime and UpdatedAtMs (monotonically; see
//     applyBaseRules), still sticky-OR isFinished. A stale-and-behind update is
//     rejected outright: accepted=false and result is the server record
//     unchanged, field for field.
//  3. Finished is sticky: result.IsFinished = server.IsFinished ||
//     IsWithinFinishedTolerance(result.CurrentTime, result.Duration). Once true
//     it can only ever become true again here — /sync has no path to clear it,
//     because the client never sends isFinished on this endpoint at all (any
//     value in incoming.IsFinished is ignored). MergeExplicit is the only path
//     that can clear it.
//
// Returns the merged Progress and whether incoming caused any change.
func MergeIncoming(server, incoming Progress) (result Progress, accepted bool) {
	result, accepted, _ = applyBaseRules(server, incoming)
	if !accepted {
		return server, false
	}
	result.IsFinished = server.IsFinished || IsWithinFinishedTolerance(result.CurrentTime, result.Duration)
	return result, true
}

// MergeExplicit implements the PATCH /api/me/progress/:id path, where Absorb
// DOES send isFinished/progress explicitly and the server must honour rather
// than contradict them (spec §1.8.7's amendment to §5). It applies the same
// newer-wins / forward-only base rules as MergeIncoming, but:
//
//   - only when accepted via the "newer wins" branch may incoming.IsFinished
//     override server.IsFinished — including clearing it back to false, i.e.
//     "re-opening" a finished book, which ABS allows per spec §5 rule 4. On that
//     branch the tolerance heuristic is deliberately NOT consulted: the client
//     stated its intent, and second-guessing it is the "contradict" failure the
//     spec forbids;
//   - a strictly-newer isFinished true->false transition additionally resets
//     result.CurrentTime to 0 (mirrors real ABS "mark as not started"). Duration
//     survives the reset, so the response can never trip §1.8.7's
//     null-duration trap.
//
// A forward-only accept via base rule 3 can NEVER carry an explicit isFinished
// — /sync-style forward-only updates do not originate from a client that
// supplies isFinished in this codepath — so that branch keeps MergeIncoming's
// sticky-OR behaviour exactly.
func MergeExplicit(server, incoming Progress) (result Progress, accepted bool) {
	result, accepted, viaNewerWins := applyBaseRules(server, incoming)
	if !accepted {
		return server, false
	}
	if viaNewerWins {
		result.IsFinished = incoming.IsFinished
		if server.IsFinished && !incoming.IsFinished {
			result.CurrentTime = 0
		}
		return result, true
	}
	result.IsFinished = server.IsFinished || IsWithinFinishedTolerance(result.CurrentTime, result.Duration)
	return result, true
}

// MergeOfflineReplay implements the abs-shim-derived guard for
// POST /api/session/local[-all] (spec §1.8.7 last bullet, abs-shim
// src/index.ts:534-541 — a distinct requirement from §1.7.3 #2's "return 200
// for unknown IDs" rule, which is an HTTP-layer concern for a later task, not
// this function's job).
//
// Offline clients re-stamp stale backlog entries with updatedAt = now before
// replaying them, which defeats a timestamp-based comparison outright: a
// backlog entry can look "newer" by timestamp while its position is far behind
// current server state. This guard therefore IGNORES timestamps entirely and is
// forward-only on CurrentTime alone — accept iff
// incoming.CurrentTime > server.CurrentTime.
//
// When accepted, UpdatedAtMs is bumped via NextServerTimestampMs so downstream
// conflict checks (and the client's own truncated-seconds comparison) see a
// fresh write rather than the replayed entry's fictional timestamp; the
// incoming stamp is used only as the "now" input, never as a comparison key.
// isFinished stays server-derived and sticky here, exactly as on /sync: this is
// the same session-report family of endpoints, where clients never send it.
func MergeOfflineReplay(server, incoming Progress) (result Progress, accepted bool) {
	if !(incoming.CurrentTime > server.CurrentTime) {
		return server, false
	}
	result = server
	result.CurrentTime = incoming.CurrentTime
	if incoming.Duration > 0 {
		result.Duration = incoming.Duration
	}
	result.UpdatedAtMs = NextServerTimestampMs(server.UpdatedAtMs, incoming.UpdatedAtMs)
	result.IsFinished = server.IsFinished || IsWithinFinishedTolerance(result.CurrentTime, result.Duration)
	return result, true
}

// MergeCombine implements spec §5 rule 5 / §4.2's merge-follow hook: when two
// items combine (a dedup merge surfacing two previously-independent progress
// records for the same book), the combined record takes max(currentTime),
// OR(isFinished) and max(updatedAt). Duration takes the larger of the two so a
// zero/unknown duration never clobbers a known one.
//
// Unlike the Merge* functions above this is unconditional and commutative: both
// inputs are already "this device's real position", so there is no
// stale/forward-only question to answer.
func MergeCombine(a, b Progress) Progress {
	return Progress{
		CurrentTime: math.Max(a.CurrentTime, b.CurrentTime),
		Duration:    math.Max(a.Duration, b.Duration),
		IsFinished:  a.IsFinished || b.IsFinished,
		UpdatedAtMs: max(a.UpdatedAtMs, b.UpdatedAtMs),
	}
}

// NextServerTimestampMs returns an UpdatedAtMs value for a server-initiated
// write that is guaranteed to beat AudioBooth's tie-break (spec §1.8.7 second
// bullet): AudioBooth compares timestamps with strict `>` after truncating
// BOTH sides via integer `/1000` (whole seconds) in MediaProgress.swift:163,
// so two writes inside the same wall-clock second compare equal and the
// client's cached value wins, silently discarding the server's update.
//
// It returns max(nowMs, prevUpdatedAtMs+1000) whenever nowMs's
// truncated-to-seconds value would not already exceed prevUpdatedAtMs's,
// guaranteeing result/1000 > prevUpdatedAtMs/1000. When nowMs is already a
// whole second ahead it is returned unchanged — no artificial inflation.
//
// Both arguments are millisecond epochs and are expected to be non-negative;
// integer division truncates toward zero, so negative inputs are not
// meaningful here.
func NextServerTimestampMs(prevUpdatedAtMs, nowMs int64) int64 {
	if nowMs/1000 > prevUpdatedAtMs/1000 {
		return nowMs
	}
	// nowMs/1000 <= prevUpdatedAtMs/1000 implies nowMs < prevUpdatedAtMs+1000,
	// so prevUpdatedAtMs+1000 IS max(nowMs, prevUpdatedAtMs+1000).
	return prevUpdatedAtMs + 1000
}

// AddListenedDelta implements POST /api/session/{id}/sync's "timeListened"
// (past tense) semantics (spec §1.8.4, SessionService.swift:131-134 /
// SessionManager.swift:351): a DELTA the server must ADD to the running total.
//
// Non-positive deltas (a malformed or clock-skewed client) are clamped to 0
// rather than subtracted, so a buggy client can never rewind total listened
// time. NaN and infinities are discarded for the same reason — one poisoned
// payload would otherwise make the stored total permanently unusable.
func AddListenedDelta(runningTotalSec, deltaSec float64) float64 {
	if math.IsNaN(deltaSec) || math.IsInf(deltaSec, 0) || deltaSec <= 0 {
		return runningTotalSec
	}
	return runningTotalSec + deltaSec
}

// SetListenedCumulative implements POST /api/session/local[-all]'s
// "timeListening" (gerund) semantics (spec §1.8.4, SessionSync.swift:11): a
// CUMULATIVE total (idempotent set), NEVER additive — reading it as a delta is
// the exact abs-shim bug the spec calls out (src/index.ts:336 reads
// "timeListening" on /sync and therefore records zero listening time from both
// clients).
//
// Forward-only: returns max(runningTotalSec, cumulativeSec) so a stale replayed
// session (see MergeOfflineReplay, which the same request body drives) cannot
// rewind a total a newer session already advanced past. NaN and infinities are
// discarded.
func SetListenedCumulative(runningTotalSec, cumulativeSec float64) float64 {
	if math.IsNaN(cumulativeSec) || math.IsInf(cumulativeSec, 0) {
		return runningTotalSec
	}
	if cumulativeSec > runningTotalSec {
		return cumulativeSec
	}
	return runningTotalSec
}

// ValidateFinishedDuration enforces the invariant behind spec §1.8.7's
// null-duration trap ("isFinished: true with a null duration sets the client's
// currentTime to 0", MediaProgress.swift:137-140): any Progress about to be
// serialized with IsFinished true must carry a positive Duration. Returns an
// error wrapping ErrFinishedWithoutDuration otherwise.
//
// This is a guard for the future Phase-6 DTO layer to call before writing a
// response body — it is deliberately not enforced inside the Merge* functions,
// which must stay total (they always return a usable result) and which cannot
// invent a duration the caller never supplied.
func ValidateFinishedDuration(p Progress) error {
	if p.IsFinished && !(p.Duration > 0) {
		return fmt.Errorf("%w (duration=%v, currentTime=%v)", ErrFinishedWithoutDuration, p.Duration, p.CurrentTime)
	}
	return nil
}
