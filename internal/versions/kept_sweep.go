// file: internal/versions/kept_sweep.go
// version: 1.0.0
// guid: 0a0e6a4a-3f6e-4f0c-9b4f-6d7a1e2c8b35
// last-edited: 2026-10-29

package versions

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// The kept-file sweep.
//
// A purge leaves a file on disk when another book_file row, or another book
// (a trashed one included), still names it, and records that on the version
// (database.BookVersion.PurgeKept). Nothing looked at those files again: once
// the trashed book was hard-deleted, or the lingering row gone, the file sat on
// disk with no owner and no record that anything wanted it removed.
//
// SweepKeptFiles is the nightly cleanup's second step. For each purged version
// that still has PurgeKept notes it asks, of each noted path, the question the
// purge asks of every path (classifyPurgePath): is it served by a live book, does
// another row or book name it (every spelling of the path), does the guard keep
// it (an import path, the iTunes library, .failed, outside the library root, a
// folder that still holds files), and can the disk say whether it is gone. It
// is the same function with the same store and the same guard, so the purge and
// the sweep cannot disagree. A path that now comes back "remove" is unlinked and
// dropped from the notes; a path that is already gone (its folder vouches for
// that) is dropped; every other answer leaves the file and its note alone.
//
// Fail closed, everywhere: a read that errors, a path the disk cannot settle, a
// guard that is not ready, a merge lock that stays busy, a removal that fails, a
// record that cannot be written. Each keeps the file and the note, counts as
// "kept on error" and is retried by the next run.
//
// What a removal records. A version purge keeps no per-file undo journal: a
// purged file is gone for good, and its only durable trace is the record the
// purge writes on the version (PurgeUnlinked, cleared when the purge ends). The
// sweep keeps to that. The note in PurgeKept is the record that names the file,
// and it is dropped only AFTER the unlink, never before, so a crash between the
// two leaves a note for a file that is not there: the next run reads it as
// "gone" when its folder holds something, and as "cannot tell" (kept, counted as
// kept on error) when the folder is empty or missing, which is also how a
// dataset that is not mounted looks. Every removal is also logged with the
// version, the book and the reason the file had been kept, and reported in
// KeptSweepResult.

// KeptSweepResult is what one sweep did, in files (paths), not versions.
type KeptSweepResult struct {
	// Versions is how many purged versions with kept-file notes the sweep looked
	// at (versions purged by the same run are skipped: the purge just asked).
	Versions int
	// Removed counts files unlinked: no row or book names them any more.
	Removed int
	// Gone counts notes dropped without an unlink: the file was already gone and
	// its folder vouches for that.
	Gone int
	// StillKept counts files left in place because something still owns them: a
	// row or book that names them, a live book that serves them, or the guard
	// (an import path, the iTunes library, .failed, outside the library root, a
	// folder that still holds files).
	StillKept int
	// KeptOnError counts files left in place because the sweep could not decide or
	// could not act: a read error, a path the disk cannot settle, a guard that is
	// not ready, a busy merge lock, a failed removal or record.
	KeptOnError int

	// RemovedFiles, GoneFiles and ErrorFiles list the paths behind the counts, per
	// version, each with its reason. ErrorFiles reasons are the errors.
	RemovedFiles []KeptVersion
	GoneFiles    []KeptVersion
	ErrorFiles   []KeptVersion
}

func (r *KeptSweepResult) merge(o KeptSweepResult) {
	r.Versions += o.Versions
	r.Removed += o.Removed
	r.Gone += o.Gone
	r.StillKept += o.StillKept
	r.KeptOnError += o.KeptOnError
	r.RemovedFiles = append(r.RemovedFiles, o.RemovedFiles...)
	r.GoneFiles = append(r.GoneFiles, o.GoneFiles...)
	r.ErrorFiles = append(r.ErrorFiles, o.ErrorFiles...)
}

// keptSweepWorkers bounds the sweep's worker pool. A var only so a test can pin
// it.
var keptSweepWorkers = min(runtime.NumCPU(), 8)

// keptAction is what the sweep does with one note.
type keptAction int

const (
	keptKeep   keptAction = iota // something owns it: leave the file and the note
	keptError                    // could not decide: leave the file and the note
	keptRemove                   // unlink the file, drop the note
	keptGone                     // already gone: drop the note
)

type keptJudgement struct {
	note   database.PurgeFileNote
	action keptAction
	why    string
}

// judgeKept asks the purge's own question of every note of ver. It only reads.
// A removal is allowed only while the version's own book is known to serve a
// file that exists, as a trashed version's purge requires: with none, the served
// guard has nothing to compare against and "nothing is served" would be a guess.
func judgeKept(store versionPurger, decider PurgeDecider, ver *database.BookVersion) []keptJudgement {
	out := make([]keptJudgement, len(ver.PurgeKept))
	failAll := func(err error) []keptJudgement {
		for i, n := range ver.PurgeKept {
			out[i] = keptJudgement{note: n, action: keptError, why: err.Error()}
		}
		return out
	}
	sg, err := rebuildServedGuard(store, ver)
	if err != nil {
		return failAll(err)
	}
	removable := false
	for i, n := range ver.PurgeKept {
		j := keptJudgement{note: n}
		verdict, why, err := classifyPurgePath(store, decider, sg, ver, n.Path, nil)
		switch {
		case err != nil:
			j.action, j.why = keptError, err.Error()
		case verdict == verdictRemove:
			j.action = keptRemove
			removable = true
		case verdict == verdictGone:
			j.action = keptGone
		case verdict == verdictUndecided:
			j.action, j.why = keptError, why
		default: // verdictKept, verdictServed
			j.action, j.why = keptKeep, why
		}
		out[i] = j
	}
	if removable {
		if err := sg.requireServedFileOnDisk(); err != nil {
			for i := range out {
				if out[i].action == keptRemove {
					out[i].action, out[i].why = keptError, err.Error()
				}
			}
		}
	}
	return out
}

// SweepKeptFiles revisits the files purged versions kept (see the comment
// above). skip names versions the caller has just purged and so just asked.
//
// The listing error is the one error returned; everything else is counted in the
// result. Concurrency: versions are partitioned into groups that share no path
// (two versions that note the same file are one group, so no two workers ever
// handle the same path or the same version), and a bounded pool works the groups.
// A worker decides without the merge lock first and takes it, as the purge does,
// only for a version that has something to remove or drop; the lock is what the
// writers of book_file rows hold, so none can add an owner between the decision
// under it and the unlink. The decision is made again under the lock for every
// path, from a fresh read of the version.
func SweepKeptFiles(store KeptSweepStore, guard PurgeGuard, skip map[string]struct{}) (KeptSweepResult, error) {
	var res KeptSweepResult
	if guard == nil {
		return res, errors.New("kept-file sweep: no purge guard is configured, so nothing is deleted")
	}
	purged, err := store.ListPurgedBookVersions()
	if err != nil {
		return res, fmt.Errorf("kept-file sweep: list purged versions: %w", err)
	}
	var cands []database.BookVersion
	for i := range purged {
		v := &purged[i]
		if v.Status != database.BookVersionStatusInactivePurged || len(v.PurgeKept) == 0 {
			continue
		}
		if _, ok := skip[v.ID]; ok {
			continue
		}
		cands = append(cands, *v)
	}
	if len(cands) == 0 {
		return res, nil
	}
	slices.SortFunc(cands, func(a, b database.BookVersion) int { return strings.Compare(a.ID, b.ID) })
	groups := groupBySharedPath(cands)

	var busy atomic.Bool
	results := make([]KeptSweepResult, len(groups))
	var eg errgroup.Group
	eg.SetLimit(max(keptSweepWorkers, 1))
	for gi := range groups {
		eg.Go(func() error {
			for _, idx := range groups[gi] {
				results[gi].merge(sweepVersion(store, guard, &cands[idx], &busy))
			}
			return nil
		})
	}
	_ = eg.Wait() // the workers return no error: every outcome is in the results
	for _, r := range results {
		res.merge(r)
	}
	return res, nil
}

// groupBySharedPath partitions cands (indexes) into groups no two of which note
// the same file, comparing paths by targetKey (the folder resolved, the file name
// not) so two spellings of one file land in one group. Each group is handled by
// one worker, in order.
func groupBySharedPath(cands []database.BookVersion) [][]int {
	parent := make([]int, len(cands))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	for i := range cands {
		for _, n := range cands[i].PurgeKept {
			k := targetKey(n.Path)
			if j, ok := owner[k]; ok {
				if a, b := find(i), find(j); a != b {
					parent[b] = a
				}
				continue
			}
			owner[k] = i
		}
	}
	byRoot := map[int][]int{}
	var order []int
	for i := range cands {
		r := find(i)
		if _, ok := byRoot[r]; !ok {
			order = append(order, r)
		}
		byRoot[r] = append(byRoot[r], i)
	}
	groups := make([][]int, 0, len(order))
	for _, r := range order {
		groups = append(groups, byRoot[r])
	}
	return groups
}

// sweepVersion sweeps one purged version.
func sweepVersion(store KeptSweepStore, guard PurgeGuard, snap *database.BookVersion, busy *atomic.Bool) KeptSweepResult {
	res := KeptSweepResult{Versions: 1}
	// First look, no lock: most versions have nothing to do (their other owner is
	// still there) and must not queue behind a merge for it.
	look := *snap
	judged := judgeKept(store, guard.NewDecider(), &look)
	if !slices.ContainsFunc(judged, func(j keptJudgement) bool { return j.action == keptRemove || j.action == keptGone }) {
		tally(&res, snap, judged)
		return res
	}
	failActionable := func(why string) KeptSweepResult {
		for i := range judged {
			if judged[i].action == keptRemove || judged[i].action == keptGone {
				judged[i].action, judged[i].why = keptError, why
			}
		}
		tally(&res, snap, judged)
		return res
	}
	if busy.Load() {
		return failActionable("a merge or combine holds the merge lock; the sweep skips the rest of this run")
	}
	if err := guard.RootReady(); err != nil {
		return failActionable(err.Error())
	}
	unlock, err := merge.TryLockMergeRMWWithin(BackgroundMergeLockWait)
	if err != nil {
		if errors.Is(err, merge.ErrMergeBusy) {
			busy.Store(true)
			lifecycleLog.Warn("kept-file sweep: a merge or combine held the merge lock for %s; the sweep skips the rest of this run", BackgroundMergeLockWait)
		}
		return failActionable(err.Error())
	}
	defer unlock()
	// The wait can last minutes, and the library can be unmounted in that time.
	if err := guard.RootReady(); err != nil {
		return failActionable(err.Error())
	}

	// Under the lock: the version as it is now, and every path decided again.
	fresh, err := store.GetBookVersion(snap.ID)
	if err != nil {
		return failActionable(fmt.Sprintf("read the version again under the merge lock: %v", err))
	}
	if fresh == nil || fresh.Status != database.BookVersionStatusInactivePurged || len(fresh.PurgeKept) == 0 {
		// Hard-deleted, or its notes already settled, while this waited: nothing
		// is owed.
		return KeptSweepResult{}
	}
	decider := guard.NewDecider()
	judged = judgeKept(store, decider, fresh)

	// Files that may be unlinked are unlinked one at a time, and the note of each
	// is dropped (and the version written) right after, never before.
	res = KeptSweepResult{Versions: 1}
	var removed, gone, errs, still []database.PurgeFileNote
	stopped := ""
	for _, j := range judged {
		if stopped != "" && (j.action == keptRemove || j.action == keptGone) {
			errs = append(errs, database.PurgeFileNote{Path: j.note.Path, Reason: stopped})
			continue
		}
		switch j.action {
		case keptKeep:
			still = append(still, database.PurgeFileNote{Path: j.note.Path, Reason: j.why})
			continue
		case keptError:
			errs = append(errs, database.PurgeFileNote{Path: j.note.Path, Reason: j.why})
			continue
		case keptRemove:
			err := removeFile(j.note.Path)
			if errors.Is(err, fs.ErrNotExist) {
				// It was there at the decision and is not now: someone else removed
				// it, or the filesystem under it went away. Not counted as removed;
				// the next run decides it from the disk.
				errs = append(errs, database.PurgeFileNote{Path: j.note.Path,
					Reason: "it was not there to remove, and this sweep did not unlink it, so it cannot tell whether it is gone"})
				continue
			}
			if err != nil {
				lifecycleLog.Warn("kept-file sweep: version %s (book %s): remove %s failed: %v", fresh.ID, fresh.BookID, j.note.Path, err)
				errs = append(errs, database.PurgeFileNote{Path: j.note.Path, Reason: fmt.Sprintf("remove failed: %v", err)})
				continue
			}
			lifecycleLog.Info("kept-file sweep: removed %s of purged version %s (book %s): nothing names it any more (it was kept: %s)",
				j.note.Path, fresh.ID, fresh.BookID, j.note.Reason)
			removed = append(removed, database.PurgeFileNote{Path: j.note.Path, Reason: j.note.Reason})
		case keptGone:
			lifecycleLog.Info("kept-file sweep: %s of purged version %s (book %s) is already gone; dropping its note (it was kept: %s)",
				j.note.Path, fresh.ID, fresh.BookID, j.note.Reason)
			gone = append(gone, database.PurgeFileNote{Path: j.note.Path, Reason: j.note.Reason})
		}
		// Drop the note and record it, now.
		fresh.PurgeKept = slices.DeleteFunc(slices.Clone(fresh.PurgeKept), func(n database.PurgeFileNote) bool { return n.Path == j.note.Path })
		if err := store.UpdateBookVersion(fresh); err != nil {
			// The file is dealt with and counted as such; its note stays stale in the
			// store, and the next run reads it as gone (its folder holds something)
			// or as cannot-tell (kept, counted as kept on error), never as removable.
			// A record that cannot be written is a removal the next run cannot trust
			// to see, so nothing further is attempted (the purge stops the same way).
			lifecycleLog.Warn("kept-file sweep: version %s: %s was dealt with but recording that failed: %v; stopping, the rest is left untouched", fresh.ID, j.note.Path, err)
			stopped = fmt.Sprintf("not attempted: recording the previous removal failed: %v", err)
		}
	}
	file := func(notes []database.PurgeFileNote) []KeptVersion {
		if len(notes) == 0 {
			return nil
		}
		return []KeptVersion{{VersionID: fresh.ID, BookID: fresh.BookID, Files: notes}}
	}
	res.Removed, res.Gone, res.StillKept, res.KeptOnError = len(removed), len(gone), len(still), len(errs)
	res.RemovedFiles, res.GoneFiles, res.ErrorFiles = file(removed), file(gone), file(errs)

	// The folders the removals emptied, only when no note is left: a folder
	// pruned while a noted file of the slot is not there yet would make that file
	// read as "its folder is missing too" for ever (finishPurge has the same rule).
	if stopped == "" && (len(removed) > 0 || len(gone) > 0) && len(fresh.PurgeKept) == 0 {
		if _, bookDir, _, derr := versionBookDir(store, fresh); derr != nil {
			lifecycleLog.Warn("kept-file sweep: version %s: cannot locate its folder to tidy it: %v", fresh.ID, derr)
		} else if bookDir != "" {
			pruneVersionDirs(decider, bookDir, fresh.ID)
		}
	}
	return res
}

// tally counts judged notes that need no action (and, after failActionable, the
// ones that could not be acted on).
func tally(res *KeptSweepResult, ver *database.BookVersion, judged []keptJudgement) {
	var still, errs []database.PurgeFileNote
	for _, j := range judged {
		switch j.action {
		case keptKeep:
			still = append(still, database.PurgeFileNote{Path: j.note.Path, Reason: j.why})
		case keptError:
			errs = append(errs, database.PurgeFileNote{Path: j.note.Path, Reason: j.why})
		}
	}
	res.StillKept += len(still)
	res.KeptOnError += len(errs)
	if len(errs) > 0 {
		res.ErrorFiles = append(res.ErrorFiles, KeptVersion{VersionID: ver.ID, BookID: ver.BookID, Files: errs})
		lifecycleLog.Warn("kept-file sweep: version %s (book %s): %d file(s) kept because the sweep could not decide or act: %s",
			ver.ID, ver.BookID, len(errs), notePaths(errs))
	}
}
