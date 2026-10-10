// file: internal/applygate/owner_rejected.go
// version: 1.0.0
// guid: 92c7d027-0282-4730-a950-0ffe326f0c24
// last-edited: 2026-10-10

package applygate

import "github.com/falkcorp/audiobook-organizer/internal/metafetch"

// The owner-rejected leg (2026-10-10). A candidate the owner rejected (POST
// /metadata/batch-reject-candidates) is never applied by a bulk or automatic
// path, and no owner-review pin lifts the refusal: a review-page apply button
// sends a pin for whatever the lane showed, and until this leg the gate never
// read the rejections at all, so a rejected candidate sitting in slot 0 was
// applied by a script's batch-apply-cached, by every review-page apply button,
// by batch-apply-candidates against an older op, and by the nightly upgrade
// (which overwrites filled fields). To apply a rejected candidate the owner
// un-rejects it first (POST /metadata/batch-unreject-candidates).
//
// The leg is a backstop: the rejection also re-orders the book's cached row
// so the rejected candidate leaves slot 0 (metafetch.RerankCachedCandidates),
// and the paths without this gate (the transcription auto-match, the
// bulk fetch) check the rejection themselves.

const (
	// ReasonOwnerRejected: the owner rejected this candidate for this book.
	// Hard: no owner-review pin lifts it.
	ReasonOwnerRejected = "owner_rejected"
	// ReasonOwnerRejectionCheckFailed: the owner's rejections could not be
	// read, so the gate cannot tell whether this candidate was rejected.
	// Hard, and never read as "not rejected".
	ReasonOwnerRejectionCheckFailed = "owner_rejection_check_failed"
)

// Rejections is the owner-rejected leg's input for one book, built by the
// caller from the store (LoadRejections) the way claims are: the gate reads
// no store itself. nil skips the leg -- only a single-book caller with no
// store does that; every bulk planner passes one.
type Rejections struct {
	// Keys is the book's rejections in matching form.
	Keys metafetch.RejectedSet
	// ReadErr is the failed read, refused as owner_rejection_check_failed.
	ReadErr error
}

// LoadRejections reads bookID's rejections for the gate.
func LoadRejections(r metafetch.RejectedCandidateReader, bookID string) *Rejections {
	keys, err := metafetch.LoadRejectedCandidates(r, bookID)
	return &Rejections{Keys: keys, ReadErr: err}
}

// refusal is the leg's verdict for c: "" when it passes.
func (r *Rejections) refusal(c *metafetch.MetadataCandidate) (reason, detail string) {
	switch {
	case r == nil || c == nil:
		return "", ""
	case r.ReadErr != nil:
		return ReasonOwnerRejectionCheckFailed, r.ReadErr.Error()
	case r.Keys.HasCandidate(c):
		return ReasonOwnerRejected, "the owner rejected " + c.Source + " candidate \"" + c.Title +
			"\" for this book; un-reject it to apply it"
	}
	return "", ""
}
