// file: internal/reconcile/reconcile.go
// version: 1.4.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-07-16

package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
	"github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"
)

// Store is the database dependency for reconcile operations.
type Store interface {
	database.BookStore
	database.BookFileStore
	database.ImportPathStore
	database.OperationStore
}

// ReconcileMatch represents a potential match between a broken DB record and an untracked file.
type ReconcileMatch struct {
	BookID     string  `json:"book_id"`
	BookTitle  string  `json:"book_title"`
	OldPath    string  `json:"old_path"`
	NewPath    string  `json:"new_path"`
	MatchType  string  `json:"match_type"` // "hash", "original_hash", "filename"
	Confidence string  `json:"confidence"` // "high", "medium", "low"
	Score      float64 `json:"score"`
}

// ReconcilePreviewResult is the full preview of what reconciliation would do.
type ReconcilePreviewResult struct {
	BrokenRecords  []ReconcileBrokenRecord `json:"broken_records"`
	UntrackedFiles []string                `json:"untracked_files"`
	Matches        []ReconcileMatch        `json:"matches"`
	UnmatchedBooks []ReconcileBrokenRecord `json:"unmatched_books"`
}

// ReconcileBrokenRecord represents a book whose file_path no longer exists on disk.
type ReconcileBrokenRecord struct {
	BookID   string  `json:"book_id"`
	Title    string  `json:"title"`
	FilePath string  `json:"file_path"`
	FileHash *string `json:"file_hash,omitempty"`
}

// ReconcileApplyRequest specifies which matches to apply.
type ReconcileApplyRequest struct {
	Matches []ReconcileApplyItem `json:"matches"`
}

// ReconcileApplyItem is a single match the user confirmed.
type ReconcileApplyItem struct {
	BookID  string `json:"book_id"`
	NewPath string `json:"new_path"`
}

// ReconcileApplyResult reports what was done.
type ReconcileApplyResult struct {
	Applied int      `json:"applied"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors,omitempty"`
}

// VersionGroupCleanupResult holds the result of pruning duplicate version groups.
type VersionGroupCleanupResult struct {
	GroupsChecked     int `json:"groups_checked"`
	GroupsCleaned     int `json:"groups_cleaned"`
	DuplicatesRemoved int `json:"duplicates_removed"`
	FilesDeleted      int `json:"files_deleted"`
}

// BrokenSegmentResult describes books with missing segment files.
type BrokenSegmentResult struct {
	BooksChecked    int                  `json:"books_checked"`
	BrokenBooks     int                  `json:"broken_books"`
	MarkedForReview int                  `json:"marked_for_review"`
	Details         []BrokenSegmentEntry `json:"details"`
}

// BrokenSegmentEntry describes one book with missing segment files.
type BrokenSegmentEntry struct {
	BookID          string   `json:"book_id"`
	Title           string   `json:"title"`
	FilePath        string   `json:"file_path"`
	TotalSegments   int      `json:"total_segments"`
	MissingSegments int      `json:"missing_segments"`
	MissingPaths    []string `json:"missing_paths"`
}

// MergeDuplicatesResult describes the outcome of merging no-VG duplicates into existing version groups.
type MergeDuplicatesResult struct {
	TotalNoVG        int                   `json:"total_no_vg"`
	MatchedToVG      int                   `json:"matched_to_vg"`
	SelfDuplicates   int                   `json:"self_duplicates"`
	MetadataMerged   int                   `json:"metadata_merged"`
	SoftDeleted      int                   `json:"soft_deleted"`
	RemainingOrphans int                   `json:"remaining_orphans"`
	Errors           int                   `json:"errors"`
	Details          []MergeDuplicateEntry `json:"details,omitempty"`
}

// MergeDuplicateEntry describes one merge action.
type MergeDuplicateEntry struct {
	DuplicateID  string   `json:"duplicate_id"`
	PrimaryID    string   `json:"primary_id"`
	Title        string   `json:"title"`
	FieldsMerged []string `json:"fields_merged,omitempty"`
	Action       string   `json:"action"`
}

// AssignVGResult holds the result of assigning version groups to orphan library books.
type AssignVGResult struct {
	TotalChecked int `json:"total_checked"`
	Assigned     int `json:"assigned"`
	AlreadyHasVG int `json:"already_has_vg"`
	NotInLibrary int `json:"not_in_library"`
	Errors       int `json:"errors"`
}

// RunReconcileScan executes the reconcile preview build and persists results.
func RunReconcileScan(store Store, ctx context.Context, opID string, progress operations.ProgressReporter) error {
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	result, err := BuildReconcilePreviewWithProgress(store, operations.LoggerFromReporter(progress))
	if err != nil {
		return fmt.Errorf("reconcile scan failed: %w", err)
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal scan results: %w", err)
	}
	if err := store.UpdateOperationResultData(opID, string(resultJSON)); err != nil {
		return fmt.Errorf("failed to store scan results: %w", err)
	}
	return nil
}

// BuildReconcilePreview builds the full reconciliation preview (sync, no progress).
func BuildReconcilePreview(store Store) (*ReconcilePreviewResult, error) {
	return BuildReconcilePreviewWithProgress(store, nil)
}

// hashFilesConcurrent computes a segment hash for every file across a bounded
// worker pool (runtime.NumCPU()) and returns a hash→path index. This replaces a
// former single-core loop that hashed potentially thousands of on-disk files
// serially (a CLAUDE.md concurrency-mandate hotspot). ComputeSegmentFileHash
// opens its own file and shares no state, so it is safe to call concurrently.
//
// Behaviour is preserved exactly: each file hashes into its own index-ordered
// slot and the map is folded serially afterwards, so on a hash collision the
// highest-indexed file still wins (as the sequential map-build did). Files that
// fail to hash are skipped. report, if non-nil, is called ~every 100 files with
// the running completed count.
func hashFilesConcurrent(files []string, report func(done int)) map[string]string {
	hashes := make([]string, len(files))
	var done int64
	var g errgroup.Group
	g.SetLimit(max(runtime.NumCPU(), 1))
	for i, fp := range files {
		g.Go(func() error {
			if h, err := scanner.ComputeSegmentFileHash(fp); err == nil {
				hashes[i] = h // sha256 hex is never empty; "" == unhashed
			}
			if n := atomic.AddInt64(&done, 1); report != nil && n%100 == 0 {
				report(int(n))
			}
			return nil
		})
	}
	_ = g.Wait() // per-file hash errors are skipped, matching prior behaviour

	idx := make(map[string]string, len(files))
	for i, h := range hashes {
		if h != "" {
			idx[h] = files[i]
		}
	}
	return idx
}

// BuildReconcilePreviewWithProgress builds the full reconciliation preview with
// progress reporting for background operations. log may be nil.
func BuildReconcilePreviewWithProgress(store Store, log logger.Logger) (*ReconcilePreviewResult, error) {
	if log == nil {
		log = logger.New("reconcile")
	}
	report := func(current, total int, msg string) {
		if total <= 0 {
			log.UpdateProgress(current, total, msg)
			return
		}
		pct := float64(current) * 100.0 / float64(total)
		var pctStr string
		if total >= 100 {
			pctStr = fmt.Sprintf("%.2f%%", pct)
		} else {
			pctStr = fmt.Sprintf("%.0f%%", pct)
		}
		log.UpdateProgress(current, total, fmt.Sprintf("%s (%s)", msg, pctStr))
	}
	logMsg := func(level, msg string) {
		switch level {
		case "info":
			log.Info("reconcile: %s", msg)
		case "warn":
			log.Warn("reconcile: %s", msg)
		case "error":
			log.Error("reconcile: %s", msg)
		default:
			log.Debug("reconcile: %s", msg)
		}
	}

	result := &ReconcilePreviewResult{
		BrokenRecords:  []ReconcileBrokenRecord{},
		UntrackedFiles: []string{},
		Matches:        []ReconcileMatch{},
		UnmatchedBooks: []ReconcileBrokenRecord{},
	}

	// Step 1: Find broken DB records
	report(0, 1, "Loading all books from database...")
	books, err := store.GetAllBooksCore(100000, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to list books: %w", err)
	}
	logMsg("info", fmt.Sprintf("Loaded %d books from database", len(books)))

	// Build set of all known file paths for quick lookup
	knownPaths := make(map[string]bool, len(books))
	var brokenBooks []database.BookCore

	totalBooks := len(books)
	if totalBooks == 0 {
		report(1, 1, "No books in database — nothing to reconcile")
		return result, nil
	}
	report(0, totalBooks, fmt.Sprintf("Checking file paths for %d books...", totalBooks))
	for i, book := range books {
		if book.FilePath == "" {
			continue
		}
		knownPaths[book.FilePath] = true
		if _, err := os.Stat(book.FilePath); err != nil {
			brokenBooks = append(brokenBooks, book)
			result.BrokenRecords = append(result.BrokenRecords, ReconcileBrokenRecord{
				BookID:   book.ID,
				Title:    book.Title,
				FilePath: book.FilePath,
				FileHash: book.FileHash,
			})
		}
		if i%1000 == 0 && i > 0 {
			report(i, totalBooks, fmt.Sprintf("Checked %d/%d book paths (%d broken so far)", i, totalBooks, len(brokenBooks)))
		}
	}
	report(totalBooks, totalBooks, fmt.Sprintf("Checked %d/%d book paths (%d broken)", totalBooks, totalBooks, len(brokenBooks)))

	logMsg("info", fmt.Sprintf("Found %d broken records out of %d books", len(brokenBooks), len(books)))

	if len(brokenBooks) == 0 {
		report(1, 1, fmt.Sprintf("All %d books have valid file paths", len(books)))
		return result, nil
	}

	// Step 2: Scan directories for untracked audio files
	report(0, 1, "Scanning directories for untracked audio files...")
	logMsg("info", "Scanning library, import paths, and iTunes directories for untracked files")
	untrackedFiles, err := FindUntrackedFiles(store, knownPaths)
	if err != nil {
		return nil, fmt.Errorf("failed to scan for untracked files: %w", err)
	}
	result.UntrackedFiles = untrackedFiles
	logMsg("info", fmt.Sprintf("Found %d untracked audio files on disk", len(untrackedFiles)))

	if len(untrackedFiles) == 0 {
		result.UnmatchedBooks = result.BrokenRecords
		report(1, 1, fmt.Sprintf("No untracked files found. %d broken records remain unmatched.", len(brokenBooks)))
		return result, nil
	}

	// Step 3: Hash untracked files for matching
	totalUntracked := len(untrackedFiles)
	report(0, totalUntracked, fmt.Sprintf("Hashing %d untracked files...", totalUntracked))
	hashIndex := hashFilesConcurrent(untrackedFiles, func(done int) {
		report(done, totalUntracked, fmt.Sprintf("Hashed %d/%d untracked files", done, totalUntracked))
	})
	report(totalUntracked, totalUntracked, fmt.Sprintf("Hashed %d/%d untracked files", totalUntracked, totalUntracked))
	logMsg("info", fmt.Sprintf("Computed hashes for %d untracked files", len(hashIndex)))

	matchedBooks := make(map[string]bool)
	matchedFiles := make(map[string]bool)

	// Step 3a: Match by file hash
	totalBroken := len(brokenBooks)
	report(0, totalBroken, fmt.Sprintf("Matching %d broken records by file hash...", totalBroken))
	for _, book := range brokenBooks {
		if book.FileHash != nil && *book.FileHash != "" {
			if fp, ok := hashIndex[*book.FileHash]; ok && !matchedFiles[fp] {
				result.Matches = append(result.Matches, ReconcileMatch{
					BookID:     book.ID,
					BookTitle:  book.Title,
					OldPath:    book.FilePath,
					NewPath:    fp,
					MatchType:  "hash",
					Confidence: "high",
					Score:      1.0,
				})
				matchedBooks[book.ID] = true
				matchedFiles[fp] = true
			}
		}
	}

	// Step 3b: Match by original_file_hash
	for _, book := range brokenBooks {
		if matchedBooks[book.ID] {
			continue
		}
		if book.OriginalFileHash != nil && *book.OriginalFileHash != "" {
			if fp, ok := hashIndex[*book.OriginalFileHash]; ok && !matchedFiles[fp] {
				result.Matches = append(result.Matches, ReconcileMatch{
					BookID:     book.ID,
					BookTitle:  book.Title,
					OldPath:    book.FilePath,
					NewPath:    fp,
					MatchType:  "original_hash",
					Confidence: "high",
					Score:      0.95,
				})
				matchedBooks[book.ID] = true
				matchedFiles[fp] = true
			}
		}
	}
	logMsg("info", fmt.Sprintf("Hash matching found %d matches", len(result.Matches)))

	// Step 4: Match by filename pattern
	report(0, totalBroken, fmt.Sprintf("Matching %d broken records by filename patterns...", totalBroken))
	filenameIndex := make(map[string][]string)
	for _, fp := range untrackedFiles {
		if matchedFiles[fp] {
			continue
		}
		base := NormalizeFilename(filepath.Base(fp))
		filenameIndex[base] = append(filenameIndex[base], fp)
	}

	for _, book := range brokenBooks {
		if matchedBooks[book.ID] {
			continue
		}
		bookBase := NormalizeFilename(filepath.Base(book.FilePath))
		if candidates, ok := filenameIndex[bookBase]; ok && len(candidates) > 0 {
			for _, fp := range candidates {
				if !matchedFiles[fp] {
					result.Matches = append(result.Matches, ReconcileMatch{
						BookID:     book.ID,
						BookTitle:  book.Title,
						OldPath:    book.FilePath,
						NewPath:    fp,
						MatchType:  "filename",
						Confidence: "low",
						Score:      0.5,
					})
					matchedBooks[book.ID] = true
					matchedFiles[fp] = true
					break
				}
			}
		}
	}

	// Step 4b: Try matching by title contained in filename
	report(0, totalBroken, fmt.Sprintf("Matching %d broken records by title in filename...", totalBroken))
	for _, book := range brokenBooks {
		if matchedBooks[book.ID] {
			continue
		}
		normalizedTitle := NormalizeFilename(book.Title)
		if normalizedTitle == "" {
			continue
		}
		for _, fp := range untrackedFiles {
			if matchedFiles[fp] {
				continue
			}
			normalizedBase := NormalizeFilename(filepath.Base(fp))
			if strings.Contains(normalizedBase, normalizedTitle) ||
				strings.Contains(normalizedTitle, normalizedBase) {
				result.Matches = append(result.Matches, ReconcileMatch{
					BookID:     book.ID,
					BookTitle:  book.Title,
					OldPath:    book.FilePath,
					NewPath:    fp,
					MatchType:  "filename",
					Confidence: "low",
					Score:      0.3,
				})
				matchedBooks[book.ID] = true
				matchedFiles[fp] = true
				break
			}
		}
	}

	// Collect unmatched books
	for _, book := range brokenBooks {
		if !matchedBooks[book.ID] {
			result.UnmatchedBooks = append(result.UnmatchedBooks, ReconcileBrokenRecord{
				BookID:   book.ID,
				Title:    book.Title,
				FilePath: book.FilePath,
				FileHash: book.FileHash,
			})
		}
	}

	summary := fmt.Sprintf("Scan complete: %d matches (%d hash, %d filename), %d unmatched out of %d broken",
		len(result.Matches),
		CountMatchType(result.Matches, "hash")+CountMatchType(result.Matches, "original_hash"),
		CountMatchType(result.Matches, "filename"),
		len(result.UnmatchedBooks),
		len(brokenBooks))
	logMsg("info", summary)
	report(totalBroken, totalBroken, summary)

	return result, nil
}

// FindUntrackedFiles walks directories in priority order (library root first,
// then import paths, then iTunes paths), collecting audio files not in the DB.
func FindUntrackedFiles(store Store, knownPaths map[string]bool) ([]string, error) {
	var dirs []string

	// Priority 1: Library root (our organized folder — always preferred)
	if config.AppConfig.RootDir != "" {
		dirs = append(dirs, config.AppConfig.RootDir)
	}

	// Priority 2: Import paths
	importPaths, err := store.GetAllImportPaths()
	if err != nil {
		slog.Warn("reconcile failed to get import paths", "err", err)
	} else {
		for _, ip := range importPaths {
			if ip.Enabled {
				dirs = append(dirs, ip.Path)
			}
		}
	}

	// Priority 3: iTunes library paths (lowest priority — never modify these)
	if config.AppConfig.ITunes.LibraryReadPath != "" {
		itunesMedia := filepath.Dir(config.AppConfig.ITunes.LibraryReadPath)
		// Walk up to find the iTunes Media/Audiobooks folder
		if sp, err := safepath.Join(itunesMedia, "iTunes Media", "Audiobooks"); err == nil {
			audiobooks := sp.String()
			if _, err := os.Stat(audiobooks); err == nil {
				dirs = append(dirs, audiobooks)
			}
		}
	}

	if len(dirs) == 0 {
		return nil, nil
	}

	// Build set of supported extensions
	extSet := make(map[string]bool)
	for _, ext := range config.AppConfig.SupportedExtensions {
		extSet[strings.ToLower(ext)] = true
	}
	// Fallback if no extensions configured
	if len(extSet) == 0 {
		for _, ext := range []string{".m4b", ".mp3", ".m4a", ".flac", ".aac", ".ogg", ".wma", ".opus", ".oga", ".wav", ".aiff", ".aif", ".mka", ".aax", ".aaxc"} {
			extSet[ext] = true
		}
	}

	var untracked []string
	seen := make(map[string]bool)

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			slog.Warn("reconcile directory does not exist", "dir", dir)
			continue
		}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip errors
			}
			if info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if !extSet[ext] {
				return nil
			}
			absPath, _ := filepath.Abs(path)
			if absPath == "" {
				absPath = path
			}
			if !knownPaths[absPath] && !knownPaths[path] && !seen[absPath] {
				untracked = append(untracked, absPath)
				seen[absPath] = true
			}
			return nil
		})
		if err != nil {
			slog.Warn("reconcile error walking", "dir", dir, "err", err)
		}
	}

	return untracked, nil
}

// ExecuteReconcile applies confirmed matches: updates DB file_path and records OperationChanges.
func ExecuteReconcile(ctx context.Context, store Store, operationID string, matches []ReconcileApplyItem, log logger.Logger) error {
	result := &ReconcileApplyResult{
		Errors: []string{},
	}

	total := len(matches)
	fmtPct := func(cur, tot int) string {
		if tot <= 0 {
			return "0%"
		}
		pct := float64(cur) * 100.0 / float64(tot)
		if tot >= 100 {
			return fmt.Sprintf("%.2f%%", pct)
		}
		return fmt.Sprintf("%.0f%%", pct)
	}
	if total == 0 {
		log.UpdateProgress(0, 1, "Reconciliation starting: 0 matches to apply")
		log.UpdateProgress(1, 1, "Reconciliation complete: 0 applied, 0 skipped (100%)")
		resultJSON, _ := json.Marshal(result)
		_ = store.UpdateOperationResultData(operationID, string(resultJSON))
		return nil
	}
	log.UpdateProgress(0, total, fmt.Sprintf("Reconciliation starting: 0/%d (0%%)", total))
	for i, m := range matches {
		if log.IsCanceled() {
			break
		}

		log.UpdateProgress(i+1, total, fmt.Sprintf("Updating %d/%d (%s) %s", i+1, total, fmtPct(i+1, total), m.BookID))

		book, err := store.GetBookByID(m.BookID)
		if err != nil || book == nil {
			result.Errors = append(result.Errors, fmt.Sprintf("book %s: not found", m.BookID))
			result.Skipped++
			continue
		}

		// Verify the new path exists
		if _, err := os.Stat(m.NewPath); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("book %s: new path does not exist: %s", m.BookID, m.NewPath))
			result.Skipped++
			continue
		}

		oldPath := book.FilePath

		// Record the change for undo support
		change := &database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			BookID:      book.ID,
			ChangeType:  "file_path_update",
			FieldName:   "file_path",
			OldValue:    oldPath,
			NewValue:    m.NewPath,
		}
		if err := store.CreateOperationChange(change); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("book %s: failed to record change: %v", m.BookID, err))
			result.Skipped++
			continue
		}

		// Update the book's file path
		book.FilePath = m.NewPath
		if _, err := store.UpdateBook(book.ID, book); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("book %s: failed to update: %v", m.BookID, err))
			result.Skipped++
			continue
		}

		log.Info("Updated book %s: %s -> %s", book.ID, oldPath, m.NewPath)
		result.Applied++
	}

	// Store result data on the operation
	resultJSON, _ := json.Marshal(result)
	_ = store.UpdateOperationResultData(operationID, string(resultJSON))
	log.UpdateProgress(total, total, fmt.Sprintf("Reconciliation complete: %d/%d processed (%s) — %d applied, %d skipped", total, total, fmtPct(total, total), result.Applied, result.Skipped))

	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			log.Error("%s", e)
		}
		return fmt.Errorf("completed with %d errors: %s", len(result.Errors), result.Errors[0])
	}
	return nil
}

// NormalizeFilename strips extension, lowercases, and removes non-alphanumeric chars for comparison.
func NormalizeFilename(name string) string {
	// Strip extension
	ext := filepath.Ext(name)
	if ext != "" {
		name = name[:len(name)-len(ext)]
	}
	name = strings.ToLower(name)
	// Replace common separators with space
	name = strings.NewReplacer("-", " ", "_", " ", ".", " ").Replace(name)
	// Remove extra whitespace
	parts := strings.Fields(name)
	return strings.Join(parts, " ")
}

// CountMatchType counts matches of a given type.
func CountMatchType(matches []ReconcileMatch, matchType string) int {
	n := 0
	for _, m := range matches {
		if m.MatchType == matchType {
			n++
		}
	}
	return n
}

// CleanupDuplicateVersionGroups finds version groups with more than 2 members
// (1 original + 1 organized) and removes the extra organized copies that were
// created by the organize-reprocessing bug.
func CleanupDuplicateVersionGroups(store Store, rootDir string, dryRun bool) (*VersionGroupCleanupResult, error) {
	result := &VersionGroupCleanupResult{}

	// Fetch all books and group by version_group_id. Core-typed: grouping and
	// dup-selection only need ID/FilePath/VersionGroupID, all Core-safe fields.
	versionGroups := make(map[string][]database.BookCore)
	const pageSize = 1000
	for offset := 0; ; offset += pageSize {
		books, err := store.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch books: %w", err)
		}
		for _, b := range books {
			if b.VersionGroupID != nil && *b.VersionGroupID != "" {
				versionGroups[*b.VersionGroupID] = append(versionGroups[*b.VersionGroupID], b)
			}
		}
		if len(books) < pageSize {
			break
		}
	}

	for groupID, members := range versionGroups {
		result.GroupsChecked++
		if len(members) <= 2 {
			continue // normal: 1 original + 1 organized
		}

		// Separate into originals (non-primary, outside library) and organized copies (in library)
		var originals, libraryCopies []database.BookCore
		for _, m := range members {
			if rootDir != "" && strings.HasPrefix(m.FilePath, rootDir) {
				libraryCopies = append(libraryCopies, m)
			} else {
				originals = append(originals, m)
			}
		}

		if len(libraryCopies) <= 1 {
			continue // only one library copy, nothing to prune
		}

		// Keep the oldest library copy (first created = lowest ULID), remove the rest
		// Sort by ID (ULIDs sort chronologically)
		keepIdx := 0
		for i := 1; i < len(libraryCopies); i++ {
			if libraryCopies[i].ID < libraryCopies[keepIdx].ID {
				keepIdx = i
			}
		}

		result.GroupsCleaned++
		for i, dup := range libraryCopies {
			if i == keepIdx {
				continue
			}

			slog.Info("version-group cleanup removing duplicate from group", "dupID", dup.ID, "dupPath", dup.FilePath, "groupID", groupID)

			if !dryRun {
				// Delete the file if it exists and is in the library
				if rootDir != "" && strings.HasPrefix(dup.FilePath, rootDir) {
					if _, err := os.Stat(dup.FilePath); err == nil {
						if err := os.Remove(dup.FilePath); err != nil {
							slog.Warn("failed to delete duplicate file", "dup", dup.FilePath, "err", err)
						} else {
							result.FilesDeleted++
						}
					}
				}
				// Delete the book record
				if err := store.DeleteBook(dup.ID); err != nil {
					slog.Warn("failed to delete duplicate book record", "dup", dup.ID, "err", err)
				}
			}
			result.DuplicatesRemoved++
		}

		// Ensure the kept library copy is primary and the original(s) are non-primary.
		// Hydrate each target via GetBookByID before writing back — kept/orig are
		// Core (slim) values here, and writing a slim struct through UpdateBook
		// would wipe the denormalized Author/Series on Pebble.
		if !dryRun {
			isPrimary := true
			isNotPrimary := false
			keptCore := libraryCopies[keepIdx]
			if kept, err := store.GetBookByID(keptCore.ID); err != nil || kept == nil {
				slog.Warn("version-group cleanup failed to hydrate kept book", "kept", keptCore.ID, "err", err)
			} else {
				kept.IsPrimaryVersion = &isPrimary
				store.UpdateBook(kept.ID, kept)
			}
			for _, origCore := range originals {
				orig, err := store.GetBookByID(origCore.ID)
				if err != nil || orig == nil {
					slog.Warn("version-group cleanup failed to hydrate original book", "orig", origCore.ID, "err", err)
					continue
				}
				orig.IsPrimaryVersion = &isNotPrimary
				store.UpdateBook(orig.ID, orig)
			}
		}
	}

	return result, nil
}

// FindBrokenSegmentBooks finds books whose segment files don't exist on disk
// and optionally marks them as needs_review.
func FindBrokenSegmentBooks(store Store, dryRun bool) (*BrokenSegmentResult, error) {
	allBooks, err := store.GetAllBooksCore(100000, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get books: %w", err)
	}

	result := &BrokenSegmentResult{}
	now := time.Now()
	needsReview := "needs_review"

	for _, book := range allBooks {
		// Only check directory-based books in import paths (not in library)
		if config.AppConfig.RootDir != "" && strings.HasPrefix(book.FilePath, config.AppConfig.RootDir) {
			continue
		}
		info, serr := os.Stat(book.FilePath)
		if serr != nil || !info.IsDir() {
			continue
		}

		files, segErr := store.GetBookFiles(book.ID)
		if segErr != nil || len(files) == 0 {
			continue
		}

		result.BooksChecked++
		var missingPaths []string
		activeCount := 0
		for _, f := range files {
			if f.Missing || f.FilePath == "" {
				continue
			}
			activeCount++
			if _, ferr := os.Stat(f.FilePath); os.IsNotExist(ferr) {
				missingPaths = append(missingPaths, f.FilePath)
			}
		}

		if len(missingPaths) == 0 {
			continue
		}

		entry := BrokenSegmentEntry{
			BookID:          book.ID,
			Title:           book.Title,
			FilePath:        book.FilePath,
			TotalSegments:   activeCount,
			MissingSegments: len(missingPaths),
			MissingPaths:    missingPaths,
		}
		result.Details = append(result.Details, entry)
		result.BrokenBooks++

		if !dryRun {
			// Hydrate before writeback — book is a Core (slim) value here, and
			// writing it straight through UpdateBook would wipe Author/Series.
			full, ferr := store.GetBookByID(book.ID)
			if ferr != nil || full == nil {
				slog.Warn("failed to hydrate broken book for update", "book", book.ID, "ferr", ferr)
			} else {
				full.LibraryState = &needsReview
				full.MarkedForDeletion = boolPtr(true)
				full.MarkedForDeletionAt = &now
				if _, uerr := store.UpdateBook(full.ID, full); uerr != nil {
					slog.Warn("failed to mark broken book", "book", full.ID, "uerr", uerr)
				} else {
					result.MarkedForReview++
				}
			}
		}
	}

	return result, nil
}

// MergeNoVGDuplicates finds no-VG books that match VG books by title, merges metadata, and soft-deletes.
// It also deduplicates among the remaining no-VG orphans (keeping one per title, soft-deleting extras).
func MergeNoVGDuplicates(store Store, rootDir string, dryRun bool) (*MergeDuplicatesResult, error) {
	result := &MergeDuplicatesResult{}

	// Load all books in pages. Core-typed: grouping/keeper-selection only needs
	// ID/Title/FilePath/VersionGroupID/IsPrimaryVersion, all Core-safe fields.
	var allBooks []database.BookCore
	pageSize := 5000
	for offset := 0; ; offset += pageSize {
		books, err := store.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("failed to get books: %w", err)
		}
		allBooks = append(allBooks, books...)
		if len(books) < pageSize {
			break
		}
	}

	// Index VG primary books by normalized title
	vgPrimaryByTitle := make(map[string]*database.BookCore)
	var noVGBooks []database.BookCore

	for i := range allBooks {
		b := &allBooks[i]
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			if b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
				normTitle := strings.TrimSpace(strings.ToLower(b.Title))
				vgPrimaryByTitle[normTitle] = b
			}
		} else {
			if rootDir != "" && strings.HasPrefix(b.FilePath, rootDir) {
				noVGBooks = append(noVGBooks, *b)
			}
		}
	}

	result.TotalNoVG = len(noVGBooks)
	now := time.Now()
	deletedState := "deleted"

	// Helper to soft-delete a book
	softDelete := func(book *database.Book) error {
		book.MarkedForDeletion = boolPtr(true)
		book.MarkedForDeletionAt = &now
		book.LibraryState = &deletedState
		_, err := store.UpdateBook(book.ID, book)
		return err
	}

	// hydrated caches full (heavy-field) Book rows fetched via GetBookByID, keyed
	// by ID. MergeBookMetadata reads/writes heavy fields (e.g. Description) that
	// BookCore does not carry, and every writeback below MUST target a hydrated
	// full row, never a Core value, or Author/Series would be wiped on Pebble.
	// The shared cache also preserves the original in-memory-accumulation
	// semantics: when several dupes merge into the same primary/keeper, later
	// merges see the earlier ones' effects (matches pre-refactor pointer reuse).
	hydrated := make(map[string]*database.Book)
	hydrate := func(id string) (*database.Book, error) {
		if b, ok := hydrated[id]; ok {
			return b, nil
		}
		full, err := store.GetBookByID(id)
		if err != nil || full == nil {
			return nil, err
		}
		hydrated[id] = full
		return full, nil
	}

	// Phase 1: Match no-VG books against existing VG primaries
	handled := make(map[string]bool)

	for i := range noVGBooks {
		dupeCore := &noVGBooks[i]
		normTitle := strings.TrimSpace(strings.ToLower(dupeCore.Title))
		primaryCore, found := vgPrimaryByTitle[normTitle]
		if !found {
			continue
		}

		handled[dupeCore.ID] = true
		result.MatchedToVG++
		entry := MergeDuplicateEntry{
			DuplicateID: dupeCore.ID,
			PrimaryID:   primaryCore.ID,
			Title:       dupeCore.Title,
		}

		primary, perr := hydrate(primaryCore.ID)
		if perr != nil || primary == nil {
			slog.Warn("merge-dupes failed to hydrate primary", "primary", primaryCore.ID, "err", perr)
			entry.Action = "error"
			result.Errors++
			result.Details = append(result.Details, entry)
			continue
		}
		dupe, derr := hydrate(dupeCore.ID)
		if derr != nil || dupe == nil {
			slog.Warn("merge-dupes failed to hydrate dupe", "dupe", dupeCore.ID, "err", derr)
			entry.Action = "error"
			result.Errors++
			result.Details = append(result.Details, entry)
			continue
		}

		merged := MergeBookMetadata(primary, dupe)
		entry.FieldsMerged = merged

		if !dryRun {
			if len(merged) > 0 {
				if _, err := store.UpdateBook(primary.ID, primary); err != nil {
					slog.Warn("merge-dupes failed to update primary", "primary", primary.ID, "err", err)
					entry.Action = "error"
					result.Errors++
					result.Details = append(result.Details, entry)
					continue
				}
				result.MetadataMerged++
			}
			if err := softDelete(dupe); err != nil {
				slog.Warn("merge-dupes failed to soft-delete", "dupe", dupe.ID, "err", err)
				entry.Action = "error"
				result.Errors++
			} else {
				result.SoftDeleted++
				if len(merged) > 0 {
					entry.Action = "merged_and_deleted"
				} else {
					entry.Action = "deleted_no_merge_needed"
				}
			}
		} else {
			result.SoftDeleted++
			if len(merged) > 0 {
				entry.Action = "would_merge_and_delete"
				result.MetadataMerged++
			} else {
				entry.Action = "would_delete_no_merge_needed"
			}
		}
		result.Details = append(result.Details, entry)
	}

	// Phase 2: Deduplicate among remaining no-VG orphans (same title → keep one, delete rest)
	orphansByTitle := make(map[string][]*database.BookCore)
	for i := range noVGBooks {
		b := &noVGBooks[i]
		if handled[b.ID] {
			continue
		}
		normTitle := strings.TrimSpace(strings.ToLower(b.Title))
		orphansByTitle[normTitle] = append(orphansByTitle[normTitle], b)
	}

	for _, group := range orphansByTitle {
		if len(group) <= 1 {
			continue
		}

		// Pick the best keeper: prefer directory-based (multi-file) paths, then longest path
		bestIdx := 0
		for i := 1; i < len(group); i++ {
			bestIsDir := !strings.Contains(filepath.Base(group[bestIdx].FilePath), ".")
			currIsDir := !strings.Contains(filepath.Base(group[i].FilePath), ".")
			if currIsDir && !bestIsDir {
				bestIdx = i
			} else if currIsDir == bestIsDir {
				if len(group[i].FilePath) < len(group[bestIdx].FilePath) {
					bestIdx = i
				}
			}
		}

		keeperCore := group[bestIdx]
		keeper, kerr := hydrate(keeperCore.ID)
		if kerr != nil || keeper == nil {
			slog.Warn("merge-self-dupes failed to hydrate keeper", "keeper", keeperCore.ID, "err", kerr)
			continue
		}
		for i, dupeCore := range group {
			if i == bestIdx {
				continue
			}

			result.SelfDuplicates++
			entry := MergeDuplicateEntry{
				DuplicateID: dupeCore.ID,
				PrimaryID:   keeper.ID,
				Title:       dupeCore.Title,
			}

			dupe, derr := hydrate(dupeCore.ID)
			if derr != nil || dupe == nil {
				slog.Warn("merge-self-dupes failed to hydrate dupe", "dupe", dupeCore.ID, "err", derr)
				entry.Action = "error"
				result.Errors++
				result.Details = append(result.Details, entry)
				continue
			}

			merged := MergeBookMetadata(keeper, dupe)
			entry.FieldsMerged = merged

			if !dryRun {
				if len(merged) > 0 {
					if _, err := store.UpdateBook(keeper.ID, keeper); err != nil {
						slog.Warn("merge-self-dupes failed to update keeper", "keeper", keeper.ID, "err", err)
					} else {
						result.MetadataMerged++
					}
				}
				if err := softDelete(dupe); err != nil {
					slog.Warn("merge-self-dupes failed to soft-delete", "dupe", dupe.ID, "err", err)
					entry.Action = "error"
					result.Errors++
				} else {
					result.SoftDeleted++
					entry.Action = "self_dupe_deleted"
				}
			} else {
				result.SoftDeleted++
				entry.Action = "would_delete_self_dupe"
				if len(merged) > 0 {
					result.MetadataMerged++
				}
			}
			result.Details = append(result.Details, entry)
		}
	}

	// Count remaining orphans (unique no-VG books with no match)
	remaining := 0
	for _, group := range orphansByTitle {
		if len(group) >= 1 {
			remaining++
		}
	}
	result.RemainingOrphans = remaining

	return result, nil
}

// MergeBookMetadata copies non-empty metadata fields from src to dst where dst field is empty.
// Returns list of field names that were merged.
func MergeBookMetadata(dst, src *database.Book) []string {
	var merged []string

	if dst.Narrator == nil && src.Narrator != nil {
		dst.Narrator = src.Narrator
		merged = append(merged, "narrator")
	}
	if dst.NarratorsJSON == nil && src.NarratorsJSON != nil {
		dst.NarratorsJSON = src.NarratorsJSON
		merged = append(merged, "narrators_json")
	}
	if dst.Description == nil && src.Description != nil {
		dst.Description = src.Description
		merged = append(merged, "description")
	}
	if dst.Language == nil && src.Language != nil {
		dst.Language = src.Language
		merged = append(merged, "language")
	}
	if dst.Publisher == nil && src.Publisher != nil {
		dst.Publisher = src.Publisher
		merged = append(merged, "publisher")
	}
	if dst.PrintYear == nil && src.PrintYear != nil {
		dst.PrintYear = src.PrintYear
		merged = append(merged, "print_year")
	}
	if dst.AudiobookReleaseYear == nil && src.AudiobookReleaseYear != nil {
		dst.AudiobookReleaseYear = src.AudiobookReleaseYear
		merged = append(merged, "audiobook_release_year")
	}
	if dst.ISBN10 == nil && src.ISBN10 != nil {
		dst.ISBN10 = src.ISBN10
		merged = append(merged, "isbn10")
	}
	if dst.ISBN13 == nil && src.ISBN13 != nil {
		dst.ISBN13 = src.ISBN13
		merged = append(merged, "isbn13")
	}
	if dst.ASIN == nil && src.ASIN != nil {
		dst.ASIN = src.ASIN
		merged = append(merged, "asin")
	}
	if dst.OpenLibraryID == nil && src.OpenLibraryID != nil {
		dst.OpenLibraryID = src.OpenLibraryID
		merged = append(merged, "open_library_id")
	}
	if dst.HardcoverID == nil && src.HardcoverID != nil {
		dst.HardcoverID = src.HardcoverID
		merged = append(merged, "hardcover_id")
	}
	if dst.GoogleBooksID == nil && src.GoogleBooksID != nil {
		dst.GoogleBooksID = src.GoogleBooksID
		merged = append(merged, "google_books_id")
	}
	if dst.Edition == nil && src.Edition != nil {
		dst.Edition = src.Edition
		merged = append(merged, "edition")
	}
	if dst.CoverURL == nil && src.CoverURL != nil {
		dst.CoverURL = src.CoverURL
		merged = append(merged, "cover_url")
	}
	if dst.Duration == nil && src.Duration != nil {
		dst.Duration = src.Duration
		merged = append(merged, "duration")
	}
	if dst.Bitrate == nil && src.Bitrate != nil {
		dst.Bitrate = src.Bitrate
		merged = append(merged, "bitrate")
	}
	if dst.Codec == nil && src.Codec != nil {
		dst.Codec = src.Codec
		merged = append(merged, "codec")
	}
	if dst.SampleRate == nil && src.SampleRate != nil {
		dst.SampleRate = src.SampleRate
		merged = append(merged, "sample_rate")
	}
	if dst.Channels == nil && src.Channels != nil {
		dst.Channels = src.Channels
		merged = append(merged, "channels")
	}
	if dst.FileHash == nil && src.FileHash != nil {
		dst.FileHash = src.FileHash
		merged = append(merged, "file_hash")
	}
	if dst.FileSize == nil && src.FileSize != nil {
		dst.FileSize = src.FileSize
		merged = append(merged, "file_size")
	}
	// iTunes fields
	if dst.ITunesPersistentID == nil && src.ITunesPersistentID != nil {
		dst.ITunesPersistentID = src.ITunesPersistentID
		merged = append(merged, "itunes_persistent_id")
	}
	if dst.ITunesPlayCount == nil && src.ITunesPlayCount != nil {
		dst.ITunesPlayCount = src.ITunesPlayCount
		merged = append(merged, "itunes_play_count")
	}
	if dst.ITunesRating == nil && src.ITunesRating != nil {
		dst.ITunesRating = src.ITunesRating
		merged = append(merged, "itunes_rating")
	}
	if dst.ITunesBookmark == nil && src.ITunesBookmark != nil {
		dst.ITunesBookmark = src.ITunesBookmark
		merged = append(merged, "itunes_bookmark")
	}
	// Author/Series: only merge if dst has none
	if dst.AuthorID == nil && src.AuthorID != nil {
		dst.AuthorID = src.AuthorID
		merged = append(merged, "author_id")
	}
	if dst.SeriesID == nil && src.SeriesID != nil {
		dst.SeriesID = src.SeriesID
		merged = append(merged, "series_id")
	}
	if dst.SeriesSequence == nil && src.SeriesSequence != nil {
		dst.SeriesSequence = src.SeriesSequence
		merged = append(merged, "series_sequence")
	}

	return merged
}

// AssignOrphanVGs finds books in the library directory that have no version group,
// creates a VG for each, and marks them as primary.
func AssignOrphanVGs(store Store, rootDir string) (*AssignVGResult, error) {
	result := &AssignVGResult{}

	var allBooks []database.BookCore
	pageSize := 5000
	for offset := 0; ; offset += pageSize {
		books, err := store.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("failed to get books: %w", err)
		}
		allBooks = append(allBooks, books...)
		if len(books) < pageSize {
			break
		}
	}

	for i := range allBooks {
		c := &allBooks[i]
		result.TotalChecked++

		// Skip books that already have a VG
		if c.VersionGroupID != nil && *c.VersionGroupID != "" {
			result.AlreadyHasVG++
			continue
		}

		// Only process books in the library directory
		if rootDir == "" || !strings.HasPrefix(c.FilePath, rootDir) {
			result.NotInLibrary++
			continue
		}

		// Hydrate before writeback — c is a Core (slim) value here, and writing
		// it straight through UpdateBook would wipe Author/Series.
		b, herr := store.GetBookByID(c.ID)
		if herr != nil || b == nil {
			slog.Warn("assign-orphan-vgs failed to hydrate book", "book", c.ID, "herr", herr)
			result.Errors++
			continue
		}

		// Create a version group and mark as primary
		vgID := fmt.Sprintf("vg-%s", ulid.Make().String())
		b.VersionGroupID = &vgID
		isPrimary := true
		b.IsPrimaryVersion = &isPrimary
		organizedState := "organized"
		b.LibraryState = &organizedState

		if _, err := store.UpdateBook(b.ID, b); err != nil {
			result.Errors++
			continue
		}
		result.Assigned++
	}

	return result, nil
}

func boolPtr(b bool) *bool {
	return &b
}
