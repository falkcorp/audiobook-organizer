// file: internal/server/handlers/dedup/label_review.go
// version: 1.4.0
// guid: 5e2a9c41-7b30-4d68-8f12-3a6e0c9d5b27
// last-edited: 2026-07-11

package deduphandler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// ListDedupLabels handles GET /api/v1/dedup/labels — the gold-dataset review feed.
// It returns labeled dedup examples (the dedup:label: keyspace) for the C6 review
// UI, filterable by label / label_source / band / folder_relation / signature_relation,
// paginated. The total is the unfiltered-by-page count for the same filters.
func (h *Handler) ListDedupLabels(c *gin.Context) {
	es := h.embeddingStore
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

// suspiciousRow is a labeled example plus the reasons it was flagged as
// suspicious, so the review UI can show WHY a rule-sourced not_dup is being
// surfaced for human confirmation. LabeledExample is embedded so its fields
// promote to the top level of the JSON object (mirroring ListDedupLabels rows).
type suspiciousRow struct {
	database.LabeledExample
	SuspicionReasons []string `json:"suspicion_reasons"`
}

// isSuspiciousLabel: rule-sourced not_dup with duplicate-shaped evidence.
func isSuspiciousLabel(ex database.LabeledExample) bool {
	return len(suspicionReasons(ex)) > 0
}

// suspicionReasons returns the human-readable evidence that makes a rule-sourced
// not_dup label suspicious, or nil when the row is not suspicious. Only
// rule-sourced rows can fire (human/auto rows are never second-guessed here).
// Empty/unknown fields never fire an arm — they are non-disqualifying, the row
// simply isn't suspicious on that evidence.
func suspicionReasons(ex database.LabeledExample) []string {
	if ex.LabelSource != "rule" {
		return nil
	}
	var reasons []string
	// Arm (a) — TRANSITIONAL: reuses the exported dataset.SharesIdentity helper
	// (same ASIN / same version-group / identical primary path). After TASK-07
	// re-mines with TASK-01's guard, identity-sharing pairs are emitted as
	// "unsure" (not rule not_dup), so this arm can only surface the historical
	// pre-re-mine backlog and then goes quiet. The queue's durable value is arms
	// (b)/(c)/(d). One identity implementation, two consumers — do NOT
	// re-implement identity comparisons here.
	if dataset.SharesIdentity(ex) {
		reasons = append(reasons, "shares hard identity (ASIN / version-group / path)")
	}
	// Arm (b) — landed in a duplicate-likely band.
	if ex.Band == "CERTAIN" || ex.Band == "HIGH" {
		reasons = append(reasons, "duplicate-likely band ("+ex.Band+")")
	}
	// Arm (c) — near-identical embedding cosine.
	if ex.Similarity != nil && *ex.Similarity >= 0.95 {
		reasons = append(reasons, "cosine similarity >= 0.95")
	}
	// Arm (d) — same title with the ms/sec duration-ratio signature (~0.001).
	if ex.A.Title != "" && ex.A.Title == ex.B.Title && ex.DurationRatio > 0 && ex.DurationRatio < 0.01 {
		reasons = append(reasons, "ms/sec duration-ratio signature (identical title)")
	}
	return reasons
}

// ListSuspiciousDedupLabels handles GET /api/v1/dedup/labels/suspicious — the
// suspicious-label review queue. It loads every rule-sourced not_dup label and
// surfaces only those carrying duplicate-shaped evidence (see suspicionReasons),
// so a reviewer can one-click override them via the existing
// POST /dedup/labels/:id/override route. Read-only: it never mutates a label.
//
// The suspicion predicate is an in-handler filter (not a store index) over the
// not_dup rows — trivial per-row string compares at ~7k-row dataset scale.
// Pagination mirrors ListDedupLabels (limit/offset), applied to the filtered
// set; total is the count of suspicious rows for the same predicate.
func (h *Handler) ListSuspiciousDedupLabels(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	// Limit:0 = load all not_dup rows; the suspicion predicate + pagination are
	// applied in-handler below.
	all, err := es.ListLabeledExamples(database.LabeledExampleFilter{Label: labelNotDup, Limit: 0})
	if err != nil {
		httputil.InternalError(c, "failed to list labeled examples", err)
		return
	}

	suspicious := make([]suspiciousRow, 0)
	for _, ex := range all {
		if !isSuspiciousLabel(ex) {
			continue
		}
		suspicious = append(suspicious, suspiciousRow{LabeledExample: ex, SuspicionReasons: suspicionReasons(ex)})
	}

	total := len(suspicious)
	limit := clampAtoi(c.Query("limit"), 50, 1, 500)
	offset := clampAtoi(c.Query("offset"), 0, 0, 1<<31)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}

	httputil.RespondWithOK(c, gin.H{
		"labels": suspicious[offset:end],
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GetDedupLabelStats handles GET /api/v1/dedup/labels/stats — counts by label and by
// label_source, so the review UI can show the dataset composition at a glance.
func (h *Handler) GetDedupLabelStats(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}
	// Count the explicit labels. NOTE: an empty Label means "no filter" (matches
	// all), so "unlabeled" is DERIVED from total minus the explicit counts — a
	// Label:"" query would wrongly return everything.
	byLabel := map[string]int{}
	labeledSum := 0
	for _, l := range []string{"true_dup", "not_dup", "unsure"} {
		n, err := es.CountLabeledExamples(database.LabeledExampleFilter{Label: l})
		if err != nil {
			httputil.InternalError(c, "failed to count labels", err)
			return
		}
		byLabel[l] = n
		labeledSum += n
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
	byLabel["unlabeled"] = total - labeledSum
	httputil.RespondWithOK(c, gin.H{"total": total, "by_label": byLabel, "by_source": bySource})
}

// OverrideDedupLabel handles POST /api/v1/dedup/labels/:id/override — a human
// reviewer setting/correcting the label on an example. The override always sets
// label_source="human" (this is gold) and re-stamps DecidedAt, so reviewer
// corrections take precedence over rule/auto-mined labels.
func (h *Handler) OverrideDedupLabel(c *gin.Context) {
	es := h.embeddingStore
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

// ExportLabeledExamples handles GET /api/v1/dedup/labels/export (C7).
//
// Streams every dedup:label: labeled example as JSONL (one JSON object per
// line), including the formula/feature version, for offline dataset analysis
// and training. Read-only — it only reads via ListLabeledExamples, never
// mutates a label.
//
// Query params (all optional, mirror LabeledExampleFilter — empty means "no
// filter"): label, label_source, band, folder_relation, signature_relation.
// Unlike ListDedupLabels this endpoint is unpaginated: it exports every row
// matching the filter. By default rows are collapsed to one per canonical
// book-pair (INIT-1 T3); pass raw=true to skip the collapse and stream every
// stored row.
func (h *Handler) ExportLabeledExamples(c *gin.Context) {
	es := h.embeddingStore
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
	}

	items, err := es.ListLabeledExamples(filter)
	if err != nil {
		httputil.InternalError(c, "failed to list labeled examples for export", err)
		return
	}

	// Collapse to one row per canonical book-pair by default (INIT-1 T3): the
	// dedup:label store keys by candidateID, so multi-layer pairs otherwise
	// export ~2.7× duplicate rows. raw=true is a debugging escape hatch that
	// streams every stored row without deduping.
	if c.Query("raw") != "true" {
		items = dataset.DedupeByPair(items)
	}

	filename := fmt.Sprintf("dedup-labels-%s.jsonl", time.Now().Format("20060102-150405"))
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("Content-Type", "application/x-ndjson")
	c.Status(http.StatusOK)

	enc := json.NewEncoder(c.Writer)
	flusher, canFlush := c.Writer.(http.Flusher)
	for _, ex := range items {
		if err := enc.Encode(ex); err != nil {
			// Client likely disconnected mid-stream; nothing else to do.
			return
		}
		if canFlush {
			flusher.Flush()
		}
	}
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
