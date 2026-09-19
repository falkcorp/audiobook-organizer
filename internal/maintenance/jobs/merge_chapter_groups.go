// file: internal/maintenance/jobs/merge_chapter_groups.go
// version: 1.5.0
// guid: a1000020-0000-0000-0000-000000000020
// last-edited: 2026-09-19

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

func init() { maintenance.Register(&mergeChapterGroupsJob{}) }

// The real merge runs through dedup.MergeSplitBookCluster, which takes the
// whole dedup.Store. Every database.Store satisfies it; this keeps that true at
// compile time, so the runtime assertion in Run cannot start failing in prod
// because database.Store lost a method.
var _ dedup.Store = database.Store(nil)

// errChapterMergeStore is returned when a real merge is asked of a store that
// cannot perform it. It fails the run before any write.
var errChapterMergeStore = errors.New("merge-chapter-groups: store does not implement dedup.Store; refusing to merge")

type mergeChapterGroupsJob struct{}

func (j *mergeChapterGroupsJob) ID() string       { return "merge-chapter-groups" }
func (j *mergeChapterGroupsJob) Name() string     { return "Merge Chapter Groups" }
func (j *mergeChapterGroupsJob) Category() string { return "files" }

// DefaultParams advertises dry_run:true, which the dispatcher applies when a
// request omits dry_run, so only an explicit "dry_run": false merges.
func (j *mergeChapterGroupsJob) DefaultParams() any {
	return chapterGroupParams{
		DryRun:             true,
		MinFiles:           chapterDefaultMinFiles,
		MaxPerFileDuration: chapterDefaultMaxPerFileDuration,
	}
}
func (j *mergeChapterGroupsJob) Description() string {
	return "Merge multi-chapter book files into consolidated book records (undoable)"
}
func (j *mergeChapterGroupsJob) CanResume() bool { return false }

// Run detects chapter groups and, unless dryRun, folds each group's sources
// into its primary (chapter 01).
//
// The merge goes through dedup.MergeSplitBookCluster -- the split-book merge
// the dedup review UI uses -- rather than store.MergeChapterBooks, because that
// path is the one with the safety net: it refuses groups touching the active
// iTunes library, writes a combine undo journal before the first write (and
// refuses the merge if it cannot), reassigns the sources' external IDs before
// soft-deleting them, withholds a title change on a user field lock, and is
// reversed by merge.Service.UndoCombine. MergeChapterBooks had none of that.
//
// Groups are merged one at a time. They are disjoint by construction (a book
// is in at most one group), but MergeSplitBookCluster holds the process-wide
// merge.LockMergeRMW for its whole run, so parallel workers would only queue on
// that lock; detection is a single linear pass.
func (j *mergeChapterGroupsJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	p, err := decodeChapterGroupParams(ctx)
	if err != nil {
		return err
	}
	p.DryRun = dryRun
	var ds dedup.Store
	if !dryRun {
		var ok bool
		if ds, ok = store.(dedup.Store); !ok {
			return errChapterMergeStore
		}
	}
	det, snapshot, err := detectChapterGroupsForRun(ctx, store, p)
	if err != nil {
		return err
	}
	opID := maintenance.OperationIDFromCtx(ctx)

	res := chapterGroupsResult{
		Job:                          j.ID(),
		DryRun:                       dryRun,
		Params:                       p,
		GroupsFound:                  len(det.Groups),
		GroupsSkippedUnknownDuration: det.SkippedUnknownDuration,
		Groups:                       make([]chapterGroupOutcome, 0, len(det.Groups)),
	}
	reporter.SetTotal(len(det.Groups))
	var runErr error
	for _, g := range det.Groups {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		reporter.Increment()
		res.TotalBooksAffected += len(g.BookIDs)
		out := newChapterGroupOutcome(g)
		if dryRun {
			previewChapterGroup(store, &out)
			if out.Status == "would_merge" {
				res.BooksMerged += len(out.SourceBookIDs)
			} else {
				res.BooksSkipped += len(out.SourceBookIDs)
			}
		} else {
			mergeChapterGroup(store, ds, opID, snapshot, &out)
			res.BooksMerged += out.BooksMerged
			res.BooksSkipped += len(out.SourceBookIDs) - out.BooksMerged
			if out.Status == "failed" {
				res.GroupsFailed++
			}
		}
		res.Groups = append(res.Groups, out)
	}
	chapterLog.Info("merge-chapter-groups complete: dry_run=%t groups=%d merged=%d skipped=%d failed=%d",
		dryRun, res.GroupsFound, res.BooksMerged, res.BooksSkipped, res.GroupsFailed)
	// Persist what was done even when cancelled part-way: a partial real run's
	// outcomes (and their journal IDs) are exactly what an operator needs.
	if err := maintenance.SetResult(ctx, res); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
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

// previewChapterGroup fills a dry-run outcome. It only reads: the primary's
// current title, and the iTunes guard a real merge would apply first.
func previewChapterGroup(store maintenance.JobStore, out *chapterGroupOutcome) {
	primary, err := store.GetBookByID(out.PrimaryBookID)
	if err != nil || primary == nil {
		out.Status = "would_skip"
		out.Errors = append(out.Errors, fmt.Sprintf("primary %s not readable: %v", out.PrimaryBookID, err))
		return
	}
	out.PrimaryTitle = primary.Title
	_, out.TitleAction = chapterTitleDecision(primary, out.CommonTitle)
	if gerr := merge.GuardITunesProtected(store, out.BookIDs); gerr != nil {
		out.Status = "would_skip"
		out.Errors = append(out.Errors, gerr.Error())
		return
	}
	out.Status = "would_merge"
	out.BooksMerged = len(out.SourceBookIDs)
}

// mergeChapterGroup performs one real merge and writes its audit record.
func mergeChapterGroup(store maintenance.JobStore, ds dedup.Store, opID string, snapshot map[string]database.BookCore, out *chapterGroupOutcome) {
	primary, err := store.GetBookByID(out.PrimaryBookID)
	if err != nil || primary == nil {
		out.Status = "failed"
		out.Errors = append(out.Errors, fmt.Sprintf("primary %s not readable: %v", out.PrimaryBookID, err))
		return
	}
	out.PrimaryTitle = primary.Title
	suggested, action := chapterTitleDecision(primary, out.CommonTitle)
	out.TitleAction = action

	audit := chapterMergeAudit{
		PrimaryBookID:      out.PrimaryBookID,
		PrimaryTitleBefore: primary.Title,
		SourceBookIDs:      out.SourceBookIDs,
		SourceTitles:       make(map[string]string, len(out.SourceBookIDs)),
		MovedFileIDs:       make(map[string][]string, len(out.SourceBookIDs)),
	}
	for _, src := range out.SourceBookIDs {
		audit.SourceTitles[src] = snapshot[src].Title
		files, ferr := store.GetBookFiles(src)
		if ferr != nil {
			continue // the merge re-reads and reports this source itself
		}
		for i := range files {
			audit.MovedFileIDs[src] = append(audit.MovedFileIDs[src], files[i].ID)
		}
	}

	mres, merr := dedup.MergeSplitBookCluster(ds, out.PrimaryBookID, out.SourceBookIDs, suggested)
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

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *mergeChapterGroupsJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
