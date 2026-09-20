// file: internal/maintenance/jobs/repoint_version_primary.go
// version: 1.3.0
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
// single-chapter book onto its imported twin, so a detected chapter run blocked
// on non-primary members can be consolidated.
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
// A blocked group qualifies when `len(Blockers) == 1` and at least one member fails
// `EffectiveIsPrimaryVersion`. That is a row-derived test, not a string match on
// the blocker prose (which has already changed twice — 3b11ae96b, 10b6e20d8):
// :852 appends the non-primary blocker iff some member is non-primary,
// so one blocker plus one non-primary member means that blocker IS it. See
// soleNonPrimaryBlocker for why this is not "every member".
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

// Policy is DefaultPolicy, so registration derives the per-job key
// "maintenance.repoint-version-primary" and this job serializes against ITSELF.
//
// It briefly declared library.scan's key instead, to join the lane the way
// merge-chapter-groups does. That is not expressible: ConcurrencyKey is one
// field and TestMaintenanceOpSerializesAgainstItself requires every maintenance
// def's key to be DISTINCT, because a shared key turns independently-runnable
// jobs into one queue. Joining library.scan therefore meant giving up
// self-exclusion, and two concurrent runs of THIS job would interleave a promote
// and a demote on the same pair — a correctness property, not a nicety. So
// self-exclusion wins the field.
//
// What still guarantees the scan interlock, in two layers:
//
//  1. refuseDuringLibraryScan on the apply path (Run), failing CLOSED: a store
//     that cannot list active operations refuses the write. That covers a scan
//     already running when this job starts.
//  2. The hazard the key was really bought for is a scan starting MID-RUN and
//     reverting library_state organized->imported under this job (PR #3097),
//     because library_state is what the predicate keys on. Lane serialization
//     would have prevented that only at op granularity; instead BOTH write
//     closures in applyPair re-assert the library_state they classified on,
//     against the row ModifyBook re-read under its write lock. A row a scan
//     flipped is then skipped and reported as drifted rather than written from a
//     stale snapshot — a strictly stronger guarantee than the lane, because it
//     holds per row and at the instant of the write.
func (j *repointVersionPrimaryJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
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
	bucketDurationMismatch  = "duration_mismatch"
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
// consolidator appends the non-primary blocker if and only if AT LEAST ONE
// member is non-primary, so one blocker plus one non-primary member proves that
// single blocker IS the non-primary one.
//
// It is deliberately "at least one", not "every member". Requiring every member
// would strand any group that repointed only partly: one member left behind in
// drifted / state_mismatch / not_electable flips the group out of this
// population on the next run, so it stays blocked from consolidation AND becomes
// invisible to the op that exists to unblock it, reported as if it carried some
// other blocker. It would also structurally exclude the 10 groups the
// 2026-09-19 measurement recorded as "K of N members are non-primary" — partial
// from the start. Mixed groups need no special handling: classify answers per
// member and returns already_primary for the ones that need nothing.
func soleNonPrimaryBlocker(g scanner.ChapterGroup, idx *repointIndex) bool {
	if len(g.Blockers) != 1 {
		return false
	}
	for _, id := range g.BookIDs {
		b := idx.byID[id]
		if b != nil && !database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
			return true
		}
	}
	return false
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
	// CORROBORATION. Everything above establishes the SHAPE of a duplicate pair;
	// nothing yet establishes that the twin is a copy of this CHAPTER rather than
	// a complete book. A single-file whole-book .m4b, organized, version-linked to
	// one chapter of the imported copy, satisfies every check above — and
	// demoting it would hide the whole book while a lone chapter became primary.
	// The measured prod cases all show EXACT duration equality between the two
	// sides (blocker-analysis note §2, "same duration" on every sampled pair), so
	// the durations must agree, and an unknown duration on either side is refused
	// rather than assumed.
	if why := repointDurationAgrees(b, twin); why != "" {
		d.Bucket = bucketDurationMismatch
		d.Reason = why
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

// repointDurationTolerance is how far the two sides' durations may differ and
// still be called copies of each other: 2 seconds, or 1% of the longer side,
// whichever is larger. The measured pairs agree EXACTLY, so this is slack for
// re-probed durations rounding differently between the two rows, not a
// similarity threshold. It is far tighter than the gap this gate exists to
// catch — a whole book against one of its chapters, which differs by a factor,
// not by a percent.
const repointDurationTolerance = 2

// repointDurationAgrees returns "" when the two rows' durations corroborate that
// they are copies of each other, or the reason they do not. An unknown duration
// on either side is refused: this gate is the only corroboration in the
// predicate, so "not measured" must not read as "agrees".
func repointDurationAgrees(member, twin *database.BookCore) string {
	m, t := 0, 0
	if member.Duration != nil {
		m = *member.Duration
	}
	if twin.Duration != nil {
		t = *twin.Duration
	}
	if m <= 0 || t <= 0 {
		return fmt.Sprintf("duration unknown on one side (member=%ds twin=%ds); nothing corroborates that the twin is a copy of this chapter rather than a whole book", m, t)
	}
	diff := m - t
	if diff < 0 {
		diff = -diff
	}
	longer := m
	if t > longer {
		longer = t
	}
	tol := repointDurationTolerance
	if pct := longer / 100; pct > tol {
		tol = pct
	}
	if diff > tol {
		return fmt.Sprintf("durations disagree (member=%ds twin=%ds, differ by %ds > %ds); the twin may be a whole book, not a copy of this chapter", m, t, diff, tol)
	}
	return ""
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
// # A skipped demote is NOT a failed demote
//
// ModifyBook answers three different things with a nil error: it wrote, it hit
// ErrSkipBookWrite (returning the unmodified row), or the row does not exist
// (returning nil, nil). So "did not demote" covers four situations, and in THREE
// of them the state this job wants ALREADY HOLDS — the twin was demoted by
// something else, it left the version group, or its row is gone — leaving the
// promoted member as the group's one primary.
//
// Reverting there would write the member back to explicit false and leave the
// group with ZERO primaries by both helpers: both books disappear from the
// library list and from ABS. That is precisely the harm the promote-first
// ordering above exists to make impossible, reintroduced on the recovery path.
// It needs no exotic interleaving — a deleted twin is enough — and the snapshot
// is minutes old by the time the last pair is written, while ConcurrencyKey
// "library.scan" does not serialize this job against elect-missing-primaries or
// a dedup merge, both of which write this same field.
//
// So the revert fires ONLY when the demote genuinely ERRORED (demoteErr != nil),
// which is the only case in which the twin is still presumed primary. On a skip
// the twin's fresh row decides:
//
//   - gone, out of the group, or explicitly false -> the target state holds;
//     counted as repointed.
//   - flag became nil -> reported as left_double_primary and NOT reverted: nil
//     reads as primary to EffectiveIsPrimaryVersion, so with the member explicit
//     true the group has two effective primaries, and reverting would swap that
//     for zero by countsAsPrimary. Both are hand fixes; only one of them hides
//     the books.
//
// The revert is guarded on the row still being explicitly true AND still in the
// group, so a member that moved groups is not demoted inside its new one. The
// guard CANNOT distinguish this run's own true from a concurrent legitimate
// promotion — nothing in the row records who wrote it. That is safe only because
// the revert now runs solely in the demote-errored branch, where the twin is
// still primary, so writing the member back to false leaves exactly one primary
// (the twin) and restores the pre-state. It is not a general-purpose revert.
//
// If the revert also fails, the pair is reported as left_double_primary with
// both ids and the version group id — that list is what an operator hand-fixes,
// and it errors the run (Run's tally) rather than completing green.
func (j *repointVersionPrimaryJob) applyPair(store maintenance.JobStore, d *repointPairDecision) {
	tru, fls := true, false
	gid := d.VersionGroupID

	// PROMOTE. Each skip reason is captured, because they are not equivalent:
	// "already explicitly true" means a previous attempt got this far and the
	// pair must still be finished, while the others mean the member is no longer
	// the row this run classified.
	promoted, alreadyTrue, skip := false, false, ""
	row, err := store.ModifyBook(d.PromoteBookID, func(cur *database.Book) error {
		switch {
		case strPtr(cur.VersionGroupID) != gid:
			skip = "member left version group " + gid
		case cur.IsPrimaryVersion != nil && *cur.IsPrimaryVersion:
			alreadyTrue = true
		case cur.IsPrimaryVersion == nil:
			skip = "member's flag became nil"
		case cur.IsSoftDeleted():
			skip = "member was trashed"
		case strPtr(cur.MergedIntoBookID) != "":
			skip = "member was merged away"
		case strPtr(cur.LibraryState) != repointStateImported:
			// A library.scan running beside this job rewrites library_state
			// (PR #3097), which is what classify keyed on. Re-asserting it here,
			// on the row ModifyBook re-read under the write lock, is what
			// replaces the library.scan ConcurrencyKey this job cannot declare.
			skip = fmt.Sprintf("member's library_state became %q", strPtr(cur.LibraryState))
		}
		if skip != "" || alreadyTrue {
			return database.ErrSkipBookWrite
		}
		cur.IsPrimaryVersion = &tru
		promoted = true
		return nil
	})
	if err != nil {
		d.Bucket = bucketWriteFailed
		d.Reason = fmt.Sprintf("promote %s failed, nothing written: %v", d.PromoteBookID, err)
		return
	}
	if row == nil {
		d.Bucket = bucketDrifted
		d.Reason = fmt.Sprintf("member %s no longer exists; nothing written", d.PromoteBookID)
		return
	}
	if !promoted && !alreadyTrue {
		d.Bucket = bucketDrifted
		d.Reason = skip + "; skipped, not clobbered"
		return
	}
	// alreadyTrue falls THROUGH to the demote deliberately. Returning here would
	// leave a member that is primary beside a twin that still is — two primaries,
	// and a re-run cannot heal it because classify stops such a member at
	// already_primary and never reaches this function.

	demoted, dskip := false, ""
	twin, demoteErr := store.ModifyBook(d.DemoteBookID, func(cur *database.Book) error {
		switch {
		case strPtr(cur.VersionGroupID) != gid:
			dskip = "twin left version group " + gid
		case cur.IsPrimaryVersion == nil:
			dskip = "twin's flag became nil"
		case !*cur.IsPrimaryVersion:
			dskip = "twin was already demoted"
		case strPtr(cur.LibraryState) != repointStateOrganized:
			// Same reason as the promote closure: a concurrent scan must not be
			// able to make this job demote a row it would no longer classify.
			// NOTE this one lands in the "not demoted, no error" branch below,
			// where the twin is STILL explicitly primary — so it is correctly
			// reported as left_double_primary and not reverted into a group with
			// no primary at all.
			dskip = fmt.Sprintf("twin's library_state became %q", strPtr(cur.LibraryState))
		}
		if dskip != "" {
			return database.ErrSkipBookWrite
		}
		cur.IsPrimaryVersion = &fls
		demoted = true
		return nil
	})
	if demoted {
		j.recordRepointed(d, "primary moved from "+d.DemoteBookID+" to "+d.PromoteBookID+"; version link untouched")
		return
	}

	if demoteErr == nil {
		// The twin was not written and the store did not fail, so the twin's
		// own state says whether the job is already done.
		if twin == nil || strPtr(twin.VersionGroupID) != gid ||
			(twin.IsPrimaryVersion != nil && !*twin.IsPrimaryVersion) {
			where := "twin row is gone"
			if twin != nil {
				where = dskip
			}
			j.recordRepointed(d, fmt.Sprintf("%s: %s already counts as the group's only primary; no revert",
				where, d.PromoteBookID))
			return
		}
		d.Bucket = bucketLeftDoublePrimary
		d.Reason = fmt.Sprintf("twin %s is not explicitly non-primary (%s) while %s is explicit true, so version group %s reads as TWO primaries; NOT reverted, because reverting would leave it with none",
			d.DemoteBookID, dskip, d.PromoteBookID, gid)
		return
	}

	// The demote ERRORED: the twin is still presumed primary, so putting the
	// member back to explicit false restores the pre-state exactly.
	reverted := false
	_, rerr := store.ModifyBook(d.PromoteBookID, func(cur *database.Book) error {
		if strPtr(cur.VersionGroupID) != gid || cur.IsPrimaryVersion == nil || !*cur.IsPrimaryVersion {
			return database.ErrSkipBookWrite
		}
		cur.IsPrimaryVersion = &fls
		reverted = true
		return nil
	})
	if reverted && rerr == nil {
		d.Bucket = bucketDrifted
		d.Reason = fmt.Sprintf("demote of %s errored (%v); promotion reverted, pair unchanged", d.DemoteBookID, demoteErr)
		return
	}
	d.Bucket = bucketLeftDoublePrimary
	d.Reason = fmt.Sprintf("demote of %s errored (%v) and the revert of %s did not restore it (reverted=%t err=%v); version group %s now has TWO primaries and needs a manual fix",
		d.DemoteBookID, demoteErr, d.PromoteBookID, reverted, rerr, gid)
}

// recordRepointed buckets a finished pair AND logs it.
//
// With no undo journal, the whole reversal record is the SetResult payload,
// which is written once after every pair. A truncated or lost payload would take
// the record of ~1,426 flips with it, so each pair is also logged at Info as it
// lands, with both ids, both PRIOR tri-states and the version group — everything
// a manual reversal needs.
func (j *repointVersionPrimaryJob) recordRepointed(d *repointPairDecision, reason string) {
	d.Bucket = bucketRepointed
	d.Reason = reason
	chapterLog.Info("repoint-version-primary: repointed version_group=%s promote=%s (was %s) demote=%s (was %s): %s",
		d.VersionGroupID, d.PromoteBookID, d.PromotePriorFlag, d.DemoteBookID, d.DemotePriorFlag, reason)
}
