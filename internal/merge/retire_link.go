// file: internal/merge/retire_link.go
// version: 1.1.0
// guid: b3ee74d4-1ccc-4309-8939-7fe3a7b5f62f
// last-edited: 2026-10-24

package merge

import (
	"errors"
	"fmt"
	"strings"
)

// Combine-undo-aware retire (retired-versions plan 6.5, owner decision
// 2026-10-22). A trashed book that an unreversed combine journal names as
// absorbed can be retired into a holder's retired version; the combine's own
// undo must then reverse that retire first, never be made impossible by it.
//
// The retire links itself to the combine with one raw key per shell AND retire
// op,
//
//	merge:combine-retire:<combine journal id>:<shell book id>:<retire op id> -> <retire op id>
//
// written by repairs.Writer.RetireVersion BEFORE the retire's first change
// row, so UndoCombine finds "the retires of my absorbed shells" with a prefix
// scan instead of reading every operation. The op is part of the key so a
// second retire op of the same shell (after a partial revert of the first left
// some rows retired) adds a link instead of overwriting, and hiding, the
// first. The key and its value cannot share a commit with the change rows (the
// journal and the raw keys are different stores' writes), so the order carries
// the guarantee: a link with no live retire rows under it (the retire was cut
// off before writing, was reverted, or was voided) is dead and ignored; change
// rows without a link cannot exist.

// combineRetirePrefix is the key family of the link; registered in
// keyfamilies.go.
const combineRetirePrefix = "merge:combine-retire:"

// CombineRetireKey is the link key of a shell's retire, by op, into a combine.
func CombineRetireKey(combineID, shellID, retireOpID string) string {
	return combineRetirePrefix + combineID + ":" + shellID + ":" + retireOpID
}

// validRetireLinkPart rejects an id the key cannot carry: empty, or holding the
// ":" separator. The retire op id is last in the key and is taken whole, so it
// is only required to be non-empty.
func validRetireLinkPart(id string) bool { return id != "" && !strings.Contains(id, ":") }

// retireLinkWriter is the one raw write WriteCombineRetireLink makes.
type retireLinkWriter interface {
	SetRaw(key string, value []byte) error
}

// WriteCombineRetireLink records that retire op retireOpID retires shellID,
// an absorbed book of combine journal combineID. Idempotent: a resumed retire
// writes the same key and value again. A second op of the same shell is a
// second key.
func WriteCombineRetireLink(db retireLinkWriter, combineID, shellID, retireOpID string) error {
	switch {
	case db == nil:
		return errors.New("no store to write the combine-retire link to")
	case !validRetireLinkPart(combineID) || !validRetireLinkPart(shellID):
		return fmt.Errorf("combine-retire link: invalid combine id %q or shell id %q", combineID, shellID)
	case retireOpID == "":
		return errors.New("combine-retire link: no retire op id")
	}
	if err := db.SetRaw(CombineRetireKey(combineID, shellID, retireOpID), []byte(retireOpID)); err != nil {
		return fmt.Errorf("write combine-retire link for shell %s of combine %s (op %s): %w", shellID, combineID, retireOpID, err)
	}
	return nil
}

// CombineRetireLink is one link of a combine: the absorbed shell and the retire
// op that retired it.
type CombineRetireLink struct {
	ShellID, OpID string
}

// ListCombineRetireLinks returns the retire links of combine journal combineID,
// in key order (by shell, then op id). A link is only a pointer: whether the
// retire still has anything to reverse is the RetireUndoer's to say.
func ListCombineRetireLinks(kv journalKVReader, combineID string) ([]CombineRetireLink, error) {
	if !validRetireLinkPart(combineID) {
		return nil, fmt.Errorf("combine-retire links: invalid combine id %q", combineID)
	}
	prefix := combineRetirePrefix + combineID + ":"
	rows, err := kv.ScanPrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("scan combine-retire links of combine %s: %w", combineID, err)
	}
	links := make([]CombineRetireLink, 0, len(rows))
	for _, r := range rows {
		shell, op, ok := strings.Cut(strings.TrimPrefix(r.Key, prefix), ":")
		if !ok || shell == "" || op == "" || string(r.Value) != op {
			return nil, fmt.Errorf("combine-retire link %q is malformed", r.Key)
		}
		links = append(links, CombineRetireLink{ShellID: shell, OpID: op})
	}
	return links, nil
}

// RetireReversal is what a RetireUndoer reads about one retire op's rows for
// one shell, once, and how to reverse them from that same read. UndoCombine
// plans every link once (a retire op can hold ~190k rows, and it runs under the
// merge lock), judges the combine from the plans and then reverses from them.
//
// The rows are stale only toward "already reverted": the merge lock is held
// from the plan to the revert, so no writer that takes it moves a row in
// between, and the only writer that does not (a concurrent RevertOperation of
// the retire op marking rows reverted) is excluded by the operation lock
// Revert takes first; each row's case is a compare-and-set that finds a row
// already restored and counts it so.
type RetireReversal struct {
	// Live: the retire op still has pending (un-reverted, un-voided) rows for
	// the shell. Not live is a dead link: nothing to do.
	Live bool
	// Reasons is every reason the reversal cannot be made right now (a row
	// edited, moved or re-retired since, a row that does not parse, a shell
	// that cannot be recreated). Only meaningful when Live.
	Reasons []string
	// PendingFileIDs are the shell's book_file rows the retire took and has not
	// given back; RevertedFileIDs the ones it took and a revert gave back (live
	// on the shell again). Both are the shell's own rows, so the combine undo's
	// "given N files" check can count them as the shell's: the retire hides the
	// first from GetBookFiles, and a revert of it makes the second look gained.
	PendingFileIDs, RevertedFileIDs []string
	// Revert reverses the pending rows, from the rows this plan read. nil when
	// not Live.
	Revert func() error
}

// RetireUndoer reverses the retire of one shell, for UndoCombine. The merge
// package cannot import the revert machinery (audiobooks imports merge), so the
// undo is injected: audiobooks.RevertService wires one in where it builds the
// merge service for a combine row (SetRetireUndoer).
//
// PlanRetireRevert runs, and the Revert it returns runs, with the merge lock
// HELD (UndoCombine holds it, and it is not re-entrant), so an implementation
// must never take it (it may take locks that are only ever TRIED under it).
type RetireUndoer interface {
	// PlanRetireRevert reads retire op opID once and returns its rows for
	// shellID (see RetireReversal). It writes nothing.
	PlanRetireRevert(opID, shellID string) (*RetireReversal, error)
}

// SetRetireUndoer wires the undoer UndoCombine reverses linked retires with.
// Without one, an undo of a combine that has a live-looking link refuses
// (fail closed): it must not restore a shell a retire still holds the rows of.
func (ms *Service) SetRetireUndoer(u RetireUndoer) { ms.retireUndo = u }
