// file: internal/plugins/maintenance/consolidation_leftovers_fixer.go
// version: 1.2.1
// guid: 6df37df9-b008-41ad-bd69-47b00e4cb50c
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// leftoverFixerID is the Repairs-lane id of the consolidation-leftovers fixer.
const leftoverFixerID = "consolidation-leftovers"

// leftoverMaxFiles is the most book_file rows a leftover may have. The
// 2026-09-06 consolidation left one-chapter books behind; three allows a
// chapter split over a few files and keeps whole books out of the plan.
const leftoverMaxFiles = 3

// leftoverStatWorkers bounds the concurrent disk stats. The library sits on
// a NAS dataset that serves playback and scans at the same time, so this is a
// small fixed pool rather than runtime.NumCPU().
const leftoverStatWorkers = 8

// leftoverIndexTTL bounds how long Replan reuses the size index built by the
// plan (or an earlier replan). Every candidate the index names is re-read
// fresh and stat'ed again under the merge lock; only a NEW same-size row
// created inside this window is missed, which is the combined-author fixer's
// window too.
const leftoverIndexTTL = 2 * time.Minute

// Classes of a leftovers row.
const (
	leftoverClassRetire  = "retire"
	leftoverClassHeld    = "held"
	leftoverClassITunes  = "itunes"
	leftoverClassNoMatch = "no-match"
)

// Skip kinds of a leftovers row (the held reasons, iTunes, no match).
const (
	leftoverSkipAmbiguous     = "held_ambiguous"
	leftoverSkipHashDisagree  = "held_hash_disagree"
	leftoverSkipBookPath      = "held_book_path_present"
	leftoverSkipStatError     = "held_stat_error"
	leftoverSkipScope         = "held_no_scope"
	leftoverSkipSplit         = "held_split_owners"
	leftoverSkipPartial       = "held_partial_match"
	leftoverSkipITunesDoubt   = "held_itunes_doubt"
	leftoverSkipITunes        = "itunes"
	leftoverSkipNoMatch       = "no-match"
	leftoverBasisSizeHash     = "size+hash"
	leftoverBasisSizeChapter  = "size+chapter"
	leftoverBasisSizeDuration = "size+duration"
	leftoverSkipNoEvidence    = "held_no_twin_evidence"
	leftoverSkipTwinAmbiguous = "held_twin_ambiguous"
	leftoverSkipNotListed     = "held_combined_not_listed"
	leftoverRolesLeftover     = "leftover"
	leftoverRolesCombined     = "combined"
	leftoverRolesGroupSibling = "version_group"
)

// consolidationLeftoversFixer retires the one-chapter books the 2026-09-06
// chapter consolidation left live. That run moved each chapter's audio into a
// combined book but did not touch the old book: its book_file row still names
// the old path, which no longer exists, and was never marked Missing (so the
// files API's file_exists, which is !Missing, reports it present). A file of
// exactly the same size now belongs to the combined book in the same series
// folder.
//
// A row is a live book with 1..leftoverMaxFiles book_file rows, at least one
// not marked Missing, where:
//   - every row's path is gone from disk (os.Stat says fs.ErrNotExist; any
//     other stat error holds the row: it cannot be told), and so is the
//     book's own file_path (a present one makes it a repoint candidate);
//   - for each row, exactly one OTHER live book owns a row with the same byte
//     size whose file is on disk at that size, under the dead row's
//     grandparent folder (the series or author folder; refused when that
//     folder is, or contains, the library root or an import path);
//   - all rows name the same owner (the combined book);
//   - where both rows carry hashes, they agree (disagreement holds the row).
//
// Several owners hold it (ambiguous); no owner is the never-applicable
// no_match class; an iTunes book on either side is the never-applicable
// itunes class. Doctor Who / Big Finish / Torchwood are held by the
// framework guard.
//
// Apply, under merge.LockMergeRMW and after a fresh re-plan whose fingerprint
// must match: each dead row not yet Missing is marked Missing in place
// (Writer.RepointBookFile to its own path with Missing=true; journaled, never
// deleted), then the leftover is retired into the combined book with
// retireInto, the retire the fragment and duplicate-copies fixers share:
// every user's listening state follows by the whole-book rule, external ids
// move, a primary is demoted with its version group handed off, and the book
// is soft-deleted with merged_into_book_id naming the combined book. Every
// step is journaled and reverts with the apply operation.
type consolidationLeftoversFixer struct {
	p      *Plugin
	statFn func(string) (os.FileInfo, error)
	now    func() time.Time

	idxMu      sync.Mutex
	idx        map[int64][]database.BookFileCore
	idxBuiltAt time.Time
}

func newConsolidationLeftoversFixer(p *Plugin) *consolidationLeftoversFixer {
	return &consolidationLeftoversFixer{p: p, statFn: os.Stat, now: time.Now}
}

var _ repairs.Fixer = (*consolidationLeftoversFixer)(nil)

func (f *consolidationLeftoversFixer) ID() string    { return leftoverFixerID }
func (f *consolidationLeftoversFixer) Title() string { return "Chapter consolidation leftovers" }
func (f *consolidationLeftoversFixer) Description() string {
	return "Live one-chapter books left behind by the 2026-09-06 chapter consolidation: their file rows name paths " +
		"that are gone from disk (but were never marked missing), and a file of exactly that size now belongs to one " +
		"combined book in the same series folder. Apply marks each dead row missing (it is kept, never deleted) and " +
		"retires the leftover into the combined book with its listening progress and external ids. Ambiguous matches " +
		"and hash disagreements are held; iTunes books and leftovers with no match are listed only. Every step is " +
		"undoable from the apply operation."
}

// leftoverState is what Replan needs from plan time: the combined book, the
// dead rows that were not Missing (a cut-off apply may have marked them since)
// and the leftover's file_path (the retire clears it).
type leftoverState struct {
	Combined   string   `json:"combined,omitempty"`
	NotMissing []string `json:"not_missing,omitempty"`
	BookPath   string   `json:"book_path,omitempty"`
}

// leftoverMark is one dead row Apply marks Missing.
type leftoverMark struct {
	RowID string
	Was   undo.BookFileLocation
}

// leftoverPlan is the Detail Replan hands Apply.
type leftoverPlan struct {
	Leftover string
	Combined string
	GroupID  string
	Marks    []leftoverMark
	Slice    merge.SliceMapping
}

// leftoverStat is one disk answer: gone (fs.ErrNotExist), present (with its
// size and whether it is a regular file), or err (anything else).
type leftoverStat struct {
	gone    bool
	size    int64
	regular bool
	err     error
}

// leftoverSource is the read side one decision runs over: the plan's
// snapshot, or Replan's fresh reads.
type leftoverSource struct {
	store  OpsStore
	book   func(id string) (*database.BookCore, error)
	rows   func(id string) ([]database.BookFileCore, error)
	same   func(size int64, scope string) ([]database.BookFileCore, error)
	shelf  []string
	statMu sync.Mutex
	stats  map[string]leftoverStat
	statFn func(string) (os.FileInfo, error)
}

func (s *leftoverSource) stat(p string) leftoverStat {
	s.statMu.Lock()
	if st, ok := s.stats[p]; ok {
		s.statMu.Unlock()
		return st
	}
	s.statMu.Unlock()
	fi, err := s.statFn(p)
	var st leftoverStat
	switch {
	case err == nil:
		st = leftoverStat{size: fi.Size(), regular: fi.Mode().IsRegular()}
	case errors.Is(err, fs.ErrNotExist):
		st = leftoverStat{gone: true}
	default:
		st = leftoverStat{err: err}
	}
	s.statMu.Lock()
	s.stats[p] = st
	s.statMu.Unlock()
	return st
}

func (f *consolidationLeftoversFixer) newSource(store OpsStore) (*leftoverSource, error) {
	shelves, err := leftoverShelves(store, f.p.deps.RootDir())
	if err != nil {
		return nil, err
	}
	return &leftoverSource{store: store, shelf: shelves, stats: map[string]leftoverStat{}, statFn: f.statFn}, nil
}

// leftoverShelves are the library root (the plugin's deps.RootDir) and every
// import path, cleaned. A copy of folderBooksFixer.common's shelf list,
// without its author cache.
func leftoverShelves(store OpsStore, root string) ([]string, error) {
	var out []string
	if root != "" {
		out = append(out, filepath.Clean(root))
	}
	ips, err := store.GetAllImportPaths()
	if err != nil {
		return nil, fmt.Errorf("list import paths: %w", err)
	}
	for _, ip := range ips {
		if ip.Path != "" {
			out = append(out, filepath.Clean(ip.Path))
		}
	}
	return out, nil
}

// leftoverScope is the folder a dead row's twin must sit under: the row's
// grandparent (the series or author folder above the chapter's own folder).
// ok is false when that folder is a shelf, contains one, or is the
// filesystem root: the search would then span the whole library.
func leftoverScope(p string, shelves []string) (string, bool) {
	scope := filepath.Dir(filepath.Dir(filepath.Clean(p)))
	if scope == "/" || scope == "." || scope == "" {
		return scope, false
	}
	for _, s := range shelves {
		if s == scope || strings.HasPrefix(s, scope+string(filepath.Separator)) {
			return scope, false
		}
	}
	return scope, true
}

// leftoverHashes are a row's hashes (current and original), non-empty.
func leftoverHashes(r database.BookFileCore) []string {
	var out []string
	for _, h := range []string{r.FileHash, r.OriginalFileHash} {
		if h != "" && !contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// leftoverHashVerdict compares two rows' hashes: "agree", "disagree", or ""
// when either side has none.
func leftoverHashVerdict(a, b database.BookFileCore) string {
	ha, hb := leftoverHashes(a), leftoverHashes(b)
	if len(ha) == 0 || len(hb) == 0 {
		return ""
	}
	for _, h := range ha {
		if contains(hb, h) {
			return "agree"
		}
	}
	return "disagree"
}

// leftoverMatch is the decision for one dead row.
type leftoverMatch struct {
	row    database.BookFileCore
	owners []string               // live owning books of a present same-size file
	twin   *database.BookFileCore // the matched row on the one owner
	basis  string                 // size+hash, size+chapter or size+duration
	skip   string                 // a held kind for this row
	why    string                 // its reason
}

// matchRow finds the owners of a present same-size file under the dead row's
// scope folder.
func (s *leftoverSource) matchRow(ctx context.Context, leftover string, r database.BookFileCore) (leftoverMatch, error) {
	m := leftoverMatch{row: r}
	if r.FileSize <= 0 {
		m.why = "the row has no recorded size"
		return m, nil
	}
	scope, ok := leftoverScope(r.FilePath, s.shelf)
	if !ok {
		m.skip, m.why = leftoverSkipScope, fmt.Sprintf("the row's series folder %q is a library root or contains one", scope)
		return m, nil
	}
	cands, err := s.same(r.FileSize, scope)
	if err != nil {
		return m, err
	}
	prefix := scope + string(filepath.Separator)
	byOwner := map[string][]database.BookFileCore{}
	for _, c := range cands {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		if c.BookID == leftover || !strings.HasPrefix(c.FilePath, prefix) {
			continue
		}
		ob, err := s.book(c.BookID)
		if err != nil {
			return m, err
		}
		if ob == nil || ob.IsSoftDeleted() {
			continue
		}
		st := s.stat(c.FilePath)
		switch {
		case st.err != nil:
			m.skip, m.why = leftoverSkipStatError, fmt.Sprintf("could not stat candidate %s: %v", c.FilePath, st.err)
			return m, nil
		case st.gone || !st.regular || st.size != r.FileSize:
			continue
		}
		byOwner[c.BookID] = append(byOwner[c.BookID], c)
	}
	for id := range byOwner {
		m.owners = append(m.owners, id)
	}
	sort.Strings(m.owners)
	switch len(m.owners) {
	case 0:
		m.why = fmt.Sprintf("no live book owns a present %d-byte file under %s", r.FileSize, scope)
		return m, nil
	case 1:
	default:
		m.skip, m.why = leftoverSkipAmbiguous, fmt.Sprintf("%d live books own a present %d-byte file under %s: %s",
			len(m.owners), r.FileSize, scope, strings.Join(m.owners, ", "))
		return m, nil
	}
	// The twin decides where the leftover's listening position lands in the
	// combined book, so a same-size file alone is not enough: a candidate
	// needs agreeing evidence (equal hash, the same chapter number in both
	// file names, or the same duration), and exactly one candidate must have
	// it. Candidates whose hash disagrees are dropped; when every candidate
	// disagrees the row is held.
	rows := byOwner[m.owners[0]]
	sort.Slice(rows, func(i, j int) bool { return rows[i].FilePath < rows[j].FilePath })
	type ev struct {
		row           *database.BookFileCore
		hash, ch, dur bool
	}
	var evs []ev
	disagree := 0
	for i := range rows {
		v := leftoverHashVerdict(r, rows[i])
		if v == "disagree" {
			disagree++
			continue
		}
		e := ev{row: &rows[i], hash: v == "agree", ch: leftoverSameChapter(r.FilePath, rows[i].FilePath),
			dur: leftoverSameDuration(r.Duration, rows[i].Duration)}
		if e.hash || e.ch || e.dur {
			evs = append(evs, e)
		}
	}
	if len(evs) == 0 {
		if disagree > 0 {
			m.skip, m.why = leftoverSkipHashDisagree, fmt.Sprintf("the same-size file(s) of %s carry a different hash", m.owners[0])
		} else {
			m.skip, m.why = leftoverSkipNoEvidence, fmt.Sprintf("%d same-size file(s) of %s, but neither a hash, the chapter number nor the duration agrees",
				len(rows), m.owners[0])
		}
		return m, nil
	}
	for _, keep := range []func(ev) bool{func(e ev) bool { return e.hash }, func(e ev) bool { return e.ch }, func(e ev) bool { return e.dur }} {
		if len(evs) == 1 {
			break
		}
		var narrowed []ev
		for _, e := range evs {
			if keep(e) {
				narrowed = append(narrowed, e)
			}
		}
		if len(narrowed) > 0 {
			evs = narrowed
		}
	}
	if len(evs) > 1 {
		m.skip, m.why = leftoverSkipTwinAmbiguous, fmt.Sprintf("%d files of %s match %s equally well: %s, %s",
			len(evs), m.owners[0], r.FilePath, evs[0].row.FilePath, evs[1].row.FilePath)
		return m, nil
	}
	e := evs[0]
	m.twin = e.row
	switch {
	case e.hash:
		m.basis = leftoverBasisSizeHash
	case e.ch:
		m.basis = leftoverBasisSizeChapter
	default:
		m.basis = leftoverBasisSizeDuration
	}
	return m, nil
}

var (
	leftoverCopySuffix = regexp.MustCompile(`(?i)_copy\d+$`)
	leftoverLeadNum    = regexp.MustCompile(`^0*(\d{1,4})\s*[-–._)\s]`)
	leftoverTrailNum   = regexp.MustCompile(`[-–._(\s]\s*0*(\d{1,4})$`)
)

// leftoverChapter is the chapter number in a file name: the leading number
// ("18 - We Hunt Monsters 8") or, with none, the trailing one ("We Hunt
// Monsters 8 - 18"), after a "_copyN" suffix is dropped. lead prefers the
// leading number; otherwise the trailing one is preferred.
func leftoverChapter(p string, lead bool) (int, bool) {
	stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	stem = strings.TrimSpace(leftoverCopySuffix.ReplaceAllString(stem, ""))
	l, t := leftoverLeadNum.FindStringSubmatch(stem), leftoverTrailNum.FindStringSubmatch(stem)
	pick := func(m []string) (int, bool) {
		n, err := strconv.Atoi(m[1])
		return n, err == nil
	}
	if lead && l != nil {
		return pick(l)
	}
	if t != nil {
		return pick(t)
	}
	if l != nil {
		return pick(l)
	}
	return 0, false
}

// leftoverSameChapter reports whether the leftover's file and the twin carry
// the same chapter number (the leftover's leading one, the twin's trailing
// one: the 09-06 consolidation renamed "NN - Title" to "Title - NN").
func leftoverSameChapter(leftover, twin string) bool {
	a, ok := leftoverChapter(leftover, true)
	if !ok {
		return false
	}
	b, ok := leftoverChapter(twin, false)
	return ok && a == b
}

// leftoverSameDuration reports whether two known durations agree within 2 s.
func leftoverSameDuration(a, b int) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	d := a - b
	return d >= -2 && d <= 2
}

// leftoverSlice maps the twin into the combined book's timeline: the offset
// is the summed duration of every row with a lower track number. Mirrors the
// fragment fixer's sliceIn over this fixer's row type. Any unknown or
// repeated track, or an unknown earlier duration, makes it not Mappable:
// then no position is carried (and a slice never carries Finished).
func leftoverSlice(rows []database.BookFileCore, twin database.BookFileCore) merge.SliceMapping {
	if twin.TrackNumber <= 0 {
		return merge.SliceMapping{}
	}
	seen := map[int]bool{}
	var off float64
	for _, r := range rows {
		if r.TrackNumber <= 0 || seen[r.TrackNumber] {
			return merge.SliceMapping{}
		}
		seen[r.TrackNumber] = true
		if r.TrackNumber < twin.TrackNumber {
			if r.Duration <= 0 {
				return merge.SliceMapping{}
			}
			off += float64(r.Duration)
		}
	}
	return merge.SliceMapping{OffsetSeconds: off, Mappable: true}
}

// decide makes one row for book id, or returns ok=false when the book is not
// a leftover at all (a present file, too many rows, nothing unmarked). st is
// the planned state on a re-plan (nil in a plan).
func (s *leftoverSource) decide(ctx context.Context, id string, st *leftoverState) (repairs.Row, bool, error) {
	b, err := s.book(id)
	if err != nil {
		return repairs.Row{}, false, err
	}
	if b == nil {
		return repairs.Row{}, false, nil
	}
	core := *b
	if core.IsSoftDeleted() {
		// A book this row already retired reads as it was planned: the
		// decision is re-made and Apply skips the finished steps.
		if st == nil || core.MergedIntoBookID == nil || *core.MergedIntoBookID != st.Combined {
			return repairs.Row{}, false, nil
		}
		core.MarkedForDeletion, core.MarkedForDeletionAt, core.MergedIntoBookID = nil, nil, nil
		if core.FilePath == "" {
			core.FilePath = st.BookPath
		}
	}
	rows, err := s.rows(id)
	if err != nil {
		return repairs.Row{}, false, err
	}
	if len(rows) == 0 || len(rows) > leftoverMaxFiles {
		return repairs.Row{}, false, nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	notMissing := 0
	for i := range rows {
		if st != nil && contains(st.NotMissing, rows[i].ID) {
			rows[i].Missing = false // marked by a cut-off run of this row
		}
		if !rows[i].Missing {
			notMissing++
		}
	}
	if notMissing == 0 {
		return repairs.Row{}, false, nil
	}
	// The disk decides: a row whose file is present makes this no leftover.
	var statErr string
	for _, r := range rows {
		ds := s.stat(r.FilePath)
		switch {
		case ds.err != nil:
			statErr = fmt.Sprintf("could not stat %s: %v", r.FilePath, ds.err)
		case !ds.gone:
			return repairs.Row{}, false, nil
		}
	}
	row := repairs.Row{RowID: "leftover:" + id, BookIDs: []string{id}, Title: core.Title, Risk: repairs.RiskReview,
		Current: map[string]string{}}
	var paths []string
	var fp strings.Builder
	fp.WriteString(id + "\n" + core.Title + "\n" + core.FilePath + "\n")
	for _, r := range rows {
		paths = append(paths, r.FilePath)
		fmt.Fprintf(&fp, "%s|%s|%d|%s|%s\n", r.ID, r.FilePath, r.FileSize, r.FileHash, r.OriginalFileHash)
	}
	row.Current["missing_row_paths"] = strings.Join(paths, "\n")
	row.Members = []repairs.RowMember{{BookID: id, Title: core.Title, Role: leftoverRolesLeftover, Files: len(rows),
		MissingFiles: len(rows) - notMissing}}
	finish := func(class, skip, why string) (repairs.Row, bool, error) {
		row.Class, row.Skipped = class, skip
		if skip != "" {
			row.SkipReason = why
		}
		row.Reason = why
		sort.Strings(row.BookIDs)
		fmt.Fprintf(&fp, "%s|%s|%s|%s\n", class, skip, why, strings.Join(row.BookIDs, ","))
		sum := sha256.Sum256([]byte(fp.String()))
		row.Fingerprint = hex.EncodeToString(sum[:])[:32]
		return row, true, nil
	}
	if statErr != "" {
		return finish(leftoverClassHeld, leftoverSkipStatError, statErr)
	}
	if core.FilePath != "" {
		ds := s.stat(core.FilePath)
		switch {
		case ds.err != nil:
			return finish(leftoverClassHeld, leftoverSkipStatError, fmt.Sprintf("could not stat the book path %s: %v", core.FilePath, ds.err))
		case !ds.gone:
			return finish(leftoverClassHeld, leftoverSkipBookPath, fmt.Sprintf("the book's own path %s is on disk: a repoint candidate, not a leftover", core.FilePath))
		}
	}
	// iTunes on the leftover's side.
	res := repairs.NewPathResolver()
	itunesOf := func(bc *database.BookCore, rs []database.BookFileCore) (string, bool, error) {
		exts, err := s.store.GetExternalIDsForBook(bc.ID)
		if err != nil {
			return "", false, fmt.Errorf("external ids of %s: %w", bc.ID, err)
		}
		why, doubt := leftoverITunesWhy(res, bc, rs, exts)
		return why, doubt, nil
	}
	why, doubt, err := itunesOf(&core, rows)
	if err != nil {
		return repairs.Row{}, false, err
	}
	switch {
	case doubt:
		return finish(leftoverClassHeld, leftoverSkipITunesDoubt, "could not tell whether the leftover is an iTunes book")
	case why != "":
		return finish(leftoverClassITunes, leftoverSkipITunes, "the leftover is an iTunes book ("+why+"); never written")
	}
	// Match every dead row.
	matches := make([]leftoverMatch, 0, len(rows))
	for _, r := range rows {
		m, err := s.matchRow(ctx, id, r)
		if err != nil {
			return repairs.Row{}, false, err
		}
		matches = append(matches, m)
	}
	owners := map[string]bool{}
	var noMatch []string
	for _, m := range matches {
		if m.skip != "" {
			for _, o := range m.owners {
				row.BookIDs = appendUnique(row.BookIDs, o)
			}
			return finish(leftoverClassHeld, m.skip, m.why)
		}
		if m.twin == nil {
			noMatch = append(noMatch, m.why)
			continue
		}
		owners[m.twin.BookID] = true
	}
	if len(owners) == 0 {
		return finish(leftoverClassNoMatch, leftoverSkipNoMatch, strings.Join(noMatch, "; "))
	}
	if len(noMatch) > 0 {
		return finish(leftoverClassHeld, leftoverSkipPartial, "some rows match and some do not: "+strings.Join(noMatch, "; "))
	}
	if len(owners) > 1 {
		var ids []string
		for o := range owners {
			ids = append(ids, o)
		}
		sort.Strings(ids)
		for _, o := range ids {
			row.BookIDs = appendUnique(row.BookIDs, o)
		}
		return finish(leftoverClassHeld, leftoverSkipSplit, "the rows match files of different books: "+strings.Join(ids, ", "))
	}
	combinedID := matches[0].twin.BookID
	row.BookIDs = appendUnique(row.BookIDs, combinedID)
	cb, err := s.book(combinedID)
	if err != nil {
		return repairs.Row{}, false, err
	}
	if cb == nil || cb.IsSoftDeleted() {
		return finish(leftoverClassNoMatch, leftoverSkipNoMatch, "the owning book is gone")
	}
	crows, err := s.rows(combinedID)
	if err != nil {
		return repairs.Row{}, false, err
	}
	if !database.ABSLibraryFilter().MatchesCore(cb) {
		return finish(leftoverClassHeld, leftoverSkipNotListed, fmt.Sprintf("the combined book %s is not listed by Audiobookshelf (not an organized, unquarantined primary): its listening state would be stranded", combinedID))
	}
	row.Members = append(row.Members, repairs.RowMember{BookID: combinedID, Title: cb.Title, Role: leftoverRolesCombined,
		Files: len(crows), MissingFiles: countMissing(crows)})
	row.Current["combined_book"] = combinedID + " " + cb.Title
	basis := leftoverBasisSizeHash
	var matched []string
	for _, m := range matches {
		matched = append(matched, m.twin.FilePath)
		if leftoverBasisRank(m.basis) > leftoverBasisRank(basis) {
			basis = m.basis
		}
		row.Evidence = append(row.Evidence, fmt.Sprintf("%s is gone from disk; %s (%d bytes, %s) is on disk and owned only by %s",
			m.row.FilePath, m.twin.FilePath, m.row.FileSize, m.basis, combinedID))
		fmt.Fprintf(&fp, "twin|%s|%s|%s|%d|%s\n", m.row.ID, m.twin.ID, m.twin.FilePath, m.row.FileSize, m.basis)
	}
	row.Current["matched_files"] = strings.Join(matched, "\n")
	row.Current["match_basis"] = basis
	if basis == leftoverBasisSizeHash || basis == leftoverBasisSizeChapter {
		row.Risk = repairs.RiskLow
	}
	// The combined book's iTunes side: a retire into it writes it.
	why, doubt, err = itunesOf(cb, crows)
	if err != nil {
		return repairs.Row{}, false, err
	}
	switch {
	case doubt:
		return finish(leftoverClassHeld, leftoverSkipITunesDoubt, "could not tell whether the combined book is an iTunes book")
	case why != "":
		return finish(leftoverClassITunes, leftoverSkipITunes, "the combined book is an iTunes book ("+why+"); never written")
	}
	// Every member of the leftover's version group: the retire's primary
	// hand-off may write any of them, so the framework guards them all.
	gid := ""
	if core.VersionGroupID != nil && *core.VersionGroupID != "" {
		gid = *core.VersionGroupID
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
	// The listening position follows as a slice of the combined book, at the
	// place of the twin of the leftover's first track.
	first := matches[0]
	for _, m := range matches[1:] {
		if m.row.TrackNumber < first.row.TrackNumber {
			first = m
		}
	}
	slice := leftoverSlice(crows, *first.twin)
	if len(matches) > 1 && !leftoverTwinsConsecutive(matches) {
		// A position inside a later row of the leftover maps past the
		// first twin only when the twins follow one another in the
		// combined book in the leftover's own order.
		slice = merge.SliceMapping{}
	}
	fmt.Fprintf(&fp, "slice|%v|%.3f\n", slice.Mappable, slice.OffsetSeconds)
	if slice.Mappable {
		row.Current["position_offset_seconds"] = strconv.FormatFloat(slice.OffsetSeconds, 'f', 0, 64)
	} else {
		row.Current["position_offset_seconds"] = "unknown: no position is carried"
	}
	plan := &leftoverPlan{Leftover: id, Combined: combinedID, GroupID: gid, Slice: slice}
	state := leftoverState{Combined: combinedID, BookPath: core.FilePath}
	for _, r := range rows {
		if r.Missing {
			continue
		}
		state.NotMissing = append(state.NotMissing, r.ID)
		plan.Marks = append(plan.Marks, leftoverMark{RowID: r.ID,
			Was: undo.BookFileLocation{Path: r.FilePath, Missing: false, Hash: r.FileHash, Size: r.FileSize}})
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return repairs.Row{}, false, err
	}
	row.State = raw
	row.Detail = plan
	row.Proposed = map[string]string{"action": fmt.Sprintf("mark %d dead row(s) missing (kept, not deleted); retire the book into %s with its listening state and external ids",
		len(plan.Marks), combinedID)}
	return finish(leftoverClassRetire, "", fmt.Sprintf("a 2026-09-06 consolidation leftover: its file is gone from disk and the same audio (%s match) now belongs to %s",
		basis, combinedID))
}

// leftoverWalkMax bounds the files one scope walk visits; a bigger folder
// fails the re-plan rather than walk a whole author's shelf.
const leftoverWalkMax = 20000

var errLeftoverWalkTooBig = errors.New("scope folder has too many files to walk")

// leftoverWalkOwners walks scope on disk and returns the rows (looked up by
// path) of every regular file of exactly size bytes not already in seen.
func leftoverWalkOwners(store OpsStore, scope string, size int64, seen map[string]bool) ([]database.BookFileCore, error) {
	var out []database.BookFileCore
	n := 0
	err := filepath.WalkDir(scope, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if n++; n > leftoverWalkMax {
			return errLeftoverWalkTooBig
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() != size {
			return nil
		}
		bf, err := store.GetBookFileByPath(p)
		if err != nil {
			return fmt.Errorf("read the row at %s: %w", p, err)
		}
		if bf != nil && !seen[bf.ID] {
			seen[bf.ID] = true
			out = append(out, bf.Core())
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", scope, err)
	}
	return out, nil
}

// leftoverITunesWhy names why a book is iTunes-owned for this fixer ("" when
// it is not): a book or row iTunes persistent id, an un-tombstoned itunes
// external id (the persistent-id mapping), or a file inside the iTunes media
// folder (books/itunes/** with symlinks resolved, or any "iTunes Media"
// folder). doubt: a path could not be settled.
//
// Unlike itunesCopyWhy it does NOT count a row's bare iTunes path reference
// (owner decision 2026-10-06): the 09-06 leftovers carry the iTunes
// library's reference to the organizer folder in itunes_path, and that alone
// does not make the book iTunes-owned. itunesCopyWhy is shared with the
// duplicate-copies and fragment fixers, so this fixer keeps its own check
// rather than change theirs. retireInto still refuses a book or row with a
// persistent id and an un-tombstoned itunes external id.
func leftoverITunesWhy(res *repairs.PathResolver, b *database.BookCore, rows []database.BookFileCore, exts []database.ExternalIDMapping) (why string, doubt bool) {
	if pid := dcStr(b.ITunesPersistentID); pid != "" {
		return "book iTunes id " + pid, false
	}
	for _, r := range rows {
		if r.ITunesPersistentID != "" {
			return "row iTunes id " + r.ITunesPersistentID, false
		}
	}
	for _, e := range exts {
		if e.Source == "itunes" && e.ExternalID != "" && !e.Tombstoned {
			return "itunes external id " + e.ExternalID, false
		}
	}
	paths := []string{b.FilePath}
	for _, r := range rows {
		paths = append(paths, r.FilePath)
	}
	for _, p := range paths {
		if strings.Contains(p, string(filepath.Separator)+"iTunes Media"+string(filepath.Separator)) {
			return "file inside an iTunes Media folder: " + p, false
		}
	}
	switch k, w := repairs.GuardBookPathsWith(res, b.ID, paths, ""); k {
	case repairs.SkipITunes:
		return w, false
	case repairs.SkipGuardUnreadable:
		return "", true
	}
	return "", false
}

// leftoverTwinsConsecutive reports whether the twins of the leftover's rows,
// taken in the leftover's track order, hold consecutive tracks of the
// combined book.
func leftoverTwinsConsecutive(ms []leftoverMatch) bool {
	sorted := append([]leftoverMatch(nil), ms...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].row.TrackNumber < sorted[j].row.TrackNumber })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].row.TrackNumber == sorted[i-1].row.TrackNumber ||
			sorted[i].twin.TrackNumber != sorted[i-1].twin.TrackNumber+1 {
			return false
		}
	}
	return true
}

// leftoverBasisRank orders match bases from strongest (0) to weakest; a row
// reports its weakest row's basis.
func leftoverBasisRank(b string) int {
	switch b {
	case leftoverBasisSizeHash:
		return 0
	case leftoverBasisSizeChapter:
		return 1
	default:
		return 2
	}
}

// leftoverRootMounted refuses a plan or re-plan when the library root is
// unset, missing or empty: an unmounted share would make every row ENOENT
// and look like a library of leftovers.
func leftoverRootMounted(root string) error {
	if root == "" {
		return errors.New("library root is not configured")
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("library root %s unreadable: %w", root, err)
	}
	if len(ents) == 0 {
		return fmt.Errorf("library root %s is empty (share not mounted?)", root)
	}
	return nil
}

func appendUnique(list []string, v string) []string {
	if contains(list, v) {
		return list
	}
	return append(list, v)
}

func countMissing(rows []database.BookFileCore) int {
	n := 0
	for _, r := range rows {
		if r.Missing {
			n++
		}
	}
	return n
}

// buildIndex reads every row of every live book and indexes them by size.
func (f *consolidationLeftoversFixer) buildIndex(store OpsStore) (books map[string]*database.BookCore, rows map[string][]database.BookFileCore, err error) {
	all, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("list books: %w", err)
	}
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, nil, fmt.Errorf("list book files: %w", err)
	}
	books = make(map[string]*database.BookCore, len(all))
	for i := range all {
		books[all[i].ID] = &all[i]
	}
	rows = map[string][]database.BookFileCore{}
	idx := map[int64][]database.BookFileCore{}
	for i := range cores {
		c := cores[i]
		rows[c.BookID] = append(rows[c.BookID], c)
		if b := books[c.BookID]; b != nil && !b.IsSoftDeleted() && c.FileSize > 0 {
			idx[c.FileSize] = append(idx[c.FileSize], c)
		}
	}
	f.idxMu.Lock()
	f.idx, f.idxBuiltAt = idx, f.now()
	f.idxMu.Unlock()
	return books, rows, nil
}

// sizeIndex is the cached index, rebuilt when older than leftoverIndexTTL.
func (f *consolidationLeftoversFixer) sizeIndex(store OpsStore) (map[int64][]database.BookFileCore, error) {
	f.idxMu.Lock()
	idx, at := f.idx, f.idxBuiltAt
	f.idxMu.Unlock()
	if idx != nil && f.now().Sub(at) < leftoverIndexTTL {
		return idx, nil
	}
	if _, _, err := f.buildIndex(store); err != nil {
		return nil, err
	}
	f.idxMu.Lock()
	defer f.idxMu.Unlock()
	return f.idx, nil
}

// Plan lists every leftover in the library.
func (f *consolidationLeftoversFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	if err := leftoverRootMounted(f.p.deps.RootDir()); err != nil {
		return nil, fmt.Errorf("%s: %w", leftoverFixerID, err)
	}
	books, rowsOf, err := f.buildIndex(store)
	if err != nil {
		return nil, err
	}
	f.idxMu.Lock()
	idx := f.idx
	f.idxMu.Unlock()
	src, err := f.newSource(store)
	if err != nil {
		return nil, err
	}
	src.book = func(id string) (*database.BookCore, error) {
		b := books[id]
		if b == nil {
			return nil, nil
		}
		c := *b
		return &c, nil
	}
	src.rows = func(id string) ([]database.BookFileCore, error) {
		return append([]database.BookFileCore(nil), rowsOf[id]...), nil
	}
	src.same = func(size int64, _ string) ([]database.BookFileCore, error) { return idx[size], nil }
	// The cheap filters first: live, 1..leftoverMaxFiles rows, one unmarked.
	var cands []string
	for id, b := range books {
		rs := rowsOf[id]
		if b.IsSoftDeleted() || len(rs) == 0 || len(rs) > leftoverMaxFiles || countMissing(rs) == len(rs) {
			continue
		}
		cands = append(cands, id)
	}
	sort.Strings(cands)
	out := make([]repairs.Row, len(cands))
	keep := make([]bool, len(cands))
	var done atomic.Int64
	// Each worker writes only out[i] and keep[i] for its own i; the stat
	// memo is locked inside leftoverSource.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(ctx context.Context, i int) error {
		defer done.Add(1)
		r, ok, err := src.decide(ctx, cands[i], nil)
		if err != nil {
			r = repairs.Row{RowID: "leftover:" + cands[i], BookIDs: []string{cands[i]}, Class: leftoverClassHeld,
				Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			sum := sha256.Sum256([]byte(r.RowID + "\nerror\n" + err.Error()))
			r.Fingerprint = hex.EncodeToString(sum[:])[:32]
			ok = true
		}
		out[i], keep[i] = r, ok
		return nil
	}, registry.RunItemsOptions{
		Concurrency: leftoverStatWorkers,
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Consolidation leftovers %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	rows := make([]repairs.Row, 0, len(out))
	for i := range out {
		if keep[i] {
			rows = append(rows, out[i])
		}
	}
	return rows, nil
}

// Replan re-reads the leftover, its rows, every same-size candidate and the
// disk, fresh. A book that is no leftover any more comes back changed.
func (f *consolidationLeftoversFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	id, ok := strings.CutPrefix(planned.RowID, "leftover:")
	if !ok {
		return changedRow(planned, "not a leftovers row"), nil
	}
	var st *leftoverState
	if len(planned.State) > 0 {
		st = &leftoverState{}
		if err := json.Unmarshal(planned.State, st); err != nil {
			return changedRow(planned, "the row's stored state is unreadable; plan again"), nil
		}
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	if err := leftoverRootMounted(f.p.deps.RootDir()); err != nil {
		return repairs.Row{}, fmt.Errorf("%s: %w", leftoverFixerID, err)
	}
	idx, err := f.sizeIndex(store)
	if err != nil {
		return repairs.Row{}, err
	}
	src, err := f.newSource(store)
	if err != nil {
		return repairs.Row{}, err
	}
	src.book = func(bid string) (*database.BookCore, error) {
		b, err := store.GetBookByID(bid)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", bid, err)
		}
		if b == nil {
			return nil, nil
		}
		c := b.Core()
		return &c, nil
	}
	src.rows = func(bid string) ([]database.BookFileCore, error) {
		fs, err := store.GetBookFiles(bid)
		if err != nil {
			return nil, fmt.Errorf("files of %s: %w", bid, err)
		}
		out := make([]database.BookFileCore, len(fs))
		for i := range fs {
			out[i] = fs[i].Core()
		}
		return out, nil
	}
	// Every candidate the index names is re-read: a row moved, re-pathed or
	// gone since the index was built counts as it is now. The scope folder is
	// then walked on disk and every same-size file's row is looked up by
	// path, so an owner created after the index was built is seen too (a
	// second owner makes the row ambiguous, never applied).
	src.same = func(size int64, scope string) ([]database.BookFileCore, error) {
		var out []database.BookFileCore
		seen := map[string]bool{}
		for _, c := range idx[size] {
			cur, err := store.GetBookFileByID(c.BookID, c.ID)
			if err != nil {
				return nil, fmt.Errorf("read row %s: %w", c.ID, err)
			}
			if cur == nil || cur.FileSize != size {
				continue
			}
			seen[cur.ID] = true
			out = append(out, cur.Core())
		}
		walked, err := leftoverWalkOwners(store, scope, size, seen)
		if err != nil {
			return nil, err
		}
		return append(out, walked...), nil
	}
	r, ok, err := src.decide(ctx, id, st)
	if err != nil {
		return repairs.Row{}, err
	}
	if !ok {
		return changedRow(planned, "the book is no longer a consolidation leftover (a file is present, it was retired, or its rows changed)"), nil
	}
	return r, nil
}

// Apply writes one fresh row under merge.LockMergeRMW: re-plan and compare
// the fingerprint, refuse a hand-off that would write an iTunes member, mark
// each dead row Missing, then retire the leftover into the combined book.
func (f *consolidationLeftoversFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	store := f.p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if err := w.LockWaiting(ctx, "the merge lock", merge.LockMergeRMW, merge.UnlockMergeRMW); err != nil {
		return err
	}
	defer merge.UnlockMergeRMW()
	// The index the re-plan reads is rebuilt under the lock when stale.
	locked, err := f.Replan(ctx, nil, fresh, nil)
	if err != nil {
		return err
	}
	if locked.Fingerprint != fresh.Fingerprint || !locked.Applicable() {
		why := locked.Reason
		if !locked.Applicable() {
			why = locked.SkipReason
		}
		return fmt.Errorf("%w: under the merge lock: %s", repairs.ErrChangedSincePlan, why)
	}
	plan, ok := locked.Detail.(*leftoverPlan)
	if !ok {
		return fmt.Errorf("%s: row %s carries no plan", leftoverFixerID, locked.RowID)
	}
	if plan.GroupID != "" {
		dc := &duplicateCopiesFixer{p: f.p}
		if why, err := dc.itunesWouldBeWritten(store, plan.GroupID, []string{plan.Leftover}); err != nil {
			return err
		} else if why != "" {
			return fmt.Errorf("%w: under the merge lock: %s", repairs.ErrChangedSincePlan, why)
		}
	}
	steps := 0
	partial := func(err error) error {
		if steps == 0 {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %w", repairs.ErrPartiallyApplied, steps, err)
	}
	for _, m := range plan.Marks {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		cur, err := store.GetBookFileByID(plan.Leftover, m.RowID)
		if err != nil {
			return partial(fmt.Errorf("read row %s: %w", m.RowID, err))
		}
		if cur != nil && cur.Missing && cur.FilePath == m.Was.Path {
			continue // a cut-off run's finished step
		}
		to := m.Was
		to.Missing = true
		if err := w.RepointBookFile(plan.Leftover, m.RowID, m.Was, to); err != nil {
			return partial(err)
		}
		steps++
	}
	// A slice, never the whole-book rule: a finished chapter must not mark
	// the combined book finished, and the position lands at the chapter.
	slice := plan.Slice
	did, err := retireInto(ctx, f.p, store, w, f.now, leftoverFixerID, plan.Leftover, plan.Combined, &slice)
	steps += did
	if err != nil {
		return partial(err)
	}
	return nil
}
