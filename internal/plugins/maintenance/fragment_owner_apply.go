// file: internal/plugins/maintenance/fragment_owner_apply.go
// version: 1.1.0
// guid: 4f8a2c61-9d37-4b05-a1e8-6c3b7d9e0f25
// last-edited: 2026-10-07

// OWNER APPLY of iTunes-tracked library copies (owner decision 2026-10-06:
// "list; I apply them", and only the owner, by clicking Apply on the row).
//
// A copy claimant whose only hands-off reason is the iTunes path on its own
// book_file row (on prod most library copies carry one naming their own
// file), proven a byte-identical copy of the parent row's file at plan time
// (CONTENT PROOF), is taken off its parent's copy row. All such fragments of
// one parent go on ONE row, "owner:<parent>", marked Row.OwnerApplicable
// (prod 2026-10-07: one parent's 300 chapter copies, each proven, were one
// manual-only copy row; one click applies them, not 300). The row stays
// skipped: no bulk selection, scheduled run or plain apply writes it. Only
// an apply holding the owner's grant for that row (repairs/owner.go: an
// interactive sign-in, never an API key) does, and then it writes DATABASE
// ROWS ONLY: for each fragment the fragment-only retire
// (retireIntoOnlyAllowingITunesPath: demote, merged_into, soft-delete, and
// the fragment's own version-group hand-off under the default iTunes
// guard). The parent is never written, no file is touched, and nothing
// under the iTunes library is written.
//
// ELIGIBLE (ownerEligible), each fragment, all of:
//   - a copy pair whose evidence is the content proof itself (a path twin,
//     an import-path or a hash match is not what the owner approved);
//   - the fragment and the parent pass the hands-off guard (no file under
//     books/itunes/**, no Doctor Who / Big Finish / Torchwood by path,
//     import path or series);
//   - the fragment's iTunes link is a row iTunes path and nothing else: no
//     iTunes persistent id on its book or row, no live itunes external id (a
//     retired book with an iTunes id would queue an iTunes remove at the
//     purge: an iTunes library write);
//   - that iTunes path names the fragment's own file (same file name, the
//     URL's percent-escapes decoded) and is not in an iTunes library tree (a
//     path into iTunes Media says iTunes tracks ANOTHER file, the one under
//     iTunes Media);
//   - nothing would have to move onto the parent: no live external id, no
//     listening state, positions or bookmarks (carriesOnto);
//   - the fragment is not one side of a path twin;
//   - the fragment's version group holds no OTHER iTunes book (another
//     fragment of the row included: a hand-off must never crown one), and
//     not the parent (the hand-off must not crown, and so write, the parent);
//   - the parent can be told (iTunes-linked or plain, not unreadable).
//
// RE-CHECKED UNDER THE MERGE LOCK in owner mode (Apply): the whole row is
// re-planned (every proof re-stat'ed, every rule above decided again), then
// for EVERY fragment, before the first write, the iTunes-protected-root
// guard on its and the parent's files, its group, its external ids and user
// state, and its row's iTunes path read fresh (ownerRetireRefusal). One
// refusal refuses the row with nothing written. The retire refuses an
// iTunes id itself. AllowITunesPath is set for these fragments only, and
// only under the owner's approval for this row.

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// fragOwnerApplyNote ends the owner row's owner-apply reason and proposed action.
const fragOwnerApplyNote = "iTunes tracks these files; only database rows change: each fragment is demoted, " +
	"merged into the parent and soft-deleted (the parent, the files and the iTunes library are not written)"

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
// here, so the folders are not compared). A file:// location is a URL, its
// name percent-encoded ("002%20of%20301.m4b" on prod); it is decoded first,
// and a name that does not decode is compared as written.
func itunesPathNamesFile(itunesPath, filePath string) bool {
	if itunesPath == "" || filePath == "" {
		return false
	}
	base := itunesPath
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSpace(base)
	want := path.Base(filePath)
	if strings.EqualFold(base, want) {
		return true
	}
	dec, err := url.PathUnescape(base)
	return err == nil && strings.EqualFold(dec, want)
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

// ownerEligible decides whether copy pair p of parent parentID may go on the
// parent's owner row (see the file comment); all are the parent's pairs (for
// the path-twin check). It returns why not ("" when it may).
func (f *fragmentFixer) ownerEligible(lib *fragLibrary, parentID string, p fragPair, all []fragPair, probe *fragProbe) (whyNot string) {
	c := p.Frag
	if _, ok := lib.contentProofOf(p); !ok {
		return "the copy is not proven by content"
	}
	if pid := c.itunesPID(); pid != "" {
		return "it carries an " + pid + " (a retired iTunes id would queue an iTunes remove at the purge)"
	}
	if why := ownerITunesPathWhyNot(c.File.ITunesPath, c.File.Path); why != "" {
		return why
	}
	if k, why := f.guard(lib, []fragBook{c.Book}, map[string][]string{c.Book.ID: {c.ImportPath}}); k != "" {
		return why
	}
	if k, why := f.guard(lib, []fragBook{lib.books[parentID]}, nil); k != "" {
		return "parent: " + why
	}
	if _, twin := twinEvidence(p.Evidence); twin {
		return "it shares its file with another fragment (path twin)"
	}
	for _, o := range all {
		if inner, twin := strings.CutPrefix(o.Evidence, fragEvTwinPrefix); twin && strings.HasPrefix(inner, c.Book.ID+",") {
			return "fragment " + o.Frag.Book.ID + " shares its file (path twin)"
		}
	}
	if what := carriesOnto(c, probe); what != "" {
		return what + " would have to move onto the parent"
	}
	if g := c.Book.VersionGroup; g != "" {
		if lib.books[parentID].VersionGroup == g {
			return "the parent is in its version group (the primary hand-off could write the parent)"
		}
		// Only this fragment is left out: another fragment of the row in
		// the same group is an iTunes-tracked book the hand-off could crown.
		if why := lib.groupsITunesExcept(map[string]bool{g: true}, map[string]bool{c.Book.ID: true}); why != "" {
			return why
		}
	}
	if _, doubt, err := lib.memberITunesWhy(lib.books[parentID]); err != nil {
		return fmt.Sprintf("parent %s cannot be read (%v)", parentID, err)
	} else if doubt {
		return fmt.Sprintf("whether parent %s is an iTunes copy cannot be told", parentID)
	}
	return ""
}

// ownerRow is the owner row of parent parentID: the copy row parentRow makes
// of the owner-eligible pairs ps (their evidence, proofs and pairing), on
// row id "owner:<parent>", skipped, and marked for the owner. Its apply
// writes every fragment and never the parent.
func (f *fragmentFixer) ownerRow(lib *fragLibrary, parentID string, ps []fragPair) repairs.Row {
	r := f.parentRow(lib, parentID, fragClassCopy, ps)
	base := r.Fingerprint
	r.RowID = fragRowOwner + ":" + parentID
	r.Class = fragClassManual
	r.Risk = repairs.RiskReview
	r.Skipped = repairs.SkipITunes
	n := len(ps)
	r.SkipReason = fmt.Sprintf("%d fragment(s) copy parent %s and are iTunes-tracked (each by its own row's iTunes path): never applied in bulk; only the owner applies them, with Apply (owner) on this row",
		n, parentID)
	parent := "a plain parent"
	if pwhy, _, _ := lib.memberITunesWhy(lib.books[parentID]); pwhy != "" {
		parent = "an iTunes-linked parent (" + pwhy + ")"
	}
	var writes []string
	var itPaths []string
	for _, p := range ps {
		writes = append(writes, p.Frag.Book.ID)
		itPaths = append(itPaths, p.Frag.Book.ID+"="+p.Frag.File.ITunesPath)
	}
	r.OwnerApplicable = true
	r.OwnerApplyReason = fmt.Sprintf("%d byte-identical cop%s of %s's files (sha256 of both files equal at plan time, re-checked by file identity at apply), whose only iTunes link is each one's own row's iTunes path; retired into %s writing the fragments alone. %s",
		n, plural(n, "y", "ies"), parentID, parent, fragOwnerApplyNote)
	r.OwnerWrites = writes
	r.Proposed = map[string]string{"action": fmt.Sprintf("owner apply: retire %d fragment book(s): %s", n, fragOwnerApplyNote)}
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
	sort.Strings(itPaths)
	r.Fingerprint = fragFingerprint(append([]string{"owner-apply", r.RowID, base}, itPaths...)...)
	return r
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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

// applyOwner writes the owner row under the owner's approval: every
// fragment is checked first (ownerRetireRefusal; one refusal refuses the
// row with nothing written), then each is retired into the parent its state
// names, fragment alone (retireIntoOnlyAllowingITunesPath). The parent is
// never written. Sequential on purpose: the row runs under one merge lock,
// and each retire's group hand-off must see the one before it.
func (f *fragmentFixer) applyOwner(ctx context.Context, store OpsStore, w *repairs.Writer, locked repairs.Row, plan []fragPair) error {
	var ps fragParentState
	if err := json.Unmarshal(locked.State, &ps); err != nil || ps.OwnerParent == "" {
		return fmt.Errorf("%w: under the merge lock (owner apply): the row names no parent", repairs.ErrChangedSincePlan)
	}
	if _, rowParent, _ := strings.Cut(locked.RowID, ":"); rowParent != ps.OwnerParent || len(plan) == 0 {
		return fmt.Errorf("%w: under the merge lock (owner apply): the row is not the owner row of parent %s", repairs.ErrChangedSincePlan, ps.OwnerParent)
	}
	frags := make([]string, 0, len(plan))
	for _, p := range plan {
		if p.Parent.BookID != ps.OwnerParent {
			return fmt.Errorf("%w: under the merge lock (owner apply): fragment %s is not a copy of parent %s", repairs.ErrChangedSincePlan, p.Frag.Book.ID, ps.OwnerParent)
		}
		frags = append(frags, p.Frag.Book.ID)
	}
	for _, id := range frags {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.ownerRetireRefusal(store, id, ps.OwnerParent); err != nil {
			return err
		}
	}
	steps := 0
	for _, id := range frags {
		did, err := retireIntoOnlyAllowingITunesPath(ctx, f.p, store, w, f.now, fragFixerID, id, ps.OwnerParent,
			"owner apply: the parent is never written")
		steps += did
		if err != nil {
			if steps > 0 {
				return fmt.Errorf("%w: after %d step(s): %w", repairs.ErrPartiallyApplied, steps, err)
			}
			return err
		}
	}
	return nil
}
