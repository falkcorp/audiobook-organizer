// file: internal/undo/user_content_follow.go
// version: 1.9.0
// guid: 5c0e6a52-3b6d-4f0e-9a43-8d1f2b7c4e91
// last-edited: 2026-10-22

package undo

import (
	"encoding/json"
	"errors"
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
//   - "collection-held:<id>" and "playlist-held:<id>": a marker journaled on
//     the SURVIVOR (BookID) when a retire only removed the retired book
//     because the survivor was already listed, so the survivor now also
//     stands in for it (NewValue is the retired book, OldValue its
//     MembershipRecord).
//   - "collection-took:<id>" and "playlist-took:<id>": a marker journaled on
//     the SURVIVOR when a retire replaced the retired book, in its slot, with
//     the survivor (the survivor was not listed before).
//     Neither marker writes anything or restores anything (IsMarker). They let
//     one operation's revert see what another operation left standing: the
//     survivor stays in the list while any retire that put it there, or relied
//     on it being there, is unreverted, and leaves with the last of them
//     (SurvivorStandOf), so retires undone in any order end with the original
//     list. A marker is stamped reverted together with its row, and only then:
//     a row that failed or was refused leaves its marker standing for the retry.
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
	UserContentCollectionTook = "collection-took"
	UserContentPlaylistTook   = "playlist-took"
)

// IsMarker reports whether kind is a survivor-side marker kind: it writes
// nothing, so its revert only marks the row reverted.
func IsMarker(kind string) bool {
	switch kind {
	case UserContentCollectionHeld, UserContentPlaylistHeld, UserContentCollectionTook, UserContentPlaylistTook:
		return true
	}
	return false
}

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
	if IsMarker(kind) {
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
	// removed the retired book and the revert puts it back beside its recorded
	// neighbours (Index when none can be found).
	SurvivorWasMember bool `json:"survivor_was_member"`
	// Anchored: Prev and Next are recorded. Every carry records them, and a
	// record without them is not one a carry wrote: DecodeMembership refuses it
	// (there is no neighbour to order the book by, and no build ever wrote such
	// a row).
	Anchored bool `json:"anchored,omitempty"`
	// Prev and Next are the ids of the books right before and after the
	// retired book in the list when the retire ran ("" at the head or the
	// tail). The revert finds the slot by them (MembershipGraph); Index is
	// the fallback.
	Prev string `json:"prev,omitempty"`
	Next string `json:"next,omitempty"`
}

// EncodeMembership renders r as a row value.
func EncodeMembership(r MembershipRecord) (string, error) {
	raw, err := json.Marshal(r)
	return string(raw), err
}

// DecodeMembership parses a row value; a negative index or a record that does
// not carry its neighbour anchors is malformed.
func DecodeMembership(v string) (MembershipRecord, error) {
	var r MembershipRecord
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return r, fmt.Errorf("%s membership %q: %w", ChangeTypeUserContentFollow, v, err)
	}
	if r.Index < 0 {
		return r, fmt.Errorf("%s membership %q: negative index", ChangeTypeUserContentFollow, v)
	}
	if !r.Anchored {
		return r, fmt.Errorf("%s membership %q: no neighbour anchors", ChangeTypeUserContentFollow, v)
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
	rec = MembershipRecord{Index: i, SurvivorWasMember: slices.Contains(cur, survivor), Anchored: true}
	if i > 0 {
		rec.Prev = cur[i-1]
	}
	if i+1 < len(cur) {
		rec.Next = cur[i+1]
	}
	next = slices.Clone(cur)
	if rec.SurvivorWasMember {
		return slices.Delete(next, i, i+1), rec, true
	}
	next[i] = survivor
	return next, rec, true
}

// SurvivorStand says what the survivor does in a list when a retire's row is
// reverted, as decided from the other operations' standing markers.
type SurvivorStand int

const (
	// SurvivorDefault: the row alone decides (a replaced slot goes back to
	// the retired book, a removal puts the book back beside the survivor).
	SurvivorDefault SurvivorStand = iota
	// SurvivorStays: another standing retire put the survivor in the list, or
	// relies on it being there (SurvivorStandOf), so the retired book goes
	// back at its slot and the survivor stays listed beside it.
	SurvivorStays
	// SurvivorLeaves: the retire only removed its book because the survivor
	// was already listed, and the survivor is there only because retires
	// that have since been reverted put it there and left it for this one
	// (SurvivorStandOf), so it leaves the list with this book's return.
	SurvivorLeaves
)

// RestoreMembership is the revert of CarryMembership on the list as it is
// now. ErrAlreadyRestored when the retired book is listed again; a
// ReasonChangedSince refusal when the survivor was put in the retired book's
// slot (or is to stay) and has since been taken out (the owner removed it on
// purpose). stand is the survivor's part (SurvivorStand); a survivor the
// owner already took out of the list is not removed again for SurvivorLeaves.
//
// A replaced slot the survivor still holds goes straight back to the retired
// book. Otherwise the book is inserted where the recorded neighbours put it
// (g, see MembershipGraph), so it lands in its original place whatever order
// several retires into the survivor are reverted in. note is non-empty when
// that was not possible and the recorded index was used (g nil, the records do
// not order the book against the books listed now, or they contradict the list
// order: the owner changed it by hand); it also says when a survivor the owner
// listed elsewhere was left where it was put. The caller reports it.
func RestoreMembership(cur []string, retired, survivor string, rec MembershipRecord, stand SurvivorStand, g *MembershipGraph) (next []string, note string, err error) {
	next, pl, err := restoreMembership(cur, retired, survivor, rec, stand, g)
	return next, pl.joined(), err
}

// placement is what a restore says about where it put the book, apart from
// the write itself. Both parts are reported by the caller, never dropped.
type placement struct {
	// byIndex: the book was put back by its recorded index (the records do not
	// order it against the books listed now).
	byIndex string
	// kept: the survivor was found listed somewhere the retire did not put it
	// (the owner moved it, or listed it again, since), so it was left where it
	// is and the book went back by the records instead of into its slot.
	kept string
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (p placement) joined() string {
	switch {
	case p.byIndex != "" && p.kept != "":
		return p.kept + "; " + p.byIndex
	case p.kept != "":
		return p.kept
	}
	return p.byIndex
}

func restoreMembership(cur []string, retired, survivor string, rec MembershipRecord, stand SurvivorStand, g *MembershipGraph) (next []string, pl placement, err error) {
	if slices.Contains(cur, retired) {
		return nil, pl, ErrAlreadyRestored
	}
	next = slices.Clone(cur)
	if !rec.SurvivorWasMember && stand != SurvivorStays {
		j := slices.Index(next, survivor)
		if j < 0 {
			return nil, pl, refuse(ReasonChangedSince, "book %s is no longer listed where the retire put it", survivor)
		}
		// The survivor is to give the slot back, so it must still be the slot:
		// listed where the records put the retired book. When the records place
		// the book elsewhere the survivor is not in its slot (the owner moved it
		// or listed it again by hand), and swapping would put the book where the
		// owner put the survivor. It is kept where it is found, and the book goes
		// back by the records.
		without := slices.Delete(slices.Clone(next), j, j+1)
		lo, hi, ok := g.slotRange(without, retired)
		if g != nil && !ok {
			// The records cannot confirm the slot (a book nothing orders against
			// the retired book is listed beside it, or the list was re-arranged
			// since): the survivor gives it back as recorded, and it is said.
			pl.kept = fmt.Sprintf("book %s went into the slot of survivor %s, which the records could not confirm: the list was changed by hand since", retired, survivor)
		}
		if ok && (j < lo || j > hi) {
			pl.kept = fmt.Sprintf("book %s was listed by hand at position %d, not in the slot the retire gave it: left where you placed it", survivor, j)
			// The slot was found in the list without the survivor, which stays
			// where it is: right before the book that followed it there.
			placed := slices.Insert(without, lo, retired)
			return slices.Insert(placed, j+btoi(lo <= j), survivor), pl, nil
		}
		next[j] = retired
		return next, pl, nil
	}
	if !rec.SurvivorWasMember && !slices.Contains(next, survivor) {
		return nil, pl, refuse(ReasonChangedSince, "book %s is no longer listed where the retire put it", survivor)
	}
	if stand == SurvivorLeaves && rec.SurvivorWasMember {
		if j := slices.Index(next, survivor); j >= 0 {
			next = slices.Delete(next, j, j+1)
		}
	}
	if at, ok := g.slot(next, retired); ok {
		return slices.Insert(next, at, retired), pl, nil
	}
	pl.byIndex = fmt.Sprintf("book %s was put back by its recorded index %d: the books beside it could not be ordered against the books listed now", retired, rec.Index)
	return slices.Insert(next, min(rec.Index, len(next)), retired), pl, nil
}

// MemberRole is the part one row's retire played in the list, as decided from
// the journals (MemberRoleOf), for RestoreMember.
type MemberRole struct {
	// Stand is the survivor's part (SurvivorStandFor).
	Stand SurvivorStand
	// Borrowed: the row borrowed the survivor, whose marker is a borrowing one
	// (SurvivorStandFor).
	Borrowed bool
	// RetiredStaysOut: the retired book was itself a survivor listed only on
	// other retires' account when it was retired, and none of them stands any
	// more, so it does not belong in the list now (RetiredStaysOut).
	RetiredStaysOut bool
	// SurvivorRetired: the row's survivor has since been retired out of this
	// list itself, by a carry that still stands (SurvivorRetiredOut). A survivor
	// that took the retired book's slot is then not listed, and the book goes
	// back beside its neighbours instead of into the survivor's slot.
	SurvivorRetired bool
	// Superseded: the row's marker belongs to an epoch a later "took" marker of
	// the same survivor ended (MarkerSuperseded): the survivor left the list
	// outside the journal since. The row has no say over the survivor: the book
	// goes back beside its neighbours and the survivor is left as it is.
	Superseded bool
}

// MemberRoleOf decides the role of row, one collection or playlist row of a
// retire's carry into list listID of kind, from the list's journal (rows: every
// row any retire into the list wrote, GetListChanges): the survivor's part from
// the survivor's markers, whether the retired book comes back at all from its own
// rows, and whether the survivor was retired out of the list since.
func MemberRoleOf(rows []*database.OperationChange, row *database.OperationChange, kind, listID string) MemberRole {
	stand, borrowed := SurvivorStandFor(rows, row, kind, listID)
	return MemberRole{Stand: stand, Borrowed: borrowed, RetiredStaysOut: RetiredStaysOut(rows, row, kind, listID),
		SurvivorRetired: SurvivorRetiredOut(rows, row, kind, listID) != nil,
		Superseded:      MarkerSuperseded(rows, row, kind, listID)}
}

// withRow is rows with row among them. The row being decided is a row of its
// list's journal by definition, so the rule that a marker counts only with a
// live row of its retire (markerHistory) never loses it when a caller hands over
// a journal that does not list it.
func withRow(rows []*database.OperationChange, row *database.OperationChange) []*database.OperationChange {
	for _, r := range rows {
		if r != nil && r.ID == row.ID && r.OperationID == row.OperationID {
			return rows
		}
	}
	return append(slices.Clone(rows), row)
}

// MarkerSuperseded reports that row's survivor-side marker belongs to an epoch
// that a later "took" marker of the same survivor in the same list ended. A
// "took" marker is written only when the survivor was not listed, so the
// survivor left the list between the two outside the journal (by hand, or a
// revert took it out and died before it stamped the marker), and row's retire no
// longer accounts for its place (or its absence).
func MarkerSuperseded(rows []*database.OperationChange, row *database.OperationChange, kind, listID string) bool {
	h := newMarkerHistory(row.NewValue, kind, listID, withRow(rows, row))
	mc := h.markerOf(row.OperationID, row.BookID)
	return mc != nil && h.superseded(mc)
}

// SurvivorRetiredOut returns the carry of row's survivor out of the same list,
// journaled after row and not reverted, from the list's journal: nil when there
// is none.
func SurvivorRetiredOut(rows []*database.OperationChange, row *database.OperationChange, kind, listID string) *database.OperationChange {
	field := UserContentField(kind, listID)
	var newest *database.OperationChange
	for _, r := range rows {
		if r == nil || r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.RevertedAt != nil ||
			r.BookID != row.NewValue || r.FieldName != field {
			continue
		}
		if latestRow(row, r) == r {
			newest = latestRow(newest, r)
		}
	}
	return newest
}

// MemberResult is what RestoreMember did besides writing the list.
type MemberResult struct {
	// Note is non-empty when the book was placed by its recorded index
	// (RestoreMembership); the caller reports it.
	Note string
	// Kept is non-empty when the survivor was found listed where the retire did
	// not put it and was left there (placement.kept); the caller reports it.
	Kept string
	// Released: the retired book was already listed and the survivor was taken
	// out of the list because nothing stands on it any more.
	Released bool
	// LeftOut: the retired book was not put back (MemberRole.RetiredStaysOut);
	// the caller reports it.
	LeftOut bool
}

// RestoreMember is RestoreMembership for the revert of one row, which also
// repairs two things the other retires leave behind.
//
// A retired book that was a survivor itself (role.RetiredStaysOut) is not put
// back: it was listed only for retires that have all been reverted since, so
// putting it back would list a book that was never in the list. The survivor's
// part is still played (it leaves with the row, or stays, as for any row).
//
// When the retired book is already listed again (the owner put it back by hand
// while the retire stood) and the row borrowed the survivor (role.Borrowed) with
// nothing else standing on it (role.Stand is not SurvivorStays), the survivor is
// in the list only because of this retire and is taken out with it (Released),
// not left beside the book it stood in for for good. A book already listed with
// nothing to release is ErrAlreadyRestored, as in RestoreMembership.
func RestoreMember(cur []string, retired, survivor string, rec MembershipRecord, role MemberRole, g *MembershipGraph) (next []string, res MemberResult, err error) {
	if role.RetiredStaysOut && !slices.Contains(cur, retired) {
		next = slices.Clone(cur)
		switch {
		case role.Superseded:
			// The row has no say over the survivor (MemberRole.Superseded).
		case !rec.SurvivorWasMember && role.Stand != SurvivorStays:
			// The survivor took the retired book's slot, and the book is not
			// coming back to it: the survivor leaves the list.
			if j := slices.Index(next, survivor); j >= 0 {
				next = slices.Delete(next, j, j+1)
			}
		case rec.SurvivorWasMember && role.Stand == SurvivorLeaves:
			if j := slices.Index(next, survivor); j >= 0 {
				next = slices.Delete(next, j, j+1)
			}
		}
		return next, MemberResult{LeftOut: true}, nil
	}
	if !rec.SurvivorWasMember && (role.Superseded || role.SurvivorRetired) {
		// The survivor took the book's slot and has since been retired out of
		// the list by a carry that still stands (into a survivor that took its
		// slot in turn, or one already listed). The slot is not the survivor's
		// to give back: the book goes back beside its neighbours (the graph
		// resolves the survivor through its stand-in), and the survivor's part
		// is left to the revert of its own retire.
		asHeld := rec
		asHeld.SurvivorWasMember = true
		next, pl, err := restoreMembership(cur, retired, survivor, asHeld, SurvivorDefault, g)
		return next, MemberResult{Note: pl.byIndex, Kept: pl.kept}, err
	}
	next, pl, err := restoreMembership(cur, retired, survivor, rec, role.Stand, g)
	if !errors.Is(err, ErrAlreadyRestored) {
		return next, MemberResult{Note: pl.byIndex, Kept: pl.kept}, err
	}
	if !role.Borrowed || role.Stand == SurvivorStays {
		return nil, MemberResult{}, err
	}
	j := slices.Index(cur, survivor)
	if j < 0 {
		return nil, MemberResult{}, err
	}
	return slices.Delete(slices.Clone(cur), j, j+1), MemberResult{Released: true}, nil
}

// RetiredStaysOut says whether the retired book of row, a collection or
// playlist row of a retire's carry into list listID of kind, is to stay out of
// the list when the row is reverted, from the list's journal (rows).
//
// A book that was itself a survivor of this list can be in it only on other
// retires' account: they put it there in a retired book's slot and it stays
// for as long as one of them stands (SurvivorStandOf). Retired into another
// survivor while it was there, it comes back only if one of those retires still
// stands; if they have all been reverted since, it left the list for good the
// moment the last one was, and restoring this row must not list it again. A
// book that was an original member of the list (no borrowing marker of its own
// when it was retired) always comes back.
func RetiredStaysOut(rows []*database.OperationChange, row *database.OperationChange, kind, listID string) bool {
	h := newMarkerHistory(row.BookID, kind, listID, withRow(rows, row))
	if _, root := h.survivorEpochAt(CreatedKey(row), row.ID); root == nil {
		return false
	}
	_, alive := h.epochNow()
	return !alive
}

// CheckMembershipFollow is what the revert of a collection or playlist row
// (kind, listID) would do to the list as it is now, for the preflight: nil when
// it would put the retired book back (by neighbours or, when they cannot
// place it, by its recorded index; the revert reports that), ErrAlreadyRestored
// when it would write nothing, or a ReasonChangedSince refusal. It decides as
// the revert does, from the list's journal (rows): a survivor that has since
// been retired out of the list itself (SurvivorRetiredOut), a retired book that
// is to stay out (RetiredStaysOut), a survivor that is to stay (SurvivorStandFor)
// and a marker a later epoch superseded (MarkerSuperseded) are predicted the way
// revertListFollow handles them. Placement is not predicted (no graph), so a row
// the revert would report by index is still nil here.
func CheckMembershipFollow(cur []string, c *database.OperationChange, kind, listID string, rows []*database.OperationChange) error {
	rec, err := DecodeMembership(c.OldValue)
	if err != nil {
		return &ReferentError{Reason: ReasonOldValueUnparsable, Detail: err.Error()}
	}
	_, _, err = RestoreMember(cur, c.BookID, c.NewValue, rec, MemberRoleOf(rows, c, kind, listID), nil)
	return err
}

// HeldKind is the marker kind a removal journals (the survivor was already
// listed), and TookKind the one a replace journals (the survivor took the
// retired book's slot), for a collection or playlist row kind.
func HeldKind(kind string) string { return kind + "-held" }
func TookKind(kind string) string { return kind + "-took" }

// SurvivorStandOf decides the survivor's part in reverting row, one
// collection or playlist row of a retire's carry into list listID of kind,
// from the list's journal (the rows GetListChanges returns).
//
// The survivor is in the list for one of two reasons: it was already a member
// (nothing the retires did), or a retire put it there in a retired book's
// slot, and it stays for as long as that retire, or one that relied on it,
// stands. The markers say which. A "took" marker borrows (the survivor entered
// the list for it); a "held" marker borrows when a borrowing marker was
// standing when it was written (the survivor was in the list only on that
// marker's account). A marker is standing until its row is reverted
// (RevertedAt), so an operation that reverts its rows one by one must stamp
// each marker as soon as its row is restored (the revert does, under the
// survivor's lock), or the last row of the operation would see its siblings
// still standing.
//
// For a borrowing row: while another borrowing marker stands, the survivor
// stays (SurvivorStays); otherwise the survivor leaves with the row
// (SurvivorLeaves for a removal, the default swap back for a replace). A row
// that does not borrow, or has no marker, leaves
// the survivor alone (SurvivorDefault).
func SurvivorStandOf(rows []*database.OperationChange, row *database.OperationChange, kind, listID string) SurvivorStand {
	stand, _ := SurvivorStandFor(rows, row, kind, listID)
	return stand
}

// SurvivorStandFor is SurvivorStandOf that also says whether the row borrowed
// the survivor (its marker is a borrowing one, see SurvivorStandOf).
func SurvivorStandFor(rows []*database.OperationChange, row *database.OperationChange, kind, listID string) (stand SurvivorStand, borrowed bool) {
	h := newMarkerHistory(row.NewValue, kind, listID, withRow(rows, row))
	mc := h.markerOf(row.OperationID, row.BookID)
	if mc == nil || !mc.borrowing || h.superseded(mc) {
		return SurvivorDefault, false
	}
	for _, m := range h.ms {
		if m != mc && m.borrowing && m.root == mc.root && m.standing() {
			return SurvivorStays, true
		}
	}
	if mc.took {
		return SurvivorDefault, true
	}
	return SurvivorLeaves, true
}

// PairedRow returns the collection or playlist row of a retire's carry that a
// survivor-side marker belongs to, among the rows of the marker's operation: the
// same operation, the retired book the marker names, the same list. nil when
// there is none. A marker is reverted only together with its row, never alone.
func PairedRow(all []*database.OperationChange, marker *database.OperationChange) *database.OperationChange {
	kind, listID, ok := UserContentFromField(marker.FieldName)
	if !ok {
		return nil
	}
	main := UserContentCollection
	if kind == UserContentPlaylistHeld || kind == UserContentPlaylistTook {
		main = UserContentPlaylist
	}
	want := UserContentField(main, listID)
	var best *database.OperationChange
	for _, r := range all {
		if r.ChangeType == ChangeTypeUserContentFollow && !r.Voided && r.OperationID == marker.OperationID &&
			r.FieldName == want && r.BookID == marker.NewValue && r.NewValue == marker.BookID {
			best = latestRow(best, r)
		}
	}
	return best
}

// latestRow is the later of two rows in the journal's order (CreatedKey), ties
// to the greater id: the rule PairedRow and MarkerOf share (and markerHistory
// sorts by), so a marker and its row are always the same retire's.
func latestRow(a, b *database.OperationChange) *database.OperationChange {
	switch ka, kb := keyOrZero(a), CreatedKey(b); {
	case a == nil:
		return b
	case kb > ka, kb == ka && b.ID > a.ID:
		return b
	}
	return a
}

func keyOrZero(c *database.OperationChange) int64 {
	if c == nil {
		return 0
	}
	return CreatedKey(c)
}

// MarkerOf returns the survivor-side marker row of row, a collection or
// playlist row of a retire's carry, among the survivor's journal rows: the
// same operation, the same retired book, the same list. nil when there is
// none (the carry voids a marker and its row together, so a row without one is
// not a carry's).
func MarkerOf(rows []*database.OperationChange, row *database.OperationChange, kind, listID string) *database.OperationChange {
	var best *database.OperationChange
	for _, r := range rows {
		if r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.BookID != row.NewValue ||
			r.OperationID != row.OperationID || r.NewValue != row.BookID {
			continue
		}
		if r.FieldName != UserContentField(TookKind(kind), listID) && r.FieldName != UserContentField(HeldKind(kind), listID) {
			continue
		}
		best = latestRow(best, r)
	}
	return best
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
