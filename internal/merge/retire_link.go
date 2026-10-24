// file: internal/merge/retire_link.go
// version: 1.0.0
// guid: b3ee74d4-1ccc-4309-8939-7fe3a7b5f62f
// last-edited: 2026-10-23

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
// The retire links itself to the combine with one raw key per shell,
//
//	merge:combine-retire:<combine journal id>:<shell book id> -> <retire op id>
//
// written by repairs.Writer.RetireVersion BEFORE the retire's first change
// row, so UndoCombine finds "the retires of my absorbed shells" with a prefix
// scan instead of reading every operation. The key and its value cannot share
// a commit with the change rows (the journal and the raw keys are different
// stores' writes), so the order carries the guarantee: a link with no live
// retire rows under it (the retire was cut off before writing, was reverted,
// or was voided) is dead and ignored; change rows without a link cannot exist.

// combineRetirePrefix is the key family of the link; registered in
// keyfamilies.go.
const combineRetirePrefix = "merge:combine-retire:"

// CombineRetireKey is the link key of a shell's retire into a combine.
func CombineRetireKey(combineID, shellID string) string {
	return combineRetirePrefix + combineID + ":" + shellID
}

// validRetireLinkPart rejects an id the key cannot carry: empty, or holding the
// ":" separator.
func validRetireLinkPart(id string) bool { return id != "" && !strings.Contains(id, ":") }

// retireLinkWriter is the one raw write WriteCombineRetireLink makes.
type retireLinkWriter interface {
	SetRaw(key string, value []byte) error
}

// WriteCombineRetireLink records that retire op retireOpID retires shellID,
// an absorbed book of combine journal combineID. Idempotent: a resumed retire
// writes the same value again.
func WriteCombineRetireLink(db retireLinkWriter, combineID, shellID, retireOpID string) error {
	switch {
	case db == nil:
		return errors.New("no store to write the combine-retire link to")
	case !validRetireLinkPart(combineID) || !validRetireLinkPart(shellID):
		return fmt.Errorf("combine-retire link: invalid combine id %q or shell id %q", combineID, shellID)
	case retireOpID == "":
		return errors.New("combine-retire link: no retire op id")
	}
	if err := db.SetRaw(CombineRetireKey(combineID, shellID), []byte(retireOpID)); err != nil {
		return fmt.Errorf("write combine-retire link for shell %s of combine %s: %w", shellID, combineID, err)
	}
	return nil
}

// CombineRetireLink is one link of a combine: the absorbed shell and the retire
// op that retired it.
type CombineRetireLink struct {
	ShellID, OpID string
}

// ListCombineRetireLinks returns the retire links of combine journal combineID,
// in key order. A link is only a pointer: whether the retire still has anything
// to reverse is the RetireUndoer's to say.
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
		shell := strings.TrimPrefix(r.Key, prefix)
		if shell == "" || shell == r.Key || len(r.Value) == 0 {
			return nil, fmt.Errorf("combine-retire link %q is malformed", r.Key)
		}
		links = append(links, CombineRetireLink{ShellID: shell, OpID: string(r.Value)})
	}
	return links, nil
}

// RetireUndoer reverses the retire of one shell, for UndoCombine. The merge
// package cannot import the revert machinery (audiobooks imports merge), so the
// undo is injected: audiobooks.RevertService wires one in where it builds the
// merge service for a combine row (SetRetireUndoer).
//
// Both methods run with the merge lock HELD (UndoCombine holds it, and it is
// not re-entrant), so an implementation must never take it.
type RetireUndoer interface {
	// CheckRetireRevert reads, writes nothing, and reports whether retire op
	// opID still has rows for shellID to reverse (live) and every reason that
	// reversal cannot be made right now (a row edited, moved or re-retired since,
	// a shell that cannot be recreated). live false with no reasons is a dead
	// link: nothing to do.
	CheckRetireRevert(opID, shellID string) (live bool, reasons []string, err error)
	// RevertRetire reverses the retire's rows for shellID: file rows back under
	// the shell, the retired version and its mappings removed, the shell's
	// pointers and external ids restored. It marks the rows it restored
	// reverted.
	RevertRetire(opID, shellID string) error
}

// SetRetireUndoer wires the undoer UndoCombine reverses linked retires with.
// Without one, an undo of a combine that has a live-looking link refuses
// (fail closed): it must not restore a shell a retire still holds the rows of.
func (ms *Service) SetRetireUndoer(u RetireUndoer) { ms.retireUndo = u }
