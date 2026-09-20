// file: internal/maintenance/jobs/repoint_version_primary.go
// version: 1.0.0
// guid: 5e1c8a07-3d42-4f96-b8d1-c07a9e25f4b3
// last-edited: 2026-09-20

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

func init() { maintenance.Register(&repointVersionPrimaryJob{}) }

// repointVersionPrimaryJob moves the is_primary_version flag from an organized
// single-chapter book onto its imported twin, so a detected chapter run whose
// every member is a non-primary version can be consolidated.
//
// # The problem this exists to fix
//
// `internal/scanner/chapter_consolidator.go:852` refuses a chapter run whose
// members all fail `database.EffectiveIsPrimaryVersion`, and tells the operator
// to "review the primary copies instead". Measured on prod 2026-09-19
// (.claude/notes/chapter-split-blocker-analysis-2026-09-19.md, §2): that advice
// is impossible to follow. 226 of the 240 primary directories the blocker names
// hold exactly ONE chapter file, so the primaries can never reach `min_files: 2`
// and no detector run can ever produce the review the message asks for. 84
// blocked groups carry the blocker; on 26 groups / 1,426 books it is the ONLY
// blocker.
//
// Each imported per-chapter book sits in a 2-member version group with an
// organized twin that holds the primary flag. The owner's decision (2026-09-19)
// is to RE-POINT which copy is primary: keep the version link — it is the only
// record that the two sides are copies of each other — and move the flag to the
// imported side so the run can merge and the merged book is visible. Dissolving
// the link was considered and rejected: it destroys that record. The 1,426
// organized single-chapter files stay on disk and in the database; their
// disposal is a separate decision the owner has not made.
//
// # Detection is reused, never re-implemented
//
// "Member of a chapter run" is decided by the SAME detection the consolidator
// uses: this job calls `detectChapterGroupsForRunWithBooks`, which is the
// scan/preview path (`scanner.DetectChapterGroupsWithOptions` with the options
// `chapterDetectOptionsForRun` builds, iTunes protector and owner-manual-only
// excluder included). There is no parallel heuristic here. The books the
// version-group index is built from are the SAME snapshot detection ran on, so
// a pair cannot qualify against rows detection never saw.
//
// # Which groups qualify
//
// A blocked group qualifies when `len(Blockers) == 1` and every member fails
// `EffectiveIsPrimaryVersion`. That is a row-derived test, not a string match on
// the blocker prose (which has already changed twice — 3b11ae96b, 10b6e20d8):
// :852 always appends the non-primary blocker when any member is non-primary,
// so one blocker plus all-members-non-primary means that one blocker IS it.
//
// # Never zero primaries, never two
//
// Two helpers disagree about nil and both must be satisfied AFTER the write:
//   - `database.EffectiveIsPrimaryVersion` — nil counts as primary.
//   - `reconcile/elect_primaries.go:203` `countsAsPrimary` — `primary != nil &&
//     *primary && electableRow(...)`: a soft-deleted row or a merge loser does
//     NOT give its group a primary.
//
// Writing an explicit true on a soft-deleted or merged-away book would therefore
// leave the group reading "primary" to every listing and "zero primaries" to
// elect-missing-primaries, which would then elect a second one. So the promoted
// book must be electable. `electableRow` is unexported; the test replicated here
// is deliberately STRICTER (any non-nil MergedIntoBookID is refused, rather than
// resolving whether the survivor is alive) — stricter only shrinks the candidate
// set, and a merge loser is never a book to crown.
//
// Both rows are written explicitly (true on the promoted, false on the demoted),
// so neither helper has to fall back on the nil convention afterwards.
type repointVersionPrimaryJob struct{}

// Library-state literals. There is no shared constant for these in
// internal/database (only scanner/override_guard.go's unexported
// libraryStateImported), so they are spelled here with this note rather than
// reaching into another package for a private identifier.
const (
	repointStateImported  = "imported"
	repointStateOrganized = "organized"
)

func (j *repointVersionPrimaryJob) ID() string { return "repoint-version-primary" }
func (j *repointVersionPrimaryJob) Name() string {
	return "Repoint version primary to the chapter copy"
}
func (j *repointVersionPrimaryJob) Category() string { return "maintenance" }
func (j *repointVersionPrimaryJob) CanResume() bool  { return false }

// DefaultParams advertises every key Run reads. Note the TWO switches: the
// dispatcher resolves `dry_run` and hands it to Run, and `apply` is this job's
// own explicit opt-in. A write needs BOTH, so the operator body is
// {"apply": true, "dry_run": false}; anything else reports and writes nothing.
func (j *repointVersionPrimaryJob) DefaultParams() any {
	return struct {
		Apply              bool     `json:"apply"`
		DryRun             bool     `json:"dry_run"`
		MinFiles           int      `json:"min_files"`
		MaxPerFileDuration int      `json:"max_per_file_duration"`
		PathPrefix         string   `json:"path_prefix"`
		GroupIDs           []string `json:"group_ids"`
	}{DryRun: true, MinFiles: chapterDefaultMinFiles, MaxPerFileDuration: chapterDefaultMaxPerFileDuration}
}

func (j *repointVersionPrimaryJob) Description() string {
	return "Move is_primary_version from an organized single-chapter book to its imported twin in a detected chapter run, so the run can be consolidated. " +
		"Report only by default; send {\"apply\": true, \"dry_run\": false} to write. Keeps the version link intact."
}

// Policy is DefaultPolicy with library.scan's ConcurrencyKey, JOINING it the way
// merge-chapter-groups does (never splitting it). The justification is specific,
// not a generic "nothing applies during a scan": the scanner REVERTS
// library_state organized->imported on every rescan (PR #3097), and
// library_state is exactly the field this job's predicate keys on. A scan
// running beside it could flip a twin between the snapshot and the write, so the
// pair the report describes would not be the pair that was written.
func (j *repointVersionPrimaryJob) Policy() maintenance.ExecutionPolicy {
	p := maintenance.DefaultPolicy()
	p.ConcurrencyKey = chapterLibraryScanKey
	return p
}

// repointVersionPrimaryParams is the operator-facing parameter shape.
type repointVersionPrimaryParams struct {
	// Apply must be explicitly true to write, AND the run's dry_run must be
	// false. Two switches, both fail-closed.
	Apply bool `json:"apply"`
	// DryRun is decoded only so the result can echo the effective params; the
	// value Run acts on arrives through its dryRun argument.
	DryRun bool `json:"dry_run"`
	// MinFiles / MaxPerFileDuration / PathPrefix are forwarded verbatim to the
	// shared chapter detection, so this job and scan-chapter-groups can be run
	// with identical scoping and compared.
	MinFiles           int    `json:"min_files"`
	MaxPerFileDuration int    `json:"max_per_file_duration"`
	PathPrefix         string `json:"path_prefix"`
	// GroupIDs restricts the run to these VERSION group ids. A version group is
	// the unit of work here and has a real database id; a detected chapter
	// group has no stable id of its own (only a primary book id and a
	// preview-time fingerprint), so naming chapter groups would not be
	// addressable across runs.
	//
	// Accepted as "group_ids" OR "groupIds"; both present with different values
	// is an error rather than a silent winner (the hazard
	// missingFileRepointParams documents for path_prefix).
	GroupIDs      []string `json:"group_ids,omitempty"`
	GroupIDsAlias []string `json:"groupIds,omitempty"`
}

func parseRepointVersionPrimaryParams(raw json.RawMessage) (repointVersionPrimaryParams, error) {
	p := repointVersionPrimaryParams{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, fmt.Errorf("decode repoint-version-primary params: %w", err)
		}
	}
	switch {
	case len(p.GroupIDsAlias) == 0:
	case len(p.GroupIDs) == 0:
		p.GroupIDs = p.GroupIDsAlias
	case !sameStringSet(p.GroupIDs, p.GroupIDsAlias):
		return p, fmt.Errorf("repoint-version-primary: group_ids and groupIds both given and differ; send one")
	}
	p.GroupIDsAlias = nil
	if p.MinFiles <= 0 {
		p.MinFiles = chapterDefaultMinFiles
	}
	if p.MaxPerFileDuration <= 0 {
		p.MaxPerFileDuration = chapterDefaultMaxPerFileDuration
	}
	return p, nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// repointBuckets: every candidate lands in exactly ONE bucket and every bucket
// is reported, so a book that is not repointed is visible rather than silently
// dropped. A zero-candidate run says WHY it found nothing.
const (
	bucketWouldRepoint      = "would_repoint"
	bucketRepointed         = "repointed"
	bucketAlreadyPrimary    = "already_primary"
	bucketNoVersionGroup    = "no_version_group"
	bucketGroupNotTwo       = "group_not_two_members"
	bucketTwinNotPrimary    = "twin_not_primary"
	bucketTwinInChapterRun  = "twin_in_chapter_run"
	bucketTwinNotSingleFile = "twin_not_single_file"
	bucketStateMismatch     = "state_mismatch"
	bucketNotElectable      = "not_electable"
	bucketOwnerManualOnly   = "owner_manual_only"
	bucketITunesProtected   = "itunes_protected"
	bucketNotSelected       = "not_selected"
	bucketDrifted           = "drifted"
	bucketWriteFailed       = "write_failed"
	bucketLeftDoublePrimary = "left_double_primary"
)

// repointPairDecision is one (imported member, organized twin) pair's outcome.
//
// PromotePriorFlag / DemotePriorFlag record the STORED tri-state ("nil", "true",
// "false") that each row carried before the write, not the effective bool. A
// reversal that restored `false` where `nil` had been would not be a reversal,
// so the payload has to carry the distinction the helpers erase.
type repointPairDecision struct {
	VersionGroupID   string `json:"version_group_id"`
	PromoteBookID    string `json:"promote_book_id"`
	PromotePath      string `json:"promote_path,omitempty"`
	PromotePriorFlag string `json:"promote_prior_flag,omitempty"`
	DemoteBookID     string `json:"demote_book_id,omitempty"`
	DemotePath       string `json:"demote_path,omitempty"`
	DemotePriorFlag  string `json:"demote_prior_flag,omitempty"`
	Bucket           string `json:"bucket"`
	Reason           string `json:"reason,omitempty"`
}

// repointGroupOutcome is one detected chapter run and what this job decided
// about each of its members.
type repointGroupOutcome struct {
	ChapterPrimaryBookID string                `json:"chapter_primary_book_id"`
	Directory            string                `json:"directory"`
	CommonTitle          string                `json:"common_title,omitempty"`
	MemberCount          int                   `json:"member_count"`
	Blockers             []string              `json:"blockers,omitempty"`
	Pairs                []repointPairDecision `json:"pairs,omitempty"`
}

type repointVersionPrimaryResult struct {
	Job    string                      `json:"job"`
	DryRun bool                        `json:"dry_run"`
	Apply  bool                        `json:"apply"`
	Params repointVersionPrimaryParams `json:"params"`

	// GroupsDetected / GroupsBlocked mirror the detector's own counts, so this
	// job's population can be compared with a scan-chapter-groups run.
	GroupsDetected int `json:"groups_detected"`
	GroupsBlocked  int `json:"groups_blocked"`
	// GroupsSoleNonPrimary are blocked groups whose ONLY blocker is that every
	// member is a non-primary version — the population this job exists for.
	GroupsSoleNonPrimary int `json:"groups_sole_non_primary"`
	// GroupsOtherBlockers are blocked groups that carry another blocker too;
	// re-pointing would not unblock them, so they are counted, not touched.
	GroupsOtherBlockers int `json:"groups_other_blockers"`

	Counts map[string]int        `json:"counts"`
	Groups []repointGroupOutcome `json:"groups,omitempty"`
}

func (r *repointVersionPrimaryResult) count(bucket string) {
	if r.Counts == nil {
		r.Counts = map[string]int{}
	}
	r.Counts[bucket]++
}

// Run detects chapter runs, classifies every member of the sole-non-primary
// blocked groups, and (only with apply AND a real run) re-points each
// qualifying pair. It persists the full per-group decision set as the
// operation's structured result.
//
// Reporting convention: this package's siblings (scan/merge-chapter-groups)
// persist their decisions with maintenance.SetResult rather than writing a TSV
// under {root_dir}/.reports/ the way missing_file_repoint.go does. That op's
// resolveReportPath is a method on the plugin *Plugin, which a maintenance Job
// does not have, so the payload IS the report here. The decisions are readable
// at GET /operations/:id/result on every run, dry or real.
func (j *repointVersionPrimaryJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	p, err := parseRepointVersionPrimaryParams(maintenance.RawParamsFromCtx(ctx))
	if err != nil {
		return err
	}
	write := p.Apply && !dryRun
	if write {
		// Fails CLOSED, like merge-chapter-groups: a store that cannot list
		// active operations is not proof that no scan is running.
		if err := refuseDuringLibraryScan(store, j.ID()); err != nil {
			return err
		}
	}
	p.DryRun = !write

	det, books, err := detectChapterGroupsForRunWithBooks(ctx, store, chapterGroupParams{
		MinFiles:           p.MinFiles,
		MaxPerFileDuration: p.MaxPerFileDuration,
		PathPrefix:         p.PathPrefix,
	})
	if err != nil {
		return err
	}
	protect, err := chapterProtector()
	if err != nil {
		return err
	}

	res := repointVersionPrimaryResult{Job: j.ID(), DryRun: !write, Apply: p.Apply, Params: p}
	res.GroupsDetected = len(det.Groups)
	res.GroupsBlocked = len(det.Blocked)

	idx := newRepointIndex(books, det)
	selected := map[string]bool{}
	for _, id := range p.GroupIDs {
		selected[id] = true
	}

	reporter.SetTotal(len(det.Blocked))
	for _, g := range det.Blocked {
		reporter.Increment()
		if err := ctx.Err(); err != nil {
			return err
		}
		if !soleNonPrimaryBlocker(g, idx) {
			res.GroupsOtherBlockers++
			continue
		}
		res.GroupsSoleNonPrimary++
		out := repointGroupOutcome{
			ChapterPrimaryBookID: g.PrimaryBookID,
			Directory:            g.Directory,
			CommonTitle:          g.CommonTitle,
			MemberCount:          len(g.BookIDs),
			Blockers:             g.Blockers,
		}
		for _, id := range g.BookIDs {
			d := j.classify(store, idx, protect, id, selected, len(p.GroupIDs) > 0)
			if d.Bucket == bucketWouldRepoint && write {
				j.applyPair(store, &d)
			}
			res.count(d.Bucket)
			out.Pairs = append(out.Pairs, d)
		}
		res.Groups = append(res.Groups, out)
	}

	chapterLog.Info("repoint-version-primary complete: dry_run=%t apply=%t blocked=%d sole_non_primary=%d other_blockers=%d would_repoint=%d repointed=%d drifted=%d write_failed=%d left_double_primary=%d",
		!write, p.Apply, res.GroupsBlocked, res.GroupsSoleNonPrimary, res.GroupsOtherBlockers,
		res.Counts[bucketWouldRepoint], res.Counts[bucketRepointed], res.Counts[bucketDrifted],
		res.Counts[bucketWriteFailed], res.Counts[bucketLeftDoublePrimary])

	if err := maintenance.SetResult(ctx, res); err != nil {
		return err
	}
	// A write that failed or left a pair double-primary is an operator action,
	// so the op must not complete green.
	if n := res.Counts[bucketWriteFailed] + res.Counts[bucketLeftDoublePrimary]; n > 0 {
		return fmt.Errorf("repoint-version-primary: %d pairs need manual attention (write_failed=%d left_double_primary=%d); see the run result",
			n, res.Counts[bucketWriteFailed], res.Counts[bucketLeftDoublePrimary])
	}
	return nil
}

// repointIndex is the single snapshot everything is derived from: the books
// detection itself ran on, indexed by id and by version group, plus the set of
// books that are members of ANY detected chapter run.
type repointIndex struct {
	byID     map[string]*database.BookCore
	byGroup  map[string][]*database.BookCore
	inRun    map[string]bool
	selected map[string]bool
}

func newRepointIndex(books []database.BookCore, det scanner.ChapterDetection) *repointIndex {
	idx := &repointIndex{
		byID:    make(map[string]*database.BookCore, len(books)),
		byGroup: map[string][]*database.BookCore{},
		inRun:   map[string]bool{},
	}
	for i := range books {
		b := &books[i]
		idx.byID[b.ID] = b
		// Soft-deleted rows are not group members for the "exactly two" test:
		// a trashed row is not a copy anybody can play, and counting it would
		// reject pairs that are, live, exactly two.
		if b.IsSoftDeleted() {
			continue
		}
		if gid := strPtr(b.VersionGroupID); gid != "" {
			idx.byGroup[gid] = append(idx.byGroup[gid], b)
		}
	}
	for _, g := range append(append([]scanner.ChapterGroup(nil), det.Groups...), det.Blocked...) {
		for _, id := range g.BookIDs {
			idx.inRun[id] = true
		}
	}
	return idx
}

func strPtr(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// priorFlag renders the STORED tri-state for the audit record.
func priorFlag(f *bool) string {
	if f == nil {
		return "nil"
	}
	if *f {
		return "true"
	}
	return "false"
}

// soleNonPrimaryBlocker is the row-derived test for "the non-primary rule is the
// only thing keeping this group out". It never reads the blocker prose: the
// consolidator appends the non-primary blocker whenever any member is
// non-primary, so one blocker plus every-member-non-primary identifies it.
func soleNonPrimaryBlocker(g scanner.ChapterGroup, idx *repointIndex) bool {
	if len(g.Blockers) != 1 || len(g.BookIDs) == 0 {
		return false
	}
	for _, id := range g.BookIDs {
		b := idx.byID[id]
		if b == nil || database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
			return false
		}
	}
	return true
}

// classify decides one member's outcome without writing anything.
func (j *repointVersionPrimaryJob) classify(store maintenance.JobStore, idx *repointIndex, protect func(*database.BookCore) string, bookID string, selected map[string]bool, filtering bool) repointPairDecision {
	d := repointPairDecision{PromoteBookID: bookID}
	b := idx.byID[bookID]
	if b == nil {
		d.Bucket = bucketDrifted
		d.Reason = "member not in the detection snapshot"
		return d
	}
	d.PromotePath = b.FilePath
	d.PromotePriorFlag = priorFlag(b.IsPrimaryVersion)

	gid := strPtr(b.VersionGroupID)
	d.VersionGroupID = gid
	if gid == "" {
		d.Bucket = bucketNoVersionGroup
		d.Reason = "member is non-primary with no version group; that is elect/normalize territory, not a repoint"
		return d
	}
	if filtering && !selected[gid] {
		d.Bucket = bucketNotSelected
		d.Reason = "version group not in group_ids"
		return d
	}
	// A nil flag reads as PRIMARY (EffectiveIsPrimaryVersion), so it is never a
	// promotion candidate; soleNonPrimaryBlocker already excluded such groups,
	// and this keeps the per-member rule true on its own.
	if database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
		d.Bucket = bucketAlreadyPrimary
		d.Reason = "member already counts as primary"
		return d
	}
	// countsAsPrimary (reconcile/elect_primaries.go:203) refuses a soft-deleted
	// row or a merge loser, so promoting one would leave the group reading zero
	// primaries to elect-missing-primaries while every other reader sees a
	// primary. Deliberately stricter than electableRow: any MergedIntoBookID.
	if !repointElectable(b) {
		d.Bucket = bucketNotElectable
		d.Reason = "member is trashed or merged away; promoting it would still leave the group with no primary"
		return d
	}
	if state := strPtr(b.LibraryState); state != repointStateImported {
		d.Bucket = bucketStateMismatch
		d.Reason = fmt.Sprintf("member library_state is %q, want %q", state, repointStateImported)
		return d
	}

	members := idx.byGroup[gid]
	if len(members) != 2 {
		d.Bucket = bucketGroupNotTwo
		d.Reason = fmt.Sprintf("version group has %d live members, want exactly 2", len(members))
		return d
	}
	var twin *database.BookCore
	for _, m := range members {
		if m.ID != b.ID {
			twin = m
		}
	}
	if twin == nil {
		d.Bucket = bucketGroupNotTwo
		d.Reason = "version group's two members are the same row"
		return d
	}
	d.DemoteBookID = twin.ID
	d.DemotePath = twin.FilePath
	d.DemotePriorFlag = priorFlag(twin.IsPrimaryVersion)

	// The twin is a lone single-chapter book BELOW min_files, so detection never
	// saw it and neither exclusion hook has looked at it. Both must be run here
	// or this job would demote a row inside the iTunes library or one of the
	// owner's manual-only titles.
	if applygate.IsOwnerManualOnly(twin.FilePath, "") || applygate.IsOwnerManualOnly(b.FilePath, "") {
		d.Bucket = bucketOwnerManualOnly
		d.Reason = "owner-manual-only title (Doctor Who / Big Finish / Torchwood)"
		return d
	}
	if why := protect(twin); why != "" {
		d.Bucket = bucketITunesProtected
		d.Reason = "twin is " + why
		return d
	}
	if why := protect(b); why != "" {
		d.Bucket = bucketITunesProtected
		d.Reason = "member is " + why
		return d
	}
	if twin.IsPrimaryVersion == nil || !*twin.IsPrimaryVersion {
		d.Bucket = bucketTwinNotPrimary
		d.Reason = "twin does not hold an explicit primary flag; there is no flag here to move"
		return d
	}
	if idx.inRun[twin.ID] {
		d.Bucket = bucketTwinInChapterRun
		d.Reason = "twin is itself a member of a detected chapter run; flipping the flag would only move the blocker"
		return d
	}
	if state := strPtr(twin.LibraryState); state != repointStateOrganized {
		d.Bucket = bucketStateMismatch
		d.Reason = fmt.Sprintf("twin library_state is %q, want %q", state, repointStateOrganized)
		return d
	}
	files, ferr := store.GetBookFiles(twin.ID)
	if ferr != nil {
		d.Bucket = bucketTwinNotSingleFile
		d.Reason = fmt.Sprintf("twin's files not readable: %v", ferr)
		return d
	}
	if len(files) != 1 {
		d.Bucket = bucketTwinNotSingleFile
		d.Reason = fmt.Sprintf("twin holds %d files, want exactly 1 (a single-chapter book)", len(files))
		return d
	}
	d.Bucket = bucketWouldRepoint
	d.Reason = fmt.Sprintf("promote imported chapter %s, demote organized single-chapter twin %s (%s)",
		filepath.Base(b.FilePath), twin.ID, filepath.Base(twin.FilePath))
	return d
}

// repointElectable replicates reconcile/elect_primaries.go:203's electableRow
// contract, strictly: a row that is trashed or names ANY merge target does not
// give its group a primary.
func repointElectable(b *database.BookCore) bool {
	return !b.IsSoftDeleted() && strPtr(b.MergedIntoBookID) == ""
}

// applyPair writes the flip.
//
// # Ordering, and what a half-written pair looks like
//
// PROMOTE FIRST, demote second. The two failure windows are not symmetric:
//   - promote-then-fail-to-demote leaves TWO primaries. Every listing still
//     shows the group, elect-missing-primaries leaves it alone (primaries > 0),
//     and maintenance.version-group-primary-report already names the state.
//   - demote-then-fail-to-promote leaves ZERO primaries: every reader that
//     filters to primaries hides BOTH rows, i.e. the books disappear from the
//     UI and from ABS.
//
// Invisible is worse than duplicated, so the transient state is the double, and
// it is only ever transient by one write.
//
// If the demote fails, a compensating revert puts the promoted row back to
// explicit false — restoring the exact pre-state, because the promotion
// candidate is always explicitly false to begin with. The revert is itself a
// guarded ModifyBook (revert only if the row is still the explicit true this
// run wrote), so a concurrent legitimate promotion is not stomped. If the revert
// ALSO fails, the pair is reported as left_double_primary with both ids and the
// version group id — that list is what an operator hand-fixes, and it is kept
// out of the generic error count for exactly that reason.
//
// Every write goes through ModifyBook, which re-reads the row under its write
// lock and applies the mutation there, so no concurrent commit lands between
// read and write. Each mutation re-checks the precondition on that fresh row and
// returns database.ErrSkipBookWrite when it no longer holds: a row that changed
// underneath is SKIPPED and reported as drifted, never clobbered. (ModifyBook
// returns the unmodified row and a nil error in that case, which is
// indistinguishable from success by the return value alone — hence the explicit
// `wrote` flag set inside each closure.)
func (j *repointVersionPrimaryJob) applyPair(store maintenance.JobStore, d *repointPairDecision) {
	tru, fls := true, false
	gid := d.VersionGroupID

	promoted := false
	if _, err := store.ModifyBook(d.PromoteBookID, func(cur *database.Book) error {
		if strPtr(cur.VersionGroupID) != gid ||
			cur.IsPrimaryVersion == nil || *cur.IsPrimaryVersion ||
			cur.IsSoftDeleted() || strPtr(cur.MergedIntoBookID) != "" {
			return database.ErrSkipBookWrite
		}
		cur.IsPrimaryVersion = &tru
		promoted = true
		return nil
	}); err != nil {
		d.Bucket = bucketWriteFailed
		d.Reason = fmt.Sprintf("promote %s failed, nothing written: %v", d.PromoteBookID, err)
		return
	}
	if !promoted {
		d.Bucket = bucketDrifted
		d.Reason = "member changed underneath between detection and the write; skipped, not clobbered"
		return
	}

	demoted := false
	demoteErr := error(nil)
	if _, err := store.ModifyBook(d.DemoteBookID, func(cur *database.Book) error {
		if strPtr(cur.VersionGroupID) != gid ||
			cur.IsPrimaryVersion == nil || !*cur.IsPrimaryVersion {
			return database.ErrSkipBookWrite
		}
		cur.IsPrimaryVersion = &fls
		demoted = true
		return nil
	}); err != nil {
		demoteErr = err
	}
	if demoted {
		d.Bucket = bucketRepointed
		d.Reason = fmt.Sprintf("primary moved from %s to %s; version link untouched", d.DemoteBookID, d.PromoteBookID)
		return
	}

	// Compensating revert: restore the promoted row's explicit false.
	reverted := false
	_, rerr := store.ModifyBook(d.PromoteBookID, func(cur *database.Book) error {
		if cur.IsPrimaryVersion == nil || !*cur.IsPrimaryVersion {
			return database.ErrSkipBookWrite
		}
		cur.IsPrimaryVersion = &fls
		reverted = true
		return nil
	})
	why := "twin changed underneath"
	if demoteErr != nil {
		why = demoteErr.Error()
	}
	if reverted && rerr == nil {
		d.Bucket = bucketDrifted
		d.Reason = fmt.Sprintf("demote of %s not applied (%s); promotion reverted, pair unchanged", d.DemoteBookID, why)
		return
	}
	d.Bucket = bucketLeftDoublePrimary
	d.Reason = fmt.Sprintf("demote of %s not applied (%s) and the revert of %s did not restore it (reverted=%t err=%v); version group %s now has TWO primaries and needs a manual fix",
		d.DemoteBookID, why, d.PromoteBookID, reverted, rerr, gid)
}
