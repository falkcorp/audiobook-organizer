// file: internal/server/handlers/entities/author_ref_audit.go
// version: 1.0.0
// guid: 7c3e1a58-9d24-4f6b-8e05-2b71c9d4a3f8
// last-edited: 2026-08-24

package entities

import (
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// authorRefBucket is one classified author id.
type authorRefBucket struct {
	ID int `json:"id"`
	// CanonicalID is set only for the "tombstoned" bucket: the author this id
	// redirects to.
	CanonicalID int `json:"canonical_id,omitempty"`
	// Name is set when the id resolves to a real author, so a caller can sanity
	// check that "live" really means live.
	Name string `json:"name,omitempty"`
}

type authorRefAuditResponse struct {
	Live       []authorRefBucket `json:"live"`
	Tombstoned []authorRefBucket `json:"tombstoned"`
	Dangling   []int             `json:"dangling"`
	Counts     map[string]int    `json:"counts"`
	Requested  int               `json:"requested"`
	Deduped    int               `json:"deduped"`
}

// AuditAuthorRefs classifies a caller-supplied list of author ids into three
// buckets, so a dangling-`book.AuthorID` population can be scoped before anyone
// attempts a repair. Read-only: it performs lookups and writes nothing.
//
// Implements GET /authors/ref-audit?ids=1,2,3.
//
// Why this exists. `book.AuthorID` is a denormalized pointer that `DeleteAuthor`
// does not sweep, so handlers that delete an author can leave it dangling. An
// audit that merely asks "is this id in the live author list?" OVERCOUNTS the
// damage, because `GetAuthorByID` follows a tombstone redirect
// (pebble_store_authors.go:51-60): a book pointing at a MERGED-AWAY author still
// resolves to the canonical one and renders correctly. Those rows are untidy,
// not broken, and repairing them would be pointless writes.
//
// The three buckets are therefore:
//
//	live       - the author row exists. Nothing wrong with this reference.
//	tombstoned - the row is gone but a tombstone redirects it. SELF-HEALING on
//	             read; safe to leave, and a repair should skip these.
//	dangling   - no row and no tombstone. GENUINELY BROKEN: on the memdb list
//	             path this renders as no author at all.
//
// Only the `dangling` bucket is repair scope. Note that repair must REPOINT:
// `UpdateBook` preserves `Author` on nil (pebble_store.go:2407), so "no author"
// cannot be expressed by clearing.
//
// The distinction is deliberately drawn from `GetAuthorTombstone` FIRST rather
// than inferred from a nil `GetAuthorByID`, because `GetAuthorByID` follows the
// redirect and so cannot tell "live" from "tombstoned" by itself -- both return
// a non-nil author. Asking the tombstone directly is the only way to separate
// them; see the ordering in classifyAuthorRef below.
func (h *Handler) AuditAuthorRefs(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	raw := strings.TrimSpace(c.Query("ids"))
	if raw == "" {
		httputil.RespondWithBadRequest(c, "ids query parameter is required (comma-separated author ids)")
		return
	}

	parts := strings.Split(raw, ",")
	// Cap the request rather than letting a caller trigger an unbounded number
	// of store lookups from one URL.
	const maxIDs = 5000
	if len(parts) > maxIDs {
		httputil.RespondWithBadRequest(c, "too many ids: at most "+strconv.Itoa(maxIDs)+" per request")
		return
	}

	seen := make(map[int]struct{}, len(parts))
	ids := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.Atoi(p)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid author id: "+p)
			return
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		httputil.RespondWithBadRequest(c, "no usable author ids in the ids parameter")
		return
	}

	resp := authorRefAuditResponse{
		Live:       []authorRefBucket{},
		Tombstoned: []authorRefBucket{},
		Dangling:   []int{},
		Requested:  len(parts),
		Deduped:    len(ids),
	}

	// Sequential on purpose: this is a diagnostic capped at maxIDs, and the
	// lookups are Pebble point-gets. The whole-library concurrency rule targets
	// full-collection scans doing meaningful per-item work; neither applies to a
	// bounded, operator-triggered classification.
	for _, id := range ids {
		bucket, kind, err := h.classifyAuthorRef(id)
		if err != nil {
			httputil.InternalError(c, "failed to classify author "+strconv.Itoa(id), err)
			return
		}
		switch kind {
		case refLive:
			resp.Live = append(resp.Live, bucket)
		case refTombstoned:
			resp.Tombstoned = append(resp.Tombstoned, bucket)
		default:
			resp.Dangling = append(resp.Dangling, id)
		}
	}

	resp.Counts = map[string]int{
		"live":       len(resp.Live),
		"tombstoned": len(resp.Tombstoned),
		"dangling":   len(resp.Dangling),
	}
	httputil.RespondWithOK(c, resp)
}

type authorRefKind int

const (
	refDangling authorRefKind = iota
	refLive
	refTombstoned
)

// classifyAuthorRef decides which bucket one author id belongs to.
//
// Order matters. The tombstone is checked FIRST because GetAuthorByID follows
// the redirect: for a tombstoned id it returns the CANONICAL author, which is
// indistinguishable from a live one at the call site. Checking the tombstone
// first is what separates "this reference is fine as-is" from "this reference
// only works because something redirects it."
func (h *Handler) classifyAuthorRef(id int) (authorRefBucket, authorRefKind, error) {
	canonicalID, err := h.store.GetAuthorTombstone(id)
	if err != nil {
		return authorRefBucket{}, refDangling, err
	}
	if canonicalID != 0 {
		b := authorRefBucket{ID: id, CanonicalID: canonicalID}
		// Name the target when it resolves, so a tombstone pointing at a
		// SECOND deleted author is visible rather than silently counted as
		// self-healing.
		if target, tErr := h.store.GetAuthorByID(canonicalID); tErr == nil && target != nil {
			b.Name = target.Name
		}
		return b, refTombstoned, nil
	}

	author, err := h.store.GetAuthorByID(id)
	if err != nil {
		return authorRefBucket{}, refDangling, err
	}
	if author != nil {
		return authorRefBucket{ID: id, Name: author.Name}, refLive, nil
	}
	return authorRefBucket{ID: id}, refDangling, nil
}
