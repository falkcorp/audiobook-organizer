// file: internal/plugins/maintenance/consolidation_leftovers_samepath.go
// version: 1.3.0
// guid: 7285fcc4-a331-4b0a-89c0-e606c9f5f7b3
// last-edited: 2026-10-06

// The same-path-twin class of the consolidation-leftovers fixer (owner
// decision 2026-10-06, plan op 01M48JSTKZV97BK42W675089GY): a leftover whose
// book_file rows are all gone from disk but whose own file_path is on disk
// and owned by another live book. Example: leftover "35 - Splashdown" (0 min)
// names the same .../35 - Splashdown/35 - Splashdown.m4b as live book
// "35 - Splashdown" (15.7 min). Until then such a row was held as
// held_book_path_present (255 of them in that plan), and a size+hash match
// of its dead row could point at a third book ("38 - Splashdown" through a
// _copy14 file). The owner's rule: fold the leftover into the live book on
// the same path.

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// leftoverPathRowsReader lists EVERY book_file row at a path. It is on
// database.Store but not on OpsStore, so it is asserted rather than widening
// OpsStore (bookJournalReader's pattern).
type leftoverPathRowsReader interface {
	BookFilesAtPath(path string) ([]database.BookFile, error)
}

// leftoverPathOwners indexes the plan's snapshot by path: every book whose
// file_path is the path, and every book with a book_file row at it. Dead
// books are left in; samePath filters on its own read.
func leftoverPathOwners(books map[string]*database.BookCore, rowsOf map[string][]database.BookFileCore) map[string][]string {
	out := map[string][]string{}
	add := func(p, id string) {
		if p != "" && !contains(out[p], id) {
			out[p] = append(out[p], id)
		}
	}
	for id, b := range books {
		add(b.FilePath, id)
	}
	for id, rs := range rowsOf {
		for _, r := range rs {
			add(r.FilePath, id)
		}
	}
	return out
}

// leftoverFreshPathOwners is the re-plan's fresh read of the same set: the
// multi-valued row and book-path indexes, never the single-owner
// GetBookFileByPath (last writer wins). A read that cannot be complete is an
// error, never "no owner": the re-plan then fails and nothing is applied.
func leftoverFreshPathOwners(store OpsStore, p string) ([]string, error) {
	pr, ok := store.(leftoverPathRowsReader)
	if !ok {
		return nil, errors.New("the store cannot list every book_file row at a path")
	}
	rows, err := pr.BookFilesAtPath(p)
	if err != nil {
		return nil, fmt.Errorf("book_file rows at %s: %w", p, err)
	}
	ids, err := store.LiveBookIDsAtPath(p)
	if err != nil {
		return nil, fmt.Errorf("live books at %s: %w", p, err)
	}
	for _, r := range rows {
		if !contains(ids, r.BookID) {
			ids = append(ids, r.BookID)
		}
	}
	return ids, nil
}

// leftoverFinish closes a row (decide's finish).
type leftoverFinish func(class, skip, why string) (repairs.Row, bool, error)

// leftoverITunesOf is decide's iTunes check of one book.
//
// survivor counts a row's bare iTunes path reference too (the book the
// retire writes); the retired leftover's side does not (owner decision
// 2026-10-06, leftoverITunesWhy).
type leftoverITunesOf func(bc *database.BookCore, rs []database.BookFileCore, survivor bool) (string, bool, error)

// samePath decides a leftover whose own file_path is on disk. core.FilePath
// is that path; rows are the leftover's book_file rows, every one gone from
// disk (decide checked). The row is applicable only when exactly ONE other
// live book owns the path (a book whose only row there is Missing does not),
// and that owner:
//   - is listed by Audiobookshelf and owns the path through a live row;
//   - is not iTunes-owned, a row's iTunes path reference included
//     (itunesOwnershipWhy);
//   - is the same work: titles, authors, ASINs and same-source external ids
//     do not disagree (samePathIdentity);
//   - has a file whose recorded length does not contradict the leftover's;
//   - ends up its version group's sole live primary after the retire's
//     hand-off in the leftover's group (leftoverHandOffHold), with no iTunes
//     copy in either group that is not explicitly non-primary.
//
// Owner-manual (Doctor Who / Big Finish / Torchwood) books are held by the
// framework guard, which reads every id in row.BookIDs, the owner's
// included, at plan and again at apply. No owner at all is the old
// held_book_path_present. A position or a finished state is carried only
// with positive same-audio evidence (size, hash or agreeing lengths).
func (s *leftoverSource) samePath(ctx context.Context, core *database.BookCore, rows []database.BookFileCore,
	row *repairs.Row, fp *strings.Builder, finish leftoverFinish, itunesOf leftoverITunesOf) (repairs.Row, bool, error) {
	id, shared := core.ID, core.FilePath
	got, err := s.owners(shared)
	if err != nil {
		return repairs.Row{}, false, err
	}
	// A copy: the plan's path index hands every worker the same slice, and
	// two leftovers on one path are decided concurrently.
	cands := append([]string(nil), got...)
	sort.Strings(cands)
	var owners, stale []string
	books := map[string]*database.BookCore{}
	ownerRows := map[string][]database.BookFileCore{}
	for _, c := range cands {
		if err := ctx.Err(); err != nil {
			return repairs.Row{}, false, err
		}
		if c == id {
			continue
		}
		b, err := s.book(c)
		if err != nil {
			return repairs.Row{}, false, err
		}
		if b == nil || b.IsSoftDeleted() {
			continue
		}
		rs, err := s.rows(c)
		if err != nil {
			return repairs.Row{}, false, err
		}
		// A book whose only reference to the path is a row already marked
		// Missing does not own the file: it is named, not counted.
		live := b.FilePath == shared
		for _, r := range rs {
			if r.FilePath == shared && !r.Missing {
				live = true
				break
			}
		}
		if !live {
			stale = append(stale, c)
			continue
		}
		owners = append(owners, c)
		books[c], ownerRows[c] = b, rs
	}
	if len(stale) > 0 {
		row.Evidence = append(row.Evidence, fmt.Sprintf("%s reference(s) %s only through a row marked missing; not counted as owner(s)",
			strings.Join(stale, ", "), shared))
	}
	if len(owners) == 0 {
		return finish(leftoverClassHeld, leftoverSkipBookPath,
			fmt.Sprintf("the book's own path %s is on disk: a repoint candidate, not a leftover", shared))
	}
	row.Current["shared_path"] = shared
	for _, o := range owners {
		row.BookIDs = appendUnique(row.BookIDs, o)
	}
	fmt.Fprintf(fp, "samepath|%s|%s\n", shared, strings.Join(owners, ","))
	if len(owners) > 1 {
		return finish(leftoverClassHeld, leftoverSkipSamePathOwners,
			fmt.Sprintf("%d live books own the leftover's path %s: %s", len(owners), shared, strings.Join(owners, ", ")))
	}
	// iTunes on the leftover's side, as for every other class (a bare row
	// iTunes path reference does not count: owner decision 2026-10-06).
	why, doubt, err := itunesOf(core, rows, false)
	if err != nil {
		return repairs.Row{}, false, err
	}
	switch {
	case doubt:
		return finish(leftoverClassHeld, leftoverSkipITunesDoubt, "could not tell whether the leftover is an iTunes book")
	case why != "":
		return finish(leftoverClassITunes, leftoverSkipITunes, "the leftover is an iTunes book ("+why+"); never written")
	}
	oid := owners[0]
	ob := books[oid]
	orows := ownerRows[oid]
	// The owner's LIVE rows, in a slice of their own: a Missing row is no
	// file of the owner's timeline (it neither decides whole-book against
	// slice nor counts toward the slice offset), and sorting a new slice
	// leaves the source's rows alone (the plan's s.rows hands out a copy,
	// but nothing here should depend on that).
	live := make([]database.BookFileCore, 0, len(orows))
	for _, r := range orows {
		if !r.Missing {
			live = append(live, r)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].ID < live[j].ID })
	var at *database.BookFileCore
	for i := range live {
		if live[i].FilePath == shared {
			at = &live[i]
			break
		}
	}
	// The owner's length for the row: its file at the path when known, else
	// the book's (seconds, as every book_file duration).
	ownerDur := 0
	if at != nil && at.Duration > 0 {
		ownerDur = at.Duration
	} else if ob.Duration != nil && *ob.Duration > 0 {
		ownerDur = *ob.Duration
	}
	row.Members = append(row.Members, repairs.RowMember{BookID: oid, Title: ob.Title, Role: leftoverRolesSamePath,
		Files: len(orows), MissingFiles: countMissing(orows)})
	row.Current["owner_book"] = fmt.Sprintf("%s %s (%s)", oid, ob.Title, leftoverDurText(ownerDur))
	if !database.ABSLibraryFilter().MatchesCore(ob) {
		return finish(leftoverClassHeld, leftoverSkipSamePathNotListed, fmt.Sprintf(
			"the same-path owner %s is not listed by Audiobookshelf (not an organized, unquarantined primary): its listening state would be stranded", oid))
	}
	// The owner is the survivor the retire writes: every iTunes signal
	// counts, a row's iTunes path reference included.
	why, doubt, err = itunesOf(ob, orows, true)
	if err != nil {
		return repairs.Row{}, false, err
	}
	switch {
	case doubt:
		return finish(leftoverClassHeld, leftoverSkipSamePathOwnerITunes, fmt.Sprintf("could not tell whether the same-path owner %s is an iTunes book", oid))
	case why != "":
		return finish(leftoverClassHeld, leftoverSkipSamePathOwnerITunes, fmt.Sprintf(
			"the same-path owner %s is an iTunes book (%s): the active iTunes library is never written", oid, why))
	}
	if at == nil {
		return finish(leftoverClassHeld, leftoverSkipSamePathNoRow, fmt.Sprintf(
			"%s names %s only as its book path, with no file row of its own there: the audio cannot be compared", oid, shared))
	}
	fmt.Fprintf(fp, "owner|%s|%s|%d|%s|%s|%d|%d|%d\n", at.ID, at.FilePath, at.FileSize, at.FileHash, at.OriginalFileHash, at.Duration, ownerDur, len(live))
	// Identity: a shared stale path is not enough to fold two different
	// books together.
	if held, why, err := s.samePathIdentity(core, ob, row, fp); err != nil {
		return repairs.Row{}, false, err
	} else if held {
		return finish(leftoverClassHeld, leftoverSkipSamePathIdentity, why)
	}
	// The leftover's own audio, as far as it is recorded. Its length (the
	// sum of its rows' when every row has one, else the book's) against the
	// owner's file at the path (else the owner book's, when that file is
	// its only live one), within 2 s: a contradiction holds the row. A zero
	// or unknown length is no evidence either way (the 2026-10-06 leftovers
	// are 0 min).
	leftDur, leftSize := 0, int64(0)
	for _, r := range rows {
		if r.Duration <= 0 {
			leftDur = -1
		} else if leftDur >= 0 {
			leftDur += r.Duration
		}
		if r.FileSize <= 0 {
			leftSize = -1
		} else if leftSize >= 0 {
			leftSize += r.FileSize
		}
	}
	if leftDur <= 0 {
		leftDur = 0
		if core.Duration != nil && *core.Duration > 0 {
			leftDur = *core.Duration
		}
	}
	if leftSize < 0 {
		leftSize = 0
	}
	cmpDur := at.Duration
	if cmpDur <= 0 && len(live) == 1 && ob.Duration != nil {
		cmpDur = *ob.Duration
	}
	row.Current["leftover_audio"] = fmt.Sprintf("%d dead row(s), %s, %s", len(rows), leftoverSizeText(leftSize), leftoverDurText(leftDur))
	row.Current["owner_audio"] = fmt.Sprintf("row %s, %d bytes, %s", at.ID, at.FileSize, leftoverDurText(cmpDur))
	fmt.Fprintf(fp, "dur|%d|%d|%d\n", leftDur, cmpDur, leftSize)
	if leftDur > 0 && cmpDur > 0 && !leftoverSameDuration(leftDur, cmpDur) {
		return finish(leftoverClassHeld, leftoverSkipSamePathAudio, fmt.Sprintf(
			"the leftover records %s of audio and the owner's file at %s %s: different audio", leftoverDurText(leftDur), shared, leftoverDurText(cmpDur)))
	}
	// Positive same-audio evidence, which alone lets a position or a
	// finished state follow: the dead rows' total size equals the owner
	// file's, a dead row's hash is the owner file's, or both lengths are
	// known and agree. The shared book path proves nothing about the audio:
	// the dead rows sit at other paths by construction, and the link is only
	// the leftover's stale file_path.
	evidence := ""
	switch {
	case leftSize > 0 && leftSize == at.FileSize:
		evidence = fmt.Sprintf("size: the dead row(s) total %d bytes, the owner's file %d", leftSize, at.FileSize)
	case leftoverSharesHash(rows, *at):
		evidence = "hash: a dead row's hash is the owner file's"
	case leftDur > 0 && cmpDur > 0:
		evidence = fmt.Sprintf("duration: %s on both sides", leftoverDurText(cmpDur))
	}
	if evidence == "" {
		row.Current["same_audio_evidence"] = "none: no size, hash or length agrees, so no position and no finished state is carried"
	} else {
		row.Current["same_audio_evidence"] = evidence
	}
	fmt.Fprintf(fp, "evidence|%s\n", evidence)
	// Version groups. The retire's primary hand-off writes the leftover's
	// group G; the owner's group is read because the hand-off may crown its
	// members when it is G, and an iTunes copy in either that is not
	// explicitly non-primary holds the row.
	gid, ogid := dcStr(core.VersionGroupID), dcStr(ob.VersionGroupID)
	dc := &duplicateCopiesFixer{}
	for _, g := range []string{gid, ogid} {
		if g == "" {
			continue
		}
		why, err := dc.itunesWouldBeWritten(s.store, g, []string{id})
		if err != nil {
			return repairs.Row{}, false, err
		}
		if why != "" {
			return finish(leftoverClassHeld, leftoverSkipSamePathITunesGroup, why)
		}
	}
	fmt.Fprintf(fp, "groups|%s|%s\n", gid, ogid)
	if gid != "" {
		group, err := s.store.GetBooksByVersionGroup(gid)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("version group %s: %w", gid, err)
		}
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		if why := leftoverHandOffHold(s.store, group, id, oid, ob, ogid == gid); why != "" {
			return finish(leftoverClassHeld, leftoverSkipSamePathPrimary, why)
		}
		for i := range group {
			if group[i].ID != id && !contains(row.BookIDs, group[i].ID) {
				row.BookIDs = appendUnique(row.BookIDs, group[i].ID)
				row.Members = append(row.Members, repairs.RowMember{BookID: group[i].ID, Title: group[i].Title, Role: leftoverRolesGroupSibling})
			}
			fmt.Fprintf(fp, "member|%s|%s|%v\n", group[i].ID, storedPrimaryFlag(group[i].IsPrimaryVersion), group[i].IsSoftDeleted())
		}
	}
	// The listening state. With same-audio evidence: the whole-book rule
	// when the owner's one live file is that file (the farther-ahead state
	// wins, finished carries), else a slice at the file's place over the
	// owner's live rows. Without it: a slice that is not mappable, so every
	// user's state follows with no position and never as finished (merge's
	// slice rule) rather than holding the row: the owner decided these fold
	// into the same-path book, and nothing unproven is carried.
	plan := &leftoverPlan{Leftover: id, Combined: oid, GroupID: gid, OwnerGroupID: ogid}
	var carry string
	switch {
	case evidence == "":
		carry = "listening state without a position and never as finished (no same-audio evidence)"
	case len(live) == 1:
		plan.WholeBook = true
		carry = "listening state and positions by the whole-book rule (the farther-ahead state wins)"
	default:
		plan.Slice = leftoverSlice(live, *at)
		if plan.Slice.Mappable {
			carry = "listening positions as a slice at " + strconv.FormatFloat(plan.Slice.OffsetSeconds, 'f', 0, 64) + " s of the owner"
		} else {
			carry = "listening state without a position (the file's place in the owner is unknown)"
		}
	}
	fmt.Fprintf(fp, "carry|%v|%v|%.3f\n", plan.WholeBook, plan.Slice.Mappable, plan.Slice.OffsetSeconds)
	exts, err := s.store.GetExternalIDsForBook(id)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("external ids of %s: %w", id, err)
	}
	var moved []string
	for _, e := range exts {
		if !e.Tombstoned {
			moved = append(moved, e.Source+"/"+e.ExternalID)
		}
	}
	sort.Strings(moved)
	if len(moved) > 0 {
		carry += "; external ids " + strings.Join(moved, ", ")
	} else {
		carry += "; no external ids"
	}
	row.Current["carries"] = carry
	state := leftoverState{Combined: oid, BookPath: shared}
	for _, r := range rows {
		if r.Missing {
			continue
		}
		state.NotMissing = append(state.NotMissing, r.ID)
		plan.Marks = append(plan.Marks, leftoverMark{RowID: r.ID,
			Was: undo.BookFileLocation{Path: r.FilePath, Missing: false, Hash: r.FileHash, Size: r.FileSize}})
		row.Evidence = append(row.Evidence, fmt.Sprintf("%s is gone from disk", r.FilePath))
	}
	row.Evidence = append(row.Evidence, fmt.Sprintf("the leftover's book path %s is on disk and owned only by %s (row %s, %d bytes)",
		shared, oid, at.ID, at.FileSize))
	raw, err := json.Marshal(state)
	if err != nil {
		return repairs.Row{}, false, err
	}
	row.State = raw
	row.Detail = plan
	row.Proposed = map[string]string{"action": fmt.Sprintf(
		"mark %d dead row(s) missing (kept, not deleted); retire the leftover into %s %q, the live book on its own path, carrying %s",
		len(plan.Marks), oid, ob.Title, carry)}
	return finish(leftoverClassSamePath, "", fmt.Sprintf(
		"a consolidation leftover whose own path %s is owned by one live book, %s: the same audio, so the leftover folds into it", shared, oid))
}

// leftoverDurText is a duration in seconds for the plan row ("15m42s"), or
// "unknown length".
func leftoverDurText(sec int) string {
	if sec <= 0 {
		return "unknown length"
	}
	return (time.Duration(sec) * time.Second).String()
}

// leftoverSizeText is a byte total for the plan row, or "unknown size".
func leftoverSizeText(n int64) string {
	if n <= 0 {
		return "unknown size"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// leftoverSharesHash reports whether any hash of the leftover's rows is one
// of the owner file's (current or original).
func leftoverSharesHash(rows []database.BookFileCore, at database.BookFileCore) bool {
	ah := leftoverHashes(at)
	for _, r := range rows {
		for _, h := range leftoverHashes(r) {
			if contains(ah, h) {
				return true
			}
		}
	}
	return false
}

// samePathIdentity holds a same-path fold of two books that are not the same
// work: normalized titles (a leading track number dropped) that differ,
// author sets that differ, ASINs that differ, or external ids of one source
// on both books with no value in common. A side with no value is no
// evidence. Both identities are shown on the row and fingerprinted.
func (s *leftoverSource) samePathIdentity(core, ob *database.BookCore, row *repairs.Row, fp *strings.Builder) (bool, string, error) {
	type ident struct {
		title   string
		authors []string
		asin    string
		ids     map[string][]string
		text    string
	}
	read := func(b *database.BookCore) (ident, error) {
		full := b.ToBook()
		names, err := repairs.BookAuthorNames(s.store, &full)
		if err != nil {
			return ident{}, fmt.Errorf("authors of %s: %w", b.ID, err)
		}
		exts, err := s.store.GetExternalIDsForBook(b.ID)
		if err != nil {
			return ident{}, fmt.Errorf("external ids of %s: %w", b.ID, err)
		}
		in := ident{title: dcTitleKey(b.Title), asin: strings.ToUpper(strings.TrimSpace(dcStr(b.ASIN))), ids: map[string][]string{}}
		names = append([]string(nil), names...)
		sort.Strings(names) // the row text is fingerprinted: a stable order
		for _, n := range names {
			if k := fbNorm(n); k != "" && !contains(in.authors, k) {
				in.authors = append(in.authors, k)
			}
		}
		sort.Strings(in.authors)
		var idText []string
		for _, e := range exts {
			if e.Tombstoned || e.ExternalID == "" {
				continue
			}
			in.ids[e.Source] = append(in.ids[e.Source], e.ExternalID)
			idText = append(idText, e.Source+"/"+e.ExternalID)
		}
		sort.Strings(idText)
		in.text = fmt.Sprintf("title %q; authors %s; ASIN %s; ids %s", b.Title, orNone(strings.Join(names, ", ")),
			orNone(in.asin), orNone(strings.Join(idText, ", ")))
		return in, nil
	}
	l, err := read(core)
	if err != nil {
		return false, "", err
	}
	o, err := read(ob)
	if err != nil {
		return false, "", err
	}
	row.Current["leftover_identity"] = l.text
	row.Current["owner_identity"] = o.text
	fmt.Fprintf(fp, "identity|%s|%s\n", l.text, o.text)
	switch {
	case l.title != "" && o.title != "" && l.title != o.title:
		return true, fmt.Sprintf("the titles differ: %q against the owner's %q", core.Title, ob.Title), nil
	case len(l.authors) > 0 && len(o.authors) > 0 && strings.Join(l.authors, "|") != strings.Join(o.authors, "|"):
		return true, fmt.Sprintf("the authors differ: %s against the owner's %s", strings.Join(l.authors, ", "), strings.Join(o.authors, ", ")), nil
	case l.asin != "" && o.asin != "" && l.asin != o.asin:
		return true, fmt.Sprintf("the ASINs differ: %s against the owner's %s", l.asin, o.asin), nil
	}
	srcs := make([]string, 0, len(l.ids))
	for src := range l.ids {
		srcs = append(srcs, src)
	}
	sort.Strings(srcs)
	for _, src := range srcs {
		ov, ok := o.ids[src]
		if !ok {
			continue
		}
		shared := false
		for _, v := range l.ids[src] {
			if contains(ov, v) {
				shared = true
				break
			}
		}
		if !shared {
			return true, fmt.Sprintf("both books carry %s ids and none agrees: %s against the owner's %s", src,
				strings.Join(l.ids[src], ", "), strings.Join(ov, ", ")), nil
		}
	}
	return false, "", nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// leftoverHandOffHold says why the retire's primary hand-off in the
// leftover's version group (members) would not leave the owner its sole
// live primary ("" when it would, or when nobody else is left in it).
// sameGroup: the owner is a member. Re-run under the merge lock by the
// apply's re-plan.
func leftoverHandOffHold(store OpsStore, members []database.Book, leftover, owner string, ob *database.BookCore, sameGroup bool) string {
	var others []string
	for i := range members {
		if members[i].ID != leftover && !members[i].IsSoftDeleted() {
			others = append(others, members[i].ID)
		}
	}
	sort.Strings(others)
	if len(others) == 0 {
		return "" // a lone leftover: nothing is handed off
	}
	gid := dcStr(ob.VersionGroupID)
	if !sameGroup {
		return fmt.Sprintf("the leftover's version group keeps live member(s) %s and the owner %s is not in it (its group: %q): "+
			"the hand-off would crown one of them, not the owner", strings.Join(others, ", "), owner, gid)
	}
	if len(others) == 1 && others[0] == owner {
		return "" // the owner is all that is left: the hand-off crowns it
	}
	live, explicit := livePrimaries(store, members, leftover)
	if live == 1 && explicit == 1 && ob.IsPrimaryVersion != nil && *ob.IsPrimaryVersion {
		return "" // the owner is already the group's one explicit primary
	}
	var prim []string
	for i := range members {
		m := &members[i]
		if m.ID != leftover && !m.IsSoftDeleted() && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			prim = append(prim, m.ID)
		}
	}
	sort.Strings(prim)
	return fmt.Sprintf("after the retire the owner %s would not be its version group's sole live primary "+
		"(explicit primaries besides the leftover: %s; live members: %s)", owner, orNone(strings.Join(prim, ", ")), strings.Join(others, ", "))
}
