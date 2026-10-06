// file: internal/metafetch/candidate_pin.go
// version: 1.15.0
// guid: 9f4a1d63-2c7e-4b85-a0d9-5e3b8c1f6a42
// last-edited: 2026-10-06

package metafetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Pin origins that earn an owner-review override. Owner ruling 2026-09-27:
// EVERY apply button on the /review page is the owner's manual apply and
// overrides the certainty gate the same way, not only the single-row Apply.
// Scripts and API callers without one of these origins, and automatic applies
// (the metadata upgrade, scheduled ops), stay fully gated. A pin with any
// other origin is checked for staleness but gets the ordinary hard gate.
const (
	// PinOriginRow: the owner clicked Apply on ONE review row, looking at
	// that row's candidate. Always carries the row's candidate_hash.
	PinOriginRow = "row"
	// PinOriginReviewBulk: a review-page bulk button (Apply selected, Apply
	// page, Apply high confidence, group Apply All). For a book whose row the
	// lane has loaded it carries that row's candidate_hash and identity, and
	// is then checked for staleness exactly like a row pin. For a book the
	// lane has no loaded row or hash for (the selection outlived a refresh
	// that dropped the row, or the row was served without a hash) it is the
	// hashless OWNER MARKER: IsUnseenOwnerReview.
	PinOriginReviewBulk = "review_bulk"
)

// Bulk apply modes for the review page's bulk buttons (owner ruling
// 2026-09-27: a toggle next to the bulk buttons). The mode is a property of
// the REQUEST (server.batchApplyOpParams.Mode), and it acts only on books
// carrying a PinOriginReviewBulk pin: a single-row pin already overwrites,
// and a book with no owner-review pin (a script, an API caller, an automatic
// apply) stays fill-only and fully gated whatever mode the request names.
// The dry-run preview never receives a mode.
const (
	// BulkApplyModeFill: fill empty fields only (owner decision A3#3). The
	// default; the empty string means the same.
	BulkApplyModeFill = "fill"
	// BulkApplyModeReplace: a review_bulk-pinned book overwrites filled
	// descriptive fields, and its change history records the owner replace.
	BulkApplyModeReplace = "replace"
)

// NormalizeBulkApplyMode maps a requested mode to its canonical form: "" for
// fill (so a fill request stays byte-identical to one that named no mode) and
// BulkApplyModeReplace for replace. ok is false for any other value, which a
// caller must refuse rather than guess at.
func NormalizeBulkApplyMode(mode string) (normalized string, ok bool) {
	switch mode {
	case "", BulkApplyModeFill:
		return "", true
	case BulkApplyModeReplace:
		return BulkApplyModeReplace, true
	}
	return "", false
}

// CandidatePin identifies the cached candidate a reviewer was LOOKING AT when
// they clicked Apply. The review lane shows the top cached candidate
// (Candidates[0], GetCacheReviewResults) and the batch apply applies the top
// cached candidate, so they are the same row unless the cache was refetched
// in between. The pin is how the server tells.
//
// ContentHash (CandidateHash of the candidate the review list served) is what
// makes the pin strong: two provider records with no ASIN/ISBN, the same
// source and the same title would otherwise satisfy each other's pin. The
// identity fields are kept alongside so a refusal can say what changed.
//
// A refetch replaces the cache row wholesale, so strict equality is the right
// test: a different record, or the same record with any field changed, is not
// what the owner reviewed.
type CandidatePin struct {
	Origin      string `json:"origin,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Source      string `json:"source"`
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	ASIN        string `json:"asin,omitempty"`
	ISBN        string `json:"isbn,omitempty"`
	ISBN10      string `json:"isbn10,omitempty"`
	ISBN13      string `json:"isbn13,omitempty"`
}

// CandidateHash is the hex SHA-256 of c's canonical JSON: encoding/json over
// the decoded struct, which emits fields in declaration order and map keys
// sorted, so the review list (which serves it) and the apply (which checks
// it) compute the same value from the same cache row. "" only if c cannot be
// encoded, which a decoded candidate always can.
func CandidateHash(c MetadataCandidate) string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PinOf is the pin that identifies c, content hash included. Origin is left
// empty: only the review lane sets an owner-review origin.
func PinOf(c MetadataCandidate) CandidatePin {
	return CandidatePin{ContentHash: CandidateHash(c), Source: c.Source, Title: c.Title, Author: c.Author,
		ASIN: c.ASIN, ISBN: c.ISBN, ISBN10: c.ISBN10, ISBN13: c.ISBN13}
}

// IsRowReview reports whether p records a single-row approval, whose apply
// always OVERWRITES filled fields (owner ruling 2026-09-14). Bulk pins lift
// the gate (IsOwnerReview) and stay fill-only (A3#3) unless the request asks
// for BulkApplyModeReplace (owner ruling 2026-09-27).
func (p CandidatePin) IsRowReview() bool { return p.Origin == PinOriginRow }

// IsOwnerReview reports whether p comes from an apply button on the review
// page (any origin above), and so makes the apply owner-reviewed.
func (p CandidatePin) IsOwnerReview() bool {
	return p.Origin == PinOriginRow || p.Origin == PinOriginReviewBulk
}

// IsUnseenOwnerReview reports whether p is the hashless owner marker: a
// review-page bulk button applied this book, but the lane held no candidate
// hash for it, so there is no candidate to check for staleness. It still
// makes the apply owner-reviewed (owner ruling 2026-09-27: every review-page
// apply button overrides the gate), with two limits, both enforced by the
// batch apply's planner (server.planCachedApply):
//
//   - it never lifts the owner's own "no match" mark: that mark is overridden
//     only by an owner who was looking at the candidate (a hash-checked pin);
//   - it is honoured only by the cached apply. The dry-run preview enqueues
//     no pins at all, and the op-results apply (planOpResultApply) takes none.
//
// A hashless pin of any other origin is not a marker: it never Matches, so it
// is refused as stale_candidate as before.
func (p CandidatePin) IsUnseenOwnerReview() bool {
	return p.Origin == PinOriginReviewBulk && p.ContentHash == ""
}

// Matches reports whether c is the candidate p was taken from: the content
// hash must be present and equal, and so must every identity field
// (whitespace at the ends ignored). A pin without a hash never matches.
func (p CandidatePin) Matches(c MetadataCandidate) bool {
	q := PinOf(c)
	if p.ContentHash == "" || p.ContentHash != q.ContentHash {
		return false
	}
	eq := func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }
	return eq(p.Source, q.Source) && eq(p.Title, q.Title) && eq(p.Author, q.Author) &&
		eq(p.ASIN, q.ASIN) && eq(p.ISBN, q.ISBN) && eq(p.ISBN10, q.ISBN10) && eq(p.ISBN13, q.ISBN13)
}

// ApplyOptions adjusts how ApplyMetadataCandidateWithOptions records an apply.
// The zero value is ApplyMetadataCandidate.
type ApplyOptions struct {
	// OwnerReviewed says the apply went through on an owner review although
	// the bulk-apply certainty gate refused it. It alone decides that the
	// override is recorded on every field's change-history row (in its
	// source), on the activity entries, and as an "owner_reviewed" version
	// note, and that a failed history write is returned to the caller. It
	// changes nothing about which fields are written.
	OwnerReviewed bool
	// GateOverride names the refusing reasons for those records. It is only a
	// label: an empty value on a reviewed apply records "owner_reviewed"
	// rather than skipping the history requirement or the note.
	GateOverride string
	// FillOnly makes the apply fill the book's descriptive fields without
	// overwriting any that already hold a value (StripFilledFields). Every
	// batch and automatic apply sets it (owner decision A3#3) except a
	// hand-picked one: the single-book apply, and a batch row the owner
	// approved in the review lane, whether the gate passed or refused it
	// (owner ruling 2026-09-14), and a review-page bulk apply in
	// BulkApplyModeReplace (OwnerReplace, owner ruling 2026-09-27), and the
	// nightly metadata upgrade when a higher-ranked source replaces a
	// lower-ranked one (RankUpgrade, owner ruling 2026-09-27), leave it
	// false and may overwrite. The rename
	// preflight and the bulk-apply preview must get the same value the apply
	// does. It is independent of OwnerReviewed, which only labels a lifted
	// gate refusal.
	//
	// With UnseenCandidate it is also what marks an apply as AUTOMATIC
	// (nobody picked this candidate, ApplyOptions.automatic): an automatic
	// apply records the match (review status, MetadataSource,
	// MetadataSourceHash) only when the book ends up holding the candidate's
	// title, while a hand-picked apply (FillOnly false, UnseenCandidate
	// false) records it whatever title the book keeps, because the person
	// asserted the match. A new automatic caller must set FillOnly, or
	// UnseenCandidate if it overwrites.
	//
	// It covers the DESCRIPTIVE fields only (StripFilledFields leaves the
	// identity fields alone), so it does not stop a title overwrite: a caller
	// that must not replace a filled title drops "title" from its fields
	// allowlist, as maintenance.auto-match-transcribed does.
	FillOnly bool
	// RefuseEmptyWrite makes the apply return ErrNothingToApply, writing
	// nothing (no review status, version note or source stamp), when the
	// fields allowlist, the fill-only strip and the field locks leave no
	// column for the candidate to change. An automatic apply that narrows its
	// allowlist sets it, so a match with nothing left to write is a skip
	// rather than a book stamped as matched. A hand-picked apply leaves it
	// false.
	//
	// The check runs after the apply body and counts both a changed book
	// column and a changed author credit list (AuthorCredits.Changed) as a
	// change: on a book that already has an author, an applied co-author adds
	// a book_authors credit, written under the store's lock, without changing
	// a column. That apply proceeds and commits (with history, so undo can
	// remove the credit) rather than reporting nothing to apply.
	RefuseEmptyWrite bool
	// OwnerReplace says the owner asked a review-page BULK button to replace
	// existing values (BulkApplyModeReplace) and this apply overwrites: the
	// caller sets FillOnly false with it. Like OwnerReviewed it changes no
	// field by itself; it labels the change history ("owner replace") and
	// makes a failed history write an error, since that history is the only
	// way to revert the overwrite.
	OwnerReplace bool
	// RankUpgrade labels an AUTOMATIC overwrite by the nightly metadata
	// upgrade (internal/metabatch): the book's metadata came from a
	// lower-ranked source (SourceOutranks) and a higher-ranked source's
	// candidate passed the bulk-apply gate, so the owner ruled on 2026-09-27
	// that it replaces existing values. The value is the reason recorded in
	// change history, e.g. "rank upgrade: open_library -> audible". Like
	// OwnerReplace it changes no field by itself: the caller sets FillOnly
	// false (and UnseenCandidate, since nobody picked the candidate), and a
	// failed history write is an error, since that history is the only way to
	// revert the overwrite. It is not OwnerReplace because no person pressed
	// anything, and history must not say one did.
	RankUpgrade string
	// UnseenCandidate says nobody was shown this candidate (the review page's
	// hashless bulk marker, CandidatePin.IsUnseenOwnerReview). An apply with
	// FillOnly false normally counts as HAND-PICKED: it overrides a "no
	// match" mark and records the match whatever title the book keeps.
	// UnseenCandidate keeps both of those automatic behaviours (see
	// automatic) while still letting the apply overwrite.
	UnseenCandidate bool
	// BatchID, when set, is the batch id every change-history row of this
	// apply carries, instead of a fresh random one. The queued single-book
	// apply (metadata.apply-when-scanned) fixes it when the apply is queued,
	// so a re-run after a restart can recognise its own rows (ApplyEditsSince)
	// and complete as "already applied" instead of applying twice.
	BatchID string
	// Guard, when set, is the caller's own check of the book row as it stands
	// under the book's write lock, inside the ModifyBook that commits the
	// apply (after the automatic apply's "no match" re-check). A non-nil
	// error refuses the commit: nothing is written and the error is returned
	// wrapped, so errors.Is still finds the caller's sentinel. The version
	// twin fixer (maintenance.version-twin-metadata) uses it to refuse a book
	// that stopped being its group's primary, left the group or was applied
	// while the apply ran. It is also run once on the row as first read,
	// before the apply body (which may create a Series row), so a row it
	// already refuses writes nothing at all. It runs under a book write
	// stripe, so it must be cheap: no full-table scan, no disk I/O.
	Guard func(fresh *database.Book) error
	// SkipHashElection skips the MATCH-4 duplicate election
	// (checkMetadataSourceHashDuplicates) after the commit. That election
	// sets merged_into_book_id and clears is_primary_version on every book
	// outside the survivor's version group that carries the same record, and
	// it runs with no guard and no iTunes check of its own. A caller sets it
	// when it has proved, under its own Guard, that no book outside the
	// target's version group carries the record (so the election has nothing
	// to decide) and must not demote or merge any book it was not asked to
	// write: the version twin fixer (maintenance.version-twin-metadata),
	// whose group's twin already carries the record.
	SkipHashElection bool
	// BookRowOnly limits the apply's writes to the book row, its author
	// credits and their change history: no metadata:source / metadata:language
	// system tags, no provider category tags, no fetched-value provenance
	// (field states), no segment titles, no identifier backfill queued and no
	// cover download handed back (PendingCoverURL stays ""). Everything it
	// writes is then in the history batch, so undoing that batch (UndoLastApply,
	// UndoApplyBatch, the op revert of a journaled apply) undoes all of it.
	// The version twin fixer sets it.
	BookRowOnly bool
	// HistorySource, when set, is the source every change-history row of
	// this apply records, instead of the candidate's provider (with the
	// owner-review / replace / upgrade notes). A Repairs fixer sets its own id
	// here so the history says which repair wrote the fields.
	HistorySource string
	// RequireHistory makes a failed change-history write an error returned
	// with the response, as OwnerReviewed, OwnerReplace and RankUpgrade do:
	// the caller's revert reads that history. The write itself stands.
	RequireHistory bool
}

// automatic reports whether nobody picked this candidate: a fill-only apply,
// or an overwriting one of a candidate nobody was shown. An automatic apply
// refuses a book marked "no match" (at the start and again at commit) and
// records the match only when the book ends up holding the candidate's title.
func (o ApplyOptions) automatic() bool { return o.FillOnly || o.UnseenCandidate }

// ErrNothingToApply is returned by an apply with RefuseEmptyWrite when no
// field would change. Nothing was written.
var ErrNothingToApply = errors.New("metadata apply: no field left to write")

// ErrMarkedNoMatch is returned by an automatic apply or fetch (nobody picked a
// candidate) for a book its owner marked "no match": the owner rejected every
// match, so nothing is written or searched. Bulk callers report the book as
// skipped, not failed. A person-picked apply is the owner overriding their own
// mark and is not refused.
var ErrMarkedNoMatch = errors.New("metadata: book is marked no match")

// IsMarkedNoMatch reports whether the owner marked this book "no match"
// (MetadataReviewStatus == "no_match", written by Service.MarkNoMatch).
func IsMarkedNoMatch(status *string) bool {
	return status != nil && *status == "no_match"
}

// overrideLabel is the refusing-reasons label recorded for an owner-reviewed
// apply, never empty.
func (o ApplyOptions) overrideLabel() string {
	if o.GateOverride == "" {
		return ownerReviewedLabel
	}
	return o.GateOverride
}

// ownerReviewedLabel matches applygate.ReasonOwnerReviewed; metafetch does
// not import applygate.
const ownerReviewedLabel = "owner_reviewed"

// ownerReplaceLabel is what change history records for an OwnerReplace apply.
const ownerReplaceLabel = "owner replace: review bulk apply replaced existing values"

// historySource is the source label change history records for an apply.
func (o ApplyOptions) historySource(candidateSource string) string {
	if o.HistorySource != "" {
		return o.HistorySource
	}
	if !o.OwnerReviewed && !o.OwnerReplace && o.RankUpgrade == "" {
		return candidateSource
	}
	src := candidateSource
	if src == "" {
		src = "unknown source"
	}
	var notes []string
	if o.OwnerReviewed {
		notes = append(notes, "owner-reviewed; certainty gate overridden: "+o.overrideLabel())
	}
	if o.OwnerReplace {
		notes = append(notes, ownerReplaceLabel)
	}
	if o.RankUpgrade != "" {
		notes = append(notes, o.RankUpgrade)
	}
	return src + " (" + strings.Join(notes, "; ") + ")"
}

// requiresHistory reports whether a failed change-history write must be
// returned to the caller: the apply lifted a gate refusal or overwrote filled
// fields, and its history is the only record to audit or revert it from.
func (o ApplyOptions) requiresHistory() bool {
	return o.OwnerReviewed || o.OwnerReplace || o.RankUpgrade != "" || o.RequireHistory
}

// historyKind names the apply in a failed-history error.
func (o ApplyOptions) historyKind() string {
	if o.OwnerReviewed {
		return "owner-reviewed"
	}
	if o.OwnerReplace {
		return "owner-replace"
	}
	if o.RankUpgrade != "" {
		return "rank-upgrade"
	}
	return "history-required"
}
