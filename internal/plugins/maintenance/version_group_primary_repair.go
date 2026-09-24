// file: internal/plugins/maintenance/version_group_primary_repair.go
// version: 1.0.0
// guid: 1cfccfec-8289-4d6a-8e2f-8a935d9ca4a5
// last-edited: 2026-09-24

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- version-group-primary-repair ---
//
// WHY THIS EXISTS. On 2026-09-23 prod had 2,788 version groups with no
// explicit primary and 4,821 with two or more (version-group-primary-report).
// Organize creates the library copy as non-primary when the group already has
// one; delete and merge paths then remove that primary without passing the
// flag on; several writers set true without demoting the rest; nothing
// enforces one primary per group. ABS lists only an organized primary, so both
// shapes hide or double books.
//
// This op repairs both shapes with versionprimary.Elect, the one rule for
// which copy is primary (m4b with chapters > m4b without > metadata > other
// formats; only a member ABS can show may win; a group whose better copy is
// only outside the library is HELD).
//
// Candidates are groups whose live members do not carry exactly one explicit
// true, or whose effective count (nil counts as primary, as the memdb index
// and the library list read it) is not exactly one. A group with one explicit
// true and every other live member explicit false is already correct and is
// not read.
//
// Guards:
//   - DRY RUN by default. The report lists every candidate group: each
//     member's state, tier and where its chapter count came from, metadata
//     score, files present or missing, merge target; the decision and its
//     reason; the fields carry-over would fill and the conflicts it would not.
//   - Apply needs explicit group_ids. It refuses while a library.scan is
//     queued or running, then holds the scan stand-down.
//   - Held groups are never written.
//   - Each group's members are re-read right before the writes. If the member
//     set, a flag, a merge target or a library state changed since the group
//     was planned (earlier in the SAME run; a dry run's plan is not carried
//     into a later apply), the group is skipped as changed_since_plan. Every
//     ModifyBook re-checks the same columns under the row lock.
//   - The winner is written first, then the others are demoted, so a failure
//     part way leaves a visible double rather than a hidden zero.
//   - Carry-over fills only the winner's EMPTY fields (owner decision
//     2026-09-24), through database.ApplyRespectingLocks so a field the user
//     locked stays as it is. The donor is not touched.
//   - Every changed field, is_primary_version included, gets a metadata
//     history row AFTER the write it describes, one batch id per book, so
//     "undo last apply" on a book reverts this op's change to it. If a row
//     cannot be recorded, an apply_incomplete marker is written so undo
//     refuses the batch rather than half-reverting it.
//   - No file I/O except a stat of each active file and an ffprobe header
//     read of single m4b/m4a files. No tags are written, nothing is moved,
//     no book_file row is touched.
//
// CONCURRENCY: registry.RunItems with Concurrency 4, one item per group.
// ffprobe is I/O-bound, and each group's members belong to that group only,
// so no two workers ever read-modify-write the same book.

const (
	vgRepairWorkers = 4
	// vgRepairDetailLimit caps the per-group detail in the result. It is set
	// above the ~7,600 broken groups prod had on 2026-09-23, so a full dry
	// run lists every group; the totals always cover all of them.
	vgRepairDetailLimit = 10000
	vgRepairSource      = "version-group-primary-repair"
	// vgRepairChangeType is the history change_type of this op's rows.
	vgRepairChangeType = "bulk_update"
	// vgRepairIncompleteType matches metafetch.ChangeTypeApplyIncomplete:
	// UndoLastApply refuses a batch carrying it. Spelled out rather than
	// imported so this plugin does not depend on the metafetch service.
	vgRepairIncompleteType = "apply_incomplete"
)

// Apply outcomes.
const (
	vgOutcomeApplied          = "applied"
	vgOutcomeChangedSincePlan = "changed_since_plan"
	vgOutcomePartial          = "partial_changed_since_plan"
	vgOutcomeFailed           = "failed"
)

var (
	errVGRepairStandDownLost = errors.New("version-group-primary-repair: scan stand-down lease lost; remaining work abandoned")
	errVGChangedSincePlan    = errors.New("version-group-primary-repair: row changed since the group was planned")
)

type vgPrimaryRepairParams struct {
	// Apply writes. Default false: report only.
	Apply bool `json:"apply"`
	// GroupIDs limits the run to these version groups. Required for apply.
	GroupIDs []string `json:"group_ids"`
}

type vgRepairGroupReport struct {
	GroupID string `json:"version_group_id"`
	versionprimary.Decision
	// ExplicitTrue / EffectivePrimaries are counted over live members as
	// read when the group was planned.
	ExplicitTrue       int                       `json:"explicit_true"`
	EffectivePrimaries int                       `json:"effective_primaries"`
	CarryOver          *versionprimary.CarryPlan `json:"carry_over,omitempty"`
	// MergeParticipantAmongPrimaries: a group with two or more explicit
	// primaries where one of those members carries a merge target.
	MergeParticipantAmongPrimaries bool   `json:"merge_participant_among_primaries,omitempty"`
	ChapterCountUnknown            bool   `json:"chapter_count_unknown,omitempty"`
	Outcome                        string `json:"outcome,omitempty"`
	Error                          string `json:"error,omitempty"`
}

type vgRepairReport struct {
	DryRun        bool `json:"dry_run"`
	TotalBooks    int  `json:"total_books"`
	GroupsScanned int  `json:"groups_scanned"`
	Candidates    int  `json:"candidates"`
	// ZeroExplicit / MultiExplicit split the candidates by explicit trues;
	// NilOnly are candidates with exactly one explicit true whose effective
	// count is off only because of nil flags.
	ZeroExplicit  int `json:"candidates_zero_explicit"`
	MultiExplicit int `json:"candidates_multi_explicit"`
	NilOnly       int `json:"candidates_nil_only"`
	// Requested ids that named no group, or a group that is not a candidate.
	RequestedUnmatched     []string       `json:"requested_unmatched,omitempty"`
	RequestedNotCandidate  []string       `json:"requested_not_candidate,omitempty"`
	ByDecision             map[string]int `json:"by_decision"`
	ByHoldReason           map[string]int `json:"by_hold_reason"`
	ContentNotMetadataBest int            `json:"content_winner_not_metadata_winner"`
	WithCarryOverFills     int            `json:"groups_with_carry_over_fills"`
	WithCarryOverConflicts int            `json:"groups_with_carry_over_conflicts"`
	MergeParticipantGroups int            `json:"multi_primary_groups_with_merge_participant"`
	ChapterUnknownGroups   int            `json:"groups_with_unknown_chapter_count"`
	Errors                 int            `json:"errors"`
	// Apply only.
	Applied          int    `json:"applied"`
	ChangedSincePlan int    `json:"changed_since_plan"`
	Partial          int    `json:"partial_changed_since_plan"`
	ApplyFailed      int    `json:"apply_failed"`
	FieldsFilled     int    `json:"fields_filled"`
	HistoryFailed    int    `json:"history_rows_failed"`
	Aborted          string `json:"aborted,omitempty"`
	// Groups is the per-group detail, ordered by group id, capped at
	// vgRepairDetailLimit.
	Groups          []vgRepairGroupReport `json:"groups"`
	GroupsTruncated bool                  `json:"groups_truncated,omitempty"`
}

func (r *vgRepairReport) summary() string {
	s := fmt.Sprintf("dry_run=%v books=%d groups=%d candidates=%d (zero_explicit=%d multi_explicit=%d nil_only=%d) "+
		"decisions=%v holds=%v content_not_metadata_best=%d carry_fills=%d carry_conflicts=%d "+
		"merge_participant=%d chapter_unknown=%d errors=%d applied=%d changed_since_plan=%d partial=%d "+
		"apply_failed=%d fields_filled=%d history_failed=%d",
		r.DryRun, r.TotalBooks, r.GroupsScanned, r.Candidates, r.ZeroExplicit, r.MultiExplicit, r.NilOnly,
		r.ByDecision, r.ByHoldReason, r.ContentNotMetadataBest, r.WithCarryOverFills, r.WithCarryOverConflicts,
		r.MergeParticipantGroups, r.ChapterUnknownGroups, r.Errors, r.Applied, r.ChangedSincePlan, r.Partial,
		r.ApplyFailed, r.FieldsFilled, r.HistoryFailed)
	if r.Aborted != "" {
		s += " ABORTED: " + r.Aborted
	}
	return s
}

func (p *Plugin) versionGroupPrimaryRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.version-group-primary-repair",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Version-group primary repair",
		Description: "Repairs version groups with no primary or more than one, using the shared primary rule: " +
			"only a live, organized copy with all its files present under the library root can be primary, " +
			"ranked m4b with chapters > m4b without > metadata > other formats. Groups whose better copy is " +
			"outside the library, or with no such copy, are held and never written. The winner's EMPTY fields " +
			"are filled from the best-metadata copy; nothing is overwritten. DRY-RUN BY DEFAULT: apply=true " +
			"needs group_ids, refuses while library.scan runs, and records metadata history after each write.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.version-group-primary-repair",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runVersionGroupPrimaryRepair,
	}
}

func (p *Plugin) runVersionGroupPrimaryRepair(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params vgPrimaryRepairParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	prober, perr := versionprimary.FFprobeChapterCounter()
	if perr != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
			"version-group-primary-repair: ffprobe unavailable (%v); chapter counts come from the chapter table", perr))
		prober = nil
	}
	report, err := p.versionGroupPrimaryRepair(ctx, params, config.AppConfig.RootDir, prober, reporter)
	if report != nil {
		if serr := registry.ReporterSetResult(reporter, report); serr != nil {
			reporter.Logger().Warn("version-group-primary-repair: report not persisted", "err", serr)
		}
	}
	return err
}

// vgCandidateCounts is a group's primary tally over its live members.
type vgCandidateCounts struct{ live, explicitTrue, effective int }

func (c vgCandidateCounts) candidate() bool {
	return c.live > 0 && (c.explicitTrue != 1 || c.effective != 1)
}

func normalizeGroupIDs(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (p *Plugin) versionGroupPrimaryRepair(ctx context.Context, params vgPrimaryRepairParams, rootDir string,
	prober versionprimary.ChapterProber, reporter sdk.Reporter) (*vgRepairReport, error) {
	store := p.deps.OpsStore()
	vps := p.deps.VersionPrimaryStore()
	if store == nil || vps == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	requested := normalizeGroupIDs(params.GroupIDs)
	report := &vgRepairReport{DryRun: !params.Apply, ByDecision: map[string]int{}, ByHoldReason: map[string]int{}}
	if params.Apply && len(requested) == 0 {
		return report, fmt.Errorf("version-group-primary-repair: apply needs explicit group_ids; run the dry run and pick them from its report")
	}
	opID := registry.ReporterOpID(reporter)
	if params.Apply && opID == "" {
		return report, fmt.Errorf("version-group-primary-repair: no operation id; refusing to apply")
	}
	log := reporter.Logger()

	_ = reporter.UpdateProgress(0, 3, "Listing books…")
	// One consistent snapshot; the Complete variant refuses a memdb known to
	// be missing rows, which would make a group look short.
	books, err := store.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		return report, fmt.Errorf("list books: %w", err)
	}
	report.TotalBooks = len(books)
	liveIDs := make(map[string]bool, len(books))
	for i := range books {
		liveIDs[books[i].ID] = !books[i].IsSoftDeleted()
	}
	snapAlive := func(id string) bool { return liveIDs[id] }
	counts := map[string]*vgCandidateCounts{}
	for i := range books {
		b := &books[i]
		if b.VersionGroupID == nil || *b.VersionGroupID == "" || b.IsSoftDeleted() {
			continue
		}
		c := counts[*b.VersionGroupID]
		if c == nil {
			c = &vgCandidateCounts{}
			counts[*b.VersionGroupID] = c
		}
		if !versionprimary.ElectableRow(b.IsSoftDeleted(), b.MergedIntoBookID, snapAlive) {
			continue
		}
		c.live++
		if b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
			c.explicitTrue++
		}
		if database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
			c.effective++
		}
	}
	report.GroupsScanned = len(counts)

	var work []string
	if len(requested) > 0 {
		for _, gid := range requested {
			c, ok := counts[gid]
			switch {
			case !ok:
				report.RequestedUnmatched = append(report.RequestedUnmatched, gid)
			case !c.candidate():
				report.RequestedNotCandidate = append(report.RequestedNotCandidate, gid)
			default:
				work = append(work, gid)
			}
		}
	} else {
		for gid, c := range counts {
			if c.candidate() {
				work = append(work, gid)
			}
		}
		sort.Strings(work)
	}
	report.Candidates = len(work)
	for _, gid := range work {
		switch c := counts[gid]; {
		case c.explicitTrue == 0:
			report.ZeroExplicit++
		case c.explicitTrue > 1:
			report.MultiExplicit++
		default:
			report.NilOnly++
		}
	}
	log.Info("version-group-primary-repair candidates", "apply", params.Apply, "candidates", len(work),
		"requested", len(requested), "unmatched", len(report.RequestedUnmatched), "not_candidate", len(report.RequestedNotCandidate))
	if len(work) == 0 {
		_ = reporter.UpdateProgress(3, 3, "nothing to repair — "+report.summary())
		return report, nil
	}

	a := &vgApplier{store: store, history: vps, reporter: reporter, apply: params.Apply}
	holderID, held := "", false
	if params.Apply {
		if err := refuseWhileLibraryScanActive(p.deps.OperationQueueStore(), "version-group-primary-repair"); err != nil {
			return report, err
		}
		var release func()
		var sdErr error
		holderID, held, release, sdErr = acquireScanStandDownForApply(ctx, p.deps, reporter, "version-group-primary-repair apply")
		if sdErr != nil {
			return report, fmt.Errorf("version-group-primary-repair: could not acquire scan stand-down; refusing to write: %w", sdErr)
		}
		defer release()
	}

	loader := versionprimary.Loader{Files: store, Chapters: vps, RootDir: rootDir, Probe: prober}
	var (
		mu      sync.Mutex
		groups  []vgRepairGroupReport
		lost    atomic.Bool
		planned atomic.Int64
	)
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Ranking %d groups…", len(work)))
	runErr := registry.RunItems(ctx, reporter, work, func(gctx context.Context, gid string) error {
		defer planned.Add(1)
		if lost.Load() {
			return nil
		}
		if params.Apply && scanStandDownLostForApply(p.deps, holderID, held) {
			lost.Store(true)
			return nil
		}
		g := a.planAndApply(gctx, loader, gid)
		mu.Lock()
		groups = append(groups, g)
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		// Set explicitly: RunItems defaults to one worker.
		Concurrency: vgRepairWorkers,
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(i, total int) string {
			return fmt.Sprintf("Groups %d/%d", planned.Load(), total)
		},
	})

	sort.Slice(groups, func(i, j int) bool { return groups[i].GroupID < groups[j].GroupID })
	for i := range groups {
		report.tally(&groups[i])
	}
	if len(groups) > vgRepairDetailLimit {
		groups, report.GroupsTruncated = groups[:vgRepairDetailLimit], true
	}
	report.Groups = groups
	report.FieldsFilled = int(a.fieldsFilled.Load())
	report.HistoryFailed = int(a.historyFailed.Load())
	if lost.Load() {
		report.Aborted = errVGRepairStandDownLost.Error()
		_ = reporter.UpdateProgress(3, 3, report.summary())
		return report, errVGRepairStandDownLost
	}
	if runErr != nil {
		return report, runErr
	}
	prefix := "DRY RUN (nothing written) — "
	if params.Apply {
		prefix = "APPLIED — "
	}
	log.Info("version-group-primary-repair complete", "summary", report.summary())
	_ = reporter.UpdateProgress(3, 3, prefix+report.summary())
	return report, nil
}

func (r *vgRepairReport) tally(g *vgRepairGroupReport) {
	if g.Error != "" && g.Kind == "" {
		r.Errors++
		return
	}
	r.ByDecision[g.Kind]++
	if g.HoldReason != "" {
		r.ByHoldReason[g.HoldReason]++
	}
	if g.WinnerID != "" && g.MetadataBestID != "" && g.WinnerID != g.MetadataBestID {
		r.ContentNotMetadataBest++
	}
	if g.CarryOver != nil && len(g.CarryOver.Fills) > 0 {
		r.WithCarryOverFills++
	}
	if g.CarryOver != nil && len(g.CarryOver.Conflicts) > 0 {
		r.WithCarryOverConflicts++
	}
	if g.MergeParticipantAmongPrimaries {
		r.MergeParticipantGroups++
	}
	if g.ChapterCountUnknown {
		r.ChapterUnknownGroups++
	}
	switch g.Outcome {
	case vgOutcomeApplied:
		r.Applied++
	case vgOutcomeChangedSincePlan:
		r.ChangedSincePlan++
	case vgOutcomePartial:
		r.Partial++
	case vgOutcomeFailed:
		r.ApplyFailed++
	}
}

// vgApplier plans and (on apply) writes one group at a time. Workers share it;
// its counters are atomics and it holds no per-group state.
type vgApplier struct {
	store    OpsStore
	history  VersionPrimaryStore
	reporter sdk.Reporter
	apply    bool

	fieldsFilled  atomic.Int64
	historyFailed atomic.Int64
}

// vgMemberKey is what must not change between planning and writing.
type vgMemberKey struct {
	primary, mergedInto, state string
	softDeleted                bool
}

func vgKeyOf(b *database.Book) vgMemberKey {
	k := vgMemberKey{primary: storedPrimaryFlag(b.IsPrimaryVersion), softDeleted: b.IsSoftDeleted()}
	if b.MergedIntoBookID != nil {
		k.mergedInto = *b.MergedIntoBookID
	}
	if b.LibraryState != nil {
		k.state = *b.LibraryState
	}
	return k
}

func vgKeys(members []database.Book) map[string]vgMemberKey {
	out := make(map[string]vgMemberKey, len(members))
	for i := range members {
		out[members[i].ID] = vgKeyOf(&members[i])
	}
	return out
}

func sameVGKeys(a, b map[string]vgMemberKey) bool {
	if len(a) != len(b) {
		return false
	}
	for id, k := range a {
		if b[id] != k {
			return false
		}
	}
	return true
}

func (a *vgApplier) planAndApply(ctx context.Context, loader versionprimary.Loader, gid string) vgRepairGroupReport {
	g := vgRepairGroupReport{GroupID: gid}
	members, err := a.store.GetBooksByVersionGroup(gid)
	if err != nil {
		g.Error = "read group: " + err.Error()
		return g
	}
	alive := vgStoreAlive(a.store)
	ms, err := loader.LoadMembers(ctx, members, alive)
	if err != nil {
		g.Error = "load signals: " + err.Error()
		return g
	}
	g.Decision = versionprimary.Elect(ms)
	byID := make(map[string]*database.Book, len(members))
	for i := range members {
		byID[members[i].ID] = &members[i]
	}
	multi := 0
	mergeAmongPrimaries := false
	for _, m := range g.Members {
		if m.ChapterSource == versionprimary.ChapterSourceUnknown {
			g.ChapterCountUnknown = true
		}
		if m.StoredPrimary == "true" && m.MergedInto != "" {
			mergeAmongPrimaries = true
		}
		if m.StoredPrimary == "true" {
			multi++
		}
		if !m.Live {
			continue
		}
		if m.StoredPrimary == "true" {
			g.ExplicitTrue++
		}
		if m.StoredPrimary != "false" {
			g.EffectivePrimaries++
		}
	}
	g.MergeParticipantAmongPrimaries = multi > 1 && mergeAmongPrimaries
	if g.WinnerID != "" && g.MetadataBestID != "" && g.MetadataBestID != g.WinnerID {
		cp := versionprimary.PlanCarryOver(byID[g.WinnerID], byID[g.MetadataBestID])
		g.CarryOver = &cp
	}
	if !a.apply || g.Kind != versionprimary.DecisionElect {
		return g
	}
	a.write(gid, members, &g)
	return g
}

// vgStoreAlive checks a merge target with a point read. A read error counts
// as alive, which keeps the loser ineligible: the group is then left as it is
// rather than crowning a book that may still be merged away.
func vgStoreAlive(store OpsStore) func(string) bool {
	return func(id string) bool {
		b, err := store.GetBookByID(id)
		if err != nil {
			return true
		}
		return b != nil && !b.IsSoftDeleted()
	}
}

// write applies one elect decision. Winner first, then the demotions.
func (a *vgApplier) write(gid string, planned []database.Book, g *vgRepairGroupReport) {
	fresh, err := a.store.GetBooksByVersionGroup(gid)
	if err != nil {
		g.Outcome, g.Error = vgOutcomeFailed, "re-read group: "+err.Error()
		return
	}
	plannedKeys := vgKeys(planned)
	if !sameVGKeys(plannedKeys, vgKeys(fresh)) {
		g.Outcome = vgOutcomeChangedSincePlan
		return
	}
	var donor *database.Book
	if g.CarryOver != nil {
		for i := range fresh {
			if fresh[i].ID == g.MetadataBestID {
				donor = &fresh[i]
			}
		}
	}

	// Winner: explicit true plus carry-over.
	filled, err := a.modify(g.WinnerID, plannedKeys[g.WinnerID], true, donor)
	if err != nil {
		if errors.Is(err, errVGChangedSincePlan) {
			g.Outcome = vgOutcomeChangedSincePlan
		} else {
			g.Outcome, g.Error = vgOutcomeFailed, fmt.Sprintf("write winner %s: %v", g.WinnerID, err)
		}
		return
	}
	a.fieldsFilled.Add(int64(filled))

	// Every other live member: explicit false.
	for _, m := range g.Members {
		if m.BookID == g.WinnerID || !m.Live || m.StoredPrimary == "false" {
			continue
		}
		if _, err := a.modify(m.BookID, plannedKeys[m.BookID], false, nil); err != nil {
			if errors.Is(err, errVGChangedSincePlan) {
				g.Outcome = vgOutcomePartial
			} else {
				g.Outcome, g.Error = vgOutcomeFailed, fmt.Sprintf("demote %s: %v", m.BookID, err)
			}
			return
		}
	}
	g.Outcome = vgOutcomeApplied
}

// vgHistoryFields is every column this op can change, in history order.
var vgHistoryFields = []string{
	"is_primary_version", "description", "narrator", "publisher", "language", "genre",
	"cover_url", "isbn10", "isbn13", "asin", "subtitle", "edition",
}

// modify sets one member's flag (and, for the winner, fills its empty fields
// from donor) inside ModifyBook, then records history for what changed.
// Returns the number of carried fields written.
func (a *vgApplier) modify(bookID string, want vgMemberKey, primary bool, donor *database.Book) (int, error) {
	var before, after *database.Book
	written, err := a.store.ModifyBook(bookID, func(row *database.Book) error {
		if vgKeyOf(row) != want {
			return errVGChangedSincePlan
		}
		snap, serr := database.SnapshotBook(row)
		if serr != nil {
			return serr
		}
		if donor != nil {
			if _, lerr := database.ApplyRespectingLocks(a.store, row, func(b *database.Book) {
				versionprimary.ApplyCarryOver(b, donor)
			}); lerr != nil {
				return lerr
			}
		}
		flag := primary
		row.IsPrimaryVersion = &flag
		post, perr := database.SnapshotBook(row)
		if perr != nil {
			return perr
		}
		// Captured on every invocation, so a retried callback leaves the
		// values of the attempt that committed.
		before, after = snap, post
		return nil
	})
	if err != nil {
		return 0, err
	}
	if written == nil {
		return 0, fmt.Errorf("book %s vanished before the write", bookID)
	}
	return a.recordHistory(bookID, before, after), nil
}

// recordHistory writes one metadata-history row per changed field, AFTER the
// write it describes, all under one batch id so undo-last-apply reverts them
// together. A failed row marks the batch incomplete so undo refuses it.
// Returns how many carried (non-flag) fields changed.
func (a *vgApplier) recordHistory(bookID string, before, after *database.Book) int {
	batchID := "vgpr-" + ulid.Make().String()
	now := time.Now()
	carried, failed := 0, 0
	for _, field := range vgHistoryFields {
		oldV, oerr := database.RenderBookField(before, field)
		newV, nerr := database.RenderBookField(after, field)
		if oerr != nil || nerr != nil || oldV == newV {
			continue
		}
		if field != "is_primary_version" {
			carried++
		}
		oldJSON, newJSON := vgJSONString(oldV), vgJSONString(newV)
		if err := a.history.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID:        bookID,
			Field:         field,
			PreviousValue: &oldJSON,
			NewValue:      &newJSON,
			ChangeType:    vgRepairChangeType,
			Source:        vgRepairSource,
			ChangedAt:     now,
			BatchID:       batchID,
		}); err != nil {
			failed++
			a.reporter.Logger().Warn("version-group-primary-repair: history row not recorded (the write itself committed)",
				"book_id", bookID, "field", field, "err", err)
		}
	}
	if failed > 0 {
		a.historyFailed.Add(int64(failed))
		if err := a.history.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID: bookID, Field: "apply", ChangeType: vgRepairIncompleteType,
			Source: vgRepairSource, ChangedAt: now, BatchID: batchID,
		}); err != nil {
			a.reporter.Logger().Error("version-group-primary-repair: neither the history nor the incomplete marker was recorded",
				"book_id", bookID, "batch_id", batchID, "err", err)
		}
	}
	return carried
}

func vgJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
