// file: internal/audiobooks/purge_forget_waiting.go
// version: 1.0.1
// guid: a46786b9-1f13-4259-bd41-49644680434d
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// forgetMaxPaths is how many paths the owner is shown, and may forget, at a time.
// The audit row names every one of them, so the bound is also the size of that
// row; a longer list is forgotten in several rounds, each listed first.
const forgetMaxPaths = 500

// forgetLockWait is how long the forget action waits for a purge that is
// removing files (replayMu) before it answers ErrPurgeBusy. A variable so a test
// can shorten it.
var forgetLockWait = 10 * time.Second

var (
	// ErrForgetNothingShown is returned when no path was sent: the action forgets
	// only the files the owner was shown, never "whatever is marked".
	ErrForgetNothingShown = errors.New("no file path was sent: forget the records of the files the dialog listed")
	// ErrForgetTooMany is returned when more than forgetMaxPaths paths were sent.
	ErrForgetTooMany = errors.New("too many file paths for one forget")
	// ErrPurgeBusy is returned when a purge or replay is removing files and did
	// not finish within forgetLockWait. Nothing was changed.
	ErrPurgeBusy = errors.New("a purge is removing files right now; try again when it has finished")
)

// WaitingFilePaths is what the owner is shown before forgetting records: the
// files whose removal waits for a folder that is missing or empty.
type WaitingFilePaths struct {
	// Paths are at most Limit of them, in path order. A path is listed only when
	// every record of it is waiting, the same rule that counts it in
	// PendingFileCounts.WaitingForFolder, so the list and the count agree.
	Paths []string `json:"paths"`
	// Total is how many such files there are, Limit how many a forget may take.
	Total int `json:"total"`
	Limit int `json:"limit"`
}

// WaitingFileRecordPaths lists the files whose journal records wait for a folder
// (see PendingFileCounts.WaitingForFolder), up to forgetMaxPaths. It reads and
// changes nothing.
func (svc *AudiobookService) WaitingFileRecordPaths() (WaitingFilePaths, error) {
	if svc.store == nil {
		return WaitingFilePaths{}, fmt.Errorf("database not initialized")
	}
	files, _, _, err := svc.readPendingFiles()
	if err != nil {
		return WaitingFilePaths{}, err
	}
	out := WaitingFilePaths{Paths: []string{}, Limit: forgetMaxPaths}
	for _, f := range files.sorted() {
		if !f.allWaiting() {
			continue
		}
		out.Total++
		if len(out.Paths) < forgetMaxPaths {
			out.Paths = append(out.Paths, f.path)
		}
	}
	return out, nil
}

// ForgetKept is a file the owner asked to forget whose record was kept, and why.
type ForgetKept struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ForgetWaitingResult is what ForgetWaitingFileRecords did.
type ForgetWaitingResult struct {
	// Records is how many journal records were deleted, and Paths how many
	// distinct files they named; ForgottenPaths names every one of them. No file
	// and no book row was touched.
	Records        int      `json:"records_forgotten"`
	Paths          int      `json:"paths_forgotten"`
	ForgottenPaths []string `json:"forgotten_paths"`
	// NoLongerWaiting are sent paths that are not waiting any more (a purge
	// settled them, an earlier forget took them, or a record of the path is not
	// marked): nothing was forgotten for them.
	NoLongerWaiting int `json:"paths_no_longer_waiting"`
	// Kept are sent paths that still wait but whose folder, checked just before
	// the forget, holds something again (or whose file is back, or whose folder
	// could not be read): the next purge decides them.
	Kept []ForgetKept `json:"kept,omitempty"`
	// Warnings are things that went wrong after the records were gone (the audit
	// row was not written).
	Warnings []string `json:"warnings,omitempty"`
}

// lockReplayWithin takes replayMu, waiting at most d (and no longer than ctx):
// a purge removing files holds it for its whole run, and a stat of a hung mount
// has no timeout of its own, so an unbounded wait here would hang the request.
func (svc *AudiobookService) lockReplayWithin(ctx context.Context, d time.Duration) error {
	if svc.replayMu.TryLock() {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return ErrPurgeBusy
		case <-tick.C:
			if svc.replayMu.TryLock() {
				return nil
			}
		}
	}
}

// stillWaitingReason re-reads the disk for one path the owner asked to forget:
// "" when the file is still not there and its folder is still missing or empty,
// otherwise the reason the record must stay (the next purge decides it).
func stillWaitingReason(path, root string) string {
	// The record's path was confined to the library root when it was journaled,
	// but root_dir may have changed since. Confine it again, on the same terms a
	// purge does, before anything is stat'ed or opened: a path that is no longer
	// inside the root is never touched, and its record stays.
	if err := purgePathInsideRoot(path, root); err != nil {
		if errors.Is(err, errPathLookupFailed) {
			return fmt.Sprintf("cannot tell whether it is inside the library root: %v", err)
		}
		return fmt.Sprintf("it is no longer inside the library root: %v", err)
	}
	if _, err := rootStat(path); err == nil {
		return "the file is there again"
	} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return fmt.Sprintf("cannot tell whether the file is there: %v", err)
	}
	missing, empty, err := folderCheck(path, root, nil)
	switch {
	case err != nil:
		return fmt.Sprintf("cannot read its folder: %v", err)
	case !missing && !empty:
		return "its folder holds files again"
	}
	return ""
}

// recheckWaiting runs stillWaitingReason for every path at once and returns when
// all have answered or d has passed. A path that has not answered by then keeps
// its record; the calls still blocked are counted in rootStatsHung until they
// return, like the root check's.
func recheckWaiting(paths []string, root string, d time.Duration) []string {
	type answer struct {
		i      int
		reason string
	}
	ch := make(chan answer, len(paths))
	call := &statCall{pending: len(paths)}
	for i, p := range paths {
		go func() {
			r := stillWaitingReason(p, root)
			call.answered()
			ch <- answer{i, r}
		}()
	}
	out := make([]string, len(paths))
	got := make([]bool, len(paths))
	timer := time.NewTimer(d)
	defer timer.Stop()
	for range paths {
		select {
		case a := <-ch:
			out[a.i], got[a.i] = a.reason, true
		case <-timer.C:
			call.giveUp()
			for i := range out {
				if !got[i] {
					out[i] = fmt.Sprintf("its folder did not answer within %v (a hung mount?)", d)
				}
			}
			return out
		}
	}
	return out
}

// ForgetWaitingFileRecords is the owner's explicit "forget these records" for
// the journal records that wait for a folder (WaitingForFolder): a pass found
// the file not there and its folder missing or empty, so it cannot say whether
// the file is gone, and keeps the record for good. When the folder is gone for
// good (the files were removed with it, or that part of the library was
// retired) nothing else ever settles them, and each pass reports them again.
//
// It forgets only what the owner was shown. shown are the paths the dialog
// listed (WaitingFileRecordPaths); a path that has become waiting since is not
// in them and is not forgotten, a path in them that is no longer waiting is
// skipped (NoLongerWaiting), and a path whose folder holds something again, or
// whose file is back, is kept (Kept): each is re-read from the disk just before
// the forget, so the action acts on the disk as it is now and not on a mark that
// can be days old. It also refuses while the library root itself is not ready,
// since then every folder reads as missing.
//
// It deletes records only (the keys under pendingFilePrefix of the paths that
// pass), in one durable write: never a file, never a book row, never a record a
// pass will still carry out (a path with an unmarked record is not "waiting"
// and is never forgotten). Records are marked only by a pass that looked, so a
// record no pass has looked at yet is not forgotten: run the purge (it finishes
// them or marks them) and forget afterwards.
//
// It writes an audit row naming the actor and EVERY forgotten path (at most
// forgetMaxPaths), and refuses when no activity log is wired
// (ErrAuditUnavailable), as every other unrecorded destructive action here is.
// If the row cannot be written after the records are gone, every forgotten path
// is logged instead. It is serialized with a purge's deferred removal and with
// the replay (replayMu), which mark and settle the same records, and waits for
// them at most forgetLockWait (ErrPurgeBusy).
func (svc *AudiobookService) ForgetWaitingFileRecords(ctx context.Context, actor string, shown []string) (*ForgetWaitingResult, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	rec := svc.recorder()
	if rec == nil {
		return nil, ErrAuditUnavailable
	}
	var paths []string
	seen := map[string]struct{}{}
	for _, p := range shown {
		if _, dup := seen[p]; dup || p == "" {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return nil, ErrForgetNothingShown
	}
	if len(paths) > forgetMaxPaths {
		return nil, fmt.Errorf("%w: %d paths were sent, at most %d at a time", ErrForgetTooMany, len(paths), forgetMaxPaths)
	}
	root := config.Snapshot().RootDir
	if err := svc.libraryRootReady(root); err != nil {
		return nil, fmt.Errorf("nothing forgotten: %w", err)
	}
	if err := svc.lockReplayWithin(ctx, forgetLockWait); err != nil {
		return nil, err
	}
	defer svc.replayMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, _, _, err := svc.readPendingFiles()
	if err != nil {
		return nil, fmt.Errorf("read the journal of pending file removals: %w", err)
	}
	waiting := map[string]*pendingFile{}
	for _, f := range files.sorted() {
		if f.allWaiting() {
			waiting[f.path] = f
		}
	}
	res := &ForgetWaitingResult{ForgottenPaths: []string{}}
	var candidates []*pendingFile
	var candidatePaths []string
	for _, p := range paths {
		f := waiting[p]
		if f == nil {
			res.NoLongerWaiting++
			continue
		}
		candidates = append(candidates, f)
		// The stored path, not the request's string: the two are equal here, but
		// only the stored one was journaled by the server.
		candidatePaths = append(candidatePaths, f.path)
	}
	reasons := recheckWaiting(candidatePaths, root, rootStatTimeout)
	var ids []string
	for i, f := range candidates {
		if reasons[i] != "" {
			res.Kept = append(res.Kept, ForgetKept{Path: f.path, Reason: reasons[i]})
			continue
		}
		ids = append(ids, f.waitingIDs...)
		res.ForgottenPaths = append(res.ForgottenPaths, f.path)
	}
	res.Records, res.Paths = len(ids), len(res.ForgottenPaths)
	if len(ids) == 0 {
		return res, nil
	}
	if err := svc.forgetPendingFiles(ids); err != nil {
		return nil, fmt.Errorf("forget %d pending file removal record(s): %w", len(ids), err)
	}
	if aerr := rec.Record(database.ActivityEntry{
		Timestamp: time.Now().UTC(),
		Tier:      "audit",
		Type:      activity.TypeTrashForgetPendingFileRecords,
		Level:     "info",
		Source:    "trash",
		Summary:   fmt.Sprintf("forgot %d pending file removal record(s) for %d file(s) whose folder was missing or empty; no file and no book was touched", res.Records, res.Paths),
		Details: map[string]any{
			"actor":                   actor,
			"records_forgotten":       res.Records,
			"paths_forgotten":         res.Paths,
			"paths":                   res.ForgottenPaths,
			"paths_sent":              len(paths),
			"paths_no_longer_waiting": res.NoLongerWaiting,
			"paths_kept":              len(res.Kept),
		},
	}); aerr != nil {
		trashLog.Error("forget waiting file records: audit row not written: %s", logger.SanitizeLogValue(aerr.Error()))
		// Nothing may be forgotten untraced: the log is the only record left.
		for _, p := range res.ForgottenPaths {
			trashLog.Error("forget waiting file records: forgotten by %s: %s", logger.SanitizeLogValue(actor), logger.SanitizeLogValue(p))
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("the records were forgotten, but the activity log row was not written (the forgotten paths are in the server log): %v", aerr))
	}
	return res, nil
}
