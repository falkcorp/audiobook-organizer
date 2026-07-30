// file: internal/syncapi/progress/policy_test.go
// version: 1.0.0
// guid: 74b0ad2a-9045-4293-b79b-886bf28d0428
// last-edited: 2026-07-30

package progress

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// The four decision paths, written out before any test was written (TASK-08
// step 2). Each endpoint family gets its OWN merge function; the rules below
// must never be collapsed into a shared code path, because "sticky finished"
// (spec §5 rule 4) and the offline-replay guard (spec §1.8.7 last bullet) are
// two different mechanisms that happen to both sound like "don't go
// backwards".
//
//  1. POST /api/session/:id/sync                        -> MergeIncoming
//     Clients do NOT send isFinished/progress here (spec §1.8.7 amendment to
//     §5): the SERVER derives isFinished. Governed by §5 rules 2+3 (newer
//     wins, else forward-only on currentTime) and §5 rule 4 as a one-way
//     sticky-OR — this path has no way to express "un-finish".
//
//  2. PATCH /api/me/progress/:id                        -> MergeExplicit
//     Absorb DOES send isFinished/progress, and spec §1.8.7 says the server
//     must honour rather than contradict them. Same §5 rule 2/3 base, but the
//     newer-wins branch lets incoming.IsFinished win outright — including
//     clearing it (§5 rule 4's "ABS allows re-opening a finished book"),
//     which additionally resets CurrentTime to 0. The forward-only branch is
//     identical to MergeIncoming: it cannot carry an explicit isFinished.
//
//  3. POST /api/session/local[-all]                     -> MergeOfflineReplay
//     Timestamps are UNUSABLE here: offline clients re-stamp stale backlog
//     entries with updatedAt = now before replaying, so a far-behind position
//     can arrive looking "newer" (spec §1.8.7 last bullet, abs-shim
//     src/index.ts:534-541). Therefore this path ignores timestamps entirely
//     and is forward-only on CurrentTime alone. Note this is a DIFFERENT
//     requirement from §1.7.3 #2's "200 for unknown IDs", which is an
//     HTTP-layer concern and not this package's job.
//
//  4. Dedup merge-follow (§4.2 / §5 rule 5)             -> MergeCombine
//     Two previously-independent progress records for what turned out to be
//     one book. Both are real positions from real devices, so there is no
//     stale/forward-only question at all: unconditional max(currentTime),
//     OR(isFinished), max(updatedAt).
//
// ---------------------------------------------------------------------------

const epsilon = 1e-9

func floatsClose(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

// Real measured durations for the Odyssey fixture (spec §5b table). These
// three values all legitimately describe the SAME book and disagree by ~52ms.
const (
	odysseyContainerDuration = 9975.480544 // m4b container duration
	odysseyLastChapterEnd    = 9975.428000 // m4b last chapter end
	odysseyTrackSum          = 9975.431111 // sum of the 6 mp3 track durations
)

// -------------------------- IsWithinFinishedTolerance ----------------------

func TestIsWithinFinishedTolerance(t *testing.T) {
	tests := []struct {
		name        string
		currentTime float64
		duration    float64
		want        bool
	}{
		{"exactly at duration", 100, 100, true},
		{"past duration", 101, 100, true},
		{"one second short is within tolerance", 99, 100, true},
		{"two seconds short is exactly at the tolerance edge", 98, 100, true},
		{"three seconds short is outside tolerance", 97, 100, false},
		{"start of book", 0, 100, false},
		{"zero duration is never finished", 0, 0, false},
		{"negative duration is never finished", 5, -1, false},
		{"m4b last-chapter-end vs container duration (§5b, ~52ms skew)", odysseyLastChapterEnd, odysseyContainerDuration, true},
		{"mp3 track-sum vs container duration (§5b)", odysseyTrackSum, odysseyContainerDuration, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWithinFinishedTolerance(tt.currentTime, tt.duration); got != tt.want {
				t.Fatalf("IsWithinFinishedTolerance(%v, %v) = %v, want %v",
					tt.currentTime, tt.duration, got, tt.want)
			}
		})
	}
}

func TestFinishedToleranceSec_IsAtLeastTwoSeconds(t *testing.T) {
	// Spec §5b: "Use an explicit tolerance of >= 2 s". A tighter epsilon makes
	// a fully-listened book sit at 99% forever.
	if FinishedToleranceSec < 2.0 {
		t.Fatalf("FinishedToleranceSec = %v, want >= 2.0 (spec §5b)", FinishedToleranceSec)
	}
}

// --------------------------- NextServerTimestampMs -------------------------

// TestNextServerTimestampMs_TieCase is the exact case spec §1.8.7 calls out:
// AudioBooth compares lastUpdate with strict `>` AFTER truncating both sides
// with integer /1000, so two writes inside the same wall-clock second compare
// equal and the client's cached value wins — silently discarding the server's
// update.
func TestNextServerTimestampMs_TieCase(t *testing.T) {
	const prev = int64(1000)
	const now = int64(1500)
	got := NextServerTimestampMs(prev, now)
	if got/1000 <= prev/1000 {
		t.Fatalf("NextServerTimestampMs(%d, %d) = %d; %d/1000 = %d must be > %d/1000 = %d",
			prev, now, got, got, got/1000, prev, prev/1000)
	}
	if got < 2000 {
		t.Fatalf("NextServerTimestampMs(%d, %d) = %d, want >= 2000", prev, now, got)
	}
}

func TestNextServerTimestampMs_NowAlreadyAheadIsUsedAsIs(t *testing.T) {
	const prev = int64(1000)
	const now = int64(2500) // already a full second-plus ahead
	if got := NextServerTimestampMs(prev, now); got != now {
		t.Fatalf("NextServerTimestampMs(%d, %d) = %d, want %d (no artificial inflation)",
			prev, now, got, now)
	}
}

func TestNextServerTimestampMs_Table(t *testing.T) {
	tests := []struct {
		name string
		prev int64
		now  int64
		want int64
	}{
		{"same second, now ahead in ms", 1000, 1500, 2000},
		{"identical values", 1000, 1000, 2000},
		{"same second, now behind in ms", 1900, 1100, 2900},
		{"now a clock-skewed full second behind", 10000, 5000, 11000},
		{"now already in the next second", 1000, 2000, 2000},
		{"now far ahead", 1000, 999000, 999000},
		{"no previous write", 0, 1500, 1500},
		{"no previous write, same second as epoch", 0, 500, 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextServerTimestampMs(tt.prev, tt.now)
			if got != tt.want {
				t.Fatalf("NextServerTimestampMs(%d, %d) = %d, want %d", tt.prev, tt.now, got, tt.want)
			}
			// The whole point of the function: truncated-to-seconds must beat prev.
			if got/1000 <= tt.prev/1000 {
				t.Fatalf("NextServerTimestampMs(%d, %d) = %d: %d/1000 does not exceed %d/1000",
					tt.prev, tt.now, got, got, tt.prev)
			}
		})
	}
}

// ------------------------------ AddListenedDelta ---------------------------

// TestAddListenedDelta_Adds pins the /sync semantics: "timeListened" (past
// tense) is a DELTA the server ADDS to the running total (spec §1.8.4).
func TestAddListenedDelta_Adds(t *testing.T) {
	tests := []struct {
		name    string
		running float64
		delta   float64
		want    float64
	}{
		{"first delta", 0, 15, 15},
		{"second delta accumulates", 15, 15, 30},
		{"fractional delta", 30, 0.5, 30.5},
		{"zero delta is a no-op", 30, 0, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AddListenedDelta(tt.running, tt.delta); !floatsClose(got, tt.want) {
				t.Fatalf("AddListenedDelta(%v, %v) = %v, want %v", tt.running, tt.delta, got, tt.want)
			}
		})
	}
}

func TestAddListenedDelta_ClampsNegative(t *testing.T) {
	tests := []struct {
		name    string
		running float64
		delta   float64
		want    float64
	}{
		{"negative delta must not subtract", 100, -50, 100},
		{"tiny negative delta", 100, -0.001, 100},
		{"NaN delta is ignored", 100, math.NaN(), 100},
		{"+Inf delta is ignored", 100, math.Inf(1), 100},
		{"-Inf delta is ignored", 100, math.Inf(-1), 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AddListenedDelta(tt.running, tt.delta); !floatsClose(got, tt.want) {
				t.Fatalf("AddListenedDelta(%v, %v) = %v, want %v", tt.running, tt.delta, got, tt.want)
			}
		})
	}
}

// --------------------------- SetListenedCumulative -------------------------

// TestSetListenedCumulative_SetsNotAdds proves the /session/local[-all]
// "timeListening" (gerund) value is a CUMULATIVE total, not a delta — the
// exact abs-shim bug in spec §1.8.4 (src/index.ts:336 reads the wrong key and
// records zero listening time from both clients). Calling it twice with the
// same cumulative value must be idempotent.
func TestSetListenedCumulative_SetsNotAdds(t *testing.T) {
	const cumulative = 600.0
	first := SetListenedCumulative(0, cumulative)
	if !floatsClose(first, cumulative) {
		t.Fatalf("SetListenedCumulative(0, %v) = %v, want %v", cumulative, first, cumulative)
	}
	second := SetListenedCumulative(first, cumulative)
	if !floatsClose(second, cumulative) {
		t.Fatalf("SetListenedCumulative(%v, %v) = %v, want %v (must be idempotent, not additive)",
			first, cumulative, second, cumulative)
	}
	third := SetListenedCumulative(second, cumulative+30)
	if !floatsClose(third, cumulative+30) {
		t.Fatalf("SetListenedCumulative(%v, %v) = %v, want %v",
			second, cumulative+30, third, cumulative+30)
	}
}

func TestSetListenedCumulative_ForwardOnly(t *testing.T) {
	tests := []struct {
		name       string
		running    float64
		cumulative float64
		want       float64
	}{
		{"stale replayed session cannot rewind the total", 600, 120, 600},
		{"equal is a no-op", 600, 600, 600},
		{"newer cumulative advances", 600, 900, 900},
		{"negative cumulative cannot rewind", 600, -5, 600},
		{"NaN cumulative is ignored", 600, math.NaN(), 600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SetListenedCumulative(tt.running, tt.cumulative)
			if !floatsClose(got, tt.want) {
				t.Fatalf("SetListenedCumulative(%v, %v) = %v, want %v",
					tt.running, tt.cumulative, got, tt.want)
			}
		})
	}
}

// -------------------------------- MergeIncoming ----------------------------
//
// Path 1: POST /api/session/:id/sync. isFinished is SERVER-derived here.

// baseServer is the "server already has real state" fixture the MergeIncoming
// tests share: mid-book, not finished, written at ms 5000.
func baseServer() Progress {
	return Progress{CurrentTime: 200, Duration: 10000, IsFinished: false, UpdatedAtMs: 5000}
}

func TestMergeIncoming_NewerWins(t *testing.T) {
	server := baseServer()
	incoming := Progress{CurrentTime: 4321.5, Duration: 10000, IsFinished: false, UpdatedAtMs: 6000}

	got, accepted := MergeIncoming(server, incoming)
	if !accepted {
		t.Fatalf("MergeIncoming(%+v, %+v) accepted = false, want true", server, incoming)
	}
	if !floatsClose(got.CurrentTime, incoming.CurrentTime) {
		t.Errorf("CurrentTime = %v, want %v (incoming wins wholesale)", got.CurrentTime, incoming.CurrentTime)
	}
	if !floatsClose(got.Duration, incoming.Duration) {
		t.Errorf("Duration = %v, want %v", got.Duration, incoming.Duration)
	}
	if got.UpdatedAtMs != incoming.UpdatedAtMs {
		t.Errorf("UpdatedAtMs = %d, want %d", got.UpdatedAtMs, incoming.UpdatedAtMs)
	}
	if got.IsFinished {
		t.Errorf("IsFinished = true, want false (position is nowhere near duration)")
	}
}

// TestMergeIncoming_StaleForwardAdvances is spec §5 rule 3's exact example: an
// offline device that listened FURTHER still advances the position, even though
// its timestamp is older than the server's.
func TestMergeIncoming_StaleForwardAdvances(t *testing.T) {
	server := baseServer()
	incoming := Progress{CurrentTime: 300, Duration: 10000, IsFinished: false, UpdatedAtMs: 4000}

	got, accepted := MergeIncoming(server, incoming)
	if !accepted {
		t.Fatalf("MergeIncoming(%+v, %+v) accepted = false, want true (stale but ahead)", server, incoming)
	}
	if !floatsClose(got.CurrentTime, 300) {
		t.Errorf("CurrentTime = %v, want 300 (position advances)", got.CurrentTime)
	}
	if got.UpdatedAtMs < server.UpdatedAtMs {
		t.Errorf("UpdatedAtMs = %d, want >= %d (a stale accept must never rewind the server timestamp)",
			got.UpdatedAtMs, server.UpdatedAtMs)
	}
}

// TestMergeIncoming_StaleBehindRejected is THE specific clobber spec §5 rule 3
// fears: a stale device that is BEHIND can never rewind newer server progress.
// Asserted field-for-field, not just on the accepted flag.
func TestMergeIncoming_StaleBehindRejected(t *testing.T) {
	server := baseServer()
	incoming := Progress{CurrentTime: 100, Duration: 10000, IsFinished: false, UpdatedAtMs: 4000}

	got, accepted := MergeIncoming(server, incoming)
	if accepted {
		t.Fatalf("MergeIncoming(%+v, %+v) accepted = true, want false", server, incoming)
	}
	if !reflect.DeepEqual(got, server) {
		t.Fatalf("MergeIncoming returned %+v, want the server record untouched: %+v", got, server)
	}
}

// TestMergeIncoming_StaleDeviceMatrix is the property-style matrix over
// {incoming older, equal, newer timestamp} x {incoming ahead, behind, equal
// position}. An equal timestamp counts as "not newer" and therefore falls to
// the forward-only branch (spec §5 rules 2-3).
func TestMergeIncoming_StaleDeviceMatrix(t *testing.T) {
	const (
		olderTS = int64(4000)
		equalTS = int64(5000) // same as baseServer().UpdatedAtMs
		newerTS = int64(6000)

		behindPos = 100.0
		equalPos  = 200.0 // same as baseServer().CurrentTime
		aheadPos  = 300.0
	)
	tests := []struct {
		name            string
		incomingTS      int64
		incomingPos     float64
		wantAccepted    bool
		wantCurrentTime float64
		wantUpdatedAtMs int64
	}{
		{"newer + ahead -> newer wins", newerTS, aheadPos, true, aheadPos, newerTS},
		// A genuinely newer write that seeked BACKWARDS is honoured: spec §5
		// rule 2 accepts a newer update wholesale. Only STALE-and-behind is a
		// clobber.
		{"newer + behind -> newer wins wholesale (deliberate seek back)", newerTS, behindPos, true, behindPos, newerTS},
		{"newer + equal position -> accepted, timestamp advances", newerTS, equalPos, true, equalPos, newerTS},

		{"equal timestamp + ahead -> forward-only accept", equalTS, aheadPos, true, aheadPos, equalTS},
		{"equal timestamp + behind -> rejected", equalTS, behindPos, false, equalPos, equalTS},
		{"equal timestamp + equal position -> rejected (no-op)", equalTS, equalPos, false, equalPos, equalTS},

		{"older + ahead -> forward-only accept (listened further offline)", olderTS, aheadPos, true, aheadPos, equalTS},
		{"older + behind -> rejected (the clobber §5 rule 3 fears)", olderTS, behindPos, false, equalPos, equalTS},
		{"older + equal position -> rejected", olderTS, equalPos, false, equalPos, equalTS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := baseServer()
			incoming := Progress{
				CurrentTime: tt.incomingPos,
				Duration:    10000,
				UpdatedAtMs: tt.incomingTS,
			}
			got, accepted := MergeIncoming(server, incoming)
			if accepted != tt.wantAccepted {
				t.Fatalf("MergeIncoming(%+v, %+v) accepted = %v, want %v",
					server, incoming, accepted, tt.wantAccepted)
			}
			if !floatsClose(got.CurrentTime, tt.wantCurrentTime) {
				t.Errorf("CurrentTime = %v, want %v", got.CurrentTime, tt.wantCurrentTime)
			}
			if got.UpdatedAtMs != tt.wantUpdatedAtMs {
				t.Errorf("UpdatedAtMs = %d, want %d", got.UpdatedAtMs, tt.wantUpdatedAtMs)
			}
			if !tt.wantAccepted && !reflect.DeepEqual(got, server) {
				t.Errorf("rejected merge returned %+v, want the server record untouched: %+v", got, server)
			}
		})
	}
}

// TestMergeIncoming_FinishedIsSticky covers spec §5 rule 4 on the /sync path:
// /sync cannot express "un-finish" (clients do not send isFinished at all
// there), so once the server has recorded finished, a newer /sync update with a
// position back near the start must NOT clear it.
func TestMergeIncoming_FinishedIsSticky(t *testing.T) {
	server := Progress{CurrentTime: 9975.43, Duration: odysseyContainerDuration, IsFinished: true, UpdatedAtMs: 5000}
	incoming := Progress{CurrentTime: 30, Duration: odysseyContainerDuration, UpdatedAtMs: 6000}

	got, accepted := MergeIncoming(server, incoming)
	if !accepted {
		t.Fatalf("MergeIncoming accepted = false, want true (incoming is strictly newer)")
	}
	if !got.IsFinished {
		t.Errorf("IsFinished = false, want true (sticky: /sync has no un-finish path)")
	}
	if !floatsClose(got.CurrentTime, 30) {
		t.Errorf("CurrentTime = %v, want 30 (the newer position is still accepted)", got.CurrentTime)
	}
}

// TestMergeIncoming_IgnoresClientSuppliedFinished pins the §1.8.7 amendment:
// on /sync the server DERIVES isFinished, so a stray client-supplied
// IsFinished=true well short of the end must not be honoured on this path
// (MergeExplicit is the only path that honours an explicit value).
func TestMergeIncoming_IgnoresClientSuppliedFinished(t *testing.T) {
	server := baseServer()
	incoming := Progress{CurrentTime: 300, Duration: 10000, IsFinished: true, UpdatedAtMs: 6000}

	got, _ := MergeIncoming(server, incoming)
	if got.IsFinished {
		t.Errorf("IsFinished = true, want false (server derives isFinished on /sync)")
	}
}

// TestMergeIncoming_FinishedDetection_LastChapterEnd is the spec §5b regression
// test: a client that plays to the end of the final CHAPTER stops ~52ms short
// of the CONTAINER duration. With a tight epsilon the book would sit at 99%
// forever; with the >=2s tolerance it auto-marks finished.
func TestMergeIncoming_FinishedDetection_LastChapterEnd(t *testing.T) {
	if !IsWithinFinishedTolerance(odysseyLastChapterEnd, odysseyContainerDuration) {
		t.Fatalf("IsWithinFinishedTolerance(%v, %v) = false, want true (spec §5b, ~52ms skew)",
			odysseyLastChapterEnd, odysseyContainerDuration)
	}

	server := Progress{CurrentTime: 9000, Duration: odysseyContainerDuration, IsFinished: false, UpdatedAtMs: 5000}
	incoming := Progress{CurrentTime: odysseyLastChapterEnd, Duration: odysseyContainerDuration, UpdatedAtMs: 6000}

	got, accepted := MergeIncoming(server, incoming)
	if !accepted {
		t.Fatalf("MergeIncoming accepted = false, want true")
	}
	if !got.IsFinished {
		t.Fatalf("IsFinished = false at last-chapter-end position %v of duration %v; "+
			"want true (this is the 'stuck at 99%% forever' bug)",
			odysseyLastChapterEnd, odysseyContainerDuration)
	}
	if err := ValidateFinishedDuration(got); err != nil {
		t.Errorf("ValidateFinishedDuration(%+v) = %v, want nil", got, err)
	}
}

// -------------------------- ValidateFinishedDuration -----------------------

// TestValidateFinishedDuration_RejectsZeroDurationWhenFinished guards spec
// §1.8.7's null-duration trap: "isFinished: true with a null duration sets the
// client's currentTime to 0" (MediaProgress.swift:137-140).
func TestValidateFinishedDuration_RejectsZeroDurationWhenFinished(t *testing.T) {
	tests := []struct {
		name string
		p    Progress
	}{
		{"finished with zero duration", Progress{CurrentTime: 100, Duration: 0, IsFinished: true}},
		{"finished with negative duration", Progress{CurrentTime: 100, Duration: -1, IsFinished: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFinishedDuration(tt.p)
			if err == nil {
				t.Fatalf("ValidateFinishedDuration(%+v) = nil, want an error", tt.p)
			}
			if !errors.Is(err, ErrFinishedWithoutDuration) {
				t.Fatalf("ValidateFinishedDuration(%+v) error = %v, want errors.Is ErrFinishedWithoutDuration",
					tt.p, err)
			}
		})
	}
}

func TestValidateFinishedDuration_AllowsUnfinishedZeroDuration(t *testing.T) {
	tests := []struct {
		name string
		p    Progress
	}{
		{"unfinished with zero duration", Progress{CurrentTime: 0, Duration: 0, IsFinished: false}},
		{"unfinished with real duration", Progress{CurrentTime: 10, Duration: 100, IsFinished: false}},
		{"finished with real duration", Progress{CurrentTime: 100, Duration: 100, IsFinished: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateFinishedDuration(tt.p); err != nil {
				t.Fatalf("ValidateFinishedDuration(%+v) = %v, want nil", tt.p, err)
			}
		})
	}
}

// -------------------------------- MergeExplicit ----------------------------
//
// Path 2: PATCH /api/me/progress/:id. isFinished is CLIENT-supplied here and
// spec §1.8.7 says the server must honour rather than contradict it.

// TestMergeExplicit_CanClearFinished covers §5 rule 4's escape hatch: ABS allows
// re-opening a finished book, and a strictly-newer explicit isFinished:false
// additionally resets the position to 0 (real ABS "mark as not started").
func TestMergeExplicit_CanClearFinished(t *testing.T) {
	server := Progress{CurrentTime: odysseyLastChapterEnd, Duration: odysseyContainerDuration, IsFinished: true, UpdatedAtMs: 5000}
	incoming := Progress{CurrentTime: odysseyLastChapterEnd, Duration: odysseyContainerDuration, IsFinished: false, UpdatedAtMs: 6000}

	got, accepted := MergeExplicit(server, incoming)
	if !accepted {
		t.Fatalf("MergeExplicit accepted = false, want true (incoming is strictly newer)")
	}
	if got.IsFinished {
		t.Errorf("IsFinished = true, want false (explicit re-open, spec §5 rule 4)")
	}
	if !floatsClose(got.CurrentTime, 0) {
		t.Errorf("CurrentTime = %v, want 0 (a finished->unfinished transition resets the position)", got.CurrentTime)
	}
	if !floatsClose(got.Duration, odysseyContainerDuration) {
		t.Errorf("Duration = %v, want %v (duration must survive the reset, spec §1.8.7)",
			got.Duration, odysseyContainerDuration)
	}
}

// TestMergeExplicit_HonoursExplicitFinishedTrue: the client says finished at a
// position nowhere near the end (e.g. the user tapped "mark as finished"); the
// server must honour that, not contradict it with its own tolerance math.
func TestMergeExplicit_HonoursExplicitFinishedTrue(t *testing.T) {
	server := baseServer()
	incoming := Progress{CurrentTime: 300, Duration: 10000, IsFinished: true, UpdatedAtMs: 6000}

	got, accepted := MergeExplicit(server, incoming)
	if !accepted {
		t.Fatalf("MergeExplicit accepted = false, want true")
	}
	if !got.IsFinished {
		t.Errorf("IsFinished = false, want true (server must honour an explicit isFinished)")
	}
	if !floatsClose(got.CurrentTime, 300) {
		t.Errorf("CurrentTime = %v, want 300 (no reset on a false->true transition)", got.CurrentTime)
	}
	if err := ValidateFinishedDuration(got); err != nil {
		t.Errorf("ValidateFinishedDuration(%+v) = %v, want nil", got, err)
	}
}

// TestMergeExplicit_ForwardOnlyBranchCannotClearFinished: a forward-only accept
// (rule 3) can never carry an explicit isFinished, so the sticky-OR applies
// there exactly as it does on /sync. Only the strictly-newer branch may clear.
func TestMergeExplicit_ForwardOnlyBranchCannotClearFinished(t *testing.T) {
	server := Progress{CurrentTime: 200, Duration: 10000, IsFinished: true, UpdatedAtMs: 5000}
	incoming := Progress{CurrentTime: 300, Duration: 10000, IsFinished: false, UpdatedAtMs: 4000}

	got, accepted := MergeExplicit(server, incoming)
	if !accepted {
		t.Fatalf("MergeExplicit accepted = false, want true (stale but ahead)")
	}
	if !got.IsFinished {
		t.Errorf("IsFinished = false, want true (a stale update may not clear finished)")
	}
	if !floatsClose(got.CurrentTime, 300) {
		t.Errorf("CurrentTime = %v, want 300", got.CurrentTime)
	}
}

func TestMergeExplicit_StaleBehindRejected(t *testing.T) {
	server := Progress{CurrentTime: 200, Duration: 10000, IsFinished: true, UpdatedAtMs: 5000}
	tests := []struct {
		name     string
		incoming Progress
	}{
		{"older and behind", Progress{CurrentTime: 100, Duration: 10000, UpdatedAtMs: 4000}},
		{"older and equal", Progress{CurrentTime: 200, Duration: 10000, UpdatedAtMs: 4000}},
		{"equal timestamp and behind", Progress{CurrentTime: 100, Duration: 10000, UpdatedAtMs: 5000}},
		{"stale un-finish attempt", Progress{CurrentTime: 0, Duration: 10000, IsFinished: false, UpdatedAtMs: 4000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, accepted := MergeExplicit(server, tt.incoming)
			if accepted {
				t.Fatalf("MergeExplicit(%+v, %+v) accepted = true, want false", server, tt.incoming)
			}
			if !reflect.DeepEqual(got, server) {
				t.Fatalf("MergeExplicit returned %+v, want the server record untouched: %+v", got, server)
			}
		})
	}
}

// ----------------------------- MergeOfflineReplay --------------------------
//
// Path 3: POST /api/session/local[-all]. Timestamps are UNUSABLE here.

// TestMergeOfflineReplay_IgnoresReStampedTimestamp is the case that would
// falsely WIN under MergeIncoming's rules: an offline client re-stamps a stale
// backlog entry with updatedAt = now, so it looks newer by timestamp while its
// position is far behind the server's. Proof that this function consults no
// timestamps at all (spec §1.8.7 last bullet, abs-shim src/index.ts:534-541).
func TestMergeOfflineReplay_IgnoresReStampedTimestamp(t *testing.T) {
	server := baseServer()
	incoming := Progress{
		CurrentTime: 100,               // far BEHIND the server's 200
		Duration:    10000,             //
		UpdatedAtMs: 9_999_999_999_999, // re-stamped "now", absurdly newer
	}

	got, accepted := MergeOfflineReplay(server, incoming)
	if accepted {
		t.Fatalf("MergeOfflineReplay(%+v, %+v) accepted = true, want false", server, incoming)
	}
	if !reflect.DeepEqual(got, server) {
		t.Fatalf("MergeOfflineReplay returned %+v, want the server record untouched: %+v", got, server)
	}

	// Contrast: the very same inputs WOULD be accepted by MergeIncoming, which
	// is exactly why these two rules must not share a code path.
	if _, wouldAccept := MergeIncoming(server, incoming); !wouldAccept {
		t.Fatalf("precondition failed: MergeIncoming should accept the re-stamped entry " +
			"(if it does not, this test no longer proves anything)")
	}
}

func TestMergeOfflineReplay_AdvancesWhenAhead(t *testing.T) {
	tests := []struct {
		name        string
		incomingTS  int64
		incomingPos float64
		wantAccept  bool
	}{
		{"ahead with an older timestamp", 1000, 300, true},
		{"ahead with an equal timestamp", 5000, 300, true},
		{"ahead with a re-stamped future timestamp", 9_999_999_999_999, 300, true},
		{"behind with an older timestamp", 1000, 100, false},
		{"equal position is a no-op", 5000, 200, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := baseServer()
			incoming := Progress{CurrentTime: tt.incomingPos, Duration: 10000, UpdatedAtMs: tt.incomingTS}
			got, accepted := MergeOfflineReplay(server, incoming)
			if accepted != tt.wantAccept {
				t.Fatalf("MergeOfflineReplay(%+v, %+v) accepted = %v, want %v",
					server, incoming, accepted, tt.wantAccept)
			}
			if !tt.wantAccept {
				if !reflect.DeepEqual(got, server) {
					t.Fatalf("rejected merge returned %+v, want %+v", got, server)
				}
				return
			}
			if !floatsClose(got.CurrentTime, tt.incomingPos) {
				t.Errorf("CurrentTime = %v, want %v", got.CurrentTime, tt.incomingPos)
			}
			// An accepted replay must look like a FRESH server write, or the
			// next client comparison discards it (spec §1.8.7 tie-break).
			if got.UpdatedAtMs/1000 <= server.UpdatedAtMs/1000 {
				t.Errorf("UpdatedAtMs = %d; %d/1000 must exceed %d/1000 after an accepted replay",
					got.UpdatedAtMs, got.UpdatedAtMs, server.UpdatedAtMs)
			}
		})
	}
}

// TestMergeOfflineReplay_DerivesFinished: /session/local is a /sync-family
// endpoint, so isFinished stays server-derived and sticky here too.
func TestMergeOfflineReplay_DerivesFinished(t *testing.T) {
	server := Progress{CurrentTime: 9000, Duration: odysseyContainerDuration, IsFinished: false, UpdatedAtMs: 5000}
	incoming := Progress{CurrentTime: odysseyLastChapterEnd, Duration: odysseyContainerDuration, UpdatedAtMs: 1000}

	got, accepted := MergeOfflineReplay(server, incoming)
	if !accepted {
		t.Fatalf("MergeOfflineReplay accepted = false, want true")
	}
	if !got.IsFinished {
		t.Errorf("IsFinished = false, want true (last-chapter-end within §5b tolerance)")
	}

	// Sticky: a replayed entry cannot clear a finished flag either.
	finishedServer := Progress{CurrentTime: 200, Duration: 10000, IsFinished: true, UpdatedAtMs: 5000}
	ahead := Progress{CurrentTime: 300, Duration: 10000, IsFinished: false, UpdatedAtMs: 6000}
	if got, _ := MergeOfflineReplay(finishedServer, ahead); !got.IsFinished {
		t.Errorf("IsFinished = false, want true (sticky on the replay path)")
	}
}

// -------------------------------- MergeCombine -----------------------------
//
// Path 4: dedup merge-follow (spec §4.2 / §5 rule 5). Unconditional.

func TestMergeCombine_TakesMaxAndOr(t *testing.T) {
	tests := []struct {
		name string
		a    Progress
		b    Progress
		want Progress
	}{
		{
			name: "a is ahead, b is finished and newer",
			a:    Progress{CurrentTime: 5000, Duration: 10000, IsFinished: false, UpdatedAtMs: 7000},
			b:    Progress{CurrentTime: 300, Duration: 10000, IsFinished: true, UpdatedAtMs: 9000},
			want: Progress{CurrentTime: 5000, Duration: 10000, IsFinished: true, UpdatedAtMs: 9000},
		},
		{
			name: "b is ahead and newer, a is finished",
			a:    Progress{CurrentTime: 100, Duration: 10000, IsFinished: true, UpdatedAtMs: 1000},
			b:    Progress{CurrentTime: 800, Duration: 10000, IsFinished: false, UpdatedAtMs: 4000},
			want: Progress{CurrentTime: 800, Duration: 10000, IsFinished: true, UpdatedAtMs: 4000},
		},
		{
			name: "neither finished",
			a:    Progress{CurrentTime: 100, Duration: 10000, UpdatedAtMs: 1000},
			b:    Progress{CurrentTime: 200, Duration: 10000, UpdatedAtMs: 2000},
			want: Progress{CurrentTime: 200, Duration: 10000, UpdatedAtMs: 2000},
		},
		{
			name: "a has no known duration",
			a:    Progress{CurrentTime: 900, Duration: 0, UpdatedAtMs: 3000},
			b:    Progress{CurrentTime: 100, Duration: 10000, UpdatedAtMs: 1000},
			want: Progress{CurrentTime: 900, Duration: 10000, UpdatedAtMs: 3000},
		},
		{
			name: "identical records",
			a:    Progress{CurrentTime: 42, Duration: 100, IsFinished: false, UpdatedAtMs: 5},
			b:    Progress{CurrentTime: 42, Duration: 100, IsFinished: false, UpdatedAtMs: 5},
			want: Progress{CurrentTime: 42, Duration: 100, IsFinished: false, UpdatedAtMs: 5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeCombine(tt.a, tt.b)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MergeCombine(%+v, %+v) = %+v, want %+v", tt.a, tt.b, got, tt.want)
			}
			// Commutative: merge order must not matter for a dedup follow-up.
			if swapped := MergeCombine(tt.b, tt.a); !reflect.DeepEqual(swapped, tt.want) {
				t.Fatalf("MergeCombine(%+v, %+v) = %+v, want %+v (must be commutative)",
					tt.b, tt.a, swapped, tt.want)
			}
		})
	}
}

// TestMerge_NewerUpdateNeverWipesKnownDuration guards the one carve-out to §5
// rule 2's "accept incoming wholesale": Duration is not a position, and a
// payload that simply omits it must not erase a duration the server already
// knows. Wiping it would disable finished-detection entirely (the book sits at
// 99% forever, spec §5b) and would make any later isFinished response trip
// §1.8.7's null-duration trap, which zeroes the client's position.
func TestMerge_NewerUpdateNeverWipesKnownDuration(t *testing.T) {
	merges := map[string]func(a, b Progress) (Progress, bool){
		"MergeIncoming":      MergeIncoming,
		"MergeExplicit":      MergeExplicit,
		"MergeOfflineReplay": MergeOfflineReplay,
	}
	for name, merge := range merges {
		t.Run(name+"/newer incoming without duration", func(t *testing.T) {
			server := baseServer() // Duration 10000
			incoming := Progress{CurrentTime: 300, Duration: 0, UpdatedAtMs: 6000}
			got, accepted := merge(server, incoming)
			if !accepted {
				t.Fatalf("accepted = false, want true")
			}
			if !floatsClose(got.Duration, server.Duration) {
				t.Errorf("Duration = %v, want %v (a missing duration must not wipe a known one)",
					got.Duration, server.Duration)
			}
			if !floatsClose(got.CurrentTime, 300) {
				t.Errorf("CurrentTime = %v, want 300", got.CurrentTime)
			}
		})
		t.Run(name+"/incoming supplies a duration the server lacks", func(t *testing.T) {
			server := Progress{CurrentTime: 100, Duration: 0, UpdatedAtMs: 5000}
			incoming := Progress{CurrentTime: 300, Duration: 10000, UpdatedAtMs: 6000}
			got, accepted := merge(server, incoming)
			if !accepted {
				t.Fatalf("accepted = false, want true")
			}
			if !floatsClose(got.Duration, 10000) {
				t.Errorf("Duration = %v, want 10000 (adopt a duration the server did not have)", got.Duration)
			}
		})
	}
}
