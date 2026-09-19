// file: internal/maintenance/jobs/merge_chapter_groups.go
// version: 1.14.1
// guid: a1000020-0000-0000-0000-000000000020
// last-edited: 2026-09-19

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

func init() { maintenance.Register(&mergeChapterGroupsJob{}) }

// The real merge runs through dedup.MergeSplitBookClusterWithOptions, which
// takes the whole dedup.Store, and the carry check needs chapterBlockerStore.
// Every database.Store satisfies both; this keeps that true at compile time, so
// the runtime assertions in Run cannot start failing in prod because
// database.Store lost a method.
var (
	_ dedup.Store         = database.Store(nil)
	_ chapterBlockerStore = database.Store(nil)
)

var (
	// errChapterMergeStore fails the run before any write.
	errChapterMergeStore = errors.New("merge-chapter-groups: store does not implement dedup.Store; refusing to merge")
	// errChapterMergeNoGroups: a real merge applies only a reviewed set.
	errChapterMergeNoGroups = errors.New(`merge-chapter-groups: a real merge ("dry_run": false) needs "groups" from a dry-run preview; it never detects on its own what to merge`)
)

type mergeChapterGroupsJob struct{}

func (j *mergeChapterGroupsJob) ID() string       { return "merge-chapter-groups" }
func (j *mergeChapterGroupsJob) Name() string     { return "Merge Chapter Groups" }
func (j *mergeChapterGroupsJob) Category() string { return "files" }

// DefaultParams advertises dry_run:true, which the dispatcher and the op bridge
// apply when dry_run is omitted, so only an explicit "dry_run": false merges.
func (j *mergeChapterGroupsJob) DefaultParams() any {
	return chapterGroupParams{
		DryRun:             true,
		MinFiles:           chapterDefaultMinFiles,
		MaxPerFileDuration: chapterDefaultMaxPerFileDuration,
	}
}
func (j *mergeChapterGroupsJob) Description() string {
	return "Merge multi-chapter book files into consolidated book records (preview first; undoable)"
}
func (j *mergeChapterGroupsJob) CanResume() bool { return false }

// ValidateParams rejects a real merge without a reviewed group list at the
// dispatcher (HTTP 400), before an operation is even queued. Run repeats the
// check for every other path into the job.
func (j *mergeChapterGroupsJob) ValidateParams(raw json.RawMessage, dryRun bool) error {
	p, err := parseChapterGroupParams(raw)
	if err != nil {
		return err
	}
	if !dryRun && len(p.Groups) == 0 {
		return errChapterMergeNoGroups
	}
	return nil
}

// Run previews (dryRun) or applies (a reviewed list of) chapter merges.
//
// A dry run detects groups over the library and returns each with a member
// snapshot and a fingerprint. A real run takes ONLY the groups the caller sends
// back (params.groups) and never detects on its own what to merge. Each group
// is re-verified against the library as it is now -- every member still
// eligible and unchanged since the preview (fingerprint), and the members still
// form exactly that one group under detection -- and any group that drifted is
// skipped and reported rather than merged. That is what makes the merged set
// exactly the reviewed set: a book imported after the preview, a title edited
// since, or a prefix the operator changed in the form cannot widen it.
//
// The merge goes through dedup.MergeSplitBookClusterWithOptions: undo journal
// written before the first write, iTunes library refused, external IDs and
// every user's listening progress carried to the primary and journaled, files
// stamped into chapter order, sources soft-deleted, reversible with
// merge.Service.UndoCombine. Source-only ASIN/narrator/series/author is filled
// onto the primary's EMPTY fields (journaled). What it cannot carry (bookmarks,
// playlist entries, ratings, a metadata conflict between sources) blocks the
// group; see chapterGroupBlockers.
//
// Groups are merged one at a time. They are disjoint by construction (a book is
// in at most one reviewed group, which verification enforces), but
// MergeSplitBookCluster holds the process-wide merge.LockMergeRMW for its whole
// run, so parallel workers would only queue on that lock.
func (j *mergeChapterGroupsJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	p, err := decodeChapterGroupParams(ctx)
	if err != nil {
		return err
	}
	p.DryRun = dryRun
	if !dryRun && len(p.Groups) == 0 {
		return errChapterMergeNoGroups
	}
	if !dryRun {
		if err := refuseDuringLibraryScan(store); err != nil {
			return err
		}
	}
	bs, ok := store.(chapterBlockerStore)
	if !ok {
		return errChapterMergeStore
	}
	var ds dedup.Store
	if !dryRun {
		if ds, ok = store.(dedup.Store); !ok {
			return errChapterMergeStore
		}
	}
	cc, err := newChapterCarryContext(bs)
	if err != nil {
		return err
	}
	res := chapterGroupsResult{Job: j.ID(), DryRun: dryRun, Params: p}
	var runErr error
	if dryRun {
		runErr = j.preview(ctx, store, reporter, cc, &res)
	} else {
		runErr = j.apply(ctx, store, ds, reporter, cc, &res)
	}
	chapterLog.Info("merge-chapter-groups complete: dry_run=%t groups=%d merged=%d skipped=%d failed=%d blocked=%d drifted=%d",
		dryRun, res.GroupsFound, res.BooksMerged, res.BooksSkipped, res.GroupsFailed, res.GroupsBlocked, res.GroupsDrifted)
	// Persist what was done even when cancelled part-way: a partial real run's
	// outcomes (and their journal IDs) are exactly what an operator needs.
	if err := maintenance.SetResult(ctx, res); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

// preview detects groups and describes what a merge of each would do. It only
// reads.
func (j *mergeChapterGroupsJob) preview(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, cc *chapterCarryContext, res *chapterGroupsResult) error {
	det, err := detectChapterGroupsForRun(ctx, store, res.Params)
	if err != nil {
		return err
	}
	res.applyDetectionCounts(det)
	res.Groups = make([]chapterGroupOutcome, 0, len(det.Groups)+len(det.Blocked))
	reporter.SetTotal(len(det.Groups) + len(det.Blocked))
	for _, g := range det.Groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		reporter.Increment()
		out := newChapterGroupOutcome(g)
		res.TotalBooksAffected += len(g.BookIDs)
		st, rerr := readChapterGroup(store, g.BookIDs)
		switch {
		case rerr != nil:
			out.Status = "would_skip"
			out.Errors = append(out.Errors, rerr.Error())
		default:
			out.Members = st.members
			out.Fingerprint = chapterFingerprint(st.members)
			out.PrimaryTitle = st.books[0].Title
			_, out.TitleAction = chapterTitleDecision(st.books[0], out.CommonTitle)
			if gerr := merge.GuardITunesProtected(store, out.BookIDs); gerr != nil {
				out.Status = "would_skip"
				out.Errors = append(out.Errors, gerr.Error())
			} else if out.Blockers, out.MetadataFills = cc.chapterGroupBlockers(st.books[0], st.books[1:]); len(out.Blockers) > 0 {
				out.Status = "blocked"
			} else if out.Blockers = chapterFileCountBlockers(st); len(out.Blockers) > 0 {
				out.Status = "blocked"
			} else {
				out.Status = "would_merge"
				out.BooksMerged = len(out.SourceBookIDs)
			}
		}
		switch out.Status {
		case "would_merge":
			res.BooksMerged += len(out.SourceBookIDs)
		case "blocked":
			res.GroupsBlocked++
			res.BooksSkipped += len(out.SourceBookIDs)
		default:
			res.BooksSkipped += len(out.SourceBookIDs)
		}
		res.Groups = append(res.Groups, out)
	}
	// Groups the detector blocked (non-primary versions, iTunes library,
	// duplicate or sparse positions...) are reported with their reasons and
	// no fingerprint: they cannot be selected for a merge.
	for _, g := range det.Blocked {
		reporter.Increment()
		out := newChapterGroupOutcome(g)
		res.BooksSkipped += len(out.SourceBookIDs)
		res.Groups = append(res.Groups, out)
	}
	return nil
}

// apply merges exactly the reviewed groups in res.Params.Groups.
func (j *mergeChapterGroupsJob) apply(ctx context.Context, store maintenance.JobStore, ds dedup.Store, reporter maintenance.ProgressReporter, cc *chapterCarryContext, res *chapterGroupsResult) error {
	opts, err := chapterDetectOptionsForRun(res.Params)
	if err != nil {
		return err
	}
	opID := maintenance.OperationIDFromCtx(ctx)
	opts.PathPrefix = "" // the members' folders ARE the scope; re-verify them as they are
	lister, ok := store.(chapterDirLister)
	if !ok {
		return errChapterMergeStore
	}
	claimed := map[string]bool{}
	res.GroupsFound = len(res.Params.Groups)
	res.Groups = make([]chapterGroupOutcome, 0, len(res.Params.Groups))
	reporter.SetTotal(len(res.Params.Groups))
	for _, sel := range res.Params.Groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		reporter.Increment()
		out := chapterGroupOutcome{PrimaryBookID: sel.PrimaryBookID, BookIDs: sel.BookIDs, Fingerprint: sel.Fingerprint}
		if len(sel.BookIDs) > 1 {
			out.SourceBookIDs = append([]string(nil), sel.BookIDs[1:]...)
		}
		res.TotalBooksAffected += len(sel.BookIDs)
		j.applyOne(store, lister, ds, cc, opID, opts, claimed, sel, &out)
		switch out.Status {
		case "failed":
			res.GroupsFailed++
		case "blocked":
			res.GroupsBlocked++
		case "drifted":
			res.GroupsDrifted++
		case "selection_mismatch":
			res.GroupsSelectionMismatch++
		}
		res.BooksMerged += out.BooksMerged
		res.BooksSkipped += len(out.SourceBookIDs) - out.BooksMerged
		res.Groups = append(res.Groups, out)
	}
	return nil
}

// chapterFolderSiblings lists the live books directly in each member folder,
// plus, for a folder that is one disc of a multi-disc book, the books in its
// sibling disc folders (same parent, same stem once the disc token is
// removed), so the multi-disc blocker the preview saw is seen here too.
func chapterFolderSiblings(lister chapterDirLister, dirs map[string]bool) ([]string, error) {
	seen := map[string]bool{}
	var ids []string
	for dir := range dirs {
		listDir := dir
		discKey, isDisc := scanner.DiscFolderKey(dir)
		if isDisc {
			listDir = filepath.Dir(dir)
		}
		books, err := lister.LiveBookPathsUnderDir(listDir)
		if err != nil {
			return nil, fmt.Errorf("re-list folder %s: %w", listDir, err)
		}
		for id, path := range books {
			bookDir := filepath.Dir(path)
			keep := bookDir == dir
			if !keep && isDisc {
				k, ok := scanner.DiscFolderKey(bookDir)
				keep = ok && k == discKey
			}
			if keep && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// chapterDirLister re-lists a folder from the book_atpath index. It is on
// database.Store (BookDirLister via BookStore), so the prod indexedStore
// forwards it.
type chapterDirLister interface {
	LiveBookPathsUnderDir(dir string) (map[string]string, error)
}

// verifyChapterSelection checks a reviewed group against the library now.
// Returns the re-detected group and "" when it may be merged, else why not
// and the outcome status.
//
// Detection runs over the members AND every live sibling book in their
// folder(s), freshly re-read (verify also runs under the merge lock), and the
// selection must EQUAL one detected group exactly. Detecting over the
// selection alone would drop every whole-set blocker -- mixed containers,
// repeated positions, two authors, full-length copies -- the moment the
// other members are left out, and let a SUBSET of a blocked group merge,
// orphaning the rest. The fingerprint is not a secret (any client can hash
// the members), so it proves only that the members did not change.
//
// Statuses: "drifted" (members changed), "selection_mismatch" (not exactly
// one detected group of the folder), "blocked" (the detector blocks that
// group, or it is LOW confidence and the selection carries no
// allow_low_confidence acknowledgement -- the card sets it only for a group
// the operator ticked one by one, never through "Select all").
//
// The folder is re-listed on every call -- once before the lock and again
// under it (precheck) -- through the book_atpath index: one range scan over
// the folder (over the parent for a disc folder, to find its sibling discs)
// plus a point read per book found. It never loads the whole library: the
// earlier version did, once per group while holding the process-wide merge
// lock.
func verifyChapterSelection(store maintenance.JobStore, lister chapterDirLister, sel chapterGroupSelection, st *chapterGroupState, opts scanner.ChapterDetectOptions) (scanner.ChapterGroup, string, string) {
	if chapterFingerprint(st.members) != sel.Fingerprint {
		return scanner.ChapterGroup{}, "members changed since the preview (fingerprint mismatch)", "drifted"
	}
	inSel := make(map[string]bool, len(st.books))
	dirs := map[string]bool{}
	cores := make([]database.BookCore, 0, len(st.books))
	for _, b := range st.books {
		inSel[b.ID] = true
		dirs[filepath.Dir(b.FilePath)] = true
		cores = append(cores, b.Core())
	}
	siblingIDs, err := chapterFolderSiblings(lister, dirs)
	if err != nil {
		return scanner.ChapterGroup{}, err.Error(), "drifted"
	}
	for _, id := range siblingIDs {
		if inSel[id] {
			continue
		}
		b, err := store.GetBookByID(id)
		if err != nil {
			return scanner.ChapterGroup{}, fmt.Sprintf("folder sibling %s not readable: %v", id, err), "drifted"
		}
		if b == nil {
			continue
		}
		cores = append(cores, b.Core())
	}
	det := scanner.DetectChapterGroupsWithOptions(cores, opts)
	for _, g := range det.Blocked {
		if slices.Equal(g.BookIDs, sel.BookIDs) {
			return scanner.ChapterGroup{}, "group is blocked: " + strings.Join(g.Blockers, "; "), "blocked"
		}
	}
	for _, g := range det.Groups {
		if !slices.Equal(g.BookIDs, sel.BookIDs) || g.PrimaryBookID != sel.PrimaryBookID {
			continue
		}
		if g.Confidence == scanner.ChapterConfidenceLow && !sel.AllowLowConfidence {
			return scanner.ChapterGroup{}, "low-confidence group: a merge needs an explicit allow_low_confidence acknowledgement for it", "blocked"
		}
		return g, "", ""
	}
	return scanner.ChapterGroup{}, fmt.Sprintf("selection_mismatch: the selected books are not exactly one detected group of their folder (the folder now has %d mergeable and %d blocked group(s)); preview again", len(det.Groups), len(det.Blocked)), "selection_mismatch"
}

func (j *mergeChapterGroupsJob) applyOne(store maintenance.JobStore, lister chapterDirLister, ds dedup.Store, cc *chapterCarryContext, opID string, opts scanner.ChapterDetectOptions, claimed map[string]bool, sel chapterGroupSelection, out *chapterGroupOutcome) {
	if len(sel.BookIDs) < 2 || sel.PrimaryBookID == "" || sel.BookIDs[0] != sel.PrimaryBookID || sel.Fingerprint == "" {
		out.Status = "failed"
		out.Errors = append(out.Errors, "malformed group: needs primary_book_id == book_ids[0], at least 2 books, and the preview fingerprint")
		return
	}
	for _, id := range sel.BookIDs {
		if claimed[id] {
			out.Status = "failed"
			out.Errors = append(out.Errors, fmt.Sprintf("book %s is in more than one requested group", id))
			return
		}
	}
	for _, id := range sel.BookIDs {
		claimed[id] = true
	}
	st, err := readChapterGroup(store, sel.BookIDs)
	if err != nil {
		out.Status = "failed"
		out.Errors = append(out.Errors, err.Error())
		return
	}
	out.Members = st.members
	out.PrimaryTitle = st.books[0].Title
	g, why, status := verifyChapterSelection(store, lister, sel, st, opts)
	if why != "" {
		out.Status = status
		if status == "blocked" || status == "selection_mismatch" {
			out.Blockers = append(out.Blockers, why)
		} else {
			out.Errors = append(out.Errors, why)
		}
		return
	}
	out.CommonTitle, out.Directory = g.CommonTitle, g.Directory
	out.TotalDuration, out.FileCount = g.TotalDuration, g.FileCount
	if out.Blockers, out.MetadataFills = cc.chapterGroupBlockers(st.books[0], st.books[1:]); len(out.Blockers) > 0 {
		out.Status = "blocked"
		return
	}
	if out.Blockers = chapterFileCountBlockers(st); len(out.Blockers) > 0 {
		out.Status = "blocked"
		return
	}
	suggested, action := chapterTitleDecision(st.books[0], out.CommonTitle)
	out.TitleAction = action

	order := chapterFileOrder(st)
	audit := chapterMergeAudit{
		PrimaryBookID:      sel.PrimaryBookID,
		PrimaryTitleBefore: st.books[0].Title,
		SourceBookIDs:      out.SourceBookIDs,
		SourceTitles:       make(map[string]string, len(out.SourceBookIDs)),
		MovedFileIDs:       make(map[string][]string, len(out.SourceBookIDs)),
		FileOrder:          order,
		Fingerprint:        sel.Fingerprint,
	}
	for _, b := range st.books[1:] {
		audit.SourceTitles[b.ID] = b.Title
		for _, f := range st.files[b.ID] {
			audit.MovedFileIDs[b.ID] = append(audit.MovedFileIDs[b.ID], f.ID)
		}
	}

	// Re-verify UNDER the merge lock (Precheck runs after
	// merge.LockMergeRMW): the reads above are outside it, so a file, title or
	// bookmark that changed between them and the lock would otherwise be
	// merged unreviewed. The fingerprint covers titles, durations, updated_at
	// and every file id+path, so a match proves the title decision, audit
	// record and play order computed above describe what is merged; the play
	// order itself is recomputed under the lock (OrderByBooks: the primary,
	// then the sources in chapter order).
	precheck := func() error {
		if chapterPrecheckTestHook != nil {
			chapterPrecheckTestHook()
		}
		cur, err := readChapterGroup(store, sel.BookIDs)
		if err != nil {
			return &chapterStepError{status: "failed", msg: err.Error()}
		}
		// verifyChapterSelection re-lists the members' folder itself, so a
		// book that landed there since the pre-lock check (a second copy, a
		// new chapter) is seen here, under the lock.
		if _, why, status := verifyChapterSelection(store, lister, sel, cur, opts); why != "" {
			if status == "blocked" || status == "selection_mismatch" {
				return &chapterStepError{status: status, msg: why, blockers: []string{why}}
			}
			return &chapterStepError{status: status, msg: why}
		}
		b, _ := cc.chapterGroupBlockers(cur.books[0], cur.books[1:])
		if b = append(b, chapterFileCountBlockers(cur)...); len(b) > 0 {
			return &chapterStepError{status: "blocked", msg: fmt.Sprint(b), blockers: b}
		}
		return nil
	}
	mres, merr := dedup.MergeSplitBookClusterWithOptions(ds, sel.PrimaryBookID, out.SourceBookIDs, suggested,
		dedup.SplitMergeOptions{OrderByBooks: true, FillEmpty: true, Precheck: precheck})
	var stepErr *chapterStepError
	if errors.As(merr, &stepErr) {
		out.Status = stepErr.status
		out.Blockers = stepErr.blockers
		if stepErr.blockers == nil {
			out.Errors = append(out.Errors, stepErr.msg)
		}
		return
	}
	switch {
	case merr != nil:
		out.Status = "failed"
		out.Errors = append(out.Errors, merr.Error())
	default:
		out.BooksMerged = mres.MergedSrcCount
		out.FilesMoved = mres.FilesMoved
		out.JournalID = mres.JournalID
		out.Errors = append(out.Errors, mres.Errors...)
		if mres.TitleKeptLocked {
			out.TitleAction = "kept_locked"
		}
		switch {
		case mres.MergedSrcCount == len(out.SourceBookIDs) && len(mres.Errors) == 0:
			out.Status = "merged"
		case mres.MergedSrcCount > 0:
			out.Status = "partial"
		default:
			out.Status = "failed"
		}
	}
	if out.TitleAction == "set" && out.Status != "failed" {
		audit.NewTitle = suggested
	}
	audit.TitleAction = out.TitleAction
	audit.BooksMerged = out.BooksMerged
	audit.FilesMoved = out.FilesMoved
	audit.JournalID = out.JournalID
	audit.Errors = out.Errors

	if out.Status == "failed" {
		chapterLog.Warn("merge-chapter-groups group failed primary=%s errors=%s",
			logger.SanitizeLogValue(out.PrimaryBookID), logger.SanitizeLogValue(fmt.Sprint(out.Errors)))
	}
	if opID == "" {
		return
	}
	raw, jerr := json.Marshal(audit)
	if jerr != nil {
		out.Errors = append(out.Errors, "audit record not written: "+jerr.Error())
		return
	}
	if cerr := store.CreateOperationResult(&database.OperationResult{
		OperationID: opID,
		BookID:      out.PrimaryBookID,
		ResultJSON:  string(raw),
		Status:      out.Status,
	}); cerr != nil {
		out.Errors = append(out.Errors, "audit record not written: "+cerr.Error())
		chapterLog.Error("merge-chapter-groups audit write failed primary=%s err=%s",
			logger.SanitizeLogValue(out.PrimaryBookID), logger.SanitizeLogValue(cerr.Error()))
	}
}

// chapterPrecheckTestHook, when set (tests only), runs first under the merge
// lock, so a test can land a change between the pre-lock verification and the
// re-verification under the lock.
var chapterPrecheckTestHook func()

// chapterStepError is a Precheck refusal, carrying the outcome status.
type chapterStepError struct {
	status   string
	msg      string
	blockers []string
}

func (e *chapterStepError) Error() string { return e.status + ": " + e.msg }

// chapterTitleDecision returns the title to offer the merge ("" = leave the
// primary's title alone) and the action to report. Only a title no one
// curated is replaced -- the scanner's filename-derived one, or a bare
// position like "157" or "108 of 310" (scanner.ChapterTitleIsReplaceable).
func chapterTitleDecision(primary *database.Book, commonTitle string) (string, string) {
	if commonTitle == "" || primary.Title == commonTitle {
		return "", "kept"
	}
	if scanner.ChapterTitleIsReplaceable(primary.Title, primary.FilePath) {
		return commonTitle, "set"
	}
	return "", "kept"
}

// Policy is DefaultPolicy with library.scan's ConcurrencyKey. The owner's
// standing rule is that nothing applies to the library during a scan: sharing
// the key makes the registry run this job and library.scan one at a time,
// in either order (a scan queued while a merge runs waits, and vice versa).
// It JOINS library.scan's key; it never gives library.scan a different one.
// The previews queue behind a running scan too -- they are cheap to re-run.
func (j *mergeChapterGroupsJob) Policy() maintenance.ExecutionPolicy {
	p := maintenance.DefaultPolicy()
	p.ConcurrencyKey = chapterLibraryScanKey
	return p
}

// chapterLibraryScanKey is library.scan's ConcurrencyKey
// (internal/server/library_core_ops.go).
const chapterLibraryScanKey = "library.scan"

// chapterActiveOpsStore is what the scan guard reads. database.Store (and so
// the prod indexedStore) has it.
type chapterActiveOpsStore interface {
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
}

// refuseDuringLibraryScan fails a real merge while a library.scan is
// running: the ConcurrencyKey keeps the dispatcher from starting both, and
// this covers any path into Run that did not go through it. It fails CLOSED:
// a store that cannot list operations is not proof that no scan runs. A
// zombie "running" scan row (silent past chapterZombieScanAfter) is ignored
// with a warning, the way the registry skips rows with no live handle.
func refuseDuringLibraryScan(store maintenance.JobStore) error {
	qs, ok := store.(chapterActiveOpsStore)
	if !ok {
		return errors.New("merge-chapter-groups: cannot verify that no library.scan is running; refusing to merge")
	}
	active, err := qs.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("merge-chapter-groups: cannot list active operations; refusing to merge: %w", err)
	}
	now := time.Now()
	for _, op := range active {
		if op.DefID != chapterLibraryScanKey || op.Status != "running" {
			continue
		}
		last := op.QueuedAt
		for _, t := range []*time.Time{op.StartedAt, op.LastProgressAt} {
			if t != nil && t.After(last) {
				last = *t
			}
		}
		if now.Sub(last) > chapterZombieScanAfter {
			chapterLog.Warn("merge-chapter-groups: ignoring library.scan row %s: running but silent for %s (a zombie row; the registry skips it too)",
				logger.SanitizeLogValue(op.ID), now.Sub(last).Round(time.Second))
			continue
		}
		return fmt.Errorf("merge-chapter-groups: library.scan is running (op %s); refusing to merge during a scan", op.ID)
	}
	return nil
}

// chapterZombieScanAfter is how long a "running" library.scan row may be
// silent (no start, no progress stamp) before this guard treats it as a
// zombie. The registry's own test for a zombie is "no live run handle"
// (registry.hasLiveHandle), which a job cannot see; the observable
// equivalent is the watchdog: a live run that reports no progress for its
// ProgressTimeout (library.scan uses the 5-minute default) is canceled and
// leaves "running". So a row silent for 3x that long has no live run behind
// it. Serialization against a LIVE scan does not depend on this guard at
// all: the shared ConcurrencyKey (dispatcher Gate 3) keeps the two apart.
const chapterZombieScanAfter = 15 * time.Minute
