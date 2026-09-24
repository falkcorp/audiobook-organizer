// file: internal/plugins/maintenance/itunes_clone_into_library.go
// version: 1.0.1
// guid: 9c4e1b27-6a3f-4d80-b5e2-3f7a0c8d1e64
// last-edited: 2026-09-24

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- itunes-clone-into-library ---
//
// WHY THIS EXISTS. After the 2026-09-24 version-group repair, 552 held groups
// had a live `organized` member whose files are all (513) or partly (55) in
// the iTunes library under books/itunes/**, outside the library root. ABS
// lists only a copy under the root, so those books are invisible. The owner
// chose (2026-09-24) to CLONE them into the library rather than move them:
// iTunes files are never mutated.
//
//   - kind "version" (every active file under iTunes): reflink the files into
//     the organize destination and create the library copy as a new version
//     row through the organize service (CreateOrganizedVersion: the iTunes PID
//     moves to the new rows, the source becomes organized_source, the group's
//     primary is handed on). Owner: the library copy owns the PID.
//   - kind "mixed" (some files already in the library folder): reflink only
//     the iTunes-side files into that same folder and repoint those rows.
//     Any conflict (the planned folder is not the book's library folder, a
//     destination exists) and the group is reported, never written.
//
// REFLINK ONLY. No copy fallback (copy_file_range full-copies silently across
// datasets), no hardlink (it would share the iTunes file's inode).
//
// ROLLBACK. Each clone writes a durable record (a `_system` preference keyed
// by group) of exactly what it changed: the new book, each PID's old and new
// row, each repointed row's old path, the source's prior state. mode rollback
// with group_ids reverses from the record, never by inference.
//
// Guards: dry run by default; apply and rollback need explicit group_ids;
// refuses while library.scan runs and holds the scan stand-down; Doctor Who /
// Big Finish / Torchwood are never touched (applygate.IsOwnerManualOnly);
// each group is re-read before writing and skipped if it changed.
//
// CONCURRENCY: registry.RunItems, Concurrency 4, one item per group. Groups
// are disjoint sets of books, so no two workers touch the same row. The work
// is I/O on one pool; four is the same bound the repair op uses.

const (
	icWorkers      = 4
	icRecordPrefix = "itunes_clone_record:"
	icSource       = "itunes-clone-into-library"

	icKindVersion = "version"
	icKindMixed   = "mixed"

	icDecisionClone  = "clone"
	icDecisionSkip   = "skip"
	icOutcomeApplied = "applied"
	icOutcomeChanged = "changed_since_plan"
	icOutcomeFailed  = "failed"
	icOutcomeNoUndo  = "applied_without_rollback_record"
	icOutcomeRolled  = "rolled_back"
)

type icParams struct {
	Apply    bool     `json:"apply"`
	Rollback bool     `json:"rollback"`
	GroupIDs []string `json:"group_ids"`
}

// icPair is one file of a clone: the source row, the row that now holds its
// place, the PID that moved (if any), and the two paths.
type icPair struct {
	SourceFileID string `json:"source_file_id"`
	CloneFileID  string `json:"clone_file_id,omitempty"`
	PID          string `json:"pid,omitempty"`
	SourcePath   string `json:"source_path"`
	ClonePath    string `json:"clone_path"`
	// OldITunesPath is the mixed repoint's prior ITunesPath.
	OldITunesPath string `json:"old_itunes_path,omitempty"`
}

// icRecord is the durable undo record of one group's clone.
type icRecord struct {
	GroupID            string    `json:"group_id"`
	Kind               string    `json:"kind"`
	SourceBookID       string    `json:"source_book_id"`
	CloneBookID        string    `json:"clone_book_id,omitempty"`
	SourcePriorState   string    `json:"source_prior_state"`
	SourcePriorPrimary string    `json:"source_prior_primary"`
	Pairs              []icPair  `json:"pairs"`
	OpID               string    `json:"op_id"`
	At                 time.Time `json:"at"`
}

type icGroupReport struct {
	GroupID      string   `json:"version_group_id"`
	Decision     string   `json:"decision"`
	Kind         string   `json:"kind,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	SourceBookID string   `json:"source_book_id,omitempty"`
	Title        string   `json:"title,omitempty"`
	Files        int      `json:"files,omitempty"`
	Bytes        int64    `json:"bytes,omitempty"`
	Destinations []string `json:"destinations,omitempty"`
	CloneBookID  string   `json:"clone_book_id,omitempty"`
	Outcome      string   `json:"outcome,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type icReport struct {
	DryRun     bool            `json:"dry_run"`
	Rollback   bool            `json:"rollback"`
	Candidates int             `json:"candidates"`
	ByDecision map[string]int  `json:"by_decision"`
	ByKind     map[string]int  `json:"by_kind"`
	BySkip     map[string]int  `json:"by_skip_reason"`
	ByOutcome  map[string]int  `json:"by_outcome"`
	Bytes      int64           `json:"bytes_to_clone"`
	Aborted    string          `json:"aborted,omitempty"`
	Groups     []icGroupReport `json:"groups"`
}

func (r *icReport) summary() string {
	s := fmt.Sprintf("dry_run=%v rollback=%v candidates=%d decisions=%v kinds=%v skips=%v outcomes=%v bytes=%d",
		r.DryRun, r.Rollback, r.Candidates, r.ByDecision, r.ByKind, r.BySkip, r.ByOutcome, r.Bytes)
	if r.Aborted != "" {
		s += " ABORTED: " + r.Aborted
	}
	return s
}

func (p *Plugin) itunesCloneIntoLibraryDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.itunes-clone-into-library",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Clone iTunes-only books into the library",
		Description: "For held version groups whose only organized copy lives in the iTunes library, reflinks the " +
			"files into the library root (reflink only: never a copy or hardlink; iTunes files are never " +
			"modified) and makes the library copy primary. Books with some files already in the library get " +
			"only the iTunes-side files cloned into that folder. DRY-RUN BY DEFAULT: apply=true needs group_ids " +
			"and refuses while library.scan runs; rollback=true with group_ids reverses a clone from its record.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.itunes-clone-into-library",
		Cancellable:     true,
		Timeout:         4 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runITunesCloneIntoLibrary,
	}
}

func (p *Plugin) runITunesCloneIntoLibrary(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params icParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	report, err := p.itunesCloneIntoLibrary(ctx, params, config.AppConfig.RootDir, reporter)
	if report != nil {
		if serr := registry.ReporterSetResult(reporter, report); serr != nil {
			reporter.Logger().Warn("itunes-clone-into-library: report not persisted", "err", serr)
		}
	}
	return err
}

// icStore is what the op reads and writes: the ops store plus the chapter
// table versionprimary's loader and hand-off need.
type icStore struct {
	OpsStore
	database.ChapterReader
}

type icRunner struct {
	store   icStore
	cloner  LibraryCloner
	rootDir string
	opID    string
	apply   bool
}

func (p *Plugin) itunesCloneIntoLibrary(ctx context.Context, params icParams, rootDir string, reporter sdk.Reporter) (*icReport, error) {
	ops, vps := p.deps.OpsStore(), p.deps.VersionPrimaryStore()
	if ops == nil || vps == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	requested := normalizeGroupIDs(params.GroupIDs)
	report := &icReport{DryRun: !params.Apply && !params.Rollback, Rollback: params.Rollback,
		ByDecision: map[string]int{}, ByKind: map[string]int{}, BySkip: map[string]int{}, ByOutcome: map[string]int{}}
	if params.Apply && params.Rollback {
		return report, fmt.Errorf("itunes-clone-into-library: apply and rollback are exclusive")
	}
	if (params.Apply || params.Rollback) && len(requested) == 0 {
		return report, fmt.Errorf("itunes-clone-into-library: apply and rollback need explicit group_ids; pick them from the dry run")
	}
	if rootDir == "" {
		return report, fmt.Errorf("itunes-clone-into-library: root_dir is not set")
	}
	opID := registry.ReporterOpID(reporter)
	writes := params.Apply || params.Rollback
	if writes && opID == "" {
		return report, fmt.Errorf("itunes-clone-into-library: no operation id; refusing to write")
	}
	run := &icRunner{store: icStore{OpsStore: ops, ChapterReader: vps}, cloner: p.deps, rootDir: rootDir, opID: opID, apply: params.Apply}

	work := requested
	if len(work) == 0 {
		var err error
		if work, err = run.discover(); err != nil {
			return report, err
		}
	}
	report.Candidates = len(work)

	holderID, held := "", false
	if writes {
		if err := refuseWhileLibraryScanActive(p.deps.OperationQueueStore(), "itunes-clone-into-library"); err != nil {
			return report, err
		}
		var release func()
		var sdErr error
		holderID, held, release, sdErr = acquireScanStandDownForApply(ctx, p.deps, reporter, "itunes-clone-into-library")
		if sdErr != nil {
			return report, fmt.Errorf("itunes-clone-into-library: could not acquire scan stand-down; refusing to write: %w", sdErr)
		}
		defer release()
	}

	var (
		mu     sync.Mutex
		groups []icGroupReport
		lost   atomic.Bool
		done   atomic.Int64
	)
	runErr := registry.RunItems(ctx, reporter, work, func(gctx context.Context, gid string) error {
		defer done.Add(1)
		if lost.Load() {
			return nil
		}
		if writes && scanStandDownLostForApply(p.deps, holderID, held) {
			lost.Store(true)
			return nil
		}
		var g icGroupReport
		if params.Rollback {
			g = run.rollback(gctx, gid)
		} else {
			g = run.planAndApply(gctx, gid)
		}
		mu.Lock()
		groups = append(groups, g)
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		// Set explicitly: RunItems defaults to one worker.
		Concurrency: icWorkers,
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Groups %d/%d", done.Load(), total) },
	})

	sort.Slice(groups, func(i, j int) bool { return groups[i].GroupID < groups[j].GroupID })
	for i := range groups {
		g := &groups[i]
		report.ByDecision[g.Decision]++
		if g.Kind != "" {
			report.ByKind[g.Kind]++
		}
		if g.Decision == icDecisionSkip {
			report.BySkip[g.Reason]++
		}
		if g.Outcome != "" {
			report.ByOutcome[g.Outcome]++
		}
		if g.Decision == icDecisionClone {
			report.Bytes += g.Bytes
		}
	}
	report.Groups = groups
	if lost.Load() {
		report.Aborted = errVGRepairStandDownLost.Error()
		return report, errors.New("itunes-clone-into-library: scan stand-down lost; remaining groups abandoned")
	}
	if runErr != nil {
		return report, runErr
	}
	reporter.Logger().Info("itunes-clone-into-library complete", "summary", report.summary())
	_ = reporter.UpdateProgress(1, 1, report.summary())
	return report, nil
}

// discover lists groups with a live organized member that is iTunes-linked
// (a PID, an iTunes import source, or a path under books/itunes). The plan
// then reads each one's files and decides.
func (r *icRunner) discover() ([]string, error) {
	books, err := r.store.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for i := range books {
		b := &books[i]
		if b.VersionGroupID == nil || *b.VersionGroupID == "" || b.IsSoftDeleted() || seen[*b.VersionGroupID] {
			continue
		}
		if b.LibraryState == nil || *b.LibraryState != "organized" {
			continue
		}
		linked := (b.ITunesPersistentID != nil && *b.ITunesPersistentID != "") ||
			(b.ITunesImportSource != nil && *b.ITunesImportSource != "") || authorPathLinkIsITunes(b.FilePath)
		if linked {
			seen[*b.VersionGroupID] = true
			out = append(out, *b.VersionGroupID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// icPlan is one group's decided clone.
type icPlan struct {
	members []database.Book
	source  *database.Book
	files   []database.BookFile // the source's active rows
	dests   []string            // planned library path per files[i]
	kind    string
}

func skipped(g icGroupReport, reason string) icGroupReport {
	g.Decision, g.Reason = icDecisionSkip, reason
	return g
}

func (r *icRunner) plan(ctx context.Context, gid string) (icGroupReport, *icPlan) {
	g := icGroupReport{GroupID: gid}
	members, err := r.store.GetBooksByVersionGroup(gid)
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "read group: "+err.Error()
		return g, nil
	}
	loader := versionprimary.Loader{Files: r.store, Chapters: r.store, RootDir: r.rootDir}
	ms, err := loader.LoadMembers(ctx, members, vgStoreAlive(r.store))
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "load signals: "+err.Error()
		return g, nil
	}
	d := versionprimary.Elect(ms)
	if d.Kind != versionprimary.DecisionHeld {
		return skipped(g, "group_not_held"), nil
	}
	// The source: the best-tier live organized member whose only reason for
	// being ineligible is files outside the library root.
	var src *versionprimary.MemberEval
	for i := range d.Members {
		e := &d.Members[i]
		if !e.Live || e.LibraryState != "organized" || e.Ineligible != versionprimary.IneligibleOutsideLibrary {
			continue
		}
		if src == nil || e.Tier > src.Tier || (e.Tier == src.Tier && e.MetadataScore > src.MetadataScore) {
			src = e
		}
	}
	if src == nil {
		return skipped(g, "no_organized_member_outside_library"), nil
	}
	if d.BestTier > src.Tier {
		return skipped(g, "better_copy_elsewhere"), nil
	}
	var book *database.Book
	for i := range members {
		if members[i].ID == src.BookID {
			book = &members[i]
		}
	}
	g.SourceBookID, g.Title = book.ID, book.Title
	if applygate.IsOwnerManualOnly(book.FilePath, "") || applygate.IsOwnerManualOnly(book.Title, "") {
		return skipped(g, "owner_manual_only"), nil
	}
	all, err := r.store.GetBookFiles(book.ID)
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "read files: "+err.Error()
		return g, nil
	}
	var active []database.BookFile
	nITunes, nRoot := 0, 0
	for _, f := range all {
		if f.Missing {
			return skipped(g, "has_missing_rows"), nil
		}
		if applygate.IsOwnerManualOnly(f.FilePath, "") {
			return skipped(g, "owner_manual_only"), nil
		}
		active = append(active, f)
		switch {
		case authorPathLinkIsITunes(f.FilePath):
			nITunes++
		case pathutil.IsWithin(f.FilePath, r.rootDir):
			nRoot++
		}
	}
	if len(active) == 0 || nITunes == 0 || nITunes+nRoot != len(active) {
		return skipped(g, "files_not_itunes_or_library"), nil
	}
	dests, err := r.cloner.PlanLibraryClone(book, active)
	if err != nil {
		g.Decision, g.Error = icDecisionSkip, "plan destinations: "+err.Error()
		return g, nil
	}
	p := &icPlan{members: members, source: book, files: active, dests: dests, kind: icKindVersion}
	if nRoot > 0 {
		p.kind = icKindMixed
		if reason := mixedConflict(active, dests, r.rootDir); reason != "" {
			g.Kind = icKindMixed
			return skipped(g, reason), nil
		}
	}
	g.Kind, g.Files, g.Destinations = p.kind, len(active), dests
	for i, f := range active {
		if !authorPathLinkIsITunes(f.FilePath) {
			continue
		}
		if !pathutil.IsWithin(dests[i], r.rootDir) {
			return skipped(g, "destination_outside_root"), nil
		}
		if _, err := os.Lstat(dests[i]); err == nil {
			return skipped(g, "destination_exists"), nil
		}
		g.Bytes += f.FileSize
	}
	g.Decision = icDecisionClone
	return g, p
}

// mixedConflict returns why a mixed book cannot be completed in place, or "".
// Every library-side file must already sit at its planned path (so the plan's
// folder IS the book's library folder), and every iTunes file must land in
// that same folder.
func mixedConflict(files []database.BookFile, dests []string, root string) string {
	dir := ""
	for i, f := range files {
		if authorPathLinkIsITunes(f.FilePath) {
			continue
		}
		if filepath.Clean(dests[i]) != filepath.Clean(f.FilePath) {
			return "mixed_library_files_not_at_planned_path"
		}
		d := filepath.Dir(f.FilePath)
		if dir != "" && d != dir {
			return "mixed_library_files_in_several_folders"
		}
		dir = d
	}
	for i, f := range files {
		if authorPathLinkIsITunes(f.FilePath) && filepath.Dir(dests[i]) != dir {
			return "mixed_itunes_files_planned_elsewhere"
		}
	}
	return ""
}

func (r *icRunner) planAndApply(ctx context.Context, gid string) icGroupReport {
	g, p := r.plan(ctx, gid)
	if p == nil || !r.apply {
		return g
	}
	fresh, err := r.store.GetBooksByVersionGroup(gid)
	if err != nil {
		g.Outcome, g.Error = icOutcomeFailed, "re-read group: "+err.Error()
		return g
	}
	if !sameVGKeys(vgKeys(p.members), vgKeys(fresh)) {
		g.Outcome = icOutcomeChanged
		return g
	}
	if p.kind == icKindMixed {
		return r.applyMixed(ctx, g, p)
	}
	return r.applyVersion(g, p)
}

func (r *icRunner) applyVersion(g icGroupReport, p *icPlan) icGroupReport {
	rec := icRecord{GroupID: g.GroupID, Kind: icKindVersion, SourceBookID: p.source.ID,
		SourcePriorState: derefString(p.source.LibraryState), SourcePriorPrimary: storedPrimaryFlag(p.source.IsPrimaryVersion),
		OpID: r.opID, At: time.Now()}
	byPID := map[string]database.BookFile{}
	for _, f := range p.files {
		if f.ITunesPersistentID != "" {
			byPID[f.ITunesPersistentID] = f
		}
	}
	newID, err := r.cloner.CloneBookIntoLibrary(p.source, p.files, r.opID)
	if err != nil {
		g.Outcome, g.Error = icOutcomeFailed, err.Error()
		return g
	}
	g.CloneBookID = newID
	rec.CloneBookID = newID
	landed, err := r.store.GetBookFiles(newID)
	if err != nil {
		g.Outcome, g.Error = icOutcomeNoUndo, "read clone rows: "+err.Error()
		return g
	}
	want := map[string]bool{}
	for _, d := range p.dests {
		want[filepath.Clean(d)] = true
	}
	offPlan := len(landed) != len(p.dests)
	for _, f := range landed {
		pair := icPair{CloneFileID: f.ID, ClonePath: f.FilePath, PID: f.ITunesPersistentID}
		if src, ok := byPID[f.ITunesPersistentID]; ok && f.ITunesPersistentID != "" {
			pair.SourceFileID, pair.SourcePath = src.ID, src.FilePath
		}
		rec.Pairs = append(rec.Pairs, pair)
		if !want[filepath.Clean(f.FilePath)] {
			offPlan = true
		}
	}
	if err := r.saveRecord(&rec); err != nil {
		g.Outcome, g.Error = icOutcomeNoUndo, "rollback record not saved: "+err.Error()
		return g
	}
	if offPlan {
		// A destination appeared after the plan (organize takes a _copyN
		// name then): undo rather than keep a copy nobody planned.
		rb := r.undo(context.Background(), &rec)
		g.Outcome, g.Error = icOutcomeFailed, "landed off plan; rolled back: "+rb.Outcome+" "+rb.Error
		return g
	}
	g.Outcome = icOutcomeApplied
	return g
}

func (r *icRunner) applyMixed(ctx context.Context, g icGroupReport, p *icPlan) icGroupReport {
	rec := icRecord{GroupID: g.GroupID, Kind: icKindMixed, SourceBookID: p.source.ID,
		SourcePriorState: derefString(p.source.LibraryState), SourcePriorPrimary: storedPrimaryFlag(p.source.IsPrimaryVersion),
		OpID: r.opID, At: time.Now()}
	var made []string
	cleanup := func() {
		for _, m := range made {
			_ = os.Remove(m)
		}
	}
	for i, f := range p.files {
		if !authorPathLinkIsITunes(f.FilePath) {
			continue
		}
		if err := fileops.Reflink(f.FilePath, p.dests[i]); err != nil {
			cleanup()
			g.Outcome, g.Error = icOutcomeFailed, fmt.Sprintf("reflink %s: %v", f.FilePath, err)
			return g
		}
		made = append(made, p.dests[i])
		rec.Pairs = append(rec.Pairs, icPair{SourceFileID: f.ID, SourcePath: f.FilePath, ClonePath: p.dests[i],
			PID: f.ITunesPersistentID, OldITunesPath: f.ITunesPath})
	}
	// The record goes down BEFORE any row moves, so a failure part way can
	// always be reversed from it.
	if err := r.saveRecord(&rec); err != nil {
		cleanup()
		g.Outcome, g.Error = icOutcomeFailed, "rollback record not saved; clones removed: "+err.Error()
		return g
	}
	for _, pr := range rec.Pairs {
		src, dst := pr.SourcePath, pr.ClonePath
		_, err := r.store.ModifyBookFile(p.source.ID, pr.SourceFileID, func(bf *database.BookFile) error {
			if bf.FilePath != src {
				return errVGChangedSincePlan
			}
			bf.FilePath = dst
			bf.ITunesPath = r.cloner.LibraryITunesPath(dst)
			return nil
		})
		if err != nil {
			rb := r.undo(ctx, &rec)
			g.Outcome, g.Error = icOutcomeFailed, fmt.Sprintf("repoint %s: %v; rolled back: %s %s", pr.SourceFileID, err, rb.Outcome, rb.Error)
			return g
		}
	}
	if _, err := versionprimary.EnsureSinglePrimary(ctx, r.store, g.GroupID, versionprimary.Env{RootDir: r.rootDir}); err != nil {
		g.Error = "hand-off: " + err.Error()
	}
	g.Outcome = icOutcomeApplied
	return g
}

func (r *icRunner) saveRecord(rec *icRecord) error {
	blob, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return r.store.SetUserPreferenceForUser("_system", icRecordPrefix+rec.GroupID, string(blob))
}

func (r *icRunner) loadRecord(gid string) (*icRecord, error) {
	prefs, err := r.store.GetAllPreferencesForUser("_system")
	if err != nil {
		return nil, err
	}
	for _, p := range prefs {
		if p.Key == icRecordPrefix+gid && strings.TrimSpace(p.Value) != "" {
			var rec icRecord
			if err := json.Unmarshal([]byte(p.Value), &rec); err != nil {
				return nil, fmt.Errorf("decode record: %w", err)
			}
			return &rec, nil
		}
	}
	return nil, nil
}

func (r *icRunner) rollback(ctx context.Context, gid string) icGroupReport {
	g := icGroupReport{GroupID: gid, Decision: "rollback"}
	rec, err := r.loadRecord(gid)
	switch {
	case err != nil:
		g.Outcome, g.Error = icOutcomeFailed, err.Error()
		return g
	case rec == nil:
		g.Outcome, g.Reason = icOutcomeFailed, "no_clone_record"
		return g
	}
	res := r.undo(ctx, rec)
	res.GroupID, res.Decision = gid, "rollback"
	return res
}

// undo reverses one clone from its record. Order: PIDs back to the source's
// rows, the clone's rows and files removed, the source's prior state
// restored, the primary handed on, then the record cleared. A step that finds
// a row no longer as the record left it stops and keeps the record.
func (r *icRunner) undo(ctx context.Context, rec *icRecord) icGroupReport {
	g := icGroupReport{GroupID: rec.GroupID, Kind: rec.Kind, SourceBookID: rec.SourceBookID, CloneBookID: rec.CloneBookID}
	fail := func(format string, a ...any) icGroupReport {
		g.Outcome, g.Error = icOutcomeFailed, fmt.Sprintf(format, a...)
		return g
	}
	switch rec.Kind {
	case icKindVersion:
		for _, pr := range rec.Pairs {
			if pr.PID == "" || pr.SourceFileID == "" {
				continue
			}
			if _, err := r.store.ModifyBookFile(rec.CloneBookID, pr.CloneFileID, func(bf *database.BookFile) error {
				if bf.ITunesPersistentID != pr.PID {
					return errVGChangedSincePlan
				}
				bf.ITunesPersistentID = ""
				return nil
			}); err != nil {
				return fail("release PID %s from clone row %s: %v", pr.PID, pr.CloneFileID, err)
			}
			if _, err := r.store.ModifyBookFile(rec.SourceBookID, pr.SourceFileID, func(bf *database.BookFile) error {
				bf.ITunesPersistentID = pr.PID
				return nil
			}); err != nil {
				return fail("return PID %s to source row %s: %v", pr.PID, pr.SourceFileID, err)
			}
		}
		var ids []string
		for _, pr := range rec.Pairs {
			ids = append(ids, pr.CloneFileID)
		}
		if err := r.store.DeleteBookFilesByIDs(ids); err != nil {
			return fail("delete clone rows: %v", err)
		}
		if err := r.store.SetBookAuthors(rec.CloneBookID, nil); err != nil {
			return fail("clear clone authors: %v", err)
		}
		if err := r.store.DeleteBook(rec.CloneBookID); err != nil {
			return fail("delete clone book %s: %v", rec.CloneBookID, err)
		}
		if _, err := r.store.ModifyBook(rec.SourceBookID, func(b *database.Book) error {
			st := rec.SourcePriorState
			b.LibraryState = &st
			switch rec.SourcePriorPrimary {
			case "true", "false":
				v := rec.SourcePriorPrimary == "true"
				b.IsPrimaryVersion = &v
			default:
				b.IsPrimaryVersion = nil
			}
			return nil
		}); err != nil {
			return fail("restore source state: %v", err)
		}
	case icKindMixed:
		for _, pr := range rec.Pairs {
			if _, err := r.store.ModifyBookFile(rec.SourceBookID, pr.SourceFileID, func(bf *database.BookFile) error {
				if bf.FilePath == pr.SourcePath {
					return database.ErrSkipBookFileWrite
				}
				if bf.FilePath != pr.ClonePath {
					return errVGChangedSincePlan
				}
				bf.FilePath, bf.ITunesPath = pr.SourcePath, pr.OldITunesPath
				return nil
			}); err != nil && !errors.Is(err, database.ErrSkipBookFileWrite) {
				return fail("repoint %s back: %v", pr.SourceFileID, err)
			}
		}
	default:
		return fail("unknown record kind %q", rec.Kind)
	}
	var leftover []string
	for _, pr := range rec.Pairs {
		if pr.ClonePath == "" || !pathutil.IsWithin(pr.ClonePath, r.rootDir) {
			continue
		}
		if err := os.Remove(pr.ClonePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Reported in the group result, not just logged: a clone left on
			// disk with no row is picked up by the next scan as a new book.
			leftover = append(leftover, pr.ClonePath+" ("+err.Error()+")")
		}
		if rec.Kind == icKindVersion {
			_ = os.Remove(filepath.Dir(pr.ClonePath)) // only if now empty
		}
	}
	if _, err := versionprimary.EnsureSinglePrimary(ctx, r.store, rec.GroupID, versionprimary.Env{RootDir: r.rootDir}); err != nil {
		g.Error = strings.TrimSpace(g.Error + " hand-off: " + err.Error())
	}
	if err := r.store.SetUserPreferenceForUser("_system", icRecordPrefix+rec.GroupID, ""); err != nil {
		g.Error = strings.TrimSpace(g.Error + " record not cleared: " + err.Error())
	}
	g.Outcome = icOutcomeRolled
	return g
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
