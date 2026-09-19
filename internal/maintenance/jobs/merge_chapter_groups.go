// file: internal/maintenance/jobs/merge_chapter_groups.go
// version: 1.7.0
// guid: a1000020-0000-0000-0000-000000000020
// last-edited: 2026-09-19

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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
// merge.Service.UndoCombine. What it cannot carry (bookmarks, playlist entries,
// ratings, source-only metadata) blocks the group; see chapterGroupBlockers.
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
	res.GroupsFound = len(det.Groups)
	res.GroupsSkippedUnknownDuration = det.SkippedUnknownDuration
	res.GroupsSkippedDuplicateCopies = det.SkippedDuplicateCopies
	res.BooksExcluded = det.SkippedExcluded
	res.Groups = make([]chapterGroupOutcome, 0, len(det.Groups))
	reporter.SetTotal(len(det.Groups))
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
			} else if out.Blockers = cc.chapterGroupBlockers(st.books[0], st.books[1:]); len(out.Blockers) > 0 {
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
	return nil
}

// apply merges exactly the reviewed groups in res.Params.Groups.
func (j *mergeChapterGroupsJob) apply(ctx context.Context, store maintenance.JobStore, ds dedup.Store, reporter maintenance.ProgressReporter, cc *chapterCarryContext, res *chapterGroupsResult) error {
	exclude, err := chapterExcluder()
	if err != nil {
		return err
	}
	opID := maintenance.OperationIDFromCtx(ctx)
	opts := chapterDetectOptions(res.Params, exclude)
	opts.PathPrefix = "" // the reviewed members ARE the scope; re-verify them as they are
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
		j.applyOne(store, ds, cc, opID, opts, claimed, sel, &out)
		switch out.Status {
		case "failed":
			res.GroupsFailed++
		case "blocked":
			res.GroupsBlocked++
		case "drifted":
			res.GroupsDrifted++
		}
		res.BooksMerged += out.BooksMerged
		res.BooksSkipped += len(out.SourceBookIDs) - out.BooksMerged
		res.Groups = append(res.Groups, out)
	}
	return nil
}

// verifyChapterSelection checks a reviewed group against the library now.
// Returns the re-detected group and "" when it may be merged, else why not.
func verifyChapterSelection(sel chapterGroupSelection, st *chapterGroupState, opts scanner.ChapterDetectOptions) (scanner.ChapterGroup, string) {
	if chapterFingerprint(st.members) != sel.Fingerprint {
		return scanner.ChapterGroup{}, "members changed since the preview (fingerprint mismatch)"
	}
	cores := make([]database.BookCore, len(st.books))
	for i, b := range st.books {
		cores[i] = b.Core()
	}
	det := scanner.DetectChapterGroupsWithOptions(cores, opts)
	if len(det.Groups) != 1 {
		return scanner.ChapterGroup{}, fmt.Sprintf("members no longer form one chapter group (detected %d)", len(det.Groups))
	}
	g := det.Groups[0]
	if g.PrimaryBookID != sel.PrimaryBookID || !slices.Equal(g.BookIDs, sel.BookIDs) {
		return scanner.ChapterGroup{}, "members no longer form this exact group in this chapter order"
	}
	return g, ""
}

func (j *mergeChapterGroupsJob) applyOne(store maintenance.JobStore, ds dedup.Store, cc *chapterCarryContext, opID string, opts scanner.ChapterDetectOptions, claimed map[string]bool, sel chapterGroupSelection, out *chapterGroupOutcome) {
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
	g, why := verifyChapterSelection(sel, st, opts)
	if why != "" {
		out.Status = "drifted"
		out.Errors = append(out.Errors, why)
		return
	}
	out.CommonTitle, out.Directory = g.CommonTitle, g.Directory
	out.TotalDuration, out.FileCount = g.TotalDuration, g.FileCount
	if out.Blockers = cc.chapterGroupBlockers(st.books[0], st.books[1:]); len(out.Blockers) > 0 {
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

	mres, merr := dedup.MergeSplitBookClusterWithOptions(ds, sel.PrimaryBookID, out.SourceBookIDs, suggested, dedup.SplitMergeOptions{FileOrder: order})
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

// chapterTitleDecision returns the title to offer the merge ("" = leave the
// primary's title alone) and the action to report. Only a title that is still
// the scanner's filename-derived one is replaced; a curated title is kept.
func chapterTitleDecision(primary *database.Book, commonTitle string) (string, string) {
	if commonTitle == "" || primary.Title == commonTitle {
		return "", "kept"
	}
	if scanner.ChapterTitleIsFilenameDerived(primary.Title, primary.FilePath) {
		return commonTitle, "set"
	}
	return "", "kept"
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *mergeChapterGroupsJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
