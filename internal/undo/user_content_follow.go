// file: internal/undo/user_content_follow.go
// version: 1.0.0
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
//
// Database rows only; nothing on disk changes. A collection or playlist that
// has since been deleted has nothing left to restore and counts restored.
const ChangeTypeUserContentFollow = "user_content_follow"

// Kinds of a ChangeTypeUserContentFollow row, the prefix of its FieldName.
const (
	UserContentRating     = "rating"
	UserContentCollection = "collection"
	UserContentPlaylist   = "playlist"
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
	case UserContentRating, UserContentCollection, UserContentPlaylist:
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

// CheckRatingFollowCurrent is the compare-and-set for a rating row, shared by
// the revert and the preflight: nil while the survivor still holds the row's
// NewValue (put OldValue back), ErrAlreadyRestored when it already holds
// OldValue, and a ReasonChangedSince refusal when it holds anything else (a
// person rated the book since).
func CheckRatingFollowCurrent(survivor *database.Book, c *database.OperationChange) error {
	oldS, err := DecodeRating(c.OldValue)
	if err != nil {
		return &ReferentError{Reason: ReasonOldValueUnparsable, Detail: err.Error()}
	}
	newS, err := DecodeRating(c.NewValue)
	if err != nil {
		return &ReferentError{Reason: ReasonOldValueUnparsable, Detail: err.Error()}
	}
	cur := RatingOf(survivor)
	switch {
	case cur.Equal(newS):
		return nil
	case cur.Equal(oldS):
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book %s ratings changed since the retire carried them", survivor.ID)
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
func RestoreMembership(cur []string, retired, survivor string, rec MembershipRecord) ([]string, error) {
	if slices.Contains(cur, retired) {
		return nil, ErrAlreadyRestored
	}
	next := slices.Clone(cur)
	if rec.SurvivorWasMember {
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
