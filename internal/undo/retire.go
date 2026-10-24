// file: internal/undo/retire.go
// version: 1.2.1
// guid: 78716d7c-a378-4584-9b30-edcdab627347
// last-edited: 2026-10-24

package undo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The change types a retire of a trashed book's files into a retired version
// of a live holder journals (retired-versions plan, sections 6.1 to 6.4). A
// retire moves no byte and deletes no row: each book_file row is re-keyed from
// the shell (the trashed book it came from) to the holder, flagged Retired and
// tied to a retired book_versions row, so every journaled step is a
// compare-and-set the revert can reverse exactly.
const (
	// ChangeTypeBookVersionCreate: the retired book_versions row was created.
	// BookID is the holder, FieldName BookVersionField(version id), OldValue "",
	// NewValue the version as JSON (database.BookVersion). Restorable: the
	// revert deletes the version, and only once no row names it any more
	// (database.ErrRetiredVersionHasFiles refuses it as long as one does, so it
	// is reported as ReasonDependentNotReverted and the retry, after the rows
	// are back, deletes it). A version already gone counts restored.
	ChangeTypeBookVersionCreate = "book_version_create"
	// ChangeTypeBookFileRetire: one book_file row was retired. BookID is the
	// holder, FieldName "book_file:<id>", OldValue the RetirePlace the row left
	// (the shell and the VersionID the row carried before), NewValue the
	// RetirePlace it went to (the holder and the retired version, plus the
	// combine journal the retire is linked to, if any). Restorable: the revert
	// un-retires the row back under the shell with its prior VersionID, and
	// recreates the shell first when a purge deleted it (EnsureShellPresent).
	ChangeTypeBookFileRetire = "book_file_retire"
	// ChangeTypeRetiredVersionRehome: a retired version and all its rows moved
	// to a new holder, because the old holder was itself trashed (the chain
	// case). BookID is the new holder, FieldName BookVersionField(version id),
	// OldValue the old holder, NewValue the new holder. Restorable: the revert
	// rehomes the version back.
	ChangeTypeRetiredVersionRehome = "retired_version_rehome"
)

const bookVersionFieldPrefix = "book_version:"

// BookVersionField is the FieldName of a ChangeTypeBookVersionCreate or
// ChangeTypeRetiredVersionRehome row.
func BookVersionField(versionID string) string { return bookVersionFieldPrefix + versionID }

// BookVersionIDFromField returns the version id of a "book_version:<id>" field.
func BookVersionIDFromField(field string) (string, bool) {
	id, ok := strings.CutPrefix(field, bookVersionFieldPrefix)
	return id, ok && id != ""
}

// RetirePlace is either value of a ChangeTypeBookFileRetire row: where the
// row stood (OldValue: the shell and the VersionID it carried, empty for the
// usual never-versioned row) or where the retire put it (NewValue: the holder
// and the retired version).
//
// CombineJournalID is set on the NewValue of a retire that was linked to a
// combine journal (retired-versions plan 6.5): the raw key
// merge:combine-retire:<combine id>:<shell id>:<retire op id> names the retire op, and this
// field names the combine on the row itself, so the link can be rebuilt from
// the journal. It rides inside the JSON because OperationChange has no field
// for it, and a row without it decodes to "".
type RetirePlace struct {
	BookID           string `json:"book_id"`
	VersionID        string `json:"version_id,omitempty"`
	CombineJournalID string `json:"combine_journal_id,omitempty"`
}

// Encode renders p as the value of a ChangeTypeBookFileRetire row.
func (p RetirePlace) Encode() string {
	b, err := json.Marshal(p)
	if err != nil {
		// A struct of three strings cannot fail to marshal.
		panic(fmt.Sprintf("undo: encode RetirePlace: %v", err))
	}
	return string(b)
}

// DecodeRetirePlace parses a RetirePlace value; the book is required.
func DecodeRetirePlace(v string) (RetirePlace, error) {
	var p RetirePlace
	if err := json.Unmarshal([]byte(v), &p); err != nil {
		return RetirePlace{}, fmt.Errorf("retire place %q: %w", v, err)
	}
	if p.BookID == "" {
		return RetirePlace{}, fmt.Errorf("retire place %q: no book", v)
	}
	return p, nil
}

// BookFileRetire is a decoded, validated ChangeTypeBookFileRetire row.
type BookFileRetire struct {
	FileID string
	// Shell is the book the row left; Holder the book it went to (the row's
	// BookID); VersionID the retired version it joined; PriorVersionID the
	// VersionID it carried before.
	Shell, Holder, VersionID, PriorVersionID string
	CombineJournalID                         string
}

// DecodeBookFileRetire reads a ChangeTypeBookFileRetire row. A row that is not
// one, or whose values do not parse, returns an error (NotRestorableLabel
// calls the row unparsable then).
func DecodeBookFileRetire(c *database.OperationChange) (BookFileRetire, error) {
	if c.ChangeType != ChangeTypeBookFileRetire {
		return BookFileRetire{}, fmt.Errorf("%s is not a %s row", c.ChangeType, ChangeTypeBookFileRetire)
	}
	id, ok := BookFileIDFromField(c.FieldName)
	if !ok {
		return BookFileRetire{}, fmt.Errorf("no book_file id in %q", c.FieldName)
	}
	from, err := DecodeRetirePlace(c.OldValue)
	if err != nil {
		return BookFileRetire{}, err
	}
	to, err := DecodeRetirePlace(c.NewValue)
	if err != nil {
		return BookFileRetire{}, err
	}
	switch {
	case to.VersionID == "":
		return BookFileRetire{}, fmt.Errorf("retire row of %s names no retired version", id)
	case to.BookID != c.BookID:
		return BookFileRetire{}, fmt.Errorf("retire row of %s is on book %s but moved the row to %s", id, c.BookID, to.BookID)
	case from.BookID == to.BookID:
		return BookFileRetire{}, fmt.Errorf("retire row of %s moved it from book %s to the same book", id, from.BookID)
	}
	return BookFileRetire{
		FileID: id, Shell: from.BookID, Holder: to.BookID, VersionID: to.VersionID,
		PriorVersionID: from.VersionID, CombineJournalID: to.CombineJournalID,
	}, nil
}

// DecodeBookVersionCreate reads the version a ChangeTypeBookVersionCreate row
// journaled.
func DecodeBookVersionCreate(c *database.OperationChange) (database.BookVersion, error) {
	var v database.BookVersion
	id, ok := BookVersionIDFromField(c.FieldName)
	if !ok {
		return v, fmt.Errorf("no book_version id in %q", c.FieldName)
	}
	if err := json.Unmarshal([]byte(c.NewValue), &v); err != nil {
		return v, fmt.Errorf("book version %s: %w", id, err)
	}
	switch {
	case v.ID != id:
		return v, fmt.Errorf("book version row names %q but carries id %q", id, v.ID)
	case v.Status != database.BookVersionStatusRetired:
		return v, fmt.Errorf("book version %s has status %q, not retired", id, v.Status)
	case v.BookID == "" || v.BookID != c.BookID:
		return v, fmt.Errorf("book version %s belongs to %q, not the holder %s", id, v.BookID, c.BookID)
	case v.RetiredFromBookID == "":
		return v, fmt.Errorf("book version %s names no shell it was retired from", id)
	}
	return v, nil
}

// EncodeBookVersionCreate renders the NewValue of a ChangeTypeBookVersionCreate
// row.
func EncodeBookVersionCreate(v database.BookVersion) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode book version %s: %w", v.ID, err)
	}
	return string(b), nil
}

// retireRowLabel is NotRestorableLabel's answer for the three retire types.
func retireRowLabel(c *database.OperationChange) string {
	switch c.ChangeType {
	case ChangeTypeBookFileRetire:
		if _, err := DecodeBookFileRetire(c); err != nil {
			return c.ChangeType + ":(unparsable)"
		}
	case ChangeTypeBookVersionCreate:
		if _, err := DecodeBookVersionCreate(c); err != nil {
			return c.ChangeType + ":(unparsable)"
		}
	case ChangeTypeRetiredVersionRehome:
		if _, ok := BookVersionIDFromField(c.FieldName); !ok || c.OldValue == "" || c.NewValue == "" || c.NewValue != c.BookID {
			return c.ChangeType + ":(unparsable)"
		}
	}
	return ""
}

// IsRetireStageType reports whether changeType is one a retire (stage R of the
// retired-versions plan) journals: the three types above, the shell's external
// id moves and its merged-into pointer. The combine undo reverses exactly
// these and nothing else (RetireScopeRefusals refuses any other row).
func IsRetireStageType(changeType string) bool {
	switch changeType {
	case ChangeTypeBookVersionCreate, ChangeTypeBookFileRetire, ChangeTypeRetiredVersionRehome,
		ChangeTypeExternalIDReassign, ChangeTypeBookMergedInto, ChangeTypeRepairPlanRecord:
		return true
	}
	return false
}

// RetiredRowRefusal is the refusal for a revert of a journaled step on a
// book_file row that has since been retired into another book's version: the
// row now belongs to that version, so putting the step's old value back would
// rewrite a version the owner keeps (the row is not "still where the operation
// left it"). Every by-id book_file revert and its preflight check it first.
func RetiredRowRefusal(f *database.BookFile) error {
	return refuse(ReasonChangedSince, "book_file %s was retired into version %q of book %s since the operation; its later retire is reversed first, never overwritten",
		f.ID, f.VersionID, f.BookID)
}

// CheckRetireCurrent is the compare-and-set of a ChangeTypeBookFileRetire
// row, shared by the revert and the preflight. onHolder and onShell are the
// row read by id under the holder and under the shell (nil when absent;
// database.PebbleStore.GetBookFileByID returns retired rows).
//
//   - nil: the row is on the holder, retired and carrying the version the
//     retire gave it; un-retire it.
//   - ErrAlreadyRestored: the row is back on the shell and not retired. That
//     is also what a Writer leaves when it journaled the step and the batch
//     then failed to commit (journal first, voiding covers refusals only).
//   - a ReasonChangedSince refusal: anything else (the row left for another
//     book, lost its flag on the holder, or names another version).
func CheckRetireCurrent(onHolder, onShell *database.BookFile, r BookFileRetire) error {
	switch {
	case onHolder != nil:
		if !onHolder.Retired {
			return refuse(ReasonChangedSince, "book_file %s is on book %s but no longer retired", r.FileID, r.Holder)
		}
		if onHolder.VersionID != r.VersionID {
			return refuse(ReasonChangedSince, "book_file %s is retired into version %q, not the %q the operation made", r.FileID, onHolder.VersionID, r.VersionID)
		}
		return nil
	case onShell != nil && !onShell.Retired:
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book_file %s is on neither the holder %s (retired) nor the shell %s", r.FileID, r.Holder, r.Shell)
}

// CheckVersionCreateCurrent is the compare-and-set of a
// ChangeTypeBookVersionCreate row. cur is the version now (nil: gone).
// ErrAlreadyRestored: it is gone. Otherwise it must still be the retired
// version of the holder the row journaled; the store's own refusal to delete a
// version that still holds rows is the dependency gate.
func CheckVersionCreateCurrent(cur *database.BookVersion, c *database.OperationChange) error {
	if cur == nil {
		return ErrAlreadyRestored
	}
	if cur.Status != database.BookVersionStatusRetired || cur.BookID != c.BookID {
		return refuse(ReasonChangedSince, "book version %s is now %q of book %s, not the retired version of %s the operation created",
			cur.ID, cur.Status, cur.BookID, c.BookID)
	}
	return nil
}

// CheckRehomeCurrent is the compare-and-set of a ChangeTypeRetiredVersionRehome
// row: the version is still held by NewValue (rehome it back), already back on
// OldValue (restored), or elsewhere or gone (refused).
func CheckRehomeCurrent(cur *database.BookVersion, c *database.OperationChange) error {
	switch {
	case cur == nil:
		return refuse(ReasonChangedSince, "retired version %s no longer exists", c.FieldName)
	case cur.BookID == c.NewValue:
		return nil
	case cur.BookID == c.OldValue:
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "retired version %s is held by book %s, not the %s the operation moved it to", cur.ID, cur.BookID, c.NewValue)
}

// PurgedShellRecreator is the seam to the purge snapshot (retired-versions
// plan 6.2 and 6.3, stage P): a shell a purge deleted can be recreated, with
// the same id and its trash state, from the snapshot the purge journaled. The
// real implementation is the purge's; until it exists the store does not
// implement this and a purged shell stays unrestorable (book missing).
// Resolve it with database.AsCapability; it is not on Store.
//
// Lock contract: both methods are called with the merge lock HELD (the revert's
// retire-row cases and pre-pass, and everything under UndoCombine) and must
// never take it: the lock is a counted mutex that is not re-entrant.
type PurgedShellRecreator interface {
	// HasPurgeSnapshot reports whether a snapshot of the purged book bookID
	// exists (and so RecreatePurgedBook can bring it back).
	HasPurgeSnapshot(bookID string) (bool, error)
	// RecreatePurgedBook recreates the book bookID from its snapshot, refusing
	// when the id exists. It also marks the purge's own change row reverted.
	RecreatePurgedBook(bookID string) error
}

// ShellState is what ShellCheck found.
type ShellState int

const (
	// ShellPresent: the shell book exists (live or soft-deleted).
	ShellPresent ShellState = iota
	// ShellRecreatable: a purge deleted it and a snapshot can bring it back.
	ShellRecreatable
)

// ShellCheck answers whether the shell a retire row names is, or can be made,
// present: ShellPresent when the book exists, ShellRecreatable when it does
// not but store (through database.AsCapability) can recreate it from a purge
// snapshot, and a ReasonBookMissing refusal otherwise (a read error refuses
// too, ReasonBookLookupFailed). The revert (EnsureShellPresent) and the
// preflight both ask it, so a recreatable shell is classified restorable and
// never book_missing.
func ShellCheck(store BookLookup, shellID string) (ShellState, error) {
	_, err := CheckRestoreBook(store, shellID)
	if err == nil {
		return ShellPresent, nil
	}
	if RefusalReason(err) != ReasonBookMissing {
		return 0, err
	}
	rc, ok := database.AsCapability[PurgedShellRecreator](store)
	if !ok {
		return 0, refuse(ReasonBookMissing, "shell %s was purged and nothing can recreate it (no purge snapshot reader)", shellID)
	}
	has, herr := rc.HasPurgeSnapshot(shellID)
	switch {
	case herr != nil:
		return 0, refuse(ReasonBookLookupFailed, "read the purge snapshot of shell %s: %v", shellID, herr)
	case !has:
		return 0, refuse(ReasonBookMissing, "shell %s was purged and has no purge snapshot to recreate it from", shellID)
	}
	return ShellRecreatable, nil
}

// EnsureShellPresent makes sure the shell exists before a revert writes to it:
// nothing when it is there, a recreate from its purge snapshot when it was
// purged (ShellCheck), and the refusal ShellCheck gives otherwise. A failed
// recreate is an error, never a refusal to ignore.
func EnsureShellPresent(store BookLookup, shellID string) (recreated bool, err error) {
	state, err := ShellCheck(store, shellID)
	if err != nil || state == ShellPresent {
		return false, err
	}
	rc, _ := database.AsCapability[PurgedShellRecreator](store)
	if err := rc.RecreatePurgedBook(shellID); err != nil {
		if errors.Is(err, ErrAlreadyRestored) {
			return false, nil
		}
		return false, fmt.Errorf("recreate purged shell %s: %w", shellID, err)
	}
	return true, nil
}

// RehomeFromShell reports whether rehome row c took its version off shellID:
// the shell was the HOLDER of a retired version (one an earlier retire put
// there) and a later retire, which retired the shell into another book, moved
// that version along with it. Reversing it puts the version back on the shell,
// which is part of restoring the shell, and does not depend on any of the
// shell's own retire rows. A rehome of a version the shell's own retire created
// is not this case: its rows are not where the retire rows say until the rehome
// is reversed.
func RehomeFromShell(c *database.OperationChange, shellID string) bool {
	return c.ChangeType == ChangeTypeRetiredVersionRehome && shellID != "" && c.OldValue == shellID
}

// RetireScopeRefusals predicts, for the combine undo, whether a retire op's
// pending rows for one shell (shellID) can all be reversed now, writing
// nothing. rows are the shell's un-reverted, un-voided rows of the op
// (audiobooks' revertScope.filter). It returns one sentence per refusal, naming
// the row, and an empty slice when every row reverts.
//
// It judges the rows exactly as PreflightUndoConflicts does (same plan, same
// gate, same row checks, the shell a purge deleted counted present when a
// snapshot can recreate it, whether or not the retire moved any file row off
// it), with these refusals of its own. The combine undo reverses a retire and
// nothing else, so a row outside IsRetireStageType (the retire op also changed
// the shell some other way) refuses: reversing part of it would leave the shell
// half-restored. A row that does not parse refuses by name, since nothing can
// say what it would restore. A rehome row refuses unless it took a version off
// the shell itself (RehomeFromShell): the rows a rehome of the retire's own
// version moved are not where the retire rows say until it is reversed, and a
// check that reads state cannot see that reversal in advance.
func RetireScopeRefusals(store ConflictChecker, shellID string, rows []*database.OperationChange) ([]string, error) {
	var reasons []string
	var judged []*database.OperationChange
	for _, c := range rows {
		switch {
		case retireRowLabel(c) != "":
			reasons = append(reasons, fmt.Sprintf("change %s (%s): the row does not parse, so the combine undo cannot tell what it would restore", c.ID, c.ChangeType))
		case c.ChangeType == ChangeTypeRetiredVersionRehome && !RehomeFromShell(c, shellID):
			reasons = append(reasons, fmt.Sprintf("change %s (%s): the retired version was moved to another holder since; that move is reversed first, not by the combine undo", c.ID, c.ChangeType))
		case !IsRetireStageType(c.ChangeType):
			reasons = append(reasons, fmt.Sprintf("change %s (%s): the retire op also changed the shell in a way the combine undo does not reverse", c.ID, c.ChangeType))
		default:
			judged = append(judged, c)
		}
	}
	var files func(string) ([]database.BookFile, error)
	if r, ok := store.(bookFilesReader); ok {
		files = r.GetBookFiles
	}
	plan, err := PlanRevert(judged, files)
	if err != nil {
		return nil, err
	}
	recreatable := recreatableShells(store, judged)
	// The shell itself, whatever rows name it: a retire whose file rows the
	// combine moved off the shell has none, only the shell's external-id and
	// merged-into rows, and those need a shell that is there.
	switch state, serr := ShellCheck(store, shellID); {
	case serr != nil:
		reasons = append(reasons, fmt.Sprintf("shell %s cannot be made present: %v", shellID, serr))
	case state == ShellRecreatable:
		recreatable[shellID] = true
	}
	for _, c := range plan.Order {
		if refusal := plan.Gate(c); refusal != nil {
			plan.Record(c, refusal)
			reasons = append(reasons, fmt.Sprintf("change %s (%s): %v", c.ID, c.ChangeType, refusal))
			continue
		}
		var v rowVerdict
		if recreatable[c.BookID] && shellRowType(c.ChangeType) {
			v = recreatableShellVerdict(store, c)
		} else {
			v = preflightRow(store, c, plan.Stamps, plan)
		}
		plan.Record(c, v.refusal)
		if v.refusal != nil {
			reasons = append(reasons, fmt.Sprintf("change %s (%s): %v", c.ID, c.ChangeType, v.refusal))
		}
	}
	return reasons, nil
}
