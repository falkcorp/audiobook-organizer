// file: internal/undo/user_content_follow.go
// version: 1.6.0
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
	// Anchored: Prev and Next are recorded. Rows journaled before the anchors
	// existed have neither, and are put back by Index alone.
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
// that was not possible and the recorded index was used (g nil, no neighbour
// of the book resolves in the list now, or the row predates the anchors); the
// caller reports it.
func RestoreMembership(cur []string, retired, survivor string, rec MembershipRecord, stand SurvivorStand, g *MembershipGraph) (next []string, note string, err error) {
	if slices.Contains(cur, retired) {
		return nil, "", ErrAlreadyRestored
	}
	next = slices.Clone(cur)
	if !rec.SurvivorWasMember && stand != SurvivorStays {
		j := slices.Index(next, survivor)
		if j < 0 {
			return nil, "", refuse(ReasonChangedSince, "book %s is no longer listed where the retire put it", survivor)
		}
		next[j] = retired
		return next, "", nil
	}
	if !rec.SurvivorWasMember && !slices.Contains(next, survivor) {
		return nil, "", refuse(ReasonChangedSince, "book %s is no longer listed where the retire put it", survivor)
	}
	if stand == SurvivorLeaves && rec.SurvivorWasMember {
		if j := slices.Index(next, survivor); j >= 0 {
			next = slices.Delete(next, j, j+1)
		}
	}
	if !rec.Anchored {
		note = fmt.Sprintf("book %s was put back by its recorded index %d: the row predates neighbour anchors", retired, rec.Index)
	} else {
		if at, ok := g.slot(next, retired); ok {
			return slices.Insert(next, at, retired), "", nil
		}
		why := "the books beside it could not be ordered against the books listed now"
		if g != nil && g.Err() != nil {
			why = g.Err().Error()
		}
		note = fmt.Sprintf("book %s was put back by its recorded index %d: %s", retired, rec.Index, why)
	}
	return slices.Insert(next, min(rec.Index, len(next)), retired), note, nil
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
}

// MemberRoleOf decides the role of row, one collection or playlist row of a
// retire's carry into list listID of kind, from the journal of its survivor
// (survivorRows) and of the retired book itself (retiredRows).
func MemberRoleOf(survivorRows, retiredRows []*database.OperationChange, row *database.OperationChange, kind, listID string) MemberRole {
	stand, borrowed := SurvivorStandFor(survivorRows, row, kind, listID)
	return MemberRole{Stand: stand, Borrowed: borrowed, RetiredStaysOut: RetiredStaysOut(retiredRows, row, kind, listID)}
}

// MemberResult is what RestoreMember did besides writing the list.
type MemberResult struct {
	// Note is non-empty when the book was placed by its recorded index
	// (RestoreMembership); the caller reports it.
	Note string
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
// When the retired book is already listed again and the row borrowed the
// survivor (role.Borrowed) with nothing else standing on it (role.Stand is not
// SurvivorStays), the survivor is still in the list only because an earlier,
// interrupted revert of the row wrote the list and died before it stamped the
// row's marker, and it is taken out now (Released). A book already listed with
// nothing to release is ErrAlreadyRestored, as in RestoreMembership.
func RestoreMember(cur []string, retired, survivor string, rec MembershipRecord, role MemberRole, g *MembershipGraph) (next []string, res MemberResult, err error) {
	if role.RetiredStaysOut && !slices.Contains(cur, retired) {
		next = slices.Clone(cur)
		switch {
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
	next, note, err := RestoreMembership(cur, retired, survivor, rec, role.Stand, g)
	if !errors.Is(err, ErrAlreadyRestored) {
		return next, MemberResult{Note: note}, err
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
// the list when the row is reverted, from the book's own journal (retiredRows).
//
// A book that was itself a survivor of this list can be in it only on other
// retires' account: they put it there in a retired book's slot and it stays
// for as long as one of them stands (SurvivorStandOf). Retired into another
// survivor while it was there, it comes back only if one of those retires still
// stands; if they have all been reverted since, it left the list for good the
// moment the last one was, and restoring this row must not list it again. A
// book that was an original member of the list (no borrowing marker of its own
// when it was retired) always comes back.
func RetiredStaysOut(retiredRows []*database.OperationChange, row *database.OperationChange, kind, listID string) bool {
	h := newMarkerHistory(row.BookID, kind, listID, retiredRows)
	if _, root := h.survivorEpochAt(row.CreatedAt.UnixNano(), row.ID); root == nil {
		return false
	}
	_, alive := h.epochNow()
	return !alive
}

// NewerCarryStands returns the carry of row's book into the same list that was
// journaled after row and has not been reverted, from the book's own journal
// (retiredRows): nil when there is none. When there is one, the book has been
// retired again since row, and row is stale: putting the book back for it would
// list a book a standing retire removed.
func NewerCarryStands(retiredRows []*database.OperationChange, row *database.OperationChange, kind, listID string) *database.OperationChange {
	field := UserContentField(kind, listID)
	var newest *database.OperationChange
	for _, r := range retiredRows {
		if r == nil || r.ID == row.ID || r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.RevertedAt != nil ||
			r.BookID != row.BookID || r.FieldName != field {
			continue
		}
		if latestRow(row, r) == r {
			newest = latestRow(newest, r)
		}
	}
	return newest
}

// CheckMembershipFollow is what the revert of a collection or playlist row
// would do to the list as it is now, for the preflight: nil when it would put
// the retired book back, ErrAlreadyRestored, or a ReasonChangedSince
// refusal. The survivor-side markers (SurvivorStandOf) change a revert only
// from a refusal-free replace to a keep, so they are not read here.
func CheckMembershipFollow(cur []string, c *database.OperationChange) error {
	rec, err := DecodeMembership(c.OldValue)
	if err != nil {
		return &ReferentError{Reason: ReasonOldValueUnparsable, Detail: err.Error()}
	}
	_, _, err = RestoreMembership(cur, c.BookID, c.NewValue, rec, SurvivorDefault, nil)
	return err
}

// HeldKind is the marker kind a removal journals (the survivor was already
// listed), and TookKind the one a replace journals (the survivor took the
// retired book's slot), for a collection or playlist row kind.
func HeldKind(kind string) string { return kind + "-held" }
func TookKind(kind string) string { return kind + "-took" }

// SurvivorStandOf decides the survivor's part in reverting row, one
// collection or playlist row of a retire's carry into list listID of kind,
// from the survivor's journal (GetBookChanges of the survivor).
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
// that does not borrow, or has no marker (journaled before markers), leaves
// the survivor alone (SurvivorDefault).
func SurvivorStandOf(survivorRows []*database.OperationChange, row *database.OperationChange, kind, listID string) SurvivorStand {
	stand, _ := SurvivorStandFor(survivorRows, row, kind, listID)
	return stand
}

// SurvivorStandFor is SurvivorStandOf that also says whether the row borrowed
// the survivor (its marker is a borrowing one, see SurvivorStandOf).
func SurvivorStandFor(survivorRows []*database.OperationChange, row *database.OperationChange, kind, listID string) (stand SurvivorStand, borrowed bool) {
	h := newMarkerHistory(row.NewValue, kind, listID, survivorRows)
	mc := h.markerOf(row.OperationID, row.BookID)
	if mc == nil || !mc.borrowing {
		return SurvivorDefault, false
	}
	for _, m := range h.ms {
		if m != mc && m.borrowing && m.standing() {
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

// latestRow is the later of two rows by CreatedAt, ties to the greater id: the
// rule PairedRow and MarkerOf share (and markerHistory sorts by), so a marker
// and its row are always the same retire's.
func latestRow(a, b *database.OperationChange) *database.OperationChange {
	switch {
	case a == nil:
		return b
	case b.CreatedAt.After(a.CreatedAt), b.CreatedAt.Equal(a.CreatedAt) && b.ID > a.ID:
		return b
	}
	return a
}

// MarkerOf returns the survivor-side marker row of row, a collection or
// playlist row of a retire's carry, among the survivor's journal rows: the
// same operation, the same retired book, the same list. nil when there is
// none (the row was journaled before markers).
func MarkerOf(survivorRows []*database.OperationChange, row *database.OperationChange, kind, listID string) *database.OperationChange {
	var best *database.OperationChange
	for _, r := range survivorRows {
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
