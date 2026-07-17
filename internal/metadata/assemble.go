// file: internal/metadata/assemble.go
// version: 1.3.0
// guid: 1b2c3d4e-5f6a-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-07-17

package metadata

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/titleutil"
)

// AssembledMetadata is the combined, priority-resolved metadata for an audiobook.
type AssembledMetadata struct {
	Title          string
	Authors        []string
	SeriesName     string
	SeriesPosition int
	Narrator       string
	Year           int
	Genre          string
	Language       string
	Publisher      string
	ISBN13         string
	ISBN10         string
	FileCount      int
	TotalDuration  float64

	TitleSource    string
	AuthorSource   string
	SeriesSource   string
	NarratorSource string
}

// AssembleBookMetadata builds a BookMetadata from folder path hierarchy + first file tags.
func AssembleBookMetadata(dirPath, firstFilePath string, fileCount int, totalDuration float64) (*AssembledMetadata, error) {
	assembleLog := logger.New("assemble")
	bm := &AssembledMetadata{
		FileCount:     fileCount,
		TotalDuration: totalDuration,
	}

	fm, err := ExtractMetadataFromFolder(dirPath)
	if err != nil {
		assembleLog.Warn("folder parser error for %s: %v", dirPath, err)
		fm = &FolderMetadata{}
	}

	var tagMeta *Metadata
	if firstFilePath != "" && firstFilePath != dirPath {
		info, statErr := os.Stat(firstFilePath)
		if statErr == nil && !info.IsDir() {
			m, tagErr := ExtractMetadata(firstFilePath, nil)
			if tagErr == nil {
				tagMeta = &m
			} else {
				assembleLog.Warn("tag extraction failed for %s: %v", firstFilePath, tagErr)
			}
		}
	}

	bm.Title, bm.TitleSource = resolveTitle(tagMeta, fm, firstFilePath, dirPath, config.AppConfig.SupportedExtensions)
	bm.Authors, bm.AuthorSource = resolveAuthors(tagMeta, fm)
	bm.SeriesName, bm.SeriesPosition, bm.SeriesSource = resolveSeries(tagMeta, fm)
	bm.Narrator, bm.NarratorSource = resolveNarrator(tagMeta, fm)

	if tagMeta != nil && tagMeta.Year > 0 {
		bm.Year = tagMeta.Year
	}
	if tagMeta != nil {
		bm.Genre = tagMeta.Genre
		bm.Language = tagMeta.Language
		bm.Publisher = tagMeta.Publisher
		bm.ISBN13 = tagMeta.ISBN13
		bm.ISBN10 = tagMeta.ISBN10
	}

	assembleLog.Info(
		"%s → title=%q authors=%v series=%q pos=%d narrator=%q",
		dirPath, bm.Title, bm.Authors, bm.SeriesName, bm.SeriesPosition, bm.Narrator,
	)
	return bm, nil
}

// agreedTagTitleSample bounds how many chapter files the CONS-17b agree-title
// discriminator reads. The iTunes twin (agreedStrippedTitle) can check every
// track for free — track names come from the library XML — but here each check
// is a real tag read off disk, so cap it. A disagreement almost always shows on
// the second file (chapter titles differ per chapter), and the loop early-exits
// the moment two disagree, so the common cost is 2 reads; only a genuinely
// agreeing multi-chapter book pays the full sample.
const agreedTagTitleSample = 5

// agreedChapterTitle implements the CONS-17b discriminator for the filesystem
// scanner: it returns the chapter-stripped tag title when EVERY sampled chapter
// in dirPath strips to the same non-empty title (e.g. "Aces Abroad - Part 1" …
// "- Part 14" all → "Aces Abroad"), and "" when they disagree (per-chapter names
// like "Big Finish Ident" / "Opening Credits"), letting the caller fall back to
// the folder name.
//
// multi reports whether dirPath actually holds more than one audio file; a
// single-file book has nothing to agree with, so the caller keeps trusting its
// tag title outright.
//
// This mirrors agreedStrippedTitle in internal/itunes/service/importer.go —
// keep the two in sync. Album-preference was rejected for this decision: album
// frequently equals the *series* name (see resolveSeries below), so preferring
// it would replace correct titles with series names.
func agreedChapterTitle(dirPath string, supportedExts []string) (agreed string, multi bool) {
	files := listAudioFiles(dirPath, supportedExts)
	if len(files) < 2 {
		return "", false
	}
	if len(files) > agreedTagTitleSample {
		files = files[:agreedTagTitleSample]
	}
	for _, fp := range files {
		m, err := ExtractMetadata(fp, nil)
		if err != nil {
			return "", true // unreadable tag → can't establish agreement
		}
		s := titleutil.StripChapterSuffix(titleutil.StripChapterPrefix(strings.TrimSpace(m.Title)))
		if s == "" {
			return "", true
		}
		if agreed == "" {
			agreed = s
		} else if !strings.EqualFold(agreed, s) {
			return "", true // chapters disagree → not a book title
		}
	}
	return agreed, true
}

// AgreedChapterTitle exposes the CONS-17b agree-title discriminator for callers
// outside this package (e.g. the maintenance.title-repair op, which re-derives
// titles for books stored before CONS-17b shipped). It is a thin wrapper over
// the unexported agreedChapterTitle — the logic stays in one place.
func AgreedChapterTitle(dirPath string, supportedExts []string) (agreed string, multi bool) {
	return agreedChapterTitle(dirPath, supportedExts)
}

// resolveTitle picks the book title. dirPath + supportedExts enable the CONS-17b
// all-chapters-agree check; pass an empty dirPath (or no exts) to skip it and
// trust a non-generic tag title outright (the pre-CONS-17b behaviour).
func resolveTitle(tag *Metadata, fm *FolderMetadata, firstFilePath, dirPath string, supportedExts []string) (string, string) {
	if tag != nil && tag.Title != "" {
		if !isGenericTitle(tag.Title) {
			// CONS-17b: a non-generic tag title on the FIRST chapter is only the
			// book's title if every chapter agrees on it. A per-chapter name that
			// merely dodges isGenericTitle (e.g. "Big Finish Ident") would
			// otherwise be adopted as the whole book's title.
			if dirPath == "" || len(supportedExts) == 0 {
				return tag.Title, "tag.Title"
			}
			agreed, multi := agreedChapterTitle(dirPath, supportedExts)
			switch {
			case !multi:
				return tag.Title, "tag.Title" // single-file book — nothing to disagree
			case agreed != "":
				return agreed, "tag.Title(agreed)"
			}
			logger.New("assemble").Debug(
				"tag title %q not shared by all chapters; trying folder parser", tag.Title)
		} else {
			logger.New("assemble").Debug("tag title %q looks generic; trying folder parser", tag.Title)
		}
	}
	if fm.Title != "" {
		return fm.Title, "folder.Title"
	}
	dirName := filepath.Base(firstFilePath)
	if dirName != "" && dirName != "." {
		dirName = strings.TrimSuffix(dirName, filepath.Ext(dirName))
		if !IsGenericPartFilename(dirName) {
			return dirName, "filename"
		}
	}
	return "", "unknown"
}

func isGenericTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	genericPrefixes := []string{
		"part ", "chapter ", "track ", "disc ", "disk ",
	}
	for _, pfx := range genericPrefixes {
		if strings.HasPrefix(lower, pfx) {
			return true
		}
	}
	return IsGenericPartFilename(title + ".mp3")
}

func resolveAuthors(tag *Metadata, fm *FolderMetadata) ([]string, string) {
	if tag != nil && tag.Artist != "" {
		authors := splitMultipleAuthors(tag.Artist)
		if len(authors) > 0 {
			return authors, "tag.Artist"
		}
	}
	if len(fm.Authors) > 0 {
		return fm.Authors, "folder.Authors"
	}
	return nil, "unknown"
}

func resolveSeries(tag *Metadata, fm *FolderMetadata) (string, int, string) {
	if tag != nil {
		if tag.Series != "" {
			return tag.Series, tag.SeriesIndex, "tag.Series"
		}
		if tag.Album != "" && fm.SeriesName != "" && strings.EqualFold(tag.Album, fm.SeriesName) {
			return fm.SeriesName, fm.SeriesPosition, "folder.Series(album-confirmed)"
		}
	}
	if fm.SeriesName != "" {
		return fm.SeriesName, fm.SeriesPosition, "folder.Series"
	}
	return "", 0, "unknown"
}

func resolveNarrator(tag *Metadata, fm *FolderMetadata) (string, string) {
	if tag != nil && tag.Narrator != "" {
		return tag.Narrator, "tag.Narrator"
	}
	if fm.Narrator != "" {
		return fm.Narrator, "folder.Narrator"
	}
	if tag != nil && tag.Comments != "" {
		if n := extractNarratorFromComment(tag.Comments); n != "" {
			return n, "tag.Comment"
		}
	}
	return "", "unknown"
}

func extractNarratorFromComment(comment string) string {
	prefixes := []string{"narrator:", "read by:", "narrated by:", "reader:"}
	lower := strings.ToLower(comment)
	for _, pfx := range prefixes {
		if idx := strings.Index(lower, pfx); idx >= 0 {
			rest := strings.TrimSpace(comment[idx+len(pfx):])
			end := strings.IndexAny(rest, "\n\r,;")
			if end > 0 {
				return strings.TrimSpace(rest[:end])
			}
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// PrimaryAuthor returns the first author from the Authors slice, or empty string.
func (bm *AssembledMetadata) PrimaryAuthor() string {
	if len(bm.Authors) == 0 {
		return ""
	}
	return bm.Authors[0]
}

// listAudioFiles returns dirPath's audio files in stable alphabetical order
// (non-recursive). Shared by FindFirstAudioFile and the CONS-17b agree-title
// check so both see the same file set in the same order.
func listAudioFiles(dirPath string, supportedExts []string) []string {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil
	}

	extSet := make(map[string]bool, len(supportedExts))
	for _, e := range supportedExts {
		extSet[strings.ToLower(e)] = true
	}

	var audioFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if extSet[ext] {
			audioFiles = append(audioFiles, filepath.Join(dirPath, e.Name()))
		}
	}

	sort.Strings(audioFiles)
	return audioFiles
}

// FindFirstAudioFile returns the alphabetically first audio file in dirPath.
func FindFirstAudioFile(dirPath string, supportedExts []string) string {
	audioFiles := listAudioFiles(dirPath, supportedExts)
	if len(audioFiles) == 0 {
		return ""
	}
	return audioFiles[0]
}
