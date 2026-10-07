// file: internal/plugins/maintenance/fragment_owner_apply.go
// version: 1.2.0
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
// ITUNES TWINS (2026-10-07, docs/plans/2026-10-07-stale-itunes-path.md D7).
// A library copy made by itunes-clone-into-library shares a version group
// with its source: a non-primary book whose only file is the iTunes Media
// file the copy was cloned from (prod: 300 such pairs). Retiring the copy
// hands the group's primary on to that twin, which the iTunes guard refuses
// (and which would make a books/itunes book a visible duplicate), so the
// copy was held for good. The twin is the same audio as the parent's file:
// its recorded hash equals the copy's content digest, which equals the
// parent's file's (ownerTwins). Such a fragment goes on the owner row WITH
// its twins, whether or not its own row still carries an iTunes path (the
// twin's books/itunes file is what makes the row the owner's), and the
// owner's apply retires the twins into the parent first (non-primary: no
// demote, no crown), then the fragment, whose hand-off then finds nobody to
// crown. A twin's file is never read, moved or touched, and nothing in
// iTunes is written: its book row is merged into the parent and
// soft-deleted, its book_file row kept. The framework guard lifts its
// books/itunes check for the twins alone (Row.OwnerITunesDatabaseOnly).
// A twin qualifies only when ALL hold: the group is exactly the fragment
// and its twins (any other member keeps the fragment where it was); each
// twin is explicitly non-primary, one row, no iTunes persistent id on book
// or row, no row iTunes path, no itunes external id (tombstoned included),
// no live external id, no listening state; its recorded hash (book or row)
// equals the content digest; its file is on disk at the proof's size (stat
// only).
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
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
// the path-twin check). It returns why not ("" when it may) and the
// fragment's iTunes twins, which the owner row carries with it.
func (f *fragmentFixer) ownerEligible(lib *fragLibrary, parentID string, p fragPair, all []fragPair, probe *fragProbe) (whyNot string, twins []fragTwin) {
	c := p.Frag
	proof, ok := lib.contentProofOf(p)
	if !ok {
		return "the copy is not proven by content", nil
	}
	if pid := c.itunesPID(); pid != "" {
		return "it carries an " + pid + " (a retired iTunes id would queue an iTunes remove at the purge)", nil
	}
	if c.File.ITunesPath != "" {
		if why := ownerITunesPathWhyNot(c.File.ITunesPath, c.File.Path); why != "" {
			return why, nil
		}
	}
	if k, why := f.guard(lib, []fragBook{c.Book}, map[string][]string{c.Book.ID: {c.ImportPath}}); k != "" {
		return why, nil
	}
	if k, why := f.guard(lib, []fragBook{lib.books[parentID]}, nil); k != "" {
		return "parent: " + why, nil
	}
	if _, twin := twinEvidence(p.Evidence); twin {
		return "it shares its file with another fragment (path twin)", nil
	}
	for _, o := range all {
		if inner, twin := strings.CutPrefix(o.Evidence, fragEvTwinPrefix); twin && strings.HasPrefix(inner, c.Book.ID+",") {
			return "fragment " + o.Frag.Book.ID + " shares its file (path twin)", nil
		}
	}
	if what := carriesOnto(c, probe); what != "" {
		return what + " would have to move onto the parent", nil
	}
	if g := c.Book.VersionGroup; g != "" {
		if lib.books[parentID].VersionGroup == g {
			return "the parent is in its version group (the primary hand-off could write the parent)", nil
		}
		tw, why := f.ownerTwins(lib, g, c, proof, probe)
		if why != "" {
			return why, nil
		}
		twins = tw
		// Only this fragment and its twins are left out: another fragment
		// of the row in the same group is an iTunes-tracked book the
		// hand-off could crown.
		except := map[string]bool{c.Book.ID: true}
		for _, t := range twins {
			except[t.ID] = true
		}
		if why := lib.groupsITunesExcept(map[string]bool{g: true}, except); why != "" {
			return why, nil
		}
	}
	if c.File.ITunesPath == "" && len(twins) == 0 {
		return ownerNoITunesLink, nil
	}
	if _, doubt, err := lib.memberITunesWhy(lib.books[parentID]); err != nil {
		return fmt.Sprintf("parent %s cannot be read (%v)", parentID, err), nil
	} else if doubt {
		return fmt.Sprintf("whether parent %s is an iTunes copy cannot be told", parentID), nil
	}
	return "", twins
}

// ownerNoITunesLink is ownerEligible's answer for a fragment with nothing
// iTunes about it: not a reason worth showing (the row is an ordinary one).
const ownerNoITunesLink = "its row carries no iTunes path and its version group holds no iTunes twin"

// fragTwin is one iTunes twin of a fragment on the owner row (ownerTwins).
type fragTwin struct {
	ID, FileID, Path string
	// Hash is the twin's recorded hash that equals the content digest.
	Hash string
	Size int64
}

func (t fragTwin) fingerprint(fragID string) string {
	return strings.Join([]string{"itunes-twin", fragID, t.ID, t.FileID, t.Path, t.Hash, strconv.FormatInt(t.Size, 10)}, "|")
}

// ownerTwins lists fragment c's iTunes twins in its version group g (see
// the file comment), read fresh (lib.groupReads; a snapshot built by hand
// has none to read, and its groups are judged by groupsITunesExcept as
// before). A group with no iTunes member has no twins and no objection; a
// group whose iTunes members are not all twins, or that holds any other
// member beside them, returns why ("" none) and the fragment stays where it
// was.
func (f *fragmentFixer) ownerTwins(lib *fragLibrary, g string, c *fragCandidate, proof fragContentProof, probe *fragProbe) ([]fragTwin, string) {
	if lib.groupReads == nil {
		return nil, ""
	}
	books, err := lib.groupReads.GetBooksByVersionGroup(g)
	if err != nil {
		return nil, fmt.Sprintf("version group %s cannot be read (%v), so its members cannot be told", g, err)
	}
	var itunesMembers []database.Book
	others := 0
	for i := range books {
		b := &books[i]
		if b.ID == c.Book.ID || b.IsSoftDeleted() {
			continue
		}
		why, doubt, err := lib.memberITunesWhy(fragBookOf(b))
		switch {
		case err != nil:
			return nil, fmt.Sprintf("book %s of version group %s cannot be read (%v)", b.ID, g, err)
		case doubt:
			return nil, fmt.Sprintf("whether book %s of version group %s is an iTunes copy cannot be told", b.ID, g)
		case why == "":
			others++
		default:
			itunesMembers = append(itunesMembers, *b)
		}
	}
	if len(itunesMembers) == 0 {
		return nil, ""
	}
	if others > 0 {
		return nil, fmt.Sprintf("version group %s holds %d other book(s) beside its iTunes twin(s); only a group of the fragment and its twins is retired whole", g, others)
	}
	var twins []fragTwin
	for i := range itunesMembers {
		t, why := f.twinOf(lib, &itunesMembers[i], proof, probe)
		if why != "" {
			return nil, fmt.Sprintf("iTunes book %s of version group %s is not a twin it can be retired with: %s", itunesMembers[i].ID, g, why)
		}
		twins = append(twins, t)
	}
	sort.Slice(twins, func(i, j int) bool { return twins[i].ID < twins[j].ID })
	return twins, ""
}

// twinOf decides whether iTunes book b is a twin of the copy proof proves
// (see the file comment): why not, or the twin.
func (f *fragmentFixer) twinOf(lib *fragLibrary, b *database.Book, proof fragContentProof, probe *fragProbe) (fragTwin, string) {
	if b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
		return fragTwin{}, "it is primary (or its flag is unset)"
	}
	if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
		return fragTwin{}, "it carries book iTunes id " + *b.ITunesPersistentID
	}
	rows, err := lib.groupReads.GetBookFiles(b.ID)
	if err != nil {
		return fragTwin{}, fmt.Sprintf("its files cannot be read (%v)", err)
	}
	if len(rows) != 1 {
		return fragTwin{}, fmt.Sprintf("it holds %d file rows, not one", len(rows))
	}
	r := rows[0]
	switch {
	case r.ITunesPersistentID != "":
		return fragTwin{}, "its row carries iTunes id " + r.ITunesPersistentID
	case r.ITunesPath != "":
		return fragTwin{}, "its row carries iTunes path " + r.ITunesPath
	case r.Missing:
		return fragTwin{}, "its file is marked missing"
	}
	if lib.extIDs == nil {
		return fragTwin{}, "its external ids cannot be read"
	}
	exts, err := lib.extIDs(b.ID)
	if err != nil {
		return fragTwin{}, fmt.Sprintf("its external ids cannot be read (%v)", err)
	}
	for _, e := range exts {
		if e.Source == "itunes" {
			return fragTwin{}, "it carries itunes external id " + e.ExternalID
		}
		if !e.Tombstoned {
			return fragTwin{}, "its external id " + e.Source + "/" + e.ExternalID + " would have to move onto the parent"
		}
	}
	switch has, err := probe.has(b.ID); {
	case err != nil:
		return fragTwin{}, "listening state that cannot be ruled out (" + err.Error() + ")"
	case has:
		return fragTwin{}, "its listening state, positions or bookmarks would have to move onto the parent"
	}
	want := strings.TrimPrefix(proof.FragDigest, "sha256:")
	hash := ""
	for _, h := range []string{dcStr(b.FileHash), dcStr(b.OriginalFileHash), r.FileHash, r.OriginalFileHash} {
		if h != "" && strings.EqualFold(strings.TrimPrefix(h, "sha256:"), want) {
			hash = h
			break
		}
	}
	if want == "" || hash == "" {
		return fragTwin{}, "no hash it records equals the copy's content digest sha256:" + want
	}
	fi, err := f.statFn(r.FilePath)
	if err != nil {
		return fragTwin{}, fmt.Sprintf("its file cannot be stat'ed (%v)", err)
	}
	if fi.Size() != proof.FragSig.Size {
		return fragTwin{}, fmt.Sprintf("its file is %d bytes, the copy %d", fi.Size(), proof.FragSig.Size)
	}
	return fragTwin{ID: b.ID, FileID: r.ID, Path: r.FilePath, Hash: hash, Size: fi.Size()}, ""
}

// ownerRow is the owner row of parent parentID: the copy row parentRow makes
// of the owner-eligible pairs ps (their evidence, proofs and pairing), on
// row id "owner:<parent>", skipped, and marked for the owner. Its apply
// writes every fragment and never the parent.
func (f *fragmentFixer) ownerRow(lib *fragLibrary, parentID string, ps []fragPair, twinsOf map[string][]fragTwin) repairs.Row {
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
	var twinIDs []string
	twinsByFrag := map[string][]string{}
	for _, p := range ps {
		writes = append(writes, p.Frag.Book.ID)
		itPaths = append(itPaths, p.Frag.Book.ID+"="+p.Frag.File.ITunesPath)
		for _, t := range twinsOf[p.Frag.Book.ID] {
			twinIDs = append(twinIDs, t.ID)
			twinsByFrag[p.Frag.Book.ID] = append(twinsByFrag[p.Frag.Book.ID], t.ID)
			itPaths = append(itPaths, t.fingerprint(p.Frag.Book.ID))
			r.Evidence = append(r.Evidence, fmt.Sprintf("iTunes twin %s of fragment %s (%s): recorded hash %s equals the copy's content digest, %d bytes on disk; retired first, database rows only (its file is not touched)",
				t.ID, p.Frag.Book.ID, t.Path, t.Hash, t.Size))
			r.Members = append(r.Members, repairs.RowMember{BookID: t.ID, Title: lib.books[t.ID].Title, Role: "itunes-twin", Files: 1})
		}
	}
	r.OwnerApplicable = true
	link := "whose only iTunes link is each one's own row's iTunes path"
	if len(twinIDs) > 0 {
		link = fmt.Sprintf("each iTunes-linked by its own row's iTunes path or by an iTunes twin in its version group (%d twin(s): non-primary books whose one file is the iTunes library's copy of the same audio, retired first, database rows only)", len(twinIDs))
		r.SkipReason = fmt.Sprintf("%d fragment(s) copy parent %s and are iTunes-tracked (by their own row's iTunes path or by an iTunes twin in their version group; %d twin(s) go with them): never applied in bulk; only the owner applies them, with Apply (owner) on this row",
			n, parentID, len(twinIDs))
	}
	r.OwnerApplyReason = fmt.Sprintf("%d byte-identical cop%s of %s's files (sha256 of both files equal at plan time, re-checked by file identity at apply), %s; retired into %s writing the fragments alone. %s",
		n, plural(n, "y", "ies"), parentID, link, parent, fragOwnerApplyNote)
	sort.Strings(twinIDs)
	r.OwnerWrites = append(writes, twinIDs...)
	r.OwnerITunesDatabaseOnly = twinIDs
	r.BookIDs = uniqueSorted(append(r.BookIDs, twinIDs...))
	r.Proposed = map[string]string{"action": fmt.Sprintf("owner apply: retire %d fragment book(s) and %d iTunes twin(s): %s", n, len(twinIDs), fragOwnerApplyNote)}
	var st fragParentState
	if len(r.State) > 0 {
		if err := json.Unmarshal(r.State, &st); err != nil {
			return f.ownerRowUnusable(r, "the row's state is unreadable: "+err.Error())
		}
	}
	st.OwnerParent = parentID
	if len(twinsByFrag) > 0 {
		st.OwnerTwins = twinsByFrag
	}
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
	r.OwnerApplicable, r.OwnerApplyReason, r.OwnerITunesDatabaseOnly = false, "", nil
	r.SkipReason += "; not owner-applicable: " + why
	return r
}

// ownerRetireRefusal is the owner apply's under-lock check of fragment
// fragID before its first write, read fresh from store: no file of the
// fragment or the parent under a protected iTunes root, no other iTunes
// book (and not the parent) in the fragment's version group, nothing to
// carry onto the parent, and its row's iTunes path still its own file's,
// outside the iTunes library.
func (f *fragmentFixer) ownerRetireRefusal(store OpsStore, fragID, parentID string, twins []string) error {
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
		switch {
		case rows[0].ITunesPath != "":
			if why := ownerITunesPathWhyNot(rows[0].ITunesPath, rows[0].FilePath); why != "" {
				return refuse("fragment %s: %s", fragID, why)
			}
		case len(twins) == 0:
			// Neither an iTunes path of its own nor a twin: not a row
			// the owner's grant covers.
			return refuse("fragment %s carries no iTunes path and has no iTunes twin", fragID)
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
		except := map[string]bool{fragID: true}
		for _, t := range twins {
			except[t] = true
		}
		if why := lib.groupsITunesExcept(map[string]bool{g: true}, except); why != "" {
			return refuse("%s", why)
		}
	}
	for _, t := range twins {
		if err := ownerTwinRefusal(f.p, store, t, fb.VersionGroup, parentID); err != nil {
			return err
		}
	}
	exts, err := store.GetExternalIDsForBook(fragID)
	if err != nil {
		return fmt.Errorf("external ids of %s: %w", fragID, err)
	}
	return onlyRetireRefusal(f.p, fragID, parentID, "owner apply: the parent is never written", exts)
}

// ownerTwinRefusal is the owner apply's under-lock check of iTunes twin
// twinID before the row's first write, read fresh: still a live,
// explicitly non-primary member of the fragment's group g, no iTunes id on
// it or its one row, no row iTunes path, no itunes external id, and nothing
// to carry onto the parent (onlyRetireRefusal). The twin's file is under
// the iTunes library by design, so merge.GuardITunesProtected is NOT asked
// about it: its retire writes database rows only and never touches the
// file.
func ownerTwinRefusal(p *Plugin, store OpsStore, twinID, g, parentID string) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: under the merge lock (owner apply): iTunes twin %s: %s", repairs.ErrChangedSincePlan, twinID, fmt.Sprintf(format, args...))
	}
	b, err := store.GetBookByID(twinID)
	if err != nil {
		return fmt.Errorf("read %s: %w", twinID, err)
	}
	switch {
	case b == nil || b.IsSoftDeleted():
		return refuse("it is gone or retired")
	case g == "" || dcStr(b.VersionGroupID) != g:
		return refuse("it is no longer in the fragment's version group")
	case b.IsPrimaryVersion == nil || *b.IsPrimaryVersion:
		return refuse("it is primary now")
	case b.ITunesPersistentID != nil && *b.ITunesPersistentID != "":
		return refuse("it carries an iTunes id now")
	}
	rows, err := store.GetBookFiles(twinID)
	if err != nil {
		return fmt.Errorf("files of %s: %w", twinID, err)
	}
	if len(rows) != 1 || rows[0].ITunesPersistentID != "" || rows[0].ITunesPath != "" {
		return refuse("its file rows changed (%d rows, or an iTunes id or path on one)", len(rows))
	}
	exts, err := store.GetExternalIDsForBook(twinID)
	if err != nil {
		return fmt.Errorf("external ids of %s: %w", twinID, err)
	}
	for _, e := range exts {
		if e.Source == "itunes" {
			return refuse("it carries itunes external id %s now", e.ExternalID)
		}
	}
	return onlyRetireRefusal(p, twinID, parentID, "owner apply: the parent is never written", exts)
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
		if err := f.ownerRetireRefusal(store, id, ps.OwnerParent, ps.OwnerTwins[id]); err != nil {
			return err
		}
	}
	steps := 0
	for _, id := range frags {
		// The twins first: each is non-primary, so its retire demotes
		// nobody and crowns nobody; the fragment's hand-off then finds no
		// live member left to crown, and never asks to write an iTunes
		// book's primary flag.
		for _, t := range ps.OwnerTwins[id] {
			did, err := retireIntoOnly(ctx, f.p, store, w, f.now, fragFixerID, t, ps.OwnerParent,
				"owner apply: the parent is never written")
			steps += did
			if err != nil {
				if steps > 0 {
					return fmt.Errorf("%w: after %d step(s): %w", repairs.ErrPartiallyApplied, steps, err)
				}
				return err
			}
		}
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
