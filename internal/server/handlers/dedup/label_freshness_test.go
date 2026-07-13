// file: internal/server/handlers/dedup/label_freshness_test.go
// version: 1.0.0
// guid: 4d7a2c81-6e39-4b05-9f28-1a3c7e0d5b62
// last-edited: 2026-07-13

// Tests for the label-write freshness refresh: dismissing / relabeling a pair
// (re)snapshots its ScoreBreakdown onto the LabeledExample via the engine's
// shared scorer, INCLUDING below-band pairs, without ever clobbering the human
// label fields or failing the primary label write.

package deduphandler_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	unifiedpkg "github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// allowFreshnessStoreReads permits (but does not require) the best-effort
// snapshot's book reads, returning books with a file so BuildExample populates
// an example. Unlike allowLabelCaptureReads it does NOT wire the engine — each
// test below sets its own ScorePairsForBook return.
func allowFreshnessStoreReads(d testDeps) {
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()
	d.store.EXPECT().GetBookFiles(mock.Anything).
		Return([]database.BookFile{{FilePath: "/lib/a.m4b", FileSize: 5 << 20, Duration: 3600}}, nil).Maybe()
}

// TestDismiss_PersistsBelowBandBreakdown proves that dismissing a pair to not_dup
// persists a non-nil ScoreBreakdown on its LabeledExample EVEN when the composed
// score is below the review band (Band == "") — the snapshot must not re-apply
// the operational scan's band-skip. It also proves the narrow write leaves the
// human label fields intact.
func TestDismiss_PersistsBelowBandBreakdown(t *testing.T) {
	h, d := newHandler(t)
	allowFreshnessStoreReads(d)
	id, aID, bID := insertCandidate(t, d.es, "book-aaa", "book-bbb")

	// A below-band score (Band == "") with real signals — the calibration
	// negative the operational scan would have discarded.
	belowBand := &unifiedpkg.UnifiedDedupScore{
		Score:   12.5,
		Band:    "", // below every review band
		Pair:    [2]string{aID, bID},
		Formula: unifiedpkg.FormulaVersion,
		Signals: []unifiedpkg.Signal{{Kind: unifiedpkg.SigDuration, Raw: 0.5, Confidence: 0.4, Evidence: "dur"}},
	}
	d.engine.EXPECT().ScorePairsForBook(mock.Anything, aID, mock.Anything).
		Return([]dedupengine.RescorePairResult{{OtherID: bID, Score: belowBand, NumSignals: 1}}, nil).Once()

	w := doReq(t, h.DismissDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/dismiss", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}

	ex, err := d.es.GetLabeledExample(id)
	if err != nil || ex == nil {
		t.Fatalf("GetLabeledExample: %v (ex=%v)", err, ex)
	}
	// Breakdown persisted despite being below-band.
	if len(ex.ScoreBreakdown) == 0 {
		t.Fatalf("expected a persisted ScoreBreakdown for a below-band dismiss, got empty")
	}
	if ex.Band != "" {
		t.Fatalf("Band=%q want empty (below-band snapshot preserved)", ex.Band)
	}
	if ex.Score != 12.5 {
		t.Fatalf("Score=%v want 12.5", ex.Score)
	}
	// Human label fields untouched by the narrow write.
	if ex.Label != "not_dup" || ex.LabelSource != "human" || ex.LabelReason != "user_dismiss" {
		t.Fatalf("label=%q source=%q reason=%q; want not_dup/human/user_dismiss", ex.Label, ex.LabelSource, ex.LabelReason)
	}
}

// TestOverrideRelabel_PreservesHumanLabelFields proves the refresh on the
// relabel/override path narrow-writes ONLY the score fields: after the refresh
// the row is still LabelSource=="human" with the reviewer's Label + LabelReason,
// and the breakdown was populated.
func TestOverrideRelabel_PreservesHumanLabelFields(t *testing.T) {
	h, d := newHandler(t)
	seedLabel(t, d, 42, "true_dup", "auto_high_conf") // Layer "exact" → no embedding cosine

	score := &unifiedpkg.UnifiedDedupScore{
		Score:   77.0,
		Band:    "HIGH",
		Pair:    [2]string{"a42", "b42"},
		Formula: unifiedpkg.FormulaVersion,
		Signals: []unifiedpkg.Signal{{Kind: unifiedpkg.SigMetaFuzzy, Raw: 0.9, Confidence: 0.8, Evidence: "meta"}},
	}
	d.engine.EXPECT().ScorePairsForBook(mock.Anything, "a42", mock.Anything).
		Return([]dedupengine.RescorePairResult{{OtherID: "b42", Score: score, NumSignals: 1}}, nil).Once()

	w := doReq(t, h.OverrideDedupLabel, http.MethodPost, "/api/v1/dedup/labels/42/override",
		map[string]string{"label": "not_dup", "reason": "reviewer says different book"},
		gin.Params{{Key: "id", Value: "42"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}

	ex, err := d.es.GetLabeledExample(42)
	if err != nil || ex == nil {
		t.Fatalf("GetLabeledExample: %v (ex=%v)", err, ex)
	}
	if ex.Label != "not_dup" || ex.LabelSource != "human" || ex.LabelReason != "reviewer says different book" {
		t.Fatalf("label=%q source=%q reason=%q; human label fields not preserved through refresh",
			ex.Label, ex.LabelSource, ex.LabelReason)
	}
	if len(ex.ScoreBreakdown) == 0 || ex.Band != "HIGH" || ex.Score != 77.0 {
		t.Fatalf("score fields not refreshed: breakdown_len=%d band=%q score=%v", len(ex.ScoreBreakdown), ex.Band, ex.Score)
	}
}

// TestDismiss_ScoringFailureIsBestEffort proves a scoring failure in the refresh
// path NEVER fails the primary label write: the dismiss still returns 200, the
// human label is still written, and no bogus breakdown is persisted.
func TestDismiss_ScoringFailureIsBestEffort(t *testing.T) {
	h, d := newHandler(t)
	allowFreshnessStoreReads(d)
	id, aID, _ := insertCandidate(t, d.es, "book-aaa", "book-bbb")

	d.engine.EXPECT().ScorePairsForBook(mock.Anything, aID, mock.Anything).
		Return(nil, assertErr{}).Once()

	w := doReq(t, h.DismissDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(id, 10)+"/dismiss", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 even when rescoring fails; body=%s", w.Code, w.Body.String())
	}

	ex, err := d.es.GetLabeledExample(id)
	if err != nil || ex == nil {
		t.Fatalf("GetLabeledExample: %v (ex=%v)", err, ex)
	}
	// The label was written despite the scoring failure.
	if ex.Label != "not_dup" || ex.LabelSource != "human" {
		t.Fatalf("label=%q source=%q; want not_dup/human written despite rescore failure", ex.Label, ex.LabelSource)
	}
	// No bogus breakdown persisted from the failed rescore.
	if len(ex.ScoreBreakdown) != 0 {
		t.Fatalf("expected no ScoreBreakdown after a rescore failure, got %d bytes", len(ex.ScoreBreakdown))
	}
}
