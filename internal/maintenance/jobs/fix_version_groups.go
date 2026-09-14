// file: internal/maintenance/jobs/fix_version_groups.go
// version: 3.0.0
// guid: a1000004-0000-0000-0000-000000000004
// last-edited: 2026-09-13

package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/oklog/ulid/v2"
)

var vgLog = logger.New("fix-version-groups")

func init() { maintenance.Register(&fixVersionGroupsJob{}) }

type fixVersionGroupsJob struct{}

func (j *fixVersionGroupsJob) ID() string       { return "fix-version-groups" }
func (j *fixVersionGroupsJob) Name() string     { return "Fix Version Groups" }
func (j *fixVersionGroupsJob) Category() string { return "library" }
func (j *fixVersionGroupsJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *fixVersionGroupsJob) Description() string { return "Fix and normalize version groups" }
func (j *fixVersionGroupsJob) CanResume() bool     { return false }

// errVGRefused marks an author-dir fix the job declines to make (no audio in
// the target, ambiguous file matching). Counted as refused, not failed.
var errVGRefused = errors.New("fix-version-groups refused")

func (j *fixVersionGroupsJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	allBooks, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("failed to list books: %w", err)
	}
	reporter.SetTotal(len(allBooks))

	// Phase 1: title mismatch within version groups
	groupMap := make(map[string][]database.BookCore)
	for i := range allBooks {
		b := &allBooks[i]
		if b.VersionGroupID == nil || *b.VersionGroupID == "" {
			continue
		}
		groupMap[*b.VersionGroupID] = append(groupMap[*b.VersionGroupID], *b)
	}

	var mismatchFixed, mismatchErrors int
	for groupID, books := range groupMap {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if len(books) < 2 {
			continue
		}

		cores := make([]vgBookCore, len(books))
		for i, b := range books {
			cores[i] = vgBookCore{book: b, core: vgExtractCoreTitle(b.Title)}
		}
		majorityCore := vgFindMajorityCore(cores)

		var outliers []database.BookCore
		for _, bc := range cores {
			if !vgCoreTitlesMatch(bc.core, majorityCore) {
				outliers = append(outliers, bc.book)
			}
		}

		if len(outliers) == 0 {
			continue
		}

		if !dryRun {
			if applyErr := vgUnlinkOutliers(store, books, outliers); applyErr != nil {
				vgLog.Error("unlink outliers group=%s: %s", logger.SanitizeLogValue(groupID), logger.SanitizeLogValue(applyErr.Error()))
				mismatchErrors++
			} else {
				mismatchFixed++
			}
		} else {
			mismatchFixed++
		}
	}

	// Phase 2: author-directory file_path detection
	var authorDirFixed, authorDirRefused, authorDirErrors int
	for i := range allBooks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		b := &allBooks[i]
		reporter.Increment()

		if b.FilePath == "" {
			continue
		}
		fi, statErr := os.Stat(b.FilePath)
		if statErr != nil || !fi.IsDir() {
			continue
		}
		if !vgIsAuthorDirectory(b.FilePath) {
			continue
		}

		suggested := vgBestMatchSubdir(b.FilePath, b.Title)
		if suggested == "" {
			continue
		}
		// The plan is built in both modes, so dry-run reports the same refusals
		// apply would hit and never counts a fix apply would not make.
		plan, planErr := vgPlanAuthorDirFix(store, b.ID, suggested)
		if planErr == nil && !dryRun {
			planErr = vgApplyAuthorDirFix(store, plan)
		}
		switch {
		case planErr == nil:
			authorDirFixed++
		case merge.IsRefusal(planErr) || errors.Is(planErr, errVGRefused):
			authorDirRefused++
			vgLog.Warn("author-dir fix refused book=%s: %s", logger.SanitizeLogValue(b.ID), logger.SanitizeLogValue(planErr.Error()))
		default:
			authorDirErrors++
			vgLog.Error("author-dir fix failed book=%s: %s", logger.SanitizeLogValue(b.ID), logger.SanitizeLogValue(planErr.Error()))
		}
	}

	summary := fmt.Sprintf("dry_run=%v mismatch_fixed=%d mismatch_errors=%d author_dir_fixed=%d author_dir_refused=%d author_dir_errors=%d",
		dryRun, mismatchFixed, mismatchErrors, authorDirFixed, authorDirRefused, authorDirErrors)
	reporter.Log("info", summary, nil)
	vgLog.Info("done: %s", summary)
	if mismatchErrors+authorDirErrors > 0 {
		return fmt.Errorf("fix-version-groups: %d write(s) failed (%s)", mismatchErrors+authorDirErrors, summary)
	}
	return nil
}

type vgBookCore struct {
	book database.BookCore
	core string
}

var vgParentheticalRE = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
var vgLeadingNumberRE = regexp.MustCompile(`^\d+[\s.\-–]+`)

func vgExtractCoreTitle(title string) string {
	s := title
	for {
		trimmed := vgParentheticalRE.ReplaceAllString(s, "")
		if trimmed == s {
			break
		}
		s = strings.TrimSpace(trimmed)
	}
	s = vgLeadingNumberRE.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func vgFindMajorityCore(cores []vgBookCore) string {
	counts := make(map[string]int)
	for _, bc := range cores {
		counts[bc.core]++
	}
	best := ""
	bestCount := 0
	for core, count := range counts {
		if count > bestCount {
			bestCount = count
			best = core
		}
	}
	return best
}

func vgCoreTitlesMatch(a, b string) bool {
	aLow := strings.ToLower(a)
	bLow := strings.ToLower(b)
	if aLow == bLow {
		return true
	}
	if strings.Contains(aLow, bLow) || strings.Contains(bLow, aLow) {
		return true
	}
	aWords := vgLongWords(aLow)
	bWords := vgLongWords(bLow)
	for w := range aWords {
		if bWords[w] {
			return true
		}
	}
	return false
}

func vgLongWords(s string) map[string]bool {
	set := make(map[string]bool)
	for w := range strings.FieldsSeq(s) {
		w = strings.Trim(w, ".,;:!?\"'")
		if len([]rune(w)) >= 4 {
			set[w] = true
		}
	}
	return set
}

// vgUnlinkOutliers moves each outlier into a fresh version group of its own,
// every write under the book's lock (ModifyBook).
//
// Primary bookkeeping, both sides:
//   - The outlier is the sole member of its new group, so it is made that
//     group's primary. A singleton whose only member is not primary is
//     invisible under the UI's default is_primary_version filter.
//   - If an outlier counted as the OLD group's primary (nil counts, the
//     destructive-guard reading -- see ddCountsAsPrimary) and no remaining
//     member is an explicit primary, the lowest-ID remaining member is
//     promoted FIRST, so the group is never left with none. IDs are ULIDs, so
//     lowest ID is earliest created, reconcile's election rule.
func vgUnlinkOutliers(store bookModifier, group, outliers []database.BookCore) error {
	isOutlier := make(map[string]bool, len(outliers))
	lostPrimary := false
	for _, ob := range outliers {
		isOutlier[ob.ID] = true
		if ob.IsPrimaryVersion == nil || *ob.IsPrimaryVersion {
			lostPrimary = true
		}
	}
	if lostPrimary {
		var remaining []database.BookCore
		hasExplicit := false
		for _, m := range group {
			if isOutlier[m.ID] || m.IsSoftDeleted() {
				continue
			}
			if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
				hasExplicit = true
			}
			remaining = append(remaining, m)
		}
		if !hasExplicit && len(remaining) > 0 {
			sort.Slice(remaining, func(i, j int) bool { return remaining[i].ID < remaining[j].ID })
			if err := ddPromotePrimary(store, remaining[0].ID); err != nil {
				return fmt.Errorf("promote %s before unlinking the group's primary: %w", remaining[0].ID, err)
			}
		}
	}
	for _, ob := range outliers {
		newGroupID := ulid.Make().String()
		updated, err := store.ModifyBook(ob.ID, func(b *database.Book) error {
			t := true
			b.VersionGroupID = &newGroupID
			b.IsPrimaryVersion = &t
			return nil
		})
		if err != nil {
			return fmt.Errorf("unlink %s: %w", ob.ID, err)
		}
		if updated == nil {
			return fmt.Errorf("book %s not found", ob.ID)
		}
	}
	return nil
}

// bookModifier is the per-book locked read-modify-write.
type bookModifier = ddBookModifier

func vgIsAuthorDirectory(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	bookSubdirs := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subPath := filepath.Join(dir, e.Name())
		if len(metafetch.AudioFilesInDir(subPath)) > 0 {
			bookSubdirs++
			if bookSubdirs >= 2 {
				return true
			}
		}
	}
	return false
}

func vgBestMatchSubdir(parent, title string) string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	titleWords := vgLongWords(strings.ToLower(vgExtractCoreTitle(title)))
	bestPath := ""
	bestScore := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(parent, e.Name())
		if len(metafetch.AudioFilesInDir(sub)) == 0 {
			continue
		}
		dirWords := vgLongWords(strings.ToLower(e.Name()))
		score := 0
		for w := range titleWords {
			if dirWords[w] {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestPath = sub
		}
	}
	if bestScore == 0 {
		return ""
	}
	return bestPath
}

// vgAuthorDirPlan is what moving a book from an author directory to its own
// subdirectory will do. Existing book_file rows are REPOINTED, never deleted
// and recreated: a row carries its fingerprint, intro transcript, duration,
// iTunes PID and track/disc numbers, none of which a disk scan can rebuild.
type vgAuthorDirPlan struct {
	bookID string
	subdir string
	// keep counts rows already at a path inside subdir.
	keep int
	// repoint holds existing rows (full, as read) with FilePath set to the
	// subdir file of the same name. Same ID, same BookID, every other field
	// untouched.
	repoint []database.BookFile
	// create holds subdir files with no row anywhere: only these get new rows.
	create []string
	// ownedElsewhere holds subdir files another book already has a row for.
	// They are left alone; taking them would steal that book's file.
	ownedElsewhere []string
	// left counts this book's rows that match nothing in subdir. They are
	// kept as they are, never deleted.
	left int
}

// vgPlanAuthorDirFix reads everything and writes nothing, so dry-run and
// apply share it. It refuses (errVGRefused) when the subdir holds no audio --
// the old code wiped the book's rows and then reported success with none --
// and when two of the book's rows share a file name, so the match is
// ambiguous. The iTunes guard covers the book, its rows and the target files.
func vgPlanAuthorDirFix(store maintenance.JobStore, bookID, subdir string) (*vgAuthorDirPlan, error) {
	if err := merge.GuardITunesProtected(store, []string{bookID}); err != nil {
		return nil, err
	}
	disk := metafetch.AudioFilesInDir(subdir)
	if len(disk) == 0 {
		return nil, fmt.Errorf("%w: %s holds no audio files; the book is left as it is", errVGRefused, subdir)
	}
	target := make([]database.BookFile, len(disk))
	for i, fp := range disk {
		target[i] = database.BookFile{BookID: bookID, FilePath: fp}
	}
	if err := merge.GuardITunesProtectedLoaded(
		[]*database.Book{{ID: bookID, FilePath: subdir}},
		map[string][]database.BookFile{bookID: target},
	); err != nil {
		return nil, err
	}

	rows, err := store.GetBookFiles(bookID)
	if err != nil {
		return nil, fmt.Errorf("read files of %s: %w", bookID, err)
	}
	onDisk := make(map[string]bool, len(disk))
	for _, fp := range disk {
		onDisk[fp] = true
	}
	rowAt := make(map[string]bool, len(rows))
	unmatched := make(map[string][]database.BookFile)
	for _, r := range rows {
		if onDisk[r.FilePath] {
			rowAt[r.FilePath] = true
			continue
		}
		base := filepath.Base(r.FilePath)
		unmatched[base] = append(unmatched[base], r)
	}

	plan := &vgAuthorDirPlan{bookID: bookID, subdir: subdir}
	for _, fp := range disk {
		if rowAt[fp] {
			plan.keep++
			continue
		}
		owner, err := store.GetBookFileByPath(fp)
		if err != nil {
			return nil, fmt.Errorf("look up row for %s: %w", fp, err)
		}
		if owner != nil && owner.BookID != bookID {
			plan.ownedElsewhere = append(plan.ownedElsewhere, fp)
			continue
		}
		base := filepath.Base(fp)
		switch cands := unmatched[base]; len(cands) {
		case 0:
			plan.create = append(plan.create, fp)
		case 1:
			r := cands[0]
			r.FilePath = fp
			plan.repoint = append(plan.repoint, r)
			delete(unmatched, base)
		default:
			return nil, fmt.Errorf("%w: %d rows of book %s are named %q; cannot tell which one is %s",
				errVGRefused, len(cands), bookID, base, fp)
		}
	}
	for _, rs := range unmatched {
		plan.left += len(rs)
	}
	return plan, nil
}

// vgApplyAuthorDirFix writes a plan: repoint rows, create rows only for files
// that have none, then move the book's own path. The book path is written
// last so a failure part-way leaves the book where it was, with every row it
// had still present.
func vgApplyAuthorDirFix(store maintenance.JobStore, plan *vgAuthorDirPlan) error {
	for i := range plan.repoint {
		r := plan.repoint[i]
		if err := store.UpdateBookFile(r.ID, &r); err != nil {
			return fmt.Errorf("repoint file %s to %s: %w", r.ID, r.FilePath, err)
		}
	}
	if err := vgCreateBookFiles(store, plan.bookID, plan.create); err != nil {
		return err
	}
	updated, err := store.ModifyBook(plan.bookID, func(b *database.Book) error {
		b.FilePath = plan.subdir
		return nil
	})
	if err != nil {
		return fmt.Errorf("set path of %s: %w", plan.bookID, err)
	}
	if updated == nil {
		return fmt.Errorf("book %s not found", plan.bookID)
	}
	if plan.left > 0 || len(plan.ownedElsewhere) > 0 {
		vgLog.Warn("author-dir fix book=%s kept=%d repointed=%d created=%d left_untouched=%d owned_by_other_books=%d",
			logger.SanitizeLogValue(plan.bookID), plan.keep, len(plan.repoint), len(plan.create), plan.left, len(plan.ownedElsewhere))
	}
	return nil
}

func vgCreateBookFiles(store bookFileCreator, bookID string, filePaths []string) error {
	for _, fp := range filePaths {
		ext := strings.ToLower(filepath.Ext(fp))
		format := strings.TrimPrefix(ext, ".")
		var fileSize int64
		if info, err := os.Stat(fp); err == nil {
			fileSize = info.Size()
		}
		bf := &database.BookFile{
			ID:               ulid.Make().String(),
			BookID:           bookID,
			FilePath:         fp,
			OriginalFilename: filepath.Base(fp),
			Format:           format,
			FileSize:         fileSize,
		}
		if err := store.CreateBookFile(bf); err != nil {
			return fmt.Errorf("CreateBookFile(%q): %w", fp, err)
		}
	}
	return nil
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *fixVersionGroupsJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
