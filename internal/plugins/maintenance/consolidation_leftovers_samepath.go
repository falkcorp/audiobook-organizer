// file: internal/plugins/maintenance/consolidation_leftovers_samepath.go
// version: 1.2.0
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
type leftoverITunesOf func(bc *database.BookCore, rs []database.BookFileCore) (string, bool, error)

// samePath decides a leftover whose own file_path is on disk. core.FilePath
// is that path; rows are the leftover's book_file rows, every one gone from
// disk (decide checked). The row is applicable only when exactly ONE other
// live book owns the path, that book is listed by Audiobookshelf, owns the
// path through a book_file row of its own, is not iTunes-owned, no iTunes
// copy in its or the leftover's version group would be written by the
// primary hand-off, and the leftover's recorded duration does not contradict
// the owner's file. Owner-manual (Doctor Who / Big Finish / Torchwood) books
// are held by the framework guard, which reads every id in row.BookIDs, the
// owner's included, at plan and again at apply. No owner at all is the old
// held_book_path_present.
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
	// iTunes on the leftover's side, as for every other class.
	why, doubt, err := itunesOf(core, rows)
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
	sort.Slice(orows, func(i, j int) bool { return orows[i].ID < orows[j].ID })
	var at *database.BookFileCore
	for i := range orows {
		if orows[i].FilePath == shared && !orows[i].Missing {
			at = &orows[i]
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
	why, doubt, err = itunesOf(ob, orows)
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
	fmt.Fprintf(fp, "owner|%s|%s|%d|%s|%s|%d|%d\n", at.ID, at.FilePath, at.FileSize, at.FileHash, at.OriginalFileHash, at.Duration, ownerDur)
	// The leftover's own audio, as far as it is recorded: a known duration
	// (the sum of its rows' when every row has one, else the book's) must
	// agree with the owner's file at the path (else the owner book's, when
	// that file is its only one) within 2 s. File against file wherever both
	// are known. A zero or unknown duration is
	// no evidence either way (the 2026-10-06 leftovers are 0 min). The dead
	// rows' hashes are NOT compared: they describe files gone from disk, and
	// a hash equal to a third book's _copyN file is exactly the match the
	// owner rejected.
	leftDur := 0
	for _, r := range rows {
		if r.Duration <= 0 {
			leftDur = 0
			break
		}
		leftDur += r.Duration
	}
	if leftDur == 0 && core.Duration != nil && *core.Duration > 0 {
		leftDur = *core.Duration
	}
	cmpDur := at.Duration
	if cmpDur <= 0 && len(orows) == 1 && ob.Duration != nil {
		cmpDur = *ob.Duration
	}
	fmt.Fprintf(fp, "dur|%d|%d\n", leftDur, cmpDur)
	if leftDur > 0 && cmpDur > 0 && !leftoverSameDuration(leftDur, cmpDur) {
		return finish(leftoverClassHeld, leftoverSkipSamePathAudio, fmt.Sprintf(
			"the leftover records %s of audio and the owner's file at %s %s: different audio", leftoverDurText(leftDur), shared, leftoverDurText(cmpDur)))
	}
	// Version groups: the retire's primary hand-off writes the leftover's
	// group, and the owner's group is where the folded state lands; an
	// iTunes copy in either that is not explicitly non-primary holds it.
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
		for i := range group {
			if group[i].ID != id && !contains(row.BookIDs, group[i].ID) {
				row.BookIDs = appendUnique(row.BookIDs, group[i].ID)
				row.Members = append(row.Members, repairs.RowMember{BookID: group[i].ID, Title: group[i].Title, Role: leftoverRolesGroupSibling})
			}
		}
	}
	// The listening state: the whole-book rule when the owner is that one
	// file (the farther-ahead state wins, finished carries); a slice at the
	// file's place when the owner has more files.
	plan := &leftoverPlan{Leftover: id, Combined: oid, GroupID: gid, OwnerGroupID: ogid}
	var carry string
	if len(orows) == 1 {
		plan.WholeBook = true
		carry = "listening state and positions by the whole-book rule (the farther-ahead state wins)"
	} else {
		plan.Slice = leftoverSlice(orows, *at)
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
