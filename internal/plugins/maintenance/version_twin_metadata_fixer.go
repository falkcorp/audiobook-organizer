// file: internal/plugins/maintenance/version_twin_metadata_fixer.go
// version: 1.5.0
// guid: 2f6c8e14-7b3a-4d59-9e02-c4a1b7d36e85
// last-edited: 2026-10-06

package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// versionTwinFixerID is the Repairs-lane id of the version twin fixer.
const versionTwinFixerID = "maintenance.version-twin-metadata"

// Classes of a version twin row.
const (
	// vtClassApplied: a twin had metadata applied; apply carries the same
	// record onto the primary through the normal apply path.
	vtClassApplied = "applied_twin"
	// vtClassCandidates: no twin is applied, but one holds fetched
	// candidates; apply copies them onto the primary (fetch state only).
	vtClassCandidates = "candidates_twin"
)

// Hold (skip) kinds of a version twin row.
const (
	vtHoldResolved         = "primary_resolved"
	vtHoldNoSource         = "no_twin_source"
	vtHoldPrimaryCands     = "primary_has_candidates"
	vtHoldPrimaryAmbiguous = "primary_ambiguous"
	vtHoldNotABSListed     = "primary_not_abs_listed"
	vtHoldITunes           = "primary_itunes"
	vtHoldITunesUnknown    = "primary_itunes_unknown"
	vtHoldLocked           = "primary_locked_fields"
	vtHoldTwinsDisagree    = "twins_disagree"
	vtHoldEdition          = "different_edition"
	vtHoldIdentity         = "identity_mismatch"
	vtHoldUnrecoverable    = "source_candidate_unrecoverable"
	vtHoldASINConflict     = "asin_conflict"
	vtHoldIdentityStale    = "identity_stale"
	vtHoldHashShared       = "hash_shared_outside_group"
	vtHoldGone             = "gone"
	vtHoldReadError        = "error"
	vtHoldIDElsewhere      = "identifier_on_book_outside_group"
	vtHoldEvidenceConflict = "edition_evidence_conflict"
	vtHoldPrimaryVerdict   = "primary_has_fetch_verdict"
	vtHoldOwnerManual      = "record_owner_manual_only"
)

// vtDurationTolerance: twins whose known runtimes differ by more than this
// fraction are different editions (held).
const vtDurationTolerance = 0.05

// vtEvidenceTolerance / vtEvidenceSlackSec: runtimes this close (1%, or 60 s
// for a short book; a provider's runtime is whole minutes) are evidence of
// one edition, which the edition-bound fields (vtEditionFields) require.
const (
	vtEvidenceTolerance = 0.01
	vtEvidenceSlackSec  = 60
)

// vtEditionFields are the apply fields that name one edition, not the work:
// they are copied from a twin's record only with positive evidence that the
// primary is that edition (vtEditionEvidence). The rest of the record
// (description, series, genre, ...) is the work's and is copied on the
// title-and-author identity alone.
var vtEditionFields = map[string]bool{"narrator": true, "asin": true, "isbn": true, "abridged": true, "duration_sec": true}

// versionTwinFixer copies metadata to a version group's primary book from a
// twin in the same group. The 2026-10-05 census found ~859 primaries counted
// "metadata missing" whose version twin was already applied, or held fetched
// candidates: nothing propagated review status or candidates across a group.
//
// A row is one version group (RowID = group id) whose single primary is not
// applied and not marked "no match", and which has a twin that is applied
// (vtClassApplied) or holds cached candidates (vtClassCandidates).
//
//   - applied_twin: the twin's applied record is recovered from its candidate
//     cache (the candidate whose metafetch.CandidateSourceHash equals the
//     twin's MetadataSourceHash) and applied to the primary through
//     metafetch's ApplyMetadataCandidateWithOptions: fill-only, field locks
//     honoured, change history after the commit (so "undo last apply"
//     reverts it), the match stamped. Title and author are NOT in the
//     allowlist: the row requires the primary to hold the record's title and
//     author already (identity), so the apply never rewrites an ABS-listed
//     book's identity, and no file work (rename, tags, cover download) is
//     queued. A twin whose record cannot be recovered is held, never
//     rebuilt from the twin's row.
//   - candidates_twin: the twin's cached candidates are copied onto the
//     primary, re-keyed for it (metafetch.Service.CopyCandidateCache), only
//     when the primary's title and author match the twin's after
//     normalisation. Nothing is applied; the primary joins the review lane.
//
// The fixer NEVER changes which book is primary, and writes no book but the
// primary: the apply's under-lock guard refuses a book that stopped being the
// primary, and the apply runs with SkipHashElection, so the post-commit
// MATCH-4 election (which demotes and merges books outside the survivor's
// group, with no iTunes check of its own) never runs for it. A record a book
// outside the group also carries is held (hash_shared_outside_group) at plan
// time and refused again inside the write.
//
// Edition-bound fields (vtEditionFields: narrator, ASIN, ISBN, abridged,
// runtime) are copied only with positive evidence that the primary is the
// twin's edition (vtEditionEvidence); without it they are dropped from the
// apply and the rest of the record is still applied. A record when a live
// book outside the group carries an identifier the apply would copy (ASIN,
// ISBN-10, ISBN-13) is held (identifier_on_book_outside_group), at plan time
// and again just before the write (vtPreWrite).
//
// ITunesDatabaseOnly: twins are often iTunes copies, which this fixer only
// reads; the framework's iTunes path guard would otherwise hold every such
// group. The primary itself is held when it is iTunes-linked
// (itunesCopyWhy), at plan time and again inside the write (vtWriteGuard),
// so no iTunes-linked book is written. Doctor Who / Big Finish / Torchwood
// stay guarded on every member by the framework, and the twin's record (or
// every copied candidate) is checked here too (record_owner_manual_only).
//
// Undo: an applied_twin apply records its change history under the batch id
// <op id>:<primary id>, sourced to this fixer, writes nothing outside that
// batch (BookRowOnly: no tags, no provenance), and journals one
// undo.ChangeTypeMetadataApply row after the commit, so both "undo last
// apply" and the op revert undo it. A candidate copy journals the primary's
// prior cache state (undo.ChangeTypeMetadataCacheCopy), and the op revert
// removes the copy.
type versionTwinFixer struct{ p *Plugin }

func newVersionTwinFixer(p *Plugin) *versionTwinFixer { return &versionTwinFixer{p: p} }

var _ repairs.Fixer = (*versionTwinFixer)(nil)

func (f *versionTwinFixer) ID() string    { return versionTwinFixerID }
func (f *versionTwinFixer) Title() string { return "Copy metadata from a version twin to the primary" }
func (f *versionTwinFixer) Description() string {
	return "Version groups whose primary book has no metadata applied while another version of the same book does, or " +
		"holds fetched candidates. Apply carries an applied twin's record onto the primary through the normal metadata " +
		"apply (fill-only; no tags; undoable from the operation or with \"undo last apply\"), or copies a twin's " +
		"candidates onto the primary for review (undoable from the operation). The primary is never changed or " +
		"re-elected, and no other book is written. Narrator, ASIN, ISBN, abridgement and runtime are copied only when " +
		"the runtimes or the narrator show the primary is the twin's edition. Held: twins that disagree, a different " +
		"edition (runtime, abridgement or narrator), locked fields, iTunes-linked or not-ABS-listed primaries, a " +
		"different title/author, an ASIN conflict or an ASIN or ISBN a book outside the group carries, a primary whose fetch " +
		"found nothing, and Doctor Who / Big Finish / Torchwood (the books or the record)."
}

// ITunesDatabaseOnly: see the type comment. The fixer writes no file and no
// iTunes id; the primary's own iTunes link is a hold.
func (f *versionTwinFixer) ITunesDatabaseOnly() bool { return true }

// vtDetail is what Apply needs from Replan.
type vtDetail struct {
	class     string
	groupID   string
	primaryID string
	twinID    string
	cand      *metafetch.MetadataCandidate
	// title is the primary's normalised title at plan time; the under-lock
	// guard refuses a primary retitled since.
	title string
	asin  string
	// fields is the apply allowlist (applied_twin): vtApplyFields less the
	// edition-bound fields when there is no edition evidence.
	fields []string
	// hash is the record's metadata_source_hash (applied_twin): the write
	// re-checks that no book outside the group has gained it.
	hash string
	// ids are the identifiers the apply writes (applied_twin, with edition
	// evidence): the write re-checks that no book outside the group has
	// gained one since the plan.
	ids vtIdentifiers
	// resume: this op already wrote the row before a restart and only its
	// journal row is missing (vtResumed); Apply records that row and writes
	// nothing else. jOld / jNew are the journal row's values.
	resume     bool
	jOld, jNew string
}

// vtPlanState is the row's Row.State: what a resumed apply needs from plan
// time. prior is the primary's candidate cache as planned
// (undo.EncodeMetadataCacheOld), the journal row's old value of a candidate
// copy whose journal row a restart cut off.
type vtPlanState struct {
	Prior string `json:"prior,omitempty"`
}

// vtMember is one live member of a group with what the row reads of it.
type vtMember struct {
	core  database.BookCore
	entry *database.MetadataCandidateCache
}

func (m *vtMember) candidates() int {
	if m.entry == nil {
		return 0
	}
	return len(m.entry.Candidates)
}

// vtReaders is everything one row reads.
type vtReaders struct {
	store OpsStore
	cache database.MetadataCacheStore
	svc   VersionTwinMetadataService
	// hash may scan every book row; memHash answers from memdb only and is
	// the one used under a book's write stripe (vtWriteGuard).
	hash    func(string) ([]database.Book, error)
	memHash func(string) ([]database.Book, error)
	ids     func(isbn10, isbn13, asin string) ([]string, bool, error)
	res     *repairs.PathResolver
}

func (f *versionTwinFixer) readers(res *repairs.PathResolver) (vtReaders, error) {
	r := vtReaders{store: f.p.deps.OpsStore(), cache: f.p.deps.MetadataCacheStore(),
		svc: f.p.deps.VersionTwinMetadataService(), hash: f.p.deps.BooksWithMetadataSourceHash,
		memHash: f.p.deps.BooksWithMetadataSourceHashInMemory, ids: f.p.deps.BookIDsWithIdentifiers, res: res}
	if r.store == nil || r.cache == nil {
		return r, fmt.Errorf("database not initialized")
	}
	if r.svc == nil {
		return r, fmt.Errorf("metadata fetch service not initialized")
	}
	return r, nil
}

// vtLive reports whether b is a live member: not trashed, not merged away.
func vtLive(b *database.BookCore) bool {
	return !b.IsSoftDeleted() && !vtMerged(b.MergedIntoBookID)
}

// vtMerged reports whether a merged_into_book_id names a book: nil and a
// blank string are both "not merged", as in metafetch's ASIN backfill and the
// book-shape report. (The store's hash lookup, GetBooksByMetadataSourceHash,
// drops any non-nil pointer; a blank one is therefore missing from its answer
// here and from MATCH-4's alike, so the two never disagree about a cluster.)
func vtMerged(p *string) bool { return p != nil && strings.TrimSpace(*p) != "" }

func vtIsPrimary(b *database.BookCore) bool {
	return b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
}

func vtNoMatch(b *database.BookCore) bool { return metafetch.IsMarkedNoMatch(b.MetadataReviewStatus) }

// Plan lists every version group whose one primary needs metadata and has a
// twin to take it from.
func (f *versionTwinFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	rd, err := f.readers(repairs.NewPathResolver())
	if err != nil {
		return nil, err
	}
	all, err := rd.store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	groups := map[string][]database.BookCore{}
	for i := range all {
		b := all[i]
		gid := dcStr(b.VersionGroupID)
		if gid == "" || !vtLive(&b) {
			continue
		}
		groups[gid] = append(groups[gid], b)
	}
	// The cheap filter: a group of two or more whose primary (or, with no
	// single primary, some member) leaves room for this fixer.
	var gids []string
	for gid, ms := range groups {
		if len(ms) < 2 {
			continue
		}
		var primaries []*database.BookCore
		applied := false
		for i := range ms {
			if vtIsPrimary(&ms[i]) {
				primaries = append(primaries, &ms[i])
			}
			applied = applied || database.MetadataApplied(ms[i].MetadataReviewStatus)
		}
		if len(primaries) != 1 {
			if applied {
				gids = append(gids, gid)
			}
			continue
		}
		if p := primaries[0]; database.MetadataApplied(p.MetadataReviewStatus) || vtNoMatch(p) {
			continue
		}
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	rows := make([]repairs.Row, len(gids))
	keep := make([]bool, len(gids))
	var done atomic.Int64
	// Each worker writes only rows[i] and keep[i] for its own i, and reads
	// only its own group: groups are disjoint, so no two workers share a book.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(gids)), func(_ context.Context, i int) error {
		defer done.Add(1)
		r, k, rerr := f.row(rd, gids[i], groups[gids[i]])
		if rerr != nil {
			r = vtErrorRow(gids[i], groups[gids[i]], rerr)
			k = true
		}
		rows[i], keep[i] = r, k
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Version twins %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([]repairs.Row, 0, len(rows))
	for i := range rows {
		if keep[i] {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// Replan re-reads the group. A group whose primary was applied, re-elected
// or retitled since, or whose twin changed, comes back with a different
// fingerprint (changed_since_plan), except a row THIS op already wrote before
// a restart cut off its journal row (vtResumed): that comes back as planned,
// and Apply records only the journal row.
func (f *versionTwinFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, rep registry.Reporter) (repairs.Row, error) {
	rd, err := f.readers(repairs.NewPathResolver())
	if err != nil {
		return repairs.Row{}, err
	}
	books, err := rd.store.GetBooksByVersionGroup(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read version group %s: %w", planned.RowID, err)
	}
	var ms []database.BookCore
	for i := range books {
		c := books[i].Core()
		if vtLive(&c) && dcStr(c.VersionGroupID) == planned.RowID {
			ms = append(ms, c)
		}
	}
	if len(ms) < 2 {
		r := repairs.Row{RowID: planned.RowID, BookIDs: planned.BookIDs, Title: planned.Title, Risk: repairs.RiskLow}
		r.Skipped, r.SkipReason = vtHoldGone, "the version group no longer has two live members"
		r.Reason = r.SkipReason
		r.Fingerprint = vtFingerprint(r, "gone")
		return r, nil
	}
	r, _, err := f.row(rd, planned.RowID, ms)
	if err != nil || r.Skipped == "" {
		return r, err
	}
	resumed, ok, err := vtResumed(rd, planned, r, registry.ReporterOpID(rep))
	if err != nil {
		return repairs.Row{}, err
	}
	if ok {
		return resumed, nil
	}
	return r, nil
}

// vtResumed recognises a row this op (opID) already wrote before a restart
// that cut it off between the write and its journal row (ledger after write:
// Apply journals after the commit). Without this the re-plan sees the
// primary as resolved (or holding candidates), the row is never applied
// again, and the write escapes the op revert. ok is false for anything else.
//
//   - applied_twin: the primary is applied and its change history holds the
//     batch vtBatchID(opID, primary), not undone. The journal row names that
//     batch.
//   - candidates_twin: the primary's cache row is the twin's copied (same
//     fetch time, ASIN fetched for and candidates; only this fixer copies a
//     cache row), and no other op's un-reverted journal row records that
//     copy (the cache row names no op). The journal row's old value is the primary's cache as
//     planned (vtPlanState), which the plan's fingerprint pinned until the
//     write: the copy's own guard refuses a primary whose cache changed.
//
// The row returned is the planned row (same fingerprint), applicable, whose
// Detail tells Apply to record the journal row only. Writer.Journal skips a
// row already in the op's journal, so this is idempotent.
func vtResumed(rd vtReaders, planned, fresh repairs.Row, opID string) (repairs.Row, bool, error) {
	pid, tid := planned.Current["primary_id"], planned.Current["twin_id"]
	if opID == "" || pid == "" {
		return repairs.Row{}, false, nil
	}
	d := &vtDetail{class: planned.Class, groupID: planned.RowID, primaryID: pid, twinID: tid, resume: true}
	switch {
	case planned.Class == vtClassApplied && fresh.Skipped == vtHoldResolved:
		hr, ok := database.AsCapability[vtHistoryReader](rd.store)
		if !ok {
			return repairs.Row{}, false, fmt.Errorf("this store cannot read book %s's change history", pid)
		}
		hist, err := hr.GetBookChangeHistory(pid, 1<<30)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("read the change history of %s: %w", pid, err)
		}
		batch := vtBatchID(opID, pid)
		found := false
		for i := range hist {
			if hist[i].BatchID != batch {
				continue
			}
			if hist[i].ChangeType == metafetch.ChangeTypeApplyUndo {
				return repairs.Row{}, false, nil // undone since: nothing to revert
			}
			found = true
		}
		if !found {
			return repairs.Row{}, false, nil
		}
		d.jNew = batch
	case planned.Class == vtClassCandidates && fresh.Skipped == vtHoldPrimaryCands && tid != "":
		var st vtPlanState
		if len(planned.State) == 0 || json.Unmarshal(planned.State, &st) != nil || st.Prior == "" {
			return repairs.Row{}, false, nil
		}
		cur, err := rd.cache.GetMetadataCache(pid)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("read the candidate cache of %s: %w", pid, err)
		}
		src, err := rd.cache.GetMetadataCache(tid)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("read the candidate cache of %s: %w", tid, err)
		}
		if !vtIsCopyOf(cur, src) {
			return repairs.Row{}, false, nil
		}
		stamp := undo.MetadataCacheStamp(cur)
		// The cache row carries no op id, so an identical copy could be
		// another op's (two apply ops of one plan). An un-reverted journal row
		// of any op recording this copy means it is that op's, not this one's.
		jr, ok := database.AsCapability[vtBookChangesReader](rd.store)
		if !ok {
			return repairs.Row{}, false, fmt.Errorf("this store cannot read book %s's operation changes", pid)
		}
		changes, err := jr.GetBookChanges(pid)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("read the operation changes of %s: %w", pid, err)
		}
		d.jOld, d.jNew = st.Prior, stamp
		for _, c := range changes {
			if c == nil || c.RevertedAt != nil || c.ChangeType != undo.ChangeTypeMetadataCacheCopy || c.NewValue != stamp {
				continue
			}
			if c.OperationID != opID {
				return repairs.Row{}, false, nil
			}
			// This op journaled it already (only the checkpoint was lost):
			// reuse that row's values, so Writer.Journal finds it and adds
			// nothing, whatever old value it recorded.
			d.jOld, d.jNew = c.OldValue, c.NewValue
		}
	default:
		return repairs.Row{}, false, nil
	}
	out := planned
	out.Skipped, out.SkipReason = "", ""
	out.Reason = "this operation wrote the row before a restart and did not record it; only its journal row is recorded now"
	out.Detail = d
	return out, true, nil
}

// vtBookChangesReader is the store's per-book operation-journal read
// (database.OperationChangeStore).
type vtBookChangesReader interface {
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

// vtHistoryReader is the store's change-history read (database.Store has
// it; OpsStore does not list it, so it is asserted for).
type vtHistoryReader interface {
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
}

// vtIsCopyOf reports whether cur is a copy of src's candidates
// (metafetch.Service.CopyCandidateCache keeps the fetch time, the ASIN
// fetched for and the candidates as they are).
func vtIsCopyOf(cur, src *database.MetadataCandidateCache) bool {
	if cur == nil || src == nil || len(cur.Candidates) == 0 || len(cur.Candidates) != len(src.Candidates) ||
		!cur.FetchedAt.Equal(src.FetchedAt) || cur.FetchedForASIN != src.FetchedForASIN {
		return false
	}
	for i := range cur.Candidates {
		if !bytes.Equal(cur.Candidates[i], src.Candidates[i]) {
			return false
		}
	}
	return true
}

func vtErrorRow(gid string, ms []database.BookCore, err error) repairs.Row {
	ids := make([]string, 0, len(ms))
	title := ""
	for i := range ms {
		ids = append(ids, ms[i].ID)
		if vtIsPrimary(&ms[i]) {
			title = ms[i].Title
		}
	}
	sort.Strings(ids)
	r := repairs.Row{RowID: gid, BookIDs: ids, Title: title, Risk: repairs.RiskReview,
		Skipped: vtHoldReadError, SkipReason: err.Error(), Reason: err.Error()}
	r.Fingerprint = vtFingerprint(r, "read-error")
	return r
}

// vtFingerprint hashes the row's decision inputs.
func vtFingerprint(r repairs.Row, extra string) string {
	sum := sha256.Sum256([]byte(r.RowID + "\n" + r.Class + "\n" + r.Skipped + "\n" + extra))
	return hex.EncodeToString(sum[:])[:32]
}

// vtBuild accumulates one row: what it shows and what its fingerprint hashes.
type vtBuild struct {
	r  repairs.Row
	fp []string
}

func (b *vtBuild) note(parts ...string) { b.fp = append(b.fp, parts...) }

func (b *vtBuild) hold(kind, why string) repairs.Row {
	b.r.Skipped, b.r.SkipReason, b.r.Reason = kind, why, why
	b.r.Fingerprint = vtFingerprint(b.r, strings.Join(b.fp, "\x00"))
	return b.r
}

// row decides one group. keep is false for a group that is not a row (the
// primary needs nothing, or no twin has anything to give); the returned row
// is then the skip a Replan reports.
func (f *versionTwinFixer) row(rd vtReaders, gid string, members []database.BookCore) (repairs.Row, bool, error) {
	ms := make([]vtMember, len(members))
	for i := range members {
		ms[i] = vtMember{core: members[i]}
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].core.ID < ms[j].core.ID })
	b := &vtBuild{r: repairs.Row{RowID: gid, Risk: repairs.RiskReview}}
	var primaries []int
	for i := range ms {
		c := &ms[i].core
		b.r.BookIDs = append(b.r.BookIDs, c.ID)
		b.note(c.ID, strconv.FormatBool(vtIsPrimary(c)), dcStr(c.MetadataReviewStatus), c.Title, dcStr(c.ASIN),
			dcStr(c.MetadataSourceHash), dcStr(c.LibraryState), vtIntStr(c.Duration), dcStr(c.Narrator), vtBoolStr(c.Abridged))
		if vtIsPrimary(c) {
			primaries = append(primaries, i)
		}
	}
	if len(primaries) != 1 {
		b.r.Title = ms[0].core.Title
		for i := range ms {
			b.r.Members = append(b.r.Members, repairs.RowMember{BookID: ms[i].core.ID, Title: ms[i].core.Title, Role: "member"})
		}
		applied := false
		for i := range ms {
			applied = applied || database.MetadataApplied(ms[i].core.MetadataReviewStatus)
		}
		r := b.hold(vtHoldPrimaryAmbiguous, fmt.Sprintf("the group has %d primary books, not one; "+
			"the version-group-primary repair settles that, this fixer never elects one", len(primaries)))
		return r, applied, nil
	}
	pi := primaries[0]
	p := &ms[pi]
	b.r.Title = p.core.Title
	if database.MetadataApplied(p.core.MetadataReviewStatus) || vtNoMatch(&p.core) {
		return b.hold(vtHoldResolved, "the primary's metadata is applied, or it is marked \"no match\""), false, nil
	}
	for i := range ms {
		entry, err := rd.cache.GetMetadataCache(ms[i].core.ID)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("read the candidate cache of %s: %w", ms[i].core.ID, err)
		}
		ms[i].entry = entry
		fetched := ""
		if entry != nil {
			fetched = entry.FetchedAt.UTC().Format(time.RFC3339Nano)
		}
		b.note(strconv.Itoa(ms[i].candidates()), fetched)
	}

	var applied, cands []int
	for i := range ms {
		if i == pi || vtNoMatch(&ms[i].core) {
			continue
		}
		if database.MetadataApplied(ms[i].core.MetadataReviewStatus) {
			applied = append(applied, i)
		} else if ms[i].candidates() > 0 {
			cands = append(cands, i)
		}
	}
	switch {
	case len(applied) > 0:
		b.r.Class = vtClassApplied
	case len(cands) > 0:
		b.r.Class = vtClassCandidates
		if p.candidates() > 0 {
			return b.hold(vtHoldPrimaryCands, "the primary already holds fetched candidates of its own"), false, nil
		}
		if vtCacheVerdict(p.entry) {
			return b.hold(vtHoldPrimaryVerdict, "the primary's own candidate fetch ran and found nothing (its cache row "+
				"holds that verdict); copying the twin's candidates over it would erase it"), true, nil
		}
	default:
		return b.hold(vtHoldNoSource, "no twin is applied or holds candidates"), false, nil
	}

	authorsOf := map[int][]string{}
	readAuthors := func(i int) ([]string, error) {
		if a, ok := authorsOf[i]; ok {
			return a, nil
		}
		book := ms[i].core.ToBook()
		a, err := repairs.BookAuthorNames(rd.store, &book)
		if err != nil {
			return nil, fmt.Errorf("read authors of %s: %w", ms[i].core.ID, err)
		}
		authorsOf[i] = a
		return a, nil
	}
	pAuthors, err := readAuthors(pi)
	if err != nil {
		return repairs.Row{}, false, err
	}

	// Choose the twin.
	twin := -1
	var cand *metafetch.MetadataCandidate
	if b.r.Class == vtClassApplied {
		for _, i := range applied {
			if c := vtAppliedCandidate(&ms[i]); c != nil {
				twin, cand = i, c
				break
			}
		}
		if twin < 0 {
			twin = applied[0]
		}
	} else {
		twin = cands[0]
		for _, i := range cands[1:] {
			if ms[i].entry.FetchedAt.After(ms[twin].entry.FetchedAt) {
				twin = i
			}
		}
	}
	t := &ms[twin]
	tAuthors, err := readAuthors(twin)
	if err != nil {
		return repairs.Row{}, false, err
	}
	b.note("twin", t.core.ID)
	f.display(b, ms, pi, twin, pAuthors, tAuthors, cand)

	// Holds on the primary itself.
	if !database.ABSLibraryFilter().MatchesCore(&p.core) {
		return b.hold(vtHoldNotABSListed, "the primary is not listed by ABS (it must be primary, organized and not quarantined)"), true, nil
	}
	why, doubt, _, err := vtITunesWhy(rd, &p.core)
	if err != nil {
		return repairs.Row{}, false, err
	}
	b.note(why, strconv.FormatBool(doubt))
	switch {
	case why != "":
		return b.hold(vtHoldITunes, "the primary is iTunes-linked ("+why+"); iTunes-linked books are never written"), true, nil
	case doubt:
		return b.hold(vtHoldITunesUnknown, "could not tell whether the primary is iTunes-linked"), true, nil
	}
	locks, err := database.LoadFieldLocks(rd.store, p.core.ID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read field locks of %s: %w", p.core.ID, err)
	}
	if locks.Any() {
		// Named by who locked each field: a person's lock, or a repair's bare
		// lock (repairs.Writer.LockFields), so the owner can tell the two
		// apart when deciding whether repair locks should hold this fixer.
		keys := make([]string, 0)
		for k := range locks.Set() {
			who := "person"
			if locks.RepairLocked(k) {
				who = "repair"
			}
			keys = append(keys, k+" ("+who+")")
		}
		sort.Strings(keys)
		b.note("locks", strings.Join(keys, ","))
		return b.hold(vtHoldLocked, "the primary has locked fields: "+strings.Join(keys, ", ")+
			"; a person or a repair chose those values"), true, nil
	}

	// The twins must agree with each other.
	disagree := applied
	if b.r.Class == vtClassCandidates {
		disagree = cands
	}
	if why, err := vtTwinsDisagree(ms, disagree, readAuthors); err != nil {
		return repairs.Row{}, false, err
	} else if why != "" {
		return b.hold(vtHoldTwinsDisagree, why), true, nil
	}

	// Same edition.
	pBook := p.core.ToBook()
	tBook := t.core.ToBook()
	pSec, tSec, err := vtRuntimes(rd, &pBook, &tBook)
	if err != nil {
		return repairs.Row{}, false, err
	}
	b.note("runtime", strconv.Itoa(pSec), strconv.Itoa(tSec))
	if why := vtEditionDiffers(&p.core, &t.core, pSec, tSec); why != "" {
		return b.hold(vtHoldEdition, why), true, nil
	}
	if b.r.Class == vtClassApplied {
		if cand == nil {
			return b.hold(vtHoldUnrecoverable, "no cached candidate of twin "+t.core.ID+" carries its applied record "+
				"(metadata_source_hash); its candidates were deleted by an older apply. Refetch the twin, or apply by hand"), true, nil
		}
		hash := metafetch.CandidateSourceHash(*cand)
		b.note("cand", hash)
		if why := vtRecordManualOnly(cand); why != "" {
			return b.hold(vtHoldOwnerManual, "the twin's record "+why+"; Doctor Who / Big Finish / Torchwood are "+
				"applied by hand"), true, nil
		}
		if why := vtIdentity(pBook.Title, pAuthors, cand.Title, tAuthors); why != "" {
			return b.hold(vtHoldIdentity, why), true, nil
		}
		if res := applygate.CheckASIN(&pBook, cand); res.Outcome == applygate.OutcomeBlock {
			return b.hold(vtHoldASINConflict, "the primary's ASIN conflicts with the twin's record: "+res.Detail), true, nil
		}
		if serr := metafetch.CandidateASINStale(t.entry, &tBook, cand); serr != nil {
			return b.hold(vtHoldIdentityStale, serr.Error()), true, nil
		}
		outside, err := vtOutsideGroup(rd, gid, hash)
		if err != nil {
			return repairs.Row{}, false, err
		}
		b.note("outside", strings.Join(outside, ","))
		if len(outside) > 0 {
			return b.hold(vtHoldHashShared, "the twin's record is also carried by "+strings.Join(outside, ", ")+
				" outside this version group; applying it would run a cross-group duplicate election"), true, nil
		}
		evidence, conflict := vtEditionEvidence(&p.core, cand, pSec, tSec)
		b.note("evidence", evidence, conflict)
		if conflict != "" {
			return b.hold(vtHoldEvidenceConflict, conflict), true, nil
		}
		// Only the identifiers the apply will write are checked against
		// books outside the group: with edition evidence, each the primary
		// lacks and the record has (fill-only).
		var wIDs vtIdentifiers
		if evidence != "" {
			wIDs = vtWrittenIdentifiers(&p.core, cand)
		}
		elsewhere, indexed, err := vtIdentifiersOutsideGroup(rd, gid, wIDs)
		if err != nil {
			return repairs.Row{}, false, err
		}
		idsNote := func(elsewhere []string, indexed bool) []string {
			return []string{"ids-elsewhere", wIDs.String(), strings.Join(elsewhere, ","), strconv.FormatBool(indexed)}
		}
		// What this row's fingerprint would be once the index is built and
		// finds the identifiers on no book outside the group: the applicable
		// row's (Skipped empty, the cleared note), from the inputs so far.
		cleared := repairs.Row{RowID: b.r.RowID, Class: b.r.Class}
		retryFP := vtFingerprint(cleared, strings.Join(append(append([]string(nil), b.fp...), idsNote(nil, true)...), "\x00"))
		b.note(idsNote(elsewhere, indexed)...)
		switch {
		case !indexed:
			// Transient: the index gets built without the row changing. The
			// engine reports the re-plan retry_later only when RetryFingerprint
			// equals the planned fingerprint, i.e. every other input is as
			// planned; a row that also changed is changed_since_plan.
			b.r.RetryLater, b.r.RetryFingerprint = true, retryFP
			return b.hold(vtHoldIDElsewhere, "the ISBN/ASIN index is not built yet (the isbn-index-build operation sets "+
				"it), so it cannot be told whether a book outside this group carries the record's "+wIDs.String()+
				", which the apply would copy"), true, nil
		case len(elsewhere) > 0:
			return b.hold(vtHoldIDElsewhere, "the record's "+wIDs.String()+" is carried by "+strings.Join(elsewhere, ", ")+
				" outside this version group; the record may be that book's"), true, nil
		}
		fields := vtApplyFields(evidence != "")
		b.r.Proposed["primary_review_status"] = "matched"
		// Fill-only: the record fills the primary's empty fields; the
		// edition-bound ones only with evidence.
		if evidence != "" {
			if dcStr(p.core.Narrator) == "" && cand.Narrator != "" {
				b.r.Proposed["primary_narrator"] = cand.Narrator
			}
			if dcStr(p.core.ASIN) == "" && cand.ASIN != "" {
				b.r.Proposed["primary_asin"] = cand.ASIN
			}
			b.r.Evidence = append(b.r.Evidence, "same edition: "+evidence)
		} else {
			b.r.Evidence = append(b.r.Evidence, "no evidence the primary is the twin's edition (runtimes not "+
				"known within max(1%, 60 s), or narrator not the record's): narrator, ASIN, ISBN, "+
				"abridgement and runtime are not copied")
		}
		b.r.Reason = fmt.Sprintf("twin %s had %s metadata applied (record %s); the primary is the same book (same "+
			"title and author) and has none", t.core.ID, cand.Source, vtRecord(cand))
		b.r.Fingerprint = vtFingerprint(b.r, strings.Join(b.fp, "\x00"))
		b.r.Detail = &vtDetail{class: b.r.Class, groupID: gid, primaryID: p.core.ID, twinID: t.core.ID, cand: cand,
			title: util.NormalizeTitle(p.core.Title), asin: dcStr(p.core.ASIN), fields: fields, hash: hash, ids: wIDs}
		return b.r, true, nil
	} else {
		if why := vtIdentity(pBook.Title, pAuthors, tBook.Title, tAuthors); why != "" {
			return b.hold(vtHoldIdentity, why), true, nil
		}
		pASIN, tASIN := dcStr(p.core.ASIN), dcStr(t.core.ASIN)
		if tASIN == "" {
			tASIN = strings.TrimSpace(t.entry.FetchedForASIN)
		}
		if pASIN != "" && tASIN != "" && !strings.EqualFold(pASIN, tASIN) {
			return b.hold(vtHoldASINConflict, fmt.Sprintf("the primary carries ASIN %s, the twin's candidates were fetched for %s", pASIN, tASIN)), true, nil
		}
		if verr := rd.svc.ValidateCachedIdentityForBook(t.entry, &tBook, tAuthors); verr != nil {
			return b.hold(vtHoldIdentityStale, "the twin's candidates were fetched for a title or author it no longer has: "+verr.Error()), true, nil
		}
		if why := vtCandidatesManualOnly(t.entry); why != "" {
			return b.hold(vtHoldOwnerManual, "a candidate of the twin "+why+"; Doctor Who / Big Finish / Torchwood are "+
				"applied by hand"), true, nil
		}
		prior, err := undo.EncodeMetadataCacheOld(p.entry)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("encode the candidate cache of %s: %w", p.core.ID, err)
		}
		st, err := json.Marshal(vtPlanState{Prior: prior})
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("encode the plan state of %s: %w", gid, err)
		}
		b.r.State = st
		b.r.Proposed["primary_candidates"] = strconv.Itoa(t.candidates())
		b.r.Reason = fmt.Sprintf("twin %s holds %d fetched candidates for the same title and author; the primary has "+
			"none. Copying them puts the primary in the review lane (nothing is applied)", t.core.ID, t.candidates())
	}
	b.r.Fingerprint = vtFingerprint(b.r, strings.Join(b.fp, "\x00"))
	b.r.Detail = &vtDetail{class: b.r.Class, groupID: gid, primaryID: p.core.ID, twinID: t.core.ID, cand: cand,
		title: util.NormalizeTitle(p.core.Title), asin: dcStr(p.core.ASIN)}
	return b.r, true, nil
}

// display fills the row's Members, Current and Evidence: primary and twin,
// each with id, title, author, duration, narrator, ASIN and review status.
func (f *versionTwinFixer) display(b *vtBuild, ms []vtMember, pi, ti int, pAuthors, tAuthors []string, cand *metafetch.MetadataCandidate) {
	for i := range ms {
		role := "version"
		switch i {
		case pi:
			role = "primary"
		case ti:
			role = "twin"
		}
		b.r.Members = append(b.r.Members, repairs.RowMember{BookID: ms[i].core.ID, Title: ms[i].core.Title, Role: role})
	}
	b.r.Author = strings.Join(pAuthors, ", ")
	b.r.Current = map[string]string{}
	b.r.Proposed = map[string]string{}
	put := func(prefix string, c *database.BookCore, authors []string, cands int) {
		b.r.Current[prefix+"_id"] = c.ID
		b.r.Current[prefix+"_title"] = c.Title
		b.r.Current[prefix+"_author"] = strings.Join(authors, ", ")
		b.r.Current[prefix+"_duration"] = vtIntStr(c.Duration)
		b.r.Current[prefix+"_narrator"] = dcStr(c.Narrator)
		b.r.Current[prefix+"_asin"] = dcStr(c.ASIN)
		b.r.Current[prefix+"_review_status"] = dcStr(c.MetadataReviewStatus)
		b.r.Current[prefix+"_candidates"] = strconv.Itoa(cands)
	}
	put("primary", &ms[pi].core, pAuthors, ms[pi].candidates())
	put("twin", &ms[ti].core, tAuthors, ms[ti].candidates())
	// The lane renders every key as current -> proposed and reads a key
	// missing from Proposed as cleared, so every display key is proposed
	// unchanged; the class's own changes overwrite theirs below (row).
	for k, v := range b.r.Current {
		b.r.Proposed[k] = v
	}
	b.r.Evidence = []string{"same version group", "twin review status: " + dcStr(ms[ti].core.MetadataReviewStatus)}
	if cand != nil {
		b.r.Evidence = append(b.r.Evidence, "twin's applied record recovered from its candidate cache: "+vtRecord(cand))
	}
}

// vtRecord names a candidate's record for the owner.
func vtRecord(c *metafetch.MetadataCandidate) string {
	id := c.ASIN
	if id == "" {
		id = c.ISBN
	}
	return fmt.Sprintf("%s %q by %s [%s]", c.Source, c.Title, c.Author, id)
}

// vtAppliedCandidate is the twin's applied candidate: the cached candidate
// whose source hash is the twin's MetadataSourceHash. nil when none is.
func vtAppliedCandidate(m *vtMember) *metafetch.MetadataCandidate {
	want := dcStr(m.core.MetadataSourceHash)
	if want == "" || m.entry == nil {
		return nil
	}
	for _, raw := range m.entry.Candidates {
		var c metafetch.MetadataCandidate
		if err := json.Unmarshal(raw, &c); err != nil {
			continue
		}
		if metafetch.CandidateSourceHash(c) == want {
			return &c
		}
	}
	return nil
}

// vtITunesInputs reads what the iTunes predicate judges the primary by: its
// paths (the book's and every book_file row's), its file rows' iTunes ids and
// paths, and its external ids. Point and prefix reads only.
func vtITunesInputs(rd vtReaders, c *database.BookCore) ([]string, []fragFile, []database.ExternalIDMapping, error) {
	files, err := rd.store.GetBookFiles(c.ID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read files of %s: %w", c.ID, err)
	}
	exts, err := rd.store.GetExternalIDsForBook(c.ID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read external ids of %s: %w", c.ID, err)
	}
	paths := []string{c.FilePath}
	ff := make([]fragFile, 0, len(files))
	for i := range files {
		paths = append(paths, files[i].FilePath)
		ff = append(ff, fragFile{ID: files[i].ID, ITunesPID: files[i].ITunesPersistentID, ITunesPath: files[i].ITunesPath})
	}
	return paths, ff, exts, nil
}

// vtITunesWhy runs the shared iTunes predicate on the primary, paths
// resolved on disk (symlinks followed). It returns the paths it cleared.
// Never under a book's write stripe: the resolver touches the disk.
func vtITunesWhy(rd vtReaders, c *database.BookCore) (string, bool, []string, error) {
	paths, ff, exts, err := vtITunesInputs(rd, c)
	if err != nil {
		return "", false, nil, err
	}
	why, doubt := itunesCopyWhy(rd.res, c.ID, dcStr(c.ITunesPersistentID), paths, ff, exts)
	return why, doubt, paths, nil
}

// vtTwinsDisagree names why the given twins are not one record: two
// different ASINs, or titles or authors that differ after normalisation.
func vtTwinsDisagree(ms []vtMember, idx []int, authors func(int) ([]string, error)) (string, error) {
	if len(idx) < 2 {
		return "", nil
	}
	first := idx[0]
	fa, err := authors(first)
	if err != nil {
		return "", err
	}
	for _, i := range idx[1:] {
		a, b := &ms[first].core, &ms[i].core
		if x, y := dcStr(a.ASIN), dcStr(b.ASIN); x != "" && y != "" && !strings.EqualFold(x, y) {
			return fmt.Sprintf("twins %s and %s carry different ASINs (%s, %s)", a.ID, b.ID, x, y), nil
		}
		ia, err := authors(i)
		if err != nil {
			return "", err
		}
		if why := vtIdentity(a.Title, fa, b.Title, ia); why != "" {
			return fmt.Sprintf("twins %s and %s disagree: %s", a.ID, b.ID, why), nil
		}
	}
	return "", nil
}

// vtEditionDiffers names why the twin is a different edition of the
// primary: runtimes more than 5% apart (both known, pSec/tSec from
// vtRuntimes), abridged against unabridged, or different narrators (both
// known).
func vtEditionDiffers(p, t *database.BookCore, pSec, tSec int) string {
	if pSec > 0 && tSec > 0 {
		a, b := float64(pSec), float64(tSec)
		if math.Abs(a-b)/math.Max(a, b) > vtDurationTolerance {
			return fmt.Sprintf("runtimes differ by more than 5%% (primary %ds, twin %ds)", pSec, tSec)
		}
	}
	if p.Abridged != nil && t.Abridged != nil && *p.Abridged != *t.Abridged {
		return fmt.Sprintf("abridged %v on the primary, %v on the twin", *p.Abridged, *t.Abridged)
	}
	pn, tn := fbNorm(dcStr(p.Narrator)), fbNorm(dcStr(t.Narrator))
	if pn != "" && tn != "" && !strings.Contains(pn, tn) && !strings.Contains(tn, pn) {
		return fmt.Sprintf("different narrators (primary %q, twin %q)", dcStr(p.Narrator), dcStr(t.Narrator))
	}
	return ""
}

// vtIdentity names why two books are not the same search identity: titles
// that differ after util.NormalizeTitle (the normalisation the apply's match
// stamp compares by), or author lists with no name in common after
// normalisation (an empty list matches nothing). "" when they match.
func vtIdentity(aTitle string, aAuthors []string, bTitle string, bAuthors []string) string {
	if util.NormalizeTitle(aTitle) != util.NormalizeTitle(bTitle) || util.NormalizeTitle(aTitle) == "" {
		return fmt.Sprintf("titles differ (%q, %q)", aTitle, bTitle)
	}
	seen := map[string]bool{}
	for _, a := range aAuthors {
		if n := fbNorm(a); n != "" {
			seen[n] = true
		}
	}
	for _, a := range bAuthors {
		if seen[fbNorm(a)] {
			return ""
		}
	}
	return fmt.Sprintf("authors differ (%q, %q)", strings.Join(aAuthors, ", "), strings.Join(bAuthors, ", "))
}

// vtOutsideGroup lists the live books outside group gid carrying hash.
func vtOutsideGroup(rd vtReaders, gid, hash string) ([]string, error) {
	books, err := rd.hash(hash)
	if err != nil {
		return nil, fmt.Errorf("read books carrying record hash %s: %w", hash, err)
	}
	var out []string
	for i := range books {
		c := books[i].Core()
		if vtLive(&c) && dcStr(c.VersionGroupID) != gid {
			out = append(out, c.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func vtIntStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func vtBoolStr(p *bool) string {
	if p == nil {
		return ""
	}
	return strconv.FormatBool(*p)
}

// vtApplyFields is every apply field except title and author (the row
// requires the primary to hold the record's title and author already, so the
// apply never rewrites them), and, without edition evidence, except the
// edition-bound fields (vtEditionFields).
func vtApplyFields(sameEdition bool) []string {
	var out []string
	for _, k := range metafetch.ApplyFieldKeys() {
		if k == "title" || k == "author" || (!sameEdition && vtEditionFields[k]) {
			continue
		}
		out = append(out, k)
	}
	return out
}

// vtRuntimes is the known runtime, in seconds, of the primary and the twin
// (0 when not known): database.LoadBookRuntime's KnownSeconds, the reading
// every runtime comparison must use (Book.Duration is a display aggregate).
func vtRuntimes(rd vtReaders, p, t *database.Book) (int, int, error) {
	var out [2]int
	for i, b := range []*database.Book{p, t} {
		rt, err := database.LoadBookRuntime(rd.store, b)
		if err != nil {
			return 0, 0, fmt.Errorf("read the runtime of %s: %w", b.ID, err)
		}
		out[i], _ = rt.KnownSeconds()
	}
	return out[0], out[1], nil
}

// vtRuntimesAgree: both known and within vtEvidenceTolerance (or
// vtEvidenceSlackSec).
func vtRuntimesAgree(a, b int) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= max(int(float64(max(a, b))*vtEvidenceTolerance), vtEvidenceSlackSec)
}

// vtEvidenceWithin is how close two runtimes must be to agree
// (vtRuntimesAgree), in words.
const vtEvidenceWithin = "max(1%, 60 s)"

// vtEditionEvidence names the positive evidence that the primary is the
// edition the twin's record describes ("" when there is none), or, in
// conflict, why the evidence contradicts itself (the row is then held).
//
// The runtimes are the primary's and the twin's known runtimes (pSec, tSec)
// and the record's own (cand.DurationSec), each 0 when not known. Every pair
// of known runtimes is compared (vtRuntimesAgree: within max(1%, 60 s)); they
// "disagree" when any pair does not.
//   - runtimes: the primary's is known, at least one of the twin's and the
//     record's is known, and no pair disagrees. That is evidence. (Twin and
//     record are compared too: a twin and a record that disagree with each
//     other are not evidence even when each is close to the primary.)
//   - the known runtimes disagree (the 5% hold, vtEditionDiffers, already
//     caught the wider primary-twin gaps): no runtime evidence, and narrator
//     equality cannot stand in for it; when the primary carries the record's
//     narrator anyway the two signals contradict each other (one narrator
//     recorded two editions), so that is a conflict, not evidence. Without
//     a narrator match the row stays applicable without the edition fields.
//   - narrator: the primary already carries the record's narrator. Evidence
//     unless the known runtimes disagree (the conflict above); an unknown
//     runtime never takes it away.
//
// An empty narrator or an unknown runtime is never evidence.
func vtEditionEvidence(p *database.BookCore, cand *metafetch.MetadataCandidate, pSec, tSec int) (evidence, conflict string) {
	rec := max(cand.DurationSec, 0)
	var known []int
	for _, v := range []int{pSec, tSec, rec} {
		if v > 0 {
			known = append(known, v)
		}
	}
	disagree := false
	for i := range known {
		for j := i + 1; j < len(known); j++ {
			disagree = disagree || !vtRuntimesAgree(known[i], known[j])
		}
	}
	if !disagree && pSec > 0 && len(known) >= 2 {
		return fmt.Sprintf("runtimes agree within %s (primary %s, twin %s, record %s)", vtEvidenceWithin,
			vtSecStr(pSec), vtSecStr(tSec), vtSecStr(rec)), ""
	}
	pn := fbNorm(dcStr(p.Narrator))
	narrator := pn != "" && pn == fbNorm(cand.Narrator)
	switch {
	case disagree && narrator:
		return "", fmt.Sprintf("the primary carries the record's narrator %q, but the known runtimes are more than %s "+
			"apart (primary %s, twin %s, record %s): one narrator may have recorded two editions, so the narrator is not "+
			"evidence of this one and the record's ASIN could be another edition's", cand.Narrator, vtEvidenceWithin,
			vtSecStr(pSec), vtSecStr(tSec), vtSecStr(rec))
	case narrator && !disagree:
		return fmt.Sprintf("the primary already carries the record's narrator %q (no two known runtimes more than %s "+
			"apart)", cand.Narrator, vtEvidenceWithin), ""
	}
	return "", ""
}

func vtSecStr(sec int) string {
	if sec <= 0 {
		return "unknown"
	}
	return strconv.Itoa(sec) + "s"
}

// vtIdentifiers are the ISBN-10, ISBN-13 and ASIN an apply writes.
type vtIdentifiers struct{ isbn10, isbn13, asin string }

func (v vtIdentifiers) empty() bool { return v.isbn10 == "" && v.isbn13 == "" && v.asin == "" }

func (v vtIdentifiers) String() string {
	var parts []string
	for _, x := range []struct{ k, v string }{{"ASIN", v.asin}, {"ISBN-10", v.isbn10}, {"ISBN-13", v.isbn13}} {
		if x.v != "" {
			parts = append(parts, x.k+" "+x.v)
		}
	}
	return strings.Join(parts, ", ")
}

// vtWrittenIdentifiers is what a fill-only apply of cand writes to the
// primary: each identifier the primary lacks and the record has, as the
// apply body maps them (metafetch.CandidateISBNs).
func vtWrittenIdentifiers(p *database.BookCore, cand *metafetch.MetadataCandidate) vtIdentifiers {
	var out vtIdentifiers
	i10, i13 := metafetch.CandidateISBNs(*cand)
	if strings.TrimSpace(dcStr(p.ISBN10)) == "" {
		out.isbn10 = strings.TrimSpace(i10)
	}
	if strings.TrimSpace(dcStr(p.ISBN13)) == "" {
		out.isbn13 = strings.TrimSpace(i13)
	}
	if strings.TrimSpace(dcStr(p.ASIN)) == "" {
		out.asin = strings.TrimSpace(cand.ASIN)
	}
	return out
}

// vtCaseVariants is v as given, upper-cased and lower-cased, without
// repeats. The ISBN/ASIN index stores values exactly as written (nothing
// on the write path upper-cases an ASIN), while every comparison of two
// ASINs is case-insensitive (strings.EqualFold, authority.NormalizeASIN), so
// a lookup asks for each spelling. A mixed-case stored value other than
// these three is still missed by the index; the per-book re-check below is
// case-insensitive.
func vtCaseVariants(v string) []string {
	if v == "" {
		return nil
	}
	out := []string{v}
	for _, x := range []string{strings.ToUpper(v), strings.ToLower(v)} {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// vtIdentifiersOutsideGroup lists the live books outside group gid carrying
// one of ids. indexed is false when the ISBN/ASIN index is not built (the
// answer then proves nothing). Nothing to check: indexed, no books.
func vtIdentifiersOutsideGroup(rd vtReaders, gid string, ids vtIdentifiers) ([]string, bool, error) {
	if ids.empty() {
		return nil, true, nil
	}
	found := map[string]bool{}
	ask := func(isbn10, isbn13, asin string) (bool, error) {
		got, indexed, err := rd.ids(isbn10, isbn13, asin)
		if err != nil {
			return false, fmt.Errorf("read books carrying %s: %w", ids.String(), err)
		}
		for _, id := range got {
			found[id] = true
		}
		return indexed, nil
	}
	for _, v := range vtCaseVariants(ids.asin) {
		if indexed, err := ask("", "", v); err != nil || !indexed {
			return nil, false, err
		}
	}
	for _, v := range vtCaseVariants(ids.isbn10) {
		if indexed, err := ask(v, "", ""); err != nil || !indexed {
			return nil, false, err
		}
	}
	for _, v := range vtCaseVariants(ids.isbn13) {
		if indexed, err := ask("", v, ""); err != nil || !indexed {
			return nil, false, err
		}
	}
	match := func(stored *string, want string) bool {
		return want != "" && strings.EqualFold(strings.TrimSpace(dcStr(stored)), want)
	}
	var out []string
	for id := range found {
		b, err := rd.store.GetBookByID(id)
		if err != nil {
			return nil, false, fmt.Errorf("read book %s (%s): %w", id, ids.String(), err)
		}
		if b == nil {
			continue
		}
		c := b.Core()
		// The index can lag a cleared or changed value; the row decides.
		if vtLive(&c) && dcStr(c.VersionGroupID) != gid &&
			(match(c.ASIN, ids.asin) || match(c.ISBN10, ids.isbn10) || match(c.ISBN13, ids.isbn13)) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, true, nil
}

// vtCacheVerdict reports whether a cache row with no candidates records a
// fetch outcome (a search that ran and found nothing): a search fingerprint,
// an empty-fetch time or a provider's empty answer.
func vtCacheVerdict(e *database.MetadataCandidateCache) bool {
	return e != nil && len(e.Candidates) == 0 &&
		(e.SearchFingerprint != "" || e.LastEmptyFetchAt != nil || len(e.EmptyAnswers) > 0)
}

// vtRecordManualOnly names how a record marks Doctor Who / Big Finish /
// Torchwood (applygate.IsOwnerManualOnly on its title, subtitle, series and
// publisher), "" when it does not.
func vtRecordManualOnly(c *metafetch.MetadataCandidate) string {
	for _, v := range []struct{ field, value string }{{"title", c.Title}, {"subtitle", c.Subtitle},
		{"series", c.Series}, {"secondary series", c.SeriesSecondary}, {"publisher", c.Publisher}} {
		if v.value != "" && applygate.IsOwnerManualOnly(v.value, "") {
			return fmt.Sprintf("has %s %q", v.field, v.value)
		}
	}
	return ""
}

// vtCandidatesManualOnly runs vtRecordManualOnly over every candidate a copy
// would carry. A candidate that does not decode cannot be cleared, so it
// holds too.
func vtCandidatesManualOnly(e *database.MetadataCandidateCache) string {
	if e == nil {
		return ""
	}
	for i, raw := range e.Candidates {
		var c metafetch.MetadataCandidate
		if err := json.Unmarshal(raw, &c); err != nil {
			return fmt.Sprintf("(#%d) could not be read to check it: %v", i+1, err)
		}
		if why := vtRecordManualOnly(&c); why != "" {
			return fmt.Sprintf("(#%d) %s", i+1, why)
		}
	}
	return ""
}

// errVTChanged wraps repairs.ErrChangedSincePlan for the under-lock guard.
func errVTChanged(format string, args ...any) error {
	return fmt.Errorf("%w: %s", repairs.ErrChangedSincePlan, fmt.Sprintf(format, args...))
}

// vtStillNeeds is the under-lock check on the primary's row: still this
// group's live primary, still unapplied and not "no match", same title and
// ASIN as planned.
func vtStillNeeds(b *database.Book, d *vtDetail) error {
	switch {
	case b == nil || b.IsSoftDeleted():
		return errVTChanged("primary %s is gone", d.primaryID)
	case vtMerged(b.MergedIntoBookID):
		return errVTChanged("book %s was merged into %s", d.primaryID, dcStr(b.MergedIntoBookID))
	case b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion:
		return errVTChanged("book %s is no longer its group's primary", d.primaryID)
	case dcStr(b.VersionGroupID) != d.groupID:
		return errVTChanged("book %s left version group %s", d.primaryID, d.groupID)
	case database.MetadataApplied(b.MetadataReviewStatus) || metafetch.IsMarkedNoMatch(b.MetadataReviewStatus):
		return errVTChanged("book %s was applied or marked no match", d.primaryID)
	case util.NormalizeTitle(b.Title) != d.title:
		return errVTChanged("book %s was retitled", d.primaryID)
	case !strings.EqualFold(dcStr(b.ASIN), d.asin):
		return errVTChanged("book %s's ASIN changed", d.primaryID)
	}
	return nil
}

// vtPreWrite is the full check of the primary immediately before the write,
// outside any lock: the row re-read (vtStillNeeds), every iTunes signal the
// plan checks with the paths resolved on disk (vtITunesWhy: the book's iTunes
// id, its book_file rows' iTunes ids and paths, a live iTunes external id, a
// path in the iTunes library behind a symlink), and, for an apply, that no
// book outside the group has gained the record (vtOutsideGroup, which may
// scan every book row while memdb is not ready). It returns the paths it
// cleared; the under-lock guard (vtWriteGuard) refuses any other.
//
// The framework's own path guard is off for this fixer (ITunesDatabaseOnly),
// so this and vtWriteGuard are the only iTunes checks between the re-plan
// and the write.
//
// The identifier check (vtIdentifiersOutsideGroup) runs here only, not again
// under the lock: it walks the ISBN/ASIN index and reads each hit, which is
// not the cheap work vtWriteGuard may do. A book outside the group that gains
// the record's ASIN or ISBN between this check and the commit (one apply's
// width: the apply body and its ModifyBook) is not caught. That window is
// accepted; the record hash, unlike the identifiers, is re-checked under the
// lock (from memdb, vtWriteGuard).
func vtPreWrite(rd vtReaders, d *vtDetail) (map[string]bool, error) {
	b, err := rd.store.GetBookByID(d.primaryID)
	if err != nil {
		return nil, fmt.Errorf("re-read book %s: %w", d.primaryID, err)
	}
	if err := vtStillNeeds(b, d); err != nil {
		return nil, err
	}
	core := b.Core()
	why, doubt, paths, err := vtITunesWhy(rd, &core)
	switch {
	case err != nil:
		return nil, err
	case why != "":
		return nil, errVTChanged("book %s is now iTunes-linked (%s)", d.primaryID, why)
	case doubt:
		return nil, errVTChanged("could not tell whether book %s is iTunes-linked", d.primaryID)
	}
	if d.hash != "" {
		outside, err := vtOutsideGroup(rd, d.groupID, d.hash)
		if err != nil {
			return nil, err
		}
		if len(outside) > 0 {
			return nil, errVTChanged("the record is now carried by %s outside version group %s", strings.Join(outside, ", "), d.groupID)
		}
	}
	// An identifier the apply copies, gained by a book outside the group
	// since the plan.
	elsewhere, indexed, err := vtIdentifiersOutsideGroup(rd, d.groupID, d.ids)
	switch {
	case err != nil:
		return nil, err
	case !indexed:
		return nil, fmt.Errorf("%w: the ISBN/ASIN index is not built, so it cannot be told whether a book outside "+
			"version group %s carries the record's %s", repairs.ErrRetryLater, d.groupID, d.ids.String())
	case len(elsewhere) > 0:
		return nil, errVTChanged("the record's %s is now carried by %s outside version group %s", d.ids.String(),
			strings.Join(elsewhere, ", "), d.groupID)
	}
	cleared := make(map[string]bool, len(paths))
	for _, p := range paths {
		cleared[p] = true
	}
	return cleared, nil
}

// vtWriteGuard is the check both classes run on the primary's row as it
// stands at the write: inside the apply's ModifyBook, under the book's write
// stripe (and on the row as first read, before the apply body; see
// metafetch.ApplyOptions.Guard), or just before the cache Put of a copy.
// Nothing slow runs under a stripe (pebble_store_book_lock.go), so it does
// only cheap work, and fails closed whatever it cannot tell cheaply:
//   - vtStillNeeds on the row (primary, group, applied, title, ASIN);
//   - the iTunes signals stored in rows: the book's iTunes id, its book_file
//     rows' iTunes ids and paths, a live iTunes external id (point and prefix
//     reads; itunesCopyWhy with no paths, so no path is resolved on disk);
//   - every path of the book and its files must be one vtPreWrite resolved
//     and cleared moments before; a path that appeared since is refused
//     rather than resolved here;
//   - for an apply, no book outside the group carries the record, read from
//     memdb only (memHash). memdb disabled, still warming up or missing rows
//     cannot answer without the full scan, so the row is refused
//     (retry_later, repairs.ErrRetryLater) instead: re-run the apply once
//     memdb is serving.
//
// The reads take no lock of their own, so none can wait on the stripe held
// here.
func vtWriteGuard(rd vtReaders, d *vtDetail, cleared map[string]bool, b *database.Book) error {
	if err := vtStillNeeds(b, d); err != nil {
		return err
	}
	core := b.Core()
	paths, ff, exts, err := vtITunesInputs(rd, &core)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if !cleared[p] {
			return errVTChanged("book %s has a path the pre-write check did not clear (%q)", d.primaryID, p)
		}
	}
	why, doubt := itunesCopyWhy(nil, core.ID, dcStr(core.ITunesPersistentID), nil, ff, exts)
	switch {
	case why != "":
		return errVTChanged("book %s is now iTunes-linked (%s)", d.primaryID, why)
	case doubt:
		return errVTChanged("could not tell whether book %s is iTunes-linked", d.primaryID)
	}
	if d.hash != "" {
		books, err := rd.memHash(d.hash)
		if err != nil {
			// Nothing about the row changed: retry_later, so the same plan
			// can be applied again once memdb serves reads.
			return fmt.Errorf("%w: cannot tell without a full book scan, which never runs under the write lock, "+
				"whether a book outside version group %s carries the record (%v); re-run the apply once memdb is "+
				"serving reads", repairs.ErrRetryLater, d.groupID, err)
		}
		var outside []string
		for i := range books {
			c := books[i].Core()
			if vtLive(&c) && dcStr(c.VersionGroupID) != d.groupID {
				outside = append(outside, c.ID)
			}
		}
		if len(outside) > 0 {
			sort.Strings(outside)
			return errVTChanged("the record is now carried by %s outside version group %s", strings.Join(outside, ", "), d.groupID)
		}
	}
	return nil
}

// errVTMatchNotRecorded: the apply wrote but did not stamp the match.
var errVTMatchNotRecorded = errors.New("the apply did not record the match")

// vtBatchID is the change-history batch id of the apply of one row: the op's
// id and the primary's, so every history row of the apply names the op that
// wrote it, and the journal row names the batch.
func vtBatchID(opID, primaryID string) string { return opID + ":" + primaryID }

// vtHistorySource is what the apply's change history records as its source.
func vtHistorySource(d *vtDetail) string {
	return fmt.Sprintf("%s (from twin %s, %s record)", versionTwinFixerID, d.twinID, d.cand.Source)
}

// Apply writes one fresh row: the twin's record applied to the primary, or
// the twin's candidates copied onto it. Neither goes through w's book
// primitives (the apply records its own change history after the commit; the
// candidate cache is not a book row), so each is preceded by w.Beat to renew
// the scan stand-down lease, and each is journaled under the op AFTER it
// lands (ledger after write: a journal row never describes a write that did
// not happen). A crash between the write and its journal row is finished on
// the resumed run of the same op: Replan recognises the op's own write
// (vtResumed) and Apply records only the missing journal row. A journal
// failure is reported as repairs.ErrPartiallyApplied.
func (f *versionTwinFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*vtDetail)
	if !ok || d == nil {
		return fmt.Errorf("row %s: no apply detail from the re-plan", fresh.RowID)
	}
	if w.OpID() == "" {
		return fmt.Errorf("row %s: %w", fresh.RowID, repairs.ErrNotJournaled)
	}
	if d.resume {
		return vtJournalResumed(w, d)
	}
	rd, err := f.readers(repairs.NewPathResolver())
	if err != nil {
		return err
	}
	// pre runs the slow checks right before the write, outside any lock;
	// guard runs the cheap ones at the write, against the paths pre cleared.
	var cleared map[string]bool
	pre := func() error {
		c, err := vtPreWrite(rd, d)
		cleared = c
		return err
	}
	guard := func(b *database.Book) error { return vtWriteGuard(rd, d, cleared, b) }
	switch d.class {
	case vtClassApplied:
		return f.applyRecord(w, rd.svc, d, pre, guard)
	case vtClassCandidates:
		return f.copyCandidates(w, rd.svc, d, pre, guard)
	}
	return fmt.Errorf("row %s: unknown class %q", fresh.RowID, d.class)
}

// vtJournalResumed records the journal row of a write this op made before a
// restart cut it off (vtResumed), and writes nothing else. Writer.Journal
// skips a row already in the op's journal.
func vtJournalResumed(w *repairs.Writer, d *vtDetail) error {
	var err error
	switch d.class {
	case vtClassApplied:
		err = w.Journal(d.primaryID, undo.ChangeTypeMetadataApply, undo.MetadataApplyField, "", d.jNew)
	case vtClassCandidates:
		err = w.Journal(d.primaryID, undo.ChangeTypeMetadataCacheCopy, undo.MetadataCacheField, d.jOld, d.jNew)
	default:
		return fmt.Errorf("row %s: unknown class %q", d.groupID, d.class)
	}
	if err != nil {
		return fmt.Errorf("%w: book %s was written by this operation before a restart, but its journal row was not "+
			"recorded: %v", repairs.ErrPartiallyApplied, d.primaryID, err)
	}
	return nil
}

// applyRecord applies the twin's record to the primary. FillOnly: an
// automatic apply (nobody picked this candidate for this book): filled
// descriptive fields are kept, a "no match" is refused, and the match is
// stamped because the primary holds the record's title. SkipHashElection: no
// MATCH-4 election (the guard proved no outside book carries the record).
// BookRowOnly: no tags, provenance, segment titles, backfill or cover, so the
// history batch is the whole write. RequireHistory: a failed history row is
// an error, since the revert reads that history.
func (f *versionTwinFixer) applyRecord(w *repairs.Writer, svc VersionTwinMetadataService, d *vtDetail, pre func() error, guard func(*database.Book) error) error {
	if err := w.Beat("metadata apply on book " + d.primaryID); err != nil {
		return err
	}
	if err := pre(); err != nil {
		return err
	}
	batch := vtBatchID(w.OpID(), d.primaryID)
	resp, err := svc.ApplyMetadataCandidateWithOptions(d.primaryID, *d.cand, d.fields, vtApplyOptions(d, batch, guard))
	if resp == nil || resp.Book == nil {
		if err == nil {
			err = errors.New("the apply returned no book")
		}
		return fmt.Errorf("apply twin %s's record to %s: %w", d.twinID, d.primaryID, err)
	}
	// The write committed. Journal it whatever else went wrong, so the op
	// revert can reach it.
	jerr := w.Journal(d.primaryID, undo.ChangeTypeMetadataApply, undo.MetadataApplyField, "", batch)
	switch {
	case err != nil && jerr != nil:
		return fmt.Errorf("%w: book %s was written but its history (%v) and its journal row (%v) were not recorded",
			repairs.ErrPartiallyApplied, d.primaryID, err, jerr)
	case err != nil:
		return fmt.Errorf("%w: book %s was written but its change history was not fully recorded: %v",
			repairs.ErrPartiallyApplied, d.primaryID, err)
	case jerr != nil:
		return fmt.Errorf("%w: book %s was written but its journal row was not recorded: %v",
			repairs.ErrPartiallyApplied, d.primaryID, jerr)
	}
	if !database.MetadataApplied(resp.Book.MetadataReviewStatus) {
		return fmt.Errorf("%w on %s", errVTMatchNotRecorded, d.primaryID)
	}
	return nil
}

// vtApplyOptions are the options of an applied_twin apply (applyRecord).
func vtApplyOptions(d *vtDetail, batch string, guard func(*database.Book) error) metafetch.ApplyOptions {
	return metafetch.ApplyOptions{
		FillOnly: true, Guard: guard, BatchID: batch, SkipHashElection: true, BookRowOnly: true,
		HistorySource: vtHistorySource(d), RequireHistory: true,
	}
}

// copyCandidates copies the twin's candidates onto the primary. The check
// runs on the primary and its cache row as re-read just before the write:
// vtWriteGuard, and no candidates or fetch verdict of its own. The prior
// row (or its absence) is journaled after the copy, so the op revert
// removes it again.
func (f *versionTwinFixer) copyCandidates(w *repairs.Writer, svc VersionTwinMetadataService, d *vtDetail, pre func() error, guard func(*database.Book) error) error {
	if err := w.Beat("candidate copy onto book " + d.primaryID); err != nil {
		return err
	}
	if err := pre(); err != nil {
		return err
	}
	var prior *database.MetadataCandidateCache
	cp, err := svc.CopyCandidateCache(d.twinID, d.primaryID, func(b *database.Book, cur *database.MetadataCandidateCache) error {
		if err := guard(b); err != nil {
			return err
		}
		switch {
		case cur != nil && len(cur.Candidates) > 0:
			return errVTChanged("book %s gained candidates of its own", d.primaryID)
		case vtCacheVerdict(cur):
			return errVTChanged("book %s's own fetch recorded that it found nothing", d.primaryID)
		}
		prior = cur
		return nil
	})
	if err != nil {
		return fmt.Errorf("copy twin %s's candidates to %s: %w", d.twinID, d.primaryID, err)
	}
	oldV, err := undo.EncodeMetadataCacheOld(prior)
	if err == nil {
		err = w.Journal(d.primaryID, undo.ChangeTypeMetadataCacheCopy, undo.MetadataCacheField, oldV, undo.MetadataCacheStamp(cp))
	}
	if err != nil {
		return fmt.Errorf("%w: %d candidates copied to %s, but the journal row was not recorded: %v",
			repairs.ErrPartiallyApplied, len(cp.Candidates), d.primaryID, err)
	}
	return nil
}
