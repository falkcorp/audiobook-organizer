// file: internal/server/batch_hard_delete_guard.go
// version: 1.0.0
// guid: a379e956-6a85-4098-a52f-3a4d2ee2db38
// last-edited: 2026-10-25

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// batchOperationsBodyLimit bounds the request body the guard buffers. The
// handler caps the operation COUNT (BulkApplyMaxItems); this only keeps the
// guard from buffering an unbounded body to learn whether one of them is a
// hard delete.
const batchOperationsBodyLimit = 32 << 20

// batchHardDeleteGuard makes POST /audiobooks/batch-operations require
// library.delete for a request that asks for a hard delete.
//
// The route is guarded by library.edit_metadata, which is right for update,
// soft delete and restore. A hard delete is the same act as
// DELETE /audiobooks/:id?hard_delete=true, which requires library.delete, so a
// role holding edit_metadata without delete could otherwise permanently
// delete books (and carry listening state to a sibling) through the batch
// route. The guard reads the body once, restores it for the handler, and when
// any operation is {action: "delete", hard_delete: true} applies the same
// library.delete check the single-delete route uses, through s.permCheck (the
// same CheckPermission semantics as s.perm: a no-op when auth is disabled,
// the same first-run bypass, 401 with no user and 403 naming the permission).
//
// A body that does not parse is passed through: the handler answers it with
// 400 and runs no operation, so nothing is deleted.
func (s *Server) batchHardDeleteGuard() gin.HandlerFunc {
	holds := s.permCheck()
	return func(c *gin.Context) {
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, batchOperationsBodyLimit))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "REQUEST_TOO_LARGE")
			} else {
				httputil.RespondWithBadRequest(c, "could not read request body")
			}
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		var req struct {
			Operations []struct {
				Action     string `json:"action"`
				HardDelete bool   `json:"hard_delete"`
			} `json:"operations"`
		}
		if json.Unmarshal(body, &req) != nil {
			return
		}
		for _, op := range req.Operations {
			if op.Action == "delete" && op.HardDelete {
				if !holds(c, auth.PermLibraryDelete) {
					c.Abort()
				}
				return
			}
		}
	}
}
