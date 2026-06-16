// file: internal/organizer/organizer.go
// version: 1.18.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b

package organizer

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrTargetOccupied is returned from OrganizeBook when the computed
// target path already exists on disk and is a DIFFERENT file than the
// book's current source. This is the "two books with identical metadata
// want the same destination" case — e.g. two different audio files both
// tagged as "Asimov / Foundation". Previously OrganizeBook silently
// returned (target, "", nil) here, which caused the caller to update
// the DB file_path to the occupant's file — two rows pointed at the
// same file on disk and the second book's audio was orphaned.
//
// Callers that can usefully act on this (e.g. the manual organize
// endpoint) should detect the error via errors.Is, look up the occupant
// via GetBookByFilePath, and either create a dedup candidate or ask
// the user to resolve the collision before retrying.
var ErrTargetOccupied = errors.New("organize: target path already occupied by a different file")

// Organizer handles file organization operations
type Organizer struct {
	config *config.Config
	hooks  OrganizeHooks
	// store is the optional backing database. Used for duplicate-file
	// detection, self-owned-target checks, and author/series
	// resolution. Nil store → those lookups skip with the same
	// semantics the pre-audit `GetGlobalStore() == nil` branches had
	// (SERVER-GLOBAL-STORE-AUDIT phase 5).
	store database.Store
}

// SetHooks sets the optional organize hooks (e.g. collision callback).
func (o *Organizer) SetHooks(hooks OrganizeHooks) {
	o.hooks = hooks
}

// SetStore wires the database used for duplicate-file + author/series
// lookups. Idempotent; pass nil to disable lookups.
func (o *Organizer) SetStore(s database.Store) {
	o.store = s
}

const (
	defaultTitle    = "Unknown Title"
	defaultNarrator = "narrator"
	tempFileSuffix  = ".tmp"
)

var (
	leftoverPlaceholderRegex  = regexp.MustCompile(`\{[^}]+\}`)
	placeholderNormalizeRegex = regexp.MustCompile(`\{[A-Za-z_]+\}`)
	tempCleanupOnce           sync.Once
)

// NewOrganizer creates a new organizer instance
func NewOrganizer(cfg *config.Config) *Organizer {
	organizer := &Organizer{
		config: cfg,
	}
	if cfg != nil && strings.TrimSpace(cfg.RootDir) != "" {
		tempCleanupOnce.Do(func() {
			if err := organizer.cleanupTempFiles(); err != nil {
				slog.Warn("failed to clean temporary organizer files", "error", err)
			}
		})
	}
	return organizer
}

// OrganizeBook organizes a book file according to the configured patterns
// Returns (targetPath, method, error) where method is "reflink", "hardlink", "copy", or "symlink"
func (o *Organizer) OrganizeBook(book *database.Book) (string, string, error) {
	if book == nil {
		return "", "", fmt.Errorf("cannot organize: book is nil")
	}
	if book.FilePath == "" {
		return "", "", fmt.Errorf("cannot organize %q (id=%s): file_path is empty — book has no tracked file", book.Title, book.ID)
	}

	// Skip directories — only organize individual files
	if info, err := os.Stat(book.FilePath); err == nil && info.IsDir() {
		return "", "", fmt.Errorf("cannot organize %q (id=%s): file_path %s is a directory but single-file organize was requested — use organizeDirectoryBook for multi-file books", book.Title, book.ID, book.FilePath)
	}

	// Generate target path
	targetPath, err := o.generateTargetPath(book)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate target path: %w", err)
	}

	// Create target directory
	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0775); err != nil {
		return "", "", fmt.Errorf("failed to create target directory: %w", err)
	}

	// Check if source and target are the same path
	if book.FilePath == targetPath {
		return targetPath, "", nil
	}

	// Same-hash dedup check runs FIRST so a true content-duplicate (same
	// bytes, two DB rows) gets a proper error before we get to the
	// target-exists check. Otherwise a re-organize of a book whose
	// content already exists at another path under root would hit the
	// target-exists branch first (seeing the OLD organized version) and
	// silently no-op. Order matters.
	if book.FileHash != nil && *book.FileHash != "" && o.store != nil {
		existingBook, err := o.store.GetBookByFileHash(*book.FileHash)
		if err == nil && existingBook != nil && existingBook.ID != book.ID {
			if strings.HasPrefix(existingBook.FilePath, o.config.RootDir) {
				// Content-identical book already organized under a
				// different row. This is a true duplicate — fire the
				// collision hook so the dedup tab picks it up.
				if o.hooks != nil {
					o.hooks.OnCollision(book.ID, existingBook.FilePath)
				}
				return existingBook.FilePath, "", fmt.Errorf("duplicate file already organized at: %s", existingBook.FilePath)
			}
		}
	}

	// Check if file already exists at target path. Four cases:
	//   1. SameFile (hardlink/reflink): already organized, success no-op.
	//   2. Different inode but the DB says this exact book owns the
	//      target path: it's a previous copy-based organize of the same
	//      book (re-organize where the caller didn't refresh book.FilePath
	//      before the retry). Success no-op.
	//   3. Different inode and the DB says another book owns the target:
	//      real collision, fire hook, return ErrTargetOccupied.
	//   4. Different inode and no DB row at the target (e.g. orphaned
	//      file from a previous partial organize): treat as collision
	//      too — refuse to overwrite, let the user resolve it.
	if targetInfo, err := os.Stat(targetPath); err == nil {
		if srcInfo, srcErr := os.Stat(book.FilePath); srcErr == nil {
			if os.SameFile(srcInfo, targetInfo) {
				return targetPath, "", nil
			}
		}
		// Case 2: ask the DB who owns the target. If it's the current
		// book, this is a re-organize no-op — don't panic, don't fire
		// the collision hook, just return the target.
		if o.store != nil && book.ID != "" {
			if owner, lookupErr := o.store.GetBookByFilePath(targetPath); lookupErr == nil && owner != nil && owner.ID == book.ID {
				return targetPath, "", nil
			}
		}
		// Case 3/4: collision. Fire the hook so the server can create a
		// pending dedup candidate between this book and whoever owns
		// the target, then return the explicit error. The old code
		// silently returned nil here, which caused the caller to set
		// this book's file_path to the occupant's file — two DB rows
		// pointing at one file on disk.
		if o.hooks != nil {
			o.hooks.OnCollision(book.ID, targetPath)
		}
		return targetPath, "", fmt.Errorf("%w: %s", ErrTargetOccupied, targetPath)
	}

	// Perform the organization based on strategy
	strategy := o.config.OrganizationStrategy

	if strategy == "auto" {
		// Try reflink -> hardlink -> copy
		if err := o.reflinkFile(book.FilePath, targetPath); err == nil {
			return targetPath, "reflink", nil
		}
		if err := o.hardlinkFile(book.FilePath, targetPath); err == nil {
			return targetPath, "hardlink", nil
		}
		strategy = "copy"
	}

	switch strategy {
	case "copy":
		return targetPath, "copy", o.copyFile(book.FilePath, targetPath)
	case "hardlink":
		return targetPath, "hardlink", o.hardlinkFile(book.FilePath, targetPath)
	case "reflink":
		return targetPath, "reflink", o.reflinkFile(book.FilePath, targetPath)
	case "symlink":
		return targetPath, "symlink", o.symlinkFile(book.FilePath, targetPath)
	default:
		return "", "", fmt.Errorf("unknown organization strategy: %s", strategy)
	}
}

// GenerateTargetPath creates the target file path based on naming patterns.
// This is the public API for computing where a book would be organized to,
// without actually performing the move. Used by preview rename and organize.
func (o *Organizer) GenerateTargetPath(book *database.Book) (string, error) {
	return o.generateTargetPath(book)
}

// GenerateTargetDirPath returns the target directory path for a directory-based
// (multi-file) book. It uses the folder naming pattern only (no file name).
func (o *Organizer) GenerateTargetDirPath(book *database.Book) (string, error) {
	folderPath, err := o.expandPattern(o.config.FolderNamingPattern, book)
	if err != nil {
		return "", fmt.Errorf("folder pattern: %w", err)
	}
	folderPath = sanitizePath(folderPath)
	result := filepath.Join(o.config.RootDir, folderPath)
	if err := ensureUnderRoot(result, o.config.RootDir); err != nil {
		return "", err
	}
	return result, nil
}

// generateTargetPath creates the target file path based on naming patterns
func (o *Organizer) generateTargetPath(book *database.Book) (string, error) {
	// Get file extension
	ext := filepath.Ext(book.FilePath)

	// Generate folder path
	folderPath, err := o.expandPattern(o.config.FolderNamingPattern, book)
	if err != nil {
		return "", fmt.Errorf("folder pattern: %w", err)
	}
	folderPath = sanitizePath(folderPath)

	// Generate file name
	fileName, err := o.expandPattern(o.config.FileNamingPattern, book)
	if err != nil {
		return "", fmt.Errorf("file pattern: %w", err)
	}
	fileName = sanitizeFilename(fileName) + ext

	// When iTunes path trimming is enabled, shorten the filename stem so the
	// Windows-equivalent path stays under MAX_PATH (260 chars). This uses
	// config.ITunes.WindowsRootPath as the Windows equivalent of RootDir.
	if config.AppConfig.ITunes.PathTrimEnabled && config.AppConfig.ITunes.WindowsRootPath != "" {
		windowsRoot := strings.TrimRight(config.AppConfig.ITunes.WindowsRootPath, `\/`)
		relDir := strings.ReplaceAll(folderPath, "/", `\`)
		// Windows path = windowsRoot + \ + relDir + \ + filename
		prefixLen := len(windowsRoot) + 1 + len(relDir) + 1
		fileExt := filepath.Ext(fileName)
		stem := strings.TrimSuffix(fileName, fileExt)
		maxStem := 260 - prefixLen - len(fileExt)
		if maxStem < 1 {
			maxStem = 1
		}
		if len(stem) > maxStem {
			stem = stem[:maxStem]
			fileName = stem + fileExt
		}
	}

	// Combine with root directory
	fullPath := filepath.Join(o.config.RootDir, folderPath, fileName)

	if err := ensureUnderRoot(fullPath, o.config.RootDir); err != nil {
		return "", err
	}

	return fullPath, nil
}

// expandPattern expands a pattern with book metadata
func (o *Organizer) expandPattern(pattern string, book *database.Book) (string, error) {
	result := placeholderNormalizeRegex.ReplaceAllStringFunc(pattern, strings.ToLower)

	// Get author name - look up by ID if Author object is nil but AuthorID is set
	authorName := "Unknown Author"
	if book.Author != nil {
		if trimmed := strings.TrimSpace(book.Author.Name); trimmed != "" {
			authorName = trimmed
		}
	} else if book.AuthorID != nil && o.store != nil {
		// Author object not populated, but we have an ID - look it up
		author, err := o.store.GetAuthorByID(*book.AuthorID)
		if err == nil && author != nil {
			if trimmed := strings.TrimSpace(author.Name); trimmed != "" {
				authorName = trimmed
			}
		}
	}

	title := strings.TrimSpace(book.Title)
	if title == "" {
		title = defaultTitle
	}

	// Get series info - look up by ID if Series object is nil but SeriesID is set
	seriesName := ""
	if book.Series != nil {
		seriesName = strings.TrimSpace(book.Series.Name)
	} else if book.SeriesID != nil && o.store != nil {
		// Series object not populated, but we have an ID - look it up
		series, err := o.store.GetSeriesByID(*book.SeriesID)
		if err == nil && series != nil {
			seriesName = strings.TrimSpace(series.Name)
		}
	}

	seriesNum := ""
	if book.SeriesSequence != nil && *book.SeriesSequence > 0 {
		seriesNum = fmt.Sprintf("%d", *book.SeriesSequence)
	}

	// Helper to convert int pointer to string
	intToString := func(i *int) string {
		if i == nil {
			return ""
		}
		return fmt.Sprintf("%d", *i)
	}

	narrator := strings.TrimSpace(stringOrEmpty(book.Narrator))
	if narrator == "" {
		narrator = defaultNarrator
	}

	// Replacements map
	replacements := map[string]string{
		"{title}":         title,
		"{author}":        authorName,
		"{series}":        seriesName,
		"{series_number}": seriesNum,
		"{narrator}":      narrator,
		"{publisher}":     stringOrEmpty(book.Publisher),
		"{language}":      stringOrEmpty(book.Language),
		"{edition}":       stringOrEmpty(book.Edition),
		"{print_year}":    intToString(book.PrintYear),
		"{year}":          intToString(book.PrintYear),
		"{isbn10}":        stringOrEmpty(book.ISBN10),
		"{isbn13}":        stringOrEmpty(book.ISBN13),
		"{bitrate}":       intToString(book.Bitrate),
		"{codec}":         stringOrEmpty(book.Codec),
		"{quality}":       stringOrEmpty(book.Quality),
	}

	// Perform replacements
	for placeholder, value := range replacements {
		if strings.TrimSpace(value) == "" {
			result = removeEmptySegment(result, placeholder)
			result = strings.ReplaceAll(result, placeholder, "")
		} else {
			result = strings.ReplaceAll(result, placeholder, value)
		}
	}

	result = cleanupPattern(result)
	if leftoverPlaceholderRegex.MatchString(result) {
		leftover := leftoverPlaceholderRegex.FindAllString(result, -1)
		return "", fmt.Errorf("naming pattern produced %q with unresolved placeholders %v — book is missing values for these fields, or the pattern references unknown placeholders", result, leftover)
	}
	return result, nil
}

// removeEmptySegment removes segments containing empty placeholders
func removeEmptySegment(pattern, placeholder string) string {
	patterns := []string{
		fmt.Sprintf(` - %s`, placeholder),
		fmt.Sprintf(`%s - `, placeholder),
		fmt.Sprintf(`\(%s[^)]*\)`, regexp.QuoteMeta(placeholder)),
		fmt.Sprintf(`\([^(]*%s\)`, regexp.QuoteMeta(placeholder)),
	}

	result := placeholderNormalizeRegex.ReplaceAllStringFunc(pattern, strings.ToLower)
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		result = re.ReplaceAllString(result, "")
	}
	return result
}

// cleanupPattern cleans up extra spaces, dashes, and parentheses
func cleanupPattern(pattern string) string {
	re := regexp.MustCompile(`\s+`)
	pattern = re.ReplaceAllString(pattern, " ")

	re = regexp.MustCompile(`\(\s*\)`)
	pattern = re.ReplaceAllString(pattern, "")

	pattern = strings.Trim(pattern, " -/")

	re = regexp.MustCompile(`/+`)
	pattern = re.ReplaceAllString(pattern, "/")

	return pattern
}

// sanitizePath sanitizes a path for filesystem use
func sanitizePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = sanitizeFilename(part)
	}
	return strings.Join(parts, "/")
}

// sanitizeFilename sanitizes a filename for filesystem use
func sanitizeFilename(name string) string {
	// Remove control characters and non-printable bytes
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)

	// Prevent path traversal — strip ".." components
	name = strings.ReplaceAll(name, "..", "_")

	invalid := []string{"<", ">", ":", "\"", "|", "?", "*"}
	for _, char := range invalid {
		name = strings.ReplaceAll(name, char, "_")
	}

	// Strip brackets (ugly in paths, cause shell escaping issues)
	name = strings.ReplaceAll(name, "[", "")
	name = strings.ReplaceAll(name, "]", "")

	re := regexp.MustCompile(`\s+`)
	name = re.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)

	// Limit filename length (255 byte max on most filesystems, leave room for extension + .tmp)
	if len(name) > 200 {
		name = name[:200]
	}

	return name
}

// ensureUnderRoot verifies that fullPath is inside rootDir after cleaning.
// Prevents path traversal via ".." in metadata fields.
func ensureUnderRoot(fullPath, rootDir string) error {
	cleanTarget := filepath.Clean(fullPath)
	cleanRoot := filepath.Clean(rootDir)
	if !strings.HasPrefix(cleanTarget, cleanRoot+string(filepath.Separator)) && cleanTarget != cleanRoot {
		return fmt.Errorf("generated path %q escapes the configured root %q — likely caused by special characters in author/title metadata or a malformed naming pattern", cleanTarget, cleanRoot)
	}
	return nil
}

// stringOrEmpty returns the string value or empty string if nil
func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// copyFile copies a file from src to dst
func (o *Organizer) copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot read source file %s: %w", src, err)
	}
	defer sourceFile.Close()

	tempPath := dst + tempFileSuffix
	_ = os.Remove(tempPath)

	destFile, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("cannot create destination file %s: %w (check parent directory permissions and disk space)", tempPath, err)
	}
	defer func() {
		_ = destFile.Close()
	}()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to copy file: %w", err)
	}

	if err := destFile.Sync(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to sync destination file: %w", err)
	}

	if err := destFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to close destination file: %w", err)
	}
	if err := os.Rename(tempPath, dst); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to finalize destination file: %w", err)
	}

	return nil
}

func (o *Organizer) cleanupTempFiles() error {
	if o == nil || o.config == nil || strings.TrimSpace(o.config.RootDir) == "" {
		return nil
	}

	return filepath.Walk(o.config.RootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(info.Name(), tempFileSuffix) {
			_ = os.Remove(path)
		}
		return nil
	})
}

// hardlinkFile creates a hard link from src to dst
func (o *Organizer) hardlinkFile(src, dst string) error {
	return os.Link(src, dst)
}

// symlinkFile creates a symbolic link from src to dst
func (o *Organizer) symlinkFile(src, dst string) error {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}
	return os.Symlink(absSrc, dst)
}

// OrganizeBookDirectory organizes a multi-file book (directory) by copying each
// segment file into the target directory generated from the book's metadata.
// Returns the target directory path and a map of old→new segment file paths.
func (o *Organizer) OrganizeBookDirectory(book *database.Book, segmentPaths []string) (string, map[string]string, error) {
	if book == nil {
		return "", nil, fmt.Errorf("invalid book")
	}
	if len(segmentPaths) == 0 {
		return "", nil, fmt.Errorf("no segment files to organize")
	}

	// Generate target directory from folder naming pattern
	folderPath, err := o.expandPattern(o.config.FolderNamingPattern, book)
	if err != nil {
		return "", nil, fmt.Errorf("folder pattern: %w", err)
	}
	folderPath = sanitizePath(folderPath)
	targetDir := filepath.Join(o.config.RootDir, folderPath)

	if err := os.MkdirAll(targetDir, 0775); err != nil {
		return "", nil, fmt.Errorf("failed to create target directory: %w", err)
	}

	pathMap := make(map[string]string, len(segmentPaths))
	for _, srcPath := range segmentPaths {
		fileName := filepath.Base(srcPath)
		dstPath := filepath.Join(targetDir, fileName)

		// Verify dstPath stays inside targetDir (defense against crafted filenames)
		if err := ensureUnderRoot(dstPath, targetDir); err != nil {
			slog.Warn("organizeFile skipping unsafe destination", "error", err)
			continue
		}

		if srcPath == dstPath {
			pathMap[srcPath] = dstPath
			continue
		}

		// Skip if target already exists
		if _, err := os.Stat(dstPath); err == nil {
			pathMap[srcPath] = dstPath
			continue
		}

		if _, err := o.organizeFile(srcPath, dstPath); err != nil {
			// Skip missing source files instead of aborting the entire book
			if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
				slog.Warn("organizeFile skipping missing source file", "path", srcPath)
				continue
			}
			// Handle race: another worker may have created the file between our stat and copy
			if os.IsExist(err) {
				pathMap[srcPath] = dstPath
				continue
			}
			return "", nil, fmt.Errorf("failed to organize segment %s: %w", fileName, err)
		}
		pathMap[srcPath] = dstPath
	}

	return targetDir, pathMap, nil
}

// organizeFile copies/links a single file using the configured strategy.
// Returns (method, error) where method is "reflink", "hardlink", "copy", or "symlink"
func (o *Organizer) organizeFile(src, dst string) (string, error) {
	strategy := o.config.OrganizationStrategy

	if strategy == "auto" {
		if err := o.reflinkFile(src, dst); err == nil {
			slog.Debug("organizeFile reflink succeeded", "src", filepath.Base(src), "dst", filepath.Base(dst))
			return "reflink", nil
		} else {
			slog.Warn("organizeFile reflink failed", "file", filepath.Base(src), "error", err)
		}
		if err := o.hardlinkFile(src, dst); err == nil {
			slog.Debug("organizeFile hardlink succeeded", "src", filepath.Base(src), "dst", filepath.Base(dst))
			return "hardlink", nil
		} else {
			slog.Warn("organizeFile hardlink failed", "file", filepath.Base(src), "error", err)
		}
		slog.Warn("organizeFile falling back to copy", "file", filepath.Base(src))
		strategy = "copy"
	}

	switch strategy {
	case "copy":
		return "copy", o.copyFile(src, dst)
	case "hardlink":
		return "hardlink", o.hardlinkFile(src, dst)
	case "reflink":
		return "reflink", o.reflinkFile(src, dst)
	case "symlink":
		return "symlink", o.symlinkFile(src, dst)
	default:
		return "", fmt.Errorf("unknown organization strategy: %s", strategy)
	}
}

// reflinkFile creates a copy-on-write reflink (platform-specific)
func (o *Organizer) reflinkFile(src, dst string) error {
	return o.reflinkFilePlatform(src, dst)
}
