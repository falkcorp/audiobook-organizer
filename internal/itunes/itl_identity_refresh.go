// file: internal/itunes/itl_identity_refresh.go
// version: 1.0.0
// guid: 5c8e2f13-7a94-4d60-9b18-2e6c1a7b0d53
// last-edited: 2026-07-24
//
// Delta-aware library-identity refresh for the 2-way-sync steady state (P1 of the
// 2-way-sync system design, §5.3). Between AO write cycles iTunes legitimately
// mutates the library (adds music/podcasts, changes play state). K13 (identity)
// samples up to 1024 track PIDs and K14 (magnitude) pins an expected track count;
// if the sidecar is never refreshed, accumulated legitimate churn eventually erodes
// the PID-sample overlap below IdentityMinOverlapPct (default 90) or moves the count
// past MagnitudeTolerancePct, and a perfectly valid relocate is false-rejected (F1).
//
// AdoptLibraryIdentity (relocate.go) already re-blesses whatever library is on disk
// — but it is all-or-nothing and blesses a CHANGED LibraryPID too, which is correct
// for a deliberate reseed but wrong for the steady state (it would silently accept a
// swapped library). RefreshLibraryIdentity is the steady-state counterpart: it PINS
// the LibraryPID (a changed PID is a reseed → error, use Adopt), applies a drift
// ceiling (a large count swing is a reseed/mass-deletion, not churn → error), and
// only then re-derives the PID sample + counts.

package itunes

import (
	"fmt"
)

// RefreshOptions tunes the steady-state identity refresh.
type RefreshOptions struct {
	// MaxDriftPct is the largest allowed change in track count (relative to the
	// existing sidecar) that is still treated as legitimate churn. A larger swing
	// errors out — it is a reseed or mass deletion that must be adopted
	// deliberately, not absorbed silently. Default 25 when zero.
	MaxDriftPct int
}

const defaultRefreshMaxDriftPct = 25

// RefreshResult reports what a refresh changed, for logging/telemetry.
type RefreshResult struct {
	LibraryPID     string `json:"library_pid"`
	PrevTrackCount int    `json:"prev_track_count"`
	NewTrackCount  int    `json:"new_track_count"`
	PrevSampleSize int    `json:"prev_sample_size"`
	NewSampleSize  int    `json:"new_sample_size"`
	DriftPct       int    `json:"drift_pct"`
}

// RefreshLibraryIdentity re-derives the .identity.json sidecar (PID sample + track/
// playlist counts) for the library currently at itlPath, KEEPING the LibraryPID
// pinned. It is the steady-state refresh the sync cycle runs each pass so K13/K14
// track legitimate iTunes churn without false-rejecting a valid relocate.
//
// Fails (and writes nothing) when:
//   - no sidecar exists yet (bootstrap must go through AdoptLibraryIdentity);
//   - the on-disk LibraryPID differs from the sidecar's (a reseed/baseline swap —
//     use AdoptLibraryIdentity to bless the new library deliberately);
//   - the track count drifted more than opts.MaxDriftPct from the sidecar (a reseed
//     or mass deletion masquerading as churn).
//
// On success the sidecar is rewritten with the fresh sample/counts and the pinned
// LibraryPID, and the refreshed identity is returned.
func RefreshLibraryIdentity(itlPath string, opts RefreshOptions) (*LibraryIdentity, *RefreshResult, error) {
	maxDrift := opts.MaxDriftPct
	if maxDrift <= 0 {
		maxDrift = defaultRefreshMaxDriftPct
	}

	prev, err := LoadLibraryIdentity(itlPath)
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: load sidecar: %w", err)
	}
	if prev == nil {
		return nil, nil, fmt.Errorf("refresh: no identity sidecar at %s — bootstrap with AdoptLibraryIdentity first", IdentitySidecarPath(itlPath))
	}

	hdr, payload, err := decodeITLForContractFile(itlPath)
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: decode %s: %w", itlPath, err)
	}

	fresh, err := ComputeLibraryIdentity(payload, hdr)
	if err != nil {
		return nil, nil, fmt.Errorf("refresh: compute identity: %w", err)
	}

	// K13 anchor: the LibraryPID must be stable. A different PID is a different
	// library (reseed / baseline swap) — never silently re-bless it here.
	if prev.LibraryPID != "" && fresh.LibraryPID != prev.LibraryPID {
		return nil, nil, fmt.Errorf("refresh: LibraryPID changed %s -> %s (reseed/baseline swap) — use AdoptLibraryIdentity to bless the new library deliberately",
			prev.LibraryPID, fresh.LibraryPID)
	}

	// Drift ceiling: a large count swing is a reseed or mass deletion, not churn.
	drift := driftPct(prev.TrackCount, fresh.TrackCount)
	if drift > maxDrift {
		return nil, nil, fmt.Errorf("refresh: track count drifted %d%% (%d -> %d) > MaxDriftPct %d%% — refusing (reseed or mass deletion; adopt deliberately if intended)",
			drift, prev.TrackCount, fresh.TrackCount, maxDrift)
	}

	// Keep the pinned LibraryPID (fresh.LibraryPID equals it, but be explicit) and
	// carry forward the last-written FileSHA256 anchor — this refresh does not write
	// the .itl, so the recorded on-disk hash of our last write is unchanged.
	fresh.LibraryPID = prev.LibraryPID
	fresh.FileSHA256 = prev.FileSHA256

	if err := SaveLibraryIdentity(itlPath, fresh); err != nil {
		return nil, nil, fmt.Errorf("refresh: save sidecar: %w", err)
	}

	res := &RefreshResult{
		LibraryPID:     fresh.LibraryPID,
		PrevTrackCount: prev.TrackCount,
		NewTrackCount:  fresh.TrackCount,
		PrevSampleSize: len(prev.PIDSample),
		NewSampleSize:  len(fresh.PIDSample),
		DriftPct:       drift,
	}
	return fresh, res, nil
}

// driftPct returns the absolute percentage change from prev to cur. A move from
// zero to non-zero is treated as 100% (any change off an empty baseline is total).
func driftPct(prev, cur int) int {
	if prev == cur {
		return 0
	}
	if prev == 0 {
		return 100
	}
	d := cur - prev
	if d < 0 {
		d = -d
	}
	return d * 100 / prev
}

// PartitionedTrackCount classifies the LIVE library at itlPath into audiobook vs
// non-audiobook tracks (isAudiobookITL). The P2 sync cycle uses this to arm K14 as
// ExpectedTrackCount = plan.AudiobookCount + liveNonAudiobookCount — AO owns the
// audiobook count (DB-authoritative, moves only by our plan), iTunes owns the
// non-audiobook count (refreshed from the live library each cycle).
//
// CAVEAT (F5): isAudiobookITL under-classifies audiobooks (misses "Audio Book"/
// literary genres), so this partition is APPROXIMATE. It is only a K14 magnitude
// anchor, never a targeting filter — a slightly-off split still bounds the expected
// count adequately for the ±MagnitudeTolerancePct band.
func PartitionedTrackCount(itlPath string) (audiobook, nonAudiobook int, err error) {
	lib, err := ParseITL(itlPath)
	if err != nil {
		return 0, 0, fmt.Errorf("partitioned count: parse %s: %w", itlPath, err)
	}
	for i := range lib.Tracks {
		if isAudiobookITL(&lib.Tracks[i]) {
			audiobook++
		} else {
			nonAudiobook++
		}
	}
	return audiobook, nonAudiobook, nil
}
