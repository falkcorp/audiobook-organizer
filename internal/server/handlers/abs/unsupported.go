// file: internal/server/handlers/abs/unsupported.go
// version: 1.0.0
// guid: cab80f73-9963-4b9d-9810-0f6c1f26a599
// last-edited: 2026-09-19

package abs

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ── ABS endpoints this server answers honestly without implementing ─────────
//
// Both of these used to fall through to the /api/* → /api/v1/* compatibility
// redirect (item-6 decode matrix, 2026-09-19). A 301 is the worst answer either
// could give: the client follows it, a POST is downgraded to a GET, and the user
// sees a generic failure (or nothing) with no hint why. An explicit status with a
// message tells the client — and anyone reading a log — what is actually true.

// NarratorImage handles GET/HEAD /api/narrators/:id/image.
//
// There is no narrator image to serve: database.Narrator carries only an id and
// a name, and no import or enrichment path stores a narrator portrait. So this is
// a plain 404, which the app renders as its placeholder. Before this route it was
// a 301 into /api/v1/narrators/:id/image followed by a 404 there — the same end
// state reached through a redirect into a different API. If narrator images are
// ever stored, serve them here the way ItemCover serves covers.
func (h *Handler) NarratorImage(c *gin.Context) {
	respondError(c, http.StatusNotFound, "narrator image not found")
}

// SendEbookToDevice handles POST /api/emails/send-ebook-to-device.
//
// Upstream ABS emails an ebook to a configured e-reader address. This server has
// no outbound email and no e-reader devices — /login and /api/authorize report
// ereaderDevices: [] — so the request cannot succeed. 400 with upstream's own
// wording for the no-device case, as plain text like upstream's error bodies, so
// the app surfaces a real error instead of silently re-sending the POST as a GET.
func (h *Handler) SendEbookToDevice(c *gin.Context) {
	c.String(http.StatusBadRequest,
		"Ereader device not found: this server has no e-reader devices or outbound email configured")
}
