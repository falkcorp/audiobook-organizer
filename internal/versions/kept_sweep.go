// file: internal/versions/kept_sweep.go
// version: 1.3.2
// guid: 0a0e6a4a-3f6e-4f0c-9b4f-6d7a1e2c8b35
// last-edited: 2026-10-30

package versions

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"

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
// the sweep cannot disagree. A path inside the version's own slot that now comes
// back "remove" is unlinked and dropped from the notes; a path that is already
// gone (its folder vouches for that) is dropped; every other answer, including a
// "remove" for a path outside the slot, leaves the file and its note alone.
//
// Report-only first. The sweep unlinks files nobody asked it to look for, in
// production, so it ships switched off (config kept_sweep_apply_enabled, passed in
// as KeptSweepOptions.Apply by the wiring layer once per run). Off, it makes every
// decision and reports what it WOULD do (KeptSweepResult.WouldRemoveFiles,
// WouldDropFiles: version, book, path and the reason the file had been kept), and
// removes no file and drops no note: the gate sits ahead of both the unlink and
// the note drop. It is not a read-only run, though: a note that really cannot be
// settled (see below) is still counted, and at the third night the version's
// status moves to purge_failed and the unsettled notes into PurgeFailed. Those
// are writes to the version record, never to a file. The owner reads a night or
// two of the would-remove list and flips the switch. On, it behaves as described
// above.
//
// A note it cannot settle. A note the sweep cannot decide (an owner lookup that
// errors, a path the disk cannot settle, an unlink that fails) would be retried,
// and warned about, every night for ever. So the nights are counted, on the
// version, in the purge's own counter (BookVersion.PurgeUndecidedNights): a night
// that leaves such a note and removes or drops nothing adds one (at most one a
// calendar day: PurgeUndecidedNightOn), any other night resets it, and at
// PurgeUndecidedEscalateAfter in a row the version moves from inactive_purged to
// purge_failed, with the unsettled notes in PurgeFailed. That is the purge's own
// exit for a version that can never finish: the trashed-versions page lists it
// with its files and reasons, and the owner retries (Purge now, which decides
// every noted file again) or hard-deletes it. The unsettled notes leave the sweep
// (it stops warning about them); the notes still in PurgeKept stay swept, every
// night, and are never escalated again. Only real "could not settle" counts. A
// decision that would only have removed something (report-only), a book that
// serves nothing on disk (the purge's ErrPurgeNothingServed: only fixing the book
// changes that), and anything environmental (the library not ready, the merge
// lock busy, a version write that failed on an earlier path) is not a night: none
// of those says anything about the note.
//
// Only inside the slot. The sweep removes a noted file ONLY when it lies inside
// the version's own .versions/{id}/ slot (classifyNotedPath). A noted file
// anywhere else is reported and kept, with OutsideSlotNeverAutoRemovedReason,
// whether or not the version's own book still exists: the notes cannot prove that
// nothing wants it (a book trashed or hard-deleted since, a row stored under a
// spelling no lookup produces, a path reused by a re-import before the scan wrote
// its row), and the owner is asked about those, not the sweep. Such a note stays
// in PurgeKept for good, and the file is listed every night as kept.
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

// KeptSweepOptions is how the caller runs the sweep.
type KeptSweepOptions struct {
	// Apply is the kept_sweep_apply_enabled switch, read once per run by the
	// caller. False (report-only) decides everything and unlinks no file and drops
	// no note. It still writes the version record when a note really cannot be
	// settled: the night count, and at the third night the move to purge_failed
	// with the unsettled notes in PurgeFailed.
	Apply bool
	// Skip names versions the caller has just purged, and so just asked.
	Skip map[string]struct{}
	// Now is the run's clock: the calendar day a night is counted on
	// (BookVersion.PurgeUndecidedNightOn) and the time a version is stamped
	// purge_failed. The cleanup passes the one it started the run with, so a
	// whole run (the purges and the sweep) has one clock. Zero means time.Now().
	Now time.Time
}

// KeptSweepResult is what one sweep did, in files (paths), not versions.
type KeptSweepResult struct {
	// ReportOnly is true when the sweep ran without Apply: Removed and Gone are
	// then always zero, and WouldRemove and WouldDrop say what Apply would do.
	ReportOnly bool
	// Versions is how many purged versions with kept-file notes the sweep looked
	// at (versions purged by the same run are skipped: the purge just asked).
	Versions int
	// Removed counts files unlinked: no row or book names them any more.
	Removed int
	// Gone counts notes dropped without an unlink: the file was already gone and
	// its folder vouches for that.
	Gone int
	// WouldRemove counts files a report-only run found nothing naming any more:
	// the files Apply would unlink. WouldDrop counts notes of files that are
	// already gone, which Apply would drop. Neither was touched.
	WouldRemove int
	WouldDrop   int
	// StillKept counts files left in place because something still owns them: a
	// row or book that names them, a live book that serves them, or the guard
	// (an import path, the iTunes library, .failed, outside the library root, a
	// folder that still holds files); and every file outside the version's own
	// .versions/{id}/ slot, which the sweep never removes
	// (OutsideSlotNeverAutoRemovedReason).
	StillKept int
	// OutsideSlot counts, within StillKept, the files kept only because they lie
	// outside the version's own .versions/{id}/ slot, which the sweep never
	// removes (OutsideSlotNeverAutoRemovedReason). They are listed in
	// OutsideSlotFiles whatever the mode, so the owner can see what the sweep
	// will not touch and decide those files by hand.
	OutsideSlot int
	// KeptOnError counts files left in place because the sweep could not decide or
	// could not act: a read error, a path the disk cannot settle, a guard that is
	// not ready, a busy merge lock, a failed removal or record.
	KeptOnError int

	// RemovedFiles, GoneFiles, WouldRemoveFiles, WouldDropFiles and ErrorFiles list
	// the paths behind the counts, per version, each with its reason: for the
	// first four the reason the file had been kept, for ErrorFiles the error.
	RemovedFiles     []KeptVersion
	GoneFiles        []KeptVersion
	WouldRemoveFiles []KeptVersion
	WouldDropFiles   []KeptVersion
	ErrorFiles       []KeptVersion
	// OutsideSlotFiles lists the files behind OutsideSlot, per version.
	OutsideSlotFiles []KeptVersion
	// Escalated lists the versions this run moved to purge_failed because a note
	// stayed unsettled for PurgeUndecidedEscalateAfter nights in a row (also in a
	// report-only run); Files are the unsettled notes (now
	// BookVersion.PurgeFailed), with the reason each could not be settled. They
	// are not listed as kept on error from the next night on.
	Escalated []KeptVersion
}

func (r *KeptSweepResult) merge(o KeptSweepResult) {
	r.Versions += o.Versions
	r.Removed += o.Removed
	r.Gone += o.Gone
	r.WouldRemove += o.WouldRemove
	r.WouldDrop += o.WouldDrop
	r.StillKept += o.StillKept
	r.OutsideSlot += o.OutsideSlot
	r.KeptOnError += o.KeptOnError
	r.RemovedFiles = append(r.RemovedFiles, o.RemovedFiles...)
	r.GoneFiles = append(r.GoneFiles, o.GoneFiles...)
	r.WouldRemoveFiles = append(r.WouldRemoveFiles, o.WouldRemoveFiles...)
	r.WouldDropFiles = append(r.WouldDropFiles, o.WouldDropFiles...)
	r.ErrorFiles = append(r.ErrorFiles, o.ErrorFiles...)
	r.OutsideSlotFiles = append(r.OutsideSlotFiles, o.OutsideSlotFiles...)
	r.Escalated = append(r.Escalated, o.Escalated...)
}

// sweepable is whether the sweep looks at v: an inactive_purged or purge_failed
// version with notes in PurgeKept.
func sweepable(v *database.BookVersion) bool {
	return (v.Status == database.BookVersionStatusInactivePurged || v.Status == database.BookVersionStatusPurgeFailed) && len(v.PurgeKept) > 0
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
	// counts is set on a keptError that is a real "could not settle": it counts
	// toward escalation. It is false for a would-remove that could not go ahead
	// because the book serves nothing on disk (the purge's ErrPurgeNothingServed,
	// which only fixing the book changes), and for environmental failures.
	counts bool
}

// judgeKept asks the purge's own question of every note of ver. It only reads.
// A removal is allowed only while the version's own book is known to serve a
// file that exists, as a trashed version's purge requires: with none, the served
// guard has nothing to compare against and "nothing is served" would be a guess.
// That downgrade never counts toward escalation, in either mode: it says the
// book's path is stale or lost, which only fixing the book changes, and it is the
// same refusal the purge makes (ErrPurgeNothingServed), which the purge does not
// count either.
func judgeKept(store versionPurger, decider PurgeDecider, ver *database.BookVersion) ([]keptJudgement, *servedGuard) {
	out := make([]keptJudgement, len(ver.PurgeKept))
	sg, err := rebuildServedGuard(store, ver)
	if err != nil {
		for i, n := range ver.PurgeKept {
			out[i] = keptJudgement{note: n, action: keptError, why: err.Error(), counts: true}
		}
		return out, nil
	}
	removable := false
	for i, n := range ver.PurgeKept {
		j := keptJudgement{note: n}
		verdict, why, err := classifyNotedPath(store, decider, sg, ver, n.Path, nil)
		switch {
		case err != nil:
			j.action, j.why, j.counts = keptError, err.Error(), true
		case verdict == verdictRemove:
			j.action = keptRemove
			removable = true
		case verdict == verdictGone:
			j.action = keptGone
		case verdict == verdictUndecided:
			j.action, j.why, j.counts = keptError, why, true
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
	return out, sg
}

// SweepKeptFiles revisits the files purged versions kept (see the comment
// above). opts.Skip names versions the caller has just purged and so just asked;
// opts.Apply is the report-only switch.
//
// The listing error is the one error returned; everything else is counted in the
// result. Concurrency: versions are partitioned into groups that share no path
// (two versions that note the same file are one group, so no two workers ever
// handle the same path or the same version), and a bounded pool works the groups.
// A worker decides without the merge lock first and takes it, as the purge does,
// only for a version that has something to remove or drop (Apply) or a note it
// cannot settle to count; the lock is what the writers of book_file rows hold, so
// none can add an owner between the decision under it and the unlink. The
// decision is made again under the lock for every path, from a fresh read of the
// version.
func SweepKeptFiles(store KeptSweepStore, guard PurgeGuard, opts KeptSweepOptions) (KeptSweepResult, error) {
	res := KeptSweepResult{ReportOnly: !opts.Apply}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if guard == nil {
		return res, errors.New("kept-file sweep: no purge guard is configured, so nothing is deleted")
	}
	purged, err := store.ListPurgedBookVersions()
	if err != nil {
		return res, fmt.Errorf("kept-file sweep: list purged versions: %w", err)
	}
	// A purge_failed version keeps its PurgeKept notes too (the files the purge, or
	// an earlier sweep, left because something owned them), and nothing else looks
	// at them: the nightly cleanup does not retry it, and the trashed-versions page
	// lists only its PurgeFailed files. They are swept, never re-escalated.
	trash, err := store.ListTrashedBookVersions()
	if err != nil {
		return res, fmt.Errorf("kept-file sweep: list trashed versions: %w", err)
	}
	purged = append(purged, trash...)
	var cands []database.BookVersion
	for i := range purged {
		v := &purged[i]
		if !sweepable(v) {
			continue
		}
		if _, ok := opts.Skip[v.ID]; ok {
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
				results[gi].merge(sweepVersion(store, guard, &cands[idx], opts.Apply, now, &busy))
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
	find := func(i int) int {
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
func sweepVersion(store KeptSweepStore, guard PurgeGuard, snap *database.BookVersion, apply bool, now time.Time, busy *atomic.Bool) KeptSweepResult {
	res := KeptSweepResult{Versions: 1}
	// First look, no lock: most versions have nothing to do (their other owner is
	// still there) and must not queue behind a merge for it. Report-only never
	// acts on a note, so a version needs the lock only to count a note it cannot
	// settle (or to reset that count).
	look := *snap
	judged, _ := judgeKept(store, guard.NewDecider(), &look)
	acts := apply && slices.ContainsFunc(judged, func(j keptJudgement) bool { return j.action == keptRemove || j.action == keptGone })
	// Nights are counted only for an inactive_purged version: a purge_failed one
	// has been escalated already, and is not escalated again.
	counting := snap.Status == database.BookVersionStatusInactivePurged
	if !acts && !(counting && (countedErrors(judged) > 0 || snap.PurgeUndecidedNights > 0)) {
		tally(&res, snap, judged)
		return res
	}
	// The lock could not be had: nothing is done to this version tonight, and
	// nothing is counted against it. What would have been removed or dropped is
	// kept on error, as it always was.
	notActed := func(why string) KeptSweepResult {
		for i := range judged {
			if judged[i].action == keptRemove || judged[i].action == keptGone {
				if apply {
					judged[i].action, judged[i].why = keptError, why
				}
			}
		}
		tally(&res, snap, judged)
		return res
	}
	if busy.Load() {
		return notActed("a merge or combine holds the merge lock; the sweep skips the rest of this run")
	}
	if err := guard.RootReady(); err != nil {
		return notActed(err.Error())
	}
	unlock, err := merge.TryLockMergeRMWWithin(BackgroundMergeLockWait)
	if err != nil {
		if errors.Is(err, merge.ErrMergeBusy) {
			busy.Store(true)
			lifecycleLog.Warn("kept-file sweep: a merge or combine held the merge lock for %s; the sweep skips the rest of this run", BackgroundMergeLockWait)
		}
		return notActed(err.Error())
	}
	defer unlock()
	// The wait can last minutes, and the library can be unmounted in that time.
	if err := guard.RootReady(); err != nil {
		return notActed(err.Error())
	}

	// Under the lock: the version as it is now, and every path decided again.
	fresh, err := store.GetBookVersion(snap.ID)
	if err != nil {
		return notActed(fmt.Sprintf("read the version again under the merge lock: %v", err))
	}
	if fresh == nil || !sweepable(fresh) {
		// Hard-deleted, purged again, or its notes already settled, while this
		// waited: nothing is owed.
		return KeptSweepResult{}
	}
	decider := guard.NewDecider()
	judged, sg := judgeKept(store, decider, fresh)

	// Files that may be unlinked are unlinked one at a time, and the note of each
	// is dropped (and the version written) right after, never before.
	res = KeptSweepResult{Versions: 1}
	var removed, gone, wouldRemove, wouldDrop, errs, counted, still []database.PurgeFileNote
	stopped := ""
	for _, j := range judged {
		if !apply && (j.action == keptRemove || j.action == keptGone) {
			// THE REPORT-ONLY GATE. Ahead of the unlink and ahead of the note drop,
			// and of the second look that precedes the unlink: nothing below this
			// point runs when the switch is off. The reason is the one the file had
			// been kept for, so the owner can see what changed.
			n := database.PurgeFileNote{Path: j.note.Path, Reason: j.note.Reason}
			if j.action == keptRemove {
				wouldRemove = append(wouldRemove, n)
			} else {
				wouldDrop = append(wouldDrop, n)
			}
			continue
		}
		if stopped != "" && (j.action == keptRemove || j.action == keptGone) {
			errs = append(errs, database.PurgeFileNote{Path: j.note.Path, Reason: stopped})
			continue
		}
		act := j.action
		if act == keptRemove {
			// The owners and the decision again, for this path, right before its
			// unlink, as the purge does (purgeVersionLocked): a writer that does not
			// take the merge lock (the library scan) can have started naming it since
			// the pass above, and that pass is older by every removal and record
			// written before this one.
			verdict, why, cerr := classifyNotedPath(store, decider, sg, fresh, j.note.Path, nil)
			switch {
			case cerr != nil:
				n := database.PurgeFileNote{Path: j.note.Path, Reason: cerr.Error()}
				errs, counted = append(errs, n), append(counted, n)
				continue
			case verdict == verdictUndecided:
				n := database.PurgeFileNote{Path: j.note.Path, Reason: why}
				errs, counted = append(errs, n), append(counted, n)
				continue
			case verdict == verdictServed || verdict == verdictKept:
				if verdict == verdictServed {
					why = KeptReason
				}
				still = append(still, database.PurgeFileNote{Path: j.note.Path, Reason: why})
				lifecycleLog.Warn("kept-file sweep: version %s: keeping %s: %s (decided since the sweep began)", fresh.ID, j.note.Path, why)
				continue
			case verdict == verdictGone:
				// Gone since the pass above, and its folder vouches for that.
				act = keptGone
			}
		}
		switch act {
		case keptKeep:
			still = append(still, database.PurgeFileNote{Path: j.note.Path, Reason: j.why})
			continue
		case keptError:
			n := database.PurgeFileNote{Path: j.note.Path, Reason: j.why}
			errs = append(errs, n)
			if j.counts {
				counted = append(counted, n)
			}
			continue
		case keptRemove:
			err := removeFile(j.note.Path)
			if errors.Is(err, fs.ErrNotExist) {
				// It was there at the decision and is not now: someone else removed
				// it, or the filesystem under it went away. Not counted as removed;
				// the next run decides it from the disk.
				n := database.PurgeFileNote{Path: j.note.Path,
					Reason: "it was not there to remove, and this sweep did not unlink it, so it cannot tell whether it is gone"}
				errs, counted = append(errs, n), append(counted, n)
				continue
			}
			if err != nil {
				lifecycleLog.Warn("kept-file sweep: version %s (book %s): remove %s failed: %v", fresh.ID, fresh.BookID, j.note.Path, err)
				n := database.PurgeFileNote{Path: j.note.Path, Reason: fmt.Sprintf("remove failed: %v", err)}
				errs, counted = append(errs, n), append(counted, n)
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
	res.WouldRemove, res.WouldDrop = len(wouldRemove), len(wouldDrop)
	res.RemovedFiles, res.GoneFiles, res.ErrorFiles = file(removed), file(gone), file(errs)
	outside := outsideSlotNotes(still)
	res.OutsideSlot, res.OutsideSlotFiles = len(outside), file(outside)
	res.WouldRemoveFiles, res.WouldDropFiles = file(wouldRemove), file(wouldDrop)

	// The folders the removals emptied, only when no note is left: a folder
	// pruned while a noted file of the slot is not there yet would make that file
	// read as "its folder is missing too" for ever (finishPurge has the same rule).
	// Not for a purge_failed version: files of its slot may still be waiting for
	// the retry, and the retry prunes when it finishes.
	if stopped == "" && fresh.Status == database.BookVersionStatusInactivePurged && (len(removed) > 0 || len(gone) > 0) && len(fresh.PurgeKept) == 0 {
		if _, bookDir, _, derr := versionBookDir(store, fresh); derr != nil {
			lifecycleLog.Warn("kept-file sweep: version %s: cannot locate its folder to tidy it: %v", fresh.ID, derr)
		} else if bookDir != "" {
			pruneVersionDirs(decider, bookDir, fresh.ID)
		}
	}
	if fresh.Status == database.BookVersionStatusInactivePurged {
		settleNights(store, fresh, &res, counted, len(removed)+len(gone) > 0, now)
	}
	return res
}

// countedErrors is how many judged notes are a real "could not settle".
func countedErrors(judged []keptJudgement) int {
	n := 0
	for _, j := range judged {
		if j.action == keptError && j.counts {
			n++
		}
	}
	return n
}

// settleNights counts the night against the version and escalates it, as the
// purge's settleStuck does for a purging version. counted are the notes that really
// could not be settled tonight; progress says the sweep removed or dropped
// something. A night with progress, or with nothing to count, resets the count; any
// other night adds one; at PurgeUndecidedEscalateAfter the version becomes
// purge_failed with the counted notes in PurgeFailed and the rest still in
// PurgeKept. The version is the fresh copy read under the merge lock and written
// by this sweep, so the write is its own. now is the run's clock.
func settleNights(store KeptSweepStore, ver *database.BookVersion, res *KeptSweepResult, counted []database.PurgeFileNote, progress bool, now time.Time) {
	was, wasOn := ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn
	today := now.Format(time.DateOnly)
	nights, on := 0, ""
	if !progress && len(counted) > 0 {
		// One night is one calendar day: a sweep that runs again the same day (a
		// manual run, a retry) leaves the count where the first run put it.
		nights, on = nextNightCount(was, wasOn, today)
	}
	if nights >= PurgeUndecidedEscalateAfter {
		prevKept, prevStatus := ver.PurgeKept, ver.Status
		slices.SortFunc(counted, func(a, b database.PurgeFileNote) int { return strings.Compare(a.Path, b.Path) })
		ver.Status = database.BookVersionStatusPurgeFailed
		ver.PurgeFailed = counted
		ver.PurgeFailedAt = &now
		ver.PurgeKept = slices.DeleteFunc(slices.Clone(prevKept), func(n database.PurgeFileNote) bool {
			return slices.ContainsFunc(counted, func(c database.PurgeFileNote) bool { return c.Path == n.Path })
		})
		ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn = 0, ""
		if err := store.UpdateBookVersion(ver); err != nil {
			ver.Status, ver.PurgeFailed, ver.PurgeFailedAt, ver.PurgeKept, ver.PurgeUndecidedNights = prevStatus, nil, nil, prevKept, was
			ver.PurgeUndecidedNightOn = wasOn
			lifecycleLog.Warn("kept-file sweep: version %s (book %s): %d night(s) in a row could not settle %s, and recording that failed: %v",
				ver.ID, ver.BookID, nights, notePaths(counted), err)
			return
		}
		lifecycleLog.Warn("kept-file sweep: version %s (book %s): %d night(s) in a row could not settle %s; the version is now purge_failed, no longer swept, and the owner can retry it or hard-delete it",
			ver.ID, ver.BookID, nights, notePaths(counted))
		res.Escalated = append(res.Escalated, KeptVersion{VersionID: ver.ID, BookID: ver.BookID, Files: counted})
		return
	}
	if nights == was && on == wasOn {
		return
	}
	ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn = nights, on
	if err := store.UpdateBookVersion(ver); err != nil {
		ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn = was, wasOn
		lifecycleLog.Warn("kept-file sweep: version %s: cannot store the night count (%d): %v; escalation is delayed", ver.ID, nights, err)
	}
}

// outsideSlotNotes are the kept notes whose reason is the outside-the-slot rule.
func outsideSlotNotes(kept []database.PurgeFileNote) []database.PurgeFileNote {
	var out []database.PurgeFileNote
	for _, n := range kept {
		if n.Reason == OutsideSlotNeverAutoRemovedReason {
			out = append(out, n)
		}
	}
	return out
}

// tally counts judged notes that no pass acted on: the ones something still owns,
// the ones that could not be decided or acted on, and, in a report-only run, the
// files and notes Apply would remove or drop.
func tally(res *KeptSweepResult, ver *database.BookVersion, judged []keptJudgement) {
	var still, errs, wouldRemove, wouldDrop []database.PurgeFileNote
	for _, j := range judged {
		switch j.action {
		case keptKeep:
			still = append(still, database.PurgeFileNote{Path: j.note.Path, Reason: j.why})
		case keptError:
			errs = append(errs, database.PurgeFileNote{Path: j.note.Path, Reason: j.why})
		case keptRemove:
			wouldRemove = append(wouldRemove, database.PurgeFileNote{Path: j.note.Path, Reason: j.note.Reason})
		case keptGone:
			wouldDrop = append(wouldDrop, database.PurgeFileNote{Path: j.note.Path, Reason: j.note.Reason})
		}
	}
	res.StillKept += len(still)
	if outside := outsideSlotNotes(still); len(outside) > 0 {
		res.OutsideSlot += len(outside)
		res.OutsideSlotFiles = append(res.OutsideSlotFiles, KeptVersion{VersionID: ver.ID, BookID: ver.BookID, Files: outside})
	}
	res.KeptOnError += len(errs)
	res.WouldRemove += len(wouldRemove)
	res.WouldDrop += len(wouldDrop)
	if len(wouldRemove) > 0 {
		res.WouldRemoveFiles = append(res.WouldRemoveFiles, KeptVersion{VersionID: ver.ID, BookID: ver.BookID, Files: wouldRemove})
	}
	if len(wouldDrop) > 0 {
		res.WouldDropFiles = append(res.WouldDropFiles, KeptVersion{VersionID: ver.ID, BookID: ver.BookID, Files: wouldDrop})
	}
	if len(errs) > 0 {
		res.ErrorFiles = append(res.ErrorFiles, KeptVersion{VersionID: ver.ID, BookID: ver.BookID, Files: errs})
		lifecycleLog.Warn("kept-file sweep: version %s (book %s): %d file(s) kept because the sweep could not decide or act: %s",
			ver.ID, ver.BookID, len(errs), notePaths(errs))
	}
}
