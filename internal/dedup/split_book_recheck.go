// file: internal/dedup/split_book_recheck.go
// version: 1.1.0
// guid: 5e8c2a47-d1b9-4f63-a0e5-7b3c9d12f684
// last-edited: 2026-10-21

// Merge-time recheck of a stored split-book candidate.
//
// A SplitBookStore row is a snapshot of what the detector saw at scan time.
// The bulk-merge op and POST /dedup/split-book-candidates/:id/merge used to act
// on that snapshot as written, so a row stored before the distinct-works vetoes
// existed (or one whose books were edited, re-tagged or had their durations
// read since) could still merge distinct books into one. Both now go through
// MergeSplitBookClusterRechecked, which re-reads every member and refuses
// unless the CURRENT rows still pass the same tests the Repairs fixer applies
// (internal/plugins/maintenance/split_books_fixer.go) and the detector's vetoes
// (vetoDistinctWorks).
//
// The check lives here, in front of the merge, and not inside
// MergeSplitBookClusterWithOptions: that function is also the merge core of the
// merge-chapter-groups job and of the Repairs fixer, which choose their members
// by other rules and run their own gates.

package dedup

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrSplitBookRefused is what errors.Is matches on a merge that
// MergeSplitBookClusterRechecked refused because the members' current data no
// longer shows them to be chapters of one work. Nothing was written.
var ErrSplitBookRefused = errors.New("split-book merge refused: the members are not shown to be chapters of one work")

// SplitBookRefusal is the error behind ErrSplitBookRefused, carrying one line
// per reason.
type SplitBookRefusal struct {
	Reasons []string
}

func (e *SplitBookRefusal) Error() string {
	return ErrSplitBookRefused.Error() + ": " + strings.Join(e.Reasons, "; ")
}

// Is makes errors.Is(err, ErrSplitBookRefused) true.
func (e *SplitBookRefusal) Is(target error) bool { return target == ErrSplitBookRefused }

// SplitBookRecheckReader is the part of Store the recheck reads through.
type SplitBookRecheckReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// RecheckSplitBookMembers re-reads each member and returns why the cluster must
// not be merged as chapters of one work, one line per reason (empty = no
// objection). An error means a member could not be read at all; the caller must
// not merge on it either.
//
// The tests, all on current rows:
//   - a member that is gone (missing or soft-deleted) or has no stored path
//   - a member with no book_file row: nothing to move and nothing to show it is
//     a chapter
//   - a file row with no recorded duration (<= 0): the fragment fixer's rule,
//     an unknown duration is not "short"
//   - a file row at or over chapterMaxSec (the fragment fixer's duration gate)
//   - every distinct-works veto (vetoDistinctWorks), on the members' current
//     titles, paths and durations
//
// The shape the vetoes need (grandparent: every member in a folder of its own)
// is inferred from the members' current paths by the detector's own rule, not
// read from the stored candidate, so a stale shape cannot switch a veto off.
func RecheckSplitBookMembers(r SplitBookRecheckReader, bookIDs []string, chapterMaxSec int) ([]string, error) {
	var (
		gone, noPath, noFiles []string
		unknown, long         int
		rows                  int
		group                 []splitBookSlim
	)
	for _, id := range bookIDs {
		b, err := r.GetBookByID(id)
		if err != nil {
			return nil, fmt.Errorf("split-book recheck: read book %s: %w", id, err)
		}
		if b == nil || b.IsSoftDeleted() {
			gone = append(gone, id)
			continue
		}
		files, err := r.GetBookFiles(id)
		if err != nil {
			return nil, fmt.Errorf("split-book recheck: files of %s: %w", id, err)
		}
		if len(files) == 0 {
			noFiles = append(noFiles, id)
		}
		for _, f := range files {
			rows++
			switch d := f.Duration; {
			case d <= 0:
				unknown++
			case chapterMaxSec > 0 && d >= chapterMaxSec:
				long++
			}
		}
		if b.FilePath == "" {
			noPath = append(noPath, id)
			continue
		}
		core := b.Core()
		group = append(group, slimFromBook(&core))
	}

	var reasons []string
	if len(gone) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d of %d books are gone or already retired (%s)", len(gone), len(bookIDs), strings.Join(gone, ", ")))
	}
	if len(noPath) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d of %d books have no stored path (%s)", len(noPath), len(bookIDs), strings.Join(noPath, ", ")))
	}
	if len(noFiles) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d of %d books have no file rows (%s)", len(noFiles), len(bookIDs), strings.Join(noFiles, ", ")))
	}
	if unknown > 0 {
		reasons = append(reasons, fmt.Sprintf("%d of %d file rows have no recorded duration; cannot tell chapters from whole books", unknown, rows))
	}
	if long > 0 {
		reasons = append(reasons, fmt.Sprintf("%d of %d file rows run %d min or longer: likely separate books, not chapters", long, rows, chapterMaxSec/60))
	}
	if len(group) >= 2 {
		shape, folder := recheckShape(group)
		reasons = append(reasons, vetoDistinctWorks(group, shape, folder, chapterMaxSec, recheckYearRun(group))...)
	}
	return reasons, nil
}

// recheckShape infers a cluster's shape and folder from its members' current
// paths by the detector's rule: a grandparent cluster is one whose members each
// sit in a folder of their own (the rogue-subdir case), anything else is read
// as a flat parent cluster. The relaxed shape is judged as a parent: no veto
// distinguishes it.
func recheckShape(group []splitBookSlim) (shape, folder string) {
	dirs := make(map[string]bool, len(group))
	for _, b := range group {
		dirs[filepath.Dir(b.FilePath)] = true
	}
	first := filepath.Dir(group[0].FilePath)
	if len(dirs) == len(group) {
		return "grandparent", filepath.Dir(first)
	}
	return "parent", first
}

// recheckYearRun reports whether the members' numbers form the run the
// detector would have vetoed as dated editions (V5), under either numbering the
// detector uses: chapter numbers from file or subfolder names, or the leading
// track numbers of the relaxed shape.
func recheckYearRun(group []splitBookSlim) bool {
	nums, _, _ := typedChapterNumbers(group)
	if ok, _, years := sequentialRun(nums); ok && years {
		return true
	}
	sib := make([]int, 0, len(group))
	for _, b := range group {
		n, _, _, ok := numberedSiblingStem(b)
		if !ok {
			return false
		}
		sib = append(sib, n)
	}
	ok, _, years := sequentialRun(sib)
	return ok && years
}

// MergeSplitBookClusterRechecked merges a REVIEWED split-book cluster: under the
// merge lock and before the first write it re-reads the members
// (RecheckSplitBookMembers, at the repair_chapter_max_min limit) and refuses
// with a *SplitBookRefusal, writing nothing, unless they still pass. Callers acting on a stored SplitBookCandidate snapshot (the
// bulk-merge op, the single-candidate merge route) must use this and not
// MergeSplitBookClusterWithOptions directly.
//
// bookIDs is the whole cluster, keepID one of them; the other members are the
// sources. The error is ErrSplitBookRefused for a refusal, and any other error
// is a failed read or a failed merge.
func MergeSplitBookClusterRechecked(store Store, bookIDs []string, keepID, suggestedTitle string, opts SplitMergeOptions) (*SplitBookMergeResult, error) {
	srcIDs := make([]string, 0, len(bookIDs))
	keepFound := false
	for _, id := range bookIDs {
		if id == keepID {
			keepFound = true
			continue
		}
		srcIDs = append(srcIDs, id)
	}
	if !keepFound {
		return nil, fmt.Errorf("MergeSplitBookClusterRechecked: keep %q is not among the cluster's books", keepID)
	}
	// The recheck runs as the merge's Precheck, i.e. UNDER the merge lock and
	// immediately before the first write. Run before the lock it would be a
	// time-of-check/time-of-use gap: another merge, an edit or a re-tag could
	// land between the check and the lock and the merge would act on rows the
	// check never saw. A caller's own Precheck still runs, after ours.
	callerPrecheck := opts.Precheck
	chapterMaxSec := RepairChapterMaxSec()
	opts.Precheck = func() error {
		reasons, err := RecheckSplitBookMembers(store, bookIDs, chapterMaxSec)
		if err != nil {
			return err
		}
		if len(reasons) > 0 {
			return &SplitBookRefusal{Reasons: reasons}
		}
		if callerPrecheck != nil {
			return callerPrecheck()
		}
		return nil
	}
	return MergeSplitBookClusterWithOptions(store, keepID, srcIDs, suggestedTitle, opts)
}
