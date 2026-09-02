// file: internal/organizer/organizer.go
// version: 1.37.1
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-09-02

package organizer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// Organizer handles file organization operations
type Organizer struct {
	config *config.Config
	hooks  OrganizeHooks
	// store is the optional backing database. Used for duplicate-file
	// detection, self-owned-target checks, and author/series
	// resolution. Nil store → those lookups skip with the same
	// semantics the pre-audit `GetGlobalStore() == nil` branches had
	// (SERVER-GLOBAL-STORE-AUDIT phase 5).
	store OrganizerStore
}

// SetHooks sets the optional organize hooks (e.g. collision callback).
func (o *Organizer) SetHooks(hooks OrganizeHooks) {
	o.hooks = hooks
}

// OrganizerStore is the narrow read surface the Organizer needs: author and
// series resolution for path templates, and book lookups for duplicate-target
// identification. It is deliberately small so every organize entry point —
// the bulk Service (whose composite Store is narrower than database.Store),
// the preview/rename services, and the server's auto-organize — can wire a
// store. Until 2026-08-14 ONLY auto-organize called SetStore, which made the
// AuthorID fallback in expandPattern dead code everywhere else: any book
// whose Author struct was not populated organized into "Unknown Author/"
// with its AuthorID sitting right there. That is the mechanism behind the
// 2026-08-11 mass-reorganize (audit, open question 2 — answered).
type OrganizerStore interface {
	GetAuthorByID(id int) (*database.Author, error)
	GetSeriesByID(id int) (*database.Series, error)
	GetBookByFileHash(hash string) (*database.Book, error)
	GetBookByFilePath(path string) (*database.Book, error)
}

// SetStore wires the database used for duplicate-file + author/series
// lookups. Idempotent; pass nil to disable lookups.
func (o *Organizer) SetStore(s OrganizerStore) {
	o.store = s
}

const (
	defaultTitle   = "Unknown Title"
	tempFileSuffix = ".tmp"

	// patternSegmentSep is the delimiter that separates naming-pattern
	// segments. A segment is the unit that gets dropped wholesale when every
	// placeholder inside it is empty — see dropEmptyPatternSegments.
	patternSegmentSep = " - "
)

// NOTE: there is deliberately no defaultNarrator constant. It used to be
// "narrator", and expandPattern substituted it whenever a book had no narrator.
// With the default pattern `{title} - {author} - read by {narrator}` that wrote
// the literal word into real filenames: measured on production 2026-08-11,
// 2,611 of 3,194 books failing organize with ErrTargetOccupied had computed a
// path ending in "- read by narrator". Empty narrator now takes the
// empty-placeholder path like every other unset field.

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

	// Check if file already exists at target path. Three cases:
	//   1. SameFile (hardlink/reflink): already organized, success no-op.
	//   2. Different inode but the DB says this exact book owns the
	//      target path: it's a previous copy-based organize of the same
	//      book (re-organize where the caller didn't refresh book.FilePath
	//      before the retry). Success no-op.
	//   3. A different file owns the target: preserve it and select the
	//      next deterministic _copyN filename for this book.
	if targetInfo, err := os.Stat(targetPath); err == nil {
		if srcInfo, srcErr := os.Stat(book.FilePath); srcErr == nil {
			if os.SameFile(srcInfo, targetInfo) {
				return targetPath, "", nil
			}
		}
		// Case 2: ask the DB who owns the target. If it's the current
		// book, this is a re-organize no-op — don't panic, don't fire
		// the collision hook, just return the target.
		//
		if o.store != nil && book.ID != "" {
			owner, lookupErr := o.store.GetBookByFilePath(targetPath)
			if lookupErr == nil {
				if owner != nil && owner.ID == book.ID {
					return targetPath, "", nil
				}
			}
		}
		var suffixErr error
		targetPath, suffixErr = nextAvailableTargetPath(targetPath)
		if suffixErr != nil {
			return "", "", suffixErr
		}
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

// nextAvailableTargetPath returns the first unoccupied sibling filename using
// the user-visible collision convention: "book.m4b", "book_copy1.m4b",
// "book_copy2.m4b", and so on. It only chooses a path; the transfer helpers
// still use exclusive destination creation so a concurrent organizer cannot
// overwrite a file created after this check.
func nextAvailableTargetPath(targetPath string) (string, error) {
	dir, name := filepath.Dir(targetPath), filepath.Base(targetPath)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)

	for copyNumber := 1; ; copyNumber++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s_copy%d%s", stem, copyNumber, ext))
		if _, err := os.Lstat(candidate); err != nil {
			if os.IsNotExist(err) {
				return candidate, nil
			}
			return "", fmt.Errorf("inspect candidate destination %s: %w", candidate, err)
		}
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

	// One composer, shared with ComputeTargetPaths — see BuildRelPath. This
	// function used to compose folder + file itself, and so did the
	// metadata-apply path, and the two disagreed.
	relPath, err := BuildRelPath(o.config.FolderNamingPattern, o.config.FileNamingPattern,
		o.pathVars(book, 0, 0, strings.TrimPrefix(ext, ".")), o.buildOpts())
	if err != nil {
		return "", err
	}

	folderPath, fileName := "", relPath
	if idx := strings.LastIndex(relPath, "/"); idx >= 0 {
		folderPath, fileName = relPath[:idx], relPath[idx+1:]
	}
	fileName += ext

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
		maxStem := max(260-prefixLen-len(fileExt), 1)
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

// placeholderAuthor is the path fallback for books with no resolvable author.
// Baking it into an organized path is what the 2026-08-11 mass-reorganize did
// 23,622 times — see docs/audits/2026-08-13-mass-reorganize-duplicated-14tb-
// under-unknown-author.md. Rename/copy paths must gate on HasResolvedAuthor
// before using a target built from this.
const placeholderAuthor = authorname.Placeholder

// resolveAuthorName returns the book's effective author name, following
// AuthorID when the Author object is not populated. Empty string means the
// author is UNRESOLVED and any generated path would fall back to
// placeholderAuthor.
func (o *Organizer) resolveAuthorName(book *database.Book) string {
	if book.Author != nil {
		if trimmed := strings.TrimSpace(book.Author.Name); trimmed != "" {
			return trimmed
		}
	} else if book.AuthorID != nil && o.store != nil {
		author, err := o.store.GetAuthorByID(*book.AuthorID)
		if err == nil && author != nil {
			if trimmed := strings.TrimSpace(author.Name); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// HasResolvedAuthor reports whether organizing this book would use a real
// author name rather than the placeholder. Callers that RENAME or COPY into
// the library tree must defer when this is false: a book filed under the
// placeholder needs metadata resolution, not a rename that cements the
// placeholder into its path (and later a content-verified purge to undo).
func (o *Organizer) HasResolvedAuthor(book *database.Book) bool {
	return o.resolveAuthorName(book) != ""
}

// expandPattern expands a naming pattern with this book's metadata.
//
// This is now a thin adapter over BuildPath, the single target-path builder
// (see pathbuild.go). It exists to resolve the book's fields -- following
// AuthorID and SeriesID through the store when the objects are not populated --
// and hand them over. All pattern semantics live in BuildPath.
func (o *Organizer) expandPattern(pattern string, book *database.Book) (string, error) {
	return BuildPath(pattern, o.pathVars(book, 0, 0, ""), o.buildOpts())
}

// buildOpts carries the organize-side path decisions. AuthorFallback is what
// keeps an authorless book in a named directory instead of flat at the library
// root -- the behaviour the old path_format builder did NOT have.
func (o *Organizer) buildOpts() BuildOpts {
	return BuildOpts{
		AuthorFallback: placeholderAuthor,
		TitleFallback:  defaultTitle,
	}
}

// pathVars collects every naming variable for a book. track/totalTracks/ext are
// zero-valued for a single-file book and populated per segment for a
// multi-file one, which is what lets ONE builder serve both.
func (o *Organizer) pathVars(book *database.Book, track, totalTracks int, ext string) PathVars {
	intToString := func(i *int) string {
		if i == nil {
			return ""
		}
		return fmt.Sprintf("%d", *i)
	}

	seriesName := ""
	if book.Series != nil {
		seriesName = strings.TrimSpace(book.Series.Name)
	} else if book.SeriesID != nil && o.store != nil {
		if series, err := o.store.GetSeriesByID(*book.SeriesID); err == nil && series != nil {
			seriesName = strings.TrimSpace(series.Name)
		}
	}

	seriesNum := ""
	if book.SeriesSequence != nil && *book.SeriesSequence > 0 {
		seriesNum = fmt.Sprintf("%d", *book.SeriesSequence)
	}

	return PathVars{
		Author:       o.resolveAuthorName(book),
		Title:        strings.TrimSpace(book.Title),
		Series:       seriesName,
		SeriesNumber: seriesNum,
		Narrator:     strings.TrimSpace(stringOrEmpty(book.Narrator)),
		Publisher:    stringOrEmpty(book.Publisher),
		Language:     stringOrEmpty(book.Language),
		Edition:      stringOrEmpty(book.Edition),
		Codec:        stringOrEmpty(book.Codec),
		Quality:      stringOrEmpty(book.Quality),
		Bitrate:      intToString(book.Bitrate),
		ISBN10:       stringOrEmpty(book.ISBN10),
		ISBN13:       stringOrEmpty(book.ISBN13),
		PrintYear:    intToString(book.PrintYear),
		Track:        track,
		TotalTracks:  totalTracks,
		Ext:          ext,
	}
}

// dropEmptyPatternSegments removes each " - "-delimited segment of a naming
// pattern whose placeholders are ALL empty, including any literal connector
// words the segment carries ("read by", "narrated by", "#", ...).
//
// It must be called on the RAW pattern, before any substitution: once values
// are in, a title like "Foundation - Part 1" is indistinguishable from a
// segment boundary and would be split apart.
//
// Rules, in order:
//   - A segment with no placeholders at all is literal text the user asked
//     for; it is always kept.
//   - A segment where at least one placeholder has a value is kept, and the
//     empty placeholders inside it are cleaned up downstream by
//     removeEmptySegment / cleanupPattern. This is what stops
//     `{title} - {series} {series_number}` from losing the series name just
//     because the book has no series number.
//   - Only a segment where every placeholder is empty is dropped.
//
// Placeholders that are not in the replacements map at all (typos, unknown
// fields) are deliberately NOT treated as empty — they survive into the
// leftover-placeholder check in expandPattern so a bad pattern errors loudly
// instead of silently deleting the segment that referenced it.
//
// Path components are handled independently so an empty folder level collapses
// on its own; cleanupPattern squashes the resulting "//".
func dropEmptyPatternSegments(pattern string, empties map[string]struct{}) string {
	components := strings.Split(pattern, "/")
	for i, component := range components {
		segments := strings.Split(component, patternSegmentSep)
		kept := segments[:0:0]
		for _, segment := range segments {
			placeholders := leftoverPlaceholderRegex.FindAllString(segment, -1)
			allEmpty := len(placeholders) > 0
			for _, placeholder := range placeholders {
				if _, empty := empties[placeholder]; !empty {
					allEmpty = false
					break
				}
			}
			if !allEmpty {
				kept = append(kept, segment)
			}
		}
		components[i] = strings.Join(kept, patternSegmentSep)
	}
	return strings.Join(components, "/")
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

// sanitizePath sanitizes a multi-component relative path, one component at a
// time. It is a thin wrapper over SanitizePathComponent, the package's single
// sanitizer — the "/" separators are structure and must survive; everything
// between them is a component.
//
// The private sanitizeFilename this used to call is gone. It duplicated
// SanitizePathComponent for every character that mattered and disagreed with it
// on exactly one: it stripped '[' and ']'. Since BuildPath sanitizes before
// returning, that made the second pass a no-op except where it silently undid
// the first. See SanitizePathComponent.
func sanitizePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = SanitizePathComponent(part)
	}
	return strings.Join(parts, "/")
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

// copyFile copies src to dst without ever replacing an existing dst.
//
// The bytes go through fileops.CopyFileIngestExclusive — INGEST, not CopyFile:
// src is a file from outside the library (a download client's output, a watch
// folder), and adopting its permission bits would let that client's umask decide
// the library's. A torrent client on umask 077 produces an 0600 .m4b, and an
// organized library of 0600 files stops being served over the share while every
// copy still reports success. The os.Create this replaced was umask-floored and
// safe for that reason; the Ingest variants keep the property deliberately.
//
// The temp name is UNIQUE PER WRITER and opened O_EXCL. It used to be the fixed
// `dst+".tmp"`, os.Remove'd first and opened O_TRUNC — and organizeBooks runs
// eight workers with no cross-book target dedupe, so two same-titled books, or
// one book planned twice, wrote through the same temp at once. In 30 of 30 probe
// iterations one worker returned success while dst held the OTHER worker's
// bytes, or an interleaving of both at full length, so verifyRenamed's size
// check passed over corrupt audio. O_EXCL turns a shared temp into an error
// instead of a shared file descriptor, and a temp this writer did not create is
// never removed.
//
// The temp still sits beside dst and ends in tempFileSuffix because
// cleanupTempFiles sweeps by that suffix under RootDir and must be able to
// recognise what a crashed worker left behind.
//
// Finalisation is finalizeExclusive rather than a bare os.Rename: os.Rename
// silently REPLACES a destination that appeared between the caller's
// exists-check and now (concurrent organize workers), which for an audiobook
// means the other worker's file is gone. A destination collision is returned
// as an error satisfying errors.Is(err, fs.ErrExist) so organizeFile callers'
// race recovery keeps working.
//
// A collision on the TEMP name is deliberately NOT one of those. It means two
// writers drew the same nonce, not that dst is occupied, and if it surfaced as
// ErrExist the caller would stat dst, find nothing, and log "lost a race" for a
// file that simply was not written. It is reported as the plain failure it is,
// and the temp is left alone because it is the other writer's.
func (o *Organizer) copyFile(src, dst string) error {
	tempPath := o.tempPathFor(dst)

	if err := fileops.CopyFileIngestExclusive(src, tempPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("temp file %s for %s already exists (nonce collision with another writer): %v", tempPath, dst, err)
		}
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to write temp file for %s: %w", dst, err)
	}

	if err := finalizeExclusive(tempPath, dst); err != nil {
		_ = os.Remove(tempPath)
		if errors.Is(err, fs.ErrExist) {
			return err
		}
		return fmt.Errorf("failed to finalize destination file: %w", err)
	}
	return nil
}

// tempPathFor returns a fresh temp name beside dst: `<dst>.<nonce>.tmp`. The
// nonce is what makes two concurrent writers of the same dst independent;
// O_EXCL at open time is what makes a collision an error rather than a shared
// file. The two are separate defences on purpose: tests pin the nonce to force
// a collision and prove the O_EXCL half holds on its own.
func (o *Organizer) tempPathFor(dst string) string {
	return dst + "." + tempNonce() + tempFileSuffix
}

// tempNonce is a package variable, not a direct call, so a test can make two
// writers collide on one temp name. Production never reassigns it. A test that
// does reassign it is mutating package state and must not run under
// t.Parallel(); restore it with t.Cleanup.
var tempNonce = randomTempNonce

// randomTempNonce returns 16 hex characters from crypto/rand. Since Go 1.24
// rand.Read is documented never to return an error (it crashes the program if
// the platform source is unavailable), so there is no fallback branch here: a
// clock-based fallback would have been dead code that, had it ever run, made
// two same-nanosecond writers collide by design.
func randomTempNonce() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (o *Organizer) cleanupTempFiles() error {
	if o == nil || o.config == nil || strings.TrimSpace(o.config.RootDir) == "" {
		return nil
	}

	app := appdirs.FromConfig(o.config)
	return filepath.Walk(o.config.RootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			// Application directories under RootDir hold multi-GB archives
			// and dump files. Walking them to look for temp files is pure I/O
			// for a result that cannot be there. Excluded by configured path,
			// not by a leading dot in the name.
			if pathutil.ShouldSkipDir(o.config.RootDir, path, app) {
				return filepath.SkipDir
			}
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

// PlanFilePaths returns current file path -> organized target path for every
// non-missing file of a directory book, using the SAME planner
// OrganizeBookDirectory copies with. It answers "where WOULD this go", which
// is not the same question as "where DID it go": a planned target can fail
// to land (source vanished, occupant not proven to be this file, lost race),
// which now fails the whole book, and until 2026-09-02 CreateOrganizedVersion
// recomputed the plan and then adopted any file that happened to exist at a
// planned target — pointing a book's row at another book's audio. Callers
// that need the landed paths must take them from OrganizeBookDirectory's
// returned map (see service.Landing), never from this plan. Files already at
// their target map to themselves.
func (o *Organizer) PlanFilePaths(book *database.Book, files []database.BookFile) (map[string]string, error) {
	planned, err := planTargetPaths(o.config.RootDir, o.config.FolderNamingPattern,
		o.config.FileNamingPattern, files, o.pathVars(book, 0, 0, ""), o.buildOpts())
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(planned))
	for _, e := range planned {
		out[e.SourcePath] = e.TargetPath
	}
	return out, nil
}

// OrganizeBookDirectory organizes a multi-file book (directory) by copying each
// of its files into the target directory generated from the book's metadata.
// Returns the target directory path and a map of old→new file paths.
//
// It takes []database.BookFile rather than the []string of paths it took until
// 2026-08-15 because a path string carries no track number. This function used
// to keep filepath.Base(src) as the destination filename -- it applied the
// folder pattern and never the file pattern -- so a directory book was folder
// aware but not file aware, and its filenames could never agree with what
// ComputeTargetPaths planned for the very same book on the metadata-apply path.
// Deriving the track from sort position alone would not fix it: whenever
// TrackNumber is set and disagrees with alphabetical order the two paths would
// still land on different names. Both now run planTargetPaths over the same
// rows, so they cannot disagree.
func (o *Organizer) OrganizeBookDirectory(book *database.Book, files []database.BookFile) (string, map[string]string, error) {
	landing, err := o.organizeBookDirectory(book, files)
	if err != nil {
		return "", nil, err
	}
	return landing.Path, landing.Files, nil
}

// organizeBookDirectory is OrganizeBookDirectory's implementation, returning
// the full Landing: which files it wrote (Created) on top of the landed map.
// The exported wrapper keeps the (dir, map, err) shape for the metafetch and
// iTunes callers that only need the map; Service.OrganizeDirectoryBook takes
// the Landing so that CreateOrganizedVersion can point rows at landed paths
// only and roll back created files only.
//
// A directory landing is ALL OR NOTHING. Every planned file (planTargetPaths
// has already dropped rows flagged Missing) must land, or the whole book fails
// and everything this call wrote is removed. There is no partial success,
// because a partial one is not a smaller success — it is a version row whose
// FilePath is a directory this book does not own. Two same-titled books
// organizing concurrently plan the same directory T; if A's 01.mp3 wins and
// B's 02.mp3 still lands, B's version row says FilePath=T just as A's does,
// and the next ReOrganizeInPlace on either book renames T wholesale, carrying
// the other book's audio with it and re-pointing its rows at files that are
// not its own. Until 2026-09-02 the skipped files were merely counted
// (Stats.Partial) and the book was promoted to a version anyway; the count
// was never read.
//
// A source that vanished since the last scan fails the book for the same
// reason: the row still says present, and a re-scan (which flags it Missing
// so planTargetPaths drops it) is the repair, not a landing that quietly
// leaves the row pointing at a path that is not there.
func (o *Organizer) organizeBookDirectory(book *database.Book, files []database.BookFile) (*Landing, error) {
	if book == nil {
		return nil, fmt.Errorf("invalid book")
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no segment files to organize")
	}

	// Generate target directory from folder naming pattern. This is the same
	// BuildPath call BuildRelPath makes for the folder half (expandPattern is a
	// thin adapter over it), so the directory here and the directory inside each
	// planned target path are the same string by construction.
	folderPath, err := o.expandPattern(o.config.FolderNamingPattern, book)
	if err != nil {
		return nil, fmt.Errorf("folder pattern: %w", err)
	}
	folderPath = sanitizePath(folderPath)
	targetDir := filepath.Join(o.config.RootDir, folderPath)

	planned, err := planTargetPaths(o.config.RootDir, o.config.FolderNamingPattern,
		o.config.FileNamingPattern, files, o.pathVars(book, 0, 0, ""), o.buildOpts())
	if err != nil {
		return nil, err
	}
	if len(planned) == 0 {
		return nil, fmt.Errorf("all %d file(s) for %q (id=%s) are flagged missing on disk — re-scan to verify, or restore from backup", len(files), book.Title, book.ID)
	}

	if err := os.MkdirAll(targetDir, 0775); err != nil {
		return nil, fmt.Errorf("failed to create target directory: %w", err)
	}

	pathMap := make(map[string]string, len(planned))
	var created []string
	// Every planned file that does not land is recorded here with why, and
	// any entry fails the book once the loop has seen every file — the loop
	// keeps going so the error can name ALL of them, not just the first.
	var unlanded []unlandedFile
	for _, entry := range planned {
		srcPath, dstPath := entry.SourcePath, entry.TargetPath
		fileName := filepath.Base(dstPath)

		// Verify dstPath stays inside targetDir (defense against crafted filenames)
		if err := ensureUnderRoot(dstPath, targetDir); err != nil {
			slog.Warn("organizeFile refusing unsafe destination",
				"error", err,
				"book_id", book.ID, "book_title", book.Title,
				"source_path", srcPath, "dest_path", dstPath,
				"target_dir", targetDir, "file_name", fileName)
			unlanded = append(unlanded, unlandedFile{Source: srcPath, Target: dstPath, Reason: "destination escapes the target directory"})
			continue
		}

		if srcPath == dstPath {
			pathMap[srcPath] = dstPath
			continue
		}

		// A file already at the destination is only OURS if it is the same file
		// or byte-identical in CONTENT. Recording an unrelated occupant here
		// would point this book's row at another book's file: the previous code
		// did a bare os.Stat and wrote pathMap[src] = dst for whatever it found,
		// which was survivable while the destination name was just
		// filepath.Base(src) and is not now that the file naming pattern decides
		// it.
		//
		// Equal size used to BE the adoption test, and equal size is not the
		// same file. Two different audiobooks of identical byte length are not
		// rare — same-length encodes of the same runtime, placeholder files,
		// files padded to a block boundary — and adopting one silently points
		// this book's row at the other book's audio, which cannot be undone from
		// inside the app. Size survives only as a free pre-filter (a differing
		// size is still a hard "not the same file"); sameness itself is now
		// proven by hashing both sides. See destinationIsSameContent for what
		// that does and does not establish.
		if dstInfo, statErr := os.Stat(dstPath); statErr == nil {
			if adoptExistingDestination(book, srcPath, dstPath, dstInfo) {
				pathMap[srcPath] = dstPath
			} else {
				unlanded = append(unlanded, unlandedFile{Source: srcPath, Target: dstPath, Reason: "destination is occupied by a file not proven to be this book's"})
			}
			continue
		}

		if _, err := o.organizeFile(srcPath, dstPath); err != nil {
			// "Is the source missing" is answered by looking at the source, not
			// by pattern-matching the error: the old test (os.IsNotExist on the
			// error, or "no such file" anywhere in its text) also matched a
			// temp swept from under a live copy and a target directory that
			// vanished mid-write, and treated those as if the source were gone.
			if _, statErr := os.Lstat(srcPath); errors.Is(statErr, fs.ErrNotExist) {
				slog.Warn("organizeFile: source file has vanished since the last scan",
					"source_path", srcPath, "dest_path", dstPath,
					"book_id", book.ID, "book_title", book.Title,
					"error", err)
				unlanded = append(unlanded, unlandedFile{Source: srcPath, Target: dstPath, Reason: "source file no longer exists (re-scan to mark it missing)"})
				continue
			}
			// Race: the destination appeared between the os.Stat above and the
			// exclusive create/finalize inside organizeFile — another worker
			// planning the same target, or the same book planned twice. This
			// used to record pathMap[src] = dst UNVERIFIED, which is the exact
			// adoption-by-existence the pre-copy check above stopped doing:
			// whoever won the race, this book's row was pointed at their file.
			// Re-run the same content test; an occupant that is not proven to
			// be this file fails the book, as it would have had it been there
			// a millisecond earlier.
			if errors.Is(err, fs.ErrExist) {
				if dstInfo, statErr := os.Stat(dstPath); statErr == nil && adoptExistingDestination(book, srcPath, dstPath, dstInfo) {
					pathMap[srcPath] = dstPath
				} else {
					slog.Warn("organizeFile lost a destination race to a file not proven to be this book's",
						"book_id", book.ID, "book_title", book.Title,
						"source_path", srcPath, "dest_path", dstPath,
						"dest_stat_error", statErr)
					unlanded = append(unlanded, unlandedFile{Source: srcPath, Target: dstPath, Reason: "lost the destination to another writer whose file is not proven to be this book's"})
				}
				continue
			}
			// Any other failure is fatal for the book too, and what this call
			// already wrote comes back out — until 2026-09-02 this return left
			// the earlier files behind under RootDir with no row naming them.
			leftover := unlinkCreated(created, targetDir, targetDir)
			return nil, fmt.Errorf("failed to organize segment %s: %w%s%s", fileName, err, unlandedSuffix(unlanded), leftoverSuffix(leftover))
		}
		pathMap[srcPath] = dstPath
		created = append(created, dstPath)
	}

	if len(unlanded) > 0 {
		leftover := unlinkCreated(created, targetDir, targetDir)
		return nil, fmt.Errorf("organize of %q (id=%s) did not land %d of %d file(s); nothing was kept: %s%s",
			book.Title, book.ID, len(unlanded), len(planned), joinUnlanded(unlanded), leftoverSuffix(leftover))
	}

	return &Landing{Path: targetDir, Files: pathMap, Created: created}, nil
}

// unlandedFile is one planned file that a directory organize could not land,
// with the reason, so the error that fails the book can name every one.
type unlandedFile struct {
	Source, Target, Reason string
}

// unlinkCreated removes the files an organize wrote and then dir — the
// target directory, or "" for a single-file landing — if and only if that
// left it empty. It returns the paths it could not remove so the caller can
// say so. Every path must sit under root (the target directory for an
// in-flight directory landing, the library root for a version rollback);
// containment is checked per path with ensureUnderRoot even though Created is
// built from paths that already passed it: the guard is cheap and a rollback
// is exactly the place a bug would be expensive. A path that fails the guard
// is reported as leftover, not removed.
//
// Organize COPIES (or links); the original survives at its source row, so
// removing the copy destroys no audio. The directory removal is os.Remove,
// which fails on a non-empty directory — files that were not ours stay.
func unlinkCreated(created []string, root, dir string) []string {
	var leftover []string
	for _, p := range created {
		if p == "" || ensureUnderRoot(p, root) != nil {
			leftover = append(leftover, p)
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			leftover = append(leftover, p)
		}
	}
	if dir != "" {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("organize: rollback left the target directory in place", "dir", dir, "error", err)
		}
	}
	return leftover
}

// leftoverSuffix renders unlinkCreated's failures for an error message, or
// nothing when the rollback was clean.
func joinUnlanded(unlanded []unlandedFile) string {
	var b strings.Builder
	for i, u := range unlanded {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s -> %s: %s", u.Source, u.Target, u.Reason)
	}
	return b.String()
}

// unlandedSuffix names the files that had already failed to land when a later
// segment failed fatally, so the fatal error does not swallow their reasons.
func unlandedSuffix(unlanded []unlandedFile) string {
	if len(unlanded) == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d earlier file(s) had not landed: %s)", len(unlanded), joinUnlanded(unlanded))
}

func leftoverSuffix(leftover []string) string {
	if len(leftover) == 0 {
		return ""
	}
	return fmt.Sprintf(" (rollback could not remove %d file(s): %s)", len(leftover), strings.Join(leftover, ", "))
}

// adoptExistingDestination decides whether a file already sitting at dstPath
// is THIS book's file (adopt it: record the row as organized there) or an
// unrelated occupant (leave the row unchanged). It is the single adoption
// test for both places organizeBooks can meet an occupied destination — the
// pre-copy os.Stat, and the os.IsExist returned when another worker won the
// race between that stat and the exclusive create/finalize — so the two
// cannot drift apart again. Until 2026-09-02 the race path adopted
// unconditionally.
//
// A destination is only OURS if it is the same file or byte-identical in
// CONTENT. Recording an unrelated occupant would point this book's row at
// another book's file: the earliest version did a bare os.Stat and wrote
// pathMap[src] = dst for whatever it found, which was survivable while the
// destination name was just filepath.Base(src) and is not now that the file
// naming pattern decides it.
//
// Equal size used to BE the adoption test, and equal size is not the same
// file. Two different audiobooks of identical byte length are not rare —
// same-length encodes of the same runtime, placeholder files, files padded to
// a block boundary — and adopting one silently points this book's row at the
// other book's audio, which cannot be undone from inside the app. Size
// survives only as a free pre-filter (a differing size is still a hard "not
// the same file"); sameness itself is proven by hashing both sides. See
// destinationIsSameContent for what that does and does not establish.
//
// Who os.SameFile actually spares: HARDLINK re-organizes, since os.Link shares
// the inode; SYMLINK, since os.Stat follows the link. REFLINK DOES NOT —
// reflink_unix.go opens the destination O_CREATE|O_EXCL, which allocates a
// NEW inode, and the FICLONE ioctl only shares extents into it, so os.SameFile
// is false for every successful reflink pair. On btrfs/XFS, where the default
// "auto" strategy reaches for reflink first, a re-organize whose rows still
// point at the pre-organize path pays two full reads here. That is the correct
// price for not adopting the wrong audio, but it is a price, not free.
func adoptExistingDestination(book *database.Book, srcPath, dstPath string, dstInfo os.FileInfo) bool {
	srcInfo, srcErr := os.Stat(srcPath)
	switch {
	case srcErr == nil && os.SameFile(srcInfo, dstInfo):
		return true
	case srcErr == nil && destinationIsSameContent(srcPath, dstPath, srcInfo.Size(), dstInfo.Size()):
		// Interrupted copy/reflink from an earlier run, or the other worker
		// in a race copied the same bytes: same content, different inode.
		// Adopt it rather than re-copying.
		return true
	default:
		// "not proven to be this book's file", NOT "a different file". This
		// branch is also where an UNDECIDABLE destination lands — unreadable
		// file, hash I/O error, size changed mid-read — because
		// destinationIsSameContent fails closed. Saying "different" here
		// would contradict the warn that function already emitted about the
		// same path, and would send an operator looking for a content
		// mismatch that may not exist.
		slog.Warn("organizeFile destination occupied by a file not proven to be this book's — leaving this file's row unchanged",
			"book_id", book.ID, "book_title", book.Title,
			"source_path", srcPath, "dest_path", dstPath,
			"source_stat_error", srcErr)
		return false
	}
}

// destinationIsSameContent reports whether srcPath and dstPath hold
// byte-identical content. It is the adoption test for a destination that
// already exists and is NOT the same inode as the source, and it replaced a
// bare size comparison that treated any two same-length files as one file.
//
// It fails CLOSED. Every uncertain answer — either file unreadable, a size that
// changed between the caller's os.Stat and the read here, any I/O error — is
// false, i.e. "do not adopt". The two outcomes are not symmetric: declining to
// adopt costs a re-copy of a file that was already in place, while adopting
// wrongly rewrites this book's row to point at a different book's audio and
// leaves no record of what the row used to say.
//
// What it can distinguish: any two files differing in a single byte, anywhere
// in the file, at any size — this is a whole-file SHA-256 of each side, not a
// sampled or head/tail digest.
//
// What it cannot distinguish: a SHA-256 collision, and a file replaced by
// byte-identical content between this check and the move. Neither changes the
// outcome — in both cases the bytes at the destination are the bytes the caller
// wanted there.
//
// Cost: it reads both files end to end, so it is deliberately the LAST test
// tried. Reaching it requires a destination that already exists, is not the
// same inode as the source, and has exactly the same size; the ordinary
// re-organize of a hardlinked or reflinked library never gets here, and a
// same-size collision between unrelated books is rare. When it does run on a
// multi-GB pair it is two sequential streaming reads and no allocation beyond
// the hash buffer — the price of not silently swapping the user's audio.
func destinationIsSameContent(srcPath, dstPath string, srcSize, dstSize int64) bool {
	// Free pre-filter: different lengths can never be the same bytes, and this
	// rejects the overwhelming majority of occupied destinations without I/O.
	if srcSize != dstSize {
		return false
	}

	srcHash, srcHashedSize, err := fileops.ComputeFileHashAndSize(srcPath)
	if err != nil {
		slog.Warn("destinationIsSameContent cannot hash source — not adopting the destination",
			"source_path", srcPath, "dest_path", dstPath, "error", err)
		return false
	}
	dstHash, dstHashedSize, err := fileops.ComputeFileHashAndSize(dstPath)
	if err != nil {
		slog.Warn("destinationIsSameContent cannot hash destination — not adopting it",
			"source_path", srcPath, "dest_path", dstPath, "error", err)
		return false
	}

	// ComputeFileHashAndSize stats the open descriptor, so each size describes
	// exactly the bytes that were hashed. Disagreeing with the caller's os.Stat
	// means the file changed under us mid-check: unknown, so do not adopt.
	if srcHashedSize != srcSize || dstHashedSize != dstSize {
		slog.Warn("destinationIsSameContent saw a size change mid-check — not adopting the destination",
			"source_path", srcPath, "dest_path", dstPath,
			"source_size_before", srcSize, "source_size_hashed", srcHashedSize,
			"dest_size_before", dstSize, "dest_size_hashed", dstHashedSize)
		return false
	}

	return srcHash == dstHash
}

// organizeFile copies/links a single file using the configured strategy.
// Returns (method, error) where method is "reflink", "hardlink", "copy", or "symlink".
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

// reflinkFile creates a copy-on-write reflink.
//
// Delegates to fileops.Reflink, the single implementation for the codebase.
// The exists-error is returned un-wrapped so the callers above can recover
// with os.IsExist, mirroring the hardlink fallback.
func (o *Organizer) reflinkFile(src, dst string) error {
	return fileops.Reflink(src, dst)
}
