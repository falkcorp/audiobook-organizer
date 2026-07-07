// file: internal/maintenance/jobs/fix_version_groups.go
// version: 2.2.0
// guid: a1000004-0000-0000-0000-000000000004
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/oklog/ulid/v2"
	"log/slog"
)

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

func (j *fixVersionGroupsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
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
			if applyErr := vgUnlinkOutliers(store, outliers); applyErr != nil {
				slog.Error("Failed to unlink outliers in group", "groupID", groupID, "applyErr", applyErr)
				mismatchErrors++
			} else {
				mismatchFixed++
			}
		} else {
			mismatchFixed++
		}
	}

	// Phase 2: author-directory file_path detection
	var authorDirFixed, authorDirErrors int
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
		if !dryRun && suggested != "" {
			if fixErr := vgFixAuthorDirPath(store, b, suggested); fixErr != nil {
				slog.Error("Failed to fix author-dir path for book", "b", b.ID, "fixErr", fixErr)
				authorDirErrors++
			} else {
				authorDirFixed++
			}
		} else if suggested != "" {
			authorDirFixed++
		}
	}

	slog.Info("Done mismatch_fixed mismatch_errors author_dir_fixed author_dir_errors dryRun", "mismatchFixed", mismatchFixed, "mismatchErrors", mismatchErrors, "authorDirFixed", authorDirFixed, "authorDirErrors", authorDirErrors, "dryRun", dryRun)
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
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, ".,;:!?\"'")
		if len([]rune(w)) >= 4 {
			set[w] = true
		}
	}
	return set
}

func vgUnlinkOutliers(store database.Store, outliers []database.BookCore) error {
	for _, ob := range outliers {
		current, err := store.GetBookByID(ob.ID)
		if err != nil {
			return fmt.Errorf("GetBookByID(%s): %w", ob.ID, err)
		}
		if current == nil {
			return fmt.Errorf("book %s not found", ob.ID)
		}
		newGroupID := ulid.Make().String()
		current.VersionGroupID = &newGroupID
		if _, err = store.UpdateBook(ob.ID, current); err != nil {
			return fmt.Errorf("UpdateBook(%s): %w", ob.ID, err)
		}
	}
	return nil
}

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

func vgFixAuthorDirPath(store database.Store, book *database.BookCore, subdir string) error {
	current, err := store.GetBookByID(book.ID)
	if err != nil {
		return fmt.Errorf("GetBookByID: %w", err)
	}
	if current == nil {
		return fmt.Errorf("book %s not found", book.ID)
	}
	current.FilePath = subdir
	if _, err = store.UpdateBook(book.ID, current); err != nil {
		return fmt.Errorf("UpdateBook: %w", err)
	}
	if err = store.DeleteBookFilesForBook(book.ID); err != nil {
		return fmt.Errorf("DeleteBookFilesForBook: %w", err)
	}
	newFiles := metafetch.AudioFilesInDir(subdir)
	if len(newFiles) == 0 {
		return nil
	}
	return vgCreateBookFiles(store, current, newFiles)
}

func vgCreateBookFiles(store database.Store, book *database.Book, filePaths []string) error {
	for _, fp := range filePaths {
		ext := strings.ToLower(filepath.Ext(fp))
		format := strings.TrimPrefix(ext, ".")
		var fileSize int64
		if info, err := os.Stat(fp); err == nil {
			fileSize = info.Size()
		}
		bf := &database.BookFile{
			ID:               ulid.Make().String(),
			BookID:           book.ID,
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
