// file: internal/plugins/dedup/rescore_op.go
// version: 1.3.0
// guid: 5c1a9f38-7b62-4d0e-9a15-6e3b8c07d24f
// last-edited: 2026-09-02

// dedup.rescore — re-band every pending dedup candidate under the CURRENT
// score ladder, and the config-PUT sink that queues it.
//
// WHY this op exists (PR #3052 follow-up, D4): a PUT /api/v1/config that
// changes dedup.signals must re-band the stored candidate rows, because
// AutoResolveCertain reads the STORED band. That re-band used to run inline
// inside the HTTP handler, on context.Background(), over the whole pending
// backlog — 27,439 rows in production — with one fsync per changed row and no
// exclusion against a running dedup.full-scan. A config save could therefore
// block for minutes and interleave its writes with a scan's on the same rows.
//
// As an operation it gets: the dispatcher's ConcurrencyKey — deliberately
// dedup.full-scan's, so a re-band and a scan never run at once — a progress
// bell entry, a cancellable context, and an op id the PUT can hand back.

package dedup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// RescoreParams is the op's payload. Apply=false is a dry run: bands are
// computed and counted but nothing is written.
type RescoreParams struct {
	Apply bool `json:"apply"`
	// Reason is free text recorded in the op log so an operator can tell a
	// config-PUT-triggered re-band from a hand-run one.
	Reason string `json:"reason,omitempty"`
	// LadderFingerprint identifies the score ladder whose save triggered this
	// re-band. It exists ONLY as a dedupe discriminator and an audit record —
	// runRescore deliberately does NOT read it (see below).
	//
	// WHY it has to be here: Registry.EnqueueOp collapses an enqueue onto an
	// already-active op with the same ConcurrencyKey and BYTE-EQUAL params.
	// Without a per-ladder field every config PUT would enqueue the identical
	// {"apply":true,"reason":"…"} blob, so a second PUT landing while the
	// first re-band is still running would be handed the RUNNING op's id and
	// queue nothing. That op read the ladder once, at its start (Rescore does
	// cfg := de.ScoreConfig()), so it would finish the backlog under the OLD
	// ladder and the new one would never reach the stored rows — the exact
	// silent half-applied state D4 set out to remove, and it would look like
	// a success because the PUT got a real op id back.
	//
	// With the fingerprint the dedupe becomes correct in every case rather
	// than defeated: the same ladder enqueued twice while a re-band for it is
	// still active DOES collapse (that op re-bands under this very ladder, so
	// a second pass is pure duplicate work), and two different ladders never
	// collapse — the second queues behind the first on the shared
	// dedup.full-scan key and re-bands the whole backlog again.
	//
	// "Still active" is load-bearing and verified, not assumed: the dedupe in
	// Registry.EnqueueOp compares only against ListActiveOperationsV2() — queued
	// and running rows, with zombie running rows skipped — so a COMPLETED
	// re-band is never a dedupe target. Setting a ladder back to a value it
	// held before therefore queues a fresh pass rather than handing back the
	// finished op's id. (rescoreDef sets neither DedupeQueuedRuns nor a
	// Schedule, the two flags that would short-circuit the params comparison.)
	//
	// It is NOT the ladder the op scores with. runRescore re-bands under the
	// engine's CURRENT ladder, which is the persisted truth; if three PUTs
	// queue three ops, all three legitimately re-band under the newest ladder
	// and the last two are cheap no-ops. Scoring from a value frozen into
	// params would instead re-band the library under a ladder the operator
	// has already replaced.
	LadderFingerprint string `json:"ladder_fingerprint,omitempty"`
}

// ladderFingerprint is a short stable digest of a score ladder, used as
// RescoreParams.LadderFingerprint. encoding/json sorts map keys, so the same
// ladder always produces the same digest.
func ladderFingerprint(cfg unified.ScoreConfig) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		// NOT unreachable, though it is narrow: encoding/json refuses NaN and
		// ±Inf, and ScoreConfig.Validate range-checks only the per-kind
		// confidence bounds of the eight primary kinds — Base, Scale, Boost and
		// every non-primary kind are unchecked. A YAML `base: .inf` therefore
		// reaches here. (A JSON PUT cannot: JSON has no literal for them.)
		//
		// The fallback direction is the safe one — a timestamp never collides,
		// so this degrades to "never dedupe" (a redundant re-band, serialized
		// by the shared key) rather than "always dedupe" (a re-band that never
		// runs). But log it: silently re-queueing a 27k-row pass on every
		// config PUT is not something to discover from a graph.
		slog.Warn("dedup: could not fingerprint the score ladder; every config PUT will queue its own re-band instead of collapsing duplicates",
			"err", err)
		return "unfingerprintable-" + time.Now().UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func (p *Plugin) rescoreDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.rescore",
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Re-band dedup candidates",
		Description:     "Recomputes every pending dedup candidate's band from its stored signals under the current score ladder. Queued automatically when dedup.signals changes; also runnable directly (equivalent to POST /api/v1/dedup/rescore {\"apply\":true}).",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityHigh,
		// Deliberately the SCAN's key, not a key of its own: dedup.full-scan
		// writes the same candidate rows this re-bands, and a scan that lands
		// mid-re-band bands its rows under whichever ladder it snapshotted at
		// its start. Sharing the key serializes them.
		ConcurrencyKey: "dedup.full-scan",
		Cancellable:    true,
		Isolate:        false,
		Timeout:        60 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runRescore,
	}
}

func (p *Plugin) runRescore(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	params := RescoreParams{Apply: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("dedup.rescore: parse params: %w", err)
		}
	}
	_ = reporter.UpdateProgress(0, 1, "Re-banding pending dedup candidates under the current score ladder…")
	res, err := p.engine.Rescore(ctx, params.Apply)
	if err != nil {
		reporter.Logger().Error("dedup rescore failed", "error", err,
			"inspected", res.Inspected, "changed", res.Changed,
			"written", res.Written, "write_errors", res.WriteErrors)
		return fmt.Errorf("dedup rescore: %w", err)
	}
	reporter.Logger().Info("dedup rescore complete",
		"apply", params.Apply, "reason", params.Reason,
		"inspected", res.Inspected, "changed", res.Changed,
		"written", res.Written,
		"skipped_no_breakdown", res.Skipped, "write_errors", res.WriteErrors,
		"band_deltas", res.BandDeltas)
	// WriteErrors is a partial failure, not a success with a footnote: those
	// rows still carry the previous ladder's band, and AutoResolveCertain acts
	// on that band. Fail the op so it is red in the bell, not green.
	if res.WriteErrors > 0 {
		_ = reporter.UpdateProgress(1, 1, fmt.Sprintf(
			"Re-band incomplete — %d of %d changed rows could not be written", res.WriteErrors, res.Changed))
		return fmt.Errorf("dedup rescore: %d of %d changed candidate rows could not be re-banded; they still carry the previous ladder's band", res.WriteErrors, res.Changed)
	}
	_ = reporter.UpdateProgress(1, 1, fmt.Sprintf(
		"Re-banded %d of %d pending candidates (%d skipped: no stored signals)",
		res.Changed, res.Inspected, res.Skipped))
	return nil
}

// dedupScoreSink is what the config UpdateService calls after it has persisted
// a changed dedup.signals ladder: swap the ladder into the live engine, then
// QUEUE the re-band and hand back its op id.
//
// The swap itself is cheap and must be synchronous — the engine has to score
// on the ladder that is now persisted, or a restart would change behaviour.
// The re-band is the expensive half and is queued: see the file comment.
func (p *Plugin) dedupScoreSink(ctx context.Context, cfg unified.ScoreConfig) (string, error) {
	if p == nil || p.engine == nil {
		return "", fmt.Errorf("dedup engine not available; the saved score ladder will take effect at the next start, and stored candidates keep their current band until you %s", dedupengine.RescoreRemedy)
	}
	if err := p.engine.SetScoreConfig(cfg); err != nil {
		// Unreachable in practice: the config layer ran the same Validate
		// before persisting. Reported rather than assumed away.
		return "", fmt.Errorf("live engine refused the score ladder: %w", err)
	}
	if p.registry == nil {
		return "", fmt.Errorf("operation registry not available; the new ladder is live but stored candidates keep their current band until you %s", dedupengine.RescoreRemedy)
	}
	opID, err := p.registry.EnqueueOp(ctx, "dedup.rescore", RescoreParams{
		Apply:             true,
		Reason:            "dedup.signals changed via PUT /api/v1/config",
		LadderFingerprint: ladderFingerprint(cfg),
	})
	if err != nil {
		return "", fmt.Errorf("queueing the candidate re-band failed: %w; the new ladder is live but stored candidates keep their current band until you %s", err, dedupengine.RescoreRemedy)
	}
	return opID, nil
}
