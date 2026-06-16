// file: internal/maintenance/jobs/relink_report.go
// version: 2.3.0
// guid: a1000022-0000-0000-0000-000000000022
// last-edited: 2026-06-16

package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog"
)

func init() { maintenance.Register(&relinkReportJob{}) }

type relinkReportJob struct{}

func (j *relinkReportJob) ID() string       { return "relink-report" }
func (j *relinkReportJob) Name() string     { return "Relink Report" }
func (j *relinkReportJob) Category() string { return "itunes" }
func (j *relinkReportJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *relinkReportJob) Description() string {
	return "Report missing iTunes-linked files that may be relinkable"
}
func (j *relinkReportJob) CanResume() bool { return false }

func (j *relinkReportJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, _ bool) error {
	iTunesRoot := config.AppConfig.ITunes.MediaRoot
	organizerRoot := config.AppConfig.RootDir

	if iTunesRoot == "" {
		return fmt.Errorf("itunes_media_root not configured")
	}
	if organizerRoot == "" {
		return fmt.Errorf("root_dir not configured")
	}

	allBooks, err := store.GetAllBooks(0, 0)
	if err != nil {
		return fmt.Errorf("failed to list books: %w", err)
	}
	reporter.SetTotal(len(allBooks))

	var resolved, unresolved, skipped int

	for i := range allBooks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		book := &allBooks[i]
		fp := book.FilePath

		if !strings.HasPrefix(fp, organizerRoot) {
			skipped++
			reporter.Increment()
			continue
		}
		if _, statErr := os.Stat(fp); statErr == nil {
			skipped++
			reporter.Increment()
			continue
		}

		rel := strings.TrimPrefix(fp, organizerRoot)
		rel = strings.TrimPrefix(rel, string(os.PathSeparator))
		authorName := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		if authorName == "" || authorName == filepath.Base(fp) {
			if book.Author != nil {
				authorName = book.Author.Name
			} else if book.AuthorID != nil {
				if a, aErr := store.GetAuthorByID(*book.AuthorID); aErr == nil && a != nil {
					authorName = a.Name
				}
			}
		}
		if authorName == "" {
			slog.Warn("No author for missing book", "bookID", book.ID, "bookTitle", book.Title)
			unresolved++
			reporter.Increment()
			continue
		}

		matches := rrFindInITunes(iTunesRoot, authorName, book.Title)

		switch len(matches) {
		case 0:
			unresolved++
		case 1:
			resolved++
			slog.Info("Relinkable book found", "bookID", book.ID, "bookTitle", book.Title, "match", matches[0])
		default:
			if best := rrDisambiguate(matches, authorName, book.Title); best != "" {
				resolved++
				slog.Info("Relinkable book found", "bookID", book.ID, "bookTitle", book.Title, "match", best)
			} else {
				unresolved++
			}
		}
		reporter.Increment()
	}

	slog.Info("Done resolved unresolved skipped", "resolved", resolved, "unresolved", unresolved, "skipped", skipped)
	return nil
}

var rrAudioExts = map[string]bool{
	".mp3": true, ".m4b": true, ".m4a": true,
	".flac": true, ".opus": true, ".ogg": true,
}

func rrFindInITunes(iTunesRoot, authorName, title string) []string {
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
					if !rrAudioExts[strings.ToLower(filepath.Ext(path))] {
						return nil
					}
					if strings.Contains(normalizeForFilename(filepath.Base(path)), titlePrefixLower) {
						dirMatches[albumPath] = struct{}{}
						return filepath.SkipDir
					}
					return nil
				})
			} else {
				if !rrAudioExts[strings.ToLower(filepath.Ext(albumPath))] {
					continue
				}
				if strings.Contains(normalizeForFilename(album.Name()), titlePrefixLower) {
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

var rrLeadingNumRE = regexp.MustCompile(`^\d+\s*[-.]?\s*`)
var rrTrailingNumRE = regexp.MustCompile(`\s+\d+$`)

func rrDisambiguate(matches []string, authorName, title string) string {
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
		leadingNum := rrLeadingNumRE.FindString(stemRaw)
		stemNoNum := rrLeadingNumRE.ReplaceAllString(stemRaw, "")
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
		if rrTrailingNumRE.MatchString(stemNorm) {
			sc -= 30
		}
		if leadingNum == "" {
			sc += 20
		} else {
			if n, err := strconv.Atoi(strings.TrimSpace(
				strings.TrimRight(strings.TrimRight(leadingNum, " "), "-."))); err == nil {
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
			s = strings.ToLower(rrLeadingNumRE.ReplaceAllString(s, ""))
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
