// file: internal/dedup/collectors_window.go
// version: 1.0.0
// guid: 9c4e1b07-63d2-4f8a-a5e9-2b7d0c8f4a13
// last-edited: 2026-10-11

// Package dedup — window-print collector (02-PR8, owner decisions D14a, D34).
//
// CollectWindowAcoustID turns the window prints the Macs wrote (fpwin:) into a
// SUPPORTING dedup signal, unified.SigWindowAcoustID:
//
//  1. Gate: nothing happens until dedup.window-index-build has completed a
//     pass (IsWindowIndexBuilt). There is no O(N) fallback.
//  2. Probe: the query file's own band member list (fpwinidx_meta:) is looked
//     up in the band index (fpwinidx:); candidate files share at least
//     MinBandHits band keys.
//  3. Verify: both files' STORED windows go through
//     fingerprint.WindowSetSimilarity. ErrIncompatibleWindows and
//     ErrNoComparableWindows mean the two files' prints are not comparable
//     (another pipeline, window set or tool version; no shared slot): that is
//     missing evidence, not disagreement, so nothing is emitted and the pair is
//     counted as incomparable. A LowConfidence result (durations more than 2
//     percent apart, every slot compared with every slot) is skipped too.
//  4. Emit one signal per candidate BOOK, the best similarity among its files.
//
// It only reads stored prints; it never decodes audio.

package dedup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// WindowAcoustIDConfig calibrates CollectWindowAcoustID.
type WindowAcoustIDConfig struct {
	// MinBandHits is the least number of shared band keys that makes a file
	// a candidate. The index probe already applies
	// fingerprint.WindowMinBandHits (2), so a lower value has no effect.
	MinBandHits int
	// MaxCandidates caps the probe's candidate files (highest hit count
	// first). <= 0 disables the cap.
	MaxCandidates int
	// MinSimilarity is the least WindowSetSimilarity score that emits.
	MinSimilarity float64
	// MinConfidence / MaxConfidence are the interpolation end-points for the
	// emitted signal's Confidence over [MinSimilarity, 1]. A supporting kind
	// scores by its configured boost, so Confidence is for display and
	// calibration only.
	MinConfidence float64
	MaxConfidence float64
	// Stats, when set, receives the collector's counts. Safe for concurrent
	// use by several collectors.
	Stats *WindowCollectStats
}

// DefaultWindowAcoustIDConfig returns the 02-PR8 starting calibration.
func DefaultWindowAcoustIDConfig() WindowAcoustIDConfig {
	return WindowAcoustIDConfig{
		MinBandHits:   fingerprint.WindowMinBandHits,
		MaxCandidates: 200,
		MinSimilarity: 0.85,
		MinConfidence: 0.90,
		MaxConfidence: 0.97,
	}
}

// WindowCollectStats counts what the collector saw across calls.
type WindowCollectStats struct {
	// Probed is the number of query files probed.
	Probed atomic.Int64
	// Candidates is the number of candidate files verified.
	Candidates atomic.Int64
	// Incomparable counts candidate files whose prints could not be compared
	// with the query's (ErrIncompatibleWindows / ErrNoComparableWindows).
	Incomparable atomic.Int64
	// LowConfidence counts comparisons skipped as LowConfidence.
	LowConfidence atomic.Int64
	// BelowMinSimilarity counts comparisons under MinSimilarity.
	BelowMinSimilarity atomic.Int64
	// Emitted counts signals emitted (one per candidate book per call).
	Emitted atomic.Int64
}

func (s *WindowCollectStats) add(f func(*WindowCollectStats) *atomic.Int64) {
	if s != nil {
		f(s).Add(1)
	}
}

// windowMatch is one emitted signal and the book it names.
type windowMatch struct {
	bookID string
	signal unified.Signal
}

// windowGateLogEvery bounds the "index not built" INFO line: the collector runs
// once per file of every book a scan scores, and until the build op has run
// that is every call.
const windowGateLogEvery = time.Hour

var windowGateLastLog atomic.Int64

// CollectWindowAcoustID probes the window-band index for queryFile and emits a
// SigWindowAcoustID signal per candidate book whose window prints verify
// against the query's. queryBookID suppresses matches inside the query's own
// book. The evidence names the candidate book ID (the engine filters signals
// per pair by it).
func CollectWindowAcoustID(
	store WindowAcoustIDStore,
	queryFile *database.BookFile,
	queryBookID string,
	cfg WindowAcoustIDConfig,
) ([]unified.Signal, error) {
	matches, err := collectWindowMatches(store, queryFile, queryBookID, cfg)
	if err != nil || len(matches) == 0 {
		return nil, err
	}
	out := make([]unified.Signal, len(matches))
	for i, m := range matches {
		out[i] = m.signal
	}
	return out, nil
}

// CollectWindowAcoustIDForBook runs CollectWindowAcoustID for every file of a
// book and keeps ONE signal per candidate book, the highest similarity. A
// supporting boost is added per signal (unified.ComposeScore), so without
// this a twenty-file book matching one candidate on every file would add the
// boost twenty times. Per-file errors are logged and skipped, like the other
// acoustic collectors' callers do.
func CollectWindowAcoustIDForBook(
	store WindowAcoustIDStore,
	files []database.BookFile,
	bookID string,
	cfg WindowAcoustIDConfig,
) []unified.Signal {
	if store == nil {
		return nil
	}
	best := make(map[string]unified.Signal)
	var order []string
	for i := range files {
		matches, err := collectWindowMatches(store, &files[i], bookID, cfg)
		if err != nil {
			logging.Debug(context.Background(), "dedup/collectors_window: collect error",
				"file_id", files[i].ID, "err", err)
			continue
		}
		for _, m := range matches {
			cur, ok := best[m.bookID]
			if !ok {
				order = append(order, m.bookID)
			}
			if !ok || m.signal.Raw > cur.Raw {
				best[m.bookID] = m.signal
			}
		}
	}
	out := make([]unified.Signal, 0, len(order))
	for _, id := range order {
		out = append(out, best[id])
	}
	return out
}

func collectWindowMatches(
	store WindowAcoustIDStore,
	queryFile *database.BookFile,
	queryBookID string,
	cfg WindowAcoustIDConfig,
) ([]windowMatch, error) {
	if store == nil || queryFile == nil || queryFile.ID == "" {
		return nil, nil
	}
	if !store.IsWindowIndexBuilt() {
		now := time.Now().UnixNano()
		last := windowGateLastLog.Load()
		if now-last >= int64(windowGateLogEvery) && windowGateLastLog.CompareAndSwap(last, now) {
			logging.Info(context.Background(), "dedup/collectors_window: window index not yet built; skipping window probe",
				"file_id", queryFile.ID,
				"hint", "run dedup.window-index-build (live) to enable the window_acoustid signal")
		}
		return nil, nil
	}

	bands, indexed, err := store.WindowIndexMembers(queryFile.ID)
	if err != nil {
		return nil, fmt.Errorf("dedup/collectors_window: members of %s: %w", queryFile.ID, err)
	}
	if !indexed || len(bands) == 0 {
		return nil, nil // no windows, or nothing to band: missing evidence
	}
	cfg.Stats.add(func(s *WindowCollectStats) *atomic.Int64 { return &s.Probed })

	hits, err := store.WindowIndexProbe(bands, cfg.MaxCandidates)
	if err != nil {
		return nil, fmt.Errorf("dedup/collectors_window: probe %s: %w", queryFile.ID, err)
	}
	if len(hits) == 0 {
		return nil, nil
	}

	var queryPrints []fingerprint.WindowPrint
	loadedQuery := false
	best := make(map[string]windowMatch)
	var order []string

	// Deterministic candidate order, so ties and logs are stable.
	candIDs := make([]string, 0, len(hits))
	for id, n := range hits {
		if id != queryFile.ID && n >= cfg.MinBandHits {
			candIDs = append(candIDs, id)
		}
	}
	sort.Strings(candIDs)

	for _, candID := range candIDs {
		cand, err := store.GetBookFileByFileID(candID)
		if err != nil {
			logging.Debug(context.Background(), "dedup/collectors_window: candidate lookup error",
				"cand_file_id", candID, "err", err)
			continue
		}
		if cand == nil || cand.BookID == "" || cand.BookID == queryBookID {
			continue
		}
		if !loadedQuery {
			loadedQuery = true
			qw, err := store.GetFingerprintWindows(database.FileWindowRef(queryFile.ID))
			if err != nil {
				return nil, fmt.Errorf("dedup/collectors_window: windows of %s: %w", queryFile.ID, err)
			}
			queryPrints = windowPrintsOf(qw)
		}
		cw, err := store.GetFingerprintWindows(database.FileWindowRef(candID))
		if err != nil {
			logging.Debug(context.Background(), "dedup/collectors_window: candidate windows error",
				"cand_file_id", candID, "err", err)
			continue
		}
		cfg.Stats.add(func(s *WindowCollectStats) *atomic.Int64 { return &s.Candidates })

		sim, err := fingerprint.WindowSetSimilarity(queryPrints, windowPrintsOf(cw))
		if errors.Is(err, fingerprint.ErrIncompatibleWindows) || errors.Is(err, fingerprint.ErrNoComparableWindows) {
			cfg.Stats.add(func(s *WindowCollectStats) *atomic.Int64 { return &s.Incomparable })
			logging.Debug(context.Background(), "dedup/collectors_window: prints not comparable (missing evidence)",
				"file_id", queryFile.ID, "cand_file_id", candID, "err", err)
			continue
		}
		if err != nil {
			logging.Debug(context.Background(), "dedup/collectors_window: similarity error",
				"file_id", queryFile.ID, "cand_file_id", candID, "err", err)
			continue
		}
		if sim.LowConfidence {
			cfg.Stats.add(func(s *WindowCollectStats) *atomic.Int64 { return &s.LowConfidence })
			continue
		}
		if sim.Score < cfg.MinSimilarity {
			cfg.Stats.add(func(s *WindowCollectStats) *atomic.Int64 { return &s.BelowMinSimilarity })
			continue
		}

		conf := lshConfidenceScale(sim.Score, cfg.MinSimilarity, 1.0, cfg.MinConfidence, cfg.MaxConfidence)
		m := windowMatch{bookID: cand.BookID, signal: unified.Signal{
			Kind:       unified.SigWindowAcoustID,
			Raw:        sim.Score,
			Confidence: conf,
			FPVersion:  windowFPVersion(cw),
			Evidence: fmt.Sprintf(
				"window acoustid: file %s -> book %s (window similarity %.4f over %d slot(s), %d band hits)",
				candID, cand.BookID, sim.Score, len(sim.Slots), hits[candID]),
		}}
		cur, ok := best[cand.BookID]
		if !ok {
			order = append(order, cand.BookID)
		}
		if !ok || m.signal.Raw > cur.signal.Raw {
			best[cand.BookID] = m
		}
	}

	out := make([]windowMatch, 0, len(order))
	for _, id := range order {
		out = append(out, best[id])
		cfg.Stats.add(func(s *WindowCollectStats) *atomic.Int64 { return &s.Emitted })
	}
	return out, nil
}

// windowPrintsOf converts stored windows to the fingerprint package's form.
// Whole-file rows are left out: they are not windows, and pairing one by
// SlotBP with a covers-whole window would compare different spans.
func windowPrintsOf(ws []database.FingerprintWindow) []fingerprint.WindowPrint {
	out := make([]fingerprint.WindowPrint, 0, len(ws))
	for i := range ws {
		w := &ws[i]
		if w.Kind == database.WindowKindWhole {
			continue
		}
		out = append(out, fingerprint.WindowPrint{
			Kind:            fingerprint.WindowKind(w.Kind),
			SlotBP:          w.SlotBP,
			WindowSet:       w.WindowSet,
			OffsetSec:       w.OffsetSec,
			LengthSec:       w.LengthSec,
			DecodedSec:      w.DecodedSec,
			CoversWhole:     w.CoversWhole,
			DurationUsedSec: w.DurationUsedSec,
			DurationSource:  fingerprint.DurationSource(w.DurationSource),
			Frames:          w.Frames,
			Raw:             w.Raw,
			Algorithm:       w.Algorithm,
			Pipeline:        w.Pipeline,
			FpcalcVersion:   w.FpcalcVersion,
			FFmpegVersion:   w.FFmpegVersion,
		})
	}
	return out
}

// windowFPVersion is the provenance stamp of a window set: its pipeline.
func windowFPVersion(ws []database.FingerprintWindow) string {
	for i := range ws {
		if ws[i].Kind == database.WindowKindWindow && ws[i].Pipeline != "" {
			return ws[i].Pipeline
		}
	}
	return ""
}
