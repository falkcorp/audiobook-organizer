// file: internal/maintenance/jobs/relink_missing_to_itunes.go
// version: 1.4.0
// guid: e0f6a4d5-7b8c-9d0e-1f2a-3b4c5d6e7f80
// last-edited: 2026-06-16

package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func init() { maintenance.Register(&relinkMissingToITunesJob{}) }

type relinkMissingToITunesJob struct {
	enqueuer maintenance.WriteBackEnqueuer
}

func (j *relinkMissingToITunesJob) InjectEnqueuer(e maintenance.WriteBackEnqueuer) { j.enqueuer = e }

func (j *relinkMissingToITunesJob) ID() string       { return "relink-missing-to-itunes" }
func (j *relinkMissingToITunesJob) Name() string     { return "Relink Missing to iTunes" }
func (j *relinkMissingToITunesJob) Category() string { return "iTunes" }
func (j *relinkMissingToITunesJob) Description() string {
	return "Finds books whose path no longer exists on disk and searches the iTunes library to re-link them"
}
func (j *relinkMissingToITunesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *relinkMissingToITunesJob) CanResume() bool { return false }

func (j *relinkMissingToITunesJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	iTunesRoot := config.AppConfig.ITunes.MediaRoot
	organizerRoot := config.AppConfig.RootDir

	if iTunesRoot == "" {
		return fmt.Errorf("itunes_media_root not configured; set itunes_media_root in settings")
	}
	if organizerRoot == "" {
		return fmt.Errorf("root_dir is not configured")
	}

	allBooks, err := store.GetAllBooks(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooks: %w", err)
	}

	reporter.SetTotal(len(allBooks))

	audioExts := map[string]bool{".mp3": true, ".m4b": true, ".m4a": true, ".flac": true, ".opus": true, ".ogg": true}

	relinked, unresolved, ambiguous, skipped := 0, 0, 0, 0

	for i := range allBooks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()

		book := &allBooks[i]
		fp := book.FilePath
		if !strings.HasPrefix(fp, organizerRoot) {
			skipped++
			continue
		}
		if _, err := os.Stat(fp); err == nil {
			skipped++
			continue
		}

		// Derive author name from organizer path; fall back to DB.
		rel := strings.TrimPrefix(fp, organizerRoot)
		rel = strings.TrimPrefix(rel, string(os.PathSeparator))
		authorName := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		if authorName == "" || authorName == filepath.Base(fp) {
			if book.Author != nil {
				authorName = book.Author.Name
			} else if book.AuthorID != nil {
				if a, aerr := store.GetAuthorByID(*book.AuthorID); aerr == nil && a != nil {
					authorName = a.Name
				}
			}
		}
		if authorName == "" {
			unresolved++
			continue
		}

		matches := rmt_findInITunes(iTunesRoot, authorName, book.Title, audioExts)

		switch len(matches) {
		case 0:
			unresolved++
		case 1:
			newFP := filepath.Clean(matches[0])
			if !util.WithinRoot(newFP, iTunesRoot) {
				slog.Warn("relink-missing-to-itunes match outside iTunesRoot, skipping", "match", newFP)
				unresolved++
				break
			}
			relinked++
			if !dryRun {
				fi, _ := os.Stat(newFP)
				book.FilePath = newFP
				if _, upErr := store.UpdateBook(book.ID, book); upErr != nil {
					slog.Warn("relink-missing-to-itunes UpdateBook", "book", book.ID, "upErr", upErr)
					relinked--
					unresolved++
					break
				}
				rmt_updateBookFiles(store, book.ID, newFP, fi, organizerRoot)
				if j.enqueuer != nil {
					j.enqueuer.Enqueue(book.ID)
				}
			}
		default:
			best := rmt_disambiguate(matches, authorName, book.Title)
			if best != "" {
				best = filepath.Clean(best)
				if !util.WithinRoot(best, iTunesRoot) {
					slog.Warn("relink-missing-to-itunes best match outside iTunesRoot, skipping", "match", best)
					unresolved++
					break
				}
				relinked++
				if !dryRun {
					fi, _ := os.Stat(best)
					book.FilePath = best
					if _, upErr := store.UpdateBook(book.ID, book); upErr != nil {
						slog.Warn("relink-missing-to-itunes UpdateBook", "book", book.ID, "upErr", upErr)
						relinked--
						unresolved++
						break
					}
					rmt_updateBookFiles(store, book.ID, best, fi, organizerRoot)
					if j.enqueuer != nil {
						j.enqueuer.Enqueue(book.ID)
					}
				}
			} else {
				ambiguous++
			}
		}
	}

	slog.Info("relink-missing-to-itunes relinked ambiguous unresolved skipped", "relinked", relinked, "ambiguous", ambiguous, "unresolved", unresolved, "skipped", skipped)
	slog.Info("relinked ambiguous unresolved skipped", "relinked", relinked, "ambiguous", ambiguous, "unresolved", unresolved, "skipped", skipped)
	return nil
}

// rmt_updateBookFiles updates all book_file rows that pointed to the old organizer path.
func rmt_updateBookFiles(store database.Store, bookID, newFP string, fi os.FileInfo, organizerRoot string) {
	bookFiles, bfErr := store.GetBookFiles(bookID)
	if bfErr != nil {
		return
	}
	for j := range bookFiles {
		bf := &bookFiles[j]
		if !strings.HasPrefix(bf.FilePath, organizerRoot) {
			continue
		}
		bf.FilePath = newFP
		bf.OriginalFilename = filepath.Base(newFP)
		bf.Missing = false
		if fi != nil && !fi.IsDir() {
			bf.FileSize = fi.Size()
			ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(newFP), "."))
			if ext != "" {
				bf.Format = ext
			}
		}
		_ = store.UpdateBookFile(bf.ID, bf)
	}
}

// normalizeForFilename normalizes a string for iTunes/macOS filename comparison.
// macOS HFS+ and iTunes replace ": " and ":" with "_ " and "_" respectively
// because colons are illegal in filenames. This function applies the same
// transformation so that "Mistborn: The Final Empire" matches the file
// "Mistborn_ The Final Empire.m4b".
func normalizeForFilename(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ": ", "_ ")
	s = strings.ReplaceAll(s, ":", "_")
	return strings.TrimSpace(s)
}

// rmt_findInITunes searches iTunesRoot for iTunes album directories matching author+title.
func rmt_findInITunes(iTunesRoot, authorName, title string, audioExts map[string]bool) []string {
	iTunesRoot = filepath.Clean(iTunesRoot)
	titlePrefix := title
	if len(titlePrefix) > 25 {
		titlePrefix = titlePrefix[:25]
	}
	// Normalize for macOS/iTunes filename encoding: ":" → "_", ": " → "_ "
	titlePrefixLower := normalizeForFilename(titlePrefix)

	authorWord := authorName
	if idx := strings.Index(authorName, " "); idx > 0 {
		authorWord = authorName[:idx]
	}
	authorWordLower := strings.ToLower(authorWord)

	dirMatches := map[string]struct{}{}

	entries, err := os.ReadDir(iTunesRoot)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !strings.Contains(strings.ToLower(entry.Name()), authorWordLower) {
			continue
		}
		authorDir := filepath.Join(iTunesRoot, entry.Name())

		albumEntries, err := os.ReadDir(authorDir)
		if err != nil {
			continue
		}
		for _, album := range albumEntries {
			albumPath := filepath.Join(authorDir, album.Name())
			if album.IsDir() {
				if strings.Contains(normalizeForFilename(album.Name()), titlePrefixLower) {
					dirMatches[albumPath] = struct{}{}
					continue
				}
				_ = filepath.WalkDir(albumPath, func(path string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					if !audioExts[strings.ToLower(filepath.Ext(path))] {
						return nil
					}
					if strings.Contains(normalizeForFilename(filepath.Base(path)), titlePrefixLower) {
						dirMatches[albumPath] = struct{}{}
						return filepath.SkipDir
					}
					return nil
				})
			} else {
				if !audioExts[strings.ToLower(filepath.Ext(albumPath))] {
					continue
				}
				if strings.Contains(normalizeForFilename(album.Name()), titlePrefixLower) {
					dirMatches[albumPath] = struct{}{}
				}
			}
		}
	}

	// Primary pass produced matches — return them directly.
	if len(dirMatches) > 0 {
		result := make([]string, 0, len(dirMatches))
		for d := range dirMatches {
			result = append(result, d)
		}
		sort.Strings(result)
		return result
	}

	// Surname fallback: when the primary (first-word) pass finds nothing,
	// try matching iTunes directories by the author's surname — the last
	// space-delimited word of the primary author name. This handles
	// co-author directories like "Robert Jordan, Brandon Sanderson".
	surname := authorName
	if idx := strings.LastIndex(authorName, " "); idx > 0 {
		surname = authorName[idx+1:]
	}
	surnameLower := strings.ToLower(surname)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !strings.Contains(strings.ToLower(entry.Name()), surnameLower) {
			continue
		}
		authorDir := filepath.Join(iTunesRoot, entry.Name())

		albumEntries, err := os.ReadDir(authorDir)
		if err != nil {
			continue
		}
		for _, album := range albumEntries {
			albumPath := filepath.Join(authorDir, album.Name())
			if album.IsDir() {
				if strings.Contains(strings.ToLower(album.Name()), titlePrefixLower) {
					dirMatches[albumPath] = struct{}{}
					continue
				}
				_ = filepath.WalkDir(albumPath, func(path string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					if !audioExts[strings.ToLower(filepath.Ext(path))] {
						return nil
					}
					if strings.Contains(strings.ToLower(filepath.Base(path)), titlePrefixLower) {
						dirMatches[albumPath] = struct{}{}
						return filepath.SkipDir
					}
					return nil
				})
			} else {
				if !audioExts[strings.ToLower(filepath.Ext(albumPath))] {
					continue
				}
				if strings.Contains(strings.ToLower(album.Name()), titlePrefixLower) {
					dirMatches[albumPath] = struct{}{}
				}
			}
		}
	}

	result := make([]string, 0, len(dirMatches))
	for d := range dirMatches {
		result = append(result, d)
	}
	sort.Strings(result)
	return result
}

var rmt_leadingNumRE = regexp.MustCompile(`^\d+\s*[-.]?\s*`)
var rmt_trailingNumRE = regexp.MustCompile(`\s+\d+$`)

// rmt_disambiguate narrows multiple iTunes matches to a single best match using a scoring heuristic.
func rmt_disambiguate(matches []string, authorName, title string) string {
	titleLower := strings.ToLower(title)

	type candidate struct {
		path  string
		score int
	}
	cands := make([]candidate, 0, len(matches))

	for _, p := range matches {
		base := filepath.Base(p)
		ext := filepath.Ext(base)
		stemRaw := strings.TrimSuffix(base, ext)
		leadingNum := rmt_leadingNumRE.FindString(stemRaw)
		stemNoNum := rmt_leadingNumRE.ReplaceAllString(stemRaw, "")
		stemLower := strings.ToLower(stemNoNum)
		stemNorm := strings.ReplaceAll(strings.ReplaceAll(stemLower, "_", " "), ":", " ")

		sc := 0

		switch {
		case stemNorm == titleLower:
			sc += 100
		case strings.HasPrefix(stemNorm, titleLower):
			rest := stemNorm[len(titleLower):]
			switch {
			case regexp.MustCompile(`^\s+book\s+\d`).MatchString(rest),
				regexp.MustCompile(`^\s+\d+$`).MatchString(rest):
				sc += 20
			default:
				sc += 60
			}
		case strings.HasPrefix(titleLower, stemNorm) && len(stemNorm) >= 10:
			sc += 80
		case strings.Contains(stemNorm, titleLower):
			sc += 10
		}

		if rmt_trailingNumRE.MatchString(stemNorm) {
			sc -= 30
		}
		if leadingNum == "" {
			sc += 20
		} else {
			if n, nerr := strconv.Atoi(strings.TrimSpace(
				strings.TrimRight(strings.TrimRight(leadingNum, " "), "-."))); nerr == nil {
				sc -= n * 2
			}
		}

		authorDir := filepath.Base(filepath.Dir(p))
		if strings.EqualFold(authorDir, authorName) {
			sc += 40
		} else if strings.Contains(strings.ToLower(authorDir), strings.ToLower(authorName)) {
			sc += 20
		}
		sc -= len(base) / 8

		cands = append(cands, candidate{path: p, score: sc})
	}

	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })

	if len(cands) > 1 {
		stemOf := func(p string) string {
			b := filepath.Base(p)
			s := strings.TrimSuffix(b, filepath.Ext(b))
			s = strings.ToLower(rmt_leadingNumRE.ReplaceAllString(s, ""))
			s = strings.ReplaceAll(strings.ReplaceAll(s, "_", " "), ":", " ")
			return s
		}
		first := stemOf(cands[0].path)
		allSame := true
		for _, c := range cands[1:] {
			if stemOf(c.path) != first {
				allSame = false
				break
			}
		}
		if allSame {
			return cands[0].path
		}
	}

	if len(cands) >= 2 && cands[0].score-cands[1].score >= 15 {
		return cands[0].path
	}
	if len(cands) == 1 {
		return cands[0].path
	}
	return ""
}
