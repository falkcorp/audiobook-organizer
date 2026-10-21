// file: internal/undo/user_content_follow.go
// version: 1.1.0
// guid: 5c0e6a52-3b6d-4f0e-9a43-8d1f2b7c4e91
// last-edited: 2026-10-21

package undo

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeUserContentFollow: a retire carried a user's own content from the
// retired book BookID onto the survivor, three kinds under one type, told
// apart by FieldName:
//
//   - "rating:<survivor>": the survivor's user ratings and notes went
//     OldValue -> NewValue (both RatingSnapshot JSON). The retire fills only
//     the fields the survivor left empty, so a rating the survivor already
//     carried is never overwritten. Restorable while the survivor still
//     holds NewValue exactly.
//   - "collection:<collection id>": the retired book was a member of that
//     static collection and the survivor took its place. OldValue is a
//     MembershipRecord (JSON), NewValue the survivor's id. Restorable: the
//     retired book goes back in the survivor's slot (or its old index when
//     the survivor was already a member).
//   - "playlist:<playlist id>": the same for a static playlist.
//   - "collection-held:<collection id>" and "playlist-held:<playlist id>":
//     a marker journaled on the SURVIVOR (BookID) when a retire only removed
//     the retired book because the survivor was already listed, so the
//     survivor now also stands in for it (NewValue is the retired book,
//     OldValue its MembershipRecord). It writes nothing and restores nothing.
//     Another operation's revert of the survivor's own slot reads it
//     (SurvivorStandsIn) so that it keeps the survivor listed rather than
//     replace it and lose the retired book's place.
//
// Database rows only; nothing on disk changes. A collection or playlist that
// has since been deleted has nothing left to restore and counts restored.
const ChangeTypeUserContentFollow = "user_content_follow"

// Kinds of a ChangeTypeUserContentFollow row, the prefix of its FieldName.
const (
	UserContentRating     = "rating"
	UserContentCollection = "collection"
	UserContentPlaylist   = "playlist"
	// UserContentCollectionHeld and UserContentPlaylistHeld are the
	// survivor-side markers described on ChangeTypeUserContentFollow.
	UserContentCollectionHeld = "collection-held"
	UserContentPlaylistHeld   = "playlist-held"
)

// UserContentField builds the FieldName of a ChangeTypeUserContentFollow row.
func UserContentField(kind, id string) string { return kind + ":" + id }

// UserContentFromField parses UserContentField; ok is false for an unknown
// kind or an empty id.
func UserContentFromField(field string) (kind, id string, ok bool) {
	kind, id, found := strings.Cut(field, ":")
	if !found || id == "" {
		return "", "", false
	}
	switch kind {
	case UserContentRating, UserContentCollection, UserContentPlaylist, UserContentCollectionHeld, UserContentPlaylistHeld:
		return kind, id, true
	}
	return "", "", false
}

// RatingSnapshot is a book's four user-rating fields. A nil field is unset.
type RatingSnapshot struct {
	Overall     *float64 `json:"overall,omitempty"`
	Story       *float64 `json:"story,omitempty"`
	Performance *float64 `json:"performance,omitempty"`
	Notes       *string  `json:"notes,omitempty"`
}

// RatingOf reads b's rating fields.
func RatingOf(b *database.Book) RatingSnapshot {
	return RatingSnapshot{Overall: b.UserRatingOverall, Story: b.UserRatingStory, Performance: b.UserRatingPerformance, Notes: b.UserRatingNotes}
}

// Apply writes s onto b's rating fields (nil clears).
func (s RatingSnapshot) Apply(b *database.Book) {
	b.UserRatingOverall, b.UserRatingStory, b.UserRatingPerformance, b.UserRatingNotes = s.Overall, s.Story, s.Performance, s.Notes
}

// Equal reports whether two snapshots hold the same values (a nil field
// equals only nil).
func (s RatingSnapshot) Equal(o RatingSnapshot) bool {
	return floatPtrEqual(s.Overall, o.Overall) && floatPtrEqual(s.Story, o.Story) &&
		floatPtrEqual(s.Performance, o.Performance) && strPtrEqual(s.Notes, o.Notes)
}

// Empty reports whether no field is set.
func (s RatingSnapshot) Empty() bool { return s.Equal(RatingSnapshot{}) }

// FillFrom returns s with every unset field taken from from: the survivor
// keeps what it has and only gains what it lacked. An empty notes string
// counts as unset.
func (s RatingSnapshot) FillFrom(from RatingSnapshot) RatingSnapshot {
	if s.Overall == nil {
		s.Overall = from.Overall
	}
	if s.Story == nil {
		s.Story = from.Story
	}
	if s.Performance == nil {
		s.Performance = from.Performance
	}
	if s.Notes == nil || *s.Notes == "" {
		if from.Notes != nil && *from.Notes != "" {
			s.Notes = from.Notes
		}
	}
	return s
}

func floatPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// EncodeRating renders s as a row value.
func EncodeRating(s RatingSnapshot) (string, error) {
	raw, err := json.Marshal(s)
	return string(raw), err
}

// DecodeRating parses a row value.
func DecodeRating(v string) (RatingSnapshot, error) {
	var s RatingSnapshot
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return s, fmt.Errorf("%s rating %q: %w", ChangeTypeUserContentFollow, v, err)
	}
	return s, nil
}

// RestoreRating is the compare-and-set for a rating row, shared by the revert
// and the preflight. A row owns only the fields it changed (the ones where its
// OldValue and NewValue differ), so a later operation's fill of another field
// of the same survivor does not block, and is not undone by, reverting this
// one. For each owned field the survivor now holds cur: the row's NewValue
// (put OldValue back), already OldValue (nothing to do), or anything else (a
// person rated the book since: the whole row is refused and nothing is
// written). It returns the ratings to write; ErrAlreadyRestored when no owned
// field needed restoring.
func RestoreRating(cur RatingSnapshot, c *database.OperationChange) (RatingSnapshot, error) {
	oldS, err := DecodeRating(c.OldValue)
	if err != nil {
		return cur, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: err.Error()}
	}
	newS, err := DecodeRating(c.NewValue)
	if err != nil {
		return cur, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: err.Error()}
	}
	out := cur
	restored := false
	// field restores one owned field: set puts the old value into out.
	field := func(name string, owned, atNew, atOld bool, set func()) error {
		switch {
		case !owned:
			return nil
		case atNew:
			set()
			restored = true
			return nil
		case atOld:
			return nil
		}
		return refuse(ReasonChangedSince, "book %s %s rating changed since the retire carried it", c.BookID, name)
	}
	for _, f := range []error{
		field("overall", !floatPtrEqual(oldS.Overall, newS.Overall), floatPtrEqual(cur.Overall, newS.Overall), floatPtrEqual(cur.Overall, oldS.Overall), func() { out.Overall = oldS.Overall }),
		field("story", !floatPtrEqual(oldS.Story, newS.Story), floatPtrEqual(cur.Story, newS.Story), floatPtrEqual(cur.Story, oldS.Story), func() { out.Story = oldS.Story }),
		field("performance", !floatPtrEqual(oldS.Performance, newS.Performance), floatPtrEqual(cur.Performance, newS.Performance), floatPtrEqual(cur.Performance, oldS.Performance), func() { out.Performance = oldS.Performance }),
		field("notes", !strPtrEqual(oldS.Notes, newS.Notes), strPtrEqual(cur.Notes, newS.Notes), strPtrEqual(cur.Notes, oldS.Notes), func() { out.Notes = oldS.Notes }),
	} {
		if f != nil {
			return cur, f
		}
	}
	if !restored {
		return cur, ErrAlreadyRestored
	}
	return out, nil
}

// CheckRatingFollowCurrent is RestoreRating's verdict on survivor's ratings
// without the result, for the preflight: nil when the row would restore,
// ErrAlreadyRestored, or a ReasonChangedSince refusal.
func CheckRatingFollowCurrent(survivor *database.Book, c *database.OperationChange) error {
	_, err := RestoreRating(RatingOf(survivor), c)
	return err
}

// MembershipRecord is the OldValue of a collection or playlist row: where the
// retired book stood in the list and whether the survivor was already in it.
type MembershipRecord struct {
	// Index is the retired book's position when the retire ran.
	Index int `json:"index"`
	// SurvivorWasMember: the survivor was already listed, so the retire only
	// removed the retired book and the revert puts it back at Index.
	SurvivorWasMember bool `json:"survivor_was_member"`
}

// EncodeMembership renders r as a row value.
func EncodeMembership(r MembershipRecord) (string, error) {
	raw, err := json.Marshal(r)
	return string(raw), err
}

// DecodeMembership parses a row value; a negative index is malformed.
func DecodeMembership(v string) (MembershipRecord, error) {
	var r MembershipRecord
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return r, fmt.Errorf("%s membership %q: %w", ChangeTypeUserContentFollow, v, err)
	}
	if r.Index < 0 {
		return r, fmt.Errorf("%s membership %q: negative index", ChangeTypeUserContentFollow, v)
	}
	return r, nil
}

// CarryMembership is the retire's write on a book id list: the retired book
// is replaced, in its slot, by the survivor, or just removed when the
// survivor is already listed. It returns the new list and the record the
// revert needs; ok is false when the retired book is not in the list (nothing
// to carry).
func CarryMembership(cur []string, retired, survivor string) (next []string, rec MembershipRecord, ok bool) {
	i := slices.Index(cur, retired)
	if i < 0 {
		return cur, MembershipRecord{}, false
	}
	rec = MembershipRecord{Index: i, SurvivorWasMember: slices.Contains(cur, survivor)}
	next = slices.Clone(cur)
	if rec.SurvivorWasMember {
		return slices.Delete(next, i, i+1), rec, true
	}
	next[i] = survivor
	return next, rec, true
}

// RestoreMembership is the revert of CarryMembership on the list as it is
// now. ErrAlreadyRestored when the retired book is listed again; a
// ReasonChangedSince refusal when the survivor was put in the retired book's
// slot and has since been taken out (the owner removed it on purpose).
//
// keepSurvivor is for a survivor that another, still standing retire also
// folded into this list (SurvivorStandsIn): the survivor is then the only
// entry that retire left for its own book, so the retired book goes back at
// its slot and the survivor stays listed beside it, instead of being replaced
// and taking that other book's place with it.
func RestoreMembership(cur []string, retired, survivor string, rec MembershipRecord, keepSurvivor bool) ([]string, error) {
	if slices.Contains(cur, retired) {
		return nil, ErrAlreadyRestored
	}
	next := slices.Clone(cur)
	if rec.SurvivorWasMember || keepSurvivor {
		if keepSurvivor && !slices.Contains(next, survivor) {
			return nil, refuse(ReasonChangedSince, "book %s is no longer listed where the retire put it", survivor)
		}
		at := min(rec.Index, len(next))
		return slices.Insert(next, at, retired), nil
	}
	j := slices.Index(next, survivor)
	if j < 0 {
		return nil, refuse(ReasonChangedSince, "book %s is no longer listed where the retire put it", survivor)
	}
	next[j] = retired
	return next, nil
}

// CheckMembershipFollow is what the revert of a collection or playlist row
// would do to the list as it is now, for the preflight: nil when it would put
// the retired book back, ErrAlreadyRestored, or a ReasonChangedSince
// refusal. The survivor-side markers (SurvivorStandsIn) change a revert only
// from a refusal-free replace to a keep, so they are not read here.
func CheckMembershipFollow(cur []string, c *database.OperationChange) error {
	rec, err := DecodeMembership(c.OldValue)
	if err != nil {
		return &ReferentError{Reason: ReasonOldValueUnparsable, Detail: err.Error()}
	}
	_, err = RestoreMembership(cur, c.BookID, c.NewValue, rec, false)
	return err
}

// HeldKind is the marker kind that goes with a collection or playlist row kind.
func HeldKind(kind string) string { return kind + "-held" }

// SurvivorStandsIn reports whether, among the survivor's journal rows
// (GetBookChanges of the survivor), another operation than thisOp left a
// standing marker that the survivor also holds list listID of kind (rows with
// RevertedAt set, or voided, no longer count). A standing marker means the
// survivor's entry in that list is also the place of a book that operation
// retired into it.
func SurvivorStandsIn(survivorRows []*database.OperationChange, thisOp, kind, listID string) bool {
	want := UserContentField(HeldKind(kind), listID)
	for _, r := range survivorRows {
		if r.ChangeType == ChangeTypeUserContentFollow && r.FieldName == want &&
			r.OperationID != thisOp && r.RevertedAt == nil && !r.Voided {
			return true
		}
	}
	return false
}

func validUserContentFollowRow(c *database.OperationChange) bool {
	kind, _, ok := UserContentFromField(c.FieldName)
	if !ok {
		return false
	}
	if kind == UserContentRating {
		if _, err := DecodeRating(c.OldValue); err != nil {
			return false
		}
		_, err := DecodeRating(c.NewValue)
		return err == nil
	}
	if c.NewValue == "" {
		return false
	}
	_, err := DecodeMembership(c.OldValue)
	return err == nil
}
