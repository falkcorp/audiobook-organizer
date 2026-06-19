// file: internal/server/handlers/dedup/label_review_test.go
// version: 1.0.0
// guid: 8a3e1c46-9b20-4d75-8f31-2a6e0c9d5b39
// last-edited: 2026-06-19

package deduphandler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func seedLabel(t *testing.T, d testDeps, candID int64, label, source string) {
	t.Helper()
	if err := d.es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: candID, EntityAID: "a", EntityBID: "b",
		Layer: "exact", Label: label, LabelSource: source, LabelReason: "seed",
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

func TestOverrideDedupLabel_SetsHuman(t *testing.T) {
	h, d := newHandler(t)
	seedLabel(t, d, 7, "true_dup", "auto_high_conf")

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
