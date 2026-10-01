// file: internal/plugins/maintenance/duplicate_copies_fixer.go
// version: 1.3.0
// guid: 937b9ff1-48ce-4136-8ca0-74793e6ed3de
// last-edited: 2026-10-01

// Repairs-lane fixer "duplicate-copies": merge whole copies of one book that
// live as separate books, so a chapter fragment that matches every copy has a
// single parent again.
//
// WHY A MERGE, NOT A VERSION LINK (plan 2026-10-01). The fragment fixer's
// parent index is every live book with two or more rows, whatever its primary
// flag. A fragment matching two copies is "ambiguous" there and never
// applied. Linking the copies as versions leaves both live parents, so only
// retiring the extra copies (soft-deleted, merged_into set) unlocks the
// fragment. merge.Service.MergeBooks is not used: it queues iTunes removals
// and is not journaled through repairs.Writer.
//
// IDENTITY (a pair of books is a proven edge only when every clause holds):
//
//   - H: at least 90% of the smaller copy's duration is in rows whose hash
//     (file hash or original hash) equals a row of the other copy. Rows under
//     60 seconds and rows with a boilerplate title (an Audible intro, opening
//     credits) never count toward it, so a shared intro never links two books.
//     A copy with a row of unknown duration cannot be measured: not proven.
//   - T: the normalized titles are equal (fbNorm after a leading track number
//     "44 - " is stripped), and the authors are equal or one is unset or
//     "Unknown Author".
//   - no ASIN conflict (both set and different is a veto);
//   - no not_dup label on the pair (any source: an owner verdict, a rule or a
//     judge -- the fixer never second-guesses one).
//
// Durations are NOT required to be close (owner, 2026-10-01): a short copy
// contained in a full one is the case to fix, and H already measures it.
//
// GROUPS. Pairs are seeded from shared row hashes (and same-title buckets,
// for the unproven listing); every gate is then checked on each pair. A group
// is a connected component of proven and vetoed edges; one with a veto inside
// it, or that is not a clique of proven edges, is skipped. A same-title pair
// with no hash coverage is listed as a skipped copy-unproven row.
//
// iTUNES (owner, 2026-10-01: "ignore the iTunes copy"). A book under
// books/itunes/**, or carrying a book PID, a row PID or iTunes path, or an
// un-tombstoned itunes external id, is never a group member: it is never
// elected, never retired, never written, and is not in the row's books (so
// the framework guard does not skip the row for it). It is listed in the
// row's members and evidence. The fragment fixer then takes the merged
// non-iTunes copy as a fragment's single parent (disregardITunesParents). A
// row whose version-group hand-off would write an iTunes copy's primary flag
// (Crown demotes every member not already explicit false) is skipped.
// Doctor Who / Big Finish / Torchwood are manual-only (framework guard).
//
// SURVIVOR: versionprimary.Eligible (organized, every active file present and
// under the library root), then most present distinct files, longest present
// duration, not under an "Unknown Author" folder, already primary,
// versionprimary.MetadataScore, the lower id.
//
// EACH LOSER ROW:
//
//   - its hash twin on the survivor is present: it stays on the loser;
//   - every twin on the survivor is Missing and the loser's file is present:
//     the survivor's row is repointed at it (book_file_repoint_location);
//   - no twin: it is folded onto the survivor (book_file_reassign) only when
//     it is present, not iTunes, and the survivor's tracks plus every folded
//     track then run 1..N with no gap or repeat; folds may add at most 25% of
//     the matched duration. Anything else skips the group (track_order /
//     fold_cap). A row whose hash an earlier fold or repoint already brought
//     over stays on its loser.
//
// The loser must keep a hash-matched row (an emptied live book reads as an
// abandoned group to the fragment fixer); no book_file row is ever deleted.
//
// RETIRE (retireInto, shared with the fragment fixer, whole-book rule): every
// user's listening state follows (a loser with progress is skipped unless the
// two copies' track layouts are identical), external ids move, it is demoted
// and soft-deleted with merged_into set and file_path cleared. A loser that
// is primary in a version group has its heir elected and crowned BEFORE any
// write (folder-books' electHeirs and handOff, iTunes copies never heirs).
// A loser with user tags the survivor lacks is skipped (owner default).
//
// UNDO. Every write goes through repairs.Writer, journaled before it is made;
// POST /operations/<apply op>/revert restores the lot. Database only: nothing
// on disk moves.
//
// RE-CHECKED UNDER THE LOCK. Apply holds merge.LockMergeRMW, re-plans the row
// (only its own books, the books folder-books' title index finds under the
// members' titles, and their version groups) and refuses it if the
// fingerprint moved. The fingerprint hashes the decision: members, survivor,
// repoint and fold pairs, hand-off groups. A cut-off run re-plans to the same
// decision: the stored fold and repoint ids are put back in the snapshot.
//
// CONCURRENCY. Plan reads the library once, then reads each candidate's
// on-disk and per-book state, judges pairs and builds rows on bounded
// RunItems pools. Apply runs through the framework engine, which never runs
// two rows sharing a book in parallel.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/boilerplate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

const dcFixerID = "duplicate-copies"

// Row classes.
const (
	dcClassMerge    = "merge"
	dcClassUnproven = "copy-unproven"
	dcClassManual   = "manual-only"
)

// Skip kinds this fixer sets itself (the framework adds the guard kinds).
const (
	dcSkipUnproven   = "skipped_copy_unproven"
	dcSkipASIN       = "skipped_conflicting_asin"
	dcSkipNotDup     = "skipped_owner_not_dup"
	dcSkipNotClique  = "skipped_not_clique"
	dcSkipNoSurvivor = "skipped_no_eligible_survivor"
	dcSkipProgress   = "skipped_progress_layout"
	dcSkipTrackOrder = "skipped_track_order"
	dcSkipFoldCap    = "skipped_fold_cap"
	dcSkipLoserEmpty = "skipped_loser_would_empty"
	dcSkipTags       = "skipped_user_tags"
	dcSkipITunesVG   = "skipped_itunes_version_group"
	dcSkipNoHeir     = "skipped_no_primary_heir"
	dcSkipUnreadable = "skipped_unreadable"
	dcSkipBoxSet     = "skipped_box_set"
)

// Identity thresholds.
const (
	dcMinLinkSec = 60
	dcCoverage   = 0.90
	dcFoldCap    = 0.25
	// dcBoxSetRatio: a largest copy with more present audio than this times
	// the Audible runtime on file is a box set, not a copy (dcBoxSet).
	dcBoxSetRatio = 1.5
	// dcMaxHashFan: a hash more books than this share seeds no pairs (a
	// publisher sting, silence); the coverage of a pair seeded otherwise
	// still counts it.
	dcMaxHashFan = 8
	// dcMaxBucket: a title shared by more books than this seeds no unproven
	// pairs.
	dcMaxBucket = 16
)

// Pair verdicts.
const (
	dcEdgeProven   = "proven"
	dcEdgeUnproven = "unproven"
	dcEdgeASIN     = "asin"
	dcEdgeNotDup   = "not_dup"
)

var dcLeadTrackRe = regexp.MustCompile(`^\s*\d{1,3}\s*[-_.]\s*`)

// dcTitleKey is fbNorm of the title after a leading track number ("44 -
// BrainWeb" is "brainweb"); a title that is only a number keeps it.
func dcTitleKey(title string) string {
	if k := fbNorm(dcLeadTrackRe.ReplaceAllString(title, "")); k != "" {
		return k
	}
	return fbNorm(title)
}

// itunesCopyWhy names why a book is an iTunes copy ("" when it is not): a
// book PID, a row PID or iTunes path, a path under books/itunes/** (symlinks
// resolved), an un-tombstoned itunes external id. doubt: a path could not be
// settled, so it cannot be told. The fragment fixer's iTunes-parent rule
// shares it.
func itunesCopyWhy(res *repairs.PathResolver, id, pid string, paths []string, rows []fragFile, exts []database.ExternalIDMapping) (why string, doubt bool) {
	if pid != "" {
		return "book iTunes id " + pid, false
	}
	for _, r := range rows {
		switch {
		case r.ITunesPID != "":
			return "row iTunes id " + r.ITunesPID, false
		case r.ITunesPath != "":
			return "row iTunes path " + r.ITunesPath, false
		}
	}
	for _, e := range exts {
		if e.Source == "itunes" && e.ExternalID != "" && !e.Tombstoned {
			return "itunes external id " + e.ExternalID, false
		}
	}
	switch k, w := repairs.GuardBookPathsWith(res, id, paths, ""); k {
	case repairs.SkipITunes:
		return w, false
	case repairs.SkipGuardUnreadable:
		return "", true
	}
	return "", false
}

// dcBook is one book as the fixer judges it.
type dcBook struct {
	Core database.BookCore
	Rows []database.BookFileCore
	// Title is dcTitleKey; Author fbNorm of the author's name ("" none).
	Title, Author string
	// Read on the pool (detail):
	Present  map[string]bool // row id -> on disk
	ITunes   string          // why it is an iTunes copy
	Doubt    string          // why that, or the guard, could not be told
	Manual   string          // Doctor Who / Big Finish / Torchwood
	UserTags []string
	Progress bool
	Signals  versionprimary.Signals
	detailed bool
}

// distinctRows is b's rows counting each audio once: a row carrying a hash
// (either of its two) an earlier row carried is that audio again, so
// duplicated junk rows cannot outweigh the real ones. A row with no hash is
// its own audio.
func (b *dcBook) distinctRows() []database.BookFileCore {
	seen := map[string]bool{}
	var out []database.BookFileCore
	for _, r := range b.Rows {
		hs := dcHashes(r)
		dup := false
		for _, h := range hs {
			dup = dup || seen[h]
			seen[h] = true
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

// dur is b's distinct audio in seconds.
func (b *dcBook) dur() int {
	n := 0
	for _, r := range b.distinctRows() {
		n += r.Duration
	}
	return n
}

// dcHashKey names a row's audio for distinct counting: its first hash, else
// the row itself.
func dcHashKey(r database.BookFileCore) string {
	if hs := dcHashes(r); len(hs) > 0 {
		return hs[0]
	}
	return "row:" + r.ID
}

func dcHashes(r database.BookFileCore) []string {
	return uniqueNonEmpty(r.FileHash, r.OriginalFileHash)
}

func dcLinkRow(r database.BookFileCore) bool {
	if r.Duration < dcMinLinkSec {
		return false
	}
	title := r.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(r.FilePath), filepath.Ext(r.FilePath))
	}
	return !boilerplate.IsBoilerplateTitle(title)
}

// dcCoverage is H: the share of the smaller copy's distinct audio in link
// rows whose hash a row of the other copy carries, each distinct hash counted
// once in both the matched and the total duration. ok is false when a row of
// either copy has no known duration: an unknown row could be any length, so
// it can neither be weighed nor picked the smaller copy by.
func dcCoverageOf(a, b *dcBook) (cov float64, ok bool) {
	for _, x := range [2]*dcBook{a, b} {
		for _, r := range x.Rows {
			if r.Duration <= 0 {
				return 0, false
			}
		}
	}
	small, big := a, b
	if da, db := a.dur(), b.dur(); db < da || (db == da && b.Core.ID < a.Core.ID) {
		small, big = b, a
	}
	have := map[string]bool{}
	for _, r := range big.Rows {
		for _, h := range dcHashes(r) {
			have[h] = true
		}
	}
	total, matched := 0, 0
	for _, r := range small.distinctRows() {
		total += r.Duration
		if !dcLinkRow(r) {
			continue
		}
		for _, h := range dcHashes(r) {
			if have[h] {
				matched += r.Duration
				break
			}
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(matched) / float64(total), true
}

// dcPairKey is the sorted pair.
func dcPairKey(a, b string) [2]string {
	if b < a {
		a, b = b, a
	}
	return [2]string{a, b}
}

// dcVerdict is one pair's judgement.
type dcVerdict struct {
	Kind string // dcEdge*; "" when the pair is unrelated
	Why  string
}

// dcJudge applies every identity clause to one pair. Each clause is checked
// here and only here.
func dcJudge(a, b *dcBook, notDup map[[2]string]string) dcVerdict {
	if a.Title == "" || a.Title != b.Title {
		return dcVerdict{}
	}
	if a.Author != "" && b.Author != "" && a.Author != b.Author {
		return dcVerdict{}
	}
	if x, y := dcStr(a.Core.ASIN), dcStr(b.Core.ASIN); x != "" && y != "" && !strings.EqualFold(x, y) {
		return dcVerdict{Kind: dcEdgeASIN, Why: fmt.Sprintf("%s and %s carry different ASINs (%s, %s)", a.Core.ID, b.Core.ID, x, y)}
	}
	if why := notDup[dcPairKey(a.Core.ID, b.Core.ID)]; why != "" {
		return dcVerdict{Kind: dcEdgeNotDup, Why: why}
	}
	cov, ok := dcCoverageOf(a, b)
	switch {
	case !ok:
		return dcVerdict{Kind: dcEdgeUnproven, Why: fmt.Sprintf("%s / %s: a row has no known duration", a.Core.ID, b.Core.ID)}
	case cov < dcCoverage:
		return dcVerdict{Kind: dcEdgeUnproven, Why: fmt.Sprintf("%s / %s: %.0f%% of the smaller copy is hash-matched (needs %.0f%%)", a.Core.ID, b.Core.ID, cov*100, dcCoverage*100)}
	}
	return dcVerdict{Kind: dcEdgeProven, Why: fmt.Sprintf("%s / %s: %.0f%% of the smaller copy is hash-matched", a.Core.ID, b.Core.ID, cov*100)}
}

// dcAuthorKey is the author clause's key: fbNorm of the author's name, ""
// (matches any author) for no author or "Unknown Author". An author id with
// no author row is unknown, not absent: its key names the id, so it matches
// only itself and never a known author.
func dcAuthorKey(authors map[int]string, id *int) string {
	if id == nil {
		return ""
	}
	name, ok := authors[*id]
	if !ok {
		return fmt.Sprintf("?author#%d", *id)
	}
	if a := fbNorm(name); a != "unknownauthor" {
		return a
	}
	return ""
}

func dcStr(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// ---- fixer ------------------------------------------------------------------

type duplicateCopiesFixer struct {
	p *Plugin
	// fb lends folder-books' library load, title index, heir election and
	// hand-off.
	fb *folderBooksFixer
	// statFn replaces os.Stat, now time.Now, in tests.
	statFn func(string) (os.FileInfo, error)
	now    func() time.Time
}

func newDuplicateCopiesFixer(p *Plugin) *duplicateCopiesFixer {
	return &duplicateCopiesFixer{p: p, fb: newFolderBooksFixer(p), statFn: os.Stat, now: time.Now}
}

var _ repairs.Fixer = (*duplicateCopiesFixer)(nil)

func (f *duplicateCopiesFixer) ID() string    { return dcFixerID }
func (f *duplicateCopiesFixer) Title() string { return "Duplicate copies" }
func (f *duplicateCopiesFixer) Description() string {
	return "Whole copies of one book stored as separate books (same title and author, 90%+ of the smaller copy's " +
		"audio hash-identical, no ASIN conflict, no not_dup label). Apply keeps the best copy, repoints its missing " +
		"rows at a present twin, folds in files it lacks (gated), and retires the other copies into it with listening " +
		"progress and external ids. iTunes copies are ignored and never written; Doctor Who / Big Finish / Torchwood " +
		"are listed only. Database only. Every step is undoable from the apply operation."
}

type dcParams struct {
	BookIDs []string `json:"book_ids,omitempty"`
}

func (f *duplicateCopiesFixer) stores() (OpsStore, FragmentRepairReader, error) {
	store := f.p.deps.OpsStore()
	hist := f.p.deps.FragmentRepairReader()
	if store == nil || hist == nil {
		return nil, nil, fmt.Errorf("database not initialized")
	}
	return store, hist, nil
}

// dcDismissedGroupsPref is the preference the review page's "reject group"
// writes (handlers/duplicates RejectBookDuplicateGroup): a JSON list of group
// keys, each the group's book ids joined by "+".
const dcDismissedGroupsPref = "dedup_dismissed_groups"

// dcPrefReader reads a user preference (OpsStore has it).
type dcPrefReader interface {
	GetUserPreference(key string) (*database.UserPreference, error)
}

// dcOwnerVerdicts reads every owner rejection of a book pair, with why:
//   - not_dup labels, from any source;
//   - dedup candidates with a terminal status (database.IsTerminalCandidateStatus:
//     dismissed by a human, or merged by auto-resolve), each a decision about
//     the pair this fixer must not remake;
//   - the review page's dismissed duplicate groups, every pair inside one.
//
// Every read is strict: a rejection it cannot read fails the plan rather than
// reading as none, and so does no verdict store at all. The fragment fixer's
// iTunes-parent rule reads the same verdicts.
func dcOwnerVerdicts(vr DedupVerdictReader, prefs dcPrefReader) (map[[2]string]string, error) {
	if vr == nil {
		return nil, errors.New("dedup verdict store unavailable: cannot honour the owner's not_dup labels and dismissals")
	}
	out := map[[2]string]string{}
	add := func(a, b, why string) {
		if a == "" || b == "" || a == b {
			return
		}
		if k := dcPairKey(a, b); out[k] == "" {
			out[k] = why
		}
	}
	labels, err := vr.ListLabeledExamplesStrict(database.LabeledExampleFilter{Label: "not_dup"})
	if err != nil {
		return nil, fmt.Errorf("list not_dup labels: %w", err)
	}
	for _, l := range labels {
		add(l.EntityAID, l.EntityBID, fmt.Sprintf("%s and %s are labeled not_dup (%s)", l.EntityAID, l.EntityBID, l.LabelSource))
	}
	cands, err := vr.TerminalCandidatesStrict("book")
	if err != nil {
		return nil, fmt.Errorf("list decided dedup candidates: %w", err)
	}
	for _, c := range cands {
		add(c.EntityAID, c.EntityBID, fmt.Sprintf("dedup candidate %d (%s / %s) is %s", c.ID, c.EntityAID, c.EntityBID, c.Status))
	}
	pref, err := prefs.GetUserPreference(dcDismissedGroupsPref)
	if err != nil {
		return nil, fmt.Errorf("read dismissed duplicate groups: %w", err)
	}
	if pref != nil && pref.Value != nil && *pref.Value != "" {
		var keys []string
		if err := json.Unmarshal([]byte(*pref.Value), &keys); err != nil {
			return nil, fmt.Errorf("dismissed duplicate groups are unreadable: %w", err)
		}
		for _, k := range keys {
			ids := strings.Split(k, "+")
			for i := range ids {
				for j := i + 1; j < len(ids); j++ {
					add(ids[i], ids[j], fmt.Sprintf("the owner dismissed duplicate group %s", k))
				}
			}
		}
	}
	return out, nil
}

// dcState is stored with the plan (Row.State): what Replan needs.
type dcState struct {
	Members  []string    `json:"members"`
	ITunes   []string    `json:"itunes,omitempty"`
	Survivor string      `json:"survivor"`
	Folds    []dcFold    `json:"folds,omitempty"`
	Repoints []dcRepoint `json:"repoints,omitempty"`
	Extra    []string    `json:"extra,omitempty"` // version-group members Crown may write
}

// dcFold is one loser row folded onto the survivor.
type dcFold struct {
	Row   string `json:"row"`
	From  string `json:"from"`
	Track int    `json:"track"`
	Dur   int    `json:"dur"`
}

// dcRepoint is one survivor row repointed at a loser's present twin.
type dcRepoint struct {
	Row     string                `json:"row"` // the survivor's row
	Loser   string                `json:"loser"`
	LoserRw string                `json:"loser_row"`
	Was     undo.BookFileLocation `json:"was"`
	To      undo.BookFileLocation `json:"to"`
}

// dcPlan is a merge row's decision (Row.Detail).
type dcPlan struct {
	Survivor string
	Losers   []string
	Folds    []dcFold
	Repoints []dcRepoint
	// HandOff maps each version group a primary loser leads to the members
	// that may be its heir (never an iTunes copy).
	HandOff map[string][]string
	ITunes  map[string]bool
}

// ---- plan -------------------------------------------------------------------

// Plan reads the library once and judges every seeded pair.
func (f *duplicateCopiesFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	var params dcParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("%s: invalid params: %w", dcFixerID, err)
		}
	}
	store, _, err := f.stores()
	if err != nil {
		return nil, err
	}
	notDup, err := dcOwnerVerdicts(f.p.deps.DedupVerdictReader(), store)
	if err != nil {
		return nil, err
	}
	lib, err := f.fb.loadFull(store)
	if err != nil {
		return nil, err
	}
	var only map[string]bool
	if len(params.BookIDs) > 0 {
		only = map[string]bool{}
		for _, id := range params.BookIDs {
			only[id] = true
		}
	}
	return f.rowsFor(ctx, rep, store, lib, notDup, only, nil)
}

// dcRun is one rowsFor pass: the snapshot and what it read.
type dcRun struct {
	lib    *fbLib
	books  map[string]*dcBook
	notDup map[[2]string]string
	res    *repairs.PathResolver
	series map[int]string
	// holders names the live books with a link row carrying a hash: the
	// box-set check's leg (a). Plan answers from its whole-library snapshot,
	// a Replan from the store's multi-valued hash lookup (dcStoreHolders),
	// under the apply's lock; both apply the same filter (dcHolder).
	holders func(hash string) ([]string, error)
}

// dcHolder is leg (a)'s one filter, shared by Plan's index and a Replan's
// store read so both reach the same answer: a row that is evidence of a work
// (dcLinkRow) on a book that is not retired.
func dcHolder(r database.BookFileCore, live bool) bool { return live && dcLinkRow(r) }

// dcStoreHolders is leg (a) for a Replan, which loads only the group's
// neighbourhood: each hash is read through BookFilesWithHash (memdb's
// multi-valued hash indexes, verified against Pebble) rather than a scan of
// every book_file per row. A read it cannot complete is an error, never "no
// holder".
func dcStoreHolders(store OpsStore) func(string) ([]string, error) {
	live := map[string]bool{}
	return func(h string) ([]string, error) {
		rows, err := store.BookFilesWithHash(h)
		if err != nil {
			return nil, fmt.Errorf("books holding %s: %w", h, err)
		}
		var out []string
		seen := map[string]bool{}
		for i := range rows {
			r := rows[i].Core()
			if seen[r.BookID] {
				continue
			}
			ok, known := live[r.BookID]
			if !known {
				b, err := store.GetBookByID(r.BookID)
				if err != nil {
					return nil, fmt.Errorf("read %s: %w", r.BookID, err)
				}
				ok = b != nil && !b.IsSoftDeleted()
				live[r.BookID] = ok
			}
			if dcHolder(r, ok) {
				seen[r.BookID] = true
				out = append(out, r.BookID)
			}
		}
		return out, nil
	}
}

func (r *dcRun) book(id string) *dcBook {
	if b, ok := r.books[id]; ok {
		return b
	}
	core, ok := r.lib.books[id]
	if !ok {
		return nil
	}
	b := &dcBook{Core: core, Rows: r.lib.files[id], Title: dcTitleKey(core.Title), Author: dcAuthorKey(r.lib.authors, core.AuthorID)}
	r.books[id] = b
	return b
}

// rowsFor builds every row over the snapshot. only limits the seeds (Plan's
// book_ids); carried is the replanned row's stored state (nil on Plan).
func (f *duplicateCopiesFixer) rowsFor(ctx context.Context, rep registry.Reporter, store OpsStore, lib *fbLib, notDup map[[2]string]string, only map[string]bool, carried *dcState) ([]repairs.Row, error) {
	if rep == nil {
		rep = dcQuiet{} // Replan: a few books, no op to report to
	}
	run := &dcRun{lib: lib, books: map[string]*dcBook{}, notDup: notDup, res: repairs.NewPathResolver(), series: map[int]string{}}
	all, err := store.GetAllSeries()
	if err != nil {
		// The Doctor Who guard reads series names: fail rather than plan past it.
		return nil, fmt.Errorf("list series: %w", err)
	}
	for _, se := range all {
		run.series[se.ID] = se.Name
	}
	var multi []string
	for id, rows := range lib.files {
		if len(rows) >= 2 {
			multi = append(multi, id)
		}
	}
	sort.Strings(multi)
	if carried == nil {
		// The box-set check's whole-library index: every live book, a
		// single-file one too (a book 2 as one m4b, a stray chapter).
		outside := map[string][]string{}
		for id, rows := range lib.files {
			b, listed := lib.books[id]
			live := listed && !b.IsSoftDeleted()
			seen := map[string]bool{}
			for _, r := range rows {
				if !dcHolder(r, live) {
					continue
				}
				for _, h := range dcHashes(r) {
					if !seen[h] {
						seen[h] = true
						outside[h] = append(outside[h], id)
					}
				}
			}
		}
		run.holders = func(h string) ([]string, error) { return outside[h], nil }
	} else {
		run.holders = dcStoreHolders(store)
	}
	byHash, byTitle := map[string][]string{}, map[string][]string{}
	for _, id := range multi {
		b := run.book(id)
		seen := map[string]bool{}
		for _, r := range b.Rows {
			if !dcLinkRow(r) {
				continue
			}
			for _, h := range dcHashes(r) {
				if !seen[h] {
					seen[h] = true
					byHash[h] = append(byHash[h], id)
				}
			}
		}
		if b.Title != "" {
			byTitle[b.Title] = append(byTitle[b.Title], id)
		}
	}
	pairs := map[[2]string]bool{}
	seed := func(ids []string, maxN int) {
		if len(ids) < 2 || len(ids) > maxN {
			return
		}
		for i := range ids {
			for j := i + 1; j < len(ids); j++ {
				if only == nil || only[ids[i]] || only[ids[j]] {
					pairs[dcPairKey(ids[i], ids[j])] = true
				}
			}
		}
	}
	for _, ids := range byHash {
		seed(ids, dcMaxHashFan)
	}
	for _, ids := range byTitle {
		seed(ids, dcMaxBucket)
	}
	keys := make([][2]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	verdicts := make([]dcVerdict, len(keys))
	var judged atomic.Int64
	// Each worker writes only verdicts[i]; run.books is complete for every
	// multi-row book above and read-only here.
	if err := registry.RunItems(ctx, rep, fbIndexes(len(keys)), func(_ context.Context, i int) error {
		defer judged.Add(1)
		verdicts[i] = dcJudge(run.books[keys[i][0]], run.books[keys[i][1]], notDup)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Copy pairs %d/%d", judged.Load(), total) },
	}); err != nil {
		return nil, fmt.Errorf("%s: pairs: %w", dcFixerID, err)
	}
	edges := map[[2]string]dcVerdict{}
	related := map[string]bool{}
	for i, k := range keys {
		if verdicts[i].Kind == "" {
			continue
		}
		edges[k] = verdicts[i]
		related[k[0]], related[k[1]] = true, true
	}
	// Per-book reads (disk, external ids, guard, tags, progress) only for the
	// books in a related pair and the version groups they are in.
	need := map[string]bool{}
	for id := range related {
		need[id] = true
		if vg := lib.books[id].VersionGroupID; vg != nil && *vg != "" {
			for _, o := range lib.byVG[*vg] {
				need[o] = true
			}
		}
	}
	ids := make([]string, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		run.book(id) // allocate before the pool: workers only fill their own
	}
	var read atomic.Int64
	if err := registry.RunItems(ctx, rep, fbIndexes(len(ids)), func(_ context.Context, i int) error {
		defer read.Add(1)
		return f.detail(store, run, run.books[ids[i]])
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Copy details %d/%d", read.Load(), total) },
	}); err != nil {
		return nil, fmt.Errorf("%s: details: %w", dcFixerID, err)
	}
	groups, unproven := dcComponents(run, edges)
	rows := make([]repairs.Row, len(groups))
	var built atomic.Int64
	if err := registry.RunItems(ctx, rep, fbIndexes(len(groups)), func(_ context.Context, i int) error {
		defer built.Add(1)
		rows[i] = f.mergeRow(run, edges, groups[i], carried)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Copy rows %d/%d", built.Load(), total) },
	}); err != nil {
		return nil, fmt.Errorf("%s: rows: %w", dcFixerID, err)
	}
	for _, g := range unproven {
		rows = append(rows, f.unprovenRow(run, edges, g))
	}
	return rows, nil
}

// detail reads one book's on-disk and per-book state. Failures to read are
// recorded as doubt (the row is then skipped), never guessed.
func (f *duplicateCopiesFixer) detail(store OpsStore, run *dcRun, b *dcBook) error {
	id := b.Core.ID
	b.detailed = true
	b.Present = map[string]bool{}
	files := make([]database.BookFile, 0, len(b.Rows))
	paths := []string{b.Core.FilePath}
	var ff []fragFile
	for _, r := range b.Rows {
		paths = append(paths, r.FilePath)
		ff = append(ff, fragFile{ID: r.ID, ITunesPID: r.ITunesPersistentID, ITunesPath: r.ITunesPath})
		if fi, err := f.statFn(r.FilePath); err == nil && fi.Mode().IsRegular() {
			b.Present[r.ID] = true
		}
		files = append(files, database.BookFile{ID: r.ID, BookID: id, FilePath: r.FilePath, Missing: r.Missing,
			Duration: r.Duration, BitrateKbps: r.BitrateKbps, FileSize: r.FileSize})
	}
	exts, err := store.GetExternalIDsForBook(id)
	if err != nil {
		b.Doubt = fmt.Sprintf("external ids of %s unreadable: %v", id, err)
		return nil
	}
	why, doubt := itunesCopyWhy(run.res, id, dcStr(b.Core.ITunesPersistentID), paths, ff, exts)
	b.ITunes = why
	if doubt {
		b.Doubt = fmt.Sprintf("could not tell whether %s is under books/itunes/**", id)
	}
	series := ""
	if b.Core.SeriesID != nil {
		series = run.series[*b.Core.SeriesID]
	}
	if k, w := repairs.GuardBookPathsWith(run.res, id, paths, series); k == repairs.SkipOwnerManual {
		b.Manual = w
	} else if k2, w2 := repairs.GuardBookTitle(id, b.Core.Title); k2 == repairs.SkipOwnerManual {
		b.Manual = w2
	}
	if b.Manual == "" {
		full := b.Core.ToBook()
		authors, aerr := repairs.BookAuthorNames(store, &full)
		if aerr != nil {
			b.Doubt = fmt.Sprintf("authors of %s unreadable: %v", id, aerr)
			return nil
		}
		if k, w := repairs.GuardBookCredits(id, dcStr(b.Core.Publisher), dcStr(b.Core.Narrator), authors); k == repairs.SkipOwnerManual {
			b.Manual = w
		}
	}
	if tr := f.p.deps.BookTagReader(); tr != nil {
		tags, terr := tr.GetBookTagsDetailed(id)
		if terr != nil {
			b.Doubt = fmt.Sprintf("tags of %s unreadable: %v", id, terr)
			return nil
		}
		for _, t := range tags {
			if t.Source == "user" || t.Source == "" {
				b.UserTags = append(b.UserTags, strings.ToLower(t.Tag))
			}
		}
		labels, lerr := tr.GetBookUserTags(id)
		if lerr != nil {
			b.Doubt = fmt.Sprintf("user labels of %s unreadable: %v", id, lerr)
			return nil
		}
		for _, l := range labels {
			b.UserTags = append(b.UserTags, strings.ToLower(l))
		}
	} else {
		b.Doubt = "tag store unavailable"
	}
	if um := f.p.deps.MergeUserStateStore(); um != nil {
		has, perr := merge.BookHasUserProgress(um, id)
		if perr != nil {
			b.Doubt = fmt.Sprintf("listening progress of %s unreadable: %v", id, perr)
			return nil
		}
		b.Progress = has
	} else {
		b.Doubt = "user-state store unavailable"
	}
	loader := versionprimary.Loader{Files: dcFiles{id: files}, Chapters: dcNoChapters{}, RootDir: config.AppConfig.RootDir, Stat: f.statFn}
	full := b.Core.ToBook()
	s, err := loader.Load(context.Background(), &full, true)
	if err != nil {
		b.Doubt = fmt.Sprintf("signals of %s: %v", id, err)
		return nil
	}
	b.Signals = s
	return nil
}

// dcQuiet is the reporter of a Replan's pools: there is no operation to
// report progress to.
type dcQuiet struct{}

func (dcQuiet) UpdateProgress(int, int, string) error      { return nil }
func (dcQuiet) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (dcQuiet) Logger() *slog.Logger                       { return logger.FromContext(context.Background()) }
func (dcQuiet) Checkpoint(any) error                       { return nil }
func (dcQuiet) IsCanceled() bool                           { return false }
func (dcQuiet) Trigger(context.Context, string, any) error { return nil }
func (dcQuiet) SetCurrentItem(string)                      {}
func (q dcQuiet) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, q)
}

// dcFiles serves versionprimary.Loader the snapshot's rows.
type dcFiles map[string][]database.BookFile

func (d dcFiles) GetBookFiles(id string) ([]database.BookFile, error) { return d[id], nil }

// dcNoChapters: chapter counts do not take part in this election.
type dcNoChapters struct{}

func (dcNoChapters) GetChaptersForBook(string) ([]database.Chapter, error) { return nil, nil }

// dcComponents returns the merge groups (connected components of proven and
// vetoed edges among non-iTunes books) and the unproven groups (components of
// unproven edges among the remaining non-iTunes books), each sorted.
func dcComponents(run *dcRun, edges map[[2]string]dcVerdict) (merge, unproven [][]string) {
	comps := func(want func(dcVerdict) bool, skip map[string]bool) [][]string {
		parent := map[string]string{}
		var find func(string) string
		find = func(x string) string {
			if parent[x] != x {
				parent[x] = find(parent[x])
			}
			return parent[x]
		}
		for k, v := range edges {
			a, b := run.books[k[0]], run.books[k[1]]
			if !want(v) || a.ITunes != "" || b.ITunes != "" || skip[k[0]] || skip[k[1]] {
				continue
			}
			for _, x := range k {
				if _, ok := parent[x]; !ok {
					parent[x] = x
				}
			}
			parent[find(k[0])] = find(k[1])
		}
		by := map[string][]string{}
		for x := range parent {
			by[find(x)] = append(by[find(x)], x)
		}
		out := make([][]string, 0, len(by))
		for _, g := range by {
			sort.Strings(g)
			out = append(out, g)
		}
		sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
		return out
	}
	merge = comps(func(v dcVerdict) bool { return v.Kind != dcEdgeUnproven }, nil)
	in := map[string]bool{}
	for _, g := range merge {
		for _, id := range g {
			in[id] = true
		}
	}
	unproven = comps(func(v dcVerdict) bool { return v.Kind == dcEdgeUnproven }, in)
	return merge, unproven
}

// dcITunesNeighbours lists the iTunes copies with a related edge to a group.
func dcITunesNeighbours(run *dcRun, edges map[[2]string]dcVerdict, members []string) []string {
	in := map[string]bool{}
	for _, m := range members {
		in[m] = true
	}
	seen := map[string]bool{}
	var out []string
	for k := range edges {
		for i, x := range k {
			y := k[1-i]
			if in[x] && !in[y] && run.books[y].ITunes != "" && !seen[y] {
				seen[y] = true
				out = append(out, y)
			}
		}
	}
	sort.Strings(out)
	return out
}

func dcMember(b *dcBook, role string) repairs.RowMember {
	m := repairs.RowMember{BookID: b.Core.ID, Title: b.Core.Title, Role: role, Files: len(b.Rows)}
	for _, r := range b.Rows {
		if r.Missing {
			m.MissingFiles++
		}
	}
	return m
}

func (f *duplicateCopiesFixer) unprovenRow(run *dcRun, edges map[[2]string]dcVerdict, ids []string) repairs.Row {
	head := run.books[ids[0]]
	r := repairs.Row{RowID: dcClassUnproven + ":" + ids[0], Class: dcClassUnproven, BookIDs: ids, Title: head.Core.Title,
		Risk: repairs.RiskReview, Skipped: dcSkipUnproven}
	for _, id := range ids {
		r.Members = append(r.Members, dcMember(run.books[id], "copy"))
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if v, ok := edges[dcPairKey(ids[i], ids[j])]; ok {
				r.Evidence = append(r.Evidence, v.Why)
			}
		}
	}
	r.Reason = fmt.Sprintf("%d books share title and author but their audio is not proven identical", len(ids))
	r.SkipReason = "title, author and duration may match, but less than 90% of the smaller copy is hash-matched; check by hand"
	r.Fingerprint = fragFingerprint(append([]string{dcClassUnproven}, ids...)...)
	return r
}

// dcBoxSet is the owner's 2026-10-01 box-set check. A set holding book 1 and
// more passes the identity gate against book 1 alone, since all of book 1 is
// inside it, so a group is skipped when either:
//
//	(a) audio the larger copies have and the smallest lacks is also held by a
//	    live book outside the group (book 2 stored as its own book). The
//	    group's iTunes copies are the same work and do not count. A stray
//	    chapter of a short copy counts too, until fragment consolidation
//	    retires it into its single parent;
//	(b) the largest copy's present audio is over dcBoxSetRatio times the
//	    Audible runtime on file (the smallest copy's, else any member's).
//
// Both legs run on Plan and on every Replan: (a) reads run.holders, so a
// book 2 imported between plan and apply skips the row under the apply's
// lock. (a) is not in the fingerprint: a hit makes the row skipped, which the
// engine refuses. The runtime (b) used is (rt). ev is the passing group's
// evidence line; with no runtime on file it says so, for the owner to see. err:
// leg (a) could not read who holds a hash, and the group must not pass.
func dcBoxSet(run *dcRun, ids, itunes []string) (skip, ev, rt string, err error) {
	byDur := append([]string(nil), ids...)
	sort.SliceStable(byDur, func(i, j int) bool {
		di, dj := run.books[byDur[i]].dur(), run.books[byDur[j]].dur()
		if di != dj {
			return di < dj
		}
		return byDur[i] < byDur[j]
	})
	small, large := run.books[byDur[0]], run.books[byDur[len(byDur)-1]]
	{
		in := map[string]bool{}
		for _, id := range append(append([]string(nil), ids...), itunes...) {
			in[id] = true
		}
		have := map[string]bool{}
		for _, r := range small.Rows {
			for _, h := range dcHashes(r) {
				have[h] = true
			}
		}
		seen := map[string]bool{}
		var hits []string
		for _, id := range byDur[1:] {
			for _, r := range run.books[id].Rows {
				hs := dcHashes(r)
				lacks := dcLinkRow(r)
				for _, h := range hs {
					lacks = lacks && !have[h]
				}
				if !lacks {
					continue
				}
				for _, h := range hs {
					holders, err := run.holders(h)
					if err != nil {
						return "", "", "", err
					}
					for _, o := range holders {
						if !in[o] && !seen[o] {
							seen[o] = true
							hits = append(hits, o)
						}
					}
				}
			}
		}
		if len(hits) > 0 {
			sort.Strings(hits)
			return fmt.Sprintf("possible box set: audio the larger copies hold beyond %s is also in live book(s) outside the group: %s "+
				"(a stray chapter clears once fragment consolidation retires it)", small.Core.ID, strings.Join(fbFirst(hits, 5), ", ")), "", "", nil
		}
	}
	runtime, from := 0, ""
	if m := small.Core.AudibleRuntimeMin; m != nil && *m > 0 {
		runtime, from = *m, small.Core.ID
	} else {
		for _, id := range ids {
			if m := run.books[id].Core.AudibleRuntimeMin; m != nil && *m > 0 {
				runtime, from = *m, id
				break
			}
		}
	}
	if runtime == 0 {
		return "", "box-set check: no runtime on file", "", nil
	}
	rt = fmt.Sprintf("rt:%s|%d", from, runtime)
	_, secs := large.presentStats()
	if float64(secs) > dcBoxSetRatio*float64(runtime)*60 {
		return fmt.Sprintf("possible box set: copy %s has %d min of present audio, over %.1fx the %d min Audible runtime on %s",
			large.Core.ID, secs/60, dcBoxSetRatio, runtime, from), "", rt, nil
	}
	return "", fmt.Sprintf("box-set check: copy %s has %d min present, Audible runtime %d min (on %s)", large.Core.ID, secs/60, runtime, from), rt, nil
}

// present reports a loser or survivor row on disk and not flagged Missing.
func (b *dcBook) present(r database.BookFileCore) bool { return !r.Missing && b.Present[r.ID] }

// presentStats counts present distinct files and their duration.
func (b *dcBook) presentStats() (files, secs int) {
	seen := map[string]bool{}
	for _, r := range b.Rows {
		if b.present(r) && !seen[r.FilePath] {
			seen[r.FilePath] = true
			files++
			secs += r.Duration
		}
	}
	return files, secs
}

// dcElect orders the members, best survivor first.
func dcElect(run *dcRun, ids []string) []string {
	type cand struct {
		id                 string
		eligible           bool
		files, secs, score int
		unknown, primary   bool
	}
	cs := make([]cand, len(ids))
	for i, id := range ids {
		b := run.books[id]
		full := b.Core.ToBook()
		files, secs := b.presentStats()
		cs[i] = cand{id: id, eligible: versionprimary.Eligible(&full, b.Signals), files: files, secs: secs,
			unknown: b.Signals.UnknownAuthorPath, primary: fbPrimary(b.Core), score: versionprimary.MetadataScore(&full)}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		switch {
		case a.eligible != b.eligible:
			return a.eligible
		case a.files != b.files:
			return a.files > b.files
		case a.secs != b.secs:
			return a.secs > b.secs
		case a.unknown != b.unknown:
			return !a.unknown
		case a.primary != b.primary:
			return a.primary
		case a.score != b.score:
			return a.score > b.score
		}
		return a.id < b.id
	})
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.id
	}
	if !cs[0].eligible {
		return nil
	}
	return out
}

// mergeRow judges one group and plans its writes.
func (f *duplicateCopiesFixer) mergeRow(run *dcRun, edges map[[2]string]dcVerdict, ids []string, carried *dcState) repairs.Row {
	head := run.books[ids[0]]
	r := repairs.Row{RowID: "dup:" + ids[0], Class: dcClassMerge, BookIDs: append([]string(nil), ids...),
		Title: head.Core.Title, Risk: repairs.RiskReview}
	if head.Core.AuthorID != nil {
		r.Author = run.lib.authors[*head.Core.AuthorID]
	}
	itunes := dcITunesNeighbours(run, edges, ids)
	if carried != nil {
		// The iTunes copies the plan listed stay listed (their edge may be
		// one Replan did not seed).
		for _, id := range carried.ITunes {
			if b := run.books[id]; b != nil && b.ITunes != "" && !contains(itunes, id) {
				itunes = append(itunes, id)
			}
		}
		sort.Strings(itunes)
	}
	st := dcState{Members: ids, ITunes: itunes}
	fp := []string{"members=" + strings.Join(ids, ",")}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if v, ok := edges[dcPairKey(ids[i], ids[j])]; ok {
				r.Evidence = append(r.Evidence, v.Why)
			}
		}
	}
	for _, it := range itunes {
		r.Evidence = append(r.Evidence, fmt.Sprintf("iTunes copy %s (%s): ignored, never written", it, run.books[it].ITunes))
	}
	finish := func() repairs.Row {
		if raw, err := json.Marshal(st); err == nil {
			r.State = raw
		}
		r.Fingerprint = fragFingerprint(fp...)
		if r.Members == nil {
			for _, id := range ids {
				r.Members = append(r.Members, dcMember(run.books[id], "copy"))
			}
			for _, it := range itunes {
				r.Members = append(r.Members, dcMember(run.books[it], "iTunes copy (ignored, never written)"))
			}
		}
		return r
	}
	fail := func(kind, why string) repairs.Row {
		r.Skipped, r.SkipReason = kind, why
		return finish()
	}
	// Hands-off and unreadable members.
	for _, id := range ids {
		b := run.books[id]
		if b.Manual != "" {
			r.Class = dcClassManual
			return fail(repairs.SkipOwnerManual, b.Manual)
		}
		if b.Doubt != "" {
			return fail(dcSkipUnreadable, b.Doubt)
		}
	}
	// Vetoes and the clique test.
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			v, ok := edges[dcPairKey(ids[i], ids[j])]
			switch {
			case ok && v.Kind == dcEdgeASIN:
				return fail(dcSkipASIN, v.Why)
			case ok && v.Kind == dcEdgeNotDup:
				return fail(dcSkipNotDup, v.Why)
			}
		}
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if v, ok := edges[dcPairKey(ids[i], ids[j])]; !ok || v.Kind != dcEdgeProven {
				why := fmt.Sprintf("%s and %s are not proven copies of each other, though each is linked to the group", ids[i], ids[j])
				if ok {
					why = v.Why
				}
				return fail(dcSkipNotClique, why)
			}
		}
	}
	skip, ev, rt, berr := dcBoxSet(run, ids, itunes)
	if berr != nil {
		return fail(dcSkipUnreadable, "box-set check could not read who else holds the larger copy's audio: "+berr.Error())
	}
	if rt != "" {
		fp = append(fp, rt)
	}
	if skip != "" {
		return fail(dcSkipBoxSet, skip)
	}
	r.Evidence = append(r.Evidence, ev)
	order := dcElect(run, ids)
	if order == nil {
		return fail(dcSkipNoSurvivor, "no copy is organized with every file present under the library root")
	}
	survivor := order[0]
	sb := run.books[survivor]
	losers := append([]string(nil), order[1:]...)
	sort.Strings(losers)
	st.Survivor = survivor
	fp = append(fp, "survivor="+survivor)
	for _, id := range ids {
		role := "copy (retired into the survivor)"
		if id == survivor {
			role = "survivor"
		}
		r.Members = append(r.Members, dcMember(run.books[id], role))
	}
	for _, it := range itunes {
		r.Members = append(r.Members, dcMember(run.books[it], "iTunes copy (ignored, never written)"))
	}
	plan := &dcPlan{Survivor: survivor, Losers: losers, HandOff: map[string][]string{}, ITunes: map[string]bool{}}
	for _, it := range itunes {
		plan.ITunes[it] = true
	}

	// Loser rows.
	svHash := map[string][]database.BookFileCore{}
	tracks := map[int]int{}
	matchedSecs := 0
	for _, x := range sb.Rows {
		for _, h := range dcHashes(x) {
			svHash[h] = append(svHash[h], x)
		}
		tracks[x.TrackNumber]++
	}
	brought := map[string]bool{}      // hashes a fold or repoint already brings
	repointed := map[string]bool{}    // survivor rows already repointed
	matchedAudio := map[string]bool{} // dcHashKey of survivor rows matched
	for _, l := range losers {
		lb := run.books[l]
		kept := 0
		for _, x := range lb.Rows {
			hs := dcHashes(x)
			var twins []database.BookFileCore
			for _, h := range hs {
				twins = append(twins, svHash[h]...)
			}
			already := false
			for _, h := range hs {
				already = already || brought[h]
			}
			switch {
			case len(twins) > 0:
				kept++
				for _, t := range twins {
					if k := dcHashKey(t); !matchedAudio[k] {
						matchedAudio[k] = true
						matchedSecs += t.Duration
					}
				}
				anyPresent := false
				for _, t := range twins {
					anyPresent = anyPresent || sb.present(t) || repointed[t.ID]
				}
				if anyPresent || !lb.present(x) {
					continue
				}
				t := twins[0]
				for _, c := range twins[1:] {
					if c.ID < t.ID {
						t = c
					}
				}
				repointed[t.ID] = true
				was := undo.BookFileLocation{Path: t.FilePath, Missing: t.Missing, Hash: t.FileHash, Size: t.FileSize}
				to := undo.BookFileLocation{Path: x.FilePath, Missing: false, Hash: x.FileHash, Size: x.FileSize}
				plan.Repoints = append(plan.Repoints, dcRepoint{Row: t.ID, Loser: l, LoserRw: x.ID, Was: was, To: to})
				fp = append(fp, "rp:"+t.ID+"|"+x.ID)
			case already:
				kept++ // a fold from an earlier loser brings this file
			default:
				switch {
				case !lb.present(x):
					return fail(dcSkipTrackOrder, fmt.Sprintf("copy %s row %s (%s) has no twin on the survivor and is not on disk to fold", l, x.ID, filepath.Base(x.FilePath)))
				case x.ITunesPersistentID != "" || x.ITunesPath != "":
					return fail(dcSkipTrackOrder, fmt.Sprintf("copy %s row %s has no twin on the survivor and is an iTunes track", l, x.ID))
				case x.Duration <= 0:
					// Never folded: a row of unknown length cannot be
					// weighed against the fold cap.
					return fail(dcSkipFoldCap, fmt.Sprintf("copy %s row %s has no twin on the survivor and no known duration", l, x.ID))
				}
				for _, h := range hs {
					brought[h] = true
				}
				plan.Folds = append(plan.Folds, dcFold{Row: x.ID, From: l, Track: x.TrackNumber, Dur: x.Duration})
				fp = append(fp, fmt.Sprintf("fold:%s|%s|%d", x.ID, l, x.TrackNumber))
			}
		}
		folded := 0
		for _, fo := range plan.Folds {
			if fo.From == l {
				folded++
			}
		}
		if kept == 0 || len(lb.Rows)-folded == 0 {
			return fail(dcSkipLoserEmpty, fmt.Sprintf("copy %s would keep no hash-matched row", l))
		}
	}
	if len(plan.Folds) > 0 {
		all := map[int]int{}
		for k, v := range tracks {
			all[k] += v
		}
		foldSecs := 0
		for _, fo := range plan.Folds {
			all[fo.Track]++
			foldSecs += fo.Dur
		}
		for n := 1; n <= len(sb.Rows)+len(plan.Folds); n++ {
			if all[n] != 1 {
				return fail(dcSkipTrackOrder, fmt.Sprintf("folding %d file(s) would not leave the survivor's tracks running 1..%d (track %d appears %d times)",
					len(plan.Folds), len(sb.Rows)+len(plan.Folds), n, all[n]))
			}
		}
		if matchedSecs <= 0 || float64(foldSecs) > dcFoldCap*float64(matchedSecs) {
			return fail(dcSkipFoldCap, fmt.Sprintf("folded files add %ds to %ds of matched audio (cap %.0f%%)", foldSecs, matchedSecs, dcFoldCap*100))
		}
	}
	// Listening progress, user tags.
	for _, l := range losers {
		lb := run.books[l]
		if lb.Progress && !dcSameLayout(lb, sb) {
			return fail(dcSkipProgress, fmt.Sprintf("copy %s has listening progress and its track layout differs from the survivor's; positions would land in the wrong place", l))
		}
		have := map[string]bool{}
		for _, t := range sb.UserTags {
			have[t] = true
		}
		var lacks []string
		for _, t := range lb.UserTags {
			if !have[t] {
				lacks = append(lacks, t)
			}
		}
		if len(lacks) > 0 {
			return fail(dcSkipTags, fmt.Sprintf("copy %s has user tag(s) the survivor lacks: %s", l, strings.Join(fbFirst(lacks, 5), ", ")))
		}
	}
	// Version groups a primary loser leads. The fingerprint names every
	// loser's group whatever its flag: the hand-off and the retire both
	// demote the loser, and a cut-off run must re-plan to the same decision.
	for _, l := range losers {
		if vg := run.books[l].Core.VersionGroupID; vg != nil && *vg != "" {
			fp = append(fp, "vg:"+l+"|"+*vg)
		}
	}
	isLoser := map[string]bool{}
	for _, l := range losers {
		isLoser[l] = true
	}
	for _, l := range losers {
		lb := run.books[l]
		vg := lb.Core.VersionGroupID
		if !fbPrimary(lb.Core) || vg == nil || *vg == "" {
			continue
		}
		gid := *vg
		if _, done := plan.HandOff[gid]; done {
			continue
		}
		var heirs []string
		others := 0
		for _, o := range run.lib.byVG[gid] {
			if isLoser[o] {
				continue
			}
			others++
			ob := run.books[o]
			if ob == nil || !ob.detailed {
				return fail(dcSkipUnreadable, fmt.Sprintf("version group %s member %s was not read", gid, o))
			}
			if ob.Doubt != "" {
				return fail(dcSkipUnreadable, ob.Doubt)
			}
			if ob.ITunes != "" {
				if ob.Core.IsPrimaryVersion == nil || *ob.Core.IsPrimaryVersion {
					return fail(dcSkipITunesVG, fmt.Sprintf("copy %s leads version group %s, whose iTunes member %s is not explicitly non-primary: the hand-off would write it", l, gid, o))
				}
				continue
			}
			full := ob.Core.ToBook()
			if versionprimary.Eligible(&full, ob.Signals) || dcCrownable(ob) {
				heirs = append(heirs, o)
			}
		}
		if others == 0 {
			continue
		}
		if len(heirs) == 0 {
			return fail(dcSkipNoHeir, fmt.Sprintf("copy %s is primary in version group %s and no non-iTunes member could be crowned in its place", l, gid))
		}
		sort.Strings(heirs)
		plan.HandOff[gid] = heirs
	}
	inRow := map[string]bool{}
	for _, id := range r.BookIDs {
		inRow[id] = true
	}
	for gid := range plan.HandOff {
		for _, o := range run.lib.byVG[gid] {
			if b := run.books[o]; b != nil && b.ITunes == "" && !inRow[o] {
				inRow[o] = true
				r.BookIDs = append(r.BookIDs, o)
				st.Extra = append(st.Extra, o)
			}
		}
	}
	sort.Strings(r.BookIDs)
	sort.Strings(st.Extra)
	sort.Strings(fp[1:])
	st.Folds, st.Repoints = plan.Folds, plan.Repoints
	r.Detail = plan
	r.Current = map[string]string{"copies": strconv.Itoa(len(ids)), "survivor_files": strconv.Itoa(len(sb.Rows))}
	r.Proposed = map[string]string{"survivor": survivor, "retire": strings.Join(losers, ","),
		"fold": strconv.Itoa(len(plan.Folds)), "repoint": strconv.Itoa(len(plan.Repoints))}
	r.Reason = fmt.Sprintf("%d copies of one book; keep %s, retire %d", len(ids), survivor, len(losers))
	return finish()
}

// dcCrownable is folder-books' noHeir rule for one member: organized, at
// least one active row, every active row under the library root.
func dcCrownable(b *dcBook) bool {
	if b.Core.LibraryState == nil || *b.Core.LibraryState != "organized" || config.AppConfig.RootDir == "" {
		return false
	}
	return b.Signals.ActiveFiles > 0 && b.Signals.AllUnderRoot
}

// dcSameLayout: same number of rows, and in track order each pair shares a
// hash or an equal known duration.
func dcSameLayout(a, b *dcBook) bool {
	if len(a.Rows) != len(b.Rows) {
		return false
	}
	order := func(rows []database.BookFileCore) []database.BookFileCore {
		out := append([]database.BookFileCore(nil), rows...)
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].TrackNumber != out[j].TrackNumber {
				return out[i].TrackNumber < out[j].TrackNumber
			}
			return out[i].FilePath < out[j].FilePath
		})
		return out
	}
	x, y := order(a.Rows), order(b.Rows)
	for i := range x {
		share := false
		for _, h := range dcHashes(x[i]) {
			share = share || contains(dcHashes(y[i]), h)
		}
		if !share && (x[i].Duration <= 0 || x[i].Duration != y[i].Duration) {
			return false
		}
	}
	return true
}

// ---- replan -----------------------------------------------------------------

// Replan reads only the row's neighbourhood: its members and iTunes copies,
// every live book folder-books' title index lists under a member's title,
// the members' version groups. A cut-off run's finished folds and repoints
// are put back in the snapshot, and its retired members read as live, so the
// row re-plans to its planned decision.
func (f *duplicateCopiesFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	if !strings.HasPrefix(planned.RowID, "dup:") {
		return planned, nil // unproven rows are never applicable
	}
	var st dcState
	if err := json.Unmarshal(planned.State, &st); err != nil || len(st.Members) == 0 {
		return changedRow(planned, "the plan carries no stored members; plan again"), nil
	}
	store, _, err := f.stores()
	if err != nil {
		return repairs.Row{}, err
	}
	notDup, err := dcOwnerVerdicts(f.p.deps.DedupVerdictReader(), store)
	if err != nil {
		return repairs.Row{}, err
	}
	lib := newFBLib()
	c, err := f.fb.common(store, false)
	if err != nil {
		return repairs.Row{}, err
	}
	lib.use(c)
	seen := map[string]bool{}
	members := map[string]bool{}
	for _, m := range st.Members {
		members[m] = true
	}
	load := func(id string) (*database.Book, error) {
		if seen[id] {
			return nil, nil
		}
		seen[id] = true
		b, err := store.GetBookByID(id)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", id, err)
		}
		if b == nil {
			return nil, nil
		}
		files, err := store.GetBookFiles(id)
		if err != nil {
			return nil, fmt.Errorf("files of %s: %w", id, err)
		}
		core := b.Core()
		// A member this row already retired reads as live: the decision is
		// re-made as planned and its finished steps are skipped by Apply.
		if members[id] && core.IsSoftDeleted() && core.MergedIntoBookID != nil && *core.MergedIntoBookID == st.Survivor {
			core.MarkedForDeletion, core.MarkedForDeletionAt, core.MergedIntoBookID = nil, nil, nil
		}
		rows := make([]database.BookFileCore, len(files))
		for i := range files {
			rows[i] = files[i].Core()
		}
		lib.add(core, rows)
		return b, nil
	}
	ids := append(append(append([]string(nil), st.Members...), st.ITunes...), st.Extra...)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return repairs.Row{}, err
		}
		if _, err := load(id); err != nil {
			return repairs.Row{}, err
		}
	}
	titles, err := f.fb.titleIndexBy(store, "dcTitleKey", dcTitleKey)
	if err != nil {
		return repairs.Row{}, err
	}
	for _, m := range st.Members {
		if b, ok := lib.books[m]; ok {
			for _, id := range titles[dcTitleKey(b.Title)] {
				if _, err := load(id); err != nil {
					return repairs.Row{}, err
				}
			}
		}
	}
	for _, m := range st.Members {
		if b, ok := lib.books[m]; ok && b.VersionGroupID != nil && *b.VersionGroupID != "" {
			group, err := store.GetBooksByVersionGroup(*b.VersionGroupID)
			if err != nil {
				return repairs.Row{}, fmt.Errorf("version group %s: %w", *b.VersionGroupID, err)
			}
			for i := range group {
				if _, err := load(group[i].ID); err != nil {
					return repairs.Row{}, err
				}
			}
		}
	}
	// Put a cut-off run's finished folds and repoints back as planned.
	for _, fo := range st.Folds {
		dcMoveRow(lib, fo.Row, st.Survivor, fo.From)
	}
	for _, rp := range st.Repoints {
		rows := lib.files[st.Survivor]
		for i := range rows {
			if rows[i].ID == rp.Row && rows[i].FilePath == rp.To.Path && !rows[i].Missing {
				rows[i].FilePath, rows[i].Missing, rows[i].FileHash, rows[i].FileSize = rp.Was.Path, rp.Was.Missing, rp.Was.Hash, rp.Was.Size
			}
		}
	}
	lib.finish()
	rows, err := f.rowsFor(ctx, nil, store, lib, notDup, members, &st)
	if err != nil {
		return repairs.Row{}, err
	}
	for _, r := range rows {
		if r.RowID == planned.RowID {
			return r, nil
		}
	}
	return changedRow(planned, "the copies no longer form this group"), nil
}

// dcMoveRow moves row id from one book to another in the snapshot.
func dcMoveRow(lib *fbLib, id, from, to string) {
	rows := lib.files[from]
	for i, r := range rows {
		if r.ID == id {
			lib.files[from] = append(rows[:i:i], rows[i+1:]...)
			r.BookID = to
			lib.files[to] = append(lib.files[to], r)
			return
		}
	}
}

// ---- apply ------------------------------------------------------------------

// Apply writes one fresh row under merge.LockMergeRMW: re-plan and compare
// the fingerprint, elect each led version group's heir (refusing before any
// write), repoint, fold, hand off primacy, then retire every loser.
func (f *duplicateCopiesFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	store, _, err := f.stores()
	if err != nil {
		return err
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
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
	plan, ok := locked.Detail.(*dcPlan)
	if !ok {
		return fmt.Errorf("%s: row %s carries no plan", dcFixerID, locked.RowID)
	}
	allowed := map[string]bool{}
	for _, ids := range plan.HandOff {
		for _, id := range ids {
			allowed[id] = true
		}
	}
	// Only the plan's non-iTunes heir candidates stand.
	heirs, err := f.fb.electHeirs(ctx, store, plan.Losers, func(id string) bool { return !allowed[id] })
	if err != nil {
		return err // nothing written yet
	}
	var steps int
	partial := func(err error) error {
		if steps == 0 {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %v", repairs.ErrPartiallyApplied, steps, err)
	}
	for _, rp := range plan.Repoints {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		cur, err := store.GetBookFileByID(plan.Survivor, rp.Row)
		if err != nil {
			return partial(fmt.Errorf("read row %s: %w", rp.Row, err))
		}
		if cur != nil && cur.FilePath == rp.To.Path && !cur.Missing {
			continue // a cut-off run's finished step
		}
		if err := w.RepointBookFile(plan.Survivor, rp.Row, rp.Was, rp.To); err != nil {
			return partial(err)
		}
		steps++
	}
	byLoser := map[string][]string{}
	for _, fo := range plan.Folds {
		cur, err := store.GetBookFileByID(fo.From, fo.Row)
		if err != nil {
			return partial(fmt.Errorf("read row %s: %w", fo.Row, err))
		}
		if cur != nil {
			byLoser[fo.From] = append(byLoser[fo.From], fo.Row)
		}
	}
	for _, l := range plan.Losers {
		if len(byLoser[l]) == 0 {
			continue
		}
		if err := w.MoveBookFiles(byLoser[l], l, plan.Survivor); err != nil {
			return partial(err)
		}
		steps++
	}
	if len(plan.Repoints) > 0 || len(plan.Folds) > 0 {
		if err := w.Recompute(plan.Survivor); err != nil {
			return partial(fmt.Errorf("recompute %s: %w", plan.Survivor, err))
		}
	}
	gids := make([]string, 0, len(heirs))
	for gid := range heirs {
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	for _, gid := range gids {
		if why, err := f.itunesWouldBeWritten(store, gid, plan.Losers); err != nil {
			return partial(err)
		} else if why != "" {
			return partial(fmt.Errorf("%w: under the merge lock: %s", repairs.ErrChangedSincePlan, why))
		}
		did, err := f.fb.handOff(store, w, plan.Losers, gid, heirs[gid])
		steps += did
		if err != nil {
			return partial(err)
		}
	}
	for _, l := range plan.Losers {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		did, err := retireInto(ctx, f.p, store, w, f.now, dcFixerID, l, plan.Survivor, nil)
		steps += did
		if err != nil {
			return partial(err)
		}
	}
	return nil
}

// itunesWouldBeWritten re-checks, under the lock and just before Crown, that
// no iTunes member of group gid other than the losers is anything but
// explicitly non-primary (Crown writes every such member).
func (f *duplicateCopiesFixer) itunesWouldBeWritten(store OpsStore, gid string, losers []string) (string, error) {
	group, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		return "", fmt.Errorf("read version group %s: %w", gid, err)
	}
	res := repairs.NewPathResolver()
	for i := range group {
		b := &group[i]
		if contains(losers, b.ID) || b.IsSoftDeleted() || (b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion) {
			continue
		}
		rows, err := store.GetBookFiles(b.ID)
		if err != nil {
			return "", fmt.Errorf("files of %s: %w", b.ID, err)
		}
		exts, err := store.GetExternalIDsForBook(b.ID)
		if err != nil {
			return "", fmt.Errorf("external ids of %s: %w", b.ID, err)
		}
		paths := []string{b.FilePath}
		var ff []fragFile
		for _, r := range rows {
			paths = append(paths, r.FilePath)
			ff = append(ff, fragFile{ID: r.ID, ITunesPID: r.ITunesPersistentID, ITunesPath: r.ITunesPath})
		}
		why, doubt := itunesCopyWhy(res, b.ID, dcStr(b.ITunesPersistentID), paths, ff, exts)
		if doubt {
			return fmt.Sprintf("could not tell whether version-group member %s is an iTunes copy", b.ID), nil
		}
		if why != "" {
			return fmt.Sprintf("iTunes copy %s in version group %s is not explicitly non-primary: the hand-off would write it", b.ID, gid), nil
		}
	}
	return "", nil
}
