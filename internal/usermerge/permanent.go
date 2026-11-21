// file: internal/usermerge/permanent.go
// version: 0.1.0
// guid: 5fc732a5-a3ab-4a17-81ec-4fe29bf985fe
// last-edited: 2026-11-20

package usermerge

import (
	"errors"
	"fmt"
)

// ProblemMergePermanent is the problem code of the 409 the handler answers when
// an undo or a resume names a merge journal after the identity flip
// (F4-08 A9; 05-authn.md §11 "Merges at the cutover", D13).
const ProblemMergePermanent = "merge_permanent"

// ErrMergePermanent: the merge is permanent. HEAD's merge journals hold every
// key a merge wrote, and undoing or resuming one writes apikey:, oauthid:,
// invite: and u: keys that the identity cutover (M3) retires, while core's
// auth.Admin has no inverse of StageUserMerge: "no merge is undone after R2"
// (05-authn.md:1661), and "finished merges are permanent after R2"
// (05-authn.md:1366). A journal that is applying or undoing blocks the cutover
// at its preflight, so after the flip only finished journals exist, and every
// one of them is permanent.
//
// The handler maps it to 409 with ProblemMergePermanent (wired by the flip).
var ErrMergePermanent = errors.New("usermerge: " + ProblemMergePermanent + ": account merges finished before the identity flip can no longer be undone")

// CheckMergeAction is the check the handler runs before it enqueues an undo or
// a resume for journalID, with identityFlipped true once the store is at or
// past books' identity level. The flip level is an argument, not state, so the
// function decides from nothing else.
//
// The pre-R2 distinction cannot be read from a journal: it carries no version
// or era (HEAD is its only writer), and M3 stamps none. It is the store's
// level that says so, and once flipped every journal of every status is
// permanent, so no journal is read for its content:
//
//   - identityFlipped false: nil. HEAD's undo and resume stand as they are.
//   - an action other than ActionUndo or ActionResume: nil. An apply names no
//     journal yet; this check is only about reversing or finishing one.
//   - flipped, no such journal: ErrJournalNotFound (the 404 stays truthful).
//   - flipped, the journal row cannot be read: that error, which is a refusal
//     too; nothing is enqueued.
//   - flipped, a journal exists (applying, applied, undoing, undone, or a row
//     that does not decode, whose status cannot be told): ErrMergePermanent.
//     An undecodable journal fails closed: it is refused, never undone.
func (s *Service) CheckMergeAction(action Action, journalID string, identityFlipped bool) error {
	if !identityFlipped {
		return nil
	}
	if action != ActionUndo && action != ActionResume {
		return nil
	}
	raw, err := s.store.GetRaw(journalKey(journalID))
	if err != nil {
		return fmt.Errorf("read merge journal %s: %w", journalID, err)
	}
	if raw == nil {
		return fmt.Errorf("%w: %s", ErrJournalNotFound, journalID)
	}
	return ErrMergePermanent
}
