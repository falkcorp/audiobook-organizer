// file: internal/server/handlers/dedup/label_review.go
// version: 1.0.0
// guid: 5e2a9c41-7b30-4d68-8f12-3a6e0c9d5b27
// last-edited: 2026-06-19

package deduphandler

import (
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// ListDedupLabels handles GET /api/v1/dedup/labels — the gold-dataset review feed.
// It returns labeled dedup examples (the dedup:label: keyspace) for the C6 review
// UI, filterable by label / label_source / band / folder_relation / signature_relation,
// paginated. The total is the unfiltered-by-page count for the same filters.
func (h *Handler) ListDedupLabels(c *gin.Context) {
	es := h.resolveEmbeddingStore()
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	filter := database.LabeledExampleFilter{
		Label:             c.Query("label"),
		LabelSource:       c.Query("label_source"),
		Band:              c.Query("band"),
		FolderRelation:    c.Query("folder_relation"),
		SignatureRelation: c.Query("signature_relation"),
		Limit:             clampAtoi(c.Query("limit"), 50, 1, 500),
		Offset:            clampAtoi(c.Query("offset"), 0, 0, 1<<31),
	}

	items, err := es.ListLabeledExamples(filter)
	if err != nil {
		httputil.InternalError(c, "failed to list labeled examples", err)
		return
	}
	// Total for the same filters, ignoring pagination.
	countFilter := filter
	countFilter.Limit, countFilter.Offset = 0, 0
	total, err := es.CountLabeledExamples(countFilter)
	if err != nil {
		httputil.InternalError(c, "failed to count labeled examples", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"labels": items,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

// GetDedupLabelStats handles GET /api/v1/dedup/labels/stats — counts by label and by
// label_source, so the review UI can show the dataset composition at a glance.
func (h *Handler) GetDedupLabelStats(c *gin.Context) {
	es := h.resolveEmbeddingStore()
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}
	byLabel := map[string]int{}
	for _, l := range []string{"true_dup", "not_dup", "unsure", ""} {
		n, err := es.CountLabeledExamples(database.LabeledExampleFilter{Label: l})
		if err != nil {
			httputil.InternalError(c, "failed to count labels", err)
			return
		}
		key := l
		if key == "" {
			key = "unlabeled"
		}
		byLabel[key] = n
	}
	bySource := map[string]int{}
	for _, src := range []string{"human", "auto_high_conf", "rule", "itunes_attr", "llm_judge"} {
		n, err := es.CountLabeledExamples(database.LabeledExampleFilter{LabelSource: src})
		if err != nil {
			httputil.InternalError(c, "failed to count sources", err)
			return
		}
		bySource[src] = n
	}
	total, _ := es.CountLabeledExamples(database.LabeledExampleFilter{})
	httputil.RespondWithOK(c, gin.H{"total": total, "by_label": byLabel, "by_source": bySource})
}

// OverrideDedupLabel handles POST /api/v1/dedup/labels/:id/override — a human
// reviewer setting/correcting the label on an example. The override always sets
// label_source="human" (this is gold) and re-stamps DecidedAt, so reviewer
// corrections take precedence over rule/auto-mined labels.
func (h *Handler) OverrideDedupLabel(c *gin.Context) {
	es := h.resolveEmbeddingStore()
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid candidate id")
		return
	}
	var body struct {
		Label  string `json:"label"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if body.Label != labelTrueDup && body.Label != labelNotDup && body.Label != "unsure" {
		httputil.RespondWithBadRequest(c, "label must be one of true_dup, not_dup, unsure")
		return
	}

	ex, err := es.GetLabeledExample(id)
	if err != nil {
		httputil.InternalError(c, "failed to load labeled example", err)
		return
	}
	if ex == nil {
		httputil.RespondWithNotFound(c, "labeled example", c.Param("id"))
		return
	}

	ex.Label = body.Label
	ex.LabelSource = labelSourceHuman
	ex.LabelReason = body.Reason
	if ex.LabelReason == "" {
		ex.LabelReason = "ui_override"
	}
	ex.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	if err := es.UpsertLabeledExample(*ex); err != nil {
		httputil.InternalError(c, "failed to save label override", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"status": "updated", "candidate_id": id, "label": ex.Label, "label_source": ex.LabelSource})
}

// clampAtoi parses s as an int, falling back to def, then clamps to [lo, hi].
func clampAtoi(s string, def, lo, hi int) int {
	v := def
	if s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			v = n
		}
	}
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return v
}
