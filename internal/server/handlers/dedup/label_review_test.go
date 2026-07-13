// file: internal/server/handlers/dedup/label_review_test.go
// version: 1.3.0
// guid: 8a3e1c46-9b20-4d75-8f31-2a6e0c9d5b39
// last-edited: 2026-07-11

package deduphandler_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
)

// seedLabel writes one labeled example. Each candidate gets its OWN distinct
// book-pair (entity IDs derived from candID) so that export/calibration
// pair-dedupe (INIT-1 T3) leaves distinct candidates intact — tests that want
// same-pair collapse construct colliding pairs explicitly.
func seedLabel(t *testing.T, d testDeps, candID int64, label, source string) {
	t.Helper()
	if err := d.es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: candID,
		EntityAID:   fmt.Sprintf("a%d", candID),
		EntityBID:   fmt.Sprintf("b%d", candID),
		Layer:       "exact", Label: label, LabelSource: source, LabelReason: "seed",
	}); err != nil {
		t.Fatalf("UpsertLabeledExample: %v", err)
	}
}

func decodeLabels(t *testing.T, w *httptest.ResponseRecorder) (int, []map[string]any) {
	t.Helper()
	var resp struct {
		Data struct {
			Total  int              `json:"total"`
			Labels []map[string]any `json:"labels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	return resp.Data.Total, resp.Data.Labels
}

func TestListDedupLabels_FilterAndTotal(t *testing.T) {
	h, d := newHandler(t)
	seedLabel(t, d, 1, "true_dup", "human")
	seedLabel(t, d, 2, "not_dup", "rule")
	seedLabel(t, d, 3, "true_dup", "auto_high_conf")

	// No filter → all 3.
	w := doReq(t, h.ListDedupLabels, http.MethodGet, "/api/v1/dedup/labels", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	total, items := decodeLabels(t, w)
	if total != 3 || len(items) != 3 {
		t.Fatalf("expected 3 labels, got total=%d len=%d", total, len(items))
	}

	// Filter by label_source=human → just 1.
	w2 := doReq(t, h.ListDedupLabels, http.MethodGet, "/api/v1/dedup/labels?label_source=human", nil, nil)
	total2, items2 := decodeLabels(t, w2)
	if total2 != 1 || len(items2) != 1 {
		t.Fatalf("expected 1 human label, got total=%d len=%d", total2, len(items2))
	}
}

func TestGetDedupLabelStats_UnlabeledDerived(t *testing.T) {
	h, d := newHandler(t)
	seedLabel(t, d, 1, "true_dup", "auto_high_conf")
	seedLabel(t, d, 2, "not_dup", "rule")
	seedLabel(t, d, 3, "", "rule") // features captured, no label yet

	w := doReq(t, h.GetDedupLabelStats, http.MethodGet, "/api/v1/dedup/labels/stats", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Total   int            `json:"total"`
			ByLabel map[string]int `json:"by_label"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Total != 3 {
		t.Fatalf("total=%d want 3", resp.Data.Total)
	}
	// The bug: by_label["unlabeled"] used a Label:"" query that matched ALL (3).
	// It must be DERIVED: total - true_dup - not_dup - unsure = 3 - 1 - 1 - 0 = 1.
	if resp.Data.ByLabel["unlabeled"] != 1 {
		t.Errorf("unlabeled=%d want 1; by_label=%v", resp.Data.ByLabel["unlabeled"], resp.Data.ByLabel)
	}
}

func TestOverrideDedupLabel_SetsHuman(t *testing.T) {
	h, d := newHandler(t)
	seedLabel(t, d, 7, "true_dup", "auto_high_conf")
	// The override path now best-effort re-snapshots the breakdown; allow it.
	d.engine.EXPECT().ScorePairsForBook(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, nil).Maybe()

	w := doReq(t, h.OverrideDedupLabel, http.MethodPost, "/api/v1/dedup/labels/7/override",
		map[string]string{"label": "not_dup", "reason": "reviewer says different book"},
		gin.Params{{Key: "id", Value: "7"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	ex, err := d.es.GetLabeledExample(7)
	if err != nil || ex == nil {
		t.Fatalf("GetLabeledExample: %v", err)
	}
	if ex.Label != "not_dup" || ex.LabelSource != "human" {
		t.Fatalf("override label=%q source=%q; want not_dup/human", ex.Label, ex.LabelSource)
	}
}

func TestOverrideDedupLabel_RejectsBadLabel(t *testing.T) {
	h, d := newHandler(t)
	seedLabel(t, d, 9, "true_dup", "rule")
	w := doReq(t, h.OverrideDedupLabel, http.MethodPost, "/api/v1/dedup/labels/9/override",
		map[string]string{"label": "garbage"}, gin.Params{{Key: "id", Value: "9"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

// TestExportLabeledExamples_JSONL covers the C7 read-only JSONL export: every
// dedup:label: row is streamed as one JSON object per line, including the
// formula/feature version, and no mutation occurs.
func TestExportLabeledExamples_JSONL(t *testing.T) {
	h, d := newHandler(t)
	seedLabel(t, d, 1, "true_dup", "human")
	seedLabel(t, d, 2, "not_dup", "rule")
	if err := d.es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: 3, EntityAID: "a", EntityBID: "b",
		Layer: "exact", Label: "unsure", LabelSource: "llm_judge",
		LabelReason: "seed", FormulaVersion: "v3",
	}); err != nil {
		t.Fatalf("UpsertLabeledExample: %v", err)
	}

	w := doReq(t, h.ExportLabeledExamples, http.MethodGet, "/api/v1/dedup/labels/export", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content-type=%q want application/x-ndjson", ct)
	}

	scanner := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	var (
		lines          int
		sawFormulaVer3 bool
	)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lines++
		var ex database.LabeledExample
		if err := json.Unmarshal(line, &ex); err != nil {
			t.Fatalf("line %d not valid LabeledExample JSON: %v; line=%s", lines, err, line)
		}
		if ex.CandidateID == 3 && ex.FormulaVersion == "v3" {
			sawFormulaVer3 = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lines != 3 {
		t.Fatalf("expected 3 JSONL lines, got %d; body=%s", lines, w.Body.String())
	}
	if !sawFormulaVer3 {
		t.Fatalf("expected candidate 3 with formula_version=v3 in export; body=%s", w.Body.String())
	}

	// Read-only: no example should have been mutated by the export.
	ex, err := d.es.GetLabeledExample(1)
	if err != nil || ex == nil {
		t.Fatalf("GetLabeledExample(1): %v", err)
	}
	if ex.Label != "true_dup" || ex.LabelSource != "human" {
		t.Fatalf("export mutated example 1: label=%q source=%q", ex.Label, ex.LabelSource)
	}
}

// TestExportLabeledExamples_FilterByLabelSource confirms the same filter
// fields honored by ListDedupLabels are honored on the export path too.
func TestExportLabeledExamples_FilterByLabelSource(t *testing.T) {
	h, d := newHandler(t)
	seedLabel(t, d, 1, "true_dup", "human")
	seedLabel(t, d, 2, "not_dup", "rule")

	w := doReq(t, h.ExportLabeledExamples, http.MethodGet, "/api/v1/dedup/labels/export?label_source=human", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	scanner := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	var lines int
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		lines++
	}
	if lines != 1 {
		t.Fatalf("expected 1 JSONL line for label_source=human filter, got %d; body=%s", lines, w.Body.String())
	}
}

// countJSONLines counts the non-empty lines in a JSONL response body.
func countJSONLines(t *testing.T, w *httptest.ResponseRecorder) int {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	n := 0
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			n++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return n
}

// TestExportLabeledExamples_DedupesByPairDefaultAndRaw verifies the INIT-1 T3
// pair-collapse on the export path: two labeled rows for the SAME book-pair
// (a rule not_dup and a human true_dup) export as ONE row by default (the human
// row wins), while raw=true streams both stored rows unchanged.
func TestExportLabeledExamples_DedupesByPairDefaultAndRaw(t *testing.T) {
	h, d := newHandler(t)
	// Same pair (dup-pair-a / dup-pair-b), two candidate IDs across layers.
	if err := d.es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: 100, EntityAID: "dup-pair-a", EntityBID: "dup-pair-b",
		Layer: "exact", Label: "not_dup", LabelSource: "rule", DecidedAt: "2026-07-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertLabeledExample rule: %v", err)
	}
	if err := d.es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: 101, EntityAID: "dup-pair-a", EntityBID: "dup-pair-b",
		Layer: "embedding", Label: "true_dup", LabelSource: "human", DecidedAt: "2026-07-05T00:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertLabeledExample human: %v", err)
	}

	// Default: deduped to one row, the human true_dup.
	w := doReq(t, h.ExportLabeledExamples, http.MethodGet, "/api/v1/dedup/labels/export", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if got := countJSONLines(t, w); got != 1 {
		t.Fatalf("default export: expected 1 deduped line, got %d; body=%s", got, w.Body.String())
	}
	var ex database.LabeledExample
	if err := json.Unmarshal(bytes.TrimSpace(w.Body.Bytes()), &ex); err != nil {
		t.Fatalf("decode deduped row: %v", err)
	}
	if ex.LabelSource != "human" || ex.Label != "true_dup" {
		t.Fatalf("deduped row should be human true_dup, got source=%q label=%q", ex.LabelSource, ex.Label)
	}

	// raw=true: escape hatch streams both stored rows.
	wRaw := doReq(t, h.ExportLabeledExamples, http.MethodGet, "/api/v1/dedup/labels/export?raw=true", nil, nil)
	if wRaw.Code != http.StatusOK {
		t.Fatalf("raw status=%d want 200; body=%s", wRaw.Code, wRaw.Body.String())
	}
	if got := countJSONLines(t, wRaw); got != 2 {
		t.Fatalf("raw=true export: expected 2 raw lines, got %d; body=%s", got, wRaw.Body.String())
	}
}

// TestExportLabeledExamples_NoEmbedStore mirrors the 503 nil-store guard used
// throughout this package's other embedding-store-backed handlers.
func TestExportLabeledExamples_NoEmbedStore(t *testing.T) {
	h, _ := newHandler(t, noEmbed)
	w := doReq(t, h.ExportLabeledExamples, http.MethodGet, "/api/v1/dedup/labels/export", nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", w.Code, w.Body.String())
	}
}

// ───────────────────────── suspicious-label review queue (T4) ────────────────

// upsertEx writes an arbitrary LabeledExample with a distinct book-pair derived
// from candID (so no pair collides), applying opt to shape the evidence fields.
func upsertEx(t *testing.T, d testDeps, candID int64, label, source string, opt func(*database.LabeledExample)) {
	t.Helper()
	ex := database.LabeledExample{
		CandidateID: candID,
		EntityAID:   fmt.Sprintf("a%d", candID),
		EntityBID:   fmt.Sprintf("b%d", candID),
		Layer:       "exact",
		Label:       label,
		LabelSource: source,
		LabelReason: "seed",
	}
	if opt != nil {
		opt(&ex)
	}
	if err := d.es.UpsertLabeledExample(ex); err != nil {
		t.Fatalf("UpsertLabeledExample: %v", err)
	}
}

// getSuspicious drives ListSuspiciousDedupLabels and returns total + rows.
func getSuspicious(t *testing.T, h *deduphandler.Handler) (int, []map[string]any) {
	t.Helper()
	w := doReq(t, h.ListSuspiciousDedupLabels, http.MethodGet, "/api/v1/dedup/labels/suspicious", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	return decodeLabels(t, w)
}

func fptr(v float64) *float64 { return &v }

// TestSuspiciousPredicateIdentity covers arm (a): a rule not_dup that shares hard
// identity (ASIN, version group, or identical primary path) is flagged, and the
// flag routes through the exported dataset.SharesIdentity helper.
func TestSuspiciousPredicateIdentity(t *testing.T) {
	cases := []struct {
		name string
		opt  func(*database.LabeledExample)
	}{
		{"asin", func(ex *database.LabeledExample) { ex.A.ASIN, ex.B.ASIN = "B00SAME", "B00SAME" }},
		{"version_group", func(ex *database.LabeledExample) { ex.A.VersionGroupID, ex.B.VersionGroupID = "vg-1", "vg-1" }},
		{"path", func(ex *database.LabeledExample) { ex.A.PrimaryPath, ex.B.PrimaryPath = "/lib/x.m4b", "/lib/x.m4b" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d := newHandler(t)
			upsertEx(t, d, 1, "not_dup", "rule", tc.opt)
			total, rows := getSuspicious(t, h)
			if total != 1 || len(rows) != 1 {
				t.Fatalf("expected 1 suspicious row, got total=%d len=%d", total, len(rows))
			}
			reasons, _ := rows[0]["suspicion_reasons"].([]any)
			if len(reasons) == 0 {
				t.Fatalf("expected a suspicion reason; row=%v", rows[0])
			}
		})
	}
}

// TestSuspiciousPredicateBand covers arm (b): a rule not_dup in a CERTAIN/HIGH
// band is flagged; a LOW-band one (all else disjoint) is not.
func TestSuspiciousPredicateBand(t *testing.T) {
	h, d := newHandler(t)
	upsertEx(t, d, 1, "not_dup", "rule", func(ex *database.LabeledExample) { ex.Band = "CERTAIN" })
	upsertEx(t, d, 2, "not_dup", "rule", func(ex *database.LabeledExample) { ex.Band = "HIGH" })
	upsertEx(t, d, 3, "not_dup", "rule", func(ex *database.LabeledExample) { ex.Band = "LOW" })
	total, _ := getSuspicious(t, h)
	if total != 2 {
		t.Fatalf("expected 2 suspicious (CERTAIN,HIGH), got %d", total)
	}
}

// TestSuspiciousPredicateSimilarity covers arm (c): cosine >= 0.95 flags; below
// does not.
func TestSuspiciousPredicateSimilarity(t *testing.T) {
	h, d := newHandler(t)
	upsertEx(t, d, 1, "not_dup", "rule", func(ex *database.LabeledExample) { ex.Similarity = fptr(0.96) })
	upsertEx(t, d, 2, "not_dup", "rule", func(ex *database.LabeledExample) { ex.Similarity = fptr(0.80) })
	total, _ := getSuspicious(t, h)
	if total != 1 {
		t.Fatalf("expected 1 suspicious (sim>=0.95), got %d", total)
	}
}

// TestSuspiciousPredicateMsSecSignature covers arm (d): identical title with a
// ms/sec duration-ratio (~0.001) flags; a sane ratio does not.
func TestSuspiciousPredicateMsSecSignature(t *testing.T) {
	h, d := newHandler(t)
	upsertEx(t, d, 1, "not_dup", "rule", func(ex *database.LabeledExample) {
		ex.A.Title, ex.B.Title = "Same Book", "Same Book"
		ex.DurationRatio = 0.001
	})
	upsertEx(t, d, 2, "not_dup", "rule", func(ex *database.LabeledExample) {
		ex.A.Title, ex.B.Title = "Same Book", "Same Book"
		ex.DurationRatio = 0.30
	})
	total, _ := getSuspicious(t, h)
	if total != 1 {
		t.Fatalf("expected 1 suspicious (ms/sec signature), got %d", total)
	}
}

// TestSuspiciousPredicateCleanRuleLabelNotFlagged is the anti-over-suppression
// case: a rule not_dup with disjoint ASINs/paths, LOW band, similarity 0.5, and
// a sane ratio 0.3 carries NO duplicate-shaped evidence and must NOT be flagged.
func TestSuspiciousPredicateCleanRuleLabelNotFlagged(t *testing.T) {
	h, d := newHandler(t)
	upsertEx(t, d, 1, "not_dup", "rule", func(ex *database.LabeledExample) {
		ex.A.ASIN, ex.B.ASIN = "B00AAA", "B00BBB"
		ex.A.PrimaryPath, ex.B.PrimaryPath = "/lib/a.m4b", "/lib/b.m4b"
		ex.A.Title, ex.B.Title = "Book A", "Book B"
		ex.Band = "LOW"
		ex.Similarity = fptr(0.5)
		ex.DurationRatio = 0.3
	})
	total, _ := getSuspicious(t, h)
	if total != 0 {
		t.Fatalf("clean rule label must not be flagged; got total=%d", total)
	}
}

// TestSuspiciousPredicateEmptyFieldsNotFlagged asserts a rule not_dup with
// all-empty identity, no band, no similarity, and zero ratio is not suspicious
// (unknown fields are non-disqualifying, never firing an arm).
func TestSuspiciousPredicateEmptyFieldsNotFlagged(t *testing.T) {
	h, d := newHandler(t)
	upsertEx(t, d, 1, "not_dup", "rule", nil) // no evidence set at all
	total, _ := getSuspicious(t, h)
	if total != 0 {
		t.Fatalf("empty-evidence rule label must not be flagged; got total=%d", total)
	}
}

// TestSuspiciousPredicateHumanLabelNeverFlagged asserts human-sourced rows are
// excluded regardless of evidence — even a shared ASIN plus CERTAIN band does
// not surface a human label into the queue (only rule rows are second-guessed).
func TestSuspiciousPredicateHumanLabelNeverFlagged(t *testing.T) {
	h, d := newHandler(t)
	upsertEx(t, d, 1, "not_dup", "human", func(ex *database.LabeledExample) {
		ex.A.ASIN, ex.B.ASIN = "B00SAME", "B00SAME"
		ex.Band = "CERTAIN"
		ex.Similarity = fptr(0.99)
	})
	total, _ := getSuspicious(t, h)
	if total != 0 {
		t.Fatalf("human label must never be flagged; got total=%d", total)
	}
}

// TestSuspiciousExcludesNonNotDupAndNonRule confirms the queue only ever
// contains rule-sourced not_dup rows: a true_dup rule row and an auto_high_conf
// not_dup row (both with strong evidence) are excluded.
func TestSuspiciousExcludesNonNotDupAndNonRule(t *testing.T) {
	h, d := newHandler(t)
	upsertEx(t, d, 1, "true_dup", "rule", func(ex *database.LabeledExample) { ex.Band = "CERTAIN" })
	upsertEx(t, d, 2, "not_dup", "auto_high_conf", func(ex *database.LabeledExample) { ex.Band = "CERTAIN" })
	total, _ := getSuspicious(t, h)
	if total != 0 {
		t.Fatalf("non-not_dup/non-rule rows must be excluded; got total=%d", total)
	}
}

// TestSuspiciousNoEmbedStore mirrors the 503 nil-store guard.
func TestSuspiciousNoEmbedStore(t *testing.T) {
	h, _ := newHandler(t, noEmbed)
	w := doReq(t, h.ListSuspiciousDedupLabels, http.MethodGet, "/api/v1/dedup/labels/suspicious", nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", w.Code, w.Body.String())
	}
}
