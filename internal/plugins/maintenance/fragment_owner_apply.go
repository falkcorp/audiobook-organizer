// file: internal/plugins/maintenance/fragment_owner_apply.go
// version: 1.0.0
// guid: 4f8a2c61-9d37-4b05-a1e8-6c3b7d9e0f25
// last-edited: 2026-10-06

// OWNER APPLY of an iTunes-tracked library copy (owner decision 2026-10-06:
// "list; I apply them", and only the owner, by clicking Apply on the row).
//
// A copy claimant whose only hands-off reason is the iTunes path on its own
// book_file row (on prod most library copies carry one naming their own
// file), proven a byte-identical copy of the parent row's file at plan time
// (CONTENT PROOF), is listed on its own "manual:<fragment>" row marked
// Row.OwnerApplicable. The row stays skipped: no bulk selection, scheduled
// run or plain apply writes it. Only an apply holding the owner's grant for
// that row (repairs/owner.go: an interactive sign-in, never an API key)
// does, and then it writes DATABASE ROWS ONLY, the fragment-only retire
// (retireIntoOnly's steps 3 and 4: demote, merged_into, soft-delete, and the
// fragment's own version-group hand-off). The parent is never written, no
// file is touched, and nothing under the iTunes library is written.
//
// ELIGIBLE (ownerEligible), all of:
//   - a copy pair whose evidence is the content proof itself (a path twin,
//     an import-path or a hash match is not what the owner approved);
//   - the fragment and the parent pass the hands-off guard (no file under
//     books/itunes/**, no Doctor Who / Big Finish / Torchwood by path,
//     import path or series);
//   - the fragment's iTunes link is a row iTunes path and nothing else: no
//     iTunes persistent id on its book or row, no live itunes external id (a
//     retired book with an iTunes id would queue an iTunes remove at the
//     purge: an iTunes library write);
//   - that iTunes path names the fragment's own file (same file name) and is
//     not in an iTunes library tree (a path into iTunes Media says iTunes
//     tracks ANOTHER file, the one under iTunes Media);
//   - nothing would have to move onto the parent: no live external id, no
//     listening state, positions or bookmarks (carriesOnto);
//   - the fragment is not one side of a path twin;
//   - the fragment's version group holds no OTHER iTunes book, and not the
//     parent (the hand-off must not crown, and so write, the parent);
//   - the parent can be told (iTunes-linked or plain, not unreadable).
//
// RE-CHECKED UNDER THE MERGE LOCK in owner mode (Apply): the whole row is
// re-planned (the proof re-stat'ed, every rule above decided again), then
// the iTunes-protected-root guard on both books' files, the fragment's
// group, its external ids and user state, and its row's iTunes path read
// fresh; the retire refuses an iTunes id itself. AllowITunesPath is set
// for this fragment only, and only under the owner's approval for this row.

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// fragOwnerApplyNote is the owner row's skip reason and owner-apply reason.
const fragOwnerApplyNote = "iTunes tracks this file; only database rows change: the fragment is demoted, " +
	"merged into the parent and soft-deleted (the parent, the file and the iTunes library are not written)"

// itunesPathInLibraryTree reports whether an iTunes location (a Windows
// path or a file:// URL) lies in an iTunes library tree: any folder named
// iTunes, iTunes Media or iTunes Music, or the books/itunes tree.
func itunesPathInLibraryTree(p string) bool {
	segs := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	for _, s := range segs {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "itunes", "itunes media", "itunes music":
			return true
		}
	}
	return false
}

// itunesPathNamesFile reports whether iTunes location itunesPath names the
// file at filePath: the same file name (the drive mapping is not known
// here, so the folders are not compared).
func itunesPathNamesFile(itunesPath, filePath string) bool {
	if itunesPath == "" || filePath == "" {
		return false
	}
	base := itunesPath
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	return strings.EqualFold(strings.TrimSpace(base), path.Base(filePath))
}

// ownerITunesPathWhyNot is why a fragment row's iTunes path keeps it from
// the owner's apply ("" none).
func ownerITunesPathWhyNot(itunesPath, filePath string) string {
	switch {
	case itunesPath == "":
		return "its row carries no iTunes path"
	case itunesPathInLibraryTree(itunesPath):
		return "its row's iTunes path " + itunesPath + " is in the iTunes library (iTunes tracks another file there)"
	case !itunesPathNamesFile(itunesPath, filePath):
		return "its row's iTunes path " + itunesPath + " does not name its own file " + filePath
	}
	return ""
}

// ownerEligible decides whether copy pair p of parent parentID may be listed
// for the owner's own apply (see the file comment); all are the row's pairs
// (for the path-twin check). It returns the owner-apply reason, or why not.
func (f *fragmentFixer) ownerEligible(lib *fragLibrary, parentID string, p fragPair, all []fragPair, probe *fragProbe) (reason, whyNot string) {
	c := p.Frag
	pr, ok := lib.contentProofOf(p)
	if !ok {
		return "", "the copy is not proven by content"
	}
	if pid := c.itunesPID(); pid != "" {
		return "", "it carries an " + pid + " (a retired iTunes id would queue an iTunes remove at the purge)"
	}
	if why := ownerITunesPathWhyNot(c.File.ITunesPath, c.File.Path); why != "" {
		return "", why
	}
	if k, why := f.guard(lib, []fragBook{c.Book}, map[string][]string{c.Book.ID: {c.ImportPath}}); k != "" {
		return "", why
	}
	if k, why := f.guard(lib, []fragBook{lib.books[parentID]}, nil); k != "" {
		return "", "parent: " + why
	}
	if _, twin := twinEvidence(p.Evidence); twin {
		return "", "it shares its file with another fragment (path twin)"
	}
	for _, o := range all {
		if inner, twin := strings.CutPrefix(o.Evidence, fragEvTwinPrefix); twin && strings.HasPrefix(inner, c.Book.ID+",") {
			return "", "fragment " + o.Frag.Book.ID + " shares its file (path twin)"
		}
	}
	if what := carriesOnto(c, probe); what != "" {
		return "", what + " would have to move onto the parent"
	}
	if g := c.Book.VersionGroup; g != "" {
		if lib.books[parentID].VersionGroup == g {
			return "", "the parent is in its version group (the primary hand-off could write the parent)"
		}
		if why := lib.groupsITunesExcept(map[string]bool{g: true}, map[string]bool{c.Book.ID: true}); why != "" {
			return "", why
		}
	}
	pwhy, doubt, err := lib.memberITunesWhy(lib.books[parentID])
	switch {
	case err != nil:
		return "", fmt.Sprintf("parent %s cannot be read (%v)", parentID, err)
	case doubt:
		return "", fmt.Sprintf("whether parent %s is an iTunes copy cannot be told", parentID)
	}
	parent := "a plain parent"
	if pwhy != "" {
		parent = "an iTunes-linked parent (" + pwhy + ")"
	}
	return fmt.Sprintf("byte-identical copy of %s's file (sha256:%s, read at plan time) whose only iTunes link is its own row's iTunes path %s; retired into %s writing the fragment alone. %s",
		parentID, pr.FragDigest, c.File.ITunesPath, parent, fragOwnerApplyNote), ""
}

// ownerRow is the owner-applicable manual row of copy pair p: the copy row
// parentRow makes of p alone (its evidence, proof state and pairing), on the
// fragment's own "manual:" row id, skipped, and marked for the owner.
func (f *fragmentFixer) ownerRow(lib *fragLibrary, parentID string, p fragPair, reason string) repairs.Row {
	r := f.parentRow(lib, parentID, fragClassCopy, []fragPair{p})
	base := r.Fingerprint
	r.RowID = "manual:" + p.Frag.Book.ID
	r.Class = fragClassManual
	r.Risk = repairs.RiskReview
	r.Skipped = repairs.SkipITunes
	r.SkipReason = fmt.Sprintf("fragment copies parent %s row %s and is iTunes-tracked (%s): never applied in bulk; only the owner applies it, with Apply (owner) on this row",
		parentID, p.Parent.ID, p.Frag.itunesWhy())
	r.OwnerApplicable, r.OwnerApplyReason = true, reason
	r.OwnerWrites = []string{p.Frag.Book.ID}
	r.Proposed = map[string]string{"action": "owner apply: " + fragOwnerApplyNote}
	var st fragParentState
	if len(r.State) > 0 {
		if err := json.Unmarshal(r.State, &st); err != nil {
			return f.ownerRowUnusable(r, "the row's state is unreadable: "+err.Error())
		}
	}
	st.OwnerParent = parentID
	raw, err := json.Marshal(st)
	if err != nil {
		return f.ownerRowUnusable(r, "cannot store the row's state: "+err.Error())
	}
	r.State = raw
	r.Fingerprint = fragFingerprint("owner-apply", r.RowID, base, p.Frag.File.ITunesPath)
	return r
}

func (f *fragmentFixer) ownerRowUnusable(r repairs.Row, why string) repairs.Row {
	r.OwnerApplicable, r.OwnerApplyReason = false, ""
	r.SkipReason += "; not owner-applicable: " + why
	return r
}

// ownerRetireRefusal is the owner apply's under-lock check of fragment
// fragID before its first write, read fresh from store: no file of the
// fragment or the parent under a protected iTunes root, no other iTunes
// book (and not the parent) in the fragment's version group, nothing to
// carry onto the parent, and its row's iTunes path still its own file's,
// outside the iTunes library.
func (f *fragmentFixer) ownerRetireRefusal(store OpsStore, fragID, parentID string) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: under the merge lock (owner apply): %s", repairs.ErrChangedSincePlan, fmt.Sprintf(format, args...))
	}
	if err := merge.GuardITunesProtected(store, []string{fragID, parentID}); err != nil {
		if errors.Is(err, merge.ErrITunesProtected) {
			return refuse("%v", err)
		}
		return err
	}
	b, err := store.GetBookByID(fragID)
	if err != nil {
		return fmt.Errorf("read %s: %w", fragID, err)
	}
	if b == nil {
		return refuse("fragment %s vanished", fragID)
	}
	if !b.IsSoftDeleted() {
		rows, err := store.GetBookFiles(fragID)
		if err != nil {
			return fmt.Errorf("files of %s: %w", fragID, err)
		}
		if len(rows) != 1 {
			return refuse("fragment %s holds %d rows, not its one planned row", fragID, len(rows))
		}
		if why := ownerITunesPathWhyNot(rows[0].ITunesPath, rows[0].FilePath); why != "" {
			return refuse("fragment %s: %s", fragID, why)
		}
	}
	fb := fragBookOf(b)
	if g := fb.VersionGroup; g != "" {
		pb, err := store.GetBookByID(parentID)
		if err != nil {
			return fmt.Errorf("read %s: %w", parentID, err)
		}
		if pb != nil && pb.VersionGroupID != nil && *pb.VersionGroupID == g {
			return refuse("parent %s is in fragment %s's version group", parentID, fragID)
		}
		lib := newFragLibrary()
		lib.extIDs, lib.groupReads = store.GetExternalIDsForBook, store
		if why := lib.groupsITunesExcept(map[string]bool{g: true}, map[string]bool{fragID: true}); why != "" {
			return refuse("%s", why)
		}
	}
	exts, err := store.GetExternalIDsForBook(fragID)
	if err != nil {
		return fmt.Errorf("external ids of %s: %w", fragID, err)
	}
	return onlyRetireRefusal(f.p, fragID, parentID, "owner apply: the parent is never written", exts)
}

// applyOwner writes an owner-applicable row under the owner's approval: the
// fragment-only retire of its one fragment into the parent its state names,
// after ownerRetireRefusal. The parent is never written.
func (f *fragmentFixer) applyOwner(ctx context.Context, store OpsStore, w *repairs.Writer, locked repairs.Row, plan []fragPair) error {
	var ps fragParentState
	if err := json.Unmarshal(locked.State, &ps); err != nil || ps.OwnerParent == "" {
		return fmt.Errorf("%w: under the merge lock (owner apply): the row names no parent", repairs.ErrChangedSincePlan)
	}
	if len(plan) != 1 || plan[0].Parent.BookID != ps.OwnerParent {
		return fmt.Errorf("%w: under the merge lock (owner apply): the row is not one fragment of parent %s", repairs.ErrChangedSincePlan, ps.OwnerParent)
	}
	fragID := plan[0].Frag.Book.ID
	if err := f.ownerRetireRefusal(store, fragID, ps.OwnerParent); err != nil {
		return err
	}
	steps, err := retireIntoWith(ctx, f.p, store, w, f.now, fragFixerID, fragID, ps.OwnerParent, nil,
		retireOpts{Only: "owner apply: the parent is never written", AllowITunesPath: true})
	if err != nil && steps > 0 {
		return fmt.Errorf("%w: after %d step(s): %w", repairs.ErrPartiallyApplied, steps, err)
	}
	return err
}
