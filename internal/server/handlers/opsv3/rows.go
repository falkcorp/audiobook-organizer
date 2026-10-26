// file: internal/server/handlers/opsv3/rows.go
// version: 1.1.0
// guid: 39018ebb-a7f9-40a8-8aef-6a3cd4889ce6
// last-edited: 2026-10-25

package opsv3

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// RowView is one item outcome of a run.
type RowView struct {
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
	Chunk   int    `json:"chunk"`
}

// ListRows implements GET /api/v3/ops/runs/:id/rows?outcome=&limit=&cursor=:
// the failed and skipped items a run recorded. total is the exact number of
// stored rows matching outcome (a prefix: "skipped" matches every
// skipped:<reason>). truncated is true when the per-run cap dropped some
// outcomes, so total then counts the stored rows, not every item that failed.
func (h *Handler) ListRows(c *gin.Context) {
	rd, ok := h.runs(c)
	if !ok {
		return
	}
	limit, ok := limitParam(c)
	if !ok {
		return
	}
	id := c.Param("id")
	rec, err := rd.GetRunV3(id)
	if err != nil {
		httputil.InternalError(c, "get run", err)
		return
	}
	if rec == nil {
		httputil.RespondWithNotFound(c, "run", id)
		return
	}
	if !h.mayReadRun(c, rec) {
		return
	}
	page, err := rd.ListItemOutcomesV3(id, database.ItemQuery{
		Outcome: c.Query("outcome"), Cursor: c.Query("cursor"), Limit: limit,
	})
	if err != nil {
		storeErr(c, "list rows", err)
		return
	}
	items := make([]RowView, 0, len(page.Rows))
	for _, r := range page.Rows {
		items = append(items, RowView{Key: r.Key, Outcome: r.Outcome, Error: r.Error, Chunk: r.Chunk})
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items, "next_cursor": page.Next, "total": page.Total, "truncated": page.Truncated,
	})
}
